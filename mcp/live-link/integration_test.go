//go:build integration

package main

// Tier 2 — boot the real sidecar binary, talk MCP + REST. Validates
// SDK wiring (manifest parsing at boot, migrations on disk, JSON-RPC
// dispatch, route mounting, /health, auth header) end-to-end.
//
// Run with:  go test -tags integration ./...
//
// We deliberately do NOT exercise expose_start here — that would
// either spawn real cloudflared (network-dependent, slow,
// non-deterministic) or require us to swap the binary path through
// the SDK config plumbing. The Manager lifecycle is covered against
// a fake binary in main_test.go (Tier 1); what's left for Tier 2 is
// the wiring around it: routes mount, MCP tools resolve, JSON
// envelopes look right.

import (
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecar_BootsAndHealthOK(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var got map[string]any
	resp := sc.GET("/health", &got)
	if resp.Status != 200 {
		t.Fatalf("status=%d body=%s", resp.Status, resp.Body)
	}
	if got["ok"] != true {
		t.Errorf("/health body=%v", got)
	}
}

func TestSidecar_StatusReturnsIdle(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var got map[string]any
	resp := sc.GET("/status", &got)
	if resp.Status != 200 {
		t.Fatalf("status=%d body=%s", resp.Status, resp.Body)
	}
	if got["status"] != "idle" {
		t.Errorf("status=%v, want idle", got["status"])
	}
	if got["resolved_target"] == "" {
		t.Error("resolved_target should be populated from APTEVA_GATEWAY_URL or default")
	}
}

func TestSidecar_RunsListEmpty(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var got map[string]any
	resp := sc.GET("/runs", &got)
	if resp.Status != 200 {
		t.Fatalf("status=%d body=%s", resp.Status, resp.Body)
	}
	runs, ok := got["runs"].([]any)
	if !ok {
		t.Fatalf("runs missing or wrong shape: %#v", got)
	}
	if len(runs) != 0 {
		t.Errorf("expected 0 runs in fresh sidecar, got %d", len(runs))
	}
}

// expose_status via MCP. Doesn't trigger a tunnel; just confirms
// MCP dispatch resolves the tool name and the response shape is
// JSON-friendly.
func TestSidecar_MCPExposeStatus(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	got := sc.MCP("expose_status", map[string]any{})
	// Either the result is unwrapped to {status: "idle", ...} or
	// the testkit returns {text: "<json>"}; either is fine. We
	// just need it not to error.
	if got == nil {
		t.Fatal("expose_status returned nil")
	}
	if status, ok := got["status"].(string); ok && status != "idle" {
		t.Errorf("status=%q, want idle", status)
	}
}

// expose_stop on an idle sidecar should be a no-op, not an error.
// This is the property real callers rely on for "make the tunnel
// off, don't care if it was already off."
func TestSidecar_MCPExposeStopWhenIdleIsNoop(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	got := sc.MCP("expose_stop", map[string]any{})
	if got == nil {
		t.Fatal("expose_stop returned nil")
	}
	// The handler returns {stopped: true} either way.
	if stopped, ok := got["stopped"].(bool); ok && !stopped {
		t.Errorf("expose_stop returned stopped=false")
	}
}

func TestSidecar_RejectsUnknownTool(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	got, err := sc.MCPRaw("tools/call", map[string]any{
		"name":      "expose_does_not_exist",
		"arguments": map[string]any{},
	})
	if err == nil && got["error"] == nil {
		t.Errorf("expected tools/call to reject unknown tool, got %#v", got)
	}
}
