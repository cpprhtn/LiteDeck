// Package mcp serves the Model Context Protocol so an AI client — Claude Code,
// Claude Desktop — can drive the same adapters the GUI drives.
//
// # Why this lives in the GUI process
//
// Not a separate `litedeck mcp` CLI. Three reasons, all of which need a running
// window: the SSH connection is already open and authenticated, so a tool call
// multiplexes onto it rather than dialling again; a write will one day need a
// dialog, and a dialog needs a screen; and every command the AI causes belongs
// in the same Command Log as the ones the user causes.
//
// # What this package is not
//
// It holds no knowledge of servers, adapters or SSH. Tools are registered from
// the app layer, which is what keeps the dependency pointing one way and lets
// the protocol be tested with tools that do nothing.
//
// # Reads and writes are not the same decision
//
// Most tools answer questions. A few change a server, and those exist only
// because the approval model they need exists too — per-host modes that expire
// on their own, owned by the app and not settable over the wire. That model
// lives in the app layer, not here: this package must not be able to relax a
// policy it also serves requests against.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// ProtocolVersion is what we advertise when a client asks for something we do
// not recognise. Clients send their own; a server that echoes an unknown
// version back is claiming support it cannot have.
const ProtocolVersion = "2025-06-18"

// supported lists the revisions this server has been checked against.
// 2025-11-25 is what Claude Code 2.1.22 negotiates.
var supported = map[string]bool{
	"2025-11-25": true,
	"2025-06-18": true,
	"2025-03-26": true,
}

// Tool is one callable the AI can reach.
type Tool struct {
	Name        string
	Description string
	// InputSchema is JSON Schema, sent to the client verbatim. The model reads
	// it to decide how to call, so the field descriptions are prompt text and
	// should read like instructions rather than like documentation.
	InputSchema map[string]any
	// Handler returns any JSON-serialisable value. An error is reported to the
	// model as a failed tool result rather than a protocol error: a model that
	// sees "no such unit" corrects itself, where a transport failure just stops
	// it (§3.3 of the design note).
	Handler func(ctx context.Context, args map[string]any) (any, error)
}

// Server speaks MCP. Transport is separate (see http.go).
type Server struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// New returns a server with no tools registered.
func New() *Server {
	return &Server{tools: make(map[string]Tool)}
}

// Register adds a tool, replacing any with the same name.
func (s *Server) Register(t Tool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[t.Name] = t
}

// Tools lists registered tools by name, sorted so the wire output is stable.
func (s *Server) Tools() []Tool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Tool, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

/* ----------------------------------------------------------- JSON-RPC 2.0 */

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC reserved codes. Anything a tool does wrong is reported inside a
// successful result, not here — see Tool.Handler.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// notification reports whether this message expects no reply. A JSON-RPC
// notification has no id, and answering one is a protocol violation.
func (r request) notification() bool { return len(r.ID) == 0 }

// Handle processes one JSON-RPC message and returns the reply, or nil when the
// message was a notification.
func (s *Server) Handle(ctx context.Context, body []byte) []byte {
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		return encode(response{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error:   &rpcError{codeParse, "parse error: " + err.Error()},
		})
	}
	if req.JSONRPC != "2.0" {
		return s.fail(req, codeInvalidRequest, `"jsonrpc" must be "2.0"`)
	}

	switch req.Method {
	case "initialize":
		return s.ok(req, s.initialize(req.Params))

	case "notifications/initialized", "notifications/cancelled":
		return nil // acknowledged by saying nothing, per JSON-RPC

	case "ping":
		return s.ok(req, map[string]any{})

	case "tools/list":
		return s.ok(req, map[string]any{"tools": s.toolList()})

	case "tools/call":
		return s.callTool(ctx, req)

	default:
		if req.notification() {
			return nil
		}
		return s.fail(req, codeMethodNotFound, "unknown method: "+req.Method)
	}
}

func (s *Server) initialize(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)

	// Agree to the client's revision when it is one we have been checked
	// against, otherwise name ours and let the client decide. Echoing an
	// unknown version back would be claiming compatibility we have not tested.
	version := ProtocolVersion
	if supported[p.ProtocolVersion] {
		version = p.ProtocolVersion
	}

	return map[string]any{
		"protocolVersion": version,
		// Only tools. No resources, no prompts, no sampling: the app has
		// nothing to offer under those and advertising them would invite calls
		// that fail.
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    "litedeck",
			"version": Version,
		},
		"instructions": Instructions,
	}
}

func (s *Server) toolList() []map[string]any {
	tools := s.Tools()
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
		})
	}
	return out
}

func (s *Server) callTool(ctx context.Context, req request) []byte {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return s.fail(req, codeInvalidParams, "bad params: "+err.Error())
	}

	s.mu.RLock()
	tool, found := s.tools[p.Name]
	s.mu.RUnlock()
	if !found {
		// Not a protocol error: the model asked for something that is not
		// there, and telling it so lets it pick a real tool instead.
		return s.ok(req, errorResult(fmt.Sprintf("no such tool: %s", p.Name)))
	}

	if p.Arguments == nil {
		p.Arguments = map[string]any{}
	}

	h := hooksFrom(ctx)
	if ok, wait := h.limiter.Allow(); !ok {
		// Reported as a tool result, not a transport error. An agent that reads
		// "wait 2s" waits; one that gets a 429 usually gives up or retries hard.
		return s.ok(req, errorResult(fmt.Sprintf(
			"rate limited: too many tool calls at once. Retry in about %.0fs. "+
				"These calls run real commands on a small server, so they are "+
				"deliberately paced.", wait.Seconds()+0.5)))
	}
	// Recorded before the call, not after: the user should see what the AI
	// asked for while it is happening, the same as their own actions.
	if h.onCall != nil {
		h.onCall(p.Name, p.Arguments)
	}

	value, err := tool.Handler(ctx, p.Arguments)
	if err != nil {
		return s.ok(req, errorResult(err.Error()))
	}
	return s.ok(req, textResult(value))
}

// textResult wraps a value as MCP tool content.
//
// The model reads text, so the payload is JSON rendered into a text block
// rather than a structured field. Indented, because a model that has to answer
// "which unit is failing" reads this the way a person would.
func textResult(v any) map[string]any {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errorResult("could not encode result: " + err.Error())
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(body)}},
		"isError": false,
	}
}

// errorResult reports a failure the model should read and act on.
func errorResult(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	}
}

func (s *Server) ok(req request, result any) []byte {
	if req.notification() {
		return nil
	}
	return encode(response{JSONRPC: "2.0", ID: req.ID, Result: result})
}

func (s *Server) fail(req request, code int, msg string) []byte {
	if req.notification() {
		return nil
	}
	id := req.ID
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return encode(response{JSONRPC: "2.0", ID: id, Error: &rpcError{code, msg}})
}

func encode(r response) []byte {
	body, err := json.Marshal(r)
	if err != nil {
		// Marshalling a response cannot normally fail; if it does, still say
		// something well-formed rather than closing the connection silently.
		return []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"internal error"}}`)
	}
	return body
}
