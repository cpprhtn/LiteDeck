package linuxsystemd

// Human-output parsing, quarantined in this one file.
//
// It exists only because `--output=json` is not universally available:
// systemd gained JSON table output in v246, but the v1.0 support matrix (§2.2)
// includes Ubuntu 20.04 (systemd 245) and RHEL/Rocky 8 (systemd 239). Worse,
// older systemd *accepts* --output=json and silently prints the ordinary table
// instead of failing, so the format has to be chosen up front from the version
// rather than discovered from the output.
//
// Everything here is priority 4 in §3.2c — last resort, golden-file tested.

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// MinJSONOutputVersion is the first systemd release whose `systemctl
// list-units --output=json` actually emits JSON.
const MinJSONOutputVersion = 246

// SupportsJSONOutput reports whether a server's systemd can be asked for JSON.
func SupportsJSONOutput(version int) bool { return version >= MinJSONOutputVersion }

// ParseSystemdVersion extracts the major version from `systemctl --version`,
// whose first line looks like "systemd 249 (249.11-0ubuntu3.21)". Detect()
// records this so the adapter can pick an output format (§3.3).
func ParseSystemdVersion(out string) (int, error) {
	fields := strings.Fields(strings.TrimSpace(out))
	for i, f := range fields {
		if !strings.EqualFold(f, "systemd") || i+1 >= len(fields) {
			continue
		}
		// The token may be "249" or, on some builds, "249.11".
		num := fields[i+1]
		if dot := strings.IndexByte(num, '.'); dot > 0 {
			num = num[:dot]
		}
		v, err := strconv.Atoi(num)
		if err != nil {
			return 0, fmt.Errorf("linuxsystemd: unparsable systemd version %q", fields[i+1])
		}
		return v, nil
	}
	return 0, fmt.Errorf("linuxsystemd: no version found in %q", out)
}

// ParseListUnitsTable parses
// `systemctl list-units --type=service --all --plain --no-legend`.
//
// Columns are UNIT, LOAD, ACTIVE, SUB, DESCRIPTION, whitespace-separated, with
// the description running to end of line. --plain suppresses the leading "●"
// marker on failed units, but it is stripped defensively anyway.
func ParseListUnitsTable(data []byte) ([]ServiceUnit, error) {
	units := []ServiceUnit{} // non-nil: a nil slice marshals to JSON null
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t")
		line = strings.TrimPrefix(strings.TrimLeft(line, " \t"), "●")
		line = strings.TrimLeft(line, " \t")
		if line == "" {
			continue
		}
		// A trailing summary such as "30 loaded units listed." can survive
		// --no-legend on some versions; it has no dotted unit name.
		fields, rest := splitFields(line, 4)
		if len(fields) < 4 || !strings.Contains(fields[0], ".") {
			continue
		}
		units = append(units, ServiceUnit{
			Name:        fields[0],
			Load:        fields[1],
			Active:      fields[2],
			Sub:         fields[3],
			Description: strings.TrimSpace(rest),
			Template:    isTemplate(fields[0]),
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("linuxsystemd: read list-units table: %w", err)
	}
	return units, nil
}

// ParseUnitFilesTable parses
// `systemctl list-unit-files --type=service --plain --no-legend`.
//
// Columns are UNIT FILE, STATE and — only on systemd 245 and later — VENDOR
// PRESET, so the third column is optional.
func ParseUnitFilesTable(data []byte) (map[string]ServiceUnit, error) {
	out := make(map[string]ServiceUnit)
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.Contains(fields[0], ".") {
			continue
		}
		u := ServiceUnit{
			Name:     fields[0],
			Enabled:  undash(fields[1]),
			Template: isTemplate(fields[0]),
		}
		if len(fields) >= 3 {
			u.Preset = undash(fields[2])
		}
		out[u.Name] = u
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("linuxsystemd: read list-unit-files table: %w", err)
	}
	return out, nil
}

// undash normalises the table's placeholder for "no value". The tabular output
// prints a literal "-" where the JSON output emits an empty string; without
// this the two parsers disagree on every unit that has no preset. Found by the
// cross-format golden test, not by reading the systemd source.
func undash(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

// splitFields pulls the first n whitespace-separated tokens off line and
// returns them along with the untouched remainder.
func splitFields(line string, n int) ([]string, string) {
	fields := make([]string, 0, n)
	rest := line
	for range n {
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			break
		}
		end := strings.IndexAny(rest, " \t")
		if end < 0 {
			fields = append(fields, rest)
			return fields, ""
		}
		fields = append(fields, rest[:end])
		rest = rest[end:]
	}
	return fields, strings.TrimLeft(rest, " \t")
}
