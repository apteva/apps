package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAttributeEventsExactOwnershipAndAmbiguity(t *testing.T) {
	now := time.Now().UTC()
	scene := &Scene{Destinations: []SceneDestination{
		{ID: "app:crm", Kind: "app", Name: "CRM", Tools: []string{"crm_search", "shared_call"}},
		{ID: "integration:mail", Kind: "integration", Name: "Mail", Tools: []string{"mail_send", "shared_call"}},
	}, Events: []SceneEvent{
		{ID: "1", Kind: "tool", Target: "crm_search", Time: now},
		{ID: "2", Kind: "result", Target: "crm_search", Time: now.Add(time.Second)},
		{ID: "3", Kind: "tool", Target: "crm", Time: now.Add(2 * time.Second)},
		{ID: "4", Kind: "tool", Target: "shared_call", Time: now.Add(3 * time.Second)},
		{ID: "5", Kind: "tool", Target: "mail_send", Time: now.Add(4 * time.Second)},
	}}
	attributeEvents(scene)
	want := []string{"app:crm", "app:crm", "other:tools", "other:tools", "integration:mail"}
	for i, event := range scene.Events {
		if event.TargetID != want[i] {
			t.Errorf("event %d target=%q want %q", i, event.TargetID, want[i])
		}
	}
	counts := map[string]int{}
	for _, item := range scene.Destinations {
		counts[item.ID] = item.CallCount
	}
	if counts["app:crm"] != 1 || counts["integration:mail"] != 1 || counts["other:tools"] != 2 {
		t.Fatalf("call counts: %#v", counts)
	}
}

func TestLongToolNameMatchesWithoutExposingArguments(t *testing.T) {
	name := strings.Repeat("a", 70)
	raw, _ := json.Marshal(map[string]any{"name": name, "arguments": map[string]any{"token": "private-token"}})
	event := normalizeEvent("1", 1, "main", "tool.call", time.Now(), raw)
	scene := &Scene{Destinations: []SceneDestination{{ID: "app:long", Tools: []string{name}}}, Events: []SceneEvent{event}}
	attributeEvents(scene)
	if scene.Events[0].TargetID != "app:long" {
		t.Fatalf("long tool did not match: %#v", scene.Events[0])
	}
	encoded, _ := json.Marshal(scene)
	if strings.Contains(string(encoded), "private-token") {
		t.Fatalf("sensitive content exposed: %s", encoded)
	}
}

func TestDestinationSceneKeepsOnlyToolPreview(t *testing.T) {
	scene := &Scene{Destinations: []SceneDestination{{ID: "app:many", Tools: []string{"one", "two", "three", "four"}}}}
	attributeEvents(scene)
	got := scene.Destinations[0]
	if got.ToolCount != 4 || len(got.Tools) != 3 {
		t.Fatalf("tool preview: count=%d tools=%v", got.ToolCount, got.Tools)
	}
}
