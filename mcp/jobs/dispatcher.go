package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// One installation-wide pool. SDK worker callbacks only wake it: they must
// never perform blocking per-project work or omit the projectless partition.
type dispatcher struct {
	app         *sdk.AppCtx
	ctx         context.Context
	cancel      context.CancelFunc
	notify      chan struct{}
	done        chan struct{}
	slots       chan struct{}
	wg          sync.WaitGroup
	lastProject string
	lastTick    atomic.Int64
	failures    atomic.Int64
}
type runtimeDispatchKey struct{}

func newDispatcher(app *sdk.AppCtx) *dispatcher {
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), runtimeDispatchKey{}, true))
	return &dispatcher{app: app, ctx: ctx, cancel: cancel, notify: make(chan struct{}, 1), done: make(chan struct{}), slots: make(chan struct{}, atoiDefault(app.Config().Get("dispatch_concurrency"), 8, 50))}
}
func (d *dispatcher) start() { go d.run() }
func (d *dispatcher) stop()  { d.cancel(); <-d.done }
func (d *dispatcher) wake() {
	select {
	case d.notify <- struct{}{}:
	default:
	}
}
func (d *dispatcher) run() {
	defer close(d.done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	maintenance := time.NewTicker(time.Minute)
	defer maintenance.Stop()
	d.wake()
	for {
		select {
		case <-d.ctx.Done():
			d.wg.Wait()
			return
		case <-ticker.C:
			d.fill()
		case <-maintenance.C:
			if err := pruneHistory(d.ctx, d.app.AppDB(), "", false, d.app.Config(), time.Now().UTC()); err != nil {
				d.failure(err)
			}
		case <-d.notify:
			d.fill()
		}
	}
}
func (d *dispatcher) fill() {
	d.lastTick.Store(time.Now().Unix())
	ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
	defer cancel()
	rows, err := d.app.AppDB().QueryContext(ctx, `SELECT DISTINCT project_id FROM jobs WHERE status IN ('pending','running') ORDER BY project_id`)
	if err != nil {
		d.failure(err)
		return
	}
	projects := []string{}
	installProject := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID"))
	for rows.Next() {
		var pid string
		if err = rows.Scan(&pid); err != nil {
			break
		}
		if installProject == "" || pid == installProject {
			projects = append(projects, pid)
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		d.failure(err)
		return
	}
	if rowErr != nil {
		d.failure(rowErr)
		return
	}
	if len(projects) == 0 {
		return
	}
	start := 0
	for i, p := range projects {
		if p > d.lastProject {
			start = i
			break
		}
	}
	misses := 0
	for len(d.slots) < cap(d.slots) && misses < len(projects) && ctx.Err() == nil {
		pid := projects[start%len(projects)]
		start++
		d.lastProject = pid
		// Capacity is reserved before any database claim, so leases never age
		// while jobs wait in an in-memory batch.
		d.slots <- struct{}{}
		job, err := claimJob(ctx, d.app.AppDB(), pid, 0)
		if err != nil {
			<-d.slots
			d.failure(err)
			return
		}
		if job == nil {
			<-d.slots
			misses++
			continue
		}
		misses = 0
		d.wg.Add(1)
		go func(j *Job) {
			defer d.wg.Done()
			defer func() { <-d.slots; d.wake() }()
			if err := dispatchOne(d.ctx, d.app.WithProject(j.ProjectID), j); err != nil {
				d.failure(err)
			}
		}(job)
	}
}
func (d *dispatcher) failure(err error) {
	d.failures.Add(1)
	d.app.Logger().Warn("dispatcher infrastructure failure", "err", err)
}

func claimJob(ctx context.Context, db *sql.DB, pid string, id int64) (*Job, error) {
	token, err := newLeaseToken()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339)
	// Separate partial-index paths avoid scanning terminal history or sorting
	// entire project partitions to find due work.
	query := `UPDATE jobs SET status='running',lease_until=?,lease_token=?,updated_at=? WHERE id=(
 SELECT id FROM (
 SELECT id,next_run_at AS due FROM jobs WHERE project_id=? AND status='pending' AND next_run_at<=? AND (?=0 OR id=?)
 UNION ALL
 SELECT id,COALESCE(lease_until,'') AS due FROM jobs WHERE project_id=? AND status='running' AND (lease_until IS NULL OR lease_until<?) AND (?=0 OR id=?)
 ) ORDER BY due,id LIMIT 1) RETURNING id`
	var claimed int64
	err = db.QueryRowContext(ctx, query, now.Add(leaseTTL).Format(time.RFC3339), token, stamp, pid, stamp, id, id, pid, stamp, id, id).Scan(&claimed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim: %w", err)
	}
	j, err := dbGetJob(db, pid, claimed, ctx)
	if err != nil {
		_, markErr := db.ExecContext(ctx, `UPDATE jobs SET status='failed',last_error='Invalid stored job data',next_run_at=NULL,lease_token=NULL,lease_until=NULL WHERE id=? AND lease_token=?`, claimed, token)
		return nil, errors.Join(err, markErr)
	}
	if j == nil {
		return nil, errors.New("claimed job disappeared")
	}
	j.LeaseToken = token
	return j, nil
}

// Synchronous single-project tick used by tests and explicit maintenance.
// Snapshot IDs only; each job is claimed after a slot becomes available.
func dispatchTick(ctx context.Context, app *sdk.AppCtx) error {
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := app.AppDB().QueryContext(ctx, `SELECT id FROM jobs WHERE project_id=? AND ((status='pending' AND next_run_at<=?) OR (status='running' AND (lease_until IS NULL OR lease_until<?))) ORDER BY next_run_at,id LIMIT ?`, app.CurrentProject(), now, now, atoiDefault(app.Config().Get("dispatch_batch_size"), 20, 200))
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	sem := make(chan struct{}, atoiDefault(app.Config().Get("dispatch_concurrency"), 8, 50))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for _, id := range ids {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		}
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			defer func() { <-sem }()
			job, err := claimJob(ctx, app.AppDB(), app.CurrentProject(), id)
			if err == nil && job != nil {
				err = dispatchOne(ctx, app, job)
			}
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(id)
	}
	wg.Wait()
	return errors.Join(errs...)
}

func beginAttempt(ctx context.Context, db *sql.DB, j *Job, started time.Time) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE jobs SET lease_until=? WHERE id=? AND project_id=? AND status='running' AND lease_token=? AND lease_until>=?`, started.Add(leaseTTL).Format(time.RFC3339), j.ID, j.ProjectID, j.LeaseToken, started.Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return 0, err
	}
	logID := j.ID
	if j.ParentJobID != 0 {
		logID = j.ParentJobID
	}
	if _, err = tx.ExecContext(ctx, `UPDATE job_runs SET status='interrupted',finished_at=?,error='Lease expired; delivery outcome is unknown' WHERE project_id=? AND execution_job_id=? AND status='running' AND lease_token<>?`, started.Format(time.RFC3339Nano), j.ProjectID, j.ID, j.LeaseToken); err != nil {
		return 0, err
	}
	var nextRunID int64
	if err := tx.QueryRowContext(ctx, `UPDATE run_sequence SET value=MAX(value,(SELECT COALESCE(MAX(id),0) FROM job_runs))+1 RETURNING value`).Scan(&nextRunID); err != nil {
		return 0, err
	}
	res, err = tx.ExecContext(ctx, `INSERT INTO job_runs(id,project_id,job_id,started_at,status,attempt,scheduled_for,idempotency_key,lease_token,execution_job_id) VALUES(?,?,?,?,'running',?,?,?,?,?)`, nextRunID, j.ProjectID, logID, started.Format(time.RFC3339Nano), j.Attempt+1, j.ScheduledFor, nullStr(occurrenceIdempotencyKey(j)), j.LeaseToken, j.ID)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}
func renewLease(ctx context.Context, db *sql.DB, j *Job, cancel context.CancelFunc, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			q, c := context.WithTimeout(ctx, 5*time.Second)
			res, err := db.ExecContext(q, `UPDATE jobs SET lease_until=? WHERE id=? AND project_id=? AND status='running' AND lease_token=?`, time.Now().UTC().Add(leaseTTL).Format(time.RFC3339), j.ID, j.ProjectID, j.LeaseToken)
			c()
			if err != nil {
				cancel()
				return
			}
			n, err := res.RowsAffected()
			if err != nil || n == 0 {
				cancel()
				return
			}
		}
	}
}
func retryDelay(seconds, attempt int) time.Duration {
	if seconds > 86400 {
		seconds = 86400
	}
	if seconds < 1 {
		seconds = 30
	}
	delay := time.Duration(seconds) * time.Second
	if delay < time.Second {
		delay = 30 * time.Second
	}
	for i := 1; i < attempt && delay < 24*time.Hour; i++ {
		delay *= 2
	}
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	return delay
}
