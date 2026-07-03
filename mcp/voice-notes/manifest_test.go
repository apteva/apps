package main

import (
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestExternalManifestValid(t *testing.T) {
	data, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := sdk.ParseManifest(data)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	if m.Name != "voice-notes" {
		t.Fatalf("name=%q", m.Name)
	}
}

func TestEmbeddedManifestValid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "voice-notes" {
		t.Fatalf("name=%q", m.Name)
	}
	if len(m.Requires.Apps) != 1 || m.Requires.Apps[0].Name != "storage" {
		t.Fatalf("expected storage app dependency, got %#v", m.Requires.Apps)
	}
	if len(m.Requires.Integrations) != 1 || m.Requires.Integrations[0].Role != "transcripts" {
		t.Fatalf("expected optional transcripts integration, got %#v", m.Requires.Integrations)
	}
	if len(m.Provides.MCPTools) != len(app.MCPTools()) {
		t.Fatalf("manifest/tools mismatch: %d vs %d", len(m.Provides.MCPTools), len(app.MCPTools()))
	}
}

func TestParseDeepgramResponse(t *testing.T) {
	raw := []byte(`{
	  "results": { "channels": [ { "alternatives": [ {
	    "transcript": "Hello world.",
	    "detected_language": "en",
	    "paragraphs": { "paragraphs": [ {
	      "speaker": 0,
	      "sentences": [ { "text": "Hello world.", "start": 0.1, "end": 1.2 } ]
	    } ] }
	  } ] } ] }
	}`)
	got, err := parseDeepgramResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "Hello world." || got.Language != "en" {
		t.Fatalf("bad transcript: %#v", got)
	}
	if len(got.Segments) != 1 || got.Segments[0].StartMS != 100 || got.Segments[0].Speaker != "speaker_0" {
		t.Fatalf("bad segments: %#v", got.Segments)
	}
}
