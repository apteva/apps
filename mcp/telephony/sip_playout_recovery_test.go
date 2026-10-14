package main

import (
	"math"
	"sort"
	"testing"
	"time"
)

func TestSIPControlledPlayoutRecovery(t *testing.T) {
	for _, codec := range []string{"G722", "PCMA", "PCMU"} {
		for _, enabled := range []bool{false, true} {
			for _, mode := range []string{"stable_shift", "bounded_jitter", "bursts", "local_stall"} {
				t.Run(codec+"/"+mode+"/"+map[bool]string{false: "default", true: "recovery"}[enabled], func(t *testing.T) {
					b := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{Resynchronize: &enabled})
					m := &sipRTPMedia{offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
					enc := &sipRTPMedia{offer: m.offer}
					pcm := make([]int16, m.sampleRate()/50)
					for i := range pcm {
						pcm[i] = int16(2000 * math.Sin(float64(i)*.1))
					}
					payload := enc.encodeAudio(pcm)
					f := newRTPAudioFramer(b, m.offer)
					base := time.Unix(100, 0)
					packets := make([]sipReplayPacket, 3000)
					for i := range packets {
						delay := 0.0
						switch mode {
						case "stable_shift":
							if i >= 500 {
								delay = 240
							}
						case "bounded_jitter":
							delay = 60 + 60*math.Sin(2*math.Pi*float64(i)/50)
						case "bursts":
							delay = float64(i%50) * 360 / 49
						}
						packets[i] = sipReplayPacket{at: time.Duration(i)*20*time.Millisecond + time.Duration(delay*float64(time.Millisecond)), seq: uint16(i), ts: uint32(i * 160), payload: payload}
					}
					sort.SliceStable(packets, func(i, j int) bool { return packets[i].at < packets[j].at })
					n := 0
					tailDrops := 0
					maxQueue := 0
					for tick := time.Duration(0); tick < 62*time.Second; tick += 20 * time.Millisecond {
						before := b.lateDroppedSamples + b.droppedSamples
						for n < len(packets) && packets[n].at <= tick {
							q := packets[n]
							at := base.Add(q.at)
							processed := at
							if mode == "local_stall" && n >= 500 {
								processed = at.Add(250 * time.Millisecond)
							}
							b.pushRTPAt(q.seq, q.payload, q.ts, at, processed)
							n++
						}
						f.popPCMAt(m, base.Add(tick))
						maxQueue = max(maxQueue, b.buffered+int(b.pendingPCMSamples.Load()))
						// Assess live speech before the finite replay stops producing samples.
						if tick > 15*time.Second && tick < 59*time.Second {
							tailDrops += b.lateDroppedSamples + b.droppedSamples - before
						}
					}
					t.Logf("late_ms=%.0f tail_discard_ms=%.0f recoveries=%d max_queue_ms=%.1f", float64(b.lateDroppedSamples)/8, float64(tailDrops)/8, b.recovery.recoveries, float64(maxQueue)/8)
					if maxQueue > 160*8 {
						t.Fatal("queue bound exceeded")
					}
					if mode == "stable_shift" && enabled {
						if b.recovery.recoveries != 1 || tailDrops > 0 {
							t.Fatal("stable live phase did not recover", b.recovery.recoveries, tailDrops)
						}
					} else if b.recovery.recoveries != 0 {
						t.Fatal("burst/jitter/local stall rebased")
					}
					if (mode == "bursts" || mode == "local_stall" || mode == "stable_shift" && !enabled) && b.lateDroppedSamples == 0 {
						t.Fatal("stale audio accepted")
					}
				})
			}
		}
	}
}
func TestSIPRecoveryHoldAndSourceResetClearCandidate(t *testing.T) {
	yes := true
	b := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{Resynchronize: &yes})
	b.recovery.since = time.Unix(1, 0)
	b.recovery.count = 48
	b.setPaused(true, time.Unix(2, 0))
	if !b.recovery.since.IsZero() {
		t.Fatal("hold retained candidate")
	}
	b.setPaused(false, time.Unix(3, 0))
	b.pushSourceRTP(1, 1, make([]byte, 160), 160, time.Unix(3, 0))
	b.recovery.since = time.Unix(3, 0)
	b.pushSourceRTP(2, 2, make([]byte, 160), 320, time.Unix(4, 0))
	if !b.recovery.since.IsZero() {
		t.Fatal("SSRC retained candidate")
	}
}
func TestSIPLiveAdmissionRechecksProcessingAfterLock(t *testing.T) {
	b := newRTPJitterBuffer()
	at := time.Unix(100, 0)
	b.pushRTPAtClock(1, make([]byte, 160), 160, at, at, func() time.Time { return at.Add(300 * time.Millisecond) })
	if b.lateDroppedSamples != 160 || b.maxIngressLockMS < 300 {
		t.Fatal("expired while waiting for lock was admitted")
	}
}
func TestSIPRecoverySettingsAreScopedAndCopied(t *testing.T) {
	a, ctx, _ := localSpeechFixture(t)
	yes := true
	p := sipPlaybackPolicy{Resynchronize: &yes}
	if err := a.saveLocalMediaSettings(ctx, "", localMediaSettings{RTPPlayback: &p}); err != nil {
		t.Fatal(err)
	}
	cfg, err := a.localMediaSettings(ctx.CurrentProject(), "")
	if err != nil {
		t.Fatal(err)
	}
	active := newRTPJitterBufferWithPolicy(*cfg.RTPPlayback)
	no := false
	p.Resynchronize = &no
	if err := a.saveLocalMediaSettings(ctx, "", localMediaSettings{RTPPlayback: &p}); err != nil {
		t.Fatal(err)
	}
	next, _ := a.localMediaSettings(ctx.CurrentProject(), "")
	foreign, _ := a.localMediaSettings("project-b", "")
	if !*active.policy.Resynchronize || *next.RTPPlayback.Resynchronize || foreign.RTPPlayback != nil {
		t.Fatal("active or foreign policy changed")
	}
}
