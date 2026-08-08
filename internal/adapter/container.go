package adapter

// The container view (§4.5). Docker in v1.0; Podman speaks the same CLI so the
// same parser covers it, which is why this file is not docker-specific in name.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Container is one row of the container view.
type Container struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	Command string `json:"command"`
	// State is the machine value — running, exited, created, paused,
	// restarting, dead. Status is the human sentence ("Up 2 hours").
	State   string `json:"state"`
	Status  string `json:"status"`
	Created string `json:"created"`
	Ports   []Port `json:"ports"`
	// ExitCode is parsed out of Status for exited containers; -1 when unknown.
	ExitCode int      `json:"exitCode"`
	Networks []string `json:"networks,omitempty"`
	Size     string   `json:"size,omitempty"`
}

// Running reports whether the container is up.
func (c Container) Running() bool { return c.State == "running" }

// Port is one published port mapping.
type Port struct {
	HostIP    string `json:"hostIp,omitempty"`
	HostPort  string `json:"hostPort,omitempty"`
	Container string `json:"container"`
	Protocol  string `json:"protocol"`
}

func (p Port) String() string {
	if p.HostPort == "" {
		return p.Container + "/" + p.Protocol
	}
	return fmt.Sprintf("%s:%s→%s/%s", p.HostIP, p.HostPort, p.Container, p.Protocol)
}

// containerRow mirrors one line of `docker ps --format '{{json .}}'`.
type containerRow struct {
	ID       string `json:"ID"`
	Names    string `json:"Names"`
	Image    string `json:"Image"`
	Command  string `json:"Command"`
	State    string `json:"State"`
	Status   string `json:"Status"`
	Ports    string `json:"Ports"`
	Networks string `json:"Networks"`
	Created  string `json:"CreatedAt"`
	Size     string `json:"Size"`
}

// PSArgsContainers returns the argv for listing containers.
//
// `{{json .}}` rather than `--format json`: the latter only exists on Docker
// 23+, while the template form has worked since 17.06 and Podman accepts it
// too. One line of JSON per container.
func PSArgsContainers() []string {
	return []string{"ps", "-a", "--no-trunc", "--format", "{{json .}}"}
}

// ParseContainers parses the output of the command above.
func ParseContainers(data []byte) ([]Container, error) {
	// Non-nil even when empty: a nil slice marshals to JSON `null`, and the
	// frontend calling .length on it throws during render, which unmounts the
	// whole React tree and blanks the window (see nil_probe_test.go).
	out := []Container{}

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row containerRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			// One malformed line must not lose the rest of the list — a warning
			// row would be better than an empty tab.
			continue
		}
		if row.ID == "" {
			continue
		}

		c := Container{
			ID:       row.ID,
			Name:     row.Names,
			Image:    row.Image,
			Command:  unquoteCommand(row.Command),
			State:    row.State,
			Status:   row.Status,
			Created:  row.Created,
			Size:     row.Size,
			Ports:    ParsePorts(row.Ports),
			ExitCode: parseExitCode(row.Status),
		}
		if row.Networks != "" {
			c.Networks = strings.Split(row.Networks, ",")
		}
		out = append(out, c)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("adapter: read docker ps output: %w", err)
	}

	// Running first, then by name: the containers that can be interacted with
	// belong at the top.
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := out[i].Running(), out[j].Running()
		if ri != rj {
			return ri
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// unquoteCommand strips the wrapping quotes docker adds to the Command column.
//
// Docker renders it as `"sh -c 'while true; …"` — quoted, and truncated with a
// real ellipsis character when long. `--no-trunc` removes the truncation but
// not the quoting.
func unquoteCommand(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	return s
}

// ParsePorts turns docker's human port column into structured mappings.
//
// The column looks like:
//
//	0.0.0.0:18080->80/tcp, [::]:18080->80/tcp, 9090/tcp
//
// IPv4 and IPv6 entries for the same mapping are separate items and would
// otherwise show as duplicates, so a v6 entry is dropped when the identical
// mapping already exists on v4 — the user published one port, not two.
func ParsePorts(s string) []Port {
	ports := []Port{} // never nil; see the note in ParseContainers
	s = strings.TrimSpace(s)
	if s == "" {
		return ports
	}
	seen := make(map[string]bool)

	for _, chunk := range strings.Split(s, ",") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}

		var p Port
		mapping := chunk
		if host, container, ok := strings.Cut(chunk, "->"); ok {
			mapping = container
			// The host side is ip:port, and the ip may be a bracketed IPv6
			// literal, so split on the *last* colon.
			if i := strings.LastIndex(host, ":"); i >= 0 {
				p.HostIP = strings.TrimSpace(host[:i])
				p.HostPort = strings.TrimSpace(host[i+1:])
			} else {
				p.HostPort = strings.TrimSpace(host)
			}
		}

		port, proto, ok := strings.Cut(strings.TrimSpace(mapping), "/")
		p.Container = port
		if ok {
			p.Protocol = proto
		} else {
			p.Protocol = "tcp"
		}

		// Deduplicate across address families.
		key := p.HostPort + "/" + p.Container + "/" + p.Protocol
		if seen[key] {
			continue
		}
		seen[key] = true
		ports = append(ports, p)
	}
	return ports
}

// parseExitCode digs the exit status out of "Exited (3) 5 minutes ago".
// Returns -1 when the container is not in an exited state.
func parseExitCode(status string) int {
	open := strings.Index(status, "(")
	closeIdx := strings.Index(status, ")")
	if !strings.HasPrefix(status, "Exited") || open < 0 || closeIdx < open {
		return -1
	}
	var code int
	if _, err := fmt.Sscanf(status[open+1:closeIdx], "%d", &code); err != nil {
		return -1
	}
	return code
}

// Images and volumes (v1.x). Same CLI shape as the container listing, so the
// same `{{json .}}` template and the same version reasoning apply.

// Image is one container image.
type Image struct {
	ID         string `json:"id"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Size       string `json:"size"`      // human, as docker prints it
	SizeBytes  int64  `json:"sizeBytes"` // parsed, for sorting
	Created    string `json:"created"`
	// Containers is how many containers use this image; -1 when the daemon did
	// not compute it. Zero is what makes an image safe to reclaim.
	Containers int `json:"containers"`
	// Dangling marks an untagged layer left behind by a rebuild — the usual
	// answer to "where did my disk go".
	Dangling bool `json:"dangling"`
}

// Volume is one named volume.
type Volume struct {
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	Mountpoint string `json:"mountpoint"`
	Scope      string `json:"scope"`
	// InUse is false for a volume no container references. Deleting one that is
	// in use is refused by the daemon, so this drives what the UI offers.
	InUse bool `json:"inUse"`
}

// ImagesArgs and VolumesArgs return the argv for the respective listings.
func ImagesArgs() []string  { return []string{"images", "--no-trunc", "--format", "{{json .}}"} }
func VolumesArgs() []string { return []string{"volume", "ls", "--format", "{{json .}}"} }

// DanglingVolumesArgs lists the names of volumes no container references.
// A separate call because `docker volume ls` has no in-use column.
func DanglingVolumesArgs() []string {
	return []string{"volume", "ls", "-q", "-f", "dangling=true"}
}

type imageRow struct {
	ID         string `json:"ID"`
	Repository string `json:"Repository"`
	Tag        string `json:"Tag"`
	Size       string `json:"Size"`
	CreatedAt  string `json:"CreatedAt"`
	Containers string `json:"Containers"`
}

type volumeRow struct {
	Name       string `json:"Name"`
	Driver     string `json:"Driver"`
	Mountpoint string `json:"Mountpoint"`
	Scope      string `json:"Scope"`
}

// ParseImages parses `docker images --format '{{json .}}'`.
func ParseImages(data []byte) ([]Image, error) {
	out := []Image{}

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r imageRow
		if err := json.Unmarshal([]byte(line), &r); err != nil || r.ID == "" {
			continue
		}
		img := Image{
			ID:         r.ID,
			Repository: r.Repository,
			Tag:        r.Tag,
			Size:       r.Size,
			SizeBytes:  parseDockerSize(r.Size),
			Created:    r.CreatedAt,
			Containers: -1,
			// docker renders both fields as "<none>" for an untagged layer.
			Dangling: r.Tag == "<none>" || r.Repository == "<none>",
		}
		if n, err := strconv.Atoi(strings.TrimSpace(r.Containers)); err == nil {
			img.Containers = n
		}
		out = append(out, img)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("adapter: read docker images output: %w", err)
	}

	// Largest first: the reason to open this list is almost always disk space.
	sort.SliceStable(out, func(i, j int) bool { return out[i].SizeBytes > out[j].SizeBytes })
	return out, nil
}

// ParseVolumes parses `docker volume ls`, marking the ones nothing references.
func ParseVolumes(data, danglingNames []byte) ([]Volume, error) {
	dangling := map[string]bool{}
	for _, n := range strings.Fields(string(danglingNames)) {
		dangling[n] = true
	}

	out := []Volume{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r volumeRow
		if err := json.Unmarshal([]byte(line), &r); err != nil || r.Name == "" {
			continue
		}
		out = append(out, Volume{
			Name:       r.Name,
			Driver:     r.Driver,
			Mountpoint: r.Mountpoint,
			Scope:      r.Scope,
			InUse:      !dangling[r.Name],
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("adapter: read docker volume output: %w", err)
	}

	// Unused first: those are the ones the user can act on.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].InUse != out[j].InUse {
			return !out[i].InUse
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// parseDockerSize turns "4.17MB" into bytes for sorting.
//
// docker uses decimal units (MB = 10^6), not binary, so the multipliers here
// deliberately do not match the KiB/MiB the file explorer uses.
func parseDockerSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" {
		return 0
	}
	units := []struct {
		suffix string
		mult   float64
	}{
		{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"KB", 1e3}, {"B", 1},
	}
	for _, u := range units {
		if !strings.HasSuffix(s, u.suffix) {
			continue
		}
		num := strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
		v, err := strconv.ParseFloat(num, 64)
		if err != nil {
			return 0
		}
		return int64(v * u.mult)
	}
	return 0
}
