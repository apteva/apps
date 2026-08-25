package main

import (
	"os"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestManifestMatchesRuntimeAndHasNoAppDependencies(t *testing.T) {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if m.Name != "currencies" || m.Version != "0.2.0" {
		t.Fatalf("unexpected identity %s %s", m.Name, m.Version)
	}
	if len(m.Requires.Apps) != 0 {
		t.Fatalf("currencies must have no app dependencies: %+v", m.Requires.Apps)
	}
	hasEgress := false
	for _, permission := range m.Requires.Permissions {
		if permission == "net.egress" {
			hasEgress = true
			break
		}
	}
	if !hasEgress {
		t.Fatal("ECB bootstrap requires the net.egress permission")
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

func TestProjectPanelUsesFullAvailableWidth(t *testing.T) {
	source, err := os.ReadFile("ui/CurrenciesPanel.tsx")
	if err != nil {
		t.Fatal(err)
	}
	panel := string(source)
	if strings.Contains(panel, "max-w-6xl") || strings.Contains(panel, "mx-auto space-y-5") {
		t.Fatal("project panel must not impose a centered maximum-width container")
	}
	if !strings.Contains(panel, `className="w-full space-y-5"`) {
		t.Fatal("project panel must explicitly consume the app shell's full width")
	}
}
