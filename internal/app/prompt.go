package app

import (
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/cpprhtn/LiteDeck/internal/secret"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// Bridging a synchronous callback to an asynchronous dialog.
//
// sshcore asks its questions — "do you trust this host key?", "what is the
// passphrase?" — from inside the SSH handshake, synchronously, because that is
// where the answer is needed. The GUI answers asynchronously: show a dialog,
// wait for a human, come back later.
//
// So the handshake goroutine emits an event and parks on a channel, and a
// binding call from the frontend wakes it. Every wait is bounded: a dialog the
// user closed, or an event the frontend never rendered, must not pin a
// connection attempt forever.

// PromptTimeout bounds how long a handshake waits for a human.
//
// Ninety seconds, and the number is not arbitrary: sshd's LoginGraceTime is two
// minutes by default, and both prompts this bounds — the host key and the
// password — are answered from inside the handshake. Waiting longer than the
// server does cannot help, because the server has already hung up.
//
// From OpenSSH 9.8 it is worse than a wasted wait. sshd now penalises the
// source address for exceeding the grace time — `persourcepenalties
// grace-exceeded:10 … min:15 max:600` — and while that penalty stands every
// connection from that address is dropped, not just the slow one. Measured on
// CentOS Stream 9 with OpenSSH 9.9: two dialogs left open past the grace time
// and the box refused this machine outright, ssh-keyscan included, with an
// error that says nothing about why.
//
// So the timeout gives up first. A dialog nobody answered in ninety seconds is
// one nobody is sitting in front of, and failing is better than being locked
// out of the server for the next ten minutes.
//
// This also bounds the sudo prompts, which are not inside a handshake. Ninety
// seconds is the right order there too — a password dialog left that long has
// been abandoned — so it stays one constant.
const PromptTimeout = 90 * time.Second

// ErrPromptCancelled reports that the user dismissed a dialog.
var ErrPromptCancelled = errors.New("app: cancelled by user")

// HostKeyPrompt is the payload of a prompt:hostkey event (§7.1).
type HostKeyPrompt struct {
	ID          string `json:"id"`
	HostID      string `json:"hostId"`
	Address     string `json:"address"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
}

// SecretPrompt is the payload of a prompt:secret event.
type SecretPrompt struct {
	ID     string `json:"id"`
	HostID string `json:"hostId"`
	Kind   string `json:"kind"`  // password, passphrase, sudo
	Label  string `json:"label"` // what to show above the field
	// SessionOnly marks a secret this app will not keep past the connection,
	// whatever the keychain could do. The dialog then explains that rather than
	// offering a checkbox that would be a lie — see askSessionSecret.
	SessionOnly bool `json:"sessionOnly,omitempty"`
	// CanRemember is false where no OS credential store exists, so the UI does
	// not offer to remember something it cannot keep (§6).
	CanRemember bool `json:"canRemember"`
	// Echo is true for challenges whose answer is not secret, such as an OTP
	// that the server asked to be displayed.
	Echo bool `json:"echo"`
}

// secretAnswer is what the frontend sends back.
type secretAnswer struct {
	value    string
	remember bool
	err      error
}

type promptBridge struct {
	app *App

	mu       sync.Mutex
	seq      int
	hostKeys map[string]chan sshcore.TrustDecision
	secrets  map[string]chan secretAnswer
	// pending keeps the payload of every prompt still waiting for an answer.
	//
	// The desktop never needed it: one window, and the event reaches it. A
	// browser can lose the WebSocket mid-prompt — a laptop lid, a proxy idle
	// timeout — and the reconnect used to bring back a page with no dialog on
	// it while Go went on waiting, so ConnectHost hung until the prompt timed
	// out two minutes later with nothing on screen to explain it.
	pendingKeys    map[string]HostKeyPrompt
	pendingSecrets map[string]SecretPrompt
}

// PendingPrompts is what a client asks for after reconnecting.
type PendingPrompts struct {
	HostKeys []HostKeyPrompt  `json:"hostKeys"`
	Secrets  []SecretPrompt   `json:"secrets"`
	Writes   []MCPWritePrompt `json:"writes"`
}

func newPromptBridge(a *App) *promptBridge {
	return &promptBridge{
		app:            a,
		hostKeys:       make(map[string]chan sshcore.TrustDecision),
		secrets:        make(map[string]chan secretAnswer),
		pendingKeys:    make(map[string]HostKeyPrompt),
		pendingSecrets: make(map[string]SecretPrompt),
	}
}

func (b *promptBridge) nextID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	return strconv.Itoa(b.seq)
}

// hostKeyPrompter adapts the bridge to sshcore.Prompter for one host.
type hostKeyPrompter struct {
	bridge *promptBridge
	hostID string
}

func (p hostKeyPrompter) ConfirmNewHost(k sshcore.KeyInfo) (sshcore.TrustDecision, error) {
	return p.bridge.confirmHostKey(p.hostID, k)
}

func (b *promptBridge) confirmHostKey(hostID string, k sshcore.KeyInfo) (sshcore.TrustDecision, error) {
	id := b.nextID()
	ch := make(chan sshcore.TrustDecision, 1)

	payload := HostKeyPrompt{
		ID:          id,
		HostID:      hostID,
		Address:     k.Address,
		KeyType:     k.Type,
		Fingerprint: k.Fingerprint,
	}
	b.mu.Lock()
	b.hostKeys[id] = ch
	b.pendingKeys[id] = payload
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.hostKeys, id)
		delete(b.pendingKeys, id)
		b.mu.Unlock()
	}()

	b.app.emit("prompt:hostkey", payload)

	select {
	case d := <-ch:
		return d, nil
	case <-time.After(PromptTimeout):
		// Refusing on timeout is the only safe default: §7.1 forbids trusting a
		// key nobody looked at.
		return sshcore.TrustReject, fmt.Errorf("app: host key confirmation timed out after %s", PromptTimeout)
	}
}

// AnswerHostKey delivers the user's decision. Called from the frontend.
func (a *App) AnswerHostKey(id, decision string) error {
	var d sshcore.TrustDecision
	switch decision {
	case "always":
		d = sshcore.TrustAlways
	case "once":
		d = sshcore.TrustOnce
	case "reject":
		d = sshcore.TrustReject
	default:
		return fmt.Errorf("app: unknown host key decision %q", decision)
	}

	a.prompts.mu.Lock()
	ch, ok := a.prompts.hostKeys[id]
	a.prompts.mu.Unlock()
	if !ok {
		return fmt.Errorf("app: host key prompt %q is no longer waiting", id)
	}
	select {
	case ch <- d:
		return nil
	default:
		return fmt.Errorf("app: host key prompt %q was already answered", id)
	}
}

// askSecret shows a secret dialog and blocks until it is answered.
// askSessionSecret asks for a secret this app will hold only until the
// connection ends.
//
// No keychain, in either direction: it is not read and it is not offered. The
// security tab's lock means "I typed this, for this session", and a checkbox
// that writes it to disk is a different promise wearing the same word. Anything
// that wants the durable behaviour uses secretFunc instead.
func (b *promptBridge) askSessionSecret(hostID string, kind secret.Kind, label string) (string, error) {
	value, _, err := b.ask(hostID, kind, label, false, true)
	return value, err
}

func (b *promptBridge) askSecret(hostID string, kind secret.Kind, label string, echo bool) (string, bool, error) {
	return b.ask(hostID, kind, label, echo, false)
}

func (b *promptBridge) ask(
	hostID string, kind secret.Kind, label string, echo, sessionOnly bool,
) (string, bool, error) {
	id := b.nextID()
	ch := make(chan secretAnswer, 1)

	payload := SecretPrompt{
		ID:          id,
		HostID:      hostID,
		Kind:        string(kind),
		Label:       label,
		SessionOnly: sessionOnly,
		CanRemember: !sessionOnly && b.app.secrets.Available(),
		Echo:        echo,
	}
	b.mu.Lock()
	b.secrets[id] = ch
	b.pendingSecrets[id] = payload
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.secrets, id)
		delete(b.pendingSecrets, id)
		b.mu.Unlock()
	}()

	b.app.emit("prompt:secret", payload)

	select {
	case ans := <-ch:
		if ans.err != nil {
			return "", false, ans.err
		}
		return ans.value, ans.remember, nil
	case <-time.After(PromptTimeout):
		return "", false, fmt.Errorf("app: %s prompt timed out after %s", kind, PromptTimeout)
	}
}

// AnswerSecret delivers a typed secret. Called from the frontend.
//
// The value crosses the Wails IPC boundary, which stays inside this process —
// but it must never be logged, and it is never echoed back.
func (a *App) AnswerSecret(id, value string, remember bool) error {
	return a.deliverSecret(id, secretAnswer{value: value, remember: remember})
}

// CancelPrompt dismisses a pending secret prompt.
func (a *App) CancelPrompt(id string) error {
	return a.deliverSecret(id, secretAnswer{err: ErrPromptCancelled})
}

func (a *App) deliverSecret(id string, ans secretAnswer) error {
	a.prompts.mu.Lock()
	ch, ok := a.prompts.secrets[id]
	a.prompts.mu.Unlock()
	if !ok {
		return fmt.Errorf("app: prompt %q is no longer waiting", id)
	}
	select {
	case ch <- ans:
		return nil
	default:
		return fmt.Errorf("app: prompt %q was already answered", id)
	}
}

// secretFunc builds the callback sshcore uses to fetch a secret: keychain
// first, dialog second, and remember the answer only if the user asked.
func (b *promptBridge) secretFunc(hostID string, kind secret.Kind, label string) func() (string, error) {
	return func() (string, error) {
		if v, err := b.app.secrets.Get(hostID, kind); err == nil {
			return v, nil
		}
		value, remember, err := b.askSecret(hostID, kind, label, false)
		if err != nil {
			return "", err
		}
		if remember {
			if err := b.app.secrets.Set(hostID, kind, value); err != nil {
				// Failing to save is not a reason to fail the connection; the
				// user just gets asked again next time.
				b.app.emit("log:warning", i18n.T("%s 저장 실패 (%s): %v", kind, hostID, err))
			}
		}
		return value, nil
	}
}

// challengeFunc handles keyboard-interactive exchanges — OTP and 2FA (§7.3).
// Answers are never stored: a one-time code is worthless the second time.
func (b *promptBridge) challengeFunc(hostID string) sshcore.Challenge {
	return func(_, instruction string, prompts []sshcore.Prompt) ([]string, error) {
		answers := make([]string, len(prompts))
		for i, p := range prompts {
			label := p.Question
			if instruction != "" && i == 0 {
				label = instruction + "\n" + label
			}
			v, _, err := b.askSecret(hostID, "challenge", label, p.Echo)
			if err != nil {
				return nil, err
			}
			answers[i] = v
		}
		return answers, nil
	}
}

// PendingPrompts returns every prompt still waiting for an answer.
//
// For a web client that reconnected: the events it missed while the socket was
// down are gone, and Go is still waiting. Without this the page came back with
// no dialog while ConnectHost sat there until the prompt timed out.
func (a *App) PendingPrompts() PendingPrompts {
	out := PendingPrompts{
		HostKeys: []HostKeyPrompt{},
		Secrets:  []SecretPrompt{},
		Writes:   []MCPWritePrompt{},
	}
	if a.prompts != nil {
		a.prompts.mu.Lock()
		for _, p := range a.prompts.pendingKeys {
			out.HostKeys = append(out.HostKeys, p)
		}
		for _, p := range a.prompts.pendingSecrets {
			out.Secrets = append(out.Secrets, p)
		}
		a.prompts.mu.Unlock()
	}
	if a.approvals != nil {
		a.approvals.mu.Lock()
		for _, p := range a.approvals.pending {
			out.Writes = append(out.Writes, p)
		}
		a.approvals.mu.Unlock()
	}
	return out
}
