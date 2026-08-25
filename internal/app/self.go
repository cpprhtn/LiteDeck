package app

import (
	"context"
	"fmt"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/config"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
	"golang.org/x/crypto/ssh"
)

// "This server" mode (arch/08).
//
// The server binary can manage the box it runs on: on boot it connects to that
// box's own sshd on loopback and registers it as a host, so opening the web UI
// lands you already inside it — files, monitoring, terminal, services — the way
// Grafana or Cockpit show the machine they are installed on, without an "add a
// host and connect" step.
//
// It is still SSH. Nothing new reads the local filesystem or /proc directly;
// the box reaches itself over 127.0.0.1, which keeps every adapter, the whole
// transport, and the security model exactly as they are for a remote host. The
// price is that the box needs its own sshd reachable on loopback and a
// credential to log in with — a key is the clean way.

// SelfID is the fixed host id for "this server".
const SelfID = "self"

// SelfConfig is how the server binary is told to reach its own box.
type SelfConfig struct {
	User     string
	Port     int    // 0 → 22
	KeyFile  string // identity file; the clean, non-interactive credential
	Password string // alternative to a key, e.g. from an env var
	UseAgent bool   // try ssh-agent first
	Label    string // display name; "" → "this server"
}

// ConnectSelf registers the local box as a host and connects to it without any
// prompt.
//
// The interactive connect flow (ConnectHost) asks for the host key and the
// password through the prompt bridge — meaningless at boot with no one
// watching. So the credential is built directly here, and the host key is
// accepted on first use: this is the box's own loopback sshd, where a
// man-in-the-middle already implies local root, at which point the game is
// over regardless.
func (a *App) ConnectSelf(ctx context.Context, cfg SelfConfig) error {
	if cfg.User == "" {
		return fmt.Errorf("app: self mode needs a user")
	}
	port := cfg.Port
	if port == 0 {
		port = 22
	}

	methods, authKinds, err := selfAuth(cfg)
	if err != nil {
		return err
	}

	label := cfg.Label
	if label == "" {
		label = "this server"
	}
	host := config.Host{
		ID:           SelfID,
		Name:         label,
		Hostname:     "127.0.0.1",
		Port:         port,
		User:         cfg.User,
		Auth:         authKinds,
		IdentityFile: cfg.KeyFile,
	}
	if err := a.hosts.Upsert(host); err != nil {
		return fmt.Errorf("app: register self host: %w", err)
	}

	// Accept-on-first-use for the loopback host key. A recorded mismatch is
	// still fatal (AcceptOnce only decides the *unknown* case), so a swapped
	// key on a box that already had one recorded is refused.
	kh, err := sshcore.NewKnownHosts(config.KnownHostsPath(a.configDir), &sshcore.AcceptOnce{})
	if err != nil {
		return fmt.Errorf("app: self known_hosts: %w", err)
	}

	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := a.mgr.Connect(dialCtx, sshcore.HostConfig{
		ID:              SelfID,
		Addr:            fmt.Sprintf("127.0.0.1:%d", port),
		User:            cfg.User,
		Auth:            methods,
		HostKeyCallback: kh.Callback(),
	}, a.log); err != nil {
		return fmt.Errorf("app: connect to this server (127.0.0.1:%d): %w", port, err)
	}
	a.selfMode = true
	return nil
}

// selfAuth builds the SSH auth methods for the self host without touching the
// interactive prompt bridge, plus the config.AuthMethod list for the host entry.
func selfAuth(cfg SelfConfig) ([]ssh.AuthMethod, []config.AuthMethod, error) {
	var (
		methods []ssh.AuthMethod
		kinds   []config.AuthMethod
	)
	if cfg.UseAgent {
		if am, err := sshcore.Agent(); err == nil {
			methods = append(methods, am)
			kinds = append(kinds, config.AuthAgent)
		}
	}
	if cfg.KeyFile != "" {
		// A passphrase-less key: the callback returns "" and is only consulted
		// if the key is encrypted, which a self-connect key must not be.
		am, err := sshcore.PublicKeyFile(cfg.KeyFile, func() (string, error) { return "", nil })
		if err != nil {
			return nil, nil, fmt.Errorf("app: self key %s: %w", cfg.KeyFile, err)
		}
		methods = append(methods, am)
		kinds = append(kinds, config.AuthKey)
	}
	if cfg.Password != "" {
		pw := cfg.Password
		methods = append(methods, sshcore.Password(func() (string, error) { return pw, nil }))
		kinds = append(kinds, config.AuthPassword)
	}
	if len(methods) == 0 {
		return nil, nil, fmt.Errorf("app: self mode needs a key, a password, or --self-agent")
	}
	return methods, kinds, nil
}
