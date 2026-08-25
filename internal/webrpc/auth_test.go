package webrpc

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func loginServer(t *testing.T, pw string) *httptest.Server {
	t.Helper()
	s := NewServer(New(&fakeApp{}), "")
	s.SetPassword(pw)
	// A trivial static handler so the gated root has something to serve.
	static := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("APP"))
	})
	srv := httptest.NewServer(s.Handler(static))
	t.Cleanup(srv.Close)
	return srv
}

// A jar-less client so each request's cookies are explicit.
func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// Without a session, the app itself redirects to /login — you cannot see it
// first and authenticate later.
func TestUnauthenticatedAppRedirectsToLogin(t *testing.T) {
	srv := loginServer(t, "hunter2")
	c := noRedirectClient()
	res, err := c.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound || !strings.Contains(res.Header.Get("Location"), "login") {
		t.Errorf("root without a session = %d %q, want redirect to login", res.StatusCode, res.Header.Get("Location"))
	}
}

// And so does the API: no session, no data.
func TestUnauthenticatedRPCIs401(t *testing.T) {
	srv := loginServer(t, "hunter2")
	body := strings.NewReader("[]")
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/rpc/Ping", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", srv.URL)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("rpc without a session = %d, want 401", res.StatusCode)
	}
}

func TestWrongPasswordDoesNotAuthenticate(t *testing.T) {
	srv := loginServer(t, "hunter2")
	c := noRedirectClient()
	res, err := c.PostForm(srv.URL+"/login", url.Values{"password": {"nope"}})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong password = %d, want 401", res.StatusCode)
	}
	if len(res.Cookies()) != 0 {
		t.Error("a wrong password still set a cookie")
	}
}

// The whole point: log in with the password, get a cookie, and the API works
// with no token in any URL.
func TestLoginThenSessionGrantsAccess(t *testing.T) {
	srv := loginServer(t, "hunter2")
	c := noRedirectClient()

	res, err := c.PostForm(srv.URL+"/login", url.Values{"password": {"hunter2"}})
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("login = %d, want redirect", res.StatusCode)
	}
	var session *http.Cookie
	for _, ck := range res.Cookies() {
		if ck.Name == sessionCookie {
			session = ck
		}
	}
	if session == nil || session.Value == "" {
		t.Fatal("login set no session cookie")
	}
	if !session.HttpOnly || session.SameSite != http.SameSiteStrictMode {
		t.Errorf("session cookie is not HttpOnly+SameSite=Strict: %+v", session)
	}

	// The cookie now authenticates /rpc — no token anywhere.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/rpc/Ping", strings.NewReader("[]"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", srv.URL)
	req.AddCookie(session)
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Errorf("rpc with a session cookie = %d, want 200", res2.StatusCode)
	}

	// The same cookie authorises the WebSocket handshake — the thing the token
	// could not do (a browser cannot set a header on a WS upgrade).
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	h := http.Header{"Origin": {srv.URL}, "Cookie": {session.Name + "=" + session.Value}}
	conn, _, err := dialWSWithHeader(wsURL, h)
	if err != nil {
		t.Fatalf("ws with session cookie: %v", err)
	}
	conn.Close()

	// Logout drops it.
	req3, _ := http.NewRequest(http.MethodGet, srv.URL+"/logout", nil)
	req3.AddCookie(session)
	res3, err := noRedirectClient().Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	res3.Body.Close()
	// The session must no longer work.
	req4, _ := http.NewRequest(http.MethodPost, srv.URL+"/rpc/Ping", strings.NewReader("[]"))
	req4.Header.Set("Content-Type", "application/json")
	req4.Header.Set("Origin", srv.URL)
	req4.AddCookie(session)
	res4, err := http.DefaultClient.Do(req4)
	if err != nil {
		t.Fatal(err)
	}
	defer res4.Body.Close()
	if res4.StatusCode != http.StatusUnauthorized {
		t.Errorf("session still valid after logout = %d, want 401", res4.StatusCode)
	}
}

// With no password set, there is no login page and the app is open (loopback
// dev) — the route simply is not registered.
func TestNoLoginRouteWithoutPassword(t *testing.T) {
	s := NewServer(New(&fakeApp{}), "")
	srv := httptest.NewServer(s.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("APP"))
	})))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	// "/login" is unregistered, so it falls through to the static "/" handler.
	if res.StatusCode == http.StatusOK {
		body := make([]byte, 3)
		res.Body.Read(body)
		if string(body) != "APP" {
			t.Errorf("unexpected /login body without password: %q", body)
		}
	}
}
