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
		return failResult(fmt.Errorf("%s 는 보호된 경로입니다 — 이름을 바꿀 수 없습니다", src))
	}
	client, err := a.mgr.SFTP(hostID)
	if err != nil {
		return failResult(err)
	}

	// Overwriting silently is how a rename becomes data loss.
	if _, err := client.Lstat(dst); err == nil {
		return failResult(fmt.Errorf("%s 가 이미 있습니다", dst))
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
	Path     string `json:"path"`
	Content  string `json:"content"`
	Size     int64  `json:"size"`
	Perm     uint32 `json:"perm"`
	TooLarge bool   `json:"tooLarge"`
	Binary   bool   `json:"binary"`
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
	out := TextFile{Path: cleaned, Size: fi.Size(), Perm: uint32(fi.Mode().Perm())}
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

// WriteTextFile saves the editor's contents back over SFTP (§4.2).
func (a *App) WriteTextFile(hostID, p, content string) ActionResult {
	cleaned, err := CleanRemotePath(p)
	if err != nil {
		return failResult(err)
	}
	client, err := a.mgr.SFTP(hostID)
	if err != nil {
		return failResult(err)
	}

	// Preserve the existing mode: opening with O_CREATE on a file that already
	// exists keeps its permissions, but a new file would otherwise be created
	// with whatever the server defaults to.
	perm := os.FileMode(0o644)
	if fi, err := client.Stat(cleaned); err == nil {
		perm = fi.Mode().Perm()
	}

	f, err := client.OpenFile(cleaned, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return a.fileFailure(hostID, cleaned, err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		f.Close()
		return a.fileFailure(hostID, cleaned, err)
	}
	if err := f.Close(); err != nil {
		return a.fileFailure(hostID, cleaned, err)
	}
	_ = client.Chmod(cleaned, perm)
	return okResult()
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
		out.Error = fmt.Sprintf("%s: 권한이 없습니다 — 관리자 권한으로 다시 시도할 수 있습니다", p)
	}
	return out
}
