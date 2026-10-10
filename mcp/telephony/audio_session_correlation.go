package main

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// These IDs are observational and convey no authorization. Tokens and hashes
// are deliberately absent, and arbitrary action/error text is never retained.
type audioSessionCorrelation struct {
	SessionID         string `json:"session_id,omitempty"`
	PreviousSessionID string `json:"previous_session_id,omitempty"`
	RecoveryID        string `json:"recovery_id,omitempty"`
	AttemptID         string `json:"attempt_id,omitempty"`
	InitiatingAction  string `json:"initiating_action,omitempty"`
	IssuedAt          string `json:"issued_at,omitempty"`
}

var audioDiagnosticIDPattern = regexp.MustCompile(`^(browser|session|recovery|attempt)-[a-f0-9-]{1,64}$`)

func safeAudioDiagnosticID(s string) string {
	if len(s) > 80 || !audioDiagnosticIDPattern.MatchString(s) {
		return ""
	}
	return s
}
func audioInitiatingAction(s string) string {
	switch s {
	case "answer", "dial", "attach", "takeover", "automatic_retry", "manual_reconnect", "audio_device_change", "component_recreation":
		return s
	}
	return "attach"
}
func normalizeAudioSessionCorrelation(s audioSessionCorrelation) audioSessionCorrelation {
	s.SessionID = safeAudioDiagnosticID(s.SessionID)
	s.PreviousSessionID = safeAudioDiagnosticID(s.PreviousSessionID)
	s.RecoveryID = safeAudioDiagnosticID(s.RecoveryID)
	s.AttemptID = safeAudioDiagnosticID(s.AttemptID)
	s.InitiatingAction = audioInitiatingAction(s.InitiatingAction)
	return s
}
func audioSessionRequest(r *http.Request, action string) audioSessionCorrelation {
	var body struct {
		Diagnostics audioSessionCorrelation `json:"media_diagnostics"`
	}
	// Invalid observational metadata is ignored; it cannot reject attachment.
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	}
	s := normalizeAudioSessionCorrelation(body.Diagnostics)
	if action == "takeover" {
		s.InitiatingAction = action
	}
	return s
}
func (t *audioCallTelemetry) sessionIssued(row *callRow, s audioSessionCorrelation, identity phoneIdentity, collect func(audioNetworkEvent)) {
	// No database and no waiting on the collector. Snapshot only the active
	// attachment; disconnected sessions remain correlated by their generation ID.
	t.mu.Lock()
	network := audioNetworkEvent{CallID: row.ID, ProjectID: row.ProjectID, Retention: 7 * 24 * time.Hour}
	for writer, e := range t.sockets {
		if e.ConnectionID == t.socket.ConnectionID {
			network = e
			e.ReplacedBy = &s
			t.sockets[writer] = e
			break
		}
	}
	t.mu.Unlock()
	if identity.valid() {
		network.SessionIssuerIdentity = &identity
	}
	enqueueAudioSessionIssued(network, s, collect)
}
func enqueueAudioSessionIssued(network audioNetworkEvent, s audioSessionCorrelation, collect func(audioNetworkEvent)) {
	if collect == nil {
		return
	}
	network.ID = s.SessionID + ":issued"
	network.Event = "softphone.media.session_issued"
	network.Action = "issued"
	if s.PreviousSessionID != "" {
		network.Event = "softphone.media.session_replaced"
		network.Action = "replaced"
	}
	network.Session = s
	network.OccurredAt = s.IssuedAt
	network.ExpiresAt = time.Now().Add(network.Retention).UTC().Format(time.RFC3339Nano)
	collect(network)
}
func newAudioSessionID() string {
	return strings.Replace(newAudioConnectionID(), "browser-", "session-", 1)
}

func safeAudioDiagnosticLabel(s string, limit int) string {
	if len(s) > limit {
		return ""
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return ""
		}
	}
	return s
}
func safeAudioDiagnosticDetail(s string) string {
	// Fixed runtime messages only. Unknown free-form browser/provider errors are
	// represented by their action/outcome/code, never persisted verbatim.
	switch s {
	case "WebSocket transport error", "WebRTC signaling error", "Audio worker failed", "WebRTC signaling unavailable":
		return s
	}
	if s != "" {
		return "detail_redacted"
	}
	return ""
}

func enqueueAudioAttachmentFailure(network audioNetworkEvent, event string, status int, reason string, diagnostic *audioDisconnectInfo, collect func(audioNetworkEvent)) {
	if collect == nil {
		return
	}
	network.ID = newAudioConnectionID() + ":failed"
	network.Event = event
	network.Action = "failed"
	network.HTTPStatus = status
	network.Reason = reason
	network.Disconnect = diagnostic
	network.OccurredAt = time.Now().UTC().Format(time.RFC3339Nano)
	network.ExpiresAt = time.Now().Add(network.Retention).UTC().Format(time.RFC3339Nano)
	collect(network)
}
