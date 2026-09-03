package main

import (
	"os"
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
	if got, want := len(m.Provides.MCPTools), 23; got != want {
		t.Fatalf("tool count = %d, want %d", got, want)
	}
	if len(m.Provides.UIPanels) != 1 {
		t.Fatalf("ui panel missing")
	}
}

func TestSourceAndEmbeddedManifestsStayInSync(t *testing.T) {
	source, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	external, err := sdk.ParseManifest(source)
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	if external.Name != embedded.Name || external.Version != embedded.Version || external.Icon != embedded.Icon {
		t.Fatalf("manifest identity differs: source=%s@%s embedded=%s@%s", external.Name, external.Version, embedded.Name, embedded.Version)
	}
	if len(external.Provides.MCPTools) != len(embedded.Provides.MCPTools) {
		t.Fatalf("tool count differs: source=%d embedded=%d", len(external.Provides.MCPTools), len(embedded.Provides.MCPTools))
	}
	for index := range external.Provides.MCPTools {
		if external.Provides.MCPTools[index].Name != embedded.Provides.MCPTools[index].Name {
			t.Fatalf("tool %d differs: source=%s embedded=%s", index, external.Provides.MCPTools[index].Name, embedded.Provides.MCPTools[index].Name)
		}
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
