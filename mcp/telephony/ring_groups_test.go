package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func ringFixture(t *testing.T, strategy string) (*App, *callsDB, *inboundRoutingPlan) {
	t.Helper()
	db := testCallsDB(t)
	app := &App{}
	withRoutingTestDB(t, app, db)
	group := &ringGroupRow{ID: "team", ProjectID: "p1", Strategy: strategy, TimeoutSec: 20, Enabled: true}
	dests := map[string]routingDestinationRow{}
	for i := 0; i < 3; i++ {
		id := fmt.Sprint("d", i)
		group.Members = append(group.Members, ringGroupMemberRow{DestinationID: id, Position: i, Priority: i / 2, TimeoutSec: 20, Enabled: true})
		dests[id] = routingDestinationRow{ID: id, ProjectID: "p1", Name: id, Kind: "agent", Enabled: true, ConfigJSON: fmt.Sprintf(`{"agent_id":%d}`, i+1)}
	}
	plan := &inboundRoutingPlan{FlowID: "flow", VersionID: "version", RingGroupID: "team", NodeID: "team", TerminalType: "ring_group", Group: group, GroupDestinations: dests, Trace: []routingTraceStep{{NodeID: "team", NodeType: "ring_group"}}}
	return app, db, plan
}
func insertRingCall(t *testing.T, db *callsDB, plan *inboundRoutingPlan, id string) {
	t.Helper()
	call := testCall(id, "pending")
	call.Direction = "inbound"
	call.ProjectID = "p1"
	call.CarrierSID = id
	call.AgentID = 0
	call.ThreadID = "pending-" + id
	call.DeadlineAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if _, _, err := db.insertInboundCallWithEvent(call, "", plan); err != nil {
		t.Fatal(err)
	}
}
func ringAdvance(t *testing.T, db *callsDB, id string, now time.Time) {
	t.Helper()
	tx, err := db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = advanceRingRunTx(tx, "ring_"+id+"_team", now); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
func TestRingSimultaneousOnlyOneClaimWins(t *testing.T) {
	_, db, plan := ringFixture(t, "simultaneous")
	insertRingCall(t, db, plan, "sim")
	offers, err := db.activeRingOffers("sim", "p1")
	if err != nil || len(offers) != 3 {
		t.Fatalf("offers %v %v", offers, err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			ok, err := db.claimPendingCall("sim", id, "p1")
			if err != nil {
				t.Error(err)
			}
			if ok {
				wins.Add(1)
			}
		}(int64(i%3 + 1))
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("winners=%d", wins.Load())
	}
	var claimed, canceled int
	_ = db.db.QueryRow(`SELECT COUNT(*) FROM call_offers WHERE status='claimed'`).Scan(&claimed)
	_ = db.db.QueryRow(`SELECT COUNT(*) FROM call_offers WHERE status='canceled'`).Scan(&canceled)
	if claimed != 1 || canceled != 2 {
		t.Fatalf("claimed=%d canceled=%d", claimed, canceled)
	}
}
func TestRingSequentialDeclineAndExpiry(t *testing.T) {
	_, db, plan := ringFixture(t, "sequential")
	insertRingCall(t, db, plan, "seq")
	offers, _ := db.activeRingOffers("seq", "p1")
	if ringOfferIDs(offers) != "d0" {
		t.Fatal(offers)
	}
	if ok, _ := db.claimPendingCall("seq", 2, "p1"); ok {
		t.Fatal("unoffered member claimed")
	}
	if grouped, err := db.declineRingOffers("seq", "p1", 1); !grouped || err != nil {
		t.Fatal(grouped, err)
	}
	offers, _ = db.activeRingOffers("seq", "p1")
	if ringOfferIDs(offers) != "d1" {
		t.Fatal(offers)
	}
	ringAdvance(t, db, "seq", time.Now().Add(21*time.Second))
	offers, _ = db.activeRingOffers("seq", "p1")
	if ringOfferIDs(offers) != "d2" {
		t.Fatal(offers)
	}
	ringAdvance(t, db, "seq", time.Now().Add(42*time.Second))
	var status string
	_ = db.db.QueryRow(`SELECT status FROM call_ring_runs`).Scan(&status)
	if status != "exhausted" {
		t.Fatal(status)
	}
	if ok, _ := db.claimPendingCall("seq", 3, "p1"); ok {
		t.Fatal("expired offer claimed")
	}
}
func TestRingRoundRobinPersistsRotationAndIgnoresReplay(t *testing.T) {
	_, db, plan := ringFixture(t, "round_robin")
	for i, want := range []string{"d0", "d1", "d2", "d0"} {
		id := fmt.Sprint("rr", i)
		insertRingCall(t, db, plan, id)
		insertRingCall(t, db, plan, id)
		offers, _ := db.activeRingOffers(id, "p1")
		if ringOfferIDs(offers) != want {
			t.Fatalf("%s = %s want %s", id, ringOfferIDs(offers), want)
		}
	}
}
func TestRingPriorityTiersAndFailedClaimRecovery(t *testing.T) {
	_, db, plan := ringFixture(t, "priority")
	insertRingCall(t, db, plan, "priority")
	offers, _ := db.activeRingOffers("priority", "p1")
	if ringOfferIDs(offers) != "d0,d1" {
		t.Fatal(offers)
	}
	if ok, err := db.claimPendingCall("priority", 2, "p1"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if err := db.releaseAnswerClaim("priority"); err != nil {
		t.Fatal(err)
	}
	offers, _ = db.activeRingOffers("priority", "p1")
	if ringOfferIDs(offers) != "d0" {
		t.Fatalf("recovery=%v", offers)
	}
	if _, err := db.declineRingOffers("priority", "p1", 1); err != nil {
		t.Fatal(err)
	}
	offers, _ = db.activeRingOffers("priority", "p1")
	if ringOfferIDs(offers) != "d2" {
		t.Fatal(offers)
	}
}
func TestRingBrowserAndAgentCompeteWithoutCrossProjectClaim(t *testing.T) {
	_, db, plan := ringFixture(t, "simultaneous")
	dest := plan.GroupDestinations["d1"]
	dest.Kind = "browser"
	plan.GroupDestinations["d1"] = dest
	insertRingCall(t, db, plan, "mixed")
	if ok, _ := db.claimPendingCallForHuman("mixed", "other"); ok {
		t.Fatal("cross-project browser claim")
	}
	if ok, err := db.claimPendingCallForHuman("mixed", "p1"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	row, _ := db.findCall("mixed")
	if row.PeerKind != peerKindHuman || row.RoutingDestinationID != "d1" {
		t.Fatalf("winner=%+v", row)
	}
	if ok, _ := db.claimPendingCall("mixed", 1, "p1"); ok {
		t.Fatal("second agent won")
	}
}

func publishedRingFixture(t *testing.T) (*App, *callsDB, *routeRow) {
	app, db, plan := ringFixture(t, "sequential")
	for _, dest := range plan.GroupDestinations {
		var config map[string]any
		_ = json.Unmarshal([]byte(dest.ConfigJSON), &config)
		if _, err := app.saveRoutingDestination("p1", dest.ID, dest.Name, dest.Kind, config, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.saveRingGroup("p1", "team", "Sales", "sequential", 20, plan.Group.Members); err != nil {
		t.Fatal(err)
	}
	if _, err := app.saveRoutingDestination("p1", "fallback", "Browser fallback", "browser", map[string]any{}, true); err != nil {
		t.Fatal(err)
	}
	def := routingDefinition{Entry: "team", Nodes: []routingNode{{ID: "team", Type: "ring_group", Config: map[string]any{"ring_group_id": "team"}, Branches: map[string]string{"no_answer": "fallback"}}, {ID: "fallback", Type: "destination", Config: map[string]any{"destination_id": "fallback"}}}}
	raw, _ := json.Marshal(def)
	flow, err := app.saveRoutingFlow("p1", "flow", "Team flow", "", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	version, validation, err := app.publishRoutingFlow("p1", flow.ID)
	if err != nil || len(validation) > 0 {
		t.Fatal(version, validation, err)
	}
	route := &routeRow{ID: "route", ProjectID: "p1", PhoneNumber: "+12025550199", AgentID: 9, AnswerMode: answerModeAgent, TimeoutSec: 20, CarrierSlug: "twilio", CarrierConnectionID: 9, Enabled: true, Secret: "route-secret", FlowID: flow.ID, PublishedFlowVersionID: version.ID, InboundTransport: inboundTransportProgrammable}
	if err := db.insertRoute(*route); err != nil {
		t.Fatal(err)
	}
	return app, db, route
}
func TestRingPublishedFlowOffersEveryMemberAndUsesFrozenOverflow(t *testing.T) {
	app, db, route := publishedRingFixture(t)
	row, _, err := app.recordInboundCall(route, "CA-published", "+12025550188", route.PhoneNumber)
	if err != nil {
		t.Fatal(err)
	}
	var offers int
	_ = db.db.QueryRow(`SELECT COUNT(*) FROM call_offers WHERE call_id=?`, row.ID).Scan(&offers)
	if offers != 3 {
		t.Fatal(offers)
	}
	// Edit the destination after publication. The current call must still offer
	// the browser fallback from its snapshot when all team members decline.
	if _, err := app.saveRoutingDestination("p1", "fallback", "Changed", "agent", map[string]any{"agent_id": 42}, true); err != nil {
		t.Fatal(err)
	}
	for id := int64(1); id <= 3; id++ {
		if grouped, err := db.declineRingOffers(row.ID, "p1", id); !grouped || err != nil {
			t.Fatal(grouped, err)
		}
	}
	runID := "ring_" + row.ID + "_team"
	if err := app.tickRingRun(globalCtx.WithProject("p1"), runID, row.ID); err != nil {
		t.Fatal(err)
	}
	row, _ = db.findCall(row.ID)
	if row.RoutingDestinationID != "fallback" || row.PeerKind != peerKindHuman {
		t.Fatalf("fallback=%+v", row)
	}
	if ok, err := db.claimPendingCallForHuman(row.ID, "p1"); !ok || err != nil {
		t.Fatalf("fallback answer %v %v", ok, err)
	}
}
func TestRingDisabledMemberIsSkippedOnlyForNewIngress(t *testing.T) {
	app, db, route := publishedRingFixture(t)
	old, _, err := app.recordInboundCall(route, "CA-old", "+12025550188", route.PhoneNumber)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.saveRoutingDestination("p1", "d0", "Disabled", "agent", map[string]any{"agent_id": 1}, false); err != nil {
		t.Fatal(err)
	}
	// Read the unmodified persisted route rather than the per-call resolved copy.
	freshRoute, _ := db.findRoute(route.ID)
	fresh, _, err := app.recordInboundCall(freshRoute, "CA-new", "+12025550187", route.PhoneNumber)
	if err != nil {
		t.Fatal(err)
	}
	oldOffers, _ := db.activeRingOffers(old.ID, "p1")
	newOffers, _ := db.activeRingOffers(fresh.ID, "p1")
	if ringOfferIDs(oldOffers) != "d0" || ringOfferIDs(newOffers) != "d1" {
		t.Fatalf("old=%v new=%v", oldOffers, newOffers)
	}
	// Replaying ingress restores the same filtered snapshot, not the old group.
	replayRoute, _ := db.findRoute(route.ID)
	if _, _, err = app.recordInboundCall(replayRoute, "CA-new", "+12025550187", route.PhoneNumber); err != nil {
		t.Fatal(err)
	}
	newOffers, _ = db.activeRingOffers(fresh.ID, "p1")
	if ringOfferIDs(newOffers) != "d1" {
		t.Fatal(newOffers)
	}
}
func TestRingBrowserStaleAnswerCannotTakeOverWinner(t *testing.T) {
	app, db, plan := ringFixture(t, "simultaneous")
	dest := plan.GroupDestinations["d1"]
	dest.Kind = "browser"
	plan.GroupDestinations["d1"] = dest
	insertRingCall(t, db, plan, "browser-race")
	answer := func(body string) int {
		request := httptest.NewRequest("POST", "/softphone/answer/browser-race", strings.NewReader(body))
		rec := httptest.NewRecorder()
		app.softphoneAnswer(rec, request, "p1", "browser-race")
		return rec.Code
	}
	if code := answer(`{"destination_id":"d1"}`); code != 200 {
		t.Fatal(code)
	}
	if code := answer(`{"destination_id":"d1"}`); code != 409 {
		t.Fatalf("stale answer took over: %d", code)
	}
	if code := answer(`{"rejoin":true}`); code != 200 {
		t.Fatalf("explicit rejoin: %d", code)
	}
}
