package main

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestEmbeddedManifestValid(t *testing.T) {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if m.Name != "books" {
		t.Fatalf("name = %q, want books", m.Name)
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Fatal("db migrations missing")
	}
	if got, want := len(m.Provides.MCPTools), 18; got != want {
		t.Fatalf("tool count = %d, want %d", got, want)
	}
	if len(m.Provides.UIPanels) != 1 {
		t.Fatalf("ui panel missing")
	}
}

func TestMCPToolsDeclaredMatchHandlers(t *testing.T) {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, tool := range m.Provides.MCPTools {
		declared[tool.Name] = true
	}
	registered := map[string]bool{}
	for _, tool := range (&App{}).MCPTools() {
		registered[tool.Name] = true
		if !declared[tool.Name] {
			t.Fatalf("handler registers undeclared tool %q", tool.Name)
		}
	}
	for name := range declared {
		if !registered[name] {
			t.Fatalf("manifest declares %q without handler", name)
		}
	}
}
