package main

import (
	"errors"
	"fmt"
	"testing"
)

func TestMediaDescriptionsConsistentAndLive(t *testing.T) {
	a, ctx, p, _, session := setupCatalog(t)
	id := attach(t, a, ctx, session, 1)
	p.mediaRows = map[string]map[string]any{"1": {"file_id": "1", "description": "Irina in a crimson dress", "description_source": "human", "description_updated_at": "2026-10-01T10:00:00Z", "probe_status": "ok", "audience_rating": "mature", "duration_ms": 12345}}
	check := func(asset *Asset) {
		t.Helper()
		if asset.Description != "Irina in a crimson dress" || asset.DescriptionSource != "human" || asset.DescriptionUpdatedAt != "2026-10-01T10:00:00Z" || asset.MediaStatus != "completed" || asset.MediaRating != "mature" {
			t.Fatalf("metadata: %+v", asset)
		}
	}
	detail, err := a.assetGet(ctx, map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	check(detail.(map[string]any)["asset"].(*Asset))
	list, err := a.assetsList(ctx, map[string]any{"session_id": session})
	if err != nil {
		t.Fatal(err)
	}
	check(&list.(map[string]any)["assets"].([]Asset)[0])
	result, err := a.search(ctx, map[string]any{"entity_type": "assets", "query": "CRIMSON DRESS"})
	if err != nil {
		t.Fatal(err)
	}
	hits := result.(map[string]any)["assets"].(searchPage[assetSearchHit]).Items
	if len(hits) != 1 || hits[0].ID != id {
		t.Fatalf("hits: %+v", hits)
	}
	check(&hits[0].Asset)
	p.mediaRows["1"]["description"] = "Revised description"
	p.mediaRows["1"]["audience_rating"] = "general"
	list, err = a.assetsList(ctx, map[string]any{"session_id": session})
	if err != nil {
		t.Fatal(err)
	}
	fresh := list.(map[string]any)["assets"].([]Asset)[0]
	if fresh.Description != "Revised description" || fresh.MediaRating != "general" {
		t.Fatalf("stale: %+v", fresh)
	}
	cached, _ := assetByID(ctx.AppDB(), "project-a", id)
	if cached.MediaStatus != "unknown" {
		t.Fatalf("read changed Catalog database: %+v", cached)
	}
}

func TestDescriptionSearchBeforePaginationAndDeduplicatedBatches(t *testing.T) {
	a, ctx, p, _, session := setupCatalog(t)
	p.mediaRows = map[string]map[string]any{}
	// 102 assets, first matching description only beyond the first 100 candidates.
	for i := 0; i < 102; i++ {
		id := fmt.Sprintf("asset-%03d", i)
		fid := fmt.Sprint(i + 1)
		_, err := ctx.AppDB().Exec(`INSERT INTO assets(id,project_id,session_id,storage_install_id,storage_file_id,name,created_at) VALUES(?,?,?,?,?,?,?)`, id, "project-a", session, 11, fid, "clip", fmt.Sprintf("2026-09-24T10:%02d:%02dZ", i/60, i%60))
		if err != nil {
			t.Fatal(err)
		}
		description := "other"
		if i < 2 {
			description = "rare description phrase"
		}
		p.mediaRows[fid] = map[string]any{"file_id": fid, "description": description, "probe_status": "ok"}
	}
	query := map[string]any{"entity_type": "assets", "query": "rare description phrase", "limit": 1}
	result, err := a.search(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	page := result.(map[string]any)["assets"].(searchPage[assetSearchHit])
	if len(page.Items) != 1 || page.Items[0].ID != "asset-001" || page.NextCursor == "" {
		t.Fatalf("page: %+v", page)
	}
	if p.mediaBatchCalls != 2 {
		t.Fatalf("want 2 batch calls for 102 assets, got %d", p.mediaBatchCalls)
	}
	query["assets_cursor"] = page.NextCursor
	result, err = a.search(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	page = result.(map[string]any)["assets"].(searchPage[assetSearchHit])
	if len(page.Items) != 1 || page.Items[0].ID != "asset-000" || page.NextCursor != "" {
		t.Fatalf("next page: %+v", page)
	}
	p.mediaBatchCalls = 0
	refs := []*Asset{{StorageFileID: "1"}, {StorageFileID: "1"}, {StorageFileID: "2"}}
	a.loadAssetMedia(ctx, refs)
	if p.mediaBatchCalls != 1 || refs[0].Description != refs[1].Description {
		t.Fatalf("dedup: %d %+v", p.mediaBatchCalls, refs)
	}
}

func TestMissingMediaAndTransportFailure(t *testing.T) {
	a, ctx, p, _, session := setupCatalog(t)
	id := attach(t, a, ctx, session, 1)
	p.mediaRows = map[string]map[string]any{}
	list, err := a.assetsList(ctx, map[string]any{"session_id": session})
	if err != nil {
		t.Fatal(err)
	}
	asset := list.(map[string]any)["assets"].([]Asset)[0]
	if asset.MediaStatus != "missing" || asset.Description != "" || asset.MediaRating != "" || asset.MediaError != "" {
		t.Fatalf("missing: %+v", asset)
	}
	detail, err := a.assetGet(ctx, map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	if detail.(map[string]any)["asset"].(*Asset).MediaStatus != "missing" {
		t.Fatal("detail should report missing")
	}
	p.mediaBatchError = errors.New("Media offline")
	list, err = a.assetsList(ctx, map[string]any{"session_id": session})
	if err != nil {
		t.Fatal(err)
	}
	asset = list.(map[string]any)["assets"].([]Asset)[0]
	if asset.MediaStatus != "unavailable" || asset.MediaError == "" {
		t.Fatalf("unavailable: %+v", asset)
	}
	if _, err = a.search(ctx, map[string]any{"entity_type": "assets", "query": "some description"}); err == nil {
		t.Fatal("must not silently return incomplete description search")
	}
}
