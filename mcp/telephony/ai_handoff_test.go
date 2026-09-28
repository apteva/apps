package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func aiRetryDue(t *testing.T, a *App, call string) {
	t.Helper()
	if _, err := a.db().db.Exec(`UPDATE ai_handoffs SET next_attempt_at='' WHERE call_id=?`, call); err != nil {
		t.Fatal(err)
	}
}
func aiJournal(t *testing.T, a *App, call string) *aiHandoff {
	t.Helper()
	h, err := a.aiHandoff(call)
	if err != nil || h == nil {
		t.Fatalf("journal=%+v err=%v", h, err)
	}
	return h
}
func TestAIStartupFailureClassification(t *testing.T) {
	cases := []struct {
		message string
		retry   bool
	}{
		{`platform /api/apps/callback/threads/spawn-realtime: http 502: spawn realtime thread "tel-test": HTTP 400 no realtime provider registered`, false},
		{`platform /api/apps/callback/threads/spawn-realtime: http 502: spawn realtime thread "tel-test": HTTP 500 invalid provider configuration`, false},
		{`platform /api/apps/callback/threads/spawn-realtime: http 502: spawn realtime thread "tel-test": HTTP 503 unavailable`, true},
		{`platform /api/apps/callback/threads/spawn-realtime: http 429: retry later`, true},
		{`platform /api/apps/callback/threads/spawn-realtime: http 401: unauthorized`, false},
		{`platform /api/apps/callback/threads/spawn-realtime: http 502: failed after possibly accepting startup`, false},
		{`provider said HTTP 503 but this is not a platform envelope`, false},
		{`unexpected EOF`, false},
	}
	for _, c := range cases {
		t.Run(c.message, func(t *testing.T) {
			_, retry := aiStartupFailure(fmt.Errorf("spawn: %w", errors.New(c.message)))
			if retry != c.retry {
				t.Fatalf("retry=%t", retry)
			}
		})
	}
}

func TestAIHandoffPermanentFailureStopsDecisionReplay(t *testing.T) {
	a, ctx, p, _, row, unblock := preparationFixture(t, true)
	p.failure = errors.New(`platform /api/apps/callback/threads/spawn-realtime: http 502: spawn realtime thread "test": HTTP 400 no realtime provider`)
	unblock()
	// Exercise the same delivery branch as production rather than just its helper.
	if _, err := a.db().db.Exec(`UPDATE calls SET carrier_slug='telnyx' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	row, _ = a.db().findCall(row.ID)
	_, plan, err := a.routingPlanForCall(row, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(plan)
	d := decisionRecord{ID: "ai-fallback-decision", CallID: row.ID, ProjectID: row.ProjectID, PlanJSON: string(raw)}
	if _, err = a.db().db.Exec(`INSERT INTO routing_decisions(id,call_id,project_id,flow_version_id,node_id,status,request_json,plan_json,created_at,deadline_at) VALUES(?,?,?,?,?,'fallback','{}',?,?,?)`, d.ID, row.ID, row.ProjectID, plan.VersionID, plan.NodeID, string(raw), ringTime(time.Now()), ringTime(time.Now().Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		_ = a.deliverDecision(ctx, d)
	}
	if spawned, _, _ := p.counts(); spawned != 1 {
		t.Fatalf("spawn count=%d", spawned)
	}
	if h := aiJournal(t, a, row.ID); h.Status != "failed" || h.Attempts != 1 || h.Code != "configuration_or_access" {
		t.Fatalf("journal=%+v", h)
	}
	if err = a.runAIHandoffs(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if h := aiJournal(t, a, row.ID); h.FallbackApplied != 1 {
		t.Fatalf("fallback not committed: %+v", h)
	}
	for range 3 {
		_ = a.deliverDecision(ctx, d)
		if err = a.runRoutingEffects(context.Background(), ctx); err != nil {
			t.Fatal(err)
		}
		_ = a.runAIHandoffs(context.Background(), ctx)
	}
	current, _ := a.db().findCall(row.ID)
	if !isTerminalStatus(current.Status) || current.MediaConnectedAt != "" || callClassification(*current) != "ai_startup_failed" || callbackOpportunityID(*current) != "callback:"+row.ID {
		t.Fatalf("outcome=%+v", current)
	}
	var effects, applied int
	_ = a.db().db.QueryRow(`SELECT COUNT(*) FROM routing_effects WHERE call_id=?`, row.ID).Scan(&effects)
	_ = a.db().db.QueryRow(`SELECT applied FROM routing_decisions WHERE id=?`, d.ID).Scan(&applied)
	if effects != 1 || applied != 1 {
		t.Fatalf("effects=%d applied=%d", effects, applied)
	}
	if spawned, _, commands := p.counts(); spawned != 1 || commands != 1 {
		t.Fatalf("spawns=%d carrier commands=%d", spawned, commands)
	}
}

func TestAIHandoffTransientBudgetBackoffAndRestart(t *testing.T) {
	a, ctx, p, _, row, unblock := preparationFixture(t, false)
	p.failure = errors.New(`platform /api/apps/callback/threads/spawn-realtime: http 503: unavailable`)
	unblock()
	for attempt := 1; attempt <= 3; attempt++ {
		current, _ := a.db().findCall(row.ID)
		_, err := a.prepareInboundRealtime(ctx, current, "Help.", "", "")
		if !errors.Is(err, errAIHandoffPending) {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		h := aiJournal(t, a, row.ID)
		if h.Attempts != attempt {
			t.Fatal(h)
		}
		for range 5 {
			current, _ = a.db().findCall(row.ID)
			_, _ = a.prepareInboundRealtime(ctx, current, "Help.", "", "")
		}
		if spawned, _, _ := p.counts(); spawned != attempt {
			t.Fatalf("backoff bypassed: %d", spawned)
		}
		// A fresh coordinator sees the same persisted attempt/deadline budget.
		a = &App{installID: 42}
		if attempt < 3 {
			if h.Status != "retry" {
				t.Fatal(h)
			}
			aiRetryDue(t, a, row.ID)
		} else if h.Status != "failed" {
			t.Fatal(h)
		}
	}
	if err := a.runAIHandoffs(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.runRoutingEffects(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	current, _ := a.db().findCall(row.ID)
	if callClassification(*current) != "ai_startup_failed" {
		t.Fatalf("outcome=%+v", current)
	}
}

func TestAIHandoffTransientRecoveryAndConcurrentRequests(t *testing.T) {
	a, ctx, p, _, row, unblock := preparationFixture(t, false)
	p.failure = errors.New(`platform /api/apps/callback/threads/spawn-realtime: http 429: busy`)
	unblock()
	_, _ = a.prepareInboundRealtime(ctx, row, "Help.", "", "")
	p.mu.Lock()
	p.failure = nil
	p.mu.Unlock()
	aiRetryDue(t, a, row.ID)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			current, _ := a.db().findCall(row.ID)
			_, err := a.prepareInboundRealtime(ctx, current, "Help.", "", "")
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if spawned, _, _ := p.counts(); spawned != 2 {
		t.Fatalf("spawns=%d", spawned)
	}
	h := aiJournal(t, a, row.ID)
	if h.Status != "ready" || h.FallbackApplied != 0 {
		t.Fatal(h)
	}
	if err := a.runAIHandoffs(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if h = aiJournal(t, a, row.ID); h.Status != "ready" {
		t.Fatal(h)
	}
}

func TestAIHandoffDeadlineFencesLateSuccess(t *testing.T) {
	a, ctx, p, _, row, unblock := preparationFixture(t, true)
	done := make(chan error, 1)
	go func() { copy := *row; _, err := a.prepareInboundRealtime(ctx, &copy, "Help.", "", ""); done <- err }()
	waitPreparationEntered(t, p)
	if _, err := a.db().db.Exec(`UPDATE ai_handoffs SET deadline_at=? WHERE call_id=?`, ringTime(time.Now().Add(-time.Second)), row.ID); err != nil {
		t.Fatal(err)
	}
	// Simulates recovery by a process without the in-memory startup coordinator.
	recovered := &App{installID: 42}
	if err := recovered.runAIHandoffs(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	unblock()
	<-done
	if err := recovered.runRoutingEffects(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	current, _ := a.db().findCall(row.ID)
	if !isTerminalStatus(current.Status) || strings.HasPrefix(current.ThreadID, "tel-") || current.MediaConnectedAt != "" {
		t.Fatalf("late attach: %+v", current)
	}
	if h := aiJournal(t, a, row.ID); h.Code != "startup_deadline" || h.FallbackApplied != 1 {
		t.Fatal(h)
	}
	if spawned, _, _ := p.counts(); spawned != 1 {
		t.Fatalf("spawns=%d", spawned)
	}
}

func TestAIHandoffCallerCancellationStopsRecovery(t *testing.T) {
	a, ctx, p, _, row, unblock := preparationFixture(t, true)
	done := make(chan error, 1)
	go func() { copy := *row; _, err := a.prepareInboundRealtime(ctx, &copy, "Help.", "", ""); done <- err }()
	waitPreparationEntered(t, p)
	if _, err := a.db().updateStatusWithFacts(row.ID, "completed", "", lifecycleFacts{Source: "carrier", TerminationInitiator: "caller"}); err != nil {
		t.Fatal(err)
	}
	if err := a.runAIHandoffs(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	unblock()
	<-done
	current, _ := a.db().findCall(row.ID)
	if current.Status != "completed" || callClassification(*current) != "caller_abandoned" {
		t.Fatalf("caller overwritten: %+v", current)
	}
	var count int
	_ = a.db().db.QueryRow(`SELECT COUNT(*) FROM routing_effects WHERE call_id=?`, row.ID).Scan(&count)
	if count != 0 {
		t.Fatalf("fallback after cancellation: %d", count)
	}
}

func TestAIHandoffPinnedFailureAnnouncement(t *testing.T) {
	a, ctx, p, _, row, unblock := preparationFixture(t, true)
	var raw, node string
	if err := a.db().db.QueryRow(`SELECT context_json,current_node_id FROM call_route_executions WHERE call_id=?`, row.ID).Scan(&raw, &node); err != nil {
		t.Fatal(err)
	}
	var ex routingExecutionContext
	if err := json.Unmarshal([]byte(raw), &ex); err != nil {
		t.Fatal(err)
	}
	for i := range ex.Definition.Nodes {
		if ex.Definition.Nodes[i].ID == node {
			ex.Definition.Nodes[i].Branches = map[string]string{"ai_startup_failed": "failure-notice"}
		}
	}
	ex.Definition.Nodes = append(ex.Definition.Nodes, routingNode{ID: "failure-notice", Type: "announcement", Config: map[string]any{"text": "Please leave a callback request."}, Next: "failure-end"}, routingNode{ID: "failure-end", Type: "hangup"})
	encoded, _ := json.Marshal(ex)
	if _, err := a.db().db.Exec(`UPDATE call_route_executions SET context_json=? WHERE call_id=?`, string(encoded), row.ID); err != nil {
		t.Fatal(err)
	}
	p.failure = errors.New("unclassified startup failure")
	unblock()
	_, _ = a.prepareInboundRealtime(ctx, row, "Help.", "", "")
	if err := a.runAIHandoffs(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	current, _ := a.db().findCall(row.ID)
	_, plan, err := a.routingPlanForCall(current, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.NodeID != "failure-end" || terminalAnnouncementText(plan) != "Please leave a callback request." {
		t.Fatalf("fallback=%+v", plan)
	}
}

func TestAIHandoffDoesNotInterruptAnotherOffer(t *testing.T) {
	a, ctx, p, _, row, unblock := preparationFixture(t, true)
	p.failure = errors.New("invalid configuration")
	unblock()
	_, _ = a.prepareInboundRealtime(ctx, row, "Private directive.", "", "")
	// A legitimate browser offer may coexist with the failed AI in a ring group.
	if _, err := a.db().db.Exec(`INSERT INTO call_offers(id,call_id,project_id,destination_id,kind,status,offered_at,expires_at) VALUES('browser-offer',?,?, 'adviser','browser','offered',?,?)`, row.ID, row.ProjectID, ringTime(time.Now()), ringTime(time.Now().Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err := a.runAIHandoffs(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := a.db().db.QueryRow(`SELECT status FROM call_offers WHERE id='browser-offer'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	current, _ := a.db().findCall(row.ID)
	if status != "offered" || current.Status != "pending" {
		t.Fatalf("offer=%s call=%s", status, current.Status)
	}
	if spawned, _, commands := p.counts(); spawned != 1 || commands != 0 {
		t.Fatalf("spawns=%d commands=%d", spawned, commands)
	}
	diagnostics, err := a.aiHandoffPublic(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(diagnostics)
	if strings.Contains(string(raw), "Private directive") || strings.Contains(string(raw), "pending-") || !strings.Contains(string(raw), `"attempt_count":1`) {
		t.Fatalf("diagnostics=%s", raw)
	}
}

func TestAIHandoffFailureBranchCannotRestartAI(t *testing.T) {
	a, ctx, p, _, row, unblock := preparationFixture(t, true)
	var raw, node string
	if err := a.db().db.QueryRow(`SELECT context_json,current_node_id FROM call_route_executions WHERE call_id=?`, row.ID).Scan(&raw, &node); err != nil {
		t.Fatal(err)
	}
	var ex routingExecutionContext
	if err := json.Unmarshal([]byte(raw), &ex); err != nil {
		t.Fatal(err)
	}
	for i := range ex.Definition.Nodes {
		if ex.Definition.Nodes[i].ID == node {
			ex.Definition.Nodes[i].Branches = map[string]string{"ai_startup_failed": node}
		}
	}
	encoded, _ := json.Marshal(ex)
	_, err := a.db().db.Exec(`UPDATE call_route_executions SET context_json=? WHERE call_id=?`, string(encoded), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	p.failure = errors.New("invalid configuration")
	unblock()
	_, _ = a.prepareInboundRealtime(ctx, row, "Help.", "", "")
	if err = a.runAIHandoffs(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if err = a.runRoutingEffects(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	current, _ := a.db().findCall(row.ID)
	if !isTerminalStatus(current.Status) {
		t.Fatalf("AI fallback loop: %+v", current)
	}
	if spawned, _, _ := p.counts(); spawned != 1 {
		t.Fatal(spawned)
	}
}

func TestAIHandoffDeadlineCappedByCall(t *testing.T) {
	a, ctx, p, _, row, unblock := preparationFixture(t, false)
	deadline := ringTime(time.Now().Add(5 * time.Second))
	if _, err := a.db().db.Exec(`UPDATE calls SET deadline_at=? WHERE id=?`, deadline, row.ID); err != nil {
		t.Fatal(err)
	}
	p.failure = errors.New("invalid configuration")
	unblock()
	_, _ = a.prepareInboundRealtime(ctx, row, "Help.", "", "")
	if h := aiJournal(t, a, row.ID); h.Deadline != deadline {
		t.Fatalf("deadline extended: %+v", h)
	}
}

func TestAIHandoffSlowRetryDoesNotBlockRecoveryWorker(t *testing.T) {
	a, ctx, p, _, row, unblock := preparationFixture(t, false)
	p.failure = errors.New(`platform /api/apps/callback/threads/spawn-realtime: http 503: unavailable`)
	unblock()
	_, _ = a.prepareInboundRealtime(ctx, row, "Help.", "", "")
	<-p.entered
	gate := make(chan struct{})
	p.mu.Lock()
	p.failure = nil
	p.gate = gate
	p.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer func() { release(); a.stopRoutingDispatcher() }()
	if _, err := a.db().db.Exec(`UPDATE calls SET carrier_slug='bandwidth' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	aiRetryDue(t, a, row.ID)
	returned := make(chan error, 1)
	go func() { returned <- a.runAIHandoffs(context.Background(), ctx) }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		release()
		t.Fatal("slow retry blocked recovery worker")
	}
	waitPreparationEntered(t, p)
	if _, err := a.db().db.Exec(`UPDATE ai_handoffs SET deadline_at=? WHERE call_id=?`, ringTime(time.Now().Add(-time.Second)), row.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.runAIHandoffs(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	release()
	a.stopRoutingDispatcher()
	if h := aiJournal(t, a, row.ID); h.FallbackApplied != 1 || h.Attempts != 2 {
		t.Fatal(h)
	}
}

func TestAIHandoffCannotResetBudgetThroughLaterNode(t *testing.T) {
	a, ctx, p, _, row, unblock := preparationFixture(t, true)
	p.failure = errors.New("invalid configuration")
	unblock()
	_, _ = a.prepareInboundRealtime(ctx, row, "Help.", "", "")
	if err := a.runAIHandoffs(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	// A later decision/menu reaching AI cannot use a new graph node as a new
	// startup budget. Reconstruct that persisted transition without any spawn.
	if _, err := a.db().db.Exec(`UPDATE call_route_executions SET current_node_id='another-ai' WHERE call_id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db().db.Exec(`UPDATE calls SET peer_kind='realtime',agent_id=7 WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	_, _ = a.prepareInboundRealtime(ctx, row, "Help again.", "", "")
	h := aiJournal(t, a, row.ID)
	if h.Code != "ai_budget_already_failed" || h.FallbackApplied != 0 || h.Attempts != 1 {
		t.Fatal(h)
	}
	if err := a.runAIHandoffs(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.runRoutingEffects(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	current, _ := a.db().findCall(row.ID)
	if !isTerminalStatus(current.Status) {
		t.Fatal("later AI node did not terminate")
	}
	if spawned, _, _ := p.counts(); spawned != 1 {
		t.Fatalf("spawns=%d", spawned)
	}
}
