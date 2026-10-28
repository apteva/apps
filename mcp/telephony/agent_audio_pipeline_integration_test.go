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
	mu            sync.Mutex
	received      []int16
	gaps          []agentPlayoutGap // audible underruns by received-sample index
	playedThrough time.Time         // simulated carrier playout clock (WebSocket carriers)
	arrivals      []map[string]any
	lastArrival   time.Time
	sequenceLoss  int
	frames        atomic.Int64
	clears        atomic.Int64
	stop          func()
	send          func([]int16) error
	sampleRate    int
	audible       chan time.Time
}

// recordArrival appends carrier audio and simulates real-time carrier playout:
// a frame that arrives after everything earlier finished playing leaves the
// caller hearing silence for that long.
func (p *agentCarrierProbe) recordArrival(pcm []int16, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Carrier jitter buffers absorb up to a packet of lateness.
	if !p.playedThrough.IsZero() && at.Sub(p.playedThrough) > 20*time.Millisecond {
		p.gaps = append(p.gaps, agentPlayoutGap{At: len(p.received), MS: float64(at.Sub(p.playedThrough)) / float64(time.Millisecond)})
	}
	if p.playedThrough.Before(at) {
		p.playedThrough = at
	}
	p.playedThrough = p.playedThrough.Add(time.Duration(len(pcm)) * time.Second / time.Duration(p.sampleRate))
	p.received = append(p.received, pcm...)
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
					if len(probe.arrivals) > 0 {
						if last, ok := probe.arrivals[len(probe.arrivals)-1]["rtp_timestamp"].(uint32); ok && packet.Timestamp-last > 160 && packet.Timestamp-last < 1<<31 {
							probe.gaps = append(probe.gaps, agentPlayoutGap{At: len(probe.received), MS: float64(packet.Timestamp-last-160) / 8})
						}
					}
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
					probe.recordArrival(bytesToPCM16(raw), time.Now())
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
					probe.recordArrival(observedPCM, time.Now())
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
				// What the caller hears must be a seamless tone: real-time
				// delivery, then one 300 ms upstream stall mid-reply. 100 ms
				// chunks keep the 500 Hz phase continuous across emits.
				for _, stall := range []time.Duration{0, 300 * time.Millisecond} {
					item := fmt.Sprintf("continuity-%d", stall/time.Millisecond)
					probe.mu.Lock()
					from := len(probe.received)
					probe.mu.Unlock()
					// Provider pace: exactly real time on an absolute schedule.
					start := time.Now()
					for i := 0; i < 30; i++ {
						due := start.Add(time.Duration(i) * 100 * time.Millisecond)
						if i >= 15 {
							due = due.Add(stall)
						}
						time.Sleep(time.Until(due))
						agentFixtureJSON(t, "POST", core.url+"/emit", map[string]any{"item": item, "ms": 100}, nil)
					}
					waitAgentPipeline(t, 6*time.Second, func() bool {
						var stats agentPipelineStats
						agentFixtureJSON(t, "GET", core.url+"/stats", nil, &stats)
						return stats.Pending == 0 && stats.Awaiting == 0
					})
					time.Sleep(200 * time.Millisecond)
					probe.mu.Lock()
					heard := append([]int16(nil), probe.received[from:]...)
					var gaps []agentPlayoutGap
					for _, g := range probe.gaps {
						if g.At > from {
							gaps = append(gaps, agentPlayoutGap{At: g.At - from, MS: g.MS})
						}
					}
					probe.mu.Unlock()
					c := agentToneContinuity(heard, probe.sampleRate, 500, gaps)
					t.Logf("%s continuity: %+v", item, c)
					if c.Samples < probe.sampleRate*25/10 {
						t.Fatalf("%s: tone truncated: %+v", item, c)
					}
					if c.Clicks != 0 || c.HardEdges != 0 {
						t.Fatalf("%s: audible discontinuities in caller audio: %+v", item, c)
					}
					// Every interruption must be soft (checked above). Real-time
					// loopback delivery on a busy host may still starve once.
					if allowed := map[bool]int{true: 1, false: 2}[stall == 0]; c.Holes > allowed || c.Gaps > allowed {
						t.Fatalf("%s: interruptions inside one reply (allowed %d): %+v", item, allowed, c)
					}
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
	// A barge-in clear happens only if the caller overlaps assistant speech;
	// with provider interruption a short greeting may already have ended.
	// Measure it when it occurs; the reply below is the required outcome.
	clearDelay := time.Duration(-1)
	for limit := time.Now().Add(15 * time.Second); time.Now().Before(limit); time.Sleep(20 * time.Millisecond) {
		if s := a.liveAgentAudio(row.ID); s != nil && len(s.Clears) > 0 {
			clearDelay = time.Since(onset)
			break
		}
		var stats agentPipelineStats
		agentFixtureJSON(t, "GET", core.url+"/stats", nil, &stats)
		if strings.Contains(strings.ToLower(string(stats.Transcripts)), "mardi") {
			break
		}
	}
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
	probe.mu.Lock()
	gaps := append([]agentPlayoutGap(nil), probe.gaps...)
	probe.mu.Unlock()
	edges := agentSpeechEdges(received, probe.sampleRate, gaps)
	t.Logf("live caller audio: %+v", edges)
	for _, g := range gaps {
		t.Logf("live playout gap at %.3fs: %.0f ms", float64(g.At)/float64(probe.sampleRate), g.MS)
	}
	if edges.HardEdges != 0 {
		t.Fatalf("live reply audio has hard edges at playout gaps: %+v", edges)
	}
	if clearDelay >= 0 {
		t.Logf("Live media resumed; caller speech recognized; clip-start to Telephony clear command %.1f ms", float64(clearDelay)/float64(time.Millisecond))
	} else {
		t.Log("Live media resumed; caller speech recognized and answered; no overlap needed a clear")
	}
}

type agentPlayoutGap struct {
	At int     `json:"at"`
	MS float64 `json:"ms"`
}

type agentToneEvidence struct {
	Samples       int     `json:"samples"`
	Amplitude     float64 `json:"amplitude"`
	MaxStep       float64 `json:"max_step"`
	AllowedStep   float64 `json:"allowed_step"`
	Clicks        int     `json:"clicks"`
	ClickAt       []int   `json:"click_at,omitempty"`
	Holes         int     `json:"holes"`
	LongestHoleMS float64 `json:"longest_hole_ms"`
	Gaps          int     `json:"gaps"`
	GapMS         float64 `json:"gap_ms"`
	HardEdges     int     `json:"hard_edges"`
}

// agentToneContinuity scores decoded caller audio of a steady tone against
// real playout timing. A click is a step the tone cannot produce (plus codec
// noise) where playout was continuous. At a real gap the caller hears silence,
// so the audio must fade into and out of it: an edge above 30% of the tone's
// amplitude is a hard edge. A hole is ≥2 ms of near-silence inside the reply.
func agentToneContinuity(pcm []int16, rate int, hz float64, gaps []agentPlayoutGap) agentToneEvidence {
	var e agentToneEvidence
	first, last := -1, -1
	for i, v := range pcm {
		if v > 2000 || v < -2000 {
			if first < 0 {
				first = i
			}
			last = i
		}
		e.Amplitude = math.Max(e.Amplitude, math.Abs(float64(v)))
	}
	if first < 0 {
		return e
	}
	pcm = pcm[first : last+1]
	gapAt := map[int]bool{}
	for _, g := range gaps {
		at := g.At - first
		if at <= 0 || at >= len(pcm) {
			continue
		}
		gapAt[at] = true
		e.Gaps++
		e.GapMS += g.MS
		if math.Abs(float64(pcm[at-1])) > e.Amplitude*0.3 || math.Abs(float64(pcm[at])) > e.Amplitude*0.3 {
			e.HardEdges++
		}
	}
	e.Samples = len(pcm)
	e.AllowedStep = e.Amplitude*2*math.Pi*hz/float64(rate)*1.15 + 600
	quiet, minHole := 0, rate/500
	for i := range pcm {
		if i > 0 && !gapAt[i] {
			step := math.Abs(float64(pcm[i]) - float64(pcm[i-1]))
			e.MaxStep = math.Max(e.MaxStep, step)
			if step > e.AllowedStep {
				e.Clicks++
				if len(e.ClickAt) < 8 {
					e.ClickAt = append(e.ClickAt, i)
				}
			}
		}
		if pcm[i] > -300 && pcm[i] < 300 {
			quiet++
			continue
		}
		if quiet >= minHole {
			e.Holes++
			e.LongestHoleMS = math.Max(e.LongestHoleMS, float64(quiet)*1000/float64(rate))
		}
		quiet = 0
	}
	return e
}

type agentSpeechEdgeEvidence struct {
	Seconds   float64 `json:"seconds"`
	Gaps      int     `json:"gaps"`
	GapMS     float64 `json:"gap_ms"`
	HardEdges int     `json:"hard_edges"`
}

// agentSpeechEdges checks real speech at audible playout gaps: the sample on
// either side of a gap must be faded relative to the 20 ms of speech around it.
func agentSpeechEdges(pcm []int16, rate int, gaps []agentPlayoutGap) agentSpeechEdgeEvidence {
	e := agentSpeechEdgeEvidence{Seconds: float64(len(pcm)) / float64(rate)}
	window := rate / 50
	level := func(from, to int) float64 {
		peak := 0.0
		for i := max(0, from); i < min(len(pcm), to); i++ {
			peak = math.Max(peak, math.Abs(float64(pcm[i])))
		}
		return peak
	}
	for _, g := range gaps {
		if g.At <= 0 || g.At >= len(pcm) {
			continue
		}
		e.Gaps++
		e.GapMS += g.MS
		before, after := level(g.At-window, g.At), level(g.At, g.At+window)
		if before > 1500 && math.Abs(float64(pcm[g.At-1])) > before*0.3 || after > 1500 && math.Abs(float64(pcm[g.At])) > after*0.3 {
			e.HardEdges++
		}
	}
	return e
}
