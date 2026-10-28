package main

import (
	"fmt"
	"math/rand"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/pion/rtp"
)

// Frozen tick-selection block from telephony/v0.12.60 sendRTCAudio.
func releasedRTCTailTick(pending *rtcPCMFramer, frames chan rtcPCMFrame, lastChunkAt, now time.Time, stats *rtcMediaStats, done <-chan struct{}) (rtcPCMFrame, bool) {
	direction := "carrier_to_operator"
	frame, open := nextRTCFrame(frames, now, stats)
	if !open {
		return rtcPCMFrame{}, false
	}
	if !lastChunkAt.IsZero() && now.Sub(lastChunkAt) >= 20*time.Millisecond {
		if dropped := pending.discardExpired(now); dropped > 0 {
			stats.drop("webrtc_playback_partial_age", direction, 0, dropped*1000/24000)
		}
		if tail, padding := pending.flushPartial(now); tail.pcm != nil {
			stats.mu.Lock()
			stats.value.PartialFlushedFrames++
			stats.value.PartialPaddedMS += float64(padding) / 24
			stats.mu.Unlock()
			if frame.pcm == nil {
				frame = tail
			} else {
				queueRTCFrame(frames, tail, done, stats)
			}
		}
	}
	return frame, true
}

type tailEvent struct {
	at   time.Duration
	pcm  []int16
	tick bool
}
type tailReplayResult struct {
	drops                  int64
	padding                float64
	flushes                uint64
	maxQueue               int
	input, output, pending int
	orderPreserved         bool
}

func replayTailJitter(t *testing.T, guard bool, seed int64) tailReplayResult {
	return replayTailSpeech(t, guard, seed, nil, nil)
}
func replayTailSpeech(t *testing.T, guard bool, seed int64, source []int16, render func(rtcPCMFrame, time.Time)) tailReplayResult {
	t.Helper()
	start := time.Unix(1700000000, 0)
	rng := rand.New(rand.NewSource(seed))
	var events []tailEvent
	var sourceOffset int
	for pair := 0; pair < 200; pair++ {
		delta := rng.Intn(3) - 1
		for k, n := range []int{480 + delta, 480 - delta} {
			pcm := make([]int16, n)
			for i := range pcm {
				if len(source) > 0 {
					pcm[i] = source[sourceOffset%len(source)]
					sourceOffset++
				} else {
					pcm[i] = int16((pair*960+k*480+i)%30000 + 1)
				}
			}
			events = append(events, tailEvent{at: time.Duration((pair*2+k)*20+rng.Intn(3)-1) * time.Millisecond, pcm: pcm})
		}
	}
	for n := 1; n <= 410; n++ {
		events = append(events, tailEvent{at: time.Duration(n) * 20 * time.Millisecond, tick: true})
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].at == events[j].at {
			return !events[i].tick && events[j].tick
		}
		return events[i].at < events[j].at
	})
	frames := make(chan rtcPCMFrame, 6)
	stats := &rtcMediaStats{}
	done := make(chan struct{})
	var pending rtcPCMFramer
	var last time.Time
	r := tailReplayResult{}
	var inputPCM, realOutput []int16
	known := map[*int16]bool{}
	for _, e := range events {
		now := start.Add(e.at)
		if !e.tick {
			pending.discardExpired(now)
			last = now
			r.input += len(e.pcm)
			inputPCM = append(inputPCM, e.pcm...)
			pending.push(pcm16ToBytes(e.pcm), now.Add(250*time.Millisecond), func(f rtcPCMFrame) bool { known[&f.pcm[0]] = true; return queueRTCFrame(frames, f, done, stats) })
			continue
		}
		freshPartial := 0
		for _, s := range pending.segments {
			if s.expires.After(now) {
				freshPartial += s.samples
			}
		}
		var frame rtcPCMFrame
		if guard {
			frame, _ = nextRTCPlaybackFrame(frames, &pending, last, now, stats)
		} else {
			frame, _ = releasedRTCTailTick(&pending, frames, last, now, stats, done)
		}
		r.output += len(frame.pcm)
		if guard && len(frame.pcm) > 0 {
			n := len(frame.pcm)
			if !known[&frame.pcm[0]] {
				n = freshPartial
			}
			realOutput = append(realOutput, frame.pcm[:n]...)
		}
		if render != nil {
			render(frame, now)
		}
	}
	s := stats.snapshot()
	r.drops = s.OutboundDroppedMS
	r.padding = s.PartialPaddedMS
	r.flushes = s.PartialFlushedFrames
	r.maxQueue = s.MaxQueueMS
	r.pending = len(pending.pending)
	r.orderPreserved = slices.Equal(inputPCM, realOutput)
	return r
}
func TestRTCOutgoingTailDoesNotManufactureOverflow(t *testing.T) {
	for seed := int64(1); seed <= 5; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			old := replayTailJitter(t, false, seed)
			fixed := replayTailJitter(t, true, seed)
			t.Logf("released=%+v guarded=%+v", old, fixed)
			if old.drops == 0 || fixed.drops != 0 {
				t.Fatal("did not reproduce or guard failed")
			}
			if !fixed.orderPreserved {
				t.Fatal("received PCM lost, repeated or reordered")
			}
			if fixed.output < fixed.input || fixed.output > fixed.input+480 || fixed.maxQueue > 60 || fixed.pending != 0 {
				t.Fatal("excess padding or missing samples", fixed)
			}
		})
	}
}

func TestRTCPlaybackTickKeepsFullFrameAndUnpaddedRemainder(t *testing.T) {
	now := time.Unix(1700000000, 0)
	var f rtcPCMFramer
	stats := &rtcMediaStats{}
	frames := make(chan rtcPCMFrame, 6)
	done := make(chan struct{})
	pcm := make([]int16, 960)
	for i := range pcm {
		pcm[i] = int16(i + 1)
	}
	emit := func(v rtcPCMFrame) bool { return queueRTCFrame(frames, v, done, stats) }
	f.push(pcm16ToBytes(pcm[:481]), now.Add(250*time.Millisecond), emit)
	first, open := nextRTCPlaybackFrame(frames, &f, now, now.Add(20*time.Millisecond), stats)
	if !open || !slices.Equal(first.pcm, pcm[:480]) || len(f.pending) != 1 || f.pending[0] != 481 || len(frames) != 0 {
		t.Fatal("full tick manufactured or changed audio")
	}
	if stats.snapshot().PartialFlushedFrames != 0 {
		t.Fatal("padded alongside complete frame")
	}
	f.push(pcm16ToBytes(pcm[481:]), now.Add(270*time.Millisecond), emit)
	second, _ := nextRTCPlaybackFrame(frames, &f, now.Add(20*time.Millisecond), now.Add(40*time.Millisecond), stats)
	if !slices.Equal(second.pcm, pcm[480:]) || len(f.pending) != 0 || len(frames) != 0 {
		t.Fatal("remainder did not join next chunk")
	}
}
func TestRTCPlaybackTickExpiresPartialWhileFullFrameExists(t *testing.T) {
	now := time.Unix(1700000000, 0)
	var f rtcPCMFramer
	stats := &rtcMediaStats{direction: "carrier_to_listener"}
	frames := make(chan rtcPCMFrame, 6)
	f.push(pcm16ToBytes(make([]int16, 240)), now.Add(10*time.Millisecond), func(rtcPCMFrame) bool { t.Fatal("partial emitted"); return false })
	frames <- rtcPCMFrame{pcm: []int16{123}, expires: now.Add(time.Second)}
	got, open := nextRTCPlaybackFrame(frames, &f, now, now.Add(20*time.Millisecond), stats)
	if !open || got.pcm[0] != 123 || len(f.pending) != 0 || stats.snapshot().PartialFlushedFrames != 0 || stats.snapshot().OutboundDroppedMS != 10 {
		t.Fatal("expiry bypassed on complete-frame tick", stats.snapshot())
	}
	if events := stats.snapshot().Events; len(events) != 1 || events[0].Direction != "carrier_to_listener" {
		t.Fatal("partial expiry changed media direction", events)
	}
}
func TestRTCPlaybackTickTailAndPauseAgeProtection(t *testing.T) {
	now := time.Unix(1700000000, 0)
	var f rtcPCMFramer
	stats := &rtcMediaStats{}
	frames := make(chan rtcPCMFrame, 6)
	pcm := make([]int16, 432)
	for i := range pcm {
		pcm[i] = int16(i + 100)
	}
	f.push(pcm16ToBytes(pcm), now.Add(250*time.Millisecond), func(rtcPCMFrame) bool { t.Fatal("partial emitted"); return false })
	// The continuation is due at 20 ms; the partial is a tail one packet later.
	if early, _ := nextRTCPlaybackFrame(frames, &f, now, now.Add(20*time.Millisecond), stats); early.pcm != nil || len(f.pending) != 432 {
		t.Fatal("partial padded before its continuation was late")
	}
	tail, _ := nextRTCPlaybackFrame(frames, &f, now, now.Add(40*time.Millisecond), stats)
	if !slices.Equal(tail.pcm[:432], pcm) || tail.pcm[479] != 0 || !tail.expires.Equal(now.Add(250*time.Millisecond)) || stats.snapshot().PartialPaddedMS != 2 {
		t.Fatal("final-word tail or lifetime changed")
	}
	if repeat, _ := nextRTCPlaybackFrame(frames, &f, now, now.Add(60*time.Millisecond), stats); repeat.pcm != nil {
		t.Fatal("tail repeated")
	}
	frames <- rtcPCMFrame{pcm: make([]int16, 480), expires: now.Add(250 * time.Millisecond)}
	f.push(pcm16ToBytes(pcm), now.Add(250*time.Millisecond), func(rtcPCMFrame) bool { return true })
	if stale, _ := nextRTCPlaybackFrame(frames, &f, now, now.Add(400*time.Millisecond), stats); stale.pcm != nil || len(f.pending) != 0 {
		t.Fatal("genuine pause replayed expired samples")
	}
	close(frames)
	if _, open := nextRTCPlaybackFrame(frames, &f, now, now.Add(420*time.Millisecond), stats); open {
		t.Fatal("closed session reopened")
	}
}
func TestRTCOutgoingTailSpeechOpusRTP(t *testing.T) {
	source := loadSIPSpeech(t, sipSpeechFixtures[0].id, sipSpeechFixtures[0].hash, 24000)
	for _, rate := range []int{24000, 48000} {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			encoder, err := newRTCOpusEncoderAtRate(24000, true, rate)
			if err != nil {
				t.Fatal(err)
			}
			defer encoder.Close()
			actualRate := rtcEncoderInputRate(encoder)
			if actualRate != rate {
				t.Skip("native input rate unavailable")
			}
			decoder, err := newRTCOpusDecoderWithFEC(true)
			if err != nil {
				t.Fatal(err)
			}
			defer decoder.Close()
			resampler := newPCMResampler(24000, actualRate)
			var converted []int16
			var samples []float32
			out := make([]byte, 1275)
			decoded := make([]int16, 2880)
			clock := rtcRTPClock{base: time.Unix(1700000000, 0), baseTS: ^uint32(0) - 960}
			var seq uint16 = 65530
			packets, skipped := 0, 0
			voiced := false
			r := replayTailSpeech(t, true, 1, source, func(frame rtcPCMFrame, now time.Time) {
				pcm := frame.pcm
				if pcm == nil {
					pcm = make([]int16, 480)
				}
				if actualRate == 24000 {
					converted = append(converted[:0], pcm...)
				} else {
					converted = resampler.ProcessInto(pcm, converted)
				}
				for _, v := range converted {
					samples = append(samples, float32(v)/32768)
				}
				size := actualRate / 50
				if len(samples) < size {
					return
				}
				n, e := encoder.EncodeFloat32(samples[:size], out)
				if e != nil {
					t.Fatal(e)
				}
				samples = samples[:copy(samples, samples[size:])]
				ts, gaps := clock.next(now)
				skipped += gaps
				packet := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: seq, Timestamp: ts, SSRC: 42}, Payload: append([]byte(nil), out[:n]...)}
				wire, e := packet.Marshal()
				if e != nil {
					t.Fatal(e)
				}
				var got rtp.Packet
				if e = got.Unmarshal(wire); e != nil || got.SequenceNumber != seq || got.Timestamp != ts {
					t.Fatal("invalid RTP", e)
				}
				n, e = decoder.decode(got.Payload, decoded)
				if e != nil || n != 480 {
					t.Fatal("invalid 20ms Opus output", n, e)
				}
				voiced = voiced || speechRMS(decoded[:n]) > 100
				packets++
				seq++
			})
			want := 410
			if actualRate == 48000 {
				want--
			}
			if r.drops != 0 || !r.orderPreserved || !voiced || packets != want || skipped != 0 {
				t.Fatal("speech/resampling/RTP regression", r, packets, want, skipped, voiced)
			}
		})
	}
}
