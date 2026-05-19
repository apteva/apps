package main

// End-to-end enrichment tests — confirms media's tools surface
// storage URLs without the agent ever calling storage. Spins up a
// fake storage HTTP server, points APTEVA_GATEWAY_URL at it, runs
// the tools, asserts URLs and metadata land on the response.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeStorage stands in for storage's HTTP API. Holds a fixed map of
// id → StorageFile so each test seeds expectations clearly.
type fakeStorage struct {
	files map[int64]*StorageFile
	calls int
}

func newFakeStorage(t *testing.T, files []StorageFile) (*fakeStorage, func()) {
	t.Helper()
	fs := &fakeStorage{files: make(map[int64]*StorageFile, len(files))}
	for i := range files {
		fs.files[files[i].ID] = &files[i]
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.calls++
		// Match storage's actual route shape: /api/apps/storage/files?ids=...
		// (the platform proxy rewrites the prefix away before storage sees
		// it; we serve at the same final path here.)
		if !strings.HasPrefix(r.URL.Path, "/api/apps/storage/files") {
			http.Error(w, "unexpected path "+r.URL.Path, 404)
			return
		}
		ids := r.URL.Query().Get("ids")
		out := []StorageFile{}
		if ids != "" {
			for _, idStr := range strings.Split(ids, ",") {
				idStr = strings.TrimSpace(idStr)
				if idStr == "" {
					continue
				}
				var id int64
				_, _ = json.Number(idStr).Int64()
				// json.Number's Int64 returns an error type that we ignore;
				// fall back to parsing manually for clarity.
				var ok bool
				if id, ok = parseInt64Local(idStr); !ok {
					continue
				}
				if f, exists := fs.files[id]; exists {
					out = append(out, *f)
				}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": out})
	}))
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "dev-1")
	return fs, srv.Close
}

// parseInt64Local is local to this test file — probe.go has its
// own parseInt64 with different semantics (json.Number-style); we
// only need a simple positive-int parser here.
func parseInt64Local(s string) (int64, bool) {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, true
}

// media_search returns rows with absolute URLs + storage metadata.
func TestSearch_EnrichesURLsAndMetadata(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "42", sampleVideoProbe(), "deadbeef", "", ""); err != nil {
		t.Fatal(err)
	}
	_, cleanup := newFakeStorage(t, []StorageFile{
		{ID: 42, Name: "demo.mp4", Folder: "/clips/", ContentType: "video/mp4",
			SizeBytes: 12345, Visibility: "public",
			URL: "https://agents.example.com/api/apps/storage/files/42/content"},
	})
	defer cleanup()

	app := &App{}
	out, err := app.toolSearch(ctx, map[string]any{"_project_id": testProj})
	if err != nil {
		t.Fatal(err)
	}
	rows := out.(map[string]any)["media"].([]MediaResponseRow)
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	row := rows[0]
	if row.URL != "https://agents.example.com/api/apps/storage/files/42/content" {
		t.Errorf("row URL = %q", row.URL)
	}
	if row.Name != "demo.mp4" {
		t.Errorf("row Name = %q", row.Name)
	}
	if row.Visibility != "public" {
		t.Errorf("row Visibility = %q", row.Visibility)
	}
	if row.SizeBytes != 12345 {
		t.Errorf("row SizeBytes = %d", row.SizeBytes)
	}
	// Probe data still surfaces — embedded MediaRow.
	if !row.HasVideo || row.Width != 1920 {
		t.Errorf("probe data lost in enrichment: %+v", row)
	}
}

// media_get on a single file enriches the same way.
func TestGet_EnrichesURL(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "7", sampleVideoProbe(), "abc", "", ""); err != nil {
		t.Fatal(err)
	}
	_, cleanup := newFakeStorage(t, []StorageFile{
		{ID: 7, Name: "x.mp4", Visibility: "public",
			URL: "https://agents.example.com/api/apps/storage/files/7/content"},
	})
	defer cleanup()

	app := &App{}
	out, err := app.toolGet(ctx, map[string]any{"_project_id": testProj, "file_id": "7"})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	if r["found"] != true {
		t.Fatal("not found")
	}
	row := r["media"].(MediaResponseRow)
	if row.URL == "" {
		t.Fatal("URL not populated")
	}
}

// Storage unreachable → graceful degrade, flag set, probe data
// still ships. Important so the agent can tell "no URL because
// broken" from "no URL because file deleted".
func TestSearch_StorageUnavailable_FlagsAndDegrades(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "42", sampleVideoProbe(), "deadbeef", "", ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APTEVA_GATEWAY_URL", "http://127.0.0.1:1") // unreachable
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "dev-1")

	app := &App{}
	out, err := app.toolSearch(ctx, map[string]any{"_project_id": testProj})
	if err != nil {
		t.Fatalf("search itself shouldn't fail: %v", err)
	}
	r := out.(map[string]any)
	if r["storage_unavailable"] != true {
		t.Errorf("expected storage_unavailable flag, got %+v", r)
	}
	rows := r["media"].([]MediaRow)
	if len(rows) != 1 || !rows[0].HasVideo {
		t.Errorf("probe data missing in degraded response: %+v", rows)
	}
}

// File deleted from storage between probe + tool call: enrichment
// returns no entry for that id, MediaResponseRow ships with empty
// URL but everything else intact. Different from
// storage_unavailable — only some rows degrade.
func TestSearch_FileDeleted_LeavesURLEmpty(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleVideoProbe(), "a", "", "")
	upsertMedia(ctx.AppDB(), testProj, "2", sampleVideoProbe(), "b", "", "")
	_, cleanup := newFakeStorage(t, []StorageFile{
		{ID: 1, Name: "alive.mp4", Visibility: "public",
			URL: "https://x.com/api/apps/storage/files/1/content"},
		// File 2 absent — was deleted from storage.
	})
	defer cleanup()

	app := &App{}
	out, _ := app.toolSearch(ctx, map[string]any{"_project_id": testProj})
	rows := out.(map[string]any)["media"].([]MediaResponseRow)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows (one alive, one stale), got %d", len(rows))
	}
	var alive, stale *MediaResponseRow
	for i := range rows {
		switch rows[i].FileID {
		case "1":
			alive = &rows[i]
		case "2":
			stale = &rows[i]
		}
	}
	if alive == nil || alive.URL == "" {
		t.Errorf("alive row missing URL: %+v", alive)
	}
	if stale == nil {
		t.Fatal("stale row missing")
	}
	if stale.URL != "" {
		t.Errorf("deleted file should have empty URL, got %q", stale.URL)
	}
	// Probe data still there.
	if !stale.HasVideo {
		t.Errorf("probe data lost on stale row: %+v", stale)
	}
}

// Single batch round-trip even with many rows. Confirms the helper
// dedups + batches rather than calling per-row.
func TestSearch_OneBatchRoundtripPerCall(t *testing.T) {
	ctx := newTestCtx(t)
	files := []StorageFile{}
	for i := int64(1); i <= 50; i++ {
		upsertMedia(ctx.AppDB(), testProj, idStrFromInt64(i), sampleVideoProbe(), "sha", "", "")
		files = append(files, StorageFile{
			ID: i, Name: "f.mp4", Visibility: "public",
			URL: "https://x.com/" + idStrFromInt64(i),
		})
	}
	fs, cleanup := newFakeStorage(t, files)
	defer cleanup()

	app := &App{}
	out, _ := app.toolSearch(ctx, map[string]any{"_project_id": testProj, "limit": 100})
	rows := out.(map[string]any)["media"].([]MediaResponseRow)
	if len(rows) != 50 {
		t.Errorf("want 50 enriched rows, got %d", len(rows))
	}
	if fs.calls != 1 {
		t.Errorf("want 1 storage roundtrip, got %d", fs.calls)
	}
}

// collectFileIDs dedups across rows + derivations. Saves storage
// load when the same id appears in both spots (rare, but the
// dedup is cheap to confirm).
func TestCollectFileIDs_Dedups(t *testing.T) {
	rows := []MediaRow{
		{FileID: "1", Derivations: []DerivationRow{{StorageFileID: "10"}, {StorageFileID: "11"}}},
		{FileID: "2", Derivations: []DerivationRow{{StorageFileID: "10"}}}, // 10 appears twice
	}
	got := collectFileIDs(rows)
	if len(got) != 4 { // 1, 10, 11, 2
		t.Errorf("got %d ids, want 4 (dedup): %v", len(got), got)
	}
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Errorf("duplicate id %q in result", id)
		}
		seen[id] = true
	}
}
