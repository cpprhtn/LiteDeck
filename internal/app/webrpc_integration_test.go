package app

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/webrpc"
	"github.com/gorilla/websocket"
)

// The claim this proves: prompts (and, by the same mechanism, PTY output and log
// follows) survive the transport swap for free, because they are already
// "emit an event, wait on a channel that a binding call fills." Nothing in the
// prompt bridge knows or cares whether the event reached a Wails webview or a
// browser over a WebSocket, or whether the answer came back through the Wails
// bridge or an HTTP POST.
//
// The flow, end to end over HTTP + WS against a real sshd:
//
//	POST /rpc/ConnectHost      → blocks inside the SSH handshake on an unknown
//	                             host key, then on a password
//	  event prompt:hostkey  ──▶ WebSocket ──▶ this test
//	  POST /rpc/AnswerHostKey ← the test answers; the parked handshake wakes
//	  event prompt:secret   ──▶ WebSocket ──▶ this test
//	  POST /rpc/AnswerSecret  ← the test answers; the handshake completes
//	ConnectHost returns nil    → the long POST finally responds
func TestPromptsRoundTripOverWebTransport(t *testing.T) {
	a, _ := liveApp(t) // real sshd fixture, empty known_hosts → first connect prompts

	srv := webrpc.NewServer(webrpc.New(a), "")
	// Events now go to WebSocket clients instead of the recorder. The manager
	// was built in liveApp with a.emitConnectionState, which reads a.emit at
	// call time, so overriding it here is enough.
	a.emit = srv.Emit

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ws := dialWSClient(t, ts)
	defer ws.Close()
	// A fire-and-forget event emitted before this socket is registered would be
	// lost. Emit a sentinel and read it back, so registration is confirmed
	// before ConnectHost can emit the real prompt.
	awaitRegistered(t, srv, ws)

	// ConnectHost blocks until both prompts are answered, so it runs on its own
	// goroutine while the test answers what arrives on the socket.
	connErr := make(chan error, 1)
	go func() {
		_, appErr, reqErr := rpcCall(t, ts, "ConnectHost", "fixture")
		if reqErr != nil {
			connErr <- reqErr
			return
		}
		connErr <- appErr
	}()

	sawHostKey, sawSecret := false, false
	deadline := time.After(90 * time.Second)
	for !(sawHostKey && sawSecret) {
		select {
		case err := <-connErr:
			t.Fatalf("ConnectHost returned before both prompts were answered (%v); hostkey=%v secret=%v",
				err, sawHostKey, sawSecret)
		case <-deadline:
			t.Fatalf("timed out waiting for prompts; hostkey=%v secret=%v", sawHostKey, sawSecret)
		default:
		}

		event, payload := readEvent(t, ws)
		switch event {
		case "prompt:hostkey":
			sawHostKey = true
			// The whole point: the answer travels back as an ordinary RPC and
			// wakes the handshake goroutine parked on the bridge channel.
			if _, appErr, reqErr := rpcCall(t, ts, "AnswerHostKey", promptID(payload), "always"); reqErr != nil || appErr != nil {
				t.Fatalf("AnswerHostKey over RPC: req=%v app=%v", reqErr, appErr)
			}
		case "prompt:secret":
			sawSecret = true
			if _, appErr, reqErr := rpcCall(t, ts, "AnswerSecret", promptID(payload), sysPass, false); reqErr != nil || appErr != nil {
				t.Fatalf("AnswerSecret over RPC: req=%v app=%v", reqErr, appErr)
			}
		}
	}

	// Both prompts answered over the web transport — the connection must now
	// complete, i.e. the long POST returns without error.
	select {
	case err := <-connErr:
		if err != nil {
			t.Fatalf("ConnectHost failed after answering both prompts over the web: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("ConnectHost did not return after both prompts were answered")
	}

	if got := a.HostState("fixture"); got != "connected" {
		t.Errorf("host state after web-driven connect = %q, want connected", got)
	}
}

/* --------------------------------------------------------------- helpers */

func dialWSClient(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	h := http.Header{"Origin": {ts.URL}}
	c, _, err := websocket.DefaultDialer.Dial(url, h)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	return c
}

// awaitRegistered blocks until an event emitted by the server reaches this
// socket, which cannot happen until the server has registered it.
func awaitRegistered(t *testing.T, srv *webrpc.Server, ws *websocket.Conn) {
	t.Helper()
	for i := 0; i < 100; i++ {
		srv.Emit("__ready__", nil)
		_ = ws.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if _, _, err := ws.ReadMessage(); err == nil {
			_ = ws.SetReadDeadline(time.Time{})
			return
		}
	}
	t.Fatal("WebSocket never registered")
}

func readEvent(t *testing.T, ws *websocket.Conn) (string, map[string]any) {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(90 * time.Second))
	_, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read ws: %v", err)
	}
	var msg struct {
		Event   string         `json:"event"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	return msg.Event, msg.Payload
}

// rpcCall drives one binding the way the frontend shim does: JSON args array,
// application/json, same origin. Returns the method's result, its application
// error, and any transport error — the three the dispatcher distinguishes.
func rpcCall(t *testing.T, ts *httptest.Server, method string, args ...any) (any, error, error) {
	t.Helper()
	body, _ := json.Marshal(args)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/rpc/"+method, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", ts.URL)
	res, err := ts.Client().Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()

	var parsed struct {
		Result any    `json:"result"`
		Error  string `json:"error"`
	}
	_ = json.NewDecoder(res.Body).Decode(&parsed)
	if res.StatusCode != http.StatusOK {
		return nil, nil, errString(parsed.Error)
	}
	if parsed.Error != "" {
		return nil, errString(parsed.Error), nil
	}
	return parsed.Result, nil, nil
}

type errString string

func (e errString) Error() string { return string(e) }

func promptID(m map[string]any) string {
	s, _ := m["id"].(string)
	return s
}

// The full web upload path against a real sshd: connect the host over HTTP/WS
// (answering the prompts as the browser would), then POST a file to /upload and
// confirm it landed on the remote over SFTP. This proves the piece the desktop
// path cannot exercise — bytes from an HTTP request, not a local disk path.
func TestUploadOverWebAfterConnect(t *testing.T) {
	a, _ := liveApp(t)
	srv := webrpc.NewServer(webrpc.New(a), "")
	srv.SetUploader(a)
	a.emit = srv.Emit

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	ws := dialWSClient(t, ts)
	defer ws.Close()
	awaitRegistered(t, srv, ws)

	// Connect, answering host-key and password prompts over the web transport.
	connErr := make(chan error, 1)
	go func() {
		_, appErr, reqErr := rpcCall(t, ts, "ConnectHost", "fixture")
		if reqErr != nil {
			connErr <- reqErr
			return
		}
		connErr <- appErr
	}()
	answerPromptsUntilConnected(t, ts, ws, connErr)

	// Pick a writable directory on the remote and upload into it.
	dir := webScratchDir(t, a)
	res := postUpload(t, ts, "fixture", dir, "web-upload.txt", "uploaded over http\n")
	if res != http.StatusOK {
		t.Fatalf("/upload status %d", res)
	}

	// It must actually be on the server.
	st, err := a.StatPath("fixture", dir+"/web-upload.txt")
	if err != nil || !st.Exists {
		t.Fatalf("uploaded file is not on the remote: %v", err)
	}
	if got, _ := a.ReadTextFile("fixture", dir+"/web-upload.txt"); got.Content != "uploaded over http\n" {
		t.Errorf("remote content = %q", got.Content)
	}
}

type wsEvent struct {
	event   string
	payload map[string]any
}

func answerPromptsUntilConnected(t *testing.T, ts *httptest.Server, ws *websocket.Conn, connErr chan error) {
	t.Helper()
	// Read events on their own goroutine so connect completing is noticed even
	// while no further prompt is arriving. The goroutine ends when the socket
	// closes (defer ws.Close in the caller).
	events := make(chan wsEvent, 8)
	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				close(events)
				return
			}
			var m struct {
				Event   string         `json:"event"`
				Payload map[string]any `json:"payload"`
			}
			if json.Unmarshal(data, &m) == nil {
				events <- wsEvent{m.Event, m.Payload}
			}
		}
	}()

	deadline := time.After(90 * time.Second)
	for {
		select {
		case err := <-connErr:
			if err != nil {
				t.Fatalf("connect failed: %v", err)
			}
			return
		case e := <-events:
			switch e.event {
			case "prompt:hostkey":
				rpcCall(t, ts, "AnswerHostKey", promptID(e.payload), "always")
			case "prompt:secret":
				rpcCall(t, ts, "AnswerSecret", promptID(e.payload), sysPass, false)
			}
		case <-deadline:
			t.Fatal("connect timed out")
		}
	}
}

func webScratchDir(t *testing.T, a *App) string {
	t.Helper()
	dir := "/tmp/litedeck-webupload"
	if res := a.MakeDir("fixture", dir); !res.OK {
		// already there from a previous run is fine
		if !strings.Contains(res.Error, "exists") {
			t.Fatalf("MakeDir: %s", res.Error)
		}
	}
	return dir
}

func postUpload(t *testing.T, ts *httptest.Server, hostID, dir, name, body string) int {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", name)
	_, _ = fw.Write([]byte(body))
	_ = mw.Close()

	url := ts.URL + "/upload?hostId=" + hostID + "&dir=" + dir
	req, _ := http.NewRequest(http.MethodPost, url, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", ts.URL)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("upload do: %v", err)
	}
	defer res.Body.Close()
	return res.StatusCode
}
