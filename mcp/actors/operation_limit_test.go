package main

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestNamedOperationCapacityPreservesLargeDefinitions(t *testing.T) {
	d := actorDefinition{SchemaVersion: 1, AllowedHosts: []string{"example.com"}, Operations: map[string]actorOperation{}}
	for i := 0; i < 100; i++ {
		d.Operations[fmt.Sprintf("operation_%d", i)] = actorOperation{Steps: []actorStep{{Action: "wait", Duration: 1}}}
	}
	if err := validateActorDefinition(d); err != nil {
		t.Fatal(err)
	}
	d.Operations["overflow"] = actorOperation{Steps: []actorStep{{Action: "wait", Duration: 1}}}
	if err := validateActorDefinition(d); err == nil {
		t.Fatal("unbounded operations accepted")
	}
}

func TestExistingVideoDraftExampleValidates(t *testing.T) {
	b, err := os.ReadFile("examples/patreon-existing-video-draft.json")
	if err != nil {
		t.Fatal(err)
	}
	var d actorDefinition
	if err = json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if err = validateActorDefinition(d); err != nil {
		t.Fatal(err)
	}
	if !d.Operations["inspect_video_draft"].ReadOnly {
		t.Fatal("draft inspection must be read-only")
	}
	for _, name := range []string{"attach_video_to_draft", "publish_video_draft"} {
		for _, s := range d.Operations[name].Steps {
			if s.Locator.Text == "Create" || s.Locator.Text == "Post" {
				t.Fatal("recovery creates a new post")
			}
		}
	}
}
