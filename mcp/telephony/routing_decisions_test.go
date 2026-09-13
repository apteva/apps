package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func decisionFixture(t *testing.T) (*App, *callsDB, *inboundRoutingPlan) {
	t.Helper()
	db := testCallsDB(t)
	a := &App{}
	withRoutingTestDB(t, a, db)
	identity := phoneTestIdentity("alice")
	config := map[string]any{"capacity": destinationCapacity{Identity: identity, Limit: 1}}
	dests := map[string]routingDestinationRow{}
	for _, id := range []string{"alice", "alias"} {
		d, e := a.saveRoutingDestination("p1", id, id, "browser", config, true)
		if e != nil {
			t.Fatal(e)
		}
		dests[id] = *d
	}
	policy := phonePolicy{Users: []phoneUser{{Identity: identity, Enabled: true, phoneGrant: phoneGrant{Role: "user", Destinations: []string{"alice", "alias"}}}}}
	raw, _ := json.Marshal(policy)
	if _, e := db.db.Exec(`INSERT INTO telephony_access_policies(project_id,revision,policy_json) VALUES('p1',1,?)`, string(raw)); e != nil {
		t.Fatal(e)
	}
	def := routingDefinition{Entry: "select", Destinations: dests, Nodes: []routingNode{{ID: "select", Type: "decision", Config: map[string]any{"function_id": 42, "timeout_ms": 5000, "destination_ids": []string{"alice", "alias"}}, Branches: map[string]string{"fallback": "end"}}, {ID: "end", Type: "hangup"}}}
	route := &routeRow{ID: "route", ProjectID: "p1", CarrierSlug: "twilio", AnswerMode: answerModeHumanBrowser, TimeoutSec: 30, Enabled: true}
	p, e := a.resolveRoutingDefinition(route, "+12025550100", map[string]string{"menu": "1"}, &routingFlowVersionRow{ID: "v1", FlowID: "flow"}, def, "")
	if e != nil {
		t.Fatal(e)
	}
	return a, db, p
}
func getDecision(t *testing.T, a *App, call string) decisionRecord {
	t.Helper()
	ds, e := a.listDecisions("p1", call)
	if e != nil || len(ds) != 1 {
		t.Fatalf("decisions %v %v", ds, e)
	}
	return ds[0]
}
func offerDecision(t *testing.T, a *App, call, dest string) {
	t.Helper()
	d := getDecision(t, a, call)
	if e := a.completeDecision(d, decisionResponse{DecisionID: d.ID, Action: "offer", DestinationID: dest, ReservationID: "business-" + call}, ""); e != nil {
		t.Fatal(e)
	}
}
func TestDecisionValidationAndMockSimulation(t *testing.T) {
	a, _, p := decisionFixture(t)
	var ex routingExecutionContext
	_ = json.Unmarshal([]byte(p.ContextJSON), &ex)
	if errs := a.validateRoutingReferences("p1", ex.Definition); len(errs) > 0 {
		t.Fatal(errs)
	}
	if errs := a.validateRoutingReferences("other", ex.Definition); len(errs) == 0 {
		t.Fatal("cross-project targets accepted")
	}
	for _, mutate := range []func(*routingDefinition){func(d *routingDefinition) { d.Nodes[0].Branches = nil }, func(d *routingDefinition) { d.Nodes[0].Config["timeout_ms"] = 5001 }, func(d *routingDefinition) { d.Nodes[0].Config["function_id"] = 0 }, func(d *routingDefinition) { d.Nodes[0].Branches["fallback"] = "select" }} {
		var d routingDefinition
		raw, _ := json.Marshal(ex.Definition)
		_ = json.Unmarshal(raw, &d)
		mutate(&d)
		if len(validateRoutingDefinition(d)) == 0 {
			t.Fatal("invalid configuration accepted")
		}
	}
	s := simulateRoutingDefinition(ex.Definition, routingSimulationContext{Decisions: map[string]decisionResponse{"select": {Action: "offer", DestinationID: "alice"}}})
	if !s.Valid || s.DestinationID != "alice" {
		t.Fatalf("mock %v", s)
	}
	s = simulateRoutingDefinition(ex.Definition, routingSimulationContext{})
	if !s.Valid || s.TerminalType != "hangup" {
		t.Fatalf("fallback %v", s)
	}
}
func TestDecisionDuplicateAcceptanceAndReconciliation(t *testing.T) {
	a, db, p := decisionFixture(t)
	insertRingCall(t, db, p, "one")
	if e := a.persistRoutingExecution("one", "p1", p); e != nil {
		t.Fatal(e)
	}
	d := getDecision(t, a, "one")
	if !strings.Contains(string(d.Request), `"menu":"1"`) {
		t.Fatalf("digits lost: %s", d.Request)
	}
	offerDecision(t, a, "one", "alice")
	offerDecision(t, a, "one", "alias")
	offers, e := db.activeRingOffers("one", "p1")
	if e != nil || len(offers) != 1 || offers[0].DestinationID != "alice" {
		t.Fatalf("offers %v %v", offers, e)
	}
	row, _ := db.findCall("one")
	var ex routingExecutionContext
	_ = json.Unmarshal([]byte(p.ContextJSON), &ex)
	if e := db.insertRoute(ex.Route); e != nil {
		t.Fatal(e)
	}
	row.RouteID = ex.Route.ID
	_, restored, e := a.routingPlanForCall(row, nil)
	if e != nil || restored.Group == nil || restored.Group.Members[0].DestinationID != "alice" {
		t.Fatalf("restore %v %v", restored, e)
	}
	for range 2 {
		if e = a.flushDecisionMarks("p1"); e != nil {
			t.Fatal(e)
		}
	}
	events, e := db.listLifecycleEvents("one", 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	found := false
	for _, event := range events {
		if seen[event.EventID] {
			t.Fatal("duplicate event")
		}
		seen[event.EventID] = true
		if event.Topic == "telephony.routing.offer.offered" {
			found = true
			if event.Payload["reservation_id"] != "business-one" {
				t.Fatal(event.Payload)
			}
		}
	}
	if !found {
		t.Fatal("offer event missing")
	}
	other, e := a.listDecisions("other", "one")
	if e != nil || len(other) > 0 {
		t.Fatal("decision leaked across project")
	}
}
func TestDecisionCapacityConcurrentAliasesOutboundAndRelease(t *testing.T) {
	a, db, p := decisionFixture(t)
	for _, id := range []string{"a", "b"} {
		insertRingCall(t, db, p, id)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, id := range []string{"a", "b"} {
		d := getDecision(t, a, id)
		wg.Add(1)
		go func(d decisionRecord, dest string) {
			defer wg.Done()
			errs <- a.completeDecision(d, decisionResponse{DecisionID: d.ID, Action: "offer", DestinationID: dest}, "")
		}(d, []string{"alice", "alias"}[i])
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var winner string
	accepted := 0
	for _, id := range []string{"a", "b"} {
		d := getDecision(t, a, id)
		if d.Status == "accepted" {
			accepted++
			winner = id
		} else if d.Reason != "capacity_unavailable" {
			t.Fatalf("unexpected %+v", d)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d", accepted)
	}
	outbound := testCall("outbound", "dialing")
	outbound.ProjectID = "p1"
	outbound.Direction = "outbound"
	if e := db.insertCall(outbound); e != nil {
		t.Fatal(e)
	}
	principal, _ := a.phonePrincipal("p1", phoneTestIdentity("alice"))
	if e := a.setPhoneOwner(&outbound, principal, ""); e == nil {
		t.Fatal("outbound bypassed reservation")
	}
	if e := db.updateStatus(winner, "completed", ""); e != nil {
		t.Fatal(e)
	}
	if e := a.setPhoneOwner(&outbound, principal, ""); e != nil {
		t.Fatal(e)
	}
	insertRingCall(t, db, p, "blocked-outbound")
	offerDecision(t, a, "blocked-outbound", "alias")
	if d := getDecision(t, a, "blocked-outbound"); d.Reason != "capacity_unavailable" {
		t.Fatal(d)
	}
}
func TestDecisionFailureTimeoutHangupAndLateResult(t *testing.T) {
	for _, mode := range []string{"invalid", "timeout", "hangup", "revoked", "reassigned", "failed-function"} {
		t.Run(mode, func(t *testing.T) {
			a, db, p := decisionFixture(t)
			insertRingCall(t, db, p, "test")
			d := getDecision(t, a, "test")
			r := decisionResponse{DecisionID: d.ID, Action: "offer", DestinationID: "alice"}
			reason := ""
			switch mode {
			case "invalid":
				r.DestinationID = "not-allowed"
			case "timeout":
				d.DeadlineAt = ringTime(time.Now().Add(-time.Second))
				_, _ = db.db.Exec(`UPDATE routing_decisions SET deadline_at=? WHERE id=?`, d.DeadlineAt, d.ID)
			case "hangup":
				_ = db.updateStatus("test", "completed", "")
			case "revoked":
				_, _ = db.db.Exec(`UPDATE telephony_access_policies SET policy_json='{}'`)
			case "reassigned":
				_, _ = a.saveRoutingDestination("p1", "alice", "Alice", "browser", map[string]any{"capacity": destinationCapacity{Identity: phoneTestIdentity("bob"), Limit: 1}}, true)
			case "failed-function":
				reason = "function_failed"
			}
			if e := a.completeDecision(d, r, reason); e != nil {
				t.Fatal(e)
			}
			completed := getDecision(t, a, "test")
			if completed.Status == "accepted" {
				t.Fatal(completed)
			}
			if e := a.completeDecision(d, r, ""); e != nil {
				t.Fatal(e)
			}
			offers, e := db.activeRingOffers("test", "p1")
			if e != nil || len(offers) > 0 {
				t.Fatalf("late offer %v %v", offers, e)
			}
		})
	}
}
func TestDecisionFailedSetupExpiryAndConnectedOccupancy(t *testing.T) {
	for _, mode := range []string{"expired", "setup-failed", "connected"} {
		t.Run(mode, func(t *testing.T) {
			a, db, p := decisionFixture(t)
			insertRingCall(t, db, p, "test")
			offerDecision(t, a, "test", "alice")
			if mode == "expired" {
				tx, _ := db.db.Begin()
				if e := advanceRingRunTx(tx, "ring_test_select", time.Now().Add(time.Minute)); e != nil {
					t.Fatal(e)
				}
				_ = tx.Commit()
				_, _ = db.db.Exec(`UPDATE phone_capacity SET expires_at=?`, ringTime(time.Now().Add(-time.Second)))
			} else {
				ok, e := db.claimPendingCallForHuman("test", "p1", "alice")
				if e != nil || !ok {
					t.Fatalf("claim %v %v", ok, e)
				}
				row, _ := db.findCall("test")
				principal, _ := a.phonePrincipal("p1", phoneTestIdentity("alice"))
				if e = a.setPhoneOwner(row, principal, "alice"); e != nil {
					t.Fatal(e)
				}
				if mode == "setup-failed" {
					if e = db.releaseAnswerClaim("test"); e != nil {
						t.Fatal(e)
					}
				} else {
					_, _ = db.db.Exec(`UPDATE calls SET status='answered',media_connected_at=?,media_status='disconnected' WHERE id='test'`, ringTime(time.Now()))
				}
			}
			if e := a.flushDecisionMarks("p1"); e != nil {
				t.Fatal(e)
			}
			insertRingCall(t, db, p, "next")
			offerDecision(t, a, "next", "alias")
			d := getDecision(t, a, "next")
			if mode == "connected" {
				if d.Status == "accepted" {
					t.Fatal("audio disconnect released occupancy")
				}
			} else if d.Status != "accepted" {
				t.Fatalf("capacity not released: %+v", d)
			}
			events, e := db.listLifecycleEvents("test", 0, 100)
			if e != nil {
				t.Fatal(e)
			}
			topics := map[string]bool{}
			for _, ev := range events {
				topics[ev.Topic] = true
				if ev.Topic == "telephony.routing.destination.connected" && ev.Payload["answering_identity"] == nil {
					t.Fatal("verified answerer missing")
				}
			}
			want := "telephony.routing.offer.expired"
			if mode == "setup-failed" {
				want = "telephony.routing.offer.failed"
			}
			if mode == "connected" {
				want = "telephony.routing.destination.connected"
			}
			if !topics[want] {
				t.Fatalf("missing %s: %v", want, topics)
			}
		})
	}
}

type decisionPlatform struct {
	answerPlatform
	invoked atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (p *decisionPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	if app != "functions" || tool != "functions_invoke" {
		return fmt.Errorf("unexpected %s/%s", app, tool)
	}
	p.invoked.Add(1)
	var request map[string]any
	raw, _ := json.Marshal(args["event"])
	_ = json.Unmarshal(raw, &request)
	if request["project_id"] != "p1" || request["deadline_at"] == nil {
		return fmt.Errorf("missing request scope/deadline")
	}
	if p.started != nil {
		p.started <- struct{}{}
	}
	if p.release != nil {
		<-p.release
	}
	response, _ := json.Marshal(decisionResponse{DecisionID: request["decision_id"].(string), Action: "offer", DestinationID: "alice"})
	raw, _ = json.Marshal(map[string]any{"status": "ok", "response": string(response)})
	return json.Unmarshal(raw, out)
}
func TestDecisionWorkerDuplicatesTimeoutAndRestart(t *testing.T) {
	a, db, plan := decisionFixture(t)
	platform := &decisionPlatform{started: make(chan struct{}, 1), release: make(chan struct{})}
	ctx := sdk.NewAppCtxForTest(&sdk.Manifest{}, db.db, sdk.Config{}, platform, nil).WithProject("p1")
	globalCtx = ctx
	insertRingCall(t, db, plan, "worker")
	if e := a.runDecisionTick(context.Background(), ctx); e != nil {
		t.Fatal(e)
	}
	select {
	case <-platform.started:
	case <-time.After(time.Second):
		t.Fatal("invocation not started")
	}
	if e := a.runDecisionTick(context.Background(), ctx); e != nil {
		t.Fatal(e)
	}
	if platform.invoked.Load() != 1 {
		t.Fatal("duplicate invocation")
	}
	_, _ = db.db.Exec(`UPDATE routing_decisions SET deadline_at=? WHERE call_id='worker'`, ringTime(time.Now().Add(-time.Second)))
	fresh := &App{}
	if e := fresh.runDecisionTick(context.Background(), ctx); e != nil {
		t.Fatal(e)
	}
	if d := getDecision(t, a, "worker"); d.Status != "timed_out" {
		t.Fatal(d)
	}
	close(platform.release)
	deadline := time.Now().Add(time.Second)
	for len(decisionSlots) > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(decisionSlots) > 0 {
		t.Fatal("invocation did not settle")
	}
	offers, _ := db.activeRingOffers("worker", "p1")
	if len(offers) > 0 || platform.invoked.Load() != 1 {
		t.Fatal("late/restarted invocation rang")
	}
}
func TestDecisionResponseContract(t *testing.T) {
	for _, raw := range []string{`{}`, `{"decision_id":"x","action":"offer","extra":true}`, `{"decision_id":"other","action":"fallback"}`, `{"decision_id":"x","action":"fallback","destination_id":"alice"}`, `{"decision_id":"x","action":"offer"} {}`, strings.Repeat("x", 16385)} {
		if _, reason := decodeDecisionResponse("x", raw); reason == "" {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, reason := decodeDecisionResponse("x", `{"decision_id":"x","action":"offer","destination_id":"alice","reservation_id":"r"}`); reason != "" {
		t.Fatal(reason)
	}
}

func TestDecisionWorkerAcceptsAndRecoversDelivery(t *testing.T) {
	a, db, p := decisionFixture(t)
	platform := &decisionPlatform{}
	ctx := sdk.NewAppCtxForTest(&sdk.Manifest{}, db.db, sdk.Config{}, platform, nil).WithProject("p1")
	globalCtx = ctx
	insertRingCall(t, db, p, "success")
	if e := a.runDecisionTick(context.Background(), ctx); e != nil {
		t.Fatal(e)
	}
	until := time.Now().Add(time.Second)
	for len(decisionSlots) > 0 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if len(decisionSlots) > 0 {
		t.Fatal("worker did not settle")
	}
	if d := getDecision(t, a, "success"); d.Status != "accepted" {
		t.Fatal(d)
	}
	fresh := &App{}
	for range 2 {
		if e := fresh.runDecisionTick(context.Background(), ctx); e != nil {
			t.Fatal(e)
		}
	}
	if d := getDecision(t, a, "success"); !d.Applied {
		t.Fatal("delivery not recovered")
	}
	if platform.invoked.Load() != 1 {
		t.Fatal("invoked twice")
	}
	offers, e := db.activeRingOffers("success", "p1")
	if e != nil || len(offers) != 1 {
		t.Fatalf("offers %v %v", offers, e)
	}
}
func TestDecisionAtomicRollbackAndRepeatedOutcomeTopics(t *testing.T) {
	a, db, p := decisionFixture(t)
	insertRingCall(t, db, p, "atomic")
	_, e := db.db.Exec(`CREATE TRIGGER fail_offer BEFORE INSERT ON call_offers BEGIN SELECT RAISE(ABORT,'injected storage failure'); END;`)
	if e != nil {
		t.Fatal(e)
	}
	d := getDecision(t, a, "atomic")
	r := decisionResponse{DecisionID: d.ID, Action: "offer", DestinationID: "alice"}
	if e = a.completeDecision(d, r, ""); e == nil {
		t.Fatal("expected storage failure")
	}
	d = getDecision(t, a, "atomic")
	if d.Status != "pending" {
		t.Fatal("partial result committed")
	}
	var count int
	_ = db.db.QueryRow(`SELECT COUNT(*) FROM phone_capacity`).Scan(&count)
	if count != 0 {
		t.Fatal("partial capacity committed")
	}
	_, _ = db.db.Exec(`DROP TRIGGER fail_offer`)
	offerDecision(t, a, "atomic", "alice")
	// Multiple offers/decisions can publish the same topic with independent IDs.
	tx, _ := db.db.Begin()
	for _, key := range []string{"one", "two", "one"} {
		if e = decisionEventTx(tx, d.ID, "offer.expired", key, "alice", ringTime(time.Now()), nil); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	_ = db.db.QueryRow(`SELECT COUNT(*) FROM call_events WHERE call_id='atomic' AND topic='telephony.routing.offer.expired'`).Scan(&count)
	if count != 2 {
		t.Fatalf("repeated events %d", count)
	}
}
func TestDecisionReselectionPreservesDigitsAndPriorOutcomes(t *testing.T) {
	a, db, p := decisionFixture(t)
	var ex routingExecutionContext
	_ = json.Unmarshal([]byte(p.ContextJSON), &ex)
	ex.Definition.Nodes[0].Branches["fallback"] = "second"
	second := ex.Definition.Nodes[0]
	second.ID = "second"
	second.Branches = map[string]string{"fallback": "end"}
	ex.Definition.Nodes = append(ex.Definition.Nodes, second)
	raw, _ := json.Marshal(ex)
	p.ContextJSON = string(raw)
	insertRingCall(t, db, p, "retry")
	offerDecision(t, a, "retry", "alice")
	tx, _ := db.db.Begin()
	if e := advanceRingRunTx(tx, "ring_retry_select", time.Now().Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	_ = tx.Commit()
	if e := a.finishRingRun(globalCtx.WithProject("p1"), "ring_retry_select", "retry", "second"); e != nil {
		t.Fatal(e)
	}
	ds, e := a.listDecisions("p1", "retry")
	if e != nil || len(ds) != 2 {
		t.Fatalf("reselection %v %v", ds, e)
	}
	var next decisionRecord
	for _, d := range ds {
		if d.NodeID == "second" {
			next = d
		}
	}
	if !strings.Contains(string(next.Request), `"outcome":"expired"`) || !strings.Contains(string(next.Request), `"menu":"1"`) || !strings.Contains(string(next.Request), `"attempt":2`) {
		t.Fatalf("context lost: %s", next.Request)
	}
	if e = a.completeDecision(next, decisionResponse{DecisionID: next.ID, Action: "offer", DestinationID: "alias"}, ""); e != nil {
		t.Fatal(e)
	}
	offers, e := db.activeRingOffers("retry", "p1")
	if e != nil || len(offers) != 1 || offers[0].DestinationID != "alias" {
		t.Fatalf("next offer %v %v", offers, e)
	}
	for i := 0; i < 4; i++ {
		n := second
		n.ID = fmt.Sprint("extra", i)
		ex.Definition.Nodes = append(ex.Definition.Nodes, n)
	}
	if len(validateRoutingDefinition(ex.Definition)) == 0 {
		t.Fatal("unbounded decisions allowed")
	}
}

func TestDecisionProgressedCallCancelsPendingWork(t *testing.T) {
	a, db, p := decisionFixture(t)
	insertRingCall(t, db, p, "progressed")
	d := getDecision(t, a, "progressed")
	_, _ = db.db.Exec(`UPDATE call_route_executions SET current_node_id='end' WHERE call_id='progressed'`)
	if e := a.completeDecision(d, decisionResponse{DecisionID: d.ID, Action: "offer", DestinationID: "alice"}, ""); e != nil {
		t.Fatal(e)
	}
	if d = getDecision(t, a, "progressed"); d.Status != "canceled" || !d.Applied {
		t.Fatal(d)
	}
}
func TestPersonalGroupReservesEachUserAndReleasesLosingOffers(t *testing.T) {
	a, db, p := decisionFixture(t)
	bob := phoneTestIdentity("bob")
	dest, e := a.saveRoutingDestination("p1", "bob", "Bob", "browser", map[string]any{"capacity": destinationCapacity{Identity: bob, Limit: 1}}, true)
	if e != nil {
		t.Fatal(e)
	}
	var ex routingExecutionContext
	_ = json.Unmarshal([]byte(p.ContextJSON), &ex)
	plan := &inboundRoutingPlan{FlowID: "flow", VersionID: "v1", NodeID: "team", TerminalType: "ring_group", AnswerMode: answerModeHumanBrowser, Group: &ringGroupRow{ID: "team", Strategy: "simultaneous", TimeoutSec: 20, Members: []ringGroupMemberRow{{DestinationID: "alice", Enabled: true}, {DestinationID: "bob", Enabled: true}}}, GroupDestinations: map[string]routingDestinationRow{"alice": ex.Definition.Destinations["alice"], "bob": *dest}}
	plan.Trace = []routingTraceStep{{NodeID: "team", NodeType: "ring_group"}}
	insertRingCall(t, db, plan, "team-call")
	var count int
	if e = db.db.QueryRow(`SELECT COUNT(*) FROM phone_capacity WHERE call_id='team-call'`).Scan(&count); e != nil || count != 2 {
		t.Fatalf("reservations %d %v", count, e)
	}
	insertRingCall(t, db, p, "decision-call")
	offerDecision(t, a, "decision-call", "alias")
	if d := getDecision(t, a, "decision-call"); d.Reason != "capacity_unavailable" {
		t.Fatal(d)
	}
	ok, e := db.claimPendingCallForHuman("team-call", "p1", "bob")
	if e != nil || !ok {
		t.Fatalf("claim %v %v", ok, e)
	}
	if e = db.db.QueryRow(`SELECT COUNT(*) FROM phone_capacity WHERE call_id='team-call'`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("winner capacity %d %v", count, e)
	}
	insertRingCall(t, db, p, "next")
	offerDecision(t, a, "next", "alice")
	if d := getDecision(t, a, "next"); d.Status != "accepted" {
		t.Fatal(d)
	}
}

func TestDecisionMigrationPreservesExistingCallsAndEvents(t *testing.T) {
	db, e := sql.Open("sqlite", ":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	files, e := os.ReadDir("migrations")
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range files {
		if f.Name() >= "025" {
			continue
		}
		raw, e := os.ReadFile(filepath.Join("migrations", f.Name()))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(string(raw)); e != nil {
			t.Fatal(e)
		}
	}
	calls := &callsDB{db: db}
	call := testCall("existing", "answered")
	if e = calls.insertCall(call); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO call_events(event_id,call_id,project_id,topic,revision,occurred_at,payload_json,created_at,published_at) VALUES('old','existing','p1','call.answered',1,'then','{}','then','sent')`); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile("migrations/025_routing_decisions.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(string(raw)); e != nil {
		t.Fatal(e)
	}
	var published, status string
	if e = db.QueryRow(`SELECT published_at FROM call_events WHERE event_id='old'`).Scan(&published); e != nil || published != "sent" {
		t.Fatalf("history %s %v", published, e)
	}
	if e = db.QueryRow(`SELECT status FROM calls WHERE id='existing'`).Scan(&status); e != nil || status != "answered" {
		t.Fatalf("call %s %v", status, e)
	}
}
