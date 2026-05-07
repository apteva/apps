package main

// Manifest sanity — embedded YAML parses, declared MCP tools match
// the runtime registrations, schema fields all present.

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestEmbeddedManifest_Valid(t *testing.T) {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.Name != "docs" {
		t.Errorf("name = %q, want docs", m.Name)
	}
	if m.Version == "" {
		t.Error("version missing")
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Error("db.migrations missing")
	}
	// 5 template tools + 2 render tools (render + preview) + 2 audit tools = 9.
	if got, want := len(m.Provides.MCPTools), 9; got != want {
		t.Errorf("expected %d MCP tools, got %d", want, got)
	}
	// Template resource declared and referenced by every permission.
	if len(m.Provides.Resources) != 1 {
		t.Errorf("expected 1 resource, got %d", len(m.Provides.Resources))
	}
	if len(m.Provides.ProvidedPermissions) != 3 {
		t.Errorf("expected 3 permissions, got %d", len(m.Provides.ProvidedPermissions))
	}
}

func TestMCPTools_DeclaredMatchHandlers(t *testing.T) {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	declared := make(map[string]bool, len(m.Provides.MCPTools))
	for _, ts := range m.Provides.MCPTools {
		declared[ts.Name] = true
	}
	app := &App{}
	registered := make(map[string]bool, 0)
	for _, tool := range app.MCPTools() {
		registered[tool.Name] = true
		if !declared[tool.Name] {
			t.Errorf("handler implements %q but manifest doesn't declare it", tool.Name)
		}
	}
	for name := range declared {
		if !registered[name] {
			t.Errorf("manifest declares %q but no handler is registered", name)
		}
	}
}

func TestStorageDependencyDeclared(t *testing.T) {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	var has bool
	for _, dep := range m.Requires.Apps {
		if dep.Name == "storage" {
			has = true
			if dep.Version == "" {
				t.Error("storage dep missing version constraint")
			}
		}
	}
	if !has {
		t.Error("docs must declare requires.apps[storage] — render uploads through it")
	}
}
