package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestVersionMatchesWailsConfig guards a drift that is invisible until a user
// reports it.
//
// The version the app shows comes from Version; the version baked into the
// macOS bundle (CFBundleShortVersionString) and the Windows version resource
// comes from wails.json. Nothing connects them, so bumping one and forgetting
// the other ships a build that reports two different versions of itself — and
// the wrong one is the one in the bug report.
//
// This was not hypothetical: before wails.json had an info block at all, Wails
// filled in its default and every build claimed to be 1.0.0.
func TestVersionMatchesWailsConfig(t *testing.T) {
	// internal/app -> repo root. Resolved rather than hardcoded so the test
	// fails loudly if the layout moves instead of silently passing on a
	// missing file.
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "wails.json"))
	if err != nil {
		t.Fatalf("read wails.json: %v", err)
	}

	var cfg struct {
		Info struct {
			ProductName    string `json:"productName"`
			ProductVersion string `json:"productVersion"`
		} `json:"info"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse wails.json: %v", err)
	}

	// An absent info block unmarshals to "" without error, which is exactly the
	// state that let 1.0.0 through — so check for empty explicitly.
	if cfg.Info.ProductVersion == "" {
		t.Fatal("wails.json has no info.productVersion; Wails will substitute its own default (1.0.0)")
	}
	if cfg.Info.ProductVersion != Version {
		t.Errorf("version drift: app.Version = %q, wails.json info.productVersion = %q",
			Version, cfg.Info.ProductVersion)
	}
	if cfg.Info.ProductName == "" {
		t.Error("wails.json has no info.productName; the bundle name falls back to the lowercase project name")
	}
}
