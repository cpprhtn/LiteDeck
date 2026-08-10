package app

import (
	"context"
	"fmt"
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
	maxServiceRows = 80
	maxListenRows  = 60
	maxDirRows     = 200
	maxFileBytes   = 64 * 1024
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
				out = append(out, map[string]any{
					"hostId":       h.ID,
					"label":        h.Label(),
					"address":      h.Addr(),
					"user":         h.User,
					"state":        h.State,
					"sharedWithAI": a.mcpAllowed(h.ID),
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
			return map[string]any{
				"units": kept,
				"page":  truncated(len(kept), total, "narrow with state or grep"),
			}, nil
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
			return map[string]any{
				"processes": procs,
				"page":      truncated(len(procs), total, "filter with grep"),
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
			return map[string]any{"containers": list}, nil
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
			return map[string]any{
				"interfaces": net.Interfaces,
				"listeners":  listeners,
				"warnings":   net.Warnings,
				"page":       truncated(len(listeners), total, "set exposedOnly"),
			}, nil
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
			return map[string]any{
				"path":    listing.Path,
				"parent":  listing.Parent,
				"entries": entries,
				"page":    truncated(len(entries), listing.Total, "list a subdirectory instead"),
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
				bad = append(bad, map[string]any{
					"name": c.Name, "image": c.Image, "state": c.State,
					"status": c.Status, "exitCode": c.ExitCode,
				})
			}
		}
		out["notRunningContainers"] = bad
		out["containerCount"] = len(list)
	}

	if net, err := a.HostNetwork(hostID); err != nil {
		unavailable["network"] = err.Error()
	} else {
		exposed := []map[string]any{}
		for _, l := range net.Listeners {
			if !l.Exposed {
				continue
			}
			exposed = append(exposed, map[string]any{
				"protocol": l.Protocol, "port": l.Port,
				"address": l.Address, "process": l.Process,
			})
		}
		out["exposedPorts"] = exposed
	}

	if len(unavailable) > 0 {
		out["unavailable"] = unavailable
	}
	return out, nil
}

// mcpAllowed reports whether the user has shared this host with AI clients.
func (a *App) mcpAllowed(hostID string) bool {
	if a.settings == nil {
		return false
	}
	return a.settings.Get().MCP.Hosts[hostID]
}
