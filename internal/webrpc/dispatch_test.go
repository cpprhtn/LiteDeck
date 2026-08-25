package webrpc

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// A stand-in with the same signature shapes the real App has, so these tests
// pin the marshaling rules without dragging the whole app in. The shapes are
// the ones the reviewer flagged as panic-prone: a struct parameter, a uint32
// parameter, and the four return arities.

type hostArg struct {
	ID   string `json:"id"`
	Port int    `json:"port"`
}

type fakeApp struct{ saved *hostArg }

// bare value, no args (like Bootstrap/Platform)
func (f *fakeApp) Ping() string { return "pong" }

// struct parameter (like SaveHost(config.Host)) — the naive []any path panics here
func (f *fakeApp) SaveHost(h hostArg) error {
	f.saved = &h
	return nil
}

// numeric widening (like Chmod(..., perm uint32)) — JSON number is float64
func (f *fakeApp) Chmod(path string, perm uint32) uint32 { return perm }

// (T, error) with a real error, to prove rejection
func (f *fakeApp) Connect(id string) (string, error) {
	if id == "" {
		return "", errors.New("no host")
	}
	return "connected:" + id, nil
}

// error-only (like ConnectHost/DeleteHost)
func (f *fakeApp) MustFail() error { return errors.New("boom") }

// void
func (f *fakeApp) Clear() {}

// a lifecycle method that must NOT be exposed — takes a real context.Context,
// exactly like App.Shutdown, which is what the exclusion rule keys on.
func (f *fakeApp) Shutdown(_ context.Context) {}

func args(t *testing.T, vs ...any) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, len(vs))
	for i, v := range vs {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal arg %d: %v", i, err)
		}
		out[i] = b
	}
	return out
}

func newD() (*Dispatcher, *fakeApp) {
	f := &fakeApp{}
	return New(f), f
}

func TestBareValueReturn(t *testing.T) {
	d, _ := newD()
	res, appErr, reqErr := d.Call("Ping", nil)
	if reqErr != nil || appErr != nil {
		t.Fatalf("errs: req=%v app=%v", reqErr, appErr)
	}
	if res != "pong" {
		t.Errorf("result = %v", res)
	}
}

// The reviewer's B1 case #1: a JSON object must land in a struct parameter. The
// naive `[]any` + reflect.Call panics here; this proves the typed-unmarshal path
// does not.
func TestStructParameterUnmarshals(t *testing.T) {
	d, f := newD()
	_, appErr, reqErr := d.Call("SaveHost", args(t, map[string]any{"id": "h1", "port": 2222}))
	if reqErr != nil || appErr != nil {
		t.Fatalf("errs: req=%v app=%v", reqErr, appErr)
	}
	if f.saved == nil || f.saved.ID != "h1" || f.saved.Port != 2222 {
		t.Errorf("struct did not decode into the parameter: %+v", f.saved)
	}
}

// Proof, not assertion: the same JSON decoded to interface{} (the naive path)
// and reflect-called into the struct slot really does panic. This is why the
// dispatcher unmarshals into reflect.New(paramType) instead.
func TestNaiveAnyPathWouldPanic(t *testing.T) {
	var decoded any
	if err := json.Unmarshal([]byte(`{"id":"h1","port":2222}`), &decoded); err != nil {
		t.Fatal(err)
	}
	m := reflect.ValueOf(&fakeApp{}).MethodByName("SaveHost")

	defer func() {
		if recover() == nil {
			t.Error("expected the naive interface{}->struct reflect.Call to panic; " +
				"if this stops panicking the typed-unmarshal ceremony could be simplified")
		}
	}()
	m.Call([]reflect.Value{reflect.ValueOf(decoded)}) // map[string]interface{} into hostArg → panic
}

// The reviewer's B1 case #2: JSON numbers are float64; a uint32 parameter must
// not panic.
func TestNumericParameterWidens(t *testing.T) {
	d, _ := newD()
	res, appErr, reqErr := d.Call("Chmod", args(t, "/etc/x", 0o644))
	if reqErr != nil || appErr != nil {
		t.Fatalf("errs: req=%v app=%v", reqErr, appErr)
	}
	if res != uint32(0o644) {
		t.Errorf("result = %v (%T), want uint32 420", res, res)
	}
}

// A method's non-nil error must surface as appErr so the browser promise
// REJECTS — a failed Connect must not resolve as success.
func TestMethodErrorIsSeparatedFromResult(t *testing.T) {
	d, _ := newD()

	_, appErr, reqErr := d.Call("Connect", args(t, ""))
	if reqErr != nil {
		t.Fatalf("req err: %v", reqErr)
	}
	if appErr == nil {
		t.Fatal("a method that returned an error reported success — the promise would resolve")
	}

	res, appErr, _ := d.Call("Connect", args(t, "prod"))
	if appErr != nil {
		t.Fatalf("app err on success path: %v", appErr)
	}
	if res != "connected:prod" {
		t.Errorf("result = %v", res)
	}
}

func TestErrorOnlyAndVoidReturns(t *testing.T) {
	d, _ := newD()
	if _, appErr, _ := d.Call("MustFail", nil); appErr == nil {
		t.Error("error-only method did not surface its error")
	}
	if res, appErr, reqErr := d.Call("Clear", nil); res != nil || appErr != nil || reqErr != nil {
		t.Errorf("void call = (%v, %v, %v)", res, appErr, reqErr)
	}
}

// B2: a lifecycle method (takes a context-shaped arg) must not be reachable.
func TestLifecycleMethodsAreNotExposed(t *testing.T) {
	d, _ := newD()
	if _, _, reqErr := d.Call("Shutdown", nil); reqErr == nil {
		t.Fatal("Shutdown is reachable over the wire — it tears down every connection")
	} else if _, ok := reqErr.(ErrNoMethod); !ok {
		t.Errorf("want ErrNoMethod, got %T: %v", reqErr, reqErr)
	}
	for _, name := range d.Methods() {
		if name == "Shutdown" {
			t.Error("Shutdown is in the exposed set")
		}
	}
}

func TestWrongArgCountIsARequestError(t *testing.T) {
	d, _ := newD()
	// SaveHost wants 1 arg; give 0.
	if _, _, reqErr := d.Call("SaveHost", nil); reqErr == nil {
		t.Error("a short argument array was accepted — reflect.Call would panic on it")
	}
}

func TestUnknownMethodIsNoMethod(t *testing.T) {
	d, _ := newD()
	_, _, reqErr := d.Call("Nope", nil)
	if _, ok := reqErr.(ErrNoMethod); !ok {
		t.Errorf("want ErrNoMethod, got %v", reqErr)
	}
	if !strings.Contains(reqErr.Error(), "Nope") {
		t.Errorf("error should name the method: %v", reqErr)
	}
}
