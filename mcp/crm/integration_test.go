//go:build integration

package main

// Tier 2 — the real binary, real HTTP. Boot the sidecar, talk MCP +
// REST. Validates the SDK wiring (manifest parsing at boot,
// migrations on disk, JSON-RPC dispatch, route mounting, /health,
// auth header) end-to-end.
//
// Run with:  go test -tags integration ./...

import (
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecar_BootsAndHealthOK(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var got map[string]any
	resp := sc.GET("/health", &got)
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	if got["ok"] != true {
		t.Errorf("/health body=%v", got)
	}
}

func TestSidecar_FullToolFlow(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	// Create via MCP.
	r := sc.MCP("contacts_upsert_by_channel", map[string]any{
		"kind":  "email",
		"value": "alice@example.com",
		"defaults": map[string]any{
			"first_name": "Alice",
			"last_name":  "Cooper",
		},
		"source": "test",
	})
	if r["was_created"] != true {
		t.Fatalf("expected was_created=true, got %#v", r["was_created"])
	}
	contact := r["contact"].(map[string]any)
	id := int64(contact["id"].(float64))

	// Same call again — should return the existing row.
	r2 := sc.MCP("contacts_upsert_by_channel", map[string]any{
		"kind":  "email",
		"value": "alice@example.com",
	})
	if r2["was_created"] != false {
		t.Errorf("expected was_created=false on dedupe, got %#v", r2["was_created"])
	}

	// Fetch via REST.
	var rest map[string]any
	sc.GET("/contacts/?id="+itoa(id), &rest)
	// /contacts/<id> path used by the panel — go ServeMux normalises
	// trailing slashes per its rules. Use the panel-shape path:
	resp := sc.GET("/contacts/"+itoa(id), &rest)
	if resp.Status != 200 {
		t.Fatalf("REST GET /contacts/%d: %d body=%s", id, resp.Status, string(resp.Body))
	}
	gotContact := rest["contact"].(map[string]any)
	if gotContact["primary_email"] != "alice@example.com" {
		t.Errorf("primary_email=%v", gotContact["primary_email"])
	}

	// Log activity via REST.
	var actOut map[string]any
	sc.POST("/contacts/"+itoa(id)+"/activities", map[string]any{
		"kind": "note", "body": "first contact made", "source": "test",
	}, &actOut)
	if actOut["activity"] == nil {
		t.Errorf("no activity in response: %v", actOut)
	}

	// Fetch context via MCP — should include the activity.
	ctxOut := sc.MCP("contacts_get_context", map[string]any{
		"id":             id,
		"activity_limit": 10,
	})
	acts := ctxOut["activities"].([]any)
	if len(acts) != 1 {
		t.Errorf("activities=%d, want 1", len(acts))
	}
}

func TestSidecar_ProjectScopeIsolation(t *testing.T) {
	// One sidecar pinned to project A.
	a := tk.SpawnSidecar(t, ".", tk.WithProjectID("proj-A"))
	a.MCP("contacts_create", map[string]any{
		"first_name": "AOnly",
		"channels":   []any{map[string]any{"kind": "email", "value": "a@x.com", "is_primary": true}},
	})
	// Search in A finds it.
	out := a.MCP("contacts_search", map[string]any{"q": "AOnly"})
	if out["count"].(float64) != 1 {
		t.Errorf("project A: expected 1, got %v", out["count"])
	}

	// Spawning a second binary pinned to project B doesn't see A's data
	// because each sidecar gets its own temp DB. (Tests cross-DB
	// isolation — the partition column is irrelevant when DBs differ.)
	b := tk.SpawnSidecar(t, ".", tk.WithProjectID("proj-B"))
	out2 := b.MCP("contacts_search", map[string]any{"q": "AOnly"})
	if out2["count"].(float64) != 0 {
		t.Errorf("project B: expected 0, got %v", out2["count"])
	}
}

func TestSidecar_GlobalScope_RequiresProjectIDPerCall(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".") // no project_id = global
	// Without _project_id, the call should fail with a clear error.
	_, err := sc.MCPRaw("tools/call", map[string]any{
		"name":      "contacts_search",
		"arguments": map[string]any{"q": "x"},
	})
	if err == nil {
		t.Fatal("expected MCP error when scope=global and project_id is missing")
	}
	if !strings.Contains(err.Error(), "project_id") {
		t.Errorf("error %q should mention project_id", err.Error())
	}

	// Same call with _project_id should work.
	out := sc.MCP("contacts_search", map[string]any{
		"_project_id": "proj-X",
		"q":           "anything",
	})
	if out["count"].(float64) != 0 {
		t.Errorf("expected 0 results in fresh project, got %v", out["count"])
	}
}

func itoa(i int64) string {
	return strconvFormatInt(i)
}

// avoid pulling in strconv just for this helper inside the build-tagged file
func strconvFormatInt(i int64) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	out := ""
	for i > 0 {
		out = string(digits[i%10]) + out
		i /= 10
	}
	if neg {
		out = "-" + out
	}
	return out
}
