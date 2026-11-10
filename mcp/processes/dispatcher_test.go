package main

import (
	"context"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type dispatcherTestPlatform struct {
	eventPlatform
	firstPublishAt time.Time
}

func (*dispatcherTestPlatform) ListProjects() ([]sdk.PlatformProject, error) {
	return []sdk.PlatformProject{{ID: "project-a"}}, nil
}

func (f *dispatcherTestPlatform) PublishAppEvent(project, id, topic string, data any) error {
	if f.firstPublishAt.IsZero() {
		f.firstPublishAt = time.Now()
	}
	return f.eventPlatform.PublishAppEvent(project, id, topic, data)
}

func startDispatcherForTest(t *testing.T, ctx *sdk.AppCtx) *App {
	t.Helper()
	a := &App{asyncDispatch: true}
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.OnUnmount(ctx) })
	return a
}

func waitForDispatch(t *testing.T, description string, ready func() bool) {
	t.Helper()
	timeout := time.Now().Add(4 * time.Second)
	for !ready() {
		if time.Now().After(timeout) {
			t.Fatalf("dispatcher did not %s", description)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDispatcherRecoversCommittedCleanupAndScopesWakes(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	f := &dispatcherTestPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(f))
	base := &App{}
	if err := base.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	p := status(t, base, create(t, base, def()).ID, "active")
	r := Run{ID: "terminal-run", ProcessID: p.ID, Version: p.Version, Kind: "manual", RequestKey: "startup", CreatedAt: timestamp(), Backend: "agent"}
	if err := insertProcessRun(base.db, r); err != nil {
		t.Fatal(err)
	}
	if _, err := base.db.Exec(`INSERT INTO process_run_workers(run_id,agent_id,thread_id,created_at) VALUES(?,7,'lost-wake-worker',?)`, r.ID, timestamp()); err != nil {
		t.Fatal(err)
	}
	if _, err := base.db.Exec(`UPDATE process_runs SET state='completed' WHERE id=?`, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := base.db.Exec(`INSERT INTO process_event_outbox(project_id,topic,payload_json) VALUES('hidden','process.updated','{}')`); err != nil {
		t.Fatal(err)
	}
	a := startDispatcherForTest(t, ctx)
	waitForDispatch(t, "recover the terminal worker and publish visible events", func() bool {
		var done bool
		err := a.db.QueryRow(`SELECT completed_at<>'' AND NOT EXISTS(SELECT 1 FROM process_event_outbox WHERE project_id=?) FROM process_worker_cleanup WHERE run_id=?`, p.ProjectID, r.ID).Scan(&done)
		if err != nil {
			t.Fatal(err)
		}
		return done
	})
	// This edit has no timer due. The committed mutation must wake the outbox.
	if _, err := a.execute(p.ProjectID, "operator", "pause", map[string]any{"process_id": p.ID}); err != nil {
		t.Fatal(err)
	}
	a.wakeDispatch("hidden")
	waitForDispatch(t, "publish the new state change", func() bool {
		return eventCount(t, a) == 1 // Only the hidden event remains queued.
	})
	_ = a.OnUnmount(ctx)
	if len(f.kills) != 1 || f.kills[0].ThreadID != "lost-wake-worker" {
		t.Fatalf("unexpected recovered cleanup: %+v", f.kills)
	}
	found := false
	for _, event := range f.accepted {
		if event.project != p.ProjectID {
			t.Fatalf("published another project's event: %+v", event)
		}
		found = found || (event.topic == "process.state_changed" && event.data["to_state"] == "paused")
	}
	if !found {
		t.Fatal("pause event was not published")
	}
}

func TestDispatcherPropagatesParentChangesAfterCommit(t *testing.T) {
	base, _, p, tr := triggerSetup(t)
	a := startDispatcherForTest(t, base.ctx)
	waitForDispatch(t, "drain the startup outbox", func() bool { return eventCount(t, a) == 0 })
	for _, change := range []struct {
		action  string
		enabled bool
	}{{"pause", false}, {"activate", true}} {
		if _, err := a.execute(p.ProjectID, "operator", change.action, map[string]any{"process_id": p.ID}); err != nil {
			t.Fatal(err)
		}
		waitForDispatch(t, "reconcile the subscription", func() bool {
			fresh, err := a.trigger(p.ProjectID, p.ID, tr.ID)
			if err != nil {
				t.Fatal(err)
			}
			return fresh.SubscriptionEnabled == change.enabled && !fresh.SyncPending
		})
	}
}

func TestDispatcherWakesForSavedScheduleAndTimedStep(t *testing.T) {
	base, _, p, r := timedSetup(t, false)
	finishStep(t, base, p, r, "research", "agent:7:t", "sent", "")
	first, next := stepBy(t, base, r, "research"), stepBy(t, base, r, "write")
	scheduled := status(t, base, create(t, base, def()).ID, "active")
	x := enableAssignment(t, base, scheduled, addAssignment(t, base, scheduled, "Scheduled", 7, "agent", &Schedule{Kind: "interval", Every: "1m"}, nil))
	at := time.Now().UTC().Add(350 * time.Millisecond)
	for _, change := range []struct {
		sql, stamp, id string
	}{
		{`UPDATE process_step_runs SET completed_at=? WHERE id=?`, at.Add(-10 * time.Minute).Format(time.RFC3339Nano), first.ID},
		{`UPDATE process_step_runs SET start_at=? WHERE id=?`, at.Format(time.RFC3339Nano), next.ID},
		{`UPDATE process_assignments SET next_run_at=? WHERE id=?`, at.Format(time.RFC3339Nano), x.ID},
	} {
		if _, err := base.db.Exec(change.sql, change.stamp, change.id); err != nil {
			t.Fatal(err)
		}
	}
	// No action or callback wakes this app after mounting.
	ctx := sdk.NewAppCtxForTest(nil, base.db, nil, &dispatcherTestPlatform{}, nil)
	a := startDispatcherForTest(t, ctx)
	waitForDispatch(t, "dispatch the timed workflow step and scheduled occurrence", func() bool {
		runs, err := a.dispatches(scheduled.ID)
		if err != nil {
			t.Fatal(err)
		}
		return len(runs) == 1 && runs[0].DeliveredAt != "" && stepBy(t, a, r, "write").DeliveredAt != ""
	})
	run := savedRun(t, a, scheduled)
	if run.ScheduledFor != at.Format(time.RFC3339Nano) {
		t.Fatalf("unexpected occurrence: %+v", run)
	}
}

func TestDispatcherRetriesOutboxInOrderAndReturnsToIdle(t *testing.T) {
	f := &dispatcherTestPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(f))
	at := time.Now().UTC().Add(350 * time.Millisecond)
	if _, err := ctx.AppDB().Exec(`INSERT INTO process_event_outbox(project_id,event_id,topic,payload_json,next_attempt_at)
 VALUES('project-a','first','process.updated','{}',?),('project-a','second','process.updated','{}','')`, at.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	a := startDispatcherForTest(t, ctx)
	waitForDispatch(t, "publish the delayed outbox batch", func() bool { return eventCount(t, a) == 0 })
	_ = a.OnUnmount(ctx)
	if f.firstPublishAt.Before(at) || len(f.accepted) != 2 || f.accepted[0].id != "first" || f.accepted[1].id != "second" {
		t.Fatalf("outbox published before its deadline or out of order: %+v", f.accepted)
	}
	d := newProcessDispatcher(a)
	defer d.cancel()
	if state := d.dispatchProject("project-a", projectDispatch{}); !state.next.IsZero() || len(a.Workers()) != 0 {
		t.Fatalf("idle dispatcher still has work scheduled: %+v", state)
	}
}

func TestRunWakeUsesClaimAndReminderBackoff(t *testing.T) {
	a, _, p, r := sequentialSetup(t)
	now := time.Now().UTC()
	step := stepBy(t, a, r, "research")
	retryAt := now.Add(time.Minute)
	if _, err := a.db.Exec(`UPDATE process_step_runs SET delivered_at=?,claim_next_at=?,next_attempt_at=? WHERE id=?`,
		now.Add(-unclaimedStepAfter).Format(time.RFC3339Nano), now.Add(time.Second).Format(time.RFC3339Nano), retryAt.Format(time.RFC3339Nano), step.ID); err != nil {
		t.Fatal(err)
	}
	if at, err := a.nextRunWake(context.Background(), p.ProjectID, now); err != nil || !at.Equal(retryAt) {
		t.Fatalf("claim recovery deadline = %v, want %v: %v", at, retryAt, err)
	}
	if _, err := a.db.Exec(`UPDATE process_step_runs SET state='running',updated_at=?,next_attempt_at='' WHERE id=?`,
		now.Add(-staleStepReminderAfter+time.Second).Format(time.RFC3339Nano), step.ID); err != nil {
		t.Fatal(err)
	}
	if at, err := a.nextRunWake(context.Background(), p.ProjectID, now); err != nil || !at.Equal(now.Add(time.Second)) {
		t.Fatalf("stale-worker deadline = %v, want %v: %v", at, now.Add(time.Second), err)
	}
}
