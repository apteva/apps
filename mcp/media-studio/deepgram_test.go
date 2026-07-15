package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestBuildDeepgramTTSArgs(t *testing.T) {
	got, err := buildAudioTTSArgs(map[string]any{
		"prompt": "Hola desde Barcelona",
		"model":  "aura-sirio-es",
		"options": map[string]any{
			"output_format": "wav",
			"sample_rate":   24000,
		},
	}, "deepgram", "audio.tts")
	if err != nil {
		t.Fatal(err)
	}
	if got["text"] != "Hola desde Barcelona" || got["model"] != "aura-sirio-es" {
		t.Fatalf("required args = %+v", got)
	}
	if got["encoding"] != "linear16" || got["container"] != "wav" || got["sample_rate"] != 24000 {
		t.Fatalf("format args = %+v", got)
	}
}

func TestBuildDeepgramTTSArgsRejectsLongText(t *testing.T) {
	_, err := buildDeepgramTTSArgs(map[string]any{"prompt": strings.Repeat("a", 2001)})
	if err == nil || !strings.Contains(err.Error(), "2000-character") {
		t.Fatalf("long prompt error = %v", err)
	}
}

func TestDeepgramModelsAreAvailableWithoutProjectCatalogCall(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newMediaStudioCtx(t, pf)
	models, err := loadModelsForBoundCapability(ctx, KindAudioTTS, "audio.tts", &sdk.BoundIntegration{
		ConnectionID: 84,
		AppSlug:      "deepgram",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) < 10 || models[0].ID != "aura-2-thalia-en" || models[0].PromptCharLimit != 2000 {
		t.Fatalf("Deepgram models = %+v", models)
	}
	if len(pf.executeCalls) != 0 {
		t.Fatalf("model listing should not require Deepgram project_id: %+v", pf.executeCalls)
	}
}

func TestToolMediaGenerateDeepgramTTSWithStorage(t *testing.T) {
	pf := newRecordingPlatform()
	pf.appSlug = "deepgram"
	pf.identity.Bindings = map[string]any{
		"audio_provider": float64(84),
		"storage":        float64(17),
	}
	pf.nextExecuteResult = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"_binary":true,"base64":"SUQzYXVkaW8=","mimeType":"audio/mpeg"}`),
	}
	pf.nextCallResult = json.RawMessage(`{"result":{"content":[{"type":"text","text":"{\"id\":7100}"}]}}`)
	ctx := newMediaStudioCtx(t, pf)
	out, err := (&App{}).toolMediaGenerate(ctx, map[string]any{
		"kind": "audio_tts", "prompt": "Hello", "model": "aura-2-thalia-en",
		"options": map[string]any{"output_format": "mp3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result := out.(map[string]any); result["isError"] == true {
		t.Fatalf("result = %+v", result)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "speak" {
		t.Fatalf("execute calls = %+v", pf.executeCalls)
	}
	input := pf.executeCalls[0].Input
	if input["text"] != "Hello" || input["model"] != "aura-2-thalia-en" || input["encoding"] != "mp3" {
		t.Fatalf("Deepgram input = %+v", input)
	}
	if len(pf.callAppCalls) != 1 || pf.callAppCalls[0].Input["content_type"] != "audio/mpeg" {
		t.Fatalf("Storage call = %+v", pf.callAppCalls)
	}
	meta := out.(map[string]any)["_meta"].(map[string]any)
	if meta["provider"] != "deepgram" || meta["kind"] != "audio_tts" {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestSelectBoundProviderUsesQualifiedDeepgramModel(t *testing.T) {
	pf := newRecordingPlatform()
	pf.identity.Bindings["audio_provider"] = map[string]any{
		"ids": []any{float64(42), float64(84)}, "default_id": float64(42),
	}
	pf.connectionSlugs = map[int64]string{42: "elevenlabs", 84: "deepgram"}
	ctx := newMediaStudioCtx(t, pf)
	args := map[string]any{"model": "deepgram:aura-2-thalia-en"}
	bound, err := selectBoundProvider(ctx, handlers[KindAudioTTS], args, "audio.tts")
	if err != nil {
		t.Fatal(err)
	}
	if bound == nil || bound.ConnectionID != 84 || bound.AppSlug != "deepgram" {
		t.Fatalf("bound = %+v", bound)
	}
	if args["model"] != "aura-2-thalia-en" {
		t.Fatalf("qualified model not stripped: %+v", args)
	}
	if audioProviderSupports("deepgram", "voice.create") {
		t.Fatal("Deepgram must not claim reusable voice creation")
	}
}
