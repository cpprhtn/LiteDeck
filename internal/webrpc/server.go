package webrpc

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

// maxBody bounds one RPC request. Bindings pass small JSON; a file's contents
// go over SFTP, not through here. fs_write-style payloads are the exception and
// are still well under this.
const maxBody = 8 << 20 // 8 MiB

// clientSendBuffer is how many events one browser tab can fall behind before it
// is dropped. emit() is called synchronously from the PTY and log readers
// (terminal.go, logs.go); a blocking send would let one stalled tab freeze the
// stream for every tab and back-pressure the SSH reader. So a slow client is
// disconnected, not waited for — it reconnects and re-fetches state.
const clientSendBuffer = 256

// Server exposes a Dispatcher over HTTP and pushes events to browsers over one
// WebSocket per tab. It carries the same two defences the MCP endpoint treats
// as mandatory (internal/mcp/http.go): an Origin check on every request, and —
// because a WebSocket handshake is not subject to CORS — an Origin check on the
// upgrade too, without which any page on the internet could open ws:// to a
// loopback port and read every event (secret-prompt labels, host fingerprints,
// the whole Command Log).
type Server struct {
	disp *Dispatcher
	// token, when non-empty, is required on every /rpc call (Authorization:
	// Bearer) and every /ws handshake (?token=). It is the thin gate for a
	// deployment that binds beyond loopback without a reverse proxy in front;
	// the WebSocket cannot carry an Authorization header, so it takes the token
	// in the query string — logged by some proxies, which is why loopback +
	// Origin remain the primary defence and this is the fallback.
	token    string
	password string        // "" = no login page; a value gates access behind /login
	sessions *sessionStore // live login sessions
	uploader Uploader

	mu      sync.Mutex
	clients map[*wsClient]struct{}
}

// Uploader receives a browser upload and streams it to a remote host. It is a
// separate seam from the RPC dispatcher because a file's bytes cannot travel as
// a JSON argument — /upload is multipart, not /rpc.
type Uploader interface {
	UploadFile(hostID, remoteDir, name string, r io.Reader) (int64, error)
}

// NewServer wraps a dispatcher. token may be empty (loopback deployments).
func NewServer(disp *Dispatcher, token string) *Server {
	return &Server{disp: disp, token: token, sessions: newSessionStore(), clients: map[*wsClient]struct{}{}}
}

// SetPassword turns on the built-in login: unauthenticated requests to /rpc,
// /ws, /upload and the app itself are sent to /login, and a correct password
// mints a session cookie. Empty leaves login off.
func (s *Server) SetPassword(pw string) { s.password = pw }

// authed reports whether a request may reach a guarded route. Open only when no
// auth is configured at all (loopback dev); otherwise a valid token OR a live
// session cookie.
func (s *Server) authed(r *http.Request) bool {
	if s.token == "" && s.password == "" {
		return true
	}
	if s.token != "" && s.bearerOK(r) {
		return true
	}
	return s.sessionValid(r)
}

// bearerOK checks the Authorization header token (machine/proxy path).
func (s *Server) bearerOK(r *http.Request) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

// SetUploader enables POST /upload, backed by u. Left unset, uploads are simply
// not offered.
func (s *Server) SetUploader(u Uploader) { s.uploader = u }

// wsAuthed authorises a WebSocket handshake. The session cookie rides the
// handshake automatically; the token, which cannot, falls back to a query param.
func (s *Server) wsAuthed(r *http.Request) bool {
	if s.token == "" && s.password == "" {
		return true
	}
	if s.password != "" && s.sessionValid(r) {
		return true
	}
	return s.token != "" && subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(s.token)) == 1
}

// Handler returns the mux. static serves the embedded UI at "/" (pass nil in
// tests that only exercise the API). When a password is set, "/login" and
// "/logout" are added and the app itself is gated behind a session.
func (s *Server) Handler(static http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc/", s.handleRPC)
	mux.HandleFunc("/ws", s.handleWS)
	if s.uploader != nil {
		mux.HandleFunc("/upload", s.handleUpload)
	}
	if s.password != "" {
		mux.HandleFunc("/login", s.handleLogin)
		mux.HandleFunc("/logout", s.handleLogout)
	}
	if static != nil {
		mux.Handle("/", s.gateStatic(static))
	}
	return mux
}

// gateStatic sends an unauthenticated browser to the login page instead of the
// app. The login page and its POST are exempt (handled on their own routes); a
// request with no login configured passes straight through.
func (s *Server) gateStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.password != "" && !s.authed(r) {
			http.Redirect(w, r, "login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// maxUploadMem is how much of a multipart upload is buffered in memory before
// spilling to a temp file. The stream itself is copied straight to SFTP, so
// this only bounds the multipart parser's own buffering.
const maxUploadMem = 16 << 20

// handleUpload streams each part of a multipart POST to a remote directory. It
// carries the same Origin and token checks as /rpc — a file write to a
// production server is at least as sensitive as any binding.
//
//	POST /upload?hostId=<id>&dir=<remote dir>   (multipart/form-data, files)
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !originOK(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	if !s.authed(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="litedeck"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	hostID := r.URL.Query().Get("hostId")
	dir := r.URL.Query().Get("dir")
	if hostID == "" || dir == "" {
		writeErr(w, http.StatusBadRequest, "hostId and dir are required")
		return
	}

	reader, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "expected multipart/form-data")
		return
	}
	saved := []map[string]any{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, "could not read upload: "+err.Error())
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			_ = part.Close()
			continue
		}
		// The stream goes straight to SFTP; UploadFile confines the client-chosen
		// name to a single component inside dir.
		n, err := s.uploader.UploadFile(hostID, dir, part.FileName(), part)
		_ = part.Close()
		if err != nil {
			writeErr(w, http.StatusBadGateway, fmt.Sprintf("%s: %v", part.FileName(), err))
			return
		}
		saved = append(saved, map[string]any{"name": part.FileName(), "bytes": n})
	}
	writeJSON(w, http.StatusOK, map[string]any{"uploaded": saved})
}

// Emit is wired into the App as its event sink (StartupHeadless). It fans one
// event out to every connected tab, dropping a tab that cannot keep up rather
// than blocking the caller.
func (s *Server) Emit(event string, payload any) {
	msg, err := json.Marshal(struct {
		Event   string `json:"event"`
		Payload any    `json:"payload"`
	}{event, payload})
	if err != nil {
		return
	}

	s.mu.Lock()
	for c := range s.clients {
		select {
		case c.send <- msg:
		default:
			// This tab is behind. Closing its channel makes its write pump exit
			// and unregister; the tab's socket closes and it reconnects.
			s.dropLocked(c)
		}
	}
	s.mu.Unlock()
}

/* --------------------------------------------------------------- RPC */

func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Origin + Content-Type together defeat the simple-request CSRF a plain
	// browser page can fire at a loopback port: a cross-origin page cannot set
	// application/json without a preflight, and the preflight fails the origin
	// check. Same reasoning as internal/mcp/http.go's originAllowed.
	if !originOK(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	if !s.authed(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="litedeck"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}

	method := strings.TrimPrefix(r.URL.Path, "/rpc/")
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read request body")
		return
	}
	var rawArgs []json.RawMessage
	if len(body) > 0 {
		if err := json.Unmarshal(body, &rawArgs); err != nil {
			writeErr(w, http.StatusBadRequest, "arguments must be a JSON array")
			return
		}
	}

	result, appErr, reqErr := s.disp.Call(method, rawArgs)
	switch {
	case reqErr != nil:
		if _, ok := reqErr.(ErrNoMethod); ok {
			writeErr(w, http.StatusNotFound, reqErr.Error())
		} else {
			writeErr(w, http.StatusBadRequest, reqErr.Error())
		}
	case appErr != nil:
		// The transport succeeded; the method failed. 200 with an error body,
		// JSON-RPC style — the frontend shim rejects the promise on the error
		// key, exactly as Wails rejects it on a non-nil returned error.
		writeJSON(w, http.StatusOK, map[string]any{"error": appErr.Error()})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"result": result})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

/* --------------------------------------------------------------- WebSocket */

type wsClient struct {
	conn *websocket.Conn
	send chan []byte
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.wsAuthed(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	up := websocket.Upgrader{
		// The handshake is not subject to CORS, so this is the only thing
		// standing between a loopback event stream and any page on the web.
		CheckOrigin: func(r *http.Request) bool { return originOK(r) },
	}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the response
	}
	c := &wsClient{conn: conn, send: make(chan []byte, clientSendBuffer)}

	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()

	go s.writePump(c)
	s.readPump(c) // blocks until the socket closes, then unregisters
}

// readPump exists only to notice the socket closing (and to drain any client
// frames, which this server ignores — the browser talks over /rpc, not the
// socket). When it returns, the client is gone.
func (s *Server) readPump(c *wsClient) {
	defer func() {
		s.mu.Lock()
		s.dropLocked(c)
		s.mu.Unlock()
	}()
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (s *Server) writePump(c *wsClient) {
	for msg := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			c.conn.Close()
			return
		}
	}
	c.conn.Close() // channel closed by dropLocked
}

// dropLocked removes a client and closes its send channel. Caller holds s.mu.
// Idempotent: Emit and readPump can both reach it for the same client.
func (s *Server) dropLocked(c *wsClient) {
	if _, ok := s.clients[c]; !ok {
		return
	}
	delete(s.clients, c)
	close(c.send)
}

/* --------------------------------------------------------------- origin */

// originOK allows a request whose Origin is absent (a non-browser caller),
// loopback, or the same origin as the page itself. Behind a reverse proxy the
// browser's Origin is the public host, which matches the request Host, so
// same-origin covers the proxied deployment; loopback covers hitting the server
// directly during development.
func originOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if isLoopbackHost(u.Hostname()) {
		return true
	}
	// Same origin as the page: the Origin's host:port equals the request's Host.
	return u.Host == r.Host
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
