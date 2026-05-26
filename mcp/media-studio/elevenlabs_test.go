package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestBuildElevenLabsTTSArgs(t *testing.T) {
	got, err := buildAudioTTSArgs(map[string]any{
		"prompt": "hello world",
		"voice":  "voice-123",
		"model":  "eleven_flash_v2_5",
		"options": map[string]any{
			"output_format":  "mp3_44100_128",
			"voice_settings": map[string]any{"stability": 0.7},
		},
	}, "elevenlabs", "audio.tts")
	if err != nil {
		t.Fatal(err)
	}
	if got["voice_id"] != "voice-123" || got["text"] != "hello world" || got["model_id"] != "eleven_flash_v2_5" {
		t.Fatalf("unexpected args: %+v", got)
	}
	if got["output_format"] != "mp3_44100_128" {
		t.Fatalf("output_format not passed through: %+v", got)
	}
	if _, ok := got["voice_settings"].(map[string]any); !ok {
		t.Fatalf("voice_settings missing: %+v", got)
	}
}

func TestBuildElevenLabsTTSArgsRequiresVoice(t *testing.T) {
	_, err := buildAudioTTSArgs(map[string]any{"prompt": "hello"}, "elevenlabs", "audio.tts")
	if err == nil || !strings.Contains(err.Error(), "voice required") {
		t.Fatalf("expected voice required error, got %v", err)
	}
}

func TestBuildElevenLabsSFXArgs(t *testing.T) {
	got, err := buildAudioSFXArgs(map[string]any{
		"prompt":   "rain on a tin roof",
		"duration": 4,
		"model":    "eleven_text_to_sound_v2",
		"options": map[string]any{
			"prompt_influence": 0.45,
			"loop":             true,
			"output_format":    "mp3_44100_128",
		},
	}, "elevenlabs", "audio.sfx")
	if err != nil {
		t.Fatal(err)
	}
	if got["text"] != "rain on a tin roof" || got["duration_seconds"] != 4 || got["model_id"] != "eleven_text_to_sound_v2" {
		t.Fatalf("unexpected args: %+v", got)
	}
	if got["loop"] != true || got["prompt_influence"] != 0.45 {
		t.Fatalf("options not passed through: %+v", got)
	}
}

func TestBuildElevenLabsMusicArgs(t *testing.T) {
	got, err := buildMusicArgs(map[string]any{
		"prompt":   "upbeat synth pop",
		"duration": 30,
		"model":    "music_v1",
		"options": map[string]any{
			"output_format": "mp3_44100_128",
		},
	}, "elevenlabs", "music.generate")
	if err != nil {
		t.Fatal(err)
	}
	if got["prompt"] != "upbeat synth pop" || got["music_length_ms"] != 30000 || got["model_id"] != "music_v1" {
		t.Fatalf("unexpected args: %+v", got)
	}
	if got["output_format"] != "mp3_44100_128" {
		t.Fatalf("output_format not passed through: %+v", got)
	}
}

func TestBuildElevenLabsMusicArgsCompositionPlan(t *testing.T) {
	plan := map[string]any{"sections": []any{"intro"}}
	got, err := buildMusicArgs(map[string]any{
		"prompt": "ignored when plan exists",
		"options": map[string]any{
			"composition_plan": plan,
		},
	}, "elevenlabs", "music.generate")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["prompt"]; ok {
		t.Fatalf("prompt should be omitted when composition_plan is set: %+v", got)
	}
	if got["composition_plan"] == nil {
		t.Fatalf("composition_plan missing: %+v", got)
	}
}

func TestNormalizeBinaryAudioResponse(t *testing.T) {
	media, _, _, err := normalizeAudioResponse("elevenlabs", "audio.tts", json.RawMessage(
		`{"_binary":true,"base64":"aGVsbG8=","mimeType":"audio/mpeg","size":5}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(media) != 1 || media[0].B64 != "aGVsbG8=" || media[0].MimeType != "audio/mpeg" || media[0].Ext != "mp3" {
		t.Fatalf("unexpected media: %+v", media)
	}
}

func TestToolMediaGenerate_ElevenLabsTTS_WithStorage(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "elevenlabs"
	pf.identity.Bindings = map[string]any{
		"audio_provider": float64(42),
		"storage":        float64(17),
	}
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"_binary":true,"base64":"aGVsbG8=","mimeType":"audio/mpeg","size":5}`),
	}
	pf.nextCallResult = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":5678,\"url\":\"/files/5678\"}"}]}}`,
	)
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":   "audio_tts",
		"prompt": "hello",
		"voice":  "voice-123",
		"model":  "eleven_flash_v2_5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "text_to_speech" {
		t.Fatalf("execute calls = %+v", pf.executeCalls)
	}
	if pf.executeCalls[0].Input["voice_id"] != "voice-123" {
		t.Fatalf("voice_id missing in provider args: %+v", pf.executeCalls[0].Input)
	}
	if len(pf.callAppCalls) != 1 || pf.callAppCalls[0].Tool != "files_upload" {
		t.Fatalf("storage calls = %+v", pf.callAppCalls)
	}
	if pf.callAppCalls[0].Input["folder"] != "/.generated/audio/" {
		t.Fatalf("storage folder = %v", pf.callAppCalls[0].Input["folder"])
	}
	res := out.(map[string]any)
	meta := res["_meta"].(map[string]any)
	if meta["kind"] != "audio_tts" || meta["provider"] != "elevenlabs" {
		t.Fatalf("unexpected meta: %+v", meta)
	}
}

func TestToolMediaGenerate_ElevenLabsSFX_UsesGenerateSFX(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "elevenlabs"
	pf.identity.Bindings = map[string]any{
		"audio_provider": float64(42),
	}
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"_binary":true,"base64":"aGVsbG8=","mimeType":"audio/mpeg","size":5}`),
	}
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	_, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":     "audio_sfx",
		"prompt":   "door creak",
		"duration": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "generate_sfx" {
		t.Fatalf("execute calls = %+v", pf.executeCalls)
	}
	if pf.executeCalls[0].Input["duration_seconds"] != 2 {
		t.Fatalf("duration not mapped: %+v", pf.executeCalls[0].Input)
	}
}

func TestToolMediaGenerate_ElevenLabsMusic_UsesGenerateMusic(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "elevenlabs"
	pf.identity.Bindings = map[string]any{
		"music_provider": float64(42),
	}
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"_binary":true,"base64":"aGVsbG8=","mimeType":"audio/mpeg","size":5}`),
	}
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	_, err := app.toolMediaGenerate(ctx, map[string]any{
		"kind":     "music",
		"prompt":   "lo-fi piano",
		"duration": 30,
		"model":    "music_v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "generate_music" {
		t.Fatalf("execute calls = %+v", pf.executeCalls)
	}
	if pf.executeCalls[0].Input["music_length_ms"] != 30000 {
		t.Fatalf("duration not mapped: %+v", pf.executeCalls[0].Input)
	}
}

func TestParseElevenLabsModelList(t *testing.T) {
	raw := json.RawMessage(`[
		{"model_id":"eleven_flash_v2_5","name":"Flash","can_do_text_to_speech":true},
		{"model_id":"eleven_text_to_sound_v2","name":"Sound","can_do_sound_effects":true},
		{"model_id":"music_v1","name":"Music","can_do_music":true}
	]`)
	if got := parseModelList("elevenlabs", KindAudioTTS, raw, 0, ""); len(got) != 1 || got[0].ID != "eleven_flash_v2_5" {
		t.Fatalf("tts models = %+v", got)
	}
	if got := parseModelList("elevenlabs", KindAudioSFX, raw, 0, ""); len(got) != 1 || got[0].ID != "eleven_text_to_sound_v2" {
		t.Fatalf("sfx models = %+v", got)
	}
	if got := parseModelList("elevenlabs", KindMusic, raw, 0, ""); len(got) != 1 || got[0].ID != "music_v1" {
		t.Fatalf("music models = %+v", got)
	}
}
