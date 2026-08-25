package main

import (
	"encoding/json"
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
