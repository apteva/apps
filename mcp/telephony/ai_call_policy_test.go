package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func aiPolicyFixture(t *testing.T) (*App, *sdk.AppCtx, *answerPlatform, *callRow, *aiPolicyRuntime, time.Time) {
	t.Helper()
	t.Setenv("APTEVA_GATEWAY_URL", "")
	t.Setenv("APTEVA_APP_TOKEN", "")
	p := &answerPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(p))
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	a := &App{installID: 42}
	t.Cleanup(a.stopAIPolicies)
	row := testCall("ai-policy", "in-progress")
	row.ThreadID = "tel-" + row.ID
	row.PeerKind = peerKindRealtime
	base := time.Now().UTC().Truncate(time.Second)
	row.MediaConnectedAt = ringTime(base)
	row.MediaStatus = "connected"
	if err := a.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	durationExec(t, a, `UPDATE calls SET media_status='connected',media_active=1,media_connected_at=? WHERE id=?`, ringTime(base), row.ID)
	if err := a.registerAIPolicy(ctx, &row, row.ThreadID, projectAICallPolicy(nil), base); err != nil {
		t.Fatal(err)
	}
	r := a.aiRuntime(&row)
	r.feedLive = true
	r.phase = "listening"
	r.playback = func() bool { return false }
	return a, ctx, p, &row, r, base
}
func policyAdvance(t *testing.T, r *aiPolicyRuntime, row *callRow, at time.Time, want string) {
	t.Helper()
	r.mu.Lock()
	got := r.advance(row, at)
	r.mu.Unlock()
	if got != want {
		t.Fatalf("at %s action %q want %q; state %+v", at, got, want, r.state)
	}
}
func TestAICallPolicyValidationAndProjectDefaults(t *testing.T) {
	defaults := projectAICallPolicy(nil)
	if defaults != (aiCallPolicy{1200, 30, 15}) {
		t.Fatal(defaults)
	}
	for _, raw := range []string{`null`, `[]`, `"x"`, `{"max_duration_seconds":59}`, `{"max_duration_seconds":14401}`, `{"inactivity_timeout_seconds":-1}`, `{"inactivity_timeout_seconds":301}`, `{"response_window_seconds":4}`, `{"response_window_seconds":121}`, `{"response_window_seconds":null}`, `{"response_window_seconds":"15"}`, `{"inactivity_timeout_seconds":1.2}`, `{"oops":1}`} {
		t.Run(raw, func(t *testing.T) {
			if _, err := destinationAICallPolicy("ai", `{"ai_call_policy":`+raw+`}`, defaults); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
	for _, kind := range []string{"browser", "pstn", "sip"} {
		if _, err := destinationAICallPolicy(kind, `{"ai_call_policy":{}}`, defaults); err == nil {
			t.Fatal("human destination accepted AI policy")
		}
	}
	got, err := destinationAICallPolicy("agent", `{"ai_call_policy":{"max_duration_seconds":600,"inactivity_timeout_seconds":0}}`, defaults)
	if err != nil || got != (aiCallPolicy{600, 0, 15}) {
		t.Fatal(got, err)
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithConfig(map[string]string{"ai_call_max_duration_seconds": "900", "ai_call_inactivity_timeout_seconds": "45", "ai_call_response_window_seconds": "20"}))
	if got := projectAICallPolicy(ctx); got != (aiCallPolicy{900, 45, 20}) {
		t.Fatal(got)
	}
}
func TestAICallPolicyReminderWaitsForPlaybackAndFullResponseWindow(t *testing.T) {
	_, _, _, row, r, base := aiPolicyFixture(t)
	policyAdvance(t, r, row, base, "")
	policyAdvance(t, r, row, base.Add(29*time.Second), "")
	policyAdvance(t, r, row, base.Add(30*time.Second), "remind")
	r.state.ReminderIssued = true
	token := r.state.ReminderToken
	policyAdvance(t, r, row, base.Add(31*time.Second), "")
	if r.state.ReminderToken != token {
		t.Fatal("duplicate reminder")
	}
	// Model finishes generating before the caller finishes hearing the reminder.
	r.state.ReminderIssued = true
	r.state.ReminderSent = true
	r.reminderAudio = true
	r.phase = "speaking"
	policyAdvance(t, r, row, base.Add(33*time.Second), "")
	r.phase = "listening"
	pending := true
	r.playback = func() bool { return pending }
	policyAdvance(t, r, row, base.Add(40*time.Second), "")
	if !r.state.ResponseDeadline.IsZero() {
		t.Fatal("response window began before playback ack")
	}
	pending = false
	r.lastOutput = base.Add(40 * time.Second)
	policyAdvance(t, r, row, base.Add(40*time.Second+300*time.Millisecond), "")
	policyAdvance(t, r, row, base.Add(41*time.Second), "")
	policyAdvance(t, r, row, base.Add(55*time.Second), "")
	policyAdvance(t, r, row, base.Add(56*time.Second), "terminate")
	if r.state.Reason != terminationAIInactivity {
		t.Fatal(r.state)
	}
}
func TestAICallPolicyPausesOnlyInactivity(t *testing.T) {
	for _, phase := range []string{"speaking", "thinking", "working", "waiting", "hold", "disconnected", "unknown"} {
		t.Run(phase, func(t *testing.T) {
			_, _, _, row, r, base := aiPolicyFixture(t)
			policyAdvance(t, r, row, base, "")
			r.phase = phase
			policyAdvance(t, r, row, base.Add(50*time.Second), "")
			r.phase = "listening"
			policyAdvance(t, r, row, base.Add(60*time.Second), "")
			policyAdvance(t, r, row, base.Add(89*time.Second), "")
			policyAdvance(t, r, row, base.Add(90*time.Second), "remind")
			r.phase = phase
			policyAdvance(t, r, row, base.Add(1200*time.Second), "terminate")
			if r.state.Reason != terminationAIMaxDuration {
				t.Fatal(r.state)
			}
		})
	}
	for _, why := range []string{"media_recovery", "hold", "feed_loss"} {
		t.Run(why, func(t *testing.T) {
			a, _, _, row, r, base := aiPolicyFixture(t)
			policyAdvance(t, r, row, base, "")
			switch why {
			case "media_recovery":
				row.MediaStatus = "disconnected"
			case "hold":
				row.HoldState = "held"
			case "feed_loss":
				a.setAIPhaseFeed(row.AgentID, false)
			}
			policyAdvance(t, r, row, base.Add(time.Minute), "")
			policyAdvance(t, r, row, base.Add(1200*time.Second), "terminate")
		})
	}
}
func TestAICallPolicySpeechAndDTMFCancelReminderAtBoundary(t *testing.T) {
	for _, kind := range []string{"VAD", "DTMF", "Core user"} {
		t.Run(kind, func(t *testing.T) {
			a, _, _, row, r, base := aiPolicyFixture(t)
			policyAdvance(t, r, row, base, "")
			policyAdvance(t, r, row, base.Add(30*time.Second), "remind")
			r.state.ReminderIssued = true
			r.state.ReminderSent = true
			r.reminderAudio = true
			policyAdvance(t, r, row, base.Add(31*time.Second), "")
			at := base.Add(46 * time.Second)
			if kind == "Core user" {
				a.observeAIPhase(sdk.TelemetryStreamEvent{AgentID: row.AgentID, ThreadID: row.ThreadID, Type: "realtime.user", Time: at, Data: json.RawMessage(`{"text":"yes"}`)})
			} else {
				a.observeAICaller(row, true, at)
			}
			policyAdvance(t, r, row, at, "")
			if r.state.Stage != "active" || r.state.ReminderToken != "" {
				t.Fatal("caller response did not cancel", r.state)
			}
		})
	}
}
func TestAICallPolicyIgnoresSilenceAndOldOwners(t *testing.T) {
	a, _, _, row, r, base := aiPolicyFixture(t)
	policyAdvance(t, r, row, base, "")
	a.observeAICaller(row, false, base.Add(29*time.Second))
	stale := *row
	stale.ThreadID = "old-thread"
	a.observeAICaller(&stale, true, base.Add(29*time.Second))
	a.observeAIPhase(sdk.TelemetryStreamEvent{AgentID: 999, ThreadID: row.ThreadID, Type: "realtime.state", Time: base, Data: json.RawMessage(`{"state":"working"}`)})
	policyAdvance(t, r, row, base.Add(30*time.Second), "remind")
}
func TestAICallPolicyDisarmsOnTerminalOrHumanTransfer(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprint(terminal), func(t *testing.T) {
			a, ctx, p, row, r, base := aiPolicyFixture(t)
			policyAdvance(t, r, row, base, "")
			if terminal {
				durationExec(t, a, `UPDATE calls SET status='canceled' WHERE id=?`, row.ID)
			} else {
				durationExec(t, a, `UPDATE calls SET peer_kind='human',thread_id='human-new' WHERE id=?`, row.ID)
			}
			if err := a.runAICallPoliciesAt(ctx, base.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if len(p.integrationCalls) != 0 {
				t.Fatal("terminated non-AI owner")
			}
			raw, err := a.aiPolicyDiagnostics(row.ProjectID, row.ID)
			if err != nil {
				t.Fatal(err)
			}
			if raw.(aiPolicyState).Stage != "disarmed" {
				t.Fatal(raw)
			}
		})
	}
}
func TestAICallPolicyDurationSurvivesReconnectAndProcessLoss(t *testing.T) {
	a, ctx, p, row, r, base := aiPolicyFixture(t)
	if err := a.runAICallPoliciesAt(ctx, base); err != nil {
		t.Fatal(err)
	}
	original := r.state.Deadline
	a.bindAIPlayback(row, "replacement", func() bool { return false })
	a.observeAIOutput(row, base.Add(1190*time.Second))
	if r.state.Deadline != original {
		t.Fatal("reconnect extended duration")
	}
	// No policy state restored as listening; only the durable maximum runs.
	fresh := &App{installID: 42}
	t.Cleanup(fresh.stopAIPolicies)
	if err := fresh.runAICallPoliciesAt(ctx, base.Add(1200*time.Second)); err != nil {
		t.Fatal(err)
	}
	got := durationRow(t, a, row.ID)
	if got.Status != "completed" || got.TerminationReason != terminationAIMaxDuration || len(p.integrationCalls) != 1 {
		t.Fatalf("%+v commands %v", got, p.integrationCalls)
	}
	// Late normal/error carrier callbacks cannot turn a policy ending into a failure.
	if _, err := a.db().updateStatusWithFacts(row.ID, "failed", "carrier ended", lifecycleFacts{Source: "provider", TerminationCause: "normal_clearing"}); err != nil {
		t.Fatal(err)
	}
	if got = durationRow(t, a, row.ID); got.Status != "completed" || got.TerminationReason != terminationAIMaxDuration || got.ErrorMessage != "" {
		t.Fatal(got)
	}
	if err := fresh.runAICallPoliciesAt(ctx, base.Add(1300*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(p.integrationCalls) != 1 {
		t.Fatal("duplicate hangup")
	}
}
func TestAICallPolicyGenericCarrierTerminationAndBoundedFailure(t *testing.T) {
	for _, slug := range []string{"twilio", "telnyx"} {
		t.Run(slug, func(t *testing.T) {
			a, ctx, p, row, _, base := aiPolicyFixture(t)
			p.credentials = &sdk.ConnectionCredentials{Slug: slug, Fields: map[string]string{"auth_token": "test", "api_key": "test", "connection_id": "test"}}
			durationExec(t, a, `UPDATE calls SET carrier_slug=?,carrier_sid=?,carrier_answered_at=? WHERE id=?`, slug, "provider-call", ringTime(base), row.ID)
			if err := a.runAICallPoliciesAt(ctx, base); err != nil {
				t.Fatal(err)
			}
			if err := a.runAICallPoliciesAt(ctx, base.Add(1200*time.Second)); err != nil {
				t.Fatal(err)
			}
			want := "update_call"
			if slug == "telnyx" {
				want = "hangup_call"
			}
			if len(p.integrationCalls) != 1 || p.integrationCalls[0].Tool != want {
				t.Fatal(p.integrationCalls)
			}
		})
	}
	t.Run("failure", func(t *testing.T) {
		a, ctx, p, row, r, base := aiPolicyFixture(t)
		p.failCarrier = true
		if err := a.runAICallPoliciesAt(ctx, base); err != nil {
			t.Fatal(err)
		}
		for _, sec := range []int{1200, 1201, 1205, 1215, 1250, 1400} {
			if err := a.runAICallPoliciesAt(ctx, base.Add(time.Duration(sec)*time.Second)); err != nil {
				t.Fatal(err)
			}
		}
		if len(p.integrationCalls) != 3 || r.state.Attempts != 3 {
			t.Fatal("unbounded retry", p.integrationCalls, r.state)
		}
		got := durationRow(t, a, row.ID)
		if isTerminalStatus(got.Status) || got.TerminationReason != terminationAIMaxDuration {
			t.Fatal("failed command falsely marked completion", got)
		}
	})
}
func TestAICallPolicyFrozenDestinationAndStartupRetries(t *testing.T) {
	a, ctx, _, row, r, base := aiPolicyFixture(t)
	// A persisted snapshot wins over later project/destination changes and retries.
	r.state.Policy = aiCallPolicy{600, 40, 20}
	r.state.Stage = "prepared"
	if err := a.persistAIPolicy(r, r.state); err != nil {
		t.Fatal(err)
	}
	changed := ctx.WithProject(row.ProjectID)
	got, err := a.aiPolicyForNewSession(changed, row)
	if err != nil || got != (aiCallPolicy{600, 40, 20}) {
		t.Fatal(got, err)
	}
	if err := a.registerAIPolicy(ctx, row, "tel-retry", aiCallPolicy{900, 10, 5}, base.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	row.ThreadID = "tel-retry"
	next := a.aiRuntime(row)
	if next.state.Policy != got {
		t.Fatal("startup retry changed snapshot")
	}
	if _, err := a.saveRoutingDestination(row.ProjectID, "bad-policy", "AI", "ai", map[string]any{"agent_id": 7, "ai_call_policy": map[string]any{"max_duration_seconds": 0}}, true); err == nil {
		t.Fatal("save accepted invalid policy")
	}
}
func TestAICallPolicyMediaHotPathAndHumanIsolation(t *testing.T) {
	a, ctx, _, row, r, base := aiPolicyFixture(t)
	var before int
	_ = ctx.AppDB().QueryRow(`SELECT count(*) FROM ai_call_policies`).Scan(&before)
	for i := 0; i < 10000; i++ {
		a.observeAICaller(row, i%7 == 0, base)
		a.observeAIOutput(row, base)
	}
	raw, err := a.aiPolicyDiagnostics(row.ProjectID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if raw.(aiPolicyState).Stage != "prepared" || len(raw.(aiPolicyState).Events) != 1 {
		t.Fatal("per-frame SQL journaling", raw)
	}
	human := *row
	human.PeerKind = peerKindHuman
	last := r.lastActivity
	a.observeAICaller(&human, true, base.Add(time.Hour))
	if r.lastActivity != last {
		t.Fatal("human audio changed AI timer")
	}
}
func TestAICallPolicyPhaseFeedDisconnectAndReconnectSafety(t *testing.T) {
	a, _, _, row, r, base := aiPolicyFixture(t)
	stream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer test" {
			t.Error("missing authorization")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		ev := sdk.TelemetryStreamEvent{AgentID: row.AgentID, ThreadID: row.ThreadID, Type: "realtime.state", Time: base, Data: json.RawMessage(`{"state":"listening"}`)}
		raw, _ := json.Marshal(ev)
		fmt.Fprintf(w, "data: %s\n\n", raw)
	}))
	defer stream.Close()
	if permanent, _ := a.readAIPhaseFeed(context.Background(), stream.Client(), stream.URL, "test", row.AgentID); permanent {
		t.Fatal("transient loss marked permanent")
	}
	// Supervisor of the stream invalidates old phases after EOF.
	a.setAIPhaseFeed(row.AgentID, false)
	policyAdvance(t, r, row, base, "")
	policyAdvance(t, r, row, base.Add(time.Minute), "")
	a.setAIPhaseFeed(row.AgentID, true)
	policyAdvance(t, r, row, base.Add(90*time.Second), "")
	if r.phase != "unknown" {
		t.Fatal("reconnect reused stale listening phase")
	}
}
func TestAICallPolicyConcurrentActivityAndGenerationObservations(t *testing.T) {
	a, _, _, row, r, base := aiPolicyFixture(t)
	policyAdvance(t, r, row, base, "")
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				a.observeAICaller(row, true, base.Add(time.Second))
				a.observeAIOutput(row, base.Add(time.Second))
				r.mu.Lock()
				r.advance(row, base.Add(2*time.Second))
				r.mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if r.state.Stage != "active" {
		t.Fatal(r.state)
	}
	if !strings.Contains(aiPolicyDirective("Help", r.state.Policy), "Telephony owns") {
		t.Fatal("policy missing from supportive instructions")
	}
}

type aiReminderPlatform struct {
	*answerPlatform
	mu        sync.Mutex
	reminders []sdk.ThreadRef
	done      chan struct{}
	fail      bool
}

func (p *aiReminderPlatform) SpawnThread(sdk.ThreadSpawnRequest) (*sdk.ThreadSpawnResult, error) {
	return nil, nil
}
func (p *aiReminderPlatform) SendThreadEvent(target sdk.ThreadRef, message any) error {
	p.mu.Lock()
	p.reminders = append(p.reminders, target)
	p.mu.Unlock()
	if p.done != nil {
		p.done <- struct{}{}
	}
	if p.fail {
		return fmt.Errorf("temporary event failure")
	}
	return nil
}
func TestAICallPolicyDispatchExactlyOnceAndNeverOnNewOwner(t *testing.T) {
	for _, transfer := range []bool{false, true} {
		t.Run(fmt.Sprint(transfer), func(t *testing.T) {
			a, _, _, row, r, base := aiPolicyFixture(t)
			platform := &aiReminderPlatform{answerPlatform: &answerPlatform{}, done: make(chan struct{}, 4)}
			ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
			policyAdvance(t, r, row, base, "")
			policyAdvance(t, r, row, base.Add(30*time.Second), "remind")
			if transfer {
				durationExec(t, a, `UPDATE calls SET peer_kind='human',thread_id='human-winner' WHERE id=?`, row.ID)
			}
			var wg sync.WaitGroup
			token := r.state.ReminderToken
			for i := 0; i < 12; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); a.dispatchAIReminder(ctx, r, token) }()
			}
			wg.Wait()
			// Drain all bounded jobs before inspecting results / restoring global context.
			deadline := time.Now().Add(time.Second)
			for len(a.aiPolicies.commands) > 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			platform.mu.Lock()
			defer platform.mu.Unlock()
			want := 1
			if transfer {
				want = 0
			}
			if len(platform.reminders) != want {
				t.Fatalf("reminders %v", platform.reminders)
			}
			if !transfer && platform.reminders[0].ThreadID != row.ThreadID {
				t.Fatal("wrong reminder thread")
			}
		})
	}
}
func TestAICallPolicyNewerPersistedTransitionsWin(t *testing.T) {
	a, _, _, row, r, base := aiPolicyFixture(t)
	old := r.state
	next := old
	next.Revision++
	next.Stage = "active"
	next.IdleSince = base
	if err := a.persistAIPolicy(r, next); err != nil {
		t.Fatal(err)
	}
	if err := a.persistAIPolicy(r, old); err != nil {
		t.Fatal(err)
	}
	got, err := a.aiPolicyDiagnostics(row.ProjectID, row.ID)
	if err != nil || got.(aiPolicyState).Stage != "active" {
		t.Fatal("older worker snapshot won", got, err)
	}
}
func TestAICallPolicySpeechCancellationPersistsDuringToolWork(t *testing.T) {
	a, ctx, _, row, r, base := aiPolicyFixture(t)
	policyAdvance(t, r, row, base, "")
	policyAdvance(t, r, row, base.Add(30*time.Second), "remind")
	r.state.Revision++
	if err := a.persistAIPolicy(r, r.state); err != nil {
		t.Fatal(err)
	}
	a.observeAICaller(row, true, base.Add(31*time.Second))
	r.phase = "working"
	if err := a.runAICallPoliciesAt(ctx, base.Add(32*time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err := a.aiPolicyDiagnostics(row.ProjectID, row.ID)
	if err != nil || got.(aiPolicyState).Stage != "active" || got.(aiPolicyState).ReminderIssued {
		t.Fatal(got, err)
	}
}
func TestAICallPolicyStaleMediaGenerationCannotCountAsReminderAudio(t *testing.T) {
	a, _, _, row, r, base := aiPolicyFixture(t)
	a.bindAIPlayback(row, "new-stream", func() bool { return false })
	policyAdvance(t, r, row, base, "")
	policyAdvance(t, r, row, base.Add(30*time.Second), "remind")
	a.observeAIOutput(row, base.Add(31*time.Second), "old-stream")
	a.observeAICaller(row, true, base.Add(31*time.Second), "old-stream")
	if r.reminderAudio || r.state.Stage != "reminder" {
		t.Fatal("stale media mutated policy")
	}
	a.observeAIOutput(row, base.Add(31*time.Second), "new-stream")
	if !r.reminderAudio {
		t.Fatal("current media output ignored")
	}
}
func TestAICallPolicyReminderFailureIsDistinctAndBounded(t *testing.T) {
	a, ctx, p, row, r, base := aiPolicyFixture(t)
	policyAdvance(t, r, row, base, "")
	policyAdvance(t, r, row, base.Add(30*time.Second), "remind")
	r.state.ReminderIssued = true
	r.state.ReminderSent = false
	if err := a.runAICallPoliciesAt(ctx, base.Add(60*time.Second)); err != nil {
		t.Fatal(err)
	}
	got := durationRow(t, a, row.ID)
	if got.Status != "failed" || got.TerminationReason != terminationAIPolicyFailure || len(p.integrationCalls) != 1 {
		t.Fatal(got, p.integrationCalls)
	}
	if _, err := a.db().updateStatusWithFacts(row.ID, "completed", "", lifecycleFacts{Source: "provider", TerminationCause: "normal_clearing"}); err != nil {
		t.Fatal(err)
	}
	if got = durationRow(t, a, row.ID); got.Status != "failed" || got.TerminationReason != terminationAIPolicyFailure {
		t.Fatal("late callback hid policy failure", got)
	}
}
func BenchmarkAICallPolicyMediaObservation(b *testing.B) {
	row := &callRow{ID: "bench", ProjectID: "project-a", ThreadID: "tel-bench", PeerKind: peerKindRealtime}
	r := &aiPolicyRuntime{id: row.ID, project: row.ProjectID, thread: row.ThreadID, state: aiPolicyState{Stage: "active"}}
	a := &App{}
	a.aiPolicies.calls = map[string]*aiPolicyRuntime{row.ID: r}
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.observeAICaller(row, true, now)
		a.observeAIOutput(row, now)
	}
}

func TestAICallPolicyCarrierOutputShapes(t *testing.T) {
	for _, shape := range []string{"telnyx", "twilio", "plivo", "bandwidth", "signalwire"} {
		t.Run(shape, func(t *testing.T) {
			a, _, _, row, r, base := aiPolicyFixture(t)
			a.bindAIPlayback(row, "stream", func() bool { return false })
			policyAdvance(t, r, row, base, "")
			policyAdvance(t, r, row, base.Add(30*time.Second), "remind")
			mark, _ := json.Marshal(map[string]any{"event": "mark", "mark": map[string]string{"name": "mark-1"}})
			a.observeAIJSONOutput(row, mark, base.Add(31*time.Second), "stream")
			if r.reminderAudio {
				t.Fatal("control frame counted as audio")
			}
			raw, _ := json.Marshal(buildCarrierOutbound(shape, "stream", "audio"))
			a.observeAIJSONOutput(row, raw, base.Add(31*time.Second), "stream")
			if !r.reminderAudio {
				t.Fatalf("%s output not observed", shape)
			}
		})
	}
}

func TestAICallPolicyReminderDoesNotExpireDuringToolOrUnknownPhase(t *testing.T) {
	for _, phase := range []string{"working", "thinking", "hold", "unknown"} {
		t.Run(phase, func(t *testing.T) {
			_, _, _, row, r, base := aiPolicyFixture(t)
			policyAdvance(t, r, row, base, "")
			policyAdvance(t, r, row, base.Add(30*time.Second), "remind")
			r.state.ReminderIssued = true
			r.phase = phase
			policyAdvance(t, r, row, base.Add(90*time.Second), "")
			policyAdvance(t, r, row, base.Add(1200*time.Second), "terminate")
		})
	}
}
func TestAICallPolicyDecodedSpeechResetsInactivityAtCarrierRates(t *testing.T) {
	for _, rate := range []int{8000, 16000, 24000} {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			a, _, _, row, r, base := aiPolicyFixture(t)
			frontend := newCarrierAudioFrontend(rate)
			frontend.process(telephoneNoise(rate, 500, 70))
			policyAdvance(t, r, row, base, "")
			decoded := frontend.process(telephoneSpeech(rate, 800, 1500))
			if !decoded.SpeechActive {
				t.Fatal("existing VAD activity not exposed")
			}
			a.observeAICaller(row, decoded.SpeechActive, base.Add(29*time.Second))
			policyAdvance(t, r, row, base.Add(30*time.Second), "")
			silent := frontend.process(make([]int16, rate/10))
			a.observeAICaller(row, silent.SpeechActive, base.Add(50*time.Second))
			policyAdvance(t, r, row, base.Add(59*time.Second), "")
			policyAdvance(t, r, row, base.Add(60*time.Second), "remind")
		})
	}
}
func TestAICallPolicyMediaObservationDoesNotWaitForDatabase(t *testing.T) {
	a, ctx, _, row, _, base := aiPolicyFixture(t)
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			a.observeAICaller(row, true, base)
			a.observeAIOutput(row, base)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("audio observations waited for occupied database")
	}
}

func TestAICallPolicyRestoresBeforeReplacementMediaRegisters(t *testing.T) {
	a, ctx, _, row, r, base := aiPolicyFixture(t)
	if err := a.runAICallPoliciesAt(ctx, base); err != nil {
		t.Fatal(err)
	}
	original := r.state.Deadline
	fresh := &App{installID: 42}
	t.Cleanup(fresh.stopAIPolicies)
	if err := fresh.restoreAIPolicyRuntimes(); err != nil {
		t.Fatal(err)
	}
	fresh.bindAIPlayback(row, "new-process-stream", func() bool { return false })
	restored := fresh.aiRuntime(row)
	if restored == nil || restored.playback == nil || restored.generation != "new-process-stream" || restored.feedLive || restored.phase == "listening" || restored.state.Deadline != original {
		t.Fatal("unsafe restored runtime", restored)
	}
}

func TestAICallPolicyFailedHangupCannotLabelHumanCompletion(t *testing.T) {
	a, ctx, p, row, _, base := aiPolicyFixture(t)
	p.failCarrier = true
	if err := a.runAICallPoliciesAt(ctx, base); err != nil {
		t.Fatal(err)
	}
	if err := a.runAICallPoliciesAt(ctx, base.Add(1200*time.Second)); err != nil {
		t.Fatal(err)
	}
	durationExec(t, a, `UPDATE calls SET peer_kind='human',thread_id='human-owner' WHERE id=?`, row.ID)
	// Provider completion arrives before the next policy tick can disarm.
	if _, err := a.db().updateStatusWithFacts(row.ID, "completed", "", lifecycleFacts{Source: "provider"}); err != nil {
		t.Fatal(err)
	}
	got := durationRow(t, a, row.ID)
	if got.TerminationReason != terminationCompleted || got.TerminationInitiator == "telephony" {
		t.Fatal("human completion mislabeled", got)
	}
	if err := a.runAICallPoliciesAt(ctx, base.Add(1250*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(p.integrationCalls) != 1 {
		t.Fatal("AI attempted to terminate new human owner")
	}
}
