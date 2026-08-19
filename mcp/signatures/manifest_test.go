package main

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestManifestAndRuntimeToolsMatch(t *testing.T) {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if m.Name != "signatures" || m.Version != "0.2.2" {
		t.Fatalf("unexpected identity: %s %s", m.Name, m.Version)
	}
	if len(m.Scopes) != 1 || m.Scopes[0] != "project" {
		t.Fatalf("signatures must be project-only: %v", m.Scopes)
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Fatal("db migrations are required")
	}
	declared := map[string]bool{}
	for _, tool := range m.Provides.MCPTools {
		declared[tool.Name] = true
	}
	registered := map[string]bool{}
	for _, tool := range (&App{}).MCPTools() {
		registered[tool.Name] = true
		if !declared[tool.Name] {
			t.Errorf("runtime tool %q is not declared", tool.Name)
		}
	}
	for name := range declared {
		if !registered[name] {
			t.Errorf("declared tool %q has no runtime handler", name)
		}
	}
}

func TestOnlyStorageIsRequired(t *testing.T) {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	foundStorage := false
	foundMessaging := false
	for _, dep := range m.Requires.Apps {
		switch dep.Name {
		case "storage":
			foundStorage = true
			if dep.Optional {
				t.Error("storage must be required")
			}
		case "messaging":
			foundMessaging = true
			if !dep.Optional {
				t.Error("messaging must be optional")
			}
		default:
			t.Errorf("unexpected app dependency %q", dep.Name)
		}
	}
	if !foundStorage || !foundMessaging {
		t.Fatalf("dependencies missing: storage=%v messaging=%v", foundStorage, foundMessaging)
	}
}

func TestPublicSigningRouteIsNoAuth(t *testing.T) {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, route := range m.Provides.HTTPRoutes {
		if route.Prefix == "/sign/" {
			found = true
			if !route.NoAuth {
				t.Error("signing route must be public and token-authorized")
			}
		}
	}
	if !found {
		t.Fatal("/sign/ route missing")
	}
}
