package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"

	"github.com/cpprhtn/LiteDeck/internal/cfgsync"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/cpprhtn/LiteDeck/internal/secret"
)

// Reaching the sync repository (§5.2).
//
// Three ways, and which ones are offered depends on the machine:
//
//   - a sync-only deploy key, generated here. Recommended, because it reaches one
//     repository and nothing else;
//   - the user's existing agent, for a bare repository on their own server. Not
//     offered on Windows, where the OpenSSH agent speaks over a named pipe that
//     this code does not use and Pageant is a different transport again — a choice
//     that cannot work is worse than one that is not there (§6.7 ⑥);
//   - HTTPS with a fine-grained token.
//
// # Where the private key goes
//
// The credential store, not a file. §5.2 offered either, and the file option only
// looks equivalent on Unix: on Windows Go's Chmod sets the read-only bit and
// nothing else, so a "0600" key file there is readable by every process the user
// runs (§6.7 ②). Where there is no credential store at all — a headless Rocky —
// the deploy-key option is refused and the user is pointed at their agent, which
// is the thing that machine does have.

// agentAvailableHere reports whether the ssh-agent option can work.
func agentAvailableHere() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	return os.Getenv("SSH_AUTH_SOCK") != ""
}

// openSyncBackend builds the git backend for the configured authentication.
func (a *App) openSyncBackend(cfg cfgsync.Config) (*cfgsync.GitBackend, error) {
	auth, err := a.syncAuth(cfg)
	if err != nil {
		return nil, err
	}
	store, err := a.openSyncStore()
	if err != nil {
		return nil, err
	}
	b, err := cfgsync.OpenGit(context.Background(), cfgsync.GitOptions{
		Dir:  store.RepoDir(),
		URL:  cfg.RemoteURL,
		Auth: auth,
	})
	if err != nil && !errors.Is(err, cfgsync.ErrEmptyRemote) {
		return nil, err
	}
	return b, nil
}

// syncAuth turns the configured method into something go-git can use.
func (a *App) syncAuth(cfg cfgsync.Config) (transport.AuthMethod, error) {
	switch cfg.AuthKind {
	case "":
		// A local path or file:// remote, which needs nothing. Also the shape the
		// integration tests use.
		return nil, nil

	case cfgsync.AuthDeployKey:
		key, err := a.syncPrivateKey()
		if err != nil {
			return nil, err
		}
		signer, err := ssh.ParsePrivateKey([]byte(key))
		if err != nil {
			return nil, fmt.Errorf("app: the sync key does not parse: %w", err)
		}
		return &gitssh.PublicKeys{User: "git", Signer: signer}, nil

	case cfgsync.AuthAgent:
		if !agentAvailableHere() {
			return nil, i18n.Errorf("이 기기에서는 ssh-agent 방식을 쓸 수 없습니다")
		}
		auth, err := gitssh.NewSSHAgentAuth("git")
		if err != nil {
			return nil, fmt.Errorf("app: ssh-agent: %w", err)
		}
		return auth, nil

	case cfgsync.AuthToken:
		token, err := a.syncToken()
		if err != nil {
			return nil, err
		}
		// The username is ignored by GitHub and GitLab for a token; what matters
		// is the password field. It is passed in memory and never written into
		// the remote URL, which is where it would end up in a log.
		return &githttp.BasicAuth{Username: "litedeck", Password: token}, nil

	default:
		return nil, i18n.Errorf("알 수 없는 인증 방식입니다: %s", cfg.AuthKind)
	}
}

// SyncGenerateKey makes a sync-only ed25519 key pair and returns the public half
// (§5.2).
//
// The private half goes to the credential store. What comes back is the one line
// the user pastes into their repository's deploy keys, with write access — which
// the UI says, because a read-only deploy key produces a sync that works until the
// first push.
func (a *App) SyncGenerateKey() (string, error) {
	if a.headless {
		return "", a.syncNotHere()
	}
	if a.secrets == nil || !a.secrets.Available() {
		return "", i18n.Errorf("이 기기에는 자격 증명 저장소가 없어 동기화 전용 키를 보관할 수 없습니다 — ssh-agent 나 토큰 방식을 쓰세요")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("app: generate sync key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "litedeck sync")
	if err != nil {
		return "", fmt.Errorf("app: encode sync key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("app: encode sync public key: %w", err)
	}
	if err := a.secrets.Set(secret.SyncAccount, secret.KindSyncKey,
		string(pem.EncodeToMemory(block))); err != nil {
		return "", fmt.Errorf("app: store sync key: %w", err)
	}
	return string(ssh.MarshalAuthorizedKey(sshPub)), nil
}

// SyncPublicKey returns the public half of the stored sync key, so the setup
// screen can show it again without generating a new one.
func (a *App) SyncPublicKey() (string, error) {
	if a.headless {
		return "", a.syncNotHere()
	}
	key, err := a.syncPrivateKey()
	if err != nil {
		return "", err
	}
	signer, err := ssh.ParsePrivateKey([]byte(key))
	if err != nil {
		return "", fmt.Errorf("app: the sync key does not parse: %w", err)
	}
	return string(ssh.MarshalAuthorizedKey(signer.PublicKey())), nil
}

// SyncSetToken stores an HTTPS token for the repository (§5.2).
//
// In the credential store, never in the remote URL: a token in the URL ends up in
// git config, in every error message, and in the screenshot somebody attaches to a
// bug report.
func (a *App) SyncSetToken(token string) error {
	if a.headless {
		return a.syncNotHere()
	}
	if token == "" {
		return i18n.Errorf("토큰이 비어 있습니다")
	}
	if a.secrets == nil || !a.secrets.Available() {
		return i18n.Errorf("이 기기에는 자격 증명 저장소가 없어 토큰을 보관할 수 없습니다")
	}
	return a.secrets.Set(secret.SyncAccount, secret.KindSyncKey, token)
}

func (a *App) syncPrivateKey() (string, error) {
	if a.secrets == nil {
		return "", i18n.Errorf("동기화 전용 키가 없습니다 — 설정에서 만드세요")
	}
	key, err := a.secrets.Get(secret.SyncAccount, secret.KindSyncKey)
	if err != nil || key == "" {
		return "", i18n.Errorf("동기화 전용 키가 없습니다 — 설정에서 만드세요")
	}
	return key, nil
}

func (a *App) syncToken() (string, error) {
	if a.secrets == nil {
		return "", i18n.Errorf("토큰이 저장되어 있지 않습니다")
	}
	token, err := a.secrets.Get(secret.SyncAccount, secret.KindSyncKey)
	if err != nil || token == "" {
		return "", i18n.Errorf("토큰이 저장되어 있지 않습니다")
	}
	return token, nil
}
