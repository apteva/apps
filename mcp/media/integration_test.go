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

func TestSidecar_StatusToolReturnsCounts(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	out := sc.MCP("media_index_status", map[string]any{
		"_project_id": "test-proj",
	})
	// Empty index → empty map (no rows to count). Just verify the
	// dispatch worked without error.
	if out == nil {
		t.Errorf("media_index_status returned nil")
	}
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

func TestSidecar_ReindexFlagsRow(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	out := sc.MCP("media_reindex", map[string]any{
		"_project_id": "test-proj",
		"failed_only": true,
	})
	// No rows yet — queued count should be 0, not an error.
	if _, ok := out["queued"]; !ok {
		t.Errorf("media_reindex didn't return queued count: %v", out)
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
