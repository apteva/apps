package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestBundledYouTubeExampleValidates(t *testing.T) {
	raw, err := os.ReadFile("examples/youtube.json")
	if err != nil {
		t.Fatal(err)
	}
	// Reject misspelled fields: the validator ignores unknown keys.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var definition actorDefinition
	if err := decoder.Decode(&definition); err != nil {
		t.Fatal(err)
	}
	if err := validateActorDefinition(definition); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fetch_posts", "fetch_videos", "fetch_studio_content", "text_post", "text_post_preflight", "image_post", "poll_post", "set_video_visibility"} {
		if len(definition.Operations[name].Steps) == 0 {
			t.Fatalf("missing YouTube operation %s", name)
		}
	}
}
