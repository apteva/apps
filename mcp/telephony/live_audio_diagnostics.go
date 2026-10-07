package main

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"runtime/metrics"
	"sync"
	"time"
)

// All server stage clocks share this monotonic epoch. Wall timestamps are for
// correlation only; they must not be subtracted from browser audio clocks.
var mediaClockOrigin = time.Now()

func mediaClockMS() float64 { return float64(time.Since(mediaClockOrigin)) / float64(time.Millisecond) }

const softphoneAudioFrameV2 uint32 = 0x32545041

func encodePlaybackFrame(data []byte, sequence uint32) []byte {
	out := make([]byte, 32+len(data))
	binary.LittleEndian.PutUint32(out, softphoneAudioFrameV2)
	binary.LittleEndian.PutUint32(out[4:], sequence)
	binary.LittleEndian.PutUint64(out[8:], math.Float64bits(mediaClockMS()))
	copy(out[32:], data)
	return out
}

type mediaGapEvent struct {
	ConnectionID         string  `json:"connection_id,omitempty"`
	PreviousConnectionID string  `json:"previous_connection_id,omitempty"`
	At                   string  `json:"at"`
	GapMS                float64 `json:"gap_ms"`
}
type mediaStageSnapshot struct {
	ConnectionID    string          `json:"connection_id,omitempty"`
	Frames          int64           `json:"frames"`
	Bytes           int64           `json:"bytes"`
	LastAt          string          `json:"last_at,omitempty"`
	LastClockMS     float64         `json:"last_clock_ms"`
	MaxGapMS        float64         `json:"max_gap_ms"`
	MaxWorkMS       float64         `json:"max_work_ms"`
	GapEvents       []mediaGapEvent `json:"gap_events,omitempty"`
	SourceTimestamp string          `json:"source_timestamp,omitempty"`
	SourceSequence  string          `json:"source_sequence,omitempty"`
}
type liveAudioTimeline struct {
	mu     sync.Mutex
	epoch  string
	stages map[string]mediaStageSnapshot
}

func (d *liveAudioTimeline) observe(stage string, bytes int, started time.Time, sourceTimestamp, sourceSequence string, connectionIDs ...string) {
	now := time.Now()
	clock := mediaClockMS()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stages == nil {
		d.stages = map[string]mediaStageSnapshot{}
		d.epoch = now.UTC().Format(time.RFC3339Nano)
	}
	s := d.stages[stage]
	connectionID := ""
	if len(connectionIDs) > 0 {
		connectionID = connectionIDs[0]
	}
	if s.Frames > 0 {
		gap := clock - s.LastClockMS
		s.MaxGapMS = max(s.MaxGapMS, gap)
		if gap >= 100 {
			s.GapEvents = append(s.GapEvents, mediaGapEvent{At: now.UTC().Format(time.RFC3339Nano), GapMS: gap, ConnectionID: connectionID, PreviousConnectionID: s.ConnectionID})
			if len(s.GapEvents) > 32 {
				s.GapEvents = append([]mediaGapEvent(nil), s.GapEvents[len(s.GapEvents)-32:]...)
			}
		}
	}
	s.ConnectionID = connectionID
	s.Frames++
	s.Bytes += int64(bytes)
	s.LastClockMS = clock
	s.LastAt = now.UTC().Format(time.RFC3339Nano)
	if !started.IsZero() {
		s.MaxWorkMS = max(s.MaxWorkMS, float64(now.Sub(started))/float64(time.Millisecond))
	}
	if sourceTimestamp != "" {
		s.SourceTimestamp = sourceTimestamp
	}
	if sourceSequence != "" {
		s.SourceSequence = sourceSequence
	}
	d.stages[stage] = s
}
func (d *liveAudioTimeline) snapshot() (string, map[string]mediaStageSnapshot) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]mediaStageSnapshot, len(d.stages))
	for k, v := range d.stages {
		v.GapEvents = append([]mediaGapEvent(nil), v.GapEvents...)
		out[k] = v
	}
	return d.epoch, out
}

type serverAudioDiagnostics struct {
	WebRTC                 rtcMediaSnapshot              `json:"webrtc"`
	Socket                 audioSocketSnapshot           `json:"browser_socket"`
	Health                 audioHealthSnapshot           `json:"audio_health"`
	Reception              carrierReceptionSnapshot      `json:"carrier_reception"`
	CarrierPacer           livePacerSnapshot             `json:"carrier_pacer"`
	Process                mediaProcessSnapshot          `json:"process"`
	CaptureStaleBytes      int64                         `json:"capture_stale_bytes"`
	Epoch                  string                        `json:"epoch"`
	UpdatedAt              string                        `json:"updated_at"`
	Stages                 map[string]mediaStageSnapshot `json:"stages"`
	CarrierForward         liveAudioQueueSnapshot        `json:"carrier_forward"`
	ToBrowser              liveAudioQueueSnapshot        `json:"to_browser"`
	ToCarrierBridge        liveAudioQueueSnapshot        `json:"to_carrier_bridge"`
	CaptureTimestampMS     float64                       `json:"capture_timestamp_ms"`
	CaptureWorkerAgeMS     float64                       `json:"capture_worker_age_ms"`
	CaptureTransitExcessMS float64                       `json:"capture_transit_excess_ms"`
	CaptureDropEvents      []audioDropEvent              `json:"capture_drop_events,omitempty"`
	CaptureSequenceGaps    int                           `json:"capture_sequence_gaps"`
	CaptureMutedFrames     uint64                        `json:"capture_muted_frames"`
	CaptureMutedMS         uint64                        `json:"capture_muted_ms"`
	CaptureMutedEvents     []captureMutedEvent           `json:"capture_muted_events,omitempty"`
}

func (h *softphoneHub) serverAudioSnapshot() serverAudioDiagnostics {
	epoch, stages := h.timeline.snapshot()
	socket, health := h.telemetry.snapshots()
	h.mu.Lock()
	defer h.mu.Unlock()
	rtc := h.completedRTC
	if h.browser != nil {
		if c, ok := h.browser.conn.(*rtcHubConn); ok {
			rtc = mergeRTCSnapshots(rtc, c.stats.snapshot())
		}
	}
	return serverAudioDiagnostics{Socket: socket, Health: health, Reception: h.reception.snapshot(mediaClockMS(), h.carrierForward != nil && !h.held && (h.status == "answered" || h.status == "in-progress")), CarrierPacer: h.pacerStats.snapshot(), Process: sampleMediaProcess(), CaptureStaleBytes: h.captureStaleBytes, Epoch: epoch, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Stages: stages,
		WebRTC:         rtc,
		CarrierForward: mergeLiveAudioSnapshots(h.completedCarrierForward, h.carrierForward.audioSnapshot()),
		ToBrowser:      mergeLiveAudioSnapshots(h.completedBrowser, h.browser.audioSnapshot()), ToCarrierBridge: mergeLiveAudioSnapshots(h.completedPeer, h.peer.audioSnapshot()),
		CaptureTimestampMS: h.captureTimestampMS, CaptureWorkerAgeMS: h.captureWorkerAgeMS, CaptureSequenceGaps: h.captureSequenceGaps, CaptureTransitExcessMS: h.captureTransitExcessMS, CaptureDropEvents: append([]audioDropEvent(nil), h.captureDropEvents...), CaptureMutedFrames: h.captureMutedFrames, CaptureMutedMS: h.captureMutedFrames * 20, CaptureMutedEvents: append([]captureMutedEvent(nil), h.captureMutedEvents...)}
}
func mergeLiveAudioSnapshots(a, b liveAudioQueueSnapshot) liveAudioQueueSnapshot {
	a.QueuedMS = b.QueuedMS
	a.MaxQueuedMS = max(a.MaxQueuedMS, b.MaxQueuedMS)
	a.MaxResidenceMS = max(a.MaxResidenceMS, b.MaxResidenceMS)
	a.MaxWriteMS = max(a.MaxWriteMS, b.MaxWriteMS)
	a.EnqueuedBytes += b.EnqueuedBytes
	a.SentBytes += b.SentBytes
	a.WhisperSentFrames += b.WhisperSentFrames
	a.WhisperDroppedFrames += b.WhisperDroppedFrames
	a.OverflowBytes += b.OverflowBytes
	a.StaleBytes += b.StaleBytes
	a.SourceStaleBytes += b.SourceStaleBytes
	a.FlushedBytes += b.FlushedBytes
	a.FailedBytes += b.FailedBytes
	a.WriteErrors += b.WriteErrors
	if b.LastWriteAt > a.LastWriteAt {
		a.LastWriteAt = b.LastWriteAt
	}
	return a
}
func (c *callsDB) updateServerAudioDiagnostics(id string, s serverAudioDiagnostics) error {
	encoded, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(`UPDATE calls SET browser_audio_diagnostics=json_set(CASE WHEN json_valid(browser_audio_diagnostics) THEN browser_audio_diagnostics ELSE '{}' END, '$.server', json(?)) WHERE id=? AND peer_kind='human'`, string(encoded), id)
	return err
}
func (h *softphoneHub) observeCaptureTiming(data []byte, connectionIDs ...string) (stale bool) {
	connectionID := ""
	if len(connectionIDs) > 0 {
		connectionID = connectionIDs[0]
	}
	if len(data) < 16 {
		return
	}
	magic := binary.LittleEndian.Uint32(data)
	if magic != softphoneAudioFrameMagic && magic != softphoneAudioFrameV2 {
		return
	}
	timestamp := math.Float64frombits(binary.LittleEndian.Uint64(data[8:]))
	h.mu.Lock()
	defer h.mu.Unlock()
	if !math.IsNaN(timestamp) && !math.IsInf(timestamp, 0) && timestamp >= 0 {
		h.captureTimestampMS = timestamp
	}
	if magic == softphoneAudioFrameV2 && len(data) >= 32 {
		sent := math.Float64frombits(binary.LittleEndian.Uint64(data[16:]))
		if !math.IsNaN(sent) && !math.IsInf(sent, 0) && sent > 0 {
			delta := mediaClockMS() - sent
			if !h.captureTransitSet || delta < h.captureTransitBase {
				h.captureTransitBase = delta
				h.captureTransitSet = true
			}
			h.captureTransitExcessMS = max(h.captureTransitExcessMS, delta-h.captureTransitBase)
			// This is EXCESS over the fastest observed transit, not absolute
			// one-way latency. No wall-clock synchronization is assumed.
			if delta-h.captureTransitBase > float64(liveAudioMaxAge/time.Millisecond) {
				h.captureStaleBytes += int64(len(data) - 32)
				h.captureDropEvents = append(h.captureDropEvents, audioDropEvent{
					Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Direction: "operator_to_carrier", Reason: "capture_transit_age", ConnectionID: connectionID,
					DurationMS: (len(data) - 32) * 1000 / 48000, QueueBeforeMS: int(min(60000, delta-h.captureTransitBase)), Sequence: uint64(binary.LittleEndian.Uint32(data[4:])),
				})
				if len(h.captureDropEvents) > 100 {
					h.captureDropEvents = h.captureDropEvents[len(h.captureDropEvents)-100:]
				}
				stale = true
			}
		}
		age := math.Float64frombits(binary.LittleEndian.Uint64(data[24:]))
		if !math.IsNaN(age) && !math.IsInf(age, 0) && age >= 0 {
			h.captureWorkerAgeMS = max(h.captureWorkerAgeMS, min(age, 60000))
		}
	}
	return stale
}

func (h *softphoneHub) setCarrierForward(w *websocketWriterPump) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.carrierForward = w
	h.reception.begin(false)
}
func (h *softphoneHub) finishCarrierForward(w *websocketWriterPump) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.completedCarrierForward = mergeLiveAudioSnapshots(h.completedCarrierForward, w.audioSnapshot())
	if h.carrierForward == w {
		h.carrierForward = nil
	}
}
func carrierMediaWriteTimeout(row *callRow) time.Duration {
	if row.PeerKind == peerKindHuman || row.PeerKind == peerKindExternal {
		return liveAudioMaxAge
	}
	return websocketWriteTimeout
}

// Sample once per process per five seconds, shared across concurrent calls.
// Histogram percentiles are cumulative since process start, not one-way audio latency.
type mediaProcessSnapshot struct {
	SampledAt      string  `json:"sampled_at"`
	Goroutines     uint64  `json:"goroutines"`
	HeapBytes      uint64  `json:"heap_bytes"`
	SchedulerP99MS float64 `json:"scheduler_p99_ms"`
	GCPauseP99MS   float64 `json:"gc_pause_p99_ms"`
}

var mediaProcessCache struct {
	sync.Mutex
	at    time.Time
	value mediaProcessSnapshot
}

func sampleMediaProcess() mediaProcessSnapshot {
	mediaProcessCache.Lock()
	defer mediaProcessCache.Unlock()
	if time.Since(mediaProcessCache.at) < 5*time.Second {
		return mediaProcessCache.value
	}
	samples := []metrics.Sample{{Name: "/sched/goroutines:goroutines"}, {Name: "/memory/classes/heap/objects:bytes"}, {Name: "/sched/latencies:seconds"}, {Name: "/gc/pauses:seconds"}}
	metrics.Read(samples)
	percentile := func(v metrics.Value) float64 {
		if v.Kind() != metrics.KindFloat64Histogram {
			return 0
		}
		h := v.Float64Histogram()
		var total uint64
		for _, n := range h.Counts {
			total += n
		}
		if total == 0 {
			return 0
		}
		var seen uint64
		for i, n := range h.Counts {
			seen += n
			if float64(seen) >= float64(total)*.99 {
				bound := h.Buckets[i+1]
				if math.IsInf(bound, 0) {
					bound = h.Buckets[i]
				}
				return max(0, bound*1000)
			}
		}
		return 0
	}
	value := mediaProcessSnapshot{SampledAt: time.Now().UTC().Format(time.RFC3339Nano), SchedulerP99MS: percentile(samples[2].Value), GCPauseP99MS: percentile(samples[3].Value)}
	if samples[0].Value.Kind() == metrics.KindUint64 {
		value.Goroutines = samples[0].Value.Uint64()
	}
	if samples[1].Value.Kind() == metrics.KindUint64 {
		value.HeapBytes = samples[1].Value.Uint64()
	}
	mediaProcessCache.at = time.Now()
	mediaProcessCache.value = value
	return value
}

// Pacer counters survive eviction of the bounded diagnostic event sample.
type livePacerSnapshot struct {
	SentMS       int64            `json:"sent_ms"`
	DroppedMS    int64            `json:"dropped_ms"`
	DropTotalsMS map[string]int64 `json:"drop_totals_ms"`
	MaxQueueMS   int              `json:"max_queue_ms"`
	MaxWriteMS   int64            `json:"max_write_ms"`
}
type livePacerStats struct {
	mu    sync.Mutex
	value livePacerSnapshot
}

func (p *livePacerStats) dropped(reason string, ms int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.value.DropTotalsMS == nil {
		p.value.DropTotalsMS = map[string]int64{}
	}
	p.value.DroppedMS += int64(ms)
	p.value.DropTotalsMS[reason] += int64(ms)
}
func (p *livePacerStats) queued(ms int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.value.MaxQueueMS = max(p.value.MaxQueueMS, ms)
}
func (p *livePacerStats) sent(ms int, started time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.value.SentMS += int64(ms)
	p.value.MaxWriteMS = max(p.value.MaxWriteMS, time.Since(started).Milliseconds())
}
func (p *livePacerStats) snapshot() livePacerSnapshot {
	if p == nil {
		return livePacerSnapshot{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.value
	s.DropTotalsMS = map[string]int64{}
	for k, v := range p.value.DropTotalsMS {
		s.DropTotalsMS[k] = v
	}
	return s
}
func (h *softphoneHub) setPacerStats(p *livePacerStats) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pacerStats = p
}
