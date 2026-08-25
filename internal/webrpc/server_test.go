package webrpc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(NewServer(New(&fakeApp{}), "").Handler(nil))
	t.Cleanup(srv.Close)
	return srv
}

// post sends an RPC the way the frontend shim will: JSON array body,
// application/json, same-origin.
func post(t *testing.T, srv *httptest.Server, method string, args ...any) (*http.Response, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(args)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/rpc/"+method, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", srv.URL) // same origin as the page
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	t.Cleanup(func() { res.Body.Close() })
	var j map[string]any
	_ = json.NewDecoder(res.Body).Decode(&j)
	return res, j
}

func TestRPCHappyPath(t *testing.T) {
	srv := testServer(t)
	res, j := post(t, srv, "Ping")
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	if j["result"] != "pong" {
		t.Errorf("result = %v", j["result"])
	}
}

// A method error comes back as an error body the shim rejects on, never as a
// result — a failed Connect must not read as success.
func TestRPCMethodErrorSurfacesAsError(t *testing.T) {
	srv := testServer(t)
	_, j := post(t, srv, "Connect", "")
	if _, ok := j["error"]; !ok {
		t.Fatalf("a failed method returned no error key: %v", j)
	}
	if _, ok := j["result"]; ok {
		t.Errorf("a failed method also returned a result: %v", j)
	}
}

func TestRPCUnknownMethodIs404(t *testing.T) {
	srv := testServer(t)
	res, _ := post(t, srv, "Shutdown") // excluded → looks like it doesn't exist
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("Shutdown status = %d, want 404 (unexposed)", res.StatusCode)
	}
}

// B3: a cross-origin page must not drive the RPC, even though it cannot read the
// reply — the side effect is the danger (DeleteHost, StartUpload).
func TestRPCRejectsForeignOrigin(t *testing.T) {
	srv := testServer(t)
	body, _ := json.Marshal([]any{})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/rpc/Ping", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example.com")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("foreign origin status = %d, want 403", res.StatusCode)
	}
}

// The other half of the CSRF defence: a form/simple-request POST cannot set
// application/json, so text/plain is refused. Combined with the origin check a
// cross-origin page has no path to a side effect.
func TestRPCRequiresJSONContentType(t *testing.T) {
	srv := testServer(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/rpc/Ping", bytes.NewReader([]byte("[]")))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Origin", srv.URL)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain status = %d, want 415", res.StatusCode)
	}
}

func TestRPCLoopbackOriginAllowed(t *testing.T) {
	srv := testServer(t)
	body, _ := json.Marshal([]any{})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/rpc/Ping", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://127.0.0.1:9999") // a different loopback port
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Errorf("loopback origin status = %d, want 200", res.StatusCode)
	}
}

func TestOriginOKMatrix(t *testing.T) {
	for _, tc := range []struct {
		origin, host string
		want         bool
	}{
		{"", "example.com", true},                                      // non-browser
		{"http://127.0.0.1:8080", "example.com", true},                 // loopback
		{"http://localhost:8080", "x", true},                           // localhost
		{"http://[::1]:8080", "x", true},                               // ipv6 loopback
		{"https://litedeck.example.com", "litedeck.example.com", true}, // same origin (proxied)
		{"https://evil.example.com", "litedeck.example.com", false},    // cross origin
		{"http://10.0.0.5", "litedeck.example.com", false},             // LAN, not same host
	} {
		r := httptest.NewRequest(http.MethodPost, "http://"+tc.host+"/rpc/Ping", nil)
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		r.Host = tc.host
		if got := originOK(r); got != tc.want {
			t.Errorf("originOK(origin=%q host=%q) = %v, want %v", tc.origin, tc.host, got, tc.want)
		}
	}
}

// Events must reach a connected socket, and the WS handshake must enforce
// origin the way /rpc does — a WS upgrade is not subject to CORS, so this is
// the only guard on the event stream.
func TestWSDeliversEventsAndChecksOrigin(t *testing.T) {
	s := NewServer(New(&fakeApp{}), "")
	srv := httptest.NewServer(s.Handler(nil))
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):] + "/ws"

	// Foreign origin is refused at the handshake.
	if _, _, err := dialWS(wsURL, "https://evil.example.com"); err == nil {
		t.Error("a cross-origin WebSocket handshake was accepted — the event stream is exposed")
	}

	// Same origin connects and receives a broadcast.
	c, _, err := dialWS(wsURL, srv.URL)
	if err != nil {
		t.Fatalf("same-origin dial: %v", err)
	}
	defer c.Close()
	waitClients(t, s, 1)

	s.Emit("conn:state", map[string]any{"hostId": "h1", "state": "connected"})

	c.SetReadDeadline(deadline())
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	var got struct {
		Event   string         `json:"event"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if got.Event != "conn:state" || got.Payload["hostId"] != "h1" {
		t.Errorf("event = %+v", got)
	}
}

// A tab that stops reading must be dropped, not allowed to stall Emit for every
// other tab — Emit runs on the PTY/log reader path.
func TestSlowClientIsDroppedNotBlocking(t *testing.T) {
	s := NewServer(New(&fakeApp{}), "")
	srv := httptest.NewServer(s.Handler(nil))
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):] + "/ws"

	c, _, err := dialWS(wsURL, srv.URL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	waitClients(t, s, 1)

	// Never read from c. The payload has to be large: a small event drains into
	// the OS socket buffer faster than it piles up, so the client never actually
	// falls behind and nothing is dropped — this test passed on macOS and failed
	// on the Linux CI runner for exactly that reason (a bigger socket buffer).
	// A large payload fills that buffer, backs the send channel up, and forces
	// the drop; Emit must never block regardless. Stop as soon as the client is
	// gone rather than marshalling megabytes past that point.
	big := strings.Repeat("x", 32*1024)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < clientSendBuffer*8; i++ {
			s.Emit("spam", big)
			s.mu.Lock()
			gone := len(s.clients) == 0
			s.mu.Unlock()
			if gone {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("Emit blocked on a client that stopped reading")
	}
	waitClients(t, s, 0) // the slow client was dropped
}
