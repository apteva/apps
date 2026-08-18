package main

import (
	"os"
	"strings"
	"testing"
)

// TestManifestMatchesAptevaYAML — the release mechanics require the
// version (and everything else) in apteva.yaml and main.go's embedded
// manifestYAML to move together; the source-installer reads the file,
// the running sidecar serves the constant.
func TestManifestMatchesAptevaYAML(t *testing.T) {
	onDisk, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(onDisk)) != strings.TrimSpace(manifestYAML) {
		t.Fatal("apteva.yaml and main.go manifestYAML have diverged — keep them byte-identical")
	}
}

// TestHTTPRoutesAvoidReservedPrefixes — the SDK owns /health /manifest
// /mcp /events /ui/ on every sidecar; an app route on one of those
// panics the sidecar at boot ("Waiting for health check…").
func TestHTTPRoutesAvoidReservedPrefixes(t *testing.T) {
	reserved := []string{"/health", "/manifest", "/mcp", "/events", "/ui/"}
	app := &App{}
	for _, route := range app.HTTPRoutes() {
		for _, prefix := range reserved {
			if route.Pattern == prefix || strings.HasPrefix(route.Pattern, strings.TrimSuffix(prefix, "/")+"/") {
				t.Errorf("route %q collides with reserved prefix %q", route.Pattern, prefix)
			}
		}
	}
}

// TestManifestToolsMatchCode — every tool the manifest advertises
// exists in MCPTools() and vice versa, so the dashboard's tool list
// never drifts from what the sidecar actually serves.
func TestManifestToolsMatchCode(t *testing.T) {
	app := &App{}
	manifest := app.Manifest()

	declared := map[string]bool{}
	for _, tool := range manifest.Provides.MCPTools {
		declared[tool.Name] = true
	}
	implemented := map[string]bool{}
	for _, tool := range app.MCPTools() {
		implemented[tool.Name] = true
	}
	for name := range declared {
		if !implemented[name] {
			t.Errorf("manifest declares %q but MCPTools() does not implement it", name)
		}
	}
	for name := range implemented {
		if !declared[name] {
			t.Errorf("MCPTools() implements %q but the manifest does not declare it", name)
		}
	}
}
