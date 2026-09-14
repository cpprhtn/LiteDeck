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

// informationalIDs are the kinds systemd writes below the warning floor. They
// have to be asked for by id or the priority filter removes them.
var informationalIDs = []string{msgBootDone, msgShutdown, msgSessionNew, msgRestartSched}

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

// JournalArgs builds the argv for the severity half of one timeline read.
//
// argv, never a shell string (§3.2b). `since` never carries user text either:
// journalctl's `--since` accepts free English ("2 hours ago") and validating
// that is not a job worth taking on, so the caller passes one of a closed set.
//
// `-q` is not cosmetic. journalctl prints "you are currently not seeing
// messages from other users" onto stdout, which is not JSON. `--no-pager`
// likewise: journalctl pipes through less when it believes it has a terminal,
// and the read then never returns.
//
// # Why this is only half
//
// The timeline wants warning-and-worse *plus* four informational kinds that
// systemd writes below that floor. There is no way to ask journalctl for that
// in one invocation, and the version that tried was wrong in two ways at once:
//
//	journalctl -p 4 + MESSAGE_ID=… + MESSAGE_ID=…
//
// `+` joins field matches and has to sit between two of them, so a leading one
// is a syntax error — "+" can only be used between terms, on every systemd
// there is. And even written correctly it would not have worked, because `-p`
// is not a term: it filters the whole read, so the informational ids it is
// meant to rescue would have been dropped again by the priority floor.
//
// So the caller makes two reads and merges them. See JournalKindArgs.
func JournalArgs(since string, maxPriority, limit int) []string {
	args := []string{
		"-o", "json",
		"--no-pager",
		"-q",
		"-p", strconv.Itoa(clampPriority(maxPriority)),
		"-n", strconv.Itoa(clampLimit(limit)),
	}
	return appendSince(args, since)
}

// JournalKindArgs builds the argv for the other half: the four informational
// kinds, by message id, at whatever priority systemd gave them.
//
// systemd writes "Startup finished", "Shutting down", "New session" and
// "Scheduled restart job" at LOG_INFO, below the warning floor, so the priority
// read never sees one. These are the boundaries the timeline draws its lines
// across — a run of failures reads differently either side of a reboot — which
// is why they are worth a second round trip.
//
// No `-p` here, deliberately: the priority is the thing being bypassed.
func JournalKindArgs(since string, limit int) []string {
	args := []string{
		"-o", "json",
		"--no-pager",
		"-q",
		"-n", strconv.Itoa(clampLimit(limit)),
	}
	for i, id := range informationalIDs {
		// Between terms, never before the first one.
		if i > 0 {
			args = append(args, "+")
		}
		args = append(args, "MESSAGE_ID="+id)
	}
	return appendSince(args, since)
}

func clampLimit(limit int) int {
	if limit <= 0 || limit > journalMaxLines {
		return journalMaxLines
	}
	return limit
}

func clampPriority(p int) int {
	if p < 0 || p > 7 {
		return 4 // warning
	}
	return p
}

// appendSince omits the flag rather than passing it empty: `--since ""` is not
// "no window", it is an argument journalctl rejects.
func appendSince(args []string, since string) []string {
	if since != "" {
		args = append(args, "--since", since)
	}
	return args
}

// MergeEvents folds the two reads into one timeline.
//
// Newest first, and de-duplicated: an event can answer both reads at once —
// a scheduled restart logged at warning, say — and the screen must not show it
// twice. Identity is the timestamp and the message, which is what a reader
// would use to call two rows the same thing.
func MergeEvents(a, b []Event) []Event {
	out := make([]Event, 0, len(a)+len(b))
	seen := make(map[string]bool, len(a)+len(b))
	for _, e := range append(append([]Event{}, a...), b...) {
		key := strconv.FormatInt(e.At.UnixNano(), 10) + "\x00" + e.Message
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > journalMaxLines {
		out = out[:journalMaxLines]
	}
	return out
}

// EventRange is how far back the timeline looks. A closed set, so that nothing
// the user typed ever reaches journalctl.
type EventRange string

const (
	EventRangeHour EventRange = "1h"
	EventRangeDay  EventRange = "24h"
	EventRangeWeek EventRange = "7d"
	// EventRangeMax is as far back as the record goes, bounded by line count
	// rather than by time.
	//
	// Bounded the way `history` itself is. A shell history file holds
	// HISTFILESIZE lines and no dates, so "everything" has always meant a
	// number of lines and never a span of time; matching that here keeps the
	// widest setting answerable on a server whose journal covers a year.
	EventRangeMax EventRange = "max"
)

// Since maps a range onto journalctl's own syntax, defaulting to a day.
func (r EventRange) Since() string {
	switch r {
	case EventRangeHour:
		return "-1h"
	case EventRangeWeek:
		return "-7d"
	case EventRangeMax:
		// No window. The line cap is what bounds the read — see the arg
		// builders, which drop the flag rather than pass it empty.
		return ""
	default:
		return "-24h"
	}
}

// JournalMaxLines is journalMaxLines, exported so the view can tell "this is
// everything" from "this is the first 500".
const JournalMaxLines = journalMaxLines

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
