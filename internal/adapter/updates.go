package adapter

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Pending updates and whether the box wants a reboot (T-25).
//
// This is the cheapest thing in the app. Both answers are already written down
// on the server by update-notifier, in files anybody can read — `-rw-r--r--`,
// measured. So nothing is executed here: the app opens two files over SFTP and
// reads them. No command, no exec channel, nothing in the Command Log.
//
// What it costs instead is honesty about three things, all of which were found
// by looking at a real server rather than by reasoning about it:
//
//   - The count is a **cached snapshot**, not a live answer. On the box this was
//     measured on it was four and a half hours old. Printing "63 updates" with
//     no date is the same class of lie as drawing a line across a gap in a chart.
//   - The package list **repeats itself**. It is appended to, so a box that has
//     skipped five kernels lists `linux-base` five times.
//   - A distribution without these files is **not a distribution with nothing to
//     do**. Debian had neither, and answering "no reboot needed" there would be
//     an invention.

// UpdateStatus is what the two files say.
type UpdateStatus struct {
	// Known is false where the server does not keep these files at all. Then
	// every other field is meaningless and the UI says nothing rather than
	// saying zero — the same rule sar follows.
	Known bool `json:"known"`
	// Updates is how many packages can be upgraded, or -1 when the file was
	// there but did not parse. Its wording follows the distribution and the
	// system locale, so failing to read it is a normal outcome.
	Updates int `json:"updates"`
	// Security is the subset that are security updates, or -1 when unstated.
	Security int `json:"security"`
	// CheckedAt is when that count was computed, from the apt stamp's mtime.
	// Zero when there is no stamp, which means the age is unknown — which the
	// UI has to say, because an unknown age is not a fresh one.
	CheckedAt time.Time `json:"checkedAt"`
	// Raw is the file as written. Shown when the count did not parse, so a
	// server speaking an unexpected dialect still tells the user something.
	Raw string `json:"raw,omitempty"`

	// RebootRequired is the presence of /var/run/reboot-required. On a server
	// that keeps these files its absence is a real "no".
	RebootRequired bool `json:"rebootRequired"`
	// RebootPkgs is what asked for the reboot, de-duplicated in first-seen
	// order. Its length is the interesting part: five kernels means five
	// updates went by without one.
	RebootPkgs []string `json:"rebootPkgs,omitempty"`
}

var (
	// "63 updates can be applied immediately."
	reUpdates = regexp.MustCompile(`(?i)(\d+)\s+updates?\s+can\s+be\s+applied\s+immediately`)
	// "3 of these updates are security updates." — the standard Ubuntu line.
	reSecurity = regexp.MustCompile(`(?i)(\d+)\s+of\s+these\s+updates?\s+(?:is|are)\s+(?:a\s+)?security\s+updates?`)
)

// ParseUpdatesAvailable reads update-notifier's MOTD snippet.
//
// Deliberately not a general "find a number near the word security" match. The
// file also carries ESM lines — "5 additional security updates can be applied
// with ESM Apps" — and those are updates the machine *cannot* apply: they need
// a subscription it does not have. Counting them would tell the user to go and
// install something that is not available to them.
func ParseUpdatesAvailable(text string) (updates, security int) {
	updates, security = -1, -1
	if m := reUpdates.FindStringSubmatch(text); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			updates = n
		}
	}
	if m := reSecurity.FindStringSubmatch(text); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			security = n
		}
	}
	return updates, security
}

// ParseRebootPkgs reads /var/run/reboot-required.pkgs.
//
// The file is appended to, never rewritten, so the same package appears once
// per upgrade that wanted a restart. First-seen order is kept because it is
// roughly chronological, which is the only ordering the file offers.
func ParseRebootPkgs(text string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, line := range strings.Split(text, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}
