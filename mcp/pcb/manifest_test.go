package main

import (
	"os"
	"testing"
)

func TestManifestMatchesRuntimeSurface(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "pcb" || m.Version != "0.3.0" {
		t.Fatalf("unexpected manifest identity: %s %s", m.Name, m.Version)
	}
	declared := map[string]bool{}
	for _, tool := range m.Provides.MCPTools {
		declared[tool.Name] = true
	}
	runtime := app.MCPTools()
	if len(runtime) != len(declared) {
		t.Fatalf("manifest declares %d tools, runtime exposes %d", len(declared), len(runtime))
	}
	for _, tool := range runtime {
		if !declared[tool.Name] {
			t.Errorf("runtime tool %s missing in manifest", tool.Name)
		}
		if tool.Handler == nil && tool.HandlerCtx == nil {
			t.Errorf("tool %s has no handler", tool.Name)
		}
	}
	for _, path := range []string{"ui/PCBPanel.mjs", "ui/PCBDesignCard.mjs", "ui/icon.svg", "skills/how-to-use-pcb.md", "PROPOSAL.md"} {
		info, err := os.Stat(path)
		if err != nil || info.Size() == 0 {
			t.Errorf("required asset %s missing or empty", path)
		}
	}
}

func TestManifestUsesNativeDependencyBindings(t *testing.T) {
	m := (&App{}).Manifest()
	if len(m.Requires.Apps) != 0 {
		t.Fatalf("legacy requires.apps dependencies remain: %#v", m.Requires.Apps)
	}
	want := map[string]struct {
		kind     string
		required bool
	}{
		"storage":           {kind: "app", required: true},
		"firmware_executor": {kind: "app", required: false},
		"component_data":    {kind: "integration", required: false},
		"pcb_fabricator":    {kind: "integration", required: false},
	}
	if len(m.Requires.Integrations) != len(want) {
		t.Fatalf("got %d native binding roles, want %d", len(m.Requires.Integrations), len(want))
	}
	for _, dep := range m.Requires.Integrations {
		expect, ok := want[dep.Role]
		if !ok {
			t.Errorf("unexpected native binding role %q", dep.Role)
			continue
		}
		if dep.Kind != expect.kind || dep.Required != expect.required {
			t.Errorf("role %s = kind %q required %v, want %q/%v", dep.Role, dep.Kind, dep.Required, expect.kind, expect.required)
		}
		if dep.Role == "storage" && (len(dep.CompatibleAppNames) != 1 || dep.CompatibleAppNames[0] != "storage") {
			t.Errorf("storage role compatibility = %#v", dep.CompatibleAppNames)
		}
	}
}
