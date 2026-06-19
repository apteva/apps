package main

// composer v0.1 — smoke tests over the validator + ffmpeg cmd builder.
// Full executor round-trip (actual ffmpeg invocation) is intentionally
// skipped — too brittle without a known input fixture, and the
// per-component pieces (filter graph generation, drawtext escaping)
// are what's worth pinning.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// --- validator ----------------------------------------------------

func TestValidateEdit_MinimalOK(t *testing.T) {
	body := `{"timeline":{"tracks":[{"clips":[{"asset":{"type":"video","src":"storage:1"},"start":0,"length":5}]}]}}`
	if _, err := parseEditJSON(body); err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
}

func TestValidateEdit_AcceptsAudioTrack(t *testing.T) {
	body := `{"timeline":{"tracks":[
		{"type":"visual","clips":[{"asset":{"type":"video","src":"storage:1"},"start":0,"length":3}]},
		{"type":"audio","clips":[{"asset":{"type":"audio","src":"storage:2"},"start":1,"length":1.5,"volume":0.4}]}
	]}}`
	if _, err := parseEditJSON(body); err != nil {
		t.Fatalf("expected audio track to validate, got %v", err)
	}
}

func TestValidateEdit_RejectsSecondVisualTrack(t *testing.T) {
	body := `{"timeline":{"tracks":[
		{"type":"visual","clips":[{"asset":{"type":"video","src":"storage:1"},"start":0,"length":1}]},
		{"type":"visual","clips":[{"asset":{"type":"video","src":"storage:2"},"start":0,"length":1}]}
	]}}`
	_, err := parseEditJSON(body)
	if err == nil || !strings.Contains(err.Error(), "one visual track") {
		t.Fatalf("want second visual track rejection, got %v", err)
	}
}

func TestValidateEdit_RejectsBadAssetType(t *testing.T) {
	body := `{"timeline":{"tracks":[{"clips":[{"asset":{"type":"hologram","src":"x"},"start":0,"length":1}]}]}}`
	_, err := parseEditJSON(body)
	if err == nil || !strings.Contains(err.Error(), "unsupported asset.type") {
		t.Fatalf("want asset-type rejection, got %v", err)
	}
}

func TestValidateEdit_RejectsBadTransition(t *testing.T) {
	body := `{"timeline":{"tracks":[{"clips":[{"asset":{"type":"video","src":"storage:1"},"start":0,"length":2,"transition":{"in":"swirl"}}]}]}}`
	_, err := parseEditJSON(body)
	if err == nil || !strings.Contains(err.Error(), "transition.in") {
		t.Fatalf("want transition rejection, got %v", err)
	}
}

func TestValidateEdit_RejectsZeroLength(t *testing.T) {
	body := `{"timeline":{"tracks":[{"clips":[{"asset":{"type":"video","src":"storage:1"},"start":0,"length":0}]}]}}`
	_, err := parseEditJSON(body)
	if err == nil || !strings.Contains(err.Error(), "length") {
		t.Fatalf("want length rejection, got %v", err)
	}
}

func TestValidateEdit_AcceptsAIClipWithoutMaterializedSource(t *testing.T) {
	body := `{"timeline":{"tracks":[{"clips":[{
		"uid":"clip-ai",
		"asset":{"type":"video","src":""},
		"ai":{"media_kind":"video","prompt":"cinematic product reveal","duration":5},
		"start":0,
		"length":5
	}]}]}}`
	e, err := parseEditJSON(body)
	if err != nil {
		t.Fatalf("expected AI clip to save before materialization, got %v", err)
	}
	if e.Timeline.Tracks[0].Clips[0].AI == nil || e.Timeline.Tracks[0].Clips[0].UID != "clip-ai" {
		t.Fatalf("AI metadata did not survive parse: %+v", e.Timeline.Tracks[0].Clips[0])
	}
}

func TestValidateEdit_AcceptsSoundtrack(t *testing.T) {
	body := `{"timeline":{
		"soundtrack":{"src":"storage:99","volume":0.5},
		"tracks":[{"clips":[{"asset":{"type":"video","src":"storage:1"},"start":0,"length":5}]}]
	}}`
	if _, err := parseEditJSON(body); err != nil {
		t.Fatalf("expected ok with soundtrack, got %v", err)
	}
}

func TestValidateEdit_RejectsSoundtrackBadVolume(t *testing.T) {
	body := `{"timeline":{
		"soundtrack":{"src":"storage:99","volume":1.5},
		"tracks":[{"clips":[{"asset":{"type":"video","src":"storage:1"},"start":0,"length":5}]}]
	}}`
	_, err := parseEditJSON(body)
	if err == nil || !strings.Contains(err.Error(), "volume") {
		t.Fatalf("want volume rejection, got %v", err)
	}
}

// --- duration sum ------------------------------------------------

func TestEditDuration_Sum(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"tracks":[{"clips":[
		{"asset":{"src":"x","type":"video"},"start":0,"length":2.5},
		{"asset":{"src":"y","type":"video"},"start":2.5,"length":3}
	]}]}}`)
	if got := editDurationSeconds(e); got != 5.5 {
		t.Errorf("duration = %v, want 5.5", got)
	}
}

// --- ffmpeg cmd builder ------------------------------------------

func TestBuildLocalFFmpegArgs_TwoClipsBasic(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"tracks":[{"clips":[
		{"asset":{"src":"https://a","type":"video"},"start":0,"length":2},
		{"asset":{"src":"https://b","type":"video"},"start":2,"length":3}
	]}]}}`)
	out := defaultOutput()
	args := buildLocalFFmpegArgs(e, out, []string{"https://a", "https://b"}, -1, "out.mp4")
	cmd := strings.Join(args, " ")
	if !strings.Contains(cmd, "concat=n=2:v=1:a=1") {
		t.Errorf("missing concat filter: %s", cmd)
	}
	if !strings.Contains(cmd, "trim=duration=2") || !strings.Contains(cmd, "trim=duration=3") {
		t.Errorf("missing per-clip trim: %s", cmd)
	}
	if !strings.Contains(cmd, "libx264") || !strings.Contains(cmd, "aac") {
		t.Errorf("missing codec flags: %s", cmd)
	}
}

func TestBuildLocalFFmpegArgs_ImageClipUsesLoop(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"tracks":[{"clips":[
		{"asset":{"src":"https://i","type":"image"},"start":0,"length":4}
	]}]}}`)
	out := defaultOutput()
	args := buildLocalFFmpegArgs(e, out, []string{"https://i"}, -1, "out.mp4")
	cmd := strings.Join(args, " ")
	if !strings.Contains(cmd, "-loop 1") || !strings.Contains(cmd, "-t 4") {
		t.Errorf("image clip should use -loop 1 -t 4: %s", cmd)
	}
	if !strings.Contains(cmd, "anullsrc") {
		t.Errorf("image clip should synthesize silent audio: %s", cmd)
	}
}

func TestBuildLocalFFmpegArgs_FadeTransition(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"tracks":[{"clips":[
		{"asset":{"src":"https://a","type":"video"},"start":0,"length":3,"transition":{"in":"fade","out":"fade"}}
	]}]}}`)
	args := buildLocalFFmpegArgs(e, defaultOutput(), []string{"https://a"}, -1, "out.mp4")
	cmd := strings.Join(args, " ")
	if !strings.Contains(cmd, "fade=t=in:st=0:d=0.3") {
		t.Errorf("missing fade-in: %s", cmd)
	}
	if !strings.Contains(cmd, "fade=t=out:") {
		t.Errorf("missing fade-out: %s", cmd)
	}
}

func TestBuildLocalFFmpegArgs_TextOverlay(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"tracks":[{"clips":[
		{"asset":{"src":"https://a","type":"video"},"start":0,"length":3,"text":{"body":"Hello: world","position":"top","font_size":40}}
	]}]}}`)
	args := buildLocalFFmpegArgs(e, defaultOutput(), []string{"https://a"}, -1, "out.mp4")
	cmd := strings.Join(args, " ")
	if !strings.Contains(cmd, "drawtext=text='Hello\\: world'") {
		t.Errorf("text overlay should be escaped + included: %s", cmd)
	}
	if !strings.Contains(cmd, "fontsize=40") {
		t.Errorf("font_size should plumb through: %s", cmd)
	}
}

func TestBuildLocalFFmpegArgs_SoundtrackMix(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{
		"soundtrack":{"src":"https://s","volume":0.5},
		"tracks":[{"clips":[{"asset":{"src":"https://a","type":"video"},"start":0,"length":4}]}]
	}}`)
	args := buildLocalFFmpegArgs(e, defaultOutput(), []string{"https://a", "https://s"}, 1, "out.mp4")
	cmd := strings.Join(args, " ")
	if !strings.Contains(cmd, "volume=0.5") {
		t.Errorf("soundtrack volume should be applied: %s", cmd)
	}
	if !strings.Contains(cmd, "amix=inputs=2") {
		t.Errorf("expected amix when soundtrack set: %s", cmd)
	}
}

func TestBuildLocalFFmpegArgs_AudioTrackMix(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"visual","clips":[{"asset":{"src":"https://v","type":"video"},"start":0,"length":4}]},
		{"type":"audio","clips":[{"asset":{"src":"https://sfx","type":"audio"},"start":1,"length":2,"volume":0.25}]}
	]}}`)
	args := buildLocalFFmpegArgs(e, defaultOutput(), []string{"https://v", "https://sfx"}, -1, "out.mp4")
	cmd := strings.Join(args, " ")
	if !strings.Contains(cmd, "adelay=1000|1000") {
		t.Errorf("audio track start should become delay: %s", cmd)
	}
	if !strings.Contains(cmd, "volume=0.25") {
		t.Errorf("audio clip volume should be applied: %s", cmd)
	}
	if !strings.Contains(cmd, "amix=inputs=2") {
		t.Errorf("visual audio and audio track should be mixed: %s", cmd)
	}
}

func TestValidateEdit_AcceptsGeneratedAudioAsset(t *testing.T) {
	body := `{"timeline":{"tracks":[
		{"type":"visual","clips":[{"asset":{"type":"video","src":"storage:1"},"start":0,"length":4}]},
		{"clips":[{"asset":{"type":"generated","provider":"media-studio","kind":"music","request":{"prompt":"minimal beat","duration":4}},"start":0,"length":4}]}
	]}}`
	e, err := parseEditJSON(body)
	if err != nil {
		t.Fatalf("expected generated audio asset to validate, got %v", err)
	}
	ai := e.Timeline.Tracks[1].Clips[0].AI
	if ai == nil || ai.MediaKind != "music" || ai.Prompt != "minimal beat" {
		t.Fatalf("generated asset did not normalize to AI metadata: %+v", ai)
	}
}

func TestValidateEdit_AcceptsAIPlaceholderWithEstimate(t *testing.T) {
	e, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"audio","clips":[{"asset":{"type":"audio","src":""},"start":0,
			"ai":{"media_kind":"audio_tts","prompt":"Hello there","estimated_duration_seconds":5}}]}
	]}}`)
	if err != nil {
		t.Fatalf("AI placeholder with estimate should validate: %v", err)
	}
	c := e.Timeline.Tracks[0].Clips[0]
	if c.DurationMode != "fit_generated_reflow" {
		t.Fatalf("duration mode = %q, want fit_generated_reflow", c.DurationMode)
	}
	if c.Length != 5 || c.EstimatedLength != 5 {
		t.Fatalf("length metadata = length %v estimated %v, want 5/5", c.Length, c.EstimatedLength)
	}
}

func TestResolveRelativeClipStarts_UsesActualGeneratedDuration(t *testing.T) {
	e, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"audio","clips":[
			{"uid":"voice-1","asset":{"type":"audio","src":"storage:1"},"start":0,"length":5,"duration_mode":"fit_generated_reflow",
				"ai":{"media_kind":"audio_tts","prompt":"first","actual_duration_seconds":7}},
			{"uid":"voice-2","asset":{"type":"audio","src":"storage:2"},"start":10,"length":3,"after_clip_id":"voice-1","gap_seconds":5}
		]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !syncClipDurationFromAI(&e.Timeline.Tracks[0].Clips[0]) {
		t.Fatal("expected first clip duration to sync")
	}
	if !resolveRelativeClipStarts(e) {
		t.Fatal("expected relative start to change")
	}
	if got := e.Timeline.Tracks[0].Clips[1].Start; got != 12 {
		t.Fatalf("second clip start = %v, want 12", got)
	}
}

func TestSyncClipDurationFromAI_FitGeneratedUpdatesLength(t *testing.T) {
	c := Clip{
		Asset:        Asset{Type: "audio", Src: "storage:1"},
		Length:       5,
		DurationMode: "fit_generated",
		AI: &AIAsset{
			MediaKind:                "audio_tts",
			Prompt:                   "hello",
			ActualDurationSeconds:    7.25,
			EstimatedDurationSeconds: 5,
		},
	}
	if !syncClipDurationFromAI(&c) {
		t.Fatal("expected duration sync to report a change")
	}
	if c.Length != 7.25 || c.ActualLength != 7.25 || c.EstimatedLength != 5 {
		t.Fatalf("clip duration metadata = %+v", c)
	}
}

func TestValidateEditOutput_AudioOnlyRequiresAudioFormat(t *testing.T) {
	e, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"audio","clips":[{"asset":{"src":"https://a.mp3","type":"audio"},"start":0,"length":2}]}
	]}}`)
	if err != nil {
		t.Fatalf("audio-only edit should parse: %v", err)
	}
	if err := validateEditOutput(e, Output{Format: "mp4"}); err == nil || !strings.Contains(err.Error(), "audio-only") {
		t.Fatalf("want audio-only format rejection, got %v", err)
	}
	if err := validateEditOutput(e, Output{Format: "mp3"}); err != nil {
		t.Fatalf("mp3 should be valid for audio-only edit: %v", err)
	}
}

func TestBuildLocalAudioFFmpegArgs_MP3WithSilenceGap(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"audio","clips":[
			{"asset":{"src":"https://a.mp3","type":"audio"},"start":0,"length":2},
			{"asset":{"src":"https://b.mp3","type":"audio"},"start":5,"length":3}
		]}
	]}}`)
	args := buildLocalAudioFFmpegArgs(e, Output{Format: "mp3"}, []string{"https://a.mp3", "https://b.mp3"}, -1, "out.mp3")
	cmd := strings.Join(args, " ")
	if strings.Contains(cmd, "[vout]") || strings.Contains(cmd, "libx264") {
		t.Errorf("audio-only render should not map or encode video: %s", cmd)
	}
	if !strings.Contains(cmd, "adelay=5000|5000") {
		t.Errorf("second clip start should create a silence gap: %s", cmd)
	}
	if !strings.Contains(cmd, "apad,atrim=duration=2") {
		t.Errorf("audio clip should trim/pad to its slot: %s", cmd)
	}
	if !strings.Contains(cmd, "libmp3lame") || !strings.Contains(cmd, "-map [aout]") {
		t.Errorf("mp3 audio mapping/codec missing: %s", cmd)
	}
}

func TestValidateEdit_AcceptsExplicitSilenceClip(t *testing.T) {
	e, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"audio","clips":[
			{"uid":"silence-1","asset":{"type":"silence"},"start":2,"length":5}
		]}
	]}}`)
	if err != nil {
		t.Fatalf("silence clip should validate: %v", err)
	}
	if got := editDurationSeconds(e); got != 7 {
		t.Fatalf("duration = %v, want 7", got)
	}
}

func TestBuildLocalAudioFFmpegArgs_SilenceAndAudioFX(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"audio","clips":[
			{"asset":{"src":"https://a.mp3","type":"audio"},"start":0,"length":2,"audio":{"gain_db":6,"normalize":true,"fade_in_seconds":0.2,"fade_out_seconds":0.3}},
			{"asset":{"type":"silence"},"start":2,"length":5},
			{"asset":{"src":"https://b.mp3","type":"audio"},"start":7,"length":3}
		]}
	]}}`)
	args := buildLocalAudioFFmpegArgs(e, Output{Format: "mp3"}, []string{"https://a.mp3", "https://b.mp3"}, -1, "out.mp3")
	cmd := strings.Join(args, " ")
	if !strings.Contains(cmd, "anullsrc=channel_layout=stereo") || !strings.Contains(cmd, "atrim=duration=5") {
		t.Fatalf("silence clip should synthesize silence: %s", cmd)
	}
	if !strings.Contains(cmd, "volume=6dB") || !strings.Contains(cmd, "loudnorm=I=-18:TP=-3") {
		t.Fatalf("audio processing filters missing: %s", cmd)
	}
	if strings.Count(cmd, " -i ") != 2 {
		t.Fatalf("silence clip should not add an input: %s", cmd)
	}
}

func TestLocalExecutorKeepsOutputUntilCleanup(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "fake-ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte(`#!/bin/sh
out=""
for arg in "$@"; do
  out="$arg"
done
printf 'rendered' > "$out"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFMPEG_PATH", ffmpeg)

	edit, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"audio","clips":[{"asset":{"src":"https://example.test/a.mp3","type":"audio"},"start":0,"length":2}]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}

	ctx := tk.NewAppCtx(t, "apteva.yaml")
	result, err := (&localFFmpegExecutor{}).Render(context.Background(), ctx, edit, Output{Format: "mp3"}, "test-project")
	if err != nil {
		t.Fatal(err)
	}
	if result.LocalPath == "" {
		t.Fatal("expected local render path")
	}
	if _, err := os.Stat(result.LocalPath); err != nil {
		t.Fatalf("render output should still exist for dispatch persistence: %v", err)
	}
	if result.Cleanup == nil {
		t.Fatal("expected cleanup callback")
	}
	result.Cleanup()
	if _, err := os.Stat(result.LocalPath); !os.IsNotExist(err) {
		t.Fatalf("cleanup should remove render output, got %v", err)
	}
}

func TestRenderContentType(t *testing.T) {
	cases := map[string]string{
		"mp3": "audio/mpeg",
		"wav": "audio/wav",
		"m4a": "audio/mp4",
		"mp4": "video/mp4",
	}
	for format, want := range cases {
		if got := renderContentType(format); got != want {
			t.Errorf("renderContentType(%q) = %q, want %q", format, got, want)
		}
	}
}

// --- editFromArgs round-trip -------------------------------------

func TestEditFromArgs_ReconstructsTimeline(t *testing.T) {
	args := map[string]any{
		"tracks": []any{map[string]any{
			"clips": []any{
				map[string]any{
					"asset":  map[string]any{"type": "video", "src": "storage:1"},
					"start":  0,
					"length": 3,
				},
			},
		}},
		"soundtrack": map[string]any{"src": "storage:2", "volume": 0.7},
		"background": "#101010",
	}
	e, err := editFromArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	if e.Timeline.Background != "#101010" {
		t.Errorf("background lost: %q", e.Timeline.Background)
	}
	if e.Timeline.Soundtrack == nil || e.Timeline.Soundtrack.Volume != 0.7 {
		t.Errorf("soundtrack lost: %+v", e.Timeline.Soundtrack)
	}
	b, _ := json.Marshal(e)
	if !strings.Contains(string(b), `"length":3`) {
		t.Errorf("clip length lost: %s", b)
	}
}

// --- escDrawText -------------------------------------------------

func TestEscDrawText(t *testing.T) {
	cases := map[string]string{
		"hello":        "hello",
		"a:b":          `a\:b`,
		"it's":         `it\'s`,
		`a\b`:          `a\\b`,
		"line1\nline2": "line1 line2",
	}
	for in, want := range cases {
		if got := escDrawText(in); got != want {
			t.Errorf("escDrawText(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- resolutionWH ------------------------------------------------

func TestResolutionWH(t *testing.T) {
	w, h := resolutionWH("hd", "16:9")
	if w != 1280 || h != 720 {
		t.Errorf("hd 16:9 = %dx%d, want 1280x720", w, h)
	}
	w, h = resolutionWH("hd", "9:16")
	if w != 720 || h != 1280 {
		t.Errorf("hd 9:16 should be portrait flipped, got %dx%d", w, h)
	}
	w, h = resolutionWH("4k", "16:9")
	if w != 3840 || h != 2160 {
		t.Errorf("4k 16:9 = %dx%d, want 3840x2160", w, h)
	}
}

func TestAssetKindHint(t *testing.T) {
	cases := map[string]string{
		"https://cdn.example.com/clip.mp4":   "video",
		"storage:42":                         "video",
		"https://cdn.example.com/still.webp": "image",
		"https://cdn.example.com/music.wav":  "audio",
	}
	for in, want := range cases {
		if got := assetKindHint(in); got != want {
			t.Errorf("assetKindHint(%q) = %q, want %q", in, got, want)
		}
	}
}
