package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func recoveryCall(t *testing.T, a *App, id, provider string) *callRow {
	t.Helper()
	row := testCall(id, "in-progress")
	row.CarrierSlug = provider
	row.PeerKind, row.AgentID, row.PeerToken = peerKindHuman, 0, "peer-secret"
	row.ThreadID = "human-" + id
	if err := a.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	return &row
}

func bridgeClaimActive(t *testing.T, a *App, id string) bool {
	t.Helper()
	var active bool
	if err := a.db().db.QueryRow(`SELECT media_active FROM calls WHERE id=?`, id).Scan(&active); err != nil {
		t.Fatal(err)
	}
	return active
}
func bridgeState(t *testing.T, b *carrierBridgeLease) string {
	t.Helper()
	var state string
	if err := b.app.db().db.QueryRow(`SELECT state FROM carrier_media_bridges WHERE generation=?`, b.generation).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestCarrierRecoveryReplacementOwnsClaimBeforeOldCleanup(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, a, "generation-race", "telnyx")
	old, err := a.claimCarrierBridge(row, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.stopCarrierBridges)
	old.setStream("old-stream")
	old.media()
	if _, err = a.claimCarrierBridge(row, t.Context()); !errors.Is(err, errCarrierBridgeBusy) {
		t.Fatal("healthy duplicate accepted", err)
	}
	socket, remote := net.Pipe()
	defer remote.Close()
	old.bind(socket)
	old.fail(carrierBridgeEvidence{Kind: "provider_error", Leg: "provider", Detail: "disconnected", EventID: "first", StreamID: "old-stream"})
	_ = remote.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = remote.Read(make([]byte, 1)); err == nil {
		t.Fatal("failed transport left open")
	}
	// Emulate a deferred/failed release write: the runtime proves this exact
	// generation is canceled, so a closed handler cannot retain the DB claim.
	if _, err = a.db().db.Exec(`UPDATE calls SET media_active=1 WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := a.claimCarrierBridge(row, t.Context())
	if err != nil {
		t.Fatal("replacement before cleanup rejected", err)
	}
	if fresh.generation == old.generation || fresh.deadline == "" {
		t.Fatal("generation/budget not retained")
	}
	fresh.setStream("new-stream")
	// A provider acknowledgement alone is insufficient to confirm recovery.
	a.carrierStreamEvent(row, callbackUpdate{MediaStatus: "connected", StreamID: "new-stream"})
	if bridgeState(t, fresh) != "connecting" {
		t.Fatal("webhook falsely confirmed media")
	}
	fresh.media() // Zero-valued PCM is valid: no voice/silence heuristic.
	before, _ := a.db().findCall(row.ID)
	old.finish()
	_ = a.db().updateCarrierAudioDiagnostics(row.ID, carrierAudioDiagnostics{Provider: "old-overwrite"}, old.generation)
	a.carrierStreamEvent(row, callbackUpdate{MediaStatus: "error", MediaError: "late", StreamID: "old-stream"})
	a.carrierStreamEvent(row, callbackUpdate{MediaStatus: "error", MediaError: "ambiguous"})
	after, _ := a.db().findCall(row.ID)
	if !bridgeClaimActive(t, a, row.ID) || after.MediaStatus != "connected" || after.Status != before.Status || after.ThreadID != row.ThreadID || after.PeerToken != row.PeerToken || fresh.ctx.Err() != nil {
		t.Fatalf("stale cleanup/callback changed replacement: %+v", after)
	}
	if bridgeState(t, fresh) != "connected" {
		t.Fatal("stale callback revived recovery")
	}
	if err = json.Unmarshal([]byte(after.CarrierAudioDiagnostics), new(map[string]any)); err == nil && strings.Contains(after.CarrierAudioDiagnostics, "old-overwrite") {
		t.Fatal("stale diagnostics overwrite")
	}
}

func TestCarrierRecoveryPreservesFirstCauseAndActualClose(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, a, "failure-evidence", "telnyx")
	b, err := a.claimCarrierBridge(row, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.stopCarrierBridges)
	b.setStream("stream-a")
	b.received(ws.OpClose, ws.NewCloseFrameBody(ws.StatusCode(1012), "edge restart"))
	b.socketError(mediaCloseLegCarrier, "read", wsutil.ClosedError{Code: ws.StatusCode(1012), Reason: "edge restart"})
	first, _ := a.db().findCall(row.ID)
	a.carrierStreamEvent(row, callbackUpdate{MediaStatus: "error", MediaError: "disconnected", StreamID: "stream-a", Facts: lifecycleFacts{ProviderEventID: "evt-a"}})
	b.finish()
	after, _ := a.db().findCall(row.ID)
	if after.MediaDisconnectedAt != first.MediaDisconnectedAt || after.MediaErrorMessage != first.MediaErrorMessage {
		t.Fatal("first failure overwritten", first.MediaDisconnectedAt, after.MediaDisconnectedAt)
	}
	history, err := a.db().carrierBridgeHistory(row.ProjectID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(history)
	for _, want := range []string{"1012", "edge restart", "disconnected", "evt-a", "local_cleanup", "cleanup_at"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("missing %s: %s", want, encoded)
		}
	}
	if after.MediaCloseCode == 1011 {
		t.Fatal("invented peer close code")
	}
}

func TestCarrierRecoveryAttemptsBoundedAndSameLeg(t *testing.T) {
	p := &answerPlatform{}
	a, ctx := withTelephonyTestContext(t, p)
	row := recoveryCall(t, a, "restart-budget", "telnyx")
	b, err := a.claimCarrierBridge(row, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.stopCarrierBridges)
	b.fail(carrierBridgeEvidence{Kind: "socket_read", Leg: "carrier", Detail: "EOF"})
	for i := 0; i < 4; i++ {
		_, err = a.db().db.Exec(`UPDATE carrier_media_bridges SET next_attempt_at=? WHERE generation=?`, ringTime(time.Now().Add(-time.Second)), b.generation)
		if err != nil {
			t.Fatal(err)
		}
		if err = a.restartCarrierMedia(ctx, row.ID, b.generation); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.integrationCalls) != 3 || bridgeState(t, b) != "failed" {
		t.Fatal("unbounded recovery", len(p.integrationCalls), bridgeState(t, b))
	}
	seen := map[string]bool{}
	for _, call := range p.integrationCalls {
		id, _ := call.Input["command_id"].(string)
		if call.Tool != "start_streaming" || call.Input["call_control_id"] != row.CarrierSID || call.Input["stream_track"] != "inbound_track" || id == "" || seen[id] {
			t.Fatalf("incorrect recovery command %+v", call)
		}
		seen[id] = true
		if call.Input["stream_bidirectional_codec"] != "L16" {
			t.Fatal("changed Telnyx codec", call.Input)
		}
	}
	current, _ := a.db().findCall(row.ID)
	if current.MediaDeadlineAt == "" || bridgeClaimActive(t, a, row.ID) || current.PeerToken != row.PeerToken || current.ThreadID != row.ThreadID {
		t.Fatal("failure not bounded/preserved", current)
	}
	if _, err = a.claimCarrierBridge(row, t.Context()); !errors.Is(err, errCarrierBridgeBusy) {
		t.Fatal("exhausted call reopened", err)
	}
}

func TestCarrierRecoveryLiveReplacementStillHasDeadline(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, a, "no-media", "telnyx")
	b, err := a.claimCarrierBridge(row, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.stopCarrierBridges)
	b.fail(carrierBridgeEvidence{Kind: "socket_read", Detail: "EOF"})
	fresh, err := a.claimCarrierBridge(row, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.db().db.Exec(`UPDATE carrier_media_bridges SET deadline_at=?,next_attempt_at='' WHERE generation=?`, ringTime(time.Now().Add(-time.Second)), fresh.generation)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.restartCarrierMedia(ctx, row.ID, fresh.generation); err != nil {
		t.Fatal(err)
	}
	if fresh.ctx.Err() == nil || bridgeState(t, fresh) != "failed" {
		t.Fatal("live socket without valid media escaped deadline")
	}
}

func TestCarrierRecoveryConnectingBudgetRehydratesAfterRuntimeLoss(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	t.Cleanup(a.stopCarrierBridges)
	row := recoveryCall(t, a, "rehydrate-budget", "telnyx")
	b, err := a.claimCarrierBridge(row, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b.attempts = 2
	b.fail(carrierBridgeEvidence{Kind: "socket_read", Detail: "EOF"})
	fresh, err := a.claimCarrierBridge(row, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	deadline := fresh.deadline
	a.stopCarrierBridges()
	// The app mount resets stale active flags; no in-memory handler survives.
	if _, err = a.db().db.Exec(`UPDATE calls SET media_active=0 WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	restarted := &App{installID: 42}
	t.Cleanup(restarted.stopCarrierBridges)
	rehydrated, err := restarted.claimCarrierBridge(row, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if rehydrated.deadline != deadline || rehydrated.attempts != 2 {
		t.Fatal("runtime loss reset recovery budget", rehydrated.deadline, rehydrated.attempts)
	}
}

func TestCarrierRecoveryWorkerReleasesFailedRuntimeOnTerminalState(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	t.Cleanup(a.stopCarrierBridges)
	row := recoveryCall(t, a, "failed-runtime", "telnyx")
	b, err := a.claimCarrierBridge(row, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b.fail(carrierBridgeEvidence{Kind: "socket_read", Detail: "EOF"})
	if err = a.failCarrierRecovery(row.ID, b.generation, b, "budget exhausted"); err != nil {
		t.Fatal(err)
	}
	// Mirror a terminal state learned by a DB/lifecycle path rather than a
	// callback calling killCallThread directly.
	if err = a.db().updateStatus(row.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err = a.runCarrierMediaRecovery(t.Context(), ctx); err != nil {
		t.Fatal(err)
	}
	a.mediaBridges.mu.Lock()
	left := a.mediaBridges.current[row.ID]
	a.mediaBridges.mu.Unlock()
	if left != nil || bridgeState(t, b) != "ended" {
		t.Fatal("failed handler retained after call ended")
	}
}

func TestCarrierRecoveryUnsupportedAdapterAndCallerCancellation(t *testing.T) {
	for _, provider := range []string{"twilio", "telnyx"} {
		t.Run(provider, func(t *testing.T) {
			p := &answerPlatform{}
			a, ctx := withTelephonyTestContext(t, p)
			row := recoveryCall(t, a, "cancel-"+provider, provider)
			b, err := a.claimCarrierBridge(row, t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(a.stopCarrierBridges)
			b.fail(carrierBridgeEvidence{Kind: "socket_read", Detail: "EOF"})
			_, _ = a.db().db.Exec(`UPDATE carrier_media_bridges SET next_attempt_at='' WHERE generation=?`, b.generation)
			if provider == "twilio" {
				if err = a.restartCarrierMedia(ctx, row.ID, b.generation); err != nil {
					t.Fatal(err)
				}
				if len(p.integrationCalls) != 0 {
					t.Fatal("invented unsupported restart")
				}
			}
			if err = a.db().updateStatus(row.ID, "completed", ""); err != nil {
				t.Fatal(err)
			}
			if err = a.killCallThread(ctx, row); err != nil {
				t.Fatal(err)
			}
			if err = a.restartCarrierMedia(ctx, row.ID, b.generation); err != nil {
				t.Fatal(err)
			}
			b.finish()
			if bridgeState(t, b) != "ended" || len(p.integrationCalls) != 0 {
				t.Fatal("late recovery after caller ended")
			}
			if _, err = a.claimCarrierBridge(row, t.Context()); err == nil {
				t.Fatal("terminal call accepted replacement")
			}
		})
	}
}

func TestCarrierRecoveryProtocolLivenessIgnoresSilenceMuteAndHold(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, a, "silent-call", "telnyx")
	b, err := a.claimCarrierBridge(row, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.stopCarrierBridges)
	b.media()
	for _, state := range []string{"silence", "muted", "held"} {
		b.received(ws.OpPong, nil)
		if !b.protocolHealthy(time.Now().Add(19*time.Second)) || b.ctx.Err() != nil || bridgeState(t, b) != "connected" {
			t.Fatal("false failure from", state)
		}
	}
	if b.protocolHealthy(time.Now().Add(21 * time.Second)) {
		t.Fatal("silent transport was considered live indefinitely")
	}
}

func TestCarrierRecoveryEstablishedFramesDoNotWaitForReporting(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	t.Cleanup(a.stopCarrierBridges)
	row := recoveryCall(t, a, "reporting-lock", "telnyx")
	b, err := a.claimCarrierBridge(row, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b.media()
	b.mu.Lock()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10000; i++ {
			b.media()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		b.mu.Unlock()
		t.Fatal("established audio blocked by reporting")
	}
	b.mu.Unlock()
}

func BenchmarkCarrierRecoveryEstablishedFrame(b *testing.B) {
	bridge := &carrierBridgeLease{ctx: context.Background()}
	bridge.flowing.Store(true)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		bridge.media()
	}
}

func TestCarrierRecoverySimultaneousFailuresRemainIsolated(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	t.Cleanup(a.stopCarrierBridges)
	var bridges []*carrierBridgeLease
	for i := 0; i < 7; i++ {
		row := recoveryCall(t, a, fmt.Sprint("parallel-", i), "telnyx")
		b, err := a.claimCarrierBridge(row, t.Context())
		if err != nil {
			t.Fatal(err)
		}
		b.media()
		bridges = append(bridges, b)
	}
	var wg sync.WaitGroup
	for _, b := range bridges {
		wg.Add(1)
		go func(b *carrierBridgeLease) {
			defer wg.Done()
			b.fail(carrierBridgeEvidence{Kind: "socket_read", Detail: "EOF"})
		}(b)
	}
	wg.Wait()
	for _, b := range bridges {
		fresh, err := a.claimCarrierBridge(&b.row, t.Context())
		if err != nil {
			t.Fatal(err)
		}
		fresh.media()
		b.finish()
		current, _ := a.db().findCall(b.row.ID)
		if current.MediaStatus != "connected" || !bridgeClaimActive(t, a, current.ID) {
			t.Fatal("cross-call interference", current)
		}
	}
}

func TestTelnyxRecoveryActualSocketsKeepAdviserAndBrowser(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, a, "socket-recovery", "telnyx")
	var handlers sync.WaitGroup
	track := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { handlers.Add(1); defer handlers.Done(); h(w, r) }
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/peer/", track(a.handlePeerSocket))
	mux.HandleFunc("/softphone/media/", track(a.handleSoftphoneMedia))
	mux.HandleFunc("/media/telnyx/", track(a.handleTelnyxMediaStream))
	server := httptest.NewServer(mux)
	parsed, _ := url.Parse(server.URL)
	t.Setenv("APTEVA_APP_PORT", parsed.Port())
	t.Cleanup(func() {
		a.stopCarrierBridges()
		server.Close()
		done := make(chan struct{})
		go func() { handlers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("handlers lingered")
		}
	})
	path := server.URL + "/media/telnyx/" + row.ID + "/" + row.CallbackSecret
	first := dialWS(t, path)
	writeRecoveryTelnyxFrame(t, first, row, "first-stream")
	browser := dialWS(t, server.URL+"/softphone/media/"+row.ID+"/"+row.PeerToken)
	readSoftphoneEventWithin(t, browser, "ready", time.Second)
	writeRecoveryTelnyxFrame(t, first, row, "first-stream")
	if pcm := readBinaryWithin(t, browser, time.Second); len(pcm) < 800 {
		t.Fatal("initial audio missing", len(pcm))
	}
	a.mediaBridges.mu.Lock()
	old := a.mediaBridges.current[row.ID]
	a.mediaBridges.mu.Unlock()
	if !a.carrierStreamEvent(row, callbackUpdate{MediaStatus: "error", MediaError: "disconnected", StreamID: "first-stream"}) {
		t.Fatal("failure unhandled")
	}
	readSoftphoneEventWithin(t, browser, "peer.disconnected", 2*time.Second)
	second := dialWS(t, path)
	writeRecoveryTelnyxFrame(t, second, row, "second-stream")
	readSoftphoneEventWithin(t, browser, "peer.connected", time.Second)
	writeRecoveryTelnyxFrame(t, second, row, "second-stream")
	if pcm := readBinaryWithin(t, browser, time.Second); len(pcm) < 800 {
		t.Fatal("replacement audio missing", len(pcm))
	}
	old.finish() // Must not release this socket or erase its live diagnostics.
	current, _ := a.db().findCall(row.ID)
	if current.Status != row.Status || current.ThreadID != row.ThreadID || current.PeerToken != row.PeerToken || current.MediaStatus != "connected" || !bridgeClaimActive(t, a, current.ID) {
		t.Fatal("call/browser changed during recovery", current)
	}
	// Verify browser microphone still travels over the replacement carrier.
	if err := wsutil.WriteClientBinary(browser, pcm16ToBytes(sinePCM(24000, 700, 480*5))); err != nil {
		t.Fatal(err)
	}
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	for {
		data, op, err := wsutil.ReadServerData(second)
		if err != nil {
			t.Fatal("microphone did not resume", err)
		}
		if op == ws.OpText && strings.Contains(string(data), `"event":"media"`) {
			break
		}
	}
	_ = first.Close()
	_ = second.Close()
	_ = browser.Close()
}

func writeRecoveryTelnyxFrame(t *testing.T, conn net.Conn, row *callRow, stream string) {
	t.Helper()
	start, _ := json.Marshal(map[string]any{"event": "start", "stream_id": stream, "start": map[string]string{"call_control_id": row.CarrierSID, "stream_id": stream}})
	if err := wsutil.WriteClientText(conn, start); err != nil {
		t.Fatal(err)
	}
	media, _ := json.Marshal(map[string]any{"event": "media", "stream_id": stream, "media": map[string]string{"payload": base64.StdEncoding.EncodeToString(pcm16ToBytes(sinePCM(16000, 440, 320)))}})
	if err := wsutil.WriteClientText(conn, media); err != nil {
		t.Fatal(err)
	}
}

func TestTelnyxFailureReasonAndStreamIdentityParsed(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/status", strings.NewReader(`{"data":{"id":"event-1","event_type":"streaming.failed","occurred_at":"2026-10-09T12:54:28Z","payload":{"call_control_id":"call-1","stream_id":"stream-1","failure_reason":"disconnected"}}}`))
	u := callbackUpdateFor("telnyx", request)
	if u.MediaError != "disconnected" || u.StreamID != "stream-1" || u.Facts.ProviderEventID != "event-1" || u.Facts.OccurredAt != "2026-10-09T12:54:28Z" {
		t.Fatal("lost provider evidence", u)
	}
}

// Compilation guard: the recovery contract remains an optional capability.
var _ carrierStreamRestarter = (*telnyxCarrier)(nil)

func TestCarrierRecoveryCallEndReconciliationRetainsTruthfulHistory(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cause    error
		provider bool
		normal   bool
	}{
		{"peer-normal", wsutil.ClosedError{Code: 1000, Reason: "call ended"}, false, true},
		{"peer-eof", io.EOF, false, true},
		{"provider-failed", nil, true, false},
		{"peer-abnormal", wsutil.ClosedError{Code: 1012, Reason: "edge restart"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := withTelephonyTestContext(t, &answerPlatform{})
			t.Cleanup(a.stopCarrierBridges)
			row := recoveryCall(t, a, "normal-"+tc.name, "telnyx")
			b, err := a.claimCarrierBridge(row, t.Context())
			if err != nil {
				t.Fatal(err)
			}
			b.media()
			if tc.provider {
				b.fail(carrierBridgeEvidence{Kind: "provider_error", Leg: "provider", Detail: "disconnected"})
			} else {
				b.socketError(mediaCloseLegCarrier, "read", tc.cause)
			}
			if err = a.db().updateStatus(row.ID, "completed", ""); err != nil {
				t.Fatal(err)
			}
			changed, err := a.db().reconcileTerminalCarrierMediaStop(row.ID, ringTime(time.Now()), true)
			if err != nil || changed != tc.normal {
				t.Fatal("incorrect call end classification", changed, err)
			}
			var first string
			if err = a.db().db.QueryRow(`SELECT first_failure_json FROM carrier_media_bridges WHERE generation=?`, b.generation).Scan(&first); err != nil {
				t.Fatal(err)
			}
			if first == "{}" {
				t.Fatal("classification erased original evidence")
			}
		})
	}
}

func TestTelnyxInboundSignedFailureUsesSharedRecovery(t *testing.T) {
	a, _, _, route, row, key := reliabilityFixture(t)
	t.Cleanup(a.stopRoutingDispatcher)
	if err := a.db().updateStatus(row.ID, "answered", ""); err != nil {
		t.Fatal(err)
	}
	b, err := a.claimCarrierBridge(row, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.stopCarrierBridges)
	b.setStream("actual-stream")
	b.media()
	for i := 0; i < 2; i++ {
		r := reliabilityEvent(t, a, route, row, key, "streaming.failed", map[string]any{"stream_id": "actual-stream", "failure_reason": "disconnected"})
		if r.Code != http.StatusNoContent {
			t.Fatal("callback rejected", r.Code, r.Body.String())
		}
	}
	current, _ := a.db().findCall(row.ID)
	if current.MediaErrorMessage != "disconnected" || current.MediaCloseCode == 1011 || bridgeClaimActive(t, a, row.ID) || b.ctx.Err() == nil {
		t.Fatal("inbound failure bypassed recovery", current)
	}
	history, err := a.db().carrierBridgeHistory(row.ProjectID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(history)
	if strings.Count(string(data), `"provider_event_id":"streaming.failed:`) != 2 { // first immutable cause plus one journal entry
		t.Fatal("provider duplicate not coalesced", string(data))
	}
}

type recoveryGatePlatform struct {
	*answerPlatform
	started  chan struct{}
	finished chan struct{}
}

func (p *recoveryGatePlatform) ExecuteIntegrationToolContext(request context.Context, _ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
	if tool != "start_streaming" {
		return nil, errors.New("unexpected recovery command")
	}
	close(p.started)
	<-request.Done()
	close(p.finished)
	return nil, request.Err()
}

func TestCarrierRecoveryNetworkAttemptDoesNotBlockReplacementOrHangup(t *testing.T) {
	for _, action := range []string{"replace", "hangup"} {
		t.Run(action, func(t *testing.T) {
			p := &recoveryGatePlatform{answerPlatform: &answerPlatform{}, started: make(chan struct{}), finished: make(chan struct{})}
			ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(p))
			prev := globalCtx
			globalCtx = ctx
			t.Cleanup(func() { globalCtx = prev })
			a := &App{installID: 42}
			t.Cleanup(a.stopCarrierBridges)
			row := recoveryCall(t, a, "blocked-"+action, "telnyx")
			b, err := a.claimCarrierBridge(row, t.Context())
			if err != nil {
				t.Fatal(err)
			}
			b.fail(carrierBridgeEvidence{Kind: "socket_read", Detail: "EOF"})
			_, _ = a.db().db.Exec(`UPDATE carrier_media_bridges SET next_attempt_at='' WHERE generation=?`, b.generation)
			done := make(chan error, 1)
			go func() { done <- a.restartCarrierMedia(ctx, row.ID, b.generation) }()
			select {
			case <-p.started:
			case <-time.After(time.Second):
				t.Fatal("restart not started")
			}
			if action == "replace" {
				fresh, err := a.claimCarrierBridge(row, t.Context())
				if err != nil {
					t.Fatal("network request blocked replacement", err)
				}
				fresh.media()
			} else {
				if err = a.db().updateStatus(row.ID, "completed", ""); err != nil {
					t.Fatal(err)
				}
				if err = a.killCallThread(ctx, row); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("in-flight request did not cancel")
			}
			select {
			case <-p.finished:
			default:
				t.Fatal("carrier request not canceled")
			}
			if action == "replace" && bridgeState(t, b) != "replaced" {
				t.Fatal("late API result revived old generation")
			}
			if action == "hangup" && bridgeState(t, b) != "ended" {
				t.Fatal("late API result revived ended call")
			}
		})
	}
}

func TestCarrierRecoveryRetainsSocketWriteAndCleanupTelemetry(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, a, "write-evidence", "telnyx")
	b, err := a.claimCarrierBridge(row, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.stopCarrierBridges)
	server, client := net.Pipe()
	defer client.Close()
	b.bind(server)
	p := newWebSocketWriterPump(server, ws.StateServerSide)
	b.trackWriter(mediaCloseLegCarrier, p)
	// A complete carrier frame cannot be written to a stalled peer within the
	// stable transport budget; teardown retains evidence separately from expiry.
	err = p.write(ws.OpText, []byte("carrier-media"), liveAudioWriteTimeout)
	if err == nil {
		t.Fatal("stalled socket accepted write")
	}
	b.socketError(mediaCloseLegCarrier, "write", err)
	newGracefulWebSocket(server, p).Close(ws.StatusGoingAway, "recovering")
	b.finish()
	var encoded string
	if err = a.db().db.QueryRow(`SELECT events_json FROM carrier_media_bridges WHERE generation=?`, b.generation).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var events []carrierBridgeEvidence
	if json.Unmarshal([]byte(encoded), &events) != nil {
		t.Fatal("invalid evidence")
	}
	found := false
	for _, e := range events {
		if e.Kind == "socket_summary" && e.Leg == "carrier" {
			found = true
			if e.Transport == nil || e.Transport.WriteTimeouts != 1 || e.Transport.LastCloseAt == "" || len(e.Transport.Events) == 0 {
				t.Fatalf("lost transport evidence: %+v", e)
			}
			first := e.Transport.Events[0]
			if first.Reason != "socket_write_timeout" || first.ConnectionID != b.generation+":carrier" || first.DeadlineMS != 250 || first.WriteMS < 200 {
				t.Fatalf("wrong timeout evidence: %+v", first)
			}
		}
	}
	if !found {
		t.Fatal("generation lacks socket summary")
	}
	if strings.Contains(encoded, row.CallbackSecret) || strings.Contains(encoded, row.PeerToken) {
		t.Fatal("diagnostic contains credentials")
	}
}
