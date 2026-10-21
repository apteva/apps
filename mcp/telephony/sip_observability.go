package main

import (
	"errors"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
	"math"
	"net"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Atomic counters and a best-effort bounded timing window. Sampling cannot hold
// an RTP reader/writer: contention only skips diagnostic sequence calculations.
type sipRTPObservations struct {
	receiveEvidence      sipReceiveWindow
	firstReceived        atomic.Int64
	firstSent            atomic.Int64
	earlyKernelDrops     atomic.Int64
	activationDropped    atomic.Int64
	kernelDrops          atomic.Int64
	kernelDropsAvailable atomic.Bool
	earlyPackets         atomic.Int64
	earlyBytes           atomic.Int64
	lastError            atomic.Int64
	lastDelayedDelivery  atomic.Int64
	packets              atomic.Int64
	bytes                atomic.Int64
	last                 atomic.Int64
	maxGap               atomic.Int64
	rejectedSource       atomic.Int64
	decodeErrors         atomic.Int64
	authErrors           atomic.Int64
	replayErrors         atomic.Int64
	decryptErrors        atomic.Int64
	readErrors           atomic.Int64
	sent                 atomic.Int64
	sentBytes            atomic.Int64
	sendErrors           atomic.Int64
	lastSent             atomic.Int64
	maxSend              atomic.Int64
	maxPacingGap         atomic.Int64
	skipped              atomic.Int64
	duplicates           atomic.Int64
	outOfOrder           atomic.Int64
	loss                 atomic.Int64
	sequenceGaps         atomic.Int64
	jitterNS             atomic.Int64
	deliveryExcessNS     atomic.Int64
	timingMu             sync.Mutex
	observedSkipped      int64
	stream               uint32
	highest              int64
	first                int64
	seen                 [256]int64
	have                 bool
	lastArrival          time.Time
	lastTimestamp        uint32
	jitter               float64

	kernelTimestampAvailable atomic.Bool
	kernelTimestampPackets   atomic.Int64
	kernelTimestampInvalid   atomic.Int64
	lastSocketWait           atomic.Int64
	maxSocketWait            atomic.Int64
}

func atomicMax(dst *atomic.Int64, n int64) {
	for {
		old := dst.Load()
		if n <= old || dst.CompareAndSwap(old, n) {
			return
		}
	}
}
func (o *sipRTPObservations) received(packet *rtp.Packet, size int, now time.Time, audio bool) {
	o.firstReceived.CompareAndSwap(0, now.UnixNano())
	o.packets.Add(1)
	o.bytes.Add(int64(size))
	previous := o.last.Swap(now.UnixNano())
	if previous > 0 {
		atomicMax(&o.maxGap, now.UnixNano()-previous)
	}
	if !o.timingMu.TryLock() {
		o.skipped.Add(1)
		return
	}
	defer o.timingMu.Unlock()
	// Packets observed during contention are not carrier loss. Restart this
	// best-effort timing window and retain explicit skipped-count confidence.
	if skipped := o.skipped.Load(); skipped != o.observedSkipped {
		o.have = false
		o.observedSkipped = skipped
	}
	if !o.have || o.stream != packet.SSRC {
		o.have = true
		o.stream = packet.SSRC
		o.highest = int64(packet.SequenceNumber)
		o.first = o.highest
		o.seen = [256]int64{}
		for i := range o.seen {
			o.seen[i] = -1
		}
		o.lastArrival = time.Time{}
		o.jitter = 0
	}
	extended := o.highest + int64(int16(packet.SequenceNumber-uint16(o.highest)))
	slot := int(packet.SequenceNumber) % len(o.seen)
	if o.seen[slot] == extended {
		o.duplicates.Add(1)
		return
	}
	o.seen[slot] = extended
	if extended > o.highest {
		if missing := extended - o.highest - 1; missing > 0 {
			o.loss.Add(missing)
			o.sequenceGaps.Add(missing)
		}
		o.highest = extended
	} else if extended < o.highest {
		o.outOfOrder.Add(1)
		if extended >= o.first && o.highest-extended < 256 && o.loss.Load() > 0 {
			o.loss.Add(-1)
		}
	}
	if audio && extended >= o.highest {
		if !o.lastArrival.IsZero() {
			gap := now.Sub(o.lastArrival)
			source := time.Duration(int64(int32(packet.Timestamp-o.lastTimestamp))) * time.Second / 8000 // RTP clock is 8 kHz, including G.722.
			variation := math.Abs(float64(gap - source))
			o.jitter += (variation - o.jitter) / 16
			o.jitterNS.Store(int64(o.jitter))
			// Positive excess is evidence of delayed delivery; an ordinary DTX pause
			// with the matching timestamp advance is not labelled a transport stall.
			if source >= 0 {
				atomicMax(&o.deliveryExcessNS, int64(gap-source))
				if gap-source > 320*time.Millisecond {
					o.lastDelayedDelivery.Store(now.UnixNano())
				}
			}
		}
		o.lastArrival = now
		o.lastTimestamp = packet.Timestamp
	}
}

func (o *sipRTPObservations) sentPacket(size int, now time.Time) {
	o.firstSent.CompareAndSwap(0, now.UnixNano())
	o.sent.Add(1)
	o.sentBytes.Add(int64(size))
	previous := o.lastSent.Swap(now.UnixNano())
	if previous > 0 {
		atomicMax(&o.maxPacingGap, now.UnixNano()-previous)
	}
}

type sipRTPSnapshot struct {
	ReceiveEvidence      []sipReceiveEvidence `json:"receive_evidence,omitempty"`
	FirstPacketAt        string               `json:"first_packet_at,omitempty"`
	FirstSentAt          string               `json:"first_sent_at,omitempty"`
	PreBridgeSocketDrops int64                `json:"pre_bridge_socket_receive_drops"`
	ActivationDropped    int64                `json:"activation_dropped_packets"`
	KernelDrops          int64                `json:"socket_receive_drops"`
	KernelDropsAvailable bool                 `json:"socket_receive_drops_available"`
	EarlyPackets         int64                `json:"pre_bridge_drained_packets"`
	EarlyBytes           int64                `json:"pre_bridge_drained_bytes"`
	Packets              int64                `json:"packets_received"`
	Bytes                int64                `json:"bytes_received"`
	LastPacketAt         string               `json:"last_packet_at,omitempty"`
	LastPacketAgeMS      float64              `json:"last_packet_age_ms"`
	MaxGapMS             float64              `json:"max_gap_ms"`
	SequenceLoss         int64                `json:"sequence_loss"`
	SequenceGaps         int64                `json:"sequence_gap_packets"`
	Duplicates           int64                `json:"duplicates"`
	OutOfOrder           int64                `json:"out_of_order"`
	JitterMS             float64              `json:"jitter_ms"`
	MaxDeliveryExcessMS  float64              `json:"max_delivery_excess_ms"`
	RejectedSource       int64                `json:"rejected_source_packets"`
	DecodeErrors         int64                `json:"decode_errors"`
	AuthErrors           int64                `json:"srtp_authentication_failures"`
	ReplayErrors         int64                `json:"srtp_replay_failures"`
	DecryptErrors        int64                `json:"srtp_decryption_failures"`
	ReadErrors           int64                `json:"read_errors"`
	Sent                 int64                `json:"packets_sent"`
	SentBytes            int64                `json:"bytes_sent"`
	SendErrors           int64                `json:"send_errors"`
	LastSentAt           string               `json:"last_sent_at,omitempty"`
	MaxSendMS            float64              `json:"max_send_duration_ms"`
	MaxPacingGapMS       float64              `json:"max_send_gap_ms"`
	Skipped              int64                `json:"timing_observations_skipped"`

	KernelTimestampAvailable bool    `json:"kernel_arrival_timestamps_available"`
	KernelTimestampPackets   int64   `json:"kernel_timestamped_packets"`
	KernelTimestampInvalid   int64   `json:"kernel_timestamp_invalid"`
	LastSocketWaitMS         float64 `json:"last_socket_read_wait_ms"`
	MaxSocketWaitMS          float64 `json:"max_socket_read_wait_ms"`
}

func (o *sipRTPObservations) snapshot(now time.Time) sipRTPSnapshot {
	s := sipRTPSnapshot{PreBridgeSocketDrops: o.earlyKernelDrops.Load(), ActivationDropped: o.activationDropped.Load(), KernelDrops: o.kernelDrops.Load(), KernelDropsAvailable: o.kernelDropsAvailable.Load(), EarlyPackets: o.earlyPackets.Load(), EarlyBytes: o.earlyBytes.Load(), Packets: o.packets.Load(), Bytes: o.bytes.Load(), MaxGapMS: float64(o.maxGap.Load()) / 1e6, SequenceLoss: o.loss.Load(), SequenceGaps: o.sequenceGaps.Load(), Duplicates: o.duplicates.Load(), OutOfOrder: o.outOfOrder.Load(), JitterMS: float64(o.jitterNS.Load()) / 1e6, MaxDeliveryExcessMS: float64(o.deliveryExcessNS.Load()) / 1e6, RejectedSource: o.rejectedSource.Load(), DecodeErrors: o.decodeErrors.Load(), AuthErrors: o.authErrors.Load(), ReplayErrors: o.replayErrors.Load(), DecryptErrors: o.decryptErrors.Load(), ReadErrors: o.readErrors.Load(), Sent: o.sent.Load(), SentBytes: o.sentBytes.Load(), SendErrors: o.sendErrors.Load(), MaxSendMS: float64(o.maxSend.Load()) / 1e6, MaxPacingGapMS: float64(o.maxPacingGap.Load()) / 1e6, Skipped: o.skipped.Load()}
	s.ReceiveEvidence = o.receiveEvidence.snapshot()
	s.KernelTimestampAvailable = o.kernelTimestampAvailable.Load()
	s.KernelTimestampPackets, s.KernelTimestampInvalid = o.kernelTimestampPackets.Load(), o.kernelTimestampInvalid.Load()
	s.LastSocketWaitMS, s.MaxSocketWaitMS = float64(o.lastSocketWait.Load())/1e6, float64(o.maxSocketWait.Load())/1e6
	if n := o.firstReceived.Load(); n > 0 {
		s.FirstPacketAt = ringTime(time.Unix(0, n))
	}
	if n := o.firstSent.Load(); n > 0 {
		s.FirstSentAt = ringTime(time.Unix(0, n))
	}
	if n := o.last.Load(); n > 0 {
		s.LastPacketAt = ringTime(time.Unix(0, n))
		s.LastPacketAgeMS = max(0, float64(now.UnixNano()-n)/1e6)
	}
	if n := o.lastSent.Load(); n > 0 {
		s.LastSentAt = ringTime(time.Unix(0, n))
	}
	return s
}

type sipJitterSnapshot struct {
	Resynchronizations     int              `json:"playout_resynchronizations"`
	RecoveryDiscardedMS    float64          `json:"recovery_discarded_ms"`
	LastRecoveryOffsetMS   float64          `json:"last_recovery_offset_ms"`
	MaxIngressProcessingMS float64          `json:"max_ingress_processing_ms"`
	MaxIngressLockMS       float64          `json:"max_ingress_lock_wait_ms"`
	SourceResetDroppedMS   float64          `json:"source_reset_discarded_ms"`
	HoldSkippedMS          float64          `json:"intentional_hold_skipped_ms"`
	MaxDeliveryAgeMS       float64          `json:"max_relative_delivery_age_ms"`
	SourceClockConfidence  string           `json:"source_clock_confidence"`
	TargetMS               int              `json:"target_ms"`
	EffectiveTargetMS      int              `json:"effective_target_ms"`
	CapacityMS             int              `json:"capacity_ms"`
	AgeLimitMS             int              `json:"age_limit_ms"`
	OverflowDroppedMS      float64          `json:"overflow_discarded_ms"`
	ExpiredDroppedMS       float64          `json:"expired_discarded_ms"`
	ConcealedMS            float64          `json:"concealed_ms"`
	SilenceFilledMS        float64          `json:"silence_filled_ms"`
	TailPaddingMS          float64          `json:"tail_padding_ms"`
	TailPaddedFrames       int64            `json:"tail_padded_frames"`
	ClockDriftPPM          float64          `json:"clock_drift_ppm"`
	ClockEstimatorUpdates  int              `json:"clock_estimator_updates"`
	ClockRejectedFits      int              `json:"clock_rejected_fits"`
	ClockObservationCount  int              `json:"clock_observation_windows"`
	ClockPhaseAdjustedMS   float64          `json:"clock_phase_adjusted_ms"`
	ClockPhaseRejected     int              `json:"clock_phase_withheld"`
	ClockRateRejected      int              `json:"clock_rate_rejected"`
	ClockRecentRecoveries  int              `json:"clock_recent_rate_recoveries"`
	ClockFits              []sipClockFit    `json:"clock_fits,omitempty"`
	MaxConsumerIntervalMS  float64          `json:"max_consumer_interval_ms"`
	AdjustedSamples        int64            `json:"reserve_clock_adjusted_samples"`
	ClockResets            int              `json:"clock_resets"`
	DTXResumes             int              `json:"dtx_resumes"`
	FreshDeliveryResumes   int              `json:"fresh_delivery_rebuffers"`
	Events                 []sipJitterEvent `json:"events,omitempty"`
	Available              bool             `json:"available"`
	QueueMS                float64          `json:"queue_ms"`
	OldestAgeMS            float64          `json:"oldest_age_ms"`
	MaxAgeMS               int              `json:"max_age_ms"`
	DroppedMS              float64          `json:"discarded_ms"`
	LateDroppedMS          float64          `json:"late_discarded_ms"`
	MissingFilledMS        float64          `json:"missing_audio_filled_ms"`
}
type sipSignalSnapshot struct {
	Negotiation      *sipCodecEvidence `json:"codec_negotiation,omitempty"`
	BYEStatus        string            `json:"bye_status,omitempty"`
	BYEFailures      int64             `json:"bye_failures"`
	BYEError         string            `json:"bye_error,omitempty"`
	DialogID         string            `json:"dialog_id"`
	State            string            `json:"state"`
	InviteAt         string            `json:"invite_at,omitempty"`
	AnswerAt         string            `json:"answer_at,omitempty"`
	ACKAt            string            `json:"ack_at,omitempty"`
	RefreshAttempts  int64             `json:"refresh_attempts"`
	RefreshSuccesses int64             `json:"refresh_successes"`
	RefreshFailures  int64             `json:"refresh_failures"`
	LastRefreshAt    string            `json:"last_refresh_at,omitempty"`
	BYEInitiator     string            `json:"bye_initiator,omitempty"`
	EndReason        string            `json:"end_reason,omitempty"`
	TimeoutCause     string            `json:"timeout_cause,omitempty"`
	Codec            string            `json:"codec,omitempty"`
	Encryption       string            `json:"encryption,omitempty"`
}
type sipLiveSnapshot struct {
	TransportState     string                 `json:"transport_state"`
	ProcessingState    string                 `json:"processing_state"`
	At                 string                 `json:"at"`
	State              string                 `json:"state"`
	ReceiveExpectation string                 `json:"receive_expectation"`
	Signal             sipSignalSnapshot      `json:"signaling"`
	RTP                sipRTPSnapshot         `json:"rtp"`
	Jitter             sipJitterSnapshot      `json:"jitter_buffer"`
	Pacer              livePacerSnapshot      `json:"send_pacer"`
	PacerAvailable     bool                   `json:"send_pacer_available"`
	Forward            liveAudioQueueSnapshot `json:"core_forward"`
	ForwardAvailable   bool                   `json:"core_forward_available"`
}
type sipLiveDiagnostics struct {
	app          *App
	row          callRow
	id           string
	mu           sync.Mutex
	signal       sipSignalSnapshot
	media        atomic.Pointer[sipRTPMedia]
	ended        atomic.Bool
	lastIncident signatureSIPIncident
	lastJitter   sipJitterSnapshot
	lastPacer    livePacerSnapshot
	lastForward  liveAudioQueueSnapshot
}
type signatureSIPIncident struct{ loss, errors, dropped, delayBucket int64 }

func (a *App) newSIPDiagnostics(row *callRow, dialog string) *sipLiveDiagnostics {
	d := &sipLiveDiagnostics{app: a, row: *row, id: newCallID(), signal: sipSignalSnapshot{DialogID: boundedContextValue(dialog), State: "inviting"}}
	a.carrierTelemetry.register(d)
	d.event("sip_invite", "signaling", "", 0)
	return d
}
func (d *sipLiveDiagnostics) event(kind, leg, detail string, status int) {
	if d == nil {
		return
	}
	now := ringTime(time.Now())
	d.mu.Lock()
	switch kind {
	case "sip_invite":
		d.signal.InviteAt = now
	case "sip_answer":
		d.signal.AnswerAt = now
		if d.signal.ACKAt == "" {
			d.signal.State = "awaiting_ack"
		}
	case "sip_ack":
		d.signal.ACKAt = now
		d.signal.State = "confirmed"
	case "sip_refresh_attempt":
		d.signal.RefreshAttempts++
		d.signal.LastRefreshAt = now
	case "sip_refresh_result":
		if status >= 200 && status < 300 {
			d.signal.RefreshSuccesses++
		} else if status != 491 {
			d.signal.RefreshFailures++
		}
	case "sip_media_timeout":
		d.signal.TimeoutCause = limitDiagnosticText(detail, 100)
	case "sip_bye_requested":
		d.signal.BYEStatus = "pending"
	case "sip_bye_result":
		d.signal.BYEStatus = "confirmed"
		if status < 200 || status >= 300 {
			d.signal.BYEStatus = "unconfirmed"
			d.signal.BYEFailures++
			d.signal.BYEError = sanitizeCarrierDetail(&d.row, detail)
		}
	case "sip_bye_crossed":
		d.signal.BYEStatus = "peer_ended"
		d.signal.BYEInitiator = "carrier"
	case "sip_bye_received":
		d.signal.BYEStatus = "peer_ended"
		d.signal.BYEInitiator = "carrier"
		d.signal.EndReason = sanitizeCarrierDetail(&d.row, detail)
	case "sip_end":
		d.signal.State = "ended"
		d.signal.BYEInitiator = leg
		if d.signal.EndReason == "" {
			d.signal.EndReason = sanitizeCarrierDetail(&d.row, detail)
		}
		if strings.Contains(strings.ToLower(detail), "timed out") || strings.Contains(strings.ToLower(detail), "expired") || strings.Contains(strings.ToLower(detail), "timeout") {
			d.signal.TimeoutCause = limitDiagnosticText(detail, 100)
		}
	}
	d.mu.Unlock()
	e := carrierBridgeEvidence{AfterLocalCancel: d.ended.Load() && kind != "sip_end", Kind: kind, Leg: leg, Detail: sanitizeCarrierDetail(&d.row, detail), At: now, HTTPStatus: status, BridgeGeneration: d.id}
	d.app.carrierTelemetry.enqueue(carrierTelemetryRecord{InstallID: d.app.installID, ProjectID: d.row.ProjectID, CallID: d.row.ID, Kind: "event", At: now, BridgeGeneration: d.id, Event: &e})
	if kind == "sip_bye_result" || kind == "sip_bye_crossed" {
		d.app.carrierTelemetry.enqueue(d.sample(time.Now()))
	}
}
func (d *sipLiveDiagnostics) attach(media *sipRTPMedia, dialog string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if dialog != "" {
		d.signal.DialogID = boundedContextValue(dialog)
	}
	d.signal.Codec = media.offer.Codec
	d.signal.Encryption = "rtp"
	if media.offer.Secure {
		d.signal.Encryption = "srtp"
	}
	d.mu.Unlock()
	media.diagnostics.Store(d)
	d.media.Store(media)
}
func (d *sipLiveDiagnostics) end(leg string, cause error) {
	if d == nil || !d.ended.CompareAndSwap(false, true) {
		return
	}
	detail := "call ended"
	if cause != nil {
		detail = transportEvidenceError(cause)
	}
	d.event("sip_end", leg, detail, 0)
	d.app.carrierTelemetry.unregister(d)
}
func (d *sipLiveDiagnostics) snapshot(now time.Time) sipLiveSnapshot {
	d.mu.Lock()
	signal := d.signal
	d.mu.Unlock()
	s := sipLiveSnapshot{At: ringTime(now), State: "healthy", Signal: signal, ReceiveExpectation: "silence_or_dtx_possible"}
	if d.ended.Load() {
		s.State = "ended"
	}
	m := d.media.Load()
	if m == nil {
		if !d.ended.Load() {
			s.State = "unobserved"
		}
		return s
	}
	s.RTP = m.observations.snapshot(now)
	if mode := m.remoteMode.Load(); mode == 2 || mode == 3 || m.hold.Load() != nil {
		s.ReceiveExpectation = "intentional_hold"
		if !d.ended.Load() {
			s.State = "inactive"
		}
	}
	lastProblem := max(m.observations.lastError.Load(), m.observations.lastDelayedDelivery.Load())
	if !d.ended.Load() && s.ReceiveExpectation != "intentional_hold" && lastProblem > 0 && now.Sub(time.Unix(0, lastProblem)) < 5*time.Second {
		s.State = "audio_degraded"
	}
	s.TransportState = s.State
	s.ProcessingState = "healthy"
	if j := m.jitterTelemetry.Load(); j != nil && !d.ended.Load() && s.ReceiveExpectation != "intentional_hold" {
		if at := j.lastDiscardNS.Load(); at > 0 && now.Sub(time.Unix(0, at)) < 5*time.Second {
			s.ProcessingState = "audio_degraded"
			s.State = "audio_degraded"
		}
	}
	// Arrival age alone cannot establish an RTP transport failure: DTX may be
	// silent indefinitely. Confirmed read/write/security failures remain distinct.
	if j := m.jitterTelemetry.Load(); j != nil && j.mu.TryLock() {
		s.Jitter = sipJitterSnapshot{Available: true, QueueMS: float64(int64(j.buffered)+j.pendingPCMSamples.Load()) / 8, MaxAgeMS: j.maxAgeMS, DroppedMS: float64(j.droppedSamples) / 8, LateDroppedMS: float64(j.lateDroppedSamples) / 8, MissingFilledMS: float64(j.missingFilledSamples.Load()) / 8}
		s.Jitter.Resynchronizations = j.recovery.recoveries
		s.Jitter.RecoveryDiscardedMS = float64(j.recoveryDiscardedSamples) / 8
		s.Jitter.LastRecoveryOffsetMS = j.recovery.lastOffsetMS
		s.Jitter.MaxIngressProcessingMS = j.maxIngressProcessingMS
		s.Jitter.MaxIngressLockMS = j.maxIngressLockMS
		s.Jitter.SourceResetDroppedMS = float64(j.resetDroppedSamples) / 8
		s.Jitter.HoldSkippedMS = float64(j.holdSkippedSamples) / 8
		s.Jitter.MaxDeliveryAgeMS = j.maxDeliveryAgeMS
		s.Jitter.SourceClockConfidence = "relative_rtp_baseline"
		s.Jitter.TargetMS, s.Jitter.CapacityMS, s.Jitter.AgeLimitMS = j.targetMS, j.policy.CapacityMS, j.policy.MaxAgeMS
		s.Jitter.EffectiveTargetMS = j.playbackTargetMS()
		s.Jitter.OverflowDroppedMS, s.Jitter.ExpiredDroppedMS = float64(j.overflowSamples)/8, float64(j.expiredSamples)/8
		s.Jitter.ConcealedMS, s.Jitter.SilenceFilledMS = float64(j.concealedSamples.Load())/8, float64(j.silenceSamples.Load())/8
		s.Jitter.TailPaddingMS, s.Jitter.TailPaddedFrames = float64(j.tailPaddingNS)/1e6, j.tailPaddedFrames
		s.Jitter.ClockDriftPPM, s.Jitter.AdjustedSamples = j.driftPPM, j.driftAdjustedSamples.Load()
		s.Jitter.ClockEstimatorUpdates, s.Jitter.ClockRejectedFits = j.clock.updates, j.clock.rejected
		s.Jitter.ClockObservationCount, s.Jitter.ClockPhaseAdjustedMS = j.clock.count, j.clock.phaseAdjustedMS
		s.Jitter.ClockPhaseRejected, s.Jitter.ClockRateRejected = j.clock.phaseRejected, j.clock.rateRejected
		s.Jitter.ClockRecentRecoveries = j.clock.recentRecoveries
		s.Jitter.MaxConsumerIntervalMS = float64(j.maxConsumerIntervalNS.Load()) / 1e6
		fits, fitHead, fitCount := j.clock.fits, j.clock.fitHead, j.clock.fitCount
		s.Jitter.ClockResets, s.Jitter.DTXResumes = j.clockResets, j.dtxResumes
		s.Jitter.FreshDeliveryResumes = j.freshResumes
		events := j.events
		eventHead, eventCount := j.eventHead, j.eventCount

		for seq, at := range j.arrivals {
			s.Jitter.OldestAgeMS = max(s.Jitter.OldestAgeMS, float64(max(now.Sub(at), now.Sub(j.sourceTime(j.timestamps[seq]))))/float64(time.Millisecond))
		}
		j.mu.Unlock()
		// Allocate/format the diagnostic history only after releasing the reader's lock.
		if fitCount > 0 {
			s.Jitter.ClockFits = make([]sipClockFit, fitCount)
		}
		for i := 0; i < fitCount; i++ {
			s.Jitter.ClockFits[i] = fits[(fitHead-fitCount+i+len(fits))%len(fits)]
		}
		if eventCount > 0 {
			s.Jitter.Events = make([]sipJitterEvent, 0, eventCount)
		}
		for i := 0; i < eventCount; i++ {
			s.Jitter.Events = append(s.Jitter.Events, events[(eventHead-eventCount+i+64)%64])
		}

	}
	if p := m.pacer.Load(); p != nil && p.diagnostics.mu.TryLock() {
		s.PacerAvailable = true
		s.Pacer = p.diagnostics.value
		s.Pacer.DropTotalsMS = make(map[string]int64, len(p.diagnostics.value.DropTotalsMS))
		for k, v := range p.diagnostics.value.DropTotalsMS {
			s.Pacer.DropTotalsMS[k] = v
		}
		p.diagnostics.mu.Unlock()
	}
	if w := m.coreForwardTelemetry.Load(); w != nil && w.audioMu.TryLock() {
		s.ForwardAvailable = true
		s.Forward = w.audioStats
		s.Forward.DropEvents = nil // Transport events have their own bounded journal.
		s.Forward.Transport = w.transportStats
		s.Forward.Transport.Events = nil
		s.Forward.QueuedMS = w.audioBytes * 1000 / 48000
		w.audioMu.Unlock()
	}
	// Retain cumulative counters when a best-effort snapshot is unavailable.
	// Availability remains explicit; stale queue gauges are not a live diagnosis.
	d.mu.Lock()
	if s.Jitter.Available {
		d.lastJitter = s.Jitter
	} else {
		s.Jitter = d.lastJitter
		s.Jitter.Available = false
	}
	if s.PacerAvailable {
		d.lastPacer = s.Pacer
	} else {
		s.Pacer = d.lastPacer
	}
	if s.ForwardAvailable {
		d.lastForward = s.Forward
	} else {
		s.Forward = d.lastForward
	}
	d.mu.Unlock()
	return s
}
func (d *sipLiveDiagnostics) sample(now time.Time) carrierTelemetryRecord {
	s := d.snapshot(now)
	// Keep before/after context on increases, separately from the rolling samples.
	delayBucket := int64(0)
	if s.RTP.MaxDeliveryExcessMS > 320 {
		delayBucket = int64(s.RTP.MaxDeliveryExcessMS) / 100
	}
	signature := signatureSIPIncident{s.RTP.SequenceGaps, s.RTP.AuthErrors + s.RTP.DecryptErrors + s.RTP.ReplayErrors + s.RTP.DecodeErrors + s.RTP.RejectedSource + s.RTP.ReadErrors + s.RTP.SendErrors, int64(s.Jitter.DroppedMS+s.Jitter.LateDroppedMS+s.Jitter.MissingFilledMS) + s.Pacer.DroppedMS, delayBucket}
	d.mu.Lock()
	incident := signature != d.lastIncident
	d.lastIncident = signature
	d.mu.Unlock()
	// Persist at most eight recent observations, avoiding repeated whole-ring
	// snapshots. The in-memory ring and archived incident history stay bounded.
	recent := s.RTP.ReceiveEvidence[:0]
	for _, e := range s.RTP.ReceiveEvidence {
		if now.Sub(e.Handler) <= 2*time.Second {
			recent = append(recent, e)
		}
	}
	if len(recent) > 8 {
		recent = recent[len(recent)-8:]
	}
	s.RTP.ReceiveEvidence = recent
	r := carrierTelemetryRecord{InstallID: d.app.installID, ProjectID: d.row.ProjectID, CallID: d.row.ID, Kind: "sample", At: ringTime(now), BridgeGeneration: d.id, SIP: &s}
	if incident {
		r.Kind = "incident"
	}
	return r
}
func (m *sipRTPMedia) observedReadError(err error) {
	var n net.Error
	if err == nil || errors.As(err, &n) && n.Timeout() {
		return
	}
	if !errors.Is(err, net.ErrClosed) && (m.diagnostics.Load() == nil || !m.diagnostics.Load().ended.Load()) {
		m.observations.readErrors.Add(1)
		m.observations.lastError.Store(time.Now().UnixNano())
	}
	if d := m.diagnostics.Load(); d != nil {
		d.event("transport_read_error", "carrier", transportEvidenceError(err), 0)
	}
}
func (m *sipRTPMedia) observedDecodeError(err error, secure bool) {
	m.observations.lastError.Store(time.Now().UnixNano())
	if secure {
		switch {
		case errors.Is(err, srtp.ErrFailedToVerifyAuthTag):
			m.observations.authErrors.Add(1)
		case strings.Contains(strings.ToLower(err.Error()), "duplicat"), strings.Contains(strings.ToLower(err.Error()), "replay"):
			m.observations.replayErrors.Add(1)
		default:
			m.observations.decryptErrors.Add(1)
		}
	} else {
		m.observations.decodeErrors.Add(1)
	}
}

var sipReasonCausePattern = regexp.MustCompile(`(?i)(SIP|Q\.850)\s*;\s*cause=(\d{1,3})`)

func safeSIPReason(req *sip.Request) string {
	h := req.GetHeader("Reason")
	if h == nil {
		return ""
	}
	matches := sipReasonCausePattern.FindStringSubmatch(h.Value())
	if len(matches) == 3 {
		return matches[1] + ";cause=" + matches[2]
	}
	return "unclassified_provider_reason"
}
