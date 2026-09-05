package main

import (
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "storage" {
		t.Errorf("name=%q", m.Name)
	}
	if m.Version == "" {
		t.Error("version empty")
	}
	if m.Version != "0.11.1" {
		t.Errorf("version=%q", m.Version)
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Error("db.migrations missing")
	}
	if len(m.Provides.MCPTools) != len((&App{}).MCPTools()) {
		t.Errorf("expected %d MCP tools, got %d", len((&App{}).MCPTools()), len(m.Provides.MCPTools))
	}
	if len(m.Provides.UISurfaces) != 1 || m.Provides.UISurfaces[0].ID != "files" {
		t.Fatalf("expected files native surface, got %#v", m.Provides.UISurfaces)
	}
	publicDownload := false
	for _, route := range m.Provides.HTTPRoutes {
		if route.Prefix == "/public/files/" && route.NoAuth {
			publicDownload = true
		}
	}
	if !publicDownload {
		t.Fatalf("public download route must be declared no-auth: %#v", m.Provides.HTTPRoutes)
	}
}

func TestNativeFilesSurface_ValidAndMatchesManifest(t *testing.T) {
	data, err := os.ReadFile("ui/surfaces/files.json")
	if err != nil {
		t.Fatal(err)
	}
	surface, err := sdk.ParseNativeSurface(data)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := (&App{}).Manifest().Provides.UISurfaces[0]
	if err := sdk.ValidateNativeSurfaceForDescriptor(surface, descriptor); err != nil {
		t.Fatal(err)
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
