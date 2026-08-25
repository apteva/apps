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
	files       map[int64]*StorageFile
	calls       int
	urlRequests []map[string]any
}

func newFakeStorage(t *testing.T, files []StorageFile) (*fakeStorage, func()) {
	t.Helper()
	fs := &fakeStorage{files: make(map[int64]*StorageFile, len(files))}
	for i := range files {
		fs.files[files[i].ID] = &files[i]
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.calls++
		// Match media's binding-gated Storage proxy route.
		// (the platform proxy rewrites the prefix away before storage sees
		// it; we serve at the same final path here.)
		if !strings.HasPrefix(r.URL.Path, "/api/apps/callback/apps/storage/proxy/files") {
			http.Error(w, "unexpected path "+r.URL.Path, 404)
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/url") {
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode signed URL request: %v", err)
			}
			fs.urlRequests = append(fs.urlRequests, request)
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			if len(parts) >= 5 {
				if id, ok := parseInt64Local(parts[len(parts)-2]); ok {
					if _, exists := fs.files[id]; exists {
						_ = json.NewEncoder(w).Encode(map[string]any{
							"url":         "https://agents.example.com/api/apps/storage/files/" + parts[len(parts)-2] + "/content?sig=test&exp=9999999999",
							"delivery":    request["delivery"],
							"disposition": request["disposition"],
							"expires_at":  int64(9999999999),
							"file_id":     id,
						})
						return
					}
				}
			}
			http.Error(w, "not found", 404)
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
	t.Setenv("APTEVA_PUBLIC_URL", "https://agents.example.com")
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
	out, err := app.toolSearch(ctx, map[string]any{"_project_id": testProj, "detail": true})
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

// media_get on a single file enriches metadata but upgrades url to a
// fresh signed/presigned fetch URL for external consumers.
func TestGet_EnrichesURL(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "7", sampleVideoProbe(), "abc", "", ""); err != nil {
		t.Fatal(err)
	}
	fs, cleanup := newFakeStorage(t, []StorageFile{
		{ID: 7, Name: "x.mp4", Visibility: "private",
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
	if !strings.Contains(row.URL, "sig=test") {
		t.Fatalf("URL was not upgraded to signed fetch URL: %q", row.URL)
	}
	if row.Delivery != "apteva" || row.Disposition != "inline" || row.ExpiresAt != 9999999999 {
		t.Fatalf("URL characteristics were not propagated: %+v", row)
	}
	if len(fs.urlRequests) != 1 || fs.urlRequests[0]["delivery"] != "apteva" || fs.urlRequests[0]["disposition"] != "inline" {
		t.Fatalf("default Storage URL request = %#v", fs.urlRequests)
	}
}

func TestGet_PublicFileStillMintsConfirmedIngestionURL(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "8", sampleVideoProbe(), "abc", "", ""); err != nil {
		t.Fatal(err)
	}
	_, cleanup := newFakeStorage(t, []StorageFile{{
		ID: 8, Name: "public.mp4", Visibility: "public",
		URL: "https://agents.example.com/api/apps/storage/files/8/content",
	}})
	defer cleanup()

	out, err := (&App{}).toolGet(ctx, map[string]any{"_project_id": testProj, "file_id": "8"})
	if err != nil {
		t.Fatal(err)
	}
	row := out.(map[string]any)["media"].(MediaResponseRow)
	if !strings.Contains(row.URL, "sig=test") || row.Delivery != "apteva" || row.ExpiresAt == 0 {
		t.Fatalf("public file did not receive confirmed ingestion URL: %+v", row)
	}
}

func TestGet_AllowsDirectAttachmentDelivery(t *testing.T) {
	ctx := newTestCtx(t)
	if err := upsertMedia(ctx.AppDB(), testProj, "10", sampleVideoProbe(), "abc", "", ""); err != nil {
		t.Fatal(err)
	}
	fs, cleanup := newFakeStorage(t, []StorageFile{{
		ID: 10, Name: "download.mp4", Visibility: "private",
		URL: "https://agents.example.com/api/apps/storage/files/10/content",
	}})
	defer cleanup()

	out, err := (&App{}).toolGet(ctx, map[string]any{
		"_project_id": testProj,
		"file_id":     "10",
		"delivery":    "direct",
		"disposition": "attachment",
	})
	if err != nil {
		t.Fatal(err)
	}
	row := out.(map[string]any)["media"].(MediaResponseRow)
	if row.Delivery != "direct" || row.Disposition != "attachment" {
		t.Fatalf("selected delivery was not propagated: %+v", row)
	}
	if len(fs.urlRequests) != 1 || fs.urlRequests[0]["delivery"] != "direct" || fs.urlRequests[0]["disposition"] != "attachment" {
		t.Fatalf("Storage URL requests = %#v", fs.urlRequests)
	}
}

func TestGet_RejectsInvalidDeliveryOptions(t *testing.T) {
	ctx := newTestCtx(t)
	for _, args := range []map[string]any{
		{"_project_id": testProj, "file_id": "10", "delivery": "legacy"},
		{"_project_id": testProj, "file_id": "10", "disposition": "open"},
	} {
		if _, err := (&App{}).toolGet(ctx, args); err == nil {
			t.Fatalf("expected validation error for %#v", args)
		}
	}
}

type signedURLPlatform struct {
	*stubPlatform
	appName  string
	toolName string
	args     map[string]any
}

func (p *signedURLPlatform) CallAppResult(appName, toolName string, args map[string]any, output any) error {
	p.appName = appName
	p.toolName = toolName
	p.args = args
	out := output.(*StorageSignedURL)
	*out = StorageSignedURL{
		URL:         "/files/9/content?sig=platform&exp=555",
		Delivery:    "apteva",
		Disposition: "inline",
		ExpiresAt:   555,
		FileID:      9,
	}
	return nil
}

func TestSignedFetchURLForMediaPlatformPathRequestsCallerDelivery(t *testing.T) {
	p := &signedURLPlatform{stubPlatform: noBindings()}
	ctx := newTestCtxWithPlatform(t, p)
	info, err := signedFetchURLForMedia(ctx, testProj, "9", "direct", "attachment")
	if err != nil {
		t.Fatal(err)
	}
	if p.appName != "storage" || p.toolName != "files_get_url" {
		t.Fatalf("call = %s.%s", p.appName, p.toolName)
	}
	if p.args["ttl_seconds"] != mediaGetSignedURLTTLSeconds || p.args["delivery"] != "direct" || p.args["disposition"] != "attachment" {
		t.Fatalf("platform arguments = %#v", p.args)
	}
	if info.URL == "" || info.Delivery != "apteva" || info.Disposition != "inline" || info.ExpiresAt != 555 {
		t.Fatalf("platform result = %+v", info)
	}
}

func TestGet_HidesRawRotationMetadataByDefault(t *testing.T) {
	ctx := newTestCtx(t)
	probe := sampleVideoProbe()
	probe.Width = 1920
	probe.Height = 1080
	probe.Rotation = 90
	probe.Raw = `{"streams":[{"codec_type":"video","width":1080,"height":1920,"side_data_list":[{"side_data_type":"Display Matrix","rotation":90}]}]}`
	if err := upsertMedia(ctx.AppDB(), testProj, "42", probe, "abc", "", ""); err != nil {
		t.Fatal(err)
	}
	_, cleanup := newFakeStorage(t, []StorageFile{
		{ID: 42, Name: "content.MOV", Visibility: "private",
			URL: "https://agents.example.com/api/apps/storage/files/42/content"},
	})
	defer cleanup()

	app := &App{}
	out, err := app.toolGet(ctx, map[string]any{"_project_id": testProj, "file_id": "42"})
	if err != nil {
		t.Fatal(err)
	}
	row := out.(map[string]any)["media"].(MediaResponseRow)
	if row.Width != 1920 || row.Height != 1080 || row.DisplayOrientation != "landscape" {
		t.Fatalf("display fields wrong: %+v", row)
	}
	if row.Rotation != 0 {
		t.Fatalf("rotation should be hidden by default, got %d", row.Rotation)
	}
	if len(row.RawProbe) != 0 {
		t.Fatalf("raw_probe should be hidden by default, got %s", string(row.RawProbe))
	}

	rawOut, err := app.toolGet(ctx, map[string]any{"_project_id": testProj, "file_id": "42", "include_raw_probe": true})
	if err != nil {
		t.Fatal(err)
	}
	rawRow := rawOut.(map[string]any)["media"].(MediaResponseRow)
	if rawRow.Rotation != 90 || len(rawRow.RawProbe) == 0 {
		t.Fatalf("include_raw_probe should return rotation/raw probe, got rotation=%d raw=%q", rawRow.Rotation, string(rawRow.RawProbe))
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
	out, err := app.toolSearch(ctx, map[string]any{"_project_id": testProj, "detail": true})
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
	out, _ := app.toolSearch(ctx, map[string]any{"_project_id": testProj, "detail": true})
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
	out, _ := app.toolSearch(ctx, map[string]any{"_project_id": testProj, "limit": 100, "detail": true})
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
