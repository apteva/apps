package main

import "time"

func addCarrierAudioSummary(s *audioDashboardSummary, r *carrierTelemetryRecord) {
	if r == nil {
		return
	}
	s.Telemetry = true
	if audioDashboardTime(r.At) > audioDashboardTime(s.ObservedAt) {
		s.ObservedAt = r.At
	}
	if s.Boundaries == nil {
		s.Boundaries = map[string]audioHealthStage{}
	}
	m := s.Metrics
	add := func(issue, stage string, bad bool) {
		if bad {
			if !containsString(s.Issues, issue) {
				s.Issues = append(s.Issues, issue)
			}
			if stage != "" && !containsString(s.Stages, stage) {
				s.Stages = append(s.Stages, stage)
			}
		}
	}
	carrier := s.Boundaries["carrier"]
	if carrier.State == "" {
		carrier.State = "unobserved"
	}
	internal := s.Boundaries["internal"]
	if internal.State == "" {
		internal.State = "unobserved"
	}
	if v := r.Carrier; v != nil {
		s.CarrierTransport = "websocket"
		if carrier.State != "audio_degraded" || v.State != "healthy" {
			carrier.State = v.State
		}
		m["carrier_max_gap_ms"] = max(m["carrier_max_gap_ms"], v.MaxGapMS)
		m["carrier_last_media_age_ms"] = v.LastMediaAgeMS
		m["carrier_decode_errors"] = float64(v.DecodeErrors)
		m["carrier_max_delivery_batch_ms"] = v.MaxBatchMS
		if v.State == "audio_degraded" {
			carrier.Reason = "reception_stalled"
		}
		if v.RecoveryActive {
			carrier.State = "audio_degraded"
			carrier.Reason = "media_recovering"
		}
		add("carrier_stall", "carrier_to_telephony", v.State == "audio_degraded")
		add("carrier_recovery", "carrier_to_telephony", v.RecoveryActive)
		add("carrier_error", "carrier_to_telephony", v.DecodeErrors > 0)
	}
	if v := r.SIP; v != nil {
		s.CarrierTransport = "sip"
		carrier.State = v.State
		if v.ReceiveExpectation == "intentional_hold" {
			carrier.Reason = "intentional_hold"
		}
		m["sip_packets_received"] = float64(v.RTP.Packets)
		m["sip_bytes_received"] = float64(v.RTP.Bytes)
		m["sip_last_packet_age_ms"] = v.RTP.LastPacketAgeMS
		m["carrier_max_gap_ms"] = max(m["carrier_max_gap_ms"], v.RTP.MaxGapMS)
		m["sip_sequence_loss"] = float64(v.RTP.SequenceLoss)
		m["sip_sequence_gap_packets"] = float64(v.RTP.SequenceGaps)
		m["sip_duplicates"] = float64(v.RTP.Duplicates)
		m["sip_out_of_order"] = float64(v.RTP.OutOfOrder)
		m["sip_jitter_ms"] = v.RTP.JitterMS
		m["sip_rejected_source_packets"] = float64(v.RTP.RejectedSource)
		m["sip_decode_errors"] = float64(v.RTP.DecodeErrors)
		m["sip_srtp_authentication_failures"] = float64(v.RTP.AuthErrors)
		m["sip_srtp_decryption_failures"] = float64(v.RTP.DecryptErrors)
		m["sip_srtp_replay_failures"] = float64(v.RTP.ReplayErrors)
		m["sip_packets_sent"] = float64(v.RTP.Sent)
		m["sip_send_errors"] = float64(v.RTP.SendErrors)
		m["sip_max_send_duration_ms"] = v.RTP.MaxSendMS
		m["sip_jitter_queue_ms"] = v.Jitter.QueueMS
		m["sip_jitter_oldest_age_ms"] = v.Jitter.OldestAgeMS
		m["sip_jitter_discarded_ms"] = v.Jitter.DroppedMS + v.Jitter.LateDroppedMS
		m["sip_missing_audio_filled_ms"] = v.Jitter.MissingFilledMS
		m["sip_overflow_discarded_ms"] = v.Jitter.OverflowDroppedMS
		m["sip_expired_discarded_ms"] = v.Jitter.ExpiredDroppedMS
		m["sip_source_reset_discarded_ms"] = v.Jitter.SourceResetDroppedMS
		m["sip_late_discarded_ms"] = v.Jitter.LateDroppedMS
		m["sip_concealed_ms"] = v.Jitter.ConcealedMS
		m["sip_silence_filled_ms"] = v.Jitter.SilenceFilledMS
		m["sip_playback_target_ms"] = float64(v.Jitter.TargetMS)
		m["sip_playback_capacity_ms"] = float64(v.Jitter.CapacityMS)
		m["sip_clock_drift_ppm"] = v.Jitter.ClockDriftPPM
		m["sip_clock_estimator_updates"] = float64(v.Jitter.ClockEstimatorUpdates)
		m["sip_clock_rejected_fits"] = float64(v.Jitter.ClockRejectedFits)
		m["sip_clock_observation_windows"] = float64(v.Jitter.ClockObservationCount)
		m["sip_clock_phase_adjusted_ms"] = v.Jitter.ClockPhaseAdjustedMS
		m["sip_max_relative_delivery_age_ms"] = v.Jitter.MaxDeliveryAgeMS
		m["sip_refresh_attempts"] = float64(v.Signal.RefreshAttempts)
		m["sip_refresh_failures"] = float64(v.Signal.RefreshFailures)
		m["carrier_send_dropped_ms"] = max(m["carrier_send_dropped_ms"], float64(v.Pacer.DroppedMS))
		m["carrier_send_max_queue_ms"] = max(m["carrier_send_max_queue_ms"], float64(v.Pacer.MaxQueueMS))
		add("sequence_gaps", "carrier_to_telephony", v.RTP.SequenceLoss > 0)
		add("carrier_error", "carrier_to_telephony", v.RTP.AuthErrors+v.RTP.DecryptErrors+v.RTP.ReadErrors+v.RTP.ReplayErrors+v.RTP.SendErrors+v.Signal.RefreshFailures > 0)
		add("processing_error", "internal_processing", v.RTP.DecodeErrors > 0)
		add("dropped_audio", "internal_processing", v.Jitter.DroppedMS+v.Jitter.LateDroppedMS > 0 || v.Pacer.DroppedMS > 0)
		// Filled audio describes local playback substitution, not WebRTC packet loss.
		add("concealed_audio", "internal_processing", v.Jitter.MissingFilledMS > 0)
		internal.State = "healthy"
		ageLimit := v.Jitter.AgeLimitMS
		if ageLimit == 0 {
			ageLimit = 120
		} // Older retained reports.
		if v.ForwardAvailable && v.Forward.QueuedMS > 320 || v.Jitter.Available && v.Jitter.OldestAgeMS > float64(ageLimit) {
			internal.State = "audio_degraded"
			internal.Reason = "queue_delay"
		}
	}
	// Historical incidents remain visible without claiming a stale reading is live.
	if at, e := time.Parse(time.RFC3339Nano, r.At); e == nil && time.Since(at) > 20*time.Second && carrier.State != "ended" {
		carrier = audioHealthStage{State: "stale"}
		internal = audioHealthStage{State: "stale"}
	}
	s.Boundaries["carrier"] = carrier
	s.Boundaries["internal"] = internal
	if s.Health.State == "" {
		s.Health.State = "healthy"
	}
	if s.Boundaries["browser"].State == "stale" && s.Health.State == "audio_degraded" {
		s.Health.State = "stale"
	}
	if carrier.State == "audio_degraded" || internal.State == "audio_degraded" || s.Boundaries["browser"].State == "audio_degraded" {
		s.Health.State = "audio_degraded"
	}

}
func audioBoundarySummary(b browserAudioDiagnostics) map[string]audioHealthStage {
	out := map[string]audioHealthStage{"carrier": {State: "unobserved"}, "browser": {State: "unobserved"}, "internal": {State: "unobserved"}}
	if b.ReceivedAt != "" {
		out["browser"] = audioHealthStage{State: "healthy"}
		if b.ConnectionState != "connected" || b.AudioContextState != "running" {
			out["browser"] = audioHealthStage{State: "inactive"}
		}
	}
	if b.Server != nil {
		out["internal"] = audioHealthStage{State: "healthy"}
		for _, stage := range []string{"telephony_to_browser", "browser_to_telephony"} {
			if h, ok := b.Server.Health.Stages[stage]; ok && h.State == "audio_degraded" {
				out["browser"] = h
			}
		}
		if b.Server.Reception.Stalled {
			out["carrier"] = audioHealthStage{State: "audio_degraded", Reason: "reception_stalled"}
		}
		for _, stage := range b.Server.Stages {
			if stage.MaxWorkMS > 320 {
				out["internal"] = audioHealthStage{State: "audio_degraded", Reason: "processing_delay"}
			}
		}
	}
	if at, e := time.Parse(time.RFC3339Nano, b.ReceivedAt); e == nil && time.Since(at) > 20*time.Second {
		for key, value := range out {
			if value.State != "unobserved" {
				out[key] = audioHealthStage{State: "stale"}
			}
		}
	}
	return out
}
