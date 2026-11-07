package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestCatalogAssetTitlesPersistAcrossReadsAndLabelEdits(t *testing.T) {
	a, ctx, p, _, session := setupCatalog(t)
	id := attach(t, a, ctx, session, 1)
	result, err := a.assetLabelsUpdate(ctx, map[string]any{
		"asset_ids": []string{id}, "title": " Studio — First take ",
		"expected_revisions": map[string]int64{id: 1}, "tags": []string{"best-take"}, "favorite": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	saved := result.(map[string]any)["assets"].([]*Asset)[0]
	if saved.Title != "Studio — First take" || saved.Revision != 2 {
		t.Fatalf("saved title: %+v", saved)
	}
	// The title is Catalog metadata even when Media is unavailable.
	p.mediaBatchError = nil
	p.mediaRows = map[string]map[string]any{}
	detail, err := a.assetGet(ctx, map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	asset := detail.(map[string]any)["asset"].(*Asset)
	if asset.Title != saved.Title || asset.Name != "clip-1.mp4" || asset.StorageFileID != "1" || !asset.Favorite {
		t.Fatalf("detail contract: %+v", asset)
	}
	list, err := a.assetsList(ctx, map[string]any{"session_id": session})
	if err != nil || list.(map[string]any)["assets"].([]Asset)[0].Title != saved.Title {
		t.Fatalf("list title: %v %v", list, err)
	}
	if hits := assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "query": "STUDIO — FIRST TAKE"}); len(hits) != 1 || hits[0].ID != id || hits[0].Title != saved.Title {
		t.Fatalf("title search: %+v", hits)
	}
	if hits := assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "query": "clip-1.mp4"}); len(hits) != 1 || hits[0].ID != id {
		t.Fatalf("filename search: %+v", hits)
	}
	if _, err = a.assetLabelsUpdate(ctx, map[string]any{"asset_ids": []string{id}, "tags_add": []string{"share-next"}}); err != nil {
		t.Fatal(err)
	}
	asset, err = assetByID(ctx.AppDB(), "project-a", id)
	if err != nil || asset.Title != saved.Title || len(asset.Tags) != 2 {
		t.Fatalf("tag edit lost title: %+v %v", asset, err)
	}
	if _, err = a.assetLabelsUpdate(ctx, map[string]any{"asset_ids": []string{id}, "title": "", "expected_revisions": map[string]int64{id: asset.Revision}}); err != nil {
		t.Fatal(err)
	}
	asset, err = assetByID(ctx.AppDB(), "project-a", id)
	if err != nil || asset.Title != "" || asset.Name != "clip-1.mp4" {
		t.Fatalf("clear title: %+v %v", asset, err)
	}
}

func TestCatalogTitleSearchPaginatesPastFirstCandidatePage(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	for i := range 102 {
		title := "Other title"
		if i < 2 {
			title = "Rare Catalog title"
		}
		if _, err := ctx.AppDB().Exec(`INSERT INTO assets(id,project_id,session_id,storage_install_id,storage_file_id,name,title,created_at) VALUES(?,?,?,?,?,?,?,?)`, fmt.Sprintf("asset-%03d", i), "project-a", session, 11, fmt.Sprint(i+1), "clip.mov", title, fmt.Sprintf("2026-09-24T10:%02d:%02dZ", i/60, i%60)); err != nil {
			t.Fatal(err)
		}
	}
	args := map[string]any{"entity_type": "assets", "query": "Rare Catalog title", "limit": 1}
	result, err := a.search(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	page := result.(map[string]any)["assets"].(searchPage[assetSearchHit])
	if len(page.Items) != 1 || page.Items[0].ID != "asset-001" || page.NextCursor == "" {
		t.Fatalf("first title page: %+v", page)
	}
	args["assets_cursor"] = page.NextCursor
	result, err = a.search(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	page = result.(map[string]any)["assets"].(searchPage[assetSearchHit])
	if len(page.Items) != 1 || page.Items[0].ID != "asset-000" || page.NextCursor != "" {
		t.Fatalf("second title page: %+v", page)
	}
}

func TestCatalogAssetTitleEditsRequireOneAssetAndRevision(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	id := attach(t, a, ctx, session, 1)
	second := attach(t, a, ctx, session, 2)
	for _, args := range []map[string]any{
		{"asset_ids": []string{id}, "title": "Title"},
		{"asset_ids": []string{id}, "title": "Title", "expected_revisions": map[string]int64{id: 99}},
		{"asset_ids": []string{id}, "title": 42, "expected_revisions": map[string]int64{id: 1}},
		{"asset_ids": []string{id}, "title": strings.Repeat("é", 301), "expected_revisions": map[string]int64{id: 1}},
		{"asset_ids": []string{id, second}, "title": "Title", "expected_revisions": map[string]int64{id: 1, second: 1}},
	} {
		if _, err := a.assetLabelsUpdate(ctx, args); err == nil {
			t.Fatalf("accepted invalid title edit: %v", args)
		}
	}
	asset, err := assetByID(ctx.AppDB(), "project-a", id)
	if err != nil || asset.Title != "" || asset.Revision != 1 {
		t.Fatalf("invalid edits mutated asset: %+v %v", asset, err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE assets SET project_id='other-project' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := a.assetLabelsUpdate(ctx, map[string]any{"asset_ids": []string{id}, "title": "Wrong project", "expected_revisions": map[string]int64{id: 1}}); err == nil {
		t.Fatal("cross-project title edit accepted")
	}
}
