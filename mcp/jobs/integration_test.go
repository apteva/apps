//go:build integration

package main

// Tier 2 — boot the real binary, talk MCP + REST. Validates SDK
// wiring (manifest parsing, migrations on disk, JSON-RPC dispatch,
// route mounting, /health, auth) end-to-end.
//
// Run with:  go test -tags integration ./...

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestSidecar_ScheduleListCancelFlow(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	// Schedule via MCP.
	r := sc.MCP("jobs_schedule", map[string]any{
		"name":     "weekly-review",
		"schedule": map[string]any{"kind": "cron", "cron": "0 9 * * 1"},
		"target":   map[string]any{"kind": "event", "instance_id": float64(7), "message": "Monday 9am — weekly review"},
	})
	job := r["job"].(map[string]any)
	id := int64(job["id"].(float64))
	if job["status"] != "pending" {
		t.Errorf("expected pending, got %v", job["status"])
	}

	// List via REST should include it.
	var rest map[string]any
	sc.GET("/jobs", &rest)
	jobs := rest["jobs"].([]any)
	if len(jobs) != 1 {
		t.Errorf("REST list got %d jobs, want 1", len(jobs))
	}

	// Cancel via MCP.
	c := sc.MCP("jobs_cancel", map[string]any{"id": id})
	if c["cancelled"] != true {
		t.Errorf("cancel returned %v", c)
	}

	// Re-fetch — status should be cancelled.
	g := sc.MCP("jobs_get", map[string]any{"id": id})
	if g["job"].(map[string]any)["status"] != "cancelled" {
		t.Errorf("status after cancel: %v", g["job"].(map[string]any)["status"])
	}
}

func TestSidecar_RandomPreview(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var out map[string]any
	resp := sc.POST("/preview", map[string]any{
		"timezone": "Europe/Paris",
		"schedule": map[string]any{
			"kind": "random", "period": "day", "runs_per_period": 5,
			"window_start": "08:00", "window_end": "22:00", "min_spacing_minutes": 60,
		},
	}, &out)
	if resp.Status != 200 {
		t.Fatalf("preview status=%d body=%s", resp.Status, resp.Body)
	}
	if runs, ok := out["runs"].([]any); !ok || len(runs) != 5 {
		t.Fatalf("preview runs=%#v, want five", out["runs"])
	}
	if seed, _ := out["schedule_seed"].(string); len(seed) != 64 {
		t.Fatalf("preview seed=%q, want 64 hex characters", seed)
	}
}

func TestSidecar_GlobalScope_RequiresProjectIDPerCall(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".") // no project_id = global scope
	_, err := sc.MCPRaw("tools/call", map[string]any{
		"name":      "jobs_list",
		"arguments": map[string]any{},
	})
	if err == nil {
		t.Fatal("expected error when project_id is missing in global scope")
	}
	if !strings.Contains(err.Error(), "project_id") {
		t.Errorf("error %q should mention project_id", err.Error())
	}

	// With _project_id it should succeed.
	out := sc.MCP("jobs_list", map[string]any{"_project_id": "proj-X"})
	if out["count"].(float64) != 0 {
		t.Errorf("expected 0 jobs in fresh project, got %v", out["count"])
	}
}

// Smoke-test: schedule a once-job that points at our test endpoint
// and let the dispatcher worker pick it up. We don't poll — the SDK
// runs the worker every 5s; the test just waits a bit and verifies a
// run row appeared.
func TestSidecar_DispatcherPicksUpDueJob(t *testing.T) {
	var delivered atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/apps/callback/agents/7" {
			w.Write([]byte(`{"id":7,"project_id":"test-proj"}`))
			return
		}
		if r.URL.Path == "/api/apps/callback/agents/7/event" {
			delivered.Add(1)
			w.Write([]byte(`{"ok":true}`))
			return
		}
		w.Write([]byte(`[]`))
	}))
	defer gateway.Close()
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL))

	// Assert the callback is delivered and finalized successfully.
	r := sc.MCP("jobs_schedule", map[string]any{
		"name":     "imminent",
		"schedule": map[string]any{"kind": "once", "run_at": time.Now().Add(-1 * time.Second).UTC().Format(time.RFC3339)},
		"target":   map[string]any{"kind": "event", "instance_id": float64(7), "message": "go"},
	})
	id := int64(r["job"].(map[string]any)["id"].(float64))

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		out := sc.MCP("jobs_runs", map[string]any{"id": id})
		runs := out["runs"].([]any)
		if len(runs) >= 1 && runs[0].(map[string]any)["status"] == "ok" && delivered.Load() == 1 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("dispatcher did not run the job within 15s")
}
