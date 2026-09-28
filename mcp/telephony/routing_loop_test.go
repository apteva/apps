package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func loopFixture(t *testing.T, call string, config map[string]any) (*App, *callsDB) {
	t.Helper()
	a, db, plan := decisionFixture(t)
	var ex routingExecutionContext
	if err := json.Unmarshal([]byte(plan.ContextJSON), &ex); err != nil {
		t.Fatal(err)
	}
	for key, value := range config {
		ex.Definition.Nodes[0].Config[key] = value
	}
	raw, _ := json.Marshal(ex)
	plan.ContextJSON = string(raw)
	insertRingCall(t, db, plan, call)
	return a, db
}

func TestDecisionFallbackKeepsTerminalAnnouncementAudible(t *testing.T) {
	a, _, original := decisionFixture(t)
	var ex routingExecutionContext
	if err := json.Unmarshal([]byte(original.ContextJSON), &ex); err != nil {
		t.Fatal(err)
	}
	ex.Definition.Nodes[0].Branches["fallback"] = "notice"
	ex.Definition.Nodes = append(ex.Definition.Nodes, routingNode{ID: "notice", Type: "announcement", Config: map[string]any{"text": "Please hold for a callback."}, Next: "end"})
	plan, err := a.resolveRoutingDefinition(&ex.Route, "+12025550100", nil, &routingFlowVersionRow{ID: "v1", FlowID: "flow"}, ex.Definition, "notice")
	if err != nil {
		t.Fatal(err)
	}
	if plan.TerminalType != "hangup" || terminalAnnouncementText(plan) == "" {
		t.Fatalf("announcement lost: %+v", plan)
	}
	row := &callRow{ID: "fallback-announcement", CarrierSlug: "twilio", Status: "pending"}
	if err := a.finishTerminalRoutingPlan(nil, row, &ex.Route, plan); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	if err := a.writeTwilioRoutingPlan(recorder, row, &ex.Route, plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recorder.Body.String(), "Please hold for a callback.") || !strings.Contains(recorder.Body.String(), "<Hangup/>") {
		t.Fatalf("terminal announcement was skipped: %s", recorder.Body.String())
	}
}

func loopDecision(t *testing.T, a *App, call, node string) decisionRecord {
	t.Helper()
	decisions, err := a.listDecisions("p1", call)
	if err != nil {
		t.Fatal(err)
	}
	for _, decision := range decisions {
		if decision.NodeID == node {
			return decision
		}
	}
	t.Fatalf("decision %s missing: %+v", node, decisions)
	return decisionRecord{}
}

func exhaustLoopRun(t *testing.T, a *App, db *callsDB, call, node string) {
	t.Helper()
	run := "ring_" + call + "_" + node
	tx, err := db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = advanceRingRunTx(tx, run, time.Now().Add(time.Minute)); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = a.finishRingRun(globalCtx.WithProject("p1"), run, call, node); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionLoopContinuesAfterOfferAndHonorsAttemptCeiling(t *testing.T) {
	a, db := loopFixture(t, "loop-offers", map[string]any{"max_attempts": 3, "total_wait_seconds": 180})
	first := loopDecision(t, a, "loop-offers", "select")
	if err := a.completeDecision(first, decisionResponse{DecisionID: first.ID, Action: "offer", DestinationID: "alice"}, ""); err != nil {
		t.Fatal(err)
	}
	exhaustLoopRun(t, a, db, "loop-offers", "select")
	var reserved int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM phone_capacity WHERE call_id='loop-offers'`).Scan(&reserved); err != nil || reserved != 0 {
		t.Fatalf("expired offer retained capacity: %d %v", reserved, err)
	}
	second := loopDecision(t, a, "loop-offers", "select#2")
	if !strings.Contains(string(second.Request), `"node_attempt":2`) || !strings.Contains(string(second.Request), `"outcome":"expired"`) {
		t.Fatalf("history missing: %s", second.Request)
	}
	// Alias is the same verified adviser. The loop must not offer twice.
	if err := a.completeDecision(second, decisionResponse{DecisionID: second.ID, Action: "offer", DestinationID: "alias"}, ""); err != nil {
		t.Fatal(err)
	}
	if updated := loopDecision(t, a, "loop-offers", "select#2"); updated.Reason != "adviser_already_offered" {
		t.Fatalf("duplicate accepted: %+v", updated)
	}
	third := loopDecision(t, a, "loop-offers", "select#3")
	if err := a.completeDecision(third, decisionResponse{DecisionID: third.ID, Action: "exhausted"}, ""); err != nil {
		t.Fatal(err)
	}
	if decisions, err := a.listDecisions("p1", "loop-offers"); err != nil || len(decisions) != 3 {
		t.Fatalf("unbounded loop: %v %v", decisions, err)
	}
	var resolution string
	if err := db.db.QueryRow(`SELECT routing_resolution FROM calls WHERE id='loop-offers'`).Scan(&resolution); err != nil || resolution != "routing_exhausted" {
		t.Fatalf("resolution %q %v", resolution, err)
	}
}

func TestDecisionLoopOfferMilestonesAreDurableAndDeduplicated(t *testing.T) {
	a, db := loopFixture(t, "loop-milestones", map[string]any{"max_attempts": 2})
	d := loopDecision(t, a, "loop-milestones", "select")
	if err := a.completeDecision(d, decisionResponse{DecisionID: d.ID, Action: "offer", DestinationID: "alice"}, ""); err != nil {
		t.Fatal(err)
	}
	var offerID string
	if err := db.db.QueryRow(`SELECT id FROM call_offers WHERE call_id='loop-milestones' AND status='offered'`).Scan(&offerID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := db.db.Exec(`UPDATE call_offers SET acknowledged_at=? WHERE id=?`, ringTime(time.Now()), offerID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.db.Exec(`UPDATE call_offers SET status='declined',declined_at=? WHERE id=?`, ringTime(time.Now()), offerID); err != nil {
		t.Fatal(err)
	}
	if err := a.flushDecisionMarks("p1"); err != nil {
		t.Fatal(err)
	}
	var count int
	for _, topic := range []string{"telephony.routing.offer.acknowledged", "telephony.routing.offer.declined"} {
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM call_events WHERE call_id='loop-milestones' AND topic=?`, topic).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s count=%d err=%v", topic, count, err)
		}
	}
}

func TestDecisionLoopReassignsRevokedDestination(t *testing.T) {
	a, db := loopFixture(t, "loop-revoked", map[string]any{"max_attempts": 3})
	d := loopDecision(t, a, "loop-revoked", "select")
	if err := a.completeDecision(d, decisionResponse{DecisionID: d.ID, Action: "offer", DestinationID: "alice"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE routing_destinations SET enabled=0 WHERE project_id='p1' AND id='alice'`); err != nil {
		t.Fatal(err)
	}
	if err := a.tickRingRun(globalCtx.WithProject("p1"), "ring_loop-revoked_select", "loop-revoked"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.db.QueryRow(`SELECT status FROM call_offers WHERE call_id='loop-revoked'`).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("revoked offer status %q %v", status, err)
	}
	_ = loopDecision(t, a, "loop-revoked", "select#2")
	var reserved int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM phone_capacity WHERE call_id='loop-revoked'`).Scan(&reserved); err != nil || reserved != 0 {
		t.Fatalf("revoked offer retained capacity: %d %v", reserved, err)
	}
}

func TestDecisionLoopWaitAndFunctionFailureRetry(t *testing.T) {
	a, _ := loopFixture(t, "loop-wait", map[string]any{"max_attempts": 4, "total_wait_seconds": 120, "function_retry_limit": 1, "retry_delay_seconds": 2})
	first := loopDecision(t, a, "loop-wait", "select")
	if err := a.completeDecision(first, decisionResponse{}, "function_failed"); err != nil {
		t.Fatal(err)
	}
	second := loopDecision(t, a, "loop-wait", "select#2")
	if second.NotBefore == "" || second.Reason != "" {
		t.Fatalf("retry not scheduled: %+v", second)
	}
	if err := a.completeDecision(second, decisionResponse{DecisionID: second.ID, Action: "wait_retry", RetryAfterSec: 3}, ""); err != nil {
		t.Fatal(err)
	}
	third := loopDecision(t, a, "loop-wait", "select#3")
	if third.NotBefore == "" || !strings.Contains(string(third.Request), `"node_attempt":3`) {
		t.Fatalf("wait not scheduled: %+v", third)
	}
}

func TestDecisionLoopCallExpiryCoversWaitingBudget(t *testing.T) {
	_, db := loopFixture(t, "loop-budget", map[string]any{"max_attempts": 8, "total_wait_seconds": 120})
	var raw string
	if err := db.db.QueryRow(`SELECT state_expires_at FROM calls WHERE id='loop-budget'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	expires, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || time.Until(expires) < 115*time.Second {
		t.Fatalf("call expires before decision budget: %q %v", raw, err)
	}
}

func TestOneShotDecisionRetainsShortCallExpiry(t *testing.T) {
	_, db, plan := decisionFixture(t)
	insertRingCall(t, db, plan, "one-shot-budget")
	var raw string
	if err := db.db.QueryRow(`SELECT state_expires_at FROM calls WHERE id='one-shot-budget'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	expires, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || time.Until(expires) > 15*time.Second {
		t.Fatalf("one-shot safety expiry changed: %q %v", raw, err)
	}
}

func TestDecisionLoopFunctionRetryLimitIsSeparateFromExhaustion(t *testing.T) {
	a, db := loopFixture(t, "loop-errors", map[string]any{"max_attempts": 4, "total_wait_seconds": 120, "function_retry_limit": 1})
	first := loopDecision(t, a, "loop-errors", "select")
	if err := a.completeDecision(first, decisionResponse{}, "function_failed"); err != nil {
		t.Fatal(err)
	}
	second := loopDecision(t, a, "loop-errors", "select#2")
	if err := a.completeDecision(second, decisionResponse{}, "function_failed"); err != nil {
		t.Fatal(err)
	}
	decisions, err := a.listDecisions("p1", "loop-errors")
	if err != nil || len(decisions) != 2 {
		t.Fatalf("function failure retried beyond limit: %v %v", decisions, err)
	}
	var resolution string
	if err := db.db.QueryRow(`SELECT routing_resolution FROM calls WHERE id='loop-errors'`).Scan(&resolution); err != nil || resolution != "routing_error" {
		t.Fatalf("function error treated as exhausted: %q %v", resolution, err)
	}
}
