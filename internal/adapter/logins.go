package adapter

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// Who got in, and who kept trying (T-26).
//
// Two sources, because the obvious one is only half the answer:
//
//   - Successes come from wtmp through `last`. Measured `-rw-rw-r--`, so the
//     login user reads it — no root.
//   - Failures do **not** come from btmp. It is `-rw-rw---- root:utmp` and the
//     login user is not in utmp, so `lastb` exits 1. The journal has the same
//     events and more of them: it says which account was tried and from where,
//     where btmp only says that something failed.
//
// The scale is what shapes this. A server on the open internet logged 3,032
// failed passwords in twenty-four hours when this was measured — six times the
// journal read cap. So the server aggregates and the app reads a summary. The
// list would have been a list of one attack, truncated.

// LoginsScript collects both halves in one round trip.
//
// A shell script rather than argv, the same exception MetricsScript takes and
// for the same reason: a compile-time constant with nothing interpolated into
// it (§3.2b). Four separate journalctl passes cost 2,305 ms on the measured
// server; folding them into one awk pass over one read cost 583 ms.
//
// Only POSIX awk is used. The measured server had gawk, but Debian ships mawk
// and a script that quietly needs gawk would work on one and not the other.
//
// Counting happens here; sorting and cutting to a top-N happen in Go. The
// expensive part is turning three thousand lines into a few hundred, and that
// is done by the time this returns — measured, one day on one server: 3,008
// failures from 36 addresses against 737 different account names. So the pairs
// that come back are the 773 lines, not the 3,008, and nothing is dropped on
// the way: a spray that tries seven hundred names once each is a shape worth
// seeing, and a floor of "two or more" would erase exactly that one.
const LoginsScript = `echo '#last'; last -F -w -n 50 2>/dev/null
echo '#auth'
journalctl -t sshd --since -24h -g 'Failed password|Accepted ' --no-pager -q -o cat 2>/dev/null | awk '
/Failed password/ {
  fail++
  u = $0; sub(/.* for /, "", u); sub(/^invalid user /, "", u); sub(/ from .*/, "", u); user[u]++
  a = $0; sub(/.* from /, "", a); sub(/ .*/, "", a); src[a]++
  next
}
/Accepted / { ok++ }
END {
  printf "total %d %d\n", fail + 0, ok + 0
  for (k in src)  printf "ip %d %s\n", src[k], k
  for (k in user) printf "user %d %s\n", user[k], k
}'
:`

// Login is one row of wtmp.
type Login struct {
	User string `json:"user"`
	TTY  string `json:"tty,omitempty"`
	// From is empty for a console login, and holds the kernel version on a
	// boot record — that is what `last` puts in the column.
	From string    `json:"from,omitempty"`
	At   time.Time `json:"at"`
	// Until is zero while the session is still open.
	Until time.Time `json:"until,omitempty"`
	Open  bool      `json:"open,omitempty"`
	// Boot marks the "reboot / system boot" pseudo-records. They are the most
	// useful rows in the file and the least like the others, so they are
	// labelled rather than filtered out.
	Boot bool `json:"boot,omitempty"`
}

// Count is one aggregated attacker or account.
type Count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// AuthSummary is what the failures add up to. Never the failures themselves.
type AuthSummary struct {
	Failed   int `json:"failed"`
	Accepted int `json:"accepted"`
	// Sources and Users are the busiest few, largest first.
	Sources []Count `json:"sources,omitempty"`
	Users   []Count `json:"users,omitempty"`
	// Distinct counts before the cut, so "top 10 of 36" can be said rather
	// than implying the ten are all of them.
	DistinctSources int `json:"distinctSources"`
	DistinctUsers   int `json:"distinctUsers"`
}

// AuthTopN is how many rows survive the cut. Enough to see the shape of an
// attack, few enough to read at a glance.
const AuthTopN = 8

var weekdays = map[string]bool{
	"Mon": true, "Tue": true, "Wed": true, "Thu": true,
	"Fri": true, "Sat": true, "Sun": true,
}

// ParseLast reads `last -F -w`.
//
// Anchored on the date rather than on column positions. `last` is
// column-formatted, and a boot record puts two words — "system boot" — in the
// tty column, so splitting on whitespace and taking field 2 as the tty reads
// every reboot wrong. Finding the weekday token instead splits the row into
// "who and where" and "when", which is true of every shape the file has.
func ParseLast(out string) []Login {
	logins := []Login{}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "wtmp begins") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		// Where the timestamp starts. Anything before it names the session.
		at := -1
		for i, f := range fields {
			if weekdays[f] {
				at = i
				break
			}
		}
		if at < 1 {
			continue
		}
		l, ok := loginFrom(fields[:at], fields[at:])
		if !ok {
			continue
		}
		logins = append(logins, l)
	}
	return logins
}

func loginFrom(who, when []string) (Login, bool) {
	start, ok := lastTime(when)
	if !ok {
		return Login{}, false
	}
	l := Login{User: who[0], At: start}
	switch {
	case len(who) >= 3:
		// user, tty…, host. The tty can be more than one word: "system boot".
		l.TTY = strings.Join(who[1:len(who)-1], " ")
		l.From = who[len(who)-1]
	case len(who) == 2:
		// No host column at all — a console login.
		l.TTY = who[1]
	}
	l.Boot = l.User == "reboot" || l.User == "shutdown"

	// "still logged in" for a session, "still running" for a boot. Two spellings
	// of the same state, so neither is matched literally.
	rest := strings.Join(when[5:], " ")
	if strings.Contains(rest, "still") {
		l.Open = true
		return l, true
	}
	if i := strings.Index(rest, "- "); i >= 0 {
		if end, ok := lastTime(strings.Fields(rest[i+2:])); ok {
			l.Until = end
		}
	}
	return l, true
}

// lastTime reads `last -F`'s timestamp: "Sun Sep  6 20:28:40 2026".
func lastTime(f []string) (time.Time, bool) {
	if len(f) < 5 {
		return time.Time{}, false
	}
	// Parsed as local time on purpose: `last` prints in the server's zone and
	// says nothing about which one. Treating it as UTC would move every login
	// by the offset.
	t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006",
		strings.Join([]string{f[0], f[1], f[2], f[3], f[4]}, " "), time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ParseAuthAggregate reads what the awk pass counted.
func ParseAuthAggregate(out string) AuthSummary {
	sum := AuthSummary{}
	var sources, users []Count
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		n, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		switch f[0] {
		case "total":
			sum.Failed = n
			if ok, err := strconv.Atoi(f[2]); err == nil {
				sum.Accepted = ok
			}
		case "ip":
			sources = append(sources, Count{Name: f[2], Count: n})
		case "user":
			// An account name can contain a space, however unlikely. Everything
			// after the count is the name.
			users = append(users, Count{Name: strings.Join(f[2:], " "), Count: n})
		}
	}
	sum.DistinctSources, sum.DistinctUsers = len(sources), len(users)
	sum.Sources, sum.Users = topN(sources), topN(users)
	return sum
}

// topN sorts largest first and cuts. Ties break on the name so the list does
// not reshuffle between reads of the same data.
func topN(in []Count) []Count {
	sort.Slice(in, func(i, j int) bool {
		if in[i].Count != in[j].Count {
			return in[i].Count > in[j].Count
		}
		return in[i].Name < in[j].Name
	})
	if len(in) > AuthTopN {
		in = in[:AuthTopN]
	}
	return in
}
