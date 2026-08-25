package main

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestManifestMatchesRuntimeAndHasNoAppDependencies(t *testing.T) {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if m.Name != "currencies" || m.Version != "0.1.0" {
		t.Fatalf("unexpected identity %s %s", m.Name, m.Version)
	}
	if len(m.Requires.Apps) != 0 {
		t.Fatalf("currencies must have no app dependencies: %+v", m.Requires.Apps)
	}
	if len(m.Requires.Integrations) != 2 {
		t.Fatalf("expected primary and fallback FX roles, got %d", len(m.Requires.Integrations))
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
	if len((&App{}).Workers()) != 1 || (&App{}).Workers()[0].Schedule != "@every 15m" {
		t.Fatal("currencies refresh worker is missing or has the wrong schedule")
	}
}
