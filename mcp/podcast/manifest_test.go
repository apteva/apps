package main

// Manifest sanity — the embedded manifest must parse, and its declared
// MCP tool surface must match what MCPTools() actually wires up. A
// mismatch means the platform advertises a tool with no handler (or an
// app handler the platform never exposes).

import (
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestEmbeddedManifest_Valid(t *testing.T) {
	m := (&App{}).Manifest()
	if m.Name != "podcast" {
		t.Errorf("manifest name = %q, want podcast", m.Name)
	}
	if len(m.Provides.MCPTools) == 0 {
		t.Fatal("embedded manifest declares no mcp_tools")
	}

	// The on-disk apteva.yaml — the installer's source of truth — must
	// also parse and agree on the app name.
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	disk, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatalf("apteva.yaml does not parse: %v", err)
	}
	if disk.Name != m.Name {
		t.Errorf("apteva.yaml name %q != embedded name %q", disk.Name, m.Name)
	}
}

func TestMCPTools_ManifestMatchesHandlers(t *testing.T) {
	app := &App{}

	declared := map[string]bool{}
	for _, tspec := range app.Manifest().Provides.MCPTools {
		declared[tspec.Name] = true
	}

	wired := map[string]bool{}
	for _, tool := range app.MCPTools() {
		if tool.Handler == nil && tool.HandlerCtx == nil {
			t.Errorf("tool %q has no handler", tool.Name)
		}
		if !declared[tool.Name] {
			t.Errorf("tool %q is wired but not declared in the manifest", tool.Name)
		}
		wired[tool.Name] = true
	}
	for name := range declared {
		if !wired[name] {
			t.Errorf("tool %q is declared in the manifest but has no handler", name)
		}
	}
}
