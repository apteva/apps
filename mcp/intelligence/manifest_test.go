package main

import (
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestManifestIdentityAndToolSurfaceStayInSync(t *testing.T) {
	diskBytes, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := sdk.ParseManifest(diskBytes)
	if err != nil {
		t.Fatalf("disk manifest: %v", err)
	}
	embedded, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatalf("embedded manifest: %v", err)
	}
	if disk.Name != embedded.Name || disk.Version != embedded.Version {
		t.Fatalf("manifest identity drift: disk=%s@%s embedded=%s@%s", disk.Name, disk.Version, embedded.Name, embedded.Version)
	}
	diskTools := map[string]bool{}
	for _, tool := range disk.Provides.MCPTools {
		diskTools[tool.Name] = true
	}
	embeddedTools := map[string]bool{}
	for _, tool := range embedded.Provides.MCPTools {
		embeddedTools[tool.Name] = true
	}
	runtimeTools := map[string]bool{}
	for _, tool := range (&App{}).MCPTools() {
		runtimeTools[tool.Name] = true
	}
	for name := range diskTools {
		if !embeddedTools[name] || !runtimeTools[name] {
			t.Errorf("tool %q missing from embedded/runtime surface", name)
		}
	}
	for name := range runtimeTools {
		if !diskTools[name] {
			t.Errorf("runtime tool %q missing from disk manifest", name)
		}
	}
}
