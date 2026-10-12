package main

import (
	"encoding/json"
	"time"
)

// Signaling readiness is distinct from carrier answer and from measured speech
// receipt. Browser runtimes combine this with their own local audio state.
type mediaReadinessEvent struct {
	Type             string `json:"type"`
	CallID           string `json:"call_id"`
	ConnectionID     string `json:"connection_id"`
	Revision         uint64 `json:"revision"`
	BridgeGeneration uint64 `json:"bridge_generation"`
	At               string `json:"occurred_at"`
	Reason           string `json:"reason"`
}

func (h *softphoneHub) publishReadiness() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.publishReadinessLocked()
}
func (h *softphoneHub) publishReadinessLocked() {
	w := h.browser
	if w == nil || h.readyBrowser != w {
		return
	}
	reason := "ready"
	switch {
	case h.closed:
		reason = "call_ended"
	case h.status != "answered" && h.status != "in-progress":
		reason = "call_not_answered"
	case h.held:
		reason = "hold"
	case h.peer == nil:
		reason = "carrier_not_attached"
	}
	if h.readinessBrowser == w && h.readinessReason == reason && h.readinessGeneration == h.bridgeGeneration {
		return
	}
	kind := "media.not_ready"
	if reason == "ready" {
		kind = "media.ready"
	}
	w.audioMu.Lock()
	connection := w.diagnosticID
	w.audioMu.Unlock()
	e := mediaReadinessEvent{kind, h.callID, connection, h.readinessRevision + 1, h.bridgeGeneration, ringTime(time.Now()), reason}
	raw, _ := json.Marshal(e)
	// A slow observer must never hold up media. The per-second watcher retries
	// the current state if this bounded control queue is full.
	if !w.queueControl(raw) {
		return
	}
	h.readinessRevision = e.Revision
	h.readinessBrowser = w
	h.readinessReason = reason
	h.readinessGeneration = h.bridgeGeneration
	h.startup.Readiness = append(h.startup.Readiness, e)
	if len(h.startup.Readiness) > 16 {
		h.startup.Readiness = append([]mediaReadinessEvent(nil), h.startup.Readiness[len(h.startup.Readiness)-16:]...)
	}
}
