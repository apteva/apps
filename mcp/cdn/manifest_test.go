package main

import "testing"

// Tier 1 — embedded manifest must parse and match the binary's tool
// surface. sdk.Run validates this at boot; testing it up front so
// edits to the manifest YAML literal don't make the binary fail to
// mount in prod.

func TestEmbeddedManifest_Valid(t *testing.T) {
	m := (&App{}).Manifest()
	if m.Name != "cdn" {
		t.Errorf("manifest.Name=%q, want cdn", m.Name)
	}
	if m.Version == "" {
		t.Error("manifest.Version is empty")
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Error("manifest.DB.Migrations missing")
	}
	scopes := map[string]bool{}
	for _, s := range m.Scopes {
		scopes[string(s)] = true
	}
	if !scopes["global"] {
		t.Error("cdn must declare scope 'global' (cdn is global-scoped like domains/certs/routes)")
	}
}

func TestMCPTools_DeclaredMatchHandlers(t *testing.T) {
	a := &App{}
	declared := map[string]bool{}
	for _, t := range a.Manifest().Provides.MCPTools {
		declared[t.Name] = true
	}
	implemented := map[string]bool{}
	for _, t := range a.MCPTools() {
		implemented[t.Name] = true
	}
	for name := range declared {
		if !implemented[name] {
			t.Errorf("manifest declares %q but no handler implements it", name)
		}
	}
	for name := range implemented {
		if !declared[name] {
			t.Errorf("handler implements %q but manifest doesn't declare it", name)
		}
	}
}
