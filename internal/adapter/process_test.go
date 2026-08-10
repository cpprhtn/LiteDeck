package adapter

import (
	"os"
	"path/filepath"
	"testing"
)

const goldenRoot = "../../testdata/golden"

func loadGolden(t *testing.T, distro, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(goldenRoot, distro, name))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	return b
}

func index(procs []ProcessInfo) map[int]ProcessInfo {
	m := make(map[int]ProcessInfo, len(procs))
	for _, p := range procs {
		m[p.PID] = p
	}
	return m
}

func TestParsePSGolden(t *testing.T) {
	procs, err := ParsePS(loadGolden(t, "ubuntu-22.04", "ps.txt"))
	if err != nil {
		t.Fatalf("ParsePS: %v", err)
	}
	if len(procs) != 11 {
		t.Fatalf("got %d processes, want 11", len(procs))
	}
	byPID := index(procs)

	init, ok := byPID[1]
	if !ok {
		t.Fatal("PID 1 missing")
	}
	if init.PPID != 0 || init.User != "root" || init.Command != "systemd" {
		t.Errorf("init = %+v", init)
	}
	if init.Args != "/lib/systemd/systemd" || init.RSS != 9196 || init.State != "Ss" {
		t.Errorf("init = %+v", init)
	}

	// args containing spaces must survive intact — it is the last column
	// precisely so it can hold anything.
	sh := byPID[37]
	if want := "/bin/sh -c while true; do sleep 30; done"; sh.Args != want {
		t.Errorf("args with spaces = %q, want %q", sh.Args, want)
	}
	sshd := byPID[41]
	if want := "sshd: /usr/sbin/sshd -D [listener] 0 of 10-100 startups"; sshd.Args != want {
		t.Errorf("sshd args = %q", sshd.Args)
	}
	if sshd.Command != "sshd" {
		t.Errorf("sshd comm = %q", sshd.Command)
	}

	// Non-root users must be attributed correctly; the view filters on this.
	if got := byPID[87].User; got != "appuser" {
		t.Errorf("appuser process user = %q", got)
	}
}

// TestParsePSZombie is the case the golden file taught us about: a zombie's
// comm is "sleep <defunct>", with a space in it, so naive field splitting
// mangles every zombie on the machine.
func TestParsePSZombie(t *testing.T) {
	procs, err := ParsePS(loadGolden(t, "ubuntu-22.04", "ps.txt"))
	if err != nil {
		t.Fatal(err)
	}
	z, ok := index(procs)[90]
	if !ok {
		t.Fatal("the zombie process is missing from the golden file")
	}
	if !z.Zombie() {
		t.Errorf("state %q not recognised as a zombie", z.State)
	}
	if z.Command != "sleep <defunct>" {
		t.Errorf("zombie comm = %q, want %q", z.Command, "sleep <defunct>")
	}
	if z.Args != "[sleep] <defunct>" {
		t.Errorf("zombie args = %q, want %q", z.Args, "[sleep] <defunct>")
	}
	if z.PPID != 89 {
		t.Errorf("zombie ppid = %d, want 89", z.PPID)
	}
}

// TestParsePSEdgeCases covers shapes the fixture cannot easily produce.
// Synthetic, and labelled as such — the golden file stays real output only.
func TestParsePSEdgeCases(t *testing.T) {
	const input = `      2       0 root                              0.0  0.0     0 S          9 kthreadd        [kthreadd]
     14       2 root                              0.0  0.0     0 I<        99 kworker/0:1H    [kworker/0:1H-kblockd]
    999       1 a-very-long-user-name-here        1.5 12.3 987654 Rl+     86400 java            /usr/bin/java -Xmx4g -cp "/opt/app/lib/*" com.example.Main --flag="a b"
   1000       1 nobody                            0.0  0.0   512 T          5 tail            tail -f /var/log/syslog
`
	procs, err := ParsePS([]byte(input))
	if err != nil {
		t.Fatalf("ParsePS: %v", err)
	}
	if len(procs) != 4 {
		t.Fatalf("got %d rows, want 4", len(procs))
	}
	byPID := index(procs)

	kt := byPID[2]
	if !kt.KernelThread() {
		t.Errorf("kthreadd not recognised as a kernel thread: %+v", kt)
	}
	if kt.Command != "kthreadd" || kt.Args != "[kthreadd]" {
		t.Errorf("kthreadd = %+v", kt)
	}

	// Quotes and equals signs in args are data, and must not be interpreted.
	j := byPID[999]
	if want := `/usr/bin/java -Xmx4g -cp "/opt/app/lib/*" com.example.Main --flag="a b"`; j.Args != want {
		t.Errorf("java args = %q", j.Args)
	}
	if j.CPU != 1.5 || j.Mem != 12.3 || j.RSS != 987654 || j.Elapsed != 86400 {
		t.Errorf("java numbers = %+v", j)
	}
	if j.User != "a-very-long-user-name-here" {
		t.Errorf("long user name = %q", j.User)
	}
	if j.State != "Rl+" {
		t.Errorf("state with modifiers = %q", j.State)
	}
}

func TestParsePSIgnoresJunk(t *testing.T) {
	procs, err := ParsePS([]byte("\n   \nnot a process row at all\n      1       0 root  0.0  0.0  100 Ss   1 systemd /sbin/init\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 1 || procs[0].PID != 1 {
		t.Errorf("got %+v, want just PID 1", procs)
	}
}

func TestTree(t *testing.T) {
	procs, err := ParsePS(loadGolden(t, "ubuntu-22.04", "ps.txt"))
	if err != nil {
		t.Fatal(err)
	}
	tree := Tree(procs)

	if len(tree) != len(procs) {
		t.Fatalf("tree has %d rows, flat list had %d — rows were lost", len(tree), len(procs))
	}

	pos := make(map[int]int, len(tree))
	for i, p := range tree {
		pos[p.PID] = i
	}
	// A child must appear after its parent and one level deeper.
	for _, p := range tree {
		if p.PPID == 0 || pos[p.PPID] == 0 && p.PPID != 1 {
			continue
		}
		parentIdx, ok := pos[p.PPID]
		if !ok {
			continue // orphan, rendered as a root
		}
		if parentIdx > pos[p.PID] {
			t.Errorf("PID %d appears before its parent %d", p.PID, p.PPID)
		}
		if p.Depth != tree[parentIdx].Depth+1 {
			t.Errorf("PID %d depth = %d, parent depth = %d", p.PID, p.Depth, tree[parentIdx].Depth)
		}
	}
	if tree[0].PID != 1 || tree[0].Depth != 0 {
		t.Errorf("tree does not start at init: %+v", tree[0])
	}
}

// TestTreeSurvivesCycles: a malformed listing must not hang the UI or drop
// rows. A frozen window is a worse failure than a wrong tree.
func TestTreeSurvivesCycles(t *testing.T) {
	procs := []ProcessInfo{
		{PID: 10, PPID: 11},
		{PID: 11, PPID: 10},
		{PID: 12, PPID: 12},
		{PID: 13, PPID: 999}, // orphan
	}
	tree := Tree(procs)
	if len(tree) != len(procs) {
		t.Errorf("tree has %d rows, want %d — a cycle swallowed some", len(tree), len(procs))
	}
	seen := map[int]bool{}
	for _, p := range tree {
		if seen[p.PID] {
			t.Errorf("PID %d appears twice", p.PID)
		}
		seen[p.PID] = true
	}
}

func TestPSArgs(t *testing.T) {
	args := PSArgs()
	if len(args) != 3 || args[0] != "-eo" || args[2] != "--no-headers" {
		t.Errorf("PSArgs() = %q", args)
	}
	// args must be last: it is the only column that can contain whitespace.
	if got := args[1]; got[len(got)-len("comm,args"):] != "comm,args" {
		t.Errorf("field list %q must end with comm,args", got)
	}
}
