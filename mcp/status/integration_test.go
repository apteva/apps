//go:build integration

package main

// Tier 2 — boot the real binary, talk MCP + REST. Same pattern as
// crm/storage/jobs.
//
// Run with:  go test -tags integration ./...

import (
	"strconv"
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

func TestSidecar_FullStatusFlow(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	// 1. Set via MCP.
	r := sc.MCP("status_set", map[string]any{
		"instance_id": 42,
		"message":     "running migrations",
		"tone":        "working",
	})
	if r["message"] != "running migrations" {
		t.Errorf("status_set returned %#v", r)
	}
	if r["tone"] != "working" {
		t.Errorf("tone wrong: %#v", r)
	}

	// 2. Read via REST.
	var rest map[string]any
	resp := sc.GET("/instances/"+strconv.Itoa(42), &rest)
	if resp.Status != 200 {
		t.Fatalf("REST GET: status=%d body=%s", resp.Status, resp.Body)
	}
	if rest["message"] != "running migrations" {
		t.Errorf("REST mismatch: %#v", rest)
	}

	// 3. Read via MCP — same data.
	got := sc.MCP("status_get", map[string]any{"instance_id": 42})
	if got["message"] != "running migrations" {
		t.Errorf("MCP status_get mismatch: %#v", got)
	}

	// 4. Update — upsert semantics.
	sc.MCP("status_set", map[string]any{
		"instance_id": 42,
		"message":     "deploying",
		"tone":        "info",
	})
	got = sc.MCP("status_get", map[string]any{"instance_id": 42})
	if got["message"] != "deploying" || got["tone"] != "info" {
		t.Errorf("update didn't take: %#v", got)
	}

	// 5. Clear via MCP.
	cleared := sc.MCP("status_clear", map[string]any{"instance_id": 42})
	if cleared["status"] != "cleared" {
		t.Errorf("status_clear returned %#v", cleared)
	}

	// 6. REST returns 204 for an absent status.
	resp2 := sc.GET("/instances/42", nil)
	if resp2.Status != 204 {
		t.Errorf("after clear, REST = %d, want 204", resp2.Status)
	}
}

func TestSidecar_RejectsInvalidTone(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	// MCPRaw exposes the JSON-RPC error envelope. With an invalid
	// tone, the handler returns an error which the SDK surfaces as
	// {error: {code, message}}.
	_, err := sc.MCPRaw("tools/call", map[string]any{
		"name": "status_set",
		"arguments": map[string]any{
			"instance_id": 1,
			"message":     "x",
			"tone":        "panic",
		},
	})
	if err == nil {
		t.Errorf("expected error envelope for invalid tone")
	}
}
