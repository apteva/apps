package main

import (
	"reflect"
	"strings"
	"testing"
)

func seedFolderScopeMedia(t *testing.T) *App {
	t.Helper()
	ctx := newTestCtx(t)

	portrait := sampleVideoProbe()
	portrait.Width = 1080
	portrait.Height = 1920
	portrait.DurationMs = 45_000
	landscape := sampleVideoProbe()
	landscape.DurationMs = 45_000
	longPortrait := *portrait
	longPortrait.DurationMs = 90_000

	rows := []struct {
		id, folder, name, rating string
		probe                    *Probe
	}{
		{"101", "/ashley/november_2023/", "ashley-reel.mp4", "general", portrait},
		{"201", "/hgv/lily_friend/", "eligible.mp4", "general", portrait},
		{"202", "/hgv/alexa/", "landscape.mp4", "general", landscape},
		{"203", "/hgv/alexa_live/", "adult.mp4", "adult", portrait},
		{"204", "/hgv/long/", "too-long.mp4", "general", &longPortrait},
		{"301", "/sessions/", "direct.mp4", "general", landscape},
		{"401", "/archive/day1/", "filtered-descendant.mp4", "general", landscape},
	}
	storageFiles := make([]StorageFile, 0, len(rows))
	for _, row := range rows {
		if err := upsertMedia(ctx.AppDB(), testProj, row.id, row.probe, "sha-"+row.id, row.folder, row.name); err != nil {
			t.Fatal(err)
		}
		if err := setAudienceRating(ctx.AppDB(), testProj, row.id, row.rating, "test"); err != nil {
			t.Fatal(err)
		}
		storageFiles = append(storageFiles, StorageFile{ID: int64Arg(row.id), Name: row.name, Folder: row.folder})
	}
	_, cleanup := newFakeStorage(t, storageFiles)
	t.Cleanup(cleanup)
	return &App{}
}

func TestToolSearch_ExactAshleyWarnsAboutMatchingDescendants(t *testing.T) {
	app := seedFolderScopeMedia(t)
	outAny, err := app.toolSearch(globalCtx, map[string]any{
		"_project_id":  testProj,
		"folder":       "/ashley",
		"folder_scope": folderScopeExact,
		"media_type":   "video",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := outAny.(map[string]any)
	if out["returned"] != 0 || out["has_more"] != false {
		t.Fatalf("exact Ashley response = %+v", out)
	}
	if out["folder_scope"] != folderScopeExact || out["empty_reason"] != "exact_folder_has_no_matching_media" {
		t.Fatalf("missing exact-folder explanation: %+v", out)
	}
	if out["has_matching_descendants"] != true || out["descendant_match_count"] != 1 {
		t.Fatalf("descendant diagnostic = %+v", out)
	}
	if got := out["sample_matching_folders"].([]string); !reflect.DeepEqual(got, []string{"/ashley/november_2023/"}) {
		t.Fatalf("sample folders = %v", got)
	}
	retry := out["retry_recommended"].(map[string]any)
	if retry["folder"] != "/ashley/" || retry["folder_scope"] != folderScopeSubtree {
		t.Fatalf("retry = %+v", retry)
	}
}

func TestToolSearch_SubtreeAshleyReturnsDescendantAsset(t *testing.T) {
	app := seedFolderScopeMedia(t)
	outAny, err := app.toolSearch(globalCtx, map[string]any{
		"_project_id":  testProj,
		"folder":       "/ashley",
		"folder_scope": folderScopeSubtree,
		"media_type":   "video",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := outAny.(map[string]any)
	media := out["media"].([]MediaSearchRow)
	if len(media) != 1 || media[0].FileID != "101" || media[0].Folder != "/ashley/november_2023/" {
		t.Fatalf("subtree Ashley media = %+v", media)
	}
	if out["folder_scope"] != folderScopeSubtree {
		t.Fatalf("effective scope not echoed: %+v", out)
	}
	query := out["query"].(map[string]any)
	if query["folder"] != "/ashley/" || query["folder_scope"] != folderScopeSubtree || query["media_type"] != "video" {
		t.Fatalf("effective non-empty query not echoed: %+v", query)
	}
}

func TestToolSearch_FilteredHGVDescendantCountUsesSameFilters(t *testing.T) {
	app := seedFolderScopeMedia(t)
	outAny, err := app.toolSearch(globalCtx, map[string]any{
		"_project_id":     testProj,
		"folder":          "/hgv",
		"folder_scope":    folderScopeExact,
		"media_type":      "video",
		"aspect":          "portrait",
		"duration_max_ms": 59_999,
		"audience_rating": "general",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := outAny.(map[string]any)
	if out["descendant_match_count"] != 1 {
		t.Fatalf("filtered descendant count = %+v", out)
	}
	if got := out["sample_matching_folders"].([]string); !reflect.DeepEqual(got, []string{"/hgv/lily_friend/"}) {
		t.Fatalf("filtered sample folders = %v", got)
	}
	query := out["query"].(map[string]any)
	for key, want := range map[string]any{
		"folder": "/hgv/", "folder_scope": folderScopeExact, "media_type": "video",
		"aspect": "portrait", "duration_max_ms": int64(59_999),
	} {
		if query[key] != want {
			t.Errorf("query[%s]=%v want %v; query=%+v", key, query[key], want, query)
		}
	}
	if got := query["audience_rating"].([]string); !reflect.DeepEqual(got, []string{"general"}) {
		t.Fatalf("query audience rating = %v", got)
	}
}

func TestToolSearch_EmptyReasonsAndPopulatedExactFolder(t *testing.T) {
	app := seedFolderScopeMedia(t)

	populatedAny, err := app.toolSearch(globalCtx, map[string]any{
		"_project_id": testProj, "folder": "/sessions", "folder_scope": folderScopeExact,
	})
	if err != nil {
		t.Fatal(err)
	}
	populated := populatedAny.(map[string]any)
	if populated["returned"] != 1 || populated["empty_reason"] != nil {
		t.Fatalf("populated exact folder changed behavior: %+v", populated)
	}

	filteredExactAny, err := app.toolSearch(globalCtx, map[string]any{
		"_project_id": testProj, "folder": "/sessions", "folder_scope": folderScopeExact, "aspect": "portrait",
	})
	if err != nil {
		t.Fatal(err)
	}
	filteredExact := filteredExactAny.(map[string]any)
	if filteredExact["empty_reason"] != "filters_excluded_exact_folder_media" {
		t.Fatalf("filtered exact reason = %+v", filteredExact)
	}

	filteredSubtreeAny, err := app.toolSearch(globalCtx, map[string]any{
		"_project_id": testProj, "folder": "/archive", "folder_scope": folderScopeExact, "aspect": "portrait",
	})
	if err != nil {
		t.Fatal(err)
	}
	filteredSubtree := filteredSubtreeAny.(map[string]any)
	if filteredSubtree["empty_reason"] != "filters_excluded_subtree_media" || filteredSubtree["has_matching_descendants"] != false {
		t.Fatalf("filtered subtree reason = %+v", filteredSubtree)
	}

	emptyAny, err := app.toolSearch(globalCtx, map[string]any{
		"_project_id": testProj, "folder": "/genuinely-empty", "folder_scope": folderScopeSubtree,
	})
	if err != nil {
		t.Fatal(err)
	}
	empty := emptyAny.(map[string]any)
	if empty["empty_reason"] != "subtree_has_no_media" || empty["has_matching_descendants"] != false || empty["descendant_match_count"] != 0 {
		t.Fatalf("genuinely empty subtree = %+v", empty)
	}
}

func TestToolSearch_RecursiveCompatibilityAndConflict(t *testing.T) {
	app := seedFolderScopeMedia(t)
	legacyAny, err := app.toolSearch(globalCtx, map[string]any{
		"_project_id": testProj, "folder": "/ashley", "recursive": true, "media_type": "video",
	})
	if err != nil {
		t.Fatal(err)
	}
	legacy := legacyAny.(map[string]any)
	if legacy["returned"] != 1 || legacy["folder_scope"] != folderScopeSubtree {
		t.Fatalf("recursive compatibility = %+v", legacy)
	}
	legacyExactAny, err := app.toolSearch(globalCtx, map[string]any{
		"_project_id": testProj, "folder": "/ashley", "recursive": false, "media_type": "video",
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyExact := legacyExactAny.(map[string]any)
	if legacyExact["returned"] != 0 || legacyExact["folder_scope"] != folderScopeExact || legacyExact["has_matching_descendants"] != true {
		t.Fatalf("recursive=false compatibility = %+v", legacyExact)
	}
	if _, err := app.toolSearch(globalCtx, map[string]any{
		"_project_id": testProj, "folder": "/ashley", "folder_scope": folderScopeExact, "recursive": true,
	}); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting scope accepted: %v", err)
	}
	if _, err := app.toolSearch(globalCtx, map[string]any{
		"_project_id": testProj, "folder": "/ashley", "folder_scope": 1,
	}); err == nil || !strings.Contains(err.Error(), "string") {
		t.Fatalf("non-string scope accepted: %v", err)
	}
}
