package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// softphoneTestServer mounts the two softphone WebSocket routes on an httptest
// server so tests can drive them exactly as the carrier bridge and the operator
// browser do in production.
func softphoneTestServer(t *testing.T, app *App) *httptest.Server {
	t.Helper()
	// httptest.Server.Close does not wait for hijacked (WebSocket) connections,
	// so without an explicit drain the handler goroutines outlive the test and
	// read globalCtx while the test's cleanup is restoring it. Cleanups run
	// LIFO, so this drain runs after dialWS closes its sockets and before
	// softphoneTestCtx swaps globalCtx back.
	var live sync.WaitGroup
	tracked := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			live.Add(1)
			defer live.Done()
			h(w, r)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/peer/", tracked(app.handlePeerSocket))
	mux.HandleFunc("/softphone/media/", tracked(app.handleSoftphoneMedia))
	server := httptest.NewServer(mux)
	t.Cleanup(func() {
		server.Close()
		drained := make(chan struct{})
		go func() { live.Wait(); close(drained) }()
		select {
		case <-drained:
		case <-time.After(5 * time.Second):
			t.Error("softphone handlers did not exit after their sockets closed")
		}
	})
	return server
}

// dialWS connects as the carrier bridge or the browser would.
//
// ws.Dial returns a *bufio.Reader alongside the connection, and the server here
// writes its first frame the moment the socket opens — so those bytes can
// already be buffered by the time Dial returns. Reading the raw conn would skip
// them and resume mid-frame ("use of reserved op code"), so reads go through the
// buffer while writes and Close stay on the socket.
func dialWS(t *testing.T, httpURL string) net.Conn {
	t.Helper()
	conn, br, _, err := ws.Dial(t.Context(), "ws"+strings.TrimPrefix(httpURL, "http"))
	if err != nil {
		t.Fatalf("dial %s: %v", httpURL, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if br != nil {
		return hijackedConn{Conn: conn, reader: br}
	}
	return conn
}

// readBinaryWithin reads server frames until a binary one arrives, ignoring the
// text status events the softphone sends (`ready`, `call.ended`, ...).
func readBinaryWithin(t *testing.T, conn net.Conn, within time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for binary frame")
		}
		_ = conn.SetReadDeadline(deadline)
		data, op, err := wsutil.ReadServerData(conn)
		if err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if op == ws.OpBinary {
			return data
		}
	}
}

func readSoftphoneEventWithin(t *testing.T, conn net.Conn, kind string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		_ = conn.SetReadDeadline(deadline)
		data, op, err := wsutil.ReadServerData(conn)
		if err != nil {
			t.Fatalf("read %s event: %v", kind, err)
		}
		if op != ws.OpText {
			continue
		}
		var event map[string]string
		if json.Unmarshal(data, &event) == nil && event["type"] == kind {
			return
		}
	}
}

func softphoneTestCtx(t *testing.T) *sdk.AppCtx {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"))
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	return ctx
}

func insertSoftphoneCall(t *testing.T, app *App, status string) callRow {
	t.Helper()
	row := callRow{
		ID: "call-soft-1", Direction: "outbound", CarrierSlug: "twilio",
		CarrierConnectionID: 10, CallbackSecret: "cb-secret",
		ToNumber: "+13334445555", FromNumber: "+13502231050",
		IngressPath: "outbound", AudioBridgeURL: "ws://127.0.0.1:8080/peer/call-soft-1/peer-secret",
		Status: status, PlacedAt: time.Now().UTC().Format(time.RFC3339),
		ProjectID: "project-a", PeerKind: peerKindHuman, PeerToken: "peer-secret",
	}
	if err := app.db().insertCall(row); err != nil {
		t.Fatalf("insert softphone call: %v", err)
	}
	return row
}

// The whole design rests on audio crossing the hub unchanged in both
// directions: caller audio arriving on the bridge-facing peer socket must reach
// the operator's browser, and microphone audio must reach the bridge.
func TestSoftphoneHubBridgesAudioBothDirections(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)

	peer := dialWS(t, server.URL+"/peer/call-soft-1/peer-secret")
	browser := dialWS(t, server.URL+"/softphone/media/call-soft-1/peer-secret")

	callerAudio := pcm16ToBytes([]int16{100, -100, 2000, -2000})
	if err := wsutil.WriteClientBinary(peer, callerAudio); err != nil {
		t.Fatalf("write caller audio: %v", err)
	}
	if got := readBinaryWithin(t, browser, 3*time.Second); string(got) != string(callerAudio) {
		t.Fatalf("browser got %v, want %v", bytesToPCM16(got), bytesToPCM16(callerAudio))
	}

	micAudio := pcm16ToBytes([]int16{7, 8, 9, -10})
	if err := wsutil.WriteClientBinary(browser, micAudio); err != nil {
		t.Fatalf("write mic audio: %v", err)
	}
	if got := readBinaryWithin(t, peer, 3*time.Second); string(got) != string(micAudio) {
		t.Fatalf("peer got %v, want %v", bytesToPCM16(got), bytesToPCM16(micAudio))
	}
}

func TestOutboundSoftphoneHoldsMicrophoneUntilCarrierAnswers(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	insertSoftphoneCall(t, app, "initiated")
	server := softphoneTestServer(t, app)

	peer := dialWS(t, server.URL+"/peer/call-soft-1/peer-secret")
	browser := dialWS(t, server.URL+"/softphone/media/call-soft-1/peer-secret")
	readSoftphoneEventWithin(t, browser, "ready", 3*time.Second)

	preAnswer := pcm16ToBytes(make([]int16, 480))
	if err := wsutil.WriteClientBinary(browser, preAnswer); err != nil {
		t.Fatal(err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, _, err := wsutil.ReadServerData(peer); err == nil {
		t.Fatal("pre-answer microphone audio reached the carrier peer")
	}

	if err := app.db().updateStatus("call-soft-1", "answered", ""); err != nil {
		t.Fatal(err)
	}
	postAnswer := pcm16ToBytes([]int16{11, 22, 33, 44})
	if err := wsutil.WriteClientBinary(browser, postAnswer); err != nil {
		t.Fatal(err)
	}
	if got := readBinaryWithin(t, peer, 3*time.Second); string(got) != string(postAnswer) {
		t.Fatalf("post-answer microphone audio=%v, want %v", bytesToPCM16(got), bytesToPCM16(postAnswer))
	}
	hub := app.softphones.lookup("call-soft-1")
	if hub == nil || hub.preAnswerDroppedMS() < 20 {
		t.Fatalf("pre-answer microphone hold was not measured: hub=%#v", hub)
	}
}

func TestSoftphonePersistsBoundedBrowserAudioDiagnostics(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)
	browser := dialWS(t, server.URL+"/softphone/media/call-soft-1/peer-secret")

	payload := `{"type":"diagnostics","diagnostics":{"rtt_ms":73,"playback_queue_ms":55,"playback_target_ms":60,"playback_underruns":2,"playback_dropped_ms":20,"audio_context_rate":24000,"microphone_sample_rate":48000,"microphone_channel_count":1,"echo_cancellation":true,"noise_suppression":false,"auto_gain_control":true,"mic_active_rms_dbfs":-22.5,"mic_peak_dbfs":-2.0}}`
	if err := wsutil.WriteClientText(browser, []byte(payload)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		row, err := app.db().findCall("call-soft-1")
		if err != nil {
			t.Fatal(err)
		}
		if row != nil && row.BrowserAudioDiagnostics != "{}" {
			var diagnostics browserAudioDiagnostics
			if err := json.Unmarshal([]byte(row.BrowserAudioDiagnostics), &diagnostics); err != nil {
				t.Fatal(err)
			}
			if diagnostics.RTTMS == nil || *diagnostics.RTTMS != 73 || diagnostics.MicActiveRMSDBFS == nil || *diagnostics.MicActiveRMSDBFS != -22.5 || diagnostics.AutoGainControl == nil || !*diagnostics.AutoGainControl {
				t.Fatalf("persisted diagnostics=%#v", diagnostics)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("browser diagnostics were not persisted")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The bridge sends TTS pacing control frames that mean nothing to a human. They
// must be consumed by the hub, never forwarded, or the browser would have to
// know the realtime protocol.
func TestSoftphoneHubDropsRealtimePacingControlFrames(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)

	peer := dialWS(t, server.URL+"/peer/call-soft-1/peer-secret")
	browser := dialWS(t, server.URL+"/softphone/media/call-soft-1/peer-secret")

	for _, kind := range []string{"input.speech_started", "playback.progress", "playback.overflow"} {
		payload, _ := json.Marshal(realtimeBridgeControl{Type: kind})
		if err := wsutil.WriteClientText(peer, payload); err != nil {
			t.Fatalf("write %s: %v", kind, err)
		}
	}
	// A binary frame behind the control frames proves ordering held and the
	// controls were swallowed rather than merely delayed.
	marker := pcm16ToBytes([]int16{42})
	if err := wsutil.WriteClientBinary(peer, marker); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	_ = browser.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		data, op, err := wsutil.ReadServerData(browser)
		if err != nil {
			t.Fatalf("read browser frame: %v", err)
		}
		if op == ws.OpBinary {
			if string(data) != string(marker) {
				t.Fatalf("unexpected binary payload %v", bytesToPCM16(data))
			}
			return
		}
		var event map[string]string
		if json.Unmarshal(data, &event) == nil && event["type"] == "ready" {
			continue // the hub's own hello frame
		}
		t.Fatalf("realtime pacing control leaked to the browser: %s", data)
	}
}

// Only `interrupt` may cross from the browser into the bridge control channel.
// Anything else would let a compromised tab drive the realtime protocol.
func TestSoftphoneHubForwardsOnlyInterruptFromBrowser(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)

	peer := dialWS(t, server.URL+"/peer/call-soft-1/peer-secret")
	browser := dialWS(t, server.URL+"/softphone/media/call-soft-1/peer-secret")

	for _, payload := range []string{
		`{"type":"audio.frame","item_id":"spoof"}`,
		`{"type":"playback.progress"}`,
		`not json at all`,
	} {
		if err := wsutil.WriteClientText(browser, []byte(payload)); err != nil {
			t.Fatalf("write %s: %v", payload, err)
		}
	}
	if err := wsutil.WriteClientText(browser, []byte(`{"type":"interrupt"}`)); err != nil {
		t.Fatalf("write interrupt: %v", err)
	}

	_ = peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	data, op, err := wsutil.ReadServerData(peer)
	if err != nil {
		t.Fatalf("read peer frame: %v", err)
	}
	if op != ws.OpText {
		t.Fatalf("expected text control, got op %v", op)
	}
	var control realtimeBridgeControl
	if err := json.Unmarshal(data, &control); err != nil {
		t.Fatalf("decode control: %v", err)
	}
	if control.Type != "interrupt" {
		t.Fatalf("first forwarded control was %q, want interrupt (spoofed frames leaked)", control.Type)
	}
}

func TestSoftphoneBrowserPingReportsRoundTripWithoutReachingPeer(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)

	peer := dialWS(t, server.URL+"/peer/call-soft-1/peer-secret")
	browser := dialWS(t, server.URL+"/softphone/media/call-soft-1/peer-secret")
	if err := wsutil.WriteClientText(browser, []byte(`{"type":"ping","nonce":123.5}`)); err != nil {
		t.Fatal(err)
	}

	_ = browser.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		data, op, err := wsutil.ReadServerData(browser)
		if err != nil {
			t.Fatal(err)
		}
		if op != ws.OpText {
			continue
		}
		var event struct {
			Type  string  `json:"type"`
			Nonce float64 `json:"nonce"`
		}
		if json.Unmarshal(data, &event) == nil && event.Type == "pong" {
			if event.Nonce != 123.5 {
				t.Fatalf("pong nonce=%v", event.Nonce)
			}
			break
		}
	}
	_ = peer.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err := wsutil.ReadServerData(peer); err == nil {
		t.Fatal("browser ping leaked into carrier audio peer")
	}
}

// A reloading tab must be able to rejoin a live call: the carrier leg stays up
// and audio resumes on the new socket.
func TestSoftphoneBrowserReconnectResumesAudio(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)

	peer := dialWS(t, server.URL+"/peer/call-soft-1/peer-secret")
	first := dialWS(t, server.URL+"/softphone/media/call-soft-1/peer-secret")
	_ = first.Close()

	// Give the server a moment to observe the closed socket before rejoining.
	time.Sleep(150 * time.Millisecond)
	second := dialWS(t, server.URL+"/softphone/media/call-soft-1/peer-secret")

	audio := pcm16ToBytes([]int16{11, 22, 33})
	if err := wsutil.WriteClientBinary(peer, audio); err != nil {
		t.Fatalf("write caller audio: %v", err)
	}
	if got := readBinaryWithin(t, second, 3*time.Second); string(got) != string(audio) {
		t.Fatalf("reconnected browser got %v, want %v", bytesToPCM16(got), bytesToPCM16(audio))
	}
}

func TestSoftphonePeerReconnectKeepsBrowserAttached(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)

	firstPeer := dialWS(t, server.URL+"/peer/call-soft-1/peer-secret")
	browser := dialWS(t, server.URL+"/softphone/media/call-soft-1/peer-secret")
	readSoftphoneEventWithin(t, browser, "ready", 3*time.Second)

	if err := firstPeer.Close(); err != nil {
		t.Fatal(err)
	}
	readSoftphoneEventWithin(t, browser, "peer.disconnected", 3*time.Second)

	secondPeer := dialWS(t, server.URL+"/peer/call-soft-1/peer-secret")
	readSoftphoneEventWithin(t, browser, "peer.connected", 3*time.Second)
	audio := pcm16ToBytes([]int16{41, 42, 43})
	if err := wsutil.WriteClientBinary(secondPeer, audio); err != nil {
		t.Fatalf("write reconnected peer audio: %v", err)
	}
	if got := readBinaryWithin(t, browser, 3*time.Second); string(got) != string(audio) {
		t.Fatalf("browser got %v after peer reconnect, want %v", bytesToPCM16(got), bytesToPCM16(audio))
	}
}

func TestSoftphoneMediaRejectsBadToken(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)

	for name, path := range map[string]string{
		"browser wrong token": "/softphone/media/call-soft-1/wrong-secret",
		"peer wrong token":    "/peer/call-soft-1/wrong-secret",
		"unknown call":        "/softphone/media/call-nope/peer-secret",
	} {
		t.Run(name, func(t *testing.T) {
			res, err := http.Get(server.URL + path)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer res.Body.Close()
			if res.StatusCode == http.StatusSwitchingProtocols || res.StatusCode == http.StatusOK {
				t.Fatalf("expected rejection, got %d", res.StatusCode)
			}
		})
	}
}

// The browser must never be able to open an audio path on an agent's call.
func TestSoftphoneMediaRejectsRealtimeCall(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	row := callRow{
		ID: "call-agent-1", Direction: "outbound", CarrierSlug: "twilio",
		CallbackSecret: "cb", ToNumber: "+13334445555", FromNumber: "+13502231050",
		AudioBridgeURL: "wss://core.example/bridge", Status: "in-progress",
		PlacedAt: time.Now().UTC().Format(time.RFC3339), ProjectID: "project-a",
		PeerKind: peerKindRealtime, PeerToken: "peer-secret",
	}
	if err := app.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	server := softphoneTestServer(t, app)

	res, err := http.Get(server.URL + "/softphone/media/call-agent-1/peer-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("realtime call accepted a softphone socket: status %d", res.StatusCode)
	}
}

// mediaBridgeURL is the single seam every carrier bridge resolves through.
// Human calls must short-circuit to loopback; realtime calls must keep their
// existing behavior byte for byte.
func TestMediaBridgeURLRoutesHumanCallsToLoopback(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}

	human := &callRow{ID: "c1", PeerKind: peerKindHuman, PeerToken: "tok", MediaStatus: "idle"}
	got, err := app.mediaBridgeURL(human)
	if err != nil {
		t.Fatalf("human bridge url: %v", err)
	}
	if !strings.HasPrefix(got, "ws://127.0.0.1:") || !strings.HasSuffix(got, "/peer/c1/tok") {
		t.Fatalf("human bridge url = %q, want loopback /peer/c1/tok", got)
	}

	// A disconnected human call must NOT attempt a Core renewal — that path
	// would call the platform for a thread that does not exist.
	humanDisconnected := &callRow{ID: "c1", PeerKind: peerKindHuman, PeerToken: "tok", MediaStatus: "disconnected"}
	if _, err := app.mediaBridgeURL(humanDisconnected); err != nil {
		t.Fatalf("disconnected human call attempted realtime renewal: %v", err)
	}

	realtime := &callRow{ID: "c2", PeerKind: peerKindRealtime, AudioBridgeURL: "wss://core.example/b", MediaStatus: "idle"}
	if got, err := app.mediaBridgeURL(realtime); err != nil || got != "wss://core.example/b" {
		t.Fatalf("realtime bridge url = %q, %v; want unchanged Core URL", got, err)
	}
}

func TestSoftphoneListenPortFollowsPlatformInjectedPort(t *testing.T) {
	t.Setenv("APTEVA_APP_PORT", "39912")
	if got := softphoneListenPort(); got != 39912 {
		t.Fatalf("softphoneListenPort() = %d, want 39912", got)
	}
	_ = os.Unsetenv("APTEVA_APP_PORT")
	if got := softphoneListenPort(); got != 8080 {
		t.Fatalf("softphoneListenPort() fallback = %d, want 8080", got)
	}
}

// Concurrent Answer clicks must resolve to exactly one winner, and the agent
// claim path must keep its agent_id scoping.
func TestClaimPendingCallForHumanIsSingleWinner(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	row := callRow{
		ID: "call-in-1", Direction: "inbound", CarrierSlug: "twilio",
		CallbackSecret: "cb", ToNumber: "+13502231050", FromNumber: "+13334445555",
		ThreadID: "pending-call-in-1", AudioBridgeURL: "pending", Status: "pending",
		PlacedAt: time.Now().UTC().Format(time.RFC3339), ProjectID: "project-a",
		AgentID: 7, PeerKind: peerKindHuman,
	}
	if err := app.db().insertCall(row); err != nil {
		t.Fatal(err)
	}

	first, err := app.db().claimPendingCallForHuman("call-in-1", "project-a")
	if err != nil || !first {
		t.Fatalf("first claim = %v, %v; want true", first, err)
	}
	second, err := app.db().claimPendingCallForHuman("call-in-1", "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if second {
		t.Fatal("second claim also won — the answer race is not atomic")
	}
}

// An inbound human call must stay parked at the carrier until the browser has
// both microphone access and an open media socket. Opening that socket is what
// finally sends the provider's answer command and moves the call to answered.
func TestInboundSoftphoneAnswersCarrierWhenBrowserMediaIsReady(t *testing.T) {
	for _, test := range []struct {
		provider string
		wantTool string
	}{
		{provider: "telnyx", wantTool: "answer_call"},
		{provider: "twilio", wantTool: "update_call"},
		{provider: "plivo", wantTool: "update_call"},
	} {
		t.Run(test.provider, func(t *testing.T) {
			platform := &answerPlatform{}
			app, _ := withTelephonyTestContext(t, platform)
			callID := "call-browser-answer-" + test.provider
			// v0.2.4 stored the first browser call with an empty thread id.
			// Keep that legacy row present so a regression to another empty id
			// reproduces the production UNIQUE constraint failure.
			legacy := testCall("legacy-browser-"+test.provider, "completed")
			legacy.ThreadID = ""
			legacy.PeerKind = peerKindHuman
			if err := app.db().insertCall(legacy); err != nil {
				t.Fatal(err)
			}
			row := callRow{
				ID: callID, ThreadID: "pending-" + callID, Direction: "inbound", CarrierSlug: test.provider,
				CarrierConnectionID: 10, CarrierSID: test.provider + "-call-1", CallbackSecret: "callback-secret",
				ToNumber: "+33123456789", FromNumber: "+33612345678", Status: "pending",
				PlacedAt: time.Now().UTC().Format(time.RFC3339), ProjectID: "project-a",
				PeerKind: peerKindHuman, AudioBridgeURL: "pending",
			}
			if err := app.db().insertCall(row); err != nil {
				t.Fatal(err)
			}
			claimed, err := app.db().claimPendingCallForHuman(callID, "project-a")
			if err != nil || !claimed {
				t.Fatalf("claim = %v, %v", claimed, err)
			}
			if err := app.db().attachHumanCall(callID,
				"ws://127.0.0.1:8080/peer/"+callID+"/browser-token", "browser-token"); err != nil {
				t.Fatalf("attach browser call alongside legacy empty thread id: %v", err)
			}
			server := softphoneTestServer(t, app)
			browser := dialWS(t, server.URL+"/softphone/media/"+callID+"/browser-token")

			_ = browser.SetReadDeadline(time.Now().Add(3 * time.Second))
			data, op, err := wsutil.ReadServerData(browser)
			if err != nil {
				t.Fatal(err)
			}
			if op != ws.OpText || !strings.Contains(string(data), `"type":"ready"`) {
				t.Fatalf("browser event = %s (op=%v), want ready", data, op)
			}
			stored, err := app.db().findCall(row.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Status != "answered" || stored.AnsweredAt == "" || stored.ThreadID != "human-"+callID {
				t.Fatalf("call was not marked answered: %+v", stored)
			}
			if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != test.wantTool {
				t.Fatalf("carrier calls = %#v, want one %s %s", platform.integrationCalls, test.provider, test.wantTool)
			}
			if test.provider == "telnyx" {
				streamURL, _ := platform.integrationCalls[0].Input["stream_url"].(string)
				if !strings.Contains(streamURL, "/media/telnyx/") {
					t.Fatalf("Telnyx answer stream_url = %q", streamURL)
				}
			}
		})
	}
}

func TestInboundSoftphoneCarrierAnswerFailureReturnsCallToPending(t *testing.T) {
	platform := &answerPlatform{failTool: "answer_call"}
	app, _ := withTelephonyTestContext(t, platform)
	row := callRow{
		ID: "call-browser-retry", ThreadID: "pending-call-browser-retry", Direction: "inbound", CarrierSlug: "telnyx",
		CarrierConnectionID: 10, CarrierSID: "telnyx-call-2", CallbackSecret: "callback-secret",
		ToNumber: "+33123456789", FromNumber: "+33612345678", Status: "pending",
		PlacedAt: time.Now().UTC().Format(time.RFC3339), ProjectID: "project-a",
		PeerKind: peerKindHuman, AudioBridgeURL: "pending",
	}
	if err := app.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	claimed, err := app.db().claimPendingCallForHuman(row.ID, row.ProjectID)
	if err != nil || !claimed {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	if err := app.db().attachHumanCall(row.ID,
		"ws://127.0.0.1:8080/peer/call-browser-retry/failed-token", "failed-token"); err != nil {
		t.Fatal(err)
	}
	server := softphoneTestServer(t, app)
	browser := dialWS(t, server.URL+"/softphone/media/call-browser-retry/failed-token")

	_ = browser.SetReadDeadline(time.Now().Add(3 * time.Second))
	data, op, err := wsutil.ReadServerData(browser)
	if err != nil {
		t.Fatal(err)
	}
	if op != ws.OpText || !strings.Contains(string(data), `"type":"call.error"`) {
		t.Fatalf("browser event = %s (op=%v), want call.error", data, op)
	}
	stored, err := app.db().findCall(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "pending" || stored.AudioBridgeURL != "pending" || stored.PeerToken != "" {
		t.Fatalf("failed answer was not reset safely: %+v", stored)
	}
}

func TestBrowserDeclineUsesProviderRejectForPendingInboundCalls(t *testing.T) {
	for _, test := range []struct {
		provider string
		wantTool string
	}{
		{provider: "telnyx", wantTool: "reject_call"},
		{provider: "twilio", wantTool: "update_call"},
		{provider: "plivo", wantTool: "hangup_call"},
	} {
		t.Run(test.provider, func(t *testing.T) {
			platform := &answerPlatform{}
			app, _ := withTelephonyTestContext(t, platform)
			callID := "call-browser-decline-" + test.provider
			row := callRow{
				ID: callID, ThreadID: "pending-" + callID, Direction: "inbound", CarrierSlug: test.provider,
				CarrierConnectionID: 10, CarrierSID: test.provider + "-call-2", CallbackSecret: "callback-secret",
				ToNumber: "+33123456789", FromNumber: "+33612345678", Status: "pending",
				PlacedAt: time.Now().UTC().Format(time.RFC3339), ProjectID: "project-a",
				PeerKind: peerKindHuman, AudioBridgeURL: "pending",
			}
			if err := app.db().insertCall(row); err != nil {
				t.Fatal(err)
			}

			req := httptest.NewRequest(http.MethodPost,
				"/calls/"+callID+"/hangup?project_id=project-a", nil)
			recorder := httptest.NewRecorder()
			app.handleCallAction(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			stored, err := app.db().findCall(callID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Status != "canceled" || stored.ErrorMessage != "" {
				t.Fatalf("declined call = %+v", stored)
			}
			if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != test.wantTool {
				t.Fatalf("carrier calls = %#v, want one %s %s", platform.integrationCalls, test.provider, test.wantTool)
			}
		})
	}
}

func TestCallsPanelHidesBenignHistoricalMediaShutdownErrors(t *testing.T) {
	for _, message := range []string{"media stream stopped", "media stream disconnected", "call media ended normally"} {
		row := testCall("historical-media", "completed")
		row.ErrorMessage = message
		if got := callsPanelErrorMessage(row); got != "" {
			t.Fatalf("callsPanelErrorMessage(%q) = %q", message, got)
		}
	}
	row := testCall("real-error", "failed")
	row.ErrorMessage = "carrier authentication failed"
	if got := callsPanelErrorMessage(row); got != row.ErrorMessage {
		t.Fatalf("real call error was hidden: %q", got)
	}
}

func TestSoftphoneMediaURLUsesInstallScopedSameOriginPath(t *testing.T) {
	app := &App{installID: 42}
	got := app.softphoneMediaURL("call-1", "browser-token")
	want := "/api/apps/telephony/_install/42/softphone/media/call-1/browser-token"
	if got != want {
		t.Fatalf("softphoneMediaURL() = %q, want %q", got, want)
	}
	if strings.Contains(got, "://") {
		t.Fatalf("browser media URL must stay on the panel origin: %q", got)
	}

	if got := (&App{}).softphoneMediaURL("call-2", "token-2"); got != "/softphone/media/call-2/token-2" {
		t.Fatalf("uninstalled softphoneMediaURL() = %q", got)
	}
}

func TestSoftphoneUsesSameOriginWorkletAsset(t *testing.T) {
	audioSource, err := os.ReadFile("ui/softphone-audio.ts")
	if err != nil {
		t.Fatal(err)
	}
	panelSource, err := os.ReadFile("ui/CallsPanel.tsx")
	if err != nil {
		t.Fatal(err)
	}
	audioText := string(audioSource)
	panelText := string(panelSource)
	if !strings.Contains(panelText, `/api/apps/telephony/_install/${encodeURIComponent(String(installId))}/ui/softphone-worklet.js`) {
		t.Fatal("softphone worklet URL is not pinned to the active install")
	}
	if !strings.Contains(audioText, `audioWorklet.addModule(workletURL)`) {
		t.Fatal("softphone does not load the explicit install-scoped worklet URL")
	}
	for _, forbidden := range []string{"createObjectURL", "new Blob([WORKLET_SOURCE]", "import.meta.url"} {
		if strings.Contains(audioText, forbidden) {
			t.Fatalf("softphone still uses CSP-sensitive blob worklet code %q", forbidden)
		}
	}
	if strings.Contains(panelText, "setStatus(`${list.length} calls`)") {
		t.Fatal("routine polling still overwrites actionable browser audio errors")
	}
	if info, err := os.Stat("ui/softphone-worklet.js"); err != nil || info.Size() == 0 {
		t.Fatalf("softphone worklet asset is missing or empty: %v", err)
	}
}

func TestSoftphoneRetriesTransientBrowserMediaDisconnects(t *testing.T) {
	audioSource, err := os.ReadFile("ui/softphone-audio.ts")
	if err != nil {
		t.Fatal(err)
	}
	panelSource, err := os.ReadFile("ui/CallsPanel.tsx")
	if err != nil {
		t.Fatal(err)
	}
	audioText := string(audioSource)
	for _, required := range []string{
		`"connecting" | "reconnecting" | "live"`,
		"MAX_RECONNECT_MS",
		"scheduleReconnect()",
		"Connection interrupted; retrying…",
		"Audio reconnected",
	} {
		if !strings.Contains(audioText, required) {
			t.Fatalf("softphone reconnect contract missing %q", required)
		}
	}
	if !strings.Contains(string(panelSource), "Reconnecting audio…") {
		t.Fatal("panel hides the live audio card while the socket reconnects")
	}
}

func TestSoftphoneUsesAdaptiveJitterAndVisibleDiagnostics(t *testing.T) {
	worklet, err := os.ReadFile("ui/softphone-worklet.js")
	if err != nil {
		t.Fatal(err)
	}
	audio, err := os.ReadFile("ui/softphone-audio.ts")
	if err != nil {
		t.Fatal(err)
	}
	panel, err := os.ReadFile("ui/CallsPanel.tsx")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"target_ms", "underruns", "dropped_ms", "stableSamples", "dropOldest"} {
		if !strings.Contains(string(worklet), required) {
			t.Fatalf("adaptive jitter worklet missing %q", required)
		}
	}
	for _, required := range []string{"onDiagnostics", `type: "ping"`, "noiseSuppression: false", "autoGainControl: false", `type: "diagnostics"`} {
		if !strings.Contains(string(audio), required) {
			t.Fatalf("softphone diagnostics/audio controls missing %q", required)
		}
	}
	for _, required := range []string{"Browser audio processing", "underruns", "buffer"} {
		if !strings.Contains(string(panel), required) {
			t.Fatalf("panel diagnostics missing %q", required)
		}
	}
}

func TestSoftphoneRecommendedAudioMigrationAndLocalMicrophoneTest(t *testing.T) {
	audio, err := os.ReadFile("ui/softphone-audio.ts")
	if err != nil {
		t.Fatal(err)
	}
	panel, err := os.ReadFile("ui/CallsPanel.tsx")
	if err != nil {
		t.Fatal(err)
	}
	audioText := string(audio)
	panelText := string(panel)
	settings, err := os.ReadFile("ui/audio-settings.ts")
	if err != nil {
		t.Fatal(err)
	}
	settingsText := string(settings)
	for _, required := range []string{"MicrophoneTestSession", "pcm16WAV", `type: "audio/wav"`, "autoGainControl: false"} {
		if !strings.Contains(audioText, required) {
			t.Fatalf("local microphone pipeline missing %q", required)
		}
	}
	for _, required := range []string{
		"Microphone test", "never uploaded", "Saved audio diagnostics",
	} {
		if !strings.Contains(panelText, required) {
			t.Fatalf("recommended audio UI/migration missing %q", required)
		}
	}
	for _, required := range []string{"apteva.telephony.softphone.audio.v3", "PREVIOUS_AUDIO_SETTINGS_KEY", "autoGainControl: false"} {
		if !strings.Contains(settingsText, required) {
			t.Fatalf("recommended audio migration missing %q", required)
		}
	}
}

func TestSoftphoneBrowserSetupFailureReleasesAnswerClaim(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	row := callRow{
		ID: "call-release-browser", ThreadID: "pending-call-release-browser",
		Direction: "inbound", CarrierSlug: "telnyx", CarrierConnectionID: 10,
		CarrierSID: "telnyx-release-1", CallbackSecret: "callback-secret",
		ToNumber: "+33123456789", FromNumber: "+33612345678", Status: "pending",
		PlacedAt: time.Now().UTC().Format(time.RFC3339), ProjectID: "project-a",
		PeerKind: peerKindHuman, AudioBridgeURL: "pending",
	}
	if err := app.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	claimed, err := app.db().claimPendingCallForHuman(row.ID, row.ProjectID)
	if err != nil || !claimed {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	if err := app.db().attachHumanCall(row.ID, "ws://127.0.0.1/peer/call-release-browser/session-token", "session-token"); err != nil {
		t.Fatal(err)
	}

	wrong := httptest.NewRequest(http.MethodPost, "/softphone/release/"+row.ID,
		strings.NewReader(`{"session_token":"wrong"}`))
	wrong.Header.Set("X-Apteva-Project-ID", row.ProjectID)
	wrongRecorder := httptest.NewRecorder()
	app.softphoneReleaseAnswer(wrongRecorder, wrong, row.ProjectID, row.ID)
	if wrongRecorder.Code != http.StatusConflict {
		t.Fatalf("wrong token status = %d, want %d", wrongRecorder.Code, http.StatusConflict)
	}

	req := httptest.NewRequest(http.MethodPost, "/softphone/release/"+row.ID,
		strings.NewReader(`{"session_token":"session-token"}`))
	req.Header.Set("X-Apteva-Project-ID", row.ProjectID)
	recorder := httptest.NewRecorder()
	app.softphoneReleaseAnswer(recorder, req, row.ProjectID, row.ID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("release status = %d: %s", recorder.Code, recorder.Body.String())
	}
	stored, err := app.db().findCall(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "pending" || stored.ThreadID != "pending-"+row.ID || stored.PeerToken != "" || stored.AudioBridgeURL != "pending" {
		t.Fatalf("browser setup failure was not made retryable: %+v", stored)
	}
}

func TestRepeatedOutboundSoftphoneCallsUseUniqueHumanThreadIDs(t *testing.T) {
	platform := &answerPlatform{
		bindings: map[string]any{"carrier": int64(9)},
		credentials: &sdk.ConnectionCredentials{
			Slug: "twilio", Fields: map[string]string{
				"auth_token": "test-auth-token", "phone_number": "+14155550101",
			},
		},
		integrationResponse: map[string]json.RawMessage{
			"make_call": json.RawMessage(`{"sid":"CA-outbound"}`),
		},
	}
	app, ctx := withTelephonyTestContext(t, platform)
	legacy := testCall("legacy-empty-thread", "completed")
	legacy.ThreadID = ""
	legacy.Direction = "inbound"
	legacy.PeerKind = peerKindHuman
	if err := app.db().insertCall(legacy); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for index := 0; index < 2; index++ {
		session, err := app.placeHumanCall(ctx, "project-a", "+14155550100", "+14155550101", 60, nil)
		if err != nil {
			t.Fatalf("outbound call %d failed: %v", index+1, err)
		}
		row, err := app.db().findCall(session.CallID)
		if err != nil {
			t.Fatal(err)
		}
		want := "human-" + session.CallID
		if row.ThreadID != want || seen[row.ThreadID] {
			t.Fatalf("outbound call %d thread_id=%q, want unique %q", index+1, row.ThreadID, want)
		}
		seen[row.ThreadID] = true
	}
}

func TestCallsPanelPinsSoftphoneSocketToCurrentOrigin(t *testing.T) {
	source, err := os.ReadFile("ui/CallsPanel.tsx")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"new URL(session.media_url, location.href)",
		"media.host = location.host",
		"media.protocol = location.protocol",
		"phone.start(media.toString(), workletURL, audioOptions)",
		"/softphone/release/",
		"session_token: session.session_token",
	} {
		if !strings.Contains(string(source), required) {
			t.Fatalf("panel does not keep browser media on the current origin: missing %q", required)
		}
	}
}

// A human-routed call must not be answerable into a realtime thread, or the
// caller ends up bridged to a thread nobody is listening to.
func TestPrepareInboundRealtimeRefusesSoftphoneCalls(t *testing.T) {
	ctx := softphoneTestCtx(t)
	app := &App{installID: 42}
	row := &callRow{ID: "call-in-2", Status: "pending", PeerKind: peerKindHuman, ProjectID: "project-a"}

	if _, err := app.prepareInboundRealtime(ctx, row, "be helpful", "", ""); err == nil {
		t.Fatal("agent answered a softphone-routed call")
	}
}

// human_browser routes must not wake an agent; agent routes must still do so.
func TestInboundPeerKindMapping(t *testing.T) {
	for mode, want := range map[string]string{
		answerModeAgent:             peerKindRealtime,
		answerModeRealtimeImmediate: peerKindRealtime,
		answerModeHumanBrowser:      peerKindHuman,
		"":                          peerKindRealtime,
	} {
		if got := inboundPeerKind(mode); got != want {
			t.Errorf("inboundPeerKind(%q) = %q, want %q", mode, got, want)
		}
	}
}

func TestNormalizeRouteAnswerConfigAcceptsHumanBrowserWithoutDirective(t *testing.T) {
	mode, directive, _, _, err := normalizeRouteAnswerConfig(answerModeHumanBrowser, "", "", "")
	if err != nil {
		t.Fatalf("human_browser rejected: %v", err)
	}
	if mode != answerModeHumanBrowser || directive != "" {
		t.Fatalf("got mode=%q directive=%q", mode, directive)
	}
	// The existing modes must keep their exact rules.
	if _, _, _, _, err := normalizeRouteAnswerConfig(answerModeRealtimeImmediate, "", "", ""); err == nil {
		t.Fatal("realtime_immediate no longer requires a directive")
	}
	if _, _, _, _, err := normalizeRouteAnswerConfig("nonsense", "", "", ""); err == nil {
		t.Fatal("invalid answer mode accepted")
	}
}
