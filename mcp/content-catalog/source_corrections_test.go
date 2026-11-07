package main

import (
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"reflect"
	"strings"
	"testing"
)

func sourceState(t *testing.T, ctx *sdk.AppCtx, id string) (int64, []SourceEdge, int) {
	t.Helper()
	a, err := assetByID(ctx.AppDB(), "project-a", id)
	if err != nil {
		t.Fatal(err)
	}
	if err = loadAssetSources(ctx.AppDB(), "project-a", []*Asset{a}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM asset_source_events WHERE asset_id=?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return a.Revision, sourceEdges(a.Sources), n
}
func replaceArgs(id, key string, rev int64, before, after []SourceEdge) map[string]any {
	return map[string]any{"child_asset_id": id, "idempotency_key": key, "reason": "Reviewed correction", "expected_revision": rev, "expected_sources": before, "sources": after}
}
func TestSourceRepairGuardsAuditAndReplay(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	master := attach(t, a, ctx, session, 1)
	frame := attach(t, a, ctx, session, 2)
	child := attach(t, a, ctx, session, 3)
	for _, args := range []map[string]any{{"child_asset_id": frame, "source_asset_id": master, "relation": "extracted_frame"}, {"child_asset_id": child, "source_asset_id": frame, "relation": "derived_from", "media_render_id": 7795}, {"child_asset_id": child, "source_asset_id": master, "relation": "derived_from", "source_order": 1, "media_render_id": 7795}} {
		if _, err := a.assetLinkSource(ctx, args); err != nil {
			t.Fatal(err)
		}
	}
	rev, before, n := sourceState(t, ctx, child)
	if len(before) != 2 {
		t.Fatal(before)
	}
	after := []SourceEdge{{frame, "cropped", 0, 7795}}
	args := replaceArgs(child, "p02", rev, before, after)
	b, _ := json.Marshal(args)
	var wire map[string]any
	json.Unmarshal(b, &wire)
	r, err := a.assetSourcesReplace(ctx, wire)
	if err != nil {
		t.Fatal(err)
	}
	out := r.(map[string]any)
	if out["status"] != "replaced" || out["revision"] != rev+1 || len(out["sources"].([]AssetSource)) != 1 {
		t.Fatal(out)
	}
	next, saved, count := sourceState(t, ctx, child)
	if next != rev+1 || count != n+1 || !reflect.DeepEqual(saved, after) {
		t.Fatal(next, saved, count)
	}
	r, err = a.assetGet(ctx, map[string]any{"id": child})
	if err != nil {
		t.Fatal(err)
	}
	detail := r.(map[string]any)
	asset := detail["asset"].(*Asset)
	if asset.StorageFileID != "3" || asset.SHA256 != "sha-3" {
		t.Fatal("file changed")
	}
	ancestors := map[string]bool{}
	for _, v := range asset.Ancestors {
		ancestors[v.ID] = true
	}
	if !ancestors[master] || !ancestors[frame] {
		t.Fatal("lost master ancestry")
	}
	history := detail["source_history"].([]map[string]any)
	if !reflect.DeepEqual(history[0]["before"], before) || !reflect.DeepEqual(history[0]["after"], after) {
		t.Fatal(history)
	}
	r, err = a.assetSourcesReplace(ctx, wire)
	if err != nil {
		t.Fatal(err)
	}
	out = r.(map[string]any)
	if out["replayed"] != true || out["status"] != "unchanged" {
		t.Fatal(out)
	}
	check := func() {
		v, s, e := sourceState(t, ctx, child)
		if v != next || e != count || !reflect.DeepEqual(s, saved) {
			t.Fatal("state changed")
		}
	}
	check()
	args["idempotency_key"] = "stale"
	if _, err = a.assetSourcesReplace(ctx, args); err == nil || !strings.Contains(err.Error(), "revision conflict") {
		t.Fatal(err)
	}
	check()
	args = replaceArgs(child, "wrong-list", next, before, after)
	if _, err = a.assetSourcesReplace(ctx, args); err == nil || !strings.Contains(err.Error(), "source list conflict") {
		t.Fatal(err)
	}
	check()
	args = replaceArgs(child, "p02", rev, before, []SourceEdge{})
	if _, err = a.assetSourcesReplace(ctx, args); err == nil || !strings.Contains(err.Error(), "different request") {
		t.Fatal(err)
	}
	check()
	// Guard the no-op too, and preserve the key so an identical retry succeeds.
	args = replaceArgs(child, "already-correct", next, after, after)
	if _, err = a.assetSourcesReplace(ctx, args); err != nil {
		t.Fatal(err)
	}
	if _, err = a.assetSourcesReplace(ctx, args); err != nil {
		t.Fatal(err)
	}
	v, s, e := sourceState(t, ctx, child)
	if v != next || !reflect.DeepEqual(s, saved) || e != count+1 {
		t.Fatal("no-op changed revision")
	}
}
func TestSourceLinkTruthfulResults(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	p := attach(t, a, ctx, session, 1)
	c := attach(t, a, ctx, session, 2)
	args := map[string]any{"child_asset_id": c, "source_asset_id": p, "relation": "cropped", "source_order": 0, "media_render_id": 7}
	r, err := a.assetLinkSource(ctx, args)
	if err != nil || r.(map[string]any)["status"] != "inserted" {
		t.Fatal(r, err)
	}
	rev, before, n := sourceState(t, ctx, c)
	r, err = a.assetLinkSource(ctx, args)
	if err != nil || r.(map[string]any)["status"] != "unchanged" {
		t.Fatal(r, err)
	}
	for _, field := range []string{"relation", "source_order", "media_render_id"} {
		changed := map[string]any{}
		for k, v := range args {
			changed[k] = v
		}
		if field == "relation" {
			changed[field] = "derived_from"
		} else {
			changed[field] = 99
		}
		r, err = a.assetLinkSource(ctx, changed)
		if err != nil {
			t.Fatal(err)
		}
		out := r.(map[string]any)
		if out["linked"] != false || out["status"] != "conflict" || len(out["sources"].([]AssetSource)) != 1 {
			t.Fatal(out)
		}
	}
	v, s, e := sourceState(t, ctx, c)
	if v != rev || e != n || !reflect.DeepEqual(s, before) {
		t.Fatal("unchanged/conflict wrote state")
	}
}
func TestSourceReplacementCycleOwnershipRollbackAndRemoval(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	root := attach(t, a, ctx, session, 1)
	mid := attach(t, a, ctx, session, 2)
	leaf := attach(t, a, ctx, session, 3)
	other := attach(t, a, ctx, session, 4)
	for _, pair := range [][2]string{{mid, root}, {leaf, mid}} {
		if _, err := a.assetLinkSource(ctx, map[string]any{"child_asset_id": pair[0], "source_asset_id": pair[1]}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO assets(id,project_id,session_id,storage_install_id,storage_file_id,name) VALUES('foreign','other-project',?,11,'100','private')`, session); err != nil {
		t.Fatal(err)
	}
	rev, before, n := sourceState(t, ctx, root)
	for key, id := range map[string]string{"self": root, "cycle": leaf, "foreign": "foreign", "missing": "missing"} {
		if _, err := a.assetSourcesReplace(ctx, replaceArgs(root, key, rev, before, []SourceEdge{{id, "derived", 0, 0}})); err == nil {
			t.Fatal("accepted", key)
		}
		v, s, e := sourceState(t, ctx, root)
		if v != rev || e != n || !reflect.DeepEqual(s, before) {
			t.Fatal("changed state", key)
		}
	}
	if _, err := a.assetLinkSource(ctx, map[string]any{"child_asset_id": root, "source_asset_id": leaf}); err == nil {
		t.Fatal("link cycle accepted")
	}
	if _, err := a.assetSourcesReplace(ctx, replaceArgs("foreign", "foreign-child", 1, []SourceEdge{}, []SourceEdge{{root, "derived", 0, 0}})); err == nil {
		t.Fatal("foreign child")
	}
	rev, before, n = sourceState(t, ctx, leaf)
	desired := []SourceEdge{{root, "cropped", 0, 7}, {other, "cropped", 1, 7}}
	// Force failure after deletion/insertion, then after revision advancement.
	for _, trigger := range []string{`CREATE TRIGGER reject_insert BEFORE INSERT ON asset_sources WHEN NEW.source_asset_id='` + other + `' BEGIN SELECT RAISE(ABORT,'forced insert failure'); END`, `CREATE TRIGGER reject_audit BEFORE INSERT ON asset_source_events BEGIN SELECT RAISE(ABORT,'forced audit failure'); END`} {
		if _, err := ctx.AppDB().Exec(trigger); err != nil {
			t.Fatal(err)
		}
		if _, err := a.assetSourcesReplace(ctx, replaceArgs(leaf, "rollback", rev, before, desired)); err == nil {
			t.Fatal("expected forced failure")
		}
		v, s, e := sourceState(t, ctx, leaf)
		if v != rev || e != n || !reflect.DeepEqual(s, before) {
			t.Fatal("partial transaction survived")
		}
		ctx.AppDB().Exec(`DROP TRIGGER IF EXISTS reject_insert`)
		ctx.AppDB().Exec(`DROP TRIGGER IF EXISTS reject_audit`)
	}
	args := replaceArgs(leaf, "remove", rev, before, []SourceEdge{})
	if _, err := a.assetSourcesReplace(ctx, args); err != nil {
		t.Fatal(err)
	}
	if _, err := a.assetSourcesReplace(ctx, args); err != nil {
		t.Fatal(err)
	}
	v, s, e := sourceState(t, ctx, leaf)
	if v != rev+1 || len(s) != 0 || e != n+1 {
		t.Fatal("removal failed")
	}
	if _, err := a.assetLinkSource(ctx, map[string]any{"child_asset_id": leaf, "source_asset_id": root}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.assetSourcesReplace(ctx, args); err == nil || !strings.Contains(err.Error(), "retry conflict") {
		t.Fatal("obsolete replay", err)
	}
}
func TestSourceReplacementStrictGuardsAndMCPMetadata(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	child := attach(t, a, ctx, session, 1)
	parent := attach(t, a, ctx, session, 2)
	rev, before, _ := sourceState(t, ctx, child)
	valid := replaceArgs(child, "validate", rev, before, []SourceEdge{{parent, "cropped", 0, 1}})
	copyArgs := func() map[string]any {
		out := map[string]any{}
		for k, v := range valid {
			out[k] = v
		}
		return out
	}
	for _, missing := range []string{"expected_revision", "expected_sources", "sources", "idempotency_key", "reason"} {
		args := copyArgs()
		delete(args, missing)
		if _, err := a.assetSourcesReplace(ctx, args); err == nil {
			t.Fatal("missing", missing)
		}
	}
	for _, edges := range []any{nil, []SourceEdge{{parent, "cropped", -1, 0}}, []SourceEdge{{parent, "", 0, 0}}, []SourceEdge{{parent, "cropped", 0, 0}, {parent, "cropped", 1, 0}}, []map[string]any{{"asset_id": parent, "relation": "cropped", "source_order": 0}}, []map[string]any{{"asset_id": parent, "relation": "cropped", "source_order": 0.5, "media_render_id": 0}}} {
		args := copyArgs()
		args["sources"] = edges
		if _, err := a.assetSourcesReplace(ctx, args); err == nil {
			t.Fatal("invalid", edges)
		}
	}
	for _, tool := range a.MCPTools() {
		if tool.Name == "content_catalog_assets_sources_replace" {
			if tool.Annotations["readOnlyHint"] != false || tool.Annotations["idempotentHint"] != true || len(tool.InputSchema["required"].([]string)) != 6 {
				t.Fatal("metadata")
			}
			return
		}
	}
	t.Fatal("missing tool")
}
