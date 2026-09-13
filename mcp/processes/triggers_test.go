package main

import (
	"context"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type triggerPlatform struct {
	directPlatform
	subscriptions map[string]sdk.AppEventSubscription
	offline       bool
}

func (f *triggerPlatform) PutAppEventSubscription(s sdk.AppEventSubscription) error {
	if f.offline {
		return errors.New("platform offline")
	}
	f.subscriptions[s.Key] = s
	return nil
}
func (f *triggerPlatform) DeleteAppEventSubscription(project, key string) error {
	delete(f.subscriptions, key)
	return nil
}
func (f *triggerPlatform) ListAppEventSources(project string) ([]sdk.AppEventSource, error) {
	return []sdk.AppEventSource{{InstallID: 41, App: "signup", Name: "Signup", Events: []sdk.EventDecl{{Name: "customer.signed_up"}}}}, nil
}
func (f *triggerPlatform) PublishAppEvent(project, id, topic string, data any) error { return nil }
func triggerSetup(t *testing.T) (*App, *triggerPlatform, *Process, Trigger) {
	t.Helper()
	f := &triggerPlatform{subscriptions: map[string]sdk.AppEventSubscription{}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(f))
	a := &App{}
	if e := a.OnMount(ctx); e != nil {
		t.Fatal(e)
	}
	d := workflowDefinition()
	d.Parameters = []Parameter{{Key: "customer_id", Type: "string", Required: true}}
	p := create(t, a, d)
	x := p.Assignments[0]
	x.Roles = map[string]Executor{"researcher": {Kind: "agent", AgentID: 7}, "writer": {Kind: "agent", AgentID: 8}, "reviewer": {Kind: "agent", AgentID: 10}, "publisher": {Kind: "agent", AgentID: 7}}
	if _, e := a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, x.AssignmentConfig); e != nil {
		t.Fatal(e)
	}
	tr, e := a.saveTrigger(p.ProjectID, p.ID, x.ID, "", 0, TriggerConfig{Name: "Pro onboarding", SourceInstallID: 41, Topic: "customer.signed_up", Filters: []EventFilter{{Path: "data.plan", Op: "eq", Value: "pro"}}, Mappings: map[string]string{"customer_id": "data.id"}})
	if e != nil {
		t.Fatal(e)
	}
	p = status(t, a, p.ID, "active")
	tr, e = a.setTriggerStatus(p.ProjectID, p.ID, tr.ID, "active", tr.Revision)
	if e != nil || tr.SyncPending {
		t.Fatalf("trigger %+v %v", tr, e)
	}
	return a, f, p, tr
}
func signupEvent(tr Trigger, id string) sdk.Event {
	return sdk.Event{Event: sdk.AppBusDeliveryEvent, DeliveryID: "delivery-" + id, SourceApp: "signup", SourceInstallID: 41, ProjectID: tr.ProjectID, Data: map[string]any{"event_id": id, "subscription_key": tr.ID, "revision": float64(tr.SubscriptionRevision), "topic": "customer.signed_up", "data": map[string]any{"id": "customer-123", "plan": "pro"}}}
}
func TestEventStartsMultiAgentWorkflowWithFrozenInputs(t *testing.T) {
	a, f, p, tr := triggerSetup(t)
	ev := signupEvent(tr, "one")
	if e := a.receiveTriggerEvent(a.ctx, ev); e != nil {
		t.Fatal(e)
	}
	runs, e := a.dispatches(p.ID)
	if e != nil || len(runs) != 1 {
		t.Fatalf("runs %v %v", runs, e)
	}
	r := runs[0]
	if r.Binding.Parameters["customer_id"] != "customer-123" || r.TriggerEventID == "" || len(f.events) != 1 || f.events[0].AgentID != 7 {
		t.Fatalf("bad event run %+v", r)
	}
	finishStep(t, a, p, r, "research", "agent:7:main", "research for customer-123", "")
	if len(f.events) != 2 || f.events[1].AgentID != 8 {
		t.Fatal("writer not dispatched")
	}
	finishStep(t, a, p, r, "write", "agent:8:main", "draft for customer-123", "")
	if f.events[2].AgentID != 10 {
		t.Fatal("reviewer not dispatched")
	}
	if stepBy(t, a, r, "publish").State != "pending" {
		t.Fatal("approval bypassed")
	}
	finishStep(t, a, p, r, "review", "agent:10:main", "approved customer-123", "approved")
	finishStep(t, a, p, r, "publish", "agent:7:main", "simulated receipt", "")
	r, e = a.getRun(p.ProjectID, p.ID, r.ID)
	if e != nil || r.State != "completed" {
		t.Fatalf("run %+v %v", r, e)
	}
	hist, e := a.triggerHistory(tr)
	if e != nil || len(hist) != 1 || hist[0]["run_id"] != r.ID || hist[0]["status"] != "started" {
		t.Fatal(hist, e)
	}
}
func TestEventConcurrentRedeliveryAndAppRestartIsIdempotent(t *testing.T) {
	a, f, p, tr := triggerSetup(t)
	ev := signupEvent(tr, "one")
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- a.receiveTriggerEvent(a.ctx, ev) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	restarted := &App{ctx: a.ctx, db: a.db}
	if e := restarted.receiveTriggerEvent(a.ctx, ev); e != nil {
		t.Fatal(e)
	}
	runs, _ := a.dispatches(p.ID)
	if len(runs) != 1 || len(f.events) != 1 {
		t.Fatal("duplicate execution", len(runs), len(f.events))
	}
}
func TestTriggerPreviewFiltersMappingAndNoRun(t *testing.T) {
	a, _, p, tr := triggerSetup(t)
	preview, e := a.execute(p.ProjectID, "operator", "trigger_preview", map[string]any{"process_id": p.ID, "trigger_id": tr.ID, "event": map[string]any{"topic": tr.Config.Topic, "data": map[string]any{"id": "abc", "plan": "pro"}}})
	if e != nil || preview.(map[string]any)["matched"] != true {
		t.Fatal(preview, e)
	}
	runs, _ := a.dispatches(p.ID)
	if len(runs) != 0 {
		t.Fatal("preview started work")
	}
	for name, edit := range map[string]func(*sdk.Event){"filter": func(e *sdk.Event) { e.Data["data"].(map[string]any)["plan"] = "free" }, "missing": func(e *sdk.Event) { delete(e.Data["data"].(map[string]any), "id") }, "type": func(e *sdk.Event) { e.Data["data"].(map[string]any)["id"] = 42.0 }, "topic": func(e *sdk.Event) { e.Data["topic"] = "customer.deleted" }, "source": func(e *sdk.Event) { e.SourceInstallID = 99 }} {
		t.Run(name, func(t *testing.T) {
			ev := signupEvent(tr, name)
			edit(&ev)
			if e := a.receiveTriggerEvent(a.ctx, ev); e != nil {
				t.Fatal(e)
			}
		})
	}
	runs, _ = a.dispatches(p.ID)
	if len(runs) != 0 {
		t.Fatal("bad event started work")
	}
	hist, _ := a.triggerHistory(tr)
	if len(hist) != 5 {
		t.Fatal(hist)
	}
}
func TestTriggerPauseAssignmentPauseAndStaleRevision(t *testing.T) {
	a, _, p, tr := triggerSetup(t)
	stale := signupEvent(tr, "stale")
	paused, e := a.setTriggerStatus(p.ProjectID, p.ID, tr.ID, "paused", tr.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.receiveTriggerEvent(a.ctx, signupEvent(paused, "paused")); e != nil {
		t.Fatal(e)
	}
	tr, e = a.setTriggerStatus(p.ProjectID, p.ID, tr.ID, "active", paused.Revision)
	if e != nil {
		t.Fatal(e)
	}
	a.receiveTriggerEvent(a.ctx, stale)
	if _, e = a.assignmentStatus(p.ProjectID, p.ID, tr.AssignmentID, "paused"); e != nil {
		t.Fatal(e)
	}
	a.receiveTriggerEvent(a.ctx, signupEvent(tr, "assignment-paused"))
	runs, _ := a.dispatches(p.ID)
	if len(runs) != 0 {
		t.Fatal("paused work started")
	}
	if e = a.tickTriggers(context.Background()); e != nil {
		t.Fatal(e)
	}
	fresh, _ := a.trigger(p.ProjectID, p.ID, tr.ID)
	if fresh.SubscriptionEnabled {
		t.Fatal("paused assignment still subscribed")
	}
}
func TestTriggerRapidParentPauseResumeInvalidatesQueuedEvents(t *testing.T) {
	for _, parent := range []string{"process", "assignment"} {
		t.Run(parent, func(t *testing.T) {
			a, f, p, tr := triggerSetup(t)
			stale := signupEvent(tr, "queued-before-pause")
			for _, status := range []string{"paused", "active"} {
				var err error
				if parent == "process" {
					_, err = a.changeStatus(p.ProjectID, p.ID, status)
				} else {
					_, err = a.assignmentStatus(p.ProjectID, p.ID, tr.AssignmentID, status)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			// No worker has run between pause, resume and this delayed delivery.
			if err := a.receiveTriggerEvent(a.ctx, stale); err != nil {
				t.Fatal(err)
			}
			runs, _ := a.dispatches(p.ID)
			if len(runs) != 0 {
				t.Fatal("queued event crossed a pause/resume boundary")
			}
			if err := a.tickTriggers(context.Background()); err != nil {
				t.Fatal(err)
			}
			fresh, err := a.trigger(p.ProjectID, p.ID, tr.ID)
			if err != nil || fresh.SubscriptionRevision <= tr.SubscriptionRevision || fresh.SyncPending || !f.subscriptions[tr.ID].Enabled {
				t.Fatalf("listener failed to resume: %+v, %v", fresh, err)
			}
			if err := a.receiveTriggerEvent(a.ctx, signupEvent(fresh, "after-resume")); err != nil {
				t.Fatal(err)
			}
			runs, _ = a.dispatches(p.ID)
			if len(runs) != 1 {
				t.Fatal("fresh event did not start work")
			}
		})
	}
}
func TestTriggerCrossProjectAndTransactionRollback(t *testing.T) {
	a, f, p, tr := triggerSetup(t)
	ev := signupEvent(tr, "one")
	ev.ProjectID = "other"
	if e := a.receiveTriggerEvent(a.ctx, ev); e == nil {
		t.Fatal("cross project accepted")
	}
	if _, e := a.trigger("other", p.ID, tr.ID); e == nil {
		t.Fatal("history leaked")
	}
	_, e := a.db.Exec(`CREATE TRIGGER fail_receipt BEFORE INSERT ON process_trigger_events BEGIN SELECT RAISE(ABORT,'injected failure'); END`)
	if e != nil {
		t.Fatal(e)
	}
	ev = signupEvent(tr, "one")
	if e = a.receiveTriggerEvent(a.ctx, ev); e == nil {
		t.Fatal("failed receipt acknowledged")
	}
	runs, _ := a.dispatches(p.ID)
	if len(runs) != 0 || len(f.events) != 0 {
		t.Fatal("non-atomic reservation")
	}
	a.db.Exec(`DROP TRIGGER fail_receipt`)
	if e = a.receiveTriggerEvent(a.ctx, ev); e != nil {
		t.Fatal(e)
	}
}
func TestTriggerAgentDeliveryFailureRecoversWithoutAnotherRun(t *testing.T) {
	a, f, p, tr := triggerSetup(t)
	f.lose = true
	if e := a.receiveTriggerEvent(a.ctx, signupEvent(tr, "one")); e != nil {
		t.Fatal(e)
	}
	a.db.Exec(`UPDATE process_step_runs SET next_attempt_at=''`)
	if e := a.tickDirect(context.Background(), time.Now()); e != nil {
		t.Fatal(e)
	}
	runs, _ := a.dispatches(p.ID)
	if len(runs) != 1 || len(f.events) != 2 || f.events[0].SourceEventID != f.events[1].SourceEventID {
		t.Fatal("retry changed run or event")
	}
}
func TestTriggerSyncFailureVisibleAndRetried(t *testing.T) {
	a, f, p, tr := triggerSetup(t)
	f.offline = true
	tr, e := a.setTriggerStatus(p.ProjectID, p.ID, tr.ID, "paused", tr.Revision)
	if e != nil || !tr.SyncPending || !strings.Contains(tr.SyncError, "offline") {
		t.Fatal(tr, e)
	}
	f.offline = false
	if e = a.tickTriggers(context.Background()); e != nil {
		t.Fatal(e)
	}
	tr, _ = a.trigger(p.ProjectID, p.ID, tr.ID)
	if tr.SyncPending {
		t.Fatal(tr)
	}
}
func TestEventFilterTypesAndExistence(t *testing.T) {
	root := map[string]any{"data": map[string]any{"n": 12.0, "flag": false}}
	for _, f := range []EventFilter{{Path: "data.absent", Op: "exists", Value: false}, {Path: "data.n", Op: "gte", Value: 12.0}, {Path: "data.flag", Op: "eq", Value: false}} {
		if !filterMatches(f, root) {
			t.Fatal(f)
		}
	}
	if filterMatches(EventFilter{Path: "data.n", Op: "eq", Value: "12"}, root) {
		t.Fatal("coerced type")
	}
}
func TestTriggerHTTPDoesNotAllowPathOverride(t *testing.T) {
	a, _, p, tr := triggerSetup(t)
	raw, _ := json.Marshal(map[string]any{"trigger_id": "other", "process_id": "other", "event": map[string]any{"topic": tr.Config.Topic, "data": map[string]any{"id": "123", "plan": "pro"}}})
	req := httptest.NewRequest("POST", "/processes/"+p.ID+"/triggers/"+tr.ID+"/preview", strings.NewReader(string(raw)))
	req.Header.Set("X-Apteva-Project-ID", p.ProjectID)
	res := httptest.NewRecorder()
	a.handleHTTP(res, req)
	if res.Code != 200 || !strings.Contains(res.Body.String(), `"matched":true`) {
		t.Fatal(res.Code, res.Body.String())
	}
}
func TestTriggerTestKeyCannotChangeInputs(t *testing.T) {
	a, _, p, tr := triggerSetup(t)
	args := map[string]any{"process_id": p.ID, "trigger_id": tr.ID, "idempotency_key": "sample", "event": map[string]any{"topic": tr.Config.Topic, "data": map[string]any{"id": "first", "plan": "pro"}}}
	if _, e := a.execute(p.ProjectID, "operator", "trigger_test_run", args); e != nil {
		t.Fatal(e)
	}
	args["event"].(map[string]any)["data"].(map[string]any)["id"] = "second"
	if _, e := a.execute(p.ProjectID, "operator", "trigger_test_run", args); e == nil {
		t.Fatal("changed inputs accepted")
	}
}

func TestFailedEventRetryKeepsHistoryAndStartsOnlyOnce(t *testing.T) {
	a, _, p, tr := triggerSetup(t)
	ev := signupEvent(tr, "bad-mapping")
	ev.Data["data"].(map[string]any)["correct_id"] = "customer-456"
	delete(ev.Data["data"].(map[string]any), "id")
	if e := a.receiveTriggerEvent(a.ctx, ev); e != nil {
		t.Fatal(e)
	}
	hist, _ := a.triggerHistory(tr)
	if hist[0]["status"] != "failed" {
		t.Fatal(hist)
	}
	paused, e := a.setTriggerStatus(p.ProjectID, p.ID, tr.ID, "paused", tr.Revision)
	if e != nil {
		t.Fatal(e)
	}
	paused.Config.Mappings["customer_id"] = "data.correct_id"
	updated, e := a.saveTrigger(p.ProjectID, p.ID, tr.AssignmentID, tr.ID, paused.Revision, paused.Config)
	if e != nil {
		t.Fatal(e)
	}
	updated, e = a.setTriggerStatus(p.ProjectID, p.ID, tr.ID, "active", updated.Revision)
	if e != nil {
		t.Fatal(e)
	}
	args := map[string]any{"process_id": p.ID, "trigger_id": tr.ID, "event_record_id": hist[0]["id"], "idempotency_key": "fixed"}
	for i := 0; i < 2; i++ {
		if _, e = a.execute(p.ProjectID, "operator", "trigger_event_retry", args); e != nil {
			t.Fatal(e)
		}
	}
	runs, _ := a.dispatches(p.ID)
	if len(runs) != 1 || runs[0].Binding.Parameters["customer_id"] != "customer-456" {
		t.Fatal(runs)
	}
	hist, _ = a.triggerHistory(updated)
	if len(hist) != 2 {
		t.Fatal("lost original failure", hist)
	}
}
