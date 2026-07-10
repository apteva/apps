package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestBuildFishAudioTTSArgs(t *testing.T) {
	got, err := buildFishAudioTTSArgs(map[string]any{
		"prompt": "Hello", "model": "s2.1-pro", "voice": "voice-1",
		"options": map[string]any{
			"output_format": "mp3_44100_128",
			"prosody":       map[string]any{"speed": 1.2, "volume": 2},
			"temperature":   0.6,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["model"] != "s2.1-pro" || got["text"] != "Hello" || got["reference_id"] != "voice-1" {
		t.Fatalf("required args = %+v", got)
	}
	if got["format"] != "mp3" || got["temperature"] != 0.6 {
		t.Fatalf("options = %+v", got)
	}
}

func TestToolMediaGenerate_FishAudioTTS(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "fish-audio"
	pf.identity.Bindings["audio_provider"] = float64(42)
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"_binary":true,"base64":"SUQzYXVkaW8=","mimeType":"audio/mpeg"}`),
	}
	pf.nextCallResult = json.RawMessage(`{"result":{"content":[{"type":"text","text":"{\"id\":7001}"}]}}`)
	ctx := newMediaStudioCtx(t, pf)
	out, err := (&App{}).toolMediaGenerate(ctx, map[string]any{
		"kind": "audio_tts", "prompt": "Hello", "model": "s2.1-pro", "voice": "fish-voice",
		"options": map[string]any{"output_format": "mp3", "prosody": map[string]any{"speed": 1.1}},
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
	input := pf.executeCalls[0].Input
	if input["model"] != "s2.1-pro" || input["reference_id"] != "fish-voice" || input["format"] != "mp3" {
		t.Fatalf("Fish input = %+v", input)
	}
	if len(pf.callAppCalls) != 1 || pf.callAppCalls[0].Input["content_type"] != "audio/mpeg" {
		t.Fatalf("Storage call = %+v", pf.callAppCalls)
	}
	meta := out.(map[string]any)["_meta"].(map[string]any)
	if meta["provider"] != "fish-audio" {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestToolMediaGenerate_FishAudioDoesNotClaimSFX(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "fish-audio"
	pf.identity.Bindings["audio_provider"] = float64(42)
	ctx := newMediaStudioCtx(t, pf)
	out, err := (&App{}).toolMediaGenerate(ctx, map[string]any{
		"kind": "audio_sfx", "prompt": "door slam", "model": "anything",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := out.(map[string]any); result["isError"] != true {
		t.Fatalf("expected capability error, got %+v", result)
	}
	if len(pf.executeCalls) != 0 {
		t.Fatalf("Fish SFX called provider: %+v", pf.executeCalls)
	}
}

func TestSelectBoundProvider_AudioUsesQualifiedModelAndVoice(t *testing.T) {
	pf := newRecordingPlatform()
	pf.identity.Bindings["audio_provider"] = map[string]any{
		"ids": []any{float64(42), float64(43)}, "default_id": float64(42),
	}
	pf.connectionSlugs = map[int64]string{42: "elevenlabs", 43: "fish-audio"}
	ctx := newMediaStudioCtx(t, pf)
	args := map[string]any{"model": "fish-audio:s2.1-pro", "voice": "fish-audio:voice-1"}
	bound, err := selectBoundProvider(ctx, handlers[KindAudioTTS], args, "audio.tts")
	if err != nil {
		t.Fatal(err)
	}
	if bound == nil || bound.ConnectionID != 43 || bound.AppSlug != "fish-audio" {
		t.Fatalf("bound = %+v", bound)
	}
	if args["model"] != "s2.1-pro" || args["voice"] != "voice-1" {
		t.Fatalf("qualified ids not stripped: %+v", args)
	}
}

func TestSelectBoundProvider_AudioRejectsProviderMismatchAndFishSFX(t *testing.T) {
	pf := newRecordingPlatform()
	pf.identity.Bindings["audio_provider"] = map[string]any{
		"ids": []any{float64(42), float64(43)}, "default_id": float64(43),
	}
	pf.connectionSlugs = map[int64]string{42: "elevenlabs", 43: "fish-audio"}
	ctx := newMediaStudioCtx(t, pf)
	_, err := selectBoundProvider(ctx, handlers[KindAudioTTS], map[string]any{
		"model": "fish-audio:s2.1-pro", "voice": "elevenlabs:voice-1",
	}, "audio.tts")
	if err == nil || !strings.Contains(err.Error(), "provider mismatch") {
		t.Fatalf("mismatch error = %v", err)
	}
	bound, err := selectBoundProvider(ctx, handlers[KindAudioSFX], map[string]any{}, "audio.sfx")
	if err != nil || bound == nil || bound.AppSlug != "elevenlabs" {
		t.Fatalf("SFX bound=%+v err=%v", bound, err)
	}
}

func TestLoadAudioModelsForAllProviders(t *testing.T) {
	pf := newRecordingPlatform()
	pf.identity.Bindings["audio_provider"] = map[string]any{
		"ids": []any{float64(42), float64(43)}, "default_id": float64(42),
	}
	pf.connectionSlugs = map[int64]string{42: "elevenlabs", 43: "fish-audio"}
	pf.perExecuteResults = map[string]*sdk.ExecuteResult{
		"list_models": {Success: true, Status: 200, Data: json.RawMessage(`[{"model_id":"eleven_v3","name":"Eleven v3","can_do_text_to_speech":true}]`)},
	}
	ctx := newMediaStudioCtx(t, pf)
	models, providers, err := loadAudioModelsForAllProviders(ctx, KindAudioTTS, "audio.tts")
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 || providers[0] != "elevenlabs" || providers[1] != "fish-audio" {
		t.Fatalf("providers = %+v", providers)
	}
	seen := map[string]bool{}
	for _, model := range models {
		seen[model.ID] = true
	}
	if !seen["elevenlabs:eleven_v3"] || !seen["fish-audio:s2.1-pro"] {
		t.Fatalf("models = %+v", models)
	}
}

func TestListVoicesForFishAudio(t *testing.T) {
	pf := newRecordingPlatform()
	pf.perExecuteResults = map[string]*sdk.ExecuteResult{
		"list_voice_models": {Success: true, Status: 200, Data: json.RawMessage(`{
			"total":2,"items":[
				{"_id":"fish-1","title":"Narrator","state":"created","languages":["en"],"samples":[{"audio":"https://audio.test/one.mp3"}]},
				{"_id":"fish-failed","title":"Bad","state":"failed"}
			]}`)},
	}
	ctx := newMediaStudioCtx(t, pf)
	voices, err := listVoicesFor(ctx, &sdk.BoundIntegration{ConnectionID: 43, AppSlug: "fish-audio"})
	if err != nil {
		t.Fatal(err)
	}
	if len(voices) != 1 || voices[0].ID != "fish-1" || voices[0].Name != "Narrator" || voices[0].Language != "en" || voices[0].Preview == "" {
		t.Fatalf("voices = %+v", voices)
	}
}

func TestCreateFishAudioCloneFromStorage(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "fish-audio"
	pf.identity.Bindings["audio_provider"] = float64(42)
	pf.perAppCallResults = map[string]json.RawMessage{
		"storage:files_get_content": json.RawMessage(`{"result":{"content":[{"type":"text","text":"{\"name\":\"sample.wav\",\"content_type\":\"audio/wav\",\"content_base64\":\"UklGRg==\"}"}]}}`),
	}
	pf.perExecuteResults = map[string]*sdk.ExecuteResult{
		"create_voice_model": {Success: true, Status: 201, Data: json.RawMessage(`{"_id":"fish-created","title":"My Voice","state":"created","languages":["en"]}`)},
	}
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaVoiceCreate(ctx, map[string]any{
		"provider": "fish-audio", "name": "My Voice", "source_type": "audio",
		"source_audio": "storage:55", "transcripts": []any{"Hello world"},
		"options": map[string]any{"visibility": "private", "enhance_audio_quality": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := out.(map[string]any); result["isError"] == true {
		t.Fatalf("result = %+v", result)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "create_voice_model" {
		t.Fatalf("execute calls = %+v", pf.executeCalls)
	}
	input := pf.executeCalls[0].Input
	voices := input["voices"].([]string)
	if len(voices) != 1 || !strings.HasPrefix(voices[0], "data:audio/wav;base64,") || input["voices_filename"] != "sample.wav" {
		t.Fatalf("provider input = %+v", input)
	}
	var provider, providerID, sourceRef, metadata string
	if err := ctx.AppDB().QueryRow(`SELECT provider, provider_identity_id, source_ref, metadata_json FROM media_identities LIMIT 1`).Scan(&provider, &providerID, &sourceRef, &metadata); err != nil {
		t.Fatal(err)
	}
	if provider != "fish-audio" || providerID != "fish-created" || sourceRef != "storage:55" {
		t.Fatalf("identity = %q %q %q", provider, providerID, sourceRef)
	}
	if strings.Contains(metadata, "UklGRg==") {
		t.Fatalf("metadata persisted inline audio: %s", metadata)
	}
}

func TestCreateElevenLabsCloneUsesGenericAudioFlow(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "elevenlabs"
	pf.identity.Bindings["audio_provider"] = float64(42)
	pf.perExecuteResults = map[string]*sdk.ExecuteResult{
		"create_ivc_voice": {Success: true, Status: 200, Data: json.RawMessage(`{"voice_id":"eleven-created","name":"Clone"}`)},
	}
	ctx := newMediaStudioCtx(t, pf)
	app := &App{}
	out, err := app.toolMediaVoiceCreate(ctx, map[string]any{
		"provider": "elevenlabs", "name": "Clone", "source_type": "audio",
		"source_audio": "data:audio/wav;base64,UklGRg==",
		"options":      map[string]any{"remove_background_noise": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := out.(map[string]any); result["isError"] == true {
		t.Fatalf("result = %+v", result)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "create_ivc_voice" || pf.executeCalls[0].Input["remove_background_noise"] != true {
		t.Fatalf("execute calls = %+v", pf.executeCalls)
	}
}

func TestResolveVoiceAudioSampleURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("audio-bytes"))
	}))
	defer server.Close()
	value, filename, err := resolveVoiceAudioSample(nil, server.URL+"/sample.mp3")
	if err != nil {
		t.Fatal(err)
	}
	want := base64.StdEncoding.EncodeToString([]byte("audio-bytes"))
	if value != "data:audio/mpeg;base64,"+want || filename != "sample.mp3" {
		t.Fatalf("value=%q filename=%q", value, filename)
	}
}
