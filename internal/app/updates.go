package app

import (
	"io"

	"github.com/cpprhtn/LiteDeck/internal/adapter"
	"github.com/pkg/sftp"
)

// Pending updates and "does this box want a reboot" (T-25).
//
// Read over SFTP, not by running anything. update-notifier has already written
// both answers into files that are world-readable — measured `-rw-r--r--` on a
// real server — so this costs a few stats and two small reads. No command, no
// exec channel, nothing in the Command Log. It is the cheapest thing the app
// asks a server for.
//
// One read per connection, like detection. The files change when apt runs,
// which is not while somebody is looking at this screen.

const (
	updateNotifierDir = "/var/lib/update-notifier"
	updatesAvailable  = "/var/lib/update-notifier/updates-available"
	rebootRequired    = "/var/run/reboot-required"
	rebootRequiredPkg = "/var/run/reboot-required.pkgs"
	aptStamp          = "/var/lib/apt/periodic/update-success-stamp"
	// Both files are a few hundred bytes. The cap is here so a server with
	// something unexpected at these paths cannot hand back a huge read.
	maxUpdateFile = 64 << 10
)

// HostUpdates reports what is waiting to be installed, and whether a restart is
// owed.
//
// A server that does not keep these files comes back with Known false rather
// than with zeroes. Debian had neither of them, and "no reboot needed" would
// have been an invention: the absence of the file only means "no" on a system
// that would have written it.
func (a *App) HostUpdates(hostID string) (adapter.UpdateStatus, error) {
	client, err := a.mgr.SFTP(hostID)
	if err != nil {
		return adapter.UpdateStatus{}, err
	}

	out := adapter.UpdateStatus{Updates: -1, Security: -1}
	if _, err := client.Stat(updateNotifierDir); err != nil {
		// No update-notifier. Everything below would be guesswork, so the view
		// is told to say nothing at all.
		return out, nil
	}
	out.Known = true

	if text, err := readSmallFile(client, updatesAvailable); err == nil {
		out.Raw = text
		out.Updates, out.Security = adapter.ParseUpdatesAvailable(text)
	}
	// The count is a cached snapshot — four and a half hours old on the server
	// this was measured against. The stamp is how the screen can say so; with
	// no stamp the age is unknown, which is not the same as fresh.
	if fi, err := client.Stat(aptStamp); err == nil {
		out.CheckedAt = fi.ModTime().UTC()
	}

	if _, err := client.Stat(rebootRequired); err == nil {
		out.RebootRequired = true
		if text, err := readSmallFile(client, rebootRequiredPkg); err == nil {
			out.RebootPkgs = adapter.ParseRebootPkgs(text)
		}
	}
	return out, nil
}

// readSmallFile reads a bounded amount of a file over SFTP.
func readSmallFile(client *sftp.Client, path string) (string, error) {
	f, err := client.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	b, err := io.ReadAll(io.LimitReader(f, maxUpdateFile))
	if err != nil && err != io.EOF {
		return "", err
	}
	return string(b), nil
}
