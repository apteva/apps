package main

import (
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func assetHits(t *testing.T, a *App, ctxProject any, args map[string]any) []assetSearchHit {
	t.Helper()
	ctx := ctxProject.(*sdk.AppCtx)
	result, err := a.search(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	return result.(map[string]any)["assets"].(searchPage[assetSearchHit]).Items
}

func TestSearchAcrossSessionsAndPublicationAvailability(t *testing.T) {
	a, ctx, _, brand, firstSession := setupCatalog(t)
	firstAsset := attach(t, a, ctx, firstSession, 1)
	secondAny, err := a.sessionCreate(ctx, map[string]any{"brand_id": brand, "title": "New editorial shoot", "session_date": "2026-09-25"})
	if err != nil {
		t.Fatal(err)
	}
	secondSession := secondAny.(map[string]any)["session"].(Session).ID
	secondAsset := attach(t, a, ctx, secondSession, 2)
	if _, err = a.assetReview(ctx, map[string]any{"asset_id": firstAsset, "review_status": "approved"}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.assetReview(ctx, map[string]any{"asset_id": secondAsset, "review_status": "approved"}); err != nil {
		t.Fatal(err)
	}

	base := map[string]any{"entity_type": "assets", "brand_id": brand, "destination": "instagram", "availability": "ready_to_publish"}
	hits := assetHits(t, a, ctx, base)
	if len(hits) != 2 || hits[0].ID != secondAsset || hits[1].ID != firstAsset {
		t.Fatalf("newest ready assets = %#v", hits)
	}
	if hits[0].SessionTitle != "New editorial shoot" || hits[0].StorageFileID != "2" {
		t.Fatalf("missing search context: %#v", hits[0])
	}
	textHits := assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "query": "editorial"})
	if len(textHits) != 1 || textHits[0].ID != secondAsset {
		t.Fatalf("session title search = %#v", textHits)
	}
	if _, err = a.assetLinkSource(ctx, map[string]any{"child_asset_id": secondAsset, "source_asset_id": firstAsset}); err != nil {
		t.Fatal(err)
	}
	derivatives := assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "lineage": "derivative"})
	if len(derivatives) != 1 || derivatives[0].ID != secondAsset || !derivatives[0].IsDerivative {
		t.Fatalf("derivative search = %#v", derivatives)
	}
	sources := assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "lineage": "source"})
	if len(sources) != 1 || sources[0].ID != firstAsset {
		t.Fatalf("source search = %#v", sources)
	}
	recent := assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "sort": "asset_newest"})
	if len(recent) != 2 || recent[0].ID != secondAsset {
		t.Fatalf("asset sort = %#v", recent)
	}
	limitedAny, err := a.search(ctx, map[string]any{"entity_type": "assets", "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	firstPage := limitedAny.(map[string]any)["assets"].(searchPage[assetSearchHit])
	if len(firstPage.Items) != 1 || firstPage.NextCursor == "" {
		t.Fatalf("first page = %#v", firstPage)
	}
	secondPageAny, err := a.search(ctx, map[string]any{"entity_type": "assets", "limit": 1, "assets_cursor": firstPage.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	secondPage := secondPageAny.(map[string]any)["assets"].(searchPage[assetSearchHit])
	if len(secondPage.Items) != 1 || secondPage.Items[0].ID == firstPage.Items[0].ID {
		t.Fatalf("second page = %#v", secondPage)
	}

	releaseAny, err := a.releaseCreate(ctx, map[string]any{"brand_id": brand, "title": "Instagram clip"})
	if err != nil {
		t.Fatal(err)
	}
	releaseID := releaseAny.(map[string]any)["release"].(*Release).ID
	targetAny, err := a.releaseTargetAdd(ctx, map[string]any{"release_id": releaseID, "destination": "instagram", "account_ref": "account-1", "asset_ids": []string{secondAsset}})
	if err != nil {
		t.Fatal(err)
	}
	targetID := targetAny.(map[string]any)["target"].(ReleaseTarget).ID
	hits = assetHits(t, a, ctx, base)
	if len(hits) != 1 || hits[0].ID != firstAsset {
		t.Fatalf("planned target must reserve asset: %#v", hits)
	}
	hits = assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "destination": "youtube", "availability": "ready_to_publish"})
	if len(hits) != 2 {
		t.Fatalf("other destination remains available: %#v", hits)
	}
	hits = assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "destination": "instagram", "account_ref": "account-2", "availability": "ready_to_publish"})
	if len(hits) != 2 {
		t.Fatalf("other account remains available: %#v", hits)
	}
	hits = assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "destination": "instagram", "availability": "not_published"})
	if len(hits) != 2 {
		t.Fatalf("planned is not published: %#v", hits)
	}
	actual := time.Now().UTC().Format(time.RFC3339)
	if _, err = a.publicationRecord(ctx, map[string]any{"target_id": targetID, "status": "provider_reported_published", "evidence_source": "social_post_list", "external_post_id": "ig-123", "actual_at": actual}); err != nil {
		t.Fatal(err)
	}
	hits = assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "destination": "instagram", "availability": "not_published"})
	if len(hits) != 1 || hits[0].ID != firstAsset {
		t.Fatalf("reported post must block not_published: %#v", hits)
	}
	published := assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "destination": "instagram", "availability": "published"})
	if len(published) != 1 || published[0].ID != secondAsset || len(published[0].Uses) != 1 || published[0].Uses[0].ExternalPostID != "ig-123" {
		t.Fatalf("published evidence = %#v", published)
	}
	if _, err = a.publicationRecord(ctx, map[string]any{"target_id": targetID, "status": "removed", "evidence_source": "social_post_list"}); err != nil {
		t.Fatal(err)
	}
	if hits = assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "destination": "instagram", "availability": "not_published"}); len(hits) != 1 || hits[0].ID != firstAsset {
		t.Fatalf("historical publication must still count: %#v", hits)
	}
	if hits = assetHits(t, a, ctx, base); len(hits) != 1 || hits[0].ID != firstAsset {
		t.Fatalf("historically published asset must not be ready: %#v", hits)
	}
	otherProject := ctx.WithProject("another-project")
	if isolated := assetHits(t, a, otherProject, map[string]any{"entity_type": "assets"}); len(isolated) != 0 {
		t.Fatalf("cross-project results = %#v", isolated)
	}
}

func TestSearchRejectsAmbiguousAvailability(t *testing.T) {
	a, ctx, _, _, _ := setupCatalog(t)
	for _, args := range []map[string]any{
		{"availability": "ready_to_publish"},
		{"destination": "instagram", "availability": "bogus"},
		{"account_ref": "account-1"},
		{"date_from": "tomorrow"},
		{"assets_cursor": "bad"},
		{"limit": 101},
	} {
		if _, err := a.search(ctx, args); err == nil {
			t.Fatalf("expected validation error for %#v", args)
		}
	}
}

func TestSearchSessionsAndReleasesByTextAndBrand(t *testing.T) {
	a, ctx, _, brand, session := setupCatalog(t)
	attach(t, a, ctx, session, 1)
	otherAny, err := a.sessionCreate(ctx, map[string]any{"brand_id": brand, "title": "Editorial portrait", "session_date": "2026-09-25", "notes": "Autumn campaign"})
	if err != nil {
		t.Fatal(err)
	}
	other := otherAny.(map[string]any)["session"].(Session).ID
	releaseAny, err := a.releaseCreate(ctx, map[string]any{"brand_id": brand, "title": "Autumn Instagram launch", "phase": "teaser"})
	if err != nil {
		t.Fatal(err)
	}
	release := releaseAny.(map[string]any)["release"].(*Release)
	if _, err = a.releaseTargetAdd(ctx, map[string]any{"release_id": release.ID, "destination": "instagram", "asset_ids": []string{attach(t, a, ctx, other, 2)}}); err != nil {
		t.Fatal(err)
	}
	result, err := a.search(ctx, map[string]any{"entity_type": "all", "query": "Autumn", "brand_id": brand})
	if err != nil {
		t.Fatal(err)
	}
	sections := result.(map[string]any)
	sessions := sections["sessions"].(searchPage[sessionSearchHit]).Items
	releases := sections["releases"].(searchPage[releaseSearchHit]).Items
	if len(sessions) != 1 || sessions[0].ID != other || sessions[0].AssetCount != 1 {
		t.Fatalf("session text results = %#v", sessions)
	}
	if len(releases) != 1 || releases[0].ID != release.ID || releases[0].TargetCount != 1 {
		t.Fatalf("release text results = %#v", releases)
	}
	result, err = a.search(ctx, map[string]any{"entity_type": "releases", "destination": "youtube"})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(map[string]any)["releases"].(searchPage[releaseSearchHit]).Items; len(got) != 0 {
		t.Fatalf("wrong destination = %#v", got)
	}
	result, err = a.search(ctx, map[string]any{"entity_type": "sessions", "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	first := result.(map[string]any)["sessions"].(searchPage[sessionSearchHit])
	if first.NextCursor == "" {
		t.Fatalf("missing session cursor: %#v", first)
	}
	result, err = a.search(ctx, map[string]any{"entity_type": "sessions", "limit": 1, "cursors": map[string]any{"sessions": first.NextCursor}})
	if err != nil {
		t.Fatal(err)
	}
	second := result.(map[string]any)["sessions"].(searchPage[sessionSearchHit])
	if len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("session pagination = %#v", second)
	}
}
