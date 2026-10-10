package main

import (
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// TestEmbeddedManifest_Valid sanity-checks the YAML the binary
// embeds — same shape we ship in apteva.yaml. If they drift, this
// breaks before anyone tries to install.
func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	external, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if external.Version != m.Version || external.Runtime.Source.Ref != m.Runtime.Source.Ref {
		t.Fatalf("runtime/installed version drift: embedded=%s external=%s", m.Version, external.Version)
	}
	if m.Name != "media" {
		t.Errorf("name=%q", m.Name)
	}
	if m.Version == "" {
		t.Error("version empty")
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Error("db.migrations missing")
	}
	if m.Runtime.Source == nil || m.Runtime.Source.Ref != "media/v"+m.Version {
		t.Errorf("runtime source must pin its immutable release tag, got %#v", m.Runtime.Source)
	}
	// Includes compact crop preview and explicit bounded evidence retrieval.
	if len(m.Provides.MCPTools) != 36 {
		t.Errorf("expected 36 MCP tools, got %d", len(m.Provides.MCPTools))
	}
	if len(m.Provides.Workers) != 1 {
		t.Errorf("expected 1 worker, got %d", len(m.Provides.Workers))
	}
	if m.Provides.Workers[0].Schedule == "" {
		t.Error("indexer worker missing schedule")
	}
	// Storage is required; jobs is an optional companion for scheduled renders.
	gotApps := map[string]sdk.RequiredAppRef{}
	for _, a := range m.Requires.Apps {
		gotApps[a.Name] = a
	}
	if storage, ok := gotApps["storage"]; !ok {
		t.Errorf("expected requires.apps to include storage, got %#v", m.Requires.Apps)
	} else if storage.Version != ">=0.10.26" {
		t.Errorf("storage version=%q, want >=0.10.26", storage.Version)
	}
	if jobs, ok := gotApps["jobs"]; !ok || !jobs.Optional {
		t.Errorf("expected requires.apps to include optional jobs, got %#v", m.Requires.Apps)
	}
}

// TestMCPTools_DeclaredMatchHandlers — manifest names and handler
// names must agree, otherwise the platform exposes a tool with no
// implementation and the SDK panics on dispatch.
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

func TestMediaGetSchema_ExposesDeliveryChoices(t *testing.T) {
	var get *sdk.Tool
	tools := (&App{}).MCPTools()
	for i := range tools {
		if tools[i].Name == "media_get" {
			get = &tools[i]
			break
		}
	}
	if get == nil {
		t.Fatal("media_get tool missing")
	}
	properties := get.InputSchema["properties"].(map[string]any)
	delivery := properties["delivery"].(map[string]any)
	disposition := properties["disposition"].(map[string]any)
	if delivery["default"] != "apteva" {
		t.Fatalf("delivery schema = %#v", delivery)
	}
	deliveryEnum, ok := delivery["enum"].([]string)
	if !ok || len(deliveryEnum) != 3 || deliveryEnum[0] != "apteva" || deliveryEnum[1] != "proxy" || deliveryEnum[2] != "direct" {
		t.Fatalf("delivery enum = %#v", delivery["enum"])
	}
	if disposition["default"] != "inline" {
		t.Fatalf("disposition schema = %#v", disposition)
	}
}
