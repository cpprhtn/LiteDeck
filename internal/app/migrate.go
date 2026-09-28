package app

import (
	"github.com/cpprhtn/LiteDeck/internal/config"
	"github.com/cpprhtn/LiteDeck/internal/secret"
)

// Giving the stored hosts IDs that travel (§2 of the sync design).
//
// The migration itself is in internal/config, which owns hosts.json and
// settings.json. This is the part that cannot live there: the credential store
// and the two host-keyed files that belong to this package and to rollback.

// secretMover adapts secret.Store to what the migration asks for.
//
// The kind crosses as a string so config does not have to import secret — the
// dependency points the other way everywhere else, and one small conversion here
// is cheaper than a package cycle. TestSecretKindsAreAllOfThem holds the two
// lists of kinds together, because a kind added to secret and not to
// config.SecretKinds would simply not be carried across, and the user would be
// asked for a password they had saved.
type secretMover struct{ store secret.Store }

func (m secretMover) Available() bool {
	return m.store != nil && m.store.Available()
}

func (m secretMover) Get(hostID, kind string) (string, error) {
	if m.store == nil {
		return "", secret.ErrUnavailable
	}
	return m.store.Get(hostID, secret.Kind(kind))
}

func (m secretMover) Set(hostID, kind, value string) error {
	if m.store == nil {
		return secret.ErrUnavailable
	}
	return m.store.Set(hostID, secret.Kind(kind), value)
}

func (m secretMover) Delete(hostID, kind string) error {
	if m.store == nil {
		return secret.ErrUnavailable
	}
	return m.store.Delete(hostID, secret.Kind(kind))
}

// migrateHostIDs runs the host-ID migration, if there is anything to move.
//
// # Where it sits
//
// After the host list is open and before startMCP, which is the last moment
// before anything else in the app reads an ID. Nothing has connected yet, so
// there is no live state keyed by the old name to keep in step — the whole
// problem is on disk.
//
// # A failure is not fatal
//
// It returns an error and the caller notes it; the app still opens. An
// interrupted run leaves hosts.json naming the old IDs, so the next start tries
// again, and in the meantime everything works exactly as it did before — the old
// IDs are perfectly good IDs, they just do not travel between machines.
func (a *App) migrateHostIDs() error {
	if a.hosts == nil {
		return nil
	}
	if !config.NeedsHostIDMigration(a.hosts.List()) {
		return nil
	}
	// Nothing is logged on success. The trace a bug report needs is in the file
	// itself — hosts.json keeps the old name as legacyId — and that outlives any
	// line written to a console nobody is reading.
	_, err := config.MigrateHostIDs(a.hosts, a.settings, secretMover{a.secrets}, a.typed, a.rollback)
	return err
}
