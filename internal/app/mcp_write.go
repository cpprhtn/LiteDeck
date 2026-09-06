package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/mcp"
	"github.com/cpprhtn/LiteDeck/internal/rollback"
)

// The write tools (§5.3 of the MCP design note).
//
// Every one of these passes through approveWrite before it touches a server,
// and none of them can reach a host the user has not shared. Two things are
// deliberately absent:
//
// **run_command.** An arbitrary-command tool makes the per-tool allowlist
// decorative — switching svc_restart off means nothing when the same thing can
// be done by typing it. The design note gives it its own toggle for that
// reason, and that toggle is not built.
//
// **Deletion.** No fs_delete, no container_remove, no image or volume pruning.
// A restart is recoverable by restarting again; a deletion is not, and the
// asymmetry is worth a deliberate gap while this is new.

// elevate is never true here. Silently escalating for an AI would put a sudo
// password behind a decision no person made; when a server refuses, the answer
// the model gets says to do it in the app.
const mcpNeverElevates = false

func (a *App) registerMCPWriteTools(s *mcp.Server) {
	hostArg := map[string]any{
		"type":        "string",
		"description": "Host ID from hosts_list.",
	}
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required}
	}

	// A change the user has to look at is described the way they would describe
	// it, not the way the protocol does.
	s.Register(mcp.Tool{
		Name: "svc_control",
		Description: "Start, stop or restart a systemd unit or Windows service. The user is " +
			"shown the exact command and approves it before anything runs, unless they have " +
			"turned that off for this host.",
		InputSchema: obj(map[string]any{
			"hostId": hostArg,
			"unit":   map[string]any{"type": "string", "description": "Exact unit name from svc_list."},
			"action": map[string]any{
				"type": "string", "enum": []string{"start", "stop", "restart"},
				"description": "reload is not offered here; use restart.",
			},
		}, "hostId", "unit", "action"),
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			id, err := a.mcpHost(args)
			if err != nil {
				return nil, err
			}
			unit, action := str(args, "unit"), str(args, "action")
			if unit == "" || !oneOf(action, "start", "stop", "restart") {
				return nil, fmt.Errorf("unit and action (start, stop or restart) are required")
			}
			out, err := a.approveWrite(writeRequest{
				hostID:  id,
				tool:    "svc_control",
				summary: fmt.Sprintf("%s %s", action, unit),
				command: fmt.Sprintf("systemctl %s -- %s", action, unit),
			})
			if err != nil {
				return nil, err
			}
			return withOutcome(a.ServiceAction(id, unit, action, mcpNeverElevates), out), nil
		},
	})

	s.Register(mcp.Tool{
		Name: "container_control",
		Description: "Start, stop or restart a container. Removal is not offered: it cannot be " +
			"undone, and this tool set stays on the recoverable side of that line.",
		InputSchema: obj(map[string]any{
			"hostId": hostArg,
			"id":     map[string]any{"type": "string", "description": "Container ID or name from container_list."},
			"action": map[string]any{
				"type": "string", "enum": []string{"start", "stop", "restart"},
			},
		}, "hostId", "id", "action"),
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			hostID, err := a.mcpHost(args)
			if err != nil {
				return nil, err
			}
			id, action := str(args, "id"), str(args, "action")
			if id == "" || !oneOf(action, "start", "stop", "restart") {
				return nil, fmt.Errorf("id and action (start, stop or restart) are required")
			}
			out, err := a.approveWrite(writeRequest{
				hostID:  hostID,
				tool:    "container_control",
				summary: fmt.Sprintf("%s container %s", action, id),
				command: fmt.Sprintf("docker %s %s", action, id),
			})
			if err != nil {
				return nil, err
			}
			return withOutcome(a.ContainerAction(hostID, id, action, mcpNeverElevates), out), nil
		},
	})

	s.Register(mcp.Tool{
		Name: "proc_signal",
		Description: "Send TERM or KILL to a process. TERM first: KILL gives the process no " +
			"chance to flush what it was writing.",
		InputSchema: obj(map[string]any{
			"hostId": hostArg,
			"pid":    map[string]any{"type": "integer", "description": "PID from proc_list."},
			"signal": map[string]any{
				"type": "string", "enum": []string{"TERM", "KILL"},
				"description": "Defaults to TERM.",
			},
		}, "hostId", "pid"),
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			hostID, err := a.mcpHost(args)
			if err != nil {
				return nil, err
			}
			pidF, ok := args["pid"].(float64)
			if !ok || int(pidF) <= 0 {
				return nil, fmt.Errorf("pid is required and must be a positive integer")
			}
			pid := int(pidF)
			signal := strings.ToUpper(str(args, "signal"))
			if signal == "" {
				signal = "TERM"
			}
			if !oneOf(signal, "TERM", "KILL") {
				return nil, fmt.Errorf("signal must be TERM or KILL")
			}
			out, err := a.approveWrite(writeRequest{
				hostID:  hostID,
				tool:    "proc_signal",
				summary: fmt.Sprintf("send %s to PID %d", signal, pid),
				command: fmt.Sprintf("kill -%s %d", signal, pid),
			})
			if err != nil {
				return nil, err
			}
			return withOutcome(a.KillProcess(hostID, pid, signal, mcpNeverElevates), out), nil
		},
	})

	s.Register(mcp.Tool{
		Name: "fs_write",
		Description: "Replace a text file's contents. The user sees a diff against what is on " +
			"the server right now before approving. Read the file first: this replaces the " +
			"whole file, it does not patch it. An answer carrying inPlace: true means the " +
			"write could not be made atomic and the file was overwritten in place — it " +
			"succeeded, but it was not crash-safe, and the user should be told.",
		InputSchema: obj(map[string]any{
			"hostId":  hostArg,
			"path":    map[string]any{"type": "string", "description": "Absolute path."},
			"content": map[string]any{"type": "string", "description": "The complete new contents."},
		}, "hostId", "path", "content"),
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			hostID, err := a.mcpHost(args)
			if err != nil {
				return nil, err
			}
			path := str(args, "path")
			content, ok := args["content"].(string)
			if path == "" || !ok {
				return nil, fmt.Errorf("path and content are required")
			}

			// The current contents are fetched so the dialog can show a diff
			// rather than a wall of new text. A file that does not exist yet is
			// a creation, which is fine and shows as a diff against nothing.
			var before string
			existed := false
			if existing, err := a.ReadTextFile(hostID, path); err == nil {
				if existing.Binary {
					return nil, fmt.Errorf("%s is a binary file", path)
				}
				before, existed = existing.Content, true
			}
			if before == content {
				return map[string]any{
					"ok":   true,
					"note": "The file already has these contents. Nothing was written.",
				}, nil
			}

			out, err := a.approveWrite(writeRequest{
				hostID:  hostID,
				tool:    "fs_write",
				summary: fmt.Sprintf("write %s", path),
				path:    path,
				before:  before,
				after:   content,
			})
			if err != nil {
				return nil, err
			}
			// Recorded before the write, so an interrupted change still leaves
			// something to go back to.
			a.recordAIChange(hostID, path, rollback.ActionWrite, []byte(before), !existed)
			// SaveTextFile, not WriteTextFile: the latter narrows the result to
			// an ActionResult and drops InPlace with it. The GUI puts that on
			// screen; over MCP it is the one thing about this write nobody can
			// find out afterwards, so a clean-looking answer would be a lie.
			res := a.SaveTextFile(hostID, SaveRequest{Path: path, Content: content})
			m := withOutcome(res.ActionResult, out)
			if res.InPlace {
				m["inPlace"] = true
				if _, taken := m["note"]; !taken {
					m["note"] = "This directory would not take a temp file, so the file was " +
						"written over itself rather than replaced atomically. A connection " +
						"that drops mid-write can truncate it. Tell the user."
				}
			}
			return m, nil
		},
	})

	s.Register(mcp.Tool{
		Name: "fs_delete",
		Description: "Delete a single file. Directories are not accepted — a recursive delete " +
			"is not something to do without looking at it, and it stays in the app. The file's " +
			"contents are kept locally first, so the user can put it back.",
		InputSchema: obj(map[string]any{
			"hostId": hostArg,
			"path":   map[string]any{"type": "string", "description": "Absolute path to a file."},
		}, "hostId", "path"),
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			hostID, err := a.mcpHost(args)
			if err != nil {
				return nil, err
			}
			// Whether deletion is offered at all is a separate question from
			// whether using it interrupts the user, so it is a separate switch.
			// Off unless asked for: most people want an agent that reads and
			// edits, not one that removes things.
			if !a.mcpDeleteAllowed(hostID) {
				return nil, fmt.Errorf("deleting files is switched off for this server. " +
					"The user turns it on per server in LiteDeck's MCP settings; it cannot be " +
					"enabled from here")
			}
			path := str(args, "path")
			if path == "" {
				return nil, fmt.Errorf("path is required")
			}

			// The same guard the GUI uses. A protected path needs the user to
			// type it out, which a model can do — so it is refused outright
			// here rather than pretending the typing meant something.
			cleaned, err := CleanRemotePath(path)
			if err != nil {
				return nil, err
			}
			if IsProtectedPath(cleaned) || cleaned == "/" {
				return nil, fmt.Errorf("%s is a protected path. Deleting it is done in the app, "+
					"where a person has to type it out", cleaned)
			}

			// Read it first: without the contents this is not undoable, and the
			// whole reason deletion is offered at all is that it is.
			var before string
			existing, err := a.ReadTextFile(hostID, cleaned)
			switch {
			case err != nil:
				return nil, fmt.Errorf("%s: %w", cleaned, err)
			case existing.Binary || existing.TooLarge:
				// No copy means no undo. Say which, rather than deleting it and
				// discovering later that it is gone for good.
				return nil, fmt.Errorf("%s cannot be copied first (binary or too large), so it "+
					"could not be undone. Delete it in the app or a terminal", cleaned)
			default:
				before = existing.Content
			}

			out, err := a.approveWrite(writeRequest{
				hostID:  hostID,
				tool:    "fs_delete",
				summary: fmt.Sprintf("delete %s", cleaned),
				command: fmt.Sprintf("rm -- %s", cleaned),
				path:    cleaned,
				before:  before,
				after:   "",
			})
			if err != nil {
				return nil, err
			}
			a.recordAIChange(hostID, cleaned, rollback.ActionDelete, []byte(before), false)
			return withOutcome(a.DeletePaths(hostID, []string{cleaned}, false, ""), out), nil
		},
	})

	s.Register(mcp.Tool{
		Name: "run_command",
		Description: "Run a shell command on the server and return its output. Off unless the " +
			"user turned it on for that server, and by default they are asked before each one. " +
			"Nothing it does can be undone — no copy is kept, the way one is for a file write. " +
			"There is no sudo: the command runs as the login user with no terminal attached, so " +
			"anything that wants a password fails rather than waiting. Prefer the narrower tools " +
			"when one of them answers the question: they are cheaper, they read as intent in the " +
			"user's Command Log, and they cannot go wrong in a way nobody expected.",
		InputSchema: obj(map[string]any{
			"hostId": hostArg,
			"command": map[string]any{
				"type": "string",
				"description": "One shell command line, run with `sh -c`. Pipes, redirection " +
					"and quoting all work. The user sees this exact text before approving.",
			},
			"timeoutSeconds": map[string]any{
				"type": "integer",
				"description": "How long to wait, 1-600. Default 60. A command holds one of " +
					"the three exec channels on the shared connection until it finishes, so " +
					"ask for a long wait only when the work is genuinely long. Running out " +
					"stops the waiting, not the command: it carries on on the server, so a " +
					"retry runs it a second time alongside the first.",
			},
		}, "hostId", "command"),
		Handler: func(_ context.Context, args map[string]any) (any, error) {
			hostID, err := a.mcpHost(args)
			if err != nil {
				return nil, err
			}
			// Whether a shell exists on this host is its own question, kept
			// apart from the approval mode and from file deletion. Without this
			// switch the per-tool allowlist would be decorative, which is the
			// objection that kept this tool out until it had one.
			if !a.mcpExecAllowed(hostID) {
				return nil, fmt.Errorf("running commands is switched off for this server. " +
					"The user turns it on per server in LiteDeck's MCP settings; it cannot be " +
					"enabled from here")
			}
			command := strings.TrimSpace(str(args, "command"))
			if command == "" {
				return nil, fmt.Errorf("command is required")
			}

			out, err := a.approveWrite(writeRequest{
				hostID:  hostID,
				tool:    "run_command",
				summary: "run " + clipArg(command),
				command: command,
			})
			if err != nil {
				return nil, err
			}

			conn, err := a.mgr.Conn(hostID)
			if err != nil {
				return nil, err
			}
			wait := execTimeout(args)
			ctx, cancel := context.WithTimeout(context.Background(), wait)
			defer cancel()

			// sh -c rather than the raw line: Exec quotes argv element by
			// element (§3.2b), so handing it the command as one argument keeps
			// that invariant intact and still gives the user's shell the pipes
			// and redirection they wrote. The Command Log shows the assembled
			// line, which is what actually ran.
			res, err := conn.Exec(ctx, "sh", "-c", command)
			if err != nil {
				// Giving up on the answer does not cancel the work. Checked
				// against a real server: a command told to wait one second and
				// sleep three still left its file behind afterwards. Saying
				// "context deadline exceeded" hides the one fact that changes
				// what to do next.
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return nil, fmt.Errorf("the command did not finish within %s, so LiteDeck "+
						"stopped waiting for it. It is still running on the server — nothing "+
						"was cancelled. Find out what it did before running it again, and "+
						"raise timeoutSeconds rather than repeating the call", wait)
				}
				return nil, err
			}

			stdout, outClipped := clipOutput(res.Stdout)
			stderr, errClipped := clipOutput(res.Stderr)
			m := map[string]any{
				"ok":       res.OK(),
				"approval": string(out),
				"exitCode": res.ExitCode,
				"stdout":   stdout,
				"stderr":   stderr,
			}
			if outClipped || errClipped {
				m["truncated"] = true
				m["note"] = "Output was cut at " + strconv.Itoa(maxCommandOutput) +
					" bytes per stream. Narrow the command rather than asking again."
			}
			return m, nil
		},
	})
}

// maxCommandOutput bounds one stream of a command's output.
//
// The cap is here rather than in the shell because a model that asks for a
// whole log gets a useful head of it either way, and an unbounded answer would
// cost the user tokens they did not choose to spend.
const maxCommandOutput = 64 << 10

func clipOutput(b []byte) (string, bool) {
	if len(b) <= maxCommandOutput {
		return string(b), false
	}
	return string(b[:maxCommandOutput]), true
}

// execTimeout reads the caller's wait, bounded.
//
// The ceiling is not a safety feature — the command runs on the server whatever
// happens here — but a command still holds one of three exec channels (A-1),
// and one that waits forever takes a third of the connection with it.
func execTimeout(args map[string]any) time.Duration {
	secs := 60
	switch v := args["timeoutSeconds"].(type) {
	case float64:
		secs = int(v)
	case int:
		secs = v
	}
	if secs < 1 {
		secs = 1
	}
	if secs > 600 {
		secs = 600
	}
	return time.Duration(secs) * time.Second
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// withOutcome turns an ActionResult into something a model can act on, carrying
// how it was approved so "it just ran" is never a surprise in the transcript.
func withOutcome(res ActionResult, out approvalOutcome) map[string]any {
	m := map[string]any{"ok": res.OK, "approval": string(out)}
	if res.Error != "" {
		m["error"] = res.Error
	}
	if res.Stderr != "" {
		m["stderr"] = res.Stderr
	}
	// The GUI offers to retry as administrator. An AI does not get that path —
	// escalating on its behalf would put a sudo password behind a decision no
	// person made — so the answer says where it can be done.
	if res.NeedsElevation {
		m["note"] = "This needs administrator rights, which are not available over MCP. " +
			"The user can retry it in LiteDeck or a terminal."
	}
	return m
}
