package app

import (
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
		// Only the tools whose dialog shows something the client could not:
		// a diff against what is on the server right now.
		return tool == "fs_write" || tool == "fs_edit" || tool == "fs_delete"
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
}

// writeRequest is what a write tool hands the gate.
type writeRequest struct {
	hostID  string
	tool    string
	summary string
	command string
	path    string
	before  string
	after   string
}

// approvalOutcome is why a write did or did not run. It reaches the model, so
// each value is something an agent can act on differently.
type approvalOutcome string

const (
	outcomeApproved approvalOutcome = "approved"
	outcomeAuto     approvalOutcome = "auto-approved"
	outcomeDeclined approvalOutcome = "declined"
	outcomeTimeout  approvalOutcome = "timeout"
)

type approvalBridge struct {
	app *App

	mu      sync.Mutex
	seq     int
	waiting map[string]chan bool
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
	if p.Mode != WriteAsk && p.Until > 0 && time.Now().Unix() >= p.Until {
		// Expiry is enforced on read rather than by a timer: a timer that does
		// not fire because the app was asleep would leave the window open.
		return config.MCPWritePolicy{Mode: WriteAsk}
	}
	return p
}

// approveWrite is the gate every write tool passes through.
func (a *App) approveWrite(req writeRequest) (approvalOutcome, error) {
	policy := a.policyFor(req.hostID)
	if !asksAbout(policy.Mode, req.tool) {
		a.log.AIWrite(req.hostID, req.summary, string(outcomeAuto))
		return outcomeAuto, nil
	}

	id := a.approvals.nextID()
	ch := make(chan bool, 1)

	a.approvals.mu.Lock()
	a.approvals.waiting[id] = ch
	a.approvals.mu.Unlock()
	defer func() {
		a.approvals.mu.Lock()
		delete(a.approvals.waiting, id)
		a.approvals.mu.Unlock()
	}()

	host := req.hostID
	if h, ok := a.hosts.Get(req.hostID); ok {
		host = h.Label()
	}
	a.emit("prompt:mcpwrite", MCPWritePrompt{
		ID: id, HostID: req.hostID, Host: host, Tool: req.tool,
		Summary: req.summary, Command: req.command,
		Path: req.path, Before: req.before, After: req.after,
	})

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
	if mode == WriteAsk {
		delete(s.Write, hostID) // the default needs no entry
	} else {
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
	return a.MCPState()
}

// WriteApprovalTimeoutForTest shortens the wait so a test does not have to sit
// through the real one. Returns a function that puts it back.
func WriteApprovalTimeoutForTest(d time.Duration) func() {
	previous := writeApprovalTimeout
	writeApprovalTimeout = d
	return func() { writeApprovalTimeout = previous }
}
