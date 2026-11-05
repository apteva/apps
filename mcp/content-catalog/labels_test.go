package main

import (
	"fmt"
	"reflect"
	"testing"
)

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

func TestAssetTagsAddRemovePreserveOtherLabelsAndSearch(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	id := attach(t, a, ctx, session, 1)
	if _, err := a.assetLabelsUpdate(ctx, map[string]any{"asset_ids": []string{id}, "tags": []string{"favorite-take", "patreon-review:hold"}, "favorite": true, "patreon_intent": "paid"}); err != nil {
		t.Fatal(err)
	}
	result, err := a.assetLabelsUpdate(ctx, map[string]any{
		"asset_ids": []string{id}, "tags_add": []any{" Review:Approved ", "favorite-take"},
		"tags_remove": []string{" PATREON-REVIEW:HOLD ", "missing"}, "expected_revisions": map[string]int64{id: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	saved := result.(map[string]any)["assets"].([]*Asset)[0]
	if !reflect.DeepEqual(saved.Tags, []string{"favorite-take", "review:approved"}) || !saved.Favorite || saved.PatreonIntent != "paid" || saved.Revision != 3 {
		t.Fatalf("unexpected saved labels: %#v", saved)
	}
	if got := assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "tag": "review:approved"}); len(got) != 1 || got[0].ID != id {
		t.Fatalf("updated tag missing from search: %#v", got)
	}
	if got := assetHits(t, a, ctx, map[string]any{"entity_type": "assets", "tag": "patreon-review:hold"}); len(got) != 0 {
		t.Fatal("removed tag still found")
	}
	for range 2 {
		if _, err = a.assetLabelsUpdate(ctx, map[string]any{"asset_ids": []string{id}, "tags_add": []string{"favorite-take"}, "tags_remove": []string{"missing"}}); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := a.assetGet(ctx, map[string]any{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	if got := detail.(map[string]any)["asset"].(*Asset).Tags; !reflect.DeepEqual(got, saved.Tags) {
		t.Fatalf("repeated delta duplicated or lost tags: %v", got)
	}
}

func TestAssetTagsBulkEditsRollbackOnStaleRevisionOrLimit(t *testing.T) {
	for _, failure := range []string{"stale", "limit", "foreign"} {
		t.Run(failure, func(t *testing.T) {
			a, ctx, _, _, session := setupCatalog(t)
			first := attach(t, a, ctx, session, 1)
			second := attach(t, a, ctx, session, 2)
			if failure == "limit" {
				tags := []string{}
				for i := range 25 {
					tags = append(tags, fmt.Sprintf("tag-%02d", i))
				}
				if _, err := a.assetLabelsUpdate(ctx, map[string]any{"asset_ids": []string{second}, "tags": tags}); err != nil {
					t.Fatal(err)
				}
			}
			expected := map[string]int64{first: 1, second: 1}
			switch failure {
			case "stale":
				expected[second] = 99
			case "limit":
				expected[second] = 2
			case "foreign":
				if _, err := ctx.AppDB().Exec(`UPDATE assets SET project_id='other-project' WHERE id=?`, second); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := a.assetLabelsUpdate(ctx, map[string]any{"asset_ids": []string{first, second}, "tags_add": []string{"new-tag"}, "expected_revisions": expected}); err == nil {
				t.Fatal("expected bulk failure")
			}
			got, err := a.assetGet(ctx, map[string]any{"id": first})
			if err != nil {
				t.Fatal(err)
			}
			asset := got.(map[string]any)["asset"].(*Asset)
			if len(asset.Tags) != 0 || asset.Revision != 1 {
				t.Fatalf("partial update survived rollback: %#v", asset)
			}
		})
	}
}

func TestAssetTagsInvalidEditsAndClear(t *testing.T) {
	a, ctx, _, _, session := setupCatalog(t)
	id := attach(t, a, ctx, session, 1)
	for _, edits := range []map[string]any{
		{"tags": []string{}, "tags_add": []string{"new"}},
		{"tags_add": []string{"same"}, "tags_remove": []string{" SAME "}},
		{"tags_add": []any{42}},
		{"tags_remove": []any{false}},
		{"tags_add": []string{"has spaces"}},
	} {
		edits["asset_ids"] = []string{id}
		if _, err := a.assetLabelsUpdate(ctx, edits); err == nil {
			t.Fatalf("accepted invalid edit: %v", edits)
		}
	}
	if _, err := a.assetLabelsUpdate(ctx, map[string]any{"tags_add": []string{"new"}}); err == nil {
		t.Fatal("accepted missing asset_ids")
	}
	if _, err := a.assetLabelsUpdate(ctx, map[string]any{"asset_ids": []string{id}, "tags": []string{"review:hold"}}); err != nil {
		t.Fatal(err)
	}
	result, err := a.assetLabelsUpdate(ctx, map[string]any{"asset_ids": []string{id}, "tags": []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if tags := result.(map[string]any)["assets"].([]*Asset)[0].Tags; tags == nil || len(tags) != 0 {
		t.Fatalf("clear did not return an empty tag array: %v", tags)
	}
}

func TestAssetTagEditingToolContract(t *testing.T) {
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name != "content_catalog_assets_labels_update" {
			continue
		}
		if tool.Annotations["readOnlyHint"] != false {
			t.Fatal("tag updates must be advertised as mutations")
		}
		props := tool.InputSchema["properties"].(map[string]any)
		for _, field := range []string{"tags", "tags_add", "tags_remove", "expected_revisions"} {
			if props[field] == nil {
				t.Fatalf("tool schema omits %s", field)
			}
		}
		return
	}
	t.Fatal("label update tool missing")
}
