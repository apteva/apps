package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestBuildCartesiaTTSArgs(t *testing.T) {
	got, err := buildAudioTTSArgs(map[string]any{
		"prompt": "Hello from Cartesia",
		"model":  "sonic-3.5",
		"voice":  "cartesia-voice",
		"options": map[string]any{
			"output_format":     "wav",
			"language":          "en",
			"generation_config": map[string]any{"speed": 1.2},
		},
	}, "cartesia", "audio.tts")
	if err != nil {
		t.Fatal(err)
	}
	voice := got["voice"].(map[string]any)
	format := got["output_format"].(map[string]any)
	if got["model_id"] != "sonic-3.5" || got["transcript"] != "Hello from Cartesia" {
		t.Fatalf("required args = %+v", got)
	}
	if voice["mode"] != "id" || voice["id"] != "cartesia-voice" {
		t.Fatalf("voice = %+v", voice)
	}
	if format["container"] != "wav" || format["encoding"] != "pcm_s16le" {
		t.Fatalf("format = %+v", format)
	}
}

func TestToolMediaGenerateCartesiaTTS(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "cartesia"
	pf.identity.Bindings["audio_provider"] = float64(51)
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"_binary":true,"base64":"SUQzYXVkaW8=","mimeType":"audio/mpeg"}`),
	}
	pf.nextCallResult = json.RawMessage(`{"result":{"content":[{"type":"text","text":"{\"id\":7201}"}]}}`)
	ctx := newMediaStudioCtx(t, pf)

	out, err := (&App{}).toolMediaGenerate(ctx, map[string]any{
		"kind": "audio_tts", "prompt": "Hello", "model": "sonic-3.5", "voice": "voice-51",
		"options": map[string]any{"output_format": "mp3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := out.(map[string]any); result["isError"] == true {
		t.Fatalf("result = %+v", result)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "create_speech" {
		t.Fatalf("execute calls = %+v", pf.executeCalls)
	}
	voice := pf.executeCalls[0].Input["voice"].(map[string]any)
	if voice["mode"] != "id" || voice["id"] != "voice-51" {
		t.Fatalf("Cartesia voice = %+v", voice)
	}
}

func TestCreateCartesiaCloneAndListVoices(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "cartesia"
	pf.identity.Bindings["audio_provider"] = float64(51)
	pf.perExecuteResults = map[string]*sdk.ExecuteResult{
		"clone_voice": {
			Success: true, Status: 200,
			Data: json.RawMessage(`{"id":"cartesia-created","name":"Barcelona Narrator","language":"es"}`),
		},
		"list_voices": {
			Success: true, Status: 200,
			Data: json.RawMessage(`{"data":[{"id":"cartesia-created","name":"Barcelona Narrator","language":"es","gender":"feminine","preview_file_url":"https://audio.test/cartesia.mp3"}]}`),
		},
	}
	ctx := newMediaStudioCtx(t, pf)

	out, err := (&App{}).toolMediaVoiceCreate(ctx, map[string]any{
		"provider": "cartesia", "name": "Barcelona Narrator", "source_type": "audio",
		"source_audio": "data:audio/wav;base64,UklGRg==", "source_audio_filename": "sample.wav",
		"language": "es",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := out.(map[string]any); result["isError"] == true {
		t.Fatalf("result = %+v", result)
	}
	if len(pf.executeCalls) == 0 || pf.executeCalls[0].Tool != "clone_voice" {
		t.Fatalf("execute calls = %+v", pf.executeCalls)
	}
	if input := pf.executeCalls[0].Input; input["language"] != "es" || input["clip_filename"] != "sample.wav" {
		t.Fatalf("clone input = %+v", input)
	}
	var provider, providerID string
	if err := ctx.AppDB().QueryRow(`SELECT provider, provider_identity_id FROM media_identities LIMIT 1`).Scan(&provider, &providerID); err != nil {
		t.Fatal(err)
	}
	if provider != "cartesia" || providerID != "cartesia-created" {
		t.Fatalf("identity = %q %q", provider, providerID)
	}
	voices, err := listVoicesFor(ctx, &sdk.BoundIntegration{ConnectionID: 51, AppSlug: "cartesia"})
	if err != nil {
		t.Fatal(err)
	}
	if len(voices) != 1 || voices[0].ID != "cartesia-created" || voices[0].Preview == "" {
		t.Fatalf("voices = %+v", voices)
	}
}

func TestBuildAndNormalizeMiniMaxTTS(t *testing.T) {
	got, err := buildAudioTTSArgs(map[string]any{
		"prompt": "Hello from MiniMax",
		"model":  "speech-2.8-hd",
		"voice":  "minimax-voice",
		"options": map[string]any{
			"output_format": "wav",
			"voice_setting": map[string]any{"voice_id": "must-not-win", "speed": 1.1, "emotion": "calm"},
		},
	}, "minimax-audio", "audio.tts")
	if err != nil {
		t.Fatal(err)
	}
	voice := got["voice_setting"].(map[string]any)
	format := got["audio_setting"].(map[string]any)
	if got["output_format"] != "hex" || voice["voice_id"] != "minimax-voice" || voice["speed"] != 1.1 {
		t.Fatalf("MiniMax args = %+v", got)
	}
	if format["format"] != "wav" {
		t.Fatalf("audio setting = %+v", format)
	}

	media, _, _, err := normalizeAudioResponse("minimax-audio", "audio.tts", json.RawMessage(
		`{"data":{"audio":"494433617564696f","status":2},"extra_info":{"audio_length":1234,"audio_format":"mp3"},"base_resp":{"status_code":0,"status_msg":"success"}}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(media) != 1 || media[0].B64 != "SUQzYXVkaW8=" || media[0].MimeType != "audio/mpeg" || media[0].DurationMs != 1234 {
		t.Fatalf("media = %+v", media)
	}
}

func TestToolMediaGenerateMiniMaxTTS(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "minimax-audio"
	pf.identity.Bindings["audio_provider"] = float64(61)
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"data":{"audio":"494433617564696f","status":2},"extra_info":{"audio_length":1234,"audio_format":"mp3"},"base_resp":{"status_code":0,"status_msg":"success"}}`),
	}
	pf.nextCallResult = json.RawMessage(`{"result":{"content":[{"type":"text","text":"{\"id\":7301}"}]}}`)
	ctx := newMediaStudioCtx(t, pf)

	out, err := (&App{}).toolMediaGenerate(ctx, map[string]any{
		"kind": "audio_tts", "prompt": "Hello", "model": "speech-2.8-hd", "voice": "MiniMax001",
		"options": map[string]any{"output_format": "mp3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := out.(map[string]any); result["isError"] == true {
		t.Fatalf("result = %+v", result)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "text_to_speech" {
		t.Fatalf("execute calls = %+v", pf.executeCalls)
	}
	if len(pf.callAppCalls) != 1 || pf.callAppCalls[0].Input["content_type"] != "audio/mpeg" {
		t.Fatalf("storage calls = %+v", pf.callAppCalls)
	}
}

func TestCreateMiniMaxCloneUsesUploadThenClone(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "minimax-audio"
	pf.identity.Bindings["audio_provider"] = float64(61)
	pf.perExecuteResults = map[string]*sdk.ExecuteResult{
		"upload_file": {
			Success: true, Status: 200,
			Data: json.RawMessage(`{"file":{"file_id":9001,"filename":"sample.wav"},"base_resp":{"status_code":0,"status_msg":"success"}}`),
		},
		"clone_voice": {
			Success: true, Status: 200,
			Data: json.RawMessage(`{"demo_audio":"https://audio.test/demo.mp3","base_resp":{"status_code":0,"status_msg":"success"}}`),
		},
	}
	ctx := newMediaStudioCtx(t, pf)

	out, err := (&App{}).toolMediaVoiceCreate(ctx, map[string]any{
		"provider": "minimax-audio", "name": "MiniMax Narrator", "source_type": "audio",
		"source_audio": "data:audio/wav;base64,UklGRg==", "source_audio_filename": "sample.wav",
		"provider_voice_id": "MiniMaxNarrator01", "transcripts": []any{"Hello world."},
		"options": map[string]any{"need_noise_reduction": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := out.(map[string]any); result["isError"] == true {
		t.Fatalf("result = %+v", result)
	}
	if len(pf.executeCalls) != 2 || pf.executeCalls[0].Tool != "upload_file" || pf.executeCalls[1].Tool != "clone_voice" {
		t.Fatalf("execute calls = %+v", pf.executeCalls)
	}
	clone := pf.executeCalls[1].Input
	if clone["file_id"] != int64(9001) || clone["voice_id"] != "MiniMaxNarrator01" ||
		clone["text_validation"] != "Hello world." || clone["need_noise_reduction"] != true {
		t.Fatalf("clone input = %+v", clone)
	}
	meta := out.(map[string]any)["_meta"].(map[string]any)
	if meta["provider"] != "minimax-audio" || meta["provider_identity_id"] != "MiniMaxNarrator01" {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestCreateMiniMaxDesignedVoice(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "minimax-audio"
	pf.identity.Bindings["audio_provider"] = float64(61)
	pf.perExecuteResults = map[string]*sdk.ExecuteResult{
		"design_voice": {
			Success: true, Status: 200,
			Data: json.RawMessage(`{"voice_id":"ttv-voice-123","trial_audio":"494433617564696f","base_resp":{"status_code":0,"status_msg":"success"}}`),
		},
	}
	ctx := newMediaStudioCtx(t, pf)

	out, err := (&App{}).toolMediaVoiceCreate(ctx, map[string]any{
		"provider": "minimax-audio", "name": "Designed Narrator", "source_type": "prompt",
		"voice_description": "A warm, composed narrator with clear pacing and a polished studio tone.",
		"preview_text":      "Welcome to Barcelona.",
	})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["isError"] == true {
		t.Fatalf("result = %+v", result)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "design_voice" ||
		pf.executeCalls[0].Input["preview_text"] != "Welcome to Barcelona." {
		t.Fatalf("execute calls = %+v", pf.executeCalls)
	}
	meta := result["_meta"].(map[string]any)
	previews := meta["previews"].([]voicePreview)
	if meta["provider_identity_id"] != "ttv-voice-123" || len(previews) != 1 ||
		previews[0].AudioBase64 != "SUQzYXVkaW8=" {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestCartesiaAndMiniMaxProviderQualificationAndModels(t *testing.T) {
	pf := newRecordingPlatform()
	pf.identity.Bindings["audio_provider"] = map[string]any{
		"ids": []any{float64(51), float64(61)}, "default_id": float64(51),
	}
	pf.connectionSlugs = map[int64]string{51: "cartesia", 61: "minimax-audio"}
	ctx := newMediaStudioCtx(t, pf)
	args := map[string]any{"model": "minimax-audio:speech-2.8-hd", "voice": "minimax-audio:MiniMax001"}
	bound, err := selectBoundProvider(ctx, handlers[KindAudioTTS], args, "audio.tts")
	if err != nil {
		t.Fatal(err)
	}
	if bound == nil || bound.AppSlug != "minimax-audio" || args["model"] != "speech-2.8-hd" || args["voice"] != "MiniMax001" {
		t.Fatalf("bound=%+v args=%+v", bound, args)
	}
	models, providers, err := loadAudioModelsForAllProviders(ctx, KindAudioTTS, "audio.tts")
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 {
		t.Fatalf("providers = %+v", providers)
	}
	seen := map[string]bool{}
	for _, model := range models {
		seen[model.ID] = true
	}
	if !seen["cartesia:sonic-3.5"] || !seen["minimax-audio:speech-2.8-hd"] {
		t.Fatalf("models = %+v", models)
	}
	if !audioProviderSupports("cartesia", "voice.create") ||
		!audioProviderSupports("minimax-audio", "voice.create") ||
		!strings.Contains(string(mustReadTestFile(t, "apteva.yaml")), "version: 0.10.55") {
		t.Fatal("provider or manifest capability missing")
	}
}

func mustReadTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
