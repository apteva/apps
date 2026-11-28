package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Publish a fully initialized empty database before the SDK enables WAL and
// creates migration receipts. Atomic link never overwrites an existing database,
// including one created by a concurrent startup. No online VACUUM is needed.
func initializeStorageDatabase(path string) error {
	if path == "" || path == ":memory:" {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".functions-storage-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Close(); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", storageDSN(name))
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = db.ExecContext(ctx, "PRAGMA auto_vacuum=INCREMENTAL"); err != nil {
		return err
	}
	var mode int
	if err = db.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&mode); err != nil {
		return err
	}
	if mode != 2 {
		return fmt.Errorf("new database auto_vacuum=%d, want incremental", mode)
	}
	if err = db.Close(); err != nil {
		return err
	}
	if err = os.Link(name, path); os.IsExist(err) {
		return nil
	}
	return err
}
func storageDSN(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return (&url.URL{Scheme: "file", Path: absolute}).String()
}

type storagePolicy struct {
	SummaryDays int `json:"summary_days"`
	SuccessDays int `json:"success_details_days"`
	FailureDays int `json:"failure_details_days"`
	Batch       int `json:"batch_rows"`
	Pages       int `json:"vacuum_pages"`
	BudgetMS    int `json:"budget_ms"`
}

func invocationStoragePolicy() storagePolicy {
	summary := envInt("APTEVA_FUNCTIONS_INVOCATION_RETENTION_DAYS", 30, 1, 3650)
	return storagePolicy{
		SummaryDays: summary,
		// 0 keeps no details of successful calls: they are written as summaries.
		SuccessDays: envInt("APTEVA_FUNCTIONS_SUCCESS_DETAILS_DAYS", min(7, summary), 0, summary),
		FailureDays: envInt("APTEVA_FUNCTIONS_FAILURE_DETAILS_DAYS", summary, 1, summary),
		Batch:       envInt("APTEVA_FUNCTIONS_RETENTION_BATCH_ROWS", 32, 1, 128),
		Pages:       envInt("APTEVA_FUNCTIONS_VACUUM_PAGES", 32, 0, 1024),
		BudgetMS:    envInt("APTEVA_FUNCTIONS_STORAGE_BUDGET_MS", 100, 10, 1000),
	}
}
func retentionCutoff(now time.Time, days int) string {
	if days <= 0 {
		// Zero-day retention: everything finished before now is due.
		return now.UTC().Format(time.RFC3339Nano)
	}
	// SQLite's oldest default timestamps have a space separator. Midnight UTC
	// cutoffs avoid deleting a record on the cutoff day because of lexical order.
	return now.UTC().Add(-time.Duration(days) * 24 * time.Hour).Format("2006-01-02")
}

const successDetailsPredicate = "details_expired=0 AND status='ok'"
const failureDetailsPredicate = "details_expired=0 AND status NOT IN ('ok','running')"

type storageWork struct {
	DetailsExpired   int64  `json:"details_expired"`
	SummariesDeleted int64  `json:"summaries_deleted"`
	PagesReclaimed   int64  `json:"pages_reclaimed"`
	VacuumSkipped    string `json:"vacuum_skipped,omitempty"`
	// Per-step wall time of the last run, so a spent budget names its cause.
	StepMS map[string]int64 `json:"step_ms,omitempty"`
	// BatchRows is the batch size the last run used (adaptive, see retainInvocations).
	BatchRows int `json:"batch_rows,omitempty"`
	// BudgetExhausted names the step that was running when the budget ran out.
	BudgetExhausted string `json:"budget_exhausted,omitempty"`
}

// Step names used in storageWork.StepMS and maintenance errors.
const (
	stepSuccessDetails = "success_details"
	stepFailureDetails = "failure_details"
	stepSummaries      = "summaries"
	stepReclaim        = "reclaim"
)

var storageStepNames = []string{stepSuccessDetails, stepFailureDetails, stepSummaries, stepReclaim}

// Use a short-lived, separately configured connection: never change the SDK's
// writer timeout or hold its sole connection while waiting for maintenance.
func openStorageMaintenance(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", storageDSN(path)+"?mode=rw&_pragma=busy_timeout(10)&_pragma=foreign_keys(on)&_pragma=cache_size(-2048)&_pragma=wal_autocheckpoint(0)")
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}
func storageDatabasePath(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var path string
	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			return "", err
		}
		if name == "main" {
			path = file
		}
	}
	return path, rows.Err()
}

func expireInvocationDetails(ctx context.Context, db *sql.DB, predicate, cutoff string, limit int) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM function_invocations WHERE `+predicate+` AND started_at < ? ORDER BY started_at,id LIMIT ?`, cutoff, limit)
	if err != nil {
		return 0, err
	}
	var ids []any
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	// Delete the potentially large telemetry/identity payloads in the same
	// transaction as body expiry. Summary timing, status and version survive.
	for _, table := range []string{"function_invocation_resources", "function_invocation_identities"} {
		if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE invocation_id IN (`+placeholders+`)`, ids...); err != nil {
			return 0, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE function_invocations SET event_json=NULL,response_body=NULL,stderr=NULL,error=NULL,details_expired=1 WHERE id IN (`+placeholders+`)`, ids...); err != nil {
		return 0, err
	}
	return int64(len(ids)), tx.Commit()
}

func maintainInvocationStorage(ctx context.Context, db *sql.DB, policy storagePolicy, now time.Time, idle bool, firstPhase int) (storageWork, error) {
	var work storageWork
	// Rotate the first phase each tick. Slow detail expiry must not indefinitely
	// starve summary deletion or physical reclamation under a shared time budget.
	phases := []func() error{
		func() error {
			n, err := expireInvocationDetails(ctx, db, successDetailsPredicate, retentionCutoff(now, policy.SuccessDays), policy.Batch)
			if err == nil {
				work.DetailsExpired += n
			}
			return err
		},
		func() error {
			n, err := expireInvocationDetails(ctx, db, failureDetailsPredicate, retentionCutoff(now, policy.FailureDays), policy.Batch)
			if err == nil {
				work.DetailsExpired += n
			}
			return err
		},
		func() error {
			result, err := db.ExecContext(ctx, `DELETE FROM function_invocations WHERE id IN (SELECT id FROM function_invocations WHERE started_at < ? AND status!='running' ORDER BY started_at LIMIT ?)`, retentionCutoff(now, policy.SummaryDays), policy.Batch)
			if err != nil {
				return err
			}
			work.SummariesDeleted, err = result.RowsAffected()
			return err
		},
		func() error {
			if !idle {
				work.VacuumSkipped = "active_work"
				return nil
			}
			var err error
			work.PagesReclaimed, work.VacuumSkipped, err = reclaimStoragePages(ctx, db, policy.Pages)
			return err
		},
	}
	work.StepMS = map[string]int64{}
	work.BatchRows = policy.Batch
	for i := range phases {
		k := (firstPhase + i) % len(phases)
		started := time.Now()
		err := phases[k]()
		work.StepMS[storageStepNames[k]] = time.Since(started).Milliseconds()
		if err != nil {
			if ctx.Err() != nil {
				work.BudgetExhausted = storageStepNames[k]
			}
			return work, fmt.Errorf("%s: %w", storageStepNames[k], err)
		}
	}
	return work, nil
}

func reclaimStoragePages(ctx context.Context, db *sql.DB, limit int) (int64, string, error) {
	if limit <= 0 {
		return 0, "disabled", nil
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, "", err
	}
	defer conn.Close()
	// Immediate transaction checks cross-runtime work under the same writer lock
	// as the vacuum; a new invocation cannot register between the two operations.
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return 0, "", err
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()
	var mode int
	var active bool
	var before, after int64
	if err = conn.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&mode); err != nil {
		return 0, "", err
	}
	if mode == 1 {
		return 0, "full_auto_vacuum", nil
	}
	if mode != 2 {
		return 0, "conversion_required", nil
	}
	if err = conn.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM function_active_work)").Scan(&active); err != nil {
		return 0, "", err
	}
	if active {
		return 0, "active_work", nil
	}
	if err = conn.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&before); err != nil {
		return 0, "", err
	}
	if before == 0 {
		if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
			return 0, "", err
		}
		skipped, err := checkpointStorage(ctx, conn)
		return 0, skipped, err
	}
	rows, err := conn.QueryContext(ctx, fmt.Sprintf("PRAGMA incremental_vacuum(%d)", limit))
	if err != nil {
		return 0, "", err
	}
	for rows.Next() {
	} // step the complete bounded pragma, including all pages
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, "", err
	}
	if err = conn.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&after); err != nil {
		return 0, "", err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return 0, "", err
	}
	skipped, err := checkpointStorage(ctx, conn)
	return before - after, skipped, err
}

// A large physical WAL can have few outstanding frames after being reset and
// reused. Inspect the pinned SQLite driver's NOOP status instead of refusing to
// checkpoint based on the historical high-water file length. Retry even when a
// previous vacuum removed every free page but a reader delayed checkpointing.
func checkpointStorage(ctx context.Context, conn *sql.Conn) (string, error) {
	var busy, frames, checkpointed int64
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(NOOP)").Scan(&busy, &frames, &checkpointed); err != nil {
		return "", err
	}
	if busy != 0 {
		return "checkpoint_busy", nil
	}
	if frames < 0 || frames == checkpointed {
		return "", nil
	}
	var pageSize int64
	if err := conn.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return "", err
	}
	if (frames-checkpointed)*pageSize > 8*1024*1024 {
		return "checkpoint_deferred", nil
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &frames, &checkpointed); err != nil {
		return "", err
	}
	if busy != 0 || frames > checkpointed {
		return "checkpoint_busy", nil
	}
	return "", nil
}

func (p *pool) storageLoop() {
	defer p.maintenanceWG.Done()
	defer p.closeStorageMaintenance()
	ticker := time.NewTicker(time.Duration(envInt("APTEVA_FUNCTIONS_STORAGE_INTERVAL_SECONDS", 5, 1, 3600)) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.life.Done():
			return
		case <-ticker.C:
			p.retainInvocations()
		}
	}
}

// storageMaintenanceDB returns the long-lived maintenance connection. It is
// opened once, outside the per-run budget: resolving the path through the
// shared read pool and opening the file used to spend most of the 100 ms
// budget before any cleanup started, and to fail whenever history readers
// held every read connection.
func (p *pool) storageMaintenanceDB() (*sql.DB, error) {
	p.mu.Lock()
	db := p.storageDB
	p.mu.Unlock()
	if db != nil {
		return db, nil
	}
	ctx, cancel := context.WithTimeout(p.life, 5*time.Second)
	defer cancel()
	path, err := storageDatabasePath(ctx, p.ctx.AppReadDB())
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	if path == "" {
		return nil, nil // in-memory test contexts have no disk to reclaim
	}
	db, err = openStorageMaintenance(path)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.storageDB != nil {
		_ = db.Close()
		return p.storageDB, nil
	}
	p.storageDB = db
	return db, nil
}

func (p *pool) closeStorageMaintenance() {
	p.mu.Lock()
	db := p.storageDB
	p.storageDB = nil
	p.mu.Unlock()
	if db != nil {
		_ = db.Close()
	}
}

// Cleanup is "failing" once no run has completed every step for this long.
const storageFailingAfter = 10 * time.Minute

func (p *pool) retainInvocations() {
	policy := invocationStoragePolicy()
	db, err := p.storageMaintenanceDB()
	if err != nil {
		p.recordStorageRun(storageWork{}, err, policy)
		return
	}
	if db == nil {
		return
	}
	p.mu.Lock()
	idle := p.activeWork == 0 && !p.draining && !p.closed
	firstPhase := p.storageNextPhase
	p.storageNextPhase = (p.storageNextPhase + 1) % 4
	// Adaptive batch: a run that spends its budget halves the batch, a
	// clean fast run grows it back towards the configured size.
	if p.storageBatch <= 0 || p.storageBatch > policy.Batch {
		p.storageBatch = policy.Batch
	}
	policy.Batch = p.storageBatch
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(p.life, time.Duration(policy.BudgetMS)*time.Millisecond)
	started := time.Now()
	work, err := maintainInvocationStorage(ctx, db, policy, time.Now(), idle, firstPhase)
	elapsed := time.Since(started)
	cancel()
	p.mu.Lock()
	switch {
	case work.BudgetExhausted != "":
		p.storageBatch = max(1, p.storageBatch/2)
	case err == nil && elapsed < time.Duration(policy.BudgetMS)*time.Millisecond/2:
		p.storageBatch = min(invocationStoragePolicy().Batch, p.storageBatch*2)
	}
	p.mu.Unlock()
	p.recordStorageRun(work, err, policy)
}

// recordStorageRun keeps the last run's outcome, raises or clears the
// cleanup_failing alert, and runs the hourly health report.
func (p *pool) recordStorageRun(work storageWork, err error, policy storagePolicy) {
	now := time.Now()
	p.mu.Lock()
	p.storageLastWork = work
	if err != nil {
		// A busy database or spent budget is retried on the next tick.
		p.storageLastError = err.Error()
		p.storageFailStreak++
	} else {
		p.storageLastError = ""
		p.storageFailStreak = 0
		p.storageLastComplete = now
	}
	if p.storageLastComplete.IsZero() {
		p.storageLastComplete = now // grace period starts at the first run
	}
	failing := now.Sub(p.storageLastComplete) >= storageFailingAfter
	streak, lastErr := p.storageFailStreak, p.storageLastError
	due := now.Sub(p.lastStorageReport) >= time.Hour
	if due {
		p.lastStorageReport = now
	}
	db := p.storageDB
	p.mu.Unlock()
	if failing {
		p.raiseStorageAlert(storageAlert{"cleanup_failing", fmt.Sprintf("Invocation cleanup has not completed for %d runs (over %s); last error: %s", streak, storageFailingAfter, lastErr)}, map[string]any{"failed_runs": streak, "last_error": lastErr, "step_ms": work.StepMS, "batch_rows": work.BatchRows})
	} else {
		p.clearStorageAlert("cleanup_failing")
	}
	if due && db != nil {
		p.reportStorageHealth(db, policy)
	}
}

// raiseStorageAlert emits storage.alert once when an alert starts; the
// condition is logged on every hourly report while it lasts.
func (p *pool) raiseStorageAlert(a storageAlert, details map[string]any) {
	p.mu.Lock()
	if p.storageAlerts == nil {
		p.storageAlerts = map[string]storageAlert{}
	}
	_, active := p.storageAlerts[a.Code]
	p.storageAlerts[a.Code] = a
	p.mu.Unlock()
	if active {
		return
	}
	p.ctx.Logger().Warn("functions storage alert", "code", a.Code, "message", a.Message)
	payload := map[string]any{"code": a.Code, "message": a.Message}
	for k, v := range details {
		payload[k] = v
	}
	p.ctx.Emit("storage.alert", payload)
}

func (p *pool) clearStorageAlert(code string) {
	p.mu.Lock()
	_, active := p.storageAlerts[code]
	delete(p.storageAlerts, code)
	p.mu.Unlock()
	if !active {
		return
	}
	p.ctx.Logger().Info("functions storage alert cleared", "code", code)
	p.ctx.Emit("storage.alert_cleared", map[string]any{"code": code})
}

type storageAlert struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
type storageReport struct {
	Scope          string         `json:"storage_scope"`
	DatabaseBytes  int64          `json:"database_bytes"`
	WALBytes       int64          `json:"wal_bytes"`
	SHMBytes       int64          `json:"shm_bytes"`
	PageBytes      int64          `json:"page_bytes"`
	ReusableBytes  int64          `json:"reusable_bytes"`
	AutoVacuum     string         `json:"auto_vacuum"`
	OldestRetained string         `json:"oldest_retained,omitempty"`
	CleanupBacklog int            `json:"cleanup_backlog"`
	BacklogCapped  bool           `json:"backlog_capped"`
	Policy         storagePolicy  `json:"retention"`
	Alerts         []storageAlert `json:"alerts"`
	LastWork       storageWork    `json:"last_maintenance"`
	LastError      string         `json:"last_maintenance_error,omitempty"`
	// FailedRuns counts consecutive cleanup runs that did not finish.
	FailedRuns int `json:"failed_maintenance_runs"`
	// CleanupLagSeconds is how far past its cutoff the oldest record still
	// due for cleanup is (0 when nothing is overdue).
	CleanupLagSeconds int64 `json:"cleanup_lag_seconds"`
}

// Physical file metrics are installation-wide; record age/backlog are project
// scoped for API callers. Cap counts instead of COUNT(*) over years of history.
func readStorageReport(ctx context.Context, db *sql.DB, pid string, policy storagePolicy, now time.Time) (storageReport, error) {
	out := storageReport{Scope: "installation", Policy: policy, Alerts: []storageAlert{}}
	var pageSize, pages, free int64
	var mode int
	for _, q := range []struct {
		sql  string
		dest any
	}{{"PRAGMA page_size", &pageSize}, {"PRAGMA page_count", &pages}, {"PRAGMA freelist_count", &free}, {"PRAGMA auto_vacuum", &mode}} {
		if err := db.QueryRowContext(ctx, q.sql).Scan(q.dest); err != nil {
			return out, err
		}
	}
	out.PageBytes = pageSize * pages
	out.ReusableBytes = pageSize * free
	out.AutoVacuum = map[int]string{0: "none", 1: "full", 2: "incremental"}[mode]
	path, err := storageDatabasePath(ctx, db)
	if err != nil {
		return out, err
	}
	if path != "" {
		for _, f := range []struct {
			path string
			dest *int64
		}{{path, &out.DatabaseBytes}, {path + "-wal", &out.WALBytes}, {path + "-shm", &out.SHMBytes}} {
			info, e := os.Stat(f.path)
			if e == nil {
				*f.dest = info.Size()
			} else if !os.IsNotExist(e) {
				return out, e
			}
		}
	}
	where := ""
	args := []any{}
	if pid != "" {
		where = "project_id=? AND "
		args = append(args, pid)
	}
	var oldest sql.NullString
	if err = db.QueryRowContext(ctx, `SELECT started_at FROM function_invocations WHERE `+where+`1 ORDER BY started_at LIMIT 1`, args...).Scan(&oldest); err != nil && err != sql.ErrNoRows {
		return out, err
	}
	out.OldestRetained = oldest.String
	// Each branch uses a retention index and stops after 1001 matches.
	for _, phase := range []struct {
		predicate string
		days      int
	}{{"status!='running'", policy.SummaryDays}, {successDetailsPredicate, policy.SuccessDays}, {failureDetailsPredicate, policy.FailureDays}} {
		remaining := 1001 - out.CleanupBacklog
		if remaining <= 0 {
			break
		}
		a := append(append([]any{}, args...), retentionCutoff(now, phase.days))
		lower := ""
		if phase.predicate != "status!='running'" {
			lower = " AND started_at >= ?"
			a = append(a, retentionCutoff(now, policy.SummaryDays))
		}
		a = append(a, remaining)
		var count int
		if err = db.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT id FROM function_invocations WHERE `+where+phase.predicate+` AND started_at < ?`+lower+` LIMIT ?)`, a...).Scan(&count); err != nil {
			return out, err
		}
		// Rows older than summary retention are counted only in the first branch.
		// Detail branches below are separately restricted to the summary window.
		out.CleanupBacklog += count
		if count == 0 {
			continue
		}
		var due string
		if err = db.QueryRowContext(ctx, `SELECT started_at FROM function_invocations WHERE `+where+phase.predicate+` AND started_at < ?`+lower+` ORDER BY started_at LIMIT 1`, a[:len(a)-1]...).Scan(&due); err != nil && err != sql.ErrNoRows {
			return out, err
		}
		if at, ok := parseStoredTime(due); ok {
			cutoff, _ := parseStoredTime(retentionCutoff(now, phase.days))
			if lag := int64(cutoff.Sub(at).Seconds()); lag > out.CleanupLagSeconds {
				out.CleanupLagSeconds = lag
			}
		}
	}
	out.BacklogCapped = out.CleanupBacklog > 1000
	if mode == 0 {
		out.Alerts = append(out.Alerts, storageAlert{"conversion_required", "Space reclamation requires a planned database conversion; see STORAGE_MANAGEMENT.md."})
	}
	if mode == 2 && policy.Pages == 0 {
		out.Alerts = append(out.Alerts, storageAlert{"reclamation_disabled", "Incremental space reclamation is disabled by configuration."})
	}
	if out.DatabaseBytes+out.WALBytes >= int64(envInt("APTEVA_FUNCTIONS_STORAGE_WARN_GIB", 10, 1, 1024))*1024*1024*1024 {
		out.Alerts = append(out.Alerts, storageAlert{"large_storage", "Functions database and WAL exceed the configured storage warning threshold."})
	}
	if free*pageSize >= 256*1024*1024 && free*5 >= pages {
		out.Alerts = append(out.Alerts, storageAlert{"reusable_space", "At least 20% of the database (256 MiB or more) is reusable space awaiting reclamation."})
	}
	if out.WALBytes >= 512*1024*1024 {
		out.Alerts = append(out.Alerts, storageAlert{"large_wal", "WAL exceeds 512 MiB; check for long-running readers or checkpoint pressure."})
	}
	if out.CleanupLagSeconds >= int64(storageBehindAfter.Seconds()) {
		out.Alerts = append(out.Alerts, storageAlert{"cleanup_behind", fmt.Sprintf("Invocation cleanup is %s behind its retention policy.", (time.Duration(out.CleanupLagSeconds) * time.Second).Round(time.Minute))})
	}
	if out.BacklogCapped {
		out.Alerts = append(out.Alerts, storageAlert{"cleanup_backlog", "More than 1000 expired invocation records await cleanup."})
	}
	return out, nil
}

// Cleanup is "behind" when the oldest record due for cleanup is this late.
const storageBehindAfter = 24 * time.Hour

func (p *pool) reportStorageHealth(db *sql.DB, policy storagePolicy) {
	// Hourly and off the request path: a wider budget than one cleanup run.
	ctx, cancel := context.WithTimeout(p.life, 2*time.Second)
	defer cancel()
	report, err := readStorageReport(ctx, db, "", policy, time.Now())
	if err != nil {
		p.ctx.Logger().Warn("storage health unavailable", "err", err)
		return
	}
	p.addStorageRuntimeStatus(&report)
	raised := map[string]bool{}
	for _, a := range report.Alerts {
		p.ctx.Logger().Warn("functions storage alert", "code", a.Code, "message", a.Message, "database_bytes", report.DatabaseBytes, "wal_bytes", report.WALBytes, "reusable_bytes", report.ReusableBytes, "cleanup_backlog", report.CleanupBacklog, "cleanup_lag_seconds", report.CleanupLagSeconds)
		if a.Code == "cleanup_behind" || a.Code == "cleanup_backlog" {
			raised[a.Code] = true
			p.raiseStorageAlert(a, map[string]any{"cleanup_backlog": report.CleanupBacklog, "backlog_capped": report.BacklogCapped, "cleanup_lag_seconds": report.CleanupLagSeconds})
		}
	}
	for _, code := range []string{"cleanup_behind", "cleanup_backlog"} {
		if !raised[code] {
			p.clearStorageAlert(code)
		}
	}
	if report.LastError != "" {
		p.ctx.Logger().Warn("functions storage maintenance deferred", "err", report.LastError, "step_ms", report.LastWork.StepMS, "batch_rows", report.LastWork.BatchRows)
	}
}
func (p *pool) addStorageRuntimeStatus(report *storageReport) {
	p.mu.Lock()
	report.LastWork = p.storageLastWork
	report.LastError = p.storageLastError
	report.FailedRuns = p.storageFailStreak
	if a, ok := p.storageAlerts["cleanup_failing"]; ok {
		report.Alerts = append(report.Alerts, a)
	}
	p.mu.Unlock()
	if report.LastWork.VacuumSkipped == "checkpoint_deferred" {
		report.Alerts = append(report.Alerts, storageAlert{"checkpoint_deferred", "More than 8 MiB of WAL frames await checkpointing; physical shrinkage is deferred. Check reader/checkpoint pressure and plan maintenance if it persists."})
	}
}
func (a *App) handleHTTPStorage(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		httpErr(w, 405, "GET required")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	report, err := projectStorageReport(r.Context(), pid)
	if err != nil {
		httpErr(w, 503, err.Error())
		return
	}
	httpJSON(w, report)
}
func projectStorageReport(parent context.Context, pid string) (storageReport, error) {
	if strings.TrimSpace(pid) == "" {
		return storageReport{}, fmt.Errorf("project_id required")
	}
	p := currentPool()
	if p == nil {
		return storageReport{}, fmt.Errorf("runtime unavailable")
	}
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	report, err := readStorageReport(ctx, p.ctx.AppReadDB(), pid, invocationStoragePolicy(), time.Now())
	p.addStorageRuntimeStatus(&report)
	return report, err
}
func storageTool() sdk.Tool {
	return sdk.Tool{Name: "functions_storage", Description: "Inspect installation database/WAL size, reusable space, reclamation mode and alerts, plus project-scoped retention age and bounded cleanup backlog.", InputSchema: schemaObject(map[string]any{}, nil), Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
		pid, err := resolveProjectFromArgs(args)
		if err != nil {
			return nil, err
		}
		return projectStorageReport(context.Background(), pid)
	}}
}

// parseStoredTime reads invocation timestamps (RFC3339Nano, SQLite's
// "YYYY-MM-DD HH:MM:SS" default, or a bare cutoff date).
func parseStoredTime(v string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
