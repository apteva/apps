package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestNetworkProfilesIsolationAuthorizationAndBounds(t *testing.T) {
	a, ctx, _ := turnTestFixture(t)
	phoneTestPolicy(t, a)
	alice, bob := phoneTestIdentity("alice"), phoneTestIdentity("bob")
	p, err := a.resolvedNetworkPolicy("project-a", alice)
	if err != nil || p.Transport != "inherit" || p.InitialBitrate != 32000 {
		t.Fatal(p, err)
	}
	c := softphoneRTCConfig{Bitrate: 24000}
	if err := a.configureNetworkPolicy(&c, "project-a", alice); err != nil || c.NetworkPolicy != nil || c.Bitrate != 24000 {
		t.Fatal("unsaved defaults changed an existing audio configuration", c, err)
	}
	request := map[string]any{"policy": map[string]any{"media_transport": "auto", "initial_bitrate_bps": 24000, "max_bitrate_bps": 24000, "allow_user_overrides": true, "upload_bitrate_control": "guarded"}}
	if _, err = a.networkPolicies(ctx, request, nil); err != nil {
		t.Fatal(err)
	}
	own := phoneTestRequest(a, &alice, "POST", "/softphone/network-policy", map[string]any{"policy": map[string]any{"initial_bitrate_bps": 20000}})
	if own.Code != 200 {
		t.Fatal(own.Code, own.Body.String())
	}
	p, err = a.resolvedNetworkPolicy("project-a", alice)
	if err != nil || p.InitialBitrate != 20000 {
		t.Fatal(p, err)
	}
	p, _ = a.resolvedNetworkPolicy("project-a", bob)
	if p.InitialBitrate != 24000 || p.UploadBitrateControl != "guarded" {
		t.Fatal("crossed advisers", p)
	}
	p, _ = a.resolvedNetworkPolicy("project-b", alice)
	if p.InitialBitrate != 32000 {
		t.Fatal("crossed projects", p)
	}
	collision := alice
	collision.IssuerInstallID = "another"
	p, _ = a.resolvedNetworkPolicy("project-a", collision)
	if p.InitialBitrate != 24000 {
		t.Fatal("identity tuple lost")
	}
	for _, body := range []map[string]any{{"identity": bob}, {"policy": map[string]any{"allow_user_overrides": true}}, {"policy": map[string]any{"min_bitrate_bps": 8000}}, {"policy": map[string]any{"playback_max_ms": 1000}}, {"policy": map[string]any{"warning_cooldown_seconds": 1}}, {"policy": map[string]any{"unknown": true}}, {"policy": map[string]any{"upload_bitrate_control": "rtt-only"}}} {
		w := phoneTestRequest(a, &alice, "POST", "/softphone/network-policy", body)
		if w.Code < 400 {
			t.Fatal("invalid policy accepted", body, w.Body.String())
		}
	}
	if w := phoneTestRequest(a, &alice, "POST", "/numbers/network-policy", request); w.Code != 403 {
		t.Fatal("user edited admin policy", w.Code)
	}
	if _, err = a.networkPolicies(ctx, map[string]any{"policy": defaultPhoneNetworkPolicy()}, nil); err != nil {
		t.Fatal(err)
	}
	if w := phoneTestRequest(a, &alice, "POST", "/softphone/network-policy", map[string]any{"inherit": true}); w.Code < 400 {
		t.Fatal("override restriction bypassed")
	}
}
func networkPrepare(t *testing.T, a *App, identity *phoneIdentity, path string) string {
	t.Helper()
	w := phoneTestRequest(a, identity, "POST", "/softphone/network-check", map[string]any{"action": "prepare", "path": path})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var v struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(w.Body.Bytes(), &v) != nil || v.ID == "" {
		t.Fatal(w.Body.String())
	}
	return v.ID
}
func TestNetworkCheckAuthQuotasAndNoCallMutation(t *testing.T) {
	a, _, _ := turnTestFixture(t)
	phoneTestPolicy(t, a)
	defer a.networkChecks.stopAll()
	alice, bob := phoneTestIdentity("alice"), phoneTestIdentity("bob")
	id := networkPrepare(t, a, &alice, "direct")
	if w := phoneTestRequest(a, &bob, "POST", "/softphone/network-check", map[string]any{"action": "cancel", "id": id}); w.Code != 404 {
		t.Fatal("cross-user probe access", w.Code)
	}
	if w := phoneTestRequest(a, &alice, "POST", "/softphone/network-check", map[string]any{"action": "prepare", "path": "direct"}); w.Code != 429 {
		t.Fatal("duplicate probe", w.Code)
	}
	if w := phoneTestRequest(a, &alice, "POST", "/softphone/network-check", map[string]any{"action": "cancel", "id": id}); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := phoneTestRequest(a, &alice, "POST", "/softphone/network-check", map[string]any{"action": "offer", "id": id, "sdp": "late"}); w.Code != 404 {
		t.Fatal("cancelled probe revived", w.Code)
	}
	// A different bounded test path is allowed immediately after the first.
	id = networkPrepare(t, a, &alice, "configured")
	a.networkChecks.stopOwner("project-a", alice.key())
	if w := phoneTestRequest(a, &alice, "POST", "/softphone/network-check", map[string]any{"action": "offer", "id": id, "sdp": "late"}); w.Code != 404 {
		t.Fatal(w.Code)
	}
	for _, body := range []map[string]any{{"action": "prepare", "path": "https://attacker.invalid"}, {"action": "prepare", "path": "direct", "project_id": "other"}} {
		if w := phoneTestRequest(a, &alice, "POST", "/softphone/network-check", body); w.Code < 400 {
			t.Fatal("bad path/context accepted")
		}
	}
	var calls int
	if err := a.db().db.QueryRow(`SELECT COUNT(*) FROM calls`).Scan(&calls); err != nil || calls != 0 {
		t.Fatal("probe created calls", calls, err)
	}
	for i := 0; i < 4; i++ {
		if _, err := a.networkChecks.create("project-x", string(rune('a'+i)), softphoneRTCConfig{}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.networkChecks.create("project-x", "excess", softphoneRTCConfig{}, time.Now()); err == nil {
		t.Fatal("capacity unbounded")
	}
}
func TestNetworkCheckOccupiedAndRequiredRelay(t *testing.T) {
	a, ctx, _ := turnTestFixture(t)
	phoneTestPolicy(t, a)
	defer a.networkChecks.stopAll()
	alice := phoneTestIdentity("alice")
	row := phoneTestCall(t, a, "busy-probe", "answered")
	if _, err := ctx.AppDB().Exec(`INSERT INTO telephony_call_owners(call_id,project_id,principal) VALUES(?,?,?)`, row.ID, "project-a", alice.key()); err != nil {
		t.Fatal(err)
	}
	if w := phoneTestRequest(a, &alice, "POST", "/softphone/network-check", map[string]any{"action": "prepare", "path": "configured"}); w.Code != 409 {
		t.Fatal("probe competed with call", w.Code, w.Body.String())
	}
	saveTURNTestPolicy(t, a, ctx, nil, phoneTURNPolicy{Source: "app", ICEPolicy: "relay"})
	if _, err := a.networkCheckConfig(t.Context(), "project-a", alice, "direct", softphoneRTCConfig{}); err == nil {
		t.Fatal("forced-relay policy bypassed")
	}
}
func TestNetworkCheckRealUnreliablePeerAndCleanup(t *testing.T) {
	a, _, _ := turnTestFixture(t)
	defer a.networkChecks.stopAll()
	s, err := a.networkChecks.create("project-a", "operator", softphoneRTCConfig{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	client, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	retransmits := uint16(0)
	ordered := false
	dc, err := client.CreateDataChannel("telephony-network-check", &webrtc.DataChannelInit{Ordered: &ordered, MaxRetransmits: &retransmits})
	if err != nil {
		t.Fatal(err)
	}
	packet := make([]byte, 96)
	binary.LittleEndian.PutUint32(packet, 0x31504e54)
	var once sync.Once
	received := make(chan struct{})
	dc.OnMessage(func(message webrtc.DataChannelMessage) {
		if len(message.Data) == 96 {
			once.Do(func() { close(received) })
		}
	})
	dc.OnOpen(func() {
		if err := dc.Send(packet); err != nil {
			t.Error(err)
		}
	})
	offer, err := client.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather := webrtc.GatheringCompletePromise(client)
	client.SetLocalDescription(offer)
	<-gather
	answer, err := a.answerNetworkCheck(t.Context(), s, client.LocalDescription().SDP)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.answerNetworkCheck(t.Context(), s, client.LocalDescription().SDP); err == nil {
		t.Fatal("duplicate offer accepted")
	}
	if err = client.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("no probe packets")
	}
	a.networkChecks.remove(s)
	a.networkChecks.mu.Lock()
	count := len(a.networkChecks.sessions)
	a.networkChecks.mu.Unlock()
	if count != 0 {
		t.Fatal("probe leaked")
	}
	select {
	case <-s.done:
	default:
		t.Fatal("probe not cancelled")
	}
}
func TestNetworkCheckMalformedSDPAndDraining(t *testing.T) {
	a, _, _ := turnTestFixture(t)
	s, err := a.networkChecks.create("p", "operator", softphoneRTCConfig{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer a.networkChecks.stopAll()
	for _, offer := range []string{"bad", strings.Repeat("x", 25000), "v=0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 111\r\n"} {
		if _, err := a.answerNetworkCheck(t.Context(), s, offer); err == nil {
			t.Fatal("invalid SDP accepted")
		}
	}
	a.legacyAdmission.Store(1)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/softphone/network-check?project_id=project-a", strings.NewReader(`{"action":"prepare","path":"configured"}`))
	a.handleNetworkCheck(w, r)
	if w.Code != 503 {
		t.Fatal("draining admitted probe", w.Code, w.Body.String())
	}
}
func TestNetworkProbeCredentialsCannotStarveCallCredentials(t *testing.T) {
	a, ctx, platform := turnTestFixture(t)
	saveTURNTestPolicy(t, a, ctx, nil, phoneTURNPolicy{Source: "app"})
	platform.result.TTLSeconds = 60
	platform.result.ExpiresAt = time.Now().Add(time.Minute).Unix()
	// Even when all four call-credential slots are occupied, a diagnostic uses
	// its own single, nonwaiting slot and cannot lengthen a call's queue.
	for range 4 {
		rtcTURNRequests <- struct{}{}
	}
	started := time.Now()
	_, err := a.networkCheckConfig(t.Context(), "project-a", phoneIdentity{}, "configured", softphoneRTCConfig{})
	for range 4 {
		<-rtcTURNRequests
	}
	if err != nil || time.Since(started) > time.Second {
		t.Fatal("diagnostic used live-call credential capacity", err, time.Since(started))
	}
	networkCheckCredentialRequests <- struct{}{}
	defer func() { <-networkCheckCredentialRequests }()
	if _, err = a.networkCheckConfig(t.Context(), "project-a", phoneIdentity{}, "configured", softphoneRTCConfig{}); err == nil {
		t.Fatal("probe credential concurrency unbounded")
	}
	provider := &rtcTURNProvider{fetch: func(context.Context) (rtcTURNCredentials, error) { return turnTestCredentials(), nil }}
	if _, err = provider.issue(t.Context()); err != nil {
		t.Fatal("diagnostic occupied call slots", err)
	}
}

func TestNetworkCheckPortsAreIsolatedFromCallCapacity(t *testing.T) {
	original := softphoneRTCConfig{PortMin: 20000, PortMax: 20100}
	c := original
	if err := networkCheckPorts(&c, nil); err != nil || c.PortMin != 0 || c.PortMax != 0 {
		t.Fatal(c, err)
	}
	c = original
	if err := networkCheckPorts(&c, map[string]string{"softphone_network_check_udp_port_min": "20099", "softphone_network_check_udp_port_max": "20110"}); err == nil {
		t.Fatal("diagnostics occupied call range")
	}
	c = original
	if err := networkCheckPorts(&c, map[string]string{"softphone_network_check_udp_port_min": "20200", "softphone_network_check_udp_port_max": "20210"}); err != nil || c.PortMin != 20200 {
		t.Fatal(c, err)
	}
	network, err := networkCheckNetwork(20000, 20100)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := network.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	port := conn.LocalAddr().(*net.UDPAddr).Port
	if port >= 20000 && port <= 20100 {
		t.Fatal("diagnostic used reserved call UDP port", port)
	}
}
