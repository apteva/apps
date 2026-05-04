package main

// manifest_test — assertions about what the manifest declares. Catches
// drift between apteva.yaml on disk and the embedded copy in main.go,
// and between the manifest's mcp_tools list and the App.MCPTools()
// surface.

import (
	"os"
	"sort"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestManifest_ParsesAndValidates(t *testing.T) {
	body, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	m, err := sdk.ParseManifest(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Name != "auth" {
		t.Errorf("name = %q, want auth", m.Name)
	}
	if m.Version == "" {
		t.Error("version empty")
	}
	if m.DB == nil || m.DB.Driver != "sqlite" {
		t.Errorf("db block missing or wrong driver: %+v", m.DB)
	}
}

func TestManifest_OnDiskMatchesEmbedded(t *testing.T) {
	disk, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	mDisk, err := sdk.ParseManifest(disk)
	if err != nil {
		t.Fatalf("parse disk manifest: %v", err)
	}
	mEmb, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse embedded manifest: %v", err)
	}
	if mDisk.Name != mEmb.Name {
		t.Errorf("name drift: disk=%q embedded=%q", mDisk.Name, mEmb.Name)
	}
	if mDisk.Version != mEmb.Version {
		t.Errorf("version drift: disk=%q embedded=%q", mDisk.Version, mEmb.Version)
	}
	// The disk manifest is the contract, so we check the tool list matches.
	dt := toolNames(mDisk.Provides.MCPTools)
	et := toolNames(mEmb.Provides.MCPTools)
	if !equalStrSlices(dt, et) {
		t.Errorf("tool list drift\ndisk: %v\nembd: %v", dt, et)
	}
}

func TestMCPTools_MatchManifest(t *testing.T) {
	body, _ := os.ReadFile("apteva.yaml")
	m, _ := sdk.ParseManifest(body)
	declared := toolNames(m.Provides.MCPTools)
	implemented := []string{}
	for _, tool := range (&App{}).MCPTools() {
		implemented = append(implemented, tool.Name)
	}
	sort.Strings(declared)
	sort.Strings(implemented)
	if !equalStrSlices(declared, implemented) {
		t.Errorf("declared vs implemented tool drift\ndeclared: %v\nimpl: %v",
			declared, implemented)
	}
}

func TestPermissions_AllInTaxonomy(t *testing.T) {
	body, _ := os.ReadFile("apteva.yaml")
	m, _ := sdk.ParseManifest(body)
	if len(m.Requires.Permissions) == 0 {
		t.Fatal("manifest declares no permissions")
	}
	// ParseManifest already validates permissions against the taxonomy;
	// a previous mistake would have been caught above. This test just
	// guards against future hand-edits that bypass ParseManifest.
	for _, p := range m.Requires.Permissions {
		if !strings.Contains(string(p), ".") {
			t.Errorf("permission %q doesn't look like a namespaced taxonomy entry", p)
		}
	}
}

// helpers

func toolNames(tools []sdk.MCPToolSpec) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	sort.Strings(out)
	return out
}

func equalStrSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
