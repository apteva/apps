package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// mustCreateEventWorkflow inserts a workflow with TriggerKind +
// TriggerJSON populated from the parsed source. mustCreateWorkflow
// in runner_test.go skips those fields (it doesn't go through
// buildAndCreateWorkflow), which is fine for runner tests but
// invisible to the event-trigger reconcile that filters on
// trigger_kind='event'.
func mustCreateEventWorkflow(t *testing.T, ctx *sdk.AppCtx, src string) *Workflow {
	t.Helper()
	def, err := ParseDefinition([]byte(src))
	if err != nil {
		t.Fatalf("ParseDefinition: %v", err)
	}
	trig, _ := json.Marshal(def.Trigger)
	wf, err := dbCreateWorkflow(ctx.AppDB(), testProj, &Workflow{
		Name:        "wf-" + randName(t),
		SourceKind:  "inline",
		Source:      src,
		SourceHash:  hashSource([]byte(src)),
		TriggerKind: def.Trigger.Kind,
		TriggerJSON: string(trig),
	})
	if err != nil {
		t.Fatalf("dbCreateWorkflow: %v", err)
	}
	return wf
}

// TestEventTrigger_FullRoundtrip: drive one SSE event through a
// stubbed app-events endpoint into the trigger manager and confirm
// the matching workflow dispatches a run with the event payload as
// input. End-to-end coverage of the lane → dispatch → RunWorkflow
// path that makes "tables row inserted → run a workflow" actually
// work in the wild.
func TestEventTrigger_FullRoundtrip(t *testing.T) {
	var sawAuth, sawProject string
	streamReady := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/app-events/") {
			http.NotFound(w, r)
			return
		}
		sawAuth = r.Header.Get("Authorization")
		sawProject = r.URL.Query().Get("project_id")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		// First event after the connection is up.
		fmt.Fprintf(w,
			"id: 1\ndata: {\"topic\":\"row.inserted\",\"app\":\"tables\","+
				"\"project_id\":\"%s\",\"seq\":1,"+
				"\"data\":{\"id\":7,\"email\":\"marco@example.com\"}}\n\n",
			testProj)
		flusher.Flush()
		close(streamReady)
		// Hold the connection open briefly so the lane goroutine
		// gets a chance to dispatch before we tear down.
		select {
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_APP_TOKEN", "test-token")

	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))

	// Workflow with an event trigger + one self-contained step
	// (kind=emit — publishes a workflow event, no cross-app call,
	// so the test doesn't need a platform stub).
	src := `name: on-row-inserted
trigger:
  kind: event
  source: tables
  topic: row.inserted
steps:
  - id: ack
    kind: emit
    topic: workflow.tables.row.received
    data:
      id: "{{ input.data.id }}"
      email: "{{ input.data.email }}"
`
	wf := mustCreateEventWorkflow(t, ctx, src)

	mgr := newEventTrigger(ctx)
	mgr.Start()
	defer mgr.Stop()

	// Wait for the stub to flush the first event.
	select {
	case <-streamReady:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE stream never opened")
	}

	// Poll the runs table for the dispatched run. Bounded wait.
	deadline := time.Now().Add(3 * time.Second)
	var runs []*Run
	for time.Now().Before(deadline) {
		out, err := dbListRuns(ctx.AppDB(), testProj, wf.ID, 10)
		if err == nil && len(out) > 0 {
			runs = out
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(runs) == 0 {
		t.Fatalf("workflow never ran (auth=%q project=%q)", sawAuth, sawProject)
	}
	run := runs[0]
	if run.TriggerKind != "event" {
		t.Errorf("trigger_kind = %q, want event", run.TriggerKind)
	}
	if !strings.Contains(run.InputJSON, "row.inserted") || !strings.Contains(run.InputJSON, "marco@example.com") {
		t.Errorf("input did not carry the event payload: %s", run.InputJSON)
	}
	if sawAuth != "Bearer test-token" {
		t.Errorf("sidecar token not on the wire; got %q", sawAuth)
	}
	if sawProject != testProj {
		t.Errorf("project_id not on the wire; got %q want %q", sawProject, testProj)
	}
}

// TestEventTrigger_NoMatch_NoRun: an event whose topic doesn't match
// the workflow's trigger.topic is silently dropped — no run row.
func TestEventTrigger_NoMatch_NoRun(t *testing.T) {
	streamReady := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprintf(w,
			"id: 1\ndata: {\"topic\":\"row.deleted\",\"app\":\"tables\","+
				"\"project_id\":\"%s\",\"seq\":1,\"data\":{\"id\":1}}\n\n",
			testProj)
		flusher.Flush()
		close(streamReady)
		select {
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()

	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_APP_TOKEN", "test-token")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))

	src := `name: only-on-insert
trigger:
  kind: event
  source: tables
  topic: row.inserted
steps:
  - id: ack
    kind: emit
    topic: ack
    data: {ok: true}
`
	wf := mustCreateEventWorkflow(t, ctx, src)

	mgr := newEventTrigger(ctx)
	mgr.Start()
	defer mgr.Stop()

	<-streamReady
	time.Sleep(300 * time.Millisecond) // give dispatch a moment to NOT fire

	runs, _ := dbListRuns(ctx.AppDB(), testProj, wf.ID, 10)
	if len(runs) != 0 {
		t.Errorf("non-matching topic ran the workflow: %d runs", len(runs))
	}
}

func TestEventTrigger_MissingConfigurationIsDegraded(t *testing.T) {
	t.Setenv("APTEVA_GATEWAY_URL", "")
	t.Setenv("APTEVA_APP_TOKEN", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))

	mgr := newEventTrigger(ctx)
	mgr.reconcile()
	status := mgr.Status()
	if status.Healthy || status.State != "degraded" {
		t.Fatalf("status = %+v, want degraded and unhealthy", status)
	}
	if status.Configured {
		t.Fatal("subscriber unexpectedly configured")
	}
	if got := strings.Join(status.MissingConfig, ","); !strings.Contains(got, "APTEVA_GATEWAY_URL") ||
		!strings.Contains(got, "APTEVA_APP_TOKEN") {
		t.Fatalf("missing config = %q", got)
	}

	previous := globalEventTrigger
	globalEventTrigger = mgr
	defer func() { globalEventTrigger = previous }()
	rec := httptest.NewRecorder()
	(&App{}).handleSubscriberHealth(rec, httptest.NewRequest(http.MethodGet, "/subscriber/health", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("health status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEventTrigger_ConfiguredIdleIsHealthy(t *testing.T) {
	t.Setenv("APTEVA_GATEWAY_URL", "http://gateway.invalid")
	t.Setenv("APTEVA_APP_TOKEN", "test-token")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	mgr := newEventTrigger(ctx)
	mgr.reconcile()
	status := mgr.Status()
	if !status.Healthy || status.State != "idle" {
		t.Fatalf("status = %+v, want healthy idle", status)
	}
}

func TestEventTrigger_AuthenticationFailureIsDiagnosable(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "invalid app token", http.StatusUnauthorized)
	}))
	defer srv.Close()

	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_APP_TOKEN", "bad-token")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	mustCreateEventWorkflow(t, ctx, `name: auth-failure
trigger:
  kind: event
  source: tables
  topic: row.inserted
steps:
  - id: ack
    kind: emit
    topic: ack
`)

	mgr := newEventTrigger(ctx)
	mgr.reconnectDelay = 10 * time.Millisecond
	mgr.Start()
	defer mgr.Stop()

	waitForEventStatus(t, mgr, func(status eventSubscriberStatus) bool {
		return len(status.Lanes) == 1 &&
			strings.Contains(status.Lanes[0].LastError, "status 401") &&
			status.Lanes[0].ReconnectCount > 0
	})
	status := mgr.Status()
	if status.Healthy || status.State != "reconnecting" {
		t.Fatalf("status = %+v, want reconnecting and unhealthy", status)
	}
	if requests.Load() == 0 {
		t.Fatal("subscriber never attempted authentication")
	}
}

func TestEventTrigger_ReconnectsWithCursorAndDelivers(t *testing.T) {
	var connections atomic.Int32
	secondDelivered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := connections.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		switch n {
		case 1:
			fmt.Fprintf(w,
				"id: 1\ndata: {\"topic\":\"row.inserted\",\"app\":\"tables\","+
					"\"project_id\":\"%s\",\"seq\":1,\"data\":{\"id\":1}}\n\n",
				testProj)
			flusher.Flush()
		case 2:
			if got := r.URL.Query().Get("since"); got != "1" {
				http.Error(w, "missing reconnect cursor: "+got, http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w,
				"id: 2\ndata: {\"topic\":\"row.inserted\",\"app\":\"tables\","+
					"\"project_id\":\"%s\",\"seq\":2,\"data\":{\"id\":2}}\n\n",
				testProj)
			flusher.Flush()
			close(secondDelivered)
			<-r.Context().Done()
		default:
			<-r.Context().Done()
		}
	}))
	defer srv.Close()

	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_APP_TOKEN", "test-token")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	wf := mustCreateEventWorkflow(t, ctx, `name: reconnect
trigger:
  kind: event
  source: tables
  topic: row.inserted
steps:
  - id: ack
    kind: emit
    topic: ack
`)

	mgr := newEventTrigger(ctx)
	mgr.reconnectDelay = 10 * time.Millisecond
	mgr.Start()
	defer mgr.Stop()

	select {
	case <-secondDelivered:
	case <-time.After(3 * time.Second):
		t.Fatal("subscriber did not reconnect and deliver the second event")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := dbListRuns(ctx.AppDB(), testProj, wf.ID, 10)
		if err == nil && len(runs) == 2 {
			status := mgr.Status()
			if len(status.Lanes) != 1 || status.Lanes[0].LastEventSeq != 2 {
				t.Fatalf("cursor diagnostics = %+v, want seq 2", status)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("both reconnected events were not delivered as workflow runs")
}

func waitForEventStatus(t *testing.T, mgr *eventTrigger, predicate func(eventSubscriberStatus) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if predicate(mgr.Status()) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("subscriber status condition not reached: %+v", mgr.Status())
}

// TestTopicMatches: cheap unit coverage on the pattern matcher.
func TestTopicMatches(t *testing.T) {
	cases := []struct {
		pattern, topic string
		want           bool
	}{
		{"row.inserted", "row.inserted", true},
		{"row.inserted", "row.deleted", false},
		{"row.*", "row.inserted", true},
		{"row.*", "row.deleted", true},
		{"row.*", "table.created", false},
		{"*", "anything.here", true},
		{"", "any", true},
	}
	for _, c := range cases {
		if got := topicMatches(c.pattern, c.topic); got != c.want {
			t.Errorf("topicMatches(%q, %q) = %v, want %v", c.pattern, c.topic, got, c.want)
		}
	}
}
