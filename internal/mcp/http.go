package mcp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Streamable HTTP transport.
//
// Claude Code requires it and XPipe's MCP server requires it, so it is the one
// transport worth having. A POST carrying a JSON-RPC request gets a JSON reply;
// this server never needs to push, so the SSE half of the transport is answered
// honestly with 405 rather than with an idle stream nobody reads.

// Version identifies this server to clients.
const Version = "0.1"

// Instructions is sent at initialize. It is prompt text: the model reads it
// before deciding anything, so it says what the tools are for and — more
// usefully — what they will refuse to do.
const Instructions = `LiteDeck exposes read-only tools for servers the user has explicitly shared over SSH.

Start with health_snapshot: one round trip covering CPU, memory, disk, failed units,
unhealthy containers and exposed ports. Reach for the narrower tools only when it does
not answer the question.

Every call runs a real command on a real server over one shared SSH connection, and the
user watches those commands appear in the app's Command Log as you make them. Servers are
often small. Ask for what you need rather than sweeping.

Nothing here can change a server. There are no write tools, and none are hidden behind a
parameter. If the user asks you to restart something, say that it has to be done in the
app or the terminal.

Hosts must be named explicitly with hostId. Call hosts_list first if you do not have one;
there is no implicit "current" host, because guessing wrong would mean reading the wrong
production machine.`

const (
	// maxBody bounds a single request. MCP messages are small; anything much
	// larger is a mistake or an attempt.
	maxBody = 1 << 20 // 1 MiB

	// callTimeout bounds one tool call end to end. Longer than a single SSH
	// round trip so a slow server still answers, short enough that a wedged
	// call does not hold a client forever.
	callTimeout = 45 * time.Second
)

// Options configures the listener.
type Options struct {
	// Token authorises every request. Required.
	Token string
	// Port to listen on. Zero asks the OS for a free one.
	Port int
	// OnCall, when set, is invoked for every accepted tool call before it runs.
	// This is how the app records what the AI asked for in the Command Log.
	OnCall func(tool string, args map[string]any)
	// Limiter, when set, gates calls. See NewLimiter.
	Limiter *Limiter
}

// Listener owns the HTTP server.
type Listener struct {
	srv  *Server
	opts Options

	mu   sync.Mutex
	http *http.Server
	ln   net.Listener
}

// NewToken returns a fresh bearer token.
//
// Persisted by the caller rather than regenerated per launch: a token that
// changes on every start breaks the `claude mcp add` line the user already
// pasted into their client config, and they would have to redo it every day.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("mcp: generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Serve starts listening on loopback and returns the bound address.
func (s *Server) Serve(opts Options) (*Listener, string, error) {
	if opts.Token == "" {
		return nil, "", errors.New("mcp: a token is required")
	}

	l := &Listener{srv: s, opts: opts}

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", l.handle)

	// 127.0.0.1 only. There is deliberately no option to bind anything else:
	// this endpoint speaks for every server the user has connected, and the one
	// thing keeping that safe is that it cannot be reached from off the machine.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", opts.Port))
	if err != nil {
		return nil, "", fmt.Errorf("mcp: listen: %w", err)
	}

	// Captured in a local before the goroutine starts. Reading l.http from
	// inside it races with Close, which nils the field — and a short-lived
	// listener closed immediately after Serve returns hits that every time.
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	l.ln, l.http = ln, srv
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = err // the app reports liveness through Addr, not through this
		}
	}()

	return l, "http://" + ln.Addr().String() + "/mcp", nil
}

// Addr returns the bound address, or "" once closed.
func (l *Listener) Addr() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ln == nil {
		return ""
	}
	return l.ln.Addr().String()
}

// Close stops the listener.
func (l *Listener) Close() error {
	l.mu.Lock()
	srv, ln := l.http, l.ln
	l.http, l.ln = nil, nil
	l.mu.Unlock()

	if srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := srv.Shutdown(ctx)
	if ln != nil {
		_ = ln.Close()
	}
	return err
}

func (l *Listener) handle(w http.ResponseWriter, r *http.Request) {
	// A page in a browser can POST to loopback without reading the reply, and
	// DNS rebinding can make that page look same-origin. The MCP spec calls for
	// checking Origin for exactly this; a real MCP client sends none.
	if !originAllowed(r.Header.Get("Origin")) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}

	if !l.authorised(r) {
		// No detail. An unauthorised caller learns whether the endpoint exists
		// and nothing else.
		w.Header().Set("WWW-Authenticate", `Bearer realm="litedeck"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodPost:
		l.post(w, r)
	case http.MethodGet, http.MethodDelete:
		// The spec allows a server to decline the server-to-client stream. This
		// one has nothing to push: tools answer in the POST that asked.
		http.Error(w, "this server does not offer a server-initiated stream",
			http.StatusMethodNotAllowed)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (l *Listener) post(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		http.Error(w, "could not read body", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), callTimeout)
	defer cancel()
	ctx = withHooks(ctx, l.opts.OnCall, l.opts.Limiter)

	reply := l.srv.Handle(ctx, body)
	if reply == nil {
		// A notification. 202 with no body is what the spec asks for.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(reply)
}

func (l *Listener) authorised(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	token, found := strings.CutPrefix(header, "Bearer ")
	if !found {
		return false
	}
	// Constant time: the token is a secret, and a comparison that returns early
	// leaks its prefix to anything that can time requests to loopback.
	return subtle.ConstantTimeCompare([]byte(token), []byte(l.opts.Token)) == 1
}

// originAllowed accepts requests with no Origin — which is what a native MCP
// client sends — and browser origins that are themselves loopback.
func originAllowed(origin string) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

/* ------------------------------------------------- per-call hooks via ctx */

type hookKey struct{}

type hooks struct {
	onCall  func(tool string, args map[string]any)
	limiter *Limiter
}

func withHooks(ctx context.Context, onCall func(string, map[string]any), lim *Limiter) context.Context {
	return context.WithValue(ctx, hookKey{}, hooks{onCall: onCall, limiter: lim})
}

func hooksFrom(ctx context.Context) hooks {
	h, _ := ctx.Value(hookKey{}).(hooks)
	return h
}
