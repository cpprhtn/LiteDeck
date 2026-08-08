package adapter

import "testing"

func TestParseTimersGolden(t *testing.T) {
	timers, err := ParseTimers(loadGolden(t, "timers", "list-timers.json"))
	if err != nil {
		t.Fatalf("ParseTimers: %v", err)
	}
	if len(timers) != 3 {
		t.Fatalf("got %d timers, want 3", len(timers))
	}

	byUnit := map[string]Timer{}
	for _, x := range timers {
		byUnit[x.Unit] = x
	}

	often, ok := byUnit["litedeck-often.timer"]
	if !ok {
		t.Fatal("litedeck-often.timer missing")
	}
	if often.Activates != "litedeck-often.service" {
		t.Errorf("activates = %q", often.Activates)
	}
	// systemd reports microseconds since the epoch — a 16-digit number where a
	// unix timestamp has 10. Treating them as seconds puts every timer fifty
	// thousand years out.
	if often.Next < 1_700_000_000 || often.Next > 2_000_000_000 {
		t.Errorf("Next = %d — not a plausible unix second", often.Next)
	}
	if often.NeverRun() {
		t.Errorf("a timer that has fired reports NeverRun: %+v", often)
	}

	// A timer that has never fired reports last=0, and that must stay 0 rather
	// than becoming 1970.
	backup := byUnit["litedeck-backup.timer"]
	if !backup.NeverRun() || backup.Last != 0 {
		t.Errorf("never-run timer = %+v", backup)
	}
	if backup.Next <= 0 {
		t.Errorf("a scheduled timer has no next run: %+v", backup)
	}
}

// The table form is the fallback for systemd below 246. Its timestamp columns
// contain spaces *and* can be the literal "n/a", so parsing from the left does
// not work — the unit is found by its .timer suffix instead.
func TestParseTimersTableGolden(t *testing.T) {
	timers, err := ParseTimersTable(loadGolden(t, "timers", "list-timers.txt"))
	if err != nil {
		t.Fatalf("ParseTimersTable: %v", err)
	}
	if len(timers) != 3 {
		t.Fatalf("got %d timers, want 3: %+v", len(timers), timers)
	}

	byUnit := map[string]Timer{}
	for _, x := range timers {
		byUnit[x.Unit] = x
	}
	for _, unit := range []string{
		"litedeck-often.timer", "litedeck-backup.timer", "systemd-tmpfiles-clean.timer",
	} {
		x, ok := byUnit[unit]
		if !ok {
			t.Errorf("%s missing from the table parse", unit)
			continue
		}
		if x.Activates == "" {
			t.Errorf("%s has no activates: %+v", unit, x)
		}
	}
	// The summary line must not become a row.
	for _, x := range timers {
		if x.Unit == "" || x.Unit == "timers" {
			t.Errorf("junk row parsed: %+v", x)
		}
	}
}

func TestParseTimersTableHandlesNA(t *testing.T) {
	const in = `NEXT                        LEFT       LAST                        PASSED UNIT                  ACTIVATES
n/a                         n/a        Fri 2026-08-07 01:14:17 UTC 8s ago apt-daily.timer       apt-daily.service
Fri 2026-08-07 01:29:01 UTC 14min left n/a                         n/a    logrotate.timer       logrotate.service

2 timers listed.
`
	timers, err := ParseTimersTable([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(timers) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(timers), timers)
	}
	if timers[0].Unit != "apt-daily.timer" || timers[0].Activates != "apt-daily.service" {
		t.Errorf("row 0 = %+v", timers[0])
	}
	if timers[1].Unit != "logrotate.timer" || timers[1].Activates != "logrotate.service" {
		t.Errorf("row 1 = %+v", timers[1])
	}
}

func TestParseTimersReturnsArray(t *testing.T) {
	timers, err := ParseTimers(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertArray(t, "ParseTimers(empty)", timers)

	tt, err := ParseTimersTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertArray(t, "ParseTimersTable(empty)", tt)
}

func TestListTimersArgs(t *testing.T) {
	// --all matters: a timer that is installed but not running is exactly what
	// someone debugging "why didn't the backup happen" needs to see.
	j := ListTimersArgs(true)
	if len(j) != 4 || j[1] != "--all" || j[3] != "--output=json" {
		t.Errorf("ListTimersArgs(true) = %q", j)
	}
	if p := ListTimersArgs(false); len(p) != 3 {
		t.Errorf("ListTimersArgs(false) = %q", p)
	}
}
