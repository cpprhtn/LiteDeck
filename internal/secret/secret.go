// Package secret keeps passwords and key passphrases in the operating
// system's credential store — macOS Keychain, Windows Credential Manager,
// Linux Secret Service (§6).
//
// LiteDeck never writes a secret to its own files. Where no credential store is
// reachable, the answer is not a weaker file format but "do not store it";
// the user is asked each time.
package secret

import (
	"errors"
	"fmt"
	"sync"

	"github.com/zalando/go-keyring"
)

// Service is the name secrets are filed under in the OS store.
const Service = "litedeck"

// Kind distinguishes the secrets a single host can have.
type Kind string

const (
	KindPassword   Kind = "password"   // login password
	KindPassphrase Kind = "passphrase" // private key passphrase
	KindSudo       Kind = "sudo"       // sudo password, opt-in only (§7.2)
	// KindKumaAPIKey reads an Uptime Kuma instance's /metrics endpoint. Not a
	// credential for the server itself, but it is a bearer secret and there is
	// no second place to keep those.
	KindKumaAPIKey Kind = "kuma-api-key"
)

// ErrNotFound reports that no secret is stored for that host and kind.
var ErrNotFound = errors.New("secret: not stored")

// ErrUnavailable reports that this machine has no usable credential store.
var ErrUnavailable = errors.New("secret: no OS credential store available")

// Store is the interface the rest of the app depends on.
type Store interface {
	Get(hostID string, k Kind) (string, error)
	Set(hostID string, k Kind, value string) error
	Delete(hostID string, k Kind) error
	// Available reports whether secrets can be persisted at all. When false,
	// the UI must not offer "remember this password".
	Available() bool
}

func account(hostID string, k Kind) string {
	return string(k) + ":" + hostID
}

// Keyring is the real store, backed by the OS.
type Keyring struct {
	once      sync.Once
	available bool
}

// NewKeyring returns a store backed by the OS credential store.
func NewKeyring() *Keyring { return &Keyring{} }

// Available probes the credential store once and caches the answer.
//
// The probe is a read of a name that does not exist. A "not found" reply proves
// the store is reachable; anything else — an unsupported platform, a missing
// D-Bus Secret Service on a headless Linux box — means it is not. Reading
// rather than writing keeps the probe from leaving anything behind.
func (k *Keyring) Available() bool {
	k.once.Do(func() {
		_, err := keyring.Get(Service, "litedeck-probe-does-not-exist")
		k.available = err == nil || errors.Is(err, keyring.ErrNotFound)
	})
	return k.available
}

func (k *Keyring) Get(hostID string, kind Kind) (string, error) {
	if !k.Available() {
		return "", ErrUnavailable
	}
	v, err := keyring.Get(Service, account(hostID, kind))
	if errors.Is(err, keyring.ErrNotFound) {
		return "", fmt.Errorf("%w: %s/%s", ErrNotFound, hostID, kind)
	}
	if err != nil {
		return "", fmt.Errorf("secret: read %s/%s: %w", hostID, kind, err)
	}
	return v, nil
}

func (k *Keyring) Set(hostID string, kind Kind, value string) error {
	if !k.Available() {
		return ErrUnavailable
	}
	if err := keyring.Set(Service, account(hostID, kind), value); err != nil {
		return fmt.Errorf("secret: store %s/%s: %w", hostID, kind, err)
	}
	return nil
}

func (k *Keyring) Delete(hostID string, kind Kind) error {
	if !k.Available() {
		return ErrUnavailable
	}
	err := keyring.Delete(Service, account(hostID, kind))
	if errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("%w: %s/%s", ErrNotFound, hostID, kind)
	}
	if err != nil {
		return fmt.Errorf("secret: delete %s/%s: %w", hostID, kind, err)
	}
	return nil
}

// Ephemeral is the fallback when no credential store exists: it stores nothing,
// so the user is asked every time. Reporting Available() as false is what makes
// the UI hide "remember this password" instead of offering a promise it cannot
// keep.
type Ephemeral struct{}

func (Ephemeral) Get(hostID string, k Kind) (string, error) {
	return "", fmt.Errorf("%w: %s/%s", ErrNotFound, hostID, k)
}
func (Ephemeral) Set(string, Kind, string) error { return ErrUnavailable }
func (Ephemeral) Delete(string, Kind) error      { return ErrUnavailable }
func (Ephemeral) Available() bool                { return false }

// Open returns the OS credential store, or the ephemeral one if there is none.
func Open() Store {
	k := NewKeyring()
	if k.Available() {
		return k
	}
	return Ephemeral{}
}
