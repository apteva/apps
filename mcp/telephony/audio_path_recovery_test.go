package main

import (
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

func pathRecoveryTestSession(t *testing.T, c softphoneRTCConfig) (*rtcICESession, *websocketWriterPump, net.Conn) {
	t.Helper()
	local, remote := net.Pipe()
	t.Cleanup(func() { local.Close(); remote.Close() })
	writer := newWebSocketWriterPump(local, ws.StateServerSide)
	t.Cleanup(writer.Stop)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	s, e := newRTCICESession(t.Context(), c, writer, done)
	if e != nil {
		t.Fatal(e)
	}
	return s, writer, remote
}

func readPathRecoveryMessage(t *testing.T, conn net.Conn) map[string]any {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	raw, _, e := wsutil.ReadServerData(conn)
	if e != nil {
		t.Fatal(e)
	}
	var value map[string]any
	if e := json.Unmarshal(raw, &value); e != nil {
		t.Fatal(e)
	}
	return value
}

func TestPathRecoveryIsOffByDefaultAndOnlyOfferedToAdvisers(t *testing.T) {
	off, e := parseSoftphoneRTCConfig(map[string]string{"softphone_webrtc_enabled": "true"})
	if e != nil || off.PathRecovery || off.Handover {
		t.Fatal("must be off by default", off, e)
	}
	on, e := parseSoftphoneRTCConfig(map[string]string{"softphone_webrtc_enabled": "true", "softphone_webrtc_path_recovery": "true"})
	if e != nil || !on.PathRecovery || on.Handover {
		t.Fatal("settings are independent", on, e)
	}
	for _, tc := range []struct {
		config   softphoneRTCConfig
		listener bool
		want     bool
	}{{off, false, false}, {on, false, true}, {on, true, false}} {
		s, _, _ := pathRecoveryTestSession(t, tc.config)
		if got := strings.Contains(string(s.initial(tc.listener)), "ice_recover_supported"); got != tc.want {
			t.Fatalf("listener=%v enabled=%v: advertised=%v", tc.listener, tc.config.PathRecovery, got)
		}
	}
}

func TestPathRecoveryIssuesANewRevisionAndIsRateLimited(t *testing.T) {
	servers := []webrtc.ICEServer{{URLs: []string{"stun:stun.example.test:3478"}}}
	s, writer, remote := pathRecoveryTestSession(t, softphoneRTCConfig{PathRecovery: true, ICEServers: servers})
	start := time.Now()
	s.requestPathRecovery(writer, start)
	first := readPathRecoveryMessage(t, remote)
	if first["type"] != "webrtc.ice_config" || first["recovery"] != true || first["revision"] != float64(2) || first["ice_transport_policy"] != "all" {
		t.Fatal("wrong recovery message", first)
	}
	if !strings.Contains(string(mustJSON(t, first["ice_servers"])), "stun.example.test") {
		t.Fatal("recovery must re-send the servers in use", first)
	}
	if s.revision.Load() != 2 {
		t.Fatal("restart offer would be rejected as stale", s.revision.Load())
	}
	// A second request inside the gap neither issues a revision nor stays silent,
	// because silence is what tells the browser that signaling is gone.
	s.requestPathRecovery(writer, start.Add(rtcPathRecoveryMinGap-time.Millisecond))
	if busy := readPathRecoveryMessage(t, remote); busy["type"] != "webrtc.ice_status" || busy["state"] != "recovery_busy" || s.revision.Load() != 2 {
		t.Fatal("repeat request was not refused", busy)
	}
	at := start
	for range rtcPathRecoveryMax - 1 {
		at = at.Add(rtcPathRecoveryMinGap)
		s.requestPathRecovery(writer, at)
		if v := readPathRecoveryMessage(t, remote); v["recovery"] != true {
			t.Fatal("request inside the budget was refused", v)
		}
	}
	s.requestPathRecovery(writer, at.Add(time.Hour))
	if v := readPathRecoveryMessage(t, remote); v["state"] != "recovery_busy" || s.revision.Load() != uint64(rtcPathRecoveryMax+1) {
		t.Fatal("the per-connection budget was not enforced", v)
	}
	var seen int
	for _, event := range s.snapshot() {
		if event.Outcome == "path_recovery_requested" {
			seen++
		}
	}
	if seen != rtcPathRecoveryMax {
		t.Fatal("each recovery must leave evidence", seen)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, e := json.Marshal(value)
	if e != nil {
		t.Fatal(e)
	}
	return raw
}

// Real peer connections on both sides: a browser asks, restarts ICE with the
// issued revision, and the same connection keeps carrying audio both ways.
func TestPathRecoveryRestartsICEWithoutReplacingTheConnection(t *testing.T) {
	previous := globalCtx
	globalCtx = tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithConfig(map[string]string{"softphone_webrtc_enabled": "true", "softphone_webrtc_path_recovery": "true"}))
	t.Cleanup(func() { globalCtx = previous })
	app := &App{installID: 42}
	row := insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)
	defer server.Close()
	carrier := dialWS(t, server.URL+"/peer/"+row.ID+"/cb-secret")
	browser := rtcTestConnect(t, server.URL+"/softphone/media/"+row.ID+"/peer-secret?transport=webrtc")
	attached := app.softphones.lookup(row.ID).browserWriter()
	sender := browser.pc.GetSenders()[0]
	checkAudio := func() {
		t.Helper()
		for range 8 {
			if e := browser.mic.WriteSample(media.Sample{Data: []byte{0xf8, 0xff, 0xfe}, Duration: 20 * time.Millisecond}); e != nil {
				t.Fatal(e)
			}
			if e := wsutil.WriteClientBinary(carrier, make([]byte, 960)); e != nil {
				t.Fatal(e)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if data := readBinaryWithin(t, carrier, 3*time.Second); len(data) < 960 {
			t.Fatal("microphone audio did not reach the carrier")
		}
		select {
		case <-browser.received:
		case <-time.After(3 * time.Second):
			t.Fatal("caller audio did not reach the browser")
		}
	}
	checkAudio()
	if e := wsutil.WriteClientText(browser.socket, []byte(`{"type":"webrtc.ice_recover"}`)); e != nil {
		t.Fatal(e)
	}
	browser.socket.SetReadDeadline(time.Now().Add(5 * time.Second))
	var issued struct {
		Type     string `json:"type"`
		Revision uint64 `json:"revision"`
		Recovery bool   `json:"recovery"`
	}
	for {
		raw, _, e := wsutil.ReadServerData(browser.socket)
		if e != nil {
			t.Fatal(e)
		}
		if json.Unmarshal(raw, &issued) == nil && issued.Type == "webrtc.ice_config" {
			break
		}
	}
	if !issued.Recovery || issued.Revision != 2 {
		t.Fatal("wrong revision", issued)
	}
	offer, e := browser.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: true})
	if e != nil {
		t.Fatal(e)
	}
	gather := webrtc.GatheringCompletePromise(browser.pc)
	if e = browser.pc.SetLocalDescription(offer); e != nil {
		t.Fatal(e)
	}
	select {
	case <-gather:
	case <-time.After(5 * time.Second):
		t.Fatal("restart gather timeout")
	}
	raw, _ := json.Marshal(map[string]any{"type": "webrtc.restart_offer", "revision": issued.Revision, "sdp": browser.pc.LocalDescription().SDP})
	if e = wsutil.WriteClientText(browser.socket, raw); e != nil {
		t.Fatal(e)
	}
	var answer struct {
		Type     string `json:"type"`
		SDP      string `json:"sdp"`
		Revision uint64 `json:"revision"`
	}
	for {
		data, _, e := wsutil.ReadServerData(browser.socket)
		if e != nil {
			t.Fatal(e)
		}
		if json.Unmarshal(data, &answer) == nil && answer.Type == "webrtc.restart_answer" {
			break
		}
	}
	if answer.Revision != issued.Revision {
		t.Fatal("uncorrelated answer", answer.Revision)
	}
	if e = browser.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(5 * time.Second)
	for browser.pc.ICEConnectionState() != webrtc.ICEConnectionStateConnected && browser.pc.ICEConnectionState() != webrtc.ICEConnectionStateCompleted {
		if time.Now().After(deadline) {
			t.Fatal("restart did not connect", browser.pc.ICEConnectionState())
		}
		time.Sleep(10 * time.Millisecond)
	}
	for len(browser.received) > 0 {
		<-browser.received
	}
	checkAudio()
	if app.softphones.lookup(row.ID).browserWriter() != attached || browser.pc.GetSenders()[0] != sender {
		t.Fatal("path recovery must keep the same attachment and sender")
	}
}
