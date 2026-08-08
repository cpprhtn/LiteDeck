package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

// A nil Go slice marshals to JSON `null`, not `[]`. Across the Wails boundary
// that becomes `null` in TypeScript, and `null.length` throws during render —
// which unmounts the whole React tree and blanks the window. The user cannot
// tell that from a crash, because it is one.
//
// Every slice a binding can return must therefore be non-nil even when empty.
func TestEmptyResultsMarshalAsArrays(t *testing.T) {
	containers, err := ParseContainers([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	assertArray(t, "ParseContainers(empty)", containers)

	// A container with no published ports: the common case, and the one that
	// blanked the container tab.
	one, err := ParseContainers([]byte(
		`{"ID":"a","Names":"n","Image":"alpine","State":"exited","Status":"Exited (0) 1 second ago"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 {
		t.Fatalf("expected one container, got %d", len(one))
	}
	assertArray(t, "Container.Ports with no ports", one[0].Ports)

	procs, err := ParsePS([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	assertArray(t, "ParsePS(empty)", procs)

	assertArray(t, "ParsePorts(empty)", ParsePorts(""))
	assertArray(t, "Tree(empty)", Tree(nil))
}

func assertArray(t *testing.T, what string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("%s: marshal: %v", what, err)
	}
	if s := string(b); strings.HasPrefix(s, "null") {
		t.Errorf("%s marshals to %s — the frontend will crash on .length/.map", what, s)
	}
}
