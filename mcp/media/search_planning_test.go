package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func setSearchPlanningMetadata(t *testing.T, ctx *sdk.AppCtx, fileID string, metadata map[string]any, rating, createdAt string) {
	t.Helper()
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE media SET metadata=?, audience_rating=?, created_at=?, updated_at=? WHERE project_id=? AND file_id=?`, raw, rating, createdAt, createdAt, testProj, fileID); err != nil {
		t.Fatal(err)
	}
}

func TestMediaSearchDetailLevelsAndExpansions(t *testing.T) {
	ctx := newTestCtx(t)
	probe := sampleVideoProbe()
	probe.Raw = `{"format":{"format_name":"mov"},"streams":[{"codec_type":"video"}]}`
	if err := upsertMedia(ctx.AppDB(), testProj, "1", probe, "sha", "/sessions/", "session.mp4"); err != nil {
		t.Fatal(err)
	}
	setSearchPlanningMetadata(t, ctx, "1", map[string]any{
		"site_id": "monika", "channel": "main", "recording_date": "2026-08-19",
		"patreon":           map[string]any{"status": "ready"},
		"social":            map[string]any{"usage_status": "unused"},
		"hosting":           map[string]any{"status": "ready", "ready": true},
		"lineage":           map[string]any{"session_id": "session-9", "package_id": "package-4", "role": "main"},
		"publication_state": "approved",
	}, "general", "2026-08-20T10:00:00Z")
	if err := upsertDerivation(ctx.AppDB(), testProj, "1", "thumbnail", 10, 320, 180, 0); err != nil {
		t.Fatal(err)
	}
	for i, storageID := range []int64{11, 12, 13} {
		if err := upsertDerivation(ctx.AppDB(), testProj, "1", "keyframe", storageID, 320, 180, int64(i+1)*1000); err != nil {
			t.Fatal(err)
		}
	}
	_, cleanup := newFakeStorage(t, []StorageFile{
		{ID: 1, Name: "session.mp4", Folder: "/sessions/", ContentType: "video/mp4", URL: "https://example.test/1"},
		{ID: 10, Name: "1.jpg", Folder: "/.media/thumbnail/", ContentType: "image/jpeg", Source: "media-derivation", URL: "https://example.test/10"},
		{ID: 11, Name: "1-1000.jpg", Folder: "/.media/keyframe/", ContentType: "image/jpeg", Source: "media-derivation", URL: "https://example.test/11"},
		{ID: 12, Name: "1-2000.jpg", Folder: "/.media/keyframe/", ContentType: "image/jpeg", Source: "media-derivation", URL: "https://example.test/12"},
		{ID: 13, Name: "1-3000.jpg", Folder: "/.media/keyframe/", ContentType: "image/jpeg", Source: "media-derivation", URL: "https://example.test/13"},
	})
	defer cleanup()

	compactAny, err := (&App{}).toolSearch(ctx, map[string]any{"_project_id": testProj})
	if err != nil {
		t.Fatal(err)
	}
	compact := compactAny.(map[string]any)["media"].([]MediaSearchRow)
	if len(compact) != 1 || compact[0].Site != "monika" || compact[0].Channel != "main" || compact[0].AudienceRating != "general" || !compact[0].BasicReady {
		t.Fatalf("compact row = %+v", compact)
	}

	planningAny, err := (&App{}).toolSearch(ctx, map[string]any{"_project_id": testProj, "detail_level": "planning"})
	if err != nil {
		t.Fatal(err)
	}
	planning := planningAny.(map[string]any)["media"].([]MediaPlanningSearchRow)
	if len(planning) != 1 || planning[0].Patreon.Status != "ready" || !planning[0].Hosting.Ready || !planning[0].Lineage.Exact || planning[0].PublicationState != "approved" || !planning[0].Readiness.MediaReady {
		t.Fatalf("planning row = %+v", planning)
	}

	fullAny, err := (&App{}).toolSearch(ctx, map[string]any{"_project_id": testProj, "detail_level": "full"})
	if err != nil {
		t.Fatal(err)
	}
	full := fullAny.(map[string]any)["media"].([]MediaResponseRow)
	if len(full) != 1 || full[0].RawProbe != nil || len(full[0].Derivations) != 1 || full[0].Derivations[0].Kind != "thumbnail" {
		t.Fatalf("bounded full row = %+v", full)
	}

	expandedAny, err := (&App{}).toolSearch(ctx, map[string]any{
		"_project_id": testProj, "detail_level": "full",
		"expand": []any{"raw_probe", "keyframes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	expanded := expandedAny.(map[string]any)["media"].([]MediaResponseRow)
	if len(expanded) != 1 || expanded[0].RawProbe == nil || len(expanded[0].Derivations) != 4 {
		t.Fatalf("expanded full row = %+v", expanded)
	}
	derivationsAny, err := (&App{}).toolSearch(ctx, map[string]any{
		"_project_id": testProj, "detail_level": "full",
		"expand": []any{"derivations"},
	})
	if err != nil {
		t.Fatal(err)
	}
	derivations := derivationsAny.(map[string]any)["media"].([]MediaResponseRow)
	if len(derivations) != 1 || derivations[0].RawProbe != nil || len(derivations[0].Derivations) != 4 {
		t.Fatalf("derivation expansion = %+v", derivations)
	}

	if _, err := (&App{}).toolSearch(ctx, map[string]any{"_project_id": testProj, "detail_level": "full", "limit": 11}); err == nil {
		t.Fatal("full detail limit above 10 was accepted")
	}
	if _, err := (&App{}).toolSearch(ctx, map[string]any{"_project_id": testProj, "detail_level": "planning", "expand": []any{"keyframes"}}); err == nil {
		t.Fatal("planning search accepted a large expansion")
	}
}

func TestMediaSearchFieldProjectionUsesPlanningFieldsWithoutStorage(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "7", sampleVideoProbe(), "sha", "/", "seven.mp4"); err != nil {
		t.Fatal(err)
	}
	setSearchPlanningMetadata(t, ctx, "7", map[string]any{
		"recording_date":    "2026-07-04",
		"patreon":           map[string]any{"status": "scheduled"},
		"hosting":           map[string]any{"ready": true, "status": "hosted"},
		"lineage":           map[string]any{"session_id": "s7", "package_id": "p7"},
		"publication_state": "scheduled",
	}, "general", "2026-07-05T00:00:00Z")
	if err := upsertDerivation(ctx.AppDB(), testProj, "7", "thumbnail", 70, 320, 180, 0); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APTEVA_GATEWAY_URL", "http://127.0.0.1:1")
	fields := []any{"id", "recording_date", "rating", "patreon.status", "hosting", "lineage", "publication_state", "readiness"}
	outAny, err := (&App{}).toolSearch(ctx, map[string]any{"_project_id": testProj, "fields": fields})
	if err != nil {
		t.Fatal(err)
	}
	out := outAny.(map[string]any)
	if out["storage_unavailable"] != nil {
		t.Fatalf("projection performed an unnecessary Storage call: %+v", out)
	}
	rows := out["media"].([]map[string]any)
	if len(rows) != 1 || len(rows[0]) != len(fields) || rows[0]["id"] != "7" || rows[0]["rating"] != "general" {
		t.Fatalf("projected row = %#v", rows)
	}
	patreon := rows[0]["patreon"].(map[string]any)
	if patreon["status"] != "scheduled" || rows[0]["publication_state"] != "scheduled" {
		t.Fatalf("planning projection = %#v", rows[0])
	}

	if _, err := (&App{}).toolSearch(ctx, map[string]any{"_project_id": testProj, "fields": []any{"raw_probe"}}); err == nil {
		t.Fatal("compact projection accepted a full-only field")
	}
	if _, err := (&App{}).toolSearch(ctx, map[string]any{"_project_id": testProj, "detail_level": "full", "fields": []any{"raw_probe"}}); err == nil {
		t.Fatal("raw_probe projection without explicit expansion was accepted")
	}
	if _, err := (&App{}).toolSearch(ctx, map[string]any{"_project_id": testProj, "fields": []any{"not_a_field"}}); err == nil {
		t.Fatal("unknown projection field was accepted")
	}
}

func TestMediaSearchCursorIsSnapshotStable(t *testing.T) {
	ctx := newTestCtx(t)
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("old-%d", i)
		if err := upsertMedia(ctx.AppDB(), testProj, id, sampleVideoProbe(), "sha", "/", id+".mp4"); err != nil {
			t.Fatal(err)
		}
		created := fmt.Sprintf("2026-01-%02dT00:00:00Z", i)
		if _, err := ctx.AppDB().Exec(`UPDATE media SET created_at=?,updated_at=? WHERE file_id=?`, created, created, id); err != nil {
			t.Fatal(err)
		}
	}
	args := map[string]any{"_project_id": testProj, "limit": 2, "fields": []any{"file_id"}, "order_by": "created_at"}
	firstAny, err := (&App{}).toolSearch(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	first := firstAny.(map[string]any)
	if first["estimated_total"] != 5 || first["estimated_remaining"] != 3 || first["must_continue"] != true {
		t.Fatalf("first page metadata = %+v", first)
	}
	if err := upsertMedia(ctx.AppDB(), testProj, "new", sampleVideoProbe(), "sha", "/", "new.mp4"); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE media SET created_at='2026-02-01T00:00:00Z' WHERE file_id='new'`); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for _, row := range first["media"].([]map[string]any) {
		seen[row["file_id"].(string)] = true
	}
	cursor := first["next_cursor"].(string)
	for page := 0; page < 2; page++ {
		nextArgs := map[string]any{"_project_id": testProj, "limit": 2, "fields": []any{"file_id"}, "order_by": "created_at", "cursor": cursor}
		nextAny, err := (&App{}).toolSearch(ctx, nextArgs)
		if err != nil {
			t.Fatal(err)
		}
		next := nextAny.(map[string]any)
		if next["estimated_total"] != 5 {
			t.Fatalf("snapshot total changed after insertion: %+v", next)
		}
		for _, row := range next["media"].([]map[string]any) {
			id := row["file_id"].(string)
			if id == "new" || seen[id] {
				t.Fatalf("unstable cursor returned %q; seen=%v", id, seen)
			}
			seen[id] = true
		}
		if next["complete"] == true {
			if len(seen) != 5 || next["estimated_remaining"] != 0 {
				t.Fatalf("final page = %+v seen=%v", next, seen)
			}
			break
		}
		cursor = next["next_cursor"].(string)
	}
	if len(seen) != 5 {
		t.Fatalf("snapshot traversal returned %d original rows: %v", len(seen), seen)
	}
}

func TestMediaSearchOffsetContinuationKeepsOriginalTotal(t *testing.T) {
	ctx := newTestCtx(t)
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("item-%02d", i)
		if err := upsertMedia(ctx.AppDB(), testProj, id, sampleVideoProbe(), "sha", "/", id+".mp4"); err != nil {
			t.Fatal(err)
		}
	}
	firstAny, err := (&App{}).toolSearch(ctx, map[string]any{
		"_project_id": testProj, "offset": 10, "limit": 15, "fields": []any{"file_id"},
	})
	if err != nil {
		t.Fatal(err)
	}
	first := firstAny.(map[string]any)
	if first["estimated_total"] != 50 || first["estimated_remaining"] != 25 {
		t.Fatalf("offset page metadata = %+v", first)
	}
	secondAny, err := (&App{}).toolSearch(ctx, map[string]any{
		"_project_id": testProj, "limit": 15, "fields": []any{"file_id"},
		"cursor": first["next_cursor"],
	})
	if err != nil {
		t.Fatal(err)
	}
	second := secondAny.(map[string]any)
	if second["estimated_total"] != 50 || second["estimated_remaining"] != 10 {
		t.Fatalf("cursor page metadata after offset = %+v", second)
	}
}

func TestMediaSearchPlanningSorts(t *testing.T) {
	ctx := newTestCtx(t)
	fixtures := []struct {
		id, recording, sessionDate, patreon, rating string
		hosting                                     bool
	}{
		{"a", "2026-01-01", "2026-01-03", "published", "adult", false},
		{"b", "2026-03-01", "2026-03-03", "ready", "general", true},
		{"c", "2026-02-01", "2026-02-03", "draft", "mature", false},
	}
	for _, fixture := range fixtures {
		if err := upsertMedia(ctx.AppDB(), testProj, fixture.id, sampleVideoProbe(), "sha", "/", fixture.id+".mp4"); err != nil {
			t.Fatal(err)
		}
		setSearchPlanningMetadata(t, ctx, fixture.id, map[string]any{
			"recording_date": fixture.recording,
			"session":        map[string]any{"date": fixture.sessionDate},
			"patreon":        map[string]any{"status": fixture.patreon},
			"hosting":        map[string]any{"ready": fixture.hosting},
		}, fixture.rating, fixture.recording+"T00:00:00Z")
	}
	for _, test := range []struct{ order, want string }{
		{"recording_date", "b"}, {"session_date", "b"}, {"hosting_readiness", "b"},
		{"patreon_status", "b"}, {"audience_rating", "b"},
	} {
		outAny, err := (&App{}).toolSearch(ctx, map[string]any{
			"_project_id": testProj, "order_by": test.order, "sort_direction": "desc",
			"fields": []any{"file_id"}, "limit": 3,
		})
		if err != nil {
			t.Fatalf("%s: %v", test.order, err)
		}
		rows := outAny.(map[string]any)["media"].([]map[string]any)
		if len(rows) != 3 || rows[0]["file_id"] != test.want {
			t.Fatalf("%s order = %#v, want %s first", test.order, rows, test.want)
		}
	}
}

func TestMediaSearchProjectionSkipsLargeColumnsAndDerivations(t *testing.T) {
	ctx := newTestCtx(t)
	probe := sampleVideoProbe()
	probe.Raw = `{"large":"` + strings.Repeat("x", 32*1024) + `"}`
	if err := upsertMedia(ctx.AppDB(), testProj, "1", probe, "source-sha", "/", "one.mp4"); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE media SET description=?, metadata=? WHERE file_id='1'`, strings.Repeat("x", 8*1024), `{"site_id":"large"}`); err != nil {
		t.Fatal(err)
	}
	if err := upsertDerivation(ctx.AppDB(), testProj, "1", "keyframe", 99, 320, 180, 1000); err != nil {
		t.Fatal(err)
	}
	rows, err := searchMedia(ctx.AppDB(), testProj, SearchFilters{
		Projection: "compact", IncludeMetadata: false, DerivationMode: "none", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RawProbe != nil || len(rows[0].Metadata) != 2 || rows[0].Description != "" || rows[0].SourceSHA256 != "" || len(rows[0].Derivations) != 0 {
		t.Fatalf("compact SQL projection loaded large fields: %+v", rows)
	}
	var indexes int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name LIKE 'ix_media_search_%'`).Scan(&indexes); err != nil || indexes < 8 {
		t.Fatalf("planning indexes=%d err=%v", indexes, err)
	}
}

func mediaInventoryBucketCount(t *testing.T, result MediaFacetResult, value string) int {
	t.Helper()
	for _, bucket := range result.Buckets {
		if bucket.Value == value {
			return bucket.Count
		}
	}
	t.Fatalf("inventory bucket %q missing from %+v", value, result)
	return 0
}

func TestMediaInventoryCountsPlanningDimensionsWithoutRecords(t *testing.T) {
	ctx := newTestCtx(t)
	fixtures := []struct {
		id, kind, rating string
		metadata         map[string]any
	}{
		{"video", "video", "general", map[string]any{
			"recording_date": "2026-01-03", "patreon": map[string]any{"status": "ready"},
			"model": map[string]any{"id": "gpt"}, "lineage": map[string]any{"session_id": "s1"},
			"hosting": map[string]any{"ready": true},
		}},
		{"image", "image", "mature", map[string]any{
			"recording_date": "2026-01-15", "patreon": map[string]any{"status": "scheduled"},
			"model": "claude", "session": map[string]any{"id": "s2"},
			"hosting": map[string]any{"ready": false},
		}},
		{"audio", "audio", "general", map[string]any{
			"recording_date": "2026-02-01", "patreon": map[string]any{"status": "ready"},
			"model_id": "gpt", "session_id": "s1",
		}},
	}
	for _, fixture := range fixtures {
		probe := sampleVideoProbe()
		switch fixture.kind {
		case "image":
			probe = sampleImageProbe()
		case "audio":
			probe = sampleAudioProbe()
		}
		if err := upsertMedia(ctx.AppDB(), testProj, fixture.id, probe, "sha", "/inventory/", fixture.id); err != nil {
			t.Fatal(err)
		}
		setSearchPlanningMetadata(t, ctx, fixture.id, fixture.metadata, fixture.rating, "2026-03-01T00:00:00Z")
	}

	groupBy := []any{"content_type", "audience_rating", "patreon_status", "model", "session", "hosting_readiness", "recording_month"}
	outAny, err := (&App{}).toolInventory(ctx, map[string]any{"_project_id": testProj, "group_by": groupBy})
	if err != nil {
		t.Fatal(err)
	}
	out := outAny.(map[string]any)
	if out["total"] != 3 || out["records_returned"] != 0 || out["media"] != nil || out["facets"] != nil {
		t.Fatalf("inventory envelope = %+v", out)
	}
	groups := out["groups"].(map[string]MediaFacetResult)
	for group, expected := range map[string]map[string]int{
		"content_type":      {"video": 1, "image": 1, "audio": 1},
		"audience_rating":   {"general": 2, "mature": 1},
		"patreon_status":    {"ready": 2, "scheduled": 1},
		"model":             {"gpt": 2, "claude": 1},
		"session":           {"s1": 2, "s2": 1},
		"hosting_readiness": {"ready": 1, "not_ready": 1, "unknown": 1},
		"recording_month":   {"2026-01": 2, "2026-02": 1},
	} {
		for value, count := range expected {
			if got := mediaInventoryBucketCount(t, groups[group], value); got != count {
				t.Fatalf("%s[%s]=%d want %d", group, value, got, count)
			}
		}
	}

	boundedAny, err := (&App{}).toolInventory(ctx, map[string]any{
		"_project_id": testProj, "group_by": []any{"content_type"}, "group_limit": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	bounded := boundedAny.(map[string]any)["groups"].(map[string]MediaFacetResult)["content_type"]
	if len(bounded.Buckets) != 1 || bounded.OtherCount != 2 {
		t.Fatalf("bounded inventory = %+v", bounded)
	}
	if _, err := (&App{}).toolInventory(ctx, map[string]any{"_project_id": testProj, "group_by": []any{"unsupported"}}); err == nil {
		t.Fatal("unsupported inventory group was accepted")
	}
}

func TestMediaInventoryToolUsesPlainInventoryLanguage(t *testing.T) {
	var inventoryFound bool
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "media_facets" {
			t.Fatal("deprecated media_facets tool name is still exposed")
		}
		if tool.Name != "media_inventory" {
			continue
		}
		inventoryFound = true
		properties := tool.InputSchema["properties"].(map[string]any)
		if _, ok := properties["group_limit"]; !ok {
			t.Fatal("media_inventory is missing group_limit")
		}
		if _, ok := properties["facet_limit"]; ok {
			t.Fatal("media_inventory exposes facet terminology")
		}
	}
	if !inventoryFound {
		t.Fatal("media_inventory tool is missing")
	}
}
