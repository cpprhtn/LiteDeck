package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/config"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
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

	// The bastion is a login of its own: its own fingerprint to accept and its
	// own password to type, both asked for before the target's. Reusing the
	// same two builders is what keeps it that way — a shortcut here would be a
	// bastion whose host key nobody checks (§7.1).
	jumpCfg, err := a.jumpConfig(h)
	if err != nil {
		return err
	}

	// The dial is bounded, but generously: the budget has to cover a human
	// reading a fingerprint and typing a password. Twice, through a bastion.
	budget := PromptTimeout + 30*time.Second
	if jumpCfg != nil {
		budget += PromptTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	err = a.mgr.Connect(ctx, sshcore.HostConfig{
		ID:              h.ID,
		Addr:            h.Addr(),
		User:            h.User,
		Auth:            auth,
		HostKeyCallback: known,
		Jump:            jumpCfg,
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
	// Addresses belong to the machine that just answered. The generation check
	// covers a reconnect on its own; this is so a host that stays disconnected
	// does not keep a listing nobody can verify.
	a.ifaces.forget(hostID)
	// The GPU feed rides a channel on the connection being dropped, and its
	// "this host has no card" is only true of the machine that just answered.
	a.gpus.forget(hostID)
	// Dropping the connection kills the sessions anyway; this is so the registry
	// does not go on offering them back as tabs that cannot be typed into.
	a.terminals.closeHost(hostID)
	return a.mgr.Disconnect(hostID)
}

// HostState reports one host's connection state.
func (a *App) HostState(hostID string) string {
	return a.mgr.State(hostID).String()
}

// jumpConfig builds the bastion leg, or nil when the host is dialled directly.
func (a *App) jumpConfig(h config.Host) (*sshcore.HostConfig, error) {
	jump, ok, err := h.JumpHost()
	if err != nil || !ok {
		return nil, err
	}
	auth, err := a.authMethods(jump)
	if err != nil {
		return nil, err
	}
	// Keyed on the bastion's own ID, so its password lands under its own entry
	// in the keychain rather than overwriting the target's.
	known, err := a.knownHosts(jump.ID)
	if err != nil {
		return nil, err
	}
	return &sshcore.HostConfig{
		ID:              jump.ID,
		Addr:            jump.Addr(),
		User:            jump.User,
		Auth:            auth,
		HostKeyCallback: known,
		// The bastion carries one channel — the forwarded connection — so it
		// needs none of the session budget the target does.
		MaxSessions:  1,
		MaxLongLived: 1,
	}, nil
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
					i18n.T("%s 의 패스프레이즈", h.IdentityFile)),
			)
			if err != nil {
				return nil, err
			}
			methods = append(methods, am)

		case config.AuthPassword:
			methods = append(methods, sshcore.Password(
				a.prompts.secretFunc(h.ID, secret.KindPassword,
					i18n.T("%s@%s 비밀번호", h.User, h.Hostname)),
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
		return i18n.Errorf("%s: 호스트 키를 신뢰하지 않아 연결을 중단했습니다", h.Label())
	case errors.Is(err, ErrPromptCancelled):
		return i18n.Errorf("%s: 사용자가 연결을 취소했습니다", h.Label())
	case strings.Contains(err.Error(), "administratively prohibited"):
		// What sshd says when AllowTcpForwarding is off. Verbatim it reads as a
		// permission problem with the account, and people go looking in the
		// wrong place — it is one line in the bastion's sshd_config.
		return i18n.Errorf(
			"%s: 경유 서버 %s 가 TCP 포워딩을 허용하지 않습니다 — 그 서버의 sshd_config 에서 AllowTcpForwarding 을 켜야 합니다",
			h.Label(), h.ProxyJump)
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

// SetLanguage records the user's UI language and applies it to the messages Go
// produces from here on (§8).
//
// An empty tag means "follow the OS", which is how somebody undoes a choice.
// The frontend resolves that, because the webview is the thing that knows the
// UI language the user actually sees.
// ApplyLanguage sets the language Go answers in, without recording a choice.
//
// The stored preference can be empty, meaning "follow the OS", and only the
// webview knows what that resolves to — `navigator.language` is the UI language
// the user actually sees, where the process environment on macOS is routinely
// unset. So the frontend resolves it and tells Go the answer at boot. That is a
// resolution rather than a preference: writing it to settings.json would freeze
// it, and somebody who never picked a language would carry English with them to
// a Korean machine.
func (a *App) ApplyLanguage(tag string) {
	i18n.SetLanguage(i18n.Parse(tag))
}

func (a *App) SetLanguage(tag string) ActionResult {
	i18n.SetLanguage(i18n.Parse(tag))
	if a.settings == nil {
		return okResult()
	}
	if err := a.settings.SetLanguage(tag); err != nil {
		return failResult(err)
	}
	return okResult()
}
