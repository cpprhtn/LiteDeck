package webrpc

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Built-in single-password login (arch/08).
//
// Grafana's shape: hit the port, get a login form, and you are in — no token in
// the URL, no reverse proxy required for basic protection. One password, so it
// stays single-user (the password is the owner). A session cookie is what makes
// this cleaner than the URL token on every axis: it is HttpOnly (a script that
// slips in cannot read it), SameSite=Strict (a cross-site page cannot ride it),
// and — the part the token could not do — it travels on the WebSocket handshake
// automatically, so the event stream is authenticated the same way as /rpc.

const sessionCookie = "litedeck_session"
const sessionTTL = 7 * 24 * time.Hour

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time // id → expiry
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: map[string]time.Time{}}
}

func (s *sessionStore) create() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)
	s.mu.Lock()
	s.sessions[id] = time.Now().Add(sessionTTL)
	s.mu.Unlock()
	return id, nil
}

func (s *sessionStore) valid(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[id]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, id)
		return false
	}
	return true
}

func (s *sessionStore) drop(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// sessionValid reports whether the request carries a live session cookie.
func (s *Server) sessionValid(r *http.Request) bool {
	if s.password == "" {
		return false
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return s.sessions.valid(c.Value)
}

// handleLogin serves the form (GET) and checks the password (POST).
//
// On success it mints a session and redirects to the app. The password is
// compared in constant time — it is a shared secret, and an early-return
// compare leaks its length and prefix to anything that can time the response.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// Already logged in? Go straight to the app.
		if s.sessionValid(r) {
			http.Redirect(w, r, "./", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(loginPage("")))
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		got := r.PostFormValue("password")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.password)) != 1 {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(loginPage("비밀번호가 올바르지 않습니다 · Wrong password")))
			return
		}
		id, err := s.sessions.create()
		if err != nil {
			http.Error(w, "session error", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookie,
			Value:    id,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			Secure:   requestIsHTTPS(r), // the browser↔proxy leg is HTTPS behind a TLS proxy
			MaxAge:   int(sessionTTL / time.Second),
		})
		http.Redirect(w, r, "./", http.StatusFound)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleLogout drops the session and clears the cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.sessions.drop(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "login", http.StatusFound)
}

func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// loginPage is a self-contained form — no bundle, no external asset, so it
// loads before (and independently of) the app. Themed to sit next to the UI.
func loginPage(errMsg string) string {
	errBlock := ""
	if errMsg != "" {
		errBlock = `<p class="err">` + htmlEscape(errMsg) + `</p>`
	}
	return `<!doctype html><html lang="ko"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>LiteDeck</title>
<style>
  :root { color-scheme: light dark; }
  body { margin:0; min-height:100vh; display:grid; place-items:center;
    font:15px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;
    background:#f6f7f9; color:#1a1a1a; }
  @media (prefers-color-scheme: dark){ body{ background:#0f1115; color:#e6e6e6; } .card{ background:#171a21; border-color:#262b36; } input{ background:#0f1115; color:#e6e6e6; border-color:#2a2f3a; } }
  .card { width:320px; padding:32px 28px; background:#fff; border:1px solid #e5e7eb;
    border-radius:12px; box-shadow:0 4px 24px rgba(0,0,0,.06); }
  h1 { margin:0 0 4px; font-size:20px; } .sub{ margin:0 0 20px; opacity:.6; font-size:13px; }
  label { display:block; font-size:13px; margin-bottom:6px; opacity:.8; }
  input { width:100%; box-sizing:border-box; padding:10px 12px; font-size:15px;
    border:1px solid #d1d5db; border-radius:8px; }
  button { width:100%; margin-top:16px; padding:10px; font-size:15px; font-weight:600;
    color:#fff; background:#2563eb; border:0; border-radius:8px; cursor:pointer; }
  button:hover { background:#1d4ed8; }
  .err { margin:0 0 14px; padding:9px 12px; font-size:13px; color:#b91c1c;
    background:#fef2f2; border-radius:8px; } @media (prefers-color-scheme: dark){ .err{ background:#3a1414; color:#fca5a5; } }
</style></head><body>
  <form class="card" method="post" action="login">
    <h1>LiteDeck</h1><p class="sub">이 서버에 접속하려면 로그인하세요</p>
    ` + errBlock + `
    <label for="pw">비밀번호 · Password</label>
    <input id="pw" name="password" type="password" autofocus autocomplete="current-password">
    <button type="submit">로그인 · Sign in</button>
  </form>
</body></html>`
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
