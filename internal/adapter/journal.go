package adapter

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The event timeline (§4.7) — what happened, rather than what is happening.
//
// The summary bar answers "is this box healthy right now". This answers the
// question people actually open a dashboard for: something broke, and when.
//
// It is deliberately not a log viewer — there is already one (§4.3). Pouring
// `journalctl -p warning` onto the screen would be a second log viewer with a
// worse filter. The value here is the selection: perhaps ten kinds of event
// explain almost every "the service died overnight", and the rest is noise.

// Well-known systemd message IDs.
//
// systemd stamps its significant events with a fixed 128-bit ID. Classifying on
// that rather than on the text is what makes this survive a systemd upgrade or
// a server running in a locale nobody here reads — the wording changes, the ID
// does not. Confirmed against `journalctl --list-catalog` on systemd 249.
const (
	msgOOMKill      = "fe6faa94e7774663a0da52717891d8ef"
	msgUnitFailed   = "d9b373ed55a64feb8242e02dbe79a49c"
	msgStartFailed  = "be02cf6855d2428ba40df7e9d022f03d"
	msgCoredump     = "fc2e22bc6ee647b6b90729ab34a250b1"
	msgRestartSched = "5eb03494b6584870a536b337290809b3"
	msgBootDone     = "b07a249cd024414a82dd00cd181378ff"
	msgShutdown     = "98268866d1d54a499c4e98921d93bc40"
	msgSessionNew   = "8d45620c1a4348dbb17410da57c60c66"
)

// EventKind is the handful of things worth a row on the timeline.
type EventKind string

const (
	// EventOOM is why this feature exists. "The service died at 3am and there
	// is nothing in its log" is nearly always answered here, and until now the
	// app held that answer and did not show it.
	EventOOM EventKind = "oom"

	EventUnitFailed  EventKind = "unit-failed"
	EventStartFailed EventKind = "start-failed"
	EventCoredump    EventKind = "coredump"

	// EventRestart on its own is routine. Several in a row is a crash loop,
	// which is why it is kept rather than filtered.
	EventRestart EventKind = "restart"

	// EventBoot and EventShutdown are the boundaries the timeline draws a line
	// across, not really events in their own right.
	EventBoot     EventKind = "boot"
	EventShutdown EventKind = "shutdown"

	EventSession EventKind = "session"

	// EventOther is anything that came back because of the priority filter but
	// carries no ID we recognise. Kept, because a server can fail in a way
	// systemd has no ID for, and dropping those would make the timeline lie by
	// omission.
	EventOther EventKind = "other"
)

// eventKinds maps a message ID onto its kind. Anything absent is EventOther.
var eventKinds = map[string]EventKind{
	msgOOMKill:      EventOOM,
	msgUnitFailed:   EventUnitFailed,
	msgStartFailed:  EventStartFailed,
	msgCoredump:     EventCoredump,
	msgRestartSched: EventRestart,
	msgBootDone:     EventBoot,
	msgShutdown:     EventShutdown,
	msgSessionNew:   EventSession,
}

// Event is one row of the timeline.
type Event struct {
	At   time.Time `json:"at"`
	Kind EventKind `json:"kind"`
	// Severity is the journal PRIORITY: 0 emerg … 7 debug. Kept as the raw
	// number because the UI sorts on it and the meaning is standard (RFC 5424).
	Severity int    `json:"severity"`
	Unit     string `json:"unit,omitempty"`
	Message  string `json:"message"`
	// BootID groups events into boots. Two adjacent rows with different IDs
	// have a reboot between them, which is the one piece of context that makes
	// "it stopped answering" and "it was restarted" tell themselves apart.
	BootID string `json:"bootId"`
}

// JournalArgs builds the argv for one timeline read.
//
// argv, never a shell string (§3.2b): `since` reaches here from the UI, and
// journalctl's own `--since` accepts free text ("2 hours ago"), so it must not
// be able to reach a shell. The caller validates the value; this only assembles.
func JournalArgs(since string, maxPriority, limit int) []string {
	if limit <= 0 || limit > journalMaxLines {
		limit = journalMaxLines
	}
	if maxPriority < 0 || maxPriority > 7 {
		maxPriority = 4 // warning
	}
	return []string{
		"-o", "json",
		"--no-pager",
		"--since", since,
		"-p", strconv.Itoa(maxPriority),
		"-n", strconv.Itoa(limit),
	}
}

// journalMaxLines bounds one read. A busy server's journal is unbounded and
// this is a timeline, not an export: past some number of rows nobody is reading
// them, and the cost is paid on the server, on the wire and in the renderer.
const journalMaxLines = 500

// ParseJournal reads journalctl's `-o json` output.
//
// The format is JSONL — one object per line, not an array — so a truncated read
// costs the last row rather than everything. Lines that do not parse are
// skipped rather than failing the batch, for the same reason.
func ParseJournal(out []byte) []Event {
	events := []Event{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw map[string]journalValue
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		e, ok := eventFrom(raw)
		if !ok {
			continue
		}
		events = append(events, e)
	}
	// Newest first: the question is "what happened", and the answer is usually
	// the most recent thing.
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].At.After(events[j].At)
	})
	return events
}

func eventFrom(raw map[string]journalValue) (Event, bool) {
	msg := raw["MESSAGE"].String()
	if msg == "" {
		return Event{}, false
	}
	at, ok := journalTime(raw["__REALTIME_TIMESTAMP"].String())
	if !ok {
		return Event{}, false
	}
	kind, known := eventKinds[strings.ToLower(raw["MESSAGE_ID"].String())]
	if !known {
		kind = EventOther
	}
	sev := 6 // info, when the field is missing
	if p, err := strconv.Atoi(raw["PRIORITY"].String()); err == nil && p >= 0 && p <= 7 {
		sev = p
	}
	unit := raw["UNIT"].String()
	if unit == "" {
		// A message from inside a unit carries _SYSTEMD_UNIT instead; systemd's
		// own messages *about* a unit carry UNIT. Both name the thing the user
		// is looking for.
		unit = raw["_SYSTEMD_UNIT"].String()
	}
	return Event{
		At:       at,
		Kind:     kind,
		Severity: sev,
		Unit:     unit,
		Message:  msg,
		BootID:   raw["_BOOT_ID"].String(),
	}, true
}

// journalTime converts journald's microseconds-since-epoch, which arrives as a
// string because the value does not survive a float64.
func journalTime(s string) (time.Time, bool) {
	usec, err := strconv.ParseInt(s, 10, 64)
	if err != nil || usec <= 0 {
		return time.Time{}, false
	}
	return time.UnixMicro(usec).UTC(), true
}

// journalValue is one field, which journalctl writes in more than one shape.
//
// Text fields are strings, but a field holding bytes that are not valid UTF-8
// comes back as an **array of numbers** instead — a log line with a stray 0x80
// in it, which happens the moment a program logs a fragment of binary. Decoding
// only the string form drops exactly the entries somebody is trying to read.
// Fields can also be an array of strings when one key appears more than once.
type journalValue struct {
	s string
}

func (v *journalValue) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		v.s = str
		return nil
	}

	var nums []int
	if err := json.Unmarshal(b, &nums); err == nil {
		buf := make([]byte, 0, len(nums))
		for _, n := range nums {
			if n < 0 || n > 255 {
				return fmt.Errorf("adapter: journal byte out of range: %d", n)
			}
			buf = append(buf, byte(n))
		}
		v.s = string(buf)
		return nil
	}

	var strs []string
	if err := json.Unmarshal(b, &strs); err == nil {
		v.s = strings.Join(strs, " ")
		return nil
	}

	// Anything else (a number, null, an object) is not a field this reads.
	// Silently empty rather than failing the whole entry.
	return nil
}

func (v journalValue) String() string { return v.s }
