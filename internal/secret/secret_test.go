package secret

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// The real credential store is not touched: on macOS that would pop an
// authorisation dialog and hang the run. go-keyring's mock exercises the same
// code path in memory.
func TestKeyringRoundTrip(t *testing.T) {
	keyring.MockInit()
	k := NewKeyring()

	if !k.Available() {
		t.Fatal("mock keyring reports unavailable")
	}

	if _, err := k.Get("h1", KindPassword); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get before Set: err = %v, want ErrNotFound", err)
	}

	if err := k.Set("h1", KindPassword, "hunter2"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := k.Get("h1", KindPassword)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "hunter2" {
		t.Errorf("Get = %q", got)
	}

	// Kinds and hosts must not collide.
	if err := k.Set("h1", KindPassphrase, "different"); err != nil {
		t.Fatal(err)
	}
	if v, _ := k.Get("h1", KindPassword); v != "hunter2" {
		t.Errorf("passphrase overwrote password: %q", v)
	}
	if err := k.Set("h2", KindPassword, "other-host"); err != nil {
		t.Fatal(err)
	}
	if v, _ := k.Get("h1", KindPassword); v != "hunter2" {
		t.Errorf("second host overwrote the first: %q", v)
	}

	if err := k.Delete("h1", KindPassword); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := k.Get("h1", KindPassword); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete: err = %v, want ErrNotFound", err)
	}
}

// TestEphemeralStoresNothing pins the §6 fallback: with no credential store the
// answer is "ask every time", never a weaker place to write it.
func TestEphemeralStoresNothing(t *testing.T) {
	var s Store = Ephemeral{}
	if s.Available() {
		t.Error("Ephemeral reports Available() == true; the UI would offer to remember passwords it cannot keep")
	}
	if err := s.Set("h1", KindPassword, "hunter2"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Set: err = %v, want ErrUnavailable", err)
	}
	if _, err := s.Get("h1", KindPassword); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get: err = %v, want ErrNotFound", err)
	}
}

func TestBufferWipe(t *testing.T) {
	b := NewBuffer("hunter2")
	raw := b.Bytes()
	if b.Reveal() != "hunter2" || b.Len() != 7 {
		t.Fatalf("Buffer = %q len %d", b.Reveal(), b.Len())
	}

	b.Wipe()
	for i, c := range raw {
		if c != 0 {
			t.Fatalf("byte %d not zeroed after Wipe: %q", i, raw)
		}
	}
	if b.Len() != 0 || b.Reveal() != "" {
		t.Errorf("Buffer still holds data after Wipe")
	}
	b.Wipe() // must be safe twice
}

// TestBufferNeverFormatsItsContents guards the §7.2 rule that a password must
// never reach a log or the Command Log. The commonest way that happens is a
// struct being printed with %v, so the type refuses to render itself.
func TestBufferNeverFormatsItsContents(t *testing.T) {
	b := NewBuffer("hunter2")
	holder := struct {
		Host   string
		Secret *Buffer
	}{Host: "prod", Secret: b}

	for _, s := range []string{
		fmt.Sprint(b),
		fmt.Sprintf("%v", b),
		fmt.Sprintf("%s", b),
		fmt.Sprintf("%#v", b),
		fmt.Sprintf("%v", holder),
		fmt.Sprintf("%+v", holder),
	} {
		if strings.Contains(s, "hunter2") {
			t.Errorf("secret leaked through formatting: %q", s)
		}
	}
}

func TestBufferEqual(t *testing.T) {
	a, b := NewBuffer("same"), NewBuffer("same")
	if !a.Equal(b) {
		t.Error("identical secrets compare unequal")
	}
	if a.Equal(NewBuffer("different")) {
		t.Error("different secrets compare equal")
	}
	var nilBuf *Buffer
	if nilBuf.Equal(a) || a.Equal(nilBuf) {
		t.Error("nil compares equal to a secret")
	}
	if !nilBuf.Equal(nil) {
		t.Error("nil should equal nil")
	}
}

// A nil Buffer must behave, since "no secret configured" is the normal case.
func TestNilBufferIsSafe(t *testing.T) {
	var b *Buffer
	if b.Len() != 0 || b.Reveal() != "" || b.Bytes() != nil {
		t.Error("nil Buffer misbehaves")
	}
	b.Wipe()
}
