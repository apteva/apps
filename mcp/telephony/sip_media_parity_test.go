package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"github.com/pion/rtp"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type localSpeechPlatform struct {
	answerPlatform
	mu      sync.Mutex
	started chan struct{}
	wait    bool
}

func (p *localSpeechPlatform) ExecuteIntegrationToolContext(ctx context.Context, id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	if p.started != nil {
		select {
		case p.started <- struct{}{}:
		default:
		}
	}
	if p.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.answerPlatform.ExecuteIntegrationTool(id, tool, input)
}
func localSpeechFixture(t *testing.T) (*App, *sdk.AppCtx, *localSpeechPlatform) {
	t.Helper()
	pcm := make([]int16, 2400)
	for i := range pcm {
		pcm[i] = int16(6000 * math.Sin(float64(i)*2*math.Pi*440/24000))
	}
	raw, _ := json.Marshal(map[string]any{"_binary": true, "base64": base64.StdEncoding.EncodeToString(pcm16ToBytes(pcm)), "mimeType": "audio/pcm"})
	p := &localSpeechPlatform{answerPlatform: answerPlatform{bindings: map[string]any{"carrier": int64(10), "speech": int64(11)}, integrationResponse: map[string]json.RawMessage{"speech": raw}}}
	t.Setenv("APTEVA_PUBLIC_URL", "https://example.test")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(p))
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	a := &App{installID: 42}
	if err := a.saveLocalMediaSettings(ctx, "", localMediaSettings{SpeechTool: "speech", SampleRate: 24000, GatherTimeoutSeconds: 1, VoicemailMaxSeconds: 1}); err != nil {
		t.Fatal(err)
	}
	return a, ctx, p
}

type sipMediaHarness struct {
	app           *App
	ctx           *sdk.AppCtx
	gateway       *sipGateway
	signal, media *net.UDPConn
	call          *callRow
	remote        *net.UDPAddr
	bye           chan time.Time
	invite        string
}

func localMediaHarness(t *testing.T, definition routingDefinition) *sipMediaHarness {
	return localMediaHarnessWithHooks(t, definition, nil, nil)
}
func localMediaHarnessWithHooks(t *testing.T, definition routingDefinition, prepare func(*localSpeechPlatform), beforeAnswer func(*sipMediaHarness)) *sipMediaHarness {
	t.Helper()
	a, ctx, platform := localSpeechFixture(t)
	if prepare != nil {
		prepare(platform)
	}
	flow, err := a.saveRoutingFlow("project-a", "", "Media test", "", mustRoutingJSON(t, definition))
	if err != nil {
		t.Fatal(err)
	}
	version, problems, err := a.publishRoutingFlow("project-a", flow.ID)
	if err != nil || len(problems) > 0 {
		t.Fatalf("publish: %v %v", err, problems)
	}
	route := routeRow{ID: "media-route", ProjectID: "project-a", CarrierSlug: "twilio", CarrierConnectionID: 10, PhoneNumber: "+12025550100", AgentID: 7, Enabled: true, AnswerMode: answerModeAgent, TimeoutSec: 60, InboundTransport: inboundTransportSIPDirect, PublishedFlowVersionID: version.ID, FlowID: flow.ID}
	if err = a.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	port := freeUDPPort(t)
	cfg := directSIPTestConfig()
	cfg.Transport = "udp"
	cfg.ListenAddress = fmt.Sprintf("127.0.0.1:%d", port)
	cfg.PublicHost = "127.0.0.1"
	cfg.PublicIP = netip.MustParseAddr("127.0.0.1")
	cfg.RTPBindIP = cfg.PublicIP
	cfg.RTPPortMin = 0
	cfg.RTPPortMax = 0
	cfg.SRTPMode = sipSRTPDisabled
	cfg.AllowedCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	g, err := newSIPGateway(a, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Start(); err != nil {
		t.Fatal(err)
	}
	a.sip.mu.Lock()
	a.sip.gateway = g
	a.sip.mu.Unlock()
	media, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	signal, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	h := &sipMediaHarness{app: a, ctx: ctx, gateway: g, signal: signal, media: media, bye: make(chan time.Time, 1)}
	t.Cleanup(func() { g.Stop(); signal.Close(); media.Close() })
	local := signal.LocalAddr().(*net.UDPAddr).Port
	body := fmt.Sprintf("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=test\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio %d RTP/AVP 0 101\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:101 telephone-event/8000\r\na=fmtp:101 0-15\r\n", media.LocalAddr().(*net.UDPAddr).Port)
	headers := fmt.Sprintf("Via: SIP/2.0/UDP 127.0.0.1:%d;branch=z9hG4bK-media;rport\r\nMax-Forwards: 70\r\nFrom: <sip:+33612345678@127.0.0.1>;tag=caller\r\nTo: <sip:%s@127.0.0.1>\r\nCall-ID: media-test@127.0.0.1\r\nCSeq: 1 INVITE\r\nContact: <sip:caller@127.0.0.1:%d>\r\n", local, route.PhoneNumber, local)
	invite := fmt.Sprintf("INVITE sip:%s@127.0.0.1:%d SIP/2.0\r\n%sContent-Type: application/sdp\r\nContent-Length: %d\r\n\r\n%s", route.PhoneNumber, port, headers, len(body), body)
	if _, err = signal.Write([]byte(invite)); err != nil {
		t.Fatal(err)
	}
	h.invite = invite
	if beforeAnswer != nil {
		beforeAnswer(h)
		return h
	}
	response := readSIPResponseContaining(t, signal, "200 OK")
	to := ""
	for _, line := range strings.Split(response, "\r\n") {
		if strings.HasPrefix(line, "To:") {
			to = line
		}
	}
	ackHeaders := strings.Replace(strings.Replace(strings.Replace(headers, "z9hG4bK-media", "z9hG4bK-media-ack", 1), "CSeq: 1 INVITE", "CSeq: 1 ACK", 1), "To: <sip:"+route.PhoneNumber+"@127.0.0.1>", to, 1)
	ack := fmt.Sprintf("ACK sip:%s@127.0.0.1:%d SIP/2.0\r\n%sContent-Length: 0\r\n\r\n", route.PhoneNumber, port, ackHeaders)
	if _, err = signal.Write([]byte(ack)); err != nil {
		t.Fatal(err)
	}
	h.call, err = a.db().findInboundCallByProviderID("media-test@127.0.0.1")
	if err != nil || h.call == nil {
		t.Fatalf("call: %v", err)
	}
	s := g.sessionByCall(h.call.ID)
	s.answerMu.Lock()
	h.remote = s.media.conn.LocalAddr().(*net.UDPAddr)
	s.answerMu.Unlock()
	go func() {
		buf := make([]byte, 8192)
		for {
			_ = signal.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, _, e := signal.ReadFromUDP(buf)
			if e != nil {
				return
			}
			msg := string(buf[:n])
			if strings.HasPrefix(msg, "BYE ") {
				h.bye <- time.Now()
				var reply strings.Builder
				reply.WriteString("SIP/2.0 200 OK\r\n")
				for _, line := range strings.Split(msg, "\r\n") {
					for _, prefix := range []string{"Via:", "From:", "To:", "Call-ID:", "CSeq:"} {
						if strings.HasPrefix(line, prefix) {
							reply.WriteString(line + "\r\n")
						}
					}
				}
				reply.WriteString("Content-Length: 0\r\n\r\n")
				_, _ = signal.Write([]byte(reply.String()))
			}
		}
	}()
	return h
}
func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		t.Fatal(e)
	}
	p := c.LocalAddr().(*net.UDPAddr).Port
	c.Close()
	return p
}
func (h *sipMediaHarness) readAudio(t *testing.T) time.Time {
	t.Helper()
	buf := make([]byte, 2048)
	_ = h.media.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := h.media.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	var packet rtp.Packet
	if err = packet.Unmarshal(buf[:n]); err != nil {
		t.Fatal(err)
	}
	pcm := decodeSIPG711(packet.Payload, "PCMU")
	energy := int64(0)
	for _, v := range pcm {
		energy += int64(v) * int64(v)
	}
	if energy == 0 {
		t.Fatal("prompt RTP contains no sound")
	}
	return time.Now()
}

func TestLocalSIPTerminalAnnouncementBeforeHangup(t *testing.T) {
	h := localMediaHarness(t, routingDefinition{Entry: "notice", Nodes: []routingNode{{ID: "notice", Type: "announcement", Config: map[string]any{"text": "Closed"}, Next: "end"}, {ID: "end", Type: "hangup"}}})
	first := h.readAudio(t)
	select {
	case ended := <-h.bye:
		if ended.Sub(first) < 60*time.Millisecond {
			t.Fatalf("prompt truncated: %v", ended.Sub(first))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal SIP call did not hang up")
	}
	waitSIPParity(t, func() bool {
		r, _ := h.app.db().findCall(h.call.ID)
		return r != nil && isTerminalStatus(r.Status) && r.CarrierAnsweredAt != ""
	})
}
func TestLocalSIPDTMFBranchesAndDeduplicatesEvents(t *testing.T) {
	h := localMediaHarness(t, routingDefinition{Entry: "menu", Nodes: []routingNode{{ID: "menu", Type: "dtmf_menu", Config: map[string]any{"prompt": "Press one"}, Branches: map[string]string{"1": "notice", "timeout": "end"}}, {ID: "notice", Type: "announcement", Config: map[string]any{"text": "Selected"}, Next: "end"}, {ID: "end", Type: "hangup"}}})
	h.readAudio(t)
	for i := 0; i < 3; i++ {
		packet := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 101, SequenceNumber: uint16(i), Timestamp: 800, SSRC: 1}, Payload: []byte{1, 0x80 | 10, 0x08, 0}}
		raw, _ := packet.Marshal()
		if _, err := h.media.WriteToUDP(raw, h.remote); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-h.bye:
	case <-time.After(2 * time.Second):
		t.Fatal("DTMF branch did not complete")
	}
	var node string
	_ = h.app.db().db.QueryRow(`SELECT current_node_id FROM call_route_executions WHERE call_id=?`, h.call.ID).Scan(&node)
	if node != "end" {
		t.Fatalf("node=%s", node)
	}
}
func waitSIPParity(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("SIP condition did not complete")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLocalSIPSpeechCancellationCannotAnswer(t *testing.T) {
	a, ctx, p := localSpeechFixture(t)
	p.wait = true
	p.started = make(chan struct{}, 1)
	c, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := a.renderLocalSpeech(c, ctx, localMediaSettings{SpeechTool: "speech", SampleRate: 24000}, "Hello", "")
		done <- err
	}()
	<-p.started
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("cancellation=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("speech synthesis did not cancel")
	}
}
func TestLocalMediaSettingsValidationAndIsolation(t *testing.T) {
	a, ctx, _ := localSpeechFixture(t)
	for _, cfg := range []localMediaSettings{{Codecs: []string{"opus"}}, {Codecs: []string{"PCMU", "PCMU"}}, {VoicemailMaxSeconds: 601}, {SampleRate: 7}, {Prompts: map[string]int64{"bad": -1}}} {
		if validateLocalMediaSettings(cfg) == nil {
			t.Fatalf("invalid config accepted: %+v", cfg)
		}
	}
	other, err := a.localMediaSettings("project-b", "")
	if err != nil || other.SpeechTool != "" {
		t.Fatalf("cross project defaults: %+v %v", other, err)
	}
	r := httptest.NewRequest(http.MethodPost, "/numbers/local-media?project_id=project-a", strings.NewReader(`{"route_id":"foreign","config":{"codecs":["PCMU"]}}`))
	w := httptest.NewRecorder()
	a.handleNumbers(w, r)
	if w.Code == 200 {
		t.Fatal("foreign route accepted")
	}
	if err = a.saveLocalMediaSettings(ctx, "", localMediaSettings{Codecs: []string{"PCMA"}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := a.localMediaSettings("project-a", "")
	if err != nil || len(cfg.Codecs) != 1 || cfg.Codecs[0] != "PCMA" {
		t.Fatal("settings not saved")
	}
}

func TestLocalSIPRecordingTracksPauseAndClose(t *testing.T) {
	for _, mode := range []string{"dual", "mono"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := localSpeechFixture(t)
			call := callRow{ID: "record-call", ProjectID: "project-a", Direction: "inbound", Status: "answered", RecordingMode: recordingModeAlways, RecordingChannels: mode, RecordingStorageMode: recordingStorageProvider}
			if err := a.db().insertCall(call); err != nil {
				t.Fatal(err)
			}
			r, err := a.newLocalRecording(&call, false)
			if err != nil {
				t.Fatal(err)
			}
			path := r.file.Name()
			t.Cleanup(func() { os.Remove(path) })
			r.Record(0, []int16{1000, 1000, 1000})
			r.Record(1, []int16{-2000, -2000, -2000})
			r.paused.Store(true)
			r.Record(0, make([]int16, 24001))
			r.paused.Store(false)
			var wg sync.WaitGroup
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for j := 0; j < 50; j++ {
						r.Record(0, make([]int16, 480))
					}
				}()
			}
			wg.Wait()
			r.Close()
			r.Close()
			row, err := a.db().findRecording(r.row.ID)
			if err != nil || row == nil || row.ProviderStatus != "completed" {
				t.Fatalf("recording: %+v %v", row, err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			info, err := inspectPCM16WAV(file)
			want := 2
			if mode == "mono" {
				want = 1
			}
			if err != nil || info.Channels != want || info.SampleRate != 24000 {
				t.Fatalf("format=%+v err=%v", info, err)
			}
			if r.dropped.Load() < 0 {
				t.Fatal("invalid drops")
			}
			if err = a.deleteLocalRecording(row); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("local recording not deleted")
			}
		})
	}
}
func TestLocalSIPCodecPreferencesAndG722LossSilence(t *testing.T) {
	cfg := directSIPTestConfig()
	body := []byte("v=0\r\no=- 1 1 IN IP4 203.0.113.5\r\ns=test\r\nc=IN IP4 203.0.113.5\r\nt=0 0\r\nm=audio 30000 RTP/AVP 9 0 8 101\r\na=rtpmap:9 G722/8000\r\na=rtpmap:101 telephone-event/8000\r\n")
	offer, err := parseSIPMediaOfferCodecs(body, cfg, []string{"PCMA", "PCMU"})
	if err != nil || offer.Codec != "PCMA" || offer.DTMFPayloadType != 101 {
		t.Fatalf("offer=%+v err=%v", offer, err)
	}
	offer, err = parseSIPMediaOfferCodecs(body, cfg, []string{"G722"})
	if err != nil {
		t.Fatal(err)
	}
	media := &sipRTPMedia{offer: offer}
	pcm := make([]int16, 320)
	for i := range pcm {
		pcm[i] = int16(6000 * math.Sin(float64(i)*2*math.Pi*1000/16000))
	}
	encoded := media.encodeAudio(pcm)
	if len(encoded) != 160 || media.sampleRate() != 16000 {
		t.Fatal("G.722 sample/RTP clock mismatch")
	}
	decoded := media.decodeAudio(encoded)
	if len(decoded) != 320 {
		t.Fatal("G.722 duration mismatch")
	}
	jitter := newRTPJitterBuffer()
	framer := newRTPAudioFramer(jitter, offer)
	jitter.pushRTP(1, encoded, 0, time.Now())
	jitter.pushRTP(3, encoded, 320, time.Now())
	jitter.pushRTP(4, encoded, 480, time.Now())
	_, _ = framer.popPCM(media)
	lost, ready := framer.popPCM(media)
	if !ready {
		t.Fatal("missing packet did not conceal")
	}
	for _, v := range lost {
		if v != 0 {
			t.Fatal("missing G.722 packet emitted decoder noise")
		}
	}
}

func TestSIPRuntimeTransportSwitchRetainsCallAndResources(t *testing.T) {
	platform := &answerPlatform{credentials: &sdk.ConnectionCredentials{Slug: "telnyx", Fields: map[string]string{"public_key": base64.StdEncoding.EncodeToString(make([]byte, 32))}}, integrationResponse: map[string]json.RawMessage{"list_phone_numbers": json.RawMessage(`{"data":[{"id":"phone","phone_number":"+12025550100","connection_id":"program"}]}`)}}
	a, ctx := withTelephonyTestContext(t, platform)
	cleanupCtx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(&contextAnswerPlatform{platform}))
	state := directSIPProviderConfig{Provider: "telnyx", PreviousConnectionID: "program", FQDNConnectionID: "sip", FQDNID: "fqdn"}
	raw, _ := json.Marshal(state)
	route := routeRow{ID: "switch-route", ProjectID: "project-a", CarrierSlug: "telnyx", CarrierConnectionID: 10, PhoneNumber: "+12025550100", PhoneNumberSID: "phone", Enabled: true, AgentID: 7, InboundTransport: inboundTransportSIPDirect, TransportConfig: string(raw), PreviousVoiceURL: `{"application_id":"program","previous_connection_id":"legacy"}`}
	if err := a.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	call := callRow{ID: "active-sip-call", ProjectID: "project-a", RouteID: route.ID, CarrierSlug: "telnyx", CarrierConnectionID: 10, CarrierSID: "dialog", Direction: "inbound", IngressPath: inboundTransportSIPDirect, Status: "in-progress", ThreadID: "thread", AudioBridgeURL: "pending", MediaStatus: "connected"}
	if err := a.db().insertCall(call); err != nil {
		t.Fatal(err)
	}
	if err := a.db().updateMediaStatus(call.ID, "connected", "", 0, ""); err != nil {
		t.Fatal(err)
	}
	updated, err := a.applyRouteTransport(ctx, route.ID, inboundTransportProgrammable, 0)
	if err != nil {
		t.Fatal(err)
	}
	if updated.InboundTransport != inboundTransportProgrammable || updated.TransportConfig != "" {
		t.Fatalf("route=%+v", updated)
	}
	if !a.callUsesDirectSIP(&call) {
		t.Fatal("existing SIP call followed new route transport")
	}
	program := call
	program.ID = "program-call"
	program.IngressPath = inboundTransportProgrammable
	if a.callUsesDirectSIP(&program) {
		t.Fatal("programmable call followed route switch")
	}
	if err = a.runRetiredSIPResources(context.Background(), cleanupCtx); err != nil {
		t.Fatal(err)
	}
	for _, command := range platform.integrationCalls {
		if strings.HasPrefix(command.Tool, "delete_") || command.Tool == "hangup_call" {
			t.Fatalf("active resource interrupted: %s", command.Tool)
		}
	}
	fresh, _ := a.db().findCall(call.ID)
	if fresh.Status != "in-progress" || fresh.MediaStatus != "connected" {
		t.Fatalf("active call mutated: %+v", fresh)
	}
	if _, err = a.applyRouteTransport(ctx.WithProject("project-b"), route.ID, inboundTransportProgrammable, 0); err == nil {
		t.Fatal("cross-project switch accepted")
	}
	if err = a.db().updateStatus(call.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	if err = a.runRetiredSIPResources(context.Background(), cleanupCtx); err != nil {
		t.Fatal(err)
	}
	var remaining int
	_ = a.db().db.QueryRow(`SELECT count(*) FROM retired_sip_resources`).Scan(&remaining)
	if remaining != 0 {
		t.Fatal("resources not retired after call finished")
	}
	if len(platform.integrationCalls) < 2 || platform.integrationCalls[len(platform.integrationCalls)-1].Tool != "delete_fqdn_connection" {
		t.Fatal("SIP connection cleanup missing")
	}
}
func TestSIPResourceCleanupAcceptsAlreadyDeletedResources(t *testing.T) {
	p := &cleanup404Platform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(p))
	if err := cleanupSIPResources(ctx, 10, directSIPProviderConfig{Provider: "telnyx", FQDNID: "gone", FQDNConnectionID: "gone-connection"}); err != nil {
		t.Fatal(err)
	}
	if p.count != 2 {
		t.Fatal("partial cleanup did not continue")
	}
}

type cleanup404Platform struct {
	tk.BasePlatformClient
	count int
}

func (p *cleanup404Platform) ExecuteIntegrationTool(_ int64, _ string, _ map[string]any) (*sdk.ExecuteResult, error) {
	p.count++
	return &sdk.ExecuteResult{Success: false, Status: 404}, nil
}

func TestSIPNativeHoldResumeKeepsDialog(t *testing.T) {
	f := newSIPDialogFixture(t, false, 5*time.Second)
	f.answer(t)
	hold := strings.Replace(f.body, "a=rtpmap:0 PCMU/8000", "a=rtpmap:0 PCMU/8000\r\na=sendonly", 1)
	f.send(t, "UPDATE", 2, hold, "")
	response := readSIPResponseContaining(t, f.signaling, "200 OK")
	if !strings.Contains(response, "a=recvonly") {
		t.Fatalf("hold answer=%s", response)
	}
	waitSIPParity(t, func() bool { return f.session.media.remoteMode.Load() == 1 })
	f.send(t, "UPDATE", 3, f.body, "")
	response = readSIPResponseContaining(t, f.signaling, "200 OK")
	if !strings.Contains(response, "a=sendrecv") {
		t.Fatal("resume did not restore bidirectional media")
	}
	waitSIPParity(t, func() bool { return f.session.media.remoteMode.Load() == 0 })
	if f.session.ended.Load() {
		t.Fatal("hold/resume ended call")
	}
	f.send(t, "BYE", 4, "", "")
	readSIPResponseContaining(t, f.signaling, "200 OK")
	f.waitEnded(t)
}
func TestSIPOutboundDTMFUsesNegotiatedRTP(t *testing.T) {
	f := newSIPDialogFixture(t, false, 5*time.Second)
	f.answer(t)
	media := f.session.media
	media.offer.DTMFPayloadType = 101
	ctx, cancel := context.WithCancel(context.Background())
	pacer := newSIPRTPPacer(ctx, media, &sipPlaybackState{}, nil)
	defer func() { cancel(); <-pacer.done }()
	if err := pacer.queueDTMF("1"); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 2048)
	var timestamp uint32
	var count int
	var ending int
	for ending < 3 {
		_ = f.media.SetReadDeadline(time.Now().Add(time.Second))
		n, _, err := f.media.ReadFromUDP(buffer)
		if err != nil {
			t.Fatal(err)
		}
		var packet rtp.Packet
		if err = packet.Unmarshal(buffer[:n]); err != nil {
			t.Fatal(err)
		}
		if packet.PayloadType != 101 || packet.Payload[0] != 1 {
			t.Fatal("DTMF not on negotiated payload")
		}
		if count == 0 {
			timestamp = packet.Timestamp
		}
		if timestamp != packet.Timestamp {
			t.Fatal("DTMF event timestamp changed")
		}
		if packet.Payload[1]&0x80 != 0 {
			ending++
		}
		count++
	}
	if count != 16 {
		t.Fatalf("DTMF packets=%d", count)
	}
	ended := make(chan error, 1)
	go func() { ended <- f.gateway.Hangup(f.call) }()
	request := readSIPResponseContaining(t, f.signaling, "BYE sip:")
	f.respond(t, request, "")
	select {
	case err := <-ended:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DTMF call cleanup did not finish")
	}
}

func BenchmarkSIPCodecRoundTrip(b *testing.B) {
	for _, codec := range []string{"PCMU", "PCMA", "G722"} {
		b.Run(codec, func(b *testing.B) {
			media := &sipRTPMedia{offer: sipMediaOffer{Codec: codec}}
			pcm := make([]int16, media.sampleRate()/50)
			for i := range pcm {
				pcm[i] = int16(6000 * math.Sin(float64(i)*2*math.Pi*440/float64(media.sampleRate())))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				data := media.encodeAudio(pcm)
				_ = media.decodeAudio(data)
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)*50/1e7, "CPU-percent/call")
		})
	}
}

type contextAnswerPlatform struct{ *answerPlatform }

func (p *contextAnswerPlatform) ExecuteIntegrationToolContext(ctx context.Context, id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.ExecuteIntegrationTool(id, tool, args)
}
func (p *cleanup404Platform) ExecuteIntegrationToolContext(ctx context.Context, id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.ExecuteIntegrationTool(id, tool, args)
}
