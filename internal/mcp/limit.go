package mcp

import (
	"sync"
	"time"
)

// Rate limiting.
//
// This is not politeness, it is the point of the feature. LiteDeck exists so a
// small server is not made to work for the client's convenience, and an agent
// loop issues calls far faster than a person clicking. The Exec pool is three
// channels wide — chosen to stay under sshd's default MaxSessions of 10
// alongside SFTP and the terminals — so a burst does not just slow the server
// down, it starves the GUI the user is looking at.
//
// A short result cache is not a substitute: it removes repeats, not volume.

// Limiter is a token bucket over tool calls.
type Limiter struct {
	mu       sync.Mutex
	capacity float64
	rate     float64 // tokens per second
	tokens   float64
	last     time.Time
	now      func() time.Time // swappable so the test does not sleep
}

// NewLimiter returns a bucket that refills at rate per second and holds burst.
//
// Defaults worth knowing: burst covers one agent turn that fans out across a
// few tools (a diagnosis usually touches four to six), and the refill rate is
// what a server can absorb indefinitely without the user noticing.
func NewLimiter(perSecond float64, burst int) *Limiter {
	return &Limiter{
		capacity: float64(burst),
		rate:     perSecond,
		tokens:   float64(burst),
		last:     time.Now(),
		now:      time.Now,
	}
}

// Allow reports whether a call may proceed, and if not, how long to wait.
func (l *Limiter) Allow() (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if elapsed := now.Sub(l.last); elapsed > 0 {
		l.tokens += elapsed.Seconds() * l.rate
		if l.tokens > l.capacity {
			l.tokens = l.capacity
		}
		l.last = now
	}

	if l.tokens >= 1 {
		l.tokens--
		return true, 0
	}

	// How long until one token exists. Reported to the model rather than
	// enforced by sleeping: a call that blocks looks like a hung server, where
	// "wait 2s" is something an agent can act on.
	need := (1 - l.tokens) / l.rate
	return false, time.Duration(need * float64(time.Second))
}
