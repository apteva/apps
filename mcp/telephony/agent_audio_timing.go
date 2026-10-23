package main

import "time"

const agentReplyHistoryLimit = 32
const agentReplyObservedQuiet = 200 * time.Millisecond

// These are server-receipt observations, not provider generation latency or
// handset playback measurements. The detector never gates or alters caller PCM.
type agentReplyTiming struct {
	ItemID         string  `json:"item_id"`
	Generation     int64   `json:"session_generation"`
	CallerSpeechAt string  `json:"last_caller_speech_observed_at"`
	CoreAudioAt    string  `json:"first_core_audio_received_at"`
	CarrierWriteAt string  `json:"first_carrier_write_at,omitempty"`
	CoreWaitMS     float64 `json:"speech_to_core_audio_ms"`
	DispatchMS     float64 `json:"core_audio_to_carrier_write_ms"`
	Outcome        string  `json:"outcome"`
	coreAt         time.Time
}

type agentReplyTimingSnapshot struct {
	Clock           string             `json:"clock"`
	ObservedQuietMS int                `json:"observed_quiet_ms"`
	ReplyCount      uint64             `json:"reply_count"`
	MaxCoreWaitMS   float64            `json:"max_speech_to_core_audio_ms"`
	MaxDispatchMS   float64            `json:"max_core_audio_to_carrier_write_ms"`
	Recent          []agentReplyTiming `json:"recent,omitempty"`
}

type agentReplyTimingState struct {
	speech               bool
	lastSpeech           time.Time
	waitingCore          bool
	count                uint64
	maxCore, maxDispatch float64
	recent               [agentReplyHistoryLimit]agentReplyTiming
}

func (s *agentAudioSession) observeCaller(speech bool, at time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &s.timing
	if speech {
		t.speech, t.lastSpeech, t.waitingCore = true, at, false
	} else if t.speech && at.Sub(t.lastSpeech) >= agentReplyObservedQuiet {
		t.speech, t.waitingCore = false, true
	}
}

func (s *agentAudioSession) observeCoreReplyLocked(f realtimeBridgeControl, at time.Time) {
	t := &s.timing
	if f.Silence || f.ItemID == "" || !t.waitingCore {
		return
	}
	if s.waitingDispatch.Load() {
		old := &t.recent[(t.count-1)%agentReplyHistoryLimit]
		old.Outcome = "superseded_before_carrier_write"
	}
	if at.IsZero() {
		at = time.Now()
	}
	if at.Before(t.lastSpeech) {
		return
	}
	gap := float64(at.Sub(t.lastSpeech)) / float64(time.Millisecond)
	t.recent[t.count%agentReplyHistoryLimit] = agentReplyTiming{
		ItemID: f.ItemID, Generation: f.Generation,
		CallerSpeechAt: t.lastSpeech.UTC().Format(time.RFC3339Nano),
		CoreAudioAt:    at.UTC().Format(time.RFC3339Nano),
		CoreWaitMS:     gap, Outcome: "awaiting_carrier_write", coreAt: at,
	}
	t.count++
	t.maxCore = max(t.maxCore, gap)
	t.waitingCore = false
	s.waitingDispatch.Store(true)
}

func (s *agentAudioSession) observeSent(item string, generation int64, silent bool, at time.Time) {
	if s == nil || silent || !s.waitingDispatch.Load() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.waitingDispatch.Load() || s.timing.count == 0 {
		return
	}
	r := &s.timing.recent[(s.timing.count-1)%agentReplyHistoryLimit]
	if r.ItemID != item || r.Generation != generation {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	if at.Before(r.coreAt) {
		return
	}
	r.CarrierWriteAt = at.UTC().Format(time.RFC3339Nano)
	r.DispatchMS = float64(at.Sub(r.coreAt)) / float64(time.Millisecond)
	r.Outcome = "carrier_write_succeeded"
	s.timing.maxDispatch = max(s.timing.maxDispatch, r.DispatchMS)
	s.waitingDispatch.Store(false)
}

func (s *agentAudioSession) cancelReplyTimingLocked(item string, generation int64) {
	if !s.waitingDispatch.Load() || s.timing.count == 0 {
		return
	}
	r := &s.timing.recent[(s.timing.count-1)%agentReplyHistoryLimit]
	if matchesAgentPlayback(r.ItemID, r.Generation, realtimeBridgeControl{ItemID: item, Generation: generation}) {
		r.Outcome = "cancelled_before_carrier_write"
		s.waitingDispatch.Store(false)
	}
}

func (s *agentAudioSession) replyTimingSnapshotLocked() agentReplyTimingSnapshot {
	t := &s.timing
	out := agentReplyTimingSnapshot{Clock: "telephony_receipt", ObservedQuietMS: int(agentReplyObservedQuiet / time.Millisecond), ReplyCount: t.count, MaxCoreWaitMS: t.maxCore, MaxDispatchMS: t.maxDispatch}
	count := min(t.count, uint64(agentReplyHistoryLimit))
	if count != 0 {
		out.Recent = make([]agentReplyTiming, 0, count)
		for i := t.count - count; i < t.count; i++ {
			out.Recent = append(out.Recent, t.recent[i%agentReplyHistoryLimit])
		}
	}
	return out
}
