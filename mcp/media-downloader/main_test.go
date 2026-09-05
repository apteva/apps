package main

import "testing"

func TestManifestVersionAndEventContract(t *testing.T) {
	manifest := (&App{}).Manifest()
	if manifest.Version != "0.3.0" {
		t.Fatalf("manifest version = %q, want 0.3.0", manifest.Version)
	}
}

func TestIngestToolIsRegistered(t *testing.T) {
	foundRoute := false
	for _, route := range (&App{}).HTTPRoutes() {
		if route.Pattern == "/ingest" {
			foundRoute = true
		}
	}
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "ingest_media" {
			if !foundRoute {
				t.Fatal("/ingest HTTP route is not registered")
			}
			return
		}
	}
	t.Fatal("ingest_media MCP tool is not registered")
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

func TestSourceProfileToolSupportsInstagram(t *testing.T) {
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name != "source_profile_create" {
			continue
		}
		provider := tool.InputSchema["properties"].(map[string]any)["provider"].(map[string]any)
		for _, value := range provider["enum"].([]string) {
			if value == "instagram" {
				return
			}
		}
		t.Fatal("source_profile_create provider enum does not include instagram")
	}
	t.Fatal("source_profile_create tool is not registered")
}
