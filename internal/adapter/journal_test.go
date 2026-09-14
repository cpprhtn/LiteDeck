package adapter

import (
	"os"
	"strings"
	"testing"
	"time"
)

// The golden file is real journalctl output, captured from the systemd fixture
// after deliberately starting a unit that fails and one that crash-loops. See
// testdata/golden/journal/provenance.txt.
func loadJournalGolden(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/golden/journal/warning-ubuntu-22.04.jsonl")
	if err != nil {
		t.Fatalf("golden: %v", err)
	}
	return b
}

func TestParseJournalGolden(t *testing.T) {
	events := ParseJournal(loadJournalGolden(t))
	if len(events) == 0 {
		t.Fatal("no events parsed from real journalctl output")
	}

	var failed, startFailed int
	for _, e := range events {
		switch e.Kind {
		case EventUnitFailed:
			failed++
		case EventStartFailed:
			startFailed++
		}
		if e.At.IsZero() {
			t.Errorf("event with no timestamp: %q", e.Message)
		}
		if e.BootID == "" {
			t.Errorf("event with no boot id: %q — the timeline cannot draw reboot boundaries", e.Message)
		}
		if e.Message == "" {
			t.Error("event with no message")
		}
	}
	if failed == 0 {
		t.Error("the units that were deliberately failed did not classify as unit-failed")
	}
	if startFailed == 0 {
		t.Error("the start job that was deliberately failed did not classify as start-failed")
	}
}

// The unit name is the whole point of a row: "something failed" without saying
// what is not an answer.
func TestParseJournalCarriesTheUnit(t *testing.T) {
	events := ParseJournal(loadJournalGolden(t))
	var named int
	for _, e := range events {
		if e.Kind == EventUnitFailed && strings.HasSuffix(e.Unit, ".service") {
			named++
		}
	}
	if named == 0 {
		t.Fatalf("no failure row names a unit; got %d events", len(events))
	}
}

// Newest first — the question is "what happened", and the answer is usually the
// most recent thing.
func TestParseJournalIsNewestFirst(t *testing.T) {
	events := ParseJournal(loadJournalGolden(t))
	for i := 1; i < len(events); i++ {
		if events[i].At.After(events[i-1].At) {
			t.Fatalf("event %d is newer than the one before it", i)
		}
	}
}

// A field holding bytes that are not valid UTF-8 comes back as an array of
// numbers, not a string. Decoding only the string form drops exactly the
// entries somebody is trying to read — a program that logged a fragment of
// binary is usually a program that was going wrong.
func TestParseJournalDecodesByteArrayFields(t *testing.T) {
	// "hi\x80" — the 0x80 is what forces journalctl into the array form.
	line := `{"MESSAGE":[104,105,128],"__REALTIME_TIMESTAMP":"1787458054336680",` +
		`"PRIORITY":"3","_BOOT_ID":"abc"}`
	events := ParseJournal([]byte(line))
	if len(events) != 1 {
		t.Fatalf("%d events, want 1 — a binary message was dropped", len(events))
	}
	if got := events[0].Message; got != "hi\x80" {
		t.Errorf("message %q, want %q", got, "hi\x80")
	}
}

// A journal key that appears more than once arrives as an array of strings.
func TestParseJournalDecodesRepeatedFields(t *testing.T) {
	line := `{"MESSAGE":["one","two"],"__REALTIME_TIMESTAMP":"1787458054336680","_BOOT_ID":"b"}`
	events := ParseJournal([]byte(line))
	if len(events) != 1 || events[0].Message != "one two" {
		t.Fatalf("got %+v", events)
	}
}

// A server can fail in a way systemd has no ID for. Dropping those would make
// the timeline lie by omission.
func TestParseJournalKeepsUnknownIDs(t *testing.T) {
	line := `{"MESSAGE":"something odd","MESSAGE_ID":"0000deadbeef0000",` +
		`"__REALTIME_TIMESTAMP":"1787458054336680","PRIORITY":"3","_BOOT_ID":"b"}`
	events := ParseJournal([]byte(line))
	if len(events) != 1 {
		t.Fatalf("%d events, want 1", len(events))
	}
	if events[0].Kind != EventOther {
		t.Errorf("kind %q, want %q", events[0].Kind, EventOther)
	}
}

// One unparseable line must cost that line, not the batch. JSONL is chosen for
// exactly this.
func TestParseJournalSurvivesATruncatedLine(t *testing.T) {
	good := `{"MESSAGE":"fine","__REALTIME_TIMESTAMP":"1787458054336680","_BOOT_ID":"b"}`
	out := good + "\n{\"MESSAGE\":\"trunc" // no closing brace
	if got := len(ParseJournal([]byte(out))); got != 1 {
		t.Fatalf("%d events, want 1 — a truncated tail took the whole read with it", got)
	}
}

// Every ID in the table has to be 32 lowercase hex characters, because that is
// what journalctl emits and the lookup is exact. A typo here silently demotes
// an event to "other" and nothing else ever notices.
func TestEventIDsAreWellFormed(t *testing.T) {
	for id, kind := range eventKinds {
		if len(id) != 32 {
			t.Errorf("%s (%s): %d characters, want 32", id, kind, len(id))
		}
		if strings.ToLower(id) != id {
			t.Errorf("%s (%s): not lowercase; the lookup would miss", id, kind)
		}
		if strings.Trim(id, "0123456789abcdef") != "" {
			t.Errorf("%s (%s): not hex", id, kind)
		}
	}
}

func TestJournalArgsBounds(t *testing.T) {
	args := strings.Join(JournalArgs("-24h", 4, 10_000), " ")
	if !strings.Contains(args, "-n 500") {
		t.Errorf("limit not capped: %s", args)
	}
	if !strings.Contains(strings.Join(JournalArgs("-24h", 99, 10), " "), "-p 4") {
		t.Error("an out-of-range priority did not fall back to warning")
	}
	// The value from the UI must ride as its own argv element, never spliced
	// into a string (§3.2b).
	raw := JournalArgs("2 hours ago", 4, 10)
	var found bool
	for _, a := range raw {
		if a == "2 hours ago" {
			found = true
		}
	}
	if !found {
		t.Errorf("the since value was not passed as one argv element: %q", raw)
	}
}

// Four of the eight kinds the timeline can name were unreachable.
//
// systemd writes "Startup finished", "Shutting down", "New session" and
// "Scheduled restart job" at LOG_INFO, and the severity read asks for warning
// and worse, so EventBoot, EventShutdown, EventSession and EventRestart never
// arrived — dead code with a place in the UI. The golden capture proves it:
// testdata/golden/journal/provenance.txt records that a restart was provoked on
// purpose and the file contains no such message id.
//
// The first attempt at fixing it appended "+ MESSAGE_ID=…" to the severity
// read. That was wrong twice over and the tab was broken everywhere it could be
// reached — see TestJournalArgsAreAValidCommand. The ids now come from a second
// read with no priority filter on it.
func TestJournalArgsAskForTheInformationalKinds(t *testing.T) {
	sev := strings.Join(JournalArgs("-24h", 4, 100), " ")
	kinds := strings.Join(JournalKindArgs("-24h", 100), " ")

	if !strings.Contains(sev, "-p 4") {
		t.Fatalf("the severity floor is gone: %s", sev)
	}
	// The priority floor is the thing the second read exists to bypass. With it
	// there, the ids are filtered out again and the read returns nothing.
	if strings.Contains(kinds, "-p ") {
		t.Errorf("the kind read carries a priority floor, which removes what it asks for: %s", kinds)
	}
	for name, id := range map[string]string{
		"boot":     msgBootDone,
		"shutdown": msgShutdown,
		"session":  msgSessionNew,
		"restart":  msgRestartSched,
	} {
		if !strings.Contains(kinds, "MESSAGE_ID="+id) {
			t.Errorf("%s is never asked for, so its EventKind can never appear: %s", name, kinds)
		}
	}

	// Every kind the parser knows should be reachable: either it clears the
	// severity floor or it is asked for by id.
	asked := map[string]bool{}
	for _, id := range informationalIDs {
		asked[id] = true
	}
	for id, kind := range eventKinds {
		switch kind {
		case EventBoot, EventShutdown, EventSession, EventRestart:
			if !asked[id] {
				t.Errorf("%s is written below the floor and is not asked for by id", kind)
			}
		}
	}
}

// journalctl has to accept the command, which for a while it did not.
//
// `+` joins two field matches and must sit between them. The version that put
// one straight after the option flags produced, on every systemd there is:
//
//	"+" can only be used between terms
//
// It went unnoticed because the events tab is gated on being able to read the
// journal, and on the Debian and Ubuntu servers this was built against the
// login user cannot. On Rocky Linux 9 and CentOS Stream 9 that user can, so
// there the tab was simply broken — which is how it was reported.
func TestJournalArgsAreAValidCommand(t *testing.T) {
	for name, args := range map[string][]string{
		"severity":  JournalArgs("-24h", 4, 100),
		"kinds":     JournalKindArgs("-24h", 100),
		"no window": JournalKindArgs("", 0),
	} {
		for i, a := range args {
			if a != "+" {
				continue
			}
			// Something has to precede it, and that something has to be a
			// match rather than a flag or a flag's value.
			if i == 0 || !strings.Contains(args[i-1], "=") {
				t.Errorf("%s: \"+\" at position %d has no term before it: %s",
					name, i, strings.Join(args, " "))
			}
			if i == len(args)-1 || !strings.Contains(args[i+1], "=") {
				t.Errorf("%s: \"+\" at position %d has no term after it: %s",
					name, i, strings.Join(args, " "))
			}
		}
	}
}

// An event that answers both reads is one event.
func TestMergeEventsDropsTheDuplicate(t *testing.T) {
	at := time.Unix(1789000000, 0)
	restart := Event{At: at, Kind: EventRestart, Severity: 4, Message: "Scheduled restart job"}
	older := Event{At: at.Add(-time.Hour), Kind: EventOOM, Severity: 2, Message: "oom"}
	newer := Event{At: at.Add(time.Hour), Kind: EventBoot, Severity: 6, Message: "Startup finished"}

	got := MergeEvents([]Event{restart, older}, []Event{restart, newer})
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3 — the restart is in both reads: %+v", len(got), got)
	}
	// Newest first, across both reads.
	for i := 1; i < len(got); i++ {
		if got[i].At.After(got[i-1].At) {
			t.Errorf("out of order: %+v", got)
			break
		}
	}
}
