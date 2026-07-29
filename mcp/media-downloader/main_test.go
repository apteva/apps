package main

import "testing"

func TestManifestVersionAndEventContract(t *testing.T) {
	manifest := (&App{}).Manifest()
	if manifest.Version != "0.2.13" {
		t.Fatalf("manifest version = %q, want 0.2.13", manifest.Version)
	}
}

func TestSearchToolAndRouteAreRegistered(t *testing.T) {
	app := &App{}
	foundTool := false
	for _, tool := range app.MCPTools() {
		if tool.Name == "youtube_search" {
			foundTool = true
			break
		}
	}
	if !foundTool {
		t.Fatal("youtube_search MCP tool is not registered")
	}
	foundRoute := false
	for _, route := range app.HTTPRoutes() {
		if route.Pattern == "/search" {
			foundRoute = true
			break
		}
	}
	if !foundRoute {
		t.Fatal("/search HTTP route is not registered")
	}
}
