package cfgsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// The git backend (§5.1).
//
// # Why go-git and not the git command
//
// LiteDeck installs nothing and assumes nothing, which has to hold for the user's
// own machine as well as for their servers. Windows does not come with git, and
// "install git first" is a setup step for a feature whose selling point is that it
// needs no service.
//
// # Why it never merges
//
// Every sync is a new commit on top of whatever the remote has right now, pushed
// fast-forward. If somebody else committed in between, the push is refused and the
// whole sync starts again from a fresh pull (§8.2). git's own merge would be
// resolving ciphertext by line, which is not a thing: two changes to one host are
// two entirely different blobs. Conflicts are settled on decrypted records, by
// rules a person can read (§8.3).
//
// # The author line
//
// Fixed: `LiteDeck <litedeck@localhost>`, and the message is `sync: <device>`.
// The user's name and email are not put in, because the repository may be hosted
// somewhere they did not expect to publish them, and the message says nothing
// about which host changed — a commit log reading "renamed prod-web to
// customer-db" would undo the encryption for anybody who can see the repository
// (§4.2).

const (
	syncAuthorName  = "LiteDeck"
	syncAuthorEmail = "litedeck@localhost"
	// SyncBranch is the only branch. A sync with branches would be a sync with
	// merges.
	SyncBranch = "main"
)

// GitBackend is a Backend over a git remote.
type GitBackend struct {
	dir    string
	url    string
	auth   transport.AuthMethod
	repo   *git.Repository
	branch plumbing.ReferenceName
}

// GitOptions is what a backend needs to reach a remote.
type GitOptions struct {
	// Dir is the local working copy, under the app data directory. It holds
	// ciphertext and a .git — nothing readable.
	Dir string
	// URL is the remote. Shown in errors with any token removed (§6.6).
	URL string
	// Auth is nil for a file:// remote, which is what the integration tests use
	// and what a local bare repository on a mounted disk would be.
	Auth transport.AuthMethod
}

// OpenGit opens the working copy, cloning it if it is not there yet.
//
// An empty remote is ErrEmptyRemote rather than an error: it is the normal state
// of a repository the user just created, and the caller (the "new sync" wizard)
// goes on to create the vault in it.
func OpenGit(ctx context.Context, opts GitOptions) (*GitBackend, error) {
	if opts.Dir == "" || opts.URL == "" {
		return nil, errors.New("cfgsync: a git backend needs a directory and a URL")
	}
	b := &GitBackend{
		dir:    opts.Dir,
		url:    opts.URL,
		auth:   opts.Auth,
		branch: plumbing.NewBranchReferenceName(SyncBranch),
	}

	repo, err := git.PlainOpen(opts.Dir)
	switch {
	case err == nil:
		b.repo = repo
		// The URL can change — a repository moved, or the user switched from
		// HTTPS to SSH — and a working copy pointing at the old one would sync
		// silently to the wrong place.
		if err := b.setRemote(); err != nil {
			return nil, err
		}
		return b, nil
	case errors.Is(err, git.ErrRepositoryNotExists):
	default:
		return nil, fmt.Errorf("cfgsync: open %s: %w", b.dir, err)
	}

	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("cfgsync: create %s: %w", opts.Dir, err)
	}
	repo, err = git.PlainCloneContext(ctx, opts.Dir, false, &git.CloneOptions{
		URL:           opts.URL,
		Auth:          opts.Auth,
		ReferenceName: b.branch,
		SingleBranch:  true,
	})
	switch {
	case err == nil:
		b.repo = repo
		return b, nil
	case errors.Is(err, transport.ErrEmptyRemoteRepository) ||
		errors.Is(err, plumbing.ErrReferenceNotFound):
		// Nothing there yet. Start a working copy pointing at it, so the first
		// push has somewhere to come from.
		repo, initErr := b.initEmpty()
		if initErr != nil {
			return nil, initErr
		}
		b.repo = repo
		return b, ErrEmptyRemote
	default:
		return nil, fmt.Errorf("cfgsync: clone %s: %w", Redact(opts.URL), err)
	}
}

func (b *GitBackend) initEmpty() (*git.Repository, error) {
	repo, err := git.PlainInitWithOptions(b.dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: b.branch},
	})
	if err != nil && !errors.Is(err, git.ErrRepositoryAlreadyExists) {
		return nil, fmt.Errorf("cfgsync: init %s: %w", b.dir, err)
	}
	if repo == nil {
		if repo, err = git.PlainOpen(b.dir); err != nil {
			return nil, fmt.Errorf("cfgsync: open %s: %w", b.dir, err)
		}
	}
	b.repo = repo
	if err := b.setRemote(); err != nil {
		return nil, err
	}
	return repo, nil
}

// setRemote points origin at the configured URL, whatever it pointed at before.
func (b *GitBackend) setRemote() error {
	cfg, err := b.repo.Config()
	if err != nil {
		return fmt.Errorf("cfgsync: read git config: %w", err)
	}
	if r, ok := cfg.Remotes[git.DefaultRemoteName]; ok {
		if len(r.URLs) == 1 && r.URLs[0] == b.url {
			return nil
		}
		if err := b.repo.DeleteRemote(git.DefaultRemoteName); err != nil {
			return fmt.Errorf("cfgsync: replace remote: %w", err)
		}
	}
	if _, err := b.repo.CreateRemote(&config.RemoteConfig{
		Name: git.DefaultRemoteName,
		URLs: []string{b.url},
	}); err != nil {
		return fmt.Errorf("cfgsync: set remote: %w", err)
	}
	return nil
}

// Pull fetches and resets the working copy onto the remote branch.
//
// A reset and not a merge or a rebase. The working copy is a cache of the remote,
// nothing more: this app never has local commits worth preserving, because every
// commit it makes is pushed in the same breath. Anything in the working copy that
// the remote does not have is a leftover from a push that failed, and keeping it
// would mean pushing it again later without having merged it (§8.2).
func (b *GitBackend) Pull(ctx context.Context) (string, error) {
	err := b.repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: git.DefaultRemoteName,
		Auth:       b.auth,
		RefSpecs: []config.RefSpec{config.RefSpec(fmt.Sprintf(
			"+refs/heads/%s:refs/remotes/origin/%s", SyncBranch, SyncBranch))},
		Force: true,
	})
	switch {
	case err == nil, errors.Is(err, git.NoErrAlreadyUpToDate):
	case errors.Is(err, transport.ErrEmptyRemoteRepository):
		return "", ErrEmptyRemote
	default:
		return "", fmt.Errorf("cfgsync: fetch %s: %w", Redact(b.url), err)
	}

	remote, err := b.repo.Reference(plumbing.NewRemoteReferenceName(git.DefaultRemoteName, SyncBranch), true)
	if err != nil {
		// Fetched, but the branch is not there: an empty repository, or one whose
		// default branch is called something else. Both are "nothing to read".
		return "", ErrEmptyRemote
	}

	w, err := b.repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("cfgsync: worktree: %w", err)
	}
	if err := w.Reset(&git.ResetOptions{Commit: remote.Hash(), Mode: git.HardReset}); err != nil {
		return "", fmt.Errorf("cfgsync: reset to %s: %w", remote.Hash(), err)
	}
	// And move the local branch with it, so a commit lands on top of the remote
	// rather than on whatever this machine was sitting on.
	if err := b.repo.Storer.SetReference(plumbing.NewHashReference(b.branch, remote.Hash())); err != nil {
		return "", fmt.Errorf("cfgsync: move %s: %w", b.branch, err)
	}
	return remote.Hash().String(), nil
}

// ReadAll returns every tracked file in the working copy.
//
// Keyed by repository path with "/" separators, because that path goes into the
// associated data of every record (§6.7 ③). The .git directory is skipped, and so
// is anything unreadable — a file the caller cannot decrypt is one warning, not a
// reason to abandon the sync (§8.2).
func (b *GitBackend) ReadAll() (map[string][]byte, error) {
	w, err := b.repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("cfgsync: worktree: %w", err)
	}
	out := map[string][]byte{}
	if err := walk(w.Filesystem, ".", out); err != nil {
		return nil, err
	}
	return out, nil
}

func walk(fs billy.Filesystem, dir string, out map[string][]byte) error {
	entries, err := fs.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("cfgsync: read %s: %w", dir, err)
	}
	// Sorted, so two machines reading the same repository do the same work in the
	// same order. Nothing depends on it today; a future reader comparing logs
	// would.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		name := e.Name()
		if name == ".git" {
			continue
		}
		p := name
		if dir != "." {
			p = path.Join(dir, name)
		}
		if e.IsDir() {
			if err := walk(fs, p, out); err != nil {
				return err
			}
			continue
		}
		f, err := fs.Open(p)
		if err != nil {
			return fmt.Errorf("cfgsync: open %s: %w", p, err)
		}
		data, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("cfgsync: read %s: %w", p, err)
		}
		out[p] = data
	}
	return nil
}

// CommitAndPush writes the changes and pushes them (§8.2).
//
// Nothing to write and nothing to delete is not an error and not a commit: an
// empty commit every five minutes would be a repository whose history is mostly
// noise, and the history is what rev checking reads (§6.4).
func (b *GitBackend) CommitAndPush(ctx context.Context, writes map[string][]byte, deletes []string, msg string) error {
	if len(writes) == 0 && len(deletes) == 0 {
		return nil
	}
	w, err := b.repo.Worktree()
	if err != nil {
		return fmt.Errorf("cfgsync: worktree: %w", err)
	}

	// Sorted so one set of changes always produces the same tree walk, whichever
	// machine made them.
	for _, p := range sortedKeys(writes) {
		if err := writeFile(w.Filesystem, p, writes[p]); err != nil {
			return err
		}
		if _, err := w.Add(p); err != nil {
			return fmt.Errorf("cfgsync: stage %s: %w", p, err)
		}
	}
	for _, p := range deletes {
		if _, err := w.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("cfgsync: delete %s: %w", p, err)
		}
	}

	now := time.Now()
	if _, err := w.Commit(msg, &git.CommitOptions{
		Author:    &object.Signature{Name: syncAuthorName, Email: syncAuthorEmail, When: now},
		Committer: &object.Signature{Name: syncAuthorName, Email: syncAuthorEmail, When: now},
	}); err != nil {
		return fmt.Errorf("cfgsync: commit: %w", err)
	}

	err = b.repo.PushContext(ctx, &git.PushOptions{
		RemoteName: git.DefaultRemoteName,
		Auth:       b.auth,
		RefSpecs: []config.RefSpec{config.RefSpec(fmt.Sprintf(
			"refs/heads/%s:refs/heads/%s", SyncBranch, SyncBranch))},
	})
	switch {
	case err == nil, errors.Is(err, git.NoErrAlreadyUpToDate):
		return nil
	case isNonFastForward(err):
		// Somebody else pushed while this sync was working. Expected, and the
		// caller's answer is to pull and redo the merge — not to force anything.
		return ErrRemoteAhead
	default:
		return fmt.Errorf("cfgsync: push to %s: %w", Redact(b.url), err)
	}
}

// isNonFastForward recognises a rejected push.
//
// go-git reports this as a plain error string rather than a sentinel, so this has
// to match on text. Kept in one function with the reason written down, because a
// missed case here does not fail loudly: it looks like a network error, the sync
// gives up until the next trigger, and the two machines quietly stop converging.
func isNonFastForward(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, git.ErrNonFastForwardUpdate) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "non-fast-forward") ||
		strings.Contains(s, "fetch first") ||
		strings.Contains(s, "reference already exists")
}

func writeFile(fs billy.Filesystem, p string, data []byte) error {
	if dir := path.Dir(p); dir != "." && dir != "/" {
		if err := fs.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("cfgsync: create %s: %w", dir, err)
		}
	}
	f, err := fs.Create(p)
	if err != nil {
		return fmt.Errorf("cfgsync: write %s: %w", p, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("cfgsync: write %s: %w", p, err)
	}
	return f.Close()
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Close releases the working copy. There is nothing held open, but callers should
// not have to know that.
func (b *GitBackend) Close() error { return nil }

// Head returns the commit the working copy is on, or "" before the first pull.
func (b *GitBackend) Head() string {
	ref, err := b.repo.Head()
	if err != nil {
		return ""
	}
	return ref.Hash().String()
}

// Redact removes a token from a URL before it is shown or logged (§6.6).
//
// The URL itself is worth showing — "could not reach it" is useless without
// saying what "it" is — and `https://user:ghp_…@github.com/...` in a log or a
// screenshot is a credential published. LiteDeck's own error messages are the
// most likely place for that to happen, because they are the thing people paste
// into a bug report.
func Redact(url string) string {
	at := strings.LastIndex(url, "@")
	if at < 0 {
		return url
	}
	scheme := strings.Index(url, "://")
	if scheme < 0 || scheme+3 > at {
		return url
	}
	return url[:scheme+3] + "***@" + url[at+1:]
}

// IsSyncRepository reports whether a pulled working copy holds a LiteDeck sync
// repository, and refuses to adopt one that holds something else (§9.1).
//
// An empty repository is fine — that is what a fresh one looks like. A repository
// with files that are not this format is not: somebody typed the wrong URL, and
// committing into it would put LiteDeck's files in the middle of their work.
func IsSyncRepository(files map[string][]byte) error {
	if len(files) == 0 {
		return nil
	}
	if _, ok := files[VaultPath]; ok {
		return nil
	}
	names := make([]string, 0, len(files))
	for k := range files {
		names = append(names, k)
	}
	sort.Strings(names)
	if len(names) > 3 {
		names = append(names[:3], "…")
	}
	return fmt.Errorf("%w: it holds %s", ErrNotASyncRepository, strings.Join(names, ", "))
}
