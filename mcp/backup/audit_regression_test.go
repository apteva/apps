package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func auditDestination(t *testing.T, ctx *sdk.AppCtx) *Destination {
	t.Helper()
	raw, _ := json.Marshal(localConfig{Path: t.TempDir()})
	dest, err := dbCreateDestination(ctx.AppDB(), &Destination{Name: "local", Kind: kindLocal, Config: raw})
	if err != nil {
		t.Fatal(err)
	}
	return dest
}
func auditPolicy(t *testing.T, ctx *sdk.AppCtx, dest *Destination) *Policy {
	t.Helper()
	p, err := dbCreatePolicy(ctx.AppDB(), &Policy{Name: "daily", Schedule: "0 3 * * *", DestinationID: dest.ID, RetentionKeep: 1, Scope: defaultScope()})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRecreatedPolicyCannotPruneDeletedPolicyArchive(t *testing.T) {
	ctx := newTestCtx(t)
	dest := auditDestination(t, ctx)
	old := auditPolicy(t, ctx, dest)
	writer, err := openDestination(dest, ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	oldKey := buildRemoteKey(&Run{ID: 1, PolicyID: old.ID, StorageID: old.StorageID, Scope: old.Scope}, false)
	if err = writer.Put(context.Background(), oldKey, strings.NewReader("old"), 3); err != nil {
		t.Fatal(err)
	}
	if err = deletePolicy(ctx.AppDB(), old.ID); err != nil {
		t.Fatal(err)
	}
	fresh := auditPolicy(t, ctx, dest)
	if fresh.StorageID == old.StorageID {
		t.Fatal("storage identity reused")
	}
	for i := int64(2); i < 4; i++ {
		key := buildRemoteKey(&Run{ID: i, PolicyID: fresh.ID, StorageID: fresh.StorageID, Scope: fresh.Scope}, false)
		if err = writer.Put(context.Background(), key, strings.NewReader("new"), 3); err != nil {
			t.Fatal(err)
		}
	}
	if err = pruneRetention(context.Background(), ctx, writer, dest, fresh); err != nil {
		t.Fatal(err)
	}
	body, err := writer.Get(context.Background(), oldKey)
	if err != nil {
		t.Fatal("old archive pruned:", err)
	}
	body.Close()
}

func TestResultCommitFailurePreservesOldAndUploadedObjects(t *testing.T) {
	ctx := newBackupTestCtx(t, &backupPlatform{snapshot: makeSnapshotArchive(t, true)})
	dest := auditDestination(t, ctx)
	policy := auditPolicy(t, ctx, dest)
	first, err := runBackup(ctx, dest, policy, policy.Scope)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ctx.AppDB().Exec(`CREATE TRIGGER reject_success BEFORE UPDATE OF status ON runs WHEN NEW.status='success' BEGIN SELECT RAISE(FAIL,'result write failed'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runBackup(ctx, dest, policy, policy.Scope); err == nil {
		t.Fatal("expected commit failure")
	}
	writer, err := openDestination(dest, ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	objects, err := writer.List(context.Background(), storagePrefixFor(policy.Scope, policy.ID, policy.StorageID))
	if err != nil || len(objects) != 2 {
		t.Fatalf("objects=%v err=%v", objects, err)
	}
	body, err := writer.Get(context.Background(), first.RemoteKey)
	if err != nil {
		t.Fatal(err)
	}
	body.Close()
	persisted, err := dbGetRun(ctx.AppDB(), first.ID)
	if err != nil || persisted.Status != "success" {
		t.Fatal("previous restore history lost")
	}
}

func TestQueueSurvivesContentionAndRestart(t *testing.T) {
	ctx := newBackupTestCtx(t, &backupPlatform{snapshot: makeSnapshotArchive(t, true)})
	dest := auditDestination(t, ctx)
	policy := auditPolicy(t, ctx, dest)
	release, err := acquireOperation("restore")
	if err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			release()
		}
	}()
	out, err := (&App{}).toolBackupNow(ctx, map[string]any{"policy_id": policy.ID, "async": true})
	if err != nil {
		t.Fatal(err)
	}
	run := out.(map[string]any)["run"].(*Run)
	saved, err := dbGetRun(ctx.AppDB(), run.ID)
	if err != nil || saved.Status != "queued" {
		t.Fatalf("acknowledged without queue: %+v %v", saved, err)
	}
	if err = processQueuedBackup(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if err = reconcileInterruptedRuns(ctx); err != nil {
		t.Fatal(err)
	}
	again, err := enqueueBackup(ctx, dest, policy)
	if err != nil || again.ID != run.ID {
		t.Fatalf("pending policy duplicated: %v", err)
	}
	release()
	locked = false
	if err = processQueuedBackup(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	saved, err = dbGetRun(ctx.AppDB(), run.ID)
	if err != nil || saved.Status != "success" {
		t.Fatalf("run=%+v err=%v", saved, err)
	}
	var remaining int
	if err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM backup_queue`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("queue=%d err=%v", remaining, err)
	}
}

func TestQueueAcknowledgmentRequiresCommit(t *testing.T) {
	ctx := newTestCtx(t)
	dest := auditDestination(t, ctx)
	policy := auditPolicy(t, ctx, dest)
	ctx.AppDB().Exec(`CREATE TRIGGER reject_queue BEFORE INSERT ON backup_queue BEGIN SELECT RAISE(FAIL,'queue unavailable'); END`)
	if _, err := (&App{}).toolBackupNow(ctx, map[string]any{"policy_id": policy.ID, "async": true}); err == nil {
		t.Fatal("queue failure acknowledged")
	}
	runs, err := dbListRuns(ctx.AppDB(), 0, 50)
	if err != nil || len(runs) != 0 {
		t.Fatalf("uncommitted run leaked: %+v %v", runs, err)
	}
}

func TestCancellationKeepsGlobalScopeAndSurfacesToolErrors(t *testing.T) {
	platform := &jobsPlatform{}
	m := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&m, openTestDB(t), sdk.Config{}, platform, silentLogger{})
	dest := auditDestination(t, ctx)
	policy := auditPolicy(t, ctx, dest)
	ctx.AppDB().Exec(`UPDATE policies SET jobs_id='42',jobs_project_id='' WHERE id=?`, policy.ID)
	old := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = old }()
	request := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/policies/%d?project_id=other-project", policy.ID), nil)
		(&App{}).handlePolicyItem(w, r)
		return w
	}
	platform.cancelErr = errors.New("MCP cancellation failed")
	if w := request(); w.Code != 502 {
		t.Fatalf("status=%d %s", w.Code, w.Body.String())
	}
	if _, err := dbGetPolicy(ctx.AppDB(), policy.ID); err != nil {
		t.Fatal("policy lost after failed cancellation")
	}
	platform.cancelErr = nil
	if w := request(); w.Code != 200 {
		t.Fatalf("status=%d %s", w.Code, w.Body.String())
	}
	if platform.cancelledInput["_project_id"] != "" {
		t.Fatalf("global scope replaced: %v", platform.cancelledInput)
	}
}

func TestHistoryPaginationBeyond500AndUsesIndex(t *testing.T) {
	ctx := newTestCtx(t)
	dest := auditDestination(t, ctx)
	// Equal timestamps exercise the stable ID tie-breaker at every boundary.
	for i := 0; i < 605; i++ {
		if _, err := dbInsertRun(ctx.AppDB(), &Run{DestinationID: dest.ID, StartedAt: "2026-09-09T10:00:00Z"}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int64]bool{}
	cursor := ""
	for {
		rows, err := dbListRunsPage(ctx.AppDB(), runListOptions{Limit: 50, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			if seen[row.ID] {
				t.Fatal("duplicate row")
			}
			seen[row.ID] = true
		}
		cursor = encodeRunCursor(rows[len(rows)-1])
	}
	if len(seen) != 605 {
		t.Fatalf("only %d rows reachable", len(seen))
	}
	rows, err := ctx.AppDB().Query(`EXPLAIN QUERY PLAN SELECT id FROM runs ORDER BY started_at DESC,id DESC LIMIT 50`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var a, b, c int
		var detail string
		if err = rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "TEMP B-TREE") {
			t.Fatal("history sorted without index:", detail)
		}
	}
}

func TestFinishedTimestampCarriesUTC(t *testing.T) {
	ctx := newTestCtx(t)
	dest := auditDestination(t, ctx)
	id, err := dbInsertRun(ctx.AppDB(), &Run{DestinationID: dest.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err = dbFinishRun(ctx.AppDB(), id, "success", 1, "", "key", "", "", false); err != nil {
		t.Fatal(err)
	}
	run, err := dbGetRun(ctx.AppDB(), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = time.Parse(time.RFC3339Nano, run.FinishedAt); err != nil {
		t.Fatal(err)
	}
}

type recoveryPlatform struct {
	backupPlatform
	snapshotPass, restorePass string
}

func (p *recoveryPlatform) OpenPlatformSnapshotWithPassphrase(ctx context.Context, pass string) (io.ReadCloser, error) {
	p.snapshotPass = pass
	return p.OpenPlatformSnapshot(ctx)
}
func (p *recoveryPlatform) RestorePlatformSnapshotWithPassphrase(ctx context.Context, body io.Reader, size int64, pass string) (map[string]any, error) {
	p.restorePass = pass
	return p.RestorePlatformSnapshot(ctx, body, size)
}
func TestPassphraseForwardedForSnapshotAndRestore(t *testing.T) {
	platform := &recoveryPlatform{backupPlatform: backupPlatform{snapshot: []byte("archive"), restoreReport: map[string]any{"restart_required": true}}}
	m := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&m, openTestDB(t), sdk.Config{"encryption_passphrase": "portable recovery passphrase"}, platform, silentLogger{})
	if _, err := streamSnapshot(context.Background(), ctx, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := postRestore(ctx, []byte("archive")); err != nil {
		t.Fatal(err)
	}
	if platform.snapshotPass != "portable recovery passphrase" || platform.restorePass != platform.snapshotPass {
		t.Fatal("recovery passphrase not forwarded")
	}
}

func TestLocalDestinationMarksSnapshotExclusion(t *testing.T) {
	ctx := newTestCtx(t)
	dest := auditDestination(t, ctx)
	writer, err := openDestination(dest, ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	path := writer.(*localDest).cfg.Path
	if _, err = os.Stat(filepath.Join(path, ".apteva-backup-destination")); err != nil {
		t.Fatal(err)
	}
}

// Assert the transactional inserter remains usable with both database handles.
var _ interface {
	Exec(string, ...any) (sql.Result, error)
} = (*sql.Tx)(nil)
