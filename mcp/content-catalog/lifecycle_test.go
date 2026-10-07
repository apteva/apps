package main

import (
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"reflect"
	"testing"
)

func lifecycleArgs(ids []string, revision int64, op string) map[string]any {
	r := map[string]int64{}
	for _, id := range ids {
		r[id] = revision
	}
	return map[string]any{"asset_ids": ids, "expected_revisions": r, "operation_id": op, "reason": "Superseded recordings"}
}
func sessionAssets(t *testing.T, a *App, ctx *sdk.AppCtx, id, scope string) []Asset {
	t.Helper()
	out, e := a.assetsList(ctx, map[string]any{"session_id": id, "lifecycle": scope})
	if e != nil {
		t.Fatal(e)
	}
	return out.(map[string]any)["assets"].([]Asset)
}
func TestHollyArchiveMoveRestorePreservesIdentities(t *testing.T) {
	a, ctx, p, brand, working := setupCatalog(t)
	ids := []string{}
	for i := int64(1); i <= 9; i++ {
		ids = append(ids, attach(t, a, ctx, working, i))
	}
	old := ids[6:]
	archiveResult, e := a.sessionCreate(ctx, map[string]any{"brand_id": brand, "title": "Holly — Alternate Takes"})
	if e != nil {
		t.Fatal(e)
	}
	archive := archiveResult.(map[string]any)["session"].(Session)
	if _, e = a.lifecycleMutation("session", "archive")(ctx, map[string]any{"session_id": archive.ID, "expected_revision": 1, "operation_id": "archive-session", "reason": "Alternate takes"}); e != nil {
		t.Fatal(e)
	}
	// Evidence and source links remain attached to the same stable identities.
	ctx.AppDB().Exec(`INSERT INTO asset_sources(project_id,child_asset_id,source_asset_id,relation,media_render_id) VALUES(?,?,?,?,?)`, "project-a", old[1], old[0], "clip", 71)
	if _, e = a.postsRecord(ctx, map[string]any{"asset_ids": old, "destination": "patreon", "status": "planned"}); e != nil {
		t.Fatal(e)
	}
	before := map[string]*Asset{}
	for _, id := range ids {
		x, e := assetByID(ctx.AppDB(), "project-a", id)
		if e != nil {
			t.Fatal(e)
		}
		before[id] = x
	}
	args := lifecycleArgs(old, 1, "holly-archive")
	args["destination_session_id"] = archive.ID
	args["destination_revision"] = int64(2)
	first, e := a.lifecycleMutation("asset", "archive")(ctx, args)
	if e != nil {
		t.Fatal(e)
	}
	again, e := a.lifecycleMutation("asset", "archive")(ctx, args)
	if e != nil {
		t.Fatal(e)
	}
	f, _ := json.Marshal(first)
	g, _ := json.Marshal(again)
	if string(f) != string(g) {
		t.Fatal("retry changed result")
	}
	if len(sessionAssets(t, a, ctx, working, "")) != 6 || len(sessionAssets(t, a, ctx, archive.ID, "")) != 0 || len(sessionAssets(t, a, ctx, archive.ID, "archived")) != 3 {
		t.Fatal("default/archive visibility")
	}
	for _, id := range old {
		x, e := assetByID(ctx.AppDB(), "project-a", id)
		if e != nil {
			t.Fatal(e)
		}
		if x.Eligible || x.Lifecycle != "archived" || x.OriginalSessionID != working || x.StorageFileID != before[id].StorageFileID || x.ReviewStatus != before[id].ReviewStatus {
			t.Fatalf("identity/review changed: %+v", x)
		}
		if _, e = a.hostingRequest(ctx, map[string]any{"asset_id": id}); e == nil {
			t.Fatal("archived hosting allowed")
		}
	}
	out, e := a.search(ctx, map[string]any{"entity_type": "assets"})
	if e != nil {
		t.Fatal(e)
	}
	if len(out.(map[string]any)["assets"].(searchPage[assetSearchHit]).Items) != 6 {
		t.Fatal("search leaked archive")
	}
	out, e = a.search(ctx, map[string]any{"entity_type": "assets", "lifecycle": "archived"})
	if e != nil {
		t.Fatal(e)
	}
	if len(out.(map[string]any)["assets"].(searchPage[assetSearchHit]).Items) != 3 {
		t.Fatal("archive search")
	}
	for _, scope := range []string{"active", "archived", "all"} {
		listed, err := a.postsList(ctx, map[string]any{"lifecycle": scope})
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if scope == "active" {
			want = 0
		}
		if len(listed.(map[string]any)["posts"].([]Publication)) != want {
			t.Fatalf("post archive visibility: %s", scope)
		}
	}
	if _, e = a.postsRecord(ctx, map[string]any{"asset_ids": old, "destination": "instagram", "status": "planned"}); e == nil {
		t.Fatal("archived plan accepted")
	}
	restore := lifecycleArgs(old, 2, "holly-restore")
	restore["destination_revision"] = int64(1)
	if _, e = a.lifecycleMutation("asset", "restore")(ctx, restore); e != nil {
		t.Fatal(e)
	}
	if len(sessionAssets(t, a, ctx, working, "")) != 9 {
		t.Fatal("restore did not return all originals")
	}
	for _, id := range ids {
		x, e := assetByID(ctx.AppDB(), "project-a", id)
		if e != nil || x.StorageFileID != before[id].StorageFileID || x.SessionID != working {
			t.Fatal("restore duplicated/moved bytes")
		}
	}
	var sources, posts, audits, count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM asset_sources`).Scan(&sources)
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM post_assets`).Scan(&posts)
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM assets`).Scan(&count)
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM lifecycle_events WHERE entity_type='asset'`).Scan(&audits)
	if sources != 1 || posts != 3 || count != 9 || audits != 6 || p.starts != 0 {
		t.Fatalf("preservation: %d %d %d %d", sources, posts, count, audits)
	}
}
func TestLifecycleAtomicConflictsAndEligibility(t *testing.T) {
	a, ctx, _, brand, s := setupCatalog(t)
	x := attach(t, a, ctx, s, 1)
	y := attach(t, a, ctx, s, 2)
	args := lifecycleArgs([]string{x, y}, 1, "bad-revision")
	args["expected_revisions"] = map[string]int64{x: 1, y: 99}
	if _, e := a.lifecycleMutation("asset", "archive")(ctx, args); e == nil {
		t.Fatal("expected conflict")
	}
	asset, _ := assetByID(ctx.AppDB(), "project-a", x)
	if asset.Lifecycle != "active" || asset.Revision != 1 {
		t.Fatal("partial batch committed")
	}
	if _, e := a.lifecycleMutation("session", "archive")(ctx, map[string]any{"session_id": s, "expected_revision": 1, "operation_id": "parent-archive", "reason": "Closed"}); e != nil {
		t.Fatal(e)
	}
	asset, _ = assetByID(ctx.AppDB(), "project-a", x)
	if asset.Eligible || asset.Lifecycle != "active" || asset.SessionLifecycle != "archived" {
		t.Fatal("parent eligibility")
	}
	out, e := a.assetsEligibility(ctx, map[string]any{"file_ids": []string{"1"}, "storage_install_id": 11})
	if e != nil || out.(map[string]any)["items"].([]map[string]any)[0]["eligible"] != false {
		t.Fatal("file archive lookup")
	}
	r, e := a.sessionCreate(ctx, map[string]any{"brand_id": brand, "title": "Active copy"})
	if e != nil {
		t.Fatal(e)
	}
	dest := r.(map[string]any)["session"].(Session)
	attach(t, a, ctx, dest.ID, 1)
	out, e = a.assetsEligibility(ctx, map[string]any{"file_ids": []string{"1"}, "storage_install_id": 11})
	item := out.(map[string]any)["items"].([]map[string]any)[0]
	if e != nil || item["requires_asset_context"] != true || item["eligible"] != false {
		t.Fatal("mixed contexts must not guess")
	}
	move := lifecycleArgs([]string{x}, 1, "duplicate-move")
	move["destination_session_id"] = dest.ID
	move["destination_revision"] = int64(1)
	if _, e = a.lifecycleMutation("asset", "move")(ctx, move); e == nil {
		t.Fatal("duplicate destination accepted")
	}
	asset, _ = assetByID(ctx.AppDB(), "project-a", x)
	if asset.SessionID != s {
		t.Fatal("duplicate conflict moved original")
	}
	archive := lifecycleArgs([]string{y}, 1, "retry")
	if _, e = a.lifecycleMutation("asset", "archive")(ctx, archive); e != nil {
		t.Fatal(e)
	}
	archive["reason"] = "changed"
	if _, e = a.lifecycleMutation("asset", "archive")(ctx, archive); e == nil {
		t.Fatal("operation ID reused with another request")
	}
	hist, e := a.lifecycleHistory(ctx, map[string]any{"entity_type": "asset", "id": y})
	if e != nil || !reflect.DeepEqual(len(hist.(map[string]any)["events"].([]map[string]any)), 1) {
		t.Fatal("audit duplicates")
	}
}

func TestRestoreBatchToDifferentOriginalSessions(t *testing.T) {
	a, ctx, _, brand, first := setupCatalog(t)
	created, err := a.sessionCreate(ctx, map[string]any{"brand_id": brand, "title": "Other production session"})
	if err != nil {
		t.Fatal(err)
	}
	second := created.(map[string]any)["session"].(Session)
	created, err = a.sessionCreate(ctx, map[string]any{"brand_id": brand, "title": "Archive"})
	if err != nil {
		t.Fatal(err)
	}
	archive := created.(map[string]any)["session"].(Session)
	x := attach(t, a, ctx, first, 1)
	y := attach(t, a, ctx, second.ID, 2)
	args := lifecycleArgs([]string{x, y}, 1, "multi-origin-archive")
	args["destination_session_id"] = archive.ID
	args["destination_revision"] = int64(1)
	if _, err = a.lifecycleMutation("asset", "archive")(ctx, args); err != nil {
		t.Fatal(err)
	}
	restore := lifecycleArgs([]string{x, y}, 2, "multi-origin-restore")
	restore["destination_revisions"] = map[string]int64{first: 1, second.ID: 1}
	if _, err = a.lifecycleMutation("asset", "restore")(ctx, restore); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{x: first, y: second.ID} {
		asset, err := assetByID(ctx.AppDB(), "project-a", id)
		if err != nil || asset.SessionID != want || !asset.Eligible {
			t.Fatalf("restoration: %+v %v", asset, err)
		}
	}
}
