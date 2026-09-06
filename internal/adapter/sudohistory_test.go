package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixture is a real capture, anonymised. See
// testdata/golden/sudo/provenance.txt — the shapes in it are the point, and an
// invented fixture would have had every field on every row.
func loadSudoGolden(t *testing.T) []SudoRun {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "sudo", "ubuntu-24.04.jsonl"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return ParseSudoHistory(b)
}

func TestSudoHistoryReadsTheCapture(t *testing.T) {
	runs := loadSudoGolden(t)

	// Six of the seven lines name a command. The seventh is a PAM session
	// notice, which belongs to the same identifier and says nothing about what
	// anybody ran.
	if len(runs) != 6 {
		for _, r := range runs {
			t.Logf("%s %s %s", r.At.Format("15:04:05"), r.PWD, r.Command)
		}
		t.Fatalf("parsed %d runs, want 6", len(runs))
	}

	// Newest first.
	for i := 1; i < len(runs); i++ {
		if runs[i].At.After(runs[i-1].At) {
			t.Errorf("row %d is newer than the one before it", i)
		}
	}

	// The working directory is the reason this source exists, so every row that
	// named a command has to carry one.
	for _, r := range runs {
		if r.PWD == "" {
			t.Errorf("no PWD on %q — the fact this source is for", r.Command)
		}
		if r.User == "" {
			t.Errorf("no user on %q", r.Command)
		}
	}
}

// The metadata journald fills from /proc is missing on most rows, because sudo
// has exited by the time it looks. Nothing may depend on it.
func TestSudoHistoryDoesNotNeedTheProcMetadata(t *testing.T) {
	runs := loadSudoGolden(t)

	// The fixture's rows are deliberately uneven: only one carries _COMM,
	// _EXE and _SYSTEMD_UNIT. If the parser had come to rely on them this
	// would be 1, not 6.
	if len(runs) != 6 {
		t.Fatalf("parsed %d runs, want 6 — a row was dropped for missing metadata", len(runs))
	}
}

func TestSudoHistorySeparatesRefusalsFromRuns(t *testing.T) {
	runs := loadSudoGolden(t)

	refused, ran := 0, 0
	for _, r := range runs {
		if r.Refused {
			refused++
			if r.Reason == "" {
				t.Errorf("refused %q without saying why", r.Command)
			}
			// The refusal sits where TTY would, and must not be read as one.
			if r.TTY != "" {
				t.Errorf("refusal parsed a TTY out of the reason: %+v", r)
			}
			continue
		}
		ran++
	}
	if refused != 2 {
		t.Errorf("refused = %d, want 2", refused)
	}
	if ran != 4 {
		t.Errorf("ran = %d, want 4", ran)
	}
}

// A command may contain the separator sudo uses between its own fields. Taking
// COMMAND as one more " ; " field truncates it at the first semicolon, which
// silently loses the tail of exactly the rows worth reading.
func TestCommandKeepsItsOwnSemicolons(t *testing.T) {
	runs := loadSudoGolden(t)

	var found bool
	for _, r := range runs {
		if !strings.Contains(r.Command, "deploy.sh") {
			continue
		}
		found = true
		if !strings.Contains(r.Command, ";") {
			t.Errorf("command was cut at its own separator: %q", r.Command)
		}
		if r.PWD != "/srv/app" {
			t.Errorf("PWD = %q, want /srv/app", r.PWD)
		}
	}
	if !found {
		t.Fatal("the row with a semicolon in its command is missing")
	}
}

// Two rows in the fixture come from different boots. Without that the history
// draws a straight line through a restart.
func TestSudoHistoryCarriesTheBootID(t *testing.T) {
	runs := loadSudoGolden(t)

	boots := map[string]bool{}
	for _, r := range runs {
		if r.BootID == "" {
			t.Errorf("no boot id on %q", r.Command)
		}
		boots[r.BootID] = true
	}
	if len(boots) != 2 {
		t.Errorf("found %d boots, want 2", len(boots))
	}
}

func TestCommandsAreSortedByWhatTheyChanged(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		want SudoEffect
	}{
		{"/usr/bin/systemctl reload nginx", SudoChange},
		{"/usr/bin/systemctl restart myapp", SudoChange},
		// The verb is what separates these, not the program.
		{"/usr/bin/systemctl status myapp", SudoRead},
		{"systemctl --no-pager status myapp", SudoRead},
		{"/usr/bin/docker logs web", SudoRead},
		{"/usr/bin/docker compose up -d", SudoChange},
		{"git status", SudoRead},
		{"git pull", SudoChange},
		{"/usr/bin/vi /etc/nginx/nginx.conf", SudoEdit},
		{"nano /etc/hosts", SudoEdit},
		{"/usr/bin/tail -f /var/log/syslog", SudoRead},
		{"/bin/rm -rf /tmp/x", SudoChange},
		{"apt install nginx", SudoChange},
		{"apt list --installed", SudoRead},
		// Unknown stays visible. Folding it in with the reads would hide a
		// change; showing it among the changes costs one line.
		{"/opt/acme/bin/deploy", SudoChange},
		{"", SudoChange},
	} {
		if got := ClassifyCommand(tc.cmd); got != tc.want {
			t.Errorf("%q: effect = %q, want %q", tc.cmd, got, tc.want)
		}
	}
}

func TestSecretsAreMasked(t *testing.T) {
	for _, tc := range []struct {
		in    string
		found bool
		gone  string // must not survive
	}{
		{"mysql -uroot -pHUNTER2 mydb", true, "HUNTER2"},
		{"curl -H 'Authorization: Bearer ey.JJ.tok' https://x", true, "ey.JJ.tok"},
		{"docker login -u me --password s3cret", true, "s3cret"},
		{"export AWS_SECRET_ACCESS_KEY=abc123xyz", true, "abc123xyz"},
		{"./deploy.sh --token=s3cr3tvalue", true, "s3cr3tvalue"},
		{"systemctl restart nginx", false, ""},
		{"ls -la /etc", false, ""},
	} {
		got, found := MaskSecrets(tc.in)
		if found != tc.found {
			t.Errorf("%q: found = %v, want %v (got %q)", tc.in, found, tc.found, got)
		}
		if tc.gone != "" && strings.Contains(got, tc.gone) {
			t.Errorf("%q: the secret survived masking: %q", tc.in, got)
		}
	}
}

// A shell history is the densest credential file on a server, and this is a
// slice of the same thing. The fixture carries one so the masking is exercised
// against a real-shaped row rather than only against invented ones.
func TestTheCaptureItselfHasSomethingWorthMasking(t *testing.T) {
	runs := loadSudoGolden(t)

	var any bool
	for _, r := range runs {
		if _, found := MaskSecrets(r.Command); found {
			any = true
		}
	}
	if !any {
		t.Error("no row in the fixture trips the masking — it no longer covers the case it was captured for")
	}
}

func TestSudoHistoryArgsAreBounded(t *testing.T) {
	args := strings.Join(SudoHistoryArgs("-7d", 9999), " ")
	if !strings.Contains(args, "-t sudo") {
		t.Errorf("args do not select sudo: %s", args)
	}
	if !strings.Contains(args, "-q") || !strings.Contains(args, "--no-pager") {
		t.Errorf("journalctl would print its notice into the output, or wait on a pager: %s", args)
	}
	if strings.Contains(args, "9999") {
		t.Errorf("the caller's limit was not capped: %s", args)
	}
	if strings.Contains(strings.Join(SudoHistoryArgs("", 10), " "), "--since") {
		t.Error("an empty range still passed --since")
	}
}
