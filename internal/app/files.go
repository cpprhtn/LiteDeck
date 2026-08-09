package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/i18n"
	"github.com/pkg/sftp"
)

// The file explorer (§4.2).
//
// Everything here goes through the SFTP subsystem rather than parsing `ls`:
// that is priority 1 in §3.2c, and it is the only way a filename containing a
// newline, a quote or a semicolon stays a filename. The one exception is
// recursive delete, which SFTP has no primitive for.

// maxListEntries bounds one directory listing. A directory with a million
// entries exists on real servers, and sending all of it would stall the window
// for no benefit — nobody reads past the first few thousand rows.
const maxListEntries = 20000

// maxEditableBytes is the ceiling for the built-in text editor (§4.2). Above it
// the file is almost certainly not something to edit in a dialog, and loading
// it would freeze the webview.
const maxEditableBytes = 2 << 20 // 2 MiB

// FileEntry is one row of the file explorer.
type FileEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Mode       string `json:"mode"`  // "drwxr-xr-x", as ls prints it
	Perm       uint32 `json:"perm"`  // 0755, for the permissions dialog
	IsDir      bool   `json:"isDir"` // after following a symlink, if it resolves
	IsSymlink  bool   `json:"isSymlink"`
	LinkTarget string `json:"linkTarget,omitempty"`
	Broken     bool   `json:"broken,omitempty"` // symlink whose target is gone
	ModTime    int64  `json:"modTime"`          // unix seconds
	UID        uint32 `json:"uid"`
	GID        uint32 `json:"gid"`
}

// DirListing is one directory as the explorer shows it.
type DirListing struct {
	Path      string      `json:"path"`
	Parent    string      `json:"parent"`
	Entries   []FileEntry `json:"entries"`
	Total     int         `json:"total"`
	Truncated bool        `json:"truncated"`
	// Protected marks a directory whose recursive deletion needs the path
	// typed out, so the UI can warn before the user gets to the dialog (§7.4).
	Protected bool `json:"protected"`
}

// ListDir reads one remote directory (§3.4: SFTP ReadDir, never `ls`).
func (a *App) ListDir(hostID, dir string) (DirListing, error) {
	cleaned, err := CleanRemotePath(dir)
	if err != nil {
		return DirListing{}, err
	}
	client, err := a.mgr.SFTP(hostID)
	if err != nil {
		return DirListing{}, err
	}

	infos, err := client.ReadDir(cleaned)
	if err != nil {
		return DirListing{}, fmt.Errorf("%s: %w", cleaned, err)
	}

	listing := DirListing{
		Path:      cleaned,
		Parent:    path.Dir(cleaned),
		Total:     len(infos),
		Protected: IsProtectedPath(cleaned),
	}

	// Directories first, then by name — the order every file manager uses, and
	// the one that makes a listing scannable.
	sort.Slice(infos, func(i, j int) bool {
		di, dj := infos[i].IsDir(), infos[j].IsDir()
		if di != dj {
			return di
		}
		return strings.ToLower(infos[i].Name()) < strings.ToLower(infos[j].Name())
	})

	if len(infos) > maxListEntries {
		infos = infos[:maxListEntries]
		listing.Truncated = true
	}

	listing.Entries = make([]FileEntry, 0, len(infos))
	for _, fi := range infos {
		listing.Entries = append(listing.Entries, a.entry(client, cleaned, fi))
	}
	return listing, nil
}

func (a *App) entry(client *sftp.Client, dir string, fi os.FileInfo) FileEntry {
	full := path.Join(dir, fi.Name())
	e := FileEntry{
		Name:    fi.Name(),
		Path:    full,
		Size:    fi.Size(),
		Mode:    fi.Mode().String(),
		Perm:    uint32(fi.Mode().Perm()),
		IsDir:   fi.IsDir(),
		ModTime: fi.ModTime().Unix(),
	}
	if st, ok := fi.Sys().(*sftp.FileStat); ok && st != nil {
		e.UID, e.GID = st.UID, st.GID
	}

	// ReadDir reports link attributes, not the target's, so a symlink has to be
	// resolved separately to know whether double-clicking it opens a directory.
	if fi.Mode()&os.ModeSymlink != 0 {
		e.IsSymlink = true
		if target, err := client.ReadLink(full); err == nil {
			e.LinkTarget = target
		}
		if st, err := client.Stat(full); err == nil {
			e.IsDir = st.IsDir()
			e.Size = st.Size()
		} else {
			e.Broken = true
		}
	}
	return e
}

// PathStatus answers "what is here, and how careful should I be?" without
// listing the whole directory.
type PathStatus struct {
	Path      string `json:"path"`
	Exists    bool   `json:"exists"`
	IsDir     bool   `json:"isDir"`
	Size      int64  `json:"size"`
	Protected bool   `json:"protected"`
}

// StatPath inspects a single path. The UI calls it before opening a delete
// dialog so it knows whether to demand the path be typed.
func (a *App) StatPath(hostID, p string) (PathStatus, error) {
	cleaned, err := CleanRemotePath(p)
	if err != nil {
		return PathStatus{}, err
	}
	client, err := a.mgr.SFTP(hostID)
	if err != nil {
		return PathStatus{}, err
	}
	out := PathStatus{Path: cleaned, Protected: IsProtectedPath(cleaned)}

	fi, err := client.Lstat(cleaned)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("%s: %w", cleaned, err)
	}
	out.Exists = true
	out.IsDir = fi.IsDir()
	out.Size = fi.Size()
	return out, nil
}

// HomeDir returns the login user's home directory — where the explorer opens.
func (a *App) HomeDir(hostID string) (string, error) {
	client, err := a.mgr.SFTP(hostID)
	if err != nil {
		return "", err
	}
	// The SFTP server's working directory is the login user's home.
	wd, err := client.Getwd()
	if err != nil || wd == "" {
		return "/", nil
	}
	return wd, nil
}

// MakeDir creates a directory (§4.2).
func (a *App) MakeDir(hostID, p string) ActionResult {
	cleaned, err := CleanRemotePath(p)
	if err != nil {
		return failResult(err)
	}
	client, err := a.mgr.SFTP(hostID)
	if err != nil {
		return failResult(err)
	}
	if err := client.Mkdir(cleaned); err != nil {
		return a.fileFailure(hostID, cleaned, err)
	}
	return okResult()
}

// RenamePath moves or renames (§3.4).
func (a *App) RenamePath(hostID, from, to string) ActionResult {
	src, err := CleanRemotePath(from)
	if err != nil {
		return failResult(err)
	}
	dst, err := CleanRemotePath(to)
	if err != nil {
		return failResult(err)
	}
	if src == dst {
		return okResult()
	}
	if IsProtectedPath(src) {
		return failResult(i18n.Errorf("%s 는 보호된 경로입니다 — 이름을 바꿀 수 없습니다", src))
	}
	client, err := a.mgr.SFTP(hostID)
	if err != nil {
		return failResult(err)
	}

	// Overwriting silently is how a rename becomes data loss.
	if _, err := client.Lstat(dst); err == nil {
		return failResult(i18n.Errorf("%s 가 이미 있습니다", dst))
	}

	// PosixRename is the atomic form, but it is an OpenSSH extension that older
	// or non-OpenSSH servers do not implement. Plain Rename is the fallback.
	if err := client.PosixRename(src, dst); err != nil {
		if err2 := client.Rename(src, dst); err2 != nil {
			return a.fileFailure(hostID, src, err2)
		}
	}
	return okResult()
}

// Chmod changes permissions (§4.2's rwx checkboxes).
func (a *App) Chmod(hostID, p string, perm uint32) ActionResult {
	cleaned, err := CleanRemotePath(p)
	if err != nil {
		return failResult(err)
	}
	if perm > 0o7777 {
		return failResult(fmt.Errorf("app: invalid mode %o", perm))
	}
	client, err := a.mgr.SFTP(hostID)
	if err != nil {
		return failResult(err)
	}
	if err := client.Chmod(cleaned, os.FileMode(perm)); err != nil {
		return a.fileFailure(hostID, cleaned, err)
	}
	return okResult()
}

// DeletePaths removes files and directories (§3.4, §7.4).
//
// typed is the path the user retyped; CheckDelete decides whether it was
// required. Non-recursive deletes use the SFTP primitives, which fail safely on
// a non-empty directory. Recursive deletion has no SFTP equivalent, so it falls
// back to `rm -rf --` with the path passed as a quoted argument.
func (a *App) DeletePaths(hostID string, paths []string, recursive bool, typed string) ActionResult {
	cleaned, err := CheckDelete(paths, recursive, typed)
	if err != nil {
		return failResult(err)
	}
	client, err := a.mgr.SFTP(hostID)
	if err != nil {
		return failResult(err)
	}

	for _, p := range cleaned {
		fi, err := client.Lstat(p)
		if err != nil {
			return failResult(fmt.Errorf("%s: %w", p, err))
		}

		// A symlink is removed as a link. Following it would delete whatever it
		// points at, which is never what dragging a shortcut to the bin means.
		isDir := fi.IsDir() && fi.Mode()&os.ModeSymlink == 0

		switch {
		case !isDir:
			err = client.Remove(p)
		case !recursive:
			err = client.RemoveDirectory(p)
		default:
			return a.removeRecursive(hostID, p)
		}
		if err != nil {
			return a.fileFailure(hostID, p, err)
		}
	}
	return okResult()
}

// removeRecursive shells out, because SFTP has no recursive remove.
func (a *App) removeRecursive(hostID, p string) ActionResult {
	conn, err := a.mgr.Conn(hostID)
	if err != nil {
		return failResult(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), PromptTimeout+pollTimeout)
	defer cancel()

	// `--` is mandatory: without it a path beginning with a dash is read as
	// options, and `rm -rf -r` is not what anybody meant (§3.4).
	res, err := conn.Exec(ctx, "rm", "-rf", "--", p)
	if err != nil {
		return failResult(err)
	}
	return a.classify(hostID, res, false)
}

// TextFile is a file small enough to edit in the app.
type TextFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Size    int64  `json:"size"`
	Perm    uint32 `json:"perm"`
	// ModTime and Size together are what the editor hands back on save, so the
	// app can tell "nobody touched this" from "somebody else edited it while
	// your tab was open" (§4.7-3).
	ModTime  int64 `json:"modTime"`
	TooLarge bool  `json:"tooLarge"`
	Binary   bool  `json:"binary"`
}

// ReadTextFile loads a file for the built-in editor (§4.2).
func (a *App) ReadTextFile(hostID, p string) (TextFile, error) {
	cleaned, err := CleanRemotePath(p)
	if err != nil {
		return TextFile{}, err
	}
	client, err := a.mgr.SFTP(hostID)
	if err != nil {
		return TextFile{}, err
	}

	fi, err := client.Stat(cleaned)
	if err != nil {
		return TextFile{}, fmt.Errorf("%s: %w", cleaned, err)
	}
	out := TextFile{
		Path:    cleaned,
		Size:    fi.Size(),
		Perm:    uint32(fi.Mode().Perm()),
		ModTime: fi.ModTime().Unix(),
	}
	if fi.Size() > maxEditableBytes {
		out.TooLarge = true
		return out, nil
	}

	f, err := client.Open(cleaned)
	if err != nil {
		return out, fmt.Errorf("%s: %w", cleaned, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxEditableBytes+1))
	if err != nil {
		return out, fmt.Errorf("%s: %w", cleaned, err)
	}
	// A NUL byte means this is not text; rendering it in a textarea would
	// corrupt the file the moment the user pressed save.
	if idx := strings.IndexByte(string(data), 0); idx >= 0 {
		out.Binary = true
		return out, nil
	}
	out.Content = string(data)
	return out, nil
}

// SaveRequest is one save from the editor (§4.7-3).
type SaveRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	// BaseModTime and BaseSize are what the file looked like when the editor
	// opened it. Zero means "do not check" — a new file, or a caller with
	// nothing to compare against.
	BaseModTime int64 `json:"baseModTime"`
	BaseSize    int64 `json:"baseSize"`
	// Force saves anyway, after the user has been shown the conflict.
	Force bool `json:"force"`
}

// SaveResult is what the editor gets back.
type SaveResult struct {
	ActionResult
	// Conflict means the file on the server is not the one that was opened.
	// Nothing was written; the UI asks before overwriting.
	Conflict bool `json:"conflict"`
	// InPlace means the atomic path was not available and the file was written
	// over itself instead. Worth saying out loud: that write is not crash-safe.
	InPlace bool `json:"inPlace"`
	// ModTime and Size of the file as it now stands, so the tab can keep editing
	// without reopening.
	ModTime int64 `json:"modTime"`
	Size    int64 `json:"size"`
}

func saveFailure(err error) SaveResult {
	return SaveResult{ActionResult: failResult(err)}
}

// WriteTextFile saves the editor's contents back over SFTP (§4.2).
//
// Kept as the unconditional form: overwrite whatever is there, no questions.
func (a *App) WriteTextFile(hostID, p, content string) ActionResult {
	res := a.SaveTextFile(hostID, SaveRequest{Path: p, Content: content})
	return res.ActionResult
}

// SaveTextFile writes the editor's contents back atomically (§4.7-3).
//
// The obvious implementation — open with O_TRUNC, then write — empties the file
// first and fills it afterwards. A connection that drops in between leaves a
// truncated file where a working config used to be, and the window is the whole
// transfer, not an instant. This is the write path the app uses most, on the
// files a server can least afford to lose, so it gets the same care as the
// destructive actions do.
//
// Instead the content is staged in a sibling temp file and moved over the
// target with a rename, which POSIX makes atomic: a reader sees the old file or
// the new one, never half of either. Rename needs write permission on the
// *directory*, which is not the same as write permission on the file — when it
// is missing the save falls back to writing in place and says so rather than
// failing.
func (a *App) SaveTextFile(hostID string, req SaveRequest) SaveResult {
	cleaned, err := CleanRemotePath(req.Path)
	if err != nil {
		return saveFailure(err)
	}
	client, err := a.mgr.SFTP(hostID)
	if err != nil {
		return saveFailure(err)
	}

	// Preserve the existing mode: O_CREATE on an existing file keeps its
	// permissions, but a staged file is new and would otherwise land with
	// whatever the server's umask says. A config that loses its 0600 on save is
	// a security bug delivered by an editor.
	perm := os.FileMode(0o644)
	var owner *sftp.FileStat

	fi, statErr := client.Stat(cleaned)
	switch {
	case statErr == nil:
		perm = fi.Mode().Perm()
		if st, ok := fi.Sys().(*sftp.FileStat); ok {
			owner = st
		}
		if c, changed := checkBase(req, fi); changed {
			return c
		}
	case errors.Is(statErr, os.ErrNotExist):
		// Gone since it was opened. Saving recreates it, which may well be what
		// the user wants — but not without being asked.
		if req.BaseModTime != 0 && !req.Force {
			return SaveResult{
				Conflict:     true,
				ActionResult: ActionResult{Error: i18n.T("%s 가 서버에서 사라졌습니다", cleaned)},
			}
		}
	default:
		return SaveResult{ActionResult: a.fileFailure(hostID, cleaned, statErr)}
	}

	out := SaveResult{}
	err = stageAndRename(client, cleaned, req.Content, perm, owner)
	if errors.Is(err, errCannotStage) {
		// The directory will not take a temp file. Writing over the target is
		// the only way left, and it is not crash-safe — the caller is told so
		// it can be said on screen instead of assumed.
		out.InPlace = true
		err = writeInPlace(client, cleaned, req.Content, perm)
	}
	if err != nil {
		return SaveResult{InPlace: out.InPlace, ActionResult: a.fileFailure(hostID, cleaned, err)}
	}

	out.OK = true
	// The mtime the next save compares against. A save that cannot read it back
	// still succeeded — the tab reopens the file rather than the save failing.
	if fi, err := client.Stat(cleaned); err == nil {
		out.ModTime = fi.ModTime().Unix()
		out.Size = fi.Size()
	}
	return out
}

// checkBase answers "is this still the file the editor opened?".
//
// SFTP v3 carries mtime in whole seconds, so two edits inside the same second
// with the same resulting size slip through. That is the limit of what the
// protocol reports; it is not worth hashing a file on every save to close it.
func checkBase(req SaveRequest, fi os.FileInfo) (SaveResult, bool) {
	if req.Force || req.BaseModTime == 0 {
		return SaveResult{}, false
	}
	if fi.ModTime().Unix() == req.BaseModTime && fi.Size() == req.BaseSize {
		return SaveResult{}, false
	}
	return SaveResult{
		Conflict: true,
		ModTime:  fi.ModTime().Unix(),
		Size:     fi.Size(),
		ActionResult: ActionResult{
			Error: i18n.S("이 파일은 연 뒤에 서버에서 바뀌었습니다 — 지금 저장하면 그 변경이 사라집니다"),
		},
	}, true
}

// errCannotStage means the atomic path is unavailable for this file, which is
// a reason to fall back rather than to fail.
var errCannotStage = errors.New(i18n.S("app: 임시 파일을 만들 수 없습니다"))

// stageSeq names staged files. Seeded from the clock so a temp file orphaned by
// an earlier run of the app does not collide with this one's first save.
var stageSeq atomic.Uint64

func init() { stageSeq.Store(uint64(time.Now().UnixNano())) }

// stageAndRename writes content to a sibling temp file and moves it over target.
func stageAndRename(
	client *sftp.Client, target, content string, perm os.FileMode, owner *sftp.FileStat,
) error {
	tmp, f, err := createStaged(client, target)
	if err != nil {
		return err
	}
	discard := func() { _ = client.Remove(tmp) }

	if _, err := f.Write([]byte(content)); err != nil {
		f.Close()
		discard()
		return err
	}
	if err := f.Close(); err != nil {
		discard()
		return err
	}

	// The staged file belongs to the login user. Renaming it over a file owned
	// by somebody else would quietly hand that file to us — a root-owned config
	// in a group-writable directory would come back owned by the operator, and
	// the daemon that reads it may well refuse it afterwards. Chown works only
	// as root, so when ownership would change and cannot be restored, the
	// in-place write is the safer answer.
	if owner != nil {
		if st, err := client.Stat(tmp); err == nil {
			if mine, ok := st.Sys().(*sftp.FileStat); ok && mine != nil &&
				(mine.UID != owner.UID || mine.GID != owner.GID) {
				if err := client.Chown(tmp, int(owner.UID), int(owner.GID)); err != nil {
					discard()
					return i18n.Errorf("%w: 소유자를 유지할 수 없습니다 (%v)", errCannotStage, err)
				}
			}
		}
	}

	if err := client.Chmod(tmp, perm); err != nil {
		discard()
		return i18n.Errorf("%w: 권한을 옮길 수 없습니다 (%v)", errCannotStage, err)
	}

	// PosixRename replaces the target in one step. It is an OpenSSH extension,
	// which covers nearly every server anyone connects to — but not all of them.
	if err := client.PosixRename(tmp, target); err == nil {
		return nil
	}
	// Plain rename refuses an existing target on most servers, so the target has
	// to go first. That leaves a gap where the path does not exist — one round
	// trip wide, against a whole transfer for the O_TRUNC form — and the content
	// is already safe on disk, which is why the error names the staged file.
	if err := client.Rename(tmp, target); err == nil {
		return nil
	}
	if err := client.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		discard()
		return fmt.Errorf("%w: %v", errCannotStage, err)
	}
	if err := client.Rename(tmp, target); err != nil {
		// tmp is deliberately left behind: it holds the complete new content,
		// and it is now the only copy.
		return i18n.Errorf("%v — 새 내용은 %s 에 남아 있습니다", err, tmp)
	}
	return nil
}

// createStaged opens a temp file beside target, exclusively.
//
// O_EXCL failing does not tell us why: OpenSSH answers "already exists" and
// "the directory is read-only" with the same generic status. So a few names are
// tried — that covers collision — and persistent failure is read as "this
// directory will not take a temp file", which is the case the caller falls back
// for.
func createStaged(client *sftp.Client, target string) (string, *sftp.File, error) {
	dir, base := path.Dir(target), path.Base(target)
	// 255 bytes is the usual filename limit; leave room for the fixed parts.
	if len(base) > 180 {
		base = base[:180]
	}
	var last error
	for i := 0; i < 3; i++ {
		// The dot keeps it out of a plain listing, and the marker makes an
		// orphan left by a killed save identifiable rather than mysterious.
		name := path.Join(dir, fmt.Sprintf(".litedeck-%s.%d.tmp", base, stageSeq.Add(1)))
		f, err := client.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err == nil {
			return name, f, nil
		}
		last = err
	}
	return "", nil, fmt.Errorf("%w: %v", errCannotStage, last)
}

// writeInPlace is the fallback: truncate and rewrite, the way it always worked.
func writeInPlace(client *sftp.Client, target, content string, perm os.FileMode) error {
	f, err := client.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(content)); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_ = client.Chmod(target, perm)
	return nil
}

// fileFailure turns an SFTP error into a result, offering elevation when the
// server refused for want of privileges (§7.2).
//
// SFTP has no sudo, so an elevated retry has to go through a shell command
// instead — which is why this only offers, and the caller decides.
func (a *App) fileFailure(hostID, p string, err error) ActionResult {
	out := ActionResult{Error: fmt.Sprintf("%s: %v", p, err)}
	if errors.Is(err, os.ErrPermission) || strings.Contains(strings.ToLower(err.Error()), "permission denied") {
		out.NeedsElevation = true
		out.Error = i18n.T("%s: 권한이 없습니다 — 관리자 권한으로 다시 시도할 수 있습니다", p)
	}
	return out
}
