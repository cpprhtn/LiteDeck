package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/pkg/sftp"
	wr "github.com/wailsapp/wails/v2/pkg/runtime"
)

// The transfer queue (§4.2): uploads and downloads with progress and cancel.
//
// Transfers are the one place where the user waits on the network rather than
// on a command, so the design goal is that they never block anything else — the
// UI stays live, other views keep polling, and a stuck transfer can be
// abandoned without touching the connection.

// maxConcurrentTransfers bounds how many run at once.
//
// Two, not more: every transfer shares the host's single SFTP channel, and
// running many at once mostly interleaves them into each other's latency
// without moving more bytes. It also leaves session headroom (see the
// MaxSessions note in 01-transport).
const maxConcurrentTransfers = 2

// progressInterval throttles progress events. At 60fps the UI cannot use more,
// and a fast local transfer would otherwise flood the event bus.
const progressInterval = 120 * time.Millisecond

// TransferStatus values.
const (
	TransferQueued    = "queued"
	TransferRunning   = "running"
	TransferDone      = "done"
	TransferFailed    = "failed"
	TransferCancelled = "cancelled"
)

// Transfer is one file, or one directory tree, moving in one direction.
type Transfer struct {
	ID        string `json:"id"`
	HostID    string `json:"hostId"`
	Direction string `json:"direction"` // upload | download
	Local     string `json:"local"`
	Remote    string `json:"remote"`
	Size      int64  `json:"size"`
	Done      int64  `json:"done"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	StartedAt int64  `json:"startedAt"`

	// Directory transfers are one queue entry covering many files, because a
	// thousand rows for a thousand files is not a progress display.
	Dir        bool   `json:"dir,omitempty"`
	Files      int    `json:"files,omitempty"`
	FilesDone  int    `json:"filesDone,omitempty"`
	CurrentRel string `json:"currentRel,omitempty"`
}

type transferJob struct {
	Transfer
	cancel context.CancelFunc
	done   atomic.Int64
}

func (j *transferJob) snapshot() Transfer {
	t := j.Transfer
	t.Done = j.done.Load()
	return t
}

type transferQueue struct {
	app  *App
	sem  chan struct{}
	mu   sync.Mutex
	seq  int
	jobs map[string]*transferJob
	// order preserves insertion order for the panel; the map alone would show
	// transfers jumping around on every render.
	order []string
}

func newTransferQueue(a *App) *transferQueue {
	return &transferQueue{
		app:  a,
		sem:  make(chan struct{}, maxConcurrentTransfers),
		jobs: make(map[string]*transferJob),
	}
}

func (q *transferQueue) add(t Transfer) *transferJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seq++
	t.ID = fmt.Sprintf("t%d", q.seq)
	t.Status = TransferQueued
	t.StartedAt = time.Now().Unix()
	j := &transferJob{Transfer: t}
	q.jobs[t.ID] = j
	q.order = append(q.order, t.ID)
	return j
}

func (q *transferQueue) get(id string) (*transferJob, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	j, ok := q.jobs[id]
	return j, ok
}

func (q *transferQueue) list() []Transfer {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Transfer, 0, len(q.order))
	for _, id := range q.order {
		if j, ok := q.jobs[id]; ok {
			out = append(out, j.snapshot())
		}
	}
	return out
}

// clearFinished drops completed rows so a long session does not accumulate
// them without bound.
func (q *transferQueue) clearFinished() {
	q.mu.Lock()
	defer q.mu.Unlock()
	kept := q.order[:0]
	for _, id := range q.order {
		j := q.jobs[id]
		if j == nil {
			continue
		}
		if j.Status == TransferQueued || j.Status == TransferRunning {
			kept = append(kept, id)
			continue
		}
		delete(q.jobs, id)
	}
	q.order = kept
}

func (q *transferQueue) setStatus(j *transferJob, status string, err error) {
	q.mu.Lock()
	j.Status = status
	if err != nil {
		j.Error = err.Error()
	}
	q.mu.Unlock()
	q.emit(j)
}

func (q *transferQueue) emit(j *transferJob) {
	q.app.emit("transfer:progress", j.snapshot())
}

// run executes one transfer, respecting the concurrency limit.
func (q *transferQueue) run(j *transferJob, body func(ctx context.Context, j *transferJob) error) {
	ctx, cancel := context.WithCancel(context.Background())
	q.mu.Lock()
	j.cancel = cancel
	q.mu.Unlock()

	go func() {
		defer cancel()

		select {
		case q.sem <- struct{}{}:
			defer func() { <-q.sem }()
		case <-ctx.Done():
			q.setStatus(j, TransferCancelled, nil)
			return
		}

		q.setStatus(j, TransferRunning, nil)
		err := body(ctx, j)

		switch {
		case err == nil:
			q.setStatus(j, TransferDone, nil)
		case errors.Is(err, context.Canceled):
			q.setStatus(j, TransferCancelled, nil)
		default:
			q.setStatus(j, TransferFailed, err)
		}
	}()
}

// progressWriter counts bytes and emits throttled progress events.
type progressWriter struct {
	q    *transferQueue
	j    *transferJob
	ctx  context.Context
	last time.Time
}

func (w *progressWriter) Write(p []byte) (int, error) {
	// Checking here rather than only between files is what makes cancelling a
	// single large transfer responsive.
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n := len(p)
	w.j.done.Add(int64(n))
	if time.Since(w.last) >= progressInterval {
		w.last = time.Now()
		w.q.emit(w.j)
	}
	return n, nil
}

// StartUpload queues local files for upload into remoteDir (§4.2).
func (a *App) StartUpload(hostID string, localPaths []string, remoteDir string) ([]string, error) {
	dir, err := CleanRemotePath(remoteDir)
	if err != nil {
		return nil, err
	}
	if _, err := a.mgr.SFTP(hostID); err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(localPaths))
	for _, local := range localPaths {
		fi, err := os.Stat(local)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", local, err)
		}
		if fi.IsDir() {
			j := a.transfers.add(Transfer{
				HostID:    hostID,
				Direction: "upload",
				Local:     local,
				Remote:    path.Join(dir, filepath.Base(local)),
				Dir:       true,
			})
			ids = append(ids, j.ID)
			a.transfers.run(j, func(ctx context.Context, job *transferJob) error {
				files, total, err := walkLocalDir(ctx, job.Local)
				if err != nil {
					return err
				}
				a.transfers.setTotals(job, len(files), total)
				return a.uploadDir(ctx, job, files)
			})
			continue
		}

		j := a.transfers.add(Transfer{
			HostID:    hostID,
			Direction: "upload",
			Local:     local,
			Remote:    path.Join(dir, filepath.Base(local)),
			Size:      fi.Size(),
		})
		ids = append(ids, j.ID)
		a.transfers.run(j, a.uploadOne)
	}
	return ids, nil
}

func (a *App) uploadOne(ctx context.Context, j *transferJob) error {
	client, err := a.mgr.SFTP(j.HostID)
	if err != nil {
		return err
	}
	src, err := os.Open(j.Local)
	if err != nil {
		return err
	}
	defer src.Close()

	// Write to a temporary name and rename on success, so an interrupted
	// upload never leaves a half-written file wearing the real name.
	tmp := j.Remote + ".litedeck-partial"
	dst, err := client.Create(tmp)
	if err != nil {
		return err
	}

	pw := &progressWriter{q: a.transfers, j: j, ctx: ctx}
	_, copyErr := io.Copy(io.MultiWriter(dst, pw), src)
	closeErr := dst.Close()

	if copyErr != nil {
		_ = client.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = client.Remove(tmp)
		return closeErr
	}
	if err := client.PosixRename(tmp, j.Remote); err != nil {
		if err2 := client.Rename(tmp, j.Remote); err2 != nil {
			_ = client.Remove(tmp)
			return err2
		}
	}
	a.transfers.emit(j)
	return nil
}

// StartDownload queues remote files for download into localDir (§4.2).
func (a *App) StartDownload(hostID string, remotePaths []string, localDir string) ([]string, error) {
	if localDir == "" {
		return nil, errors.New("app: no local directory chosen")
	}
	client, err := a.mgr.SFTP(hostID)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(remotePaths))
	for _, remote := range remotePaths {
		cleaned, err := CleanRemotePath(remote)
		if err != nil {
			return nil, err
		}
		fi, err := client.Stat(cleaned)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", cleaned, err)
		}
		if fi.IsDir() {
			j := a.transfers.add(Transfer{
				HostID:    hostID,
				Direction: "download",
				Local:     filepath.Join(localDir, path.Base(cleaned)),
				Remote:    cleaned,
				Dir:       true,
			})
			ids = append(ids, j.ID)
			a.transfers.run(j, func(ctx context.Context, job *transferJob) error {
				files, total, err := walkRemoteDir(ctx, client, job.Remote)
				if err != nil {
					return err
				}
				a.transfers.setTotals(job, len(files), total)
				return a.downloadDir(ctx, job, files)
			})
			continue
		}

		j := a.transfers.add(Transfer{
			HostID:    hostID,
			Direction: "download",
			Local:     filepath.Join(localDir, path.Base(cleaned)),
			Remote:    cleaned,
			Size:      fi.Size(),
		})
		ids = append(ids, j.ID)
		a.transfers.run(j, a.downloadOne)
	}
	return ids, nil
}

func (a *App) downloadOne(ctx context.Context, j *transferJob) error {
	client, err := a.mgr.SFTP(j.HostID)
	if err != nil {
		return err
	}
	src, err := client.Open(j.Remote)
	if err != nil {
		return err
	}
	defer src.Close()

	tmp := j.Local + ".litedeck-partial"
	dst, err := os.Create(tmp)
	if err != nil {
		return err
	}

	pw := &progressWriter{q: a.transfers, j: j, ctx: ctx}
	_, copyErr := io.Copy(io.MultiWriter(dst, pw), src)
	closeErr := dst.Close()

	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if err := os.Rename(tmp, j.Local); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	a.transfers.emit(j)
	return nil
}

// CancelTransfer stops a running or queued transfer.
func (a *App) CancelTransfer(id string) error {
	j, ok := a.transfers.get(id)
	if !ok {
		return fmt.Errorf("app: no transfer %q", id)
	}
	a.transfers.mu.Lock()
	cancel := j.cancel
	a.transfers.mu.Unlock()
	if cancel == nil {
		return fmt.Errorf("app: transfer %q has already finished", id)
	}
	cancel()
	return nil
}

// Transfers returns the queue for the panel.
func (a *App) Transfers() []Transfer { return a.transfers.list() }

// ClearFinishedTransfers removes completed rows from the panel.
func (a *App) ClearFinishedTransfers() { a.transfers.clearFinished() }

// PickLocalFiles opens the OS file chooser for uploading.
func (a *App) PickLocalFiles() ([]string, error) {
	if a.ctx == nil {
		return nil, errors.New("app: no window")
	}
	return wr.OpenMultipleFilesDialog(a.ctx, wr.OpenDialogOptions{
		Title: i18n.S("업로드할 파일 선택"),
	})
}

// PickLocalUploadDir opens the OS directory chooser for uploading a whole
// folder. Separate from PickLocalFiles because no OS file chooser will select
// files and folders in the same pass — asking for one or the other is the only
// way to offer both.
func (a *App) PickLocalUploadDir() (string, error) {
	if a.ctx == nil {
		return "", errors.New("app: no window")
	}
	return wr.OpenDirectoryDialog(a.ctx, wr.OpenDialogOptions{
		Title: i18n.S("업로드할 폴더 선택"),
	})
}

// PickLocalDir opens the OS directory chooser for downloading into.
func (a *App) PickLocalDir() (string, error) {
	if a.ctx == nil {
		return "", errors.New("app: no window")
	}
	return wr.OpenDirectoryDialog(a.ctx, wr.OpenDialogOptions{
		Title: i18n.S("저장할 위치 선택"),
	})
}

// Recursive directory transfer (v1.x).
//
// The tree is walked in full before a single byte moves, so the progress bar
// has a real denominator — one that grows as it goes tells the user nothing
// about how much longer they are waiting.
//
// The walk happens inside the job rather than before it is queued. On the
// remote side it is readdir after readdir over the server's sftp-server, and a
// deep tree is a lot of them; running it in the job means it holds a transfer
// slot like everything else, the Cancel button stops it, and the window is not
// waiting on it. Nothing but SFTP is used — no find, no tar, no process on the
// server at all.

// maxWalkFiles bounds a directory transfer. Someone who drags their home
// directory by accident should get a refusal, not a machine that spends an hour
// discovering how big the mistake was.
const maxWalkFiles = 20000

// relFile is one file inside a tree, with its path relative to the tree root.
type relFile struct {
	Rel  string // always forward-slashed, both directions
	Size int64
}

func walkLocalDir(ctx context.Context, root string) ([]relFile, int64, error) {
	var files []relFile
	var total int64

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Symlinks are skipped rather than followed: following one that points
		// outside the tree would silently copy something the user never
		// selected, and a cycle would never terminate.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if len(files) >= maxWalkFiles {
			return i18n.Errorf("디렉터리에 파일이 %d개를 넘습니다 — 더 좁은 범위를 선택하세요", maxWalkFiles)
		}
		files = append(files, relFile{Rel: filepath.ToSlash(rel), Size: info.Size()})
		total += info.Size()
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", root, err)
	}
	return files, total, nil
}

func walkRemoteDir(ctx context.Context, client *sftp.Client, root string) ([]relFile, int64, error) {
	var files []relFile
	var total int64

	walker := client.Walk(root)
	for walker.Step() {
		// Checked every step, not only between files: this is the part that
		// costs the server, and a walk nobody can stop is exactly the thing
		// the Cancel button exists for.
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if err := walker.Err(); err != nil {
			// One unreadable subdirectory must not abort the whole transfer;
			// a permission-denied /proc entry is normal.
			continue
		}
		info := walker.Stat()
		if info == nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		rel := strings.TrimPrefix(walker.Path(), root)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			continue
		}
		if len(files) >= maxWalkFiles {
			return nil, 0, i18n.Errorf("디렉터리에 파일이 %d개를 넘습니다 — 더 좁은 범위를 선택하세요", maxWalkFiles)
		}
		files = append(files, relFile{Rel: rel, Size: info.Size()})
		total += info.Size()
	}
	return files, total, nil
}

func (a *App) uploadDir(ctx context.Context, j *transferJob, files []relFile) error {
	client, err := a.mgr.SFTP(j.HostID)
	if err != nil {
		return err
	}
	if err := client.MkdirAll(j.Remote); err != nil {
		return err
	}

	for i, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.transfers.progressFile(j, i, f.Rel)

		src := filepath.Join(j.Local, filepath.FromSlash(f.Rel))
		dst := path.Join(j.Remote, f.Rel)
		if parent := path.Dir(dst); parent != "." {
			if err := client.MkdirAll(parent); err != nil {
				return err
			}
		}
		if err := a.copyToRemote(ctx, j, client, src, dst); err != nil {
			return fmt.Errorf("%s: %w", f.Rel, err)
		}
	}
	a.transfers.progressFile(j, len(files), "")
	return nil
}

func (a *App) downloadDir(ctx context.Context, j *transferJob, files []relFile) error {
	client, err := a.mgr.SFTP(j.HostID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(j.Local, 0o755); err != nil {
		return err
	}

	for i, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.transfers.progressFile(j, i, f.Rel)

		src := path.Join(j.Remote, f.Rel)
		dst := filepath.Join(j.Local, filepath.FromSlash(f.Rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := a.copyFromRemote(ctx, j, client, src, dst); err != nil {
			return fmt.Errorf("%s: %w", f.Rel, err)
		}
	}
	a.transfers.progressFile(j, len(files), "")
	return nil
}

// copyToRemote and copyFromRemote share the single-file body, minus the
// temporary-name dance: a partially written file inside a tree is cleaned up by
// the caller failing the whole transfer, and renaming every file would double
// the round trips for no benefit.
func (a *App) copyToRemote(ctx context.Context, j *transferJob, client *sftp.Client, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := client.Create(dst)
	if err != nil {
		return err
	}
	pw := &progressWriter{q: a.transfers, j: j, ctx: ctx}
	_, copyErr := io.Copy(io.MultiWriter(out, pw), in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (a *App) copyFromRemote(ctx context.Context, j *transferJob, client *sftp.Client, src, dst string) error {
	in, err := client.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	pw := &progressWriter{q: a.transfers, j: j, ctx: ctx}
	_, copyErr := io.Copy(io.MultiWriter(out, pw), in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// setTotals fills in what the walk found, turning a row that was showing
// "reading the listing" into one with a progress bar.
func (q *transferQueue) setTotals(j *transferJob, files int, size int64) {
	q.mu.Lock()
	j.Files = files
	j.Size = size
	q.mu.Unlock()
	q.emit(j)
}

// progressFile records which file of the tree is in flight.
func (q *transferQueue) progressFile(j *transferJob, done int, rel string) {
	q.mu.Lock()
	j.FilesDone = done
	j.CurrentRel = rel
	q.mu.Unlock()
	q.emit(j)
}
