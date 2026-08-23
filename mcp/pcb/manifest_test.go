package main

import (
	"os"
	"testing"
)

func TestManifestMatchesRuntimeSurface(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "pcb" || m.Version != "0.1.0" {
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
