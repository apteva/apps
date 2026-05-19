package main

// Folder support tests — confirms the new folder column + filters
// + media_list_folders tool work end-to-end.
//
// Folder lives on MediaRow now; tests insert directly via
// upsertMedia (the indexer's path) and exercise the search +
// list-folders surface.

import (
	"testing"
)

// upsertWithFolder is sugar so tests don't repeat the long arg list.
func upsertWithFolder(t *testing.T, ctx interface{ AppDB() *anyDB }, fileID, folder string) {
	t.Helper()
}

// helpers from store_test reach AppDB() — reuse those.

func TestSearch_FolderExactMatch(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleVideoProbe(), "a", "/clips/", "")
	upsertMedia(ctx.AppDB(), testProj, "2", sampleVideoProbe(), "b", "/clips/q3/", "")
	upsertMedia(ctx.AppDB(), testProj, "3", sampleVideoProbe(), "c", "/raw/", "")

	rows, err := searchMedia(ctx.AppDB(), testProj, SearchFilters{Folder: "/clips/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].FileID != "1" {
		t.Fatalf("exact match should return only the /clips/ row, got %v", rows)
	}
}

func TestSearch_FolderRecursive(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleVideoProbe(), "a", "/clips/", "")
	upsertMedia(ctx.AppDB(), testProj, "2", sampleVideoProbe(), "b", "/clips/q3/", "")
	upsertMedia(ctx.AppDB(), testProj, "3", sampleVideoProbe(), "c", "/clips/q3/sub/", "")
	upsertMedia(ctx.AppDB(), testProj, "4", sampleVideoProbe(), "d", "/raw/", "")

	rows, err := searchMedia(ctx.AppDB(), testProj, SearchFilters{Folder: "/clips/", Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("recursive should match /clips/, /clips/q3/, /clips/q3/sub/, got %d rows: %v", len(rows), rows)
	}
}

func TestSearch_FolderComposesWithOtherFilters(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleVideoProbe(), "a", "/clips/", "")
	upsertMedia(ctx.AppDB(), testProj, "2", sampleAudioProbe(), "b", "/clips/", "")
	upsertMedia(ctx.AppDB(), testProj, "3", sampleVideoProbe(), "c", "/raw/", "")

	hasVideo := true
	rows, err := searchMedia(ctx.AppDB(), testProj, SearchFilters{
		Folder:   "/clips/",
		HasVideo: &hasVideo,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].FileID != "1" {
		t.Fatalf("folder + has_video should return only id 1, got %v", rows)
	}
}

func TestListChildFolders_OneLevel(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleVideoProbe(), "a", "/clips/q3/", "")
	upsertMedia(ctx.AppDB(), testProj, "2", sampleVideoProbe(), "b", "/clips/q4/", "")
	upsertMedia(ctx.AppDB(), testProj, "3", sampleVideoProbe(), "c", "/clips/q4/sub/", "")
	upsertMedia(ctx.AppDB(), testProj, "4", sampleVideoProbe(), "d", "/raw/2026/", "")

	// Root: top-level folders that contain media.
	got, err := listChildFolders(ctx.AppDB(), testProj, "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "clips" || got[1] != "raw" {
		t.Fatalf("root listing = %v, want [clips raw]", got)
	}

	// /clips/: shows q3 + q4 (one level only — no q4's sub).
	got, err = listChildFolders(ctx.AppDB(), testProj, "/clips/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "q3" || got[1] != "q4" {
		t.Fatalf("/clips/ listing = %v, want [q3 q4]", got)
	}
}

// Folders that contain only non-media (e.g. agent has uploaded
// PDFs to /docs/) should NOT appear in media's folder tree.
// This is the user-visible difference from storage's tree.
func TestListChildFolders_OmitsMediaLessFolders(t *testing.T) {
	ctx := newTestCtx(t)
	// One media folder + one folder with only failed-probe rows.
	upsertMedia(ctx.AppDB(), testProj, "1", sampleVideoProbe(), "a", "/clips/", "")
	// Mark a failed-probe row in another folder — listChildFolders
	// filters to probe_status='ok' so this shouldn't show up.
	if err := markFailed(ctx.AppDB(), testProj, "2", "b", "unsupported", "no media stream"); err != nil {
		t.Fatal(err)
	}
	// Patch its folder via the helper to simulate the indexer's path.
	if err := updateFolder(ctx.AppDB(), testProj, "2", "/docs/"); err != nil {
		t.Fatal(err)
	}

	got, _ := listChildFolders(ctx.AppDB(), testProj, "/")
	if len(got) != 1 || got[0] != "clips" {
		t.Fatalf("listing should omit media-less folders, got %v", got)
	}
}

// updateFolder is what file.updated handlers call. After a move, the
// row's folder reflects the new location without a reprobe.
func TestUpdateFolder_OnMove(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleVideoProbe(), "sha", "/raw/", "")

	if err := updateFolder(ctx.AppDB(), testProj, "1", "/edited/"); err != nil {
		t.Fatal(err)
	}
	row, err := getMedia(ctx.AppDB(), testProj, "1")
	if err != nil {
		t.Fatal(err)
	}
	if row.Folder != "/edited/" {
		t.Fatalf("folder = %q, want /edited/", row.Folder)
	}
	// And it now shows up under the new folder, not the old.
	rows, _ := searchMedia(ctx.AppDB(), testProj, SearchFilters{Folder: "/edited/"})
	if len(rows) != 1 {
		t.Fatalf("not found in new folder: %v", rows)
	}
	rows, _ = searchMedia(ctx.AppDB(), testProj, SearchFilters{Folder: "/raw/"})
	if len(rows) != 0 {
		t.Fatalf("still in old folder: %v", rows)
	}
}

// toolListFolders normalizes parent — accepts "clips", "/clips",
// "/clips/" interchangeably so agents don't have to remember
// trailing slashes.
func TestToolListFolders_NormalizesParent(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleVideoProbe(), "a", "/clips/q3/", "")

	app := &App{}
	for _, parent := range []string{"clips", "/clips", "/clips/"} {
		out, err := app.toolListFolders(ctx, map[string]any{"_project_id": testProj, "parent": parent})
		if err != nil {
			t.Fatal(err)
		}
		got := out.(map[string]any)["folders"].([]string)
		if len(got) != 1 || got[0] != "q3" {
			t.Errorf("parent=%q: got %v, want [q3]", parent, got)
		}
	}
}

// Render submission persists per-call output_folder. Fallback to
// the install's render_output_folder is the executor's job (covered
// by renderpool integration tests); here we confirm the column
// makes it from arg → row.
func TestSubmitRender_PersistsOutputFolder(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	handler := app.toolSubmitRender("trim", []string{"start_ms", "end_ms"}, []string{"file_id"})
	out, err := handler(ctx, map[string]any{
		"_project_id":   testProj,
		"file_id":       "1",
		"start_ms":      int64(0),
		"end_ms":        int64(1000),
		"output_name":   "clip.mp4",
		"output_folder": "highlights/q3",
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := out.(map[string]any)["render_id"].(int64)
	row, err := getRender(ctx.AppDB(), testProj, id)
	if err != nil {
		t.Fatal(err)
	}
	if row.OutputFolder != "/highlights/q3/" {
		t.Errorf("OutputFolder = %q, want /highlights/q3/", row.OutputFolder)
	}
}

// Without output_folder, the row stores empty string — executor
// falls back to the install's config at run time.
func TestSubmitRender_DefaultsToEmptyOutputFolder(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	handler := app.toolSubmitRender("trim", []string{"start_ms", "end_ms"}, []string{"file_id"})
	out, err := handler(ctx, map[string]any{
		"_project_id": testProj,
		"file_id":     "1",
		"start_ms":    int64(0),
		"end_ms":      int64(1000),
		"output_name": "clip.mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := out.(map[string]any)["render_id"].(int64)
	row, _ := getRender(ctx.AppDB(), testProj, id)
	if row.OutputFolder != "" {
		t.Errorf("expected empty (= use config fallback), got %q", row.OutputFolder)
	}
}

// avoid unused-helper warning if we ever drop fixtures
var _ = anyDBSentinel

// anyDB is a placeholder type — kept around because earlier
// versions of this file used a typed alias. Defining it as an
// alias for *sql.DB keeps the test compile-friendly without
// importing database/sql at the top level.
type anyDB struct{}

var anyDBSentinel = struct{}{}
