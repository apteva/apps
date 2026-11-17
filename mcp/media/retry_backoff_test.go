package main

import (
	"testing"
	"time"
)

func TestIndexRetryDelay_DoublesAndCaps(t *testing.T) {
	want := map[int64]time.Duration{0: time.Minute, 1: time.Minute, 2: 2 * time.Minute, 5: 16 * time.Minute, 9: 256 * time.Minute, 10: 6 * time.Hour, 50: 6 * time.Hour}
	for n, d := range want {
		if got := indexRetryDelay(n); got != d {
			t.Errorf("indexRetryDelay(%d)=%v want %v", n, got, d)
		}
	}
}

// A failed file is not offered again until its retry time; success and an
// explicit reindex reset the backoff.
func TestFailedFileBacksOffUntilRetryTime(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	if err := markFailed(db, testProj, "41", "sha", "failed", "remote indexing failed"); err != nil {
		t.Fatal(err)
	}
	ids, err := pendingIndexerFileIDs(db, testProj, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("failed file retried immediately: %v", ids)
	}
	var failures int64
	var retryAt string
	if err := db.QueryRow(`SELECT index_failures, index_retry_at FROM media WHERE file_id='41'`).Scan(&failures, &retryAt); err != nil {
		t.Fatal(err)
	}
	at, _ := time.Parse(time.RFC3339, retryAt)
	if failures != 1 || at.Sub(time.Now()) > time.Minute+5*time.Second || at.Before(time.Now()) {
		t.Fatalf("failures=%d retry_at=%s", failures, retryAt)
	}

	// Second consecutive failure doubles the wait.
	if err := markFailed(db, testProj, "41", "sha", "failed", "again"); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT index_failures, index_retry_at FROM media WHERE file_id='41'`).Scan(&failures, &retryAt)
	at, _ = time.Parse(time.RFC3339, retryAt)
	if failures != 2 || at.Sub(time.Now()) < 110*time.Second {
		t.Fatalf("second failure: failures=%d retry_at=%s", failures, retryAt)
	}

	// Once due, it is offered again.
	if _, err := db.Exec(`UPDATE media SET index_retry_at=? WHERE file_id='41'`, time.Now().Add(-time.Second).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if ids, _ := pendingIndexerFileIDs(db, testProj, 10); len(ids) != 1 {
		t.Fatalf("due file not offered: %v", ids)
	}

	// An explicit reindex bypasses and resets the backoff.
	_ = markFailed(db, testProj, "41", "sha", "failed", "again")
	if err := queueMediaReindex(db, testProj, "41", false); err != nil {
		t.Fatal(err)
	}
	if ids, _ := pendingIndexerFileIDs(db, testProj, 10); len(ids) != 1 {
		t.Fatalf("reindex did not bypass backoff: %v", ids)
	}
	_ = db.QueryRow(`SELECT index_failures FROM media WHERE file_id='41'`).Scan(&failures)
	if failures != 0 {
		t.Fatalf("reindex did not reset failures: %d", failures)
	}

	// Success resets the counter.
	_ = markFailed(db, testProj, "41", "sha", "failed", "again")
	if err := upsertMedia(db, testProj, "41", sampleAudioProbe(), "sha", "/", "a.mp3"); err != nil {
		t.Fatal(err)
	}
	var retry *string
	_ = db.QueryRow(`SELECT index_failures, index_retry_at FROM media WHERE file_id='41'`).Scan(&failures, &retry)
	if failures != 0 || retry != nil {
		t.Fatalf("success did not reset backoff: failures=%d retry=%v", failures, retry)
	}
}

// Non-retryable outcomes (unsupported, skipped_size) do not count failures.
func TestNonRetryableOutcomeDoesNotScheduleRetry(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	if err := markFailed(db, testProj, "42", "sha", "unsupported", "not media"); err != nil {
		t.Fatal(err)
	}
	var failures int64
	_ = db.QueryRow(`SELECT index_failures FROM media WHERE file_id='42'`).Scan(&failures)
	if failures != 0 {
		t.Fatalf("unsupported counted as failure: %d", failures)
	}
}
