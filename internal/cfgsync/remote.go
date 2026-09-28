package cfgsync

import (
	"fmt"
	"net/url"
	"strings"
)

// Making sense of whatever the user pasted.
//
// # Why this exists
//
// GitHub shows you three different spellings of one repository and never says
// which one anything wants:
//
//	https://github.com/me/litedeck-sync.git
//	git@github.com:me/litedeck-sync.git
//	gh repo clone me/litedeck-sync
//
// Asking somebody to know that a deploy key needs the second one, and to
// hand-convert the colon into a slash, is asking them to know how SSH URLs work
// in order to sync a host list. They paste what they have; this turns it into
// what go-git needs and shows them what it became, so a wrong guess here is
// visible rather than a connection failure three screens later.

// NormalizeRemote turns a pasted repository address into the URL LiteDeck will
// use, for the chosen authentication method.
//
// Key authentication needs an SSH URL; a token needs an HTTPS one. The same
// repository has both, and which one the user happened to copy says nothing
// about how they want to log in.
func NormalizeRemote(raw, authKind string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("cfgsync: the repository address is empty")
	}
	// A local path or file:// URL is a repository on this machine or a mounted
	// disk. Nothing to convert, and no authentication either.
	if strings.HasPrefix(s, "file://") || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") {
		return s, nil
	}

	host, path, err := splitRemote(s)
	if err != nil {
		return "", err
	}

	switch authKind {
	case AuthToken:
		// The `git@` from an SSH spelling is dropped rather than carried into the
		// HTTPS URL: a token authenticates as itself, and a username left in the
		// URL is the one place a credential ends up in git config and in every
		// error message (§6.6).
		if _, rest, ok := strings.Cut(host, "@"); ok {
			host = rest
		}
		return "https://" + host + "/" + path, nil
	case AuthDeployKey, AuthAgent:
		// The user on an SSH remote is the account git runs as on the far side.
		// For GitHub, GitLab and every other hosted forge that is literally
		// "git"; for somebody's own server it is whoever owns the bare
		// repository, and that only arrives if they typed it.
		user := "git"
		if u, rest, ok := strings.Cut(host, "@"); ok {
			user, host = u, rest
		}
		return "ssh://" + user + "@" + host + "/" + path, nil
	default:
		// No method chosen yet: keep the shape it arrived in, so the preview
		// shows something rather than guessing.
		if strings.Contains(s, "://") {
			return s, nil
		}
		return "https://" + host + "/" + path, nil
	}
}

// splitRemote pulls the host and the repository path out of any of the spellings.
func splitRemote(s string) (host, path string, err error) {
	switch {
	case strings.Contains(s, "://"):
		u, perr := url.Parse(s)
		if perr != nil || u.Host == "" {
			return "", "", fmt.Errorf("cfgsync: %q is not an address LiteDeck can use", s)
		}
		host = u.Host
		if u.User != nil {
			host = u.User.Username() + "@" + u.Host
		}
		path = strings.TrimPrefix(u.Path, "/")

	case strings.Contains(s, ":") && !strings.Contains(s, "/"):
		return "", "", fmt.Errorf("cfgsync: %q has no repository in it", s)

	case strings.Contains(s, ":"):
		// scp-style: git@github.com:me/repo.git — the colon is a separator, not
		// a port. This is the spelling GitHub's "SSH" button gives you and the
		// one go-git will not take.
		left, right, _ := strings.Cut(s, ":")
		host, path = left, right

	default:
		// github.com/me/repo, with no scheme at all.
		left, right, ok := strings.Cut(s, "/")
		if !ok {
			return "", "", fmt.Errorf("cfgsync: %q has no repository in it", s)
		}
		host, path = left, right
	}

	path = strings.Trim(path, "/")
	if path == "" {
		return "", "", fmt.Errorf("cfgsync: %q names a server but no repository", s)
	}
	return host, path, nil
}

// DeployKeysURL is the page where a deploy key is registered, when the address is
// a forge this knows.
//
// Returns "" for anything else — somebody's own server has no such page, and a
// made-up link is worse than none. GitLab's is under the repository's settings
// rather than a page of its own, which is exactly the sort of thing nobody should
// have to go and find while half-way through a setup.
func DeployKeysURL(raw string) string {
	host, path, err := splitRemote(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	if _, rest, ok := strings.Cut(host, "@"); ok {
		host = rest
	}
	repo := strings.TrimSuffix(path, ".git")
	switch host {
	case "github.com":
		return "https://github.com/" + repo + "/settings/keys"
	case "gitlab.com":
		return "https://gitlab.com/" + repo + "/-/settings/repository"
	default:
		return ""
	}
}

// RemoteKind is what was found at a remote, before anything is written to it.
const (
	// RemoteEmpty is a repository with no commits: what "create a new sync"
	// wants.
	RemoteEmpty = "empty"
	// RemoteSync already holds a LiteDeck sync: what "join" wants.
	RemoteSync = "sync"
	// RemoteOther holds somebody's work. LiteDeck does not write into it.
	RemoteOther = "other"
	// RemoteDenied was reached and refused the credentials.
	RemoteDenied = "denied"
	// RemoteUnreachable could not be reached at all.
	RemoteUnreachable = "unreachable"
)

// ClassifyRemoteError turns a connection failure into one of the kinds above.
//
// Matching on text, because every transport reports this differently and none of
// them with a sentinel. The distinction earns the ugliness: "the key is not
// registered yet" and "that address does not exist" are different problems with
// different next steps, and a setup screen that says "failed" for both is a setup
// screen people give up on.
func ClassifyRemoteError(err error) string {
	if err == nil {
		return RemoteEmpty
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "unable to authenticate"),
		strings.Contains(s, "permission denied"),
		strings.Contains(s, "authentication required"),
		strings.Contains(s, "authorization failed"),
		strings.Contains(s, "handshake failed"),
		strings.Contains(s, "403"),
		strings.Contains(s, "401"):
		return RemoteDenied
	default:
		return RemoteUnreachable
	}
}
