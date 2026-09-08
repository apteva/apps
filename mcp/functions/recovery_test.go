package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"github.com/google/uuid"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoveryOwnershipOverlapAndCompletion(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	mountApp(t, ctx)
	p := currentPool()
	db := ctx.AppDB()
	fn, err := dbCreateFunction(db, testProj, &Function{Name: "owned", Runtime: "node", SourceKind: "inline", Source: echoHandler})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := dbInsertInvocation(db, testProj, &Invocation{FunctionID: fn.ID, Status: "running", StartedAt: time.Now().UTC().Format(time.RFC3339Nano), TriggerKind: "manual"}, p.owner.id)
	if err != nil {
		t.Fatal(err)
	}
	ver, err := dbCreateVersion(db, testProj, &FunctionVersion{FunctionID: fn.ID, SourceKind: "inline", Source: echoHandler}, p.owner.id)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := openRuntimeOwner(context.Background(), db, filepath.Dir(p.owner.dir))
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.close()
	if n, err := replacement.recover(context.Background(), db); err != nil || n != 0 {
		t.Fatalf("touched live work: n=%d %v", n, err)
	}
	if _, err = db.Exec("UPDATE function_invocations SET status='ok' WHERE id=?", inv); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow("SELECT count(*) FROM function_active_work WHERE kind='invocation'").Scan(&n)
	if n != 0 {
		t.Fatal("completed invocation retained active entry")
	}
	abandoned, err := dbInsertInvocation(db, testProj, &Invocation{FunctionID: fn.ID, Status: "running", TriggerKind: "manual"}, p.owner.id)
	if err != nil {
		t.Fatal(err)
	}
	p.owner.close()
	if n, err := replacement.recover(context.Background(), db); err != nil || n != 2 {
		t.Fatalf("recover dead owner: n=%d %v", n, err)
	}
	got, err := dbGetVersion(db, testProj, ver.ID)
	if err != nil || got.BuildStatus != "failed" {
		t.Fatalf("abandoned build: %+v %v", got, err)
	}
	var interrupted string
	if err = db.QueryRow("SELECT status FROM function_invocations WHERE id=?", abandoned).Scan(&interrupted); err != nil || interrupted != "error" {
		t.Fatalf("abandoned invocation status=%s err=%v", interrupted, err)
	}
	var status string
	db.QueryRow("SELECT status FROM function_invocations WHERE id=?", inv).Scan(&status)
	if status != "ok" {
		t.Fatal("completed work overwritten")
	}
}
func TestRecoveryOwnerProcessHelper(t *testing.T) {
	if os.Getenv("FUNCTIONS_LOCK_HELPER") != "1" {
		return
	}
	f, err := lockOwner(os.Getenv("FUNCTIONS_LOCK_DIR"), os.Getenv("FUNCTIONS_LOCK_ID"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fmt.Println("locked")
	time.Sleep(time.Minute)
}
func TestRecoveryKernelLockSurvivesOverlapReleasesOnKill(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	owner, err := openRuntimeOwner(context.Background(), ctx.AppDB(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer owner.close()
	id := uuid.NewString()
	if _, err = ctx.AppDB().Exec("INSERT INTO function_runtime_owners(id) VALUES(?)", id); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryOwnerProcessHelper$")
	cmd.Env = append(os.Environ(), "FUNCTIONS_LOCK_HELPER=1", "FUNCTIONS_LOCK_DIR="+owner.dir, "FUNCTIONS_LOCK_ID="+id)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "locked" {
		t.Fatal("child did not acquire lock")
	}
	if _, err = owner.recover(context.Background(), ctx.AppDB()); err != nil {
		t.Fatal(err)
	}
	var n int
	ctx.AppDB().QueryRow("SELECT count(*) FROM function_runtime_owners WHERE id=?", id).Scan(&n)
	if n != 1 {
		t.Fatal("live child owner removed")
	}
	cmd.Process.Kill()
	cmd.Wait()
	if _, err = owner.recover(context.Background(), ctx.AppDB()); err != nil {
		t.Fatal(err)
	}
	ctx.AppDB().QueryRow("SELECT count(*) FROM function_runtime_owners WHERE id=?", id).Scan(&n)
	if n != 0 {
		t.Fatal("dead child owner retained")
	}
}
func TestRecoveryLegacyBoundedAndOwnedExcluded(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	mountApp(t, ctx)
	p := currentPool()
	db := ctx.AppDB()
	fn, err := dbCreateFunction(db, testProj, &Function{Name: "legacy", Runtime: "node", SourceKind: "inline", Source: echoHandler})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 300; i++ {
		_, err = dbInsertInvocation(db, testProj, &Invocation{FunctionID: fn.ID, Status: "running", StartedAt: time.Now().UTC().Format(time.RFC3339Nano), TriggerKind: "manual"})
		if err != nil {
			t.Fatal(err)
		}
	}
	owned, err := dbInsertInvocation(db, testProj, &Invocation{FunctionID: fn.ID, Status: "running", TriggerKind: "manual"}, p.owner.id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE function_recovery_progress SET ceiling=? WHERE kind='invocation'", owned); err != nil {
		t.Fatal(err)
	}
	done, err := p.recoverLegacyBatch(context.Background(), "invocation")
	if err != nil || done {
		t.Fatalf("first batch %v %v", done, err)
	}
	var n int
	db.QueryRow("SELECT count(*) FROM function_invocations WHERE status='error'").Scan(&n)
	if n != 128 {
		t.Fatalf("unbounded batch: %d", n)
	}
	for !done {
		done, err = p.recoverLegacyBatch(context.Background(), "invocation")
		if err != nil {
			t.Fatal(err)
		}
	}
	var status string
	db.QueryRow("SELECT status FROM function_invocations WHERE id=?", owned).Scan(&status)
	if status != "running" {
		t.Fatal("legacy recovery touched owned work")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = p.recoverLegacyBatch(canceled, "invocation"); err == nil {
		t.Fatal("ignored cancellation")
	}
}
func TestRuntimeDrainWaitsForRealInvocation(t *testing.T) {
	t.Setenv("APTEVA_APP_DRAIN_TOKEN", "test-control-token")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "drain", "source": `export default async()=>{await new Promise(r=>setTimeout(r,6500));return "complete"}`})
	result := make(chan *invokeResult, 1)
	go func() {
		r, e := invokeFunction(ctx, context.Background(), fn, nil, "manual")
		if e != nil {
			result <- nil
			return
		}
		result <- r
	}()
	p := currentPool()
	deadline := time.Now().Add(time.Second)
	for {
		p.mu.Lock()
		n := p.activeWork
		p.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("invocation not admitted")
		}
		time.Sleep(time.Millisecond)
	}
	drain := func() bool {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/runtime/drain", nil)
		r.Header.Set("X-Apteva-Drain-Token", "test-control-token")
		app.handleRuntimeDrain(w, r)
		var state struct {
			Drained bool `json:"drained"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		return state.Drained
	}
	if drain() {
		t.Fatal("drained while invocation active")
	}
	if _, err := invokeFunction(ctx, context.Background(), fn, nil, "manual"); err == nil {
		t.Fatal("admitted new work while draining")
	}
	select {
	case r := <-result:
		if r == nil || r.Status != "ok" {
			t.Fatalf("drain canceled existing call: %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("invocation stuck")
	}
	if !drain() {
		t.Fatal("completed invocation not drained")
	}
	var n int
	ctx.AppDB().QueryRow("SELECT count(*) FROM function_active_work").Scan(&n)
	if n != 0 {
		t.Fatalf("active work leaked: %d", n)
	}
}
func TestStartupLargeHistory(t *testing.T) {
	if os.Getenv("RUN_FUNCTIONS_LARGE_HISTORY") != "1" {
		t.Skip("opt-in 8.8 GB disk fixture")
	}
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	files, _ := filepath.Glob("migrations/*.sql")
	for _, file := range files {
		if strings.HasSuffix(file, "007_active_work.sql") {
			continue
		}
		body, _ := os.ReadFile(file)
		if _, err = db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err = migrateExecutionIdentity(context.Background(), db, nil); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO functions(id,project_id,name,runtime,source_kind,source_hash) VALUES(1,'test','history','node','inline','test')`)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO function_invocations(project_id,function_id,status,trigger_kind,response_body) VALUES('test',1,'ok','manual',zeroblob(32768))`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 262144; i++ {
		if _, err = stmt.Exec(); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	stat, _ := os.Stat(path)
	t.Logf("fixture bytes=%d (%.2f GiB), rows=262144", stat.Size(), float64(stat.Size())/(1<<30))
	oldQuery := `UPDATE function_invocations SET status='error',error='Invocation interrupted by restart' WHERE status='running'`
	var a, b, c int
	var plan string
	if err = db.QueryRow("EXPLAIN QUERY PLAN "+oldQuery).Scan(&a, &b, &c, &plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "SCAN function_invocations") {
		t.Fatal(plan)
	}
	start := time.Now()
	if _, err = db.Exec(oldQuery); err != nil {
		t.Fatal(err)
	}
	old := time.Since(start)
	migration, err := os.ReadFile("migrations/007_active_work.sql")
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	migrationTime := time.Since(start)
	t.Logf("new migration on populated database=%s", migrationTime)
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, nil, nil, nil)
	start = time.Now()
	p, err := newPool(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fresh := time.Since(start)
	p.shutdown()
	t.Logf("old recovery=%s; new full pool startup=%s; ratio=%.1fx; including migration=%s", old, fresh, float64(old)/float64(fresh), migrationTime+fresh)
	if fresh > 5*time.Second {
		t.Fatalf("startup still history-bound: %s", fresh)
	}
}

func TestRecoveryRestoredDatabaseWithoutLocks(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	mountApp(t, ctx)
	p := currentPool()
	db := ctx.AppDB()
	fn, err := dbCreateFunction(db, testProj, &Function{Name: "restore", Runtime: "node", SourceKind: "inline", Source: echoHandler})
	if err != nil {
		t.Fatal(err)
	}
	id, err := dbInsertInvocation(db, testProj, &Invocation{FunctionID: fn.ID, Status: "running", TriggerKind: "manual"}, p.owner.id)
	if err != nil {
		t.Fatal(err)
	}
	// A restored DB has owner rows, but none of the old host's kernel locks.
	restored := &runtimeOwner{id: uuid.NewString(), dir: t.TempDir()}
	if n, err := restored.recover(context.Background(), db); err != nil || n != 1 {
		t.Fatalf("restored recovery n=%d err=%v", n, err)
	}
	var status string
	if err = db.QueryRow("SELECT status FROM function_invocations WHERE id=?", id).Scan(&status); err != nil || status != "error" {
		t.Fatalf("restored status %s %v", status, err)
	}
}
func TestRecoveryActiveQueriesUseIndexes(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	for _, query := range []string{
		`UPDATE function_invocations SET status='error' WHERE id IN (SELECT id FROM function_active_work WHERE owner='dead' AND kind='invocation') AND status='running'`,
		`UPDATE function_versions SET build_status='failed' WHERE id IN (SELECT id FROM function_active_work WHERE owner='dead' AND kind='build') AND build_status IN ('pending','building')`,
	} {
		rows, err := ctx.AppDB().Query("EXPLAIN QUERY PLAN " + query)
		if err != nil {
			t.Fatal(err)
		}
		var plans []string
		for rows.Next() {
			var a, b, c int
			var detail string
			if err = rows.Scan(&a, &b, &c, &detail); err != nil {
				t.Fatal(err)
			}
			plans = append(plans, detail)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		plan := strings.Join(plans, "\n")
		if strings.Contains(plan, "SCAN function_invocations") || strings.Contains(plan, "SCAN function_versions") || !strings.Contains(plan, "ix_active_work_owner") {
			t.Fatal(plan)
		}
	}
}
func TestRuntimeStatusExposesStartupSteps(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	app := mountApp(t, ctx)
	w := httptest.NewRecorder()
	app.handleRuntimeStatus(w, httptest.NewRequest("GET", "/runtime/status", nil))
	var status struct {
		Steps    []startupStep `json:"startup_steps"`
		Draining bool          `json:"draining"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(status.Steps) != 4 || status.Draining {
		t.Fatalf("bad status: %s", w.Body.String())
	}
}

func TestLegacyInactiveVersionRecoversOnRollback(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { _ = removeTree(dir) })
	t.Setenv("APTEVA_DATA_DIR", dir)
	stub := &auditRepoPlatform{result: json.RawMessage(`{"content":"export default ()=>1"}`)}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(stub))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "legacy-rollback", "source_kind": "repo", "repo_id": 1, "repo_path": "handler.mjs"})
	oldID := *fn.ActiveVersionID
	stub.result = json.RawMessage(`{"content":"export default ()=>2"}`)
	if _, err := deployVersion(ctx, fn, "repo", "", fn.RepoID, "handler.mjs", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec("UPDATE function_versions SET source='' WHERE id=?", oldID); err != nil {
		t.Fatal(err)
	}
	app.OnUnmount(ctx)
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	<-currentPool().initialPreparationScan
	v, err := dbGetVersion(ctx.AppDB(), testProj, oldID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Source != "" {
		t.Fatal("startup visited inactive history")
	}
	v, err = rollbackFunction(ctx, testProj, fn.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if v.Source != "export default ()=>1" {
		t.Fatalf("rollback used mutable upstream source: %q", v.Source)
	}
}

func TestRecoveryOldOwnerDiesAfterReplacementMounted(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	mountApp(t, ctx)
	p := currentPool()
	db := ctx.AppDB()
	old, err := openRuntimeOwner(context.Background(), db, filepath.Dir(p.owner.dir))
	if err != nil {
		t.Fatal(err)
	}
	defer old.close()
	fn, err := dbCreateFunction(db, testProj, &Function{Name: "late-crash", Runtime: "node", SourceKind: "inline", Source: echoHandler})
	if err != nil {
		t.Fatal(err)
	}
	id, err := dbInsertInvocation(db, testProj, &Invocation{FunctionID: fn.ID, Status: "running", TriggerKind: "manual"}, old.id)
	if err != nil {
		t.Fatal(err)
	}
	p.recoverAbandonedWork()
	var status string
	db.QueryRow("SELECT status FROM function_invocations WHERE id=?", id).Scan(&status)
	if status != "running" {
		t.Fatal("live old runtime recovered")
	}
	old.close()
	p.recoverAbandonedWork()
	db.QueryRow("SELECT status FROM function_invocations WHERE id=?", id).Scan(&status)
	if status != "error" {
		t.Fatal("late crash not recovered")
	}
}

func TestRuntimeDrainRejectsOrdinaryCallers(t *testing.T) {
	t.Setenv("APTEVA_APP_DRAIN_TOKEN", "private-control-token")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	app := mountApp(t, ctx)
	for _, token := range []string{"", "wrong"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/runtime/drain", nil)
		r.Header.Set("X-Apteva-Drain-Token", token)
		app.handleRuntimeDrain(w, r)
		if w.Code != 403 {
			t.Fatalf("unauthorized drain: %d", w.Code)
		}
	}
	if currentPool().draining {
		t.Fatal("unauthorized caller drained runtime")
	}
}
