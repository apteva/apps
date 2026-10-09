package main

import (
	"context"
	"database/sql"
	"net/http"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const projectionCleanupEvery = "@every 45s"
const projectionMaintenanceBudget = 150 * time.Millisecond
const projectionDefinitionCacheLimit = 1024

type projectionSchedulerState struct {
	storageDB            *sql.DB
	definitionDB         *sql.DB
	mu                   sync.Mutex
	wake                 chan struct{}
	storageReady         bool
	storageGeneration    uint64
	definitionGeneration uint64
	definitionEpoch      int64
	definitions          []*projectionDefinition
	cleanupAfter         int64
	metrics              map[string]projectionWorkerMetrics
}

type projectionWorkerMetrics struct {
	Ticks               uint64 `json:"ticks"`
	IdleTicks           uint64 `json:"idle_ticks"`
	DefinitionLoads     uint64 `json:"definition_loads"`
	DefinitionCacheHits uint64 `json:"definition_cache_hits"`
	RefreshJobs         uint64 `json:"refresh_jobs"`
	FailedRefreshes     uint64 `json:"failed_refreshes"`
	CleanupBatches      uint64 `json:"cleanup_batches"`
	PrunedChanges       uint64 `json:"confirmed_pruned_change_records"`
	IdleCheckNs         int64  `json:"idle_check_ns"`
	ConsumptionNs       int64  `json:"change_consumption_ns"`
	CapacityWaitNs      int64  `json:"capacity_wait_ns"`
	ReadWaitNs          int64  `json:"read_connection_wait_ns"`
	SQLNs               int64  `json:"sql_execution_ns"`
	RefreshNs           int64  `json:"refresh_ns"`
	CleanupNs           int64  `json:"cleanup_ns"`
	EventNs             int64  `json:"event_delivery_ns"`
	UpdatedAt           string `json:"updated_at,omitempty"`
}

func (a *App) updateWorkerMetrics(pid string, update func(*projectionWorkerMetrics)) {
	a.scheduler.mu.Lock()
	defer a.scheduler.mu.Unlock()
	if a.scheduler.metrics == nil {
		a.scheduler.metrics = map[string]projectionWorkerMetrics{}
	}
	if _, ok := a.scheduler.metrics[pid]; !ok && len(a.scheduler.metrics) >= maxSchemaCacheEntries {
		return
	}
	m := a.scheduler.metrics[pid]
	update(&m)
	m.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	a.scheduler.metrics[pid] = m
}
func (a *App) workerMetrics(pid string) projectionWorkerMetrics {
	a.scheduler.mu.Lock()
	defer a.scheduler.mu.Unlock()
	return a.scheduler.metrics[pid]
}
func (a *App) projectionWakeChannel() chan struct{} {
	a.scheduler.mu.Lock()
	defer a.scheduler.mu.Unlock()
	if a.scheduler.wake == nil {
		a.scheduler.wake = make(chan struct{}, 1)
	}
	return a.scheduler.wake
}
func (a *App) wakeProjectionWorker() {
	select {
	case a.projectionWakeChannel() <- struct{}{}:
	default:
	}
}

// Coalesce local notifications without spawning a goroutine per mutation. The
// durable database is authoritative; the separate one-second worker covers raw
// SQL, other processes, lost wakeups and installations without dispatched projects.
func (a *App) projectionWakeWorker(ctx context.Context, app *sdk.AppCtx) error {
	wake := a.projectionWakeChannel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		// A wakeup that arrives during a periodic tick waits for it, retaining
		// one follow-up pass. Further notifications coalesce in the channel.
		a.projectionWorkerMu.Lock()
		if ctx.Err() != nil {
			a.projectionWorkerMu.Unlock()
			return nil
		}
		err := a.projectionWorkerTick(ctx, app.WithProject(""))
		a.projectionWorkerMu.Unlock()
		if err != nil && ctx.Err() == nil {
			app.Logger().Warn("projection wakeup failed", "err", err)
		}
	}
}
func (a *App) ensureWorkerStorage(app *sdk.AppCtx) error {
	a.scheduler.mu.Lock()
	defer a.scheduler.mu.Unlock()
	generation := app.AppDBGeneration()
	if a.scheduler.storageReady && a.scheduler.storageGeneration == generation && a.scheduler.storageDB == app.AppDB() {
		return nil
	}
	if err := a.ensureProjectionStorage(app); err != nil {
		return err
	}
	a.scheduler.storageDB = app.AppDB()
	a.scheduler.storageReady = true
	a.scheduler.storageGeneration = generation
	a.scheduler.definitions = nil
	return nil
}
func definitionEpoch(ctx context.Context, app *sdk.AppCtx) (int64, error) {
	var epoch int64
	err := app.AppReadDB().QueryRowContext(ctx, `SELECT epoch FROM projection_definition_epoch WHERE singleton=1`).Scan(&epoch)
	return epoch, err
}

// Only the worker uses this cache. Public permission checks and status queries
// retain their ordinary live database reads. Callers must refresh mutable fields.
func (a *App) workerDefinitions(ctx context.Context, app *sdk.AppCtx, pid string) ([]*projectionDefinition, int64, error) {
	epoch, err := definitionEpoch(ctx, app)
	if err != nil {
		return nil, 0, err
	}
	hit, loaded := false, false
	defer func() {
		if hit || loaded {
			a.updateWorkerMetrics(pid, func(m *projectionWorkerMetrics) {
				if hit {
					m.DefinitionCacheHits++
				}
				if loaded {
					m.DefinitionLoads++
				}
			})
		}
	}()
	a.scheduler.mu.Lock()
	defer a.scheduler.mu.Unlock()
	generation := app.AppDBGeneration()
	if a.scheduler.definitions != nil && a.scheduler.definitionGeneration == generation && a.scheduler.definitionDB == app.AppDB() && a.scheduler.definitionEpoch == epoch {
		hit = true
		return selectWorkerDefinitions(a.scheduler.definitions, pid), epoch, nil
	}
	rows, err := app.AppReadDB().QueryContext(ctx, projectionSelect+`ORDER BY id`)
	if err != nil {
		return nil, epoch, err
	}
	defs := make([]*projectionDefinition, 0)
	for rows.Next() {
		p, err := decodeProjection(app, rows)
		if err != nil {
			rows.Close()
			return nil, epoch, err
		}
		// These fields are runtime state, never served from this cache.
		p.Built = false
		p.Latest = 0
		p.Published = 0
		p.PublishedAt = sql.NullInt64{}
		p.LastFailure = sql.NullString{}
		defs = append(defs, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, epoch, err
	}
	after, err := definitionEpoch(ctx, app)
	if err != nil {
		return nil, epoch, err
	}
	if after == epoch && len(defs) <= projectionDefinitionCacheLimit {
		a.scheduler.definitionDB = app.AppDB()
		a.scheduler.definitionGeneration = generation
		a.scheduler.definitionEpoch = epoch
		a.scheduler.definitions = defs
	} else {
		a.scheduler.definitions = nil
	}
	loaded = true
	return selectWorkerDefinitions(defs, pid), epoch, nil
}
func selectWorkerDefinitions(defs []*projectionDefinition, pid string) []*projectionDefinition {
	var out []*projectionDefinition
	for _, p := range defs {
		if pid == "" || p.ProjectID == pid {
			copy := *p
			out = append(out, &copy)
		}
	}
	return out
}

// One indexed durable work check avoids decoding definitions, write claims and
// cleanup transactions on idle ticks. Paused versions consume invalidations but
// their queues cannot be claimed. Retired versions participate in neither path.
func (a *App) projectionWorkProjects(ctx context.Context, app *sdk.AppCtx) ([]string, error) {
	rows, err := app.AppReadDB().QueryContext(ctx, `SELECT DISTINCT p.project_id FROM projection_definitions p JOIN projection_cursors c ON c.projection_id=p.id WHERE (?='' OR p.project_id=?) AND p.status IN ('active','paused','building') AND (EXISTS(SELECT 1 FROM projection_changes x WHERE x.project_id=p.project_id AND x.change_id>c.last_change_id) OR (p.status IN ('active','building') AND EXISTS(SELECT 1 FROM projection_queue q WHERE q.projection_id=p.id AND q.due_at_ms<=? AND (q.claimed_until IS NULL OR q.claimed_until<=CURRENT_TIMESTAMP) AND (p.built=1 OR q.scope_key='__all__')))) ORDER BY p.project_id`, app.CurrentProject(), app.CurrentProject(), a.projectionTime().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []string
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			return nil, err
		}
		projects = append(projects, pid)
	}
	return projects, rows.Err()
}

func (a *App) projectionCleanupWorker(parent context.Context, app *sdk.AppCtx) error {
	if !a.projectionCleanupMu.TryLock() {
		return nil
	}
	defer a.projectionCleanupMu.Unlock()
	if err := a.ensureWorkerStorage(app); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, projectionMaintenanceBudget)
	defer cancel()
	release, wait, err := a.acquireCapacity(ctx, app, projectionCapacityKind)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer release()
	a.updateWorkerMetrics(app.CurrentProject(), func(m *projectionWorkerMetrics) { m.CapacityWaitNs += wait.Nanoseconds() })
	if err := a.pruneRetiredProjectionLeases(ctx, app); err != nil {
		return err
	}
	pruneCtx, pruneCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	err = a.pruneProjectionChanges(pruneCtx, app)
	pruneCancel()
	if err != nil {
		return err
	}
	defs, _, err := a.workerDefinitions(ctx, app, app.CurrentProject())
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	a.scheduler.mu.Lock()
	after := a.scheduler.cleanupAfter
	a.scheduler.mu.Unlock()
	// Rotate to prevent long cleanup backlogs starving later definitions/projects.
	for pass := 0; pass < 2; pass++ {
		for _, p := range defs {
			if (pass == 0 && p.ID <= after) || (pass == 1 && p.ID > after) {
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			a.scheduler.mu.Lock()
			a.scheduler.cleanupAfter = p.ID
			a.scheduler.mu.Unlock()
			start := time.Now()
			batches := uint64(0)
			for i := 0; i < 32 && ctx.Err() == nil; i++ {
				removed, err := a.cleanupProjectionGenerations(ctx, app, p)
				if err != nil {
					if ctx.Err() != nil {
						return nil
					}
					return err
				}
				if !removed {
					break
				}
				batches++
			}
			elapsed := time.Since(start)
			a.updateWorkerMetrics(p.ProjectID, func(m *projectionWorkerMetrics) { m.CleanupNs += elapsed.Nanoseconds(); m.CleanupBatches += batches })
			if batches > 0 {
				// Do not overwrite concurrent refresh measurements with an older snapshot.
				_, err := app.AppDB().ExecContext(ctx, `UPDATE projection_definitions SET last_cleanup_ms=? WHERE id=?`, elapsed.Milliseconds(), p.ID)
				if err != nil && ctx.Err() == nil {
					return err
				}
				if err == nil {
					a.projectionMetricsMu.Lock()
					if a.projectionMetrics == nil {
						a.projectionMetrics = map[int64]projectionPhaseMetrics{}
					}
					m := a.projectionMetrics[p.ID]
					m.Cleanup = elapsed.Milliseconds()
					a.projectionMetrics[p.ID] = m
					a.projectionMetricsMu.Unlock()
				}
			}
			a.scheduler.mu.Lock()
			a.scheduler.cleanupAfter = p.ID
			a.scheduler.mu.Unlock()
		}
	}
	return nil
}
func (a *App) pruneRetiredProjectionLeases(ctx context.Context, app *sdk.AppCtx) error {
	var exists bool
	if err := app.AppReadDB().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projection_retired_leases WHERE claimed_until<=CURRENT_TIMESTAMP LIMIT 1)`).Scan(&exists); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if !exists {
		return nil
	}
	_, err := app.AppDB().ExecContext(ctx, `DELETE FROM projection_retired_leases WHERE rowid IN (SELECT rowid FROM projection_retired_leases WHERE claimed_until<=CURRENT_TIMESTAMP LIMIT 256)`)
	if ctx.Err() != nil {
		return nil
	}
	return err
}
func (a *App) pruneProjectionChanges(ctx context.Context, app *sdk.AppCtx) error {
	projects := []string{app.CurrentProject()}
	if projects[0] == "" {
		rows, err := app.AppReadDB().QueryContext(ctx, `SELECT DISTINCT project_id FROM projection_changes`)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		projects = nil
		for rows.Next() {
			var pid string
			if err := rows.Scan(&pid); err != nil {
				rows.Close()
				return err
			}
			projects = append(projects, pid)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
	const floor = `COALESCE((SELECT MIN(c.last_change_id) FROM projection_cursors c JOIN projection_definitions p ON p.id=c.projection_id WHERE c.project_id=? AND p.status IN ('active','paused','building')),9223372036854775807)`
	batches := 0
	for _, pid := range projects {
		// Evaluate the project cursor floor once per statement, instead of testing
		// every consumer for every change record. Recheck under the writer lock so
		// concurrent definition creation cannot cause unconsumed changes to be lost.
		for batches < 32 && ctx.Err() == nil {
			var exists bool
			if err := app.AppReadDB().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM projection_changes WHERE project_id=? AND change_id<=`+floor+` LIMIT 1)`, pid, pid).Scan(&exists); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			if !exists {
				break
			}
			result, err := app.AppDB().ExecContext(ctx, `DELETE FROM projection_changes WHERE change_id IN (SELECT change_id FROM projection_changes WHERE project_id=? AND change_id<=`+floor+` ORDER BY change_id LIMIT 512)`, pid, pid)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			removed, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if removed == 0 {
				break
			}
			a.updateWorkerMetrics(pid, func(m *projectionWorkerMetrics) { m.PrunedChanges += uint64(removed) })
			batches++
		}
	}
	return nil
}
func (a *App) toolProjectionWorkerStatus(app *sdk.AppCtx, args map[string]any) (any, error) {
	cp := map[string]any{}
	for k, v := range args {
		cp[k] = v
	}
	cp["name"] = "*"
	scoped, finish, err := a.beginOperation(app, cp, "projections_worker_status", false)
	if err != nil {
		return nil, err
	}
	defer finish()
	return map[string]any{"metrics": a.workerMetrics(scoped.CurrentProject()), "metrics_since": "process_start", "cleanup_interval_seconds": 45, "refresh_fallback_interval_seconds": 1}, nil
}
func (a *App) handleProjectionWorkerStatus(w http.ResponseWriter, r *http.Request) {
	if globalCtx == nil {
		httpErr(w, http.StatusServiceUnavailable, "app not yet mounted")
		return
	}
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	out, err := a.toolProjectionWorkerStatus(requestAppCtx(r), injectProject(r, map[string]any{}))
	writeToolResult(w, out, err)
}
