package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/config"
	"github.com/cpprhtn/LiteDeck/internal/secret"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
	"golang.org/x/crypto/ssh"
)

// Host management and the connect flow (§4.1).

// HostView is a host as the sidebar shows it: the stored definition plus its
// live connection state, so the frontend needs one call rather than two.
type HostView struct {
	config.Host
	State string `json:"state"`
}

// ListHosts returns every configured host with its current state.
func (a *App) ListHosts() []HostView {
	hosts := a.hosts.List()
	out := make([]HostView, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, HostView{Host: h, State: a.mgr.State(h.ID).String()})
	}
	return out
}

// SaveHost adds or updates a host.
func (a *App) SaveHost(h config.Host) error {
	if h.ID == "" {
		h.ID = fmt.Sprintf("host-%d", time.Now().UnixNano())
	}
	return a.hosts.Upsert(h)
}

// DeleteHost removes a host, disconnecting it first and forgetting its secrets.
func (a *App) DeleteHost(id string) error {
	_ = a.mgr.Disconnect(id)
	for _, k := range []secret.Kind{secret.KindPassword, secret.KindPassphrase, secret.KindSudo} {
		_ = a.secrets.Delete(id, k)
	}
	return a.hosts.Delete(id)
}

// ImportSSHConfigResult reports what an import found.
type ImportSSHConfigResult struct {
	Path     string `json:"path"`
	Imported int    `json:"imported"`
	Skipped  int    `json:"skipped"`
}

// ImportSSHConfig merges ~/.ssh/config into the host list (§4.1).
//
// Entries the user has already edited are left alone: a re-import must not
// silently undo their changes. Only rows this importer created are refreshed.
func (a *App) ImportSSHConfig() (ImportSSHConfigResult, error) {
	var res ImportSSHConfigResult

	path, err := config.DefaultSSHConfigPath()
	if err != nil {
		return res, err
	}
	res.Path = path

	imported, err := config.ImportSSHConfig(path)
	if err != nil {
		return res, err
	}

	for _, h := range imported {
		if existing, ok := a.hosts.Get(h.ID); ok && existing.Source != "ssh_config" {
			res.Skipped++
			continue
		}
		if err := a.hosts.Upsert(h); err != nil {
			res.Skipped++
			continue
		}
		res.Imported++
	}
	return res, nil
}

// ConnectHost opens a connection, prompting for whatever it needs on the way.
func (a *App) ConnectHost(hostID string) error {
	h, ok := a.hosts.Get(hostID)
	if !ok {
		return fmt.Errorf("app: no host with ID %q", hostID)
	}
	if a.mgr.State(hostID) == sshcore.StateConnected {
		return nil
	}

	auth, err := a.authMethods(h)
	if err != nil {
		return err
	}
	known, err := a.knownHosts(hostID)
	if err != nil {
		return err
	}

	// The dial is bounded, but generously: the budget has to cover a human
	// reading a fingerprint and typing a password.
	ctx, cancel := context.WithTimeout(context.Background(), PromptTimeout+30*time.Second)
	defer cancel()

	err = a.mgr.Connect(ctx, sshcore.HostConfig{
		ID:              h.ID,
		Addr:            h.Addr(),
		User:            h.User,
		Auth:            auth,
		HostKeyCallback: known,
	}, a.log)
	if err != nil {
		return a.explainConnectError(h, err)
	}
	return nil
}

// DisconnectHost closes a connection.
func (a *App) DisconnectHost(hostID string) error {
	// Detection is cached for the life of a connection. A server can be
	// upgraded between sessions — systemd replaced, Docker installed — so the
	// next connect must probe again rather than trust a stale answer.
	a.detected.forget(hostID)
	// The CPU baseline is meaningless across a reconnect: /proc/stat counters
	// reset on reboot, and a stale sample would draw a wild first reading.
	a.cpu.forget(hostID)
	// Dropping the connection kills the sessions anyway; this is so the registry
	// does not go on offering them back as tabs that cannot be typed into.
	a.terminals.closeHost(hostID)
	return a.mgr.Disconnect(hostID)
}

// HostState reports one host's connection state.
func (a *App) HostState(hostID string) string {
	return a.mgr.State(hostID).String()
}

// knownHosts builds the §7.1 verifier, wired to the GUI dialog.
func (a *App) knownHosts(hostID string) (ssh.HostKeyCallback, error) {
	dir := a.configDir
	if dir == "" {
		var err error
		if dir, err = config.Dir(); err != nil {
			return nil, err
		}
	}
	kh, err := sshcore.NewKnownHosts(
		config.KnownHostsPath(dir),
		hostKeyPrompter{bridge: a.prompts, hostID: hostID},
	)
	if err != nil {
		return nil, err
	}
	return kh.Callback(), nil
}

// authMethods turns a host's configured preferences into ssh.AuthMethods, in
// the order the user listed them.
func (a *App) authMethods(h config.Host) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	for _, m := range h.Auth {
		switch m {
		case config.AuthAgent:
			// A missing agent is not an error; it just means this method is
			// unavailable and the next one gets its turn.
			if am, err := sshcore.Agent(); err == nil {
				methods = append(methods, am)
			}

		case config.AuthKey:
			am, err := sshcore.PublicKeyFile(
				h.IdentityFile,
				a.prompts.secretFunc(h.ID, secret.KindPassphrase,
					fmt.Sprintf("%s 의 패스프레이즈", h.IdentityFile)),
			)
			if err != nil {
				return nil, err
			}
			methods = append(methods, am)

		case config.AuthPassword:
			methods = append(methods, sshcore.Password(
				a.prompts.secretFunc(h.ID, secret.KindPassword,
					fmt.Sprintf("%s@%s 비밀번호", h.User, h.Hostname)),
			))
		}
	}

	// Always offered last: the server decides whether to use it, and this is
	// the path OTP and 2FA arrive on (§7.3).
	methods = append(methods, sshcore.KeyboardInteractive(a.prompts.challengeFunc(h.ID)))

	if len(methods) == 0 {
		return nil, fmt.Errorf("app: host %q has no usable authentication method", h.Label())
	}
	return methods, nil
}

// explainConnectError turns a transport failure into something worth showing.
// §8 requires the underlying text to survive, so the original is always wrapped
// rather than replaced.
func (a *App) explainConnectError(h config.Host, err error) error {
	var mismatch *sshcore.HostKeyMismatchError
	switch {
	case errors.As(err, &mismatch):
		return err // already the full-screen warning case
	case errors.Is(err, sshcore.ErrHostKeyRejected):
		return fmt.Errorf("%s: 호스트 키를 신뢰하지 않아 연결을 중단했습니다", h.Label())
	case errors.Is(err, ErrPromptCancelled):
		return fmt.Errorf("%s: 사용자가 연결을 취소했습니다", h.Label())
	default:
		return fmt.Errorf("%s (%s): %w", h.Label(), h.Addr(), err)
	}
}

// A stored password can go stale — the user changes it on the server and the
// app keeps offering the old one. ForgetSecrets clears them so the next connect
// asks again.
func (a *App) ForgetSecrets(hostID string) error {
	var firstErr error
	for _, k := range []secret.Kind{secret.KindPassword, secret.KindPassphrase, secret.KindSudo} {
		if err := a.secrets.Delete(hostID, k); err != nil &&
			!errors.Is(err, secret.ErrNotFound) && !errors.Is(err, secret.ErrUnavailable) {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
