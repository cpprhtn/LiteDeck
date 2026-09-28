package cfgsync

import (
	"errors"
	"testing"
)

// Whatever GitHub showed them, LiteDeck takes.
//
// This is the step where the first setup used to fail: GitHub's "SSH" button
// gives you `git@github.com:me/repo.git`, go-git wants
// `ssh://git@github.com/me/repo.git`, and the difference is one character in the
// middle of a string nobody reads. The failure arrived as a connection error two
// screens later.
func TestNormalizeRemoteTakesEverySpellingOfOneRepository(t *testing.T) {
	const wantSSH = "ssh://git@github.com/me/litedeck-sync.git"
	for _, in := range []string{
		"git@github.com:me/litedeck-sync.git",
		"https://github.com/me/litedeck-sync.git",
		"github.com/me/litedeck-sync.git",
		"ssh://git@github.com/me/litedeck-sync.git",
		"  https://github.com/me/litedeck-sync.git  ",
	} {
		got, err := NormalizeRemote(in, AuthDeployKey)
		if err != nil {
			t.Errorf("NormalizeRemote(%q): %v", in, err)
			continue
		}
		if got != wantSSH {
			t.Errorf("NormalizeRemote(%q) = %q, want %q", in, got, wantSSH)
		}
	}

	// A token needs the HTTPS spelling of the same repository.
	for _, in := range []string{
		"git@github.com:me/litedeck-sync.git",
		"https://github.com/me/litedeck-sync.git",
	} {
		got, err := NormalizeRemote(in, AuthToken)
		if err != nil {
			t.Fatalf("NormalizeRemote(%q): %v", in, err)
		}
		if got != "https://github.com/me/litedeck-sync.git" {
			t.Errorf("NormalizeRemote(%q, token) = %q", in, got)
		}
	}
}

// Somebody's own server keeps the account they typed.
//
// `git` is right for every hosted forge and wrong for a bare repository in a
// person's home directory, which is the case the settings screen tells people
// they can use.
func TestNormalizeRemoteKeepsTheAccountOnAPrivateServer(t *testing.T) {
	got, err := NormalizeRemote("deploy@my-server:~/litedeck-sync.git", AuthDeployKey)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got != "ssh://deploy@my-server/~/litedeck-sync.git" {
		t.Errorf("= %q", got)
	}

	// A local path is left exactly as it is: there is nothing to log in to.
	for _, in := range []string{"file:///tmp/sync.git", "/srv/sync.git", "~/sync.git"} {
		got, err := NormalizeRemote(in, AuthDeployKey)
		if err != nil || got != in {
			t.Errorf("NormalizeRemote(%q) = %q, %v", in, got, err)
		}
	}
}

func TestNormalizeRemoteRefusesWhatItCannotUse(t *testing.T) {
	for _, in := range []string{"", "   ", "github.com", "git@github.com", "https://github.com"} {
		if got, err := NormalizeRemote(in, AuthDeployKey); err == nil {
			t.Errorf("NormalizeRemote(%q) = %q, want an error", in, got)
		}
	}
}

// The page where a deploy key goes, for the two forges most people mean.
//
// GitLab's is not a page of its own — it is a section of the repository settings
// — which is the sort of thing somebody would otherwise be hunting for in the
// middle of a setup they are half-way through.
func TestDeployKeysURL(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:me/litedeck-sync.git": "https://github.com/me/litedeck-sync/settings/keys",
		"https://github.com/me/litedeck-sync": "https://github.com/me/litedeck-sync/settings/keys",
		"git@gitlab.com:me/sync.git":          "https://gitlab.com/me/sync/-/settings/repository",
		"ssh://deploy@my-server/~/sync.git":   "",
		"file:///tmp/sync.git":                "",
		"not a url":                           "",
	} {
		if got := DeployKeysURL(in); got != want {
			t.Errorf("DeployKeysURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// "Not registered yet" and "no such address" are different problems.
//
// A setup screen that says "failed" to both is one people give up on: the first
// is fixed by finishing a step they are in the middle of, and the second by
// looking at what they typed.
func TestClassifyRemoteError(t *testing.T) {
	cases := map[string]string{
		"ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey]": RemoteDenied,
		"remote: Permission denied to deploy key":                                                RemoteDenied,
		"authentication required":                                                                RemoteDenied,
		"unexpected status 403":                                                                  RemoteDenied,
		"dial tcp: lookup nosuchhost: no such host":                                              RemoteUnreachable,
		"connection refused":                                                                     RemoteUnreachable,
	}
	for msg, want := range cases {
		if got := ClassifyRemoteError(errors.New(msg)); got != want {
			t.Errorf("ClassifyRemoteError(%q) = %q, want %q", msg, got, want)
		}
	}
	if got := ClassifyRemoteError(nil); got != RemoteEmpty {
		t.Errorf("no error classified as %q", got)
	}
}
