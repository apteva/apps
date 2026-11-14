package main

import (
	"errors"
	"strconv"
	"strings"
)

var errWorkerCapacity = errors.New("worker capacity exhausted; queued until a worker slot is released")

func workerCapacityError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "worker capacity exhausted")
}

// Processes admission is shared across this installation's runs and assignments.
// Delegated Core children remain outside this app-owned root-worker budget.
func (a *App) reserveWorker(run string, agent int64, thread string) error {
	limit := 8
	if configured, err := strconv.Atoi(a.ctx.Config().Get("max_workers_per_agent")); err == nil && configured > 0 && configured <= 240 {
		limit = configured
	}
	result, err := a.db.Exec(`INSERT INTO process_worker_slots(agent_id,thread_id,run_id,created_at)
 SELECT ?,?,?,? WHERE (SELECT count(*) FROM process_worker_slots WHERE agent_id=?)<?
 OR EXISTS(SELECT 1 FROM process_worker_slots WHERE agent_id=? AND thread_id=? AND run_id=?)
 ON CONFLICT DO NOTHING`, agent, thread, run, timestamp(), agent, limit, agent, thread, run)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count > 0 {
		return err
	}
	var reserved bool
	if err := a.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM process_worker_slots WHERE agent_id=? AND thread_id=? AND run_id=?)`, agent, thread, run).Scan(&reserved); err != nil {
		return err
	}
	if !reserved {
		return errWorkerCapacity
	}
	return nil
}
