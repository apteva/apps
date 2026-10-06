package main

import "testing"

func TestAssetSourcesOnListDetailAndSearch(t *testing.T) {
	a, ctx, _, brand, session := setupCatalog(t)
	original := attach(t, a, ctx, session, 1)
	other := attach(t, a, ctx, session, 2)
	child := attach(t, a, ctx, session, 3)
	nested := attach(t, a, ctx, session, 4)
	for _, link := range []map[string]any{{"child_asset_id": child, "source_asset_id": original, "relation": "reel", "source_order": int64(1)}, {"child_asset_id": child, "source_asset_id": other, "relation": "derived", "source_order": int64(2)}, {"child_asset_id": nested, "source_asset_id": child, "relation": "clip"}} {
		if _, err := a.assetLinkSource(ctx, link); err != nil {
			t.Fatal(err)
		}
	}
	externalResult, err := a.sessionCreate(ctx, map[string]any{"brand_id": brand, "title": "Other session"})
	if err != nil {
		t.Fatal(err)
	}
	externalSession := externalResult.(map[string]any)["session"].(Session).ID
	external := attach(t, a, ctx, externalSession, 5)
	if _, err := a.assetLinkSource(ctx, map[string]any{"child_asset_id": nested, "source_asset_id": external, "relation": "derived", "source_order": int64(1)}); err != nil {
		t.Fatal(err)
	}
	list, err := a.assetsList(ctx, map[string]any{"session_id": session})
	if err != nil {
		t.Fatal(err)
	}
	items := list.(map[string]any)["assets"].([]Asset)
	byID := map[string]Asset{}
	for _, asset := range items {
		byID[asset.ID] = asset
	}
	if len(byID[original].Sources) != 0 || byID[original].Sources == nil {
		t.Fatal("unlinked source needs explicit empty array")
	}
	sources := byID[child].Sources
	if len(sources) != 2 || sources[0].AssetID != original || sources[0].Name != "clip-1.mp4" || sources[0].SessionID != session || sources[0].Relation != "reel" || sources[1].AssetID != other {
		t.Fatalf("multi-parent lineage: %+v", sources)
	}
	parents := byID[nested].Sources
	if len(parents) != 2 || parents[0].AssetID != child || parents[1].SessionID != externalSession {
		t.Fatalf("nested/external parents: %+v", parents)
	}
	detail, err := a.assetGet(ctx, map[string]any{"id": child})
	if err != nil {
		t.Fatal(err)
	}
	detailed := detail.(map[string]any)["asset"].(*Asset)
	legacy := detail.(map[string]any)["sources"].([]AssetSource)
	if len(detailed.Sources) != 2 || len(legacy) != 2 || legacy[0] != sources[0] {
		t.Fatal("detail/list lineage inconsistent")
	}
	found, err := a.search(ctx, map[string]any{"entity_type": "assets", "session_id": session, "lineage": "derivative"})
	if err != nil {
		t.Fatal(err)
	}
	hits := found.(map[string]any)["assets"].(searchPage[assetSearchHit]).Items
	if len(hits) != 2 {
		t.Fatalf("derivative hits: %+v", hits)
	}
	for _, hit := range hits {
		if len(hit.Sources) != 2 {
			t.Fatalf("search missing sources: %+v", hit)
		}
	}
}

func TestSourceMetadataDoesNotLeakAcrossProjects(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	child := attach(t, a, ctx, session, 1)
	_, err := ctx.AppDB().Exec(`INSERT INTO assets(id,project_id,session_id,storage_install_id,storage_file_id,name) VALUES('foreign','other-project',?,11,'100','Private other project')`, session)
	if err != nil {
		t.Fatal(err)
	}
	// Defend even against malformed historical links; normal linking forbids this.
	_, err = ctx.AppDB().Exec(`INSERT INTO asset_sources(project_id,child_asset_id,source_asset_id) VALUES('project-a',?,'foreign')`, child)
	if err != nil {
		t.Fatal(err)
	}
	asset, _ := assetByID(ctx.AppDB(), "project-a", child)
	if err := loadAssetSources(ctx.AppDB(), "project-a", []*Asset{asset}); err != nil {
		t.Fatal(err)
	}
	if len(asset.Sources) != 1 || asset.Sources[0].Name != "" || asset.Sources[0].SessionID != "" {
		t.Fatalf("foreign metadata leaked: %+v", asset.Sources)
	}
}
