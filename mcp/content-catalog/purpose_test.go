package main

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestPurposeSelectionAndHiddenAncestry(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	root := attach(t, a, ctx, session, 1)
	original := attach(t, a, ctx, session, 3)
	for _, id := range []string{"helper", "crop"} {
		sourceFixtureAsset(t, ctx, session, id)
	}
	sourceFixtureLink(t, ctx, root, original)
	sourceFixtureLink(t, ctx, "helper", root)
	sourceFixtureLink(t, ctx, "crop", "helper")
	_, err := ctx.AppDB().Exec(`UPDATE assets SET lifecycle='archived' WHERE id=?`, original)
	if err != nil {
		t.Fatal(err)
	}
	// Explicit purpose edits with revisions; no filenames or links drive purpose.
	for id, role := range map[string]string{root: "main", "helper": "intermediate", "crop": "derivative"} {
		v, _ := assetByID(ctx.AppDB(), "project-a", id)
		if _, err := a.assetLabelsUpdate(ctx, map[string]any{"asset_ids": []string{id}, "role": role, "output_type": "portrait", "expected_revisions": map[string]int64{id: v.Revision}}); err != nil {
			t.Fatal(err)
		}
	}
	base := map[string]any{"entity_type": "assets", "source_asset_id": root, "include_descendants": true}
	if got := sourceHitIDs(assetHits(t, a, ctx, base)); !reflect.DeepEqual(got, []string{"crop"}) {
		t.Fatalf("selection %v", got)
	}
	base["include_intermediates"] = true
	if got := sourceHitIDs(assetHits(t, a, ctx, base)); !reflect.DeepEqual(got, []string{"crop", "helper"}) {
		t.Fatalf("inspection %v", got)
	}
	mains := assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "role": "main", "kind": "video"})
	if len(mains) != 1 || mains[0].ID != root || len(mains[0].Sources) != 1 {
		t.Fatalf("main with source: %+v", mains)
	}
	detail, err := a.assetGet(ctx, map[string]any{"id": "crop"})
	if err != nil {
		t.Fatal(err)
	}
	crop := detail.(map[string]any)["asset"].(*Asset)
	if len(crop.Ancestors) != 3 || crop.Role != "derivative" {
		t.Fatalf("hidden context: %+v", crop)
	}
	helper, err := assetByID(ctx.AppDB(), "project-a", "helper")
	if err != nil {
		t.Fatal(err)
	}
	if helper.Eligible {
		t.Fatal("intermediate eligible")
	}
	if requireActiveAsset(ctx.AppDB(), "project-a", "helper") == nil {
		t.Fatal("hosting guard allows intermediate")
	}
	if _, err := a.postsRecord(ctx, map[string]any{"asset_ids": []string{"helper"}, "destination": "patreon", "status": "planned"}); err == nil {
		t.Fatal("intermediate scheduled")
	}
	result, err := a.assetsList(ctx, map[string]any{"session_id": session})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.(map[string]any)["assets"].([]Asset)) != 2 {
		t.Fatal("default session exposed helper/original")
	}
	// HTTP applies the same inspection flag and rejects invalid boolean values.
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	for _, flag := range []string{"false", "true", "bogus"} {
		rec := httptest.NewRecorder()
		a.handleList("content_catalog_assets_list")(rec, httptest.NewRequest("GET", "/assets?project_id=project-a&session_id="+session+"&include_intermediates="+flag, nil))
		if flag == "bogus" {
			if rec.Code != 400 {
				t.Fatal("invalid boolean")
			}
			continue
		}
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		var body struct{ Assets []Asset }
		json.Unmarshal(rec.Body.Bytes(), &body)
		want := 2
		if flag == "true" {
			want = 3
		}
		if len(body.Assets) != want {
			t.Fatalf("HTTP selection: %+v", body)
		}
	}
}
func TestPurposeBulkRevisionAndProjectSafety(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	first := attach(t, a, ctx, session, 1)
	second := attach(t, a, ctx, session, 2)
	f, _ := assetByID(ctx.AppDB(), "project-a", first)
	s, _ := assetByID(ctx.AppDB(), "project-a", second)
	bad := map[string]any{"asset_ids": []string{first, second}, "role": "main", "expected_revisions": map[string]int64{first: f.Revision, second: s.Revision + 1}}
	if _, err := a.assetLabelsUpdate(ctx, bad); err == nil {
		t.Fatal("stale bulk edit accepted")
	}
	f, _ = assetByID(ctx.AppDB(), "project-a", first)
	if f.Role != "unspecified" {
		t.Fatal("partial update")
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM asset_purpose_events`).Scan(&count)
	if count != 0 {
		t.Fatal("rolled-back audit leaked")
	}
	for _, args := range []map[string]any{{"asset_ids": []string{first}, "role": "main"}, {"asset_ids": []string{first}, "role": "invalid", "expected_revisions": map[string]int64{first: f.Revision}}, {"entity_type": "assets", "role": "main' OR 1=1--"}, {"entity_type": "assets", "output_type": "invalid token"}, {"include_intermediates": "true"}} {
		var err error
		if args["asset_ids"] != nil {
			_, err = a.assetLabelsUpdate(ctx, args)
		} else {
			_, err = a.search(ctx, args)
		}
		if err == nil {
			t.Fatalf("invalid request %+v", args)
		}
	}
	if _, err := a.assetLabelsUpdate(ctx.WithProject("foreign"), map[string]any{"asset_ids": []string{first}, "role": "main", "expected_revisions": map[string]int64{first: f.Revision}}); err == nil {
		t.Fatal("foreign edit")
	}
}

func TestPurposeAncestryDoesNotTraverseForeignNodes(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	root := attach(t, a, ctx, session, 1)
	sourceFixtureAsset(t, ctx, session, "crop")
	_, err := ctx.AppDB().Exec(`INSERT INTO assets(id,project_id,session_id,storage_install_id,storage_file_id,name) VALUES('foreign','other-project',?,11,'foreign','private')`, session)
	if err != nil {
		t.Fatal(err)
	}
	sourceFixtureLink(t, ctx, "crop", "foreign")
	sourceFixtureLink(t, ctx, "foreign", root)
	detail, err := a.assetGet(ctx, map[string]any{"id": "crop"})
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.(map[string]any)["asset"].(*Asset).Ancestors) != 0 {
		t.Fatal("foreign path leaked into provenance")
	}
}
