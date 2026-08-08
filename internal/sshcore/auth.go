package sshcore

import (
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Authentication methods (§7.3). Every one of them takes its secret from a
// callback rather than a string field, so nothing here holds a password longer
// than the handshake that needs it.

// SecretFunc supplies a secret on demand. It is called at most once per
// connection attempt, and the caller is expected to wipe whatever backing
// storage it used once the attempt completes.
type SecretFunc func() (string, error)

// Prompt is one question from a keyboard-interactive exchange — the mechanism
// behind OTP and 2FA challenges. Echo reports whether the answer should be
// visible as it is typed.
type Prompt struct {
	Question string
	Echo     bool
}

// Challenge presents keyboard-interactive prompts to the user. Name and
// instruction come from the server and are shown verbatim.
type Challenge func(name, instruction string, prompts []Prompt) ([]string, error)

// ErrPassphraseRequired reports that a private key is encrypted and no
// passphrase callback was supplied. Callers surface this as a prompt rather
// than an error.
var ErrPassphraseRequired = errors.New("sshcore: private key is passphrase-protected")

// PublicKeyFile loads a private key from disk. If the key is encrypted,
// passphrase is called to unlock it; passing nil for an encrypted key returns
// ErrPassphraseRequired so the UI can ask and retry.
func PublicKeyFile(path string, passphrase SecretFunc) (ssh.AuthMethod, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sshcore: read private key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(pem)
	if err == nil {
		return ssh.PublicKeys(signer), nil
	}

	var needsPass *ssh.PassphraseMissingError
	if !errors.As(err, &needsPass) {
		return nil, fmt.Errorf("sshcore: parse private key %s: %w", path, err)
	}
	if passphrase == nil {
		return nil, fmt.Errorf("%w: %s", ErrPassphraseRequired, path)
	}

	pass, err := passphrase()
	if err != nil {
		return nil, fmt.Errorf("sshcore: passphrase for %s: %w", path, err)
	}
	signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(pass))
	if err != nil {
		return nil, fmt.Errorf("sshcore: decrypt private key %s: %w", path, err)
	}
	return ssh.PublicKeys(signer), nil
}

// Agent authenticates through a running ssh-agent, which is the preferred path
// because the private key never enters this process. On Windows the same
// SSH_AUTH_SOCK convention covers the OpenSSH agent; Pageant needs a separate
// transport and is not handled here yet.
func Agent() (ssh.AuthMethod, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("sshcore: no ssh-agent (SSH_AUTH_SOCK is unset)")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("sshcore: connect to ssh-agent: %w", err)
	}
	// The connection intentionally outlives this call: x/crypto/ssh queries the
	// agent during the handshake. It is closed when the process exits.
	return ssh.PublicKeysCallback(agent.NewClient(conn).Signers), nil
}

// Password authenticates with a password fetched on demand.
func Password(secret SecretFunc) ssh.AuthMethod {
	return ssh.PasswordCallback(func() (string, error) {
		if secret == nil {
			return "", errors.New("sshcore: no password source configured")
		}
		return secret()
	})
}

// KeyboardInteractive handles server-driven challenges (OTP, 2FA, PAM prompts).
func KeyboardInteractive(c Challenge) ssh.AuthMethod {
	return ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
		if c == nil {
			return nil, errors.New("sshcore: no challenge handler configured")
		}
		prompts := make([]Prompt, len(questions))
		for i, q := range questions {
			prompts[i] = Prompt{Question: q}
			if i < len(echos) {
				prompts[i].Echo = echos[i]
			}
		}
		answers, err := c(name, instruction, prompts)
		if err != nil {
			return nil, err
		}
		if len(answers) != len(questions) {
			return nil, fmt.Errorf("sshcore: challenge returned %d answers for %d questions", len(answers), len(questions))
		}
		return answers, nil
	})
}
