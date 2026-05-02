//go:build integration

package main

import (
	"net/http"
	"os/exec"
	"strconv"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// Tier 2 — spin up the real binary and exercise the API. We don't
// have a storage sidecar in this scope, so the worker can't probe
// anything; what we check is that the surface area is intact:
// /health responds, /media returns an empty list, and every MCP
// tool dispatches without panic. The end-to-end probe → derivation
// flow is validated in scenarios (Tier 3) once scenario-runner
// supports multi-app installs.

func TestSidecar_HealthOK(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var got map[string]any
	resp := sc.GET("/health", &got)
	if resp.Status != 200 || got["ok"] != true {
		t.Fatalf("/health status=%d body=%v", resp.Status, got)
	}
}

func TestSidecar_EmptyCatalog(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	// REST.
	var rest map[string]any
	resp := sc.GET("/media?project_id=test-proj", &rest)
	if resp.Status != 200 {
		t.Fatalf("/media status=%d", resp.Status)
	}
	if rows, ok := rest["media"].([]any); !ok || len(rows) != 0 {
		t.Errorf("expected empty media list, got %v", rest)
	}

	// MCP.
	out := sc.MCP("media_search", map[string]any{
		"_project_id": "test-proj",
	})
	if rows, ok := out["media"].([]any); !ok || len(rows) != 0 {
		t.Errorf("expected empty media via MCP, got %v", out)
	}
}

func TestSidecar_StatusEndpoint(t *testing.T) {
	// Status counts now live on a plain HTTP route — agents don't see
	// it as an MCP tool. The dashboard footer hits this.
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var got map[string]any
	resp := sc.GET("/status?project_id=test-proj", &got)
	if resp.Status != 200 {
		t.Fatalf("/status status=%d", resp.Status)
	}
	// Empty index → empty map. Just verifying the route works.
}

func TestSidecar_GetMissingFile(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	out := sc.MCP("media_get", map[string]any{
		"_project_id": "test-proj",
		"file_id":     "9999",
	})
	if found, _ := out["found"].(bool); found {
		t.Errorf("media_get on missing file_id should report found=false: %v", out)
	}
}

func TestSidecar_ReindexEndpoint(t *testing.T) {
	// Reindex moved off the MCP surface to a plain HTTP route. With
	// an empty catalog this is a no-op that should still return 200.
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	req, _ := http.NewRequest("POST",
		sc.URL()+"/reindex?project_id=test-proj&failed_only=true", nil)
	req.Header.Set("Authorization", "Bearer "+sc.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("reindex status=%d", resp.StatusCode)
	}
}

// Sanity: ffmpeg + ffprobe are on PATH in this environment. Skipping
// runProbe live-fire here since Tier 1 already covers it; this is a
// guard against the binary being mis-shipped to a host that lacks
// ffmpeg.
func TestEnvironment_FFmpegOnPath(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Errorf("ffmpeg not on PATH: %v", err)
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Errorf("ffprobe not on PATH: %v", err)
	}
}

// Direct REST GET on a missing item — Tier 2 hits the sidecar with
// the install bearer (storage's pattern); withTokenAuth in run.go
// gates everything except /health and signed URLs.
func TestSidecar_MediaItem_MissingReturns404(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	req, _ := http.NewRequest("GET", sc.URL()+"/media/9999?project_id=test-proj", nil)
	req.Header.Set("Authorization", "Bearer "+sc.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("expected 404 for missing media, got %d", resp.StatusCode)
	}
}

// Helpers shared with other tests but kept package-local.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// ─── Render surface (Tier 2) ────────────────────────────────────────
//
// These tests cover the submit / list / get / cancel surface end-to-
// end through MCP + REST. We don't assert that renders complete:
// without a storage sidecar the worker pool will fail every render
// at the source-download step. What we verify is that the row lands
// in the queue, the panel/agent can observe it, and cancellation
// flips terminal status before any worker picks it up.

func TestSidecar_SubmitTrim_QueuesRow(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		// Set pool size to 0-ish via a huge poll interval; we can't
		// disable the pool, but the sidecar's gateway is unreachable
		// in this scope so any claim that does happen will fail
		// quickly and the failed row is fine for our assertions.
		tk.WithConfig(map[string]string{
			"render_pool_size":      "1",
			"render_timeout_seconds": "5",
		}),
	)

	out := sc.MCP("media_trim", map[string]any{
		"_project_id": "test-proj",
		"file_id":     "999",
		"start_ms":    1000,
		"end_ms":      3000,
		"output_name": "clip.mp4",
	})
	id, ok := out["render_id"].(float64) // JSON numbers come back as float64
	if !ok || id == 0 {
		t.Fatalf("missing render_id in response: %v", out)
	}
	if out["operation"] != "trim" {
		t.Errorf("operation=%v want trim", out["operation"])
	}
	if out["status"] != "pending" {
		t.Errorf("status=%v want pending", out["status"])
	}
}

func TestSidecar_GetRender_RoundsTrip(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	// Submit, then immediately fetch the row.
	subm := sc.MCP("media_trim", map[string]any{
		"_project_id": "test-proj",
		"file_id":     "1",
		"start_ms":    0,
		"end_ms":      1000,
	})
	id := int64(subm["render_id"].(float64))

	out := sc.MCP("media_get_render", map[string]any{
		"_project_id": "test-proj",
		"render_id":   id,
	})
	if !out["found"].(bool) {
		t.Fatalf("expected found=true: %v", out)
	}
	r := out["render"].(map[string]any)
	if r["operation"] != "trim" {
		t.Errorf("round-trip lost operation: %v", r)
	}
}

func TestSidecar_GetRender_MissingReturnsNotFoundFlag(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	out := sc.MCP("media_get_render", map[string]any{
		"_project_id": "test-proj",
		"render_id":   999999,
	})
	if found, _ := out["found"].(bool); found {
		t.Errorf("expected found=false for missing render: %v", out)
	}
}

func TestSidecar_ListRenders_FiltersByOperation(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	sc.MCP("media_trim", map[string]any{
		"_project_id": "test-proj", "file_id": "1",
		"start_ms": 0, "end_ms": 500,
	})
	sc.MCP("media_resize", map[string]any{
		"_project_id": "test-proj", "file_id": "1",
		"width": 320, "height": 240,
	})

	out := sc.MCP("media_list_renders", map[string]any{
		"_project_id": "test-proj",
		"operation":   "trim",
	})
	rows, _ := out["renders"].([]any)
	if len(rows) != 1 {
		t.Errorf("expected 1 trim render, got %d (rows=%v)", len(rows), rows)
	}
}

func TestSidecar_CancelPendingRender(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	subm := sc.MCP("media_trim", map[string]any{
		"_project_id": "test-proj", "file_id": "1",
		"start_ms": 0, "end_ms": 500,
	})
	id := int64(subm["render_id"].(float64))

	out := sc.MCP("media_cancel_render", map[string]any{
		"_project_id": "test-proj",
		"render_id":   id,
	})
	// Race: the worker pool may have picked it up and failed it
	// before we cancelled (no storage to download from). Either
	// "cancelled" or already-terminal "failed" are acceptable; we
	// just assert it isn't "running" / "pending" any more.
	status, _ := out["status"].(string)
	if status != "cancelled" && status != "failed" {
		t.Errorf("expected cancelled or already-failed, got %v (raw=%v)", status, out)
	}
}

func TestSidecar_SubmitInvalidParamsFailsFast(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	// JSON-RPC error: end_ms <= start_ms is rejected at submit time
	// (buildPlan validation), the row never enters the queue.
	out, err := sc.MCPRaw("tools/call", map[string]any{
		"name": "media_trim",
		"arguments": map[string]any{
			"_project_id": "test-proj",
			"file_id":     "1",
			"start_ms":    int64(5000),
			"end_ms":      int64(1000),
		},
	})
	if err == nil && out["error"] == nil {
		// Some apps surface tool errors as result.isError=true rather
		// than a JSON-RPC error. Accept either shape.
		if r, ok := out["result"].(map[string]any); ok {
			if ie, _ := r["isError"].(bool); !ie {
				t.Errorf("expected error response, got success: %v", out)
			}
		}
	}

	// And the queue must still be empty.
	listed := sc.MCP("media_list_renders", map[string]any{
		"_project_id": "test-proj",
	})
	rows, _ := listed["renders"].([]any)
	if len(rows) != 0 {
		t.Errorf("invalid submit leaked a row: %v", rows)
	}
}

// ─── Description surface (v0.3) ────────────────────────────────────
//
// We can't seed a media row through the sidecar's MCP — only the
// indexer creates rows, and it needs storage to do that. So this
// test asserts the not-found path (no row → found:false) and the
// argument validation path. The full set+get round-trip lives in
// the description Tier 1 tests; the cross-app version is covered
// by the Tier 3 scenario.

func TestSidecar_SetDescription_NotFound(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	out := sc.MCP("media_set_description", map[string]any{
		"_project_id": "test-proj",
		"file_id":     "999",
		"description": "anything",
	})
	if found, _ := out["found"].(bool); found {
		t.Errorf("expected found=false on missing row: %v", out)
	}
}

func TestSidecar_SetDescription_RequiresFileID(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	_, err := sc.MCPRaw("tools/call", map[string]any{
		"name": "media_set_description",
		"arguments": map[string]any{
			"_project_id": "test-proj",
			"description": "x",
		},
	})
	if err == nil {
		t.Error("expected JSON-RPC error when file_id missing")
	}
}

func TestSidecar_RenderHTTPRoutes(t *testing.T) {
	// /renders supports POST (jobs-app callback shape) + GET (panel
	// listing). Exercise both.
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	// POST /renders
	body := map[string]any{
		"operation": "trim",
		"file_id":   "1",
		"params":    map[string]any{"start_ms": 0, "end_ms": 1000},
	}
	var posted map[string]any
	resp := sc.POST("/renders?project_id=test-proj", body, &posted)
	if resp.Status != http.StatusAccepted {
		t.Fatalf("POST /renders status=%d", resp.Status)
	}
	if _, ok := posted["render_id"]; !ok {
		t.Errorf("response missing render_id: %v", posted)
	}

	// GET /renders
	var listed map[string]any
	if r := sc.GET("/renders?project_id=test-proj", &listed); r.Status != 200 {
		t.Fatalf("GET /renders status=%d", r.Status)
	}
	rows, _ := listed["renders"].([]any)
	if len(rows) == 0 {
		t.Errorf("GET /renders returned empty list after POST")
	}
}
