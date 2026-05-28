package main

import "testing"

func TestManifestBasics(t *testing.T) {
	m := (&App{}).Manifest()
	if m.Name != "calls" {
		t.Fatalf("manifest.Name=%q, want calls", m.Name)
	}
	if m.DB == nil || m.DB.Migrations != "migrations/" {
		t.Fatalf("manifest DB migrations not wired: %#v", m.DB)
	}
	if len(m.Requires.Apps) != 0 {
		t.Fatalf("calls should not require other apps, got %#v", m.Requires.Apps)
	}
	names := map[string]bool{}
	for _, tool := range m.Provides.MCPTools {
		names[tool.Name] = true
	}
	for _, name := range []string{
		"calls_create_room",
		"calls_create_join_token",
		"calls_join_room",
		"calls_send_message",
		"calls_append_transcript",
	} {
		if !names[name] {
			t.Fatalf("manifest missing tool %s", name)
		}
	}
}
