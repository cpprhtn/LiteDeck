package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/cpprhtn/LiteDeck/internal/config"
	"github.com/cpprhtn/LiteDeck/internal/rollback"
	"github.com/cpprhtn/LiteDeck/internal/secret"
)

// The two host-keyed stores that live outside internal/config come across too.
//
// config's own tests cover hosts.json, settings.json and the credential store.
// These two cannot be tested there — the terminal history belongs to this
// package and the undo copies to rollback — and both fail silently: the typed
// column of the command-history panel goes empty, and the list of things an AI
// overwrote last night goes empty, which reads as "there is nothing to put
// back".
func TestMigrationCarriesTypedHistoryAndUndoCopies(t *testing.T) {
	dir := t.TempDir()
	const old = "host-1786033219477533000"

	store, err := config.Open(dir)
	if err != nil {
		t.Fatalf("open hosts: %v", err)
	}
	if err := store.ReplaceAll([]config.Host{{
		ID: old, Name: "prod", Hostname: "10.0.0.5", Port: 22, User: "deploy",
		Auth: []config.AuthMethod{config.AuthPassword},
	}}); err != nil {
		t.Fatalf("seed hosts: %v", err)
	}

	a := New()
	a.hosts = store
	a.settings = config.OpenSettings(dir)
	a.typed = newTypedLog(dir)
	a.rollback = rollback.Open(dir)
	secrets := newMemSecrets()
	_ = secrets.Set(old, secret.KindPassword, "hunter2")
	a.secrets = secrets

	if got := a.typed.enter(old, "term-1", "systemctl restart nginx", false); got == nil {
		t.Fatal("seed typed history: nothing recorded")
	}
	entry, err := a.rollback.Record(old, "/etc/nginx/nginx.conf", rollback.ActionWrite, []byte("worker_processes 1;\n"), false)
	if err != nil {
		t.Fatalf("seed rollback: %v", err)
	}

	if err := a.migrateHostIDs(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	hosts := a.hosts.List()
	if len(hosts) != 1 {
		t.Fatalf("hosts = %+v", hosts)
	}
	id := hosts[0].ID
	if id == old {
		t.Fatal("the host was not renamed")
	}

	// The terminal history, in memory and on disk.
	rows := a.typed.list(id)
	if len(rows) != 1 {
		t.Fatalf("typed history under the new id = %+v", rows)
	}
	if rows[0].HostID != id {
		t.Errorf("the row still names %q; the panel merges three sources by this field", rows[0].HostID)
	}
	if left := a.typed.list(old); len(left) != 0 {
		t.Errorf("the old id still has history: %+v", left)
	}
	var stored map[string][]TypedCommand
	if b, err := os.ReadFile(filepath.Join(dir, "typed.json")); err != nil {
		t.Errorf("read typed.json: %v", err)
	} else if json.Unmarshal(b, &stored) != nil {
		t.Errorf("typed.json is not readable: %s", b)
	} else if len(stored[id]) != 1 || len(stored[old]) != 0 {
		t.Errorf("typed.json was not rewritten: %v", stored)
	}

	// The undo list, in memory and on disk.
	if list := a.rollback.List(id); len(list) != 1 || list[0].ID != entry.ID {
		t.Errorf("undo copies under the new id = %+v", list)
	}
	if list := a.rollback.List(old); len(list) != 0 {
		t.Errorf("undo copies still under the old id: %+v", list)
	}
	var index []rollback.Entry
	if b, err := os.ReadFile(filepath.Join(dir, "ai-history", "index.json")); err != nil {
		t.Errorf("read index.json: %v", err)
	} else if json.Unmarshal(b, &index) != nil {
		t.Errorf("index.json is not readable: %s", b)
	} else if len(index) != 1 || index[0].HostID != id {
		t.Errorf("index.json was not rewritten: %+v", index)
	}

	// And the thing the user would notice first.
	if got, err := secrets.Get(id, secret.KindPassword); err != nil || got != "hunter2" {
		t.Errorf("the saved password did not move: %q %v", got, err)
	}
}

// Every kind of secret the app can store is a kind the migration carries.
//
// config.SecretKinds is a copy, written out by hand to keep config from
// importing secret. A kind added on one side and not the other is not a compile
// error and not a visible failure: the secret simply stays behind under an ID no
// host claims, and the user is asked for a password they had saved.
func TestSecretKindsAreAllOfThem(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "secret", "secret.go"))
	if err != nil {
		t.Fatalf("read secret.go: %v", err)
	}
	re := regexp.MustCompile(`Kind\s*=\s*"([a-z]+)"`)
	var declared []string
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		declared = append(declared, m[1])
	}
	if len(declared) == 0 {
		t.Fatal("no Kind constants found — this test is reading the wrong file")
	}
	for _, kind := range declared {
		found := false
		for _, k := range config.SecretKinds {
			if k == kind {
				found = true
			}
		}
		if !found {
			t.Errorf("secret.Kind %q is not in config.SecretKinds, so the host-ID "+
				"migration leaves it behind and the user is asked for a secret they saved", kind)
		}
	}
	if len(config.SecretKinds) != len(declared) {
		t.Errorf("config.SecretKinds = %v, secret declares %v", config.SecretKinds, declared)
	}
}

// boot runs the migration, after the host list and before MCP.
//
// Order is the whole point: it has to happen after hosts.json is open (there is
// nothing to rename before that) and before anything reads an ID — MCP first,
// which answers for every shared host by ID.
func TestBootMigratesBeforeAnythingReadsAnID(t *testing.T) {
	src := readAppSource(t, "app.go")
	i := strings.Index(src, "func (a *App) boot()")
	if i < 0 {
		t.Fatal("boot() is gone — this test needs rewriting")
	}
	body := src[i:]
	open := strings.Index(body, "config.Open(dir)")
	migrate := strings.Index(body, "a.migrateHostIDs()")
	mcp := strings.Index(body, "a.startMCP()")
	if migrate < 0 {
		t.Fatal("boot() does not migrate host IDs, so hosts added before this release never get one that travels")
	}
	if !(open < migrate && migrate < mcp) {
		t.Errorf("order in boot() is open=%d migrate=%d startMCP=%d", open, migrate, mcp)
	}
}

// A store that has already been migrated is not touched again.
//
// It runs at every start. A second UUID would orphan everything the first run
// carried across, which is worse than never having run it.
func TestMigrationDoesNothingOnASecondStart(t *testing.T) {
	dir := t.TempDir()
	store, err := config.Open(dir)
	if err != nil {
		t.Fatalf("open hosts: %v", err)
	}
	if err := store.ReplaceAll([]config.Host{{
		ID: "9f8d6c3a-1b2e-4d5f-8a90-112233445566", Name: "prod",
		Hostname: "10.0.0.5", Port: 22, User: "deploy",
		Auth: []config.AuthMethod{config.AuthPassword},
	}}); err != nil {
		t.Fatalf("seed hosts: %v", err)
	}
	before, err := os.Stat(filepath.Join(dir, "hosts.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	a := New()
	a.hosts = store
	a.settings = config.OpenSettings(dir)
	a.typed = newTypedLog(dir)
	a.rollback = rollback.Open(dir)

	time.Sleep(10 * time.Millisecond)
	if err := a.migrateHostIDs(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	after, err := os.Stat(filepath.Join(dir, "hosts.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("hosts.json was rewritten although there was nothing to migrate")
	}
	if a.hosts.List()[0].ID != "9f8d6c3a-1b2e-4d5f-8a90-112233445566" {
		t.Errorf("the id changed: %q", a.hosts.List()[0].ID)
	}
}
