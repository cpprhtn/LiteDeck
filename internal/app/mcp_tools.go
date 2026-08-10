package app

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/cpprhtn/LiteDeck/internal/mcp"
)

// The read-only tool set (§5.1 of the MCP design note).
//
// Every tool here wraps a binding the GUI already calls, so an AI request takes
// the same adapter, the same SSH connection and the same Command Log as a click.
// Nothing bypasses that path; there is no SSH access in this file at all.
//
// # Two things shape these signatures
//
// **Everything returned enters a model's context.** The GUI can hand a virtual
// list ten thousand rows and let the user scroll; a tool that does the same
// wastes the context the model needs to reason with. So the list tools cap and
// filter, and say when they truncated rather than quietly cutting.
//
// **Every call is a real command on someone's server.** So health_snapshot
// exists: the question is nearly always "is this box alright", and answering it
// with one call rather than six is the difference between a tool that is polite
// to a 512 MB VPS and one that is not.

// Row caps. Chosen so a full answer stays inside a few thousand tokens.
const (
	maxProcessRows = 40
	// Sixty rather than eighty: an unfiltered listing is somebody browsing for
	// a name, and the page note tells them how to narrow. A real server has
	// hundreds of units and no answer needs all of them at once.
	maxServiceRows = 60
	maxListenRows  = 60
	maxDirRows     = 200
	maxFileBytes   = 64 * 1024
	maxLogLines    = 300
)

// mcpHost resolves the hostId argument, enforcing the per-connection opt-in.
//
// Refusing by default is the point: registering a server in LiteDeck must not
// be what hands it to an AI. The error names the app as the only place to
// change that, so a model cannot read it as "ask the user to pass a flag".
func (a *App) mcpHost(args map[string]any) (string, error) {
	id, _ := args["hostId"].(string)
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("hostId is required. Call hosts_list to see which servers are available")
	}
	h, ok := a.hosts.Get(id)
	if !ok {
		return "", fmt.Errorf("no host with ID %q. Call hosts_list for the current list", id)
	}
	if !a.mcpAllowed(id) {
		return "", fmt.Errorf(
			"%q has not been shared with AI clients. The user turns this on per server "+
				"in LiteDeck's settings; it cannot be enabled from here", h.Label())
	}
	return id, nil
}

func str(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

func num(args map[string]any, key string, def, max int) int {
	f, ok := args[key].(float64)
	if !ok || int(f) <= 0 {
		return def
	}
	if n := int(f); n < max {
		return n
	}
	return max
}

// Everything a tool returns is spent from the model's context, so these shape
// the adapters' structs into what a model can act on and nothing else.
//
// The GUI structs are the wrong shape here and it is not a small difference:
// measured against a real Ubuntu server, svc_list returned 13KB where the
// useful part was under 4KB. A field that is constant across every row (load
// "loaded"), a field that restates another (mode and perm), or one nothing can
// be done with (uid 1000 with no name to resolve it against) is pure cost.
//
// The rule applied throughout: emit a key only when it says something. An
// absent key reads as "nothing to report" to a model just as well as a null,
// and costs nothing.

func put(m map[string]any, key string, v any) {
	switch t := v.(type) {
	case string:
		if t == "" {
			return
		}
	case int:
		if t == 0 {
			return
		}
	case int64:
		if t == 0 {
			return
		}
	case bool:
		if !t {
			return
		}
	}
	m[key] = v
}

// truncated annotates a capped list so the model knows it is not seeing
// everything, and knows how to narrow rather than asking again for more.
func truncated(shown, total int, hint string) map[string]any {
	m := map[string]any{"shown": shown, "total": total}
	if shown < total {
		m["truncated"] = true
		m["hint"] = hint
	}
	return m
}

// registerMCPTools installs the read-only tool set.
func (a *App) registerMCPTools(s *mcp.Server) {
	obj := func(props map[string]any, required ...string) map[string]any {
		schema := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}
	hostArg := map[string]any{
		"type":        "string",
		"description": "Host ID from hosts_list. There is no default; naming the wrong server reads the wrong machine.",
	}
	grepArg := map[string]any{
		"type":        "string",
		"description": "Case-insensitive substring filter. Use it before raising any limit.",
	}

	s.Register(mcp.Tool{
		Name: "hosts_list",
		Description: "List the servers this LiteDeck can reach, and which of them the user has " +
			"shared with AI clients. Start here: every other tool needs a hostId.",
		InputSchema: obj(map[string]any{}),
		Handler: func(_ context.Context, _ map[string]any) (any, error) {
			views := a.ListHosts()
			out := make([]map[string]any, 0, len(views))
			for _, h := range views {
				// A host that is not shared is still listed, or a model asked
				// about "prod" would answer that there is no such server. But
				// its address and connection state are of no use to something
				// that cannot read it, so they are left off.
				if !a.mcpAllowed(h.ID) {
					out = append(out, map[string]any{
						"hostId": h.ID, "label": h.Label(), "sharedWithAI": false,
					})
					continue
				}
				out = append(out, map[string]any{
					"hostId":       h.ID,
					"label":        h.Label(),
					"address":      h.Addr(),
					"user":         h.User,
					"state":        h.State,
					"sharedWithAI": true,
				})
			}
			return map[string]any{
				"hosts": out,
				"note": "Only hosts with sharedWithAI true can be read. The user changes that " +
					"in LiteDeck; it cannot be changed from here.",
			}, nil
		},
	})

	s.Register(mcp.Tool{
		Name: "health_snapshot",
		Description: "One call for 'is this server alright': CPU, memory, disk, failed units, " +
			"non-running containers and externally reachable ports. Prefer this over calling the " +
			"narrower tools one by one — it is fewer round trips on a machine that may be small.",
		InputSchema: obj(map[string]any{"hostId": hostArg}, "hostId"),
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			id, err := a.mcpHost(args)
			if err != nil {
				return nil, err
			}
			return a.healthSnapshot(id)
		},
	})

	s.Register(mcp.Tool{
		Name: "sys_stats",
		Description: "CPU, memory, load and filesystem usage. health_snapshot already includes " +
			"this; use it alone when that is all you need.",
		InputSchema: obj(map[string]any{"hostId": hostArg}, "hostId"),
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			id, err := a.mcpHost(args)
			if err != nil {
				return nil, err
			}
			return a.HostMetrics(id)
		},
	})

	s.Register(mcp.Tool{
		Name: "svc_list",
		Description: "systemd units, or Windows services. Filter with state='failed' when " +
			"diagnosing; the unfiltered list on a real server is hundreds of rows.",
		InputSchema: obj(map[string]any{
			"hostId": hostArg,
			"state": map[string]any{
				"type": "string", "enum": []string{"all", "running", "failed"},
				"description": "Defaults to all.",
			},
			"grep": grepArg,
		}, "hostId"),
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			id, err := a.mcpHost(args)
			if err != nil {
				return nil, err
			}
			units, err := a.ListServices(id)
			if err != nil {
				return nil, err
			}
			state, grep := str(args, "state"), strings.ToLower(str(args, "grep"))
			kept := units[:0:0]
			for _, u := range units {
				switch state {
				case "failed":
					if u.Active != "failed" {
						continue
					}
				case "running":
					if u.Sub != "running" {
						continue
					}
				}
				if grep != "" && !strings.Contains(strings.ToLower(u.Name+" "+u.Description), grep) {
					continue
				}
				kept = append(kept, u)
			}
			total := len(kept)
			if len(kept) > maxServiceRows {
				kept = kept[:maxServiceRows]
			}
			out := make([]map[string]any, 0, len(kept))
			for _, u := range kept {
				row := map[string]any{"name": u.Name}
				// active and sub are always read together ("active/running"), so
				// one composite costs a key less per row across eighty rows.
				state := u.Active
				if u.Sub != "" {
					state += "/" + u.Sub
				}
				put(row, "state", state)
				put(row, "enabled", u.Enabled)
				// Plenty of units describe themselves by restating their own
				// name ("auditd.service" → "auditd.service", "ModemManager"
				// → "Modem Manager"). Only descriptions that say something the
				// name does not are worth carrying — and the test for that has
				// to be exact equality, because a loose one throws away real
				// ones like apparmor's "Load AppArmor profiles".
				if !restatesName(u.Name, u.Description) {
					put(row, "description", u.Description)
				}
				// "loaded" is what almost every listed unit says. Only the
				// exceptions — not-found, masked — are worth a key.
				if u.Load != "" && u.Load != "loaded" {
					row["load"] = u.Load
				}
				out = append(out, row)
			}
			return map[string]any{
				"units": out,
				"page":  truncated(len(out), total, "narrow with state or grep"),
			}, nil
		},
	})

	s.Register(mcp.Tool{
		Name: "svc_logs",
		Description: "Read a systemd unit's journal. This is what says *why* a service failed; " +
			"svc_list only says that it did. Narrow with since and priority before raising lines.",
		InputSchema: obj(map[string]any{
			"hostId": hostArg,
			"unit": map[string]any{
				"type":        "string",
				"description": "Unit name, e.g. nginx.service. Get exact names from svc_list.",
			},
			"lines": map[string]any{"type": "integer", "description": "Default 200, max 500."},
			"since": map[string]any{
				"type":        "string",
				"description": "journalctl syntax: \"-10m\", \"1 hour ago\", \"2026-08-10 09:00\".",
			},
			"priority": map[string]any{
				"type": "string", "enum": []string{"err", "warning", "info", "debug"},
				"description": "Keep this level and worse. Start with err when diagnosing.",
			},
			"grep": grepArg,
		}, "hostId", "unit"),
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			id, err := a.mcpHost(args)
			if err != nil {
				return nil, err
			}
			unit := str(args, "unit")
			if unit == "" {
				return nil, fmt.Errorf("unit is required. Use svc_list to find the exact name")
			}
			since := str(args, "since")
			if !safeJournalArg(since) {
				return nil, fmt.Errorf("since %q is not a time expression journalctl accepts", since)
			}
			out, err := a.ServiceLogTail(id, unit, num(args, "lines", 200, 500), since, str(args, "priority"))
			if err != nil {
				return nil, err
			}
			return logResult(unit, out, str(args, "grep")), nil
		},
	})

	s.Register(mcp.Tool{
		Name: "container_logs",
		Description: "Read the tail of a container's log, the container equivalent of svc_logs. " +
			"Get IDs and names from container_list.",
		InputSchema: obj(map[string]any{
			"hostId": hostArg,
			"id": map[string]any{
				"type":        "string",
				"description": "Container ID or name from container_list.",
			},
			"lines": map[string]any{"type": "integer", "description": "Default 200, max 500."},
			"grep":  grepArg,
		}, "hostId", "id"),
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			hostID, err := a.mcpHost(args)
			if err != nil {
				return nil, err
			}
			id := str(args, "id")
			if id == "" {
				return nil, fmt.Errorf("id is required. Use container_list to find one")
			}
			out, err := a.ContainerLogs(hostID, id, num(args, "lines", 200, 500))
			if err != nil {
				return nil, err
			}
			return logResult(id, out, str(args, "grep")), nil
		},
	})

	s.Register(mcp.Tool{
		Name: "proc_list",
		Description: "Running processes, heaviest first. Use grep for a known name rather than " +
			"raising limit.",
		InputSchema: obj(map[string]any{
			"hostId": hostArg,
			"sort": map[string]any{
				"type": "string", "enum": []string{"cpu", "mem", "pid"},
				"description": "Defaults to cpu.",
			},
			"limit": map[string]any{"type": "integer", "description": "Default 20, max 40."},
			"grep":  grepArg,
		}, "hostId"),
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			id, err := a.mcpHost(args)
			if err != nil {
				return nil, err
			}
			procs, err := a.ListProcesses(id, false)
			if err != nil {
				return nil, err
			}
			if grep := strings.ToLower(str(args, "grep")); grep != "" {
				kept := procs[:0:0]
				for _, p := range procs {
					if strings.Contains(strings.ToLower(p.Command+" "+p.Args+" "+p.User), grep) {
						kept = append(kept, p)
					}
				}
				procs = kept
			}
			switch str(args, "sort") {
			case "mem":
				sort.SliceStable(procs, func(i, j int) bool { return procs[i].Mem > procs[j].Mem })
			case "pid":
				sort.SliceStable(procs, func(i, j int) bool { return procs[i].PID < procs[j].PID })
			default:
				sort.SliceStable(procs, func(i, j int) bool { return procs[i].CPU > procs[j].CPU })
			}
			total := len(procs)
			if n := num(args, "limit", 20, maxProcessRows); len(procs) > n {
				procs = procs[:n]
			}
			out := make([]map[string]any, 0, len(procs))
			for _, p := range procs {
				row := map[string]any{"pid": p.PID, "user": p.User, "cpu": p.CPU, "mem": p.Mem}
				put(row, "rssKb", p.RSS)
				// args is the full command line and command is its first word,
				// so the short form only earns its place when it is not already
				// there — a kernel thread, mostly.
				cmd := p.Args
				if cmd == "" {
					cmd = p.Command
				}
				row["command"] = cmd
				// Sleeping is what almost every process is doing. Running,
				// zombie and uninterruptible sleep are the ones worth a key.
				if st := p.State; st != "" && st[0] != 'S' {
					row["state"] = st
				}
				out = append(out, row)
			}
			return map[string]any{
				"processes": out,
				"page":      truncated(len(out), total, "filter with grep"),
			}, nil
		},
	})

	s.Register(mcp.Tool{
		Name:        "container_list",
		Description: "Docker or Podman containers with their state and published ports.",
		InputSchema: obj(map[string]any{
			"hostId": hostArg,
			"state": map[string]any{
				"type": "string", "enum": []string{"all", "running"},
				"description": "Defaults to all.",
			},
		}, "hostId"),
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			id, err := a.mcpHost(args)
			if err != nil {
				return nil, err
			}
			list, err := a.ListContainers(id)
			if err != nil {
				return nil, err
			}
			if str(args, "state") == "running" {
				kept := list[:0:0]
				for _, c := range list {
					if c.State == "running" {
						kept = append(kept, c)
					}
				}
				list = kept
			}
			out := make([]map[string]any, 0, len(list))
			for _, c := range list {
				// The short ID is what the CLI accepts and what a person reads;
				// the other 52 characters are never used.
				id := c.ID
				if len(id) > 12 {
					id = id[:12]
				}
				row := map[string]any{"id": id, "name": c.Name, "image": c.Image, "state": c.State}
				// Status already says "Exited (0) 4 months ago", which covers
				// what created would have told us.
				put(row, "status", c.Status)
				if c.State != "running" && c.ExitCode != 0 {
					row["exitCode"] = c.ExitCode
				}
				if len(c.Ports) > 0 {
					row["ports"] = c.Ports
				}
				out = append(out, row)
			}
			return map[string]any{"containers": out}, nil
		},
	})

	s.Register(mcp.Tool{
		Name: "net_ports",
		Description: "Listening sockets. exposedOnly=true is the security question: which ports " +
			"answer from off the machine rather than only on loopback.",
		InputSchema: obj(map[string]any{
			"hostId":      hostArg,
			"exposedOnly": map[string]any{"type": "boolean", "description": "Defaults to false."},
		}, "hostId"),
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			id, err := a.mcpHost(args)
			if err != nil {
				return nil, err
			}
			net, err := a.HostNetwork(id)
			if err != nil {
				return nil, err
			}
			listeners := net.Listeners
			if only, _ := args["exposedOnly"].(bool); only {
				kept := listeners[:0:0]
				for _, l := range listeners {
					if l.Exposed {
						kept = append(kept, l)
					}
				}
				listeners = kept
			}
			total := len(listeners)
			if len(listeners) > maxListenRows {
				listeners = listeners[:maxListenRows]
			}
			// The tool is about ports; interfaces are context. MAC addresses and
			// MTUs answer no question a model is being asked here.
			ifaces := make([]map[string]any, 0, len(net.Interfaces))
			for _, n := range net.Interfaces {
				addrs := make([]string, 0, len(n.Addresses))
				for _, a := range n.Addresses {
					addrs = append(addrs, fmt.Sprintf("%s/%d", a.Address, a.Prefix))
				}
				row := map[string]any{"name": n.Name, "state": n.State}
				if len(addrs) > 0 {
					row["addresses"] = addrs
				}
				ifaces = append(ifaces, row)
			}
			out := make([]map[string]any, 0, len(listeners))
			for _, l := range listeners {
				row := map[string]any{"proto": l.Protocol, "port": l.Port, "address": l.Address}
				put(row, "process", l.Process)
				put(row, "pid", l.PID)
				put(row, "exposed", l.Exposed)
				out = append(out, row)
			}
			res := map[string]any{
				"interfaces": ifaces,
				"listeners":  out,
				"page":       truncated(len(out), total, "set exposedOnly"),
			}
			if len(net.Warnings) > 0 {
				res["warnings"] = net.Warnings
			}
			return res, nil
		},
	})

	s.Register(mcp.Tool{
		Name:        "fs_list",
		Description: "List a directory over SFTP. Nothing is installed on the server to do this.",
		InputSchema: obj(map[string]any{
			"hostId": hostArg,
			"path":   map[string]any{"type": "string", "description": "Absolute path. Defaults to the login home directory."},
		}, "hostId"),
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			id, err := a.mcpHost(args)
			if err != nil {
				return nil, err
			}
			path := str(args, "path")
			if path == "" {
				if path, err = a.HomeDir(id); err != nil {
					return nil, err
				}
			}
			listing, err := a.ListDir(id, path)
			if err != nil {
				return nil, err
			}
			entries := listing.Entries
			if len(entries) > maxDirRows {
				entries = entries[:maxDirRows]
			}
			// Names, not paths: the directory is already on the response, and
			// repeating it on every row costs more than the names themselves.
			// perm restates mode, and uid 1000 means nothing without a name to
			// resolve it against.
			out := make([]map[string]any, 0, len(entries))
			for _, e := range entries {
				row := map[string]any{"name": e.Name, "mode": e.Mode, "modTime": e.ModTime}
				put(row, "isDir", e.IsDir)
				if !e.IsDir {
					put(row, "size", e.Size)
				}
				put(row, "isSymlink", e.IsSymlink)
				put(row, "linkTarget", e.LinkTarget)
				put(row, "broken", e.Broken)
				out = append(out, row)
			}
			return map[string]any{
				"path":    listing.Path,
				"parent":  listing.Parent,
				"entries": out,
				"page":    truncated(len(out), listing.Total, "list a subdirectory instead"),
			}, nil
		},
	})

	s.Register(mcp.Tool{
		Name: "fs_read",
		Description: "Read a text file over SFTP. Binary files and anything large are refused " +
			"rather than truncated into nonsense.",
		InputSchema: obj(map[string]any{
			"hostId": hostArg,
			"path":   map[string]any{"type": "string", "description": "Absolute path to a text file."},
		}, "hostId", "path"),
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			id, err := a.mcpHost(args)
			if err != nil {
				return nil, err
			}
			path := str(args, "path")
			if path == "" {
				return nil, fmt.Errorf("path is required")
			}
			f, err := a.ReadTextFile(id, path)
			if err != nil {
				return nil, err
			}
			if f.Binary {
				return nil, fmt.Errorf("%s is a binary file", path)
			}
			if f.TooLarge {
				return nil, fmt.Errorf("%s is over the reader's size limit; read a smaller file "+
					"or narrow with fs_list first", path)
			}
			content, cut := f.Content, false
			if len(content) > maxFileBytes {
				content, cut = content[:maxFileBytes], true
			}
			return map[string]any{
				"path":      f.Path,
				"content":   content,
				"sizeBytes": f.Size,
				"truncated": cut,
			}, nil
		},
	})

	s.Register(mcp.Tool{
		Name:        "sessions_list",
		Description: "Who is logged in over SSH right now, including LiteDeck's own connection.",
		InputSchema: obj(map[string]any{"hostId": hostArg}, "hostId"),
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			id, err := a.mcpHost(args)
			if err != nil {
				return nil, err
			}
			sessions, err := a.ListSSHSessions(id)
			if err != nil {
				return nil, err
			}
			return map[string]any{"sessions": sessions}, nil
		},
	})
}

// logResult trims a log to something a model can read.
//
// Filtering happens here rather than in journalctl's -g, whose PCRE support is
// a build option and silently absent on some distributions — a filter that
// quietly matches nothing is worse than no filter. The tail is kept rather than
// the head: the last lines are the ones that explain a failure.
func logResult(subject, out, grep string) map[string]any {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	total := len(lines)

	if grep != "" {
		needle := strings.ToLower(grep)
		kept := lines[:0:0]
		for _, l := range lines {
			if strings.Contains(strings.ToLower(l), needle) {
				kept = append(kept, l)
			}
		}
		lines = kept
	}
	matched := len(lines)
	lines = collapseRepeats(lines)

	truncatedBy := 0
	if len(lines) > maxLogLines {
		truncatedBy = len(lines) - maxLogLines
		lines = lines[truncatedBy:]
	}

	// Folding and truncation are different facts and the model acts on them
	// differently. A folded reply is complete — every line is still accounted
	// for by a count — so telling it to "narrow the query" would spend the
	// tokens the folding just saved on a second call that finds the same
	// thing. Only a reply that actually dropped lines gets that hint.
	page := map[string]any{"shown": len(lines), "sourceLines": total}
	if grep != "" {
		page["matched"] = matched
	}
	if len(lines) < matched {
		page["folded"] = true
	}
	if truncatedBy > 0 {
		page["truncated"] = truncatedBy
		page["hint"] = "narrow with since, priority or grep"
	}

	res := map[string]any{
		"subject": subject,
		"lines":   lines,
		"page":    page,
	}
	// An empty journal is an answer that gets misread. Say what it means so a
	// model does not report "the service never logged anything".
	if len(lines) == 0 {
		res["note"] = "No matching lines. The unit may not have logged in this window, " +
			"the name may be wrong (check svc_list), or the filter may be too narrow."
	}
	return res
}

// syslogPrefix matches the timestamp, host and process that journald puts in
// front of every line, so two occurrences of the same message can be compared.
var syslogPrefix = regexp.MustCompile(`^\w{3} [ 0-9]\d \d\d:\d\d:\d\d \S+ [^:]+: `)

// collapseRepeats folds a message that occurs several times into one line
// carrying the count and the span it covers.
//
// A unit that fails every few hours writes the same block over and over with
// only the timestamps moving. Reading sixty lines of that costs a model as much
// as sixty distinct lines and tells it what eight would have.
//
// Order is preserved and the collapsed line stays where the message *first*
// appeared, so a diagnosis that depends on what happened before what still
// reads correctly. What is lost is the interleaving of repeats — a message
// alternating with another no longer shows its rhythm. That is the trade, and
// it is why the count and the last time are kept rather than just the first.
func collapseRepeats(lines []string) []string {
	const minRepeat = 3

	type seen struct {
		count int
		last  string // the last full line, for its timestamp
	}
	index := map[string]*seen{}
	order := make([]string, 0, len(lines))

	key := func(line string) string {
		k := syslogPrefix.ReplaceAllString(line, "")
		if k == "" {
			return line
		}
		return k
	}

	for _, line := range lines {
		k := key(line)
		if s, ok := index[k]; ok {
			s.count++
			s.last = line
			continue
		}
		index[k] = &seen{count: 1, last: line}
		order = append(order, line)
	}

	out := make([]string, 0, len(order))
	for _, line := range order {
		s := index[key(line)]
		if s.count < minRepeat {
			// Two occurrences are not a pattern, and the fold marker would cost
			// more characters than leaving them alone.
			for i := 0; i < s.count; i++ {
				out = append(out, line)
			}
			continue
		}
		out = append(out, fmt.Sprintf("%s   [×%d, last %s]", line, s.count, timeOf(s.last)))
	}
	return out
}

// timeOf pulls the timestamp off a journald line, or returns the line's start
// when it is not in that shape.
func timeOf(line string) string {
	if len(line) >= 15 && syslogPrefix.MatchString(line) {
		return line[:15]
	}
	if len(line) > 20 {
		return line[:20]
	}
	return line
}

// restatesName reports whether a unit's description only repeats its name.
func restatesName(name, description string) bool {
	if description == "" {
		return true
	}
	squash := func(v string) string {
		var b strings.Builder
		for _, r := range strings.ToLower(v) {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	d := squash(description)
	return d == squash(name) || d == squash(strings.TrimSuffix(name, ".service"))
}

// safeJournalArg keeps a time expression to characters journalctl uses. Not a
// shell concern — arguments are quoted before they leave — but a malformed one
// makes journalctl fail in a way that reads to a model like the unit is broken.
func safeJournalArg(v string) bool {
	if v == "" {
		return true
	}
	if len(v) > 40 {
		return false
	}
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r == '-' || r == '+' || r == ':' || r == ' ' || r == '.':
		default:
			return false
		}
	}
	return true
}

// healthSnapshot answers "is this server alright" in as few round trips as the
// adapters allow.
//
// Partial answers are kept rather than discarded. A box with no Docker should
// still report its disk usage, and a model told "containers: not available"
// stops asking, where a hard failure sends it hunting for another tool.
func (a *App) healthSnapshot(hostID string) (map[string]any, error) {
	info, err := a.DetectHost(hostID)
	if err != nil {
		return nil, err
	}

	out := map[string]any{
		"host": map[string]any{
			"hostId":   hostID,
			"os":       info.PrettyName,
			"platform": info.Platform,
			"kernel":   info.Kernel,
		},
	}
	unavailable := map[string]string{}

	if m, err := a.HostMetrics(hostID); err != nil {
		unavailable["metrics"] = err.Error()
	} else {
		out["cpuPercent"] = m.CPU
		out["memory"] = map[string]any{
			"usedBytes": m.MemUsed, "totalBytes": m.MemTotal, "percent": m.MemPercent,
		}
		out["uptimeSeconds"] = m.UptimeSeconds
		if m.HasLoad {
			out["load"] = []float64{m.Load1, m.Load5, m.Load15}
		}
		disks := make([]map[string]any, 0, len(m.Disks))
		for _, d := range m.Disks {
			disks = append(disks, map[string]any{
				"mount": d.MountPoint, "percent": d.Percent,
				"usedBytes": d.Used, "totalBytes": d.Size,
			})
		}
		out["disks"] = disks
	}

	// Only the failures. A healthy unit is not news, and the whole list would
	// crowd out the part of the answer that matters.
	if units, err := a.ListServices(hostID); err != nil {
		unavailable["services"] = err.Error()
	} else {
		failed := []map[string]any{}
		for _, u := range units {
			if u.Active == "failed" {
				failed = append(failed, map[string]any{"name": u.Name, "sub": u.Sub})
			}
		}
		out["failedUnits"] = failed
	}

	if list, err := a.ListContainers(hostID); err != nil {
		unavailable["containers"] = err.Error()
	} else {
		bad := []map[string]any{}
		for _, c := range list {
			if c.State != "running" {
				row := map[string]any{"name": c.Name, "image": c.Image}
				// "Exited (0) 4 months ago" already carries the state and the
				// exit code, the same way a mode string carried the octal.
				if c.Status != "" {
					row["status"] = c.Status
				} else {
					put(row, "state", c.State)
					put(row, "exitCode", c.ExitCode)
				}
				bad = append(bad, row)
			}
		}
		out["notRunningContainers"] = bad
		out["containerCount"] = len(list)
	}

	if net, err := a.HostNetwork(hostID); err != nil {
		unavailable["network"] = err.Error()
	} else {
		// A service listening on both families shows up twice, once for
		// 0.0.0.0 and once for ::. That is one fact, and the question this
		// answers — what is reachable from outside — does not change with the
		// address family.
		seen := map[string]int{}
		exposed := []map[string]any{}
		for _, l := range net.Listeners {
			if !l.Exposed {
				continue
			}
			key := l.Protocol + "/" + l.Port
			if at, ok := seen[key]; ok {
				// Keep whichever row managed to name the process.
				if exposed[at]["process"] == nil && l.Process != "" {
					exposed[at]["process"] = l.Process
				}
				continue
			}
			row := map[string]any{"proto": l.Protocol, "port": l.Port}
			put(row, "process", l.Process)
			seen[key] = len(exposed)
			exposed = append(exposed, row)
		}
		out["exposedPorts"] = exposed
	}

	if len(unavailable) > 0 {
		out["unavailable"] = unavailable
	}
	return out, nil
}

// mcpDeleteAllowed reports whether deletion is offered on this host.
func (a *App) mcpDeleteAllowed(hostID string) bool {
	if a.settings == nil {
		return false
	}
	return a.settings.Get().MCP.Delete[hostID]
}

// mcpAllowed reports whether the user has shared this host with AI clients.
func (a *App) mcpAllowed(hostID string) bool {
	if a.settings == nil {
		return false
	}
	return a.settings.Get().MCP.Hosts[hostID]
}
