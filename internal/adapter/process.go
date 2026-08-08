package adapter

// The process list (§4.4). Lives here rather than under linuxsystemd because
// `ps` is POSIX: every Unix adapter — systemd, OpenRC, Darwin — shares it.
//
// One `ps` invocation rather than walking /proc over SFTP: a few hundred
// processes would be a few hundred round trips, and the polling interval is two
// seconds (§3.2d).

import (
	"bufio"
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ProcessInfo is one row of the process view.
type ProcessInfo struct {
	PID     int     `json:"pid"`
	PPID    int     `json:"ppid"`
	User    string  `json:"user"`
	CPU     float64 `json:"cpu"`     // percent
	Mem     float64 `json:"mem"`     // percent
	RSS     int64   `json:"rss"`     // KiB
	State   string  `json:"state"`   // S, R, Z, Ss, S<s …
	Elapsed int64   `json:"elapsed"` // seconds since start
	Command string  `json:"command"` // comm
	Args    string  `json:"args"`    // full command line

	// Depth is set only by Tree; zero in a flat listing.
	Depth int `json:"depth,omitempty"`
}

// Zombie reports a process that has exited but whose parent has not reaped it.
func (p ProcessInfo) Zombie() bool { return strings.HasPrefix(p.State, "Z") }

// KernelThread reports a kernel thread, which ps renders in brackets. They
// cannot be killed and have no real command line.
func (p ProcessInfo) KernelThread() bool {
	return strings.HasPrefix(p.Args, "[") && strings.HasSuffix(p.Args, "]")
}

// psFields is the format string handed to ps.
//
// Machine-oriented columns only, in a fixed order, with args last because it is
// the one field that can contain anything (§3.2c). user:32 widens the column so
// long names are not truncated.
const psFields = "pid,ppid,user:32,%cpu,%mem,rss,stat,etimes,comm,args"

// PSArgs returns the argv for listing processes.
func PSArgs() []string {
	return []string{"-eo", psFields, "--no-headers"}
}

// fixedFields is how many whitespace-delimited columns precede comm.
const fixedFields = 8

// ParsePS parses the output of `ps` invoked with PSArgs.
//
// Parsing rule, which is subtler than it looks:
//
// The first eight columns are guaranteed free of whitespace — two PIDs, a POSIX
// user name, two percentages, RSS, the state flags and elapsed seconds. What
// follows is comm and then args, and comm is *not* reliably one token: a zombie
// is reported as "sleep <defunct>", with a space in it. Splitting on whitespace
// and taking the ninth field silently mangles every zombie on the machine.
//
// The state column tells us which case we are in, so it drives the split: for a
// zombie, comm runs through the "<defunct>" marker; otherwise comm is one token.
func ParsePS(data []byte) ([]ProcessInfo, error) {
	// Non-nil even when empty; see the note in container.go.
	out := []ProcessInfo{}

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}

		fields, rest := splitLeading(line, fixedFields)
		if len(fields) < fixedFields {
			continue // not a process row
		}

		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue // header or summary line
		}
		ppid, _ := strconv.Atoi(fields[1])
		cpu, _ := strconv.ParseFloat(fields[3], 64)
		mem, _ := strconv.ParseFloat(fields[4], 64)
		rss, _ := strconv.ParseInt(fields[5], 10, 64)
		state := fields[6]
		elapsed, _ := strconv.ParseInt(fields[7], 10, 64)

		comm, args := splitCommArgs(rest, state)

		out = append(out, ProcessInfo{
			PID:     pid,
			PPID:    ppid,
			User:    fields[2],
			CPU:     cpu,
			Mem:     mem,
			RSS:     rss,
			State:   state,
			Elapsed: elapsed,
			Command: comm,
			Args:    args,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("adapter: read ps output: %w", err)
	}
	return out, nil
}

const defunct = "<defunct>"

// splitCommArgs separates comm from args, given the process state.
func splitCommArgs(rest, state string) (comm, args string) {
	rest = strings.TrimLeft(rest, " \t")

	// A zombie's comm carries a "<defunct>" suffix and therefore a space.
	if strings.HasPrefix(state, "Z") {
		if i := strings.Index(rest, defunct); i >= 0 {
			end := i + len(defunct)
			return strings.TrimSpace(rest[:end]), strings.TrimSpace(rest[end:])
		}
	}

	i := strings.IndexAny(rest, " \t")
	if i < 0 {
		return rest, "" // kernel threads and exec'd-away processes have no args
	}
	return rest[:i], strings.TrimLeft(rest[i:], " \t")
}

// splitLeading pulls the first n whitespace-delimited tokens off line and
// returns them with the untouched remainder.
func splitLeading(line string, n int) ([]string, string) {
	fields := make([]string, 0, n)
	rest := line
	for range n {
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			break
		}
		end := strings.IndexAny(rest, " \t")
		if end < 0 {
			fields = append(fields, rest)
			return fields, ""
		}
		fields = append(fields, rest[:end])
		rest = rest[end:]
	}
	return fields, strings.TrimLeft(rest, " \t")
}

// Tree reorders a flat listing into parent-then-children order, setting Depth
// along the way (§4.4's tree toggle).
//
// Orphans — a process whose parent is not in the listing, which happens when the
// parent exits between the listing being taken and being parsed — are treated as
// roots rather than dropped. Losing rows would be worse than showing them flat.
func Tree(procs []ProcessInfo) []ProcessInfo {
	children := make(map[int][]ProcessInfo, len(procs))
	present := make(map[int]bool, len(procs))
	for _, p := range procs {
		present[p.PID] = true
	}
	var roots []ProcessInfo
	for _, p := range procs {
		if p.PPID == p.PID || !present[p.PPID] {
			roots = append(roots, p)
			continue
		}
		children[p.PPID] = append(children[p.PPID], p)
	}

	byPID := func(s []ProcessInfo) { sort.Slice(s, func(i, j int) bool { return s[i].PID < s[j].PID }) }
	byPID(roots)
	for k := range children {
		byPID(children[k])
	}

	out := make([]ProcessInfo, 0, len(procs))
	// Iterative depth-first walk: a corrupted listing could otherwise recurse
	// without end, and a hung UI is a worse failure than a wrong tree.
	seen := make(map[int]bool, len(procs))
	var visit func(p ProcessInfo, depth int)
	visit = func(p ProcessInfo, depth int) {
		if seen[p.PID] || depth > 64 {
			return
		}
		seen[p.PID] = true
		p.Depth = depth
		out = append(out, p)
		for _, c := range children[p.PID] {
			visit(c, depth+1)
		}
	}
	for _, r := range roots {
		visit(r, 0)
	}
	// Anything a cycle kept out still has to be shown.
	for _, p := range procs {
		if !seen[p.PID] {
			out = append(out, p)
		}
	}
	return out
}
