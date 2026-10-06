package main

import "testing"

func TestAssetLabelsUpdateAndSearchFilters(t *testing.T) {
	a, ctx, _, brand, session := setupCatalog(t)
	video := attach(t, a, ctx, session, 1)
	image := attach(t, a, ctx, session, 2)
	if _, err := a.assetLabelsUpdate(ctx, map[string]any{"asset_ids": []string{video}, "tags": []string{"best-take", "share-next"}, "favorite": true, "patreon_intent": "paid", "expected_revisions": map[string]any{video: int64(1)}}); err != nil {
		t.Fatal(err)
	}
	detail, err := a.assetGet(ctx, map[string]any{"id": video})
	if err != nil {
		t.Fatal(err)
	}
	asset := detail.(map[string]any)["asset"].(*Asset)
	if !asset.Favorite || asset.PatreonIntent != "paid" || len(asset.Tags) != 2 || asset.Tags[0] != "best-take" {
		t.Fatalf("labels not returned on detail: %#v", asset)
	}
	hits := assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "brand_id": brand, "favorite": true, "patreon_intent": "paid", "tag": "share-next"})
	if len(hits) != 1 || hits[0].ID != video || !hits[0].Favorite || hits[0].PatreonIntent != "paid" {
		t.Fatalf("filtered labels = %#v", hits)
	}
	if got := assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "favorite": false}); len(got) != 1 || got[0].ID != image {
		t.Fatalf("non-favorites = %#v", got)
	}
	if _, err := a.assetLabelsUpdate(ctx, map[string]any{"asset_ids": []string{video, image}, "tags": []string{"teaser"}, "patreon_intent": "free"}); err != nil {
		t.Fatal(err)
	}
	if got := assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "tag": "teaser", "patreon_intent": "free"}); len(got) != 2 {
		t.Fatalf("bulk labels = %#v", got)
	}
	if _, err := a.assetLabelsUpdate(ctx, map[string]any{"asset_ids": []string{video, image}, "favorite": false, "expected_revisions": map[string]any{video: int64(3), image: int64(2)}}); err != nil {
		t.Fatal(err)
	}
}

func TestAssetLabelsBulkRevisionConflictRollsBack(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	first := attach(t, a, ctx, session, 1)
	second := attach(t, a, ctx, session, 2)
	if _, err := a.assetLabelsUpdate(ctx, map[string]any{"asset_ids": []string{first, second}, "favorite": true, "expected_revisions": map[string]any{first: int64(99), second: int64(1)}}); err == nil {
		t.Fatal("stale bulk update should fail")
	}
	for _, id := range []string{first, second} {
		got, err := assetByID(ctx.AppDB(), "project-a", id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Favorite {
			t.Fatalf("conflicting bulk update partially changed %s", id)
		}
	}
}
