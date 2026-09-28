package rollback

// RenameHosts moves every entry from an old host ID to its new one.
//
// The undo list is the only thing standing behind a night of unattended AI
// writes, and it is keyed by host ID. A host that gets a new ID (config's UUID
// migration) leaves its copies behind under the old one: the blobs are still on
// disk, the index still names them, and the panel shows nothing — which reads as
// "there is nothing to put back" at the one moment that sentence matters.
//
// Entries whose host is not being renamed are untouched, so this is safe to call
// with a map that only partly overlaps the history.
func (s *Store) RenameHosts(renamed map[string]string) error {
	if len(renamed) == 0 {
		return nil
	}
	s.mu.Lock()
	changed := false
	for i := range s.entries {
		if id, ok := renamed[s.entries[i].HostID]; ok {
			s.entries[i].HostID = id
			changed = true
		}
	}
	s.mu.Unlock()
	if !changed {
		return nil
	}
	return s.save()
}
