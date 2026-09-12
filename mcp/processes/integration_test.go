//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Boots both real binaries. The gateway fixture implements only platform
// routing and agent delivery; all procedure/task state lives in real sidecars.
func TestSidecarsProcessToTasks(t *testing.T) {
	var tasks *tk.Sidecar
	var delivered atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/apps/callback/agents/7":
			_ = json.NewEncoder(w).Encode(sdk.PlatformInstance{ID: 7, ProjectID: "project-a", DefaultThreadID: "opaque-owner"})
		case "/api/apps/callback/agents/7/event":
			var request sdk.AgentEventRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			delivered.Add(1)
			_ = json.NewEncoder(w).Encode(sdk.AgentEventReceipt{SourceEventID: request.SourceEventID, ExecutionID: "preview:" + request.SourceEventID, ThreadID: request.ThreadID, Accepted: true})
		case "/api/apps/callback/apps/tasks/call":
			var input struct {
				Tool  string         `json:"tool"`
				Input map[string]any `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": input.Tool, "arguments": input.Input}})
			req, _ := http.NewRequest("POST", tasks.URL()+"/mcp", bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer "+tasks.Token())
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(sdk.HeaderBoundCallerInstallID, "88")
			req.Header.Set(sdk.HeaderBoundCallerAppName, "processes")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			defer resp.Body.Close()
			w.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(w, resp.Body)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	tasks = tk.SpawnSidecar(t, "../tasks", tk.WithProjectID("project-a"), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL))
	processes := tk.SpawnSidecar(t, ".", tk.WithProjectID("project-a"), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL))
	var p Process
	resp := processes.POST("/processes?project_id=project-a", map[string]any{"definition": def()}, &p)
	if resp.Status != 200 {
		t.Fatalf("create %d %s", resp.Status, resp.Body)
	}
	resp = processes.POST("/processes/"+p.ID+"/activate?project_id=project-a", map[string]any{}, &p)
	if resp.Status != 200 || p.SyncPending {
		t.Fatalf("activate %d %s", resp.Status, resp.Body)
	}
	var started struct {
		Task            map[string]any `json:"task"`
		DeliveryWarning string         `json:"delivery_warning"`
	}
	path := "/processes/" + p.ID + "/start?project_id=project-a"
	resp = processes.POST(path, map[string]any{"idempotency_key": "september", "inputs": "September records"}, &started)
	if resp.Status != 200 {
		t.Fatalf("start %d %s", resp.Status, resp.Body)
	}
	id := started.Task["id"].(string)
	if started.DeliveryWarning != "" || delivered.Load() != 1 || !strings.Contains(started.Task["description"].(string), "Procedure version: 1") {
		t.Fatal("snapshot or delivery missing")
	}
	processes.POST(path, map[string]any{"idempotency_key": "september", "inputs": "September records"}, &started)
	if started.Task["id"] != id {
		t.Fatal("duplicate run")
	}
	tasks.MCPAs("complete", map[string]any{"task_id": id, "result": "Approved report attached"}, 7, "opaque-owner", "project-a")
	var history struct {
		Runs []struct {
			Version int            `json:"version"`
			Task    map[string]any `json:"task"`
		} `json:"runs"`
	}
	resp = processes.GET("/processes/"+p.ID+"/runs?project_id=project-a", &history)
	if resp.Status != 200 || len(history.Runs) != 1 || history.Runs[0].Task["state"] != "completed" || history.Runs[0].Version != 1 {
		t.Fatalf("history %d %s", resp.Status, resp.Body)
	}
	// SDK must reject private tools when called through an agent connection.
	var denied map[string]any
	resp = tasks.RequestWithHeaders("POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "process_task", "arguments": map[string]any{"action": "list", "process_id": p.ID}}}, &denied, nil)
	if denied["error"] == nil {
		t.Fatalf("private bridge exposed: %s", resp.Body)
	}
}
