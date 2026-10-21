//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	bench "github.com/apteva/apps/mcp/telephony/benchmarks/softphone"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"
)

type outboundSoakIncident struct {
	StartMS    float64  `json:"start_ms"`
	EndMS      float64  `json:"end_ms"`
	RecoveryMS *float64 `json:"fresh_marker_recovery_ms"`
}
type outboundSoakMarker struct {
	SourceMS  float64 `json:"source_ms"`
	ArrivedMS float64 `json:"arrived_ms"`
	DelayMS   float64 `json:"delay_ms"`
	Healthy   bool    `json:"healthy"`
}
type outboundSoakGap struct {
	StartMS        float64 `json:"start_ms"`
	EndMS          float64 `json:"end_ms"`
	InterruptionMS float64 `json:"interruption_ms"`
	Healthy        bool    `json:"healthy"`
}
type outboundSoakSample struct {
	AtMS             float64           `json:"at_ms"`
	Packets          int64             `json:"packets"`
	Pacer            livePacerSnapshot `json:"pacer"`
	CaptureQueueMS   int64             `json:"capture_queue_ms"`
	CaptureDroppedMS int64             `json:"capture_dropped_ms"`
}
type outboundSoakCall struct {
	sourceMarkers      map[int]float64
	pendingMarkers     map[int]float64
	lastMarkerWall     float64
	lastMarkerBlock    int
	recorder           *localCallRecorder
	answerBody         []byte
	Refreshes          int   `json:"sip_refreshes"`
	RecordingDroppedMS int64 `json:"recording_dropped_ms"`
	mu                 sync.Mutex
	ID                 string                 `json:"id"`
	Codec              string                 `json:"codec"`
	Profile            string                 `json:"profile"`
	PauseMS            int                    `json:"pause_ms,omitempty"`
	Epoch              time.Time              `json:"-"`
	OriginMS           float64                `json:"source_origin_ms"`
	Packets            int64                  `json:"packets"`
	Errors             []string               `json:"errors"`
	Markers            []outboundSoakMarker   `json:"markers"`
	Incidents          []outboundSoakIncident `json:"incidents"`
	Samples            []outboundSoakSample   `json:"samples"`
	DeliveryGaps       []outboundSoakGap      `json:"delivery_gaps_over_80ms"`
	LongestGapMS       float64                `json:"longest_delivery_interruption_ms"`
	HealthyGapMS       float64                `json:"longest_healthy_delivery_interruption_ms"`
	SilenceMS          float64                `json:"longest_unexpected_silent_voiced_run_ms"`
	proxy              *outboundSoakProxy
	carrier            *sipRTPMedia
	row                callRow
	session            *outboundSIPSession
	wg                 sync.WaitGroup
	speech             []int16
	silentRun          float64
	lastDelay          float64
	lastPacket         time.Time
	lastSourceMS       float64
	decoder            *bench.Decoder
	markerCount        int
	measurementStart   atomic.Int64
	end                atomic.Bool
}

// Real Chromium -> production capture Worklet/Opus -> Telephony WebRTC decoder,
// source envelope/hub -> actual outbound SIP session -> local TLS/SRTP peer.
// The carrier and platform identity are fixtures. No production API is used.
func TestOutboundSIPBrowserSoak(t *testing.T) {
	output := os.Getenv("TELEPHONY_OUTBOUND_SOAK_OUTPUT")
	if output == "" {
		t.Skip("opt-in: bun run benchmark:outbound-sip --minutes 30 --calls 16")
	}
	seconds := soakEnvInt(t, "TELEPHONY_OUTBOUND_SOAK_SECONDS", 1800, 8, 14400)
	count := soakEnvInt(t, "TELEPHONY_OUTBOUND_SOAK_CALLS", 16, 2, 64)
	pauseMS := soakEnvInt(t, "TELEPHONY_OUTBOUND_SOAK_PAUSE_MS", 420, 20, 1000)
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	previous := globalCtx
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(&answerPlatform{}), tk.WithConfig(map[string]string{"softphone_webrtc_enabled": "true"}))
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	app := &App{installID: 42}
	mediaServer := softphoneTestServer(t, app)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(mediaServer.URL, "http://"))
	t.Setenv("APTEVA_APP_PORT", port)
	epoch := time.Now()
	calls := make([]*outboundSoakCall, count)
	for i := range calls {
		codec := []string{"PCMA", "G722"}[i%2]
		profile := []string{"healthy", "jitter", "shift180", "pause420"}[(i/2)%4]
		if strings.HasPrefix(profile, "pause") {
			profile = fmt.Sprintf("pause%d", pauseMS)
		}
		id := fmt.Sprintf("outbound-soak-%d", i)
		row := callRow{ID: id, ThreadID: "human-" + id, ProjectID: "project-a", Direction: "outbound", CarrierSlug: "telnyx", CarrierSID: id, IngressPath: inboundTransportSIPDirect, Status: "initiated", PeerKind: peerKindHuman, PeerToken: "local-media-" + id, CallbackSecret: "local-carrier-" + id, ToNumber: fmt.Sprintf("+1202555%04d", i), FromNumber: "+12025550000", PlacedAt: ringTime(epoch), RecordingMode: recordingModeOff}
		if os.Getenv("TELEPHONY_OUTBOUND_SOAK_RECORDING") == "1" {
			row.RecordingMode = recordingModeAlways
		}
		if err := app.db().insertCall(row); err != nil {
			t.Fatal(err)
		}
		calls[i] = &outboundSoakCall{ID: id, Codec: codec, Profile: profile, PauseMS: pauseMS, Epoch: epoch, row: row, sourceMarkers: map[int]float64{}, pendingMarkers: map[int]float64{}, decoder: bench.NewDecoder(map[string]int{"PCMA": 8000, "G722": 16000}[codec])}
	}
	g, settings := startOutboundSoakCarrier(t, app, ctx, calls)
	t.Cleanup(func() {
		for _, c := range calls {
			c.end.Store(true)
			c.mu.Lock()
			if c.proxy != nil {
				c.proxy.close()
			}
			if c.carrier != nil {
				c.carrier.Close()
			}
			c.mu.Unlock()
		}
		for _, c := range calls {
			c.wg.Wait()
			if c.recorder != nil {
				_ = os.Remove(c.recorder.file.Name())
			}
		}
	})
	ready := make(chan struct{})
	var readyOnce sync.Once
	var finished atomic.Bool
	var browserMu sync.Mutex
	browserLog, createErr := os.Create(filepath.Join(output, "browser.jsonl"))
	if createErr != nil {
		t.Fatal(createErr)
	}
	defer browserLog.Close()
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		find := func(id string) *outboundSoakCall {
			for _, c := range calls {
				if c.ID == id {
					return c
				}
			}
			return nil
		}
		switch r.URL.Path {
		case "/fixture/config":
			fixtureSpeech := sipSpeechFixtures[0]
			speech := loadSIPSpeech(t, fixtureSpeech.id, fixtureSpeech.hash, 24000)
			for _, c := range calls {
				c.mu.Lock()
				c.speech = speech
				c.mu.Unlock()
			}
			entries := []map[string]any{}
			for _, c := range calls {
				entries = append(entries, map[string]any{"id": c.ID, "media_url": "ws" + strings.TrimPrefix(mediaServer.URL, "http") + "/softphone/media/" + c.ID + "/" + c.row.PeerToken})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"calls": entries, "speech": speech})
		case "/fixture/offer", "/fixture/proxy":
			var req struct{ ID, SDP string }
			if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req) != nil {
				http.Error(w, "invalid", 400)
				return
			}
			c := find(req.ID)
			if c == nil {
				http.Error(w, "unknown", 404)
				return
			}
			c.mu.Lock()
			proxy := c.proxy
			c.mu.Unlock()
			if r.URL.Path == "/fixture/offer" {
				fresh, err := newOutboundSoakProxy(c)
				if err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				c.mu.Lock()
				old := c.proxy
				c.proxy = fresh
				c.mu.Unlock()
				if old != nil {
					old.close()
				}
				proxy = fresh
				_ = json.NewEncoder(w).Encode(map[string]string{"sdp": rewriteSoakCandidates(req.SDP, proxy.out.LocalAddr().(*net.UDPAddr).Port)})
			} else {
				if proxy == nil {
					http.Error(w, "missing offer proxy", 400)
					return
				}
				if err := proxy.setTarget(req.SDP); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"sdp": rewriteSoakCandidates(req.SDP, proxy.in.LocalAddr().(*net.UDPAddr).Port)})
			}
		case "/fixture/clock":
			var req struct {
				ID     string
				Origin float64 `json:"source_origin_ms"`
			}
			if json.NewDecoder(r.Body).Decode(&req) == nil {
				if c := find(req.ID); c != nil {
					c.mu.Lock()
					c.OriginMS = req.Origin
					c.mu.Unlock()
				}
			}
			_, _ = w.Write([]byte(`{}`))
		case "/fixture/ready":
			readyOnce.Do(func() { close(ready) })
			_, _ = w.Write([]byte(`{}`))
		case "/fixture/browser":
			var raw json.RawMessage
			if json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&raw) == nil {
				var batch struct {
					Calls []struct {
						ID      string
						Markers []struct {
							Index int
							AtMS  float64 `json:"at_ms"`
						} `json:"source_markers"`
						Overflow int `json:"source_marker_overflow"`
					}
				}
				if json.Unmarshal(raw, &batch) == nil {
					for _, b := range batch.Calls {
						if c := find(b.ID); c != nil {
							c.mu.Lock()
							if b.Overflow > 0 {
								c.Errors = append(c.Errors, "source marker oracle overflow")
							}
							for _, m := range b.Markers {
								c.sourceMarkers[m.Index] = m.AtMS
								if arrived, ok := c.pendingMarkers[m.Index]; ok {
									c.resolveMarkerLocked(m.Index, m.AtMS, arrived)
									delete(c.pendingMarkers, m.Index)
								}
								delete(c.sourceMarkers, m.Index-256)
							}
							c.mu.Unlock()
						}
					}
				}

				browserMu.Lock()
				_, _ = browserLog.Write(append(raw, '\n'))
				browserMu.Unlock()
			}
			_, _ = w.Write([]byte(`{}`))
		case "/fixture/status":
			_ = json.NewEncoder(w).Encode(map[string]bool{"finished": finished.Load()})
		default:
			http.NotFound(w, r)
		}
	}))
	defer fixture.Close()
	cmd := exec.CommandContext(t.Context(), "bun", "benchmarks/outbound-sip/driver.ts")
	cmd.Env = append(os.Environ(), "TELEPHONY_OUTBOUND_FIXTURE="+fixture.URL)
	logFile, err := os.Create(filepath.Join(output, "browser.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	browserDone := make(chan error, 1)
	go func() { browserDone <- cmd.Wait() }()
	select {
	case <-ready:
	case err := <-browserDone:
		t.Fatalf("browser setup: %v; see %s", err, logFile.Name())
	case <-time.After(90 * time.Second):
		t.Fatal("browser setup timed out")
	}
	// All browser microphones are connected before INVITE/180/answer. This is the
	// ordering of the reported outbound incidents, not an already-answered call.
	for _, c := range calls {
		callSettings := settings
		callSettings.codecs = []string{c.Codec}
		if err := g.startSIPOutbound(&c.row, callSettings, 30, seconds+120); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for _, c := range calls {
		for time.Now().Before(deadline) {
			s := g.outboundSessionByCall(c.ID)
			if s != nil && s.mediaReady.Load() && app.softphones.lookup(c.ID) != nil {
				c.session = s
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if c.session == nil {
			t.Fatal("outbound answer failed", c.ID)
		}
		// Exercise an authenticated in-dialog refresh even in the short CI run.
		if err := c.session.sendSessionRefresh(sipSessionTimer{interval: 90 * time.Second, local: true}); err != nil {
			t.Fatal("outbound session refresh failed", c.ID, err)
		}
	}
	time.Sleep(2 * time.Second)
	for _, c := range calls {
		c.recorder = c.session.media.recorder.Load()
		if c.row.RecordingMode == recordingModeAlways && c.recorder == nil {
			t.Fatal("recording did not start", c.ID)
		}
	}
	start := time.Now()
	cpuStart, _ := rtcProcessCPUSeconds()
	for _, c := range calls {
		c.measurementStart.Store(start.UnixNano())
	}
	ticker := time.NewTicker(time.Second)
	measuredSeconds := seconds
measurementLoop:
	for tick := 0; tick < seconds; tick++ {
		select {
		case <-ticker.C:
		case err := <-browserDone:
			t.Fatalf("browser exited: %v; see %s", err, logFile.Name())
		}
		for _, c := range calls {
			hub := app.softphones.lookup(c.ID)
			if hub == nil {
				t.Error("call lost hub", c.ID)
				continue
			}
			s := hub.serverAudioSnapshot()
			c.mu.Lock()
			c.Samples = append(c.Samples, outboundSoakSample{AtMS: float64(time.Since(start).Milliseconds()), Packets: c.Packets, Pacer: s.CarrierPacer, CaptureQueueMS: s.WebRTC.CaptureMaxQueueMS, CaptureDroppedMS: s.WebRTC.IngressDiscardedMS})
			bad := len(c.Errors) > 0
			last := c.lastPacket
			c.mu.Unlock()
			if bad || (tick > 5 && (last.IsZero() || time.Since(last) > 3*time.Second)) {
				if !bad {
					c.mu.Lock()
					c.Errors = append(c.Errors, "outgoing audio stalled for over three seconds")
					c.mu.Unlock()
				}
				t.Error("audio made no progress or recorded a transport/codec/clock error", c.ID)
				measuredSeconds = tick + 1
				break measurementLoop
			}
		}
		if tick%30 == 29 {
			t.Logf("outbound browser/SIP soak: %d/%d seconds, %d concurrent calls", tick+1, seconds, count)
		}
	}
	ticker.Stop()
	elapsed := time.Since(start)
	cpuEnd, _ := rtcProcessCPUSeconds()
	finished.Store(true)
	select {
	case err := <-browserDone:
		if err != nil {
			t.Error("browser shutdown", err)
		}
	case <-time.After(15 * time.Second):
		t.Error("browser did not finish")
	}
	for _, c := range calls {
		if err := g.Hangup(&c.row); err != nil {
			t.Error("outbound SIP termination", c.ID, err)
		}
		c.end.Store(true)
		c.mu.Lock()
		if c.proxy != nil {
			c.proxy.close()
		}
		if c.carrier != nil {
			c.carrier.Close()
		}
		c.mu.Unlock()
	}
	results := []map[string]any{}
	for _, c := range calls {
		c.wg.Wait()
		if c.recorder != nil {
			select {
			case <-c.recorder.done:
			case <-time.After(10 * time.Second):
				t.Error("recorder did not finalize", c.ID)
			}
			c.RecordingDroppedMS = c.recorder.dropped.Load()
			if c.RecordingDroppedMS > 0 {
				t.Error("recording dropped frames", c.ID, c.RecordingDroppedMS)
			}
			if info, err := os.Stat(c.recorder.file.Name()); err != nil || info.Size() <= 44 {
				t.Error("recording missing", c.ID, err)
			}
		}
		r := c.report(start, elapsed)
		results = append(results, r)
		if pass, _ := r["accepted"].(bool); !pass {
			t.Errorf("outbound call failed acceptance: %s codec=%s profile=%s: %v", c.ID, c.Codec, c.Profile, r["failures"])
		}
	}
	raw, _ := json.MarshalIndent(map[string]any{"scope": "real Chromium production WebRTC/capture DSP, actual outbound INVITE/180/ACK/BYE and local TLS/SRTP carrier; synthetic microphone with natural speech/markers; local platform identity; excludes Telnyx network and Apteva remote proxy", "app_version": app.Manifest().Version, "wall_seconds": elapsed.Seconds(), "completed_seconds": measuredSeconds, "requested_seconds": seconds, "concurrent_calls": count, "process_cpu_one_core_pct": 100 * (cpuEnd - cpuStart) / elapsed.Seconds(), "results": results}, "", "  ")
	if err := os.WriteFile(filepath.Join(output, "results.json"), raw, 0600); err != nil {
		t.Error("save soak results", err)
	}
	t.Logf("Results: %s", filepath.Join(output, "results.json"))
}

func soakEnvInt(t *testing.T, key string, fallback, min, max int) int {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	n, e := strconv.Atoi(value)
	if e != nil || n < min || n > max {
		t.Fatalf("%s must be %d..%d", key, min, max)
	}
	return n
}

func startOutboundSoakCarrier(t *testing.T, app *App, ctx *sdk.AppCtx, calls []*outboundSoakCall) (*sipGateway, didwwSIPSettings) {
	t.Helper()
	dir := t.TempDir()
	writeSIPTestCertificate(t, dir, "localhost")
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	pem, _ := os.ReadFile(filepath.Join(dir, "fullchain.pem"))
	roots.AppendCertsFromPEM(pem)
	ua, err := sipgo.NewUA()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ua.Close() })
	server, err := sipgo.NewServer(ua)
	if err != nil {
		t.Fatal(err)
	}
	cfg := directSIPTestConfig()
	cfg.PublicHost = "127.0.0.1"
	cfg.PublicIP = mustSoakAddr("127.0.0.1")
	cfg.RTPBindIP = cfg.PublicIP
	cfg.RTPPortMin = 44000
	cfg.RTPPortMax = 44999
	cfg.MaxSessions = 128
	cfg.AllowedCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	server.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		if req.GetHeader("Authorization") == nil {
			response := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
			response.AppendHeader(sip.NewHeader("WWW-Authenticate", `Digest realm="local-soak", nonce="local-test-nonce", algorithm=MD5, qop="auth"`))
			_ = tx.Respond(response)
			return
		}
		var c *outboundSoakCall
		for _, candidate := range calls {
			if candidate.row.ToNumber == req.Recipient.User {
				c = candidate
				break
			}
		}
		if c == nil {
			_ = tx.Respond(sip.NewResponseFromRequest(req, 404, "Unknown fixture", nil))
			return
		}
		c.mu.Lock()
		existing := c.carrier
		answer := append([]byte(nil), c.answerBody...)
		c.mu.Unlock()
		if existing != nil {
			response := sip.NewSDPResponseFromRequest(req, answer)
			response.AppendHeader(&sip.ContactHeader{Address: req.Recipient})
			response.AppendHeader(sip.NewHeader("Session-Expires", "90;refresher=uac"))
			if err := tx.Respond(response); err == nil {
				c.mu.Lock()
				c.Refreshes++
				c.mu.Unlock()
			}
			return
		}
		offer, e := parseSIPMediaOfferCodecs(req.Body(), cfg, []string{c.Codec})
		if e != nil {
			t.Error(e)
			return
		}
		udp, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if e != nil {
			t.Error(e)
			return
		}
		key := make([]byte, 30)
		_, _ = rand.Read(key)
		carrier := &sipRTPMedia{conn: udp, remote: net.UDPAddrFromAddrPort(netipSoakPort(offer)), offer: offer}
		carrier.offer.Codec = c.Codec
		carrier.security, e = newSIPMediaSecurityWithLocalKey(carrier.offer, key)
		if e != nil {
			t.Error(e)
			udp.Close()
			return
		}
		c.mu.Lock()
		c.carrier = carrier
		c.mu.Unlock()
		_ = tx.Respond(sip.NewResponseFromRequest(req, 180, "Ringing", nil))
		time.Sleep(1500 * time.Millisecond)
		body, e := buildSIPMediaAnswer(cfg, sipMediaOffer{Secure: true, Codec: c.Codec, PayloadType: map[string]uint8{"PCMA": 8, "G722": 9}[c.Codec], CryptoTag: "1"}, udp.LocalAddr().(*net.UDPAddr).Port, &sipMediaSecurity{LocalKey: key})
		if e != nil {
			t.Error(e)
			return
		}
		response := sip.NewSDPResponseFromRequest(req, body)
		c.mu.Lock()
		c.answerBody = append([]byte(nil), body...)
		c.mu.Unlock()
		response.AppendHeader(&sip.ContactHeader{Address: req.Recipient})
		response.AppendHeader(sip.NewHeader("Session-Expires", "90;refresher=uac"))
		_ = tx.Respond(response)
		c.wg.Add(2)
		go func() { defer c.wg.Done(); c.receiveCarrier() }()
		go func() { defer c.wg.Done(); c.sendCarrier() }()
	})
	server.OnAck(func(*sip.Request, sip.ServerTransaction) {})
	server.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		_ = tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})
	probe, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	address := probe.Addr().String()
	probe.Close()
	ready := make(chan struct{}, 1)
	serverCtx, stop := context.WithCancel(context.WithValue(t.Context(), sipgo.ListenReadyCtxKey, sipgo.ListenReadyCtxValue(ready)))
	t.Cleanup(stop)
	go func() {
		_ = server.ListenAndServeTLS(serverCtx, "tcp", address, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("local TLS carrier unavailable")
	}
	clientUA, e := sipgo.NewUA(sipgo.WithUserAgentHostname("127.0.0.1"), sipgo.WithUserAgenTLSConfig(&tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS12}))
	if e != nil {
		t.Fatal(e)
	}
	client, e := sipgo.NewClient(clientUA)
	if e != nil {
		t.Fatal(e)
	}
	params := sip.NewParams()
	params.Add("transport", "tls")
	contact := sip.ContactHeader{Address: sip.Uri{Scheme: "sips", User: "soak", Host: "127.0.0.1", UriParams: params}}
	gc, cancel := context.WithCancel(t.Context())
	g := &sipGateway{app: app, appCtx: ctx, cfg: cfg, ua: clientUA, client: client, outboundDialogs: sipgo.NewDialogClientCache(client, contact), ctx: gc, cancel: cancel, outboundByCall: map[string]*outboundSIPSession{}, outboundByProviderCall: map[string]*outboundSIPSession{}}
	app.sip.gateway = g
	t.Cleanup(g.Stop)
	_, p, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(p)
	return g, didwwSIPSettings{host: "127.0.0.1", port: port, username: "local-test", password: "local-fixture-password", secure: true, e164: true, scheme: "sip"}
}

func mustSoakAddr(s string) netip.Addr { return netip.MustParseAddr(s) }
func netipSoakPort(offer sipMediaOffer) netip.AddrPort {
	return netip.AddrPortFrom(offer.RemoteAddress, uint16(offer.RemotePort))
}

func (c *outboundSoakCall) receiveCarrier() {
	buf := make([]byte, 2048)
	for !c.end.Load() {
		n, _, err := c.carrier.readPacket(buf)
		if err != nil {
			if !c.end.Load() {
				c.mu.Lock()
				c.Errors = append(c.Errors, "carrier read: "+err.Error())
				c.mu.Unlock()
			}
			return
		}
		packet, err := c.carrier.decodePacketAt(buf[:n], c.carrier.readAt)
		if err != nil {
			c.mu.Lock()
			c.Errors = append(c.Errors, "SRTP decode: "+err.Error())
			c.mu.Unlock()
			continue
		}
		pcm := c.carrier.decodeAudio(packet.Payload)
		now := time.Now()
		c.mu.Lock()
		c.Packets++
		if !c.lastPacket.IsZero() {
			gap := math.Max(0, float64(now.Sub(c.lastPacket))/float64(time.Millisecond)-20)
			if c.measurementStart.Load() != 0 {
				c.LongestGapMS = math.Max(c.LongestGapMS, gap)
				healthy := c.healthyAt(now) && c.healthyAt(c.lastPacket)
				if gap > 80 {
					if len(c.DeliveryGaps) == 200 {
						copy(c.DeliveryGaps, c.DeliveryGaps[1:])
						c.DeliveryGaps = c.DeliveryGaps[:199]
					}
					c.DeliveryGaps = append(c.DeliveryGaps, outboundSoakGap{StartMS: float64(c.lastPacket.UnixNano()-c.measurementStart.Load()) / 1e6, EndMS: float64(now.UnixNano()-c.measurementStart.Load()) / 1e6, InterruptionMS: gap, Healthy: healthy})
				}
				if healthy {
					c.HealthyGapMS = math.Max(c.HealthyGapMS, gap)
				}
			}
		}
		c.lastPacket = now
		c.decoder.Push(pcm, float64(now.Sub(c.Epoch))/float64(time.Millisecond))
		for c.markerCount < len(c.decoder.Markers) {
			m := c.decoder.Markers[c.markerCount]
			c.markerCount++
			if c.OriginMS == 0 {
				continue
			}
			arrived := float64(c.Epoch.UnixNano())/1e6 + m.AtMS
			block := float64(m.ID) + 256*math.Round(((arrived-c.OriginMS)/500-float64(m.ID))/256)
			index := int(block)
			if source, ok := c.sourceMarkers[index]; ok {
				c.resolveMarkerLocked(index, source, arrived)
			} else {
				c.pendingMarkers[index] = arrived
				if len(c.pendingMarkers) > 16 {
					c.Errors = append(c.Errors, "missing source timestamp oracle")
				}
			}

		}
		// Silence against independently known source activity. Marker clocks give
		// alignment; no decoder energy assumption turns replacement audio into speech.
		if c.lastDelay > 0 && c.measurementStart.Load() > 0 && c.healthyAt(now) {
			sourceMS := float64(c.lastMarkerBlock)*500 + float64(now.UnixNano())/1e6 - c.lastDelay - c.lastMarkerWall
			expected := soakExpectedRMS(c.speech, sourceMS-20)
			if expected > 800 && rmsPCM(pcm) < 100 {
				c.silentRun += 20
				c.SilenceMS = math.Max(c.SilenceMS, c.silentRun)
			} else {
				c.silentRun = 0
			}
		}
		c.mu.Unlock()
	}
}

// Source times are captured at the AudioWorklet marker and mapped through the
// browser output clock. Audio-clock drift cannot masquerade as queue growth.
func (c *outboundSoakCall) resolveMarkerLocked(index int, source, arrived float64) {
	delay := arrived - source
	if delay < -50 || delay > 2000 {
		c.Errors = append(c.Errors, fmt.Sprintf("invalid marker clock: %.1fms", delay))
		return
	}
	c.lastDelay = delay
	c.lastMarkerWall = source
	c.lastMarkerBlock = index
	if start := c.measurementStart.Load(); start > 0 && source >= float64(start)/1e6 {
		c.Markers = append(c.Markers, outboundSoakMarker{SourceMS: source, ArrivedMS: arrived, DelayMS: delay, Healthy: c.healthyAt(time.Unix(0, int64(source*1e6))) && c.healthyAt(time.Unix(0, int64(arrived*1e6)))})
	}
	for i := range c.Incidents {
		event := &c.Incidents[i]
		end := float64(c.Epoch.UnixNano())/1e6 + event.EndMS
		if event.RecoveryMS == nil && source >= end {
			recovery := arrived - end
			event.RecoveryMS = &recovery
		}
	}
}

func (c *outboundSoakCall) sendCarrier() {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	start := time.Now()
	seq := uint16(0)
	for !c.end.Load() {
		<-ticker.C
		frame := uint32(time.Since(start) / (20 * time.Millisecond))
		samples := sinePCM(c.carrier.sampleRate(), 440, c.carrier.sampleRate()/50)
		packet := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: c.carrier.offer.PayloadType, SequenceNumber: seq, Timestamp: frame * 160, SSRC: 42}, Payload: c.carrier.encodeAudio(samples)}
		seq++
		if err := c.carrier.writePacket(packet); err != nil {
			if !c.end.Load() {
				c.mu.Lock()
				c.Errors = append(c.Errors, "carrier send: "+err.Error())
				c.mu.Unlock()
			}
			return
		}
	}
}
func soakExpectedRMS(speech []int16, atMS float64) float64 {
	if atMS < 0 {
		return 0
	}
	var energy float64
	for i := 0; i < 480; i++ {
		sample := int(atMS*24) + i
		phase := sample % (24000 * 128)
		within := float64(phase) * 1000 / 24000
		var v int16
		if math.Mod(within, 500) < 200 {
			v = bench.Sample(phase, 24000, 0)
		} else if len(speech) > 0 {
			v = speech[sample%len(speech)]
		}
		energy += float64(v) * float64(v)
	}
	return math.Sqrt(energy / 480)
}
func (c *outboundSoakCall) healthyAt(at time.Time) bool {
	start := c.measurementStart.Load()
	if start == 0 {
		return false
	}
	ms := float64(at.UnixNano()-start) / 1e6
	if ms < 1000 {
		return false
	}
	phase := math.Mod(ms, 30000)
	if c.Profile == "shift180" {
		return phase < 2800 || phase > 12500
	}
	if strings.HasPrefix(c.Profile, "pause") {
		return phase < 2800 || phase > float64(4500+c.PauseMS)
	}
	return true
}
func (c *outboundSoakCall) report(start time.Time, duration time.Duration) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	failures := []string{}
	delays := []float64{}
	early, late := []float64{}, []float64{}
	windows := map[int][]float64{}
	for _, m := range c.Markers {
		if !m.Healthy {
			continue
		}
		delays = append(delays, m.DelayMS)
		at := m.SourceMS - float64(start.UnixNano())/1e6
		bucket := int(at / 60000)
		windows[bucket] = append(windows[bucket], m.DelayMS)
		if at < duration.Seconds()*1000/3 {
			early = append(early, m.DelayMS)
		}
		if at > duration.Seconds()*2000/3 {
			late = append(late, m.DelayMS)
		}
	}
	percentile := func(values []float64, q float64) float64 {
		if len(values) == 0 {
			return 0
		}
		v := append([]float64(nil), values...)
		sort.Float64s(v)
		return v[int(float64(len(v)-1)*q)]
	}
	growth := percentile(late, .5) - percentile(early, .5)
	if (c.Profile == "shift180" || strings.HasPrefix(c.Profile, "pause")) && duration > 12*time.Second {
		expected := int((duration-3*time.Second)/(30*time.Second)) + 1
		if len(c.Incidents) < expected {
			failures = append(failures, "configured fault was not injected")
		}
	}
	if len(delays) < int(duration.Seconds()*.5) {
		failures = append(failures, "too few healthy source markers")
	}
	if growth > 40 {
		failures = append(failures, fmt.Sprintf("accumulating delay: median grew %.1fms", growth))
	}
	if percentile(delays, .95) > 300 {
		failures = append(failures, "healthy source-to-carrier delay exceeds 300ms")
	}
	if c.HealthyGapMS > 160 || c.SilenceMS > 160 {
		failures = append(failures, "healthy speech interruption exceeds 160ms")
	}
	if len(c.Errors) > 0 {
		failures = append(failures, "transport/codec/clock errors")
	}
	if c.RecordingDroppedMS > 0 {
		failures = append(failures, "recording dropped audio")
	}
	for _, e := range c.Incidents {
		end := c.Epoch.Add(time.Duration(e.EndMS * float64(time.Millisecond)))
		if end.After(start.Add(duration - 2*time.Second)) {
			continue
		}
		if e.RecoveryMS == nil || *e.RecoveryMS > 1500 {
			failures = append(failures, "fault failed fresh-content recovery within 1500ms")
		}
	}
	for _, s := range c.Samples {
		if s.CaptureQueueMS > 320 || s.Pacer.MaxQueueMS > 220 {
			failures = append(failures, "queue bound exceeded")
			break
		}
	}
	minutes := []map[string]any{}
	keys := []int{}
	for key := range windows {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	for _, key := range keys {
		minutes = append(minutes, map[string]any{"minute": key, "p50_ms": percentile(windows[key], .5), "p95_ms": percentile(windows[key], .95), "max_ms": percentile(windows[key], 1)})
	}
	return map[string]any{"id": c.ID, "codec": c.Codec, "profile": c.Profile, "accepted": len(failures) == 0, "failures": failures, "healthy_delay_p50_ms": percentile(delays, .5), "healthy_delay_p95_ms": percentile(delays, .95), "median_delay_growth_ms": growth, "longest_speech_delivery_interruption_ms": math.Max(c.LongestGapMS, c.SilenceMS), "longest_healthy_speech_delivery_interruption_ms": math.Max(c.HealthyGapMS, c.SilenceMS), "delay_by_minute": minutes, "evidence": c}
}

// Test-only UDP fault link. STUN/DTLS/RTCP pass unchanged; only encrypted
// microphone RTP is delayed. At most 64 datagrams are retained per connection.
type outboundSoakDatagram struct {
	data []byte
	due  time.Time
}
type outboundSoakProxy struct {
	target  atomic.Pointer[net.UDPAddr]
	in, out *net.UDPConn
	done    chan struct{}
	once    sync.Once
	peer    atomic.Pointer[net.UDPAddr]
	queue   chan outboundSoakDatagram
	c       *outboundSoakCall
	cycle   int64
}

func newOutboundSoakProxy(c *outboundSoakCall) (*outboundSoakProxy, error) {
	in, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, err
	}
	out, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		in.Close()
		return nil, err
	}
	p := &outboundSoakProxy{in: in, out: out, done: make(chan struct{}), queue: make(chan outboundSoakDatagram, 64), c: c, cycle: -1}
	go p.readBrowser()
	go p.readServer()
	go p.schedule()
	return p, nil
}
func rewriteSoakCandidates(sdp string, port int) string {
	result := []string{}
	for _, line := range strings.Split(sdp, "\r\n") {
		if strings.HasPrefix(line, "a=candidate:") || line == "a=end-of-candidates" {
			continue
		}
		result = append(result, line)
	}
	for len(result) > 0 && result[len(result)-1] == "" {
		result = result[:len(result)-1]
	}
	result = append(result, fmt.Sprintf("a=candidate:soak 1 udp 2130706431 127.0.0.1 %d typ host", port), "a=end-of-candidates", "")
	return strings.Join(result, "\r\n")
}
func (p *outboundSoakProxy) setTarget(sdp string) error {
	interfaces, _ := net.InterfaceAddrs()
	local := func(ip net.IP) bool {
		if ip.IsLoopback() {
			return true
		}
		for _, a := range interfaces {
			network, _, _ := net.ParseCIDR(a.String())
			if network.Equal(ip) {
				return true
			}
		}
		return false
	}
	for _, line := range strings.Split(sdp, "\r\n") {
		if !strings.HasPrefix(line, "a=candidate:") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 8 || !strings.EqualFold(f[2], "udp") {
			continue
		}
		ip := net.ParseIP(f[4])
		port, _ := strconv.Atoi(f[5])
		if ip != nil && ip.To4() != nil && local(ip) && port > 0 {
			p.target.Store(&net.UDPAddr{IP: ip, Port: port})
			return nil
		}
	}
	return errors.New("no local UDP ICE candidate")
}
func (p *outboundSoakProxy) close() { p.once.Do(func() { close(p.done); p.in.Close(); p.out.Close() }) }
func (p *outboundSoakProxy) readBrowser() {
	buf := make([]byte, 2048)
	for {
		n, peer, err := p.in.ReadFromUDP(buf)
		if err != nil {
			return
		}
		p.peer.Store(peer)
		now := time.Now()
		due := now
		if n > 12 && buf[0]&0xc0 == 0x80 && buf[1]&0x7f == 111 {
			start := p.c.measurementStart.Load()
			if start > 0 {
				elapsed := now.Sub(time.Unix(0, start))
				phase := elapsed % (30 * time.Second)
				cycle := int64(elapsed / (30 * time.Second))
				jitter := time.Duration(binary.BigEndian.Uint16(buf[2:4])%3) * 8 * time.Millisecond
				if p.c.Profile != "healthy" {
					due = due.Add(jitter)
				}
				if p.c.Profile == "shift180" && phase >= 3*time.Second && phase < 11*time.Second {
					due = due.Add(180 * time.Millisecond)
					p.incident(cycle, 3*time.Second, 11*time.Second)
				}
				pauseEnd := 3*time.Second + time.Duration(p.c.PauseMS)*time.Millisecond
				if strings.HasPrefix(p.c.Profile, "pause") && phase >= 3*time.Second && phase < pauseEnd {
					due = time.Unix(0, start).Add(time.Duration(cycle)*30*time.Second + pauseEnd + jitter)
					p.incident(cycle, 3*time.Second, pauseEnd)
				}
			}
		}
		item := outboundSoakDatagram{append([]byte(nil), buf[:n]...), due}
		select {
		case p.queue <- item:
		case <-p.done:
			return
		default:
			p.c.mu.Lock()
			p.c.Errors = append(p.c.Errors, "fault link overflow")
			p.c.mu.Unlock()
		}
	}
}
func (p *outboundSoakProxy) incident(cycle int64, from, to time.Duration) {
	if p.cycle == cycle {
		return
	}
	p.cycle = cycle
	start := time.Unix(0, p.c.measurementStart.Load()).Add(time.Duration(cycle) * 30 * time.Second)
	p.c.mu.Lock()
	p.c.Incidents = append(p.c.Incidents, outboundSoakIncident{StartMS: float64(start.Add(from).Sub(p.c.Epoch)) / float64(time.Millisecond), EndMS: float64(start.Add(to).Sub(p.c.Epoch)) / float64(time.Millisecond)})
	p.c.mu.Unlock()
}
func (p *outboundSoakProxy) readServer() {
	buf := make([]byte, 2048)
	for {
		n, server, err := p.out.ReadFromUDP(buf)
		if err != nil {
			return
		}
		p.target.Store(server)
		if peer := p.peer.Load(); peer != nil {
			_, _ = p.in.WriteToUDP(buf[:n], peer)
		}
	}
}
func (p *outboundSoakProxy) schedule() {
	pending := make([]outboundSoakDatagram, 0, 64)
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var wake <-chan time.Time
	for {
		if len(pending) > 0 {
			timer.Reset(max(time.Until(pending[0].due), time.Microsecond))
			wake = timer.C
		} else {
			wake = nil
		}
		select {
		case <-p.done:
			return
		case item := <-p.queue:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if len(pending) == 64 {
				p.c.mu.Lock()
				p.c.Errors = append(p.c.Errors, "fault scheduler overflow")
				p.c.mu.Unlock()
				continue
			}
			pending = append(pending, item)
			sort.SliceStable(pending, func(i, j int) bool { return pending[i].due.Before(pending[j].due) })
		case <-wake:
			now := time.Now()
			for len(pending) > 0 && !pending[0].due.After(now) {
				if target := p.target.Load(); target != nil {
					_, _ = p.out.WriteToUDP(pending[0].data, target)
				}
				pending[0] = outboundSoakDatagram{}
				pending = pending[1:]
			}
		}
	}
}
