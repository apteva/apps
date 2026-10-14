//go:build (linux || darwin) && (amd64 || arm64)

package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pion/opus"
	"github.com/pion/rtp"
)

// Local, paced capacity evidence, not a deployment-wide capacity guarantee.
// Includes actual bidirectional UDP/SRTP, production SIP decoding/framing,
// resampling and Opus encode/decode. Excludes SIP signaling, Apteva's proxy,
// browser rendering and routing. TELEPHONY_SIP_MIXED_LOAD=1 adds recording,
// call-list and notification work. No external carrier.
func TestSIPPlaybackClockPacedLoad(t *testing.T) {
	path := os.Getenv("TELEPHONY_SIP_LOAD_RESULTS")
	if path == "" {
		t.Skip("opt-in paced SIP/SRTP + Opus load measurement")
	}
	type result struct {
		ProcessGCPauseMS          float64 `json:"process_gc_pause_ms"`
		GoHeartbeatMaxGapMS       float64 `json:"go_heartbeat_max_gap_ms"`
		MixedBackgroundOperations int64   `json:"mixed_background_operations"`
		RecordingDroppedMS        int64   `json:"recording_dropped_ms"`
		Calls                     int     `json:"calls"`
		Codec                     string  `json:"browser_codec"`
		Seconds                   float64 `json:"wall_seconds"`
		CPUPercent                float64 `json:"cpu_percent_one_core"`
		WorkP99MS                 float64 `json:"playback_work_p99_ms"`
		WorkMaxMS                 float64 `json:"playback_work_max_ms"`
		ScheduleMaxMS             float64 `json:"playback_schedule_max_ms"`
		DiscardedMS               float64 `json:"discarded_ms"`
		ReplacementMS             float64 `json:"replacement_ms"`
		Received, Sent, Returned  int64
		MaxQueueMS                float64            `json:"max_queue_ms"`
		AllocBytes                uint64             `json:"allocated_bytes"`
		ClockUpdates              int                `json:"clock_rate_updates"`
		ClockRateRejected         int                `json:"clock_rate_rejected"`
		ClockPhaseWithheld        int                `json:"clock_phase_withheld"`
		LateDroppedMS             float64            `json:"late_discarded_ms"`
		ExpiredDroppedMS          float64            `json:"expired_discarded_ms"`
		OverflowDroppedMS         float64            `json:"overflow_discarded_ms"`
		ObservedDropReasons       map[string]float64 `json:"retained_drop_event_reasons_ms"`
	}
	type incident struct {
		Codec    string          `json:"codec"`
		Calls    int             `json:"concurrent_calls"`
		Stream   int             `json:"stream_index"`
		Snapshot sipLiveSnapshot `json:"snapshot"`
	}
	var incidents []incident
	var results []result
	for _, count := range []int{32, 64} {
		for _, variant := range []string{"portable", "native_fec", "native_24k_fec"} {
			t.Run(fmt.Sprintf("%s/%d", variant, count), func(t *testing.T) {
				type worker struct {
					carrier, server       *sipRTPMedia
					jitter                *rtpJitterBuffer
					framer                *rtpAudioFramer
					encoder               rtcOpusEncoder
					decoder               rtcOpusDecoder
					up, down              *pcmResampler
					costs                 []float64
					scheduleMax, queueMax float64
				}
				recorders, stopMixed := startSIPMixedLoad(t, count)
				workers := make([]worker, count)
				for i := range workers {
					w := &workers[i]
					open := func() *net.UDPConn {
						conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = conn.Close() })
						return conn
					}
					left, right := open(), open()
					key := make([]byte, 30)
					for k := range key {
						key[k] = byte(i + k + 1)
					}
					w.server = &sipRTPMedia{conn: left, remote: right.LocalAddr().(*net.UDPAddr), offer: sipMediaOffer{Codec: "G722", PayloadType: 9, PacketSamples: 160, Secure: true, RemoteKey: key}}
					var err error
					w.server.security, err = newSIPMediaSecurity(w.server.offer)
					if err != nil {
						t.Fatal(err)
					}
					w.carrier = &sipRTPMedia{conn: right, remote: left.LocalAddr().(*net.UDPAddr), offer: w.server.offer}
					w.server.observations.kernelTimestampAvailable.Store(enableSIPSocketTimestamps(left))
					w.carrier.observations.kernelTimestampAvailable.Store(enableSIPSocketTimestamps(right))
					w.carrier.offer.RemoteKey = w.server.security.LocalKey
					w.carrier.security, err = newSIPMediaSecurityWithLocalKey(w.carrier.offer, key)
					if err != nil {
						t.Fatal(err)
					}
					if variant == "portable" {
						w.encoder, err = newRTCGoEncoder(32000)
						d, derr := opus.NewDecoderWithOutput(24000, 1)
						if derr != nil {
							t.Fatal(derr)
						}
						w.decoder = &rtcGoOpusDecoder{&d}
					} else {
						rate := 48000
						if variant == "native_24k_fec" {
							rate = 24000
						}
						w.encoder, err = newRTCNativeEncoderAtRate(32000, true, rate)
						if err == nil {
							w.decoder, err = newRTCNativeDecoder()
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(w.encoder.Close)
					t.Cleanup(w.decoder.Close)
					recoveryEnabled := os.Getenv("TELEPHONY_SIP_RECOVERY_LOAD") == "1"
					w.jitter = newRTPJitterBufferWithPolicy(sipPlaybackPolicy{Resynchronize: &recoveryEnabled})
					w.server.jitterTelemetry.Store(w.jitter)
					w.framer = newRTPAudioFramer(w.jitter, w.server.offer)
					w.up, w.down = newPCMResampler(16000, rtcEncoderInputRate(w.encoder)), newPCMResampler(24000, 16000)
					w.costs = make([]float64, 0, 800)
				}
				var wg sync.WaitGroup
				errors := make(chan error, count*4)
				start := time.Now().Add(100 * time.Millisecond)
				const frames = 800 // 16 seconds, including multiple estimator updates.
				for i := range workers {
					w := &workers[i]
					for _, receive := range []bool{true, false} {
						wg.Add(1)
						go func(receive bool) {
							defer wg.Done()
							media := w.carrier
							if receive {
								media = w.server
							}
							_ = media.conn.SetReadDeadline(start.Add(frames*20*time.Millisecond + 300*time.Millisecond))
							buf := make([]byte, 2048)
							for {
								n, _, err := media.readPacket(buf)
								if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
									return
								}
								if err != nil {
									errors <- err
									return
								}
								p, err := media.decodePacketAt(buf[:n], media.readAt)
								if err != nil {
									errors <- err
									return
								}
								if receive {
									w.jitter.pushSourceRTPAt(p.SSRC, p.SequenceNumber, p.Payload, p.Timestamp, media.readAt, time.Now())
								}
							}
						}(receive)
					}
					wg.Add(2)
					go func(i int) {
						defer wg.Done()
						pcm := make([]int16, 320)
						for frame := 0; frame < frames; frame++ {
							// In-order 0/12 ms delivery variation, matching the long replay.
							at := start.Add(time.Duration(frame)*20*time.Millisecond + time.Duration(i%16)*time.Millisecond + time.Duration(frame%2)*12*time.Millisecond)
							if wait := time.Until(at); wait > 0 {
								time.Sleep(wait)
							}
							for k := range pcm {
								pcm[k] = int16(4000 * math.Sin(float64(frame*320+k)*.0785))
							}
							p := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 9, SequenceNumber: uint16(frame), Timestamp: uint32(frame * 160), SSRC: uint32(i + 1)}, Payload: w.carrier.encodeAudio(pcm)}
							if err := w.carrier.writePacket(p); err != nil {
								errors <- err
								return
							}
						}
					}(i)
					go func(i int) {
						defer wg.Done()
						encoded := make([]byte, 1275)
						decoded := make([]int16, 2880)
						frameSamples := rtcEncoderInputRate(w.encoder) / 50
						converted := make([]int16, 0, frameSamples+2)
						pending := make([]float32, 0, frameSamples*2)
						seq := uint16(0)
						var returnPending []int16
						for frame := 0; frame < frames; frame++ {
							at := start.Add(time.Duration(frame)*20*time.Millisecond + time.Duration(i%16)*time.Millisecond)
							if wait := time.Until(at); wait > 0 {
								time.Sleep(wait)
							}
							w.scheduleMax = max(w.scheduleMax, float64(time.Since(at))/float64(time.Millisecond))
							began := time.Now()
							pcm, ready := w.framer.popPCM(w.server)
							if !ready {
								continue
							}
							w.jitter.mu.Lock()
							w.queueMax = max(w.queueMax, float64(w.jitter.buffered+int(w.jitter.pendingPCMSamples.Load()))/8)
							w.jitter.mu.Unlock()
							converted = w.up.ProcessInto(pcm, converted)
							for _, v := range converted {
								pending = append(pending, float32(v)/32768)
							}
							if len(pending) < frameSamples {
								continue
							}
							n, err := w.encoder.EncodeFloat32(pending[:frameSamples], encoded)
							pending = pending[:copy(pending, pending[frameSamples:])]
							if err != nil {
								errors <- err
								return
							}
							samples, err := w.decoder.decode(encoded[:n], decoded)
							if err != nil {
								errors <- err
								return
							}
							recorders[i].Record(0, decoded[:samples])
							recorders[i].Record(1, decoded[:samples])
							returnPending = append(returnPending, w.down.Process(decoded[:samples])...)
							if len(returnPending) < 320 {
								continue
							}
							pcmBack := returnPending[:320]
							p := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 9, SequenceNumber: seq, Timestamp: uint32(seq) * 160, SSRC: uint32(i + 1000)}, Payload: w.server.encodeAudio(pcmBack)}
							if err := w.server.writePacket(p); err != nil {
								errors <- err
								return
							}
							returnPending = returnPending[320:]
							seq++
							w.costs = append(w.costs, float64(time.Since(began))/float64(time.Millisecond))
						}
					}(i)
				}
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				cpuStart, err := rtcProcessCPUSeconds()
				if err != nil {
					t.Fatal(err)
				}
				began := time.Now()
				heartbeatStop := make(chan struct{})
				heartbeatDone := make(chan time.Duration, 1)
				go func() {
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					last := time.Now()
					var gap time.Duration
					for {
						select {
						case <-heartbeatStop:
							heartbeatDone <- gap
							return
						case <-ticker.C:
							now := time.Now()
							gap = max(gap, now.Sub(last))
							last = now
						}
					}
				}()
				wg.Wait()
				close(heartbeatStop)
				heartbeatGap := <-heartbeatDone
				cpuEnd, err := rtcProcessCPUSeconds()
				if err != nil {
					t.Fatal(err)
				}
				runtime.ReadMemStats(&after)
				close(errors)
				for err := range errors {
					t.Error(err)
				}
				r := result{Calls: count, Codec: variant, Seconds: time.Since(began).Seconds(), AllocBytes: after.TotalAlloc - before.TotalAlloc, ObservedDropReasons: make(map[string]float64)}
				r.ProcessGCPauseMS = float64(after.PauseTotalNs-before.PauseTotalNs) / 1e6
				r.GoHeartbeatMaxGapMS = float64(heartbeatGap) / float64(time.Millisecond)
				r.MixedBackgroundOperations, r.RecordingDroppedMS = stopMixed()
				r.CPUPercent = (cpuEnd - cpuStart) / r.Seconds * 100
				var costs []float64
				for index, w := range workers {
					r.DiscardedMS += float64(w.jitter.droppedSamples+w.jitter.lateDroppedSamples) / 8
					r.ReplacementMS += float64(w.jitter.missingFilledSamples.Load()) / 8
					r.Received += w.server.observations.packets.Load()
					r.Sent += w.server.observations.sent.Load()
					r.Returned += w.carrier.observations.packets.Load()
					r.MaxQueueMS = max(r.MaxQueueMS, w.queueMax)
					r.ClockUpdates += w.jitter.clock.updates
					r.ClockRateRejected += w.jitter.clock.rateRejected
					r.ClockPhaseWithheld += w.jitter.clock.phaseRejected
					r.LateDroppedMS += float64(w.jitter.lateDroppedSamples) / 8
					r.ExpiredDroppedMS += float64(w.jitter.expiredSamples) / 8
					r.OverflowDroppedMS += float64(w.jitter.overflowSamples) / 8
					if w.jitter.droppedSamples+w.jitter.lateDroppedSamples > 0 {
						d := &sipLiveDiagnostics{}
						d.media.Store(w.server)
						snapshot := d.snapshot(time.Now())
						incidents = append(incidents, incident{Codec: variant, Calls: count, Stream: index, Snapshot: snapshot})
						for _, event := range snapshot.Jitter.Events {
							switch event.Reason {
							case "source_delivery_age", "ingress_processing_age", "queue_age", "decoded_partial_age", "late_arrival", "queue_overflow", "decoded_reserve_overflow", "oversized_packet":
								r.ObservedDropReasons[event.Reason] += event.DurationMS
							}
						}
					}
					r.ScheduleMaxMS = max(r.ScheduleMaxMS, w.scheduleMax)
					costs = append(costs, w.costs...)
				}
				slices.Sort(costs)
				if len(costs) > 0 {
					r.WorkP99MS = costs[int(float64(len(costs)-1)*.99)]
					r.WorkMaxMS = costs[len(costs)-1]
				}
				results = append(results, r)
				t.Logf("%+v", r)
				if r.RecordingDroppedMS != 0 || r.Received != int64(count*frames) || r.Returned != r.Sent || r.DiscardedMS != 0 || r.ReplacementMS != 0 || r.MaxQueueMS > 160 || r.WorkP99MS > 20 {
					t.Errorf("paced-load gate failed: %+v", r)
				}
			})
		}
	}
	data, err := json.MarshalIndent(map[string]any{"scope": "local bidirectional UDP/SRTP + SIP framer + resampling + Opus encode/decode; optional mixed mode adds recording, database call-list reads and retryable notification persistence; excludes live signaling, external proxy and browser rendering", "controlled_playout_recovery_enabled": os.Getenv("TELEPHONY_SIP_RECOVERY_LOAD") == "1", "mixed_recording_database_notifications": os.Getenv("TELEPHONY_SIP_MIXED_LOAD") == "1", "gomaxprocs": runtime.GOMAXPROCS(0), "logical_cpus": runtime.NumCPU(), "results": results, "incident_history": incidents, "incident_history_scope": "bounded retained events, not a complete packet trace; drop-reason durations can be lower than cumulative counters"}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
