package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Telling somebody a new release exists (T-41).
//
// # Why this stops at telling
//
// The obvious next step is downloading and installing it, and that step is not
// taken. The releases are not code-signed — the README says so as a warning —
// and a path that fetches an unsigned binary and runs it is the most valuable
// thing in this app to attack: one bad DNS answer or CDN object becomes code
// execution on every machine running it. That is true of any updater and
// especially true of one shipped with an SSH client that holds keys.
//
// So this reads a version number and draws a link. The download and the install
// stay a thing a person does, having looked at where it came from.
//
// # Why it is off by default
//
// This is the only outward request the app makes. Everything else it does goes
// to servers the user named. "No account, no telemetry" is a stated principle,
// and a version check is a request to a third party that says something about
// when this person is at their desk — small, but theirs to allow rather than
// ours to assume.

// UpdateInfo is what the sidebar shows.
type UpdateInfo struct {
	// Checked is false where the user has not turned this on, or nothing has
	// been asked yet. The UI then shows nothing at all rather than "up to date",
	// which would be a claim nobody made.
	Checked bool `json:"checked"`
	// Latest is the newest published tag, without the leading v.
	Latest string `json:"latest,omitempty"`
	// Newer reports that Latest is ahead of this build.
	Newer bool `json:"newer,omitempty"`
	// URL is the release page. The app never fetches the asset itself.
	URL string `json:"url,omitempty"`
}

// updateCheckInterval bounds how often the release list is asked for.
//
// Once a day. A release is not a thing that happens hourly, and this is
// somebody else's server being polled by every copy of this app that is open.
const updateCheckInterval = 24 * time.Hour

// updateEndpoint is GitHub's own API for the newest release.
const updateEndpoint = "https://api.github.com/repos/cpprhtn/LiteDeck/releases/latest"

type updateChecker struct {
	mu     sync.Mutex
	at     time.Time
	cached UpdateInfo
}

// CheckForUpdate reports whether a newer release has been published.
//
// Returns the last answer where one is recent enough, so opening the app twice
// in a day asks once. Never returns an error to the caller: a version check
// that cannot reach the internet is not something to interrupt anybody about.
func (a *App) CheckForUpdate() UpdateInfo {
	if a.settings == nil || !a.settings.Get().CheckUpdates {
		return UpdateInfo{}
	}
	a.updates.mu.Lock()
	if time.Since(a.updates.at) < updateCheckInterval && a.updates.cached.Checked {
		cached := a.updates.cached
		a.updates.mu.Unlock()
		return cached
	}
	a.updates.mu.Unlock()

	info := UpdateInfo{Checked: true, URL: "https://github.com/cpprhtn/LiteDeck/releases/latest"}
	if tag, err := latestTag(); err == nil {
		info.Latest = tag
		info.Newer = isNewer(tag, Version)
	}

	a.updates.mu.Lock()
	a.updates.at, a.updates.cached = time.Now(), info
	a.updates.mu.Unlock()
	return info
}

// SetCheckUpdates turns the version check on or off.
func (a *App) SetCheckUpdates(on bool) error {
	if a.settings == nil {
		return nil
	}
	return a.settings.SetCheckUpdates(on)
}

// CheckUpdatesEnabled reports the current setting, for the switch that sets it.
func (a *App) CheckUpdatesEnabled() bool {
	return a.settings != nil && a.settings.Get().CheckUpdates
}

func latestTag() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, updateEndpoint, nil)
	if err != nil {
		return "", err
	}
	// GitHub asks for this and answers differently without it.
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "LiteDeck/"+Version)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("app: release check answered %s", res.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	// Bounded: an answer this app cannot use is not worth reading to the end of.
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	tag := strings.TrimPrefix(strings.TrimSpace(body.TagName), "v")
	if tag == "" {
		return "", fmt.Errorf("app: release check returned no tag")
	}
	return tag, nil
}

// isNewer compares two dotted versions.
//
// Deliberately not a semver library. The versions this compares are the ones
// this repository publishes — three numbers, no pre-release suffixes — and
// anything it cannot read is reported as "not newer", which shows nothing
// rather than a wrong claim.
func isNewer(latest, current string) bool {
	l, c := versionParts(latest), versionParts(current)
	if l == nil || c == nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func versionParts(v string) []int {
	f := strings.Split(strings.TrimSpace(v), ".")
	if len(f) != 3 {
		return nil
	}
	out := make([]int, 3)
	for i, p := range f {
		n := 0
		for _, ch := range p {
			if ch < '0' || ch > '9' {
				return nil
			}
			n = n*10 + int(ch-'0')
		}
		out[i] = n
	}
	return out
}
