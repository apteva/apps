package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

type activationGatePlatform struct {
	*answerPlatform
	entered chan struct{}
	proceed chan struct{}
	calls   atomic.Int32
	result  error
}

func (p *activationGatePlatform) ExecuteIntegrationToolContext(request context.Context, _ int64, _ string, _ map[string]any) (*sdk.ExecuteResult, error) {
	if p.calls.Add(1) == 1 {
		close(p.entered)
	}
	select {
	case <-p.proceed:
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}, p.result
	case <-request.Done():
		return nil, request.Err()
	}
}

func activationGate(t *testing.T, a *App, p *answerPlatform) (*activationGatePlatform, *sdk.AppCtx, func()) {
	t.Helper()
	g := &activationGatePlatform{answerPlatform: p, entered: make(chan struct{}), proceed: make(chan struct{})}
	manifest, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	ctx := sdk.NewAppCtxForTest(manifest, a.db().db, sdk.Config{}, g, nil).WithProject("project-a")
	globalCtx = ctx // original fixture restores this after all socket handlers drain
	var once sync.Once
	release := func() { once.Do(func() { close(g.proceed) }) }
	t.Cleanup(release)
	return g, ctx, release
}

func activationSocketServer(t *testing.T, a *App) *httptest.Server {
	t.Helper()
	var handlers sync.WaitGroup
	track := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { handlers.Add(1); defer handlers.Done(); h(w, r) }
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/media/telnyx/", track(a.handleTelnyxMediaStream))
	mux.HandleFunc("/media/twilio/", track(a.handleTwilioMediaStream))
	mux.HandleFunc("/peer/", track(a.handlePeerSocket))
	mux.HandleFunc("/softphone/media/", track(a.handleSoftphoneMedia))
	s := httptest.NewServer(mux)
	u, _ := url.Parse(s.URL)
	t.Setenv("APTEVA_APP_PORT", u.Port())
	t.Cleanup(func() {
		a.stopCarrierBridges()
		s.Close()
		done := make(chan struct{})
		go func() { handlers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("activation socket handlers did not drain")
		}
	})
	return s
}

func waitActivationCommand(t *testing.T, g *activationGatePlatform) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(time.Second):
		t.Fatal("carrier command not dispatched")
	}
}

func waitActivationMedia(t *testing.T, a *App, id string) {
	t.Helper()
	end := time.Now().Add(time.Second)
	for time.Now().Before(end) {
		row := activationRow(t, a, id)
		if row.MediaConnectedAt != "" && row.MediaStatus == "connected" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("carrier socket could not connect while command was in flight")
}

// The carrier opens its actual authenticated WebSocket and sends audio BEFORE
// returning the activation HTTP response. This reproduces the 0.11.5 deadlock.
func TestCarrierActivationSocketBeforeCommandResponse(t *testing.T) {
	for _, lateError := range []bool{false, true} {
		t.Run(map[bool]string{false: "accepted", true: "late_HTTP_error"}[lateError], func(t *testing.T) {
			a, _, p, _, row, _ := reliabilityFixture(t)
			g, ctx, release := activationGate(t, a, p)
			if lateError {
				g.result = errors.New("HTTP response lost after media connected")
			}
			core, coreServer := newFakeCoreAudioBridge(t)
			activationExec(t, a, `UPDATE calls SET callback_secret='activation-secret',peer_kind='realtime',agent_id=7,thread_id='ready-ai',audio_bridge_url=?,status='answering',carrier_answered_at=? WHERE id=?`, "ws"+strings.TrimPrefix(coreServer.URL, "http"), ringTime(time.Now()), row.ID)
			row = activationRow(t, a, row.ID)
			if err := a.ensureCarrierActivation(row); err != nil {
				t.Fatal(err)
			}
			server := activationSocketServer(t, a)
			done := make(chan error, 1)
			var command sync.WaitGroup
			command.Add(1)
			go func() { defer command.Done(); done <- a.driveCarrierActivation(ctx, row.ID) }()
			t.Cleanup(func() { release(); command.Wait() })
			waitActivationCommand(t, g)
			for range 10 {
				activationPending(t, a.driveCarrierActivation(ctx, row.ID))
			}
			carrier := dialWS(t, server.URL+"/media/telnyx/"+row.ID+"/"+row.CallbackSecret)
			writeRecoveryTelnyxFrame(t, carrier, row, "activation-stream")
			select {
			case frame := <-core.inbound:
				if len(frame) == 0 {
					t.Fatal("empty audio")
				}
			case <-time.After(time.Second):
				t.Fatal("actual caller audio did not reach Core before command returned")
			}
			waitActivationMedia(t, a, row.ID)
			if g.calls.Load() != 1 {
				t.Fatal("duplicate carrier commands", g.calls.Load())
			}
			release()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("activation did not finish")
			}
			state, err := a.carrierActivationPublic(row.ID)
			if err != nil || state["status"] != "connected" {
				t.Fatal("actual media success lost", state, err)
			}
			_ = carrier.Close()
		})
	}
}

func TestSoftphoneAnswerSocketBeforeCommandResponse(t *testing.T) {
	for _, provider := range []string{"telnyx", "twilio"} {
		t.Run(provider, func(t *testing.T) {
			a, _, p, _, row, _ := reliabilityFixture(t)
			if provider == "twilio" {
				p.credentials = &sdk.ConnectionCredentials{Slug: "twilio", Fields: map[string]string{"auth_token": "test-auth-token"}}
			}
			g, _, release := activationGate(t, a, p)
			activationExec(t, a, `UPDATE calls SET callback_secret='activation-secret',carrier_slug=?,status='answering',thread_id='human-ready',peer_token='peer-secret',audio_bridge_url='human-ready' WHERE id=?`, provider, row.ID)
			row = activationRow(t, a, row.ID)
			server := activationSocketServer(t, a)
			t.Cleanup(release)
			browser := dialWS(t, server.URL+"/softphone/media/"+row.ID+"/peer-secret")
			waitActivationCommand(t, g)
			path := "/media/" + provider + "/" + row.ID + "/" + row.CallbackSecret
			dialer := ws.Dialer{}
			if provider == "twilio" {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				dialer.Header = ws.HandshakeHeaderHTTP(http.Header{"X-Twilio-Signature": {twilioTestSignature(a.publicRequestURL(req), url.Values{}, "test-auth-token")}})
			}
			dialCtx, cancel := context.WithTimeout(t.Context(), time.Second)
			carrier, _, _, err := dialer.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http")+path)
			cancel()
			if err != nil {
				t.Fatal("carrier callback blocked by human answer", err)
			}
			t.Cleanup(func() { _ = carrier.Close() })
			if provider == "telnyx" {
				writeRecoveryTelnyxFrame(t, carrier, row, "human-stream")
			} else {
				start, _ := json.Marshal(map[string]any{"event": "start", "streamSid": "human-stream", "start": map[string]string{"callSid": row.CarrierSID}})
				if err = wsutil.WriteClientText(carrier, start); err != nil {
					t.Fatal(err)
				}
				media := `{"event":"media","streamSid":"human-stream","media":{"payload":"/////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////w=="}}`
				if err = wsutil.WriteClientText(carrier, []byte(media)); err != nil {
					t.Fatal(err)
				}
			}
			waitActivationMedia(t, a, row.ID)
			release()
			readSoftphoneEventWithin(t, browser, "ready", time.Second)
			if g.calls.Load() != 1 || activationRow(t, a, row.ID).Status != "answered" {
				t.Fatal("human answer duplicated or lost")
			}
			_ = browser.Close()
			_ = carrier.Close()
		})
	}
}

func TestCarrierActivationLateResultCannotReviveOwnership(t *testing.T) {
	for _, action := range []string{"caller_hangup", "new_owner"} {
		t.Run(action, func(t *testing.T) {
			a, _, p, _, row, _ := reliabilityFixture(t)
			g, ctx, release := activationGate(t, a, p)
			activationExec(t, a, `UPDATE calls SET peer_kind='realtime',agent_id=7,thread_id='ready-ai',audio_bridge_url='wss://bridge.test',status='answering' WHERE id=?`, row.ID)
			row = activationRow(t, a, row.ID)
			if err := a.ensureCarrierActivation(row); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			var command sync.WaitGroup
			command.Add(1)
			go func() { defer command.Done(); done <- a.driveCarrierActivation(ctx, row.ID) }()
			t.Cleanup(func() { release(); command.Wait() })
			waitActivationCommand(t, g)
			unlock := a.softphones.lockClaim(row.ID)
			if action == "caller_hangup" {
				activationExec(t, a, `UPDATE calls SET status='completed' WHERE id=?`, row.ID)
			} else {
				activationExec(t, a, `UPDATE calls SET peer_kind='human',thread_id='new-owner',status='pending' WHERE id=?`, row.ID)
			}
			unlock()
			release()
			select {
			case err := <-done:
				if !errors.Is(err, errAnswerCallEnded) {
					t.Fatal("late result not rejected", err)
				}
			case <-time.After(time.Second):
				t.Fatal("late activation hung")
			}
			current := activationRow(t, a, row.ID)
			if current.Status == "answered" || current.MediaConnectedAt != "" || g.calls.Load() != 1 {
				t.Fatal("late activation revived call", current)
			}
		})
	}
}

func TestCarrierActivationMediaFailureUsesFullRetryBudget(t *testing.T) {
	a, ctx, p, _, row, _ := reliabilityFixture(t)
	t.Cleanup(a.stopRoutingDispatcher)
	t.Cleanup(a.stopCarrierBridges)
	activationExec(t, a, `UPDATE calls SET peer_kind='realtime',agent_id=7,thread_id='ready-ai',audio_bridge_url='wss://bridge.test',status='answering',carrier_answered_at=? WHERE id=?`, ringTime(time.Now()), row.ID)
	row = activationRow(t, a, row.ID)
	if err := a.ensureCarrierActivation(row); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		activationExec(t, a, `UPDATE carrier_activations SET next_attempt_at='' WHERE call_id=?`, row.ID)
		activationPending(t, a.driveCarrierActivation(ctx, row.ID))
		current := activationRow(t, a, row.ID)
		b, err := a.claimCarrierBridge(current, t.Context())
		if err != nil {
			t.Fatal(err)
		}
		b.fail(carrierBridgeEvidence{Kind: "bridge_setup_error", Detail: "simulated connection failure"})
		activationPending(t, a.driveCarrierActivation(ctx, row.ID))
		if current := activationRow(t, a, row.ID); isTerminalStatus(current.Status) {
			t.Fatal("hung up before bounded attempts completed", attempt)
		}
		if err = a.restartCarrierMedia(ctx, row.ID, b.generation); err != nil {
			t.Fatal(err)
		}
		if len(p.integrationCalls) != attempt {
			t.Fatal("recovery dispatched competing activation", p.integrationCalls)
		}
	}
	activationExec(t, a, `UPDATE carrier_activations SET next_attempt_at='' WHERE call_id=?`, row.ID)
	if err := a.driveCarrierActivation(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	a.stopRoutingDispatcher()
	activationCommands(t, p, "start_streaming", "start_streaming", "start_streaming", "hangup_call")
	if activationRow(t, a, row.ID).Status != "failed" {
		t.Fatal("exhausted activation not ended")
	}
}
