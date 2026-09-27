package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// Giving every host a stable identifier (§6).
//
// # Why
//
// A host's ID used to be the moment it was created — `host-1786033219477533000`
// — or, for one imported from ssh_config, the alias in that file. Neither
// travels. The same server added on a laptop and on a desktop gets two
// different IDs, which is fine while the two machines never speak and is the
// whole problem the moment they do.
//
// # What makes this more than a rename
//
// The ID is a foreign key in four other places, and every one of them is silent
// when it breaks:
//
//   - the OS credential store, keyed `kind:hostID`, holding the login password,
//     the key passphrase and (opt-in) the sudo password;
//   - seven maps in settings.json, keyed by host ID — which server is shared
//     with AI clients, its write policy, its delete and exec toggles, when it
//     was last looked at, whether its shell history may be read, and which
//     addresses have been seen logging in;
//   - typed.json, the command history typed in LiteDeck's own terminal, a map
//     from host ID to its lines;
//   - ai-history/index.json, which names the file contents an AI overwrote so
//     they can be put back — by host ID;
//   - hosts.json itself.
//
// Rename the host and forget the rest and the user is told nothing. They are
// asked for a password they had saved, an AI client quietly loses access to a
// server they shared, the security tab forgets which addresses it had been
// taught to recognise, and last night's unattended writes stop being undoable.
// So this moves all of it, or it moves none of it.
//
// The last two live outside this package — the terminal history in internal/app
// and the undo copies in internal/rollback — so they arrive as HostRenamer.
//
// # Hosts from ssh_config keep their names
//
// Those are re-derived from `~/.ssh/config` on every import and matched by ID
// (app/hosts.go). Renaming one means the next import does not recognise it and
// adds a second copy. They are also deliberately out of scope for sync, for the
// same reason: that file is already the user's own way of carrying hosts
// between machines.

// LegacyIDPrefix marks the IDs this migration replaces.
//
// Anything else — an ssh_config alias, or an ID already migrated — is left
// alone, which is what makes running this twice harmless.
const LegacyIDPrefix = "host-"

// SSHConfigSource is Host.Source for an entry imported from ~/.ssh/config.
const SSHConfigSource = "ssh_config"

// SecretMover is the part of the credential store this migration needs.
//
// An interface rather than *secret.Keyring so config does not import secret —
// and so the test can be a map, and can be made to fail halfway.
type SecretMover interface {
	// Available reports whether the credential store can be reached at all. A
	// headless Linux box with no D-Bus Secret Service has none, and there is
	// then nothing stored to move.
	Available() bool
	Get(hostID, kind string) (string, error)
	Set(hostID, kind, value string) error
	Delete(hostID, kind string) error
}

// SecretKinds are the credential kinds stored per host.
//
// Listed here rather than taken from the secret package to keep the dependency
// pointing one way. TestSecretKindsAreAllOfThem holds the two lists together.
var SecretKinds = []string{"password", "passphrase", "sudo"}

// HostRenamer is any other store keyed by host ID.
//
// The two that exist are the typed-command history (internal/app) and the undo
// copies (internal/rollback); both sit above this package, so they are passed in
// rather than imported. A renamer that fails stops the migration before
// hosts.json is written, which is what makes the next start retry it.
type HostRenamer interface {
	RenameHosts(renamed map[string]string) error
}

// MigrationReport says what moved, for the log and for the tests.
type MigrationReport struct {
	// Renamed maps each old ID to the new one.
	Renamed map[string]string
	// Secrets is how many credential entries were carried over.
	Secrets int
	// SecretsSkipped is true where the credential store could not be reached,
	// so nothing was moved and nothing was lost — there was nothing there.
	SecretsSkipped bool
	// Kept is the IDs deliberately left alone: ssh_config entries and anything
	// already migrated.
	Kept []string
}

// NeedsHostIDMigration reports whether any host still carries a generated ID.
func NeedsHostIDMigration(hosts []Host) bool {
	for _, h := range hosts {
		if migratable(h) {
			return true
		}
	}
	return false
}

func migratable(h Host) bool {
	return h.Source != SSHConfigSource && strings.HasPrefix(h.ID, LegacyIDPrefix)
}

// MigrateHostIDs gives every eligible host a UUID and carries everything keyed
// by the old ID across with it.
//
// # Order
//
// Secrets first, then settings, then hosts.json last. That order is what makes
// a crash survivable: the credential store is written new-then-old-deleted, so
// an interruption leaves a duplicate rather than a hole, and hosts.json still
// names the old ID — so the next start runs the same migration again and
// finishes it. Writing hosts.json first would strand every secret behind an ID
// no host claims any more.
func MigrateHostIDs(store *Store, settings *SettingsStore, secrets SecretMover, others ...HostRenamer) (MigrationReport, error) {
	rep := MigrationReport{Renamed: map[string]string{}}
	if store == nil {
		return rep, nil
	}

	hosts := store.List()
	var moving []Host
	for _, h := range hosts {
		if migratable(h) {
			moving = append(moving, h)
			continue
		}
		rep.Kept = append(rep.Kept, h.ID)
	}
	if len(moving) == 0 {
		return rep, nil
	}

	for _, h := range moving {
		id, err := newUUID()
		if err != nil {
			return rep, fmt.Errorf("config: new host id: %w", err)
		}
		rep.Renamed[h.ID] = id
	}

	// 1. Credentials. New name written before the old one is dropped.
	if secrets != nil && secrets.Available() {
		for old, id := range rep.Renamed {
			for _, kind := range SecretKinds {
				v, err := secrets.Get(old, kind)
				if err != nil || v == "" {
					// Not stored, or the store refused this one. Either way
					// there is nothing to carry and nothing to delete.
					continue
				}
				if err := secrets.Set(id, kind, v); err != nil {
					return rep, fmt.Errorf("config: carry %s for %s: %w", kind, old, err)
				}
				// Only now. A delete that ran first would lose the secret if
				// the write failed.
				_ = secrets.Delete(old, kind)
				rep.Secrets++
			}
		}
	} else {
		rep.SecretsSkipped = true
	}

	// 2. settings.json — every map keyed by host ID.
	if settings != nil {
		if err := settings.RenameHosts(rep.Renamed); err != nil {
			return rep, fmt.Errorf("config: carry settings: %w", err)
		}
	}

	// 3. Everything else keyed by host ID and living above this package.
	for _, r := range others {
		if r == nil {
			continue
		}
		if err := r.RenameHosts(rep.Renamed); err != nil {
			return rep, fmt.Errorf("config: carry host-keyed store: %w", err)
		}
	}

	// 4. hosts.json last, which is what makes an interrupted run repeatable.
	for i := range hosts {
		id, ok := rep.Renamed[hosts[i].ID]
		if !ok {
			continue
		}
		// The old ID is kept, not thrown away: it is what a bug report says
		// ("my host-1786… vanished"), and what a future reader needs to
		// understand a settings file written before this ran.
		hosts[i].LegacyID = hosts[i].ID
		hosts[i].ID = id
	}
	if err := store.ReplaceAll(hosts); err != nil {
		return rep, fmt.Errorf("config: write hosts: %w", err)
	}
	return rep, nil
}

// newUUID returns a version 4 UUID in the lowercase hyphenated form.
//
// Hand-rolled rather than a dependency: it is eight lines, and the alternative
// is a module in go.mod for something crypto/rand already does. Lowercase is
// load-bearing — a sync repository names its files after these, and macOS and
// Windows would fold two differently-cased names into one.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 1
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}
