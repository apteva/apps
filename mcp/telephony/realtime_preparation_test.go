package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type preparationPlatform struct {
	*answerPlatform
	mu             sync.Mutex
	entered        chan sdk.RealtimeSpawnRequest
	gate           chan struct{}
	failure        error
	requests       []sdk.RealtimeSpawnRequest
	killedThreads  []string
	carrierAnswers int
}

func (p *preparationPlatform) SpawnRealtimeThread(req sdk.RealtimeSpawnRequest) (*sdk.RealtimeSpawnResult, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	failure := p.failure
	p.mu.Unlock()
	p.entered <- req
	<-p.gate
	if failure != nil {
		return nil, failure
	}
	return &sdk.RealtimeSpawnResult{ThreadID: req.ThreadID, AudioBridgeURL: "wss://bridge.test/audio"}, nil
}
func (p *preparationPlatform) KillThread(_ int64, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.killedThreads = append(p.killedThreads, id)
	return nil
}
func (p *preparationPlatform) ExecuteIntegrationTool(_ int64, _ string, _ map[string]any) (*sdk.ExecuteResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.carrierAnswers++
	return &sdk.ExecuteResult{Success: true, Status: 200}, nil
}
func (p *preparationPlatform) counts() (int, int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests), len(p.killedThreads), p.carrierAnswers
}

func preparationFixture(t *testing.T, routed bool) (*App, *sdk.AppCtx, *preparationPlatform, *routeRow, *callRow, func()) {
	t.Helper()
	p := &preparationPlatform{answerPlatform: &answerPlatform{}, entered: make(chan sdk.RealtimeSpawnRequest, 20), gate: make(chan struct{})}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(p))
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	a := &App{installID: 42}
	a.preparations.wait = time.Second
	route := &routeRow{ID: "route-preparation", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 9, PhoneNumber: "+14155550101", AgentID: 7, Enabled: true, TimeoutSec: 60, AnswerMode: answerModeRealtimeImmediate, AutoDirective: "Help the caller.", AutoGreeting: "Hello.", Secret: "route-secret"}
	if err := a.db().insertRoute(*route); err != nil {
		t.Fatal(err)
	}
	if routed {
		if err := a.ensureLegacyRoutingFlows(ctx); err != nil {
			t.Fatal(err)
		}
		route, _ = a.db().findRoute(route.ID)
	}
	row, _, err := a.recordInboundCall(route, "CA-preparation", "+14155550102", route.PhoneNumber)
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	unblock := func() { once.Do(func() { close(p.gate) }) }
	t.Cleanup(func() {
		unblock()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			a.preparations.mu.Lock()
			n := len(a.preparations.active)
			a.preparations.mu.Unlock()
			if n == 0 {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Error("answer workers did not finish")
	})
	return a, ctx, p, route, row, unblock
}
func preparationWebhook(t *testing.T, a *App, route *routeRow) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"CallSid": {"CA-preparation"}, "From": {"+14155550102"}, "To": {route.PhoneNumber}}
	req := httptest.NewRequest("POST", strings.TrimPrefix(a.inboundRouteURL(*route), a.publicAppURL()), strings.NewReader(form.Encode()))
	signTwilioTestRequest(t, a, req, form)
	rec := httptest.NewRecorder()
	a.handleTwilioInbound(rec, req)
	return rec
}
func waitPreparationEntered(t *testing.T, p *preparationPlatform) sdk.RealtimeSpawnRequest {
	t.Helper()
	select {
	case req := <-p.entered:
		return req
	case <-time.After(3 * time.Second):
		t.Fatal("spawn did not start")
		return sdk.RealtimeSpawnRequest{}
	}
}
func waitPreparationResult(t *testing.T, ch <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("webhook did not finish")
		return nil
	}
}

func TestRealtimePreparationConcurrentWebhooksReuseWinner(t *testing.T) {
	for _, routed := range []bool{false, true} {
		t.Run(map[bool]string{false: "immediate", true: "routing-flow"}[routed], func(t *testing.T) {
			a, _, p, route, row, unblock := preparationFixture(t, routed)
			responses := make(chan *httptest.ResponseRecorder, 2)
			go func() { responses <- preparationWebhook(t, a, route) }()
			winner := waitPreparationEntered(t, p)
			stored, _ := a.db().findCall(row.ID)
			if stored.Status != "answering" || realtimePreparationReady(stored) {
				t.Fatalf("not in claimed intermediate state: %+v", stored)
			}
			go func() { responses <- preparationWebhook(t, a, route) }()
			select {
			case response := <-responses:
				t.Fatalf("concurrent request failed before winner completed: %d %s", response.Code, response.Body.String())
			case <-time.After(50 * time.Millisecond):
			}
			unblock()
			for range 2 {
				rec := waitPreparationResult(t, responses)
				if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<Connect><Stream") || strings.Contains(rec.Body.String(), "<Hangup") {
					t.Fatalf("response=%d %s", rec.Code, rec.Body.String())
				}
			}
			stored, _ = a.db().findCall(row.ID)
			if stored.Status != "answered" || stored.ThreadID != winner.ThreadID {
				t.Fatalf("wrong winning call: %+v", stored)
			}
			if spawned, killed, _ := p.counts(); spawned != 1 || killed != 0 {
				t.Fatalf("spawned=%d killed=%d", spawned, killed)
			}
			if winner.CapabilityMode != sdk.RealtimeCapabilitiesInheritAgent {
				t.Fatal("agent capability inheritance changed")
			}
		})
	}
}

func TestRealtimePreparationSlowWebhookWaitsWithoutHangupAndResumes(t *testing.T) {
	for _, routed := range []bool{false, true} {
		t.Run(map[bool]string{false: "immediate", true: "routing-flow"}[routed], func(t *testing.T) {
			a, _, p, route, row, unblock := preparationFixture(t, routed)
			a.preparations.wait = 60 * time.Millisecond
			responses := make(chan *httptest.ResponseRecorder, 1)
			go func() { responses <- preparationWebhook(t, a, route) }()
			waitPreparationEntered(t, p)
			rec := waitPreparationResult(t, responses)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<Redirect") || strings.Contains(rec.Body.String(), "<Hangup") || strings.Contains(rec.Body.String(), "<Say") {
				t.Fatalf("timeout response=%d %s", rec.Code, rec.Body.String())
			}
			stored, _ := a.db().findCall(row.ID)
			if stored.Status != "answering" {
				t.Fatalf("timeout changed status to %s", stored.Status)
			}
			unblock()
			// Follow the actual wait endpoint until the shared preparation is attached.
			deadline := time.Now().Add(2 * time.Second)
			for {
				form := url.Values{"CallSid": {row.CarrierSID}, "From": {row.FromNumber}, "To": {route.PhoneNumber}}
				req := httptest.NewRequest("POST", a.twilioWaitURL(*route, row.ID), strings.NewReader(form.Encode()))
				signTwilioTestRequest(t, a, req, form)
				rec = httptest.NewRecorder()
				a.handleTwilioInboundWait(rec, req, route.ID)
				if strings.Contains(rec.Body.String(), "<Connect><Stream") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("wait never resumed: %d %s", rec.Code, rec.Body.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			if spawned, killed, _ := p.counts(); spawned != 1 || killed != 0 {
				t.Fatalf("spawned=%d killed=%d", spawned, killed)
			}
		})
	}
}

func TestRealtimePreparationFailedSpawnReleasesOnlyItsClaim(t *testing.T) {
	a, ctx, p, route, row, unblock := preparationFixture(t, true)
	p.failure = errors.New("Core spawn unavailable")
	responses := make(chan *httptest.ResponseRecorder, 2)
	go func() { responses <- preparationWebhook(t, a, route) }()
	first := waitPreparationEntered(t, p)
	go func() { responses <- preparationWebhook(t, a, route) }()
	time.Sleep(30 * time.Millisecond)
	unblock()
	for range 2 {
		rec := waitPreparationResult(t, responses)
		if strings.Contains(rec.Body.String(), "<Hangup") || rec.Code != 200 {
			t.Fatalf("failed spawn killed caller: %d %s", rec.Code, rec.Body.String())
		}
	}
	current, _ := a.db().findCall(row.ID)
	if current.Status != "pending" {
		t.Fatalf("failed spawn left %s", current.Status)
	}
	p.mu.Lock()
	p.failure = nil
	p.mu.Unlock()
	thread, err := a.prepareInboundRealtime(ctx, current, "Retry.", "", "")
	if err != nil || thread == first.ThreadID {
		t.Fatalf("retry thread=%s err=%v", thread, err)
	}
	if err := a.db().releaseRealtimePreparation(row.ID, "pending-"+strings.TrimPrefix(first.ThreadID, "tel-")); err != nil {
		t.Fatal(err)
	}
	current, _ = a.db().findCall(row.ID)
	if current.Status != "answering" || current.ThreadID != thread {
		t.Fatalf("old release changed new claim: %+v", current)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) != 2 || len(p.killedThreads) != 1 || p.killedThreads[0] != first.ThreadID {
		t.Fatalf("spawns=%v kills=%v", p.requests, p.killedThreads)
	}
}

func TestRealtimePreparationInterruptedRequestDoesNotCancelWinner(t *testing.T) {
	a, ctx, p, _, row, unblock := preparationFixture(t, false)
	request, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	copy := *row
	go func() { _, err := a.prepareInboundRealtime(ctx, &copy, "Help.", "", "", request); result <- err }()
	winner := waitPreparationEntered(t, p)
	cancel()
	if err := <-result; !errors.Is(err, errAnswerPreparationInProgress) {
		t.Fatalf("cancel=%v", err)
	}
	current, _ := a.db().findCall(row.ID)
	if current.Status != "answering" {
		t.Fatal(current.Status)
	}
	unblock()
	thread, err := a.prepareInboundRealtime(ctx, current, "Help.", "", "")
	if err != nil || thread != winner.ThreadID {
		t.Fatalf("resume=%s %v", thread, err)
	}
	if spawned, killed, _ := p.counts(); spawned != 1 || killed != 0 {
		t.Fatalf("spawned=%d killed=%d", spawned, killed)
	}
}

func TestRealtimePreparationInterruptedPersistedClaimIsNeverTakenOver(t *testing.T) {
	a, ctx, p, _, row, _ := preparationFixture(t, false)
	a.preparations.wait = 60 * time.Millisecond
	if _, err := a.db().db.Exec(`UPDATE calls SET status='answering' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	current, _ := a.db().findCall(row.ID)
	_, err := a.prepareInboundRealtime(ctx, current, "Help.", "", "")
	if !errors.Is(err, errAnswerPreparationInProgress) {
		t.Fatalf("incomplete claim=%v", err)
	}
	current, _ = a.db().findCall(row.ID)
	if current.Status != "answering" || current.ThreadID != row.ThreadID {
		t.Fatalf("foreign claim changed: %+v", current)
	}
	if spawned, killed, _ := p.counts(); spawned != 0 || killed != 0 {
		t.Fatalf("spawned=%d killed=%d", spawned, killed)
	}
}

func TestRealtimePreparationCallerHangupDuringSpawnCleansOnlyLateThread(t *testing.T) {
	a, _, p, route, row, unblock := preparationFixture(t, true)
	responses := make(chan *httptest.ResponseRecorder, 1)
	go func() { responses <- preparationWebhook(t, a, route) }()
	winner := waitPreparationEntered(t, p)
	form := url.Values{"CallSid": {row.CarrierSID}, "CallStatus": {"completed"}, "To": {route.PhoneNumber}}
	endpoint := a.inboundRouteURL(*route) + "&fixture=status"
	req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	signTwilioTestRequest(t, a, req, form)
	rec := httptest.NewRecorder()
	a.handleTwilioInboundStatus(rec, req, route.ID)
	if rec.Code >= 400 {
		t.Fatalf("hangup callback=%d %s", rec.Code, rec.Body.String())
	}
	unblock()
	rec = waitPreparationResult(t, responses)
	if strings.Contains(rec.Body.String(), "<Stream") {
		t.Fatalf("ended call received bridge: %s", rec.Body.String())
	}
	current, _ := a.db().findCall(row.ID)
	if current.Status != "completed" {
		t.Fatalf("hangup overwritten: %s", current.Status)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.killedThreads) != 1 || p.killedThreads[0] != winner.ThreadID {
		t.Fatalf("late thread not cleaned: %v", p.killedThreads)
	}
}

func TestRealtimePreparationConcurrentAnswerToolsDoNotDuplicateCarrierAnswer(t *testing.T) {
	a, ctx, p, _, row, unblock := preparationFixture(t, false)
	results := make(chan error, 2)
	for range 2 {
		copy := *row
		go func() { _, err := a.answerCall(ctx, &copy, "Help.", "", "", false); results <- err }()
	}
	waitPreparationEntered(t, p)
	unblock()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if spawned, killed, answered := p.counts(); spawned != 1 || killed != 0 || answered != 1 {
		t.Fatalf("spawned=%d killed=%d carrier answers=%d", spawned, killed, answered)
	}
}

func TestRealtimePreparationLateFailureCannotReleaseReplacement(t *testing.T) {
	a, ctx, p, _, row, unblock := preparationFixture(t, false)
	p.failure = errors.New("late spawn failure")
	result := make(chan error, 1)
	copy := *row
	go func() { _, err := a.prepareInboundRealtime(ctx, &copy, "Help.", "", ""); result <- err }()
	first := waitPreparationEntered(t, p)
	if _, err := a.db().db.Exec(`UPDATE calls SET thread_id='replacement-thread',audio_bridge_url='wss://replacement.test/audio' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-result; err == nil {
		t.Fatal("spawn failure was ignored")
	}
	current, _ := a.db().findCall(row.ID)
	if current.Status != "answering" || current.ThreadID != "replacement-thread" || current.AudioBridgeURL != "wss://replacement.test/audio" {
		t.Fatalf("late cleanup damaged replacement: %+v", current)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.killedThreads) != 1 || p.killedThreads[0] != first.ThreadID {
		t.Fatalf("wrong thread cleaned: %v", p.killedThreads)
	}
}

func TestRealtimePreparationPlivoWaitResumesPreparedCall(t *testing.T) {
	a, ctx, p, route, row, unblock := preparationFixture(t, false)
	a.preparations.wait = 60 * time.Millisecond
	if _, err := a.db().db.Exec(`UPDATE calls SET carrier_slug='plivo' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	row.CarrierSlug = "plivo"
	route.CarrierSlug = "plivo"
	copy := *row
	result := make(chan error, 1)
	go func() { _, err := a.prepareInboundRealtime(ctx, &copy, "Help.", "", ""); result <- err }()
	waitPreparationEntered(t, p)
	if err := <-result; !errors.Is(err, errAnswerPreparationInProgress) {
		t.Fatal(err)
	}
	unblock()
	deadline := time.Now().Add(time.Second)
	for {
		req := httptest.NewRequest("POST", a.plivoWaitURL(*route, row.ID), strings.NewReader(url.Values{"CallUUID": {row.CarrierSID}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		a.handlePlivoInboundWait(rec, req, route)
		if strings.Contains(rec.Body.String(), "<Stream") {
			break
		}
		if strings.Contains(rec.Body.String(), "<Hangup") || time.Now().After(deadline) {
			t.Fatalf("Plivo wait failed: %d %s", rec.Code, rec.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if spawned, killed, _ := p.counts(); spawned != 1 || killed != 0 {
		t.Fatalf("spawned=%d killed=%d", spawned, killed)
	}
}

func TestRealtimePreparationSlowAnswerAPICompletesCarrierActivationAfterTimeout(t *testing.T) {
	a, ctx, p, _, row, unblock := preparationFixture(t, false)
	a.preparations.wait = 60 * time.Millisecond
	copy := *row
	result := make(chan error, 1)
	go func() { _, err := a.answerCall(ctx, &copy, "Help.", "", "", false); result <- err }()
	waitPreparationEntered(t, p)
	if err := <-result; !errors.Is(err, errAnswerPreparationInProgress) {
		t.Fatal(err)
	}
	unblock()
	deadline := time.Now().Add(time.Second)
	for {
		current, _ := a.db().findCall(row.ID)
		if current.Status == "answered" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background carrier activation did not complete: %s", current.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if spawned, killed, answered := p.counts(); spawned != 1 || killed != 0 || answered != 1 {
		t.Fatalf("spawned=%d killed=%d answered=%d", spawned, killed, answered)
	}
}
