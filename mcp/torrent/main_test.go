// Regression tests for bugs we hit in 0.1.5–0.1.13.
//
// These cover the failure modes the user actually saw, not abstract
// invariants — pinning them so a future change can't silently
// resurrect any of them:
//
//   * listIndexers + NULL last_ok_at  — broke search and
//     seedDefaultIndexer simultaneously (v0.1.10 fix).
//   * apibay JSON parsing             — zero-config indexer correctness.
//   * panel-state contract            — every state snapshot() emits
//     must show up in TorrentPanel.tsx, otherwise newly-added
//     torrents go invisible (v0.1.11 fix).
//   * chunked upload streaming        — multi-GB torrents must reach
//     storage without buffering the whole file in RAM (v0.1.13 fix).

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// osWriteFile — t.TempDir + os.WriteFile shorthand kept inline so
// individual tests stay lean.
func osWriteFile(path string, body []byte) error {
	return os.WriteFile(path, body, 0644)
}

// openTestDB creates an in-memory SQLite DB with the indexers schema
// applied (matching 001_init.sql + 002_apibay_indexer_kind.sql). Kept
// inline so the test doesn't need filesystem access.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE indexers (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id      TEXT    NOT NULL,
			name            TEXT    NOT NULL,
			kind            TEXT    NOT NULL DEFAULT 'jackett'
			                CHECK(kind IN ('jackett','prowlarr','rss','apibay')),
			base_url        TEXT    NOT NULL,
			api_key_enc     TEXT    NOT NULL DEFAULT '',
			categories_json TEXT    NOT NULL DEFAULT '[]',
			priority        INTEGER NOT NULL DEFAULT 0,
			enabled         INTEGER NOT NULL DEFAULT 1,
			last_ok_at      TEXT,
			last_error      TEXT    NOT NULL DEFAULT '',
			created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(project_id, name)
		)`); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestListIndexers_NullLastOkAt — last_ok_at is NULL until first
// successful indexer query. v0.1.7's listIndexers scanned the column
// into a plain string and panicked, breaking torrent_search AND
// silently aborting seedDefaultIndexer's pre-flight check. Pin the
// behavior: a freshly-inserted row scans cleanly with LastOKAt = "".
func TestListIndexers_NullLastOkAt(t *testing.T) {
	db := openTestDB(t)
	if _, err := db.Exec(
		`INSERT INTO indexers (project_id, name, kind, base_url) VALUES (?,?,?,?)`,
		"proj1", "apibay", "apibay", "https://apibay.org"); err != nil {
		t.Fatal(err)
	}
	out, err := listIndexers(db, "proj1", false)
	if err != nil {
		t.Fatalf("listIndexers: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 indexer, got %d", len(out))
	}
	if out[0].Kind != "apibay" {
		t.Errorf("kind = %q, want apibay", out[0].Kind)
	}
	if out[0].LastOKAt != "" {
		t.Errorf("LastOKAt = %q, want empty (NULL row)", out[0].LastOKAt)
	}
}

// TestApibayParser — feed queryApibay the raw shape apibay.org
// returns and assert the SearchResult has the fields the panel
// expects. Catches:
//   * the "no results" sentinel row leaking through
//   * infohash → magnet construction (the click handler reads
//     r.magnet, so an empty magnet means clicks do nothing)
func TestApibayParser_ValidResults(t *testing.T) {
	const fixture = `[
		{"id":"1","name":"Big Buck Bunny 1080p","info_hash":"DEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEF",
		 "leechers":"3","seeders":"42","num_files":"4","size":"1500000000",
		 "added":"1700000000","category":"207","username":"someone"},
		{"id":"2","name":"Some Album 320kbps","info_hash":"CAFEBABECAFEBABECAFEBABECAFEBABECAFEBABE",
		 "leechers":"1","seeders":"5","num_files":"12","size":"120000000",
		 "added":"1710000000","category":"101","username":"music_uploader"}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "q=test") {
			t.Errorf("unexpected query: %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(fixture))
	}))
	t.Cleanup(srv.Close)

	httpc := &http.Client{Timeout: 2 * time.Second}
	results, err := queryApibay(context.Background(), httpc, srv.URL, "test", "apibay")
	if err != nil {
		t.Fatalf("queryApibay: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	r := results[0]
	if r.Infohash != "DEADBEEFDEADBEEFDEADBEEFDEADBEEFDEADBEEF" {
		t.Errorf("infohash = %q", r.Infohash)
	}
	if r.Seeders != 42 {
		t.Errorf("seeders = %d, want 42", r.Seeders)
	}
	if r.SizeBytes != 1500000000 {
		t.Errorf("size = %d", r.SizeBytes)
	}
	if r.Category != "video" {
		t.Errorf("category = %q, want video (cat 2xx)", r.Category)
	}
	if !strings.HasPrefix(r.Magnet, "magnet:?xt=urn:btih:DEADBEEF") {
		t.Errorf("magnet missing or wrong: %q", r.Magnet)
	}
	if !strings.Contains(r.Magnet, "tr=") {
		t.Errorf("magnet missing trackers — peers won't be findable")
	}
	if r.PublishedAt == "" {
		t.Errorf("PublishedAt should be set when added is a valid unix ts")
	}
	if results[1].Category != "music" {
		t.Errorf("results[1].category = %q, want music (cat 1xx)", results[1].Category)
	}
}

// TestApibayParser_NoResults — apibay returns one synthetic
// {"name": "No results returned", ...} row when there are no hits.
// Make sure that doesn't leak through as a real result.
func TestApibayParser_NoResults(t *testing.T) {
	const fixture = `[{"id":"0","name":"No results returned","info_hash":"0000000000000000000000000000000000000000","seeders":"0","leechers":"0","num_files":"0","size":"0","added":"0","category":"0","username":""}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixture))
	}))
	t.Cleanup(srv.Close)

	results, err := queryApibay(context.Background(), &http.Client{Timeout: time.Second}, srv.URL, "asdfasdfsdf", "apibay")
	if err != nil {
		t.Fatalf("queryApibay: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("want 0 results, got %d (sentinel leaked through)", len(results))
	}
}

// TestPanelStateContract — every state snapshot() in engine.go can
// emit must be handled by the panel's bucket filter (TorrentPanel.tsx
// KNOWN_STATES). When you add a new state, update both this list
// AND the panel set, otherwise torrents in that state silently
// disappear — exactly the v0.1.10 → v0.1.11 regression that produced
// "clicking a search result does nothing" (the click added a
// 'queued' torrent that no panel section showed).
func TestPanelStateContract(t *testing.T) {
	engineStates := []string{"downloading", "seeding", "paused", "completed", "error", "queued"}
	panelStates := map[string]bool{
		"downloading": true, "seeding": true, "paused": true,
		"completed": true, "error": true, "queued": true,
	}
	for _, s := range engineStates {
		if !panelStates[s] {
			t.Errorf("engine state %q has no panel bucket — torrents in this state will be invisible", s)
		}
	}
	// Bytes-on-disk check that the TSX panel actually mentions each
	// state (defensive — keeps Go and TS in sync without running the
	// frontend test runner).
	body, err := os.ReadFile("ui/TorrentPanel.tsx")
	if err != nil {
		t.Fatalf("read ui/TorrentPanel.tsx: %v", err)
	}
	tsx := string(body)
	for _, s := range engineStates {
		if !strings.Contains(tsx, `"`+s+`"`) {
			t.Errorf("ui/TorrentPanel.tsx never references %q — bucket missing", s)
		}
	}
}

// TestChunkedUpload_EndToEnd — exercises uploadOneFile against a
// stand-in storage HTTP server that mimics the real /uploads
// protocol. Verifies bytes round-trip exactly, sha256 matches, and
// the file_id is returned. Rules out a regression to the v0.1.x
// inline-base64 path that capped at 256 MiB.
func TestChunkedUpload_EndToEnd(t *testing.T) {
	// 12 MB synthetic file — forces 3 chunks at the default 5 MB part size.
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "big.bin")
	const total = 12 * 1024 * 1024
	payload := make([]byte, total)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	if err := osWriteFile(filePath, payload); err != nil {
		t.Fatal(err)
	}
	wantSHA := sha256.Sum256(payload)
	wantSHAHex := hex.EncodeToString(wantSHA[:])

	type initReq struct {
		Filename    string `json:"filename"`
		ContentType string `json:"content_type"`
		Folder      string `json:"folder"`
		Size        int64  `json:"size"`
	}
	var got initReq
	parts := map[int][]byte{}
	var completeSHA string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/apps/storage")
		switch {
		case path == "/uploads" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &got)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"upload_id":"UPLOAD123","part_size":5242880}`))
		case strings.HasPrefix(path, "/uploads/UPLOAD123/parts/") && r.Method == http.MethodPut:
			n, _ := strconv.Atoi(strings.TrimPrefix(path, "/uploads/UPLOAD123/parts/"))
			body, _ := io.ReadAll(r.Body)
			parts[n] = body
			_, _ = w.Write([]byte(`{"ok":true}`))
		case path == "/uploads/UPLOAD123/complete" && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var b struct {
				SHA256 string `json:"sha256"`
			}
			_ = json.Unmarshal(body, &b)
			completeSHA = b.SHA256
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"file":{"id":4242},"was_existing":false}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, path)
			http.Error(w, "unexpected", 400)
		}
	}))
	t.Cleanup(srv.Close)

	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_APP_TOKEN", "dev-1")
	t.Setenv("APTEVA_PROJECT_ID", "proj-test")
	t.Setenv("APTEVA_DATA_DIR", tmp)

	// uploadOneFile uses resolveWorkingDir(a.ctx). When a.ctx is nil,
	// resolveWorkingDir's first call (configString) safely returns ""
	// then falls through to APTEVA_DATA_DIR/torrents — but our test
	// file lives directly under tmp, so override working_dir env var
	// equivalent by temporarily symlinking. Simpler path: pass a
	// FileSnapshot.Path that resolves correctly under DataDir/torrents.
	torrentsDir := filepath.Join(tmp, "torrents")
	if err := os.MkdirAll(torrentsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filePath, filepath.Join(torrentsDir, "big.bin")); err != nil {
		t.Fatal(err)
	}

	app := &App{}
	id, err := app.uploadOneFile(nil, "/downloads", FileSnapshot{
		Path:           "big.bin",
		Length:         total,
		BytesCompleted: total,
		Priority:       "normal",
	})
	if err != nil {
		t.Fatalf("uploadOneFile: %v", err)
	}
	if id != 4242 {
		t.Errorf("file_id = %d, want 4242", id)
	}
	if got.Filename != "big.bin" || got.Size != total {
		t.Errorf("init body wrong: %+v", got)
	}
	if got.Folder != "/downloads" {
		t.Errorf("folder = %q, want /downloads", got.Folder)
	}
	if len(parts) != 3 {
		t.Errorf("expected 3 part PUTs (12 MB / 5 MB chunks), got %d", len(parts))
	}
	var recon []byte
	for i := 1; i <= len(parts); i++ {
		recon = append(recon, parts[i]...)
	}
	if !bytes.Equal(recon, payload) {
		t.Errorf("reassembled bytes don't match original (%d vs %d)", len(recon), len(payload))
	}
	if completeSHA != wantSHAHex {
		t.Errorf("complete sha256 = %s, want %s", completeSHA, wantSHAHex)
	}
}