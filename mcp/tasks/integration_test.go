//go:build integration

package main

// Tier 2 — boot the real sidecar binary, talk MCP + REST. Validates
// SDK wiring (manifest parsing at boot, migrations on disk, JSON-RPC
// dispatch, route mounting, /health, auth header) end-to-end.
//
// Run with:  go test -tags integration ./...
//
// Same pattern as apps/mcp/crm and apps/mcp/storage.

import (
	"encoding/json"
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

func TestSidecar_FullToolFlow(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	// 1. Create a task via MCP.
	r := sc.MCP("tasks_create", map[string]any{
		"instance_id": 7,
		"title":       "Ship social v0.2",
		"notes":       "remember to push panel",
	})
	id := int64(r["id"].(float64))
	if id == 0 {
		t.Fatalf("tasks_create returned no id: %#v", r)
	}
	if r["status"] != "open" {
		t.Errorf("default status = %v, want open", r["status"])
	}
	if r["title"] != "Ship social v0.2" {
		t.Errorf("title round-trip wrong: %#v", r)
	}

	// 2. Read back via MCP — list filtered by status=open. The MCP
	// wrapper returns a list inside content[0].text; the testkit's
	// MCPRaw unwrap falls back to {text: "<json>"} for non-object
	// results, so we re-decode the text manually.
	raw := sc.MCP("tasks_list", map[string]any{"instance_id": 7})
	var listed []map[string]any
	if text, ok := raw["text"].(string); ok {
		if err := json.Unmarshal([]byte(text), &listed); err != nil {
			t.Fatalf("decode list text: %v (text=%q)", err, text)
		}
	}
	if len(listed) != 1 || listed[0]["title"] != "Ship social v0.2" {
		t.Errorf("MCP list mismatch: %#v", listed)
	}

	// 3. Read back via REST — same data, panel-shape.
	var rest []map[string]any
	resp := sc.GET("/instances/7", &rest)
	if resp.Status != 200 {
		t.Fatalf("REST GET: status=%d body=%s", resp.Status, resp.Body)
	}
	if len(rest) != 1 || rest[0]["title"] != "Ship social v0.2" {
		t.Errorf("REST list mismatch: %#v", rest)
	}

	// 4. Complete the task via MCP.
	done := sc.MCP("tasks_complete", map[string]any{"task_id": id})
	if done["status"] != "done" {
		t.Errorf("tasks_complete returned %#v", done)
	}

	// 5. Confirm via REST that status is now done.
	var doneList []map[string]any
	sc.GET("/instances/7?status=done", &doneList)
	if len(doneList) != 1 || doneList[0]["status"] != "done" {
		t.Errorf("expected one done task, got %#v", doneList)
	}

	// 6. Delete the task via REST.
	delResp := sc.DELETE("/tasks/" + strconv.FormatInt(id, 10))
	if delResp.Status != 204 {
		t.Errorf("DELETE returned %d", delResp.Status)
	}

	// 7. Listing all (status=all) should now be empty.
	var all []map[string]any
	sc.GET("/instances/7?status=all", &all)
	if len(all) != 0 {
		t.Errorf("expected empty list after delete, got %#v", all)
	}
}

func TestSidecar_RejectsUnknownTool(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	// MCPRaw returns the JSON-RPC error envelope without panicking.
	got, err := sc.MCPRaw("tools/call", map[string]any{
		"name":      "tasks_does_not_exist",
		"arguments": map[string]any{},
	})
	if err == nil && got["error"] == nil {
		t.Errorf("expected tools/call to reject unknown tool, got %#v", got)
	}
}
