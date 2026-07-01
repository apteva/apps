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

func TestValidateEdit_PreservesAIImageSize(t *testing.T) {
	body := `{"timeline":{"tracks":[{"clips":[{
		"uid":"clip-ai-image",
		"asset":{"type":"image","src":""},
		"ai":{"media_kind":"image","prompt":"portrait recipe still","model":"flux-2-pro","size":"720x1280"},
		"start":0,
		"length":5
	}]}]}}`
	e, err := parseEditJSON(body)
	if err != nil {
		t.Fatalf("expected AI image clip to validate, got %v", err)
	}
	ai := e.Timeline.Tracks[0].Clips[0].AI
	if ai == nil || ai.Size != "720x1280" {
		t.Fatalf("AI image size did not survive parse: %+v", ai)
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

func TestAssetSearchMatching(t *testing.T) {
	file := storageListFile{
		ID:          185,
		Name:        "sfx-hands10.mp3",
		Folder:      "/.composer/sfx/",
		ContentType: "audio/mpeg",
		Source:      "human",
		Tags:        []string{"composer", "sfx", "snap", "fiftysounds"},
	}
	if !assetMatchesSearch(file, "snap", "audio", []string{"sfx"}) {
		t.Fatal("expected snap SFX audio asset to match")
	}
	if assetMatchesSearch(file, "", "image", nil) {
		t.Fatal("audio asset should not match image kind")
	}
	if assetMatchesSearch(file, "", "audio", []string{"ambience"}) {
		t.Fatal("missing tag should not match")
	}
	if got := kindFromStorageFile(file); got != "audio" {
		t.Fatalf("kind = %q, want audio", got)
	}
	if generatedOrSystemAsset(file) {
		t.Fatal("reusable composer SFX should not be treated as generated/system")
	}
	generated := storageListFile{
		Name:        "voice.mp3",
		Folder:      "/.generated/audio/",
		ContentType: "audio/mpeg",
		Source:      "generated",
		Tags:        []string{"ai", "generated", "elevenlabs"},
	}
	if !generatedOrSystemAsset(generated) {
		t.Fatal("generated media should be hidden from reusable asset search by default")
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

func TestBuildLocalFFmpegArgs_FitSourceVideoDoesNotLoopOrClonePad(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"tracks":[{"clips":[
		{"asset":{"src":"https://v","type":"video"},"start":0,"length":8,"actual_length":3,
		 "timing":{"mode":"fit_source","source":"track:audio"}}
	]}]}}`)
	args := buildLocalFFmpegArgs(e, defaultOutput(), []string{"https://v"}, -1, "out.mp4")
	cmd := strings.Join(args, " ")
	if strings.Contains(cmd, "-stream_loop -1 -i https://v") {
		t.Fatalf("matched video should not loop input: %s", cmd)
	}
	if strings.Contains(cmd, "tpad=stop_mode=clone") {
		t.Fatalf("matched video should not clone-pad the final frame unless explicitly requested: %s", cmd)
	}
	if !strings.Contains(cmd, "trim=duration=8") {
		t.Fatalf("video should trim exactly to the target slot: %s", cmd)
	}
}

func TestBuildLocalFFmpegArgs_LoopedVisualVideoUsesStreamLoop(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"tracks":[{"clips":[
		{"asset":{"src":"https://spiral.mp4","type":"video"},"start":0,"length":12,"actual_length":3,
		 "timing":{"mode":"fit_source","source":"track:audio","behavior":"loop"}}
	]}]}}`)
	args := buildLocalFFmpegArgs(e, defaultOutput(), []string{"https://spiral.mp4"}, -1, "out.mp4")
	cmd := strings.Join(args, " ")
	if !strings.Contains(cmd, "-stream_loop -1 -i https://spiral.mp4") {
		t.Fatalf("looped visual video should loop input: %s", cmd)
	}
	if strings.Contains(cmd, "tpad=stop_mode=clone") {
		t.Fatalf("looped visual video should not clone-pad: %s", cmd)
	}
	if !strings.Contains(cmd, "trim=duration=12") {
		t.Fatalf("looped visual video should trim repeated input to target slot: %s", cmd)
	}
}

func TestBuildLocalFFmpegArgs_TrimOrLoopVisualVideoUsesStreamLoop(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"tracks":[{"clips":[
		{"asset":{"src":"https://spiral.mp4","type":"video"},"start":0,"length":12,"actual_length":3,
		 "timing":{"mode":"fit_source","source":"track:audio","behavior":"trim_or_loop"}}
	]}]}}`)
	args := buildLocalFFmpegArgs(e, defaultOutput(), []string{"https://spiral.mp4"}, -1, "out.mp4")
	cmd := strings.Join(args, " ")
	if !strings.Contains(cmd, "-stream_loop -1 -i https://spiral.mp4") {
		t.Fatalf("trim_or_loop visual video should loop input so short sources fill the slot: %s", cmd)
	}
	if strings.Contains(cmd, "tpad=stop_mode=clone") {
		t.Fatalf("trim_or_loop visual video should not clone-pad: %s", cmd)
	}
}

func TestBuildLocalFFmpegArgs_ExplicitPadKeepsClonePadding(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"tracks":[{"clips":[
		{"asset":{"src":"https://v","type":"video"},"start":0,"length":8,"actual_length":3,
		 "timing":{"mode":"fit_source","source":"track:audio","behavior":"pad"}}
	]}]}}`)
	args := buildLocalFFmpegArgs(e, defaultOutput(), []string{"https://v"}, -1, "out.mp4")
	cmd := strings.Join(args, " ")
	if strings.Contains(cmd, "-stream_loop -1 -i https://v") {
		t.Fatalf("explicit pad should not loop input: %s", cmd)
	}
	if !strings.Contains(cmd, "tpad=stop_mode=clone") {
		t.Fatalf("explicit pad should clone-pad the final frame: %s", cmd)
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

func TestBuildLocalFFmpegArgs_SilentVideoFallback(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"tracks":[{"clips":[
		{"asset":{"src":"https://silent.mp4","type":"video"},"start":0,"length":3}
	]}]}}`)
	args := buildLocalFFmpegArgsWithAudioInfo(e, defaultOutput(), []string{"https://silent.mp4"}, -1, "out.mp4", []bool{false})
	cmd := strings.Join(args, " ")
	if strings.Contains(cmd, "[0:a]") {
		t.Fatalf("no-audio video should not reference missing audio stream: %s", cmd)
	}
	if !strings.Contains(cmd, "anullsrc=channel_layout=stereo") || !strings.Contains(cmd, "atrim=duration=3") {
		t.Fatalf("no-audio video should synthesize silent audio: %s", cmd)
	}
}

func TestBuildLocalFFmpegArgs_MutesNoSoundAIVideo(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"tracks":[{"clips":[
		{"asset":{"src":"https://video.mp4","type":"video"},"start":0,"length":3,
		 "ai":{"media_kind":"video","prompt":"silent clip","options":{"no_sound":true}}}
	]}]}}`)
	args := buildLocalFFmpegArgsWithAudioInfo(e, defaultOutput(), []string{"https://video.mp4"}, -1, "out.mp4", []bool{true})
	cmd := strings.Join(args, " ")
	if strings.Contains(cmd, "[0:a]") {
		t.Fatalf("no_sound AI video should ignore source audio: %s", cmd)
	}
	if !strings.Contains(cmd, "anullsrc=channel_layout=stereo") {
		t.Fatalf("no_sound AI video should synthesize silent audio: %s", cmd)
	}
}

func TestBuildLocalFFmpegArgs_KeepsVideoAudioWhenPresent(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"tracks":[{"clips":[
		{"asset":{"src":"https://video.mp4","type":"video"},"start":0,"length":3,"source_audio":"keep"}
	]}]}}`)
	args := buildLocalFFmpegArgsWithAudioInfo(e, defaultOutput(), []string{"https://video.mp4"}, -1, "out.mp4", []bool{true})
	cmd := strings.Join(args, " ")
	if !strings.Contains(cmd, "[0:a]apad,atrim=duration=3") {
		t.Fatalf("video with source audio should keep source audio: %s", cmd)
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

func TestBuildLocalFFmpegArgs_TextOverlayTrack(t *testing.T) {
	e, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"visual","clips":[{"asset":{"src":"https://a","type":"video"},"start":0,"length":6}]},
		{"type":"overlay","clips":[{
			"asset":{"type":"text","text":"Cinematic title","font":{"size":72,"color":"#ffffff"},"stroke":{"color":"#000000","width":4},"shadow":{"color":"#ff2f6d","offset_y":2}},
			"start":1,"length":3,
			"animation":{"in":{"preset":"fade_up","duration":0.5},"out":{"preset":"fade","duration":0.4}}
		}]}
	]}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := buildLocalFFmpegArgs(e, defaultOutput(), []string{"https://a"}, -1, "out.mp4")
	cmd := strings.Join(args, " ")
	if !strings.Contains(cmd, "drawtext=text='Cinematic title'") {
		t.Fatalf("text overlay track should emit drawtext: %s", cmd)
	}
	if !strings.Contains(cmd, "enable='between(t\\,1\\,4)'") {
		t.Fatalf("text overlay should be time-gated: %s", cmd)
	}
	if !strings.Contains(cmd, "alpha='(if(lt(t\\,1)") {
		t.Fatalf("fade animation should emit alpha expression: %s", cmd)
	}
	if !strings.Contains(cmd, "[vcat]drawtext") || !strings.Contains(cmd, "[vtxt0];[vtxt0]null[vout]") {
		t.Fatalf("text overlay should apply after visual concat: %s", cmd)
	}
}

func TestValidateEdit_TextOverlayTrackRejectsMissingText(t *testing.T) {
	_, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"visual","clips":[{"asset":{"src":"https://a","type":"video"},"start":0,"length":3}]},
		{"type":"overlay","clips":[{"asset":{"type":"text"},"start":0,"length":2}]}
	]}}`)
	if err == nil || !strings.Contains(err.Error(), "text asset requires") {
		t.Fatalf("expected missing text rejection, got %v", err)
	}
}

func TestValidateEdit_TextOverlayTrackRejectsBadAnimation(t *testing.T) {
	_, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"visual","clips":[{"asset":{"src":"https://a","type":"video"},"start":0,"length":3}]},
		{"type":"text","clips":[{"asset":{"type":"text","text":"Hello"},"start":0,"length":2,"animation":{"in":{"preset":"explode"}}}]}
	]}}`)
	if err == nil || !strings.Contains(err.Error(), "animation.in") {
		t.Fatalf("expected animation rejection, got %v", err)
	}
}

func TestBuildLocalFFmpegArgs_SoundtrackMix(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{
		"soundtrack":{"src":"https://s","volume":0.5},
		"tracks":[{"clips":[{"asset":{"src":"https://a","type":"video"},"start":0,"length":4}]}]
	}}`)
	args := buildLocalFFmpegArgs(e, defaultOutput(), []string{"https://a", "https://s"}, 1, "out.mp4")
	cmd := strings.Join(args, " ")
	if !strings.Contains(cmd, "-stream_loop -1 -i https://s") {
		t.Errorf("soundtrack should loop as a bed input: %s", cmd)
	}
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

func TestSyncClipDurationFromAI_RefreshesStaleActualLength(t *testing.T) {
	c := Clip{
		Asset:           Asset{Type: "audio", Src: "storage:1"},
		Length:          5,
		ActualLength:    5,
		EstimatedLength: 5,
		DurationMode:    "fit_generated_reflow",
		AI: &AIAsset{
			MediaKind:                "audio_tts",
			Prompt:                   "hello",
			ActualDurationSeconds:    9.4,
			EstimatedDurationSeconds: 5,
		},
	}
	if !syncClipDurationFromAI(&c) {
		t.Fatal("expected stale duration metadata to update")
	}
	if c.Length != 9.4 || c.ActualLength != 9.4 {
		t.Fatalf("clip duration metadata = %+v, want real generated duration", c)
	}
}

func TestApplyClipTiming_FitSourceTrackAudioBySection(t *testing.T) {
	e, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"visual","clips":[
			{"uid":"image-1","section_id":"intro","asset":{"type":"image","src":"storage:1"},"start":0,"length":4,
				"timing":{"mode":"fit_source","source":"track:audio","padding_after":0.35}}
		]},
		{"type":"audio","clips":[
			{"uid":"voice-1","section_id":"intro","asset":{"type":"audio","src":"storage:2"},"start":0,"length":8.2}
		]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !applyTimelineTiming(e) {
		t.Fatal("expected timing to update visual clip")
	}
	got := e.Timeline.Tracks[0].Clips[0].Length
	if !sameTime(got, 8.55) {
		t.Fatalf("visual length = %v, want audio length plus padding", got)
	}
}

func TestPrepareAIVideoDurationForTiming_UsesAudioMatchedHandle(t *testing.T) {
	e, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"visual","clips":[
			{"uid":"video-1","section_id":"intro","asset":{"type":"video","src":"storage:1"},"start":0,"length":3,
				"timing":{"mode":"fit_source","source":"track:audio","padding_after":0.35},
				"ai":{"media_kind":"video","prompt":"recipe shot","duration":3,"status":"ready","storage_id":12,"generation_id":34,"cache_key":"composer:old","actual_duration_seconds":3}}
		]},
		{"type":"audio","clips":[
			{"uid":"voice-1","section_id":"intro","asset":{"type":"audio","src":"storage:2"},"start":0,"length":8.2}
		]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}
	clip := &e.Timeline.Tracks[0].Clips[0]
	if !prepareAIVideoDurationForTiming(e, clip) {
		t.Fatal("expected AI video duration to be prepared")
	}
	if clip.AI.Duration != 10 {
		t.Fatalf("video duration = %d, want 10", clip.AI.Duration)
	}
	if clip.AI.Status != "draft" || clip.AI.StorageID != 0 || clip.AI.GenerationID != 0 || clip.AI.CacheKey != "" {
		t.Fatalf("AI video generated state was not reset: %+v", clip.AI)
	}
}

func TestPrepareAIVideoDurationForTiming_LoopKeepsReadyShortVideo(t *testing.T) {
	e, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"visual","clips":[
			{"uid":"video-1","section_id":"intro","asset":{"type":"video","src":"storage:1"},"start":0,"length":3,
				"timing":{"mode":"fit_source","source":"track:audio","behavior":"loop"},
				"ai":{"media_kind":"video","prompt":"spiral","duration":3,"status":"ready","storage_id":12,"generation_id":34,"cache_key":"composer:ready","actual_duration_seconds":3}}
		]},
		{"type":"audio","clips":[
			{"uid":"voice-1","section_id":"intro","asset":{"type":"audio","src":"storage:2"},"start":0,"length":60}
		]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}
	clip := &e.Timeline.Tracks[0].Clips[0]
	if prepareAIVideoDurationForTiming(e, clip) {
		t.Fatal("looped ready video should not be regenerated")
	}
	if clip.AI.Duration != 3 || clip.AI.Status != "ready" || clip.AI.StorageID != 12 || clip.AI.GenerationID != 34 || clip.AI.CacheKey != "composer:ready" {
		t.Fatalf("looped ready AI video state changed unexpectedly: %+v", clip.AI)
	}
}

func TestPrepareAIVideoDurationForTiming_KeepsReadyVideoWhenActualCoversTarget(t *testing.T) {
	e, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"visual","clips":[
			{"uid":"video-1","section_id":"intro","asset":{"type":"video","src":"storage:1"},"start":0,"length":7.9,"actual_length":10.04,
				"timing":{"mode":"fit_source","source":"track:audio","padding_after":0.35},
				"ai":{"media_kind":"video","prompt":"recipe shot","duration":9,"status":"ready","storage_id":12,"generation_id":34,"cache_key":"composer:ready","actual_duration_seconds":10.04}}
		]},
		{"type":"audio","clips":[
			{"uid":"voice-1","section_id":"intro","asset":{"type":"audio","src":"storage:2"},"start":0,"length":7.9}
		]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}
	clip := &e.Timeline.Tracks[0].Clips[0]
	if prepareAIVideoDurationForTiming(e, clip) {
		t.Fatal("ready video with enough actual source duration should not be regenerated")
	}
	if clip.AI.StorageID != 12 || clip.AI.Status != "ready" {
		t.Fatalf("ready AI video state changed unexpectedly: %+v", clip.AI)
	}
}

func TestResolveRelativeClipStarts_UsesTimingReflow(t *testing.T) {
	e, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"audio","clips":[
			{"uid":"voice-1","asset":{"type":"audio","src":"storage:1"},"start":0,"length":5,
				"timing":{"mode":"fit_generated","reflow":"following"},
				"ai":{"media_kind":"audio_tts","prompt":"first","actual_duration_seconds":7}},
			{"uid":"voice-2","asset":{"type":"audio","src":"storage:2"},"start":5,"length":3}
		]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !applyTimelineTiming(e) {
		t.Fatal("expected timing to update first clip")
	}
	if !resolveRelativeClipStarts(e) {
		t.Fatal("expected timing reflow to move following clip")
	}
	if got := e.Timeline.Tracks[0].Clips[1].Start; got != 7 {
		t.Fatalf("second clip start = %v, want 7", got)
	}
}

func TestAICacheKey_IncludesImageSize(t *testing.T) {
	base := &AIAsset{MediaKind: "image", Prompt: "portrait recipe still", Model: "flux-2-pro", Size: "720x1280"}
	other := *base
	other.Size = "1280x720"
	if aiCacheKey(base) == aiCacheKey(&other) {
		t.Fatal("cache key should change when AI image size changes")
	}
}

func TestAICacheKey_IgnoresInternalEstimatedDurationOption(t *testing.T) {
	base := &AIAsset{
		MediaKind: "video",
		Prompt:    "quick recipe prep shot",
		Model:     "pixverse-c1-text-to-video",
		Duration:  3,
		Aspect:    "16:9",
		Options: map[string]any{
			"no_sound": true,
		},
	}
	other := *base
	other.Options = map[string]any{
		"estimated_duration_seconds": 3,
		"no_sound":                   true,
	}
	if aiCacheKey(base) != aiCacheKey(&other) {
		t.Fatal("internal estimated duration option should not change AI cache key")
	}
}

func TestMediaGenerateOptions_DoesNotMutateAIOptions(t *testing.T) {
	ai := &AIAsset{
		EstimatedDurationSeconds: 3,
		Options: map[string]any{
			"no_sound": true,
		},
	}
	opts := mediaGenerateOptions(ai)
	if _, ok := opts["estimated_duration_seconds"]; !ok {
		t.Fatal("expected media generate options to include estimated duration hint")
	}
	if _, ok := ai.Options["estimated_duration_seconds"]; ok {
		t.Fatal("media generate options must not mutate stored AI options")
	}
}

func TestMediaGenerateOptions_OverridesStaleInternalEstimate(t *testing.T) {
	ai := &AIAsset{
		EstimatedDurationSeconds: 10,
		Options: map[string]any{
			"estimated_duration_seconds": 3,
			"no_sound":                   true,
		},
	}
	opts := mediaGenerateOptions(ai)
	if got := opts["estimated_duration_seconds"]; got != float64(10) {
		t.Fatalf("estimated duration option = %v, want 10", got)
	}
	if got := ai.Options["estimated_duration_seconds"]; got != 3 {
		t.Fatalf("stored AI options mutated = %v, want original 3", got)
	}
}

func TestValidateEdit_DefaultsTTSVoiceSettings(t *testing.T) {
	edit := `{"timeline":{"tracks":[{"type":"audio","clips":[{"uid":"voice","asset":{"type":"audio","src":""},"start":0,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Hello there"}}]}]}}`
	got, err := parseEditJSON(edit)
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := got.Timeline.Tracks[0].Clips[0].AI.Options["voice_settings"].(map[string]any)
	if settings == nil {
		t.Fatalf("voice_settings missing: %+v", got.Timeline.Tracks[0].Clips[0].AI.Options)
	}
	if settings["stability"] != 0.85 {
		t.Fatalf("stability = %v, want 0.85", settings["stability"])
	}
	if settings["similarity_boost"] != 0.95 {
		t.Fatalf("similarity_boost = %v, want 0.95", settings["similarity_boost"])
	}
	if settings["style"] != 0 {
		t.Fatalf("style = %v, want 0", settings["style"])
	}
	if settings["use_speaker_boost"] != true {
		t.Fatalf("use_speaker_boost = %v, want true", settings["use_speaker_boost"])
	}
}

func TestValidateEdit_PreservesExplicitTTSVoiceSettings(t *testing.T) {
	edit := `{"timeline":{"tracks":[{"type":"audio","clips":[{"uid":"voice","asset":{"type":"audio","src":""},"start":0,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Hello there","options":{"voice_settings":{"stability":0.4,"similarity_boost":0.7,"style":0.2,"use_speaker_boost":false}}}}]}]}}`
	got, err := parseEditJSON(edit)
	if err != nil {
		t.Fatal(err)
	}
	settings, _ := got.Timeline.Tracks[0].Clips[0].AI.Options["voice_settings"].(map[string]any)
	if settings == nil {
		t.Fatal("voice_settings missing")
	}
	if settings["stability"] != 0.4 {
		t.Fatalf("stability = %v, want explicit 0.4", settings["stability"])
	}
	if settings["style"] != 0.2 {
		t.Fatalf("style = %v, want explicit 0.2", settings["style"])
	}
	if settings["use_speaker_boost"] != false {
		t.Fatalf("use_speaker_boost = %v, want explicit false", settings["use_speaker_boost"])
	}
}

func TestTTSContinuityOptions_UsesNeighboringSameVoiceText(t *testing.T) {
	edit := `{"timeline":{"tracks":[{"type":"audio","clips":[
		{"uid":"a","asset":{"type":"audio","src":""},"start":0,"length":5,"ai":{"media_kind":"audio_tts","prompt":"First part.","voice":"voice-1","model":"eleven_multilingual_v2"}},
		{"uid":"gap","asset":{"type":"silence","src":""},"start":5,"length":1},
		{"uid":"b","asset":{"type":"audio","src":""},"start":6,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Second part.","voice":"voice-1","model":"eleven_multilingual_v2"}},
		{"uid":"sfx","asset":{"type":"audio","src":""},"start":11,"length":1,"ai":{"media_kind":"audio_sfx","prompt":"soft whoosh"}},
		{"uid":"c","asset":{"type":"audio","src":""},"start":12,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Third part.","voice":"voice-1","model":"eleven_multilingual_v2"}}
	]}]}}`
	got, err := parseEditJSON(edit)
	if err != nil {
		t.Fatal(err)
	}
	opts := ttsContinuityOptions(&got.Timeline.Tracks[0], 2)
	if opts["previous_text"] != "First part." {
		t.Fatalf("previous_text = %v, want First part.", opts["previous_text"])
	}
	if opts["next_text"] != "Third part." {
		t.Fatalf("next_text = %v, want Third part.", opts["next_text"])
	}
}

func TestTTSContinuityOptions_PrefersRequestIDs(t *testing.T) {
	edit := `{"timeline":{"tracks":[{"type":"audio","clips":[
		{"uid":"a","asset":{"type":"audio","src":""},"start":0,"length":5,"ai":{"media_kind":"audio_tts","prompt":"First part.","voice":"voice-1","model":"eleven_multilingual_v2","provider_request_id":"req-a"}},
		{"uid":"gap","asset":{"type":"silence","src":""},"start":5,"length":1},
		{"uid":"b","asset":{"type":"audio","src":""},"start":6,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Second part.","voice":"voice-1","model":"eleven_multilingual_v2"}},
		{"uid":"c","asset":{"type":"audio","src":""},"start":11,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Third part.","voice":"voice-1","model":"eleven_multilingual_v2","provider_request_id":"req-c"}}
	]}]}}`
	got, err := parseEditJSON(edit)
	if err != nil {
		t.Fatal(err)
	}
	opts := ttsContinuityOptions(&got.Timeline.Tracks[0], 2)
	prev, _ := opts["previous_request_ids"].([]string)
	next, _ := opts["next_request_ids"].([]string)
	if len(prev) != 1 || prev[0] != "req-a" {
		t.Fatalf("previous_request_ids = %+v, want req-a", prev)
	}
	if len(next) != 1 || next[0] != "req-c" {
		t.Fatalf("next_request_ids = %+v, want req-c", next)
	}
	if _, ok := opts["previous_text"]; ok {
		t.Fatalf("previous_text should be omitted when request IDs are available: %+v", opts)
	}
	if _, ok := opts["next_text"]; ok {
		t.Fatalf("next_text should be omitted when request IDs are available: %+v", opts)
	}
}

func TestTTSContinuityOptions_StopsAtDifferentVoice(t *testing.T) {
	edit := `{"timeline":{"tracks":[{"type":"audio","clips":[
		{"uid":"a","asset":{"type":"audio","src":""},"start":0,"length":5,"ai":{"media_kind":"audio_tts","prompt":"First part.","voice":"voice-1","model":"eleven_multilingual_v2"}},
		{"uid":"b","asset":{"type":"audio","src":""},"start":5,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Second part.","voice":"voice-2","model":"eleven_multilingual_v2"}},
		{"uid":"c","asset":{"type":"audio","src":""},"start":10,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Third part.","voice":"voice-1","model":"eleven_multilingual_v2"}}
	]}]}}`
	got, err := parseEditJSON(edit)
	if err != nil {
		t.Fatal(err)
	}
	opts := ttsContinuityOptions(&got.Timeline.Tracks[0], 1)
	if len(opts) != 0 {
		t.Fatalf("different voice should not receive context: %+v", opts)
	}
}

func TestTTSContinuityOptions_SkipsElevenV3(t *testing.T) {
	edit := `{"timeline":{"tracks":[{"type":"audio","clips":[
		{"uid":"a","asset":{"type":"audio","src":""},"start":0,"length":5,"ai":{"media_kind":"audio_tts","prompt":"First part.","voice":"voice-1","model":"eleven_v3"}},
		{"uid":"b","asset":{"type":"audio","src":""},"start":5,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Second part.","voice":"voice-1","model":"eleven_v3"}},
		{"uid":"c","asset":{"type":"audio","src":""},"start":10,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Third part.","voice":"voice-1","model":"eleven_v3"}}
	]}]}}`
	got, err := parseEditJSON(edit)
	if err != nil {
		t.Fatal(err)
	}
	opts := ttsContinuityOptions(&got.Timeline.Tracks[0], 1)
	if len(opts) != 0 {
		t.Fatalf("eleven_v3 should not receive continuity context: %+v", opts)
	}
}

func TestMediaGenerateOptions_AddsContinuityWithoutMutatingClipOptions(t *testing.T) {
	ai := &AIAsset{
		MediaKind: "audio_tts",
		Prompt:    "Middle.",
		Options: map[string]any{
			"voice_settings": map[string]any{"stability": 0.85},
		},
	}
	opts := mediaGenerateOptions(ai, map[string]any{"previous_text": "Before.", "next_text": "After."})
	if opts["previous_text"] != "Before." || opts["next_text"] != "After." {
		t.Fatalf("continuity options missing: %+v", opts)
	}
	if _, ok := ai.Options["previous_text"]; ok {
		t.Fatalf("stored AI options mutated: %+v", ai.Options)
	}
}

func TestAICacheKey_IncludesContinuityContext(t *testing.T) {
	ai := &AIAsset{MediaKind: "audio_tts", Prompt: "Middle.", Voice: "voice-1", Model: "eleven_multilingual_v2"}
	a := aiCacheKeyWithOptions(ai, map[string]any{"previous_text": "Before."})
	b := aiCacheKeyWithOptions(ai, map[string]any{"previous_text": "Different before."})
	if a == b {
		t.Fatal("cache key should change when TTS continuity context changes")
	}
}

func TestMCPErrorText(t *testing.T) {
	got := map[string]any{
		"isError": true,
		"content": []any{
			map[string]any{"type": "text", "text": "provider returned non-2xx: unsupported model"},
		},
	}
	if text := mcpErrorText(got); text != "provider returned non-2xx: unsupported model" {
		t.Fatalf("unexpected error text: %q", text)
	}
}

func TestEnrichEditJSONWithMediaHistory_RestoresImageSize(t *testing.T) {
	edit := `{"timeline":{"tracks":[{"type":"visual","clips":[{"asset":{"type":"image","src":"storage:42"},"start":0,"length":5}]}]}}`
	out := enrichEditJSONWithMediaHistory(edit, map[int64]*mediaHistoryRow{
		42: {
			ID:         7,
			Kind:       "image",
			Prompt:     "portrait recipe still",
			Model:      "flux-2-pro",
			Size:       "720x1280",
			StorageIDs: []int64{42},
			Status:     "complete",
		},
	})
	var got Edit
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	ai := got.Timeline.Tracks[0].Clips[0].AI
	if ai == nil || ai.Size != "720x1280" {
		t.Fatalf("expected enriched AI size, got %+v", ai)
	}
}

func TestProbeAssetDurationSeconds_UsesFFProbe(t *testing.T) {
	dir := t.TempDir()
	ffprobe := filepath.Join(dir, "fake-ffprobe")
	if err := os.WriteFile(ffprobe, []byte(`#!/bin/sh
printf '6.75\n'
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FFPROBE_PATH", ffprobe)

	ctx := tk.NewAppCtx(t, "apteva.yaml")
	if got := probeAssetDurationSeconds(ctx, "https://example.test/audio.mp3"); got != 6.75 {
		t.Fatalf("duration = %v, want 6.75", got)
	}
}

func TestStorageLocalPathForKey_FindsSiblingStorageBlob(t *testing.T) {
	root := t.TempDir()
	appData := filepath.Join(root, "apps", "composer", "data", "60")
	storageBlob := filepath.Join(root, "apps", "storage", "data", "6", "storage-blobs", "ab", "asset.mp3")
	if err := os.MkdirAll(filepath.Dir(storageBlob), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storageBlob, []byte("mp3"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := storageLocalPathForKey(appData, "asset.mp3")
	if err != nil {
		t.Fatalf("storage local path: %v", err)
	}
	if got != storageBlob {
		t.Fatalf("path = %q, want %q", got, storageBlob)
	}
}

func TestAIKindHasMediaDuration(t *testing.T) {
	for _, kind := range []string{"audio_tts", "audio_sfx", "music", "video", "avatar"} {
		if !aiKindHasMediaDuration(kind) {
			t.Fatalf("%s should have media duration", kind)
		}
	}
	if aiKindHasMediaDuration("image") {
		t.Fatal("image should not be probed for duration")
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

func TestValidateEdit_DefaultsGeneratedSFXNormalization(t *testing.T) {
	e, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"audio","clips":[
			{"asset":{"src":"https://snap.mp3","type":"audio"},"start":0,"length":1,
				"ai":{"media_kind":"audio_sfx","prompt":"single finger snap"}}
		]}
	]}}`)
	if err != nil {
		t.Fatalf("generated sfx should validate: %v", err)
	}
	clip := e.Timeline.Tracks[0].Clips[0]
	if clip.Audio == nil {
		t.Fatal("generated sfx should default audio processing")
	}
	if !clip.Audio.Normalize || !clip.Audio.TrimSilence {
		t.Fatalf("generated sfx audio defaults = %+v, want normalize and trim", clip.Audio)
	}
	if clip.Audio.LoudnessTarget != -16 || clip.Audio.PeakLimitDB != -2 {
		t.Fatalf("generated sfx loudness defaults = %+v, want -16 LUFS / -2 dBTP", clip.Audio)
	}

	args := buildLocalAudioFFmpegArgs(e, Output{Format: "mp3"}, []string{"https://snap.mp3"}, -1, "out.mp3")
	cmd := strings.Join(args, " ")
	if !strings.Contains(cmd, "silenceremove=") || !strings.Contains(cmd, "loudnorm=I=-16:TP=-2") {
		t.Fatalf("generated sfx normalization filters missing: %s", cmd)
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

func TestRemoteRenderScriptUsesStorageUploadLadder(t *testing.T) {
	script := remoteRenderScript(
		[]string{"https://example.com/in.mp4"},
		"ffmpeg -i ./in0 ./out.mp4",
		"mp4",
		"project-1",
		"https://agents.example.com/",
		"token-redacted",
		"composition.mp4",
		"video/mp4",
	)
	for _, want := range []string{
		"$STORAGE_BASE/files/init?project_id=$PROJECT_ID",
		"$STORAGE_BASE/uploads?project_id=$PROJECT_ID",
		"$STORAGE_BASE/uploads/$CHUNK_UPLOAD_ID/parts/$PART?project_id=$PROJECT_ID",
		"$STORAGE_BASE/uploads/$CHUNK_UPLOAD_ID/complete?project_id=$PROJECT_ID",
		"composer-render",
		"APTEVA_RESULT:{\\\"storage_id\\\":${STORAGE_ID}",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("remote script missing %q\n%s", want, script)
		}
	}
	oneShot := "$STORAGE_BASE/files?project_id=$PROJECT_ID"
	if first := strings.Index(script, "$STORAGE_BASE/files/init?project_id=$PROJECT_ID"); first < 0 {
		t.Fatalf("missing direct upload init")
	} else if last := strings.LastIndex(script, oneShot); last < first {
		t.Fatalf("legacy one-shot upload should only appear after upload ladder")
	}
}
