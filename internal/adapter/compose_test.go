package adapter

import (
	"encoding/json"
	"strings"
	"testing"
)

// The Labels strings below are copied verbatim out of `docker ps --no-trunc
// --format '{{json .}}'` against Docker 29.4.0 / Compose v5.1.2. Handwritten
// fixtures would have missed the thing this file exists for: the column
// separates labels with commas and one of the values contains commas too.

const twoFileLabels = `com.docker.compose.config-hash=8b08690bc131b4d37c0251e3e91fc995de2ed0f3ed390a30b80c9c4227162a34,com.docker.compose.container-number=1,com.docker.compose.depends_on=,com.docker.compose.image=sha256:ab3fe4defd29ba6231229a4d41440ac8bde8218e85870e53876277faa24b35c4,com.docker.compose.oneoff=False,com.docker.compose.project.config_files=/tmp/composetest/compose.yaml,/tmp/composetest/extra.yaml,com.docker.compose.project.working_dir=/tmp/composetest,com.docker.compose.project=litedeck-probe2,com.docker.compose.service=cache,com.docker.compose.version=5.1.2`

func TestParseLabelsRejoinsValuesHoldingCommas(t *testing.T) {
	got := parseLabels(twoFileLabels)

	want := "/tmp/composetest/compose.yaml,/tmp/composetest/extra.yaml"
	if got["com.docker.compose.project.config_files"] != want {
		t.Errorf("config_files = %q, want %q",
			got["com.docker.compose.project.config_files"], want)
	}
	// The second path must not have become a label of its own, which is what a
	// plain split on "," produces.
	if _, ok := got["/tmp/composetest/extra.yaml"]; ok {
		t.Error("the continuation fragment was read as a label")
	}
	// The label after the multi-valued one still has to arrive intact.
	if got["com.docker.compose.project.working_dir"] != "/tmp/composetest" {
		t.Errorf("working_dir = %q, want /tmp/composetest",
			got["com.docker.compose.project.working_dir"])
	}
	if got["com.docker.compose.project"] != "litedeck-probe2" {
		t.Errorf("project = %q, want litedeck-probe2", got["com.docker.compose.project"])
	}
}

func TestComposeOf(t *testing.T) {
	tests := []struct {
		name   string
		labels string
		want   *Compose
	}{
		{
			name:   "a compose container",
			labels: twoFileLabels,
			want:   &Compose{Project: "litedeck-probe2", Service: "cache"},
		},
		{
			// `docker run` with no labels at all, and the common case of a
			// container carrying labels that are nothing to do with Compose.
			name:   "no labels",
			labels: "",
			want:   nil,
		},
		{
			name:   "unrelated labels",
			labels: "maintainer=NGINX Docker Maintainers <docker-maint@nginx.com>",
			want:   nil,
		},
		{
			// A project label with no service is not something Compose writes;
			// treating it as a project would offer an action that cannot name
			// its target.
			name:   "project without service",
			labels: "com.docker.compose.project=x",
			want:   nil,
		},
		{
			name:   "one-off from compose run",
			labels: "com.docker.compose.project=x,com.docker.compose.service=web,com.docker.compose.oneoff=True",
			want:   &Compose{Project: "x", Service: "web", OneOff: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := composeOf(tt.labels)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("composeOf = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("composeOf = nil, want a project")
			}
			if *got != *tt.want {
				t.Errorf("composeOf = %+v, want %+v", *got, *tt.want)
			}
		})
	}
}

// A container that is not part of a project must marshal without a compose key
// at all: the frontend decides whether to offer the project actions by asking
// whether the field is there.
func TestNonComposeContainerCarriesNoComposeField(t *testing.T) {
	row := `{"ID":"abc","Names":"solo","Image":"nginx","State":"running","Status":"Up 2 hours","Labels":"maintainer=someone"}`
	got, err := ParseContainers([]byte(row))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d containers, want 1", len(got))
	}
	if got[0].Compose != nil {
		t.Errorf("Compose = %+v, want nil", got[0].Compose)
	}
	out, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "compose") {
		t.Errorf("marshalled as %s, want no compose key", out)
	}
}

func TestParseContainersReadsComposeLabels(t *testing.T) {
	row := `{"ID":"abc","Names":"litedeck-probe2-cache-1","Image":"alpine:3.20","State":"running","Status":"Up 4 seconds","Labels":"` + twoFileLabels + `"}`
	got, err := ParseContainers([]byte(row))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Compose == nil {
		t.Fatalf("got %+v, want one container in a project", got)
	}
	if got[0].Compose.Project != "litedeck-probe2" || got[0].Compose.Service != "cache" {
		t.Errorf("Compose = %+v", *got[0].Compose)
	}
	if got[0].Compose.OneOff {
		t.Error("OneOff = true, want false — the label says False")
	}
}

func TestComposeArgs(t *testing.T) {
	tests := []struct {
		name             string
		project, service string
		action           string
		want             []string
	}{
		{
			name:    "whole project",
			project: "shop",
			action:  "restart",
			want:    []string{"compose", "--project-name", "shop", "restart"},
		},
		{
			name:    "one service",
			project: "shop",
			service: "web",
			action:  "restart",
			want:    []string{"compose", "--project-name", "shop", "restart", "--", "web"},
		},
		{
			// The separator is the point: without it this reads as a flag.
			name:    "a service named like a flag",
			project: "shop",
			service: "-f",
			action:  "stop",
			want:    []string{"compose", "--project-name", "shop", "stop", "--", "-f"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComposeArgs(tt.project, tt.service, tt.action)
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Errorf("ComposeArgs = %v, want %v", got, tt.want)
			}
		})
	}
}
