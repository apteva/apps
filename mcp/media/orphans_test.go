package main

// Tests for purgeOrphans — the cascade cleanup that runs when the
// indexer notices a media row whose underlying storage file is
// gone (soft-deleted by the user via the storage app).

import (
	"testing"
)

func TestPurgeOrphans_DeletesMissingFiles(t *testing.T) {
	ctx := newTestCtx(t)

	// Three media rows representing files 1, 2, 3.
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha-1", "")
	upsertMedia(ctx.AppDB(), testProj, "2", sampleAudioProbe(), "sha-2", "")
	upsertMedia(ctx.AppDB(), testProj, "3", sampleAudioProbe(), "sha-3", "")

	// Storage lists file 1 only — files 2 + 3 were deleted.
	n, err := purgeOrphans(ctx.AppDB(), testProj, []string{"1"})
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
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha", "")
	upsertDerivation(ctx.AppDB(), testProj, "1", "thumbnail", 100, 320, 240)
	upsertDerivation(ctx.AppDB(), testProj, "1", "waveform", 101, 800, 100)

	// Storage no longer has file 1 → orphan.
	if _, err := purgeOrphans(ctx.AppDB(), testProj, []string{}); err != nil {
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
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha", "")
	upsertTranscript(ctx.AppDB(), &TranscriptRow{
		FileID: "1", ProjectID: testProj, Status: "ok", Text: "to be cleaned",
	})

	if _, err := purgeOrphans(ctx.AppDB(), testProj, []string{}); err != nil {
		t.Fatal(err)
	}
	if _, err := getTranscript(ctx.AppDB(), testProj, "1"); !notFound(err) {
		t.Errorf("orphan transcript should have been deleted")
	}
}

func TestPurgeOrphans_NoOpWhenAllPresent(t *testing.T) {
	ctx := newTestCtx(t)
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha-1", "")
	upsertMedia(ctx.AppDB(), testProj, "2", sampleAudioProbe(), "sha-2", "")

	n, err := purgeOrphans(ctx.AppDB(), testProj, []string{"1", "2"})
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
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha-a", "")
	upsertMedia(ctx.AppDB(), "other-proj", "1", sampleAudioProbe(), "sha-b", "")

	if _, err := purgeOrphans(ctx.AppDB(), testProj, []string{}); err != nil {
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
	upsertMedia(ctx.AppDB(), testProj, "1", sampleAudioProbe(), "sha", "")
	upsertMedia(ctx.AppDB(), testProj, "2", sampleAudioProbe(), "sha", "")

	n, err := purgeOrphans(ctx.AppDB(), testProj, nil)
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
		upsertMedia(ctx.AppDB(), testProj, fid, sampleAudioProbe(), "sha"+fid, "")
	}
	n, err := purgeOrphans(ctx.AppDB(), testProj, nil)
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
