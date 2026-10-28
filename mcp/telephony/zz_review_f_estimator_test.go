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

type reviewEstimatorResult struct {
	Played, Missing, Rejected    int
	ConcealedMS                  int64
	Coverage                     float64
	DamagedMS                    int
	P95ResidenceMS, MaxResidence float64
	Target, Reserve              int
}

// Same speech packets and arrival schedule through the production receive
// path (rtcJitter → decodeRTCPacket → rtcAudioRelease) with each estimator.
func replayRTCEstimator(t *testing.T, packets [][]byte, reference []int16, arrival []int, adaptive bool) reviewEstimatorResult {
	t.Helper()
	type incoming struct{ index, ms int }
	var input []incoming
	for i, ms := range arrival {
		if ms >= 0 {
			input = append(input, incoming{i, ms})
		}
	}
	sort.SliceStable(input, func(i, j int) bool { return input[i].ms < input[j].ms })
	j := rtcJitter{adaptive: adaptive}
	dec, err := newRTCOpusDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	stats := &rtcMediaStats{}
	var timeline rtcDecodeTimeline
	var release rtcAudioRelease
	pcm := make([]int16, 2880)
	base := time.Unix(100, 0)
	next := 0
	var frames []sipSpeechFrame
	var positions []int
	var residence []float64
	r := reviewEstimatorResult{}
	end := len(arrival)*20 + 400
	for ms := 0; ms < end; ms++ {
		now := base.Add(time.Duration(ms) * time.Millisecond)
		for next < len(input) && input[next].ms <= ms {
			v := input[next]
			next++
			p := &rtp.Packet{Header: rtp.Header{SequenceNumber: uint16(v.index), Timestamp: uint32(v.index * 960), SSRC: 42}, Payload: packets[v.index%len(packets)]}
			if !j.push(p, now) {
				r.Rejected++
			}
		}
		for p := j.pop(now); p != nil; p = j.pop(now) {
			decodeRTCPacket(p, &j, &timeline, &release, dec, pcm, stats, "operator_to_carrier", true, now, 0)
		}
		if f, ok := release.pop(now, stats, "operator_to_carrier"); ok {
			frames = append(frames, sipSpeechFrame{at: now.Sub(base), pcm: append([]int16(nil), f.pcm...), sourceSamples: len(f.pcm)})
			positions = append(positions, int((f.source.Sub(base)+10*time.Millisecond)/(20*time.Millisecond))*480)
			if ms >= 2000 {
				r.Played++
				residence = append(residence, float64(now.Sub(f.source))/float64(time.Millisecond))
			}
		}
	}
	for _, ms := range arrival {
		if ms < 0 {
			r.Missing++
		}
	}
	speech := analyzeSIPSpeech(reference, 24000, frames, positions, 60)
	r.Coverage, r.DamagedMS = speech.ActiveSpeechCoverage, speech.LongestDamagedSpeechMS
	r.ConcealedMS = stats.snapshot().IngressConcealedMS
	slices.Sort(residence)
	if len(residence) > 0 {
		r.P95ResidenceMS, r.MaxResidence = residence[len(residence)*95/100], residence[len(residence)-1]
	}
	r.Target, r.Reserve = j.distribution.target, int(j.reserve/time.Millisecond)
	if stats.snapshot().DecodeErrors != 0 {
		t.Fatal("decode errors")
	}
	return r
}

func reviewEstimatorScenarios(n int) map[string][]int {
	rng := rand.New(rand.NewSource(7))
	s := map[string][]int{}
	add := func(name string, at func(i int) int) {
		v := make([]int, n)
		for i := range v {
			v[i] = at(i)
		}
		s[name] = v
	}
	add("clean", func(i int) int { return i * 20 })
	add("jitter_3ms", func(i int) int { return i*20 + rng.Intn(4) })
	add("spikes_95ms", func(i int) int {
		if i%10 == 5 {
			return i*20 + 95
		}
		return i * 20
	})
	add("loss_1pct", func(i int) int {
		if i > 0 && rng.Intn(100) == 0 {
			return -1
		}
		return i * 20
	})
	// A 200 ms network freeze every 10 s releases the held packets at once.
	add("stall_200ms", func(i int) int {
		ms := i * 20
		if ms%10000 >= 5000 && ms%10000 < 5200 {
			return ms - ms%10000 + 5200
		}
		return ms
	})
	// Packets arrive in bunches of three every 60 ms.
	add("burst_3", func(i int) int { return (i/3)*60 + 40 })
	return s
}

// Repeatable fixed vs distribution comparison on identical inputs (logged
// table). Distribution is the default only while it loses and conceals no
// more audio than fixed in every scenario, within the 100 ms adaptation cap.
func TestReviewFRTCReceiveEstimatorScenarios(t *testing.T) {
	packets := rtcRecoverySpeech(t)
	clean, err := newRTCOpusDecoder()
	if err != nil {
		t.Fatal(err)
	}
	const n = 1500 // 30 s
	var reference []int16
	cleanPCM := make([]int16, 2880)
	for i := 0; i < n; i++ {
		k, e := clean.decode(packets[i%len(packets)], cleanPCM)
		if e != nil {
			t.Fatal(e)
		}
		reference = append(reference, cleanPCM[:k]...)
	}
	clean.Close()
	scenarios := reviewEstimatorScenarios(n)
	names := []string{"clean", "jitter_3ms", "spikes_95ms", "loss_1pct", "stall_200ms", "burst_3"}
	t.Logf("%-12s %-12s %6s %7s %8s %9s %8s %8s %7s %7s %6s %7s", "scenario", "estimator", "played", "missing", "rejected", "concealed", "coverage", "damaged", "p95_ms", "max_ms", "target", "reserve")
	for _, name := range names {
		for _, adaptive := range []bool{false, true} {
			r := replayRTCEstimator(t, packets, reference, scenarios[name], adaptive)
			t.Logf("%-12s %-12s %6d %7d %8d %9d %8.4f %8d %7.1f %7.1f %6d %7d", name, map[bool]string{false: "fixed", true: "distribution"}[adaptive],
				r.Played, r.Missing, r.Rejected, r.ConcealedMS, r.Coverage, r.DamagedMS, r.P95ResidenceMS, r.MaxResidence, r.Target, r.Reserve)
			if r.MaxResidence > 320 {
				t.Fatal(fmt.Sprintf("%s adaptive=%v exceeded the receive age bound", name, adaptive), r)
			}
		}
	}
}

func TestReviewFRTCReceiveEstimatorDefaultsToDistribution(t *testing.T) {
	c, err := parseSoftphoneRTCConfig(map[string]string{"softphone_webrtc_enabled": "true"})
	if err != nil || c.ReceiveEstimator != "distribution" {
		t.Fatal("unset estimator", c.ReceiveEstimator, err)
	}
	if c, _ = parseSoftphoneRTCConfig(map[string]string{"softphone_webrtc_receive_estimator": "fixed"}); c.ReceiveEstimator != "fixed" {
		t.Fatal("explicit fixed overridden")
	}
}
