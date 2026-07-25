package main

import "testing"

func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "domains" {
		t.Errorf("name=%q", m.Name)
	}
	if m.Version != "0.5.1" {
		t.Errorf("version=%q, want 0.5.0", m.Version)
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Error("db.migrations missing")
	}
}

func TestMCPTools_DeclaredMatchHandlers(t *testing.T) {
	app := &App{}
	declared := map[string]bool{}
	for _, t := range app.Manifest().Provides.MCPTools {
		declared[t.Name] = true
	}
	implemented := map[string]bool{}
	for _, t := range app.MCPTools() {
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

func TestEmbeddedManifest_DNSProviderAllowsSpaceship(t *testing.T) {
	app := &App{}
	for _, dep := range app.Manifest().Requires.Integrations {
		if dep.Role != "dns_provider" {
			continue
		}
		if !containsString(dep.CompatibleSlugs, "spaceship") {
			t.Fatalf("dns_provider compatible_slugs=%v, want spaceship", dep.CompatibleSlugs)
		}
		return
	}
	t.Fatal("dns_provider integration role missing")
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
