package main

import (
	"reflect"
	"testing"
)

func TestInboxToolIsExplicitlyReadOnly(t *testing.T) {
	found := false
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "conversations_inbox" {
			found = true
			want := map[string]any{"readOnlyHint": true, "destructiveHint": false}
			if !reflect.DeepEqual(tool.Annotations, want) {
				t.Fatalf("inbox annotations=%#v, want %#v", tool.Annotations, want)
			}
		} else if tool.Name != "conversation_drafts_get" && tool.Name != "conversation_drafts_list" && tool.Annotations["readOnlyHint"] == true {
			t.Errorf("unexpected blanket read-only annotation on %q", tool.Name)
		}
	}
	if !found {
		t.Fatal("conversations_inbox not registered")
	}
}
