// Package linuxsystemd implements the server adapter for systemd-based Linux
// distributions — the only adapter shipping in v1.0 (§2.2).
package linuxsystemd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ServiceUnit is one row of the service view (§4.3).
//
// It is assembled from two commands, because neither one alone carries
// everything the view needs: `list-units` knows the runtime state but not
// whether a unit is enabled, and `list-unit-files` knows the reverse.
type ServiceUnit struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`

	// Runtime state, from `systemctl list-units`. Empty when the unit exists on
	// disk but systemd has not loaded it — a disabled service, typically.
	Load   string `json:"load,omitempty"`   // loaded, not-found, masked
	Active string `json:"active,omitempty"` // active, inactive, failed
	Sub    string `json:"sub,omitempty"`    // running, dead, exited, ...

	// Install state, from `systemctl list-unit-files`. Empty for units with no
	// unit file, such as those created by a generator.
	Enabled string `json:"enabled,omitempty"` // enabled, disabled, static, masked, ...
	Preset  string `json:"preset,omitempty"`

	// Template reports a unit template such as getty@.service, which cannot be
	// started directly — only instances of it can. The UI greys these out.
	Template bool `json:"template,omitempty"`
}

// Loaded reports whether systemd currently has the unit loaded.
func (u ServiceUnit) Loaded() bool { return u.Load != "" }

// Failed reports whether the unit is in the failed state, which the view
// colours and offers as a filter (§4.3).
func (u ServiceUnit) Failed() bool { return u.Active == "failed" }

// listUnitsRow mirrors one element of `systemctl list-units --output=json`.
type listUnitsRow struct {
	Unit        string `json:"unit"`
	Load        string `json:"load"`
	Active      string `json:"active"`
	Sub         string `json:"sub"`
	Description string `json:"description"`
}

// unitFileRow mirrors one element of `systemctl list-unit-files --output=json`.
//
// The preset column is named vendor_preset up to systemd 252 and preset after
// it. Both are accepted so a distro bump does not silently blank the field.
type unitFileRow struct {
	UnitFile     string `json:"unit_file"`
	State        string `json:"state"`
	VendorPreset string `json:"vendor_preset"`
	Preset       string `json:"preset"`
}

// ParseListUnits parses `systemctl list-units --type=service --all --output=json`.
func ParseListUnits(data []byte) ([]ServiceUnit, error) {
	var rows []listUnitsRow
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("linuxsystemd: parse list-units: %w", err)
	}
	units := make([]ServiceUnit, 0, len(rows))
	for _, r := range rows {
		if r.Unit == "" {
			continue
		}
		units = append(units, ServiceUnit{
			Name:        r.Unit,
			Description: r.Description,
			Load:        r.Load,
			Active:      r.Active,
			Sub:         r.Sub,
			Template:    isTemplate(r.Unit),
		})
	}
	return units, nil
}

// ParseUnitFiles parses `systemctl list-unit-files --type=service --output=json`,
// keyed by unit name.
func ParseUnitFiles(data []byte) (map[string]ServiceUnit, error) {
	var rows []unitFileRow
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("linuxsystemd: parse list-unit-files: %w", err)
	}
	out := make(map[string]ServiceUnit, len(rows))
	for _, r := range rows {
		if r.UnitFile == "" {
			continue
		}
		preset := r.VendorPreset
		if preset == "" {
			preset = r.Preset
		}
		out[r.UnitFile] = ServiceUnit{
			Name:     r.UnitFile,
			Enabled:  r.State,
			Preset:   preset,
			Template: isTemplate(r.UnitFile),
		}
	}
	return out, nil
}

// MergeServices combines the two listings into the rows the view renders.
//
// The result is the union, not the intersection. A unit that is installed but
// disabled never gets loaded, so it is absent from list-units — yet it must
// still appear, because enabling it is exactly what the user came to do.
// Sorted by name so polling produces a stable order.
func MergeServices(loaded []ServiceUnit, files map[string]ServiceUnit) []ServiceUnit {
	merged := make(map[string]ServiceUnit, len(files)+len(loaded))
	for name, f := range files {
		merged[name] = f
	}
	for _, u := range loaded {
		if f, ok := merged[u.Name]; ok {
			u.Enabled = f.Enabled
			u.Preset = f.Preset
		}
		merged[u.Name] = u
	}

	out := make([]ServiceUnit, 0, len(merged))
	for _, u := range merged {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// isTemplate reports whether name is a unit template (foo@.service) rather
// than a concrete unit or an instance (foo@bar.service).
func isTemplate(name string) bool {
	at := strings.LastIndex(name, "@")
	if at < 0 {
		return false
	}
	dot := strings.LastIndex(name, ".")
	return dot == at+1
}
