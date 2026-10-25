package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func storageScalar(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestStorageNewDatabaseInitialization(t *testing.T) {
	for _, mode := range []string{"new", "legacy", "full"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.db")
			if mode != "new" {
				db, _ := sql.Open("sqlite", path)
				if mode == "full" {
					if _, e := db.Exec("PRAGMA auto_vacuum=FULL"); e != nil {
						t.Fatal(e)
					}
				}
				if _, e := db.Exec("CREATE TABLE keep(value TEXT);INSERT INTO keep VALUES('preserved')"); e != nil {
					t.Fatal(e)
				}
				db.Close()
			}
			if e := initializeStorageDatabase(path); e != nil {
				t.Fatal(e)
			}
			db, e := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			want := map[string]int64{"new": 2, "legacy": 0, "full": 1}[mode]
			if got := storageScalar(t, db, "PRAGMA auto_vacuum"); got != want {
				t.Fatalf("mode=%d want=%d", got, want)
			}
			if mode != "new" && storageScalar(t, db, "SELECT count(*) FROM keep") != 1 {
				t.Fatal("legacy data lost")
			}
		})
	}
}
func seedStorageInvocation(t *testing.T, db *sql.DB, id int, project, status, started string) {
	t.Helper()
	fn := 1
	if project == "b" {
		fn = 3
	}
	_, err := db.Exec(`INSERT INTO function_invocations(id,project_id,function_id,status,trigger_kind,started_at,event_json,response_body,stderr,error,duration_ms,execution_ms) VALUES(?,?,?,?, 'http',?,'event','body','console','message',100,90);
 INSERT INTO function_invocation_resources VALUES(?, '{"cpu":1}');
 INSERT INTO function_invocation_identities VALUES(?, '{"subject":"secret"}')`, id, project, fn, status, started, id, id)
	if err != nil {
		t.Fatal(err)
	}
}
func TestStorageRetentionPreservesSummariesAndActiveWork(t *testing.T) {
	db := historyTestDB(t)
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for _, v := range []struct {
		id                int
		pid, status, date string
	}{
		{1, "a", "ok", "2026-10-10T12:00:00Z"},
		{2, "a", "error", "2026-10-10T12:00:00Z"},
		{3, "a", "timeout", "2026-09-20T12:00:00Z"},
		{4, "a", "running", "2026-09-01T12:00:00Z"},
		{5, "a", "ok", "2026-10-24T12:00:00Z"},
		{6, "b", "ok", "2026-10-10 12:00:00"},
		{7, "a", "ok", "2026-10-18 00:00:00"},
	} {
		seedStorageInvocation(t, db, v.id, v.pid, v.status, v.date)
	}
	policy := storagePolicy{SummaryDays: 30, SuccessDays: 7, FailureDays: 20, Batch: 32, Pages: 0}
	work, err := maintainInvocationStorage(context.Background(), db, policy, now, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if work.SummariesDeleted != 1 || work.DetailsExpired != 3 {
		t.Fatalf("work=%+v", work)
	}
	for _, id := range []int{1, 6} {
		pid := "a"
		if id == 6 {
			pid = "b"
		}
		inv, e := dbGetInvocation(db, pid, int64(id))
		if e != nil {
			t.Fatal(e)
		}
		if !inv.DetailsExpired || inv.ResponseBody != "" || inv.Stderr != "" || inv.EventJSON != "" || inv.ExecutionMS != 90 || inv.Status != "ok" {
			t.Fatalf("expired summary=%+v", inv)
		}
		if string(inv.Identity) != "null" || string(inv.Resources) != "null" {
			t.Fatalf("telemetry remains: %+v", inv)
		}
	}
	for _, id := range []int{2, 4, 5, 7} {
		inv, e := dbGetInvocation(db, "a", int64(id))
		if e != nil || inv == nil || inv.DetailsExpired || inv.ResponseBody != "body" {
			t.Fatalf("retained id=%d inv=%+v err=%v", id, inv, e)
		}
	}
	if storageScalar(t, db, "SELECT count(*) FROM function_invocation_resources WHERE invocation_id=3") != 0 {
		t.Fatal("cascade orphan")
	}
	// A previously running old call becomes eligible on the next tick.
	if _, e := db.Exec("UPDATE function_invocations SET status='error' WHERE id=4"); e != nil {
		t.Fatal(e)
	}
	if _, e := maintainInvocationStorage(context.Background(), db, policy, now, true, 0); e != nil {
		t.Fatal(e)
	}
	if storageScalar(t, db, "SELECT count(*) FROM function_invocations WHERE id=4") != 0 {
		t.Fatal("completed old call survived retention")
	}
}
func TestStorageRetentionBatchAndIndexedProgress(t *testing.T) {
	db := historyTestDB(t)
	for i := 1; i <= 9; i++ {
		seedStorageInvocation(t, db, i, "a", "ok", "2026-10-01T00:00:00Z")
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	policy := storagePolicy{SummaryDays: 30, SuccessDays: 7, FailureDays: 30, Batch: 2}
	for _, want := range []int64{2, 4, 6, 8, 9} {
		w, e := maintainInvocationStorage(context.Background(), db, policy, now, true, 0)
		if e != nil {
			t.Fatal(e)
		}
		if w.DetailsExpired > 2 || storageScalar(t, db, "SELECT count(*) FROM function_invocations WHERE details_expired=1") != want {
			t.Fatalf("batch=%+v want=%d", w, want)
		}
	}
	for _, v := range []struct{ predicate, index string }{{successDetailsPredicate, "ix_inv_success_details"}, {failureDetailsPredicate, "ix_inv_failure_details"}} {
		plan := historyPlan(t, db, `SELECT id FROM function_invocations WHERE `+v.predicate+` AND started_at < ? ORDER BY started_at,id LIMIT ?`, "2026-10-01", 32)
		if !strings.Contains(plan, v.index) || strings.Contains(plan, "TEMP B-TREE") {
			t.Fatal(plan)
		}
	}
}
func TestStorageExpiryCancellationIsAtomic(t *testing.T) {
	db := historyTestDB(t)
	seedStorageInvocation(t, db, 1, "a", "ok", "2026-01-01")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := expireInvocationDetails(ctx, db, successDetailsPredicate, "2026-10-01", 32); e == nil {
		t.Fatal("cancellation ignored")
	}
	// Abort after telemetry deletion, proving the whole transaction rolls back.
	_, e := db.Exec(`CREATE TRIGGER reject_expiry BEFORE UPDATE OF details_expired ON function_invocations BEGIN SELECT RAISE(ABORT,'test rollback'); END`)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = expireInvocationDetails(context.Background(), db, successDetailsPredicate, "2026-10-01", 32); e == nil {
		t.Fatal("trigger ignored")
	}
	if storageScalar(t, db, "SELECT count(*) FROM function_invocation_resources") != 1 || storageScalar(t, db, "SELECT details_expired FROM function_invocations WHERE id=1") != 0 {
		t.Fatal("partial expiry committed")
	}
}
func vacuumFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db := historyTestDB(t)
	// Only fixtures perform the offline conversion.
	if _, e := db.Exec("PRAGMA auto_vacuum=INCREMENTAL;VACUUM;CREATE TABLE storage_blob(x BLOB)"); e != nil {
		t.Fatal(e)
	}
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 256; i++ {
		if _, e = tx.Exec("INSERT INTO storage_blob VALUES(zeroblob(8192))"); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("DELETE FROM storage_blob;PRAGMA wal_checkpoint(TRUNCATE)"); e != nil {
		t.Fatal(e)
	}
	path, e := storageDatabasePath(context.Background(), db)
	if e != nil {
		t.Fatal(e)
	}
	return db, path
}
func TestStorageIncrementalVacuumBoundedAndShrinksFile(t *testing.T) {
	db, path := vacuumFixture(t)
	before := storageScalar(t, db, "PRAGMA freelist_count")
	stat, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	n, skip, e := reclaimStoragePages(context.Background(), db, 16)
	if e != nil || skip != "" || n <= 0 || n > 16 {
		t.Fatalf("vacuum=%d %s %v", n, skip, e)
	}
	after := storageScalar(t, db, "PRAGMA freelist_count")
	if before-after != n {
		t.Fatalf("free pages %d -> %d, reported %d", before, after, n)
	}
	statAfter, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	if statAfter.Size() >= stat.Size() {
		t.Fatalf("file did not shrink: %d -> %d", stat.Size(), statAfter.Size())
	}
	if storageScalar(t, db, "SELECT count(*) FROM functions") != 3 {
		t.Fatal("function data lost")
	}
}
func TestStorageVacuumSkipsOtherRuntimeAndLegacy(t *testing.T) {
	db, _ := vacuumFixture(t)
	_, e := db.Exec("INSERT INTO function_runtime_owners VALUES('another-runtime');INSERT INTO function_active_work VALUES('build',123,'another-runtime')")
	if e != nil {
		t.Fatal(e)
	}
	before := storageScalar(t, db, "PRAGMA freelist_count")
	if n, skip, e := reclaimStoragePages(context.Background(), db, 32); n != 0 || skip != "active_work" || e != nil {
		t.Fatalf("%d %s %v", n, skip, e)
	}
	if storageScalar(t, db, "PRAGMA freelist_count") != before {
		t.Fatal("reclaimed during active work")
	}
	legacy := historyTestDB(t)
	if n, skip, e := reclaimStoragePages(context.Background(), legacy, 32); n != 0 || skip != "conversion_required" || e != nil {
		t.Fatalf("%d %s %v", n, skip, e)
	}
}
func TestStorageBusyWriterIsNotHeldForSDKTimeout(t *testing.T) {
	db, path := vacuumFixture(t)
	conn, e := db.Conn(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	if _, e = conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); e != nil {
		t.Fatal(e)
	}
	maintenance, e := openStorageMaintenance(path)
	if e != nil {
		t.Fatal(e)
	}
	defer maintenance.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, _, e = reclaimStoragePages(ctx, maintenance, 32); e == nil {
		t.Fatal("vacuum ignored writer lock")
	}
	if time.Since(start) > time.Second {
		t.Fatal("maintenance blocked too long")
	}
	if _, e = conn.ExecContext(context.Background(), "ROLLBACK"); e != nil {
		t.Fatal(e)
	}
	// A deferred attempt must not leave the maintenance connection holding a lock.
	if _, _, e = reclaimStoragePages(context.Background(), maintenance, 32); e != nil {
		t.Fatal(e)
	}
}
func TestStorageReportScopeBacklogAndFileSizes(t *testing.T) {
	db, path := vacuumFixture(t)
	seedStorageInvocation(t, db, 1, "a", "ok", "2026-01-01")
	seedStorageInvocation(t, db, 2, "a", "ok", "2026-10-01")
	seedStorageInvocation(t, db, 3, "b", "error", "2025-01-01")
	policy := storagePolicy{SummaryDays: 30, SuccessDays: 7, FailureDays: 30, Pages: 32}
	report, e := readStorageReport(context.Background(), db, "a", policy, time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
	if e != nil {
		t.Fatal(e)
	}
	stat, _ := os.Stat(path)
	if report.CleanupBacklog != 2 || !strings.HasPrefix(report.OldestRetained, "2026-01-01") || report.DatabaseBytes != stat.Size() || report.AutoVacuum != "incremental" || report.ReusableBytes <= 0 {
		t.Fatalf("report=%+v", report)
	}
}
func TestStorageReportBacklogCapped(t *testing.T) {
	db := historyTestDB(t)
	_, e := db.Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1100) INSERT INTO function_invocations(project_id,function_id,status,trigger_kind,started_at) SELECT 'a',1,'ok','http','2026-01-01' FROM n`)
	if e != nil {
		t.Fatal(e)
	}
	report, e := readStorageReport(context.Background(), db, "a", invocationStoragePolicy(), time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC))
	if e != nil {
		t.Fatal(e)
	}
	if report.CleanupBacklog != 1001 || !report.BacklogCapped {
		t.Fatalf("%+v", report)
	}
	codes := fmt.Sprint(report.Alerts)
	if !strings.Contains(codes, "conversion_required") || !strings.Contains(codes, "cleanup_backlog") {
		t.Fatal(codes)
	}
}
func TestStorageHTTPAndToolRequireProject(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	app := mountApp(t, ctx)
	r := httptest.NewRequest(http.MethodGet, "/storage", nil)
	w := httptest.NewRecorder()
	app.handleHTTPStorage(w, r)
	if w.Code != 400 {
		t.Fatalf("missing project: %d %s", w.Code, w.Body.String())
	}
	for _, args := range []map[string]any{{}, {"_project_id": ""}, {"_project_id": " "}} {
		if _, e := storageTool().Handler(ctx, args); e == nil {
			t.Fatalf("tool missing project accepted: %v", args)
		}
	}
	r = httptest.NewRequest(http.MethodGet, "/storage?project_id=a", nil)
	w = httptest.NewRecorder()
	app.handleHTTPStorage(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "database_bytes") {
		t.Fatalf("storage: %d %s", w.Code, w.Body.String())
	}
}

func TestStorageInitializationConcurrentAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new directory", "app.db")
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { errors <- initializeStorageDatabase(path) }()
	}
	for i := 0; i < 8; i++ {
		if e := <-errors; e != nil {
			t.Fatal(e)
		}
	}
	db, e := sql.Open("sqlite", storageDSN(path)+"?_pragma=journal_mode(WAL)")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("CREATE TABLE keep(x TEXT);INSERT INTO keep VALUES('persistent')"); e != nil {
		t.Fatal(e)
	}
	db.Close()
	if e = initializeStorageDatabase(path); e != nil {
		t.Fatal(e)
	}
	db, e = sql.Open("sqlite", storageDSN(path))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if storageScalar(t, db, "PRAGMA auto_vacuum") != 2 || storageScalar(t, db, "SELECT count(*) FROM keep") != 1 {
		t.Fatal("restart lost mode or data")
	}
	files, e := filepath.Glob(filepath.Join(filepath.Dir(path), ".functions-storage-*"))
	if e != nil || len(files) != 0 {
		t.Fatalf("bootstrap files remain: %v %v", files, e)
	}
}
func TestStoragePolicyBounds(t *testing.T) {
	t.Setenv("APTEVA_FUNCTIONS_INVOCATION_RETENTION_DAYS", "3")
	t.Setenv("APTEVA_FUNCTIONS_SUCCESS_DETAILS_DAYS", "100")
	t.Setenv("APTEVA_FUNCTIONS_FAILURE_DETAILS_DAYS", "-1")
	t.Setenv("APTEVA_FUNCTIONS_VACUUM_PAGES", "0")
	t.Setenv("APTEVA_FUNCTIONS_STORAGE_BUDGET_MS", "5000")
	p := invocationStoragePolicy()
	if p.SuccessDays != 3 || p.FailureDays != 3 || p.Pages != 0 || p.BudgetMS != 100 {
		t.Fatalf("policy=%+v", p)
	}
}

func TestStorageReclaimsWithLargeReusedWAL(t *testing.T) {
	db, path := vacuumFixture(t)
	if _, err := db.Exec("PRAGMA wal_autocheckpoint=0;INSERT INTO storage_blob VALUES(zeroblob(12582912));DELETE FROM storage_blob;PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		t.Fatal(err)
	}
	wal, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if wal.Size() <= 8*1024*1024 {
		t.Fatal("fixture needs a large physical WAL")
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	n, skip, err := reclaimStoragePages(context.Background(), db, 16)
	if err != nil || skip != "" || n < 1 || n > 16 {
		t.Fatalf("vacuum %d %s %v", n, skip, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("reused large WAL prevented shrink: %d -> %d", before.Size(), after.Size())
	}
}
func TestStorageCheckpointRetriesAfterLastFreePage(t *testing.T) {
	db, path := vacuumFixture(t)
	reader, err := sql.Open("sqlite", storageDSN(path)+"?mode=ro&_pragma=query_only(on)")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM storage_blob").Scan(&count); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	n, skip, err := reclaimStoragePages(context.Background(), db, 1024)
	if err != nil || skip != "checkpoint_busy" || n < 1 {
		t.Fatalf("pinned checkpoint: %d %s %v", n, skip, err)
	}
	if storageScalar(t, db, "PRAGMA freelist_count") != 0 {
		t.Fatal("fixture did not exhaust free pages")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	n, skip, err = reclaimStoragePages(context.Background(), db, 32)
	if err != nil || n != 0 || skip != "" {
		t.Fatalf("retry checkpoint: %d %s %v", n, skip, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size() {
		t.Fatal("checkpoint did not finish shrinking database after reader released")
	}
}
func TestStorageMaintenanceRotationPreventsStarvation(t *testing.T) {
	db, _ := vacuumFixture(t)
	seedStorageInvocation(t, db, 1, "a", "ok", "2026-10-01")
	if _, err := db.Exec(`CREATE TRIGGER reject_expiry BEFORE UPDATE OF details_expired ON function_invocations BEGIN SELECT RAISE(ABORT,'blocked detail phase'); END`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	policy := storagePolicy{SummaryDays: 30, SuccessDays: 7, FailureDays: 30, Batch: 32, Pages: 16}
	work, err := maintainInvocationStorage(context.Background(), db, policy, now, true, 0)
	if err == nil || work.PagesReclaimed != 0 {
		t.Fatalf("first detail phase should stop this pass: %+v %v", work, err)
	}
	work, err = maintainInvocationStorage(context.Background(), db, policy, now, true, 3)
	if err == nil || work.PagesReclaimed <= 0 {
		t.Fatalf("rotating vacuum first must make progress despite blocked detail: %+v %v", work, err)
	}
}

func TestStorageMigrationFailureRollsBackAndRetries(t *testing.T) {
	db := legacyMigrationDB(t)
	migration, err := os.ReadFile("migrations/010_storage_retention.sql")
	if err != nil {
		t.Fatal(err)
	}
	// A conflicting index simulates migration failure after ADD COLUMN. The SDK
	// uses one transaction for statements and receipt; neither may partially stick.
	if _, err = db.Exec("CREATE INDEX ix_inv_success_details ON functions(id)"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(string(migration)); err == nil {
		t.Fatal("migration accepted conflicting index")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if storageScalar(t, db, "SELECT count(*) FROM pragma_table_info('function_invocations') WHERE name='details_expired'") != 0 {
		t.Fatal("failed migration left partial column")
	}
	if _, err = db.Exec("DROP INDEX ix_inv_success_details"); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("INSERT INTO _migrations(filename) VALUES('010_storage_retention.sql')"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if storageScalar(t, db, "SELECT count(*) FROM sqlite_schema WHERE name IN ('ix_inv_success_details','ix_inv_failure_details')") != 2 {
		t.Fatal("retry lost retention indexes")
	}
	var body string
	if err = db.QueryRow("SELECT response_body FROM function_invocations WHERE id=1").Scan(&body); err != nil || body != "keep response" {
		t.Fatalf("upgrade lost history: %q %v", body, err)
	}
}
