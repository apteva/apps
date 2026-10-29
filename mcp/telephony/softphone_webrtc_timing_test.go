package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func rtcRecoverySpeech(t *testing.T) [][]byte {
	t.Helper()
	source := loadSIPSpeech(t, sipSpeechFixtures[0].id, sipSpeechFixtures[0].hash, 48000)
	enc, err := newRTCOpusEncoder(32000)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	packets := make([][]byte, 250)
	frame := make([]float32, 960)
	buf := make([]byte, 1275)
	for i := range packets {
		for k := range frame {
			frame[k] = float32(source[(i*960+k)%len(source)]) / 32768
		}
		n, e := enc.EncodeFloat32(frame, buf)
		if e != nil {
			t.Fatal(e)
		}
		packets[i] = append([]byte(nil), buf[:n]...)
	}
	return packets
}

type rtcRecoveryEvidence struct {
	Scenario                               string `json:"scenario"`
	Legacy                                 bool   `json:"legacy"`
	Accepted, Rejected, Decoded, Forwarded int
	Recoveries                             uint64
	FirstForwardMS                         float64
	DecodeErrors                           int64
	MaxQueue                               int
}

// Virtual five-minute warmup and thirty seconds after the fault. These are
// controlled hypotheses, not a replay of Emilie's unrecorded RTP timeline.
func replayRTCRecovery(t *testing.T, packets [][]byte, scenario string, legacy bool) (rtcRecoveryEvidence, [][]int16) {
	t.Helper()
	at := time.Unix(1000, 0)
	stats := &rtcMediaStats{}
	j := rtcJitter{onTiming: stats.timing}
	old := v59RTCJitter{}
	dec, err := newRTCOpusDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	validator, err := newRTCOpusDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer validator.Close()
	scratch := make([]int16, 2880)
	pcm := make([]int16, 2880)
	j.validate = func(p []byte) bool { n, e := validator.decode(p, scratch); return e == nil && n > 0 }
	var timeline rtcDecodeTimeline
	var release rtcAudioRelease
	result := rtcRecoveryEvidence{Scenario: scenario, Legacy: legacy, FirstForwardMS: -1}
	var recovered [][]int16
	firstFault := at.Add(300 * time.Second)
	for i := 0; i < 16500; i++ {
		now := at.Add(time.Duration(i) * 20 * time.Millisecond)
		ts := uint32(0xfffff000) + uint32(i*960)
		if i >= 15000 {
			switch scenario {
			case "backward500":
				ts -= 24000
			case "reset":
				ts = uint32((i - 15000) * 960)
			case "delivery500":
				now = now.Add(500 * time.Millisecond)
			case "dtx":
				now = now.Add(time.Second)
				ts += 48000
			case "duplicate":
				ts = uint32((uint64(0xfffff000) + 14999*960) & 0xffffffff)
			case "batch":
				now = at.Add(330 * time.Second)
			}
		}
		if i == 15000 {
			firstFault = now
		}
		seq := uint16(60000 + i)
		if scenario == "duplicate" && i >= 15000 {
			seq = uint16((60000 + 14999) % 65536)
		}
		payload := packets[i%len(packets)]
		if scenario == "malformed" && i >= 15000 {
			payload = []byte{3, 255}
		}
		p := &rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: seq, Timestamp: ts}, Payload: payload}
		if scenario == "malformed" && i >= 15000 {
			p.Timestamp -= 24000
		}
		accepted := false
		if legacy {
			accepted = old.push(p, now)
			j.base = old.base
			j.baseTS = old.baseTS
		} else {
			accepted = j.push(p, now)
		}
		if i >= 15000 {
			if accepted {
				result.Accepted++
			} else {
				result.Rejected++
			}
		}
		result.MaxQueue = max(result.MaxQueue, len(j.packets), len(old.packets), release.count)
		pop := j.pop
		if legacy {
			pop = old.pop
		}
		for got := pop(now); got != nil; got = pop(now) {
			decodeRTCPacket(got, &j, &timeline, &release, dec, pcm, stats, "operator_to_carrier", true, now, 0)
			if i >= 15000 && accepted {
				result.Decoded++
			}
		}
		if f, ok := release.pop(now, stats, "operator_to_carrier"); ok && i >= 15000 && accepted {
			result.Forwarded++
			if result.FirstForwardMS < 0 {
				result.FirstForwardMS = float64(now.Sub(firstFault)) / float64(time.Millisecond)
			}
			if j.epoch > 0 && len(recovered) < 10 {
				recovered = append(recovered, append([]int16(nil), f.pcm...))
			}
		}
	}
	result.Recoveries = j.epoch
	result.DecodeErrors = stats.snapshot().DecodeErrors
	return result, recovered
}
func TestRTCTimelineRecoveryLongSpeech(t *testing.T) {
	packets := rtcRecoverySpeech(t)
	var results []rtcRecoveryEvidence
	for _, scenario := range []string{"normal", "backward500", "reset", "delivery500", "dtx", "duplicate", "batch", "malformed"} {
		for _, legacy := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/legacy=%v", scenario, legacy), func(t *testing.T) {
				r, _ := replayRTCRecovery(t, packets, scenario, legacy)
				results = append(results, r)
				raw, _ := json.Marshal(r)
				t.Log(string(raw))
				if r.MaxQueue > 16 {
					t.Fatal("unbounded receive queue")
				}
				switch scenario {
				case "backward500", "reset", "delivery500":
					if legacy {
						if r.Accepted != 0 {
							t.Fatal("baseline no longer reproduces")
						}
					} else if r.Rejected > 6 || r.Forwarded < 1480 || r.Recoveries != 1 || r.FirstForwardMS > 180 || r.DecodeErrors != 0 {
						t.Fatal("timeline remained stuck or thrashed", r)
					}
				case "normal", "dtx":
					if r.Accepted != 1500 || r.Recoveries != 0 || r.DecodeErrors != 0 {
						t.Fatal("healthy/DTX timeline changed", r)
					}
				default:
					if r.Recoveries != 0 {
						t.Fatal("untrusted input changed epoch", r)
					}
				}
			})
		}
	}
	if path := os.Getenv("TELEPHONY_RTC_RECOVERY_RESULTS"); path != "" {
		raw, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func TestRTCTimelineRecoveryReachesSIPWire(t *testing.T) {
	packets := rtcRecoverySpeech(t)
	for _, scenario := range []string{"backward500", "delivery500"} {
		t.Run(scenario, func(t *testing.T) {
			_, frames := replayRTCRecovery(t, packets, scenario, false)
			if len(frames) != 10 {
				t.Fatal("missing decoded recovery frames")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sender, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if e != nil {
				t.Fatal(e)
			}
			defer sender.Close()
			receiver, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if e != nil {
				t.Fatal(e)
			}
			defer receiver.Close()
			media := &sipRTPMedia{conn: sender, remote: receiver.LocalAddr().(*net.UDPAddr), offer: sipMediaOffer{Codec: "PCMA", PayloadType: 8, PacketSamples: 160}}
			pacer := newSIPRTPPacerWithPolicy(ctx, media, &sipPlaybackState{}, liveHumanCarrierPacerPolicy(), nil)
			defer func() { cancel(); <-pacer.done }()
			// A scheduler stall of 200 ms or more may legitimately send keepalive
			// silence between frames; it is not part of the recovered speech.
			var keepaliveMu sync.Mutex
			keepalive := map[uint16]bool{}
			observe := func(seq uint16) { keepaliveMu.Lock(); keepalive[seq] = true; keepaliveMu.Unlock() }
			pacer.keepaliveObserver.Store(&observe)
			resampler := newPCMResampler(24000, 8000)
			var sequence uint16
			var timestamp uint32
			voiced := 0
			for i, frame := range frames {
				pcm := resampler.Process(frame)
				if _, e = pacer.enqueue([]sipRTPOutboundPacket{{pcm: pcm, enqueuedAt: time.Now()}}); e != nil {
					t.Fatal(e)
				}
				var packet rtp.Packet
				skipped := false
				for {
					receiver.SetReadDeadline(time.Now().Add(time.Second))
					buf := make([]byte, 2048)
					n, _, e := receiver.ReadFromUDP(buf)
					if e != nil {
						t.Fatal(e)
					}
					if e = packet.Unmarshal(buf[:n]); e != nil {
						t.Fatal(e)
					}
					keepaliveMu.Lock()
					isKeepalive := keepalive[packet.SequenceNumber]
					keepaliveMu.Unlock()
					if !isKeepalive {
						break
					}
					sequence, timestamp, skipped = packet.SequenceNumber, packet.Timestamp, true
				}
				// After keepalive the timeline resumes on the wall clock, still in
				// whole 20 ms frames.
				if step := packet.Timestamp - timestamp; i > 0 && (uint16(packet.SequenceNumber-sequence) != 1 || step != 160 && !(skipped && step%160 == 0)) {
					t.Fatal("SIP continuity failed")
				}
				if len(packet.Payload) != 160 {
					t.Fatal("wrong SIP frame")
				}
				if speechRMS(decodeSIPG711(packet.Payload, "PCMA")) > 100 {
					voiced++
				}
				sequence, timestamp = packet.SequenceNumber, packet.Timestamp
			}
			if voiced < 3 || media.observations.sent.Load() != 10 || pacer.droppedMS() != 0 {
				t.Fatalf("not sustained real speech: voiced=%d sent=%d drops=%d", voiced, media.observations.sent.Load(), pacer.droppedMS())
			}
		})
	}
}
func TestRTCReceiveEstimatorComparison(t *testing.T) {
	packets := rtcRecoverySpeech(t)
	clean, err := newRTCOpusDecoder()
	if err != nil {
		t.Fatal(err)
	}
	var reference []int16
	cleanPCM := make([]int16, 2880)
	for i := 0; i < 2500; i++ {
		n, e := clean.decode(packets[i%250], cleanPCM)
		if e != nil {
			t.Fatal(e)
		}
		reference = append(reference, cleanPCM[:n]...)
	}
	clean.Close()
	var speechResults [2]sipSpeechEvidence
	type incoming struct{ index, ms int }
	var input []incoming
	for i := 0; i < 2500; i++ {
		ms := i * 20
		if i%10 == 5 {
			ms += 95
		}
		input = append(input, incoming{i, ms})
	}
	sort.SliceStable(input, func(i, j int) bool { return input[i].ms < input[j].ms })
	rejected := [2]int{}
	totalRejected := [2]int{}
	outputs := [2]int{}
	var maxAge [2]time.Duration
	for mode := 0; mode < 2; mode++ {
		j := rtcJitter{adaptive: mode == 1}
		dec, e := newRTCOpusDecoder()
		if e != nil {
			t.Fatal(e)
		}
		stats := &rtcMediaStats{}
		var timeline rtcDecodeTimeline
		var release rtcAudioRelease
		pcm := make([]int16, 2880)
		base := time.Unix(100, 0)
		next := 0
		var frames []sipSpeechFrame
		var positions []int
		for ms := 0; ms < 50200; ms++ {
			now := base.Add(time.Duration(ms) * time.Millisecond)
			for next < len(input) && input[next].ms <= ms {
				v := input[next]
				next++
				p := &rtp.Packet{Header: rtp.Header{SequenceNumber: uint16(v.index), Timestamp: uint32(v.index * 960), SSRC: 42}, Payload: packets[v.index%250]}
				if !j.push(p, now) {
					totalRejected[mode]++
					if ms >= 10000 {
						rejected[mode]++
					}
				}
			}
			for p := j.pop(now); p != nil; p = j.pop(now) {
				decodeRTCPacket(p, &j, &timeline, &release, dec, pcm, stats, "operator_to_carrier", true, now, 0)
			}
			if f, ok := release.pop(now, stats, "operator_to_carrier"); ok {
				frames = append(frames, sipSpeechFrame{at: now.Sub(base), pcm: append([]int16(nil), f.pcm...), sourceSamples: len(f.pcm)})
				positions = append(positions, int((f.source.Sub(base)+10*time.Millisecond)/(20*time.Millisecond))*480)
				if ms >= 10000 {
					outputs[mode]++
					maxAge[mode] = max(maxAge[mode], now.Sub(f.source))
				}
			}
		}
		dec.Close()
		speechResults[mode] = analyzeSIPSpeech(reference, 24000, frames, positions, 60)
		t.Logf("speech adaptive=%v coverage=%.4f damaged=%dms interruption=%dms", mode == 1, speechResults[mode].ActiveSpeechCoverage, speechResults[mode].LongestDamagedSpeechMS, speechResults[mode].LongestPlaybackInterruptionMS)
		t.Logf("adaptive=%v rejected=%d total_rejected=%d output=%d residence=%v reserve=%v target=%d", mode == 1, rejected[mode], totalRejected[mode], outputs[mode], maxAge[mode], j.reserve, j.distribution.target)
		if j.epoch != 0 || maxAge[mode] > 200*time.Millisecond || stats.snapshot().DecodeErrors != 0 {
			t.Fatal("unsafe adaptation")
		}
	}
	if speechResults[1].ActiveSpeechCoverage < speechResults[0].ActiveSpeechCoverage || speechResults[1].LongestDamagedSpeechMS > speechResults[0].LongestDamagedSpeechMS {
		t.Fatal("audio quality regressed", speechResults)
	}
	if rejected[1] >= rejected[0]/2 || outputs[1] < outputs[0] {
		t.Fatal("adaptation did not improve late speech delivery", rejected, outputs)
	}
}

func TestRTCTimingReasonsAndBoundedSnapshots(t *testing.T) {
	base := time.Unix(100, 0)
	stats := &rtcMediaStats{}
	packet := func(seq uint16, ts uint32) *rtp.Packet {
		return &rtp.Packet{Header: rtp.Header{SSRC: 42, SequenceNumber: seq, Timestamp: ts}, Payload: []byte{1}}
	}
	check := func(want string, j *rtcJitter, p *rtp.Packet, at time.Time) {
		t.Helper()
		j.onTiming = stats.timing
		if j.push(p, at) || j.lastReject != want {
			t.Fatalf("want %s got %s", want, j.lastReject)
		}
	}
	j := rtcJitter{}
	p := packet(1, 0)
	p.Payload = nil
	check("invalid_payload_size", &j, p, base)
	j.push(packet(1, 0), base)
	check("duplicate_timestamp", &j, packet(2, 0), base)
	j.pop(base.Add(60 * time.Millisecond))
	check("already_played", &j, packet(1, 0), base.Add(80*time.Millisecond))
	check("too_late", &j, packet(3, 960), base.Add(time.Second))
	check("too_early", &j, packet(4, 48000), base.Add(80*time.Millisecond))
	p = packet(5, 1920)
	p.SSRC = 123
	check("unexpected_ssrc", &j, p, base.Add(80*time.Millisecond))
	full := rtcJitter{}
	for i := 0; i < 16; i++ {
		full.push(packet(uint16(i), uint32(i*480)), base)
	}
	check("queue_full", &full, packet(16, 16*480), base)
	late := rtcJitter{onTiming: stats.timing}
	late.push(packet(1, 0), base)
	late.pop(base.Add(time.Second))
	if late.lastReject != "expired_before_playout" {
		t.Fatal(late.lastReject)
	}
	for i := 0; i < 100; i++ {
		check("already_played", &j, packet(1, 0), base.Add(time.Second))
	}
	snap := stats.snapshot().Timing
	for _, reason := range []string{"invalid_payload_size", "duplicate_timestamp", "already_played", "too_late", "too_early", "unexpected_ssrc", "queue_full", "expired_before_playout"} {
		if snap.Rejections[reason] == 0 {
			t.Fatal("missing reason", reason, snap)
		}
	}
	if len(snap.Events) != 32 || len(snap.Rejections) > 10 {
		t.Fatal("unbounded telemetry")
	}
	snap.Rejections["already_played"] = 0
	snap.Events[0].Reason = "modified"
	if stats.snapshot().Timing.Rejections["already_played"] == 0 || stats.snapshot().Timing.Events[0].Reason == "modified" {
		t.Fatal("mutable snapshot aliases media state")
	}
}
func TestRTCTimelineResetDiscardsOldDecodedEpoch(t *testing.T) {
	now := time.Unix(100, 0)
	stats := &rtcMediaStats{}
	var q rtcAudioRelease
	q.push(make([]int16, 960), now, stats, "operator_to_carrier")
	q.resetTimeline(stats, "operator_to_carrier", 1, now)
	if q.count != 0 || stats.snapshot().IngressDiscardedMS != 40 || q.recoveryEpoch != 1 {
		t.Fatal("old epoch retained/unaccounted")
	}
	q.resetTimeline(stats, "operator_to_carrier", 1, now)
	if stats.snapshot().IngressDiscardedMS != 40 {
		t.Fatal("epoch reset counted twice")
	}
	if stats.snapshot().Timing.DecodedRecoveries != 0 || stats.snapshot().Timing.BridgeHandoffs != 0 {
		t.Fatal("queue reset claimed playback")
	}
}
func TestRTCTimelineRecoveryCooldownAndDuplicateInterleaving(t *testing.T) {
	speech := rtcRecoverySpeech(t)
	dec, e := newRTCOpusDecoder()
	if e != nil {
		t.Fatal(e)
	}
	defer dec.Close()
	scratch := make([]int16, 2880)
	j := rtcJitter{validate: func(p []byte) bool { n, e := dec.decode(p, scratch); return e == nil && n > 0 }}
	base := time.Unix(100, 0)
	for i := 0; i < 600; i++ {
		ts := uint32(i * 960)
		if i >= 150 {
			ts -= 24000
		}
		if i >= 350 {
			ts -= 24000
		}
		p := &rtp.Packet{Header: rtp.Header{SSRC: 42, SequenceNumber: uint16(i), Timestamp: ts}, Payload: speech[i%250]}
		now := base.Add(time.Duration(i) * 20 * time.Millisecond)
		j.push(p, now)
		if i >= 150 && i < 160 {
			j.push(p, now)
		}
		for j.pop(now) != nil {
		}
	}
	if j.epoch != 2 || j.candidate.count != 0 {
		t.Fatal("recovery thrashed or duplicates stalled it", j.epoch, j.candidate)
	}
}

func TestRTCReceiveHealthyPCMIdentical(t *testing.T) {
	packets := rtcRecoverySpeech(t)
	for _, adaptive := range []bool{false, true} {
		t.Run(fmt.Sprint(adaptive), func(t *testing.T) {
			decoder, err := newRTCOpusDecoder()
			if err != nil {
				t.Fatal(err)
			}
			defer decoder.Close()
			referenceDecoder, err := newRTCOpusDecoder()
			if err != nil {
				t.Fatal(err)
			}
			defer referenceDecoder.Close()
			var expected, actual []int16
			pcm := make([]int16, 2880)
			for _, payload := range packets {
				n, e := referenceDecoder.decode(payload, pcm)
				if e != nil {
					t.Fatal(e)
				}
				expected = append(expected, pcm[:n]...)
			}
			stats := &rtcMediaStats{}
			j := rtcJitter{adaptive: adaptive, onTiming: stats.timing}
			var timeline rtcDecodeTimeline
			var release rtcAudioRelease
			base := time.Unix(100, 0)
			for i := 0; i < len(packets)+10; i++ {
				now := base.Add(time.Duration(i) * 20 * time.Millisecond)
				if i < len(packets) {
					p := &rtp.Packet{Header: rtp.Header{SSRC: 42, SequenceNumber: uint16(i), Timestamp: uint32(i * 960)}, Payload: packets[i]}
					if !j.push(p, now) {
						t.Fatal(j.lastReject)
					}
				}
				for p := j.pop(now); p != nil; p = j.pop(now) {
					decodeRTCPacket(p, &j, &timeline, &release, decoder, pcm, stats, "operator_to_carrier", true, now, 0)
				}
				if f, ok := release.pop(now, stats, "operator_to_carrier"); ok {
					actual = append(actual, f.pcm...)
				}
			}
			if !slices.Equal(expected, actual) {
				t.Fatalf("healthy PCM changed: expected=%d actual=%d", len(expected), len(actual))
			}
			if stats.snapshot().Timing.TargetMS != 60 || j.epoch != 0 {
				t.Fatal("healthy timing changed")
			}
		})
	}
}

func TestRTCTimingConcurrentSnapshots(t *testing.T) {
	stats := &rtcMediaStats{}
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				stats.timing(rtcTimingEvent{Kind: "rejected", Reason: "too_late", TargetMS: 60})
				s := stats.snapshot()
				if len(s.Timing.Events) > 32 {
					t.Error("unbounded event history")
				}
				s.Timing.Rejections["too_late"] = 0
			}
		}()
	}
	wg.Wait()
	if stats.snapshot().Timing.Rejections["too_late"] != 4000 {
		t.Fatal("lost diagnostic updates")
	}
}

func TestRTCTimelineRecoveryRateLimited(t *testing.T) {
	packets := rtcRecoverySpeech(t)
	decoder, err := newRTCOpusDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	pcm := make([]int16, 2880)
	j := rtcJitter{validate: func(payload []byte) bool {
		n, e := decoder.decode(payload, pcm)
		return e == nil && n > 0
	}}
	base := time.Unix(100, 0)
	var firstReset time.Time
	for i := 0; i < 400; i++ {
		now := base.Add(time.Duration(i) * 20 * time.Millisecond)
		ts := uint32(i * 960)
		if i >= 150 {
			ts -= 24000
		}
		if i >= 160 {
			ts -= 24000
		} // A second fault during the cooldown.
		previous := j.epoch
		j.push(&rtp.Packet{Header: rtp.Header{SSRC: 42, SequenceNumber: uint16(i), Timestamp: ts}, Payload: packets[i%len(packets)]}, now)
		if j.epoch != previous {
			if firstReset.IsZero() {
				firstReset = now
			} else if now.Sub(firstReset) < 2*time.Second {
				t.Fatal("repeated faults bypassed cooldown")
			}
		}
		for j.pop(now) != nil {
		}
	}
	if j.epoch != 2 {
		t.Fatal("stream did not recover after cooldown", j.epoch)
	}
}
