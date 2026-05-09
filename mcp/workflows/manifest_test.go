package main

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestManifestParses(t *testing.T) {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m.Name != "workflows" {
		t.Errorf("Name = %q, want workflows", m.Name)
	}
	if m.Version == "" {
		t.Error("Version empty")
	}
	if len(m.Provides.MCPTools) == 0 {
		t.Error("expected MCP tools in manifest")
	}
}

func TestAppManifestRoundtrips(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "workflows" {
		t.Errorf("Name = %q, want workflows", m.Name)
	}
}

func TestMCPToolsHaveSchemas(t *testing.T) {
	app := &App{}
	tools := app.MCPTools()
	if len(tools) == 0 {
		t.Fatal("no MCP tools declared")
	}
	for _, tool := range tools {
		if tool.Name == "" {
			t.Error("tool with empty name")
		}
		if tool.Description == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no InputSchema", tool.Name)
		}
		if tool.Handler == nil && tool.HandlerCtx == nil {
			t.Errorf("tool %q has no handler", tool.Name)
		}
	}
}
