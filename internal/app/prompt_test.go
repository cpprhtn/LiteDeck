package app

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/secret"
	"github.com/cpprhtn/LiteDeck/internal/sshcore"
)

// recorder captures emitted events so a test can act as the frontend.
type recorder struct {
	mu       sync.Mutex
	hostKeys chan HostKeyPrompt
	secrets  chan SecretPrompt
	other    []string
}

func newRecorder() *recorder {
	return &recorder{
		hostKeys: make(chan HostKeyPrompt, 8),
		secrets:  make(chan SecretPrompt, 8),
	}
}

func (r *recorder) emit(event string, payload any) {
	switch p := payload.(type) {
	case HostKeyPrompt:
		r.hostKeys <- p
	case SecretPrompt:
		r.secrets <- p
	default:
		r.mu.Lock()
		r.other = append(r.other, event)
		r.mu.Unlock()
	}
}

// testApp wires an App with a fake frontend and an in-memory keychain.
func testApp(t *testing.T) (*App, *recorder) {
	t.Helper()
	a := New()
	rec := newRecorder()
	a.emit = rec.emit
	a.secrets = newMemSecrets()
	return a, rec
}

type memSecrets struct {
	mu sync.Mutex
	m  map[string]string
}

func newMemSecrets() *memSecrets { return &memSecrets{m: map[string]string{}} }

func (s *memSecrets) key(h string, k secret.Kind) string { return h + "/" + string(k) }

func (s *memSecrets) Get(h string, k secret.Kind) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[s.key(h, k)]
	if !ok {
		return "", secret.ErrNotFound
	}
	return v, nil
}

func (s *memSecrets) Set(h string, k secret.Kind, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[s.key(h, k)] = v
	return nil
}

func (s *memSecrets) Delete(h string, k secret.Kind) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, s.key(h, k))
	return nil
}

func (s *memSecrets) Available() bool { return true }

func TestHostKeyPromptRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		answer string
		want   sshcore.TrustDecision
	}{
		{"always", sshcore.TrustAlways},
		{"once", sshcore.TrustOnce},
		{"reject", sshcore.TrustReject},
	} {
		t.Run(tc.answer, func(t *testing.T) {
			a, rec := testApp(t)

			type result struct {
				d   sshcore.TrustDecision
				err error
			}
			done := make(chan result, 1)
			go func() {
				// This is the call sshcore makes from inside the handshake.
				d, err := a.prompts.confirmHostKey("h1", sshcore.KeyInfo{
					Address:     "10.0.0.5:22",
					Type:        "ssh-ed25519",
					Fingerprint: "SHA256:abc",
				})
				done <- result{d, err}
			}()

			var prompt HostKeyPrompt
			select {
			case prompt = <-rec.hostKeys:
			case <-time.After(2 * time.Second):
				t.Fatal("no prompt:hostkey event was emitted")
			}
			if prompt.Fingerprint != "SHA256:abc" || prompt.Address != "10.0.0.5:22" {
				t.Errorf("prompt = %+v", prompt)
			}

			if err := a.AnswerHostKey(prompt.ID, tc.answer); err != nil {
				t.Fatalf("AnswerHostKey: %v", err)
			}

			select {
			case got := <-done:
				if got.err != nil {
					t.Fatalf("confirmHostKey: %v", got.err)
				}
				if got.d != tc.want {
					t.Errorf("decision = %v, want %v", got.d, tc.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("handshake goroutine was never released")
			}
		})
	}
}

func TestHostKeyPromptRejectsUnknownAnswer(t *testing.T) {
	a, _ := testApp(t)
	if err := a.AnswerHostKey("1", "maybe"); err == nil {
		t.Error("an unrecognised decision was accepted")
	}
}

func TestAnswerAfterPromptIsGone(t *testing.T) {
	a, _ := testApp(t)
	if err := a.AnswerHostKey("999", "always"); err == nil {
		t.Error("answering a prompt nobody is waiting on succeeded")
	}
	if err := a.AnswerSecret("999", "x", false); err == nil {
		t.Error("answering a secret prompt nobody is waiting on succeeded")
	}
}

// TestSecretFromKeychainSkipsPrompt: a stored secret must not make the user
// retype it. If this regresses, every connection nags.
func TestSecretFromKeychainSkipsPrompt(t *testing.T) {
	a, rec := testApp(t)
	if err := a.secrets.Set("h1", secret.KindPassword, "stored"); err != nil {
		t.Fatal(err)
	}

	got, err := a.prompts.secretFunc("h1", secret.KindPassword, "password")()
	if err != nil {
		t.Fatalf("secretFunc: %v", err)
	}
	if got != "stored" {
		t.Errorf("got %q, want the stored secret", got)
	}
	select {
	case p := <-rec.secrets:
		t.Errorf("prompted despite a stored secret: %+v", p)
	default:
	}
}

func TestSecretPromptAndRemember(t *testing.T) {
	a, rec := testApp(t)

	done := make(chan string, 1)
	errs := make(chan error, 1)
	go func() {
		v, err := a.prompts.secretFunc("h1", secret.KindPassword, "prod 비밀번호")()
		if err != nil {
			errs <- err
			return
		}
		done <- v
	}()

	var prompt SecretPrompt
	select {
	case prompt = <-rec.secrets:
	case <-time.After(2 * time.Second):
		t.Fatal("no prompt:secret event was emitted")
	}
	if prompt.Kind != string(secret.KindPassword) || !prompt.CanRemember {
		t.Errorf("prompt = %+v", prompt)
	}

	if err := a.AnswerSecret(prompt.ID, "typed", true); err != nil {
		t.Fatalf("AnswerSecret: %v", err)
	}

	select {
	case v := <-done:
		if v != "typed" {
			t.Errorf("got %q", v)
		}
	case err := <-errs:
		t.Fatalf("secretFunc: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("caller was never released")
	}

	// remember == true must have persisted it.
	if v, err := a.secrets.Get("h1", secret.KindPassword); err != nil || v != "typed" {
		t.Errorf("secret not remembered: %q %v", v, err)
	}
}

func TestSecretPromptNotRememberedWhenDeclined(t *testing.T) {
	a, rec := testApp(t)
	go func() {
		_, _ = a.prompts.secretFunc("h1", secret.KindPassword, "pw")()
	}()

	p := <-rec.secrets
	if err := a.AnswerSecret(p.ID, "typed", false); err != nil {
		t.Fatal(err)
	}
	// Give the goroutine a moment to finish its store-if-remembered branch.
	time.Sleep(50 * time.Millisecond)
	if _, err := a.secrets.Get("h1", secret.KindPassword); !errors.Is(err, secret.ErrNotFound) {
		t.Error("secret was stored even though the user declined")
	}
}

func TestCancelPrompt(t *testing.T) {
	a, rec := testApp(t)

	errs := make(chan error, 1)
	go func() {
		_, err := a.prompts.secretFunc("h1", secret.KindPassword, "pw")()
		errs <- err
	}()

	p := <-rec.secrets
	if err := a.CancelPrompt(p.ID); err != nil {
		t.Fatalf("CancelPrompt: %v", err)
	}
	select {
	case err := <-errs:
		if !errors.Is(err, ErrPromptCancelled) {
			t.Errorf("err = %v, want ErrPromptCancelled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling did not release the caller")
	}
}

// TestKeyboardInteractiveAnswersEachPrompt covers the 2FA path (§7.3): the
// server can ask several questions in one exchange and every one needs an
// answer, in order.
func TestKeyboardInteractiveAnswersEachPrompt(t *testing.T) {
	a, rec := testApp(t)

	type result struct {
		answers []string
		err     error
	}
	done := make(chan result, 1)
	go func() {
		ans, err := a.prompts.challengeFunc("h1")(
			"", "2단계 인증",
			[]sshcore.Prompt{
				{Question: "Password:"},
				{Question: "Verification code:", Echo: true},
			},
		)
		done <- result{ans, err}
	}()

	for i, want := range []string{"pw", "123456"} {
		var p SecretPrompt
		select {
		case p = <-rec.secrets:
		case <-time.After(2 * time.Second):
			t.Fatalf("prompt %d never arrived", i)
		}
		if i == 0 && !strings.Contains(p.Label, "2단계 인증") {
			t.Errorf("instruction not shown on the first prompt: %q", p.Label)
		}
		if i == 1 && !p.Echo {
			t.Error("an echoing challenge was marked secret")
		}
		if err := a.AnswerSecret(p.ID, want, true); err != nil {
			t.Fatal(err)
		}
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("challenge: %v", r.err)
		}
		if len(r.answers) != 2 || r.answers[0] != "pw" || r.answers[1] != "123456" {
			t.Errorf("answers = %q", r.answers)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("challenge never completed")
	}

	// One-time codes must never be stored, whatever the user ticked.
	if _, err := a.secrets.Get("h1", "challenge"); !errors.Is(err, secret.ErrNotFound) {
		t.Error("a keyboard-interactive answer was persisted; OTPs are worthless the second time")
	}
}

// TestConcurrentPromptsDoNotCrossWires: two hosts connecting at once must not
// receive each other's answers.
func TestConcurrentPromptsDoNotCrossWires(t *testing.T) {
	a, rec := testApp(t)

	results := make(chan string, 2)
	for _, host := range []string{"hostA", "hostB"} {
		go func() {
			v, err := a.prompts.secretFunc(host, secret.KindPassword, host)()
			if err != nil {
				results <- "error: " + err.Error()
				return
			}
			results <- host + "=" + v
		}()
	}

	// Answer each prompt with a value derived from the host it belongs to.
	for range 2 {
		select {
		case p := <-rec.secrets:
			if err := a.AnswerSecret(p.ID, "pw-"+p.HostID, false); err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("expected two prompts")
		}
	}

	got := map[string]bool{}
	for range 2 {
		select {
		case r := <-results:
			got[r] = true
		case <-time.After(2 * time.Second):
			t.Fatal("a caller was never released")
		}
	}
	for _, want := range []string{"hostA=pw-hostA", "hostB=pw-hostB"} {
		if !got[want] {
			t.Errorf("missing %q; answers were crossed: %v", want, got)
		}
	}
}
