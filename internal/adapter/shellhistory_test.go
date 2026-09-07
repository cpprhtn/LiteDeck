package adapter

import (
	"encoding/json"
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
		if c.At != nil {
			t.Errorf("%q got a time out of a file that has none: %s", c.Command, c.At)
		}
	}
}

func TestBashTimestampsAreReadWhenPresent(t *testing.T) {
	cmds := ParseBashHistory("#1788000000\nsystemctl restart app\n#1788000060\nls\n")
	if len(cmds) != 2 {
		t.Fatalf("parsed %d commands, want 2", len(cmds))
	}
	if cmds[0].At == nil || cmds[0].Command != "systemctl restart app" {
		t.Errorf("%+v", cmds[0])
	}
	if cmds[1].At == nil || !cmds[1].At.After(*cmds[0].At) {
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

// With no home and no absolute move yet there is no path — but the relative
// tail is still worth carrying. `…/src` narrows it down; the leading slash is
// what says the whole path is known, and a fragment never gets one or claims
// to be certain.
func TestUnanchoredReplayKeepsTheTailAndNotTheClaim(t *testing.T) {
	got := ReplayCd(ParseBashHistory("ls\ndocker ps\ncd sub\nls\n"), "")
	for _, c := range got[:2] {
		if c.PWD != "" {
			t.Errorf("%q 는 아직 아무 근거도 없는데 경로 %q 를 받았다", c.Command, c.PWD)
		}
	}
	for _, c := range got[2:] {
		if c.PWD != "sub" {
			t.Errorf("%q → %q, 기대 %q", c.Command, c.PWD, "sub")
		}
		if strings.HasPrefix(c.PWD, "/") {
			t.Errorf("조각인데 절대경로처럼 보인다: %q", c.PWD)
		}
		if c.PWDCertain {
			t.Errorf("%q 의 경로가 조각인데 확신한다고 되어 있다", c.Command)
		}
	}
}

func TestZshExtendedHistory(t *testing.T) {
	cmds := ParseZshHistory(": 1788000000:0;systemctl restart app\n: 1788000060:3;ls -la\n")
	if len(cmds) != 2 {
		t.Fatalf("parsed %d, want 2", len(cmds))
	}
	if cmds[0].Command != "systemctl restart app" || cmds[0].At == nil {
		t.Errorf("%+v", cmds[0])
	}
	// Without EXTENDED_HISTORY the file is plain lines, and both shapes appear
	// in the wild depending on whether oh-my-zsh set it.
	plain := ParseZshHistory("ls\ndocker ps\n")
	if len(plain) != 2 || plain[0].Command != "ls" || plain[0].At != nil {
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

// The measured file's 321 `cd`s were all forms the replay can follow: 274
// relative, 26 absolute, 21 bare. Marking those as guesses threw away what the
// replay knew, so the certainty is per line and only a real failure spoils it.
func TestReplayIsCertainWhileItCanFollow(t *testing.T) {
	cmds := goldenHistory(t)

	// The fixture has exactly one form the replay cannot follow — `cd
	// $PROJECT_DIR`, added on purpose. Everything before it is certain.
	spoiled := false
	certainBefore := 0
	for _, c := range cmds {
		if strings.Contains(c.Command, "$PROJECT_DIR") {
			spoiled = true
		}
		if !spoiled && !c.PWDCertain {
			t.Errorf("%q was marked uncertain before anything defeated the replay", c.Command)
		}
		if !spoiled {
			certainBefore++
		}
		// An absolute `cd` puts it back on solid ground, and everything after
		// that is certain again — which is the point of tracking it per line
		// rather than condemning the whole file.
		if strings.HasPrefix(c.Command, "cd /") {
			spoiled = false
		}
	}
	if certainBefore == len(cmds) {
		t.Fatal("the fixture no longer contains the unfollowable cd this test is about")
	}
	if certainBefore < 15 {
		t.Errorf("only %d rows were certain; the replay follows more than that", certainBefore)
	}
}

func TestUnresolvableCdSpoilsTheRunUntilAnAbsoluteOne(t *testing.T) {
	cmds := ReplayCd(ParseBashHistory(
		"cd /srv/app\nls\ncd $DEPLOY\ndocker ps\ncd /etc/nginx\nnginx -t\n"), "/home/deploy")

	want := map[string]struct {
		pwd     string
		certain bool
	}{
		"ls":        {"/srv/app", true},
		"docker ps": {"/srv/app", false}, // the shell moved; this cannot say where
		"nginx -t":  {"/etc/nginx", true},
	}
	for _, c := range cmds {
		w, ok := want[c.Command]
		if !ok {
			continue
		}
		if c.PWD != w.pwd || c.PWDCertain != w.certain {
			t.Errorf("%q: pwd=%q certain=%v, want %q and %v",
				c.Command, c.PWD, c.PWDCertain, w.pwd, w.certain)
		}
	}
}

func TestBareCdGoesHome(t *testing.T) {
	cmds := ReplayCd(ParseBashHistory("cd /var/log\nls\ncd\npwd\n"), "/home/deploy")
	for _, c := range cmds {
		if c.Command == "pwd" && c.PWD != "/home/deploy" {
			t.Errorf("bare cd left the path at %q, want home", c.PWD)
		}
	}
}

func TestCdDashGoesBack(t *testing.T) {
	cmds := ReplayCd(ParseBashHistory("cd /srv/app\ncd /etc/nginx\ncd -\npwd\n"), "/home/deploy")
	for _, c := range cmds {
		if c.Command == "pwd" {
			if c.PWD != "/srv/app" {
				t.Errorf("`cd -` landed in %q, want /srv/app", c.PWD)
			}
			if !c.PWDCertain {
				t.Error("`cd -` is followable and should not spoil the run")
			}
		}
	}
}

func TestPushdPopd(t *testing.T) {
	cmds := ReplayCd(ParseBashHistory(
		"cd /srv/app\npushd /etc/nginx\nnginx -t\npopd\nmake\n"), "/home/deploy")
	for _, c := range cmds {
		switch c.Command {
		case "nginx -t":
			if c.PWD != "/etc/nginx" {
				t.Errorf("pushd landed in %q", c.PWD)
			}
		case "make":
			if c.PWD != "/srv/app" {
				t.Errorf("popd returned to %q, want /srv/app", c.PWD)
			}
		}
	}
}

// A `cd` in a subshell or after a pipe does not move this shell.
func TestOnlyTheLeadingCdMoves(t *testing.T) {
	cmds := ReplayCd(ParseBashHistory(
		"cd /srv/app\n(cd /tmp && ls)\npwd\ncd build && make\nwhoami\n"), "/home/deploy")
	for _, c := range cmds {
		switch c.Command {
		case "pwd":
			if c.PWD != "/srv/app" {
				t.Errorf("a subshell cd moved the outer shell to %q", c.PWD)
			}
		case "whoami":
			// `cd build && make` does move it.
			if c.PWD != "/srv/app/build" {
				t.Errorf("`cd x && …` did not move the path: %q", c.PWD)
			}
		}
	}
}

func TestQuotedLiteralIsFollowable(t *testing.T) {
	cmds := ReplayCd(ParseBashHistory("cd /srv\ncd \"my dir\"\npwd\n"), "")
	for _, c := range cmds {
		if c.Command == "pwd" {
			if c.PWD != "/srv/my dir" {
				t.Errorf("quoted literal landed in %q", c.PWD)
			}
			if !c.PWDCertain {
				t.Error("a quoted literal is still a literal")
			}
		}
	}
	// Expansion inside the quotes is not.
	spoiled := ReplayCd(ParseBashHistory("cd /srv\ncd \"$D\"\npwd\n"), "")
	for _, c := range spoiled {
		if c.Command == "pwd" && c.PWDCertain {
			t.Error("`cd \"$D\"` was treated as followable")
		}
	}
}

// A time that is not known must be absent on the wire, not zero.
//
// `omitempty` does nothing for a struct, so a zero time.Time marshals to
// "0001-01-01T00:00:00Z" — a real date as far as any reader is concerned, and
// the screen rendered it as "24662 months ago". The harness never caught it
// because a hand-written fixture leaves the field out; only Go puts a year 1 in
// there.
func TestAbsentTimeIsAbsentOnTheWire(t *testing.T) {
	b, err := json.Marshal(ParseBashHistory("docker ps\n")[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "0001-01-01") {
		t.Errorf("an unknown time went out as a date: %s", b)
	}
	if strings.Contains(string(b), `"at"`) {
		t.Errorf("the field is present for a command with no time: %s", b)
	}

	timed, err := json.Marshal(ParseBashHistory("#1788000000\ndocker ps\n")[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(timed), `"at"`) {
		t.Errorf("a command with a time lost it: %s", timed)
	}
}

// Cases the replay is expected to get right, gathered by walking a real
// history line by line. Table-driven because the failures are individual
// rather than structural: one operand form is wrong, not the walk.
func TestReplayOperandForms(t *testing.T) {
	const home = "/home/deploy"
	for _, tc := range []struct {
		name    string
		lines   []string
		wantEnd string
		certain bool
	}{
		{"상대", []string{"cd a", "cd b"}, "/home/deploy/a/b", true},
		{"절대", []string{"cd a", "cd /etc/nginx"}, "/etc/nginx", true},
		{"뒤로", []string{"cd a/b/c", "cd ../.."}, "/home/deploy/a", true},
		{"슬래시로 끝남", []string{"cd monitoring/"}, "/home/deploy/monitoring", true},
		{"점 하나", []string{"cd a", "cd ."}, "/home/deploy/a", true},
		{"틸드", []string{"cd /etc", "cd ~"}, home, true},
		{"틸드 하위", []string{"cd /etc", "cd ~/app"}, "/home/deploy/app", true},
		{"루트", []string{"cd /"}, "/", true},
		{"루트에서 상대", []string{"cd /", "cd etc"}, "/etc", true},
		{"홈 위로 둘", []string{"cd ../.."}, "/", true},
		{"다른 사용자 홈", []string{"cd ~root"}, "", false},
		{"맨 pushd", []string{"cd a", "pushd"}, "/home/deploy/a", false},
		{"cd 두 인자", []string{"cd a b"}, home, false},
		{"cd --", []string{"cd -- a"}, "/home/deploy/a", true},
		{"환경변수", []string{"cd $D"}, home, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmds := make([]ShellCommand, len(tc.lines))
			for i, l := range tc.lines {
				cmds[i] = ShellCommand{Command: l}
			}
			got := ReplayCd(cmds, home)
			last := got[len(got)-1]
			if tc.wantEnd != "" && last.PWD != tc.wantEnd {
				t.Errorf("경로 %q, 기대 %q", last.PWD, tc.wantEnd)
			}
			if last.PWDCertain != tc.certain {
				t.Errorf("확신 %v, 기대 %v (경로 %q)", last.PWDCertain, tc.certain, last.PWD)
			}
		})
	}
}

// The app's terminal and the history file must spell the same directory the
// same way, or the tree grows two branches for one place. They share a walker
// now; this is what would catch them being split again.
func TestTrackerAndReplayAgreeOnSpelling(t *testing.T) {
	const home = "/home/deploy"
	for _, line := range []string{"cd ~", "cd ~/app", "cd app", "cd /etc", "cd", "cd ../.."} {
		replayed := ReplayCd([]ShellCommand{{Command: line}}, home)[0].PWD
		tr := NewCdTracker(home, home)
		tr.Step(line)
		tracked, _ := tr.Here()
		if tracked != replayed {
			t.Errorf("%q → 터미널 %q, 이력 %q — 같은 곳이 두 갈래가 된다", line, tracked, replayed)
		}
	}
}

// The terminal used a second, smaller cd parser. Anything the walker follows
// and it did not left the tracked path stale *and still marked certain*, which
// is the one state that misleads rather than merely disappoints.
func TestTrackerFollowsWhatTheReplayFollows(t *testing.T) {
	const home = "/home/deploy"
	for _, tc := range []struct {
		line    string
		wantEnd string
		certain bool
	}{
		{"cd app", "/home/deploy/app", true},
		{"cd -", "/tmp", true}, // 직전이 /tmp 였다
		{"cd $DEPLOY_DIR", "", false},
		{"cd a b", "", false},
		{"pushd", "", false},
		{"cd 'my dir'", "/home/deploy/my dir", true},
		{"cd app && make", "/home/deploy/app", true},
		{"(cd app && make)", "/home/deploy", true},
	} {
		tr := NewCdTracker(home, home)
		tr.Step("cd /tmp")
		tr.Step("cd " + home)
		tr.Step(tc.line)
		got, certain := tr.Here()
		if tc.wantEnd != "" && got != tc.wantEnd {
			t.Errorf("%q → %q, 기대 %q", tc.line, got, tc.wantEnd)
		}
		if certain != tc.certain {
			t.Errorf("%q → 확신 %v, 기대 %v (경로 %q)", tc.line, certain, tc.certain, got)
		}
	}
}

// An unreadable line may have been a cd, so it costs the path its confidence
// without moving it.
func TestTrackerBlindLineKeepsThePathAndDropsCertainty(t *testing.T) {
	tr := NewCdTracker("/home/deploy", "/home/deploy")
	tr.Step("cd app")
	tr.Blind()
	got, certain := tr.Here()
	if got != "/home/deploy/app" {
		t.Errorf("경로가 움직였다: %q", got)
	}
	if certain {
		t.Error("못 읽은 줄이 지나갔는데 확신이 남아 있다")
	}
}

// A history file is several sessions concatenated, not one shell.
//
// bash appends a session's lines when that shell *exits*, so the file is
// ordered by when sessions ended and every session began at home. Walked as one
// continuous shell, the second session's `cd project` lands inside the first
// session's last directory — and a file with four sessions that each start
// `cd project` produces project/project/project/project, a path that does not
// exist on any server.
func TestSessionRestartDoesNotNestIntoItself(t *testing.T) {
	const home = "/home/deploy"
	exists := func(p string) bool {
		switch p {
		case home, home + "/project", home + "/project/src":
			return true
		}
		return false
	}
	lines := []string{
		"cd project", "make", // session 1
		"cd project", "make", // session 2, from home again
		"cd project", "cd src", "make", // session 3
	}
	cmds := make([]ShellCommand, len(lines))
	for i, l := range lines {
		cmds[i] = ShellCommand{Command: l}
	}
	got := ReplayCdChecked(cmds, home, exists)
	want := []string{
		home + "/project", home + "/project",
		home + "/project", home + "/project",
		home + "/project", home + "/project/src", home + "/project/src",
	}
	for i := range want {
		if got[i].PWD != want[i] {
			t.Errorf("%d번째 %q → %q, 기대 %q", i, lines[i], got[i].PWD, want[i])
		}
	}
}
