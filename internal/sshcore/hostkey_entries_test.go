package sshcore

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func testKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return k
}

// A key written for an address is found again under the same address.
//
// The spelling matters: known_hosts writes `[host]:port` for anything but 22, and
// a reader that looked for the literal `host:2222` would find nothing and report a
// server as never seen — which, to settings sync, means "take whatever key the
// repository offers".
func TestTrustedKeysRoundTripIncludingANonDefaultPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	k22 := testKey(t)
	k2222 := testKey(t)

	if err := TrustKey(path, "10.0.0.5:22", k22); err != nil {
		t.Fatalf("trust: %v", err)
	}
	if err := TrustKey(path, "10.0.0.5:2222", k2222); err != nil {
		t.Fatalf("trust: %v", err)
	}

	got, err := TrustedKeys(path, "10.0.0.5:22")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 || !keyEqual(got[0], k22) {
		t.Errorf("port 22 gave %d keys", len(got))
	}
	got, err = TrustedKeys(path, "10.0.0.5:2222")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 || !keyEqual(got[0], k2222) {
		t.Errorf("port 2222 gave %d keys", len(got))
	}
	// A host nobody has connected to has no keys, and that is not an error.
	if got, err := TrustedKeys(path, "10.0.0.9:22"); err != nil || len(got) != 0 {
		t.Errorf("an unknown host gave %d keys, %v", len(got), err)
	}
	if got, err := TrustedKeys(filepath.Join(t.TempDir(), "absent"), "10.0.0.5:22"); err != nil || got != nil {
		t.Errorf("a missing file gave %v, %v", got, err)
	}
}

// The verifier and this reader agree about what is in the file.
//
// Two spellings of an address would mean a key the connection trusts and the sync
// cannot see, or the other way round.
func TestWhatTheVerifierWroteIsWhatThisReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	key := testKey(t)

	kh, err := NewKnownHosts(path, &AcceptOnce{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// The path the first-contact prompt takes, with the same address the ssh
	// client hands the callback.
	if err := kh.append("10.0.0.5:2222", key); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := TrustedKeys(path, "10.0.0.5:2222")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 || !keyEqual(got[0], key) {
		t.Errorf("the verifier's own entry was not readable: %d keys", len(got))
	}
}

// A line nobody can parse does not hide the rest of the file.
func TestOneBadLineDoesNotHideTheOthers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	key := testKey(t)
	if err := TrustKey(path, "10.0.0.5:22", key); err != nil {
		t.Fatalf("trust: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(path, append([]byte("this is not a known_hosts line\n"), data...), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := TrustedKeys(path, "10.0.0.5:22")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("a hand-edited file cost a real entry: %d keys", len(got))
	}
}

func keyEqual(a, b ssh.PublicKey) bool {
	return ssh.FingerprintSHA256(a) == ssh.FingerprintSHA256(b)
}
