package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestBundledQuipsExampleValidates(t *testing.T) {
	raw, err := os.ReadFile("examples/patreon-quips.json")
	if err != nil {
		t.Fatal(err)
	}
	var definition actorDefinition
	if err := json.Unmarshal(raw, &definition); err != nil {
		t.Fatal(err)
	}
	if err := validateActorDefinition(definition); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fetch_quips", "quip_post", "quip_image_post", "quip_video_post", "schedule_quip", "verify_scheduled_quip"} {
		if len(definition.Operations[name].Steps) == 0 {
			t.Fatalf("missing Quips operation %s", name)
		}
	}
}
