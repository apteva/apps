package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gobwas/ws/wsutil"
)

type simReportPlatform struct {
	answerPlatform
	mu      sync.Mutex
	reports []sdk.IntegrationSimulationReport
}

func (p *simReportPlatform) ReportIntegrationSimulation(report sdk.IntegrationSimulationReport) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reports = append(p.reports, report)
	return nil
}

// record merges the reports the way the platform does.
func (p *simReportPlatform) record(id string) (map[string]any, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	merged := map[string]any{}
	var events []string
	for _, report := range p.reports {
		if report.ID != id {
			continue
		}
		for key, value := range report.Fields {
			merged[key] = value
		}
		if report.Event != "" {
			events = append(events, report.Event)
		}
	}
	return merged, events
}

func (p *simReportPlatform) waitFor(t *testing.T, id string, what string, ok func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if record, _ := p.record(id); ok(record) {
			return record
		}
		time.Sleep(20 * time.Millisecond)
	}
	record, events := p.record(id)
	t.Fatalf("simulation record %s never reached %s: %#v (events %v)", id, what, record, events)
	return nil
}

func carrierSimToneWAV(rate int, duration time.Duration) string {
	samples := int(duration * time.Duration(rate) / time.Second)
	data := make([]byte, samples*2)
	for i := range samples {
		value := int16(9000 * math.Sin(2*math.Pi*440*float64(i)/float64(rate)))
		binary.LittleEndian.PutUint16(data[i*2:], uint16(value))
	}
	header := make([]byte, 44)
	copy(header, "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(36+len(data)))
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 1)
	binary.LittleEndian.PutUint32(header[24:], uint32(rate))
	binary.LittleEndian.PutUint32(header[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(header[32:], 2)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(len(data)))
	return base64.StdEncoding.EncodeToString(append(header, data...))
}

type carrierSimFixture struct {
	app       *App
	ctx       *sdk.AppCtx
	platform  *simReportPlatform
	gateway   *sipGateway
	port      int
	transport string
}

// newCarrierSimFixture runs a Telnyx direct-SIP route on a loopback gateway
// configured the way an Environment configures it.
func newCarrierSimFixture(t *testing.T) *carrierSimFixture {
	return newCarrierSimFixtureTransport(t, "udp")
}

func newCarrierSimFixtureTransport(t *testing.T, transport string) *carrierSimFixture {
	t.Helper()
	t.Setenv("APTEVA_ENVIRONMENT_ID", "rt_carrier_sim")
	platform := &simReportPlatform{}
	app, ctx := withTelephonyTestContext(t, platform)
	cfg, err := loadSIPGatewayConfig(sdk.Config{
		"sip_enabled": "true", "sip_transport": transport, "sip_allow_insecure_signaling": "true",
		"sip_listen": "127.0.0.1:0", "sip_public_host": "127.0.0.1", "sip_public_ip": "127.0.0.1",
		"sip_allowed_cidrs": "telnyx=127.0.0.1/32", "sip_srtp": "disabled",
		"sip_rtp_port_min": "0", "sip_rtp_port_max": "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := newSIPGateway(app, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gateway.Stop)
	app.sip.gateway = gateway
	t.Cleanup(func() {
		if app.carrierSim != nil {
			app.carrierSim.stop()
		}
	})
	route := routeRow{ID: "telnyx-sip-route", ProjectID: "project-a", CarrierSlug: "telnyx", CarrierConnectionID: 10,
		PhoneNumber: "+12025550100", AgentID: 7, Enabled: true, AnswerMode: answerModeAgent, TimeoutSec: 60,
		InboundTransport: inboundTransportSIPDirect, TransportConfig: `{"provider":"telnyx","fqdn_connection_id":"fc_1","fqdn_id":"fq_1"}`}
	if err := app.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	listen, _ := netip.ParseAddrPort(cfg.ListenAddress)
	return &carrierSimFixture{app: app, ctx: ctx, platform: platform, gateway: gateway, port: int(listen.Port()), transport: transport}
}

func (f *carrierSimFixture) handOff(t *testing.T, kind, id string, payload any) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(payload)
	return f.app.HandleIntegrationSimulation(f.ctx, sdk.IntegrationSimulationEvent{
		Integration: "telnyx", ConnectionID: 10, Kind: kind, Collection: "sip_calls", ID: id, Payload: raw,
	})
}

func (f *carrierSimFixture) inbound(codecs []string, audio string) map[string]any {
	payload := map[string]any{
		"from": "+33612345678", "to": "+12025550100", "codecs": codecs, "dtmf_type": "RFC 2833",
		"target": map[string]any{"host": "127.0.0.1", "port": f.port, "transport": f.transport},
	}
	if audio != "" {
		payload["caller_audio"] = map[string]any{"name": "caller.wav", "url": "https://fixtures.invalid/caller.wav", "wav_base64": audio}
	}
	return payload
}

func (f *carrierSimFixture) ringingCall(t *testing.T, id string, result any) *callRow {
	t.Helper()
	header, _ := result.(map[string]any)["sip_call_id_header"].(string)
	f.platform.waitFor(t, id, "ringing", func(r map[string]any) bool { return r["state"] == "ringing" })
	call, err := f.app.db().findInboundCallByProviderID(header)
	if err != nil || call == nil {
		t.Fatalf("Telephony recorded no call for %q: %v", header, err)
	}
	return call
}

// answerWithCore answers the call and bridges it to a fake agent core that
// plays a tone and reports whether it heard the caller.
func (f *carrierSimFixture) answerWithCore(t *testing.T, call *callRow) *atomic.Int64 {
	t.Helper()
	heard := &atomic.Int64{}
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := upgradeBuffered(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		go func() {
			for {
				audio, _, err := wsutil.ReadClientData(conn)
				if err != nil {
					return
				}
				if !bytes.Equal(audio, make([]byte, len(audio))) {
					heard.Add(1)
				}
			}
		}()
		frame := make([]byte, 960) // 20 ms of 24 kHz PCM16
		for tick := 0; ; tick++ {
			for i := range 480 {
				value := int16(8000 * math.Sin(2*math.Pi*300*float64(tick*480+i)/24000))
				binary.LittleEndian.PutUint16(frame[i*2:], uint16(value))
			}
			if wsutil.WriteServerBinary(conn, frame) != nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}))
	t.Cleanup(core.Close)
	if _, err := f.app.db().db.Exec(`UPDATE calls SET audio_bridge_url=?,peer_kind='realtime' WHERE id=?`, strings.Replace(core.URL, "http:", "ws:", 1), call.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.app.db().updateStatus(call.ID, "answering", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.gateway.Answer(call); err != nil {
		t.Fatal(err)
	}
	if err := f.app.db().updateStatus(call.ID, "answered", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.gateway.StartMedia(call); err != nil {
		t.Fatal(err)
	}
	return heard
}

func (f *carrierSimFixture) waitCallEnded(t *testing.T, call *callRow) *callRow {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		row, _ := f.app.db().findCall(call.ID)
		if f.gateway.sessionCount() == 0 && row != nil && isTerminalStatus(row.Status) {
			return row
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Telephony did not end the SIP call")
	return nil
}

func rtpStat(record map[string]any, key string) int64 {
	stats, _ := record["rtp"].(map[string]any)
	switch value := stats[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	}
	return 0
}

func TestEnvironmentLoopbackSIPConfig(t *testing.T) {
	config := sdk.Config{
		"sip_enabled": "true", "sip_transport": "udp", "sip_allow_insecure_signaling": "true",
		"sip_listen": "127.0.0.1:0", "sip_public_host": "127.0.0.1", "sip_public_ip": "127.0.0.1",
		"sip_allowed_cidrs": "telnyx=127.0.0.1/32", "sip_srtp": "disabled",
		"sip_rtp_port_min": "0", "sip_rtp_port_max": "0",
	}
	t.Setenv("APTEVA_ENVIRONMENT_ID", "")
	if _, err := loadSIPGatewayConfig(config); err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("loopback SIP outside an Environment: err=%v", err)
	}

	t.Setenv("APTEVA_ENVIRONMENT_ID", "rt_env")
	cfg, err := loadSIPGatewayConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	listen, err := netip.ParseAddrPort(cfg.ListenAddress)
	if err != nil || !listen.Addr().IsLoopback() || listen.Port() == 0 {
		t.Fatalf("listen address %q was not resolved to a free loopback port", cfg.ListenAddress)
	}
	if cfg.RTPPortMin != 0 || cfg.RTPPortMax != 0 || cfg.RTPBindIP.String() != "127.0.0.1" || cfg.sourceCarrier("127.0.0.1:5060") != "telnyx" {
		t.Fatalf("loopback media config: %#v", cfg)
	}
	if !strings.HasSuffix(cfg.endpointURI(), ":"+strconv.Itoa(int(listen.Port()))+";transport=udp") {
		t.Fatalf("registered endpoint %q does not carry the chosen port", cfg.endpointURI())
	}

	for key, value := range map[string]string{"sip_listen": "0.0.0.0:0", "sip_rtp_bind_ip": "10.0.0.5"} {
		bad := sdk.Config{}
		for k, v := range config {
			bad[k] = v
		}
		bad[key] = value
		if _, err := loadSIPGatewayConfig(bad); err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Fatalf("%s=%s inside an Environment: err=%v", key, value, err)
		}
	}
}

func TestCarrierSimulationCallsGatewayOverLoopback(t *testing.T) {
	for _, codec := range []string{"G722", "G711U"} {
		t.Run(codec, func(t *testing.T) {
			f := newCarrierSimFixture(t)
			id := "sip_" + strings.ToLower(codec)
			result, err := f.handOff(t, carrierSimKindInbound, id, f.inbound([]string{codec}, carrierSimToneWAV(16000, 1500*time.Millisecond)))
			if err != nil {
				t.Fatal(err)
			}
			call := f.ringingCall(t, id, result)
			heard := f.answerWithCore(t, call)

			want := map[string]string{"G722": "G722", "G711U": "PCMU"}[codec]
			record := f.platform.waitFor(t, id, "two-way audio", func(r map[string]any) bool {
				return r["state"] == "answered" && rtpStat(r, "non_silent_ms") >= 400 && rtpStat(r, "sent_packets") >= 25
			})
			if record["codec"] != want {
				t.Fatalf("negotiated codec %v, want %s", record["codec"], want)
			}
			deadline := time.Now().Add(3 * time.Second)
			for heard.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if heard.Load() == 0 {
				t.Fatal("the agent never heard the caller's fixture audio")
			}

			if _, err := f.handOff(t, carrierSimKindDTMF, id, map[string]any{"digits": "1#"}); err != nil {
				t.Fatal(err)
			}
			f.platform.waitFor(t, id, "DTMF sent", func(r map[string]any) bool { return r["dtmf_sent"] == "1#" })

			if _, err := f.handOff(t, carrierSimKindHangup, id, map[string]any{"cause": "normal_clearing", "q850": 16}); err != nil {
				t.Fatal(err)
			}
			record = f.platform.waitFor(t, id, "caller hang-up", func(r map[string]any) bool { return r["state"] == "ended" })
			if record["ended_by"] != "caller" || record["q850"] != 16 || record["ended_at"] == nil || record["error"] != nil {
				t.Fatalf("caller hang-up record: %#v", record)
			}
			f.waitCallEnded(t, call)
			_, events := f.platform.record(id)
			if strings.Join(events, ",") != "inviting,ringing,answered,ended" {
				t.Fatalf("report events %v", events)
			}
			if _, err := f.handOff(t, carrierSimKindHangup, id, nil); err == nil {
				t.Fatal("a hang-up after the call ended was accepted")
			}
		})
	}
}

func TestCarrierSimulationReportsAppHangup(t *testing.T) {
	for _, transport := range []string{"udp", "tcp"} {
		t.Run(transport, func(t *testing.T) { testCarrierSimulationAppHangup(t, transport) })
	}
}

func testCarrierSimulationAppHangup(t *testing.T, transport string) {
	f := newCarrierSimFixtureTransport(t, transport)
	result, err := f.handOff(t, carrierSimKindInbound, "sip_app_bye", f.inbound([]string{"G711A"}, ""))
	if err != nil {
		t.Fatal(err)
	}
	call := f.ringingCall(t, "sip_app_bye", result)
	f.answerWithCore(t, call)
	f.platform.waitFor(t, "sip_app_bye", "answered", func(r map[string]any) bool { return r["state"] == "answered" })
	if _, err := f.handOff(t, carrierSimKindPlay, "sip_app_bye", map[string]any{
		"caller_audio": map[string]any{"name": "later.wav", "wav_base64": carrierSimToneWAV(8000, 300*time.Millisecond)},
	}); err != nil {
		t.Fatal(err)
	}
	f.platform.waitFor(t, "sip_app_bye", "played audio", func(r map[string]any) bool {
		played, _ := r["audio_played"].([]string)
		return len(played) == 1 && played[0] == "later.wav"
	})
	if err := f.gateway.Hangup(call); err != nil {
		t.Fatal(err)
	}
	record := f.platform.waitFor(t, "sip_app_bye", "app hang-up", func(r map[string]any) bool { return r["state"] == "ended" })
	if record["ended_by"] != "app" || record["codec"] != "PCMA" {
		t.Fatalf("app hang-up record: %#v", record)
	}
	f.waitCallEnded(t, call)
}

func TestCarrierSimulationCallerHangsUpWhileRinging(t *testing.T) {
	f := newCarrierSimFixture(t)
	result, err := f.handOff(t, carrierSimKindInbound, "sip_cancel", f.inbound(nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	call := f.ringingCall(t, "sip_cancel", result)
	if _, err := f.handOff(t, carrierSimKindDTMF, "sip_cancel", map[string]any{"digits": "1"}); err == nil {
		t.Fatal("DTMF was accepted before the answer")
	}
	if _, err := f.handOff(t, carrierSimKindHangup, "sip_cancel", map[string]any{"cause": "originator_cancel", "q850": 16}); err != nil {
		t.Fatal(err)
	}
	record := f.platform.waitFor(t, "sip_cancel", "cancelled", func(r map[string]any) bool { return r["state"] == "ended" })
	if record["ended_by"] != "caller" || record["cause"] != "originator_cancel" || record["answered_at"] != nil {
		t.Fatalf("cancel record: %#v", record)
	}
	f.waitCallEnded(t, call)
}

func TestCarrierSimulationRefusesForeignTargets(t *testing.T) {
	f := newCarrierSimFixture(t)
	base := f.inbound([]string{"G711U"}, "")
	cases := map[string]func(map[string]any){
		"other port":      func(p map[string]any) { p["target"].(map[string]any)["port"] = f.port + 1 },
		"remote host":     func(p map[string]any) { p["target"].(map[string]any)["host"] = "198.51.100.7" },
		"tls":             func(p map[string]any) { p["target"].(map[string]any)["transport"] = "tls" },
		"srtp":            func(p map[string]any) { p["encrypted_media"] = "SRTP" },
		"no usable codec": func(p map[string]any) { p["codecs"] = []string{"OPUS"} },
		"bad audio":       func(p map[string]any) { p["caller_audio"] = map[string]any{"name": "x", "wav_base64": "bm90IGEgd2F2"} },
	}
	for name, mutate := range cases {
		payload := map[string]any{}
		raw, _ := json.Marshal(base)
		_ = json.Unmarshal(raw, &payload)
		mutate(payload)
		if _, err := f.handOff(t, carrierSimKindInbound, "sip_bad", payload); err == nil {
			t.Fatalf("%s: hand-off accepted", name)
		}
	}
	if _, err := f.handOff(t, "sip_transfer", "sip_bad", map[string]any{}); err == nil {
		t.Fatal("unknown kind accepted")
	}
	t.Setenv("APTEVA_ENVIRONMENT_ID", "")
	if _, err := f.handOff(t, carrierSimKindInbound, "sip_outside", base); err == nil || !strings.Contains(err.Error(), "Environment") {
		t.Fatalf("outside an Environment: %v", err)
	}
	if f.gateway.sessionCount() != 0 || len(f.platform.reports) != 0 {
		t.Fatalf("refused hand-offs reached the gateway (%d sessions) or reported (%d)", f.gateway.sessionCount(), len(f.platform.reports))
	}
}

func TestCarrierSimulationWAVDecoding(t *testing.T) {
	pcm, rate, err := decodeCarrierSimWAV(carrierSimToneWAV(16000, 100*time.Millisecond))
	if err != nil || rate != 16000 || len(pcm) != 1600 {
		t.Fatalf("decode: rate=%d samples=%d err=%v", rate, len(pcm), err)
	}
	if _, _, err := decodeCarrierSimWAV(carrierSimToneWAV(16000, carrierSimMaxAudio+time.Second)); err == nil {
		t.Fatal("over-long audio accepted")
	}
	if !carrierSimEncrypted(json.RawMessage(`"SRTP"`)) || carrierSimEncrypted(json.RawMessage(`null`)) || carrierSimEncrypted(nil) {
		t.Fatal("encrypted_media detection")
	}
	codecs, err := carrierSimCodecs([]string{"G722", "G711U", "g711a", "OPUS"})
	if err != nil || strings.Join(codecs, ",") != "G722,PCMU,PCMA" {
		t.Fatalf("codecs %v %v", codecs, err)
	}
}
