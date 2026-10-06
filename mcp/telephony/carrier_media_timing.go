package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"
)

// APT3 is internal human-bridge framing and negotiated browser playback.
// Realtime/AI peers continue receiving plain PCM. Clocks are relative to the
// server monotonic epoch, not UTC. Source age is excess over the fastest
// observed carrier delivery, not a claim of absolute one-way carrier latency.
const softphoneAudioFrameV3 uint32 = 0x33545041
const sourceAudioHeaderBytes = 64
const liveSourceBudgetMS = 320.0
const carrierDeliveryStallMS = 2000.0

type carrierSource struct {
	Stream      string
	TimestampMS float64
	Sequence    uint64
	Timed       bool
}

type carrierReceptionSnapshot struct {
	Stream             string                 `json:"stream,omitempty"`
	Epoch              uint32                 `json:"epoch"`
	Frames             int64                  `json:"frames"`
	ReceivedMS         float64                `json:"received_ms"`
	LastReceiptMS      float64                `json:"last_receipt_ms"`
	SourceTimestampMS  float64                `json:"source_timestamp_ms"`
	SourceSequence     uint64                 `json:"source_sequence"`
	MaxGapMS           float64                `json:"max_gap_ms"`
	MaxExcessAgeMS     float64                `json:"max_excess_age_ms"`
	StaleDroppedMS     float64                `json:"stale_dropped_ms"`
	DuplicateDroppedMS float64                `json:"duplicate_dropped_ms"`
	MaxBatchMS         float64                `json:"max_batch_ms"`
	Stalls             int64                  `json:"stalls"`
	Recoveries         int64                  `json:"recoveries"`
	Stalled            bool                   `json:"stalled"`
	ContinuousExpected bool                   `json:"continuous_expected"`
	Events             []carrierDeliveryEvent `json:"events,omitempty"`
}

type carrierDeliveryEvent struct {
	At                string  `json:"at"`
	Reason            string  `json:"reason"`
	ReceiptMS         float64 `json:"receipt_ms"`
	SourceTimestampMS float64 `json:"source_timestamp_ms"`
	SourceSequence    uint64  `json:"source_sequence"`
	GapMS             float64 `json:"gap_ms"`
	ExcessAgeMS       float64 `json:"excess_age_ms"`
}

type carrierReception struct {
	mu            sync.Mutex
	s             carrierReceptionSnapshot
	set           bool
	base          float64
	lastTimestamp float64
	lastSequence  uint64
	batchMS       float64
	paused        bool
}

func (r *carrierReception) event(reason string, now, gap, age float64, src carrierSource) {
	r.s.Events = append(r.s.Events, carrierDeliveryEvent{time.Now().UTC().Format(time.RFC3339Nano), reason, now, src.TimestampMS, src.Sequence, gap, age})
	if len(r.s.Events) > 32 {
		r.s.Events = append([]carrierDeliveryEvent(nil), r.s.Events[len(r.s.Events)-32:]...)
	}
}

func (r *carrierReception) pause(value bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.paused != value {
		r.s.LastReceiptMS = 0
		r.s.Stalled = false
	}
	r.paused = value
}

func mediaNumber(value any) (float64, bool) {
	if value == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(fmt.Sprint(value), 64)
	return n, err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0
}

func sourceMedia(stream string, timestamp, chunk any) carrierSource {
	t, valid := mediaNumber(timestamp)
	q, ok := mediaNumber(chunk)
	s := carrierSource{Stream: stream, TimestampMS: t, Timed: valid}
	if ok && q < float64(^uint64(0)) && math.Trunc(q) == q {
		s.Sequence = uint64(q)
	}
	return s
}

func (r *carrierReception) begin(continuous bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.s.Epoch++
	r.s.Stream = ""
	r.s.ContinuousExpected = continuous
	r.s.Stalled = false
	r.s.LastReceiptMS = 0
	r.set = false
	r.lastSequence = 0
	r.batchMS = 0
}

// observe counts valid media including zero-valued PCM. It never uses speech
// amplitude or operator microphone traffic as a transport-health signal.
func (r *carrierReception) observe(src carrierSource, duration, now float64) (mapped float64, drop bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if src.Stream != "" && src.Stream != r.s.Stream {
		r.s.Stream = src.Stream
		r.s.Epoch++
		r.set = false
		r.lastSequence = 0
	}
	gap := 0.0
	if r.s.LastReceiptMS > 0 {
		gap = max(0, now-r.s.LastReceiptMS)
		r.s.MaxGapMS = max(r.s.MaxGapMS, gap)
	}
	// A stall followed by an entire batch before the watcher ticks must still
	// appear in the incident counters.
	if !r.paused && r.s.ContinuousExpected && gap >= carrierDeliveryStallMS && !r.s.Stalled {
		r.s.Stalled = true
		r.s.Stalls++
		r.event("reception_stalled", now, gap, 0, src)
	}
	r.s.Frames++
	r.s.ReceivedMS += duration
	r.s.LastReceiptMS = now
	if gap < 5 {
		r.batchMS += duration
	} else {
		r.batchMS = duration
	}
	r.s.MaxBatchMS = max(r.s.MaxBatchMS, r.batchMS)
	mapped = now
	if src.Timed {
		if r.set && src.Sequence > 0 && src.Sequence <= r.lastSequence {
			r.s.DuplicateDroppedMS += duration
			return now, true
		}
		// Timestamp restart with continuing sequence defines a new source epoch.
		// A new stream/bridge also resets the mapping. A forward timestamp jump
		// may be omitted silence, so it is never counted as discarded PCM.
		if r.set && src.TimestampMS < r.lastTimestamp {
			r.set = false
			r.s.Epoch++
		}
		offset := now - src.TimestampMS
		if !r.set {
			r.base = offset
			r.set = true
		} else {
			r.base = min(r.base, offset)
		}
		mapped = src.TimestampMS + r.base
		age := max(0, now-mapped)
		if gap >= carrierDeliveryStallMS {
			r.event("delivery_after_gap", now, gap, age, src)
		}
		r.s.MaxExcessAgeMS = max(r.s.MaxExcessAgeMS, age)
		r.lastTimestamp = src.TimestampMS
		r.lastSequence = src.Sequence
		r.s.SourceTimestampMS = src.TimestampMS
		r.s.SourceSequence = src.Sequence
		// Duration-sized packets may partially overlap the live budget. Keep
		// that boundary packet; downstream applies the same budget, not a new one.
		if age-duration > liveSourceBudgetMS {
			r.s.StaleDroppedMS += duration
			return mapped, true
		}
	}
	if r.s.Stalled {
		r.s.Stalled = false
		r.s.Recoveries++
		r.event("reception_recovered", now, gap, max(0, now-mapped), src)
	}
	return mapped, false
}

func (r *carrierReception) snapshot(now float64, active bool) carrierReceptionSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	if active && !r.paused && r.s.ContinuousExpected && r.s.LastReceiptMS > 0 && now-r.s.LastReceiptMS >= carrierDeliveryStallMS && !r.s.Stalled {
		r.s.Stalled = true
		r.s.Stalls++
		r.event("reception_stalled", now, now-r.s.LastReceiptMS, 0, carrierSource{})
	}
	s := r.s
	s.Events = append([]carrierDeliveryEvent(nil), s.Events...)
	if !active {
		s.Stalled = false
	}
	return s
}

func encodeSourceAudio(data []byte, src carrierSource, mapped, receipt float64, epoch uint32) []byte {
	out := make([]byte, sourceAudioHeaderBytes+len(data))
	binary.LittleEndian.PutUint32(out, softphoneAudioFrameV3)
	binary.LittleEndian.PutUint64(out[8:], math.Float64bits(receipt))
	binary.LittleEndian.PutUint64(out[32:], math.Float64bits(src.TimestampMS))
	binary.LittleEndian.PutUint64(out[40:], src.Sequence)
	binary.LittleEndian.PutUint64(out[48:], math.Float64bits(mapped))
	binary.LittleEndian.PutUint32(out[56:], epoch)
	if src.Timed {
		binary.LittleEndian.PutUint32(out[60:], 1)
	}
	copy(out[sourceAudioHeaderBytes:], data)
	return out
}

func sourceAudioHeader(data []byte) int {
	if len(data) >= sourceAudioHeaderBytes && binary.LittleEndian.Uint32(data) == softphoneAudioFrameV3 {
		return sourceAudioHeaderBytes
	}
	if len(data) >= 32 && binary.LittleEndian.Uint32(data) == softphoneAudioFrameV2 {
		return 32
	}
	return 0
}

func (h *softphoneHub) carrierDeliveryNotice() {
	h.mu.Lock()
	active := h.carrierForward != nil && !h.held && (h.status == "answered" || h.status == "in-progress")
	if !active {
		h.deliveryNotice = ""
		h.mu.Unlock()
		return
	}
	s := h.reception.snapshot(mediaClockMS(), active)
	if h.readyBrowser != h.browser || h.browser == nil {
		h.mu.Unlock()
		return
	}
	state := "flowing"
	if s.Stalled {
		state = "stalled"
	}
	if h.deliveryNotice == "" && state == "flowing" {
		h.deliveryNotice = state
		h.mu.Unlock()
		return
	}
	if state == h.deliveryNotice {
		h.mu.Unlock()
		return
	}
	data, _ := json.Marshal(map[string]any{"type": "media.delivery", "direction": "carrier_to_operator", "state": state, "gap_ms": max(0, mediaClockMS()-s.LastReceiptMS)})
	if h.browser.queueControl(data) {
		h.deliveryNotice = state
	}
	h.mu.Unlock()
}
