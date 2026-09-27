package cfgsync

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// A bare repository on disk, used as the remote (§10.2).
//
// file:// is the same transport code path as ssh:// and https:// as far as
// everything above the Backend is concerned — what it exercises is the sequence:
// clone, read, commit, push, and what happens when two machines push at once. The
// SSH path gets its own integration test against a container (§10.2), because it
// is the authentication that differs, not the git.
func bareRemote(t *testing.T) string {
	t.Helper()
	// go-git's file transport runs git-upload-pack and git-receive-pack, which
	// come with git. Skipping is the repo's existing convention for a test that
	// needs something the machine may not have.
	if _, err := exec.LookPath("git-upload-pack"); err != nil {
		t.Skip("git-upload-pack is not installed; skipping the file:// backend test")
	}
	dir := t.TempDir()
	if _, err := git.PlainInit(dir, true); err != nil {
		t.Fatalf("init bare: %v", err)
	}
	return "file://" + dir
}

func openDevice(t *testing.T, url, name string) *GitBackend {
	t.Helper()
	b, err := OpenGit(context.Background(), GitOptions{
		Dir: filepath.Join(t.TempDir(), name), URL: url,
	})
	if err != nil && !errors.Is(err, ErrEmptyRemote) {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// One machine writes, another reads it back.
func TestGitBackendCarriesFilesBetweenTwoWorkingCopies(t *testing.T) {
	url := bareRemote(t)
	ctx := context.Background()

	a := openDevice(t, url, "a")
	if _, err := a.Pull(ctx); !errors.Is(err, ErrEmptyRemote) {
		t.Fatalf("pulling an empty repository gave %v", err)
	}

	writes := map[string][]byte{
		VaultPath:                  []byte(`{"format":"litedeck-sync"}`),
		AttributesPath:             []byte(GitAttributes),
		"hosts/" + testID + ".enc": []byte(`{"v":1,"nonce":"x","ct":"y"}`),
	}
	if err := a.CommitAndPush(ctx, writes, nil, "sync: device-a"); err != nil {
		t.Fatalf("first push: %v", err)
	}

	b := openDevice(t, url, "b")
	if _, err := b.Pull(ctx); err != nil {
		t.Fatalf("b pull: %v", err)
	}
	got, err := b.ReadAll()
	if err != nil {
		t.Fatalf("b read: %v", err)
	}
	for p, want := range writes {
		if string(got[p]) != string(want) {
			t.Errorf("%s = %q, want %q", p, got[p], want)
		}
	}
	if _, ok := got[".git"]; ok {
		t.Error("the .git directory was read as a record")
	}
	// The keys are repository paths with "/" — they become associated data, and a
	// separator that follows the OS would make a record unreadable elsewhere
	// (§6.7 ③).
	for p := range got {
		if strings.Contains(p, `\`) {
			t.Errorf("path %q has a backslash", p)
		}
	}

	// And a delete takes the file away for the next machine.
	if err := b.CommitAndPush(ctx, nil, []string{AttributesPath}, "sync: device-b"); err != nil {
		t.Fatalf("delete push: %v", err)
	}
	if _, err := a.Pull(ctx); err != nil {
		t.Fatalf("a pull: %v", err)
	}
	after, err := a.ReadAll()
	if err != nil {
		t.Fatalf("a read: %v", err)
	}
	if _, ok := after[AttributesPath]; ok {
		t.Error("the deleted file came back")
	}
}

// Two machines pushing from the same commit: the second is told to start over.
//
// This is the ordinary case, not the rare one — two machines on a five-minute
// timer will collide. What makes it safe is that the loser is refused rather than
// merged, so nothing is resolved by a tool that cannot read what it is resolving.
func TestASecondPushFromTheSameBaseIsRefused(t *testing.T) {
	url := bareRemote(t)
	ctx := context.Background()

	a := openDevice(t, url, "a")
	if err := a.CommitAndPush(ctx, map[string][]byte{VaultPath: []byte("{}")}, nil, "sync: a"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	b := openDevice(t, url, "b")
	if _, err := b.Pull(ctx); err != nil {
		t.Fatalf("b pull: %v", err)
	}
	// Both now sit on the same commit.
	if _, err := a.Pull(ctx); err != nil {
		t.Fatalf("a pull: %v", err)
	}

	if err := a.CommitAndPush(ctx, map[string][]byte{"hosts/" + testID + ".enc": []byte("a")}, nil, "sync: a"); err != nil {
		t.Fatalf("a push: %v", err)
	}
	err := b.CommitAndPush(ctx, map[string][]byte{"hosts/" + testID + ".enc": []byte("b")}, nil, "sync: b")
	if !errors.Is(err, ErrRemoteAhead) {
		t.Fatalf("b push gave %v, want ErrRemoteAhead — a sync that cannot tell this "+
			"apart from a network error stops converging", err)
	}

	// Starting over works, and A's write is still there.
	if _, err := b.Pull(ctx); err != nil {
		t.Fatalf("b re-pull: %v", err)
	}
	files, err := b.ReadAll()
	if err != nil {
		t.Fatalf("b read: %v", err)
	}
	if string(files["hosts/"+testID+".enc"]) != "a" {
		t.Errorf("after the refused push, b sees %q", files["hosts/"+testID+".enc"])
	}
	if err := b.CommitAndPush(ctx, map[string][]byte{"hosts/" + testID + ".enc": []byte("b")}, nil, "sync: b"); err != nil {
		t.Fatalf("b retry: %v", err)
	}
}

// A pull throws away whatever the working copy had.
//
// The working copy is a cache. A commit that failed to push has to disappear,
// because the sync will redo the merge from the remote state and push a new one —
// keeping the old one would push an unmerged change later.
func TestPullDiscardsAnUnpushedCommit(t *testing.T) {
	url := bareRemote(t)
	ctx := context.Background()

	a := openDevice(t, url, "a")
	if err := a.CommitAndPush(ctx, map[string][]byte{VaultPath: []byte("{}")}, nil, "sync: a"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	b := openDevice(t, url, "b")
	if _, err := b.Pull(ctx); err != nil {
		t.Fatalf("b pull: %v", err)
	}

	// A moves on; B's push will be refused, leaving a local commit behind.
	if _, err := a.Pull(ctx); err != nil {
		t.Fatalf("a pull: %v", err)
	}
	if err := a.CommitAndPush(ctx, map[string][]byte{"hosts/" + testID + ".enc": []byte("a")}, nil, "sync: a"); err != nil {
		t.Fatalf("a push: %v", err)
	}
	if err := b.CommitAndPush(ctx, map[string][]byte{"hosts/" + testID + ".enc": []byte("b")}, nil, "sync: b"); !errors.Is(err, ErrRemoteAhead) {
		t.Fatalf("b push gave %v", err)
	}

	head, err := b.Pull(ctx)
	if err != nil {
		t.Fatalf("b pull: %v", err)
	}
	if head != a.Head() {
		t.Errorf("after the pull b is on %s, a is on %s", head, a.Head())
	}
	files, err := b.ReadAll()
	if err != nil {
		t.Fatalf("b read: %v", err)
	}
	if string(files["hosts/"+testID+".enc"]) != "a" {
		t.Errorf("b's own unpushed write survived the pull: %q", files["hosts/"+testID+".enc"])
	}
}

// Nothing to write is not a commit.
//
// Five minutes apart forever, an empty commit each time, and the history that rev
// checking reads becomes almost entirely noise.
func TestNothingToWriteMakesNoCommit(t *testing.T) {
	url := bareRemote(t)
	ctx := context.Background()
	a := openDevice(t, url, "a")
	if err := a.CommitAndPush(ctx, map[string][]byte{VaultPath: []byte("{}")}, nil, "sync: a"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before := a.Head()
	if err := a.CommitAndPush(ctx, nil, nil, "sync: a"); err != nil {
		t.Fatalf("empty push: %v", err)
	}
	if a.Head() != before {
		t.Error("an empty sync made a commit")
	}
}

// A repository holding somebody else's work is not adopted (§9.1).
func TestARepositoryWithOtherContentIsRefused(t *testing.T) {
	if err := IsSyncRepository(nil); err != nil {
		t.Errorf("an empty repository was refused: %v", err)
	}
	if err := IsSyncRepository(map[string][]byte{VaultPath: []byte("{}")}); err != nil {
		t.Errorf("a sync repository was refused: %v", err)
	}
	err := IsSyncRepository(map[string][]byte{
		"README.md": []byte("# my dissertation"), "chapter1.tex": nil, "chapter2.tex": nil, "refs.bib": nil,
	})
	if !errors.Is(err, ErrNotASyncRepository) {
		t.Fatalf("somebody else's repository was accepted: %v", err)
	}
	if !strings.Contains(err.Error(), "chapter1.tex") {
		t.Errorf("the refusal does not say what it found: %v", err)
	}
}

// A token in the remote URL does not reach an error message (§6.6).
func TestRedactHidesTheTokenAndNothingElse(t *testing.T) {
	for in, want := range map[string]string{
		"https://user:ghp_secret@github.com/me/litedeck-sync.git": "https://***@github.com/me/litedeck-sync.git",
		"https://ghp_secret@github.com/me/sync.git":               "https://***@github.com/me/sync.git",
		"ssh://git@github.com/me/sync.git":                        "ssh://***@github.com/me/sync.git",
		"https://github.com/me/sync.git":                          "https://github.com/me/sync.git",
		"file:///tmp/x":                                           "file:///tmp/x",
		"/home/me/sync.git":                                       "/home/me/sync.git",
	} {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
	// The point of the exercise: no secret survives.
	if strings.Contains(Redact("https://user:ghp_secret@github.com/me/sync.git"), "ghp_secret") {
		t.Error("the token is still in the redacted URL")
	}
}

// The commit does not say what changed, and is not signed with the user's name
// (§4.2).
//
// A commit log reading "renamed prod-web" would undo the encryption for anybody
// who can see the repository, and the author line would publish the user's email
// on a host they may not have thought of as public.
func TestCommitsSayNothingAboutTheHosts(t *testing.T) {
	url := bareRemote(t)
	ctx := context.Background()
	a := openDevice(t, url, "a")
	if err := a.CommitAndPush(ctx, map[string][]byte{VaultPath: []byte("{}")}, nil, "sync: 3f2a0c1e"); err != nil {
		t.Fatalf("push: %v", err)
	}

	repo, err := git.PlainOpen(strings.TrimPrefix(url, "file://"))
	if err != nil {
		t.Fatalf("open bare: %v", err)
	}
	// By the branch, not by HEAD: a bare repository created by go-git still has
	// HEAD on master, and this app only ever pushes main.
	head, err := repo.Reference(plumbing.NewBranchReferenceName(SyncBranch), true)
	if err != nil {
		t.Fatalf("read %s: %v", SyncBranch, err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	// The literals, not the constants: comparing the commit against the same
	// constant the commit was made from proves nothing, and the thing worth
	// pinning is that neither is the user's own name or address.
	if commit.Author.Name != "LiteDeck" || commit.Author.Email != "litedeck@localhost" {
		t.Errorf("author = %s <%s> — the user's name and email must not be published "+
			"to whoever hosts the repository", commit.Author.Name, commit.Author.Email)
	}
	if commit.Committer.Name != "LiteDeck" || commit.Committer.Email != "litedeck@localhost" {
		t.Errorf("committer = %s <%s>", commit.Committer.Name, commit.Committer.Email)
	}
	if !strings.HasPrefix(commit.Message, "sync: ") {
		t.Errorf("message = %q", commit.Message)
	}
	if strings.Contains(commit.Message, "prod") || strings.Contains(commit.Message, "hosts/") {
		t.Errorf("the message describes the change: %q", commit.Message)
	}
}
