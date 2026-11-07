package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestDestinationTurnDetectionValidation(t *testing.T) {
	cases := []struct {
		name, kind, raw string
		want            *sdk.RealtimeTurnDetection
		invalid         bool
	}{
		{name: "absent", kind: "ai", raw: `{}`},
		{name: "empty inherits", kind: "ai", raw: `{"turn_detection":{}}`, want: telephonyTurnDetection()},
		{name: "profile only", kind: "ai", raw: `{"turn_detection":{"profile":"telephony"}}`, want: telephonyTurnDetection()},
		{name: "500", kind: "ai", raw: `{"turn_detection":{"profile":"telephony","silence_duration_ms":500}}`, want: &sdk.RealtimeTurnDetection{Profile: "telephony", SilenceDurationMS: 500}},
		{name: "implicit profile", kind: "agent", raw: `{"turn_detection":{"silence_duration_ms":500}}`, want: &sdk.RealtimeTurnDetection{Profile: "telephony", SilenceDurationMS: 500}},
		{name: "provider default", kind: "ai", raw: `{"turn_detection":{"profile":"default"}}`, want: &sdk.RealtimeTurnDetection{Profile: "default"}},
		{name: "minimum", kind: "ai", raw: `{"turn_detection":{"silence_duration_ms":1}}`, want: &sdk.RealtimeTurnDetection{Profile: "telephony", SilenceDurationMS: 1}},
		{name: "maximum", kind: "ai", raw: `{"turn_detection":{"silence_duration_ms":60000}}`, want: &sdk.RealtimeTurnDetection{Profile: "telephony", SilenceDurationMS: 60000}},
		{name: "null", kind: "ai", raw: `{"turn_detection":null}`, invalid: true},
		{name: "array", kind: "ai", raw: `{"turn_detection":[]}`, invalid: true},
		{name: "string", kind: "ai", raw: `{"turn_detection":"500"}`, invalid: true},
		{name: "null profile", kind: "ai", raw: `{"turn_detection":{"profile":null}}`, invalid: true},
		{name: "unknown profile", kind: "ai", raw: `{"turn_detection":{"profile":"fast"}}`, invalid: true},
		{name: "empty profile", kind: "ai", raw: `{"turn_detection":{"profile":""}}`, invalid: true},
		{name: "numeric profile", kind: "ai", raw: `{"turn_detection":{"profile":500}}`, invalid: true},
		{name: "zero", kind: "ai", raw: `{"turn_detection":{"silence_duration_ms":0}}`, invalid: true},
		{name: "negative", kind: "ai", raw: `{"turn_detection":{"silence_duration_ms":-1}}`, invalid: true},
		{name: "too large", kind: "ai", raw: `{"turn_detection":{"silence_duration_ms":60001}}`, invalid: true},
		{name: "fractional", kind: "ai", raw: `{"turn_detection":{"silence_duration_ms":500.5}}`, invalid: true},
		{name: "numeric string", kind: "ai", raw: `{"turn_detection":{"silence_duration_ms":"500"}}`, invalid: true},
		{name: "boolean", kind: "ai", raw: `{"turn_detection":{"silence_duration_ms":true}}`, invalid: true},
		{name: "null silence", kind: "ai", raw: `{"turn_detection":{"silence_duration_ms":null}}`, invalid: true},
		{name: "unknown field", kind: "ai", raw: `{"turn_detection":{"silence_duration":500}}`, invalid: true},
		{name: "human unchanged", kind: "browser", raw: `{}`},
		{name: "human override", kind: "browser", raw: `{"turn_detection":{}}`, invalid: true},
		{name: "pstn override", kind: "pstn", raw: `{"turn_detection":{}}`, invalid: true},
	}
	a := &App{}
	withRoutingTestDB(t, a, testCallsDB(t))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := destinationTurnDetection(c.kind, c.raw)
			if (err != nil) != c.invalid || !c.invalid && !reflect.DeepEqual(got, c.want) {
				t.Fatalf("turn=%+v err=%v, want %+v invalid=%t", got, err, c.want, c.invalid)
			}
			var config map[string]any
			if err := json.Unmarshal([]byte(c.raw), &config); err != nil {
				t.Fatal(err)
			}
			config["agent_id"] = 4
			config["phone_number"] = "+33189000000"
			_, err = a.saveRoutingDestination("project-a", "test", "AI", c.kind, config, true)
			if (err != nil) != c.invalid {
				t.Fatalf("save error=%v invalid=%t", err, c.invalid)
			}
		})
	}
}

// Use real destination save, flow publication and ingress persistence. Changes
// after publication must not affect that flow or its newly spawned session.
func installTurnDetectionFlow(t *testing.T, a *App, route *routeRow, silence int, fallback bool) *routingFlowRow {
	t.Helper()
	config := map[string]any{"agent_id": route.AgentID, "directive": "Help.", "greeting": "Hello."}
	if silence > 0 {
		config["turn_detection"] = map[string]any{"profile": "telephony", "silence_duration_ms": silence}
	}
	dest, err := a.saveRoutingDestination(route.ProjectID, "ai-turn", "AI", "ai", config, true)
	if err != nil {
		t.Fatal(err)
	}
	def := routingDefinition{Entry: "ai", Nodes: []routingNode{{ID: "ai", Type: "destination", Config: map[string]any{"destination_id": dest.ID}}}}
	if fallback {
		identity := phoneTestIdentity("adviser")
		if _, err := a.saveRoutingDestination(route.ProjectID, "human-turn", "Human", "browser", map[string]any{"capacity": destinationCapacity{Identity: identity, Limit: 1}}, true); err != nil {
			t.Fatal(err)
		}
		policy, _ := json.Marshal(phonePolicy{Users: []phoneUser{{Identity: identity, Enabled: true, phoneGrant: phoneGrant{Role: "user", Destinations: []string{"human-turn"}}}}})
		if _, err := a.db().db.Exec(`INSERT INTO telephony_access_policies(project_id,revision,policy_json) VALUES(?,1,?)`, route.ProjectID, string(policy)); err != nil {
			t.Fatal(err)
		}
		def.Entry = "choose"
		def.Nodes = append(def.Nodes, routingNode{ID: "choose", Type: "decision", Branches: map[string]string{"fallback": "ai"}, Config: map[string]any{"function_id": 1, "destination_ids": []any{"human-turn"}}})
	}
	raw, _ := json.Marshal(def)
	flow, err := a.saveRoutingFlow(route.ProjectID, "", "Turn test", "", string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, validation, err := a.publishRoutingFlow(route.ProjectID, flow.ID); err != nil || len(validation) > 0 {
		t.Fatalf("publish: %v %v", err, validation)
	}
	if result, err := a.assignRoutingFlowToNumbers(route.ProjectID, flow.ID, []string{route.ID}, nil); err != nil || !result.Valid {
		t.Fatalf("assign: %+v %v", result, err)
	}
	return flow
}

func requireTurnDetection(t *testing.T, got *sdk.RealtimeTurnDetection, silence int) {
	t.Helper()
	want := &sdk.RealtimeTurnDetection{Profile: "telephony", SilenceDurationMS: silence}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("turn detection=%+v want %+v (other defaults must remain unset)", got, want)
	}
}

func TestDestinationTurnDetectionSpawnAndExistingSession(t *testing.T) {
	for _, silence := range []int{0, 500} {
		t.Run(map[int]string{0: "default", 500: "override"}[silence], func(t *testing.T) {
			a, ctx, p, route, _, unblock := preparationFixture(t, false)
			flow := installTurnDetectionFlow(t, a, route, silence, false)
			route, _ = a.db().findRoute(route.ID)
			row, _, err := a.recordInboundCall(route, "turn-call", "+14155550102", route.PhoneNumber)
			if err != nil {
				t.Fatal(err)
			}
			_, plan, err := a.routingPlanForCall(row, nil)
			if err != nil {
				t.Fatal(err)
			}
			if silence > 0 {
				requireTurnDetection(t, plan.TurnDetection, silence)
			}
			// Editing live config has no effect on the published/call snapshot.
			if _, err := a.saveRoutingDestination(route.ProjectID, "ai-turn", "AI", "ai", map[string]any{"agent_id": 7, "directive": "Changed.", "turn_detection": map[string]any{"silence_duration_ms": 220}}, true); err != nil {
				t.Fatal(err)
			}
			unblock()
			if _, err := a.prepareInboundRealtime(ctx, row, plan.Directive, plan.Voice, plan.Greeting); err != nil {
				t.Fatal(err)
			}
			requireTurnDetection(t, waitPreparationEntered(t, p).TurnDetection, silence)
			thread := row.ThreadID
			if err := a.db().updateStatus(row.ID, "answered", ""); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				current, _ := a.db().findCall(row.ID)
				if _, err := a.prepareInboundRealtime(ctx, current, "Changed.", "", ""); err != nil || current.ThreadID != thread {
					t.Fatalf("existing session changed: %+v %v", current, err)
				}
			}
			if spawned, killed, carrier := p.counts(); spawned != 1 || killed != 0 || carrier != 0 {
				t.Fatalf("existing session operations: spawn=%d kill=%d carrier=%d", spawned, killed, carrier)
			}
			// A new publication applies the edited setting only to new ingress.
			if _, validation, err := a.publishRoutingFlow(route.ProjectID, flow.ID); err != nil || len(validation) > 0 {
				t.Fatalf("republish: %v %v", err, validation)
			}
			if result, err := a.assignRoutingFlowToNumbers(route.ProjectID, flow.ID, []string{route.ID}, nil); err != nil || !result.Valid {
				t.Fatalf("reassign: %+v %v", result, err)
			}
			route, _ = a.db().findRoute(route.ID)
			next, _, err := a.recordInboundCall(route, "turn-new-publication", "+14155550102", route.PhoneNumber)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.prepareInboundRealtime(ctx, next, "Changed.", "", ""); err != nil {
				t.Fatal(err)
			}
			requireTurnDetection(t, waitPreparationEntered(t, p).TurnDetection, 220)
			current, _ := a.db().findCall(row.ID)
			if current.ThreadID != thread || current.Status != "answered" {
				t.Fatalf("publication changed existing session: %+v", current)
			}
		})
	}
}

func TestDestinationTurnDetectionFallbackAndRetry(t *testing.T) {
	a, ctx, p, route, _, unblock := preparationFixture(t, false)
	installTurnDetectionFlow(t, a, route, 500, true)
	route, _ = a.db().findRoute(route.ID)
	row, _, err := a.recordInboundCall(route, "turn-fallback", "+14155550102", route.PhoneNumber)
	if err != nil {
		t.Fatal(err)
	}
	var d decisionRecord
	d.CallID, d.ProjectID, d.VersionID, d.NodeID = row.ID, row.ProjectID, row.RoutingFlowVersionID, "choose"
	plan, _, err := a.decisionPlan(row, d, decisionResponse{Action: "exhausted"})
	if err != nil {
		t.Fatal(err)
	}
	requireTurnDetection(t, plan.TurnDetection, 500)
	// Accepted plans are persisted as JSON by the routing worker.
	raw, _ := json.Marshal(plan)
	var restored inboundRoutingPlan
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err := a.persistRoutingProgress(row.ID, row.ProjectID, &restored); err != nil {
		t.Fatal(err)
	}
	p.failure = errors.New(`platform /api/apps/callback/threads/spawn-realtime: http 429: busy`)
	unblock()
	row, _ = a.db().findCall(row.ID)
	if _, err := a.prepareInboundRealtime(ctx, row, plan.Directive, plan.Voice, plan.Greeting); !errors.Is(err, errAIHandoffPending) {
		t.Fatalf("expected bounded retry, got %v", err)
	}
	requireTurnDetection(t, waitPreparationEntered(t, p).TurnDetection, 500)
	if _, err := a.saveRoutingDestination(route.ProjectID, "ai-turn", "AI", "ai", map[string]any{"agent_id": 7, "directive": "Help.", "turn_detection": map[string]any{"silence_duration_ms": 220}}, true); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.failure = nil
	p.mu.Unlock()
	aiRetryDue(t, a, row.ID)
	row, _ = a.db().findCall(row.ID)
	if _, err := a.prepareInboundRealtime(ctx, row, plan.Directive, plan.Voice, plan.Greeting); err != nil {
		t.Fatal(err)
	}
	requireTurnDetection(t, waitPreparationEntered(t, p).TurnDetection, 500)
	if spawned, _, _ := p.counts(); spawned != 2 {
		t.Fatalf("spawn count=%d", spawned)
	}
}

func TestDestinationTurnDetectionSaveIsProjectScoped(t *testing.T) {
	a := &App{}
	withRoutingTestDB(t, a, testCallsDB(t))
	config := func(ms int) map[string]any {
		return map[string]any{"agent_id": 4, "turn_detection": map[string]any{"silence_duration_ms": ms}}
	}
	first, err := a.saveRoutingDestination("first", "first-ai", "AI", "ai", config(500), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.saveRoutingDestination("second", first.ID, "AI", "ai", config(220), true); err == nil {
		t.Fatal("another project overwrote destination")
	}
	if _, err := a.saveRoutingDestination("second", "second-ai", "AI", "ai", config(220), true); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		project, id string
		silence     int
	}{{"first", "first-ai", 500}, {"second", "second-ai", 220}} {
		row, err := a.findRoutingDestination(c.project, c.id)
		if err != nil || row == nil {
			t.Fatalf("find: %+v %v", row, err)
		}
		turn, err := destinationTurnDetection(row.Kind, row.ConfigJSON)
		if err != nil {
			t.Fatal(err)
		}
		requireTurnDetection(t, turn, c.silence)
	}
	if row, err := a.findRoutingDestination("second", first.ID); err != nil || row != nil {
		t.Fatalf("destination leaked across projects: %+v %v", row, err)
	}
}

func TestDestinationTurnDetectionLegacySnapshotExplicit(t *testing.T) {
	a, ctx, p, _, row, unblock := preparationFixture(t, true)
	var raw string
	if err := a.db().db.QueryRow(`SELECT context_json FROM call_route_executions WHERE call_id=?`, row.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var ex routingExecutionContext
	if err := json.Unmarshal([]byte(raw), &ex); err != nil {
		t.Fatal(err)
	}
	dest := ex.Definition.Destinations[row.RoutingDestinationID]
	var config map[string]any
	if err := json.Unmarshal([]byte(dest.ConfigJSON), &config); err != nil {
		t.Fatal(err)
	}
	config["turn_detection"] = map[string]any{"silence_duration_ms": 500}
	destConfig, _ := json.Marshal(config)
	dest.ConfigJSON = string(destConfig)
	ex.Definition.Destinations[dest.ID] = dest
	rawContext, _ := json.Marshal(ex)
	if _, err := a.db().db.Exec(`UPDATE call_route_executions SET context_json=? WHERE call_id=?`, string(rawContext), row.ID); err != nil {
		t.Fatal(err)
	}
	_, plan, err := a.routingPlanForCall(row, nil)
	if err != nil {
		t.Fatal(err)
	}
	requireTurnDetection(t, plan.TurnDetection, 500)
	unblock()
	if _, err := a.prepareInboundRealtime(ctx, row, plan.Directive, plan.Voice, plan.Greeting); err != nil {
		t.Fatal(err)
	}
	requireTurnDetection(t, waitPreparationEntered(t, p).TurnDetection, 500)
}

func TestDestinationTurnDetectionClaimedOfferIsolation(t *testing.T) {
	a, db, plan := ringFixture(t, "simultaneous")
	dest := plan.GroupDestinations["d1"]
	dest.ConfigJSON = `{"agent_id":2,"turn_detection":{"silence_duration_ms":500}}`
	plan.GroupDestinations[dest.ID] = dest
	insertRingCall(t, db, plan, "turn-ring")
	if claimed, err := db.claimPendingCall("turn-ring", 2, "p1"); err != nil || !claimed {
		t.Fatalf("claim: %t %v", claimed, err)
	}
	row, _ := db.findCall("turn-ring")
	turn, err := a.turnDetectionForNewSession(row)
	if err != nil {
		t.Fatal(err)
	}
	requireTurnDetection(t, turn, 500)
	// No accidental reuse across projects, destinations or outbound calls.
	for _, change := range []func(*callRow){
		func(r *callRow) { r.ProjectID = "other"; r.RoutingFlowVersionID = "" },
		func(r *callRow) { r.RoutingDestinationID = "" },
		func(r *callRow) { r.Direction = "outbound" },
	} {
		copy := *row
		change(&copy)
		turn, err := a.turnDetectionForNewSession(&copy)
		if err != nil {
			t.Fatal(err)
		}
		requireTurnDetection(t, turn, 0)
	}
}
