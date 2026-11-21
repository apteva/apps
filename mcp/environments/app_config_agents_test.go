package main

import (
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// orderPlatform records runtime creation, seed calls and agent spawns in order.
type orderPlatform struct {
	*auditPlatform
	created []sdk.RuntimeCreateRequest
	events  []string
	inputs  []map[string]any
	nextID  int64
}

func (p *orderPlatform) CreateRuntime(req sdk.RuntimeCreateRequest) (*sdk.RuntimeSummary, error) {
	p.created = append(p.created, req)
	return p.auditPlatform.CreateRuntime(req)
}

func (p *orderPlatform) CallRuntimeAppResult(_, app, tool string, input map[string]any, _ any) error {
	p.events = append(p.events, "seed:"+app+"."+tool)
	p.inputs = append(p.inputs, input)
	return nil
}

func (p *orderPlatform) SpawnRuntimeAgent(_ string, req sdk.RuntimeAgentSpawnRequest) (*sdk.RuntimeAgent, error) {
	p.nextID++
	p.events = append(p.events, "agent:"+req.Alias)
	return &sdk.RuntimeAgent{ID: 9000 + p.nextID, Alias: req.Alias}, nil
}

func orderTestApp(t *testing.T) (*App, *orderPlatform) {
	t.Helper()
	s, base := auditService(t)
	p := &orderPlatform{auditPlatform: base}
	s.ctx = sdk.NewAppCtxForTest(nil, s.db.db, nil, p, nil).WithProject("audit")
	return &App{svc: s}, p
}

func TestAppConfigReachesRuntimeCreate(t *testing.T) {
	a, p := orderTestApp(t)
	cfg := map[string]map[string]string{"telephony": {"softphone_webrtc_enabled": "true"}}
	if _, err := a.svc.start("", "test", EnvironmentSpec{AppConfig: cfg}); err != nil {
		t.Fatal(err)
	}
	if len(p.created) != 1 || p.created[0].AppConfig["telephony"]["softphone_webrtc_enabled"] != "true" {
		t.Fatalf("created=%+v", p.created)
	}
}

func TestAppConfigValidation(t *testing.T) {
	bad := []map[string]map[string]string{
		{"": {"a": "b"}},
		{"telephony": {"": "b"}},
		{"telephony": {"k": strings.Repeat("x", maxAppConfigValueLen+1)}},
	}
	for i, cfg := range bad {
		if err := validateSpec(EnvironmentSpec{AppConfig: cfg}); err == nil {
			t.Fatalf("bad %d accepted", i)
		}
	}
	// A JSON boolean is not a setting value: settings are strings.
	if _, err := orderTestAppSpec(t, `{"app_config":{"telephony":{"softphone_webrtc_enabled":true}}}`); err == nil {
		t.Fatal("accepted a boolean value")
	}
}

func orderTestAppSpec(t *testing.T, raw string) (EnvironmentSpec, error) {
	t.Helper()
	var spec EnvironmentSpec
	err := decodeArgs(jsonArgs(t, raw), &spec)
	return spec, err
}

func TestAppConfigChangesSpecDigest(t *testing.T) {
	a := mustDigest(t, `{"version":1}`)
	b := mustDigest(t, `{"version":1,"app_config":{"telephony":{"softphone_webrtc_enabled":"true"}}}`)
	if a == b {
		t.Fatal("app_config did not change spec_digest")
	}
}

func mustDigest(t *testing.T, raw string) string {
	t.Helper()
	d, err := specDigest([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSeedsReferenceRunAgentsSpawnedFirst(t *testing.T) {
	a, p := orderTestApp(t)
	spec := EnvironmentSpec{
		Agents: []AgentSpec{
			{Alias: "observer", Draft: &sdk.RuntimeAgentDraft{Name: "o", Directive: "watch"}},
			{Alias: "fallback", Draft: &sdk.RuntimeAgentDraft{Name: "f", Directive: "answer calls"}, Provider: "openai", Model: "gpt-realtime"},
		},
		Seeds: []SeedStep{
			{App: "telephony", Tool: "telephony_destinations_create", Input: map[string]any{"kind": "agent", "agent_id": map[string]any{"$agent": "fallback"}}},
			{App: "telephony", Tool: "telephony_flows_update", Input: map[string]any{"steps": []any{map[string]any{"destination": "dest_1"}, map[string]any{"agent_id": map[string]any{"$agent": "fallback"}}}}},
		},
	}
	if err := validateSpec(spec); err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.start("", "test", spec); err != nil {
		t.Fatal(err)
	}
	want := []string{"agent:fallback", "seed:telephony.telephony_destinations_create", "seed:telephony.telephony_flows_update", "agent:observer"}
	if strings.Join(p.events, ",") != strings.Join(want, ",") {
		t.Fatalf("order=%v want %v", p.events, want)
	}
	if p.inputs[0]["agent_id"] != int64(9001) {
		t.Fatalf("destination agent_id=%#v", p.inputs[0]["agent_id"])
	}
	steps := p.inputs[1]["steps"].([]any)
	if steps[1].(map[string]any)["agent_id"] != int64(9001) {
		t.Fatalf("flow agent_id=%#v", steps[1])
	}
}

func TestSeedAgentRefValidation(t *testing.T) {
	cases := map[string]EnvironmentSpec{
		"no agent in this spec": {Seeds: []SeedStep{{App: "a", Tool: "t", Input: map[string]any{"id": map[string]any{"$agent": "ghost"}}}}},
		"one run agent alias":   {Agents: []AgentSpec{{Alias: "x"}}, Seeds: []SeedStep{{App: "a", Tool: "t", Input: map[string]any{"id": map[string]any{"$agent": "x", "extra": 1}}}}},
		"requires an agent":     {Seeds: []SeedStep{{App: "a", Tool: "t", Input: map[string]any{"id": map[string]any{"$agent": ""}}}}},
	}
	for want, spec := range cases {
		if err := validateSpec(spec); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err=%v", want, err)
		}
	}
	// An agent without an alias cannot be referenced.
	spec := EnvironmentSpec{Agents: []AgentSpec{{SourceAgentID: 5}}, Seeds: []SeedStep{{App: "a", Tool: "t", Input: map[string]any{"id": map[string]any{"$agent": "main"}}}}}
	if err := validateSpec(spec); err == nil {
		t.Fatal("accepted a reference to an agent without an alias")
	}
}

func TestAgentsWithoutSeedRefsKeepStartingAfterSeeds(t *testing.T) {
	a, p := orderTestApp(t)
	spec := EnvironmentSpec{Agents: []AgentSpec{{Alias: "main"}}, Seeds: []SeedStep{{App: "crm", Tool: "seed"}}}
	if _, err := a.svc.start("", "test", spec); err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.events, ",") != "seed:crm.seed,agent:main" {
		t.Fatalf("order=%v", p.events)
	}
}
