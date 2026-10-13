package main

import "testing"

func TestPostListDeclaresReadOnlyWithoutAnnotatingPublishers(t *testing.T) {
	tools := (&App{}).MCPTools()
	found := false
	for _, tool := range tools {
		if tool.Name == "post_list" {
			found = true
			if tool.Annotations["readOnlyHint"] != true || tool.Annotations["destructiveHint"] != false {
				t.Fatalf("post_list hints: %#v", tool.Annotations)
			}
		}
		switch tool.Name {
		case "post_create", "post_retry", "post_reschedule", "post_delete", "post_draft_publish", "post_publish_scheduled":
			if tool.Annotations["readOnlyHint"] == true {
				t.Fatalf("mutator %s advertised as read-only", tool.Name)
			}
		}
	}
	if !found {
		t.Fatal("post_list missing")
	}
}
