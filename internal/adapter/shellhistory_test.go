package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func goldenHistory(t *testing.T) []ShellCommand {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "history", "ubuntu-24.04-bash_history"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return ReplayCd(ParseBashHistory(string(b)), "/home/deploy")
}

// One command written across five lines has to come back as one command.
//
// Read a line at a time, `docker run … sh -c '` becomes five entries, four of
// which are fragments that mean nothing on their own — and the last of them is
// a lone quote.
func TestMultiLineCommandIsOneCommand(t *testing.T) {
	cmds := goldenHistory(t)

	var found bool
	for _, c := range cmds {
		if !strings.Contains(c.Command, "mkdir -p /data/rules") {
			continue
		}
		found = true
		if !strings.HasPrefix(c.Command, "docker run") {
			t.Errorf("the command lost its head: %q", c.Command)
		}
		if !strings.Contains(c.Command, "ls -ld /data") {
			t.Errorf("the command lost its tail: %q", c.Command)
		}
	}
	if !found {
		t.Fatal("the multi-line command is missing entirely")
	}
	// And none of its middle lines became commands of their own.
	for _, c := range cmds {
		if c.Command == "'" || strings.HasPrefix(c.Command, "chown -R 10001") {
			t.Errorf("a fragment was recorded as a command: %q", c.Command)
		}
	}
}

// The measured file had none. A zero time means unknown and must not be shown
// as a date — "1970" over a command run last week is worse than no date.
func TestBashHistoryWithoutTimestampsHasNoTimes(t *testing.T) {
	for _, c := range goldenHistory(t) {
		if !c.At.IsZero() {
			t.Errorf("%q got a time out of a file that has none: %s", c.Command, c.At)
		}
	}
}

func TestBashTimestampsAreReadWhenPresent(t *testing.T) {
	cmds := ParseBashHistory("#1788000000\nsystemctl restart app\n#1788000060\nls\n")
	if len(cmds) != 2 {
		t.Fatalf("parsed %d commands, want 2", len(cmds))
	}
	if cmds[0].At.IsZero() || cmds[0].Command != "systemctl restart app" {
		t.Errorf("%+v", cmds[0])
	}
	if !cmds[1].At.After(cmds[0].At) {
		t.Error("the second command is not later than the first")
	}
	// A comment is not a timestamp.
	if got := ParseBashHistory("# note to self\nls\n"); len(got) != 2 {
		t.Errorf("a plain comment was eaten as a timestamp: %+v", got)
	}
}

// The first move in a real file is relative, because the shell already stands
// in the user's home. Without that anchor the replay produces nothing for most
// of the history.
func TestCdReplayFollowsRelativeAndAbsoluteMoves(t *testing.T) {
	cmds := goldenHistory(t)

	want := map[string]string{
		"docker restart vector":              "monitoring/vector",
		"docker build -t test-python-logs .": "test-logs",
		"sudo vi nginx.conf":                 "/etc/nginx",
	}
	for _, c := range cmds {
		for prefix, wantPath := range want {
			if strings.TrimSpace(c.Command) != prefix {
				continue
			}
			if !strings.HasSuffix(c.PWD, wantPath) {
				t.Errorf("%q ran in %q, want something ending %q", prefix, c.PWD, wantPath)
			}
		}
	}
}

// `cd $PROJECT_DIR` cannot be resolved from the text. It must leave the path
// where it was rather than moving it somewhere invented.
func TestUnresolvableCdDoesNotMoveThePath(t *testing.T) {
	cmds := ParseBashHistory("cd /srv/app\nls\ncd $PROJECT_DIR\ndocker ps\ncd -\nuptime\n")
	replayed := ReplayCd(cmds, "")
	for _, c := range replayed {
		if c.Command == "cd $PROJECT_DIR" || c.Command == "docker ps" ||
			c.Command == "cd -" || c.Command == "uptime" {
			if c.PWD != "/srv/app" {
				t.Errorf("%q ran in %q; the path should not have moved", c.Command, c.PWD)
			}
		}
	}
}

// Nothing absolute has been seen yet, so there is nothing to say.
// With no home to start from and nothing absolute yet, there is nothing to say.
func TestReplayClaimsNoPathBeforeItHasOne(t *testing.T) {
	for _, c := range ReplayCd(ParseBashHistory("ls\ndocker ps\ncd sub\nls\n"), "") {
		if c.PWD != "" {
			t.Errorf("%q was given the path %q with nothing to base it on", c.Command, c.PWD)
		}
	}
}

func TestZshExtendedHistory(t *testing.T) {
	cmds := ParseZshHistory(": 1788000000:0;systemctl restart app\n: 1788000060:3;ls -la\n")
	if len(cmds) != 2 {
		t.Fatalf("parsed %d, want 2", len(cmds))
	}
	if cmds[0].Command != "systemctl restart app" || cmds[0].At.IsZero() {
		t.Errorf("%+v", cmds[0])
	}
	// Without EXTENDED_HISTORY the file is plain lines, and both shapes appear
	// in the wild depending on whether oh-my-zsh set it.
	plain := ParseZshHistory("ls\ndocker ps\n")
	if len(plain) != 2 || plain[0].Command != "ls" || !plain[0].At.IsZero() {
		t.Errorf("plain zsh history: %+v", plain)
	}
}

func TestQuotesBalanced(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{`docker ps`, true},
		{`docker logs -f loki | grep "server listening"`, true},
		{`docker run alpine sh -c '`, false},
		{`bash -c "while true; do echo hi; done"`, true},
		{`echo "it's fine"`, true}, // apostrophe inside double quotes
		{`echo 'a "b" c'`, true},   // double inside single
		{`echo \'`, true},          // escaped, not a quote
		{`labels = ["service"]2025-08-24T08:40:11Z INFO level="info"`, true},
	} {
		if got := quotesBalanced(tc.in); got != tc.want {
			t.Errorf("%q: balanced = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// Pasted output is indistinguishable from a command, and nothing here pretends
// otherwise. It is kept, because dropping "lines that do not look like
// commands" would drop real commands somebody typed.
func TestPastedOutputIsKeptRatherThanGuessedAt(t *testing.T) {
	var found bool
	for _, c := range goldenHistory(t) {
		if strings.HasPrefix(c.Command, "labels = ") {
			found = true
		}
	}
	if !found {
		t.Error("a pasted line was silently dropped")
	}
}
