package sshcore

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// TrustDecision is the user's answer when a host is seen for the first time.
type TrustDecision int

const (
	// TrustReject aborts the connection.
	TrustReject TrustDecision = iota
	// TrustOnce proceeds without recording the key.
	TrustOnce
	// TrustAlways proceeds and appends the key to known_hosts.
	TrustAlways
)

// KeyInfo is what the user is shown before deciding.
type KeyInfo struct {
	Address     string // "host:port" as dialled
	Type        string // e.g. "ssh-ed25519"
	Fingerprint string // "SHA256:..." — the same form OpenSSH prints
}

// Prompter is implemented by the UI layer. It is consulted only for hosts that
// are not yet known; a key that contradicts a recorded one is never offered to
// the user as a choice.
type Prompter interface {
	ConfirmNewHost(KeyInfo) (TrustDecision, error)
}

// ErrHostKeyRejected is returned when the user declines an unknown host.
var ErrHostKeyRejected = errors.New("sshcore: host key rejected by user")

// HostKeyMismatchError reports that a host presented a key different from the
// one on record. This is the signature of a man-in-the-middle, so it is fatal
// by construction: there is no option anywhere in this package to continue
// past it. The UI shows it full-screen (§7.1).
type HostKeyMismatchError struct {
	Address    string
	GotType    string
	GotFinger  string
	KnownFiles []string
}

func (e *HostKeyMismatchError) Error() string {
	return fmt.Sprintf(
		"sshcore: HOST KEY MISMATCH for %s: server offered %s key %s, which contradicts the key recorded in %v",
		e.Address, e.GotType, e.GotFinger, e.KnownFiles,
	)
}

// KnownHosts verifies server keys against an OpenSSH-format known_hosts file,
// prompting on first contact (trust on first use).
type KnownHosts struct {
	path     string
	prompter Prompter

	mu    sync.Mutex
	inner ssh.HostKeyCallback
}

// NewKnownHosts opens (creating if absent) the known_hosts file at path.
func NewKnownHosts(path string, p Prompter) (*KnownHosts, error) {
	if p == nil {
		return nil, errors.New("sshcore: a Prompter is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("sshcore: known_hosts directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("sshcore: known_hosts: %w", err)
	}
	f.Close()

	kh := &KnownHosts{path: path, prompter: p}
	if err := kh.reload(); err != nil {
		return nil, err
	}
	return kh, nil
}

func (k *KnownHosts) reload() error {
	cb, err := knownhosts.New(k.path)
	if err != nil {
		return fmt.Errorf("sshcore: parse known_hosts %s: %w", k.path, err)
	}
	k.inner = cb
	return nil
}

// Callback returns the ssh.HostKeyCallback to install in a HostConfig.
func (k *KnownHosts) Callback() ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		k.mu.Lock()
		defer k.mu.Unlock()

		err := k.inner(hostname, remote, key)
		if err == nil {
			return nil
		}

		var ke *knownhosts.KeyError
		if !errors.As(err, &ke) {
			return fmt.Errorf("sshcore: verify host key for %s: %w", hostname, err)
		}

		// A populated Want list means we hold a different key for this host.
		// Refuse, unconditionally.
		if len(ke.Want) > 0 {
			files := make([]string, 0, len(ke.Want))
			for _, w := range ke.Want {
				files = append(files, w.Filename)
			}
			return &HostKeyMismatchError{
				Address:    hostname,
				GotType:    key.Type(),
				GotFinger:  ssh.FingerprintSHA256(key),
				KnownFiles: files,
			}
		}

		// Otherwise the host is simply unknown: ask.
		decision, perr := k.prompter.ConfirmNewHost(KeyInfo{
			Address:     hostname,
			Type:        key.Type(),
			Fingerprint: ssh.FingerprintSHA256(key),
		})
		if perr != nil {
			return fmt.Errorf("sshcore: host key prompt for %s: %w", hostname, perr)
		}

		switch decision {
		case TrustAlways:
			if err := k.append(hostname, key); err != nil {
				return err
			}
			return nil
		case TrustOnce:
			return nil
		default:
			return fmt.Errorf("%w: %s", ErrHostKeyRejected, hostname)
		}
	}
}

// append records a newly trusted key and re-reads the file so the in-memory
// view matches what is on disk.
func (k *KnownHosts) append(hostname string, key ssh.PublicKey) error {
	f, err := os.OpenFile(k.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("sshcore: open known_hosts for append: %w", err)
	}
	line := knownhosts.Line([]string{hostname}, key)
	if _, err := fmt.Fprintln(f, line); err != nil {
		f.Close()
		return fmt.Errorf("sshcore: write known_hosts: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("sshcore: close known_hosts: %w", err)
	}
	return k.reload()
}

// AcceptOnce is a Prompter for tests and for the probe CLI's --insecure mode.
// It accepts any new host without recording it. Mismatches are still fatal,
// because that check happens before the prompter is consulted.
type AcceptOnce struct{ Seen []KeyInfo }

func (a *AcceptOnce) ConfirmNewHost(k KeyInfo) (TrustDecision, error) {
	a.Seen = append(a.Seen, k)
	return TrustOnce, nil
}
