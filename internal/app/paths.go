package app

import (
	"fmt"
	"path"
	"strings"
)

// Destructive-action guards for remote paths (§7.4).
//
// These live in Go, not in the confirmation dialog. A dialog is a suggestion:
// a frontend bug, a stray double-click on a focused button, or anyone calling
// the binding directly walks straight past it. The rule that a recursive delete
// of /etc requires the user to type "/etc" is only a rule if the Go side
// refuses to proceed without it.

// CleanRemotePath normalises a remote path.
//
// Remote paths are POSIX regardless of what the client runs on, so path is used
// rather than filepath — on Windows, filepath.Clean would turn "/etc/ssh" into
// `\etc\ssh` and every subsequent operation would fail confusingly.
func CleanRemotePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("app: empty path")
	}
	if strings.ContainsRune(p, 0) {
		// A NUL would be truncated somewhere downstream, turning one path into
		// a different, shorter one.
		return "", fmt.Errorf("app: path contains a NUL byte")
	}
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("app: path must be absolute: %q", p)
	}
	return path.Clean(p), nil
}

// Depth reports how many components a cleaned absolute path has: "/" is 0,
// "/etc" is 1, "/etc/ssh" is 2.
func Depth(cleaned string) int {
	if cleaned == "/" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(cleaned, "/"), "/")
}

// extraProtected covers directories two levels down whose loss is as bad as a
// top-level one. Home directories are here because "/home/alice" is the single
// most valuable directory on many servers and the depth rule alone would miss it.
var extraProtected = map[string]bool{
	"/usr/local":     true,
	"/usr/bin":       true,
	"/usr/lib":       true,
	"/usr/share":     true,
	"/var/lib":       true,
	"/var/log":       true,
	"/var/www":       true,
	"/etc/ssh":       true,
	"/etc/systemd":   true,
	"/opt/local":     true,
	"/root/.ssh":     true,
	"/boot/efi":      true,
	"/var/lib/mysql": true,
}

// IsProtectedPath reports whether recursively deleting cleaned would be severe
// enough to demand the user type the path out (§7.4).
//
// The rule is the root and everything directly under it — /, /etc, /usr, and
// also /srv, /opt, /data or whatever else that server happens to have at the
// top — plus home directories and the well-known second-level paths above.
func IsProtectedPath(cleaned string) bool {
	if cleaned == "/" || Depth(cleaned) <= 1 {
		return true
	}
	if extraProtected[cleaned] {
		return true
	}
	// /home/<user> and /Users/<user>: one user's entire data.
	if d := path.Dir(cleaned); (d == "/home" || d == "/Users") && Depth(cleaned) == 2 {
		return true
	}
	return false
}

// CheckDelete validates a delete request before anything is sent to the server.
//
// typed is what the user retyped into the confirmation field. It is compared
// against the path only when the path is protected; for ordinary paths it is
// ignored, so the common case stays a single confirm click.
func CheckDelete(paths []string, recursive bool, typed string) ([]string, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("app: nothing to delete")
	}

	cleaned := make([]string, 0, len(paths))
	var protected []string
	for _, p := range paths {
		c, err := CleanRemotePath(p)
		if err != nil {
			return nil, err
		}
		if c == "/" {
			// Not a confirmation problem. There is no circumstance in which a
			// file manager should delete the root filesystem, so it is not on
			// offer at any level of insistence.
			return nil, fmt.Errorf("루트(/)는 삭제할 수 없습니다")
		}
		cleaned = append(cleaned, c)
		if recursive && IsProtectedPath(c) {
			protected = append(protected, c)
		}
	}

	if len(protected) == 0 {
		return cleaned, nil
	}

	// Protected paths go one at a time: a batch cannot be meaningfully
	// confirmed by typing a single path, and mixing one into a multi-select is
	// exactly how an accident happens.
	if len(cleaned) > 1 {
		return nil, fmt.Errorf(
			"보호된 경로(%s)는 다른 항목과 함께 삭제할 수 없습니다 — 하나씩 진행하세요",
			strings.Join(protected, ", "))
	}
	if strings.TrimSpace(typed) != cleaned[0] {
		return nil, fmt.Errorf(
			"%s 는 보호된 경로입니다 — 삭제하려면 경로를 정확히 입력해야 합니다",
			cleaned[0])
	}
	return cleaned, nil
}
