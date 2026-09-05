package main

import (
	"os"
	"reflect"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// The embedded manifest must always parse — it's our single source of
// truth for the manifest the binary advertises. If this test fails,
// the binary won't survive sdk.Run's ValidateManifest at boot.
func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "crm" {
		t.Errorf("manifest.Name=%q, want crm", m.Name)
	}
	if m.Version == "" {
		t.Error("manifest.Version is empty")
	}
	if len(m.Provides.MCPTools) != 53 {
		t.Errorf("expected 53 MCP tools, got %d", len(m.Provides.MCPTools))
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Errorf("manifest.DB.Migrations missing")
	}
	if len(m.Provides.Publishes) != 26 {
		t.Errorf("expected 26 published event declarations, got %d", len(m.Provides.Publishes))
	}
	// Surfaces the embedded scopes — should accept project + global.
	gotScopes := map[string]bool{}
	for _, s := range m.Scopes {
		gotScopes[string(s)] = true
	}
	for _, want := range []string{"project", "global"} {
		if !gotScopes[want] {
			t.Errorf("manifest missing scope %q", want)
		}
	}
}

func TestEmbeddedManifest_PublishesCRMEvents(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	got := map[string]bool{}
	for _, ev := range m.Provides.Publishes {
		got[ev.Name] = true
		if ev.Description == "" {
			t.Errorf("event %q missing description", ev.Name)
		}
	}
	for _, want := range []string{
		"contact.added",
		"contact.updated",
		"contact.channel.deliverability.changed",
		"contact.deleted",
		"contact.merged",
		"contact.activity.added",
		"conversation.status.changed",
		"conversation.message.received",
		"list.created",
		"list.updated",
		"list.archived",
		"list.member.added",
		"list.member.removed",
		"segment.created",
		"segment.updated",
		"segment.archived",
		"segment.materialised",
		"pipeline.created",
		"pipeline.stage.created",
		"pipeline.stage.updated",
		"opportunity.created",
		"opportunity.updated",
		"opportunity.stage.changed",
		"opportunity.status.changed",
		"opportunity.won",
		"opportunity.lost",
	} {
		if !got[want] {
			t.Errorf("manifest missing published event %q", want)
		}
	}
}

func TestContactAddedManifestDeclaresEmittedPayload(t *testing.T) {
	want := map[string]string{
		"event_id":     "string",
		"id":           "integer",
		"display_name": "string",
		"first_name":   "string",
		"last_name":    "string",
		"archived":     "boolean",
		"list_ids":     "array<integer>",
	}
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	for source, manifest := range map[string]sdk.Manifest{
		"embedded": (&App{}).Manifest(),
		"disk":     *disk,
	} {
		found := false
		for _, event := range manifest.Provides.Publishes {
			if event.Name != "contact.added" {
				continue
			}
			found = true
			if !reflect.DeepEqual(event.Payload, want) {
				t.Fatalf("%s contact.added payload=%v, want %v", source, event.Payload, want)
			}
		}
		if !found {
			t.Fatalf("%s manifest does not declare contact.added", source)
		}
	}
}

// ToolHandlers and the manifest's mcp_tools list must agree on count
// and names. A common mistake is adding a tool to one and forgetting
// the other; this test catches it.
func TestMCPTools_ManifestMatchesHandlers(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	declared := map[string]bool{}
	for _, t := range m.Provides.MCPTools {
		declared[t.Name] = true
	}
	implemented := map[string]bool{}
	for _, t := range app.MCPTools() {
		implemented[t.Name] = true
	}
	for name := range declared {
		if !implemented[name] {
			t.Errorf("manifest declares tool %q but no handler implements it", name)
		}
	}
	for name := range implemented {
		if !declared[name] {
			t.Errorf("handler implements %q but manifest doesn't declare it", name)
		}
	}
}

func TestDiskManifestParsesAndMatchesEmbeddedSurface(t *testing.T) {
	body, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := sdk.ParseManifest(body)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	embedded := (&App{}).Manifest()
	if disk.Version != embedded.Version {
		t.Fatalf("manifest version drift: disk=%s embedded=%s", disk.Version, embedded.Version)
	}
	if len(disk.Provides.MCPTools) != len(embedded.Provides.MCPTools) {
		t.Fatalf("tool declaration drift: disk=%d embedded=%d", len(disk.Provides.MCPTools), len(embedded.Provides.MCPTools))
	}
	if len(disk.Provides.Publishes) != len(embedded.Provides.Publishes) {
		t.Fatalf("event declaration drift: disk=%d embedded=%d", len(disk.Provides.Publishes), len(embedded.Provides.Publishes))
	}
	if len(disk.Provides.Skills) != len(embedded.Provides.Skills) {
		t.Fatalf("skill declaration drift: disk=%d embedded=%d", len(disk.Provides.Skills), len(embedded.Provides.Skills))
	}
	for i := range disk.Provides.Skills {
		if disk.Provides.Skills[i].Name != embedded.Provides.Skills[i].Name ||
			disk.Provides.Skills[i].BodyFile != embedded.Provides.Skills[i].BodyFile {
			t.Fatalf("skill declaration drift: disk=%+v embedded=%+v", disk.Provides.Skills[i], embedded.Provides.Skills[i])
		}
	}
}
