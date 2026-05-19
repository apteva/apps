package main

// composer v0.1 — smoke tests over the validator + ffmpeg cmd builder.
// Full executor round-trip (actual ffmpeg invocation) is intentionally
// skipped — too brittle without a known input fixture, and the
// per-component pieces (filter graph generation, drawtext escaping)
// are what's worth pinning.

import (
	"encoding/json"
	"strings"
	"testing"
)

// --- validator ----------------------------------------------------

func TestValidateEdit_MinimalOK(t *testing.T) {
	body := `{"timeline":{"tracks":[{"clips":[{"asset":{"type":"video","src":"storage:1"},"start":0,"length":5}]}]}}`
	if _, err := parseEditJSON(body); err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
}

func TestValidateEdit_RejectsMultiTrack(t *testing.T) {
	body := `{"timeline":{"tracks":[
		{"clips":[{"asset":{"type":"video","src":"storage:1"},"start":0,"length":1}]},
		{"clips":[{"asset":{"type":"video","src":"storage:2"},"start":0,"length":1}]}
	]}}`
	_, err := parseEditJSON(body)
	if err == nil || !strings.Contains(err.Error(), "single video track") {
		t.Fatalf("want multi-track rejection, got %v", err)
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
		"hello":         "hello",
		"a:b":           `a\:b`,
		"it's":          `it\'s`,
		`a\b`:           `a\\b`,
		"line1\nline2":  "line1 line2",
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
