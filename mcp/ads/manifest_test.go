package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// A release has to update both the Go tool registry and the manifest the
// dashboard reads. Nothing enforced that, so a new tool could ship invisible
// to the marketplace listing, or the manifest could advertise a tool the
// server does not serve.

var manifestToolPattern = regexp.MustCompile(`^\s*-\s*\{\s*name:\s*([a-z_]+)\s*,`)

func manifestToolNames(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	inBlock := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "mcp_tools:" {
			inBlock = true
			continue
		}
		if !inBlock {
			continue
		}
		// The block ends at the next sibling key, such as ui_panels:.
		if trimmed != "" && !strings.HasPrefix(trimmed, "-") {
			break
		}
		if match := manifestToolPattern.FindStringSubmatch(line); match != nil {
			names[match[1]] = true
		}
	}
	if len(names) == 0 {
		t.Fatal("no mcp_tools entries parsed from apteva.yaml")
	}
	return names
}

func TestManifestDeclaresEveryServedTool(t *testing.T) {
	declared := manifestToolNames(t)
	served := map[string]bool{}
	for _, tool := range (&App{}).MCPTools() {
		served[tool.Name] = true
		if !declared[tool.Name] {
			t.Errorf("tool %q is served but missing from apteva.yaml mcp_tools", tool.Name)
		}
	}
	for name := range declared {
		if !served[name] {
			t.Errorf("apteva.yaml declares tool %q but the server does not serve it", name)
		}
	}
}

func TestMCPToolNamesAreUniqueAndHandled(t *testing.T) {
	seen := map[string]bool{}
	for _, tool := range (&App{}).MCPTools() {
		if seen[tool.Name] {
			t.Errorf("duplicate tool name %q", tool.Name)
		}
		seen[tool.Name] = true
		if tool.Handler == nil {
			t.Errorf("tool %q has no handler", tool.Name)
		}
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("tool %q has no description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no input schema", tool.Name)
		}
	}
}

// The manifest version and the release ref move together; a mismatch ships an
// app whose runtime points at the previous tag.
func TestManifestVersionMatchesReleaseRef(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	version := regexp.MustCompile(`(?m)^version:\s*([0-9.]+)\s*$`).FindStringSubmatch(string(raw))
	ref := regexp.MustCompile(`(?m)^\s*ref:\s*ads/v([0-9.]+)\s*$`).FindStringSubmatch(string(raw))
	if version == nil {
		t.Fatal("no version in apteva.yaml")
	}
	if ref == nil {
		t.Fatal("no ads/vX release ref in apteva.yaml")
	}
	if version[1] != ref[1] {
		t.Fatalf("version %s does not match release ref ads/v%s", version[1], ref[1])
	}
}
