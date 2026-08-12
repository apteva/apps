package main

import (
	"os"
	"strings"
	"testing"
)

func TestManifestMatchesRuntimeSurface(t *testing.T) {
	app := &App{}
	manifest := app.Manifest()
	if manifest.Name != "design" || manifest.Version != "0.1.1" {
		t.Fatalf("unexpected manifest identity: %s %s", manifest.Name, manifest.Version)
	}
	declared := map[string]bool{}
	for _, tool := range manifest.Provides.MCPTools {
		declared[tool.Name] = true
	}
	runtime := app.MCPTools()
	if len(runtime) != len(declared) {
		t.Fatalf("manifest declares %d tools, runtime exposes %d", len(declared), len(runtime))
	}
	for _, tool := range runtime {
		if !declared[tool.Name] {
			t.Errorf("runtime tool %s missing from manifest", tool.Name)
		}
		if tool.Handler == nil && tool.HandlerCtx == nil {
			t.Errorf("tool %s has no handler", tool.Name)
		}
	}
	for _, path := range []string{"ui/DesignPanel.mjs", "ui/DesignCard.mjs", "ui/icon.svg", "skills/how-to-use-design.md", "runner/dist/runner.mjs", "runner/dist/replicad_single.wasm"} {
		if info, err := os.Stat(path); err != nil || info.Size() == 0 {
			t.Errorf("required asset %s missing or empty", path)
		}
	}
}

func TestDesignPanelOwnsLayoutAndUsesSolidDepthRendering(t *testing.T) {
	source, err := os.ReadFile("ui/DesignPanel.tsx")
	if err != nil {
		t.Fatal(err)
	}
	panel := string(source)
	for _, required := range []string{
		".design-workspace{display:grid;grid-template-columns:220px minmax(360px,1fr) 320px",
		"gl.enable(gl.DEPTH_TEST)",
		"gl.disable(gl.BLEND)",
	} {
		if !strings.Contains(panel, required) {
			t.Errorf("DesignPanel.tsx is missing rendering invariant %q", required)
		}
	}
}
