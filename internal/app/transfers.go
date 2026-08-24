package app

import (
	"bytes"
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

	// Resumable means the bytes already moved are still on disk under the
	// temporary name, so this can pick up where it stopped rather than start
	// over. Only single files: see resumeOffset.
	Resumable bool `json:"resumable,omitempty"`
	// Resumed is the offset this attempt began at, so a bar that is 90% along
	// does not appear to restart at zero.
	Resumed int64 `json:"resumed,omitempty"`
}

type transferJob struct {
	Transfer
	cancel context.CancelFunc
	done   atomic.Int64
	// srcModTime is what the source's timestamp was when this was queued.
	// Resuming appends to a file, so it has to be the same file — a source
	// edited between the two attempts would otherwise produce a result that is
	// half one version and half another, and nothing about it would look wrong.
	srcModTime int64
}

// snapshot copies the row for the UI. The caller must hold q.mu: this reads
// every field of the embedded Transfer, and ResumeTransfer writes some of them
// from whichever goroutine the user clicked on.
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
//
// Clearing a row that could have been resumed also deletes the bytes it was
// holding. Otherwise dismissing a failed transfer would silently leave a
// half-file on the server named after something the user no longer sees.
func (q *transferQueue) clearFinished() {
	q.mu.Lock()
	kept := q.order[:0]
	var abandoned []*transferJob
	for _, id := range q.order {
		j := q.jobs[id]
		if j == nil {
			continue
		}
		if j.Status == TransferQueued || j.Status == TransferRunning {
			kept = append(kept, id)
			continue
		}
		if j.Resumable {
			abandoned = append(abandoned, j)
		}
		delete(q.jobs, id)
	}
	q.order = kept
	q.mu.Unlock()

	// Outside the lock: dropPartial takes it, and an SFTP round trip has no
	// business holding the queue while the panel wants to render.
	for _, j := range abandoned {
		q.dropPartial(j)
	}
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
	// Snapshot under the lock, publish outside it. Emitting while holding the
	// queue would put an event-bus call in front of every other caller, and
	// reading the row without it races the resume that just re-queued this job.
	q.mu.Lock()
	t := j.snapshot()
	q.mu.Unlock()
	q.app.emit("transfer:progress", t)
}

// resumable reads the flag the transfer goroutine needs but does not own —
// keepPartial and dropPartial write it from elsewhere.
func (q *transferQueue) resumable(j *transferJob) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return j.Resumable
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
		j.srcModTime = fi.ModTime().Unix()
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
	size, modTime := statOf(src)
	if err := a.transfers.checkSourceUnchanged(j, size, modTime); err != nil {
		return err
	}

	// Write to a temporary name and rename on success, so an interrupted
	// upload never leaves a half-written file wearing the real name. The same
	// file is what a later attempt resumes into.
	tmp := j.Remote + partialSuffix

	var (
		dst *sftp.File
		at  int64
	)
	if fi, statErr := client.Stat(tmp); statErr == nil && a.transfers.resumable(j) {
		at = resumeOffset(fi.Size(), j.Size)
	}
	if at > 0 {
		// O_RDWR, not O_WRONLY: the seam has to be read back before anything is
		// written past it.
		dst, err = client.OpenFile(tmp, os.O_RDWR)
		if err == nil {
			err = a.transfers.checkSeam(j, src, dst, at)
		}
		if err == nil {
			_, err = dst.Seek(at, io.SeekStart)
		}
		if err == nil {
			_, err = src.Seek(at, io.SeekStart)
		}
		if err != nil && dst != nil {
			_ = dst.Close()
		}
	} else {
		dst, err = client.Create(tmp)
	}
	if err != nil {
		return err
	}
	a.transfers.beginAt(j, at)

	pw := &progressWriter{q: a.transfers, j: j, ctx: ctx}
	_, copyErr := io.Copy(io.MultiWriter(dst, pw), src)
	closeErr := dst.Close()

	if copyErr != nil {
		a.transfers.keepPartial(j)
		return copyErr
	}
	if closeErr != nil {
		a.transfers.keepPartial(j)
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
		// The last component is a name the server chose, not one the user
		// typed — they picked it out of a listing. path.Base only strips at
		// "/", so a name carrying the client's *other* separator arrives whole.
		base := path.Base(cleaned)
		if !safeLocalName(base) {
			return nil, errUnsafeName(base)
		}
		if fi.IsDir() {
			j := a.transfers.add(Transfer{
				HostID:    hostID,
				Direction: "download",
				Local:     filepath.Join(localDir, base),
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
			Local:     filepath.Join(localDir, base),
			Remote:    cleaned,
			Size:      fi.Size(),
		})
		j.srcModTime = fi.ModTime().Unix()
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
	size, modTime := statOf(src)
	if err := a.transfers.checkSourceUnchanged(j, size, modTime); err != nil {
		return err
	}

	tmp := j.Local + partialSuffix

	var (
		dst *os.File
		at  int64
	)
	if fi, statErr := os.Stat(tmp); statErr == nil && a.transfers.resumable(j) {
		at = resumeOffset(fi.Size(), j.Size)
	}
	if at > 0 {
		dst, err = os.OpenFile(tmp, os.O_RDWR, 0o644)
		if err == nil {
			err = a.transfers.checkSeam(j, src, dst, at)
		}
		if err == nil {
			_, err = dst.Seek(at, io.SeekStart)
		}
		if err == nil {
			_, err = src.Seek(at, io.SeekStart)
		}
		if err != nil && dst != nil {
			_ = dst.Close()
		}
	} else {
		dst, err = os.Create(tmp)
	}
	if err != nil {
		return err
	}
	a.transfers.beginAt(j, at)

	pw := &progressWriter{q: a.transfers, j: j, ctx: ctx}
	_, copyErr := io.Copy(io.MultiWriter(dst, pw), src)
	closeErr := dst.Close()

	if copyErr != nil {
		a.transfers.keepPartial(j)
		return copyErr
	}
	if closeErr != nil {
		a.transfers.keepPartial(j)
		return closeErr
	}
	if err := os.Rename(tmp, j.Local); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	a.transfers.emit(j)
	return nil
}

// Resuming an interrupted transfer (§4.2).
//
// SFTP makes the mechanics trivial and the safety the whole job. Every read and
// write on the wire already carries an absolute offset — SSH_FXP_READ and
// SSH_FXP_WRITE are addressed, not streamed — so continuing is a seek, with no
// Range header to negotiate and nothing for the server to opt into.
//
// What SFTP will not tell you is whether the bytes already on disk came from
// the file you are about to append to. Nothing prevents appending the second
// half of a rebuilt archive to the first half of the old one; the result is the
// right length, the transfer reports success, and the corruption surfaces
// somewhere else entirely. So the source's size and timestamp are recorded when
// the transfer is queued and checked again before a single byte is appended.

// partialSuffix names the in-progress file. Visible on purpose: a transfer that
// dies with the app leaves this behind, and a name that says what it is beats a
// hidden file nobody can account for.
const partialSuffix = ".litedeck-partial"

// resumeOffset decides where to pick up, or 0 to start over. A partial at least
// as large as the source is not a resume point — it is evidence that something
// else has been writing there.
func resumeOffset(partial, total int64) int64 {
	if partial <= 0 || total <= 0 || partial >= total {
		return 0
	}
	return partial
}

type statter interface{ Stat() (os.FileInfo, error) }

func statOf(f statter) (int64, int64) {
	fi, err := f.Stat()
	if err != nil {
		return 0, 0
	}
	return fi.Size(), fi.ModTime().Unix()
}

// overlapWindow is how much of the already-transferred region is read back and
// compared before anything is appended to it.
//
// Size and timestamp alone are not enough, and the gap is not theoretical: SFTP
// carries mtime in whole seconds, so a file rebuilt to the same length within
// the same second as the one already half-transferred looks identical by every
// cheap measure. Comparing the bytes on both sides of the seam catches it.
//
// This checks the seam, not the whole prefix. Verifying every byte already
// transferred would mean reading the source in full, which is the download this
// exists to avoid. A source edited only in a region further back than this
// window, to exactly the same length, would still get through — stated here
// because a guard whose limits are unwritten gets trusted past them.
const overlapWindow = 64 << 10

type readerAt interface {
	ReadAt(p []byte, off int64) (int, error)
}

// sameSeam reports whether the bytes just before `at` agree on both sides.
func sameSeam(src, partial readerAt, at int64) (bool, error) {
	n := int64(overlapWindow)
	if at < n {
		n = at
	}
	if n <= 0 {
		return true, nil
	}
	off := at - n
	a, b := make([]byte, n), make([]byte, n)
	if _, err := src.ReadAt(a, off); err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if _, err := partial.ReadAt(b, off); err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return bytes.Equal(a, b), nil
}

// checkSourceUnchanged is the cheap half: a source of a different length, or
// with a different timestamp, is a different file and there is nothing to
// compare. Only bites on a resume — a fresh transfer overwrites from zero and
// is correct whatever the source now says.
func (q *transferQueue) checkSourceUnchanged(j *transferJob, size, modTime int64) error {
	q.mu.Lock()
	resumable, want, wantMod := j.Resumable, j.Size, j.srcModTime
	q.mu.Unlock()
	if !resumable {
		return nil
	}
	if size == want && (wantMod == 0 || modTime == wantMod) {
		return nil
	}
	q.dropPartial(j)
	return errSourceChanged
}

var errSourceChanged = i18n.Errorf("전송을 시작한 뒤 원본이 바뀌었습니다 — 이어받을 수 없어 받다 만 파일을 지웠습니다. 처음부터 다시 전송하세요")

// checkSeam is the half that costs a read: the bytes leading up to the resume
// point have to be the same bytes on both sides.
func (q *transferQueue) checkSeam(j *transferJob, src, partial readerAt, at int64) error {
	if at <= 0 {
		return nil
	}
	same, err := sameSeam(src, partial, at)
	if err != nil {
		return err
	}
	if same {
		return nil
	}
	q.dropPartial(j)
	return errSourceChanged
}

// beginAt records where this attempt starts so the progress bar counts from
// there rather than from zero.
func (q *transferQueue) beginAt(j *transferJob, at int64) {
	q.mu.Lock()
	j.Resumed = at
	q.mu.Unlock()
	j.done.Store(at)
	q.emit(j)
}

// keepPartial marks a stopped transfer as one that can be picked up. The bytes
// are left where they are; that is the whole point.
func (q *transferQueue) keepPartial(j *transferJob) {
	q.mu.Lock()
	j.Resumable = !j.Dir && j.done.Load() > 0
	q.mu.Unlock()
}

// dropPartial deletes the in-progress file and forgets it.
func (q *transferQueue) dropPartial(j *transferJob) {
	q.mu.Lock()
	j.Resumable = false
	dir, direction, local, remote, host := j.Dir, j.Direction, j.Local, j.Remote, j.HostID
	q.mu.Unlock()
	if dir {
		return
	}
	if direction == "download" {
		_ = os.Remove(local + partialSuffix)
		return
	}
	if client, err := q.app.mgr.SFTP(host); err == nil {
		_ = client.Remove(remote + partialSuffix)
	}
}

// ResumeTransfer restarts a stopped transfer from where it got to.
func (a *App) ResumeTransfer(id string) error {
	j, ok := a.transfers.get(id)
	if !ok {
		return fmt.Errorf("app: no transfer %q", id)
	}
	a.transfers.mu.Lock()
	status, resumable, direction := j.Status, j.Resumable, j.Direction
	a.transfers.mu.Unlock()

	if status == TransferQueued || status == TransferRunning {
		return i18n.Errorf("이미 진행 중입니다")
	}
	if !resumable {
		return i18n.Errorf("이어받을 수 있는 전송이 아닙니다")
	}

	a.transfers.mu.Lock()
	j.Error = ""
	// Synchronously, before the goroutine starts: leaving the row reading
	// "cancelled" until the worker gets scheduled makes a resume that is about
	// to run look like one that failed instantly.
	j.Status = TransferQueued
	a.transfers.mu.Unlock()

	if direction == "upload" {
		a.transfers.run(j, a.uploadOne)
	} else {
		a.transfers.run(j, a.downloadOne)
	}
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

// safeLocalName reports whether a name the *server* chose can be joined under a
// local directory without escaping it.
//
// A downloaded name is not ours. It comes out of the server's directory
// listing, and joining it to the folder the user picked is the moment it stops
// being data and starts being a path — the same step that scp got wrong
// (CVE-2019-6111): a server, or anyone able to write into a directory on it,
// decides where bytes land on the client.
//
// Both separators are treated as separators here, on every platform, and that
// is the whole point rather than an excess of caution. A backslash is an
// ordinary character in a POSIX filename and a path separator on Windows, so
// `..\..\..\evil.exe` is a legal file name on the Linux box that serves it
// and an escape on the Windows laptop that receives it — filepath.Join ends in
// Clean, and Windows's Clean resolves `..` across `\` as readily as across `/`.
// A check written against the running platform's separator would pass on the
// developer's machine, pass in CI (which is Linux), and fail only for the user.
func safeLocalName(rel string) bool {
	if rel == "" {
		return false
	}
	norm := strings.ReplaceAll(rel, `\`, "/")
	if strings.HasPrefix(norm, "/") {
		return false // absolute: not a name inside the tree at all
	}
	if len(rel) >= 2 && rel[1] == ':' {
		return false // a Windows volume ("C:..") is not a name either
	}
	for _, part := range strings.Split(norm, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// errUnsafeName is what a rejected name reports. The transfer fails rather than
// skipping the file: a server sending a path that climbs out of the download
// folder is not a corrupt file to step over, and burying it in a progress
// counter is how it would go unnoticed.
//
// The name is quoted rather than interpolated raw. Everything about this path
// assumes the far side is not being straightforward, and a file name is allowed
// to contain newlines and terminal escapes — which would otherwise be rendered
// as-is in a message about that very server.
func errUnsafeName(rel string) error {
	return i18n.Errorf("서버가 보낸 이름 %q 이 받을 폴더 밖을 가리킵니다 — 전송을 중단했습니다", rel)
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

		if !safeLocalName(f.Rel) {
			return errUnsafeName(f.Rel)
		}
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
