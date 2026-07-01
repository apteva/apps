package main

import (
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
	want := []string{"storage:10", "storage:11", "storage:12"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	got = defaultImageSourceRefs(refs, items, map[string]any{"model": "gemini-2.5-flash-image"})
	if len(got) != 5 || got[3] != "storage:20" || got[4] != "storage:21" {
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
