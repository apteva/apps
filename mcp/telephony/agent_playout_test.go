package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestAgentPlayoutConfig(t *testing.T) {
	p, err := resolveAgentAudioProfile(agentAudioProfile{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Playout.Mode != "fixed" || p.Playout.SpeedPercent != 0 || p.Playout.TraceFrames != 0 || p.Playout.RecoveryIdleMS != 1000 {
		t.Fatal(p)
	}
	for _, patch := range []string{`{"mode":"unknown"}`, `{"speed_percent":4}`, `{"speed_percent":-1}`, `{"min_reserve_ms":80}`, `{"max_reserve_ms":420}`, `{"recovery_idle_ms":100}`, `{"stable_ms":100}`, `{"trace_frames":2001}`, `{"match_correlation":0.5}`} {
		var c agentPlayoutConfig
		_ = json.Unmarshal([]byte(patch), &c)
		if _, err := resolveAgentPlayoutConfig(c, 60, 1000); err == nil {
			t.Fatal("accepted invalid policy", patch)
		}
	}
}
func TestAgentPlayoutBurstEstimator(t *testing.T) {
	s := &agentAudioSession{profile: agentAudioProfile{Mode: "continuous", Playout: agentPlayoutConfig{Mode: "adaptive", StableMS: 1000}}}
	a := newAgentPlayoutTuning(s, 60)
	b := a
	base := time.Unix(100, 0)
	for i := 0; i < 10; i++ {
		at := base.Add(time.Duration(i) * 100 * time.Millisecond)
		a.observe(at, 100*time.Millisecond)
		for j := 0; j < 5; j++ {
			b.observe(at.Add(time.Duration(j)*time.Millisecond), 20*time.Millisecond)
		}
	}
	if a.stats.TargetMS != 60 || b.stats.TargetMS != 60 {
		t.Fatal("healthy split bursts inflated reserve", a.stats, b.stats)
	}
	a.observe(base.Add(1200*time.Millisecond), 100*time.Millisecond)
	if a.stats.TargetMS != 200 {
		t.Fatal(a.stats)
	}
	for i := 13; i < 50; i++ {
		a.observe(base.Add(time.Duration(i)*100*time.Millisecond), 100*time.Millisecond)
	}
	if a.stats.TargetMS >= 200 || a.stats.TargetMS < 60 {
		t.Fatal("no bounded decay", a.stats)
	}
	before := a.stats.TargetMS
	a.observe(base.Add(30*time.Second), 100*time.Millisecond)
	if a.stats.TargetMS != before {
		t.Fatal("silence mistaken for jitter")
	}
}
func TestAgentPlayoutCycleQualityAndRateBound(t *testing.T) {
	for _, rate := range []int{8000, 16000, 24000} {
		for _, dir := range []int{-1, 1} {
			cfg := agentPlayoutConfig{SpeedPercent: 3}
			c, _ := resolveAgentPlayoutConfig(cfg, 60, 1000)
			a := agentPlayoutTuning{cfg: c, stats: agentPlayoutStats{TargetMS: 60}}
			total, source := 0, 0
			var output []int16
			maxJump := 0
			for i := 0; i < 1000; i++ {
				pcm := make([]int16, rate/50)
				for j := range pcm {
					pcm[j] = int16(9000 * math.Sin(2*math.Pi*200*float64(i*len(pcm)+j)/float64(rate)))
				}
				queued := 0
				if dir < 0 {
					queued = 200
				}
				out := a.process(pcm, rate, queued, realtimeBridgeControl{ItemID: "voice", Generation: 1}, time.Unix(0, int64(i)*20000000))
				if out[0] != pcm[0] || out[len(out)-1] != pcm[len(pcm)-1] {
					t.Fatal("changed chunk boundary")
				}
				for j := 1; j < len(out); j++ {
					maxJump = max(maxJump, int(math.Abs(float64(out[j])-float64(out[j-1]))))
				}
				source += len(pcm)
				total += len(out)
				output = append(output, out...)
			}
			delta := total - source
			if delta*dir <= 0 || math.Abs(float64(delta)) > float64(source)*.03001 {
				t.Fatal("rate budget", rate, dir, delta, source)
			}
			crossings := 0
			for i := 1; i < len(output); i++ {
				if output[i-1] <= 0 && output[i] > 0 {
					crossings++
				}
			}
			hz := float64(crossings) * float64(rate) / float64(len(output))
			if math.Abs(hz-200) > 1 || maxJump > int(9000*2*math.Pi*200/float64(rate))+5 {
				t.Fatal("pitch/click gate", rate, hz, maxJump)
			}
			t.Logf("rate=%d direction=%d duration_change=%.3f%% pitch_hz=%.3f", rate, dir, 100*float64(delta)/float64(source), hz)
		}
	}
	for _, kind := range []string{"noise", "silence", "impulse"} {
		x := make([]int16, 320)
		r := rand.New(rand.NewSource(42))
		for i := range x {
			if kind == "noise" {
				x[i] = int16(r.Intn(20000) - 10000)
			}
		}
		if kind == "impulse" {
			x[160] = 20000
		}
		for _, debt := range []int{-160, 160} {
			y, d := matchSpeechCycle(x, 16000, debt, .97)
			if d != 0 || !slices.Equal(x, y) {
				t.Fatal("altered unsuitable audio", kind)
			}
		}
	}
}
func TestAgentPlayoutSIPRetryAndSourceReceipts(t *testing.T) {
	p := manualAgentPacer("G722", 60*time.Millisecond)
	p.agentSession.profile.Playout = agentPlayoutConfig{Mode: "adaptive", SpeedPercent: 3}
	base := time.Unix(100, 0)
	for i := 0; i < 30; i++ {
		x := make([]int16, 320)
		for j := range x {
			x[j] = int16(9000 * math.Sin(2*math.Pi*200*float64(j)/16000))
		}
		if _, err := p.enqueueAt([]sipRTPOutboundPacket{{pcm: x, itemID: "a", generation: 1, audioEndMS: (i + 1) * 20}}, base.Add(time.Duration(i)*20*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	before := p.agentPlayout
	oversized := make([]int16, 16000*30)
	if _, err := p.enqueueAt([]sipRTPOutboundPacket{{pcm: oversized, itemID: "a", generation: 1}}, base.Add(time.Second)); err != errCarrierPacerOverflow {
		t.Fatal(err)
	}
	if p.agentPlayout != before {
		t.Fatal("failed admission changed controller")
	}
	last := 0
	for tick := 600; tick < 2000; tick += 20 {
		if !p.prepareAgentTick(base.Add(time.Duration(tick) * time.Millisecond)) {
			continue
		}
		packet, ok := p.dequeueLivePacket(base.Add(time.Duration(tick) * time.Millisecond))
		if !ok {
			continue
		}
		if packet.audioEndMS < last || packet.audioEndMS > 600 {
			t.Fatal("invalid source receipt", packet.audioEndMS, last)
		}
		last = packet.audioEndMS
	}
	if last != 600 {
		t.Fatal("final source receipt", last)
	}
	p.drain()
	if p.agentPlayout.tuning.debt != 0 || !p.agentPlayout.tuning.lastArrival.IsZero() {
		t.Fatal("clear retained rate debt")
	}
}

// Same real speech and arrivals for each mode. A green replay is not proof that
// a 300 ms provider outage sounds good: report gaps and delay explicitly.
func TestAgentPlayoutRecordedSpeechComparison(t *testing.T) {
	for _, codec := range []string{"G722", "PCMA"} {
		input := loadSIPSpeech(t, sipSpeechFixtures[0].id, sipSpeechFixtures[0].hash, 24000)
		for len(input) < 24000*12 {
			input = append(input, input...)
		}
		input = input[:24000*12]
		for _, pattern := range []string{"normal", "jitter", "burst320", "slow102", "long_stall"} {
			for _, mode := range []string{"fixed", "adaptive", "adaptive_speed"} {
				t.Run(codec+"/"+pattern+"/"+mode, func(t *testing.T) {
					p := manualAgentPacer(codec, 60*time.Millisecond)
					// Sample preservation is asserted without stretching; drift
					// compensation has its own replay and quality tests.
					p.agentSession.profile.Playout.Drift = "off"
					if mode != "fixed" {
						p.agentSession.profile.Playout = agentPlayoutConfig{Mode: "adaptive", Drift: "off"}
						if mode == "adaptive_speed" {
							p.agentSession.profile.Playout.SpeedPercent = 3
						}
					}
					converter := newAgentMediaConverter(p.agentSession, p.media.sampleRate(), false)
					base := time.Unix(100, 0)
					next, sent, maxGap, maxQueue := 0, 0, 0, 0
					gapCount, emptyMS := 0, 0
					first, last := 0, 0
					var want, got []int16
					arrivals := make([]int, 120)
					for i := range arrivals {
						at := i * 100
						switch pattern {
						case "jitter":
							if i%4 == 2 {
								at += 30
							}
						case "burst320":
							if i >= 10 && i <= 13 {
								at = max(at, 1320)
							}
						case "slow102":
							at = i * 102
						case "long_stall":
							if i >= 30 {
								at += 1500
							}
						}
						arrivals[i] = at
					}
					for tick := 0; tick < 16000; tick += 5 {
						for next < len(arrivals) && arrivals[next] <= tick {
							pcm := converter.output(pcm16ToBytes(input[next*2400 : (next+1)*2400]))
							want = append(want, pcm...)
							if _, err := p.enqueueAt([]sipRTPOutboundPacket{{pcm: pcm, itemID: "reply", generation: 1, audioEndMS: (next + 1) * 100}}, base.Add(time.Duration(arrivals[next])*time.Millisecond)); err != nil {
								t.Fatal(err)
							}
							next++
						}
						maxQueue = max(maxQueue, len(p.queue)*20+carrierSamplesToMS(p.packetizer.queuedSamples, p.media.sampleRate()))
						if tick%20 != 0 || !p.prepareAgentTick(base.Add(time.Duration(tick)*time.Millisecond)) {
							continue
						}
						packet, ok := p.dequeueLivePacket(base.Add(time.Duration(tick) * time.Millisecond))
						if !ok {
							continue
						}
						if sent == 0 {
							first = tick
						} else {
							maxGap = max(maxGap, tick-last)
							if tick-last > 20 {
								gapCount++
								emptyMS += tick - last - 20
							}
						}
						last = tick
						sent++
						got = append(got, packet.pcm...)
					}
					if mode != "adaptive_speed" && !slices.Equal(want, got) {
						t.Fatal("sample preservation failed")
					}
					if pattern == "normal" && maxGap > 20 {
						t.Fatal("healthy continuity regressed")
					}
					if maxQueue > 1000 || first > 200 || last > 15000 {
						t.Fatal("latency/queue bound", maxQueue, first, last)
					}
					if math.Abs(float64(len(got)-len(want))) > float64(len(want))*.031 {
						t.Fatal("duration budget")
					}
					if dir := os.Getenv("TELEPHONY_AGENT_REPLAY_OUTPUT"); dir != "" {
						dir = filepath.Join(dir, codec, pattern, mode)
						if err := os.MkdirAll(dir, 0700); err != nil {
							t.Fatal(err)
						}
						writeSpeechAcceptanceWAV(t, filepath.Join(dir, "source.wav"), p.media.sampleRate(), want)
						writeSpeechAcceptanceWAV(t, filepath.Join(dir, "output-samples.wav"), p.media.sampleRate(), got)
						evidence := map[string]any{"codec": codec, "pattern": pattern, "mode": mode, "first_audio_ms": first, "max_packet_gap_ms": maxGap, "empty_intervals": gapCount, "total_empty_ms": emptyMS, "excess_timeline_ms": last - first + 20 - 12000, "max_queue_ms": maxQueue, "source_samples": len(want), "output_samples": len(got), "playout": p.agentPlayout.tuning.stats, "note": "Output samples exclude scheduling gaps; metrics describe packet timeline, not measured speech loss."}
						raw, _ := json.MarshalIndent(evidence, "", "  ")
						if err := os.WriteFile(filepath.Join(dir, "metrics.json"), raw, 0600); err != nil {
							t.Fatal(err)
						}
					}
					t.Logf("%s max_gap_ms=%d first_ms=%d excess_timeline_ms=%d max_queue_ms=%d stats=%+v", fmt.Sprint(mode), maxGap, first, last-first+20-12000, maxQueue, p.agentPlayout.tuning.stats)
				})
			}
		}
	}
}
func TestAgentAudioTraceBoundedAndOptional(t *testing.T) {
	for _, limit := range []int{0, 4} {
		s := &agentAudioSession{profile: agentAudioProfile{Playout: agentPlayoutConfig{TraceFrames: limit}}}
		base := time.Unix(100, 0)
		for i := 0; i < 10; i++ {
			s.acceptAt(realtimeBridgeControl{ItemID: "a", Generation: 1, AudioEndMS: (i + 1) * 20, Timing: &agentCoreAudioTiming{ProviderReceivedAt: base.Add(-time.Hour).Format(time.RFC3339Nano), CoreQueueMS: 3}}, base)
			s.traceSent("a", 1, false, (i+1)*20, 20, "rtp_transmitted", base.Add(60*time.Millisecond), time.Millisecond)
		}
		snap := s.snapshot()
		if len(snap.TimingTrace) != limit {
			t.Fatal(snap)
		}
		if limit > 0 && snap.TraceOmitted != 16 {
			t.Fatal(snap.TraceOmitted)
		}
		if limit == 0 && snap.TraceOmitted != 0 {
			t.Fatal("disabled tracing still recorded")
		}
	}
}

func TestAgentPlayoutProfilePinnedScopeExpiry(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	row := testCall("playout-pinning", "answered")
	row.ProjectID = ctx.CurrentProject()
	row.Direction = "outbound"
	row.AgentID = 7
	row.PeerKind = peerKindRealtime
	if err := a.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"scope": "agent/7", "ttl_seconds": 60, "config": map[string]any{"playout": map[string]any{"mode": "adaptive", "speed_percent": 2, "max_reserve_ms": 180, "trace_frames": 400}}}
	if _, err := a.toolAgentAudioProfile(context.Background(), ctx, args); err != nil {
		t.Fatal(err)
	}
	p, err := a.pinAgentAudioProfile(&row)
	if err != nil || p.Playout.Mode != "adaptive" || p.Playout.SpeedPercent != 2 || p.Playout.TraceFrames != 400 {
		t.Fatal(p, err)
	}
	if _, err = a.db().db.Exec(`UPDATE agent_audio_profiles SET expires_at=?`, time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	pinned, err := a.pinAgentAudioProfile(&row)
	if err != nil || pinned != p {
		t.Fatal("active policy changed", pinned, err)
	}
	next, err := a.resolveAgentAudioForCall(&row)
	if err != nil || next.Playout.Mode != "fixed" || next.Playout.SpeedPercent != 0 {
		t.Fatal("TTL failed", next, err)
	}
	args["config"] = map[string]any{"playout": map[string]any{"unknown": true}}
	if _, err = a.toolAgentAudioProfile(context.Background(), ctx, args); err == nil {
		t.Fatal("unknown nested option accepted")
	}
	row.ProjectID = "isolated"
	next, err = a.resolveAgentAudioForCall(&row)
	if err != nil || next.Playout.Mode != "fixed" {
		t.Fatal("scope leak")
	}
}

func TestAgentPlayoutLongClockDrift(t *testing.T) {
	// Two minutes, a source delivering 2% slower than the phone clock. An ideal
	// periodic signal isolates rate correction; the recorded-speech test separately
	// reports the fraction of real speech that passes the conservative matcher.
	// Mode 0 is uncorrected (drift compensation off, no speed_percent); mode 1
	// is speed_percent alone; mode 2 is the default drift compensation.
	results := make([]int, 3)
	for mode := 0; mode < 3; mode++ {
		p := manualAgentPacer("G722", 60*time.Millisecond)
		p.agentSession.profile.Playout = agentPlayoutConfig{Mode: "adaptive", Drift: "off"}
		if mode == 1 {
			p.agentSession.profile.Playout.SpeedPercent = 3
		}
		if mode == 2 {
			p.agentSession.profile.Playout.Drift = ""
		}
		base := time.Unix(100, 0)
		next, last, empty, first := 0, 0, 0, -1
		for tick := 0; tick < 124000; tick += 2 {
			for next < 6000 && next*204/10 <= tick {
				x := make([]int16, 320)
				for i := range x {
					x[i] = int16(9000 * math.Sin(2*math.Pi*200*float64(i)/16000))
				}
				if _, err := p.enqueueAt([]sipRTPOutboundPacket{{pcm: x, itemID: "long", generation: 1, audioEndMS: (next + 1) * 20}}, base.Add(time.Duration(next)*20400*time.Microsecond)); err != nil {
					t.Fatal(err)
				}
				next++
			}
			if tick%20 != 0 || !p.prepareAgentTick(base.Add(time.Duration(tick)*time.Millisecond)) {
				continue
			}
			packet, ok := p.dequeueLivePacket(base.Add(time.Duration(tick) * time.Millisecond))
			if !ok {
				continue
			}
			if first < 0 {
				first = tick
			} else {
				empty += max(0, tick-last-20)
			}
			last = tick
			if packet.audioEndMS > 120000 {
				t.Fatal("receipt exceeds source")
			}
			if len(p.queue)*20 > 1000 {
				t.Fatal("queue bound")
			}
		}
		results[mode] = empty
		t.Logf("mode=%d total_empty_ms=%d first_ms=%d final_packet_ms=%d stats=%+v", mode, empty, first, last, p.agentPlayout.tuning.stats)
	}
	if results[0] == 0 || results[1] >= results[0]/2 || results[2] >= results[0]/2 {
		t.Fatal("rate correction failed controlled drift", results)
	}
}

func TestAgentPlayoutSilenceDoesNotTrainJitter(t *testing.T) {
	s := continuousAgentFixture()
	s.profile.Playout = agentPlayoutConfig{Mode: "adaptive", SpeedPercent: 3}
	a := newAgentPlayoutTuning(s, 60)
	base := time.Unix(100, 0)
	for i := 0; i < 30; i++ {
		a.process(make([]int16, 320), 16000, 0, realtimeBridgeControl{Silence: true}, base.Add(time.Duration(i)*300*time.Millisecond))
	}
	if a.stats.TargetMS != 60 || a.stats.AdjustedFrames != 0 || !a.lastArrival.IsZero() {
		t.Fatal(a)
	}
}

func BenchmarkAgentPlayoutPCM(b *testing.B) {
	for _, ms := range []int{20, 100} {
		b.Run(fmt.Sprint(ms), func(b *testing.B) {
			c, _ := resolveAgentPlayoutConfig(agentPlayoutConfig{Mode: "adaptive", SpeedPercent: 3}, 60, 1000)
			a := agentPlayoutTuning{cfg: c, stats: agentPlayoutStats{TargetMS: 60}}
			pcm := make([]int16, 16000*ms/1000)
			for i := range pcm {
				pcm[i] = int16(9000 * math.Sin(2*math.Pi*200*float64(i)/16000))
			}
			base := time.Now()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				a.process(pcm, 16000, 0, realtimeBridgeControl{ItemID: "speech", Generation: 1}, base.Add(time.Duration(i*ms)*time.Millisecond))
			}
		})
	}
}
