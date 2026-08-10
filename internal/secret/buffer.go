package secret

// §7.2 requires a password to be wiped from memory as soon as it has been used.
// Go makes that only partly possible, and the gap is worth stating plainly
// rather than papering over.
//
// A Go string is immutable and may be copied by the runtime at any time, so a
// secret held as a string cannot be erased. x/crypto/ssh takes passwords as
// strings, which means at the moment of authentication a copy exists that this
// package cannot reach; it becomes garbage immediately and is freed on the next
// collection, but it is not zeroed on demand.
//
// Status: NOT WIRED UP. The auth and sudo paths still carry the password as a
// plain string end to end, so nothing is wiped on demand today — the README's
// security section says exactly this rather than implying otherwise. This type
// exists for the paths that will hold a secret long enough to be worth wiping;
// until one uses it, do not read the comment above as a guarantee.
//
// What Buffer does achieve when it is used: the app's own long-lived copy — the one sitting in
// a struct between the user typing it and the handshake finishing, and the one
// that would otherwise appear in a heap dump or a core file minutes later — is
// held as bytes and wiped deterministically. That is the copy whose lifetime is
// long enough to matter.

import "crypto/subtle"

// Buffer holds a secret in memory that can be wiped on demand.
type Buffer struct {
	b []byte
}

// NewBuffer copies s into a wipeable buffer.
func NewBuffer(s string) *Buffer {
	return &Buffer{b: []byte(s)}
}

// NewBufferFromBytes takes ownership of b; the caller must not reuse it.
func NewBufferFromBytes(b []byte) *Buffer {
	return &Buffer{b: b}
}

// Bytes exposes the underlying slice without copying. Callers must not retain
// it past a Wipe.
func (s *Buffer) Bytes() []byte {
	if s == nil {
		return nil
	}
	return s.b
}

// Len reports the secret's length.
func (s *Buffer) Len() int {
	if s == nil {
		return 0
	}
	return len(s.b)
}

// Reveal returns the secret as a string for an API that demands one.
//
// This is the point at which the guarantee weakens: the returned string is
// immutable and outside this package's control. Call it as late as possible,
// pass the result straight to the API that needs it, and keep no reference.
func (s *Buffer) Reveal() string {
	if s == nil {
		return ""
	}
	return string(s.b)
}

// Equal compares two secrets in constant time.
func (s *Buffer) Equal(other *Buffer) bool {
	if s == nil || other == nil {
		return s == nil && other == nil
	}
	return subtle.ConstantTimeCompare(s.b, other.b) == 1
}

// Wipe overwrites the buffer with zeroes and releases it. Safe to call twice.
func (s *Buffer) Wipe() {
	if s == nil {
		return
	}
	for i := range s.b {
		s.b[i] = 0
	}
	s.b = nil
}

// String hides the secret from accidental logging. fmt verbs, structured
// loggers and %v on an enclosing struct all route through this.
func (s *Buffer) String() string { return "«secret»" }

// GoString does the same for %#v.
func (s *Buffer) GoString() string { return "«secret»" }
