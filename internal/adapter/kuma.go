package adapter

// Uptime Kuma's Prometheus endpoint.
//
// Kuma's own README is explicit that its API is built for its own web UI and
// that third-party integrations are not a supported surface. `/metrics` is the
// exception: it exists *for* outside readers, it is Prometheus text format
// rather than something bespoke, and the four series below have carried the
// same names and the same label set for as long as the endpoint has existed.
// So that is all this reads. Nothing here touches /api/*, which is the part the
// README is warning about.
//
// Priority-4 by the table in arch/02 — human-ish output, parsed — so it is
// isolated in its own file and golden tested, like the ss parser.

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Kuma's own encoding of monitor_status. The comment on the HELP line of the
// endpoint spells these out; they are not guessed.
const (
	kumaDown        = 0
	kumaUp          = 1
	kumaPending     = 2
	kumaMaintenance = 3
)

// KumaMonitor is one monitor as /metrics reports it.
type KumaMonitor struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	// Target is the URL for an http monitor, host:port otherwise. Kuma writes
	// the string "null" into the labels that do not apply to a given type, so
	// this picks the one that does rather than handing on both.
	Target string `json:"target,omitempty"`
	// Status is up, down, pending, maintenance or unknown.
	Status string `json:"status"`
	// ResponseMs is absent when the monitor is down — Kuma emits NaN there, and
	// a response time of zero would read as "instant" rather than "no answer".
	ResponseMs float64 `json:"responseMs,omitempty"`
	// CertDays is days until the TLS certificate expires, for monitors that
	// have one. Absent otherwise.
	CertDays int  `json:"certDays,omitempty"`
	HasCert  bool `json:"-"`
}

// Down reports whether this monitor is failing. Pending is not down: it is a
// monitor that has failed once and not yet exhausted its retries, and calling
// it down would page somebody for a blip.
func (m KumaMonitor) Down() bool { return m.Status == "down" }

// KumaSnapshot is one read of the endpoint.
type KumaSnapshot struct {
	Monitors    []KumaMonitor `json:"monitors"`
	Up          int           `json:"up"`
	Down        int           `json:"down"`
	Pending     int           `json:"pending"`
	Maintenance int           `json:"maintenance"`
}

// ParseKumaMetrics reads the Prometheus text at /metrics.
//
// Series other than the four Kuma emits per monitor are ignored rather than
// rejected: a future version adding one must not empty this view.
func ParseKumaMetrics(data []byte) (KumaSnapshot, error) {
	var snap KumaSnapshot
	snap.Monitors = []KumaMonitor{}

	// Keyed on the raw label text, which is identical across all four series
	// for one monitor. Keying on the name would merge two monitors a user
	// deliberately gave the same label to.
	byLabels := map[string]*KumaMonitor{}
	var order []string

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, rawLabels, value, ok := splitPromSample(line)
		if !ok {
			continue
		}
		switch name {
		case "monitor_status", "monitor_response_time", "monitor_cert_days_remaining":
		default:
			continue
		}

		m, seen := byLabels[rawLabels]
		if !seen {
			labels := parsePromLabels(rawLabels)
			m = &KumaMonitor{
				Name:   labels["monitor_name"],
				Type:   labels["monitor_type"],
				Target: kumaTarget(labels),
				Status: "unknown",
			}
			byLabels[rawLabels] = m
			order = append(order, rawLabels)
		}

		switch name {
		case "monitor_status":
			m.Status = kumaStatus(value)
		case "monitor_response_time":
			// NaN is what Kuma writes while a monitor is down. Carrying it
			// through would serialise as a JSON error, and rounding it to zero
			// would claim an instant reply.
			if !math.IsNaN(value) && value >= 0 {
				m.ResponseMs = value
			}
		case "monitor_cert_days_remaining":
			if !math.IsNaN(value) {
				m.CertDays = int(value)
				m.HasCert = true
			}
		}
	}
	if err := sc.Err(); err != nil {
		return snap, fmt.Errorf("adapter: read kuma metrics: %w", err)
	}

	for _, key := range order {
		snap.Monitors = append(snap.Monitors, *byLabels[key])
	}

	// Failing first, then by name. Whoever opens this is looking for what is
	// broken, and a stable order underneath keeps a poll from reshuffling rows
	// that did not change.
	sort.SliceStable(snap.Monitors, func(i, j int) bool {
		ri, rj := kumaRank(snap.Monitors[i].Status), kumaRank(snap.Monitors[j].Status)
		if ri != rj {
			return ri < rj
		}
		return snap.Monitors[i].Name < snap.Monitors[j].Name
	})

	for _, m := range snap.Monitors {
		switch m.Status {
		case "up":
			snap.Up++
		case "down":
			snap.Down++
		case "pending":
			snap.Pending++
		case "maintenance":
			snap.Maintenance++
		}
	}
	return snap, nil
}

func kumaStatus(v float64) string {
	switch int(v) {
	case kumaDown:
		return "down"
	case kumaUp:
		return "up"
	case kumaPending:
		return "pending"
	case kumaMaintenance:
		return "maintenance"
	default:
		return "unknown"
	}
}

func kumaRank(status string) int {
	switch status {
	case "down":
		return 0
	case "pending":
		return 1
	case "maintenance":
		return 2
	case "up":
		return 3
	default:
		return 4
	}
}

// kumaTarget picks the label that applies to this monitor's type.
//
// Kuma fills the inapplicable ones with the literal string "null" rather than
// leaving them out, so a naive join produces "null:null" for every HTTP check.
func kumaTarget(labels map[string]string) string {
	if u := kumaLabel(labels, "monitor_url"); u != "" {
		return u
	}
	host := kumaLabel(labels, "monitor_hostname")
	port := kumaLabel(labels, "monitor_port")
	switch {
	case host != "" && port != "":
		return host + ":" + port
	case host != "":
		return host
	}
	return ""
}

func kumaLabel(labels map[string]string, key string) string {
	v := strings.TrimSpace(labels[key])
	if v == "null" || v == "" {
		return ""
	}
	return v
}

// splitPromSample cuts one Prometheus sample line into its parts.
//
// `name{labels} value` or `name value`. Timestamps are permitted by the format
// and Kuma does not write them, but a trailing field must not turn the value
// into a parse failure.
func splitPromSample(line string) (name, labels string, value float64, ok bool) {
	brace := strings.IndexByte(line, '{')
	rest := line
	if brace >= 0 {
		end := strings.LastIndexByte(line, '}')
		if end < brace {
			return "", "", 0, false
		}
		name = strings.TrimSpace(line[:brace])
		labels = line[brace+1 : end]
		rest = line[end+1:]
	} else {
		sp := strings.IndexByte(line, ' ')
		if sp < 0 {
			return "", "", 0, false
		}
		name = line[:sp]
		rest = line[sp:]
	}

	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", "", 0, false
	}
	// ParseFloat handles "NaN", "+Inf" and "-Inf", which the format allows and
	// Kuma uses for a monitor that has not answered.
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "", "", 0, false
	}
	return name, labels, v, true
}

// parsePromLabels reads `a="1",b="2"` with the format's escaping.
//
// Written by hand rather than split on commas because a monitor named
// `db, replica` is legal and would otherwise cut the label set in half.
func parsePromLabels(s string) map[string]string {
	out := map[string]string{}
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ',' || s[i] == ' ') {
			i++
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 {
			return out
		}
		key := strings.TrimSpace(s[i : i+eq])
		i += eq + 1
		if i >= len(s) || s[i] != '"' {
			return out
		}
		i++ // opening quote

		var b strings.Builder
		for i < len(s) {
			c := s[i]
			if c == '\\' && i+1 < len(s) {
				switch s[i+1] {
				case 'n':
					b.WriteByte('\n')
				case '\\':
					b.WriteByte('\\')
				case '"':
					b.WriteByte('"')
				default:
					b.WriteByte(s[i+1])
				}
				i += 2
				continue
			}
			if c == '"' {
				i++
				break
			}
			b.WriteByte(c)
			i++
		}
		out[key] = b.String()
	}
	return out
}
