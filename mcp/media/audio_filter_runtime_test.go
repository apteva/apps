package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPrepareAudioFilterParamsInjectsIndexedSampleRate(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "42", sampleAudioProbe(), "sha", "", "source.wav"); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"mode":"normalize","target_lufs":-16}`)
	got := prepareAudioFilterParams(ctx.AppDB(), testProj, "audio_filter", []string{"42"}, raw)
	var params audioFilterParams
	if err := json.Unmarshal(got, &params); err != nil {
		t.Fatal(err)
	}
	if params.SourceSampleRate != 44_100 {
		t.Fatalf("source sample rate=%d want 44100; params=%s", params.SourceSampleRate, got)
	}
}

func TestPrepareAudioFilterParamsFallbackAndNoop(t *testing.T) {
	raw := json.RawMessage(`{"mode":"normalize"}`)
	got := prepareAudioFilterParams(nil, "project", "audio_filter", []string{"missing"}, raw)
	var params audioFilterParams
	if err := json.Unmarshal(got, &params); err != nil {
		t.Fatal(err)
	}
	if params.SourceSampleRate != 48_000 {
		t.Fatalf("fallback sample rate=%d want 48000", params.SourceSampleRate)
	}
	if unchanged := prepareAudioFilterParams(nil, "project", "trim", []string{"missing"}, raw); string(unchanged) != string(raw) {
		t.Fatalf("non-audio operation params changed: %s", unchanged)
	}
}

func TestUntrustedVideoEvidenceCannotSkipValidation(t *testing.T) {
	raw := json.RawMessage(`{"mode":"normalize","_validated_video_evidence":{"sha256":"fake"},"video_evidence":{"decode_ok":true}}`)
	got := prepareAudioFilterParams(nil, "project", "audio_filter", []string{"1"}, raw)
	var params map[string]any
	if json.Unmarshal(got, &params) != nil || params["_validated_video_evidence"] != nil || params["video_evidence"] != nil {
		t.Fatalf("%s", got)
	}
	raw = json.RawMessage(`{"_validated_video_evidence":{"sha256":"old"}}`)
	if got := verifySourceEvidenceIdentity(raw, "new"); strings.Contains(string(got), "_validated_video_evidence") {
		t.Fatal(string(got))
	}
}

func TestTrustedEvidenceAvailableBeforeSourceIndexing(t *testing.T) {
	app := newTestCtx(t)
	id, err := insertRender(app.AppDB(), testProj, "trim", []string{"1"}, nil, "out.mov", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claimNextPending(app.AppDB()); err != nil {
		t.Fatal(err)
	}
	if err := renderUpdateResolvedParams(app.AppDB(), id, json.RawMessage(`{"video_evidence":{"algorithm_version":"media-shared-validation-1","decode_ok":true,"sha256":"verified","frames_checked":60}}`)); err != nil {
		t.Fatal(err)
	}
	if err := renderMarkOk(app.AppDB(), id, "42"); err != nil {
		t.Fatal(err)
	}
	got := prepareAudioFilterParams(app.AppDB(), testProj, "audio_filter", []string{"42"}, json.RawMessage(`{"mode":"normalize"}`))
	var params map[string]any
	if json.Unmarshal(got, &params) != nil || params["_validated_video_evidence"] == nil {
		t.Fatalf("indexing unnecessarily blocked evidence reuse: %s", got)
	}
	if got := verifySourceEvidenceIdentity(got, "changed"); strings.Contains(string(got), "_validated_video_evidence") {
		t.Fatal(string(got))
	}
}
