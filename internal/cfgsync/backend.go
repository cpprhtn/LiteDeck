package cfgsync

import (
	"context"
	"errors"
)

// Where the encrypted records are kept (§5.1, §7.2).
//
// An interface with one implementation, for one reason: the sync loop has no
// business knowing about git. Everything above this seam works in terms of
// "read every file" and "write these, delete those, on top of whatever the remote
// has now" — which is also the whole of what a directory over SFTP could do, if
// somebody wants that later.

// Backend is a remote store of ciphertext.
type Backend interface {
	// Pull brings the working copy to the remote's current state and returns the
	// commit it landed on. It never merges: what comes back is exactly what the
	// remote has (§8.2).
	Pull(ctx context.Context) (head string, err error)
	// ReadAll returns every file in the working copy, keyed by repository path
	// with "/" separators.
	ReadAll() (map[string][]byte, error)
	// CommitAndPush writes and deletes, commits on top of the current HEAD and
	// fast-forward pushes. It returns ErrRemoteAhead when somebody else got
	// there first, and the caller starts over from Pull (§8.2).
	CommitAndPush(ctx context.Context, writes map[string][]byte, deletes []string, msg string) error
	// Head is the commit the working copy is on, or "" before the first pull.
	// Read after a push, so the state file records what was actually applied
	// rather than what was there when the pass started.
	Head() string
	// Close releases whatever the backend holds open.
	Close() error
}

// ErrRemoteAhead reports that the remote moved while this sync was working.
//
// Not a failure: the expected outcome of two machines syncing at once. The caller
// pulls and tries again (§8.2), which is why this has to be distinguishable from
// a network error — retrying a network error immediately is how a sync loop turns
// into a denial of service against the user's own repository.
var ErrRemoteAhead = errors.New("cfgsync: the repository has new commits")

// ErrNotASyncRepository reports that the remote holds something else.
//
// The wizard stops here rather than committing into it. Somebody pointing this at
// the wrong URL should be told, not have LiteDeck add files to a repository they
// care about (§9.1).
var ErrNotASyncRepository = errors.New("cfgsync: the remote is not empty and is not a LiteDeck sync repository")

// ErrEmptyRemote reports a remote with no commits: the normal state of a
// repository somebody just created for this.
var ErrEmptyRemote = errors.New("cfgsync: the repository is empty")
