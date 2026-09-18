package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type auditPlatform struct {
	sdk.PlatformClient
	sdk.RuntimeClient
	live         map[string]sdk.RuntimeSummary
	destroyErr   error
	seedErr      error
	created      int
	destroyed    int
	telemetry    []sdk.RuntimeTelemetryEvent
	since        time.Time
	catalogCalls int
	mu           sync.Mutex
}

func (p *auditPlatform) ListRuntimes() ([]sdk.RuntimeSummary, error) {
	out := []sdk.RuntimeSummary{}
	for _, r := range p.live {
		out = append(out, r)
	}
	return out, nil
}
func (p *auditPlatform) CreateRuntime(req sdk.RuntimeCreateRequest) (*sdk.RuntimeSummary, error) {
	p.created++
	r := sdk.RuntimeSummary{ID: req.ID}
	p.live[r.ID] = r
	return &r, nil
}
func (p *auditPlatform) GetRuntime(id string) (*sdk.RuntimeSummary, error) {
	r, ok := p.live[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return &r, nil
}
func (p *auditPlatform) DestroyRuntime(id string) error {
	p.destroyed++
	if p.destroyErr != nil {
		return p.destroyErr
	}
	delete(p.live, id)
	return nil
}
func (p *auditPlatform) CallRuntimeAppResult(_, _, _ string, _ map[string]any, _ any) error {
	return p.seedErr
}
func (p *auditPlatform) ListRuntimeAgentTelemetry(_, _ string, since time.Time, _ int) ([]sdk.RuntimeTelemetryEvent, error) {
	p.since = since
	return p.telemetry, nil
}
func (p *auditPlatform) catalogHit() { p.mu.Lock(); p.catalogCalls++; p.mu.Unlock() }
func (p *auditPlatform) ListRuntimeCatalogApps(string) ([]sdk.RuntimeCatalogApp, error) {
	p.catalogHit()
	return []sdk.RuntimeCatalogApp{}, nil
}
func (p *auditPlatform) ListConnections(sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	p.catalogHit()
	return []sdk.PlatformConnection{}, nil
}
func (p *auditPlatform) ListRuntimeCatalogIntegrations() ([]sdk.RuntimeCatalogIntegration, error) {
	p.catalogHit()
	return []sdk.RuntimeCatalogIntegration{}, nil
}
func (p *auditPlatform) ListRuntimeCatalogManagedMCPServers(string) ([]sdk.RuntimeCatalogManagedMCPServer, error) {
	p.catalogHit()
	return []sdk.RuntimeCatalogManagedMCPServer{}, nil
}
func (p *auditPlatform) ListRuntimeCatalogAgents(string) ([]sdk.RuntimeCatalogAgent, error) {
	p.catalogHit()
	return []sdk.RuntimeCatalogAgent{}, nil
}
func (p *auditPlatform) ListRuntimeSnapshots() ([]sdk.RuntimeSnapshot, error) {
	p.catalogHit()
	return []sdk.RuntimeSnapshot{}, nil
}
func (p *auditPlatform) ListRuntimeRealtimeProviders(string) ([]sdk.RuntimeRealtimeProvider, error) {
	p.catalogHit()
	return []sdk.RuntimeRealtimeProvider{}, nil
}
func auditService(t *testing.T) (*service, *auditPlatform) {
	t.Helper()
	db := testStore(t)
	p := &auditPlatform{live: map[string]sdk.RuntimeSummary{}}
	ctx := sdk.NewAppCtxForTest(nil, db.db, nil, p, nil).WithProject("audit")
	return &service{db: db, ctx: ctx}, p
}
func TestReconcileFindsActiveRunBeyondHistoryLimit(t *testing.T) {
	s, p := auditService(t)
	if err := s.db.saveDefinition(&Definition{ID: "env", Name: "Old", DesiredState: "running", Spec: EnvironmentSpec{Version: 1}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 220; i++ {
		r := &Run{ID: fmt.Sprint("run", i), RuntimeID: fmt.Sprint("rt", i), Status: "stopped", StartedAt: time.Now().Add(time.Duration(i-300) * time.Minute)}
		if i == 0 {
			r.EnvironmentID = "env"
			r.Status = "running"
		}
		if err := s.db.createRun(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	old, _ := s.db.getRun("run0")
	definition, _ := s.db.getDefinition("env")
	if old.Status != "expired" || p.created != 0 || definition.ReconcileStatus != "retrying" || definition.ReconcileFailures != 1 {
		t.Fatalf("old=%+v created=%d", old, p.created)
	}
}

func TestReconcileBacksOffAndDegradesAfterPersistentFailure(t *testing.T) {
	t.Setenv("ENVIRONMENTS_RECONCILE_FAILURE_THRESHOLD", "3")
	s, p := auditService(t)
	p.seedErr = errors.New("seed is permanently broken")
	if err := s.db.saveDefinition(&Definition{ID: "env", Name: "Broken", DesiredState: "running", Spec: EnvironmentSpec{Version: 1, Seeds: []SeedStep{{App: "tasks", Tool: "create"}}}}); err != nil {
		t.Fatal(err)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		if err := s.reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		d, _ := s.db.getDefinition("env")
		if d.ReconcileFailures != attempt {
			t.Fatalf("attempt %d failures=%d", attempt, d.ReconcileFailures)
		}
		if attempt < 3 {
			if d.ReconcileStatus != "retrying" || d.ReconcileNextAt == nil {
				t.Fatalf("attempt %d state=%+v", attempt, d)
			}
			// An immediate worker tick is suppressed. Move the persisted deadline
			// into the past to exercise the next scheduled attempt.
			if err := s.reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			if p.created != attempt {
				t.Fatalf("backoff allowed attempt %d to run again", attempt)
			}
			_, _ = s.db.db.Exec(`UPDATE environment_definitions SET reconcile_next_at=? WHERE id='env'`, time.Now().Add(-time.Second).Format(time.RFC3339Nano))
		} else if d.ReconcileStatus != "degraded" || d.DegradedAt == nil || d.ReconcileNextAt != nil {
			t.Fatalf("terminal state=%+v", d)
		}
	}

	for i := 0; i < 4; i++ {
		if err := s.reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if p.created != 3 {
		t.Fatalf("degraded definition kept provisioning: created=%d", p.created)
	}
	var runs int
	if err := s.db.db.QueryRow(`SELECT COUNT(*) FROM environment_runs WHERE environment_id='env'`).Scan(&runs); err != nil || runs != 3 {
		t.Fatalf("runs=%d err=%v", runs, err)
	}
}

func TestDefinitionUpdateAndExplicitStartResetDegradedState(t *testing.T) {
	t.Setenv("ENVIRONMENTS_RECONCILE_FAILURE_THRESHOLD", "1")
	s, p := auditService(t)
	p.seedErr = errors.New("seed is permanently broken")
	if err := s.db.saveDefinition(&Definition{ID: "env", Name: "Broken", DesiredState: "running", Spec: EnvironmentSpec{Version: 1, Seeds: []SeedStep{{App: "tasks", Tool: "create"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	d, _ := s.db.getDefinition("env")
	if d.ReconcileStatus != "degraded" {
		t.Fatalf("state=%+v", d)
	}

	if _, err := s.updateDefinition("env", map[string]any{"name": "Updated"}); err != nil {
		t.Fatal(err)
	}
	d, _ = s.db.getDefinition("env")
	if d.ReconcileStatus != "healthy" || d.ReconcileFailures != 0 {
		t.Fatalf("update did not reset state: %+v", d)
	}
	if err := s.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.created != 2 {
		t.Fatalf("updated definition was not retried: created=%d", p.created)
	}

	if _, err := s.startDefinition("env"); err == nil {
		t.Fatal("explicit retry unexpectedly succeeded")
	}
	if p.created != 3 {
		t.Fatalf("explicit retry did not provision: created=%d", p.created)
	}
}

func TestReconcileBackoffAndThresholdConfiguration(t *testing.T) {
	if got := reconcileBackoff(1); got != 30*time.Second {
		t.Fatalf("first backoff=%v", got)
	}
	if got := reconcileBackoff(2); got != time.Minute {
		t.Fatalf("second backoff=%v", got)
	}
	if got := reconcileBackoff(20); got != reconcileBackoffMax {
		t.Fatalf("capped backoff=%v", got)
	}
	t.Setenv("ENVIRONMENTS_RECONCILE_FAILURE_THRESHOLD", "7")
	if got, err := reconcileFailureThreshold(); err != nil || got != 7 {
		t.Fatalf("threshold=%d err=%v", got, err)
	}
	t.Setenv("ENVIRONMENTS_RECONCILE_FAILURE_THRESHOLD", "0")
	if _, err := reconcileFailureThreshold(); err == nil {
		t.Fatal("invalid threshold accepted")
	}
}

func TestCleanupRetriesAreBackedOffAndBounded(t *testing.T) {
	t.Setenv("ENVIRONMENTS_RECONCILE_FAILURE_THRESHOLD", "3")
	s, p := auditService(t)
	if err := s.db.saveDefinition(&Definition{ID: "env", Name: "Cleanup", DesiredState: "running", Spec: EnvironmentSpec{Version: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.startDefinition("env"); err != nil {
		t.Fatal(err)
	}
	p.destroyErr = errors.New("runtime destroy unavailable")
	if err := s.stopDefinition("env"); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	if p.destroyed != 1 {
		t.Fatalf("destroy attempts=%d", p.destroyed)
	}
	if err := s.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.destroyed != 1 {
		t.Fatal("cleanup backoff was ignored")
	}
	for failures := 2; failures <= 3; failures++ {
		_, _ = s.db.db.Exec(`UPDATE environment_definitions SET reconcile_next_at=? WHERE id='env'`, time.Now().Add(-time.Second).Format(time.RFC3339Nano))
		if err := s.reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		d, _ := s.db.getDefinition("env")
		if d.ReconcileFailures != failures {
			t.Fatalf("cleanup failures=%d, want %d", d.ReconcileFailures, failures)
		}
	}
	d, _ := s.db.getDefinition("env")
	if d.ReconcileStatus != "degraded" || p.destroyed != 3 {
		t.Fatalf("state=%+v destroy attempts=%d", d, p.destroyed)
	}
	p.destroyErr = nil
	if err := s.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.destroyed != 3 {
		t.Fatal("degraded cleanup retried automatically")
	}
	if err := s.stopDefinition("env"); err != nil {
		t.Fatal(err)
	}
	if p.destroyed != 4 {
		t.Fatal("explicit stop did not retry cleanup")
	}
	d, _ = s.db.getDefinition("env")
	if d.ReconcileStatus != "healthy" || d.ActiveRun != nil {
		t.Fatalf("cleanup recovery state=%+v", d)
	}
}

func TestCleanupFailureRemainsActiveAndIsRetried(t *testing.T) {
	s, p := auditService(t)
	r, err := s.start("env", "eval", EnvironmentSpec{})
	if err != nil {
		t.Fatal(err)
	}
	p.destroyErr = errors.New("temporary unavailable")
	if s.stopRun(r) == nil {
		t.Fatal("expected cleanup error")
	}
	pending, _ := s.db.activeRun("env")
	if pending == nil || pending.Status != "stopping" {
		t.Fatalf("pending=%+v", pending)
	}
	if _, err = s.start("env", "eval", EnvironmentSpec{}); err != nil {
		t.Fatal(err)
	}
	if p.created != 1 {
		t.Fatal("duplicate runtime")
	}
	p.destroyErr = nil
	if err = s.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	pending, _ = s.db.activeRun("env")
	if pending != nil || len(p.live) != 0 {
		t.Fatal("cleanup not completed")
	}
}
func TestStartupRollbackRetriesCleanup(t *testing.T) {
	s, p := auditService(t)
	p.seedErr = errors.New("seed failed")
	p.destroyErr = errors.New("destroy unavailable")
	r, err := s.start("env", "eval", EnvironmentSpec{Seeds: []SeedStep{{App: "tasks", Tool: "create"}}})
	if err == nil || r.Status != "stopping" {
		t.Fatalf("run=%+v err=%v", r, err)
	}
	p.destroyErr = nil
	if err = s.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.live) != 0 {
		t.Fatal("runtime leaked")
	}
}
func TestPartialUpdatesPreserveDefinition(t *testing.T) {
	s, _ := auditService(t)
	_, err := s.saveDefinition(&Definition{ID: "env", Name: "Original", DesiredState: "running", Spec: EnvironmentSpec{Version: 1, TTLSeconds: 3600, AppInstallIDs: []int64{7}, Seeds: []SeedStep{{App: "tasks", Tool: "create"}}}})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{svc: s}
	_, err = app.toolSave(true)(nil, map[string]any{"id": "env", "name": "Renamed"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/environments/env", strings.NewReader(`{"spec":{"ttl_seconds":900}}`))
	w := httptest.NewRecorder()
	app.handleEnvironment(w, req)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	d, _ := s.db.getDefinition("env")
	if d.Name != "Renamed" || d.DesiredState != "running" || len(d.Spec.AppInstallIDs) != 1 || len(d.Spec.Seeds) != 1 || d.Spec.TTLSeconds != 900 {
		t.Fatalf("definition lost fields: %+v", d)
	}
	if _, err = s.updateDefinition("missing", map[string]any{"name": "x"}); err == nil {
		t.Fatal("updated missing definition")
	}
}
func TestMCPAssertionRejectsUnrelatedCalls(t *testing.T) {
	s, p := auditService(t)
	p.telemetry = []sdk.RuntimeTelemetryEvent{{Type: "tool.call", Data: json.RawMessage(`{"name":"unrelated_search"}`)}}
	for _, tool := range []string{"", "search"} {
		r, err := s.assert("rt", Assertion{Type: "mcp_tool_call", MCP: "wanted", Tool: tool})
		if err != nil || r.Passed {
			t.Fatalf("result=%+v err=%v", r, err)
		}
	}
	p.telemetry[0].Data = json.RawMessage(`{"name":"wanted_search"}`)
	r, err := s.assert("rt", Assertion{Type: "mcp_tool_call", MCP: "wanted", Tool: "search"})
	if err != nil || !r.Passed {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	p.telemetry = make([]sdk.RuntimeTelemetryEvent, 1000)
	if _, err = s.assert("rt", Assertion{Type: "telemetry", AgentAlias: "main"}); !errors.Is(err, errTelemetryIncomplete) {
		t.Fatalf("err=%v", err)
	}
}
func TestTelemetryHistoryRetainsAndDeduplicates(t *testing.T) {
	p := &auditPlatform{}
	h := newTelemetryHistory(p)
	now := time.Now()
	for batch := 0; batch < 3; batch++ {
		p.telemetry = nil
		for i := 0; i < 600; i++ {
			n := batch*500 + i
			p.telemetry = append(p.telemetry, sdk.RuntimeTelemetryEvent{ID: fmt.Sprint(n), Time: now.Add(time.Duration(n) * time.Millisecond)})
		}
		events, err := h.list("rt", "main", now, 500)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != batch*500+600 {
			t.Fatalf("events=%d", len(events))
		}
	}
	if !p.since.After(now) {
		t.Fatal("cursor did not advance")
	}
	p.telemetry = make([]sdk.RuntimeTelemetryEvent, 1000)
	if _, err := h.list("rt", "main", now, 1000); !errors.Is(err, errTelemetryIncomplete) {
		t.Fatal(err)
	}
	p.telemetry = nil
	if _, err := h.list("rt", "main", now, 1000); !errors.Is(err, errTelemetryIncomplete) {
		t.Fatal("gap was forgotten")
	}
}
func TestCatalogHTTPAndMCPAgreeAndCache(t *testing.T) {
	s, p := auditService(t)
	app := &App{svc: s}
	w := httptest.NewRecorder()
	app.handleCatalog(w, httptest.NewRequest("GET", "/api/catalog", nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var httpValue map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &httpValue); err != nil {
		t.Fatal(err)
	}
	value, err := app.toolCatalog(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(value)
	var mcpValue map[string]any
	_ = json.Unmarshal(raw, &mcpValue)
	for _, key := range []string{"protocol_fixtures", "assertion_types", "apps", "connections", "snapshots"} {
		if httpValue[key] == nil || mcpValue[key] == nil {
			t.Fatal("missing", key)
		}
	}
	if p.catalogCalls != 7 {
		t.Fatalf("calls=%d", p.catalogCalls)
	}
}
func TestCarrierCorrelatesDestinationAndReportsRejectedCallbacks(t *testing.T) {
	var active carrierActiveCalls
	_ = json.Unmarshal([]byte(`{"calls":[{"thread_id":"other","to":"+111"},{"thread_id":"correct","to":"+222"}]}`), &active)
	thread, err := carrierTargetThread(active, "+222")
	if err != nil || thread != "correct" {
		t.Fatalf("thread=%s err=%v", thread, err)
	}
	if _, err = carrierTargetThread(active, "+333"); err == nil {
		t.Fatal("matched unrelated call")
	}
	s, _ := auditService(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "rejected", 403) }))
	defer server.Close()
	endpoint := &sdk.RuntimeAppEndpoint{PlatformURL: "https://runtime.invalid/rt", GatewayURL: server.URL + "/rt"}
	err = s.deliverCarrierCallback(context.Background(), "run", "carrier", "call", "callback.call_completed", "https://runtime.invalid/rt/status", endpoint, url.Values{"CallSid": {"CA1"}}, "secret")
	if err == nil {
		t.Fatal("rejection hidden")
	}
	events, _ := s.db.listProtocolEvents("run", "carrier", "call")
	if len(events) != 1 || events[0].Type != "callback.call_completed.failed" || events[0].Data["delivered"] != false {
		t.Fatalf("events=%+v", events)
	}
}
func TestRetentionPreservesActiveRunsAndSnapshots(t *testing.T) {
	s, _ := auditService(t)
	t.Setenv("APTEVA_DATA_DIR", t.TempDir())
	old := time.Now().Add(-72 * time.Hour)
	for _, id := range []string{"old", "active", "recent", "pending"} {
		r := &Run{ID: id, RuntimeID: "rt-" + id, Status: "running", StartedAt: old}
		if err := s.db.createRun(r); err != nil {
			t.Fatal(err)
		}
		if id != "active" {
			_ = s.db.updateRun(id, "stopped", "")
			if id != "recent" {
				_, _ = s.db.db.Exec(`UPDATE environment_runs SET stopped_at=? WHERE id=?`, old.Format(time.RFC3339Nano), id)
			}
		}
	}
	_ = s.db.saveVoiceCall(&VoiceCall{ID: "voice_old", RunID: "old", Status: "completed", StartedAt: old})
	_ = s.db.saveVoiceCall(&VoiceCall{ID: "voice_pending", RunID: "pending", Status: "running", StartedAt: old})
	if err := s.writeVoiceRecording("voice_old", "caller", []byte{0, 0}); err != nil {
		t.Fatal(err)
	}
	_ = s.db.saveSnapshot(Snapshot{ID: "snap", CreatedAt: old})
	if err := s.pruneBefore(time.Now().Add(-24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.db.getRun("old"); r != nil {
		t.Fatal("old run retained")
	}
	for _, id := range []string{"active", "recent", "pending"} {
		if r, _ := s.db.getRun(id); r == nil {
			t.Fatal("removed", id)
		}
	}
	path, _ := s.voiceRecordingPath("voice_old", "caller")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("recording retained")
	}
	var count int
	_ = s.db.db.QueryRow(`SELECT count(*) FROM environment_snapshots`).Scan(&count)
	if count != 1 {
		t.Fatal("snapshot removed")
	}
}

func TestBatchDecorationWorksWithOneConnectionAndManyRuns(t *testing.T) {
	s, _ := auditService(t)
	s.db.db.SetMaxOpenConns(1)
	runs := make([]*Run, 205)
	for i := range runs {
		runs[i] = &Run{ID: fmt.Sprint("run", i)}
		if err := s.db.createWebFixture(&WebFixtureInstance{RunID: runs[i].ID, ID: "web", Pack: "patreon", Token: fmt.Sprint("token", i), State: map[string]any{"index": i}}); err != nil {
			t.Fatal(err)
		}
		if err := s.db.createProtocolFixture(&ProtocolFixtureInstance{RunID: runs[i].ID, ID: "carrier", Pack: "telephony-carrier"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.decorateRuns(runs); err != nil {
		t.Fatal(err)
	}
	for i, run := range runs {
		if len(run.WebFixtures) != 1 || len(run.ProtocolFixtures) != 1 || run.WebFixtures[0].State["index"] != float64(i) {
			t.Fatalf("wrong fixture association: %+v", run)
		}
	}
}
func TestEmptyStopCannotStopEveryEnvironment(t *testing.T) {
	s, p := auditService(t)
	if err := s.stopDefinition(""); err == nil {
		t.Fatal("empty id accepted")
	}
	if p.destroyed != 0 {
		t.Fatal("destroyed a runtime")
	}
}
func TestFixtureCreationFailureFinalizesRun(t *testing.T) {
	s, _ := auditService(t)
	// Fail before CreateRuntime to verify rollback is registered before fixtures.
	_, err := s.db.db.Exec(`CREATE TRIGGER fail_fixture BEFORE INSERT ON environment_web_fixtures BEGIN SELECT RAISE(FAIL, 'injected fixture failure'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.start("env", "eval", EnvironmentSpec{WebFixtures: []WebFixtureSpec{{ID: "web", Pack: "patreon"}}})
	if err == nil || run.Status != "failed" {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	if active, _ := s.db.activeRun("env"); active != nil {
		t.Fatal("run stuck starting")
	}
}
func TestVoiceRejectsStoppedRun(t *testing.T) {
	s, _ := auditService(t)
	run := &Run{ID: "old", RuntimeID: "rt", Status: "running", StartedAt: time.Now()}
	_ = s.db.createRun(run)
	_ = s.db.updateRun(run.ID, "stopped", "")
	if _, err := s.runVoiceCall(context.Background(), run, VoiceFixtureSpec{CallerGoal: "hello"}); err == nil {
		t.Fatal("voice accepted stale active state")
	}
}
