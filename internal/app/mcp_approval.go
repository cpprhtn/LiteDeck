package app

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/config"
	"github.com/cpprhtn/LiteDeck/internal/i18n"
)

// The approval model (§4.2–§4.6 of the MCP design note).
//
// # Why this exists before any write tool
//
// Shipping a write without a gate cannot be undone: somebody's agent restarts
// something on a production box and there was never a moment where a person
// could have said no. So the gate lands first, and the write tools are built
// on top of it rather than beside it.
//
// # Why the gate can be turned off, and why the switch lives here
//
// The first draft said "every write gets a dialog, no exceptions". That was
// wrong for two reasons, and the second is the stronger one.
//
// A user who has put Claude Code into an autonomous mode has already made an
// informed choice. An app that answers "I will ask anyway" is overriding a
// decision that was theirs to make.
//
// And a control that people switch off protects nobody. There are MCP servers
// that delegate approval to the client entirely; if LiteDeck is the safe but
// irritating one, the irritated go and use those, and the net safety of the
// ecosystem goes down rather than up.
//
// So the mode is adjustable. What is *not* adjustable is who adjusts it. There
// is no tool that changes a policy, no parameter that relaxes one, and nothing
// in an MCP request that is consulted when deciding. This is the single line in
// the design that has no trade-off attached: the protocol carries no trustworthy
// statement of a client's intent — verified against Claude Code 2.1.22, whose
// initialize sends only a protocol version and a client name — and even if it
// did, prompt injection would put "call this with bypass enabled" into a log
// file and be believed.
//
// The consequence to be honest about: with bypass on, an injected instruction
// wins. What remains is not defence but attribution and blast radius — every
// call in the Command Log, the mode expiring on its own, and the fact that it
// is set per host so production can stay behind a dialog while staging does not.

// Write modes, per host.
//
// The default is not "ask about everything", and that is a deliberate reversal.
// A restart is something the MCP client already showed the user in the same
// words LiteDeck would use, so asking again is a second click for a decision
// they just made — and a gate people click through without reading is not a
// gate. What LiteDeck knows that no client can is the file's *current contents
// on the server*, so that is the one place the extra dialog earns itself.
const (
	// WriteAsk asks before changing a file, and lets service, container and
	// process actions through. The default.
	WriteAsk = "ask"
	// WriteStrict asks before anything. For the host you cannot afford to be
	// wrong about, whatever mode the AI client happens to be in.
	WriteStrict = "strict"
	// WriteBypass asks about nothing, until it expires.
	WriteBypass = "bypass"
)

// asksAbout reports whether a mode wants a dialog for this tool.
func asksAbout(mode, tool string) bool {
	switch mode {
	case WriteBypass:
		return false
	case WriteStrict:
		return true
	default:
		// The tools whose dialog shows something the client could not. For a
		// file that is the diff against what is on the server right now. For a
		// command it is the command: `svc_control(restart, nginx.service)` is
		// fully described by the call the client already displayed, and a shell
		// line is not — the schema bounds the first and says nothing about the
		// second.
		// fs_edit is not registered and has not been; it is here so a tool by
		// that name could not arrive later and default to silent.
		return tool == "fs_write" || tool == "fs_edit" || tool == "fs_delete" ||
			tool == "run_command"
	}
}

// maxWriteWindow bounds how long a relaxed mode can last.
//
// There is no "forever". A mode nobody remembers turning on is the one that
// causes the incident, and an expiry the user can renew in a click costs them
// very little.
const maxWriteWindow = 8 * time.Hour

// WriteApprovalTimeout bounds how long a tool call waits for a human.
//
// Shorter than the SSH prompt timeout: an agent blocked on an absent user
// should report back rather than hold the client open, and §4.6 requires the
// answer be a readable result rather than a hang.
var writeApprovalTimeout = 2 * time.Minute

// MCPWritePrompt is the payload of a prompt:mcpwrite event.
//
// It carries what will actually happen, not a description of it. The dialog is
// the only place a person sees the real command or the real diff, and that is
// the whole reason the gate is here rather than in the MCP client: the client's
// own confirmation shows the arguments a model composed, which is a different
// thing from what the server is about to be told to do.
type MCPWritePrompt struct {
	ID     string `json:"id"`
	HostID string `json:"hostId"`
	Host   string `json:"host"`
	Tool   string `json:"tool"`
	// Summary is one line: what this does, in the user's language.
	Summary string `json:"summary"`
	// Command is the literal command line, where there is one.
	Command string `json:"command,omitempty"`
	// Before and After are set for a file write, so the dialog can show a diff.
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Path   string `json:"path,omitempty"`
	// Grant is set when the tool is switched off for this host — "delete" or
	// "exec" — and the dialog is asking whether to allow it at all. See
	// askGrant.
	Grant string `json:"grant,omitempty"`
	// Guard is set when this is asked about in every mode — one of the guard*
	// kinds in mcp_guard.go — and GuardDetail is the part that tripped it. The
	// dialog says why, and leaves out "stop asking", which would not cover it.
	Guard       string `json:"guard,omitempty"`
	GuardDetail string `json:"guardDetail,omitempty"`
}

// writeRequest is what a write tool hands the gate.
type writeRequest struct {
	// ctx is the tool call's, so the outcome reaches the work record. Nil
	// outside an MCP call.
	ctx     context.Context
	hostID  string
	tool    string
	summary string
	command string
	path    string
	before  string
	after   string
	// grant marks a request for a switch that is off; see askGrant.
	grant string
	// guard is set by guardFor when the request is below the floor every mode
	// shares.
	guard guardReason
}

// approvalOutcome is why a write did or did not run. It reaches the model, so
// each value is something an agent can act on differently.
type approvalOutcome string

const (
	outcomeApproved approvalOutcome = "approved"
	outcomeAuto     approvalOutcome = "auto-approved"
	outcomeDeclined approvalOutcome = "declined"
	outcomeTimeout  approvalOutcome = "timeout"

	// Restores are not approvals — nobody was asked — but they end up on the
	// same log line, and they are successes. Left out of the set below they
	// would be filed as failed commands.
	outcomeRestored        approvalOutcome = "restored"
	outcomeRestoredInPlace approvalOutcome = "restored in place"
)

type approvalBridge struct {
	app *App

	mu      sync.Mutex
	seq     int
	waiting map[string]chan bool
	// pending keeps the payload of every approval still waiting, so a web
	// client that lost its socket can ask for them again. See PendingPrompts.
	pending map[string]MCPWritePrompt
}

func newApprovalBridge(a *App) *approvalBridge {
	return &approvalBridge{app: a, waiting: make(map[string]chan bool)}
}

// policyFor returns the effective write mode for a host, treating an expired
// window as if it had never been set.
func (a *App) policyFor(hostID string) config.MCPWritePolicy {
	if a.settings == nil {
		return config.MCPWritePolicy{Mode: WriteAsk}
	}
	p := a.settings.Get().MCP.Write[hostID]
	if p.Mode == "" {
		return config.MCPWritePolicy{Mode: WriteAsk}
	}
	// Until == 0 means "does not expire", which is what strict is stored with.
	// The condition already required a non-zero Until, so this is unchanged
	// behaviour for it — the bug was on the writing side.
	if p.Mode != WriteAsk && p.Until > 0 && time.Now().Unix() >= p.Until {
		// Expiry is enforced on read rather than by a timer: a timer that does
		// not fire because the app was asleep would leave the window open.
		return config.MCPWritePolicy{Mode: WriteAsk}
	}
	return p
}

// approveWrite is the gate every write tool passes through.
func (a *App) approveWrite(req writeRequest) (out approvalOutcome, err error) {
	defer func() { aiNoteFrom(req.ctx).setApproval(out, req.command) }()
	// The floor comes before the mode, so no mode can be below it. See
	// mcp_guard.go for what is on it and why.
	if g := guardFor(req); g.any() {
		req.guard = g
		return a.askGuarded(req)
	}
	policy := a.policyFor(req.hostID)
	if !asksAbout(policy.Mode, req.tool) {
		a.log.AIWrite(req.hostID, req.summary, string(outcomeAuto))
		return outcomeAuto, nil
	}
	return a.askPerson(req)
}

// askGrant is the dialog for a tool that is switched off on this host.
//
// It used to be a flat refusal, and the first person to meet it asked why the
// agent could not delete a file when every other change went through: the
// switch was in a panel they had no reason to open. Now the agent's request is
// what brings the question to them — with the file or the command in front of
// them — and the answer is theirs: no, this once, or from now on for this
// server (the dialog turns the switch on itself, through the same binding the
// panel uses).
//
// Asked whatever the approval mode. Bypass decides whether allowed changes
// interrupt anyone; it is not consent to something that was never allowed.
// And nothing the agent sends can answer it: the switch still only changes in
// the app, by a person.
func (a *App) askGrant(req writeRequest, grant string) (out approvalOutcome, err error) {
	defer func() { aiNoteFrom(req.ctx).setApproval(out, req.command) }()
	req.grant = grant
	// Asked anyway, but the person should still be told when the command is
	// one of the kinds the floor exists for.
	req.guard = guardFor(req)
	out, err = a.askPerson(req)
	if err == nil {
		return out, nil
	}
	what := "deleting files"
	if grant == "exec" {
		what = "running commands"
	}
	if out == outcomeTimeout {
		return out, fmt.Errorf("%s is switched off for this server. LiteDeck asked the user whether "+
			"to allow it and nobody answered within %s, so nothing happened. It cannot be enabled "+
			"from here", what, writeApprovalTimeout)
	}
	return out, fmt.Errorf("%s is switched off for this server, and the user chose not to allow "+
		"it for this request. Do not retry; ask them what they want instead. It cannot be enabled "+
		"from here", what)
}

// askGuarded asks about a request below the floor, and says so in the answer
// when the person does not approve it.
//
// The explanation matters more than it looks. An agent told only "declined"
// in the relaxed mode reasonably concludes the mode was not what the user
// thought, and asks them to relax it further — which would change nothing.
func (a *App) askGuarded(req writeRequest) (approvalOutcome, error) {
	out, err := a.askPerson(req)
	if err != nil {
		return out, fmt.Errorf("%v. %s", err, req.guard.forModel())
	}
	return out, nil
}

// askToRead is the floor for fs_read: a file that probably holds a secret is
// shown to the person before its contents go to the client.
//
// Reads never ask otherwise, and there is no mode for them, so this is the
// whole of it. It goes through the same dialog bridge as the writes so a web
// client that reconnects is asked again, and through the same log line so the
// decision is on record next to the read it allowed.
func (a *App) askToRead(ctx context.Context, hostID, path string, g guardReason) (out approvalOutcome, err error) {
	defer func() { aiNoteFrom(ctx).setApproval(out, "") }()
	return a.askGuarded(writeRequest{
		ctx:     ctx,
		hostID:  hostID,
		tool:    "fs_read",
		summary: "read " + path,
		path:    path,
		guard:   g,
	})
}

// askPerson puts a change in front of the user and waits for the answer.
func (a *App) askPerson(req writeRequest) (approvalOutcome, error) {
	id := a.approvals.nextID()
	ch := make(chan bool, 1)

	a.approvals.mu.Lock()
	a.approvals.waiting[id] = ch
	a.approvals.mu.Unlock()
	defer func() {
		a.approvals.mu.Lock()
		delete(a.approvals.waiting, id)
		delete(a.approvals.pending, id)
		a.approvals.mu.Unlock()
	}()

	host := req.hostID
	if h, ok := a.hosts.Get(req.hostID); ok {
		host = h.Label()
	}
	payload := MCPWritePrompt{
		ID: id, HostID: req.hostID, Host: host, Tool: req.tool,
		Summary: req.summary, Command: req.command,
		Path: req.path, Before: req.before, After: req.after,
		Grant: req.grant,
		Guard: req.guard.Kind, GuardDetail: req.guard.Detail,
	}
	a.approvals.mu.Lock()
	if a.approvals.pending == nil {
		a.approvals.pending = map[string]MCPWritePrompt{}
	}
	a.approvals.pending[id] = payload
	a.approvals.mu.Unlock()
	a.emit("prompt:mcpwrite", payload)

	select {
	case approved := <-ch:
		if !approved {
			a.log.AIWrite(req.hostID, req.summary, string(outcomeDeclined))
			return outcomeDeclined, fmt.Errorf(
				"the user declined this. Do not retry it; ask them what they want instead")
		}
		a.log.AIWrite(req.hostID, req.summary, string(outcomeApproved))
		return outcomeApproved, nil

	case <-time.After(writeApprovalTimeout):
		// Refusing on timeout is the only safe default, and saying so plainly
		// lets the agent report back rather than hammer the same call.
		a.log.AIWrite(req.hostID, req.summary, string(outcomeTimeout))
		return outcomeTimeout, fmt.Errorf(
			"nobody answered the approval dialog within %s, so nothing ran. "+
				"The user is probably away from the machine", writeApprovalTimeout)
	}
}

func (b *approvalBridge) nextID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	return "mcpw-" + strconv.Itoa(b.seq)
}

/* ------------------------------------------------------------- bindings */

// AnswerMCPWrite delivers the user's decision. Called from the frontend.
func (a *App) AnswerMCPWrite(id string, approved bool) error {
	a.approvals.mu.Lock()
	ch, found := a.approvals.waiting[id]
	a.approvals.mu.Unlock()
	if !found {
		return fmt.Errorf("app: no pending approval %q", id)
	}
	select {
	case ch <- approved:
	default:
	}
	return nil
}

// SetMCPWritePolicy sets a host's write mode.
//
// The only way a mode changes. There is deliberately no MCP tool that reaches
// this, and no request field that influences it (§4.3).
func (a *App) SetMCPWritePolicy(hostID, mode string, minutes int) MCPStatus {
	if a.settings == nil {
		return a.MCPState()
	}
	switch mode {
	case WriteAsk, WriteStrict, WriteBypass:
	default:
		out := a.MCPState()
		out.Error = i18n.T("알 수 없는 쓰기 모드: %s", mode)
		return out
	}

	s := a.settings.Get().MCP
	if s.Write == nil {
		s.Write = map[string]config.MCPWritePolicy{}
	}
	switch mode {
	case WriteAsk:
		delete(s.Write, hostID) // the default needs no entry

	case WriteStrict:
		// No expiry. The window exists so a relaxation cannot outlive the
		// session that wanted it; strict is the opposite of a relaxation, and
		// expiring it turned the most careful setting into a temporary one that
		// quietly became the *less* careful default overnight. Somebody who
		// marks a production box "ask about everything" and comes back in the
		// morning would have found svc_control restarting services without a
		// word. A policy must not relax itself.
		s.Write[hostID] = config.MCPWritePolicy{Mode: mode}

	default:
		window := time.Duration(minutes) * time.Minute
		if window <= 0 || window > maxWriteWindow {
			window = maxWriteWindow
		}
		s.Write[hostID] = config.MCPWritePolicy{
			Mode:  mode,
			Until: time.Now().Add(window).Unix(),
		}
	}
	if err := a.settings.SetMCP(s); err != nil {
		out := a.MCPState()
		out.Error = err.Error()
		return out
	}
	out := a.MCPState()
	a.emitMCPState(out)
	return out
}

// WriteApprovalTimeoutForTest shortens the wait so a test does not have to sit
// through the real one. Returns a function that puts it back.
func WriteApprovalTimeoutForTest(d time.Duration) func() {
	previous := writeApprovalTimeout
	writeApprovalTimeout = d
	return func() { writeApprovalTimeout = previous }
}
