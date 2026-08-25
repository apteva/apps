package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func metadataMap(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode metadata %q: %v", raw, err)
	}
	return out
}

func metadataPatchJSON(t *testing.T, patch map[string]any) []byte {
	t.Helper()
	_, b, err := normalizeMetadataObject(patch, "patch", true)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMetadata_DefaultRoundTripAndReindexPreservation(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "1", sampleVideoProbe(), "sha-1", "/", "one.mp4"); err != nil {
		t.Fatal(err)
	}
	row, err := getMedia(ctx.AppDB(), testProj, "1")
	if err != nil {
		t.Fatal(err)
	}
	if string(row.Metadata) != "{}" || row.MetadataVersion != 0 {
		t.Fatalf("new row metadata=%s version=%d", row.Metadata, row.MetadataVersion)
	}

	result, err := patchMediaMetadata(ctx.AppDB(), testProj, "1", metadataPatchJSON(t, map[string]any{
		"site_id": "monika-hypnosis-reactions",
		"patreon": map[string]any{"status": "ready"},
	}), nil, nil)
	if err != nil || !result.Updated || result.MetadataVersion != 1 {
		t.Fatalf("initial patch result=%+v err=%v", result, err)
	}

	updatedProbe := sampleVideoProbe()
	updatedProbe.DurationMs = 99_000
	if err := upsertMedia(ctx.AppDB(), testProj, "1", updatedProbe, "sha-2", "/", "one.mp4"); err != nil {
		t.Fatal(err)
	}
	row, err = getMedia(ctx.AppDB(), testProj, "1")
	if err != nil {
		t.Fatal(err)
	}
	got := metadataMap(t, row.Metadata)
	if got["site_id"] != "monika-hypnosis-reactions" || row.MetadataVersion != 1 {
		t.Fatalf("reindex changed metadata=%v version=%d", got, row.MetadataVersion)
	}
	if row.DurationMs != 99_000 {
		t.Fatalf("reindex did not update probe fields: duration=%d", row.DurationMs)
	}
}

func TestMetadata_ConditionalTransitionOnlyOneCallerWins(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "1", sampleVideoProbe(), "sha", "/", "one.mp4"); err != nil {
		t.Fatal(err)
	}
	initial, err := patchMediaMetadata(ctx.AppDB(), testProj, "1", metadataPatchJSON(t, map[string]any{
		"patreon": map[string]any{"status": "ready"},
	}), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	conditions, err := parseMetadataConditions(map[string]any{"metadata.patreon.status": "ready"}, "conditions")
	if err != nil {
		t.Fatal(err)
	}
	patch := metadataPatchJSON(t, map[string]any{"patreon": map[string]any{"status": "posting"}})
	first, err := patchMediaMetadata(ctx.AppDB(), testProj, "1", patch, conditions, nil)
	if err != nil || !first.Updated || first.MetadataVersion != initial.MetadataVersion+1 {
		t.Fatalf("first transition=%+v err=%v", first, err)
	}
	second, err := patchMediaMetadata(ctx.AppDB(), testProj, "1", patch, conditions, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Updated || second.Reason != "condition_failed" || second.MetadataVersion != first.MetadataVersion {
		t.Fatalf("second transition should lose atomically: %+v", second)
	}
	status := metadataMap(t, second.Metadata)["patreon"].(map[string]any)["status"]
	if status != "posting" {
		t.Fatalf("winning state=%v", status)
	}
}

func TestMetadata_VersionCompareAndSwapAndMergeDelete(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleVideoProbe(), "sha", "/", "one.mp4")
	first, err := patchMediaMetadata(ctx.AppDB(), testProj, "1", metadataPatchJSON(t, map[string]any{
		"workflow": map[string]any{"status": "ready", "attempt": 1},
		"keep":     true,
	}), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	staleVersion := int64(0)
	conflict, err := patchMediaMetadata(ctx.AppDB(), testProj, "1", metadataPatchJSON(t, map[string]any{
		"workflow": map[string]any{"status": "posting"},
	}), nil, &staleVersion)
	if err != nil || conflict.Updated || conflict.Reason != "metadata_version_mismatch" {
		t.Fatalf("stale CAS=%+v err=%v", conflict, err)
	}
	if conflict.MetadataVersion != first.MetadataVersion {
		t.Fatalf("failed CAS changed version: before=%d after=%d", first.MetadataVersion, conflict.MetadataVersion)
	}

	currentVersion := first.MetadataVersion
	merged, err := patchMediaMetadata(ctx.AppDB(), testProj, "1", metadataPatchJSON(t, map[string]any{
		"workflow": map[string]any{"status": "posting", "attempt": nil},
	}), nil, &currentVersion)
	if err != nil || !merged.Updated {
		t.Fatalf("merge patch=%+v err=%v", merged, err)
	}
	got := metadataMap(t, merged.Metadata)
	workflow := got["workflow"].(map[string]any)
	if workflow["status"] != "posting" {
		t.Fatalf("nested merge failed: %v", got)
	}
	if _, exists := workflow["attempt"]; exists {
		t.Fatalf("null must delete nested key: %v", got)
	}
	if got["keep"] != true {
		t.Fatalf("merge erased sibling: %v", got)
	}
}

func TestMetadata_SearchFiltersAreNestedTypedAndProjectScoped(t *testing.T) {
	ctx := newTestCtx(t)
	for _, id := range []string{"1", "2", "3"} {
		if err := upsertMedia(ctx.AppDB(), testProj, id, sampleVideoProbe(), "sha-"+id, "/", id+".mp4"); err != nil {
			t.Fatal(err)
		}
	}
	patches := map[string]map[string]any{
		"1": {"site_id": "monika-hypnosis-reactions", "release_role": "main_video", "patreon": map[string]any{"status": "ready"}},
		"2": {"site_id": "monika-hypnosis-reactions", "release_role": "trailer", "patreon": map[string]any{"status": "ready"}},
		"3": {"site_id": "other", "release_role": "main_video", "patreon": map[string]any{"status": "ready"}},
	}
	for id, patch := range patches {
		if _, err := patchMediaMetadata(ctx.AppDB(), testProj, id, metadataPatchJSON(t, patch), nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	filters, err := parseMetadataConditions(map[string]any{
		"metadata.site_id":        "monika-hypnosis-reactions",
		"metadata.release_role":   "main_video",
		"metadata.patreon.status": "ready",
	}, "metadata_filters")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := searchMedia(ctx.AppDB(), testProj, SearchFilters{MetadataFilters: filters})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].FileID != "1" {
		t.Fatalf("filtered rows=%+v", rows)
	}
	if metadataMap(t, rows[0].Metadata)["release_role"] != "main_video" {
		t.Fatalf("search detail dropped metadata: %s", rows[0].Metadata)
	}

	typed, _ := parseMetadataConditions(map[string]any{"metadata.site_id": 123}, "metadata_filters")
	rows, err = searchMedia(ctx.AppDB(), testProj, SearchFilters{MetadataFilters: typed})
	if err != nil || len(rows) != 0 {
		t.Fatalf("numeric filter must not equal string metadata: rows=%d err=%v", len(rows), err)
	}
	rows, err = searchMedia(ctx.AppDB(), "another-project", SearchFilters{MetadataFilters: filters})
	if err != nil || len(rows) != 0 {
		t.Fatalf("metadata filter crossed project scope: rows=%d err=%v", len(rows), err)
	}

	toolOut, err := (&App{}).toolSearch(ctx, map[string]any{
		"_project_id": testProj,
		"metadata_filters": map[string]any{
			"metadata.site_id":        "monika-hypnosis-reactions",
			"metadata.release_role":   "main_video",
			"metadata.patreon.status": "ready",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	toolResult := toolOut.(map[string]any)
	compact := toolResult["media"].([]MediaSearchRow)
	if len(compact) != 1 || compact[0].FileID != "1" {
		t.Fatalf("media_search metadata_filters result=%+v", compact)
	}
	query := toolResult["query"].(map[string]any)
	if got, ok := query["metadata_filters"].(map[string]any); !ok || len(got) != 3 {
		t.Fatalf("media_search query echo=%+v", query)
	}
}

func TestMetadata_ToolEmitsUpdateAndReturnsCurrentOnConflict(t *testing.T) {
	recorder := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithEmitter(recorder))
	globalCtx = ctx
	upsertMedia(ctx.AppDB(), testProj, "1", sampleVideoProbe(), "sha", "/", "one.mp4")

	out, err := (&App{}).toolPatchMetadata(ctx, map[string]any{
		"_project_id": testProj,
		"file_id":     "1",
		"patch":       map[string]any{"patreon": map[string]any{"status": "ready"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(MetadataPatchResult)
	if !result.Updated || result.MetadataVersion != 1 {
		t.Fatalf("tool result=%+v", result)
	}
	events := recorder.EventsByTopic("media.updated")
	if len(events) != 1 {
		t.Fatalf("media.updated events=%d", len(events))
	}
	data := events[0].Data.(map[string]any)
	if data["change"] != "metadata_patched" || data["metadata_version"] != int64(1) {
		t.Fatalf("event=%+v", data)
	}

	missing, err := (&App{}).toolPatchMetadata(ctx, map[string]any{
		"_project_id": testProj,
		"file_id":     "404",
		"patch":       map[string]any{"state": "ready"},
	})
	if err != nil {
		t.Fatal(err)
	}
	missingResult := missing.(MetadataPatchResult)
	if missingResult.Found || missingResult.Updated || missingResult.Reason != "not_found" {
		t.Fatalf("missing result=%+v", missingResult)
	}
	if len(recorder.EventsByTopic("media.updated")) != 1 {
		t.Fatal("failed patch must not emit media.updated")
	}
}

func TestMetadata_ValidationRejectsUnsafeOrAmbiguousInputs(t *testing.T) {
	badPaths := []string{
		"patreon.status",
		"metadata.",
		"metadata.patreon..status",
		"metadata.patreon.status') OR 1=1 --",
		"metadata.dash-key",
	}
	for _, path := range badPaths {
		t.Run(path, func(t *testing.T) {
			if _, err := parseMetadataConditions(map[string]any{path: "ready"}, "conditions"); err == nil {
				t.Fatalf("expected path %q to fail", path)
			}
		})
	}
	if _, err := parseMetadataConditions(map[string]any{"metadata.tags": []any{"a"}}, "conditions"); err == nil {
		t.Fatal("array condition must be rejected")
	}
	if _, _, err := normalizeMetadataObject([]any{"not", "object"}, "patch", true); err == nil {
		t.Fatal("top-level array patch must be rejected")
	}
	if _, _, err := normalizeMetadataObject(map[string]any{}, "patch", true); err == nil {
		t.Fatal("empty patch must be rejected")
	}
	oversized := map[string]any{"value": strings.Repeat("x", maxMediaMetadataBytes)}
	if _, _, err := normalizeMetadataObject(oversized, "patch", true); err == nil {
		t.Fatal("oversized patch must be rejected")
	}
}

func TestMediaPanel_MetadataEditorContract(t *testing.T) {
	source, err := os.ReadFile("ui/MediaPanel.tsx")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"MetadataEditor",
		"media_patch_metadata",
		"expected_metadata_version",
		"buildMetadataMergePatch",
		"Metadata changed elsewhere",
		"Maximum 16 KB",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("MediaPanel metadata UI missing %q", required)
		}
	}
	bundle, err := os.ReadFile("ui/MediaPanel.mjs")
	if err != nil {
		t.Fatal(err)
	}
	built := string(bundle)
	for _, required := range []string{"media_patch_metadata", "expected_metadata_version", "Metadata changed elsewhere"} {
		if !strings.Contains(built, required) {
			t.Errorf("built MediaPanel metadata UI missing %q", required)
		}
	}
	if strings.Contains(built, "jsxDEV") || strings.Contains(built, "react/jsx-dev-runtime") {
		t.Fatal("built MediaPanel must use the production JSX runtime")
	}
}
