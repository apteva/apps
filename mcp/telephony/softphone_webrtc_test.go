package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/pion/ice/v4"
	"github.com/pion/logging"
	"github.com/pion/opus"
	"github.com/pion/rtp"
	"github.com/pion/transport/v5/vnet"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

func TestRTCConfigAndJitterBounds(t *testing.T) {
	if c, e := parseSoftphoneRTCConfig(nil); e != nil || c.Enabled {
		t.Fatal(c, e)
	}
	for _, values := range []map[string]string{
		{"softphone_webrtc_enabled": "true", "softphone_webrtc_ice_servers": "broken"},
		{"softphone_webrtc_enabled": "true", "softphone_webrtc_ice_servers": `[{"urls":["https://invalid"]}]`},
		{"softphone_webrtc_enabled": "true", "softphone_webrtc_public_ips": "host.example"},
		{"softphone_webrtc_enabled": "true", "softphone_webrtc_udp_port_min": "9000", "softphone_webrtc_udp_port_max": "8999"},
	} {
		if _, e := parseSoftphoneRTCConfig(values); e == nil {
			t.Fatal("accepted invalid config")
		}
	}
	now := time.Now()
	j := rtcJitter{}
	packet := func(ts uint32) *rtp.Packet { return &rtp.Packet{Header: rtp.Header{Timestamp: ts}, Payload: []byte{1}} }
	if !j.push(packet(0xfffffc00), now) || !j.push(packet(0xfffffc00+960), now) || !j.push(packet(896), now) {
		t.Fatal("wrap rejected")
	}
	if j.push(packet(896), now) {
		t.Fatal("duplicate accepted")
	}
	if j.pop(now) != nil {
		t.Fatal("jitter cushion skipped")
	}
	for _, ts := range []uint32{0xfffffc00, 0xfffffc00 + 960, 896} {
		p := j.pop(now.Add(100 * time.Millisecond))
		if p == nil || p.Timestamp != ts {
			t.Fatal("order", p, ts)
		}
	}
	if j.push(packet(1856), now.Add(10*time.Second)) {
		t.Fatal("seconds-old catchup accepted")
	}
	for k := uint32(0); k < 100; k++ {
		j.push(packet(k*960+2816), now)
	}
	if len(j.packets) > 16 {
		t.Fatal("unbounded jitter queue")
	}
}

func TestRTCUDPNetworkProfiles(t *testing.T) {
	if testing.Short() {
		t.Skip("UDP network benchmark; run without -short")
	}
	for _, profile := range []struct {
		name             string
		kbps             float64
		jitter           time.Duration
		lossEvery        int
		minimum, maximum int
	}{
		{"256k", 256, 0, 0, 85, 100}, {"64k-jitter-loss", 64, 15 * time.Millisecond, 30, 75, 100}, {"24k-constrained", 24, 0, 0, 15, 75},
	} {
		t.Run(profile.name, func(t *testing.T) {
			router, e := vnet.NewRouter(&vnet.RouterConfig{CIDR: "10.42.0.0/24", QueueSize: 256, MinDelay: 10 * time.Millisecond, MaxJitter: profile.jitter, LoggerFactory: logging.NewDefaultLoggerFactory()})
			if e != nil {
				t.Fatal(e)
			}
			a, _ := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{"10.42.0.1"}})
			b, _ := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{"10.42.0.2"}})
			if e := router.AddNet(a); e != nil {
				t.Fatal(e)
			}
			if e := router.AddNet(b); e != nil {
				t.Fatal(e)
			}
			type budget struct {
				at      time.Time
				tokens  float64
				packets int
			}
			budgets := map[string]*budget{}
			var mu sync.Mutex
			router.AddChunkFilter(func(c vnet.Chunk) bool {
				data := c.UserData()
				if len(data) < 12 || data[0]&0xc0 != 0x80 || data[1]&0x7f != 111 {
					return true
				}
				mu.Lock()
				defer mu.Unlock()
				key := c.SourceAddr().String()
				now := time.Now()
				v := budgets[key]
				if v == nil {
					v = &budget{at: now, tokens: 1600}
					budgets[key] = v
				}
				v.tokens = math.Min(1600, v.tokens+now.Sub(v.at).Seconds()*profile.kbps*1000/8)
				v.at = now
				v.packets++
				if profile.lossEvery > 0 && v.packets%profile.lossEvery == 0 {
					return false
				}
				cost := float64(len(data) + 28)
				if v.tokens < cost {
					return false
				}
				v.tokens -= cost
				return true
			})
			if e := router.Start(); e != nil {
				t.Fatal(e)
			}
			defer router.Stop()
			newPC := func(n *vnet.Net) *webrtc.PeerConnection {
				api, e := softphoneRTCAPI(softphoneRTCConfig{}, func(s *webrtc.SettingEngine) {
					s.SetNet(n)
					s.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
					s.SetIncludeLoopbackCandidate(false)
					s.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
				})
				if e != nil {
					t.Fatal(e)
				}
				pc, e := api.NewPeerConnection(webrtc.Configuration{})
				if e != nil {
					t.Fatal(e)
				}
				t.Cleanup(func() { pc.Close() })
				return pc
			}
			pa, pb := newPC(a), newPC(b)
			var receivedA, receivedB atomic.Int64
			add := func(pc *webrtc.PeerConnection, id string) *webrtc.TrackLocalStaticSample {
				track, e := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, id, "test")
				if e != nil {
					t.Fatal(e)
				}
				sender, e := pc.AddTrack(track)
				if e != nil {
					t.Fatal(e)
				}
				go func() {
					buf := make([]byte, 1500)
					for {
						if _, _, e := sender.Read(buf); e != nil {
							return
						}
					}
				}()
				return track
			}
			ta, tb := add(pa, "caller"), add(pb, "adviser")
			observe := func(pc *webrtc.PeerConnection, count *atomic.Int64) {
				pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
					go func() {
						dec, _ := opus.NewDecoderWithOutput(24000, 1)
						pcm := make([]int16, 2880)
						for {
							p, _, e := remote.ReadRTP()
							if e != nil {
								return
							}
							n, e := dec.DecodeToInt16(p.Payload, pcm)
							if e == nil && n > 0 {
								count.Add(1)
							}
						}
					}()
				})
			}
			observe(pa, &receivedA)
			observe(pb, &receivedB)
			offer, _ := pa.CreateOffer(nil)
			ga := webrtc.GatheringCompletePromise(pa)
			if e := pa.SetLocalDescription(offer); e != nil {
				t.Fatal(e)
			}
			select {
			case <-ga:
			case <-time.After(5 * time.Second):
				t.Fatal("offer gathering")
			}
			if e := pb.SetRemoteDescription(*pa.LocalDescription()); e != nil {
				t.Fatal(e)
			}
			answer, _ := pb.CreateAnswer(nil)
			gb := webrtc.GatheringCompletePromise(pb)
			pb.SetLocalDescription(answer)
			select {
			case <-gb:
			case <-time.After(5 * time.Second):
				t.Fatal("answer gathering")
			}
			if e := pa.SetRemoteDescription(*pb.LocalDescription()); e != nil {
				t.Fatal(e)
			}
			deadline := time.Now().Add(5 * time.Second)
			for pa.ConnectionState() != webrtc.PeerConnectionStateConnected || pb.ConnectionState() != webrtc.PeerConnectionStateConnected {
				if time.Now().After(deadline) {
					t.Fatal("ICE connection")
				}
				time.Sleep(10 * time.Millisecond)
			}
			encoder, _ := opus.NewEncoder(opus.WithChannels(1), opus.WithBitrate(32000))
			pcm := make([]float32, 960)
			out := make([]byte, 1275)
			const frames = 160
			for f := 0; f < frames; f++ {
				for k := range pcm {
					pcm[k] = float32(.2 * math.Sin(2*math.Pi*440*float64(f*960+k)/48000))
				}
				n, e := encoder.EncodeFloat32(pcm, out)
				if e != nil {
					t.Fatal(e)
				}
				sample := media.Sample{Data: append([]byte(nil), out[:n]...), Duration: 20 * time.Millisecond}
				ta.WriteSample(sample)
				tb.WriteSample(sample)
				time.Sleep(20 * time.Millisecond)
			}
			time.Sleep(200 * time.Millisecond)
			for _, count := range []int64{receivedA.Load(), receivedB.Load()} {
				pct := int(count * 100 / frames)
				if pct < profile.minimum || pct > profile.maximum {
					t.Fatalf("received %d/%d (%d%%), expected %d–%d%%", count, frames, pct, profile.minimum, profile.maximum)
				}
			}
			t.Logf("actual SRTP/Opus packets decoded: %d/%d and %d/%d at %.0fkbit/s", receivedA.Load(), frames, receivedB.Load(), frames, profile.kbps)
		})
	}
}

func TestRTCOpusRoundTripSpeechBand(t *testing.T) {
	encoder, e := opus.NewEncoder(opus.WithChannels(1), opus.WithBitrate(32000))
	if e != nil {
		t.Fatal(e)
	}
	decoder, e := opus.NewDecoderWithOutput(24000, 1)
	if e != nil {
		t.Fatal(e)
	}
	input := make([]float32, 960)
	out := make([]byte, 1275)
	pcm := make([]int16, 2880)
	bytes := 0
	var energy float64
	for frame := 0; frame < 60; frame++ {
		for k := range input {
			input[k] = float32(.25 * math.Sin(2*math.Pi*440*float64(frame*960+k)/48000))
		}
		n, e := encoder.EncodeFloat32(input, out)
		if e != nil {
			t.Fatal(e)
		}
		bytes += n
		count, e := decoder.DecodeToInt16(out[:n], pcm)
		if e != nil || count != 480 {
			t.Fatal(count, e)
		}
		if frame > 5 {
			for _, v := range pcm[:count] {
				energy += float64(v) * float64(v)
			}
		}
	}
	if energy < 1e9 {
		t.Fatal("decoded speech lost")
	}
	if bps := bytes * 8 * 1000 / (60 * 20); bps > 48000 || bps < 16000 {
		t.Fatal("unexpected bitrate", bps)
	}
}

type rtcTestBrowser struct {
	socket   net.Conn
	pc       *webrtc.PeerConnection
	mic      *webrtc.TrackLocalStaticSample
	caller   chan []byte
	received chan rtcTestRTP
}
type rtcTestRTP struct {
	timestamp uint32
	sequence  uint16
	at        time.Time
}

func rtcTestConnect(t *testing.T, url string, beforeConnect ...func()) *rtcTestBrowser {
	t.Helper()
	socket := dialWS(t, url)
	data, op, e := wsutil.ReadServerData(socket)
	if e != nil || op != ws.OpText {
		t.Fatal(e)
	}
	var config struct {
		Type    string             `json:"type"`
		Servers []webrtc.ICEServer `json:"ice_servers"`
	}
	if json.Unmarshal(data, &config) != nil || config.Type != "webrtc.config" {
		t.Fatal(string(data))
	}
	api, e := softphoneRTCAPI(softphoneRTCConfig{})
	if e != nil {
		t.Fatal(e)
	}
	pc, e := api.NewPeerConnection(webrtc.Configuration{ICEServers: config.Servers})
	if e != nil {
		t.Fatal(e)
	}
	b := &rtcTestBrowser{socket: socket, pc: pc, caller: make(chan []byte, 100), received: make(chan rtcTestRTP, 128)}
	t.Cleanup(func() { socket.Close(); pc.Close() })
	b.mic, e = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "mic", "browser")
	if e != nil {
		t.Fatal(e)
	}
	sender, e := pc.AddTrack(b.mic)
	if e != nil {
		t.Fatal(e)
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, e := sender.Read(buf); e != nil {
				return
			}
		}
	}()
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		go func() {
			for {
				p, _, e := track.ReadRTP()
				if e != nil {
					return
				}
				select {
				case b.caller <- p.Payload:
				default:
				}
				select {
				case b.received <- rtcTestRTP{p.Timestamp, p.SequenceNumber, time.Now()}:
				default:
				}
			}
		}()
	})
	offer, e := pc.CreateOffer(nil)
	if e != nil {
		t.Fatal(e)
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if e := pc.SetLocalDescription(offer); e != nil {
		t.Fatal(e)
	}
	select {
	case <-gather:
	case <-time.After(5 * time.Second):
		t.Fatal("test ICE timeout")
	}
	text, _ := json.Marshal(map[string]string{"type": "webrtc.offer", "sdp": pc.LocalDescription().SDP})
	if e := wsutil.WriteClientText(socket, text); e != nil {
		t.Fatal(e)
	}
	data, op, e = wsutil.ReadServerData(socket)
	if e != nil || op != ws.OpText {
		t.Fatal(e)
	}
	var answer struct {
		Type string `json:"type"`
		SDP  string `json:"sdp"`
	}
	if json.Unmarshal(data, &answer) != nil || answer.Type != "webrtc.answer" {
		t.Fatal(string(data))
	}
	if len(beforeConnect) > 0 {
		beforeConnect[0]()
	}
	if e := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}); e != nil {
		t.Fatal(e)
	}
	if len(beforeConnect) == 0 {
		readSoftphoneEventWithin(t, socket, "ready", 8*time.Second)
	}
	socket.SetReadDeadline(time.Time{})
	return b
}

func TestRTCCallerCancellationCannotReviveCall(t *testing.T) {
	rtcTestCtx(t)
	app := &App{installID: 42}
	insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)
	b := rtcTestConnect(t, server.URL+"/softphone/media/call-soft-1/peer-secret?transport=webrtc", func() {
		if e := app.db().updateStatus("call-soft-1", "completed", ""); e != nil {
			t.Fatal(e)
		}
	})
	b.socket.SetReadDeadline(time.Now().Add(3 * time.Second))
	data, _, e := wsutil.ReadServerData(b.socket)
	if e == nil {
		t.Fatalf("cancelled call attached: %s", data)
	}
	row, _ := app.db().findCall("call-soft-1")
	if row.Status != "completed" || app.softphones.lookup(row.ID) != nil {
		t.Fatal("RTC revived cancelled call")
	}
}

func TestRTCPrivateCoachingUsesAdviserControlOnly(t *testing.T) {
	rtcTestCtx(t)
	app := &App{installID: 42}
	insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)
	peer := dialWS(t, server.URL+"/peer/call-soft-1/cb-secret")
	b := rtcTestConnect(t, server.URL+"/softphone/media/call-soft-1/peer-secret?transport=webrtc")
	if e := wsutil.WriteClientText(b.socket, []byte(`{"type":"media.capabilities","version":2,"versions":[3],"whisper":true}`)); e != nil {
		t.Fatal(e)
	}
	h := app.softphones.lookup("call-soft-1")
	deadline := time.Now().Add(time.Second)
	for {
		h.mu.Lock()
		supported := h.whisperBrowser == h.browser
		h.mu.Unlock()
		if supported {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("RTC adviser did not negotiate coaching")
		}
		time.Sleep(time.Millisecond)
	}
	l := &callListener{done: make(chan struct{}), coachStops: make(chan coachingStop, 4)}
	h.mu.Lock()
	l.coaching.grant.BrowserEpoch = h.browserEpoch
	h.mu.Unlock()
	l.coaching.expires.Store(time.Now().Add(time.Minute).Unix())
	if !h.startCoach(l, 1) || !h.forwardCoach(l, 1, make([]byte, 160), mediaClockMS()) {
		t.Fatal("RTC coach rejected")
	}
	got := readBinaryWithin(t, b.socket, 2*time.Second)
	if len(got) != 176 || binary.LittleEndian.Uint32(got) != coachPlaybackMagic {
		t.Fatal("whisper wire", len(got))
	}
	peer.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
	if data, op, e := wsutil.ReadServerData(peer); e == nil && op == ws.OpBinary && len(data) > 0 {
		t.Fatal("coaching leaked to caller")
	}
	h.stopCoach(l, "operator_stop")
	if h.forwardCoach(l, 1, make([]byte, 160), mediaClockMS()) {
		t.Fatal("stopped coach accepted")
	}
}

func rtcTestCtx(t *testing.T) {
	t.Helper()
	previous := globalCtx
	globalCtx = tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithConfig(map[string]string{"softphone_webrtc_enabled": "true"}))
	t.Cleanup(func() { globalCtx = previous })
}

func TestRTCExistingHubMediaHoldAndReplacement(t *testing.T) {
	rtcTestCtx(t)
	app := &App{installID: 42}
	insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)
	peer := dialWS(t, server.URL+"/peer/call-soft-1/cb-secret")
	b := rtcTestConnect(t, server.URL+"/softphone/media/call-soft-1/peer-secret?transport=webrtc")
	encoder, _ := opus.NewEncoder(opus.WithChannels(1), opus.WithBitrate(32000))
	pcm := make([]float32, 960)
	packet := make([]byte, 1275)
	for k := range pcm {
		pcm[k] = float32(.2 * math.Sin(2*math.Pi*440*float64(k)/48000))
	}
	n, e := encoder.EncodeFloat32(pcm, packet)
	if e != nil {
		t.Fatal(e)
	}
	for k := 0; k < 12; k++ {
		if e := b.mic.WriteSample(media.Sample{Data: packet[:n], Duration: 20 * time.Millisecond}); e != nil {
			t.Fatal(e)
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := readBinaryWithin(t, peer, 3*time.Second)
	if len(got) != 960 {
		t.Fatal("hub PCM", len(got))
	}
	callerPCM := make([]int16, 480)
	for k := range callerPCM {
		callerPCM[k] = int16(9000 * math.Sin(2*math.Pi*660*float64(k)/24000))
	}
	for k := 0; k < 12; k++ {
		if e := wsutil.WriteClientBinary(peer, pcm16ToBytes(callerPCM)); e != nil {
			t.Fatal(e)
		}
		time.Sleep(20 * time.Millisecond)
	}
	decoder, _ := opus.NewDecoderWithOutput(24000, 1)
	out := make([]int16, 2880)
	found := false
	deadline := time.After(3 * time.Second)
	for !found {
		select {
		case p := <-b.caller:
			n, e := decoder.DecodeToInt16(p, out)
			if e == nil {
				for _, v := range out[:n] {
					if v > 1000 || v < -1000 {
						found = true
						break
					}
				}
			}
		case <-deadline:
			t.Fatal("no caller audio over Opus")
		}
	}
	hub := app.softphones.lookup("call-soft-1")
	old := hub.browserWriter()
	hub.setHeld(true)
	hub.forwardMicrophone(old, []byte{1, 0})
	if !hub.held {
		t.Fatal("hold lost")
	}
	hub.setHeld(false)
	// Closing/replacing browser media never closes or replaces the carrier.
	newBrowser := dialWS(t, server.URL+"/softphone/media/call-soft-1/peer-secret")
	readSoftphoneEventWithin(t, newBrowser, "ready", 3*time.Second)
	if hub.peerWriter() == nil || hub.browserWriter() == old {
		t.Fatal("replacement changed carrier")
	}
	hub.forwardMicrophone(old, []byte{1, 0})
	if row, _ := app.db().findCall("call-soft-1"); row.Status != "in-progress" {
		t.Fatal("RTC replacement changed call")
	}
}

func TestRTCFailedSetupPreservesExistingBrowserAndCaller(t *testing.T) {
	rtcTestCtx(t)
	app := &App{installID: 42}
	insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)
	peer := dialWS(t, server.URL+"/peer/call-soft-1/cb-secret")
	browser := dialWS(t, server.URL+"/softphone/media/call-soft-1/peer-secret")
	readSoftphoneEventWithin(t, browser, "ready", 3*time.Second)
	hub := app.softphones.lookup("call-soft-1")
	old := hub.browserWriter()
	rtc := dialWS(t, server.URL+"/softphone/media/call-soft-1/peer-secret?transport=webrtc")
	_, _, _ = wsutil.ReadServerData(rtc)
	if e := wsutil.WriteClientText(rtc, []byte(`{"type":"webrtc.offer","sdp":"invalid"}`)); e != nil {
		t.Fatal(e)
	}
	rtc.Close()
	time.Sleep(30 * time.Millisecond)
	if hub.browserWriter() != old || hub.peerWriter() == nil {
		t.Fatal("failed setup replaced a live socket")
	}
	audio := []byte{1, 0, 2, 0}
	wsutil.WriteClientBinary(peer, audio)
	if got := readBinaryWithin(t, browser, time.Second); string(got) != string(audio) {
		t.Fatal("existing audio interrupted")
	}
}

func TestRTCDisabledAndUnauthorizedDoNotUpgrade(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{installID: 42}
	insertSoftphoneCall(t, app, "in-progress")
	server := softphoneTestServer(t, app)
	for _, token := range []string{"peer-secret", "wrong"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, _, _, err := ws.DefaultDialer.Dial(ctx, "ws"+server.URL[4:]+"/softphone/media/call-soft-1/"+token+"?transport=webrtc")
		cancel()
		if conn != nil {
			conn.Close()
		}
		if err == nil {
			t.Fatal("upgraded disabled or unauthorized RTC")
		}
	}
	if app.softphones.lookup("call-soft-1") != nil {
		t.Fatal("denied requests created hub")
	}
}

func TestRTCDiagnosticsPersistenceAndLossAccounting(t *testing.T) {
	rtcTestCtx(t)
	a := &App{installID: 42}
	insertSoftphoneCall(t, a, "in-progress")
	b := browserAudioDiagnostics{MediaTransport: "webrtc", Codec: "opus", MicrophoneMuted: true,
		WebRTC: &browserWebRTCStats{Protocol: "udp", CandidateType: "relay", PacketsLost: 3, PacketsDiscarded: 2, ConcealedMS: 20, JitterMS: math.NaN(), SendBitrateBPS: math.Inf(1)},
		Server: &serverAudioDiagnostics{WebRTC: rtcMediaSnapshot{OutboundDroppedMS: 40, IngressRejectedPackets: 1}}}
	if e := a.db().updateBrowserAudioDiagnostics("call-soft-1", b); e != nil {
		t.Fatal(e)
	}
	row, e := a.db().findCall("call-soft-1")
	if e != nil {
		t.Fatal(e)
	}
	var saved browserAudioDiagnostics
	if e := json.Unmarshal([]byte(row.BrowserAudioDiagnostics), &saved); e != nil {
		t.Fatal(e)
	}
	if saved.MediaTransport != "webrtc" || saved.Codec != "opus" || saved.WebRTC.PacketsLost != 3 || saved.WebRTC.JitterMS != 0 || saved.WebRTC.SendBitrateBPS != 0 {
		t.Fatal(saved)
	}
	s := summarizeAudioDashboard(row)
	if s.Metrics["webrtc_packets_discarded"] != 2 || s.Metrics["server_webrtc_outbound_dropped_ms"] != 40 || s.Metrics["webrtc_concealed_ms"] != 20 {
		t.Fatal(s.Metrics)
	}
	if !containsString(s.Stages, "telephony_to_browser") || !containsString(s.Stages, "browser_to_telephony") || saved.PlaybackDroppedMS != 0 || row.Status != "in-progress" {
		t.Fatal(s.Stages, row.Status)
	}
	// Concealment is an RTP reconstruction statistic, never fabricated PCM loss.
	clean := &callRow{BrowserAudioDiagnostics: `{"media_transport":"webrtc","codec":"opus","microphone_muted":true,"webrtc":{"concealedMs":20}}`}
	if containsString(summarizeAudioDashboard(clean).Issues, "dropped_audio") {
		t.Fatal("concealment/mute classified as discarded PCM")
	}
	stats := &rtcMediaStats{}
	for range 100 {
		stats.drop("webrtc_playback_overflow", "carrier_to_operator", 1, 20)
	}
	v := stats.snapshot()
	if v.OutboundDroppedMS != 2000 || len(v.Events) != 32 {
		t.Fatal(v)
	}
	for _, event := range v.Events {
		if _, e := time.Parse(time.RFC3339Nano, event.Timestamp); e != nil {
			t.Fatal(event)
		}
	}
}

func TestRTCOutputQueueRejectsOldSpeechAfterProcessPause(t *testing.T) {
	now := time.Now()
	frames := make(chan rtcPCMFrame, 6)
	stats := &rtcMediaStats{}
	for range 5 {
		frames <- rtcPCMFrame{pcm: make([]int16, 480), expires: now.Add(-7 * time.Second)}
	}
	fresh := make([]int16, 480)
	fresh[0] = 123
	frames <- rtcPCMFrame{pcm: fresh, expires: now.Add(100 * time.Millisecond)}
	frame, open := nextRTCFrame(frames, now, stats)
	if !open || len(frame.pcm) != 480 || frame.pcm[0] != 123 || len(frames) != 0 {
		t.Fatal("old speech replayed or fresh speech lost")
	}
	if v := stats.snapshot(); v.OutboundDroppedMS != 100 || len(v.Events) != 5 {
		t.Fatal(v)
	}
	if frame, open := nextRTCFrame(frames, now, stats); frame.pcm != nil || !open {
		t.Fatal("empty queue should send silence")
	}
	close(frames)
	if _, open := nextRTCFrame(frames, now, stats); open {
		t.Fatal("closed queue revived")
	}
}

func TestRTCJitterExpiredPacketsRemainDiagnosable(t *testing.T) {
	now := time.Now()
	stats := &rtcMediaStats{}
	j := rtcJitter{discard: func(p *rtp.Packet) {
		stats.drop("webrtc_rtp_rejected", "operator_to_carrier", uint64(p.SequenceNumber), 0)
	}}
	if !j.push(&rtp.Packet{Header: rtp.Header{Timestamp: 960, SequenceNumber: 17}, Payload: []byte{1}}, now) {
		t.Fatal("fresh packet rejected")
	}
	if j.pop(now.Add(7*time.Second)) != nil {
		t.Fatal("expired packet replayed")
	}
	v := stats.snapshot()
	if v.IngressRejectedPackets != 1 || len(v.Events) != 1 || v.Events[0].Sequence != 17 {
		t.Fatal("expiry lost diagnostic evidence", v)
	}
}

func TestRTPLiveClockCannotAccumulateEncoderSchedulingDelay(t *testing.T) {
	start := time.Now()
	steady := rtcRTPClock{base: start}
	for i := 1; i <= 100; i++ {
		tick := start.Add(time.Duration(i) * 20 * time.Millisecond)
		if steady.sent && steady.slot(tick) <= steady.lastSlot {
			t.Fatal("encoder phase suppressed a normal tick")
		}
		ts, skipped := steady.next(tick.Add(4 * time.Millisecond))
		if ts != uint32(i*960) || skipped != 0 {
			t.Fatal("steady RTP clock drift", ts, skipped)
		}
	}
	clock := rtcRTPClock{baseTS: 0xfffffe00}
	first, _ := clock.next(start)
	for i := 1; i <= 200; i++ {
		now := start.Add(time.Duration(i) * 30 * time.Millisecond) // slower than 20ms encoder cadence
		ts, _ := clock.next(now)
		elapsed := time.Duration(uint32(ts-first)) * time.Second / 48000
		if gap := now.Sub(start) - elapsed; gap < 0 || gap >= 20*time.Millisecond {
			t.Fatal("RTP clock accumulated delay", gap)
		}
	}
	before, _ := clock.next(start.Add(7 * time.Second))
	after, skipped := clock.next(start.Add(12 * time.Second))
	if uint32(after-before) != 48000*5 || skipped != 4980 {
		t.Fatal("process pause did not advance live RTP timeline", before, after, skipped)
	}
	stats := &rtcMediaStats{}
	stats.drop("webrtc_pacing_gap", "carrier_to_operator", 0, skipped)
	if v := stats.snapshot(); v.PacingSkippedMS != 4980 || v.OutboundDroppedMS != 0 {
		t.Fatal("pacing gap double counted as PCM discard", v)
	}
}

func TestRTCSenderWireCadencePreservesLiveTime(t *testing.T) {
	rtcTestCtx(t)
	a := &App{installID: 42}
	insertSoftphoneCall(t, a, "in-progress")
	server := softphoneTestServer(t, a)
	b := rtcTestConnect(t, server.URL+"/softphone/media/call-soft-1/peer-secret?transport=webrtc")
	var first, last rtcTestRTP
	count := 0
	deadline := time.After(2 * time.Second)
	for {
		select {
		case packet := <-b.received:
			if count == 0 {
				first = packet
			} else if uint16(packet.sequence-last.sequence) != 1 {
				t.Fatal("sender invented a sequence loss")
			}
			last = packet
			count++
		case <-deadline:
			if count < 70 {
				t.Fatal("encoder phase suppressed normal packet cadence", count)
			}
			elapsed := last.at.Sub(first.at)
			rtpElapsed := time.Duration(uint32(last.timestamp-first.timestamp)) * time.Second / 48000
			if delta := elapsed - rtpElapsed; delta > 80*time.Millisecond || delta < -80*time.Millisecond {
				t.Fatal("wire RTP clock fell behind wall time", elapsed, rtpElapsed)
			}
			return
		}
	}
}

func TestRTCTransportPaddingIsNotAudioLoss(t *testing.T) {
	stats := &rtcMediaStats{}
	for range 100 {
		if !ignoreRTCPadding(&rtp.Packet{Header: rtp.Header{Padding: true}}, stats) {
			t.Fatal("valid transport padding rejected")
		}
	}
	if ignoreRTCPadding(&rtp.Packet{Payload: []byte{1}}, stats) || ignoreRTCPadding(&rtp.Packet{}, stats) {
		t.Fatal("audio or malformed empty payload silently ignored")
	}
	v := stats.snapshot()
	if v.IngressPaddingPackets != 100 || v.IngressRejectedPackets != 0 || v.IngressQueueDrops != 0 || len(v.Events) != 0 {
		t.Fatal("padding reported as audio fault", v)
	}
}
