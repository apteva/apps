package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

func testStore(t *testing.T) store {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/environments.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, name := range []string{"001_init.sql", "002_web_fixtures.sql", "003_single_active_run.sql", "004_voice_calls.sql", "005_protocol_fixtures.sql"} {
		migration, err := os.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	return store{db: db}
}

func TestProtocolFixturePersistenceBindingsAndEvents(t *testing.T) {
	s := testStore(t)
	spec := EnvironmentSpec{ProtocolFixtures: []ProtocolFixtureSpec{{ID: "carrier", Pack: "telephony-carrier"}}}
	if err := validateSpec(spec); err != nil {
		t.Fatal(err)
	}
	normalizeProtocolFixtureSpecs(&spec)
	bindings := protocolFixtureBindings(spec)
	if len(bindings) != 1 || bindings[0].App != "telephony" || bindings[0].Role != "carrier" || bindings[0].Slug != "twilio" || bindings[0].Credentials["auth_token"] == "" {
		t.Fatalf("bindings=%#v", bindings)
	}
	run := &Run{ID: "run-carrier", RuntimeID: "rt-carrier", Status: "starting", StartedAt: time.Now().UTC()}
	if err := s.createRun(run); err != nil {
		t.Fatal(err)
	}
	fixture := spec.ProtocolFixtures[0]
	if bindings[0].Credentials["auth_token"] != stringConfig(fixture.Config, "auth_token", "") {
		t.Fatal("persisted fixture and runtime binding use different auth tokens")
	}
	instance := &ProtocolFixtureInstance{RunID: run.ID, ID: fixture.ID, Pack: fixture.Pack, Version: fixture.Version, Protocol: "twilio", TargetApp: fixture.TargetApp, Status: "running", Config: fixture.Config}
	if err := s.createProtocolFixture(instance); err != nil {
		t.Fatal(err)
	}
	event := &ProtocolFixtureEvent{RunID: run.ID, FixtureID: fixture.ID, CallID: "call-one", Type: "webhook.inbound", Direction: "fixture_to_app", Data: map[string]any{"status": 200}}
	if err := s.addProtocolEvent(event); err != nil {
		t.Fatal(err)
	}
	got, err := s.getProtocolFixture(run.ID, fixture.ID)
	if err != nil || got == nil || got.TargetApp != "telephony" {
		t.Fatalf("fixture=%#v err=%v", got, err)
	}
	events, err := s.listProtocolEvents(run.ID, fixture.ID, "call-one")
	if err != nil || len(events) != 1 || events[0].Type != "webhook.inbound" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestEnvironmentSpecPreservesRealConnectionBindings(t *testing.T) {
	spec := EnvironmentSpec{
		ConnectionIDs: []int64{6},
		ConnectionBindings: []sdk.RuntimeConnectionBinding{{
			App: "computer", Role: "browserbase", ConnectionID: 6,
		}},
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("api_key")) || bytes.Contains(raw, []byte("credentials")) {
		t.Fatalf("environment spec leaked credentials: %s", raw)
	}
	var restored EnvironmentSpec
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored.ConnectionBindings) != 1 || restored.ConnectionBindings[0].ConnectionID != 6 || restored.ConnectionBindings[0].App != "computer" || restored.ConnectionBindings[0].Role != "browserbase" {
		t.Fatalf("connection bindings lost: %#v", restored.ConnectionBindings)
	}
}

func TestTwilioFixtureSignatureAndCodec(t *testing.T) {
	form := url.Values{"CallSid": {"CA1"}, "From": {"+15550100002"}}
	if got := twilioFixtureSignature("https://example.test/inbound", form, "secret"); got != "IOSPEgp/OjWf5/tJEDTAUc/GDDo=" {
		t.Fatalf("signature=%q", got)
	}
	pcm := []int16{-20000, -1000, 0, 1000, 20000}
	roundTrip := carrierUlawToPCM(carrierPCMToUlaw(pcm))
	for i := range pcm {
		if delta := int(roundTrip[i]) - int(pcm[i]); delta < -2000 || delta > 2000 {
			t.Fatalf("sample %d round trip=%d want near %d", i, roundTrip[i], pcm[i])
		}
	}
	gateway, err := carrierGatewayURL("wss://runtime.apteva.invalid/rt-one/api/apps/telephony/_install/7/media/twilio/call/token", &sdk.RuntimeAppEndpoint{
		PlatformURL: "https://runtime.apteva.invalid/rt-one",
		GatewayURL:  "http://127.0.0.1:5280/api/environment-app-public/rt-one",
	})
	if err != nil || gateway != "ws://127.0.0.1:5280/api/environment-app-public/rt-one/api/apps/telephony/_install/7/media/twilio/call/token" {
		t.Fatalf("gateway=%q err=%v", gateway, err)
	}
}

func TestVoiceCallPersistenceAndValidation(t *testing.T) {
	s := testStore(t)
	started := time.Now().UTC()
	call := &VoiceCall{
		ID: "call-one", RunID: "run-one", Status: "completed",
		Spec:       VoiceFixtureSpec{CallerGoal: "Book an appointment", TargetAgent: "main", TimeoutSeconds: 90},
		Transcript: []VoiceTranscriptTurn{{Speaker: "caller", Text: "I need an appointment.", Time: started, AtMS: 250}},
		Metrics:    VoiceCallMetrics{DurationMS: 3200, FirstResponseMS: 850, ToolCalls: 1, EndedBy: "caller"},
		BridgeExits: []VoiceBridgeExitResult{{
			Leg: "caller_to_target", Endpoint: "caller", Operation: "read",
			CloseCode: 1000, Reason: "caller_done", ElapsedMS: 3100, NormalClosure: true,
		}},
		StartedAt: started,
	}
	if err := s.saveVoiceCall(call); err != nil {
		t.Fatal(err)
	}
	got, err := s.getVoiceCall(call.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Spec.CallerGoal != call.Spec.CallerGoal || len(got.Transcript) != 1 ||
		got.Metrics.FirstResponseMS != 850 || len(got.BridgeExits) != 1 ||
		got.BridgeExits[0].Reason != "caller_done" {
		t.Fatalf("voice call=%#v", got)
	}
	event := voiceCallEvent(got)
	if event["call_id"] != call.ID || event["run_id"] != call.RunID || len(event["transcript"].([]VoiceTranscriptTurn)) != 1 {
		t.Fatalf("voice event=%#v", event)
	}
	if len(event["bridge_exits"].([]VoiceBridgeExitResult)) != 1 {
		t.Fatalf("voice event bridge exits=%#v", event["bridge_exits"])
	}
	if err := validateVoiceSpec(VoiceFixtureSpec{CallerGoal: "Help me", TimeoutSeconds: 90}); err != nil {
		t.Fatal(err)
	}
	if err := validateVoiceSpec(VoiceFixtureSpec{TimeoutSeconds: 90}); err == nil {
		t.Fatal("accepted voice call without caller goal")
	}
}

func TestDefinitionAndRunPersistence(t *testing.T) {
	s := testStore(t)
	d := &Definition{ID: "env-one", Name: "One", Spec: EnvironmentSpec{Version: 1, TTLSeconds: 3600, AppInstallIDs: []int64{4}}, DesiredState: "stopped"}
	if err := s.saveDefinition(d); err != nil {
		t.Fatal(err)
	}
	got, err := s.getDefinition(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Name != "One" || !reflect.DeepEqual(got.Spec.AppInstallIDs, []int64{4}) {
		t.Fatalf("definition=%#v", got)
	}
	r := &Run{ID: "run-one", EnvironmentID: d.ID, RuntimeID: "rt-one", Kind: "interactive", Status: "starting", StartedAt: d.CreatedAt}
	if err := s.createRun(r); err != nil {
		t.Fatal(err)
	}
	if err := s.updateRun(r.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	active, err := s.activeRun(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active == nil || active.RuntimeID != "rt-one" || active.Status != "running" {
		t.Fatalf("run=%#v", active)
	}
	if err := s.updateRun(r.ID, "stopped", ""); err != nil {
		t.Fatal(err)
	}
	stopped, err := s.getRun(r.ID)
	if err != nil || stopped == nil || stopped.StoppedAt == nil {
		t.Fatalf("stopped=%#v err=%v", stopped, err)
	}
	if err := s.updateRun(r.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	restarted, err := s.getRun(r.ID)
	if err != nil || restarted == nil || restarted.StoppedAt != nil {
		t.Fatalf("restarted=%#v err=%v", restarted, err)
	}
	if err := s.updateRun(r.ID, "stopped", ""); err != nil {
		t.Fatal(err)
	}
	active, err = s.activeRun(d.ID)
	if err != nil || active != nil {
		t.Fatalf("active=%#v err=%v", active, err)
	}
}

func TestOnlyOneActiveRunPerEnvironment(t *testing.T) {
	s := testStore(t)
	now := time.Now().UTC()
	first := &Run{ID: "run-first", EnvironmentID: "env-one", RuntimeID: "rt-first", Kind: "interactive", Status: "starting", StartedAt: now}
	second := &Run{ID: "run-second", EnvironmentID: "env-one", RuntimeID: "rt-second", Kind: "reconcile", Status: "starting", StartedAt: now.Add(time.Second)}
	if err := s.createRun(first); err != nil {
		t.Fatal(err)
	}
	if err := s.createRun(second); err == nil {
		t.Fatal("created a second active run for one environment")
	}
	if err := s.updateRun(first.ID, "expired", "runtime no longer exists"); err != nil {
		t.Fatal(err)
	}
	if err := s.createRun(second); err != nil {
		t.Fatalf("create after terminal transition: %v", err)
	}
}

func TestListDefinitionsWithSingleDatabaseConnection(t *testing.T) {
	s := testStore(t)
	s.db.SetMaxOpenConns(1)
	d := &Definition{ID: "env-list", Name: "List", Spec: EnvironmentSpec{Version: 1, TTLSeconds: 3600}, DesiredState: "running"}
	if err := s.saveDefinition(d); err != nil {
		t.Fatal(err)
	}
	if err := s.createRun(&Run{ID: "run-list", EnvironmentID: d.ID, RuntimeID: "rt-list", Kind: "interactive", Status: "running", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	type result struct {
		definitions []Definition
		err         error
	}
	done := make(chan result, 1)
	go func() {
		definitions, err := s.listDefinitions()
		done <- result{definitions: definitions, err: err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if len(got.definitions) != 1 || got.definitions[0].ActiveRun == nil || got.definitions[0].ActiveRun.ID != "run-list" {
			t.Fatalf("definitions=%#v", got.definitions)
		}
	case <-time.After(time.Second):
		// Release a nested-query implementation so test cleanup can complete.
		s.db.SetMaxOpenConns(2)
		<-done
		t.Fatal("listDefinitions blocked on its own open result set")
	}
}

func TestEmbeddedAndSourceManifestsExposeSameTools(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	source, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	embedded := (&App{}).Manifest()
	toolNames := func(m sdk.Manifest) []string {
		out := make([]string, 0, len(m.Provides.MCPTools))
		for _, tool := range m.Provides.MCPTools {
			out = append(out, tool.Name)
		}
		sort.Strings(out)
		return out
	}
	if source.Name != embedded.Name || source.Version != embedded.Version || !reflect.DeepEqual(toolNames(*source), toolNames(embedded)) {
		t.Fatalf("manifest drift: source=%s@%s embedded=%s@%s", source.Name, source.Version, embedded.Name, embedded.Version)
	}
	expectedRef := "environments/v" + source.Version
	if source.Runtime.Source == nil || embedded.Runtime.Source == nil ||
		source.Runtime.Source.Ref != expectedRef || embedded.Runtime.Source.Ref != expectedRef {
		t.Fatalf("manifest runtime ref drift: expected=%s source=%v embedded=%v", expectedRef, source.Runtime.Source, embedded.Runtime.Source)
	}
}

func TestSpecValidation(t *testing.T) {
	if err := validateSpec(EnvironmentSpec{TTLSeconds: 59}); err == nil {
		t.Fatal("accepted short TTL")
	}
	if err := validateSpec(EnvironmentSpec{TTLSeconds: 3600}); err != nil {
		t.Fatal(err)
	}
	if err := validateSpec(EnvironmentSpec{WebFixtures: []WebFixtureSpec{{ID: "site", Pack: "unknown"}}}); err == nil {
		t.Fatal("accepted unknown web fixture pack")
	}
	if err := validateSpec(EnvironmentSpec{}); err != nil {
		t.Fatalf("empty infrastructure spec must remain valid for Evals: %v", err)
	}
}

func TestEnvironmentRunCreateSchemaIsStrictAndComplete(t *testing.T) {
	var schema map[string]any
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "environment_run_create" {
			schema = tool.InputSchema
			break
		}
	}
	if schema == nil {
		t.Fatal("environment_run_create tool not found")
	}
	if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
		t.Fatalf("top-level schema must reject unknown properties: %#v", schema)
	}
	properties, _ := schema["properties"].(map[string]any)
	spec, _ := properties["spec"].(map[string]any)
	if additional, ok := spec["additionalProperties"].(bool); !ok || additional {
		t.Fatalf("EnvironmentSpec must reject unknown properties: %#v", spec)
	}
	specProperties, _ := spec["properties"].(map[string]any)
	for _, field := range []string{
		"version", "ttl_seconds", "app_install_ids", "connection_ids", "mcp_server_ids",
		"network_mode", "integration_mode", "allow_host_suffixes", "http_mocks",
		"integration_fixtures", "integration_bindings", "connection_bindings", "subscriptions",
		"seeds", "agents", "snapshot_id", "web_fixtures", "protocol_fixtures", "voice_fixtures",
	} {
		if _, found := specProperties[field]; !found {
			t.Errorf("EnvironmentSpec schema is missing %q", field)
		}
	}
}

func TestEnvironmentRunCreateRejectsPseudoEvaluationArguments(t *testing.T) {
	app := &App{}
	argsWithProject := map[string]any{"_project_id": "project-one", "kind": "eval", "spec": map[string]any{"version": 1.0}}
	var decoded struct {
		Kind string           `json:"kind"`
		Spec *EnvironmentSpec `json:"spec"`
	}
	if err := decodeStrictArgs(argsWithProject, &decoded); err != nil || decoded.Spec == nil || decoded.Spec.Version != 1 {
		t.Fatalf("trusted project context rejected: decoded=%#v err=%v", decoded, err)
	}
	if argsWithProject["_project_id"] != "project-one" {
		t.Fatal("strict decoder mutated caller arguments")
	}
	for name, args := range map[string]map[string]any{
		"top-level suite": {
			"environment_id": "env-one",
			"suite_id":       "suite-one",
			"spec":           map[string]any{},
		},
		"nested case": {
			"spec": map[string]any{
				"input":      "support request",
				"case_id":    "billing-1",
				"assertions": []any{},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := app.toolRunCreate(nil, args); err == nil || !strings.Contains(err.Error(), "must be executed through Evals") {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if _, err := app.toolRunCreate(nil, map[string]any{"spec": map[string]any{}, "unexpected": true}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error=%v", err)
	}
	if _, err := app.toolRunCreate(nil, map[string]any{}); err == nil || err.Error() != "spec required" {
		t.Fatalf("missing spec error=%v", err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/runs", strings.NewReader(`{"suite_id":"suite-one","spec":{"input":"support request","assertions":[]}}`))
	app.handleRuns(recorder, request)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "must be executed through Evals") {
		t.Fatalf("HTTP status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAssertionTypeCatalogIsAuthoritative(t *testing.T) {
	want := []string{"app_state", "mcp_state", "mcp_tool_call", "edge_call", "telemetry", "web_state", "web_event", "protocol_event"}
	got := assertionTypeCatalog()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("assertion types=%v, want %v", got, want)
	}
	got[0] = "mutated"
	if assertionTypeCatalog()[0] != "app_state" {
		t.Fatal("assertion type catalog returned mutable shared state")
	}
}

func TestPatreonFixtureLifecycleAndAssertions(t *testing.T) {
	svc := &service{db: testStore(t)}
	run := &Run{ID: "run-web", RuntimeID: "runtime-web", Kind: "test", Status: "starting", StartedAt: time.Now().UTC()}
	if err := svc.db.createRun(run); err != nil {
		t.Fatal(err)
	}
	spec := EnvironmentSpec{WebFixtures: []WebFixtureSpec{{ID: "patreon", Pack: "patreon", Scenario: "new-visitor", Seed: map[string]any{"creator_slug": "studio-north"}}}}
	if err := svc.createWebFixtures(run, spec); err != nil {
		t.Fatal(err)
	}
	if len(run.WebFixtures) != 1 || run.WebFixtures[0].TestURL == "" {
		t.Fatalf("fixtures=%#v", run.WebFixtures)
	}
	if _, _, err := svc.applyFixtureAction(run.ID, "patreon", "select_tier", map[string]any{"tier": "supporter"}); err != nil {
		t.Fatal(err)
	}
	if _, event, err := svc.applyFixtureAction(run.ID, "patreon", "checkout", nil); err != nil {
		t.Fatal(err)
	} else if event == nil || event.Type != "membership.created" {
		t.Fatalf("event=%#v", event)
	}
	stateResult, err := svc.assert(run.RuntimeID, Assertion{Type: "web_state", Fixture: "patreon", Path: "membership.tier", Equals: "supporter"})
	if err != nil || !stateResult.Passed {
		t.Fatalf("state assertion=%#v err=%v", stateResult, err)
	}
	eventResult, err := svc.assert(run.RuntimeID, Assertion{Type: "web_event", Fixture: "patreon", EventType: "membership.created", Path: "tier", Equals: "supporter"})
	if err != nil || !eventResult.Passed {
		t.Fatalf("event assertion=%#v err=%v", eventResult, err)
	}
	if err := svc.resetFixture(run.ID, "patreon"); err != nil {
		t.Fatal(err)
	}
	detail, err := svc.fixtureDetail(run.ID, "patreon")
	if err != nil {
		t.Fatal(err)
	}
	state := detail["state"].(map[string]any)
	if state["membership"] != nil || len(detail["events"].([]WebFixtureEvent)) != 0 {
		t.Fatalf("fixture did not reset: %#v", detail)
	}
}

func TestPatreonFixtureSignedRoute(t *testing.T) {
	svc := &service{db: testStore(t)}
	run := &Run{ID: "run-route", RuntimeID: "runtime-route", Kind: "test", Status: "starting", StartedAt: time.Now().UTC()}
	if err := svc.db.createRun(run); err != nil {
		t.Fatal(err)
	}
	if err := svc.createWebFixtures(run, EnvironmentSpec{WebFixtures: []WebFixtureSpec{{ID: "patreon", Pack: "patreon"}}}); err != nil {
		t.Fatal(err)
	}
	x, _ := svc.db.getWebFixture(run.ID, "patreon")
	app := &App{svc: svc}

	bad := httptest.NewRecorder()
	app.handleFixture(bad, httptest.NewRequest(http.MethodGet, "/fixtures/run-route/patreon/wrong/", nil))
	if bad.Code != http.StatusNotFound {
		t.Fatalf("bad token status=%d", bad.Code)
	}

	page := httptest.NewRecorder()
	app.handleFixture(page, httptest.NewRequest(http.MethodGet, "/fixtures/run-route/patreon/"+x.Token+"/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Choose your membership") || !strings.Contains(page.Body.String(), "Creator dashboard") || !strings.Contains(page.Body.String(), "Video link") || !strings.Contains(page.Body.String(), "Request payout") {
		t.Fatalf("page status=%d body=%s", page.Code, page.Body.String())
	}

	body, _ := json.Marshal(map[string]any{"action": "select_tier", "input": map[string]any{"tier": "supporter"}})
	action := httptest.NewRecorder()
	app.handleFixture(action, httptest.NewRequest(http.MethodPost, "/fixtures/run-route/patreon/"+x.Token+"/api/action", bytes.NewReader(body)))
	if action.Code != http.StatusOK || !strings.Contains(action.Body.String(), "checkout.started") {
		t.Fatalf("action status=%d body=%s", action.Code, action.Body.String())
	}
	if err := svc.db.updateRun(run.ID, "stopped", ""); err != nil {
		t.Fatal(err)
	}
	stopped := httptest.NewRecorder()
	app.handleFixture(stopped, httptest.NewRequest(http.MethodGet, "/fixtures/run-route/patreon/"+x.Token+"/", nil))
	if stopped.Code != http.StatusNotFound {
		t.Fatalf("stopped fixture status=%d", stopped.Code)
	}
}

func TestFixtureStatusBlocksActions(t *testing.T) {
	svc := &service{db: testStore(t)}
	run := &Run{ID: "run-inactive-fixture", RuntimeID: "runtime-inactive-fixture", Kind: "test", Status: "running", StartedAt: time.Now().UTC()}
	if err := svc.db.createRun(run); err != nil {
		t.Fatal(err)
	}
	if err := svc.createWebFixtures(run, EnvironmentSpec{WebFixtures: []WebFixtureSpec{{ID: "patreon", Pack: "patreon"}}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.db.setWebFixturesStatus(run.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.applyFixtureAction(run.ID, "patreon", "follow", nil); err == nil {
		t.Fatal("action succeeded against a stopped fixture")
	}
}

func TestPatreonCreatorOperations(t *testing.T) {
	legacy := normalizePatreonState(WebFixtureSpec{Pack: "patreon"}, map[string]any{
		"posts": []any{
			map[string]any{"id": "legacy", "excerpt": "Old snapshot", "locked": true},
			map[string]any{"id": "post-2", "excerpt": "Unlocked by a membership snapshot", "locked": false},
		},
	})
	legacyPost := legacy["posts"].([]any)[0].(map[string]any)
	legacyKnownPost := legacy["posts"].([]any)[1].(map[string]any)
	if legacy["payouts"] == nil || legacy["members"] == nil || legacyPost["status"] != "published" || legacyPost["audience"] != "members" || legacyKnownPost["audience"] != "members" || legacyKnownPost["video_url"] != "https://vimeo.com/915402741" {
		t.Fatalf("legacy state was not normalized: %#v", legacy)
	}

	svc := &service{db: testStore(t)}
	run := &Run{ID: "run-creator", RuntimeID: "runtime-creator", Kind: "test", Status: "running", StartedAt: time.Now().UTC()}
	if err := svc.db.createRun(run); err != nil {
		t.Fatal(err)
	}
	if err := svc.createWebFixtures(run, EnvironmentSpec{WebFixtures: []WebFixtureSpec{{ID: "patreon", Pack: "patreon"}}}); err != nil {
		t.Fatal(err)
	}
	if err := svc.db.setWebFixturesStatus(run.ID, "running"); err != nil {
		t.Fatal(err)
	}

	postInput := map[string]any{
		"title":        "Director commentary",
		"body":         "The new cut is ready for members.",
		"video_url":    "https://vimeo.com/123456789",
		"audience":     "paid",
		"publish_mode": "schedule",
		"scheduled_at": "2026-07-20T18:30",
	}
	if _, event, err := svc.applyFixtureAction(run.ID, "patreon", "create_post", postInput); err != nil {
		t.Fatal(err)
	} else if event == nil || event.Type != "post.scheduled" || event.Data["video_url"] != postInput["video_url"] {
		t.Fatalf("post event=%#v", event)
	}
	if _, _, err := svc.applyFixtureAction(run.ID, "patreon", "create_post", map[string]any{"title": "Bad link", "body": "Body", "video_url": "javascript:alert(1)"}); err == nil {
		t.Fatal("accepted an unsafe video URL")
	}

	if _, event, err := svc.applyFixtureAction(run.ID, "patreon", "request_payout", map[string]any{"amount": 200.0}); err != nil {
		t.Fatal(err)
	} else if event == nil || event.Type != "payout.requested" || event.Data["amount"] != 200.0 {
		t.Fatalf("payout event=%#v", event)
	}
	if _, _, err := svc.applyFixtureAction(run.ID, "patreon", "request_payout", map[string]any{"amount": 5000.0}); err == nil {
		t.Fatal("accepted a payout above the available balance")
	}
	if _, event, err := svc.applyFixtureAction(run.ID, "patreon", "set_payout_method", map[string]any{"type": "paypal", "last_four": "7788"}); err != nil {
		t.Fatal(err)
	} else if event == nil || event.Type != "payout.method_updated" || event.Data["last_four"] != "7788" {
		t.Fatalf("payout method event=%#v", event)
	}

	if _, event, err := svc.applyFixtureAction(run.ID, "patreon", "send_member_message", map[string]any{"member_id": "member-maya", "message": "Yes, commentary is included."}); err != nil {
		t.Fatal(err)
	} else if event == nil || event.Type != "member_message.sent" || event.Data["member_id"] != "member-maya" {
		t.Fatalf("member message event=%#v", event)
	}

	detail, err := svc.fixtureDetail(run.ID, "patreon")
	if err != nil {
		t.Fatal(err)
	}
	state := detail["state"].(map[string]any)
	posts := state["posts"].([]any)
	created := posts[0].(map[string]any)
	if created["status"] != "scheduled" || created["scheduled_at"] != "2026-07-20T18:30" || created["video_url"] != "https://vimeo.com/123456789" {
		t.Fatalf("created post=%#v", created)
	}
	payouts := state["payouts"].(map[string]any)
	if payouts["available"] != 1048.75 || payouts["pending"] != 200.0 {
		t.Fatalf("payouts=%#v", payouts)
	}
	method := payouts["method"].(map[string]any)
	if method["type"] != "paypal" || method["last_four"] != "7788" {
		t.Fatalf("payout method=%#v", method)
	}
	threads := state["member_threads"].(map[string]any)
	thread := threads["member-maya"].([]any)
	lastMessage := thread[len(thread)-1].(map[string]any)
	if lastMessage["from"] != "creator" || lastMessage["body"] != "Yes, commentary is included." {
		t.Fatalf("thread=%#v", thread)
	}

	for _, assertion := range []Assertion{
		{Type: "web_event", Fixture: "patreon", EventType: "post.scheduled", Path: "video_url", Equals: "https://vimeo.com/123456789"},
		{Type: "web_event", Fixture: "patreon", EventType: "payout.requested", Path: "amount", Equals: 200.0},
		{Type: "web_event", Fixture: "patreon", EventType: "payout.method_updated", Path: "last_four", Equals: "7788"},
		{Type: "web_event", Fixture: "patreon", EventType: "member_message.sent", Path: "member_id", Equals: "member-maya"},
	} {
		result, err := svc.assert(run.RuntimeID, assertion)
		if err != nil || !result.Passed {
			t.Fatalf("assertion=%#v result=%#v err=%v", assertion, result, err)
		}
	}
}
