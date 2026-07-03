package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
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

func TestCreateFromAudioWithoutDeepgramStillRecords(t *testing.T) {
	pf := &voiceNotesPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithPlatform(pf),
		tk.WithConfig(map[string]string{"auto_transcribe": "true"}),
	)
	app := &App{}

	n, err := app.createFromAudio(ctx.WithProject("test-proj"), "test-proj", uploadRequest{
		Name:          "sample.webm",
		ContentBase64: base64.StdEncoding.EncodeToString([]byte("audio")),
		ContentType:   "audio/webm",
		Transcribe:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.TranscriptStatus != "none" || n.ErrorMessage != "" {
		t.Fatalf("transcript status=%q error=%q, want none/no error", n.TranscriptStatus, n.ErrorMessage)
	}
	if n.Status != "recorded" {
		t.Fatalf("status=%q, want recorded", n.Status)
	}
	if n.StorageFileID != "42" {
		t.Fatalf("storage_file_id=%q, want 42", n.StorageFileID)
	}
	if n.PlaybackURL == "" {
		t.Fatal("expected playback URL")
	}
	if pf.executions != 0 {
		t.Fatalf("Deepgram executions=%d, want 0", pf.executions)
	}
}

func TestAttachPlaybackURLsAddsSignedURLsToListRows(t *testing.T) {
	pf := &voiceNotesPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithPlatform(pf),
	)
	if _, err := insertNote(ctx.AppDB(), "test-proj", &noteInput{
		Title:            "Recorded note",
		Status:           "recorded",
		StorageFileID:    "42",
		FileName:         "sample.webm",
		TranscriptStatus: "none",
	}); err != nil {
		t.Fatal(err)
	}
	notes, err := listNotes(ctx.AppDB(), "test-proj", "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if notes[0].PlaybackURL != "" {
		t.Fatalf("listNotes playback_url=%q before attach, want empty", notes[0].PlaybackURL)
	}
	attachPlaybackURLs(ctx.WithProject("test-proj"), notes)
	if notes[0].PlaybackURL == "" {
		t.Fatal("expected playback URL after attach")
	}
}

type voiceNotesPlatform struct {
	tk.BasePlatformClient
	executions int
}

func (p *voiceNotesPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{}}, nil
}

func (p *voiceNotesPlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	var payload map[string]any
	switch tool {
	case "files_upload":
		payload = map[string]any{
			"id":         42,
			"url":        "/api/apps/storage/files/42/content/sample.webm",
			"size_bytes": 5,
			"name":       input["name"],
		}
	case "files_get_url":
		payload = map[string]any{
			"url":        "/api/apps/storage/files/42/content/sample.webm?sig=test&exp=999",
			"expires_at": 999,
		}
	default:
		payload = map[string]any{}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (p *voiceNotesPlatform) ExecuteIntegrationTool(int64, string, map[string]any) (*sdk.ExecuteResult, error) {
	p.executions++
	return &sdk.ExecuteResult{Success: false}, nil
}
