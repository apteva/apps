package main

import (
	"os"
	"reflect"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestEmbeddedManifestValid(t *testing.T) {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	if m.Name != "testimonials" {
		t.Fatalf("name = %q, want testimonials", m.Name)
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Fatal("db migrations missing")
	}
	if got, want := len(m.Provides.MCPTools), 6; got != want {
		t.Fatalf("tool count = %d, want %d", got, want)
	}
	if len(m.Provides.UIPanels) != 1 {
		t.Fatal("ui panel missing")
	}
}

func TestEmbeddedManifestMatchesReleaseManifest(t *testing.T) {
	externalBytes, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	external, err := sdk.ParseManifest(externalBytes)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	embedded, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("parse embedded manifest: %v", err)
	}
	if !reflect.DeepEqual(external, embedded) {
		t.Fatal("embedded manifest differs from apteva.yaml")
	}
	if external.Version != "0.1.1" {
		t.Fatalf("version = %q, want 0.1.1", external.Version)
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
