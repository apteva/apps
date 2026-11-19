package main

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
)

// simulationPlatform adds the optional simulation client to the audit stub
// and records the runtime create request.
type simulationPlatform struct {
	*auditPlatform
	testkit.IntegrationSimulationStub
	mu        sync.Mutex
	created   []sdk.RuntimeCreateRequest
	stateRead int
	// states, when set, answers successive state reads; the last repeats.
	states []*sdk.RuntimeIntegrationState
}

func (p *simulationPlatform) CreateRuntime(req sdk.RuntimeCreateRequest) (*sdk.RuntimeSummary, error) {
	p.mu.Lock()
	p.created = append(p.created, req)
	p.mu.Unlock()
	return p.auditPlatform.CreateRuntime(req)
}

func (p *simulationPlatform) GetRuntimeIntegrationState(runtimeID, slug string, sel sdk.RuntimeIntegrationSelector) (*sdk.RuntimeIntegrationState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.states) == 0 {
		return p.IntegrationSimulationStub.GetRuntimeIntegrationState(runtimeID, slug, sel)
	}
	i := p.stateRead
	if i >= len(p.states) {
		i = len(p.states) - 1
	}
	p.stateRead++
	return p.states[i], nil
}

func simulationTestApp(t *testing.T) (*App, *simulationPlatform) {
	t.Helper()
	s, base := auditService(t)
	p := &simulationPlatform{auditPlatform: base}
	s.ctx = sdk.NewAppCtxForTest(nil, s.db.db, nil, p, nil).WithProject("audit")
	return &App{svc: s}, p
}

func simulatedRun(t *testing.T, a *App, p *simulationPlatform) *Run {
	t.Helper()
	run, err := a.svc.start("", "test", EnvironmentSpec{IntegrationBindings: []sdk.RuntimeIntegrationBinding{{
		App: "telephony", Role: "carrier", Slug: "telnyx", Simulate: true,
		Seed: map[string][]map[string]any{"phone_numbers": {{"phone_number": "+15550100001"}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestSimulatedBindingReachesRuntimeCreate(t *testing.T) {
	a, p := simulationTestApp(t)
	simulatedRun(t, a, p)
	if len(p.created) != 1 || len(p.created[0].IntegrationBindings) != 1 {
		t.Fatalf("created=%+v", p.created)
	}
	b := p.created[0].IntegrationBindings[0]
	if !b.Simulate || b.Slug != "telnyx" || b.Seed["phone_numbers"][0]["phone_number"] != "+15550100001" {
		t.Fatalf("binding=%+v", b)
	}
}

func TestIntegrationBindingValidation(t *testing.T) {
	carrierFixture := ProtocolFixtureSpec{ID: "carrier", Pack: protocolPackTelephonyCarrier}
	cases := []struct {
		name string
		spec EnvironmentSpec
		want string
	}{
		{"seed without simulate", EnvironmentSpec{IntegrationBindings: []sdk.RuntimeIntegrationBinding{{App: "a", Role: "r", Slug: "telnyx", Seed: map[string][]map[string]any{"calls": {}}}}}, "seed requires simulate"},
		{"simulate without slug", EnvironmentSpec{IntegrationBindings: []sdk.RuntimeIntegrationBinding{{App: "a", Role: "r", Simulate: true}}}, "requires slug"},
		{"duplicate", EnvironmentSpec{IntegrationBindings: []sdk.RuntimeIntegrationBinding{{App: "a", Role: "r", Slug: "telnyx", Simulate: true}, {App: "a", Role: "r", Slug: "telnyx", Simulate: true}}}, "already simulated"},
		{"carrier fixture conflict", EnvironmentSpec{ProtocolFixtures: []ProtocolFixtureSpec{carrierFixture}, IntegrationBindings: []sdk.RuntimeIntegrationBinding{{App: "telephony", Role: "carrier", Slug: "telnyx", Simulate: true}}}, "conflicts with the telephony-carrier"},
	}
	for _, c := range cases {
		err := validateSpec(c.spec)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: err=%v", c.name, err)
		}
	}
	ok := []EnvironmentSpec{
		{IntegrationBindings: []sdk.RuntimeIntegrationBinding{{App: "a", Role: "r", Slug: "telnyx"}}},
		{IntegrationBindings: []sdk.RuntimeIntegrationBinding{{App: "a", Role: "r1", Slug: "telnyx", Simulate: true}, {App: "a", Role: "r2", Slug: "telnyx", Simulate: true}}},
		// A simulated binding on another role coexists with the old fixture.
		{ProtocolFixtures: []ProtocolFixtureSpec{carrierFixture}, IntegrationBindings: []sdk.RuntimeIntegrationBinding{{App: "telephony", Role: "sms", Slug: "telnyx", Simulate: true}}},
	}
	for i, spec := range ok {
		if err := validateSpec(spec); err != nil {
			t.Fatalf("ok %d: %v", i, err)
		}
	}
}

func TestSimulateChangesSpecDigest(t *testing.T) {
	digest := func(spec EnvironmentSpec) string {
		raw, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		d, err := specDigest(raw)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	plain := EnvironmentSpec{Version: 1, IntegrationBindings: []sdk.RuntimeIntegrationBinding{{App: "a", Role: "r", Slug: "telnyx"}}}
	sim := EnvironmentSpec{Version: 1, IntegrationBindings: []sdk.RuntimeIntegrationBinding{{App: "a", Role: "r", Slug: "telnyx", Simulate: true}}}
	seeded := EnvironmentSpec{Version: 1, IntegrationBindings: []sdk.RuntimeIntegrationBinding{{App: "a", Role: "r", Slug: "telnyx", Simulate: true, Seed: map[string][]map[string]any{"phone_numbers": {{"phone_number": "+1"}}}}}}
	if digest(plain) == digest(sim) || digest(sim) == digest(seeded) {
		t.Fatal("simulate or seed did not change spec_digest")
	}
}

func TestIntegrationTriggerTool(t *testing.T) {
	a, p := simulationTestApp(t)
	run := simulatedRun(t, a, p)
	p.OnTrigger = func(call testkit.IntegrationTriggerCall) (*sdk.RuntimeIntegrationTriggerResult, error) {
		if call.Request.Input["to"] == "+19999" {
			return nil, errors.New(`422: {"errors":[{"code":"number_not_found"}]}`)
		}
		return &sdk.RuntimeIntegrationTriggerResult{Result: json.RawMessage(`{"call_control_id":"c1"}`), Deliveries: []sdk.RuntimeIntegrationDelivery{{ID: "d1", Event: "call.initiated", App: "telephony", Target: "/webhooks/telnyx", State: "delivered", Status: 200}}}, nil
	}
	out, err := a.toolIntegrationTrigger(nil, jsonArgs(t, `{"run_id":"`+run.ID+`","slug":" telnyx ","trigger":"incoming_call","app":"telephony","role":"carrier","input":{"to":"+15550100001"},"wait":true,"timeout_ms":5000}`))
	if err != nil {
		t.Fatal(err)
	}
	res := out.(*sdk.RuntimeIntegrationTriggerResult)
	if len(res.Deliveries) != 1 || res.Deliveries[0].Status != 200 {
		t.Fatalf("result=%+v", res)
	}
	calls := p.Triggers()
	if len(calls) != 1 || calls[0].RuntimeID != run.RuntimeID || calls[0].Slug != "telnyx" || calls[0].Trigger != "incoming_call" ||
		calls[0].Request.App != "telephony" || calls[0].Request.Role != "carrier" || !calls[0].Request.Wait || calls[0].Request.TimeoutMS != 5000 {
		t.Fatalf("calls=%+v", calls)
	}
	if _, err := a.toolIntegrationTrigger(nil, jsonArgs(t, `{"run_id":"`+run.ID+`","slug":"telnyx","trigger":"incoming_call","input":{"to":"+19999"}}`)); err == nil || !strings.Contains(err.Error(), "number_not_found") {
		t.Fatalf("rejected trigger err=%v", err)
	}
	for _, bad := range []string{
		`{"run_id":"` + run.ID + `","trigger":"incoming_call"}`,
		`{"run_id":"` + run.ID + `","slug":"telnyx","trigger":"x","timeout_ms":60001}`,
		`{"run_id":"missing","slug":"telnyx","trigger":"x"}`,
	} {
		if _, err := a.toolIntegrationTrigger(nil, jsonArgs(t, bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestIntegrationStateTool(t *testing.T) {
	a, p := simulationTestApp(t)
	run := simulatedRun(t, a, p)
	p.States = map[string]*sdk.RuntimeIntegrationState{run.RuntimeID + "/telnyx": {Slug: "telnyx", Collections: map[string][]map[string]any{"calls": {{"state": "answered"}}}}}
	out, err := a.toolIntegrationState(nil, map[string]any{"run_id": run.ID, "slug": "telnyx"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(*sdk.RuntimeIntegrationState).Collections["calls"][0]["state"] != "answered" {
		t.Fatalf("state=%+v", out)
	}
	if _, err := a.toolIntegrationState(nil, map[string]any{"run_id": run.ID}); err == nil {
		t.Fatal("accepted missing slug")
	}
}

func TestIntegrationStateAssertion(t *testing.T) {
	a, p := simulationTestApp(t)
	run := simulatedRun(t, a, p)
	ringing := &sdk.RuntimeIntegrationState{Slug: "telnyx", Collections: map[string][]map[string]any{"calls": {{"to": "+1555", "state": "ringing"}}}}
	answered := &sdk.RuntimeIntegrationState{Slug: "telnyx", Collections: map[string][]map[string]any{"calls": {{"to": "+1555", "state": "answered"}}},
		Deliveries: []sdk.RuntimeIntegrationDelivery{
			{ID: "1", App: "telephony", State: "delivered", Status: 200},
			{ID: "2", App: "telephony", State: "delivered", Status: 204},
			{ID: "3", App: "other", State: "failed", Status: 500},
		}}
	p.states = []*sdk.RuntimeIntegrationState{ringing, ringing, answered}
	assert := func(raw string) AssertionResult {
		t.Helper()
		args := jsonArgs(t, raw)
		args["run_id"] = run.ID
		args["type"] = "integration_state"
		out, err := a.toolAssert(nil, args)
		if err != nil {
			t.Fatal(err)
		}
		return out.(AssertionResult)
	}
	// Polls until the call is answered.
	start := time.Now()
	if r := assert(`{"slug":"telnyx","collection":"calls","where":{"to":"+1555"},"require":{"state":"answered"},"timeout_ms":5000}`); !r.Passed {
		t.Fatalf("not answered: %+v", r)
	}
	if p.stateRead != 3 || time.Since(start) < 2*integrationAssertPoll {
		t.Fatalf("reads=%d elapsed=%s", p.stateRead, time.Since(start))
	}
	if r := assert(`{"slug":"telnyx","deliveries":true,"where":{"app":"telephony"},"require":{"state":"delivered","status":{"gte":200,"lt":300}},"match":"all"}`); !r.Passed {
		t.Fatalf("telephony deliveries: %+v", r)
	}
	if r := assert(`{"slug":"telnyx","deliveries":true,"require":{"status":{"gte":200,"lt":300}},"match":"all"}`); r.Passed {
		t.Fatalf("all deliveries should fail on the 500: %+v", r)
	} else if samples := r.Actual.(map[string]any)["samples"].([]map[string]any); len(samples) != 1 || samples[0]["id"] != "3" {
		t.Fatalf("samples=%+v", samples)
	}
	if r := assert(`{"slug":"telnyx","deliveries":true,"require":{"state":"failed"},"match":"none"}`); r.Passed {
		t.Fatal("none passed with a failed delivery")
	}
	if r := assert(`{"slug":"telnyx","deliveries":true,"where":{"app":"telephony"},"min_count":3}`); r.Passed {
		t.Fatal("min_count 3 passed with 2 records")
	}
	if r := assert(`{"slug":"telnyx","collection":"calls","require":{"state":{"in":["answered","bridged"]},"to":{"contains":"555"},"missing":{"exists":false}}}`); !r.Passed {
		t.Fatalf("operators: %+v", r)
	}
	// Without timeout_ms a failing check returns at once.
	if r := assert(`{"slug":"telnyx","collection":"calls","require":{"state":"hangup"}}`); r.Passed {
		t.Fatal("hangup passed")
	}
	for _, bad := range []string{
		`{"slug":"telnyx"}`,
		`{"slug":"telnyx","collection":"calls","deliveries":true}`,
		`{"collection":"calls"}`,
		`{"slug":"telnyx","collection":"calls","match":"some"}`,
		`{"slug":"telnyx","collection":"calls","timeout_ms":60001}`,
		`{"slug":"telnyx","collection":"calls","min_count":-1}`,
		`{"slug":"telnyx","collection":"calls","where":{"status":{"gte":"x"}}}`,
		`{"slug":"telnyx","collection":"calls","require":{"state":{"in":"answered"}}}`,
	} {
		args := jsonArgs(t, bad)
		args["run_id"], args["type"] = run.ID, "integration_state"
		if _, err := a.toolAssert(nil, args); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestRecordMatchesLiteralObjectsAndNumbers(t *testing.T) {
	record := map[string]any{"status": float64(200), "meta": map[string]any{"leg": "a", "n": float64(1)}}
	if !recordMatches(record, map[string]any{"status": 200, "meta": map[string]any{"leg": "a", "n": 1}, "meta.leg": "a"}) {
		t.Fatal("literal match failed")
	}
	if recordMatches(record, map[string]any{"meta.leg": map[string]any{"ne": "a"}}) {
		t.Fatal("ne matched")
	}
	if !recordMatches(record, map[string]any{"absent": map[string]any{"ne": "x"}}) {
		t.Fatal("ne on a missing field should match")
	}
	if recordMatches(record, map[string]any{"absent": nil}) {
		t.Fatal("null literal matched a missing field")
	}
}

func TestCatalogMarksCarrierFixtureDeprecated(t *testing.T) {
	items := protocolFixtureCatalog()
	if len(items) != 1 || !items[0].Deprecated || !strings.Contains(items[0].Replacement, `"simulate": true`) {
		t.Fatalf("catalog=%+v", items)
	}
	for _, name := range assertionTypeCatalog() {
		if name == "integration_state" {
			return
		}
	}
	t.Fatal("integration_state missing from assertion types")
}
