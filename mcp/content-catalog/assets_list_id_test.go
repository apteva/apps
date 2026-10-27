package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestAssetsListExactIDDoesNotFallBackOrCrossScope(t *testing.T) {
	a, ctx, _, brand, session := setupCatalog(t)
	first := attach(t, a, ctx, session, 1)
	second := attach(t, a, ctx, session, 2)
	created, err := a.sessionCreate(ctx, map[string]any{"brand_id": brand, "title": "Other session"})
	if err != nil {
		t.Fatal(err)
	}
	otherSession := created.(map[string]any)["session"].(Session).ID
	other := attach(t, a, ctx, otherSession, 3)
	if _, err = ctx.AppDB().Exec(`INSERT INTO assets(id,project_id,session_id,storage_install_id,storage_file_id,name) VALUES('foreign','other-project',?,11,'4','Foreign')`, session); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id   string
		want []string
	}{{first, []string{first}}, {second, []string{second}}, {" " + second + " ", []string{second}}, {other, []string{}}, {"foreign", []string{}}, {"missing", []string{}}, {"1", []string{}}, {"%", []string{}}} {
		r, e := a.assetsList(ctx, map[string]any{"session_id": session, "id": tc.id})
		if e != nil {
			t.Fatal(e)
		}
		items := r.(map[string]any)["assets"].([]Asset)
		ids := []string{}
		for _, item := range items {
			ids = append(ids, item.ID)
		}
		if !reflect.DeepEqual(ids, tc.want) {
			t.Fatalf("id=%q: got %v want %v", tc.id, ids, tc.want)
		}
	}
	r, err := a.assetsList(ctx, map[string]any{"session_id": session})
	if err != nil || len(r.(map[string]any)["assets"].([]Asset)) != 2 {
		t.Fatal("unfiltered behavior changed", r, err)
	}
	for _, id := range []any{"", "  ", nil, true, int64(1), []string{first}} {
		if _, err = a.assetsList(ctx, map[string]any{"session_id": session, "id": id}); err == nil {
			t.Fatalf("invalid id silently ignored: %#v", id)
		}
	}
	if _, err = a.assetsList(ctx, map[string]any{"id": first}); err == nil {
		t.Fatal("session must remain required; use assets_get for direct lookup")
	}
}
func TestAssetsListIDRetainsVisibilityAndMetadata(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	parent := attach(t, a, ctx, session, 1)
	child := attach(t, a, ctx, session, 2)
	if _, err := a.assetLinkSource(ctx, map[string]any{"child_asset_id": child, "source_asset_id": parent, "relation": "cropped"}); err != nil {
		t.Fatal(err)
	}
	read := func(extra map[string]any) []Asset {
		t.Helper()
		args := map[string]any{"session_id": session, "id": child}
		for k, v := range extra {
			args[k] = v
		}
		r, err := a.assetsList(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		return r.(map[string]any)["assets"].([]Asset)
	}
	items := read(nil)
	if len(items) != 1 || items[0].StorageFileID != "2" || len(items[0].Sources) != 1 || items[0].Sources[0].AssetID != parent || items[0].MediaStatus != "completed" {
		t.Fatal(items)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE assets SET role='intermediate' WHERE id=?`, child); err != nil {
		t.Fatal(err)
	}
	if len(read(nil)) != 0 || len(read(map[string]any{"include_intermediates": true})) != 1 || len(read(map[string]any{"role": "main"})) != 0 {
		t.Fatal("purpose visibility bypassed")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE assets SET lifecycle='archived' WHERE id=?`, child); err != nil {
		t.Fatal(err)
	}
	if len(read(map[string]any{"include_intermediates": true})) != 0 || len(read(map[string]any{"lifecycle": "archived", "include_intermediates": true})) != 1 {
		t.Fatal("asset lifecycle bypassed")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE assets SET role='unspecified',lifecycle='active' WHERE id=?`, child); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE sessions SET lifecycle='archived' WHERE id=?`, session); err != nil {
		t.Fatal(err)
	}
	if len(read(nil)) != 0 || len(read(map[string]any{"lifecycle": "all"})) != 1 {
		t.Fatal("session lifecycle bypassed")
	}
	detail, err := a.assetGet(ctx, map[string]any{"id": child})
	if err != nil || detail.(map[string]any)["asset"].(*Asset).ID != child {
		t.Fatal("direct inspection changed", detail, err)
	}
}
func TestAssetsListIDAppliedBeforeSessionLimit(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	wanted := attach(t, a, ctx, session, 1)
	ctx.AppDB().Exec(`UPDATE assets SET created_at='2000-01-01T00:00:00Z' WHERE id=?`, wanted)
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 201; i++ {
		_, err = tx.Exec(`INSERT INTO assets(id,project_id,session_id,storage_install_id,storage_file_id,name,created_at) VALUES(?,'project-a',?,11,?,?, '2026-10-27T00:00:00Z')`, fmt.Sprintf("padding-%03d", i), session, fmt.Sprint(i+100), fmt.Sprintf("File %d", i))
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	r, err := a.assetsList(ctx, map[string]any{"session_id": session, "id": wanted})
	if err != nil {
		t.Fatal(err)
	}
	items := r.(map[string]any)["assets"].([]Asset)
	if len(items) != 1 || items[0].ID != wanted {
		t.Fatal("filter applied after the list cap", items)
	}
}
func TestAssetsListExactIDHTTPAndToolContract(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	first := attach(t, a, ctx, session, 1)
	attach(t, a, ctx, session, 2)
	previous := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = previous }()
	handler := a.handleList("content_catalog_assets_list")
	for _, tc := range []struct {
		suffix        string
		status, count int
	}{{"&id=" + first, 200, 1}, {"&id=missing", 200, 0}, {"", 200, 2}, {"&id=", 400, 0}, {"&id=%20", 400, 0}, {"&id=" + first + "&id=missing", 400, 0}} {
		rec := httptest.NewRecorder()
		handler(rec, httptest.NewRequest("GET", "/assets?project_id=project-a&session_id="+session+tc.suffix, nil))
		if rec.Code != tc.status {
			t.Fatal(tc.suffix, rec.Code, rec.Body.String())
		}
		if rec.Code == 200 {
			var data struct {
				Assets []Asset `json:"assets"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
				t.Fatal(err)
			}
			if len(data.Assets) != tc.count {
				t.Fatal(tc.suffix, data)
			}
			if tc.count == 1 && data.Assets[0].ID != first {
				t.Fatal(data)
			}
		}
	}
	for _, tool := range a.MCPTools() {
		if tool.Name != "content_catalog_assets_list" {
			continue
		}
		if tool.Annotations["readOnlyHint"] != true {
			t.Fatal("read-only annotation changed")
		}
		props := tool.InputSchema["properties"].(map[string]any)
		id := props["id"].(map[string]any)
		if id["minLength"] != 1 || !strings.Contains(id["description"].(string), "Catalog asset ID") || !strings.Contains(tool.Description, "content_catalog_assets_get") {
			t.Fatal("ambiguous tool contract", tool)
		}
		if _, advertised := props["brand_id"]; advertised {
			t.Fatal("unimplemented brand_id advertised")
		}
		return
	}
	t.Fatal("missing tool")
}
