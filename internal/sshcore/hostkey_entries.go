package sshcore

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Reading and adding known_hosts entries by address.
//
// The verification path (Callback) never needs this: x/crypto answers "does this
// key match" and that is all a connection asks. Settings sync does need it — it
// carries trusted host keys between machines, and the unit it can merge on is the
// address, because that is what the file is keyed by (§6.3 of the sync design).
//
// Kept beside the verifier rather than in the sync package so there is one piece
// of code that knows how an address is spelled in this file. Two spellings would
// mean a key written by one path and invisible to the other.

// TrustedKeys returns the keys recorded for addr, which is `host:port`.
//
// A missing file is no keys and no error: that is a machine that has not connected
// anywhere yet.
func TrustedKeys(path, addr string) ([]ssh.PublicKey, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sshcore: read known_hosts %s: %w", path, err)
	}
	want := knownhosts.Normalize(addr)

	var out []ssh.PublicKey
	// Line by line rather than handing the whole file to ssh.ParseKnownHosts: on a
	// line it cannot parse, that function does not reliably say where the next one
	// starts, and this file is hand-edited often enough that one bad line must not
	// hide the rest. A line that is missing would be read as "this server has
	// never been seen", which is the answer that makes settings sync take whatever
	// key the repository offers.
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		marker, hosts, key, _, _, err := ssh.ParseKnownHosts(append(line, '\n'))
		if err != nil {
			continue
		}
		// A revoked or CA line is not a key this host is trusted by.
		if marker != "" {
			continue
		}
		for _, h := range hosts {
			if h == want {
				out = append(out, key)
				break
			}
		}
	}
	return out, nil
}

// TrustKey records a key for addr, the same way the first-contact prompt does.
//
// Appended, never replacing: this file is the record of what this machine has
// actually seen, and rewriting an entry would be the one operation a host key file
// must not offer without somebody deciding it.
func TrustKey(path, addr string, key ssh.PublicKey) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("sshcore: known_hosts directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("sshcore: open known_hosts: %w", err)
	}
	line := knownhosts.Line([]string{knownhosts.Normalize(addr)}, key)
	if _, err := fmt.Fprintln(f, line); err != nil {
		_ = f.Close()
		return fmt.Errorf("sshcore: write known_hosts: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("sshcore: close known_hosts: %w", err)
	}
	return nil
}
