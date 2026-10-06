package main

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"sync/atomic"
	"testing"
	"time"
)

func receiveSoon[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(500 * time.Millisecond):
		t.Fatal("immediate work did not arrive within 500 ms")
	}
	var zero T
	return zero
}
func TestRoutingDispatcherCoalescesAndBoundsWork(t *testing.T) {
	var active, peak atomic.Int32
	started := make(chan string, 16)
	gate := make(chan struct{})
	d := newRoutingDispatcher(func(p string) time.Time {
		n := active.Add(1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		started <- p
		<-gate
		active.Add(-1)
		return time.Time{}
	})
	defer d.close()
	defer close(gate)
	d.wake("one")
	if p := receiveSoon(t, started); p != "one" {
		t.Fatal(p)
	}
	for range 10000 {
		d.wake("one")
	}
	d.mu.Lock()
	count := len(d.tasks)
	d.mu.Unlock()
	if count != 1 {
		t.Fatalf("wake burst stored %d projects", count)
	}
	rejected := 0
	for i := 0; i < 1000; i++ {
		if !d.wake(fmt.Sprint("project-", i)) {
			rejected++
		}
	}
	for range routingDispatchWorkers - 1 {
		receiveSoon(t, started)
	}
	d.mu.Lock()
	count = len(d.tasks)
	d.mu.Unlock()
	if count > routingDispatchProjects || rejected == 0 || peak.Load() > routingDispatchWorkers {
		t.Fatalf("count %d rejected %d peak %d", count, rejected, peak.Load())
	}
	// Do not block test cleanup sending started events for remaining queued work.
	go func() {
		for {
			select {
			case <-started:
			case <-d.stop:
				return
			}
		}
	}()
}
func TestRoutingDispatcherUsesDeadlinesWithoutRecoveryPoll(t *testing.T) {
	times := make(chan time.Time, 4)
	var passes atomic.Int32
	d := newRoutingDispatcher(func(string) time.Time {
		times <- time.Now()
		if passes.Add(1) == 1 {
			return time.Now().Add(60 * time.Millisecond)
		}
		return time.Time{}
	})
	defer d.close()
	start := time.Now()
	d.wake("p")
	first := receiveSoon(t, times)
	second := receiveSoon(t, times)
	if first.Sub(start) > 250*time.Millisecond || second.Sub(first) < 50*time.Millisecond || second.Sub(first) > 250*time.Millisecond {
		t.Fatalf("dispatch %s deadline %s", first.Sub(start), second.Sub(first))
	}
}
func TestDecisionCommitWakesImmediatelyAndRollbackDoesNot(t *testing.T) {
	a, db, plan := decisionFixture(t)
	platform := &decisionPlatform{started: make(chan struct{}, 8)}
	ctx := sdk.NewAppCtxForTest(&sdk.Manifest{}, db.db, sdk.Config{}, platform, nil).WithProject("p1")
	globalCtx = ctx
	a.startRoutingDispatcher(ctx)
	t.Cleanup(a.stopRoutingDispatcher)
	db.afterCommit = a.routingCommitted
	// The insertion fails after a decision has been staged. No after-commit wake.
	_, e := db.db.Exec(`CREATE TRIGGER reject_decision BEFORE INSERT ON routing_decisions BEGIN SELECT RAISE(ABORT,'rollback'); END;`)
	if e != nil {
		t.Fatal(e)
	}
	call := testCall("rollback", "pending")
	call.ProjectID = "p1"
	call.CarrierSID = "rollback"
	call.Direction = "inbound"
	if _, _, e = db.insertInboundCallWithEvent(call, "", plan); e == nil {
		t.Fatal("expected rollback")
	}
	select {
	case <-platform.started:
		t.Fatal("rolled-back decision dispatched")
	case <-time.After(30 * time.Millisecond):
	}
	_, _ = db.db.Exec(`DROP TRIGGER reject_decision`)
	started := time.Now()
	insertRingCall(t, db, plan, "immediate")
	receiveSoon(t, platform.started)
	until := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(until) {
		d := getDecision(t, a, "immediate")
		if d.Status == "accepted" {
			if d.DispatchDelayMS > 250 || d.StartedAt == "" {
				t.Fatalf("dispatch timing %+v", d)
			}
			t.Logf("decision commit through accepted offer: %s (dispatch %d ms)", time.Since(started), d.DispatchDelayMS)
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("offer did not commit immediately")
}
func TestImmediateDecisionDeadlineExpiresAndFallbackRuns(t *testing.T) {
	a, db, plan := decisionFixture(t)
	platform := &decisionPlatform{started: make(chan struct{}, 8), release: make(chan struct{})}
	ctx := sdk.NewAppCtxForTest(&sdk.Manifest{}, db.db, sdk.Config{}, platform, nil).WithProject("p1")
	globalCtx = ctx
	// Keep the provider request blocked beyond its deadline; timeout delivery must
	// not wait for it, and releasing it later must not create an offer.
	a.startRoutingDispatcher(ctx)
	t.Cleanup(a.stopRoutingDispatcher)
	t.Cleanup(func() { close(platform.release) })
	db.afterCommit = a.routingCommitted
	insertRingCall(t, db, plan, "deadline")
	receiveSoon(t, platform.started)
	_, _ = db.db.Exec(`UPDATE routing_decisions SET deadline_at=? WHERE call_id='deadline'`, ringTime(time.Now().Add(60*time.Millisecond)))
	a.wakeRouting("p1")
	until := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(until) {
		d := getDecision(t, a, "deadline")
		if d.Status == "timed_out" {
			offers, _ := db.activeRingOffers("deadline", "p1")
			if len(offers) > 0 {
				t.Fatal("timeout offered a call")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("deadline waited for the recovery timer")
}

func TestImmediateDecisionFallbackChainsWithoutRecoveryPoll(t *testing.T) {
	a, db, plan := decisionFixture(t)
	var ex routingExecutionContext
	if err := json.Unmarshal([]byte(plan.ContextJSON), &ex); err != nil {
		t.Fatal(err)
	}
	ex.Definition.Nodes[0].Branches["fallback"] = "second"
	second := ex.Definition.Nodes[0]
	second.ID = "second"
	second.Branches = map[string]string{"fallback": "end"}
	ex.Definition.Nodes = append(ex.Definition.Nodes, second)
	raw, _ := json.Marshal(ex)
	plan.ContextJSON = string(raw)
	insertRingCall(t, db, plan, "chain")
	first := getDecision(t, a, "chain")
	if _, err := db.db.Exec(`UPDATE routing_decisions SET status='running' WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	platform := &decisionPlatform{started: make(chan struct{}, 8)}
	ctx := sdk.NewAppCtxForTest(&sdk.Manifest{}, db.db, sdk.Config{}, platform, nil).WithProject("p1")
	globalCtx = ctx
	a.startRoutingDispatcher(ctx)
	t.Cleanup(a.stopRoutingDispatcher)
	if err := a.completeDecision(first, decisionResponse{DecisionID: first.ID, Action: "fallback"}, ""); err != nil {
		t.Fatal(err)
	}
	receiveSoon(t, platform.started)
	until := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(until) {
		ds, err := a.listDecisions("p1", "chain")
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range ds {
			if d.NodeID == "second" && d.Status == "accepted" {
				if platform.invoked.Load() != 1 {
					t.Fatal("duplicate fallback invocation")
				}
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("fallback waited for recovery polling")
}

func TestRoutingCapacityReleaseWakesPendingDecision(t *testing.T) {
	a, db, plan := decisionFixture(t)
	if len(decisionSlots) != 0 {
		t.Fatal("leaked invocation slots")
	}
	held := cap(decisionSlots)
	for range held {
		decisionSlots <- struct{}{}
	}
	defer func() {
		for range held {
			<-decisionSlots
		}
	}()
	platform := &decisionPlatform{started: make(chan struct{}, 8)}
	ctx := sdk.NewAppCtxForTest(&sdk.Manifest{}, db.db, sdk.Config{}, platform, nil).WithProject("p1")
	globalCtx = ctx
	a.startRoutingDispatcher(ctx)
	t.Cleanup(a.stopRoutingDispatcher)
	db.afterCommit = a.routingCommitted
	insertRingCall(t, db, plan, "saturated")
	select {
	case <-platform.started:
		t.Fatal("exceeded capacity")
	case <-time.After(40 * time.Millisecond):
	}
	<-decisionSlots
	held--
	a.routingCapacityReleased()
	receiveSoon(t, platform.started)
}

func TestRoutingShutdownPreventsNewAdmission(t *testing.T) {
	a, db, plan := decisionFixture(t)
	platform := &decisionPlatform{started: make(chan struct{}, 8)}
	ctx := sdk.NewAppCtxForTest(&sdk.Manifest{}, db.db, sdk.Config{}, platform, nil).WithProject("p1")
	globalCtx = ctx
	a.startRoutingDispatcher(ctx)
	a.stopRoutingDispatcher()
	insertRingCall(t, db, plan, "stopped")
	if err := a.runDecisionTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if platform.invoked.Load() != 0 || getDecision(t, a, "stopped").Status != "pending" {
		t.Fatal("admitted work after shutdown")
	}
}
