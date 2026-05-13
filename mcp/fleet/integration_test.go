//go:build integration

package main

// Tier 3: builds the actual fleet binary and runs it as a real
// sidecar (build tag "integration" — opt in with `go test -tags
// integration ./...`). What this catches that tier 2 cannot:
//
//   - panic-on-mount (the v0.2.3 duplicate /health route bug). If
//     sdk.Run blows up, /health never returns 200 and the testkit
//     fails the test inside the readiness wait.
//   - the embedded manifest actually parsing through ParseManifest
//     inside the real binary path.
//   - HTTP routes reachable from outside the process.
//   - migrations applying on a fresh DB.
//
// We deliberately do NOT call tenant_create here — that would spawn
// a child apteva on the dev machine. Cover that path separately if
// needed with a stub-binary mode.

import (
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecar_HealthOK(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var got map[string]any
	resp := sc.GET("/health", &got)
	if resp.Status != 200 || got["ok"] != true {
		t.Fatalf("/health status=%d body=%+v", resp.Status, got)
	}
}

func TestSidecar_TenantListEmpty(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	out := sc.MCP("tenant_list", map[string]any{})
	if int(out["count"].(float64)) != 0 {
		t.Errorf("fresh sidecar list count=%v want 0", out["count"])
	}
}

func TestSidecar_TenantGet404(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	_, err := sc.MCPRaw("tools/call", map[string]any{
		"name":      "tenant_get",
		"arguments": map[string]any{"tenant_id": "tnt_doesnotexist"},
	})
	if err == nil {
		t.Fatal("tenant_get of unknown id should error")
	}
}

func TestSidecar_AttachKeyRejectsUnknownTenant(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	_, err := sc.MCPRaw("tools/call", map[string]any{
		"name": "tenant_attach_key",
		"arguments": map[string]any{
			"tenant_id": "tnt_doesnotexist",
			"api_key":   "sk-fake",
		},
	})
	if err == nil {
		t.Fatal("attach_key against unknown tenant should error")
	}
}

func TestSidecar_RESTSurface(t *testing.T) {
	// GET /tenants returns the registry view. On a fresh sidecar:
	// empty list + count=0.
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var got map[string]any
	resp := sc.GET("/tenants", &got)
	if resp.Status != 200 {
		t.Fatalf("GET /tenants: %d", resp.Status)
	}
	if int(got["count"].(float64)) != 0 {
		t.Errorf("count=%v want 0", got["count"])
	}
}
