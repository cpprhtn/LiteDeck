package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func goldenLast(t *testing.T) []Login {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "logins", "ubuntu-24.04-last.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return ParseLast(string(b))
}

func TestLastReadsTheCapture(t *testing.T) {
	logins := goldenLast(t)

	// Seven records. The blank line and "wtmp begins ..." are not records.
	if len(logins) != 7 {
		for _, l := range logins {
			t.Logf("%+v", l)
		}
		t.Fatalf("parsed %d rows, want 7", len(logins))
	}
	for _, l := range logins {
		if l.User == "" || l.At.IsZero() {
			t.Errorf("row without a user or a time: %+v", l)
		}
	}
}

// A boot record puts two words in the tty column and the kernel version where a
// host would be. Splitting the line on whitespace and taking field 2 as the tty
// reads every one of them wrong, which is why the parser anchors on the date.
func TestBootRecordsKeepTheirShape(t *testing.T) {
	var boots []Login
	for _, l := range goldenLast(t) {
		if l.Boot {
			boots = append(boots, l)
		}
	}
	if len(boots) != 2 {
		t.Fatalf("found %d boot records, want 2", len(boots))
	}
	first := boots[0]
	if first.TTY != "system boot" {
		t.Errorf("tty = %q, want %q — the column holds two words", first.TTY, "system boot")
	}
	if first.From != "6.8.0-124-generic" {
		t.Errorf("from = %q, want the kernel version", first.From)
	}
	// "still running", not "still logged in". Same state, different words.
	if !first.Open {
		t.Error("the running boot was not read as still open")
	}
	if boots[1].Open {
		t.Error("a finished boot was read as still running")
	}
	if boots[1].Until.IsZero() {
		t.Error("a finished boot has no end time — the (523+17:18) form was not handled")
	}
}

func TestOpenSessionHasNoEndTime(t *testing.T) {
	logins := goldenLast(t)

	open := 0
	for _, l := range logins {
		if l.Open {
			open++
			if !l.Until.IsZero() {
				t.Errorf("an open session was given an end: %+v", l)
			}
		}
	}
	// One session still logged in, one boot still running.
	if open != 2 {
		t.Errorf("open = %d, want 2", open)
	}
}

func TestSessionTimesAreRead(t *testing.T) {
	for _, l := range goldenLast(t) {
		if l.User != "deploy" || l.TTY != "pts/0" || l.Open {
			continue
		}
		if l.Until.IsZero() {
			t.Errorf("closed session has no end: %+v", l)
			continue
		}
		if !l.Until.After(l.At) {
			t.Errorf("session ends before it starts: %+v", l)
		}
		if got := l.Until.Sub(l.At); got > 2*time.Hour {
			t.Errorf("session length = %v, which is not what the fixture says", got)
		}
	}
}

func TestAuthAggregateIsSummarisedNotListed(t *testing.T) {
	// The awk pass's output shape, with more distinct sources than the cut.
	const out = `total 3032 6
ip 783 45.153.34.149
ip 558 77.239.124.204
ip 497 77.239.124.179
ip 69 188.40.47.81
ip 61 171.231.192.28
ip 50 223.17.1.118
ip 50 51.75.247.232
ip 39 152.53.185.81
ip 34 4.157.250.195
ip 1 14.29.208.128
user 1138 root
user 108 admin
user 84 ubuntu
`
	sum := ParseAuthAggregate(out)

	if sum.Failed != 3032 || sum.Accepted != 6 {
		t.Errorf("failed=%d accepted=%d, want 3032 and 6", sum.Failed, sum.Accepted)
	}
	// Ten sources came in, eight survive — and the count before the cut is kept
	// so the screen can say "8 of 10" instead of implying eight is all of them.
	if sum.DistinctSources != 10 {
		t.Errorf("distinct sources = %d, want 10", sum.DistinctSources)
	}
	if len(sum.Sources) != AuthTopN {
		t.Fatalf("kept %d sources, want %d", len(sum.Sources), AuthTopN)
	}
	if sum.Sources[0].Name != "45.153.34.149" || sum.Sources[0].Count != 783 {
		t.Errorf("busiest source = %+v, want 45.153.34.149 with 783", sum.Sources[0])
	}
	for i := 1; i < len(sum.Sources); i++ {
		if sum.Sources[i].Count > sum.Sources[i-1].Count {
			t.Errorf("sources are not largest-first at %d", i)
		}
	}
	if sum.DistinctUsers != 3 || len(sum.Users) != 3 {
		t.Errorf("users: distinct=%d kept=%d, want 3 and 3", sum.DistinctUsers, len(sum.Users))
	}
	if sum.Users[0].Name != "root" {
		t.Errorf("most-tried account = %q, want root", sum.Users[0].Name)
	}
}

func TestAuthAggregateSurvivesNothingToCount(t *testing.T) {
	sum := ParseAuthAggregate("total 0 0\n")
	if sum.Failed != 0 || sum.Accepted != 0 || len(sum.Sources) != 0 {
		t.Errorf("%+v, want an empty summary", sum)
	}
}

// The script is a compile-time constant, so the thing worth pinning is that it
// stays one journalctl pass. Four passes cost four times as much, measured.
func TestLoginsScriptReadsTheJournalOnce(t *testing.T) {
	if n := strings.Count(LoginsScript, "journalctl"); n != 1 {
		t.Errorf("the script runs journalctl %d times; one pass is the design", n)
	}
	for _, want := range []string{"--no-pager", "-q", "last -F -w"} {
		if strings.Count(LoginsScript, want) == 0 {
			t.Errorf("the script no longer contains %q", want)
		}
	}
	// gawk-only features would work on the measured server and fail on Debian,
	// which ships mawk.
	for _, banned := range []string{"asort", "gensub", "strftime", "PROCINFO"} {
		if strings.Count(LoginsScript, banned) > 0 {
			t.Errorf("the script uses %q, which mawk does not have", banned)
		}
	}
}
