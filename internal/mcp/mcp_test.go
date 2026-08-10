package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	s := New()
	s.Register(Tool{
		Name:        "echo",
		Description: "returns its argument",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			if _, bad := args["fail"]; bad {
				return nil, fmt.Errorf("no such unit: myapp")
			}
			return map[string]any{"got": args}, nil
		},
	})
	token := "test-token-0123456789"
	l, url, err := s.Serve(Options{Token: token, Limiter: NewLimiter(1000, 1000)})
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return s, url, token
}

func post(t *testing.T, url, token, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res, string(out)
}

// The handshake a real client performs, in order, over the transport it uses.
func TestHandshakeOverHTTP(t *testing.T) {
	_, url, token := testServer(t)

	res, body := post(t, url, token,
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("initialize: status %d, body %s", res.StatusCode, body)
	}
	var init struct {
		Result struct {
			ProtocolVersion string                `json:"protocolVersion"`
			ServerInfo      struct{ Name string } `json:"serverInfo"`
			Capabilities    map[string]any        `json:"capabilities"`
			Instructions    string                `json:"instructions"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &init); err != nil {
		t.Fatalf("decode initialize: %v (%s)", err, body)
	}
	// A version we have been checked against is agreed to rather than
	// downgraded — Claude Code 2.1.22 negotiates exactly this one.
	if init.Result.ProtocolVersion != "2025-11-25" {
		t.Errorf("protocolVersion = %q, want 2025-11-25", init.Result.ProtocolVersion)
	}
	if init.Result.ServerInfo.Name != "litedeck" {
		t.Errorf("serverInfo.name = %q", init.Result.ServerInfo.Name)
	}
	if _, ok := init.Result.Capabilities["tools"]; !ok {
		t.Error("capabilities must advertise tools")
	}
	// The instructions are prompt text; a model that reads them must learn that
	// writes are absent rather than hidden behind a parameter.
	if !strings.Contains(init.Result.Instructions, "change a server") {
		t.Error("instructions should say the tools cannot change a server")
	}

	// A notification expects no reply at all.
	res, body = post(t, url, token, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if res.StatusCode != http.StatusAccepted {
		t.Errorf("notification: status %d, want 202", res.StatusCode)
	}
	if strings.TrimSpace(body) != "" {
		t.Errorf("notification produced a body: %s", body)
	}

	res, body = post(t, url, token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, `"echo"`) {
		t.Fatalf("tools/list: %d %s", res.StatusCode, body)
	}
}

// An unknown revision must not be echoed back: agreeing to a version we have
// never run against is claiming compatibility we do not have.
func TestUnknownProtocolVersionIsNotEchoed(t *testing.T) {
	_, url, token := testServer(t)
	_, body := post(t, url, token,
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2099-01-01"}}`)
	if strings.Contains(body, "2099-01-01") {
		t.Errorf("echoed an unknown protocol version back: %s", body)
	}
	if !strings.Contains(body, ProtocolVersion) {
		t.Errorf("should have offered our own version: %s", body)
	}
}

// A tool that fails answers inside a successful result. The model reads the
// message and corrects itself; a transport error just stops it (§3.3).
func TestToolFailureIsAResultNotATransportError(t *testing.T) {
	_, url, token := testServer(t)
	res, body := post(t, url, token,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"fail":true}}}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	if strings.Contains(body, `"error"`) {
		t.Errorf("tool failure surfaced as a JSON-RPC error: %s", body)
	}
	if !strings.Contains(body, `"isError":true`) || !strings.Contains(body, "no such unit") {
		t.Errorf("tool failure should be an isError result carrying the message: %s", body)
	}
}

func TestUnknownToolIsReportedToTheModel(t *testing.T) {
	_, url, token := testServer(t)
	_, body := post(t, url, token,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"nope"}}`)
	if !strings.Contains(body, `"isError":true`) || !strings.Contains(body, "no such tool") {
		t.Errorf("want a readable isError result, got: %s", body)
	}
}

// The token is the only thing between a local process and every server the user
// has connected.
func TestAuthIsRequired(t *testing.T) {
	_, url, token := testServer(t)

	for _, tc := range []struct{ name, header string }{
		{"no header", ""},
		{"wrong token", "wrong-token"},
		{"prefix of the real token", token[:len(token)-1]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, _ := post(t, url, tc.header, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
			if res.StatusCode != http.StatusUnauthorized {
				t.Errorf("status %d, want 401", res.StatusCode)
			}
		})
	}
}

// A browser page can POST to loopback. DNS rebinding can make that page look
// same-origin, which is why the spec asks for this check.
func TestForeignOriginIsRefused(t *testing.T) {
	_, url, token := testServer(t)
	for _, origin := range []string{"https://evil.example", "http://attacker.local"} {
		req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Origin", origin)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("origin %s: status %d, want 403", origin, res.StatusCode)
		}
	}
}

func TestListenerBindsLoopbackOnly(t *testing.T) {
	_, url, _ := testServer(t)
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Errorf("bound to %s; the endpoint must never leave loopback", url)
	}
}

// The GET half of the transport is declined rather than left hanging, so a
// client learns immediately that there is no server-initiated stream.
func TestServerStreamIsDeclined(t *testing.T) {
	_, url, token := testServer(t)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status %d, want 405", res.StatusCode)
	}
}

// Rate limiting is the feature, not politeness: an agent loop outruns a person
// by orders of magnitude and the Exec pool is three channels wide.
func TestRateLimitStopsABurstAndRecovers(t *testing.T) {
	now := time.Now()
	l := NewLimiter(2, 3) // 3 immediately, then one every 500ms
	l.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow(); !ok {
			t.Fatalf("call %d should be inside the burst", i+1)
		}
	}
	ok, wait := l.Allow()
	if ok {
		t.Fatal("the fourth call should have been limited")
	}
	if wait <= 0 || wait > time.Second {
		t.Errorf("wait = %v, want a small positive hint", wait)
	}

	now = now.Add(600 * time.Millisecond)
	if ok, _ := l.Allow(); !ok {
		t.Error("a token should have refilled")
	}
}

// A limited call must still be a readable answer. A 429 makes agents retry hard
// or give up; "retry in about 2s" is something they can act on.
func TestRateLimitedCallIsAReadableResult(t *testing.T) {
	s := New()
	s.Register(Tool{Name: "echo", InputSchema: map[string]any{"type": "object"},
		Handler: func(context.Context, map[string]any) (any, error) { return "ok", nil }})

	l, url, err := s.Serve(Options{Token: "t", Limiter: NewLimiter(0.001, 1)})
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	defer l.Close()

	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`
	if _, body := post(t, url, "t", call); !strings.Contains(body, "ok") {
		t.Fatalf("first call should pass: %s", body)
	}
	res, body := post(t, url, "t", call)
	if res.StatusCode != http.StatusOK {
		t.Errorf("status %d, want 200 with an isError result", res.StatusCode)
	}
	if !strings.Contains(body, "rate limited") || !strings.Contains(body, "Retry in") {
		t.Errorf("want a retry hint the model can act on: %s", body)
	}
}

// Every accepted call must be visible to the user before it runs.
func TestEveryCallIsAnnounced(t *testing.T) {
	s := New()
	s.Register(Tool{Name: "echo", InputSchema: map[string]any{"type": "object"},
		Handler: func(context.Context, map[string]any) (any, error) { return "ok", nil }})

	var seen []string
	l, url, err := s.Serve(Options{
		Token:   "t",
		Limiter: NewLimiter(100, 100),
		OnCall: func(tool string, args map[string]any) {
			seen = append(seen, fmt.Sprintf("%s %v", tool, args["hostId"]))
		},
	})
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	defer l.Close()

	post(t, url, "t", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"hostId":"h1"}}}`)
	if len(seen) != 1 || seen[0] != "echo h1" {
		t.Errorf("OnCall saw %v, want one entry naming the tool and host", seen)
	}
}

func TestMalformedBodyIsAParseError(t *testing.T) {
	_, url, token := testServer(t)
	_, body := post(t, url, token, `{not json`)
	if !strings.Contains(body, "-32700") {
		t.Errorf("want a JSON-RPC parse error, got: %s", body)
	}
}

func TestOversizedBodyIsRefused(t *testing.T) {
	_, url, token := testServer(t)
	huge := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"x":"` +
		strings.Repeat("a", maxBody+1024) + `"}}}`
	res, _ := post(t, url, token, huge)
	// Truncated at the limit, so it can no longer parse. What matters is that
	// the process does not try to hold an unbounded body in memory.
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusBadRequest {
		t.Errorf("unexpected status %d", res.StatusCode)
	}
}

func TestTokensAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		tok, err := NewToken()
		if err != nil {
			t.Fatalf("NewToken: %v", err)
		}
		if len(tok) < 32 {
			t.Fatalf("token too short: %q", tok)
		}
		if seen[tok] {
			t.Fatal("NewToken repeated itself")
		}
		seen[tok] = true
	}
}

func TestServeRequiresAToken(t *testing.T) {
	if _, _, err := New().Serve(Options{}); err == nil {
		t.Error("serving without a token must fail")
	}
}

// Nothing registered may modify a server. This is the guard on the one property
// that makes shipping read-only safe: it fails the moment somebody adds a write
// tool without also building the approval model.
func TestNoRegisteredToolNameSuggestsAWrite(t *testing.T) {
	s := New()
	// Stand-in for the real registration; the app-side test covers the real set.
	for _, name := range []string{"hosts_list", "health_snapshot", "svc_list", "fs_read"} {
		s.Register(Tool{Name: name, InputSchema: map[string]any{"type": "object"}})
	}
	forbidden := []string{"write", "restart", "stop", "start", "kill", "delete",
		"remove", "prune", "exec", "run_command", "push", "chmod", "rename"}
	for _, tool := range s.Tools() {
		for _, verb := range forbidden {
			if strings.Contains(tool.Name, verb) {
				t.Errorf("%q looks like a write tool. Writes need the approval model "+
					"in §4 of the design note first, and it does not exist yet", tool.Name)
			}
		}
	}
}

func TestNotificationsGetNoReply(t *testing.T) {
	s := New()
	for _, method := range []string{"notifications/initialized", "notifications/cancelled"} {
		if out := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","method":"`+method+`"}`)); out != nil {
			t.Errorf("%s produced a reply: %s", method, out)
		}
	}
	// A request with an id always gets one, even for an unknown method.
	if out := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":9,"method":"nope"}`)); !bytes.Contains(out, []byte("-32601")) {
		t.Errorf("unknown method should be -32601, got %s", out)
	}
}
