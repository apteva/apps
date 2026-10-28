package main

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

// Real recorded speech, converter, packetizer and paced dequeue. The baseline
// reproduces 0.12.57's re-prime rule only; audio processing is otherwise identical.
func TestAgentSIPRecoverySpeechTimeline(t *testing.T) {
	for _, codec := range []string{"G722", "PCMA", "PCMU"} {
		for _, pattern := range []string{"regular", "jitter", "burst240", "burst320", "slow110", "repeated"} {
			t.Run(codec+"/"+pattern, func(t *testing.T) {
				var gaps [2]int
				for mode := 0; mode < 2; mode++ {
					input := loadSIPSpeech(t, sipSpeechFixtures[0].id, sipSpeechFixtures[0].hash, 24000)
					// Repeat the real fixture to supply five seconds without synthesized tones.
					for len(input) < 120000 {
						input = append(input, input...)
					}
					input = input[:120000]
					p := manualAgentPacer(codec, 60*time.Millisecond)
					converter := newAgentMediaConverter(p.agentSession, p.media.sampleRate(), false)
					base := time.Unix(100, 0)
					var arrivals []time.Duration
					for i := 0; i < 50; i++ {
						at := time.Duration(i) * 100 * time.Millisecond
						switch pattern {
						case "jitter":
							if i%4 == 2 {
								at += 30 * time.Millisecond
							}
						case "burst240":
							if i >= 10 && i <= 12 {
								at = max(at, 1240*time.Millisecond)
							}
						case "burst320":
							if i >= 10 && i <= 13 {
								at = max(at, 1320*time.Millisecond)
							}
						case "slow110":
							at = time.Duration(i) * 110 * time.Millisecond
						case "repeated":
							if i%10 >= 5 && i%10 <= 8 {
								at = max(at, time.Duration(i/10*1000+820)*time.Millisecond)
							}
						}
						arrivals = append(arrivals, at)
					}
					next, sent, maxGap := 0, 0, 0
					var last, first time.Duration
					var want, got []int16
					for tick := time.Duration(0); tick < 8*time.Second; tick += 20 * time.Millisecond {
						for next < len(arrivals) && arrivals[next] <= tick {
							pcm := converter.output(pcm16ToBytes(input[next*2400 : (next+1)*2400]))
							want = append(want, pcm...)
							if _, err := p.enqueueAt([]sipRTPOutboundPacket{{pcm: pcm, itemID: fmt.Sprint(next / 3), generation: 1}}, base.Add(arrivals[next])); err != nil {
								t.Fatal(err)
							}
							next++
						}
						if !p.prepareAgentTick(base.Add(tick)) {
							continue
						}
						packet, ok := p.dequeueLivePacket(base.Add(tick))
						if !ok {
							if mode == 0 {
								p.agentPlayout.readyAt = time.Time{}
								p.agentPlayout.exhaustedAt = time.Time{}
							}
							continue
						}
						if sent == 0 {
							first = tick
						} else {
							maxGap = max(maxGap, int((tick-last)/time.Millisecond))
						}
						last = tick
						sent++
						got = append(got, packet.pcm...)
						p.playback.adjust(packet, -1)
					}
					if !slices.Equal(want, got) {
						t.Fatal("speech changed, reordered or lost")
					}
					if first != 60*time.Millisecond {
						t.Fatal("startup cushion changed", first)
					}
					if p.playback.hasPendingSpeech() {
						t.Fatal("pending speech leaked")
					}
					gaps[mode] = maxGap
					t.Logf("legacy=%v max_gap_ms=%d excess_timeline_ms=%d immediate_recoveries=%d samples_preserved=%d", mode == 0, maxGap, (last-first+20*time.Millisecond).Milliseconds()-5000, p.agentPlayout.recoveries, len(got))
				}
				// Mid-reply recovery re-primes min(reserve, 60 ms, starvation): a
				// long hold costs at most the 60 ms cushion beyond the hold itself
				// (here the full reserve, so bursts match the baseline), and short
				// starvation re-primes only as long as it starved.
				limits := map[string]int{"regular": 20, "jitter": 20, "burst240": 260, "burst320": 340, "slow110": 60, "repeated": 340}
				if gaps[1] > limits[pattern] || gaps[1] > gaps[0] {
					t.Fatalf("recovery worsened: baseline=%d new=%d limit=%d", gaps[0], gaps[1], limits[pattern])
				}
				if pattern == "slow110" && gaps[0]-gaps[1] != 40 {
					t.Fatal("short starvation re-primed the full reserve", gaps)
				}
			})
		}
	}
}

func TestAgentSIPRecoveryIdleBoundaryAndExplicitClear(t *testing.T) {
	for _, gap := range []time.Duration{20 * time.Millisecond, 999 * time.Millisecond, time.Second, 2 * time.Second} {
		t.Run(gap.String(), func(t *testing.T) {
			p := manualAgentPacer("G722", 60*time.Millisecond)
			base := time.Unix(100, 0)
			put := func(at time.Time) {
				_, err := p.enqueueAt([]sipRTPOutboundPacket{{pcm: make([]int16, 320), itemID: "reply", generation: 1}}, at)
				if err != nil {
					t.Fatal(err)
				}
			}
			put(base)
			p.prepareAgentTick(base.Add(60 * time.Millisecond))
			f, _ := p.dequeueLivePacket(base.Add(60 * time.Millisecond))
			p.playback.adjust(f, -1)
			empty := base.Add(80 * time.Millisecond)
			p.prepareAgentTick(empty)
			p.prepareAgentTick(empty.Add(time.Millisecond))
			if p.agentPlayout.exhaustions != 1 {
				t.Fatal("counted idle ticks as gaps")
			}
			at := empty.Add(gap)
			put(at)
			// Recovery re-primes min(reserve, 60 ms, starvation); a genuinely
			// idle stream restarts with its full reserve as a new start.
			wait := max(sipRTPPacketTime, min(60*time.Millisecond, gap))
			if p.prepareAgentTick(at.Add(wait-time.Nanosecond)) || !p.prepareAgentTick(at.Add(wait)) {
				t.Fatal("recovery cushion not applied", wait)
			}
			if (p.agentPlayout.starts == 2) != (gap >= sipAgentRecoveryIdle) {
				t.Fatal("wrong idle boundary")
			}
			p.drain()
			put(at.Add(time.Second))
			if p.prepareAgentTick(at.Add(time.Second)) {
				t.Fatal("explicit clear bypassed startup")
			}
		})
	}
}

// Ten minutes of stream time with a 320ms catch-up burst every ten seconds.
// Output wall time must settle after each burst rather than accumulating an
// additional startup reserve at every recovery. No wall-clock sleeps required.
func TestAgentSIPRecoveryLongRepeatedBursts(t *testing.T) {
	p := manualAgentPacer("G722", 60*time.Millisecond)
	base := time.Unix(100, 0)
	const chunks = 6000
	next, samples, maxQueued := 0, 0, 0
	var last time.Duration
	for tick := time.Duration(0); tick < 602*time.Second; tick += 20 * time.Millisecond {
		for next < chunks {
			at := time.Duration(next) * 100 * time.Millisecond
			if next%100 >= 50 && next%100 <= 53 {
				at = max(at, time.Duration(next/100*10000+5320)*time.Millisecond)
			}
			if at > tick {
				break
			}
			pcm := make([]int16, 1600)
			for i := range pcm {
				pcm[i] = int16((next*1600 + i) % 30000)
			}
			if _, err := p.enqueueAt([]sipRTPOutboundPacket{{pcm: pcm, itemID: "long", generation: 1}}, base.Add(at)); err != nil {
				t.Fatal(err)
			}
			maxQueued = max(maxQueued, len(p.queue)*20)
			next++
		}
		if !p.prepareAgentTick(base.Add(tick)) {
			continue
		}
		packet, ok := p.dequeueLivePacket(base.Add(tick))
		if !ok {
			continue
		}
		for _, v := range packet.pcm {
			if v != int16(samples%30000) {
				t.Fatal("sample lost or reordered")
			}
			samples++
		}
		last = tick
		p.playback.adjust(packet, -1)
	}
	if samples != chunks*1600 || last > 601*time.Second || maxQueued > 500 || p.agentPlayout.starts != 1 {
		t.Fatalf("samples=%d last=%s queue=%d starts=%d", samples, last, maxQueued, p.agentPlayout.starts)
	}
	t.Logf("duration=600s final_packet_ms=%d max_queue_ms=%d recoveries=%d", last.Milliseconds(), maxQueued, p.agentPlayout.recoveries)
}
