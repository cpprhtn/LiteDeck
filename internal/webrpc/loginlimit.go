package webrpc

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Slowing down password guessing on /login.
//
// The login page is one shared password in front of an endpoint that opens SSH
// sessions to every host the user has saved. Constant-time comparison stops the
// secret leaking through timing; it does nothing about trying the whole
// dictionary, which over a LAN is a few thousand attempts a second.
//
// Per source address rather than globally: a global counter lets one attacker
// lock the real user out, which trades a guessing problem for a denial of
// service on the person who owns the box.

const (
	// loginBurst is how many wrong passwords an address may try before it waits.
	// Five is more than a person mistypes in a row and far fewer than a
	// dictionary needs.
	loginBurst = 5
	// loginWindow is how long failures are remembered, and how long a blocked
	// address stays blocked. A minute turns an unlimited guess rate into 5 a
	// minute — 300 a day against a password that only has to survive until
	// somebody changes it.
	loginWindow = time.Minute
	// loginTrackMax bounds the table. An attacker with many source addresses
	// could otherwise grow it without limit; past this, the oldest entries go.
	loginTrackMax = 4096
)

type loginLimiter struct {
	mu   sync.Mutex
	seen map[string]*loginAttempts
}

type loginAttempts struct {
	fails int
	until time.Time // when the current window ends
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{seen: map[string]*loginAttempts{}}
}

// allow reports whether this address may try a password now.
func (l *loginLimiter) allow(addr string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.seen[addr]
	if a == nil || now.After(a.until) {
		return true
	}
	return a.fails < loginBurst
}

// fail records a wrong password and returns how long the address must wait.
func (l *loginLimiter) fail(addr string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)

	a := l.seen[addr]
	if a == nil || now.After(a.until) {
		a = &loginAttempts{}
		l.seen[addr] = a
	}
	a.fails++
	a.until = now.Add(loginWindow)
	if a.fails < loginBurst {
		return 0
	}
	return loginWindow
}

// succeed clears the address. Getting it right is proof this is not a guesser,
// and leaving the count would lock somebody out of their own box after five
// typos earlier in the day.
func (l *loginLimiter) succeed(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.seen, addr)
}

// sweep drops expired entries, and — only if that was not enough — the rest.
// Called with the lock held.
func (l *loginLimiter) sweep(now time.Time) {
	if len(l.seen) < loginTrackMax {
		return
	}
	for k, v := range l.seen {
		if now.After(v.until) {
			delete(l.seen, k)
		}
	}
	if len(l.seen) < loginTrackMax {
		return
	}
	// Still full: every entry is live, which means a distributed attempt. Start
	// over rather than grow. Losing the counts is the lesser harm — the table
	// itself must not become the denial of service.
	l.seen = map[string]*loginAttempts{}
}

// clientAddr is the key attempts are counted against.
//
// RemoteAddr only. X-Forwarded-For is whatever the client typed unless a proxy
// is known to rewrite it, and trusting it here would let one attacker spread
// across a million imaginary addresses — which is exactly the limit this is
// meant to impose. Behind a reverse proxy every attempt therefore shares the
// proxy's address, so the limit applies to the proxy as a whole: stricter than
// intended rather than looser.
func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
