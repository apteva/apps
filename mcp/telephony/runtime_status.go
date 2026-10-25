package main

import (
	"net/http"
	"time"
)

func (rt *telephonyRuntime) fenceLocked(reason string, now time.Time) {
	if !rt.fenced {
		rt.fenceReason = reason
		rt.fencedAt = now
	}
	rt.recoveryVerified = false
	rt.fenced = true
	rt.state = "fenced"
}

// Status is available even when normal app routes are fenced. It never changes
// ownership or admission. The controller, not this process, owns replacement.
func (a *App) runtimeStatus() map[string]any {
	if a.runtime == nil {
		state := "active"
		admitting := a.legacyAdmission.Load() == 0
		if !admitting {
			state = "draining"
		}
		return map[string]any{"state": state, "admitting": admitting, "load": a.legacyWork.Load()}
	}
	rt := a.runtime
	rt.mu.Lock()
	defer rt.mu.Unlock()
	now := time.Now()
	state := rt.state
	valid := now.Before(rt.expires)
	if rt.initialized && !valid && state == "active" {
		state = "ownership_expired"
	}
	if rt.fenced {
		state = "fenced"
	}
	admitting := !rt.fenced && valid && rt.initialized && rt.armed && state == "active"
	out := map[string]any{"state": state, "admitting": admitting, "deployment_id": rt.deployment, "generation": rt.generation, "load": rt.work, "initialized": rt.initialized}
	if !rt.expires.IsZero() {
		out["ownership_expires_at"] = rt.expires.UTC().Format(time.RFC3339Nano)
	}
	if !rt.fencedAt.IsZero() {
		out["fenced_at"] = rt.fencedAt.UTC().Format(time.RFC3339Nano)
		out["fence_reason"] = rt.fenceReason
	}
	if rt.lastOwnershipError != "" {
		out["last_ownership_error"] = rt.lastOwnershipError
		out["last_ownership_error_at"] = rt.lastOwnershipErrorAt.UTC().Format(time.RFC3339Nano)
	}
	// Request replacement only once this exact owner can verify its lease again.
	// A revoked or displaced execution cannot initiate recovery of its successor.
	if rt.fenced && valid && rt.recoveryVerified {
		out["recovery"] = map[string]string{"action": "restart", "reason": rt.fenceReason}
	}
	return out
}

func (a *App) handleRuntimeHealth(w http.ResponseWriter, r *http.Request) {
	s := a.runtimeStatus()
	state := s["state"].(string)
	admitting := s["admitting"].(bool)
	// Passive replacements must pass process readiness before ownership commit.
	// Explicit standby is not a claim that calls can already be accepted.
	ready := admitting || state == "standby" || state == "activating"
	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSONStatus(w, status, map[string]any{"ok": ready, "status": state, "admitting": admitting})
}
