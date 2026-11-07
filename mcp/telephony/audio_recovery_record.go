package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// Evidence from the browser, posted once per recovery over HTTP because a
// recovery usually means signaling is unavailable. Every field is validated
// here: this is untrusted diagnostic input and grants nothing.
type audioRecoveryAttempt struct {
	AttemptID  string `json:"attempt_id,omitempty"`
	AtMS       int64  `json:"at_ms"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Reached    string `json:"reached,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
	Code       string `json:"code,omitempty"`
}
type audioRecoveryCheck struct {
	OK bool  `json:"ok"`
	MS int64 `json:"ms"`
}
type audioRecoveryChecks struct {
	Telephony *audioRecoveryCheck `json:"telephony,omitempty"`
	Page      *audioRecoveryCheck `json:"page,omitempty"`
}
type audioRecoveryRecord struct {
	RecoveryID      string                 `json:"recovery_id"`
	Trigger         string                 `json:"trigger"`
	StartedAt       string                 `json:"started_at,omitempty"`
	EndedAt         string                 `json:"ended_at,omitempty"`
	Outcome         string                 `json:"outcome,omitempty"`
	LostMS          int64                  `json:"lost_ms,omitempty"`
	ConnectionState string                 `json:"connection_state,omitempty"`
	ICEState        string                 `json:"ice_state,omitempty"`
	SignalingState  string                 `json:"signaling_state,omitempty"`
	LastPacketAgeMS *int64                 `json:"last_packet_age_ms,omitempty"`
	Online          *bool                  `json:"online,omitempty"`
	Visibility      string                 `json:"visibility,omitempty"`
	Microphone      string                 `json:"microphone,omitempty"`
	Checks          *audioRecoveryChecks   `json:"checks,omitempty"`
	Attempts        []audioRecoveryAttempt `json:"attempts,omitempty"`
	AttemptsTotal   int64                  `json:"attempts_total"`
}

const (
	audioRecoveryBodyLimit   = 8192
	audioRecoveryAttemptsMax = 5
	audioRecoveryPerCallMax  = 32
	audioRecoveryMSLimit     = int64(24 * time.Hour / time.Millisecond)
)

func boundedRecoveryMS(v int64) int64 { return max(0, min(v, audioRecoveryMSLimit)) }
func recoveryTimestamp(s string) string {
	if at, err := time.Parse(time.RFC3339Nano, s); err == nil && len(s) <= 40 {
		return at.UTC().Format(time.RFC3339Nano)
	}
	return ""
}
func recoveryChoice(s string, allowed ...string) string {
	for _, v := range allowed {
		if s == v {
			return s
		}
	}
	return ""
}

// Reports whether the record identifies a recovery. Unknown values are dropped,
// not stored, so nothing free-form reaches the diagnostic store.
func normalizeAudioRecoveryRecord(r audioRecoveryRecord) (audioRecoveryRecord, bool) {
	id := safeAudioDiagnosticID(r.RecoveryID)
	if !strings.HasPrefix(id, "recovery-") {
		return audioRecoveryRecord{}, false
	}
	out := audioRecoveryRecord{RecoveryID: id, StartedAt: recoveryTimestamp(r.StartedAt), EndedAt: recoveryTimestamp(r.EndedAt),
		Trigger:         recoveryChoice(r.Trigger, "signaling_closed", "signaling_unresponsive", "microphone_ended", "webrtc_failed", "webrtc_closed"),
		Outcome:         recoveryChoice(r.Outcome, "connected", "timed_out", "revoked", "cancelled", "handed_over", "handover_failed"),
		LostMS:          boundedRecoveryMS(r.LostMS),
		ConnectionState: recoveryChoice(r.ConnectionState, "new", "connecting", "connected", "disconnected", "failed", "closed"),
		ICEState:        recoveryChoice(r.ICEState, "new", "checking", "connected", "completed", "disconnected", "failed", "closed"),
		SignalingState:  recoveryChoice(r.SignalingState, "none", "connecting", "open", "closing", "closed"),
		Online:          r.Online,
		Visibility:      recoveryChoice(r.Visibility, "visible", "hidden", "prerender"),
		Microphone:      recoveryChoice(r.Microphone, "live", "ended"),
		AttemptsTotal:   boundedRecoveryMS(r.AttemptsTotal)}
	if out.Trigger == "" {
		out.Trigger = "other"
	}
	if r.LastPacketAgeMS != nil {
		age := boundedRecoveryMS(*r.LastPacketAgeMS)
		out.LastPacketAgeMS = &age
	}
	check := func(c *audioRecoveryCheck) *audioRecoveryCheck {
		if c == nil {
			return nil
		}
		return &audioRecoveryCheck{OK: c.OK, MS: boundedRecoveryMS(c.MS)}
	}
	if r.Checks != nil && (r.Checks.Telephony != nil || r.Checks.Page != nil) {
		out.Checks = &audioRecoveryChecks{Telephony: check(r.Checks.Telephony), Page: check(r.Checks.Page)}
	}
	for _, a := range r.Attempts[:min(len(r.Attempts), audioRecoveryAttemptsMax)] {
		out.Attempts = append(out.Attempts, audioRecoveryAttempt{AttemptID: safeAudioDiagnosticID(a.AttemptID), AtMS: boundedRecoveryMS(a.AtMS), DurationMS: boundedRecoveryMS(a.DurationMS),
			Reached: recoveryChoice(a.Reached, "requested", "session", "socket_open", "configured", "offer_sent", "answered", "media_connected", "ready"),
			Outcome: recoveryChoice(a.Outcome, "connected", "failed"), Code: safeAudioDiagnosticLabel(a.Code, 80)})
	}
	return out, true
}

// Accepted after the call has ended: a recovery can outlast the call, and that
// is when the evidence is most useful. Identity comes from the request.
func (a *App) handleMediaRecoveryRecord(w http.ResponseWriter, r *http.Request, project, id string) {
	var body struct {
		Record audioRecoveryRecord `json:"record"`
	}
	if r.Body == nil || json.NewDecoder(io.LimitReader(r.Body, audioRecoveryBodyLimit)).Decode(&body) != nil {
		writeJSONStatus(w, 400, map[string]any{"code": "invalid_recovery_record"})
		return
	}
	record, ok := normalizeAudioRecoveryRecord(body.Record)
	if !ok {
		writeJSONStatus(w, 400, map[string]any{"code": "invalid_recovery_record"})
		return
	}
	row, err := a.db().findCall(id)
	if err != nil {
		writeJSONStatus(w, 503, map[string]any{"code": "call_lookup_failed"})
		return
	}
	if row == nil || row.ProjectID != project || row.PeerKind != peerKindHuman {
		http.Error(w, "call not found", 404)
		return
	}
	p := phoneUserFrom(r)
	if !a.phoneCallAllowed(p, row, true) {
		http.Error(w, "call not found", 404)
		return
	}
	// A browser sends a few of these per call at most. The cap only protects the
	// diagnostic store, and this request is rare, so one read here costs nothing.
	var stored int
	if a.db().db.QueryRow(`SELECT COUNT(*) FROM telephony_browser_network_events WHERE project_id=? AND call_id=? AND id LIKE 'recovery-%:recovery'`, project, id).Scan(&stored) == nil && stored >= audioRecoveryPerCallMax {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	at := time.Now().UTC()
	network := audioNetworkEvent{ID: record.RecoveryID + ":recovery", Event: "softphone.browser.recovery", Action: "recovery", CallID: row.ID, ProjectID: row.ProjectID,
		Session: audioSessionCorrelation{RecoveryID: record.RecoveryID, InitiatingAction: "automatic_retry"}, Recovery: &record,
		Reason: record.Outcome, IdentitySource: "unattributed", OccurredAt: at.Format(time.RFC3339Nano), Retention: 7 * 24 * time.Hour}
	if p != nil {
		network.AdviserIdentity = p.Identity
		network.IdentitySource = "authorized_http_session"
	}
	network.ExpiresAt = at.Add(network.Retention).Format(time.RFC3339Nano)
	a.audioNetworks.enqueue(network)
	w.WriteHeader(http.StatusAccepted)
}

// Server timings for one attach, relative to the socket arriving. A session with
// no connected or setup_failed event never got its socket as far as Telephony.
type rtcSetupTrace struct {
	OfferMS     int64 `json:"offer_ms,omitempty"`
	AnswerMS    int64 `json:"answer_ms,omitempty"`
	ConnectedMS int64 `json:"connected_ms,omitempty"`
	FailedMS    int64 `json:"failed_ms,omitempty"`
}

// A plain reading of what had already stopped when a browser connection closed.
// Computed when read, from fields stored for every disconnect, so it costs a
// live call nothing and also works on events stored before this existed.
type audioDisconnectCause struct {
	FirstFailed    string `json:"first_failed"`
	AudioAgeMS     *int64 `json:"audio_age_ms,omitempty"`
	SignalingAgeMS *int64 `json:"signaling_age_ms,omitempty"`
}

const (
	audioStaleAfter = 3 * time.Second
	// The browser reports over signaling every 5 s, so a quiet socket is normal
	// for that long. Past 8 s at least one report did not arrive.
	signalingStaleAfter = 8 * time.Second
)

func audioDisconnectCauseAt(d *audioDisconnectInfo, at time.Time) audioDisconnectCause {
	out := audioDisconnectCause{FirstFailed: "unknown"}
	if d == nil {
		return out
	}
	age := func(value string) *int64 {
		seen, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return nil
		}
		ms := max(int64(0), at.Sub(seen).Milliseconds())
		return &ms
	}
	out.AudioAgeMS, out.SignalingAgeMS = age(d.LastAudioReadAt), age(d.LastReadAt)
	if out.AudioAgeMS == nil && out.SignalingAgeMS == nil {
		return out
	}
	stale := func(ms *int64, limit time.Duration) bool { return ms == nil || *ms > limit.Milliseconds() }
	// A muted microphone stops audio on purpose, which says nothing about the path.
	audio := !d.MicrophoneMuted && stale(out.AudioAgeMS, audioStaleAfter)
	signaling := stale(out.SignalingAgeMS, signalingStaleAfter)
	switch {
	case audio && signaling:
		out.FirstFailed = "both"
	case audio:
		out.FirstFailed = "audio"
	case signaling:
		out.FirstFailed = "signaling"
	default:
		out.FirstFailed = "neither"
	}
	return out
}
