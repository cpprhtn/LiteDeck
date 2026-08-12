package sshcore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// The agent-exhausts-MaxAuthTries failure, against a real sshd.
//
// Reported on PR #1: with several keys in the agent the connection is dropped
// before the password method is reached, and the server's own wording says
// "too many authentication failures" — which reads as a locked-out account
// rather than "your agent spent the budget". The contributor's first move was
// to drop ssh-agent from the defaults, which is the wrong knob: it takes away
// the one path where the private key never enters this process.
//
// No agent is needed to reproduce it. Offering more public keys than the
// server's MaxAuthTries allows is the same thing an agent does on its own.

func bogusKeys(t *testing.T, n int) []ssh.AuthMethod {
	t.Helper()
	signers := make([]ssh.Signer, 0, n)
	for i := 0; i < n; i++ {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		s, err := ssh.NewSignerFromKey(priv)
		if err != nil {
			t.Fatal(err)
		}
		signers = append(signers, s)
	}
	// One method offering every key, which is exactly what an agent presents.
	return []ssh.AuthMethod{ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
		return signers, nil
	})}
}

func TestAuthFailureFromTooManyKeysSaysWhichKnobToTurn(t *testing.T) {
	if sshdSkip != "" {
		t.Skipf("sshd fixture unavailable: %s", sshdSkip)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Ten keys against a default MaxAuthTries of six: the server gives up
	// first. The password method is listed after them and never gets a turn,
	// which is the whole point.
	auth := append(bogusKeys(t, 10), ssh.Password(testPass))

	conn, err := Dial(ctx, HostConfig{
		ID:              "exhausted",
		Addr:            sshdAddr,
		User:            testUser,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         20 * time.Second,
	})
	if err == nil {
		conn.Close()
		t.Skip("this server let all ten keys through, so it has no MaxAuthTries to exceed")
	}

	got := err.Error()
	if !strings.Contains(strings.ToLower(got), "too many authentication failures") {
		t.Skipf("the server refused for another reason, so there is nothing to explain here: %v", err)
	}
	// The server's own words are kept — they are what a search engine matches —
	// and ours says where to go.
	if !strings.Contains(got, "ssh-agent") {
		t.Errorf("error = %q, want it to name ssh-agent as the cause", got)
	}
	if !strings.Contains(got, "호스트 편집기") {
		t.Errorf("error = %q, want it to point at the host editor", got)
	}
}

// A refusal for any other reason must pass through untouched: a wrong password
// that came back saying "check your ssh-agent" would be worse than the raw
// message.
func TestOtherAuthFailuresAreNotRewritten(t *testing.T) {
	if sshdSkip != "" {
		t.Skipf("sshd fixture unavailable: %s", sshdSkip)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := Dial(ctx, HostConfig{
		ID:              "wrongpw",
		Addr:            sshdAddr,
		User:            testUser,
		Auth:            []ssh.AuthMethod{ssh.Password("not-the-password")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         20 * time.Second,
	})
	if err == nil {
		conn.Close()
		t.Fatal("the wrong password was accepted")
	}
	if strings.Contains(err.Error(), "ssh-agent") {
		t.Errorf("a plain wrong password was explained as an agent problem: %v", err)
	}
}
