package main

// Tests for purgeOrphans — the cascade cleanup that runs when the
// indexer notices a media row whose underlying storage file is
// gone (soft-deleted by the user via the storage app).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPurgeOrphans_DeletesMissingFiles(t *testing.T) {
	ctx := newTestCtx(t)

	// Three media rows representing files 1, 2, 3.
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha-1", "", "")
	upsertMedia(ctx.AppDB(), testProj, "2", sampleAudioProbe(), "sha-2", "", "")
	upsertMedia(ctx.AppDB(), testProj, "3", sampleAudioProbe(), "sha-3", "", "")

	// Storage lists file 1 only — files 2 + 3 were deleted.
	n, err := purgeOrphans(nil, nil, ctx.AppDB(), testProj, []string{"1"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected 2 orphans purged, got %d", n)
	}
	if _, err := getMedia(ctx.AppDB(), testProj, "1"); err != nil {
		t.Errorf("file 1 should still exist: %v", err)
	}
	for _, fid := range []string{"2", "3"} {
		if _, err := getMedia(ctx.AppDB(), testProj, fid); !notFound(err) {
			t.Errorf("file %s should have been purged", fid)
		}
	}
}

func TestPurgeOrphans_CascadesDerivations(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha", "", "")
	upsertDerivation(ctx.AppDB(), testProj, "1", "thumbnail", 100, 320, 240, 0)
	upsertDerivation(ctx.AppDB(), testProj, "1", "waveform", 101, 800, 100, 0)

	// Storage no longer has file 1 → orphan.
	if _, err := purgeOrphans(nil, nil, ctx.AppDB(), testProj, []string{}); err != nil {
		t.Fatal(err)
	}

	// Derivations gone too.
	rows, _ := listDerivations(ctx.AppDB(), testProj, "1")
	if len(rows) != 0 {
		t.Errorf("orphan derivations remain: %v", rows)
	}
}

func TestPurgeOrphans_CascadesTranscripts(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha", "", "")
	upsertTranscript(ctx.AppDB(), &TranscriptRow{
		FileID: "1", ProjectID: testProj, Status: "ok", Text: "to be cleaned",
	})

	if _, err := purgeOrphans(nil, nil, ctx.AppDB(), testProj, []string{}); err != nil {
		t.Fatal(err)
	}
	if _, err := getTranscript(ctx.AppDB(), testProj, "1"); !notFound(err) {
		t.Errorf("orphan transcript should have been deleted")
	}
}

func TestPurgeOrphans_NoOpWhenAllPresent(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha-1", "", "")
	upsertMedia(ctx.AppDB(), testProj, "2", sampleAudioProbe(), "sha-2", "", "")

	n, err := purgeOrphans(nil, nil, ctx.AppDB(), testProj, []string{"1", "2"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0 purges, got %d", n)
	}
	for _, fid := range []string{"1", "2"} {
		if _, err := getMedia(ctx.AppDB(), testProj, fid); err != nil {
			t.Errorf("file %s missing after no-op purge", fid)
		}
	}
}

func TestPurgeOrphans_OtherProjectUntouched(t *testing.T) {
	// Cross-tenant safety: passing an empty list for project A must
	// not delete project B's rows.
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha-a", "", "")
	upsertMedia(ctx.AppDB(), "other-proj", "1", sampleAudioProbe(), "sha-b", "", "")

	if _, err := purgeOrphans(nil, nil, ctx.AppDB(), testProj, []string{}); err != nil {
		t.Fatal(err)
	}
	if _, err := getMedia(ctx.AppDB(), "other-proj", "1"); err != nil {
		t.Errorf("other-project row vanished: %v", err)
	}
}

func TestPurgeOrphans_NilFileListWipesProject(t *testing.T) {
	// Documented behaviour: nil currentFileIDs == "no files exist
	// for this project" == purge everything. Useful for explicit
	// resets; the indexer never calls it this way without the
	// safety guard.
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha", "", "")
	upsertMedia(ctx.AppDB(), testProj, "2", sampleAudioProbe(), "sha", "", "")

	n, err := purgeOrphans(nil, nil, ctx.AppDB(), testProj, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected to wipe 2 rows, got %d", n)
	}
}

func TestPurgeOrphans_LargeBatch(t *testing.T) {
	// Sanity: SQLite has a default IN-list limit of 999 host params.
	// Verify we handle up to 500 orphans without hitting that cap.
	// (If we ever cross 999 the test will start failing and we'll
	// chunk the DELETE in store.go.)
	ctx := newTestCtx(t)
	for i := 0; i < 500; i++ {
		fid := strDigit(i)
		upsertMedia(ctx.AppDB(), testProj, fid, sampleAudioProbe(), "sha"+fid, "", "test.wav")
	}
	n, err := purgeOrphans(nil, nil, ctx.AppDB(), testProj, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 500 {
		t.Errorf("expected 500 orphans purged, got %d", n)
	}
}

func strDigit(i int) string {
	const hex = "0123456789abcdef"
	if i < 10 {
		return string(hex[i])
	}
	out := []byte{}
	for i > 0 {
		out = append([]byte{hex[i%16]}, out...)
		i /= 16
	}
	return string(out)
}

// TestPurgeOrphans_VerifyByID — exercises the v0.13.5+ resolve-by-id
// path. The old "list all + diff" approach silently failed when
// storage's listing endpoint capped the response below the project's
// actual file count (operators saw "media disappears when I upload",
// because each new upload triggered a tick where purgeOrphans saw
// only the 50 newest files and treated everything older as orphan).
//
// This test stands up a fake storage that responds to /files?ids=A,B,C
// the same way storage's real handler does — returning entries for
// requested IDs that "still exist" and dropping the rest from the
// response. We seed media with 5 rows. The fake reports 3 still
// present. purgeOrphans should delete exactly the 2 unreported rows.
//
// Critically: we make NO bulk-listing call available on the fake.
// If the production code ever falls back to "list everything",
// the fake's catch-all 404 ensures the test fails loudly instead
// of silently passing on stale-listing reads.
func TestPurgeOrphans_VerifyByID_DeletesUnreported(t *testing.T) {
	ctx := newTestCtx(t)
	// Seed 5 media rows.
	for _, fid := range []string{"100", "101", "102", "103", "104"} {
		if err := upsertMedia(ctx.AppDB(), testProj, fid, sampleAudioProbe(), "sha-"+fid, "", ""); err != nil {
			t.Fatal(err)
		}
	}

	// Fake storage that ONLY responds to /files?ids=… requests.
	// Reports 100, 102, 104 as present; 101 + 103 missing → orphans.
	stillPresent := map[int64]bool{100: true, 102: true, 104: true}
	var resolveCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/apps/storage/files") {
			http.Error(w, "unexpected path "+r.URL.Path, 404)
			return
		}
		// We expect ids= queries only. A bulk listing (no ids) means
		// the code regressed to the listing-cap path → fail loud.
		idsRaw := r.URL.Query().Get("ids")
		if idsRaw == "" {
			// files_delete also POSTs to /files/.../mcp but uses a
			// different path. A GET /files with no ids is the
			// bulk-listing call we want to forbid.
			if r.Method == http.MethodGet {
				http.Error(w, "test rejects bulk listing — must use ids= filter", 500)
				return
			}
		}
		resolveCalls++
		out := []StorageFile{}
		for _, idStr := range strings.Split(idsRaw, ",") {
			id, ok := parseInt64Local(strings.TrimSpace(idStr))
			if !ok {
				continue
			}
			if stillPresent[id] {
				out = append(out, StorageFile{ID: id, Name: "f.bin", Folder: "/"})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": out})
	}))
	defer srv.Close()
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "dev-test")

	sc := newStorageClient()
	n, err := purgeOrphans(nil, sc, ctx.AppDB(), testProj, nil)
	if err != nil {
		t.Fatalf("purgeOrphans: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 orphans purged (101, 103), got %d", n)
	}
	if resolveCalls == 0 {
		t.Error("storage was never asked about any IDs — purgeOrphans isn't using the resolve path")
	}

	// Survivors stay.
	for _, fid := range []string{"100", "102", "104"} {
		m, _ := getMedia(ctx.AppDB(), testProj, fid)
		if m == nil {
			t.Errorf("file_id=%s was wrongly purged — storage reported it present", fid)
		}
	}
	// Orphans are gone.
	for _, fid := range []string{"101", "103"} {
		m, _ := getMedia(ctx.AppDB(), testProj, fid)
		if m != nil {
			t.Errorf("file_id=%s should have been purged — storage didn't report it", fid)
		}
	}
}

// TestPurgeOrphans_VerifyByID_StorageErrorSkipsSweep — when the
// resolve call errors mid-sweep, we MUST NOT cascade-delete based
// on partial data. Conservative fallback: log + skip this tick.
// Next tick retries; the orphans (if any) stick around for one
// more sweep cycle rather than being silently wiped because storage
// hiccupped.
func TestPurgeOrphans_VerifyByID_StorageErrorSkipsSweep(t *testing.T) {
	ctx := newTestCtx(t)
	for _, fid := range []string{"200", "201"} {
		if err := upsertMedia(ctx.AppDB(), testProj, fid, sampleAudioProbe(), "sha-"+fid, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "storage is down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "dev-test")

	sc := newStorageClient()
	n, err := purgeOrphans(nil, sc, ctx.AppDB(), testProj, nil)
	if err != nil {
		t.Fatalf("purgeOrphans should swallow storage errors, not return them: %v", err)
	}
	if n != 0 {
		t.Errorf("storage error should produce 0 purges, got %d", n)
	}
	// Both rows still here.
	for _, fid := range []string{"200", "201"} {
		m, _ := getMedia(ctx.AppDB(), testProj, fid)
		if m == nil {
			t.Errorf("file_id=%s was purged despite storage error — sweep should have skipped", fid)
		}
	}
}
