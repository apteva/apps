package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func reservationBytes(t *testing.T, app *sdk.AppCtx) int64 {
	t.Helper()
	var total int64
	if err := app.AppDB().QueryRow(`SELECT COALESCE(sum(size_bytes),0) FROM upload_reservations`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	return total
}

func assertSavedBytes(t *testing.T, app *sdk.AppCtx, want *File, body string) {
	t.Helper()
	f, err := dbGetByID(app.AppDB(), want.ProjectID, want.ID)
	if err != nil || f == nil || f.SHA256 != want.SHA256 || f.BackendKey != want.BackendKey || f.Revision != want.Revision {
		t.Fatalf("saved file changed: %+v, %v", f, err)
	}
	r, err := backend().OpenObject(context.Background(), fileObjectKey(f), ObjectReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	got, err := io.ReadAll(r.Body)
	if err != nil || string(got) != body {
		t.Fatalf("saved bytes changed: %q, %v", got, err)
	}
}

func TestNativeCompletionsReleaseLargeReservationsAndReplay(t *testing.T) {
	app := auditCtx(t, tk.WithConfig(map[string]string{"max_pending_upload_mb": "5120"}))
	var files []*File
	for i := 0; i < 4; i++ {
		id := agentSession(t, app, fmt.Sprintf("/native/%d/", i), 4)
		// Reproduce GiB-scale quota accounting using small real file bytes;
		// reservation release must not depend on the reserved byte count.
		reserved := int64(1_280_464_691)
		if i == 3 {
			reserved++
		}
		if _, err := app.AppDB().Exec(`UPDATE upload_reservations SET size_bytes=? WHERE upload_id=?`, reserved, id); err != nil {
			t.Fatal(err)
		}
		if err := writeUploadPartBytes(app, id, 1, []byte("safe")); err != nil {
			t.Fatal(err)
		}
		out, err := completeUploadSessionForTool(app, context.Background(), id, "")
		if err != nil {
			t.Fatal(err)
		}
		f := out.(map[string]any)["file"].(*File)
		files = append(files, f)
		if got := reservationBytes(t, app); got != 0 {
			t.Fatalf("completed upload retains %d reserved bytes", got)
		}
		// Model a legacy leaked reservation, then replay the real MCP path.
		if _, err := app.AppDB().Exec(`INSERT INTO upload_reservations(upload_id,project_id,size_bytes) VALUES(?,?,?)`, id, f.ProjectID, reserved); err != nil {
			t.Fatal(err)
		}
		replayed, err := (&App{}).toolUploadCompleteCtx(context.Background(), app, map[string]any{"upload_id": id})
		if err != nil || replayed.(map[string]any)["file"].(*File).ID != f.ID {
			t.Fatalf("completion replay: %v, %v", replayed, err)
		}
		if got := reservationBytes(t, app); got != 0 {
			t.Fatalf("replay retains %d reserved bytes", got)
		}
	}
	if err := recoverStorageState(app); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		assertSavedBytes(t, app, f, "safe")
	}
	if err := reserveUpload(app, newUploadID(), "test-proj", 5<<30, 0); err != nil {
		t.Fatalf("quota not reusable after completion and recovery: %v", err)
	}
}

func TestDeduplicatedCompletionReleasesReservationAtomically(t *testing.T) {
	app := auditCtx(t)
	f := mustUpload(t, app, "test.txt", "/", "safe")
	id := agentSession(t, app, "/", 4)
	if err := writeUploadPartBytes(app, id, 1, []byte("safe")); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AppDB().Exec(`CREATE TRIGGER fail_reservation_release BEFORE DELETE ON upload_reservations BEGIN SELECT RAISE(ABORT,'injected cleanup failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := completeUploadSessionForTool(app, context.Background(), id, ""); err == nil {
		t.Fatal("deduplicated completion reported success while quota remained reserved")
	}
	assertSavedBytes(t, app, f, "safe")
	if _, err := app.AppDB().Exec(`DROP TRIGGER fail_reservation_release`); err != nil {
		t.Fatal(err)
	}
	out, err := completeUploadSessionForTool(app, context.Background(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["was_existing"] != true || result["file"].(*File).ID != f.ID || reservationBytes(t, app) != 0 {
		t.Fatalf("deduplicated completion changed identity or retained quota: %+v", result)
	}
	assertSavedBytes(t, app, f, "safe")
}

func TestCompletionReservationFailureRollsBackAndCanRetry(t *testing.T) {
	app := auditCtx(t)
	id := agentSession(t, app, "/", 4)
	if err := writeUploadPartBytes(app, id, 1, []byte("safe")); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AppDB().Exec(`CREATE TRIGGER fail_reservation_release BEFORE DELETE ON upload_reservations BEGIN SELECT RAISE(ABORT,'injected cleanup failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := completeUploadSessionForTool(app, context.Background(), id, ""); err == nil {
		t.Fatal("completion reported success despite failing transactional release")
	}
	var receipts, files int
	if err := app.AppDB().QueryRow(`SELECT (SELECT count(*) FROM completed_uploads), (SELECT count(*) FROM files)`).Scan(&receipts, &files); err != nil || receipts != 0 || files != 0 {
		t.Fatalf("completion was partially committed: receipts=%d files=%d err=%v", receipts, files, err)
	}
	if got := reservationBytes(t, app); got != 4 {
		t.Fatalf("unfinished upload lost its reservation: %d", got)
	}
	if _, err := app.AppDB().Exec(`DROP TRIGGER fail_reservation_release`); err != nil {
		t.Fatal(err)
	}
	out, err := completeUploadSessionForTool(app, context.Background(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	assertSavedBytes(t, app, out.(map[string]any)["file"].(*File), "safe")
	if got := reservationBytes(t, app); got != 0 {
		t.Fatalf("retry retains %d reserved bytes", got)
	}
}

func TestRecoveryDoesNotRecreateCompletedScratchReservations(t *testing.T) {
	app := auditCtx(t)
	id := agentSession(t, app, "/completed/", 4)
	meta, err := loadUploadMeta(uploadSessionDir(app, id))
	if err != nil {
		t.Fatal(err)
	}
	f := mustUpload(t, app, "saved.txt", "/completed/", "safe")
	if err := recordCompletion(app, id, f.ProjectID, f.Folder, f.ID, false, 0); err != nil {
		t.Fatal(err)
	}
	// Leave valid completed scratch as after a crash during RemoveAll.
	if err := os.MkdirAll(filepath.Join(uploadSessionDir(app, id), "parts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveUploadMeta(uploadSessionDir(app, id), meta); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AppDB().Exec(`UPDATE completed_uploads SET completed_at=? WHERE upload_id=?`, time.Now().Add(-8*24*time.Hour).Unix(), id); err != nil {
		t.Fatal(err)
	}
	sweepBlobCleanup(app)
	live := agentSession(t, app, "/unfinished/", 7)
	if _, err := app.AppDB().Exec(`DELETE FROM upload_reservations WHERE upload_id=?`, live); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := recoverStorageState(app); err != nil {
			t.Fatal(err)
		}
		if got := reservationBytes(t, app); got != 7 {
			t.Fatalf("restart retained completed scratch or lost live upload quota: %d", got)
		}
	}
	if _, err := loadUploadMeta(uploadSessionDir(app, live)); err != nil {
		t.Fatalf("unfinished upload lost: %v", err)
	}
	assertSavedBytes(t, app, f, "safe")
}

func TestNewUploadAdmissionRepairsLegacyCompletedQuota(t *testing.T) {
	app := auditCtx(t, tk.WithConfig(map[string]string{"max_pending_upload_mb": "5120"}))
	for i := 0; i < 4; i++ {
		f := mustUpload(t, app, fmt.Sprintf("saved-%d.txt", i), "/", "safe")
		id := newUploadID()
		if err := recordCompletion(app, id, f.ProjectID, f.Folder, f.ID, false, 0); err != nil {
			t.Fatal(err)
		}
		reserved := int64(1_280_464_691)
		if i == 3 {
			reserved++
		}
		if _, err := app.AppDB().Exec(`INSERT INTO upload_reservations(upload_id,project_id,size_bytes) VALUES(?,?,?)`, id, f.ProjectID, reserved); err != nil {
			t.Fatal(err)
		}
	}
	if got := reservationBytes(t, app); got != 5_121_858_765 {
		t.Fatalf("incorrect leaked quota fixture: %d", got)
	}
	// A 1 GiB request exceeds the remaining allowance unless admission repairs
	// the four completed reservations first.
	if err := reserveUpload(app, newUploadID(), "test-proj", 1<<30, 0); err != nil {
		t.Fatal(err)
	}
	if got := reservationBytes(t, app); got != 1<<30 {
		t.Fatalf("admission retained completed quota: %d", got)
	}
}

func TestFailedReplayReleaseIsDurableAndPreservesOldReceipt(t *testing.T) {
	app := auditCtx(t)
	f := mustUpload(t, app, "saved.txt", "/", "safe")
	id := newUploadID()
	if err := recordCompletion(app, id, f.ProjectID, f.Folder, f.ID, false, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AppDB().Exec(`UPDATE completed_uploads SET completed_at=? WHERE upload_id=?`, time.Now().Add(-8*24*time.Hour).Unix(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AppDB().Exec(`INSERT INTO upload_reservations(upload_id,project_id,size_bytes) VALUES(?,?,?)`, id, f.ProjectID, 5<<30); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AppDB().Exec(`CREATE TRIGGER fail_reservation_release BEFORE DELETE ON upload_reservations BEGIN SELECT RAISE(ABORT,'injected cleanup failure'); END`); err != nil {
		t.Fatal(err)
	}
	if out, err := completeUploadSessionForTool(app, context.Background(), id, ""); err != nil || out.(map[string]any)["file"].(*File).ID != f.ID {
		t.Fatalf("committed file unavailable on replay: %v %v", out, err)
	}
	var queued int
	if err := app.AppDB().QueryRow(`SELECT count(*) FROM upload_reservation_cleanup WHERE upload_id=?`, id).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("failed release lacks durable repair intent: %d %v", queued, err)
	}
	sweepBlobCleanup(app)
	var receipts int
	if err := app.AppDB().QueryRow(`SELECT count(*) FROM completed_uploads WHERE upload_id=?`, id).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("failed repair lost completion evidence: %d %v", receipts, err)
	}
	if _, err := app.AppDB().Exec(`DROP TRIGGER fail_reservation_release`); err != nil {
		t.Fatal(err)
	}
	sweepBlobCleanup(app)
	if got := reservationBytes(t, app); got != 0 {
		t.Fatalf("periodic repair retained %d bytes", got)
	}
	assertSavedBytes(t, app, f, "safe")
}

func TestFailedAbortReservationReleaseResumesOnRecovery(t *testing.T) {
	app := auditCtx(t)
	id := agentSession(t, app, "/abort/", 4)
	if _, err := app.AppDB().Exec(`CREATE TRIGGER fail_reservation_release BEFORE DELETE ON upload_reservations BEGIN SELECT RAISE(ABORT,'injected cleanup failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := abortUploadSession(app, id, 0, "tool"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AppDB().Exec(`DROP TRIGGER fail_reservation_release`); err != nil {
		t.Fatal(err)
	}
	if err := recoverStorageState(app); err != nil {
		t.Fatal(err)
	}
	if got := reservationBytes(t, app); got != 0 {
		t.Fatalf("restart retained failed abort reservation: %d", got)
	}
}
