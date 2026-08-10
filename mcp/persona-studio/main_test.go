package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func newPersonaCtx(t *testing.T) *sdk.AppCtx {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"))
	globalCtx = ctx
	return ctx
}

type personaMediaPlatform struct {
	tk.BasePlatformClient
	models       []map[string]any
	generateOut  map[string]any
	generateCall map[string]any
}

func (p *personaMediaPlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	var value any
	switch appName + ":" + tool {
	case "media-studio:media_models":
		value = map[string]any{"kind": "video", "bound": true, "provider": "venice-ai", "models": p.models}
	case "media-studio:media_generate":
		p.generateCall = cloneMap(input)
		value = p.generateOut
	default:
		return tk.ErrNotImplemented
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func newPersonaCtxWithPlatform(t *testing.T, platform sdk.PlatformClient) *sdk.AppCtx {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(platform))
	globalCtx = ctx
	return ctx
}

func TestPersonaCreateSeedsDefaultStyles(t *testing.T) {
	ctx := newPersonaCtx(t)
	app := &App{}

	out, err := app.toolPersonaCreate(ctx, map[string]any{
		"name":         "Mira Vale",
		"handle":       "@mira",
		"tone":         "warm, direct",
		"visual_style": "clean studio lighting",
	})
	if err != nil {
		t.Fatalf("create persona: %v", err)
	}
	p := out.(map[string]any)["persona"].(*Persona)
	if p.ID == 0 {
		t.Fatal("expected persona id")
	}
	styles, err := listStyleProfiles(ctx.AppDB(), "test-proj", p.ID, "")
	if err != nil {
		t.Fatalf("list styles: %v", err)
	}
	if len(styles) < 5 {
		t.Fatalf("expected default style profiles, got %d", len(styles))
	}
}

func TestItemsReferencesAndPromptResolution(t *testing.T) {
	ctx := newPersonaCtx(t)
	app := &App{}
	p := mustPersona(t, app, ctx)

	if _, err := app.toolReferenceAdd(ctx, map[string]any{
		"persona_id":      p.ID,
		"storage_file_id": 42,
		"kind":            "face",
		"label":           "hero reference",
	}); err != nil {
		t.Fatalf("add reference: %v", err)
	}
	itemOut, err := app.toolItemCreate(ctx, map[string]any{
		"persona_id":       p.ID,
		"name":             "Green smoothie bottle",
		"kind":             "product",
		"visual_rules":     "Label must stay readable and centered.",
		"storage_file_ids": []any{99.0},
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	item := itemOut.(map[string]any)["item"].(*Item)

	refs, _ := listReferences(ctx.AppDB(), "test-proj", p.ID, "", true)
	items, _ := listItemsByIDs(ctx.AppDB(), "test-proj", p.ID, []int64{item.ID})
	prompt := buildResolvedPrompt(p, nil, refs, items, "film a vertical product teaser", "video")
	for _, want := range []string{"Mira Vale", "storage:42", "Green smoothie bottle", "Label must stay readable", "film a vertical product teaser"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("resolved prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestPersonaUpdateStoresVoiceAndAvatarDefaults(t *testing.T) {
	ctx := newPersonaCtx(t)
	app := &App{}
	p := mustPersona(t, app, ctx)
	out, err := app.toolPersonaUpdate(ctx, map[string]any{
		"id": p.ID,
		"patch": map[string]any{
			"default_voice_id":  "voice_alpha",
			"default_avatar_id": "avatar_mira",
		},
	})
	if err != nil {
		t.Fatalf("update persona: %v", err)
	}
	got := out.(map[string]any)["persona"].(*Persona)
	if got.DefaultVoiceID != "voice_alpha" || got.DefaultAvatarID != "avatar_mira" {
		t.Fatalf("defaults not stored: voice=%q avatar=%q", got.DefaultVoiceID, got.DefaultAvatarID)
	}
}

func TestGenerationCacheKeyChangesWithItems(t *testing.T) {
	settings := map[string]any{"aspect": "9:16"}
	a := generationCacheKey(1, "image", "prompt", settings, []int64{1}, []int64{2})
	b := generationCacheKey(1, "image", "prompt", settings, []int64{1}, []int64{3})
	if a == b {
		t.Fatal("cache key must include item ids")
	}
}

func TestGenerationCacheKeyChangesWithStorageFolder(t *testing.T) {
	a := generationCacheKey(1, "image", "prompt", map[string]any{"storage_folder": "/a/"}, []int64{1}, nil)
	b := generationCacheKey(1, "image", "prompt", map[string]any{"storage_folder": "/b/"}, []int64{1}, nil)
	if a == b {
		t.Fatal("cache key must include storage_folder")
	}
}

func TestWriteGenerationResultReturnsBadGatewayForRecordedFailure(t *testing.T) {
	rec := httptest.NewRecorder()
	writeGenerationResult(rec, map[string]any{
		"asset": map[string]any{"id": 9, "status": "failed"},
		"error": "provider rejected request",
	}, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if !strings.Contains(rec.Body.String(), "provider rejected request") {
		t.Fatalf("response did not preserve provider error: %s", rec.Body.String())
	}
}

func TestMediaEventsCompleteAndFailQueuedAssets(t *testing.T) {
	ctx := newPersonaCtx(t)
	app := &App{}
	p := mustPersona(t, app, ctx)
	ready, err := app.insertAsset(ctx, "test-proj", p.ID, 0, "video", "queued", "ready prompt", "resolved", "venice-ai", "video-model", nil, nil, nil, "ready-cache", "", 0, 0, 71)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := app.insertAsset(ctx, "test-proj", p.ID, 0, "avatar", "queued", "failed prompt", "resolved", "heygen", "avatar-model", nil, nil, nil, "failed-cache", "", 0, 0, 72)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.handleMediaGenerated(ctx, sdk.Event{
		ProjectID: "test-proj",
		Data:      map[string]any{"job_id": 71, "generation_id": 801, "storage_id": 901},
	}); err != nil {
		t.Fatalf("complete event: %v", err)
	}
	if err := app.handleMediaFailed(ctx, sdk.Event{
		ProjectID: "test-proj",
		Data:      map[string]any{"job_id": 72, "error": "provider timeout"},
	}); err != nil {
		t.Fatalf("failed event: %v", err)
	}
	ready, err = getAsset(ctx.AppDB(), "test-proj", ready.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ready.Status != "ready" || ready.StorageFileID != 901 || ready.MediaGenerationID != 801 || ready.MediaJobID != 71 {
		t.Fatalf("completed asset not reconciled: %#v", ready)
	}
	failed, err = getAsset(ctx.AppDB(), "test-proj", failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != "failed" || failed.Error != "provider timeout" {
		t.Fatalf("failed asset not reconciled: %#v", failed)
	}
	if _, ok := cachedAsset(ctx.AppDB(), "test-proj", "ready-cache"); !ok {
		t.Fatal("completed async asset was not cached")
	}
}

func TestResolvePersonaCompositionPlanInjectsDefaults(t *testing.T) {
	ctx := newPersonaCtx(t)
	app := &App{}
	p := mustPersona(t, app, ctx)
	if _, err := app.toolPersonaUpdate(ctx, map[string]any{
		"id": p.ID,
		"patch": map[string]any{
			"default_voice_id":  "voice_local",
			"default_avatar_id": "avatar_local",
		},
	}); err != nil {
		t.Fatalf("update defaults: %v", err)
	}
	if _, err := app.toolReferenceAdd(ctx, map[string]any{
		"persona_id":      p.ID,
		"storage_file_id": 42,
		"kind":            "face",
	}); err != nil {
		t.Fatalf("add reference: %v", err)
	}
	if _, err := app.toolReferenceAdd(ctx, map[string]any{
		"persona_id":      p.ID,
		"storage_file_id": 43,
		"kind":            "outfit",
	}); err != nil {
		t.Fatalf("add second reference: %v", err)
	}
	source := map[string]any{
		"output": map[string]any{"format": "mp3"},
		"tracks": []any{
			map[string]any{"type": "audio", "clips": []any{
				map[string]any{"asset": map[string]any{"type": "audio", "src": ""}, "start": float64(0), "length": float64(5), "ai": map[string]any{"media_kind": "audio_tts", "prompt": "Say hello."}},
			}},
			map[string]any{"type": "visual", "clips": []any{
				map[string]any{"asset": map[string]any{"type": "image", "src": ""}, "start": float64(0), "length": float64(5), "ai": map[string]any{"media_kind": "image", "prompt": "Portrait."}},
			}},
		},
	}
	resolved, err := resolvePersonaCompositionPlan(ctx, "test-proj", p.ID, source)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	tracks := resolved["tracks"].([]any)
	audioClip := tracks[0].(map[string]any)["clips"].([]any)[0].(map[string]any)
	audioAI := audioClip["ai"].(map[string]any)
	if audioAI["voice"] != "voice_local" {
		t.Fatalf("voice default not injected: %#v", audioAI)
	}
	if audioAI["prompt"] != "Say hello." {
		t.Fatalf("audio script should stay literal, got: %s", audioAI["prompt"])
	}
	visualClip := tracks[1].(map[string]any)["clips"].([]any)[0].(map[string]any)
	visualAI := visualClip["ai"].(map[string]any)
	if visualAI["source_image"] != "storage:42" {
		t.Fatalf("source image not injected: %#v", visualAI)
	}
	sourceImages, _ := visualAI["source_images"].([]string)
	if len(sourceImages) != 2 || sourceImages[0] != "storage:42" || sourceImages[1] != "storage:43" {
		t.Fatalf("source images not injected: %#v", visualAI["source_images"])
	}
	if !strings.Contains(visualAI["prompt"].(string), "Mira Vale") || !strings.Contains(visualAI["prompt"].(string), "Portrait.") {
		t.Fatalf("visual prompt should be persona-resolved: %s", visualAI["prompt"])
	}
}

func TestBuildComposerTracksSeparatesAudioAndVisualAssets(t *testing.T) {
	tracks := buildComposerTracks([]Asset{
		{ID: 1, StorageFileID: 10, AssetType: "image", Prompt: "cover"},
		{ID: 2, StorageFileID: 11, AssetType: "audio_tts", Prompt: "voice"},
	}, 10000)
	if len(tracks) != 2 {
		t.Fatalf("expected visual and audio tracks, got %#v", tracks)
	}
	if tracks[0]["type"] != "visual" || tracks[1]["type"] != "audio" {
		t.Fatalf("unexpected track order/types: %#v", tracks)
	}
}

func TestDefaultImageSourceRefsUsesSourceImagesArrayForOneReference(t *testing.T) {
	refs := []Reference{{ID: 1, StorageFileID: 42, Kind: "face", Active: true}}
	got := defaultImageSourceRefs(refs, nil, map[string]any{"model": "firered-image-edit"})
	if len(got) != 1 || got[0] != "storage:42" {
		t.Fatalf("unexpected source refs: %#v", got)
	}
}

func TestDefaultVisualSourceRefsVideoPreservesExplicitThenPersonaRefs(t *testing.T) {
	refs := []Reference{
		{ID: 1, StorageFileID: 42, Kind: "face", Active: true},
		{ID: 2, StorageFileID: 43, Kind: "outfit", Active: true},
	}
	got := defaultVisualSourceRefs("video", refs, nil, map[string]any{
		"source_images": []any{"storage:99"},
		"model":         "seedance-2-0-mini-enhanced-reference-to-video",
	})
	want := []string{"storage:99", "storage:42", "storage:43"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("video source refs = %#v, want %#v", got, want)
	}
}

func TestVisualSourceCandidatesPrioritizeWeightedFacesThenSelectedItems(t *testing.T) {
	refs := []Reference{
		{ID: 1, StorageFileID: 41, Kind: "style", Weight: 5},
		{ID: 2, StorageFileID: 42, Kind: "face", Weight: 1},
		{ID: 3, StorageFileID: 43, Kind: "face", Weight: 2},
		{ID: 4, StorageFileID: 44, Kind: "outfit", Weight: 10},
	}
	items := []Item{{ID: 7, StorageFileIDs: []int64{90}}}
	got := visualSourceCandidates(refs, items, map[string]any{"source_images": []any{"storage:99"}})
	want := []string{"storage:99", "storage:43", "storage:42", "storage:90", "storage:44", "storage:41"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("visual source order = %#v, want %#v", got, want)
	}
}

func TestGenerateVideoAutoSelectsBestReferenceModelAndSendsRankedSources(t *testing.T) {
	platform := &personaMediaPlatform{
		models: []map[string]any{
			{"id": "kling-text-to-video", "model_type": "text-to-video"},
			{"id": "seedance-image-to-video", "model_type": "image-to-video", "max_source_images": 1},
			{"id": "seedance-reference-to-video", "model_type": "image-to-video", "max_source_images": 9},
		},
		generateOut: map[string]any{
			"_meta": map[string]any{
				"status":        "queued",
				"job_id":        71,
				"generation_id": 81,
				"provider":      "venice-ai",
				"model":         "seedance-reference-to-video",
			},
		},
	}
	ctx := newPersonaCtxWithPlatform(t, platform)
	app := &App{}
	persona := mustPersona(t, app, ctx)
	for _, input := range []map[string]any{
		{"persona_id": persona.ID, "storage_file_id": 42, "kind": "face", "weight": 2},
		{"persona_id": persona.ID, "storage_file_id": 43, "kind": "outfit", "weight": 3},
	} {
		if _, err := app.toolReferenceAdd(ctx, input); err != nil {
			t.Fatalf("add reference: %v", err)
		}
	}
	itemOut, err := app.toolItemCreate(ctx, map[string]any{
		"persona_id":       persona.ID,
		"name":             "Black sequin dress",
		"kind":             "wardrobe",
		"storage_file_ids": []any{90},
	})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	item := itemOut.(map[string]any)["item"].(*Item)
	out, err := app.toolGenerateAsset(ctx, map[string]any{
		"persona_id": persona.ID,
		"asset_type": "video",
		"prompt":     "Walk into the party wearing the selected dress.",
		"item_ids":   []any{item.ID},
		"settings":   map[string]any{"aspect": "9:16", "duration": 5},
	})
	if err != nil {
		t.Fatalf("generate video: %v", err)
	}
	asset := out.(map[string]any)["asset"].(*Asset)
	if asset.Status != "queued" || asset.MediaJobID != 71 {
		t.Fatalf("queued asset not recorded: %#v", asset)
	}
	if platform.generateCall["model"] != "seedance-reference-to-video" {
		t.Fatalf("model = %#v, want multi-reference model", platform.generateCall["model"])
	}
	sources, ok := platform.generateCall["source_images"].([]string)
	if !ok {
		t.Fatalf("source_images type/value = %#v", platform.generateCall["source_images"])
	}
	want := []string{"storage:42", "storage:90", "storage:43"}
	if strings.Join(sources, ",") != strings.Join(want, ",") {
		t.Fatalf("source_images = %#v, want %#v", sources, want)
	}
	if _, leaked := platform.generateCall["source_image_limit"]; leaked {
		t.Fatalf("internal source_image_limit leaked to Media Studio: %#v", platform.generateCall)
	}
	if _, leaked := platform.generateCall["reference_image_urls"]; leaked {
		t.Fatalf("provider-specific reference_image_urls leaked from Persona Studio: %#v", platform.generateCall)
	}
	if _, legacy := platform.generateCall["source_image"]; legacy {
		t.Fatalf("persona video should use the generic source_images array only: %#v", platform.generateCall)
	}
}

func TestPrepareVideoReferenceSettingsRejectsTextOnlyModel(t *testing.T) {
	platform := &personaMediaPlatform{
		models: []map[string]any{{"id": "kling-text-to-video", "model_type": "text-to-video"}},
	}
	ctx := newPersonaCtxWithPlatform(t, platform)
	settings := map[string]any{"model": "kling-text-to-video"}
	err := prepareVideoReferenceSettings(ctx, "test-proj", settings)
	if err == nil || !strings.Contains(err.Error(), "not a reference-to-video model") {
		t.Fatalf("expected reference capability error, got %v", err)
	}
}

func TestGenerateVideoRequiresVisualReference(t *testing.T) {
	platform := &personaMediaPlatform{
		models: []map[string]any{{"id": "seedance-reference-to-video", "max_source_images": 9}},
	}
	ctx := newPersonaCtxWithPlatform(t, platform)
	app := &App{}
	persona := mustPersona(t, app, ctx)
	_, err := app.toolGenerateAsset(ctx, map[string]any{
		"persona_id": persona.ID,
		"asset_type": "video",
		"prompt":     "Walk into the party.",
	})
	if err == nil || !strings.Contains(err.Error(), "requires at least one visual reference image") {
		t.Fatalf("expected missing visual reference error, got %v", err)
	}
	if platform.generateCall != nil {
		t.Fatalf("Media Studio generation should not be called without a visual reference")
	}
}

func TestPrepareVideoReferenceSettingsHonorsModelAndRequestedLimits(t *testing.T) {
	platform := &personaMediaPlatform{
		models: []map[string]any{
			{"id": "seedance-image-to-video", "max_source_images": 1},
			{"id": "seedance-reference-to-video", "max_source_images": 9},
		},
	}
	ctx := newPersonaCtxWithPlatform(t, platform)
	single := map[string]any{"model": "seedance-image-to-video", "source_image_limit": 5}
	if err := prepareVideoReferenceSettings(ctx, "test-proj", single); err == nil || !strings.Contains(err.Error(), "not a reference-to-video model") {
		t.Fatalf("image-to-video model should be rejected, got %v", err)
	}
	multi := map[string]any{"model": "seedance-reference-to-video", "source_image_limit": 4}
	if err := prepareVideoReferenceSettings(ctx, "test-proj", multi); err != nil {
		t.Fatalf("prepare multi-reference model: %v", err)
	}
	if got := intArg(multi, "source_image_limit", 0); got != 4 {
		t.Fatalf("requested lower limit = %d, want 4", got)
	}
}

func TestReferenceRemoveUnlinksActiveReference(t *testing.T) {
	ctx := newPersonaCtx(t)
	app := &App{}
	p := mustPersona(t, app, ctx)
	out, err := app.toolReferenceAdd(ctx, map[string]any{
		"persona_id":      p.ID,
		"storage_file_id": 42,
		"kind":            "face",
	})
	if err != nil {
		t.Fatalf("add reference: %v", err)
	}
	ref := out.(map[string]any)["reference"].(*Reference)
	if _, err := app.toolReferenceRemove(ctx, map[string]any{"id": ref.ID}); err != nil {
		t.Fatalf("remove reference: %v", err)
	}
	refs, err := listReferences(ctx.AppDB(), "test-proj", p.ID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Fatalf("expected active references to be empty, got %#v", refs)
	}
}

func TestDefaultImageSourceRefsIncludesItemsAndHonorsLimit(t *testing.T) {
	refs := []Reference{
		{ID: 1, StorageFileID: 10, Kind: "face", Active: true},
		{ID: 2, StorageFileID: 11, Kind: "style", Active: true},
		{ID: 3, StorageFileID: 12, Kind: "outfit", Active: true},
		{ID: 4, StorageFileID: 13, Kind: "voice", Active: true},
		{ID: 5, StorageFileID: 14, Kind: "avatar", Active: true},
	}
	items := []Item{{ID: 7, StorageFileIDs: []int64{20, 21}}}
	got := defaultImageSourceRefs(refs, items, map[string]any{"model": "firered-image-edit"})
	want := []string{"storage:10", "storage:20", "storage:21"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	got = defaultImageSourceRefs(refs, items, map[string]any{"model": "gemini-2.5-flash-image"})
	if len(got) != 5 || got[1] != "storage:20" || got[2] != "storage:21" || got[3] != "storage:12" || got[4] != "storage:11" {
		t.Fatalf("gemini refs should include item images up to 5, got %#v", got)
	}
}

func TestIsImageEditModelKeepsEditCapableModels(t *testing.T) {
	yes := []string{"", "firered-image-edit", "qwen-edit", "gemini-2.5-flash-image"}
	for _, model := range yes {
		if !isImageEditModel(model) {
			t.Fatalf("%q should be accepted as image-edit capable", model)
		}
	}
	no := []string{"gpt-image-2", "gpt-image-1.5", "dall-e-2", "dall-e-3", "flux-dev", "stable-diffusion-3.5", "qwen-image-2"}
	for _, model := range no {
		if isImageEditModel(model) {
			t.Fatalf("%q should not be accepted as image-edit capable", model)
		}
	}
}

func TestFilterStorageBrowserOutputHidesDotFolders(t *testing.T) {
	out := map[string]any{
		"files": []any{
			map[string]any{"id": float64(1), "folder": "/", "name": "face.png"},
			map[string]any{"id": float64(2), "folder": "/.generated/images/", "name": "render.png"},
			map[string]any{"id": float64(3), "folder": "/campaign/.composer/", "name": "clip.mp4"},
			map[string]any{"id": float64(4), "folder": "/refs/", "name": ".placeholder"},
		},
		"count": 4,
	}
	filterStorageBrowserOutput(out)
	files := out["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("expected only user-facing file, got %#v", files)
	}
	if out["count"] != 1 {
		t.Fatalf("expected count to be updated, got %#v", out["count"])
	}
}

func TestMediaStudioResultMetaExtractsMCPMeta(t *testing.T) {
	meta := mediaStudioResultMeta(map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "ok"}},
		"_meta": map[string]any{
			"provider":    "venice-ai",
			"model":       "firered-image-edit",
			"storage_ids": []any{float64(192)},
		},
	})
	if strFromMap(meta, "provider") != "venice-ai" {
		t.Fatalf("provider not extracted from _meta: %#v", meta)
	}
	if firstInt(meta["storage_ids"]) != 192 {
		t.Fatalf("storage_ids not extracted from _meta: %#v", meta)
	}
}

func TestMCPResultErrorExtractsText(t *testing.T) {
	msg := mcpResultError(map[string]any{
		"isError": true,
		"content": []any{
			map[string]any{"type": "text", "text": "provider returned zero items"},
		},
	})
	if msg != "provider returned zero items" {
		t.Fatalf("unexpected error message: %q", msg)
	}
}

func mustPersona(t *testing.T, app *App, ctx *sdk.AppCtx) *Persona {
	t.Helper()
	out, err := app.toolPersonaCreate(ctx, map[string]any{
		"name":         "Mira Vale",
		"tone":         "confident",
		"visual_style": "editorial, bright, consistent face",
	})
	if err != nil {
		t.Fatalf("create persona: %v", err)
	}
	return out.(map[string]any)["persona"].(*Persona)
}
