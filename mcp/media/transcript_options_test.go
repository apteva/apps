package main

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestTranscriptionOptionsFrozenAcrossMixedJobsAndRetries(t *testing.T) {
	stub := boundDeepgram()
	ctx := newTestCtxWithPlatform(t, stub, tk.WithConfig(map[string]string{"transcribe_model": "nova-3", "transcribe_language": "auto"}))
	for _, id := range []string{"1", "2"} {
		if err := upsertMedia(ctx.AppDB(), testProj, id, sampleAudioProbe(), "sha", "", "source.wav"); err != nil {
			t.Fatal(err)
		}
		seedTranscriptAudio(t, ctx, id, "sha", 9001, 5000)
	}
	app := &App{}
	if _, err := app.toolTranscribe(ctx, map[string]any{"_project_id": testProj, "file_id": "1", "model": "nova-2", "language": "en"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolTranscribe(ctx, map[string]any{"_project_id": testProj, "file_id": "2"}); err != nil {
		t.Fatal(err)
	}
	if ctx.Config().Get("transcribe_model") != "nova-3" || ctx.Config().Get("transcribe_language") != "auto" {
		t.Fatal("request mutated global defaults")
	}
	// Operator defaults change after both jobs were accepted.
	ctx.Config()["transcribe_model"] = "whisper"
	ctx.Config()["transcribe_language"] = "fr"
	first, err := claimNextPendingTranscript(ctx.AppDB(), testProj)
	if err != nil {
		t.Fatal(err)
	}
	stub.executeErr = errors.New("retryable provider failure")
	runOneTranscription(ctx, ctx.IntegrationFor("transcripts"), first)
	second, err := claimNextPendingTranscript(ctx.AppDB(), testProj)
	if err != nil {
		t.Fatal(err)
	}
	stub.executeErr = nil
	stub.executeResp = &sdk.ExecuteResult{Success: true, Status: 200, Data: timedResponse(phraseWords)}
	runOneTranscription(ctx, ctx.IntegrationFor("transcripts"), second)
	if err = insertPendingTranscript(ctx.AppDB(), testProj, first.FileID, "auto", transcriptionDefaults(ctx)); err != nil {
		t.Fatal(err)
	}
	retry, err := claimNextPendingTranscript(ctx.AppDB(), testProj)
	if err != nil {
		t.Fatal(err)
	}
	runOneTranscription(ctx, ctx.IntegrationFor("transcripts"), retry)
	if len(stub.ExecuteCalls) != 3 {
		t.Fatalf("provider calls=%d", len(stub.ExecuteCalls))
	}
	expected := map[string]transcriptionOptions{"1": {Model: "nova-2", Language: "en"}, "2": {Model: "nova-3", Language: "auto"}}
	for _, row := range []*TranscriptRow{first, second, retry} {
		want := expected[row.FileID]
		if row.Options == nil || row.Options.Model != want.Model || row.Options.Language != want.Language {
			t.Fatalf("settings drifted: %+v expected %+v", row.Options, want)
		}
	}
	for i, row := range []*TranscriptRow{first, second, retry} {
		call := stub.ExecuteCalls[i]
		want := expected[row.FileID]
		if call.Input["model"] != want.Model {
			t.Fatalf("call %d wrong model: %+v", i, call.Input)
		}
		if want.Language == "auto" {
			if call.Input["detect_language"] != true {
				t.Fatal("lost auto language")
			}
		} else if call.Input["language"] != want.Language {
			t.Fatal("lost explicit language")
		}
	}
	for _, id := range []string{"1", "2"} {
		saved, err := getTranscript(ctx.AppDB(), testProj, id)
		if err != nil || saved.Status != "ok" || saved.Model != expected[id].Model || saved.Options.Language != expected[id].Language {
			t.Fatalf("persisted options: %+v err=%v", saved, err)
		}
	}
	if ctx.Config().Get("transcribe_model") != "whisper" || ctx.Config().Get("transcribe_language") != "fr" {
		t.Fatal("worker changed operator defaults")
	}
}

func TestTranscriptionRequestDoesNotOverwriteInflightSettings(t *testing.T) {
	ctx := newTestCtxWithPlatform(t, boundDeepgram(), tk.WithConfig(map[string]string{"transcribe_model": "nova-3", "transcribe_language": "auto"}))
	app := &App{}
	if _, err := app.toolTranscribe(ctx, map[string]any{"_project_id": testProj, "file_id": "1", "model": "nova-2", "language": "en"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolTranscribe(ctx, map[string]any{"_project_id": testProj, "file_id": "1", "model": "nova-3", "language": "fr", "force": true}); err == nil {
		t.Fatal("in-flight settings overwritten")
	}
	saved, err := getTranscriptAttempt(ctx.AppDB(), testProj, "1")
	if err != nil || saved.Options.Model != "nova-2" || saved.Options.Language != "en" {
		t.Fatalf("settings lost: %+v %v", saved, err)
	}
}

func TestNewSourceUsesCurrentDefaultsAndManualRetryKeepsFrozenOptions(t *testing.T) {
	ctx := newTestCtxWithPlatform(t, boundDeepgram(), tk.WithConfig(map[string]string{"transcribe_model": "nova-3", "transcribe_language": "auto"}))
	if err := upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "old-source", "", "source.wav"); err != nil {
		t.Fatal(err)
	}
	if err := upsertTranscript(ctx.AppDB(), &TranscriptRow{FileID: "1", ProjectID: testProj, SourceSHA256: "old-source", Text: "completed", Options: &transcriptionOptions{Model: "nova-2", Language: "en"}}); err != nil {
		t.Fatal(err)
	}
	if err := upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "new-source", "", "source.wav"); err != nil {
		t.Fatal(err)
	}
	if err := insertPendingTranscript(ctx.AppDB(), testProj, "1", "auto", transcriptionDefaults(ctx)); err != nil {
		t.Fatal(err)
	}
	row, err := claimNextPendingTranscript(ctx.AppDB(), testProj)
	if err != nil {
		t.Fatal(err)
	}
	if row.Options.Model != "nova-3" || row.Options.Language != "auto" {
		t.Fatalf("new source inherited previous completed settings: %+v", row.Options)
	}
	if err = transcriptMarkFailed(ctx.AppDB(), testProj, "1", "temporary failure", row.StartedAt); err != nil {
		t.Fatal(err)
	}
	ctx.Config()["transcribe_model"] = "whisper"
	ctx.Config()["transcribe_language"] = "fr"
	if _, err = (&App{}).toolTranscribe(ctx, map[string]any{"_project_id": testProj, "file_id": "1"}); err != nil {
		t.Fatal(err)
	}
	retry, err := getTranscriptAttempt(ctx.AppDB(), testProj, "1")
	if err != nil {
		t.Fatal(err)
	}
	if retry.Options.Model != "nova-3" || retry.Options.Language != "auto" {
		t.Fatalf("retry reread changed defaults: %+v", retry.Options)
	}
}

func TestTranscriptionRESTOptionsAndConflict(t *testing.T) {
	ctx := newTestCtxWithPlatform(t, boundDeepgram(), tk.WithConfig(map[string]string{"transcribe_model": "nova-3", "transcribe_language": "auto"}))
	app := &App{}
	request := func(query string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/media/1/transcribe?project_id="+testProj+query, nil)
		w := httptest.NewRecorder()
		app.handleMediaItem(w, r)
		return w
	}
	accepted := request("&model=nova-2&language=en&force=true")
	if accepted.Code != 202 {
		t.Fatalf("queue response: %d %s", accepted.Code, accepted.Body.String())
	}
	var response struct {
		Options *transcriptionOptions `json:"options"`
	}
	if err := json.Unmarshal(accepted.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Options == nil || response.Options.Model != "nova-2" || response.Options.Language != "en" {
		t.Fatalf("missing effective options: %+v", response)
	}
	if w := request("&model=nova-3&language=fr&force=true"); w.Code != 409 {
		t.Fatalf("conflict response: %d %s", w.Code, w.Body.String())
	}
	if w := request("&model=nova-2&language=invalid%20language"); w.Code != 400 {
		t.Fatalf("invalid options: %d %s", w.Code, w.Body.String())
	}
	row, err := getTranscriptAttempt(ctx.AppDB(), testProj, "1")
	if err != nil || row.Options.Model != "nova-2" || row.Options.Language != "en" {
		t.Fatalf("REST changed active attempt: %+v %v", row, err)
	}
	if ctx.Config().Get("transcribe_model") != "nova-3" || ctx.Config().Get("transcribe_language") != "auto" {
		t.Fatal("REST mutated defaults")
	}
}

func TestLegacyTranscriptionOptionsFreezeOnce(t *testing.T) {
	ctx := newTestCtxWithPlatform(t, boundDeepgram(), tk.WithConfig(map[string]string{"transcribe_model": "nova-2", "transcribe_language": "en"}))
	if err := insertPendingTranscript(ctx.AppDB(), testProj, "1", "manual"); err != nil {
		t.Fatal(err)
	}
	row, err := claimNextPendingTranscript(ctx.AppDB(), testProj)
	if err != nil {
		t.Fatal(err)
	}
	if err = freezeLegacyTranscriptOptions(ctx.AppDB(), row, transcriptionDefaults(ctx)); err != nil {
		t.Fatal(err)
	}
	ctx.Config()["transcribe_model"] = "nova-3"
	ctx.Config()["transcribe_language"] = "fr"
	if err = freezeLegacyTranscriptOptions(ctx.AppDB(), row, transcriptionDefaults(ctx)); err != nil {
		t.Fatal(err)
	}
	saved, err := getTranscriptAttempt(ctx.AppDB(), testProj, "1")
	if err != nil || saved.Options.Model != "nova-2" || saved.Options.Language != "en" {
		t.Fatalf("legacy options drifted: %+v %v", saved, err)
	}
}
