package main

// composer v0.1 — smoke tests over the validator + ffmpeg cmd builder.
// Full executor round-trip (actual ffmpeg invocation) is intentionally
// skipped — too brittle without a known input fixture, and the
// per-component pieces (filter graph generation, drawtext escaping)
// are what's worth pinning.

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
	"golang.org/x/image/font"
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

func TestValidateEdit_AcceptsLayeredVisualTracks(t *testing.T) {
	body := `{"timeline":{"tracks":[
		{"type":"visual","clips":[{"asset":{"type":"video","src":"storage:1"},"start":0,"length":1}]},
		{"type":"visual","clips":[{"asset":{"type":"video","src":"storage:2"},"start":0,"length":1,"position":"bottomLeft","width":0.25}]}
	]}}`
	edit, err := parseEditJSON(body)
	if err != nil {
		t.Fatalf("expected layered visual tracks to validate, got %v", err)
	}
	if got := len(visualOverlayClipRefs(edit)); got != 1 {
		t.Fatalf("overlay visual refs = %d, want 1", got)
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

func TestEditDuration_BaseTrackHonorsExplicitGap(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"tracks":[{"clips":[
		{"asset":{"src":"x","type":"video"},"start":0,"length":2},
		{"asset":{"src":"y","type":"video"},"start":5,"length":3}
	]}]}}`)
	if got := editDurationSeconds(e); got != 8 {
		t.Errorf("duration = %v, want explicit timeline end 8", got)
	}
}

func TestEditDuration_IncludesVisualOverlayEnd(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"visual","clips":[{"asset":{"src":"x","type":"image"},"start":0,"length":2}]},
		{"type":"visual","clips":[{"asset":{"src":"y","type":"video"},"start":3,"length":4,"source_audio":"mute"}]}
	]}}`)
	if got := editDurationSeconds(e); got != 7 {
		t.Errorf("duration = %v, want overlay end 7", got)
	}
}

func TestVisualOverlayClipRefsSortByZIndex(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"visual","clips":[{"uid":"base","asset":{"src":"x","type":"image"},"start":0,"length":5}]},
		{"type":"visual","clips":[{"uid":"top","asset":{"src":"y","type":"image"},"start":0,"length":5,"z_index":20}]},
		{"type":"visual","clips":[{"uid":"under","asset":{"src":"z","type":"image"},"start":0,"length":5,"z_index":10}]}
	]}}`)
	refs := visualOverlayClipRefs(e)
	if len(refs) != 2 || refs[0].clip.UID != "under" || refs[1].clip.UID != "top" {
		t.Fatalf("overlay order = %+v, want under then top", refs)
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

func TestBuildLocalFFmpegArgs_BaseTrackHonorsExplicitGap(t *testing.T) {
	e, _ := parseEditJSON(`{"timeline":{"background":"#101010","tracks":[{"clips":[
		{"asset":{"src":"https://a","type":"video"},"start":0,"length":2,"source_audio":"mute"},
		{"asset":{"src":"https://b","type":"video"},"start":5,"length":3,"source_audio":"mute"}
	]}]}}`)
	args := buildLocalFFmpegArgsWithAudioInfo(e, defaultOutput(), []string{"https://a", "https://b"}, -1, "out.mp4", []bool{false, false})
	cmd := strings.Join(args, " ")
	if strings.Contains(cmd, "concat=n=2") {
		t.Fatalf("gapped base track must not collapse through concat: %s", cmd)
	}
	if !strings.Contains(cmd, "color=c=0x101010:s=1280x720:r=30:d=8[vbg]") {
		t.Fatalf("missing full-duration background canvas: %s", cmd)
	}
	if !strings.Contains(cmd, "setpts=PTS-STARTPTS+5/TB") || !strings.Contains(cmd, "between(t\\,5\\,8)") {
		t.Fatalf("second base clip must retain start=5: %s", cmd)
	}
	if !strings.Contains(cmd, "[a1]adelay=5000|5000") {
		t.Fatalf("second base audio must retain start=5: %s", cmd)
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

func TestBuildLocalFFmpegArgs_LayeredVisualOverlay(t *testing.T) {
	e, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"visual","clips":[{"asset":{"src":"https://bg","type":"image"},"start":0,"length":10,"fit":"crop"}]},
		{"type":"visual","clips":[{"asset":{"src":"https://pip","type":"video"},"start":2,"length":5,"position":"bottomLeft","offset":{"x":0.05,"y":0.05},"width":320,"height":180,"fit":"crop"}]}
	]}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := buildLocalFFmpegArgsWithAudioInfo(e, defaultOutput(), []string{"https://bg", "https://pip"}, -1, "out.mp4", []bool{false, true})
	cmd := strings.Join(args, " ")
	if !strings.Contains(cmd, "-loop 1 -t 10 -i https://bg") {
		t.Fatalf("base image should be looped for its duration: %s", cmd)
	}
	if !strings.Contains(cmd, "[1:v]format=rgba,scale=320:180:force_original_aspect_ratio=increase,crop=320:180") {
		t.Fatalf("overlay video should be scaled/cropped to requested box: %s", cmd)
	}
	if !strings.Contains(cmd, "overlay=x=64:y=504:enable='between(t\\,2\\,7)'") {
		t.Fatalf("overlay should be positioned and time-gated: %s", cmd)
	}
	if strings.Contains(cmd, "[1:a]") {
		t.Fatalf("overlay video should default to muted audio: %s", cmd)
	}
}

func TestBuildLocalFFmpegArgs_LayeredVisualOverlayKeepsExplicitAudio(t *testing.T) {
	e, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"visual","clips":[{"asset":{"src":"https://bg","type":"image"},"start":0,"length":6}]},
		{"type":"visual","clips":[{"asset":{"src":"https://pip","type":"video"},"start":1,"length":3,"source_audio":"keep","layout":{"fit":"contain","width":0.25,"anchor":"bottom-right","margin":36,"opacity":0.5}}]}
	]}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := buildLocalFFmpegArgsWithAudioInfo(e, defaultOutput(), []string{"https://bg", "https://pip"}, -1, "out.mp4", []bool{false, true})
	cmd := strings.Join(args, " ")
	if !strings.Contains(cmd, "[1:a]apad,atrim=duration=3") || !strings.Contains(cmd, "adelay=1000|1000") {
		t.Fatalf("explicit overlay source_audio keep should mix timed overlay audio: %s", cmd)
	}
	if !strings.Contains(cmd, "colorchannelmixer=aa=0.5") {
		t.Fatalf("layout opacity should apply to visual overlay: %s", cmd)
	}
	if !strings.Contains(cmd, "overlay=x=924:y=504:enable='between(t\\,1\\,4)'") {
		t.Fatalf("layout anchor/margin/width should position bottom-right: %s", cmd)
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
	if !strings.Contains(cmd, "fontfile='"+composerFontToken+"'") {
		t.Errorf("text overlay should use the bundled font token: %s", cmd)
	}
}

func TestBuildLocalFFmpegArgs_RichTextIgnoresUnavailableFontFamily(t *testing.T) {
	e, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"visual","clips":[{"asset":{"src":"https://a","type":"video"},"start":0,"length":3}]},
		{"type":"overlay","clips":[{"asset":{"type":"text","text":"Remote-safe","font":{"family":"Courier New","size":42}},"start":0,"length":2}]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}
	args := buildLocalFFmpegArgs(e, defaultOutput(), []string{"https://a"}, -1, "out.mp4")
	cmd := strings.Join(args, " ")
	if strings.Contains(cmd, "font='Courier New'") {
		t.Fatalf("font family must not require host Fontconfig: %s", cmd)
	}
	if !strings.Contains(cmd, "fontfile='"+composerFontToken+"'") {
		t.Fatalf("rich text should use the bundled font token: %s", cmd)
	}
	materialized := strings.Join(materializeComposerFontArgs(args, "./"+composerFontFilename), " ")
	if strings.Contains(materialized, composerFontToken) || !strings.Contains(materialized, "fontfile='./"+composerFontFilename+"'") {
		t.Fatalf("font token was not materialized: %s", materialized)
	}
}

func TestRenderFontEndpointServesBundledTTF(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/render-font", nil)
	rec := httptest.NewRecorder()
	(&App{}).handleRenderFont(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "font/ttf" {
		t.Fatalf("status=%d content-type=%q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	body := rec.Body.Bytes()
	if len(body) < 4 || body[0] != 0 || body[1] != 1 || body[2] != 0 || body[3] != 0 {
		t.Fatalf("response is not a TrueType font: len=%d prefix=%v", len(body), body[:min(4, len(body))])
	}
}

func TestBundledFontRendersWithFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	fontPath, err := writeComposerFont(dir)
	if err != nil {
		t.Fatal(err)
	}
	filter := materializeComposerFontArgs([]string{buildDrawText(&TextOver{
		Body: "Remote-safe text", FontSize: 32, Color: "white", Position: "center",
	}, 320, 180)}, fontPath)[0]
	cmd := exec.Command(ffmpeg,
		"-v", "error", "-f", "lavfi", "-i", "color=c=black:s=320x180:d=0.1",
		"-vf", filter, "-frames:v", "1", "-f", "null", "-",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg rejected bundled font filter: %v\n%s\nfilter=%s", err, output, filter)
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
	if !strings.Contains(cmd, "[vbase]drawtext") || !strings.Contains(cmd, "[vtxt0];[vtxt0]null[vout]") {
		t.Fatalf("text overlay should apply after visual concat: %s", cmd)
	}
}

func TestBuildLocalFFmpegArgs_TypewriterOverlayTrack(t *testing.T) {
	e, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"visual","clips":[{"asset":{"src":"https://a","type":"video"},"start":0,"length":6}]},
		{"type":"overlay","clips":[{
			"asset":{"type":"text","text":"Hi","font":{"size":40,"color":"#ffffff"}},
			"start":1,"length":3,
			"animation":{"in":{"preset":"typewriter","duration":1,"style":"character"},"out":{"preset":"fade","duration":0.4}}
		}]}
	]}}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	args := buildLocalFFmpegArgs(e, defaultOutput(), []string{"https://a"}, -1, "out.mp4")
	cmd := strings.Join(args, " ")
	if strings.Count(cmd, "drawtext=text=") < 2 {
		t.Fatalf("typewriter should emit multiple reveal drawtext filters: %s", cmd)
	}
	if !strings.Contains(cmd, "drawtext=text='H '") {
		t.Fatalf("first reveal step should pad unrevealed characters to keep position stable: %s", cmd)
	}
	if !strings.Contains(cmd, "drawtext=text='Hi'") {
		t.Fatalf("final reveal step should contain the complete text: %s", cmd)
	}
	if !strings.Contains(cmd, "enable='between(t\\,1\\,1.5)'") || !strings.Contains(cmd, "enable='between(t\\,1.5\\,4)'") {
		t.Fatalf("typewriter reveal steps should be time-gated: %s", cmd)
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

func TestSyncClipDurationFromAI_KeepStartPreservesReservedLength(t *testing.T) {
	c := Clip{
		Asset:           Asset{Type: "video", Src: "storage:1"},
		Start:           0,
		Length:          30,
		EstimatedLength: 30,
		DurationMode:    "fit_generated_keep_start",
		AI: &AIAsset{
			MediaKind:                "avatar",
			Prompt:                   "hello",
			ActualDurationSeconds:    22.099,
			EstimatedDurationSeconds: 30,
		},
	}
	if !syncClipDurationFromAI(&c) {
		t.Fatal("expected actual duration metadata to sync")
	}
	if c.Length != 30 || c.ActualLength != 22.099 || c.EstimatedLength != 30 {
		t.Fatalf("reserved slot collapsed: %+v", c)
	}
}

func TestBuildLocalFFmpegArgs_ShortReservedAvatarWithLaterPIP(t *testing.T) {
	e, err := parseEditJSON(`{"timeline":{"tracks":[
		{"type":"visual","clips":[
			{"uid":"avatar","asset":{"src":"https://avatar","type":"video"},"start":0,"length":5,"actual_length":2,"duration_mode":"fit_generated_keep_start","source_audio":"keep","ai":{"media_kind":"avatar","prompt":"intro","actual_duration_seconds":2}},
			{"uid":"screen","asset":{"src":"https://screen","type":"image"},"start":5,"length":5}
		]},
		{"type":"visual","clips":[
			{"uid":"pip","asset":{"src":"https://pip","type":"video"},"start":5,"length":3,"position":"bottomRight","width":320,"height":180,"source_audio":"mute"}
		]}
	]}}`)
	if err != nil {
		t.Fatal(err)
	}
	avatar := &e.Timeline.Tracks[0].Clips[0]
	avatar.ActualLength = 0
	if !syncClipDurationFromAI(avatar) {
		t.Fatal("expected avatar actual length to sync")
	}
	if avatar.Length != 5 {
		t.Fatalf("avatar reserved slot = %v, want 5", avatar.Length)
	}
	args := buildLocalFFmpegArgsWithAudioInfo(e, defaultOutput(), []string{"https://avatar", "https://screen", "https://pip"}, -1, "out.mp4", []bool{true, false, false})
	cmd := strings.Join(args, " ")
	if !strings.Contains(cmd, "tpad=stop_mode=clone:stop_duration=5,trim=duration=5") {
		t.Fatalf("short avatar must pad inside its five-second slot: %s", cmd)
	}
	if !strings.Contains(cmd, "overlay=x=960:y=540:enable='between(t\\,5\\,8)'") {
		t.Fatalf("later PIP must remain at absolute start=5: %s", cmd)
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

func TestAICacheKey_IncludesSourceImages(t *testing.T) {
	base := &AIAsset{MediaKind: "video", Prompt: "try-on", Model: "seedance-2-0-mini-enhanced-reference-to-video", SourceImages: []string{"storage:1", "storage:2"}}
	other := *base
	other.SourceImages = []string{"storage:1", "storage:3"}
	if aiCacheKey(base) == aiCacheKey(&other) {
		t.Fatal("cache key should change when AI source images change")
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
		{"uid":"a","asset":{"type":"audio","src":"storage:1"},"start":0,"length":5,"ai":{"media_kind":"audio_tts","prompt":"First part.","voice":"voice-1","model":"eleven_multilingual_v2","provider_request_id":"req-a","storage_id":1,"status":"ready"}},
		{"uid":"gap","asset":{"type":"silence","src":""},"start":5,"length":1},
		{"uid":"b","asset":{"type":"audio","src":""},"start":6,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Second part.","voice":"voice-1","model":"eleven_multilingual_v2"}},
		{"uid":"c","asset":{"type":"audio","src":"storage:3"},"start":11,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Third part.","voice":"voice-1","model":"eleven_multilingual_v2","provider_request_id":"req-c","storage_id":3,"status":"ready"}}
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

func TestTTSContinuityOptions_RequestIDsCrossSFX(t *testing.T) {
	edit := `{"timeline":{"tracks":[{"type":"audio","clips":[
		{"uid":"a","asset":{"type":"audio","src":"storage:1"},"start":0,"length":5,"ai":{"media_kind":"audio_tts","prompt":"First.","voice":"voice-1","model":"eleven_multilingual_v2","provider_request_id":"req-a","storage_id":1,"status":"ready"}},
		{"uid":"snap","asset":{"type":"audio","src":"storage:2"},"start":5,"length":1,"ai":{"media_kind":"audio_sfx","prompt":"finger snap","storage_id":2,"status":"ready"}},
		{"uid":"b","asset":{"type":"audio","src":""},"start":6,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Second.","voice":"voice-1","model":"eleven_multilingual_v2"}}
	]}]}}`
	got, err := parseEditJSON(edit)
	if err != nil {
		t.Fatal(err)
	}
	opts := ttsContinuityOptions(&got.Timeline.Tracks[0], 2)
	ids, _ := opts["previous_request_ids"].([]string)
	if len(ids) != 1 || ids[0] != "req-a" {
		t.Fatalf("previous_request_ids = %+v, want req-a across SFX", ids)
	}
}

func TestTTSContinuityOptions_UsesTimelineOrder(t *testing.T) {
	edit := `{"timeline":{"tracks":[{"type":"audio","clips":[
		{"uid":"late","asset":{"type":"audio","src":""},"start":10,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Third.","voice":"voice-1"}},
		{"uid":"early","asset":{"type":"audio","src":""},"start":0,"length":5,"ai":{"media_kind":"audio_tts","prompt":"First.","voice":"voice-1","model":"eleven_multilingual_v2"}},
		{"uid":"middle","asset":{"type":"audio","src":""},"start":5,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Second.","voice":"voice-1"}}
	]}]}}`
	got, err := parseEditJSON(edit)
	if err != nil {
		t.Fatal(err)
	}
	opts := ttsContinuityOptions(&got.Timeline.Tracks[0], 2)
	if opts["previous_text"] != "First." || opts["next_text"] != "Third." {
		t.Fatalf("timeline context = %+v, want First/Third", opts)
	}
}

func TestTTSContinuityOptions_MissingImmediateRequestIDFallsBackToText(t *testing.T) {
	edit := `{"timeline":{"tracks":[{"type":"audio","clips":[
		{"uid":"a","asset":{"type":"audio","src":"storage:1"},"start":0,"length":5,"ai":{"media_kind":"audio_tts","prompt":"First.","voice":"voice-1","provider_request_id":"req-a","storage_id":1,"status":"ready"}},
		{"uid":"b","asset":{"type":"audio","src":"storage:2"},"start":5,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Second.","voice":"voice-1","storage_id":2,"status":"ready"}},
		{"uid":"c","asset":{"type":"audio","src":""},"start":10,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Third.","voice":"voice-1"}}
	]}]}}`
	got, err := parseEditJSON(edit)
	if err != nil {
		t.Fatal(err)
	}
	opts := ttsContinuityOptions(&got.Timeline.Tracks[0], 2)
	if _, exists := opts["previous_request_ids"]; exists {
		t.Fatalf("stale non-contiguous request IDs should not be used: %+v", opts)
	}
	if opts["previous_text"] != "Second." {
		t.Fatalf("previous_text = %v, want immediate Second.", opts["previous_text"])
	}
}

func TestTTSContinuityCacheContextIgnoresProviderRequestIDs(t *testing.T) {
	edit := `{"timeline":{"tracks":[{"type":"audio","clips":[
		{"uid":"a","asset":{"type":"audio","src":""},"start":0,"length":5,"ai":{"media_kind":"audio_tts","prompt":"First.","voice":"voice-1"}},
		{"uid":"b","asset":{"type":"audio","src":""},"start":5,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Second.","voice":"voice-1"}},
		{"uid":"c","asset":{"type":"audio","src":""},"start":10,"length":5,"ai":{"media_kind":"audio_tts","prompt":"Third.","voice":"voice-1"}}
	]}]}}`
	got, err := parseEditJSON(edit)
	if err != nil {
		t.Fatal(err)
	}
	track := &got.Timeline.Tracks[0]
	before := planTTSContinuity(track, 1)
	beforeKey := aiCacheKeyWithOptions(track.Clips[1].AI, before.CacheOptions)
	track.Clips[0].AI.ProviderRequestID = "req-a"
	track.Clips[0].AI.StorageID = 1
	track.Clips[0].AI.Status = "ready"
	track.Clips[2].AI.ProviderRequestID = "req-c"
	track.Clips[2].AI.StorageID = 3
	track.Clips[2].AI.Status = "ready"
	after := planTTSContinuity(track, 1)
	afterKey := aiCacheKeyWithOptions(track.Clips[1].AI, after.CacheOptions)
	if beforeKey != afterKey {
		t.Fatalf("request IDs changed stable cache key: %s != %s", beforeKey, afterKey)
	}
	if _, ok := after.ProviderOptions["previous_request_ids"]; !ok {
		t.Fatalf("provider request IDs missing after neighbors became ready: %+v", after.ProviderOptions)
	}
}

func TestApplyDefaultAIOptions_MergesPartialVoiceSettings(t *testing.T) {
	ai := &AIAsset{MediaKind: "audio_tts", Options: map[string]any{
		"voice_settings": map[string]any{"stability": 0.9},
	}}
	applyDefaultAIOptions(ai)
	settings := ai.Options["voice_settings"].(map[string]any)
	if settings["stability"] != 0.9 || settings["similarity_boost"] != 0.95 || settings["style"] != 0 || settings["use_speaker_boost"] != true {
		t.Fatalf("partial settings were not completed correctly: %+v", settings)
	}
}

func TestDefaultGeneratedAudioFX_NormalizesTTSWithoutTrimming(t *testing.T) {
	clip := Clip{AI: &AIAsset{MediaKind: "audio_tts"}}
	defaultGeneratedAudioFX(&clip)
	if clip.Audio == nil || !clip.Audio.Normalize || clip.Audio.LoudnessTarget != -16 || clip.Audio.PeakLimitDB != -2 {
		t.Fatalf("TTS normalization defaults missing: %+v", clip.Audio)
	}
	if clip.Audio.TrimSilence {
		t.Fatal("TTS default must preserve natural leading/trailing silence")
	}
}

func TestResetAIGeneratedStateClearsProviderMetadata(t *testing.T) {
	ai := &AIAsset{StorageID: 1, GenerationID: 2, ProviderRequestID: "old", JobID: 3, Status: "ready", ActualDurationSeconds: 4, AudioAnalysis: &AudioAnalysis{}, PeakDB: -1, RMSDB: -10, Error: "old"}
	resetAIGeneratedState(ai)
	if ai.StorageID != 0 || ai.GenerationID != 0 || ai.ProviderRequestID != "" || ai.JobID != 0 || ai.Status != "draft" || ai.ActualDurationSeconds != 0 || ai.AudioAnalysis != nil || ai.Error != "" {
		t.Fatalf("generated state not fully cleared: %+v", ai)
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

func TestEnrichEditJSONWithMediaHistory_RestoresSourceImages(t *testing.T) {
	edit := `{"timeline":{"tracks":[{"type":"visual","clips":[{"asset":{"type":"video","src":"storage:42"},"start":0,"length":4}]}]}}`
	out := enrichEditJSONWithMediaHistory(edit, map[int64]*mediaHistoryRow{
		42: {
			ID:         7,
			Kind:       "video",
			Prompt:     "try-on",
			Model:      "seedance-2-0-mini-enhanced-reference-to-video",
			StorageIDs: []int64{42},
			Status:     "complete",
			ExtraJSON:  `{"source_image_refs":["storage:10","storage:11"]}`,
		},
	})
	var got Edit
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	ai := got.Timeline.Tracks[0].Clips[0].AI
	if ai == nil || len(ai.SourceImages) != 2 || ai.SourceImages[0] != "storage:10" || ai.SourceImages[1] != "storage:11" {
		t.Fatalf("expected enriched source images, got %+v", ai)
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

func TestAsyncRenderCreatesDurableQueuedRow(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml").WithProject("project-live")
	edit := `{"timeline":{"tracks":[{"type":"visual","clips":[{"uid":"hero","asset":{"type":"image","src":"https://example.test/hero.jpg"},"start":0,"length":5}]}]}}`
	output := `{"format":"mp4","resolution":"hd","aspect":"16:9","fps":30}`
	res, err := ctx.AppDB().Exec(
		`INSERT INTO compositions(project_id,name,edit_json,output_json,duration_seconds) VALUES(?,?,?,?,?)`,
		"project-live", "Live card test", edit, output, 5,
	)
	if err != nil {
		t.Fatal(err)
	}
	compositionID, _ := res.LastInsertId()

	got, err := (&App{}).toolCompositionRender(ctx, map[string]any{"id": compositionID, "wait": false})
	if err != nil {
		t.Fatal(err)
	}
	row := got.(map[string]any)
	renderID, _ := row["render_id"].(int64)
	if renderID == 0 || row["status"] != "queued" {
		t.Fatalf("unexpected result: %#v", row)
	}

	var status, phase, editSnapshot, outputSnapshot string
	if err := ctx.AppDB().QueryRow(
		`SELECT status,phase,edit_snapshot,output_snapshot FROM renders WHERE id=?`, renderID,
	).Scan(&status, &phase, &editSnapshot, &outputSnapshot); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || phase != "queued" || editSnapshot != edit || outputSnapshot != output {
		t.Fatalf("durable row mismatch: status=%s phase=%s edit=%q output=%q", status, phase, editSnapshot, outputSnapshot)
	}
}

func TestCompositionAndRenderCardDataAreProjectScoped(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml").WithProject("project-card")
	edit := `{"timeline":{"tracks":[{"type":"visual","clips":[{"uid":"hero","asset":{"type":"image","src":"storage:1"},"start":0,"length":5,"ai":{"media_kind":"image","prompt":"hero","status":"ready","storage_id":1}}]},{"type":"audio","clips":[{"uid":"pause","asset":{"type":"silence","src":""},"start":1,"length":2}]}]}}`
	output := `{"format":"mp4","resolution":"hd","aspect":"16:9","fps":30}`
	res, err := ctx.AppDB().Exec(`INSERT INTO compositions(project_id,name,edit_json,output_json,duration_seconds) VALUES(?,?,?,?,?)`, "project-card", "Card test", edit, output, 5)
	if err != nil {
		t.Fatal(err)
	}
	compositionID, _ := res.LastInsertId()
	renderID, err := createRenderRow(ctx, compositionID, "project-card", "auto", edit, output, "queued", "queued")
	if err != nil {
		t.Fatal(err)
	}

	composition, err := compositionCardData(ctx, compositionID, "project-card")
	if err != nil {
		t.Fatal(err)
	}
	counts := composition["counts"].(map[string]int)
	if counts["visual"] != 1 || counts["silence"] != 1 || counts["ai"] != 1 {
		t.Fatalf("bad counts: %#v", counts)
	}
	if _, err := compositionCardData(ctx, compositionID, "other-project"); err == nil {
		t.Fatal("cross-project composition lookup succeeded")
	}
	render, err := renderCardData(ctx, renderID, "project-card")
	if err != nil {
		t.Fatal(err)
	}
	if render["composition_name"] != "Card test" || render["status"] != "queued" {
		t.Fatalf("bad render card: %#v", render)
	}
	if _, err := renderCardData(ctx, renderID, "other-project"); err == nil {
		t.Fatal("cross-project render lookup succeeded")
	}
}

func TestRecoverInterruptedComposerRenders(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml").WithProject("project-recovery")
	res, err := ctx.AppDB().Exec(`INSERT INTO compositions(project_id,name,edit_json,output_json,duration_seconds) VALUES(?,?,?,?,0)`, "project-recovery", "Recovery", `{"timeline":{"tracks":[]}}`, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	compositionID, _ := res.LastInsertId()
	renderID, err := createRenderRow(ctx, compositionID, "project-recovery", "auto", `{}`, `{}`, "rendering", "uploading")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := recoverInterruptedComposerRenders(ctx.AppDB()); err != nil || n != 1 {
		t.Fatalf("recover = %d, %v", n, err)
	}
	var status, phase string
	if err := ctx.AppDB().QueryRow(`SELECT status,phase FROM renders WHERE id=?`, renderID).Scan(&status, &phase); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || phase != "queued" {
		t.Fatalf("status=%s phase=%s", status, phase)
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

func TestV2ValidateAndConvertScenes(t *testing.T) {
	body := `{
		"version":"composer/v2",
		"output":{"format":"mp4","width":1920,"height":1080,"fps":30,"background":"#050505"},
		"assets":[
			{"id":"a","type":"image","src":"storage:1"},
			{"id":"b","type":"image","src":"storage:2"},
			{"id":"music","type":"audio","src":"storage:3"}
		],
		"scenes":[
			{"duration":4,"elements":[{"type":"image","asset":"a"},{"type":"text","text":"Open","style":{"position":"center","font_size":48}}]},
			{"duration":6,"elements":[{"type":"image","asset":"b"},{"type":"text","text":"Proof","style":{"position":"bottom"}}]}
		],
		"audio":[{"asset":"music","volume":0.2}]
	}`
	spec, err := parseV2CompositionJSON(body)
	if err != nil {
		t.Fatalf("parse v2: %v", err)
	}
	edit, output, warnings, err := v2ToV1FFmpeg(spec)
	if err != nil {
		t.Fatalf("convert v2: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if output.Resolution != "fullhd" || output.Aspect != "16:9" {
		t.Fatalf("output = %+v, want fullhd 16:9", output)
	}
	if got := editDurationSeconds(edit); got != 10 {
		t.Fatalf("duration = %v, want 10", got)
	}
	if edit.Timeline.Soundtrack == nil || edit.Timeline.Soundtrack.Src != "storage:3" {
		t.Fatalf("soundtrack not converted: %+v", edit.Timeline.Soundtrack)
	}
	if edit.Timeline.Tracks[0].Clips[0].Text == nil || edit.Timeline.Tracks[0].Clips[0].Text.Position != "center" {
		t.Fatalf("text overlay not converted: %+v", edit.Timeline.Tracks[0].Clips[0].Text)
	}
}

func TestV2ValidationMarksAdvancedSceneAsNativeV2(t *testing.T) {
	body := `{
		"version":"composer/v2",
		"output":{"format":"mp4","width":1920,"height":1080,"fps":30},
		"scenes":[{"duration":5,"elements":[
			{"type":"shape","style":{"fill":"#111111"}},
			{"type":"text","text":"Title","enter":{"type":"typewriter","duration":1}}
		]}]
	}`
	validation := validateCompositionJSON(body)
	if !validation.Valid {
		t.Fatalf("expected v2 valid, got errors: %v", validation.Errors)
	}
	if validation.Renderer != "native-v2" {
		t.Fatalf("renderer = %q, want native-v2", validation.Renderer)
	}
	if len(validation.Warnings) == 0 {
		t.Fatalf("expected a warning explaining native-v2 support level")
	}
}

func TestV2ValidationMarksBrowserRenderer(t *testing.T) {
	body := `{
		"version":"composer/v2",
		"output":{"format":"mp4","renderer":"browser","width":1920,"height":1080,"fps":24},
		"scenes":[{"duration":3,"elements":[
			{"type":"component","component":"browser_window","x":"20%","y":"20%","width":"60%","height":"30%","meta":{"body":"Browser-rendered layout"}},
			{"type":"component","component":"loop_pattern","x":"0%","y":"0%","width":"100%","height":"100%"}
		]}]
	}`
	validation := validateCompositionJSON(body)
	if !validation.Valid {
		t.Fatalf("expected v2 valid, got errors: %v", validation.Errors)
	}
	if validation.Renderer != "browser-v2" {
		t.Fatalf("renderer = %q, want browser-v2", validation.Renderer)
	}
	if len(validation.Warnings) == 0 || !strings.Contains(validation.Warnings[0], "CSS scene graphs") {
		t.Fatalf("expected browser-v2 capability warning, got %v", validation.Warnings)
	}
}

func TestV2ParsePreservesComponentField(t *testing.T) {
	spec, err := parseV2CompositionJSON(`{
		"version":"composer/v2",
		"output":{"format":"mp4","renderer":"browser","width":640,"height":360,"fps":24},
		"scenes":[{"duration":1,"elements":[
			{"id":"phone","type":"component","component":"phone","meta":{"body":"Continue on mobile"}}
		]}]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := spec.Scenes[0].Elements[0].Component; got != "phone" {
		t.Fatalf("component = %q, want phone", got)
	}
}

func TestV2PublicSurfaceDisabledByDefault(t *testing.T) {
	t.Setenv("COMPOSER_V2_ENABLED", "0")
	args := map[string]any{
		"spec": map[string]any{
			"version": "composer/v2",
			"output":  map[string]any{"format": "mp4", "width": 640, "height": 360, "fps": 24},
			"scenes": []any{
				map[string]any{"duration": 1, "elements": []any{
					map[string]any{"type": "text", "text": "Hidden"},
				}},
			},
		},
	}
	_, _, _, _, ok, err := compositionPayloadFromV2Args(args)
	if !ok {
		t.Fatal("expected v2 payload to be detected")
	}
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled error, got %v", err)
	}
	out, err := (&App{}).toolCompositionValidate(nil, args)
	if err != nil {
		t.Fatal(err)
	}
	validation := out.(CompositionValidation)
	if validation.Valid || validation.Renderer != "disabled" {
		t.Fatalf("validation = %+v, want disabled invalid result", validation)
	}
	examplesOut, err := (&App{}).toolCompositionExamples(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	examples := examplesOut.(map[string]any)["examples"].([]map[string]any)
	if len(examples) != 0 {
		t.Fatalf("public examples should be hidden, got %d", len(examples))
	}
}

func TestV2NativeRenderShapeAndText(t *testing.T) {
	if _, err := exec.LookPath(ffmpegPath()); err != nil {
		t.Skip("ffmpeg not available")
	}
	if _, err := exec.LookPath(ffprobePath()); err != nil {
		t.Skip("ffprobe not available")
	}
	spec, err := parseV2CompositionJSON(`{
		"version":"composer/v2",
		"name":"Native vector render",
		"output":{"format":"mp4","width":320,"height":180,"fps":24,"background":"#05070c"},
		"scenes":[{
			"duration":1,
			"elements":[
				{"type":"shape","x":"8%","y":"16%","width":"84%","height":"68%","style":{"fill":"#101827","stroke":"#ff7a1a","stroke_width":2,"radius":18},"enter":{"type":"scale_pop","duration":0.2}},
				{"type":"text","text":"Native V2","x":"12%","y":"28%","width":"76%","height":"28%","style":{"font_size":34,"color":"#ffffff","weight":800,"align":"center"},"enter":{"type":"fade_up","duration":0.25}},
				{"type":"text","text":"shape + text + animation","x":"12%","y":"58%","width":"76%","height":"18%","style":{"font_size":17,"color":"#ffb36b","align":"center"},"enter":{"type":"typewriter","duration":0.5}}
			]
		}]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	result, warnings, err := renderV2Native(context.Background(), nil, spec, "")
	if err != nil {
		t.Fatalf("renderV2Native: %v", err)
	}
	defer result.Cleanup()
	if result.LocalPath == "" {
		t.Fatal("missing render path")
	}
	if got := probeRenderDuration(result.LocalPath); got < 0.8 || got > 1.4 {
		t.Fatalf("duration = %v, want about 1s; warnings=%v", got, warnings)
	}
}

func TestV2RevealTextLinesHonorsDelayAndKeepsLayoutStable(t *testing.T) {
	lines := []string{"Applications", "open now"}
	before := revealTextLines(lines, -0.1, 1, "typewriter")
	if len(before) != len(lines) {
		t.Fatalf("line count before reveal = %d, want %d", len(before), len(lines))
	}
	for _, line := range before {
		if line != "" {
			t.Fatalf("text revealed before delay: %#v", before)
		}
	}
	during := revealTextLines(lines, 0.25, 1, "typewriter")
	if len(during) != len(lines) {
		t.Fatalf("line count during reveal = %d, want %d", len(during), len(lines))
	}
	if during[0] == "" {
		t.Fatalf("expected first line to reveal, got %#v", during)
	}
	if during[1] != "" {
		t.Fatalf("second line should remain blank early in reveal, got %#v", during)
	}
	after := revealTextLines(lines, 1.2, 1, "typewriter")
	if strings.Join(after, "\x00") != strings.Join(lines, "\x00") {
		t.Fatalf("after reveal = %#v, want %#v", after, lines)
	}
}

func TestV2ParseRGBAUsesPremultipliedAlpha(t *testing.T) {
	got := parseColor("rgba(255,255,255,0.5)", color.RGBA{})
	if got.A < 126 || got.A > 128 {
		t.Fatalf("alpha = %d, want about 127", got.A)
	}
	if got.R > got.A || got.G > got.A || got.B > got.A {
		t.Fatalf("RGBA should be premultiplied, got %+v", got)
	}
}

func TestV2NativeRenderScalesDesignCoordinates(t *testing.T) {
	r := &v2NativeRender{width: 960, height: 540, designW: 1920, designH: 1080, scaleX: 0.5, scaleY: 0.5, scale: 0.5}
	box := r.elementBox(V2Element{
		X:      float64(200),
		Y:      float64(100),
		Width:  float64(400),
		Height: float64(220),
	})
	want := image.Rect(100, 50, 300, 160)
	if box != want {
		t.Fatalf("scaled box = %v, want %v", box, want)
	}
	if got := parseMeasure("80px", 1000, 0); got != 80 {
		t.Fatalf("plain parseMeasure should remain unscaled, got %v", got)
	}
}

func TestV2NativeRenderParentMotionAppliesAroundParent(t *testing.T) {
	r := &v2NativeRender{width: 1000, height: 1000, designW: 1000, designH: 1000, scaleX: 1, scaleY: 1, scale: 1}
	parent := V2Element{
		ID:       "card",
		Type:     "group",
		X:        float64(100),
		Y:        float64(100),
		Width:    float64(300),
		Height:   float64(160),
		Duration: 1,
		Enter:    map[string]any{"type": "slide_left", "duration": 1.0},
	}
	child := V2Element{
		ID:       "label",
		Parent:   "card",
		Type:     "text",
		X:        float64(130),
		Y:        float64(130),
		Width:    float64(120),
		Height:   float64(40),
		Duration: 1,
	}
	base := r.elementBox(child)
	parentBox := r.elementBox(parent)
	motion := r.elementMotion(parent, 0.5, 1)
	moved := transformBoxAround(base, parentBox, motion.xOff, motion.yOff, motion.scale)
	if moved.Min.X <= base.Min.X {
		t.Fatalf("child did not inherit parent slide motion: base=%v moved=%v motion=%+v", base, moved, motion)
	}
	if motion.opacity <= 0 || motion.opacity >= 1 {
		t.Fatalf("expected parent enter opacity to be mid-animation, got %+v", motion)
	}
}

func TestV2NativeRenderTextScaleTracksElementScale(t *testing.T) {
	r := &v2NativeRender{width: 1000, height: 1000, designW: 1000, designH: 1000, scaleX: 1, scaleY: 1, scale: 1}
	el := V2Element{
		Type:     "text",
		X:        float64(100),
		Y:        float64(100),
		Width:    float64(400),
		Height:   float64(120),
		Duration: 1,
		Style:    map[string]any{"font_size": float64(48)},
		Enter:    map[string]any{"type": "zoom_in", "duration": 1.0},
	}
	state := r.elementState(el, r.elementBox(el), 0.5, 1)
	if state.scale <= 0.94 || state.scale >= 1 {
		t.Fatalf("expected zoom_in to expose mid-animation scale, got %+v", state)
	}
}

func TestV2NativeRenderFitTextShrinksTightBoxes(t *testing.T) {
	r := &v2NativeRender{width: 400, height: 300, designW: 400, designH: 300, scaleX: 1, scaleY: 1, scale: 1, faces: map[string]font.Face{}}
	box := image.Rect(0, 0, 180, 44)
	size, _, lines := r.fitText("This label is deliberately too long for the card", true, 34, box)
	if size >= 34 {
		t.Fatalf("expected fitText to shrink, got size=%v lines=%v", size, lines)
	}
	if size < 8 {
		t.Fatalf("fitText shrank below the minimum, got %v", size)
	}
}

func TestV2AudioOnlyConvertsToAudioTrack(t *testing.T) {
	body := `{
		"version":"composer/v2",
		"output":{"format":"mp3"},
		"assets":[
			{"id":"voice-1","type":"audio","src":"storage:10"},
			{"id":"voice-2","type":"audio","src":"storage:11"}
		],
		"audio":[
			{"id":"a","asset":"voice-1","start":0,"duration":8,"volume":1},
			{"id":"b","asset":"voice-2","start":10,"duration":6,"volume":1}
		]
	}`
	spec, err := parseV2CompositionJSON(body)
	if err != nil {
		t.Fatalf("parse v2 audio: %v", err)
	}
	edit, output, _, err := v2ToV1FFmpeg(spec)
	if err != nil {
		t.Fatalf("convert audio-only v2: %v", err)
	}
	if output.Format != "mp3" {
		t.Fatalf("format = %q, want mp3", output.Format)
	}
	if len(edit.Timeline.Tracks) != 1 || edit.Timeline.Tracks[0].Type != "audio" {
		t.Fatalf("expected one audio track, got %+v", edit.Timeline.Tracks)
	}
	if len(edit.Timeline.Tracks[0].Clips) != 2 {
		t.Fatalf("expected two audio clips, got %+v", edit.Timeline.Tracks[0].Clips)
	}
}

func TestV2SpecFromArgs(t *testing.T) {
	t.Setenv("COMPOSER_V2_ENABLED", "1")
	args := map[string]any{
		"name": "V2 Example",
		"spec": map[string]any{
			"version": "composer/v2",
			"output":  map[string]any{"format": "mp4", "width": 1080, "height": 1920, "fps": 30},
			"assets": []any{
				map[string]any{"id": "v", "type": "video", "src": "storage:9"},
			},
			"tracks": []any{
				map[string]any{"type": "video", "clips": []any{
					map[string]any{"asset": "v", "duration": 4},
				}},
			},
		},
	}
	editJSON, outputJSON, duration, version, ok, err := compositionPayloadFromV2Args(args)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if !ok {
		t.Fatal("expected v2 payload")
	}
	if version != composerV2Version {
		t.Fatalf("version = %q", version)
	}
	if duration != 4 {
		t.Fatalf("duration = %v, want 4", duration)
	}
	if !isV2EditJSON(editJSON) {
		t.Fatalf("stored edit is not v2: %s", editJSON)
	}
	var output Output
	if err := json.Unmarshal([]byte(outputJSON), &output); err != nil {
		t.Fatal(err)
	}
	if output.Aspect != "9:16" {
		t.Fatalf("aspect = %q, want 9:16", output.Aspect)
	}
}

func TestComposerV2ExamplesValidate(t *testing.T) {
	examples := composerV2Examples()
	if len(examples) < 3 {
		t.Fatalf("expected at least 3 examples, got %d", len(examples))
	}
	for _, ex := range examples {
		b, _ := json.Marshal(ex["spec"])
		validation := validateCompositionJSON(string(b))
		if !validation.Valid {
			t.Fatalf("example %v invalid: %v", ex["id"], validation.Errors)
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
		"https://agents.example.com/api/apps/composer/render-font?project_id=project-1",
	)
	for _, want := range []string{
		"$STORAGE_BASE/files/init?project_id=$PROJECT_ID",
		"$STORAGE_BASE/uploads?project_id=$PROJECT_ID",
		"$STORAGE_BASE/uploads/$CHUNK_UPLOAD_ID/parts/$PART?project_id=$PROJECT_ID",
		"$STORAGE_BASE/uploads/$CHUNK_UPLOAD_ID/complete?project_id=$PROJECT_ID",
		"composer-render",
		"Authorization: Bearer token-redacted",
		"-o ./composer-go-mono.ttf",
		"/api/apps/composer/render-font?project_id=project-1",
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
