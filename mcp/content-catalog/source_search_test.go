package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func sourceFixtureAsset(t *testing.T, ctx *sdk.AppCtx, session, id string) {
	t.Helper()
	_, err := ctx.AppDB().Exec(`INSERT INTO assets(id,project_id,session_id,storage_install_id,storage_file_id,name,kind,review_status,created_at) VALUES(?,?,?,?,?,?, 'image','approved','2026-09-24T10:00:00Z')`, id, "project-a", session, 11, id, "same misleading source filename")
	if err != nil {
		t.Fatal(err)
	}
}
func sourceFixtureLink(t *testing.T, ctx *sdk.AppCtx, child, parent string) {
	t.Helper()
	_, err := ctx.AppDB().Exec(`INSERT INTO asset_sources(project_id,child_asset_id,source_asset_id) VALUES('project-a',?,?)`, child, parent)
	if err != nil {
		t.Fatal(err)
	}
}
func sourceHitIDs(hits []assetSearchHit) []string {
	ids := []string{}
	for _, h := range hits {
		ids = append(ids, h.ID)
	}
	sort.Strings(ids)
	return ids
}
func TestExactSourceSearchFiltersAndDescendants(t *testing.T) {
	a, ctx, _, brand, session := setupCatalog(t)
	root := attach(t, a, ctx, session, 1)
	other := attach(t, a, ctx, session, 3)
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("frame-%d", i)
		sourceFixtureAsset(t, ctx, session, id)
		sourceFixtureLink(t, ctx, id, root)
	}
	for _, id := range []string{"crop-1", "unrelated", "unlinked"} {
		sourceFixtureAsset(t, ctx, session, id)
	}
	sourceFixtureLink(t, ctx, "crop-1", "frame-1")
	sourceFixtureLink(t, ctx, "crop-1", "frame-2") // multiple paths still return one asset
	sourceFixtureLink(t, ctx, "unrelated", other)
	second, err := a.sessionCreate(ctx, map[string]any{"brand_id": brand, "title": "Other session", "session_date": "2026-09-25"})
	if err != nil {
		t.Fatal(err)
	}
	secondID := second.(map[string]any)["session"].(Session).ID
	sourceFixtureAsset(t, ctx, secondID, "crop-2")
	sourceFixtureLink(t, ctx, "crop-2", "crop-1")
	args := map[string]any{"entity_type": "assets", "source_asset_id": root, "kind": "image", "review_status": "approved", "lifecycle": "active", "limit": 100}
	want := []string{"frame-1", "frame-2", "frame-3", "frame-4", "frame-5"}
	if got := sourceHitIDs(assetHits(t, a, ctx, args)); !reflect.DeepEqual(got, want) {
		t.Fatalf("direct: %v", got)
	}
	args["include_descendants"] = true
	hits := assetHits(t, a, ctx, args)
	if got := sourceHitIDs(hits); !reflect.DeepEqual(got, append([]string{"crop-1", "crop-2"}, want...)) {
		t.Fatalf("descendants: %v", got)
	}
	for _, h := range hits {
		if len(h.Sources) == 0 {
			t.Fatalf("missing source links: %+v", h)
		}
	}
	args["session_id"] = session
	if len(assetHits(t, a, ctx, args)) != 6 {
		t.Fatal("session filter lost")
	}
	delete(args, "session_id")
	args["date_from"] = "2026-09-25"
	if got := sourceHitIDs(assetHits(t, a, ctx, args)); !reflect.DeepEqual(got, []string{"crop-2"}) {
		t.Fatalf("date filter: %v", got)
	}
	delete(args, "date_from")
	args["brand_id"] = "another-brand"
	if len(assetHits(t, a, ctx, args)) != 0 {
		t.Fatal("brand filter lost")
	}
	delete(args, "brand_id")
	_, err = a.postsRecord(ctx, map[string]any{"asset_ids": []string{"frame-1"}, "destination": "patreon", "status": "verified_published", "external_post_id": "p1", "actual_at": "2026-09-24T11:00:00Z", "evidence_source": "creator_page_manual"})
	if err != nil {
		t.Fatal(err)
	}
	args["destination"] = "patreon"
	args["availability"] = "not_published"
	if len(assetHits(t, a, ctx, args)) != 6 {
		t.Fatal("publication filter lost")
	}
	delete(args, "destination")
	delete(args, "availability")
	if _, err = ctx.AppDB().Exec(`UPDATE assets SET lifecycle='archived' WHERE id='frame-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err = ctx.AppDB().Exec(`UPDATE sessions SET lifecycle='archived' WHERE id=?`, secondID); err != nil {
		t.Fatal(err)
	}
	if got := sourceHitIDs(assetHits(t, a, ctx, args)); !reflect.DeepEqual(got, []string{"crop-1", "frame-2", "frame-3", "frame-4", "frame-5"}) {
		t.Fatalf("active filter: %v", got)
	}
	args["lifecycle"] = "archived"
	if got := sourceHitIDs(assetHits(t, a, ctx, args)); !reflect.DeepEqual(got, []string{"crop-2", "frame-1"}) {
		t.Fatalf("archive inspection: %v", got)
	}
	args["lifecycle"] = "all"
	if len(assetHits(t, a, ctx, args)) != 7 {
		t.Fatal("all inspection lost")
	}
}

func TestSourceSearchPaginationCyclesAndProjectIsolation(t *testing.T) {
	a, ctx, p, _, session := setupCatalog(t)
	root := attach(t, a, ctx, session, 1)
	p.mediaRows = map[string]map[string]any{}
	for i := 0; i < 125; i++ {
		id := fmt.Sprintf("frame-%03d", i)
		sourceFixtureAsset(t, ctx, session, id)
		sourceFixtureLink(t, ctx, id, root)
		p.mediaRows[id] = map[string]any{"file_id": id, "description": "other"}
	}
	sourceFixtureAsset(t, ctx, session, "crop")
	sourceFixtureLink(t, ctx, "crop", "frame-000")
	sourceFixtureLink(t, ctx, "crop", "frame-001")
	sourceFixtureLink(t, ctx, "frame-000", "crop") // malformed historical cycle
	sourceFixtureLink(t, ctx, root, "crop")        // root must never return
	sourceFixtureAsset(t, ctx, session, "unrelated")
	_, err := ctx.AppDB().Exec(`INSERT INTO assets(id,project_id,session_id,storage_install_id,storage_file_id,name) VALUES('foreign','other-project',?,11,'foreign','private')`, session)
	if err != nil {
		t.Fatal(err)
	}
	sourceFixtureLink(t, ctx, "foreign", root)
	sourceFixtureLink(t, ctx, "unrelated", "foreign")
	args := map[string]any{"entity_type": "assets", "source_asset_id": root, "include_descendants": true, "kind": "image", "limit": 100}
	seen := map[string]bool{}
	pages := 0
	for {
		r, err := a.search(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		page := r.(map[string]any)["assets"].(searchPage[assetSearchHit])
		pages++
		for _, h := range page.Items {
			if seen[h.ID] {
				t.Fatalf("duplicate %s", h.ID)
			}
			seen[h.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		args["cursors"] = map[string]any{"assets": page.NextCursor}
		if pages > 3 {
			t.Fatal("pagination failed to terminate")
		}
	}
	if pages != 2 || len(seen) != 126 || seen[root] || seen["foreign"] || seen["unrelated"] {
		t.Fatalf("pages=%d matches=%d leaked=%v", pages, len(seen), seen)
	}
	delete(args, "cursors")
	args["include_descendants"] = false
	seen = map[string]bool{}
	for {
		r, err := a.search(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		page := r.(map[string]any)["assets"].(searchPage[assetSearchHit])
		for _, h := range page.Items {
			if seen[h.ID] {
				t.Fatalf("duplicate direct result %s", h.ID)
			}
			seen[h.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		args["cursors"] = map[string]any{"assets": page.NextCursor}
	}
	if len(seen) != 125 || seen["crop"] {
		t.Fatalf("direct inventory = %v", seen)
	}
	delete(args, "cursors")
	// Description matching beyond the first hundred candidates retains exact source scope.
	p.mediaRows["frame-000"]["description"] = "rare phrase"
	p.mediaRows["unrelated"] = map[string]any{"file_id": "unrelated", "description": "rare phrase"}
	args["query"] = "rare phrase"
	if got := sourceHitIDs(assetHits(t, a, ctx, args)); !reflect.DeepEqual(got, []string{"frame-000"}) {
		t.Fatalf("description scope: %v", got)
	}
	for _, bad := range []map[string]any{{"source_asset_id": "foreign"}, {"source_asset_id": "missing"}, {"include_descendants": true}, {"source_asset_id": root, "include_descendants": "true"}, {"entity_type": "sessions", "source_asset_id": root}} {
		if _, err := a.search(ctx, bad); err == nil {
			t.Fatalf("expected rejection: %+v", bad)
		}
	}
	if _, err := a.search(ctx.WithProject("other-project"), map[string]any{"source_asset_id": root}); err == nil {
		t.Fatal("cross-project source readable")
	}
}

func TestSourceSearchHTTPParameters(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	root := attach(t, a, ctx, session, 1)
	sourceFixtureAsset(t, ctx, session, "frame")
	sourceFixtureAsset(t, ctx, session, "crop")
	sourceFixtureLink(t, ctx, "frame", root)
	sourceFixtureLink(t, ctx, "crop", "frame")
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	for _, value := range []string{"false", "true", "bogus"} {
		req := httptest.NewRequest("GET", "/search?project_id=project-a&entity_type=assets&source_asset_id="+root+"&include_descendants="+value, nil)
		rec := httptest.NewRecorder()
		a.handleSearch(rec, req)
		if value == "bogus" {
			if rec.Code != 400 {
				t.Fatal("invalid boolean accepted")
			}
			continue
		}
		if rec.Code != 200 {
			t.Fatalf("%d: %s", rec.Code, rec.Body.String())
		}
		var body struct{ Assets searchPage[assetSearchHit] }
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		want := 1
		if value == "true" {
			want = 2
		}
		if len(body.Assets.Items) != want {
			t.Fatalf("HTTP descendants %s: %+v", value, body)
		}
	}
}
