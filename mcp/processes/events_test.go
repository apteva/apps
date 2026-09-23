package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type publishedEvent struct {
	project, id, topic string
	data               map[string]any
}
type eventPlatform struct {
	triggerPlatform
	accepted    []publishedEvent
	failProject string
	lost        bool
}

func (f *eventPlatform) PublishAppEvent(project, id, topic string, data any) error {
	if project == f.failProject {
		return errors.New("offline")
	}
	f.accepted = append(f.accepted, publishedEvent{project, id, topic, data.(map[string]any)})
	if f.lost {
		f.lost = false
		return errors.New("acknowledgement lost")
	}
	return nil
}
func publisher(t *testing.T, a *App, f *eventPlatform) *App {
	t.Helper()
	return &App{db: a.db, ctx: tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(f))}
}
func eventCount(t *testing.T, a *App) int {
	t.Helper()
	var n int
	if e := a.db.QueryRow(`SELECT count(*) FROM process_event_outbox`).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func TestEventsAtomicAndSilentForRollbackAndNoop(t *testing.T) {
	a, _, p := directSetup(t)
	n := eventCount(t, a)
	tx, e := a.db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(`UPDATE processes SET status='paused' WHERE id=?`, p.ID); e != nil {
		t.Fatal(e)
	}
	tx.Rollback()
	if eventCount(t, a) != n {
		t.Fatal("rolled back event escaped")
	}
	if _, e = a.db.Exec(`UPDATE processes SET status=status,updated_at=? WHERE id=?`, timestamp(), p.ID); e != nil {
		t.Fatal(e)
	}
	if eventCount(t, a) != n {
		t.Fatal("no-op emitted")
	}
	if _, e = a.db.Exec(`UPDATE processes SET status='paused' WHERE id=?`, p.ID); e != nil {
		t.Fatal(e)
	}
	if eventCount(t, a) <= n {
		t.Fatal("committed change missing")
	}
}
func TestEventsRetryAfterRestartRetainsIDAndOrder(t *testing.T) {
	a, _, _ := directSetup(t)
	f := &eventPlatform{lost: true}
	p := publisher(t, a, f)
	if e := p.drainEvents(context.Background()); e == nil {
		t.Fatal("lost acknowledgement not reported")
	}
	if len(f.accepted) != 1 {
		t.Fatal("later project events overtook failed event")
	}
	first := f.accepted[0]
	a.db.Exec(`UPDATE process_event_outbox SET next_attempt_at=''`)
	restarted := &App{db: a.db, ctx: p.ctx}
	if e := restarted.drainEvents(context.Background()); e != nil {
		t.Fatal(e)
	}
	if f.accepted[1].id != first.id || f.accepted[1].data["revision"] != first.data["revision"] {
		t.Fatal("retry identity changed")
	}
	if eventCount(t, a) != 0 {
		t.Fatal("acknowledged events left pending")
	}
	if e := restarted.drainEvents(context.Background()); e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	for i, e := range f.accepted {
		if i == 1 {
			continue
		}
		if seen[e.id] {
			t.Fatal("duplicate after acknowledgement")
		}
		seen[e.id] = true
		if e.project != "project-a" || e.data["schema_version"] != 1 || e.data["event_id"] != e.id {
			t.Fatal("bad event envelope", e)
		}
	}
}
func TestEventsProjectFailureDoesNotStarveOtherProjects(t *testing.T) {
	a, _, _ := directSetup(t)
	// More than a publication batch in an unavailable project.
	for i := 0; i < 220; i++ {
		if _, e := a.db.Exec(`INSERT INTO process_event_outbox(project_id,topic,payload_json) VALUES('project-a','task.updated','{}')`); e != nil {
			t.Fatal(e)
		}
	}
	a.db.Exec(`INSERT INTO process_event_outbox(project_id,topic,payload_json) VALUES('project-b','task.updated','{}')`)
	f := &eventPlatform{failProject: "project-a"}
	p := publisher(t, a, f)
	_ = p.drainEvents(context.Background())
	if e := p.drainEvents(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(f.accepted) != 1 || f.accepted[0].project != "project-b" {
		t.Fatal("other project starved", len(f.accepted))
	}
	if eventCount(t, a) < 220 {
		t.Fatal("lost failed events")
	}
}
func TestEventsMultiAgentGenericStepCompletionContract(t *testing.T) {
	a, _, p, r := workflowSetup(t)
	finishStep(t, a, p, r, "research", "agent:8:t", "Research evidence", "")
	finishStep(t, a, p, r, "write", "agent:7:t", "Draft", "")
	finishStep(t, a, p, r, "review", "operator", "Approved", "approved")
	finishStep(t, a, p, r, "publish", "agent:8:t", "Published URL", "")
	before := eventCount(t, a)
	finishStep(t, a, p, r, "review", "operator", "Approved", "approved")
	if eventCount(t, a) != before {
		t.Fatal("duplicate completion emitted again")
	}
	f := &eventPlatform{}
	pub := publisher(t, a, f)
	if e := pub.drainEvents(context.Background()); e != nil {
		t.Fatal(e)
	}
	declared := map[string]bool{}
	for _, d := range a.Manifest().Provides.Publishes {
		declared[d.Name] = true
	}
	completed := 0
	agents := map[float64]bool{}
	for _, e := range f.accepted {
		if !declared[e.topic] {
			t.Fatal("undeclared event", e.topic)
		}
		if e.data["run_id"] != r.ID {
			continue
		}
		if e.data["process_id"] != p.ID || e.data["assignment_id"] != r.AssignmentID {
			t.Fatal("missing scope", e)
		}
		if strings.Contains(e.topic, "approval") {
			t.Fatal("approval-specific event emitted", e.topic)
		}
		if e.topic == "run.state_changed" && e.data["to_state"] == "completed" {
			completed++
		}
		if x, ok := e.data["executor"].(map[string]any); ok {
			if id, ok := x["agent_id"].(float64); ok {
				agents[id] = true
			}
		}
		if _, ok := e.data["output"]; ok {
			t.Fatal("full result leaked into bus")
		}
		if _, ok := e.data["decision"]; ok {
			t.Fatal("removed decision field leaked into bus")
		}
	}
	if completed != 1 || !agents[7] || !agents[8] {
		t.Fatalf("contract complete=%d agents=%v", completed, agents)
	}
}
func TestEventsNativeTaskAndDeliveryRecovery(t *testing.T) {
	t.Skip("legacy task event namespace assertions replaced by step.* events")
	a, f := nativeSetup(t)
	f.lose = true
	s := newNative(t, a, "operator", "delivery", taskConfig())
	a.db.Exec(`UPDATE process_step_runs SET next_attempt_at='' WHERE id=?`, s.ID)
	if e := a.tickDirect(context.Background(), time.Now()); e != nil {
		t.Fatal(e)
	}
	nativeUpdate(t, a, s, "agent:7:t", map[string]any{"state": "completed", "output": "Evidence"})
	rec := &eventPlatform{}
	if e := publisher(t, a, rec).drainEvents(context.Background()); e != nil {
		t.Fatal(e)
	}
	retry, delivered, done := false, false, false
	for _, e := range rec.accepted {
		if e.data["task_id"] != s.ID {
			continue
		}
		if e.data["origin"] != "standalone" {
			t.Fatal("wrong origin")
		}
		retry = retry || e.topic == "delivery.state_changed" && e.data["to_state"] == "retrying"
		delivered = delivered || e.topic == "delivery.state_changed" && e.data["to_state"] == "delivered"
		done = done || e.topic == "task.state_changed" && e.data["to_state"] == "completed"
	}
	if !retry || !delivered || !done {
		t.Fatalf("missing retry/recovery/completion: %v %v %v", retry, delivered, done)
	}
}
func TestEventsTriggerProcessing(t *testing.T) {
	a, _, _, tr := triggerSetup(t)
	if e := a.receiveTriggerEvent(a.ctx, signupEvent(tr, "bus-contract")); e != nil {
		t.Fatal(e)
	}
	rows, e := a.db.Query(`SELECT payload_json FROM process_event_outbox WHERE topic='trigger.event_processed'`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var raw string
		rows.Scan(&raw)
		var data map[string]any
		json.Unmarshal([]byte(raw), &data)
		if data["source_event_id"] == "bus-contract" && data["trigger_id"] == tr.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("trigger outcome event missing")
	}
	_ = sdk.AppBusDeliveryEvent
}
