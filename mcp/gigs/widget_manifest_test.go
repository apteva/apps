package main

import "testing"

func TestManifestDeclaresGigQueueWidget(t *testing.T) {
	m := (&App{}).Manifest()
	if len(m.Provides.UIComponents) != 1 {
		t.Fatalf("ui_components = %d, want 1", len(m.Provides.UIComponents))
	}
	c := m.Provides.UIComponents[0]
	if c.Name != "gig-queue" || c.Entry != "/ui/GigQueueWidget.mjs" {
		t.Fatalf("component = %+v", c)
	}
	if len(c.Slots) != 1 || c.Slots[0] != "dashboard.home" {
		t.Fatalf("slots = %v", c.Slots)
	}
	if len(c.RefreshTopics) != 12 {
		t.Fatalf("refresh_topics = %d, want 12 gig.* events", len(c.RefreshTopics))
	}
	if c.SettingsSchema == nil {
		t.Fatal("settings_schema missing")
	}
	properties, _ := c.SettingsSchema["properties"].(map[string]any)
	primary, _ := properties["primary_date"].(map[string]any)
	if primary["default"] != "scheduled_for" {
		t.Fatalf("primary_date default = %v", primary["default"])
	}
}
