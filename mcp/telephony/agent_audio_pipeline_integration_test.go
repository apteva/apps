//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"github.com/gobwas/ws"
	"github.com/pion/rtp"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type agentPipelineStats struct {
	InputFrames int64             `json:"input_frames"`
	InputBytes  int64             `json:"input_bytes"`
	Pending     int               `json:"pending"`
	Awaiting    int               `json:"awaiting"`
	Delivery    map[string]any    `json:"delivery"`
	Transcripts json.RawMessage   `json:"transcripts"`
	Events      []json.RawMessage `json:"events"`
}

func agentFixtureJSON(t *testing.T, method, target string, body, out any) {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, target, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 40 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("local fixture request: %T", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		t.Fatalf("local fixture status %d", response.StatusCode)
	}
	if out != nil {
		if json.NewDecoder(response.Body).Decode(out) != nil {
			t.Fatal("invalid fixture response")
		}
	}
}

type agentCarrierProbe struct {
	mu           sync.Mutex
	received     []int16
	arrivals     []map[string]any
	lastArrival  time.Time
	sequenceLoss int
	frames       atomic.Int64
	clears       atomic.Int64
	stop         func()
	send         func([]int16) error
	sampleRate   int
	audible      chan time.Time
}

func (p *agentCarrierProbe) observePCM(pcm []int16) {
	for _, v := range pcm {
		if v > 1000 || v < -1000 {
			select {
			case p.audible <- time.Now():
			default:
			}
			return
		}
	}
}

func newAgentPipeline(t *testing.T, provider, direction string, live bool) (*App, *callRow, *internalAudioHelper, *agentCarrierProbe, string) {
	t.Helper()
	output := os.Getenv("TELEPHONY_INTERNAL_OUTPUT")
	if output == "" {
		t.Fatal("run benchmark:agent-audio")
	}
	output = filepath.Join(output, provider+"-"+direction)
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	core := startInternalAudioHelper(t, "core", output, "")
	server := startInternalAudioHelper(t, "server", output, core.url)
	var auth struct {
		Thread string `json:"thread"`
		Token  string `json:"token"`
	}
	replyAfterMS, _ := strconv.Atoi(os.Getenv("TELEPHONY_LATENCY_REPLY_AFTER_MS"))
	pauseMS, _ := strconv.Atoi(os.Getenv("TELEPHONY_LATENCY_PAUSE_MS"))
	agentFixtureJSON(t, "POST", core.url+"/new", map[string]any{"live": live, "latency": os.Getenv("TELEPHONY_AGENT_AUDIO_LATENCY") == "1", "reply_after_input_ms": replyAfterMS, "input_pause_ms": pauseMS}, &auth)
	platform := &answerPlatform{}
	if provider == "sinch" {
		platform.credentials = &sdk.ConnectionCredentials{Fields: map[string]string{"service_id": "fixture-sinch"}}
	}
	a, ctx := withTelephonyTestContext(t, platform)
	row := testCall(provider+"-"+direction, "in-progress")
	row.ProjectID = ctx.CurrentProject()
	row.PeerKind = peerKindRealtime
	profile := agentAudioProfile{Playout: agentPlayoutConfig{Mode: os.Getenv("TELEPHONY_AGENT_PLAYOUT")}}
	profile.Playout.SpeedPercent, _ = strconv.Atoi(os.Getenv("TELEPHONY_AGENT_SPEED"))
	profile.Playout.TraceFrames, _ = strconv.Atoi(os.Getenv("TELEPHONY_AGENT_TRACE"))
	if live && os.Getenv("TELEPHONY_AGENT_AUDIO_LOCAL_INTERRUPT") == "1" {
		profile.Interruption = "local"
	}
	resolved, err := resolveAgentAudioProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	row.preparedAgentAudio = &resolved

	row.Direction = direction
	row.ThreadID = auth.Thread
	row.AudioBridgeURL = "ws" + strings.TrimPrefix(server.url, "http") + "/api/realtime/audio?agent_id=42&thread=" + url.QueryEscape(auth.Thread) + "&token=" + url.QueryEscape(auth.Token)
	row.CarrierSlug = provider
	if strings.HasPrefix(provider, "sip-") {
		row.CarrierSlug = "telnyx"
	}
	if err := a.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	if row.preparedAgentAudio != nil {
		if _, err := a.pinAgentAudioProfile(&row); err != nil {
			t.Fatal(err)
		}
	}
	probe := &agentCarrierProbe{audible: make(chan time.Time, 1024)}
	if strings.HasPrefix(provider, "conversations-pcm") {
		conn, _, _, err := ws.DefaultDialer.Dial(context.Background(), row.AudioBridgeURL)
		if err != nil {
			t.Fatal(err)
		}
		writer := newWebSocketWriterPump(conn, ws.StateClientSide)
		probe.sampleRate = 24000
		down, up := newPCMResampler(24000, 8000), newPCMResampler(8000, 24000)
		probe.send = func(pcm []int16) error {
			if provider == "conversations-pcm-pcmu" {
				pcm = up.Process(ulawToPCM16(pcm16ToUlaw(down.Process(pcm))))
			}
			return writer.Write(ws.OpBinary, pcm16ToBytes(pcm))
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			var meta struct {
				Type   string `json:"type"`
				ItemID string `json:"item_id"`
				EndMS  int    `json:"audio_end_ms"`
			}
			for {
				raw, op, err := readWebSocketData(conn, ws.StateClientSide, writer)
				if err != nil {
					return
				}
				if op == ws.OpText {
					_ = json.Unmarshal(raw, &meta)
					continue
				}
				if op != ws.OpBinary {
					continue
				}
				probe.frames.Add(1)
				probe.observePCM(bytesToPCM16(raw))
				if meta.Type == "audio.frame" {
					progress, _ := json.Marshal(map[string]any{"type": "playback.progress", "item_id": meta.ItemID, "audio_end_ms": meta.EndMS})
					_ = writer.Write(ws.OpText, progress)
				}
			}
		}()
		probe.stop = func() { conn.Close(); <-done }
	} else if strings.HasPrefix(provider, "sip-") {
		sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		codec := strings.TrimPrefix(provider, "sip-")
		payloadType := uint8(8)
		if codec == "G722" {
			payloadType = 9
		}
		media := &sipRTPMedia{conn: sender, remote: peer.LocalAddr().(*net.UDPAddr), offer: sipMediaOffer{Codec: codec, PayloadType: payloadType, PacketSamples: 160}}
		source := &sipRTPMedia{offer: media.offer}
		var seq uint16
		probe.sampleRate = media.sampleRate()
		probe.send = func(pcm []int16) error {
			packet := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: payloadType, SequenceNumber: seq, Timestamp: uint32(seq) * 160, SSRC: 1234}, Payload: source.encodeAudio(pcm)}
			seq++
			raw, err := packet.Marshal()
			if err != nil {
				return err
			}
			_, err = peer.WriteToUDP(raw, sender.LocalAddr().(*net.UDPAddr))
			return err
		}
		runCtx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			a.bridgeSIPMedia(sipBridgeSession{callID: row.ID, ctx: runCtx, media: media, finish: func(string, error) {}, hangup: func() error { return nil }})
		}()
		go func() {
			buf := make([]byte, 2048)
			decoder := &sipRTPMedia{offer: media.offer}
			var previous uint16
			for {
				n, _, err := peer.ReadFromUDP(buf)
				if err != nil {
					return
				}
				var packet rtp.Packet
				if packet.Unmarshal(buf[:n]) == nil {
					pcm := decoder.decodeAudio(packet.Payload)
					now := time.Now()
					probe.mu.Lock()
					gap := 0.0
					if !probe.lastArrival.IsZero() {
						gap = float64(now.Sub(probe.lastArrival)) / float64(time.Millisecond)
						probe.sequenceLoss += int(uint16(packet.SequenceNumber-previous)) - 1
					}
					previous = packet.SequenceNumber
					probe.lastArrival = now
					probe.received = append(probe.received, pcm...)
					probe.arrivals = append(probe.arrivals, map[string]any{"at_unix_ns": now.UnixNano(), "sequence": packet.SequenceNumber, "rtp_timestamp": packet.Timestamp, "gap_ms": gap})
					probe.mu.Unlock()
					probe.observePCM(pcm)
				}
				probe.frames.Add(1)
			}
		}()
		probe.stop = func() {
			cancel()
			peer.Close()
			sender.Close()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("SIP bridge did not stop")
			}
		}
	} else {
		handlerDone := make(chan struct{})
		telephony := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer close(handlerDone)
			if provider == "twilio" {
				a.handleTwilioMediaStream(w, r)
			} else if provider == "vonage" {
				a.handleVonageMediaStream(w, r)
			} else if provider == "sinch" {
				a.handleSinchMediaStream(w, r)
			} else {
				a.handleTelnyxMediaStream(w, r)
			}
		}))
		path := "/media/" + provider + "/" + row.ID + "/" + row.CallbackSecret
		dialer := ws.DefaultDialer
		if provider == "twilio" {
			t.Setenv("APTEVA_PUBLIC_URL", "https://public.example.test")
			r := httptest.NewRequest("GET", path, nil)
			signature := twilioTestSignature(a.publicRequestURL(r), url.Values{}, "test-auth-token")
			dialer.Header = ws.HandshakeHeaderHTTP(http.Header{"X-Twilio-Signature": {signature}})
		}
		conn, _, _, err := dialer.Dial(context.Background(), "ws"+strings.TrimPrefix(telephony.URL, "http")+path)
		if err != nil {
			telephony.Close()
			t.Fatal(err)
		}
		writer := newWebSocketWriterPump(conn, ws.StateClientSide)
		start := map[string]any{"event": "start", "stream_id": "stream", "streamSid": "stream", "start": map[string]any{"call_control_id": row.CarrierSID, "callSid": row.CarrierSID, "stream_id": "stream"}}
		if provider == "sinch" {
			start = map[string]any{"command": "connect", "version": 1, "callId": row.CarrierSID, "applicationKey": "fixture-sinch", "codec": "PCM", "sampleRate": 16000}
		}
		raw, _ := json.Marshal(start)
		if writer.Write(ws.OpText, raw) != nil {
			t.Fatal("carrier start")
		}
		probe.sampleRate = 16000
		if provider == "twilio" {
			probe.sampleRate = 8000
		}
		probe.send = func(pcm []int16) error {
			raw := pcm16ToBytes(pcm)
			if provider == "vonage" || provider == "sinch" {
				return writer.Write(ws.OpBinary, raw)
			}
			if provider == "twilio" {
				raw = pcm16ToUlaw(pcm)
			}
			frame, _ := json.Marshal(map[string]any{"event": "media", "streamSid": "stream", "media": map[string]string{"payload": base64.StdEncoding.EncodeToString(raw)}})
			return writer.Write(ws.OpText, frame)
		}
		go func() {
			var bufferedThrough time.Time
			for {
				raw, op, err := readWebSocketData(conn, ws.StateClientSide, writer)
				if err != nil {
					return
				}
				if op == ws.OpBinary {
					probe.frames.Add(1)
					continue
				}
				if op != ws.OpText {
					continue
				}
				var frame struct {
					Event   string `json:"event"`
					Command string `json:"command"`
					Media   struct {
						Payload string `json:"payload"`
					} `json:"media"`
					Mark struct {
						Name string `json:"name"`
					} `json:"mark"`
				}
				json.Unmarshal(raw, &frame)
				if frame.Command == "clear" {
					probe.clears.Add(1)
				}
				switch frame.Event {
				case "media":
					payload, _ := base64.StdEncoding.DecodeString(frame.Media.Payload)
					observedPCM := bytesToPCM16(payload)
					if provider == "twilio" {
						observedPCM = ulawToPCM16(payload)
					}
					probe.observePCM(observedPCM)
					count := len(payload) / 2
					if provider == "twilio" {
						count = len(payload)
					}
					if bufferedThrough.Before(time.Now()) {
						bufferedThrough = time.Now()
					}
					bufferedThrough = bufferedThrough.Add(time.Duration(count) * time.Second / time.Duration(probe.sampleRate))
					if live {
						raw, _ := base64.StdEncoding.DecodeString(frame.Media.Payload)
						pcm := bytesToPCM16(raw)
						if provider == "twilio" {
							pcm = ulawToPCM16(raw)
						}
						probe.mu.Lock()
						probe.received = append(probe.received, pcm...)
						probe.mu.Unlock()
					}
					probe.frames.Add(1)
				case "clear":
					bufferedThrough = time.Now()
					probe.clears.Add(1)
				case "mark":
					mark, _ := json.Marshal(map[string]any{"event": "mark", "mark": map[string]string{"name": frame.Mark.Name}})
					delay := max(time.Duration(0), time.Until(bufferedThrough))
					time.AfterFunc(delay, func() { _ = writer.Write(ws.OpText, mark) })
				}
			}
		}()
		probe.stop = func() {
			conn.Close()
			telephony.Close()
			select {
			case <-handlerDone:
			case <-time.After(3 * time.Second):
				t.Error("carrier handler did not finish")
			}
		}
	}
	stopProbe := probe.stop
	var stopOnce sync.Once
	probe.stop = func() { stopOnce.Do(stopProbe) }
	t.Cleanup(probe.stop)
	return a, &row, core, probe, output
}
func waitAgentPipeline(t *testing.T, limit time.Duration, condition func() bool) {
	t.Helper()
	end := time.Now().Add(limit)
	for time.Now().Before(end) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("agent pipeline condition timed out")
}
func TestAgentAudioActualCoreServerCarrierPath(t *testing.T) {
	if os.Getenv("TELEPHONY_INTERNAL_CORE_HELPER") == "" {
		t.Skip("run benchmark:agent-audio")
	}
	for _, direction := range []string{"inbound", "outbound"} {
		for _, provider := range []string{"telnyx", "twilio", "sip-PCMA", "sip-G722", "vonage", "sinch"} {
			t.Run(direction+"/"+provider, func(t *testing.T) {
				a, row, core, probe, output := newAgentPipeline(t, provider, direction, false)
				stop := make(chan struct{})
				micDone := make(chan struct{})
				clips := make(chan []int16, 1)
				var completedClips atomic.Int64
				defer func() { close(stop); <-micDone }()
				go func() {
					defer close(micDone)
					tick := time.NewTicker(20 * time.Millisecond)
					defer tick.Stop()
					var clip []int16
					position := 0
					for {
						select {
						case clip = <-clips:
							position = 0
						case <-stop:
							return
						case <-tick.C:
							pcm := make([]int16, probe.sampleRate/50)
							if position < len(clip) {
								copy(pcm, clip[position:min(position+len(pcm), len(clip))])
								position += len(pcm)
								if position >= len(clip) {
									completedClips.Add(1)
									clip = nil
								}
							}
							if probe.send(pcm) != nil {
								return
							}
						}
					}
				}()
				waitAgentPipeline(t, 2*time.Second, func() bool { s := a.liveAgentAudio(row.ID); return s != nil && s.Evidence != "" })
				s := a.liveAgentAudio(row.ID)
				if !s.Capabilities.Continuous || !s.Capabilities.ScopedCancellation {
					t.Fatal("capability handshake lost", s)
				}
				if !s.Capabilities.PlaybackFlowControl || s.PlaybackCapacityMS != s.Profile.BacklogMS {
					t.Fatal("renderer capacity not negotiated", s)
				}
				// A provider burst larger than our one-second backlog must be
				// paced by the actual Core using receipts, without deleting words.
				beforeBurst := probe.frames.Load()
				agentFixtureJSON(t, "POST", core.url+"/emit", map[string]any{"item": "large-burst", "ms": 2500}, nil)
				expectedBurstPackets := func() int64 {
					if !strings.HasPrefix(provider, "sip-") {
						return 125
					}
					stats := a.liveAgentAudio(row.ID).Playout
					// Speed tuning changes rendered duration, never the source
					// receipt. SIP re-packetizes the adjusted PCM into 20 ms.
					return int64(math.Ceil((2500 + stats.ExpandedMS - stats.CompressedMS) / 20))
				}
				waitAgentPipeline(t, 6*time.Second, func() bool {
					var stats agentPipelineStats
					agentFixtureJSON(t, "GET", core.url+"/stats", nil, &stats)
					return probe.frames.Load() >= beforeBurst+expectedBurstPackets() && stats.Pending == 0 && stats.Awaiting == 0
				})
				if got := probe.frames.Load() - beforeBurst; got != expectedBurstPackets() {
					t.Fatal("rendered burst packet count does not match adjusted duration", got, expectedBurstPackets())
				}

				burst := a.liveAgentAudio(row.ID)
				if len(burst.Clears) != 0 || burst.PlaybackPressure.Exhausted != 0 || burst.CoreLink.OutputOmittedMS != 0 || burst.CoreLink.Reconnects != 0 {
					t.Fatal("large provider burst overflowed or reset the bridge", burst)
				}
				// Reproduce the reviewed call's risk: an actual caller speaks
				// while the renderer has only continuous silence outstanding.
				// Silence must still flow and produce receipts, without clear.
				speech, err := readLocalPromptWAV("testdata/agent-audio/caller-interrupt-fr.wav")
				if err != nil {
					t.Fatal(err)
				}
				beforeQuiet := probe.frames.Load()
				agentFixtureJSON(t, "POST", core.url+"/emit", map[string]any{"item": "silence-only", "ms": 800, "silent": true}, nil)
				waitAgentPipeline(t, 2*time.Second, func() bool { return probe.frames.Load() > beforeQuiet+2 })
				clips <- newPCMResampler(24000, probe.sampleRate).Process(speech)
				clipDuration := time.Duration(len(speech)) * time.Second / 24000
				waitAgentPipeline(t, clipDuration+2*time.Second, func() bool { return completedClips.Load() == 1 })
				if got := len(a.liveAgentAudio(row.ID).Clears); got != 0 {
					t.Fatalf("caller speaking over silence cleared playback: %d", got)
				}
				rearmAt := time.Now().Add(500 * time.Millisecond)
				waitAgentPipeline(t, time.Second, func() bool { return time.Now().After(rearmAt) })
				for cycle := 0; cycle < 3; cycle++ {
					before := probe.frames.Load()
					id := fmt.Sprintf("old-%d", cycle)
					agentFixtureJSON(t, "POST", core.url+"/emit", map[string]any{"item": id, "ms": 1600}, nil)
					waitAgentPipeline(t, 2*time.Second, func() bool { return probe.frames.Load() > before+2 })
					agentFixtureJSON(t, "POST", core.url+"/interrupt", map[string]any{}, nil)
					waitAgentPipeline(t, 2*time.Second, func() bool { return len(a.liveAgentAudio(row.ID).Clears) > cycle })
					receipt := a.liveAgentAudio(row.ID).Clears[cycle]
					if receipt.ItemID != id || receipt.Generation <= 0 || receipt.RemoteConfirmed {
						t.Fatal("incorrect clear evidence", receipt)
					}
					if strings.HasPrefix(provider, "sip-") && receipt.Scope != "local" {
						t.Fatal("SIP claimed remote clear", receipt)
					}
					// Late cancelled model output and its receipts must not revive speech.
					agentFixtureJSON(t, "POST", core.url+"/emit", map[string]any{"item": id, "ms": 100}, nil)
					before = probe.frames.Load()
					agentFixtureJSON(t, "POST", core.url+"/emit", map[string]any{"item": fmt.Sprintf("next-%d", cycle), "ms": 200}, nil)
					waitAgentPipeline(t, 2*time.Second, func() bool { return probe.frames.Load() > before+5 })
					waitAgentPipeline(t, 2*time.Second, func() bool {
						var stats agentPipelineStats
						agentFixtureJSON(t, "GET", core.url+"/stats", nil, &stats)
						return stats.Pending == 0 && stats.Awaiting == 0
					})
				}
				before := probe.frames.Load()
				agentFixtureJSON(t, "POST", core.url+"/emit", map[string]any{"item": "natural-old", "ms": 600}, nil)
				waitAgentPipeline(t, 2*time.Second, func() bool { return probe.frames.Load() > before+2 })
				onset := time.Now()
				clips <- newPCMResampler(24000, probe.sampleRate).Process(speech)
				waitAgentPipeline(t, time.Second, func() bool { return len(a.liveAgentAudio(row.ID).Clears) == 4 })
				naturalClearMS := float64(time.Since(onset)) / float64(time.Millisecond)
				agentFixtureJSON(t, "POST", core.url+"/emit", map[string]any{"item": "natural-next", "ms": 200}, nil)
				waitAgentPipeline(t, 2*time.Second, func() bool {
					var stats agentPipelineStats
					agentFixtureJSON(t, "GET", core.url+"/stats", nil, &stats)
					return stats.Pending == 0 && stats.Awaiting == 0
				})
				var stats agentPipelineStats
				agentFixtureJSON(t, "GET", core.url+"/stats", nil, &stats)
				if stats.InputFrames < 15 || stats.InputBytes < stats.InputFrames*800 {
					t.Fatal("caller stopped flowing during output", stats.InputFrames, stats.InputBytes)
				}
				raw, _ := json.MarshalIndent(map[string]any{"natural_clip_start_to_clear_ms": naturalClearMS, "carrier_frames": probe.frames.Load(), "core": stats, "telephony": a.liveAgentAudio(row.ID)}, "", "  ")
				if os.WriteFile(filepath.Join(output, "result.json"), raw, 0600) != nil {
					t.Fatal("write result")
				}
				probe.stop()
				retained, err := a.db().findCall(row.ID)
				if err != nil {
					t.Fatal(err)
				}
				var report carrierAudioDiagnostics
				if json.Unmarshal([]byte(retained.CarrierAudioDiagnostics), &report) != nil || report.AgentAudio == nil || len(report.AgentAudio.Clears) != 4 {
					t.Fatalf("AI diagnostics lost after bridge cleanup (%s): %s", provider, retained.CarrierAudioDiagnostics)
				}

			})
		}
	}
}
func TestAgentAudioActualLiveCarrierPath(t *testing.T) {
	if os.Getenv("TELEPHONY_AGENT_AUDIO_LIVE") != "1" {
		t.Skip("paid Live verification is opt-in")
	}
	provider := os.Getenv("TELEPHONY_AGENT_AUDIO_CARRIER")
	if provider == "" {
		provider = "telnyx"
	}
	if provider != "telnyx" && provider != "sip-G722" && provider != "sip-PCMA" {
		t.Fatal("unsupported test carrier")
	}
	a, row, core, probe, output := newAgentPipeline(t, provider, "outbound", true)
	response, err := http.Get(core.url + "/caller")
	if err != nil {
		t.Fatal(err)
	}
	caller, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	response.Body.Close()
	if err != nil || len(caller) == 0 {
		t.Fatal("no generated caller audio")
	}
	callerPCM := newPCMResampler(24000, probe.sampleRate).Process(bytesToPCM16(caller))
	t.Cleanup(func() {
		var stats agentPipelineStats
		agentFixtureJSON(t, "GET", core.url+"/stats", nil, &stats)
		raw, _ := json.MarshalIndent(map[string]any{"failed": t.Failed(), "core": stats, "telephony": a.liveAgentAudio(row.ID)}, "", "  ")
		_ = os.WriteFile(filepath.Join(output, "final-state.json"), raw, 0600)
		probe.mu.Lock()
		received := append([]int16(nil), probe.received...)
		probe.mu.Unlock()
		writeSpeechAcceptanceWAV(t, filepath.Join(output, "carrier-output.wav"), probe.sampleRate, received)
		writeSpeechAcceptanceWAV(t, filepath.Join(output, "caller-input.wav"), probe.sampleRate, callerPCM)
	})
	stop := make(chan struct{})
	defer close(stop)
	startCaller := make(chan struct{})
	go func() {
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		position := -1
		signal := startCaller
		for {
			select {
			case <-stop:
				return
			case <-signal:
				position = 0
				signal = nil
			case <-tick.C:
				pcm := make([]int16, probe.sampleRate/50)
				if position >= 0 && position < len(callerPCM) {
					copy(pcm, callerPCM[position:min(position+len(pcm), len(callerPCM))])
					position += len(pcm)
				}
				if probe.send(pcm) != nil {
					return
				}
			}
		}
	}()
	waitAgentPipeline(t, 25*time.Second, func() bool { return probe.frames.Load() >= 30 })
	before := probe.frames.Load()
	onset := time.Now()
	close(startCaller)
	waitAgentPipeline(t, 15*time.Second, func() bool { s := a.liveAgentAudio(row.ID); return s != nil && len(s.Clears) > 0 })
	clearDelay := time.Since(onset)
	waitAgentPipeline(t, 35*time.Second, func() bool {
		var stats agentPipelineStats
		agentFixtureJSON(t, "GET", core.url+"/stats", nil, &stats)
		var entries []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		json.Unmarshal(stats.Transcripts, &entries)
		confirmed := false
		for _, entry := range entries {
			if entry.Role == "assistant" && strings.Contains(strings.ToLower(entry.Content), "mardi") {
				confirmed = true
			}
		}
		return confirmed && probe.frames.Load() > before+50
	})
	var stats agentPipelineStats
	agentFixtureJSON(t, "GET", core.url+"/stats", nil, &stats)
	snapshot := a.liveAgentAudio(row.ID)
	wantEvidence := "carrier_mark"
	if strings.HasPrefix(provider, "sip-") {
		wantEvidence = "rtp_transmitted"
	}
	if snapshot.Evidence != wantEvidence {
		t.Fatal("Telnyx playback certainty overstated", snapshot.Evidence)
	}
	raw, _ := json.MarshalIndent(map[string]any{"model": "gpt-live-1", "path": provider + " loopback -> Telephony -> actual Server proxy -> actual Core -> OpenAI Live", "caller_clip_start_to_clear_ms": float64(clearDelay) / float64(time.Millisecond), "carrier_frames": probe.frames.Load(), "core": stats, "telephony": snapshot}, "", "  ")
	if os.WriteFile(filepath.Join(output, "result.json"), raw, 0600) != nil {
		t.Fatal("write result")
	}
	probe.mu.Lock()
	received := append([]int16(nil), probe.received...)
	probe.mu.Unlock()
	writeSpeechAcceptanceWAV(t, filepath.Join(output, "carrier-output.wav"), probe.sampleRate, received)
	writeSpeechAcceptanceWAV(t, filepath.Join(output, "caller-input.wav"), probe.sampleRate, callerPCM)
	probe.mu.Lock()
	wire, _ := json.MarshalIndent(map[string]any{"arrivals": probe.arrivals, "sequence_loss": probe.sequenceLoss}, "", "  ")
	loss := probe.sequenceLoss
	probe.mu.Unlock()
	if err := os.WriteFile(filepath.Join(output, "rtp-arrivals.json"), wire, 0600); err != nil {
		t.Fatal(err)
	}
	if loss != 0 {
		t.Fatalf("RTP sequence loss=%d", loss)
	}
	t.Logf("Live media resumed; caller speech recognized; clip-start to Telephony clear command %.1f ms", float64(clearDelay)/float64(time.Millisecond))
}
