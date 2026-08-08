package config

// Importing ~/.ssh/config (§4.1). The point is that the app is useful the first
// time it opens, without the user re-typing what OpenSSH already knows.
//
// This reads the subset LiteDeck can act on — Host, HostName, User, Port,
// IdentityFile, ProxyJump — and ignores the rest rather than guessing. An
// unsupported directive is not an error; it just does not survive the import.

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// DefaultSSHConfigPath returns ~/.ssh/config.
func DefaultSSHConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: locate home directory: %w", err)
	}
	return filepath.Join(home, ".ssh", "config"), nil
}

// ImportSSHConfig parses an OpenSSH client config into hosts.
//
// Wildcard patterns (`Host *`, `Host *.internal`) are skipped: they configure
// defaults for other entries rather than naming a server, so importing them
// would put unconnectable rows in the sidebar.
func ImportSSHConfig(path string) ([]Host, error) {
	entries, err := parseSSHConfig(path, 0)
	if err != nil {
		return nil, err
	}

	var out []Host
	for _, e := range entries {
		if e.hostname == "" {
			// No HostName means the alias itself is the address.
			e.hostname = e.alias
		}
		h := Host{
			ID:           "sshconfig:" + e.alias,
			Name:         e.alias,
			Hostname:     e.hostname,
			Port:         e.port,
			User:         e.user,
			IdentityFile: e.identityFile,
			ProxyJump:    e.proxyJump,
			Source:       "ssh_config",
		}
		if h.User == "" {
			// OpenSSH falls back to the local username.
			if u := os.Getenv("USER"); u != "" {
				h.User = u
			} else {
				h.User = os.Getenv("USERNAME")
			}
		}
		if h.IdentityFile != "" {
			h.Auth = []AuthMethod{AuthAgent, AuthKey}
		} else {
			h.Auth = []AuthMethod{AuthAgent, AuthPassword}
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

type sshEntry struct {
	alias        string
	hostname     string
	user         string
	port         int
	identityFile string
	proxyJump    string
}

const maxIncludeDepth = 8

func parseSSHConfig(path string, depth int) ([]sshEntry, error) {
	if depth > maxIncludeDepth {
		return nil, fmt.Errorf("config: ssh_config Include nested more than %d deep", maxIncludeDepth)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: open %s: %w", path, err)
	}
	defer f.Close()

	var (
		entries []sshEntry
		current []*sshEntry // one Host line can name several aliases
	)

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, value, ok := splitDirective(sc.Text())
		if !ok {
			continue
		}

		switch strings.ToLower(key) {
		case "host":
			for _, e := range current {
				entries = append(entries, *e)
			}
			current = nil
			for _, pattern := range strings.Fields(value) {
				if strings.ContainsAny(pattern, "*?!") {
					continue // a defaults block, not a server
				}
				current = append(current, &sshEntry{alias: pattern})
			}

		case "include":
			// Resolved relative to the including file's directory, as OpenSSH
			// does. Common in configs split across config.d/.
			for _, pattern := range strings.Fields(value) {
				if !filepath.IsAbs(pattern) {
					pattern = filepath.Join(filepath.Dir(path), expandHome(pattern))
				}
				matches, _ := filepath.Glob(pattern)
				for _, m := range matches {
					if info, err := os.Stat(m); err != nil || info.IsDir() {
						continue
					}
					included, err := parseSSHConfig(m, depth+1)
					if err != nil && !isNotExist(err) {
						return nil, err
					}
					entries = append(entries, included...)
				}
			}

		case "hostname":
			for _, e := range current {
				e.hostname = value
			}
		case "user":
			for _, e := range current {
				e.user = value
			}
		case "port":
			if p, err := strconv.Atoi(value); err == nil {
				for _, e := range current {
					e.port = p
				}
			}
		case "identityfile":
			for _, e := range current {
				e.identityFile = expandHome(value)
			}
		case "proxyjump":
			for _, e := range current {
				e.proxyJump = value
			}
		}
	}
	for _, e := range current {
		entries = append(entries, *e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	return entries, nil
}

// splitDirective returns the keyword and value of one config line. OpenSSH
// accepts both "Key value" and "Key=value", and keywords are case-insensitive.
func splitDirective(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	if i := strings.IndexAny(line, " \t="); i > 0 {
		key = line[:i]
		value = strings.TrimSpace(strings.TrimLeft(line[i:], " \t="))
	} else {
		return "", "", false
	}
	value = strings.Trim(value, `"`)
	if value == "" {
		return "", "", false
	}
	return key, value, true
}

func expandHome(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}

// isNotExist reports a missing file through any amount of wrapping — an
// Include that points at a file the user deleted is normal, not fatal.
func isNotExist(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}
