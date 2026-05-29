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

func TestGenerationCacheKeyChangesWithItems(t *testing.T) {
	settings := map[string]any{"aspect": "9:16"}
	a := generationCacheKey(1, "image", "prompt", settings, []int64{1}, []int64{2})
	b := generationCacheKey(1, "image", "prompt", settings, []int64{1}, []int64{3})
	if a == b {
		t.Fatal("cache key must include item ids")
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
