//go:build integration

package main

import (
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAuditSidecarProjectlessJobDispatches(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/apps/callback/projects" {
			w.Write([]byte(`[{"id":"project-a","name":"A"}]`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer gateway.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer target.Close()
	sc := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL))
	makeJob := func(pid string) int64 {
		var out map[string]any
		path := "/jobs?scope=global"
		if pid != "" {
			path = "/jobs?project_id=" + pid
		}
		resp := sc.POST(path, map[string]any{
			"_project_id": pid, "name": "audit",
			"schedule": map[string]any{"kind": "once", "run_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)},
			"target":   map[string]any{"kind": "http", "url": target.URL},
		}, &out)
		if resp.Status != 200 {
			t.Fatalf("create: %d %s", resp.Status, resp.Body)
		}
		return int64(out["job"].(map[string]any)["id"].(float64))
	}
	globalID := makeJob("")
	projectID := makeJob("project-a")
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		out := sc.MCP("jobs_get", map[string]any{"_project_id": "project-a", "id": projectID})
		if out["job"].(map[string]any)["status"] == "done" {
			global := sc.MCP("jobs_get", map[string]any{"_project_id": "", "id": globalID})
			if global["job"].(map[string]any)["status"] != "done" {
				t.Fatalf("SDK tick completed project-a job but left projectless job %v", global["job"].(map[string]any)["status"])
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("control job in project-a did not run within 8 seconds")
}
