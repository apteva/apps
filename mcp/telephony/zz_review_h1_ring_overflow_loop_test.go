package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// A ring node with no no_answer/next falls back to the group's overflow node.
// Publishing must see that implicit edge and reject the cycle.
func TestReviewH1RingOverflowCycleRejectedAtPublish(t *testing.T) {
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
	if _, err := db.db.Exec(`UPDATE ring_groups SET overflow_node_id='team' WHERE id='team'`); err != nil {
		t.Fatal(err)
	}
	def := routingDefinition{Entry: "team", Nodes: []routingNode{{ID: "team", Type: "ring_group", Config: map[string]any{"ring_group_id": "team"}}}}
	raw, _ := json.Marshal(def)
	flow, err := app.saveRoutingFlow("p1", "flow", "Loop", "", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	_, validation, err := app.publishRoutingFlow("p1", flow.ID)
	if err == nil && !strings.Contains(strings.Join(validation, ";"), "ring-group overflow") {
		t.Fatalf("self-overflow published: validation=%v", validation)
	}
}

// Runtime defense for plans that were frozen before validation existed: an
// overflow that points back at the ring node ends the call instead of
// re-trying a missing decision on every tick until the 10-minute deadline.
func TestReviewH1RingSelfOverflowFailsThroughAtRuntime(t *testing.T) {
	app, db, plan := ringFixture(t, "simultaneous")
	plan.OverflowNodeID = "team"
	plan.ContextJSON = "{}"
	insertRingCall(t, db, plan, "loop-call")
	for id := int64(1); id <= 3; id++ {
		if grouped, err := db.declineRingOffers("loop-call", "p1", id); !grouped || err != nil {
			t.Fatal(grouped, err)
		}
	}
	runID := "ring_loop-call_team"
	if err := app.tickRingRun(globalCtx.WithProject("p1"), runID, "loop-call"); err != nil {
		t.Fatalf("tick: %v", err)
	}
	row, _ := db.findCall("loop-call")
	if row == nil || row.Status == "pending" {
		t.Fatalf("caller still pending: %+v", row)
	}
	var status string
	_ = db.db.QueryRow(`SELECT status FROM call_ring_runs WHERE id=?`, runID).Scan(&status)
	if status != "finished" {
		t.Fatalf("run status %q", status)
	}
}
