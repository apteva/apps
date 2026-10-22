package main

import (
	"context"
	"database/sql"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTerminalWorkerCleanup(t *testing.T) {
	for _, state := range []string{"completed", "failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			a, f, p, r := workflowSetup(t)
			first := stepBy(t, a, r, "research")
			if state == "cancelled" {
				if _, err := a.cancelWorkflow(p.ProjectID, "operator", p.ID, r.ID, "operator stopped"); err != nil {
					t.Fatal(err)
				}
			} else if state == "failed" {
				if _, err := a.stepAction(p.ProjectID, "agent:8:"+first.ThreadID, p.ID, r.ID, first.ID, "step_update", map[string]any{"state": "failed", "error": "fixture failed", "output": "accepted operation id 123"}); err != nil {
					t.Fatal(err)
				}
			} else {
				finishStep(t, a, p, r, "research", "agent:8:t", "notes", "")
				finishStep(t, a, p, r, "write", "agent:7:t", "draft", "")
				finishStep(t, a, p, r, "review", "operator", "Approved", "")
				finishStep(t, a, p, r, "publish", "agent:8:t", "receipt-123", "")
			}
			run, err := a.getRun(p.ProjectID, p.ID, r.ID)
			if err != nil || run.State != state {
				t.Fatal(run, err)
			}
			if err = a.cleanupWorkers(context.Background(), time.Now(), p.ProjectID); err != nil {
				t.Fatal(err)
			}
			if len(f.kills) != len(f.threads) {
				t.Fatalf("workers not stopped: spawned %d killed %d", len(f.threads), len(f.kills))
			}
			for _, w := range f.threads {
				found := false
				for _, k := range f.kills {
					if k.AgentID == w.AgentID && k.ThreadID == w.ThreadID {
						found = true
					}
				}
				if !found {
					t.Fatal("missing kill", w)
				}
			}
			before := len(f.kills)
			if err = a.cleanupWorkers(context.Background(), time.Now().Add(time.Hour), p.ProjectID); err != nil {
				t.Fatal(err)
			}
			if len(f.kills) != before {
				t.Fatal("successful cleanup retried")
			}
			after, _ := a.getRun(p.ProjectID, p.ID, r.ID)
			if after.State != run.State || after.Result != run.Result {
				t.Fatal("cleanup changed saved outcome")
			}
			if _, err = a.stepAction(p.ProjectID, "agent:8:"+first.ThreadID, p.ID, r.ID, first.ID, "step_get", nil); err != nil {
				t.Fatal("history unreadable", err)
			}
		})
	}
}

func TestWorkerCleanupBackoffRestartAndScope(t *testing.T) {
	a, f, p, r := workflowSetup(t)
	if _, err := a.cancelWorkflow(p.ProjectID, "operator", p.ID, r.ID, "stop"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	f.killError = errors.New("Core temporarily unavailable")
	if err := a.cleanupWorkers(context.Background(), now, "other"); err != nil || len(f.kills) != 0 {
		t.Fatal("cross-project cleanup", err)
	}
	if err := a.cleanupWorkers(context.Background(), now, ""); err != nil || len(f.kills) != 0 {
		t.Fatal("empty project cleanup", err)
	}
	if err := a.cleanupWorkers(context.Background(), now, p.ProjectID); err == nil {
		t.Fatal("lost kill error")
	}
	var attempts int
	var msg, completed string
	if err := a.db.QueryRow(`SELECT attempts,last_error,completed_at FROM process_worker_cleanup WHERE run_id=?`, r.ID).Scan(&attempts, &msg, &completed); err != nil || attempts != 1 || msg == "" || completed != "" {
		t.Fatal(attempts, msg, completed, err)
	}
	// Reopen a saved database, with no in-memory cleanup state carried over.
	checkpoint := filepath.Join(t.TempDir(), "restarted.db")
	if _, err := a.db.Exec(`VACUUM INTO ?`, checkpoint); err != nil {
		t.Fatal(err)
	}
	restored, err := sql.Open("sqlite", checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restarted := &App{ctx: a.ctx, db: restored}
	f.killError = nil
	if err := restarted.cleanupWorkers(context.Background(), now.Add(time.Second), p.ProjectID); err != nil || len(f.kills) != 1 {
		t.Fatal("backoff lost", err)
	}
	if err := restarted.cleanupWorkers(context.Background(), now.Add(6*time.Second), p.ProjectID); err != nil || len(f.kills) != 2 {
		t.Fatal("restart did not recover", err)
	}
	if err := restarted.db.QueryRow(`SELECT completed_at FROM process_worker_cleanup WHERE run_id=?`, r.ID).Scan(&completed); err != nil || completed == "" {
		t.Fatal("missing shutdown acknowledgement", err)
	}
}

func TestTerminalWorkerRejectsLateEventsAndDelivery(t *testing.T) {
	a, f, p, r := workflowSetup(t)
	s := stepBy(t, a, r, "research")
	if _, err := a.cancelWorkflow(p.ProjectID, "operator", p.ID, r.ID, "stop"); err != nil {
		t.Fatal(err)
	}
	if err := a.stepLifecycle(sdk.Event{ProjectID: p.ProjectID, SourceApp: "apteva-server", InstanceID: s.Executor.AgentID}, &sdk.AgentEventLifecycle{SourceEventID: s.DeliveryEventID, ThreadID: s.ThreadID, Type: "event.error", Reason: "late", Sequence: 99}); err != nil {
		t.Fatal(err)
	}
	after := stepBy(t, a, r, "research")
	if after.State != s.State || after.Error != s.Error || after.LifecycleSequence != s.LifecycleSequence {
		t.Fatal("late callback changed terminal run", after)
	}
	all, _ := a.steps(r.ID)
	s.DeliveredAt = "" // stale pre-cancellation dispatch snapshot
	if err := a.deliverStep(p, r, &s, all); err == nil {
		t.Fatal("stale dispatch accepted")
	}
	if err := a.spawnSequentialWorker(p, &r, &s, all); err == nil {
		t.Fatal("stale provisioning accepted")
	}
	if len(f.events) != 1 || len(f.threads) != 1 {
		t.Fatal("terminal worker revived")
	}
}

func TestWorkerCleanupUpgradeBackfillAndMainSafety(t *testing.T) {
	a, f, p, r := workflowSetup(t)
	// Reapply the new migration over an old cancelled-run database.
	for _, q := range []string{`DROP TRIGGER process_terminal_worker_cleanup`, `DROP TABLE process_worker_cleanup`, `DROP TABLE process_run_terminal_events`, `DROP VIEW process_owned_workers`, `UPDATE process_runs SET state='cancelled' WHERE id=?`} {
		if _, err := a.db.Exec(q, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.db.Exec(`INSERT INTO process_run_workers(run_id,agent_id,thread_id,created_at) VALUES(?,7,'main',?)`, r.ID, timestamp()); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/020_worker_cleanup.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	if err = a.cleanupWorkers(context.Background(), time.Now(), p.ProjectID); err != nil {
		t.Fatal(err)
	}
	if len(f.kills) != 1 || f.kills[0].ThreadID == "main" {
		t.Fatal("backfill missed worker or killed main", f.kills)
	}
}

func TestWorkerWaitParksWithoutTimer(t *testing.T) {
	r := Run{State: "blocked"}
	s := StepRun{State: "blocked"}
	done, next := workerAction(r, s, "agent:7:test", "", nil)
	if done || !strings.Contains(next, "clear_wake=true") {
		t.Fatal("blocked worker must park", next)
	}
}

func TestWorkerCleanupLeavesLiveRunAndDirectMainAlone(t *testing.T) {
	a, f, p, r := workflowSetup(t)
	first := stepBy(t, a, r, "research")
	if _, err := a.stepAction(p.ProjectID, "agent:8:"+first.ThreadID, p.ID, r.ID, first.ID, "step_update", map[string]any{"state": "blocked", "error": "permission needed", "output": "accepted id 123"}); err != nil {
		t.Fatal(err)
	}
	if err := a.cleanupWorkers(context.Background(), time.Now(), p.ProjectID); err != nil || len(f.kills) != 0 {
		t.Fatal("live blocked worker stopped", err)
	}
	direct, df, dp := directSetup(t)
	raw, err := direct.start(dp.ProjectID, dp.ID, "main-only", "")
	if err != nil {
		t.Fatal(err)
	}
	dr := raw.(map[string]any)["run"].(Run)
	if _, err = direct.directRun(dp.ProjectID, "agent:7:main", dp.ID, dr.ID, "run_update", map[string]any{"state": "completed", "result": "receipt"}); err != nil {
		t.Fatal(err)
	}
	if err = direct.cleanupWorkers(context.Background(), time.Now(), dp.ProjectID); err != nil || len(df.kills) != 0 {
		t.Fatal("direct run killed main", err)
	}
}

func TestWorkerCleanupIntentRollsBackWithRun(t *testing.T) {
	a, f, p, r := workflowSetup(t)
	tx, err := a.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE process_runs SET state='cancelled' WHERE id=?`, r.ID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = a.cleanupWorkers(context.Background(), time.Now(), p.ProjectID); err != nil || len(f.kills) != 0 {
		t.Fatal("rolled back cancellation stopped worker", err)
	}
}

func TestWorkerCleanupNotifiesCoordinatorWithDurableRetry(t *testing.T) {
	a, f, p, r := workflowSetup(t)
	if _, err := a.cancelWorkflow(p.ProjectID, "operator", p.ID, r.ID, "operator stop"); err != nil {
		t.Fatal(err)
	}
	f.lose = true // Main accepts the notification, but its acknowledgement is lost.
	now := time.Now().UTC()
	if err := a.cleanupWorkers(context.Background(), now, p.ProjectID); err == nil {
		t.Fatal("notification loss was hidden")
	}
	if len(f.kills) != 1 {
		t.Fatal("notification failure prevented worker shutdown")
	}
	first := f.events[len(f.events)-1]
	if first.SourceEventID != "process-run-terminal:"+r.ID || first.AgentID != r.Binding.OwnerAgentID || first.ThreadID != "owner-default" {
		t.Fatal("wrong coordinator notification", first)
	}
	if err := a.cleanupWorkers(context.Background(), now.Add(6*time.Second), p.ProjectID); err != nil {
		t.Fatal(err)
	}
	retry := f.events[len(f.events)-1]
	if first.SourceEventID != retry.SourceEventID || first.ThreadID != retry.ThreadID || first.Message != retry.Message {
		t.Fatal("notification retry identity changed")
	}
	if len(f.kills) != 1 {
		t.Fatal("notification retry killed worker again")
	}
	var delivered string
	if err := a.db.QueryRow(`SELECT delivered_at FROM process_run_terminal_events WHERE run_id=?`, r.ID).Scan(&delivered); err != nil || delivered == "" {
		t.Fatal("notification not acknowledged", err)
	}
	events := len(f.events)
	if err := a.cleanupWorkers(context.Background(), now.Add(time.Hour), p.ProjectID); err != nil || len(f.events) != events {
		t.Fatal("acknowledged terminal event resent", err)
	}
}
