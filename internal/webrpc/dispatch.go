// Package webrpc exposes an App's bound methods over HTTP, the way Wails exposes
// them over its in-process bridge — so the same frontend can drive the same
// core from a browser (arch/08).
//
// # Why reflection, and why carefully
//
// The App has ~90 bound methods. Hand-writing a handler per method would be its
// own maintenance surface and would drift from the desktop bindings. So the
// dispatcher reflects — but the naive form (decode args to []any, reflect.Call)
// *panics* on the real signatures: a JSON object handed to a config.Host
// parameter, or a JSON number handed to a uint32, is not a wrong value, it is a
// panic. Every argument is therefore unmarshalled into its concrete parameter
// type, which is what Wails itself does.
//
// # What is and is not reachable
//
// Reflecting over every exported method would also expose Startup and Shutdown
// — and POST /rpc/Shutdown tears down every SSH connection, PTY and the MCP
// endpoint. So the allowlist is built by exclusion of exactly the shapes that
// must never be a remote endpoint: anything taking a context.Context (which is
// precisely the two lifecycle methods) and the spike-only Bench* methods. The
// exposed set is pinned by a test.
package webrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

var (
	ctxType = reflect.TypeOf((*context.Context)(nil)).Elem()
	errType = reflect.TypeOf((*error)(nil)).Elem()
)

// Dispatcher resolves a method name to a bound method and calls it with JSON
// arguments.
type Dispatcher struct {
	recv    reflect.Value
	methods map[string]reflect.Method
}

// New reflects over target's exported methods and keeps the ones safe to expose.
func New(target any) *Dispatcher {
	d := &Dispatcher{recv: reflect.ValueOf(target), methods: map[string]reflect.Method{}}
	t := d.recv.Type()
	for i := 0; i < t.NumMethod(); i++ {
		m := t.Method(i)
		if exposable(m) {
			d.methods[m.Name] = m
		}
	}
	return d
}

// exposable decides whether a bound method may be reached over the wire.
//
// The two exclusions are deliberate and load-bearing, not cosmetic:
//   - A method that takes a context.Context is a lifecycle method (Startup,
//     Shutdown) — never something a browser should invoke.
//   - Bench* are the render-benchmark spike (report.go); they write files and
//     are not shipped behaviour.
func exposable(m reflect.Method) bool {
	if strings.HasPrefix(m.Name, "Bench") {
		return false
	}
	mt := m.Func.Type()
	for i := 1; i < mt.NumIn(); i++ { // skip receiver at 0
		if mt.In(i) == ctxType {
			return false
		}
	}
	return true
}

// Methods returns the exposed method names, sorted. For the pinning test.
func (d *Dispatcher) Methods() []string {
	out := make([]string, 0, len(d.methods))
	for name := range d.methods {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ErrNoMethod reports that the named method is not exposed. The caller turns
// this into a 404 so an unexposed name (Shutdown) is indistinguishable from a
// misspelt one — a probe learns nothing about what exists.
type ErrNoMethod struct{ Name string }

func (e ErrNoMethod) Error() string { return fmt.Sprintf("webrpc: no such method %q", e.Name) }

// Call invokes method with the given JSON arguments and returns the method's
// value result (nil for void/error-only methods) and its error.
//
// The two error channels are distinct on purpose. A malformed request (unknown
// method, wrong argument count, un-decodable argument) is a *transport* error —
// the caller answers 4xx and the returned result is meaningless. A non-nil
// error from the method itself is an *application* error — the caller answers
// with it so the browser promise rejects exactly as it would under Wails, which
// rejects the JS promise when a bound method returns a non-nil error. Collapsing
// the two would let a failed ConnectHost resolve as success.
func (d *Dispatcher) Call(method string, rawArgs []json.RawMessage) (result any, appErr error, reqErr error) {
	m, ok := d.methods[method]
	if !ok {
		return nil, nil, ErrNoMethod{Name: method}
	}
	mt := m.Func.Type()

	wantArgs := mt.NumIn() - 1 // minus receiver
	if len(rawArgs) != wantArgs {
		return nil, nil, fmt.Errorf("webrpc: %s expects %d argument(s), got %d", method, wantArgs, len(rawArgs))
	}

	in := make([]reflect.Value, 0, wantArgs+1)
	in = append(in, d.recv)
	for i := 0; i < wantArgs; i++ {
		// Into the concrete parameter type, not into interface{}. This is the
		// line the naive version gets wrong: a JSON object must land in a
		// config.Host, a JSON number in a uint32, without a panic.
		pv := reflect.New(mt.In(i + 1))
		if err := json.Unmarshal(rawArgs[i], pv.Interface()); err != nil {
			return nil, nil, fmt.Errorf("webrpc: %s argument %d: %w", method, i, err)
		}
		in = append(in, pv.Elem())
	}

	out := m.Func.Call(in)
	return interpretReturns(out)
}

// interpretReturns maps a method's return values onto (result, appErr).
//
// The four shapes that actually occur, all present in the App: (), (T), (error),
// (T, error). A trailing error is separated from a value so the transport can
// reject on it.
func interpretReturns(out []reflect.Value) (result any, appErr error, reqErr error) {
	switch len(out) {
	case 0:
		return nil, nil, nil
	case 1:
		if out[0].Type() == errType || out[0].Type().Implements(errType) {
			return nil, asError(out[0]), nil
		}
		return out[0].Interface(), nil, nil
	case 2:
		return out[0].Interface(), asError(out[1]), nil
	default:
		return nil, nil, fmt.Errorf("webrpc: method returns %d values, which is not supported", len(out))
	}
}

func asError(v reflect.Value) error {
	if v.IsNil() {
		return nil
	}
	return v.Interface().(error)
}
