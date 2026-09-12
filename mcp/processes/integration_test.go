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
			Version      int            `json:"version"`
			AssignmentID string         `json:"assignment_id"`
			Task         map[string]any `json:"task"`
		} `json:"runs"`
	}
	resp = processes.GET("/processes/"+p.ID+"/runs?project_id=project-a", &history)
	if resp.Status != 200 || len(history.Runs) != 1 || history.Runs[0].Task["state"] != "completed" || history.Runs[0].Version != 1 || history.Runs[0].AssignmentID != "assignment-"+p.ID {
		t.Fatalf("history %d %s", resp.Status, resp.Body)
	}
	// SDK must reject private tools when called through an agent connection.
	var denied map[string]any
	resp = tasks.RequestWithHeaders("POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "process_task", "arguments": map[string]any{"action": "list", "process_id": p.ID}}}, &denied, nil)
	if denied["error"] == nil {
		t.Fatalf("private bridge exposed: %s", resp.Body)
	}
}

// Processes alone: no Tasks sidecar or inter-app gateway exists.
func TestSidecarDirectWithoutTasks(t *testing.T) {
	var delivered atomic.Int32
	var interApp atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/apps/callback/agents/7":
			json.NewEncoder(w).Encode(sdk.PlatformInstance{ID: 7, ProjectID: "project-a", DefaultThreadID: "owner-thread"})
		case "/api/apps/callback/agents/7/event":
			var request sdk.AgentEventRequest
			json.NewDecoder(r.Body).Decode(&request)
			delivered.Add(1)
			json.NewEncoder(w).Encode(sdk.AgentEventReceipt{Accepted: true, ExecutionID: "direct-execution", SourceEventID: request.SourceEventID, ThreadID: request.ThreadID})
		default:
			if strings.HasPrefix(r.URL.Path, "/api/apps/callback/apps/") {
				interApp.Add(1)
			}
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	app := tk.SpawnSidecar(t, ".", tk.WithProjectID("project-a"), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL))
	d := def()
	d.ExecutionMode = "agent"
	var p Process
	resp := app.POST("/processes?project_id=project-a", map[string]any{"definition": d}, &p)
	if resp.Status != 200 {
		t.Fatalf("create %s", resp.Body)
	}
	resp = app.POST("/processes/"+p.ID+"/activate?project_id=project-a", map[string]any{}, &p)
	if resp.Status != 200 || p.SyncPending {
		t.Fatalf("activate %s", resp.Body)
	}
	var started struct {
		Run Run `json:"run"`
	}
	path := "/processes/" + p.ID + "/start?project_id=project-a"
	resp = app.POST(path, map[string]any{"idempotency_key": "direct"}, &started)
	if resp.Status != 200 || started.Run.DeliveredAt == "" {
		t.Fatalf("start %s", resp.Body)
	}
	app.POST(path, map[string]any{"idempotency_key": "direct"}, &started)
	if delivered.Load() != 1 {
		t.Fatal("duplicate delivery")
	}
	app.MCPAs("run_get", map[string]any{"process_id": p.ID, "run_id": started.Run.ID}, 7, "owner-thread", "project-a")
	app.MCPAs("run_update", map[string]any{"process_id": p.ID, "run_id": started.Run.ID, "state": "completed", "result": "Approved report attached"}, 7, "owner-thread", "project-a")
	var history struct {
		Direct []Run `json:"direct_runs"`
	}
	resp = app.GET("/processes/"+p.ID+"/runs?project_id=project-a", &history)
	if resp.Status != 200 || len(history.Direct) != 1 || history.Direct[0].State != "completed" || interApp.Load() != 0 {
		t.Fatalf("direct history %s; inter-app=%d", resp.Body, interApp.Load())
	}
}

func TestSidecarAssignmentsAndParameterIsolation(t *testing.T) {
	var delivered atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/apps/callback/agents/7" || r.URL.Path == "/api/apps/callback/agents/8" {
			id := int64(7)
			if strings.HasSuffix(r.URL.Path, "/8") {
				id = 8
			}
			json.NewEncoder(w).Encode(sdk.PlatformInstance{ID: id, ProjectID: "project-a", DefaultThreadID: "owner-thread"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/event") {
			var request sdk.AgentEventRequest
			json.NewDecoder(r.Body).Decode(&request)
			delivered.Add(1)
			json.NewEncoder(w).Encode(sdk.AgentEventReceipt{Accepted: true, ExecutionID: request.SourceEventID, SourceEventID: request.SourceEventID, ThreadID: request.ThreadID})
			return
		}
		http.NotFound(w, r)
	}))
	defer gateway.Close()
	app := tk.SpawnSidecar(t, ".", tk.WithProjectID("project-a"), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL))
	d := def()
	d.ExecutionMode = "agent"
	d.Parameters = []Parameter{{Key: "page", Type: "string", Default: "default-page"}}
	var p Process
	resp := app.POST("/processes?project_id=project-a", map[string]any{"definition": d}, &p)
	if resp.Status != 200 {
		t.Fatal(string(resp.Body))
	}
	base := "/processes/" + p.ID
	var x Assignment
	resp = app.POST(base+"/assignments?project_id=project-a", map[string]any{"assignment": AssignmentConfig{Name: "Cooking", OwnerAgentID: 8, ExecutionMode: "agent", FollowLatest: true, Parameters: map[string]any{"page": "cooking"}}}, &x)
	if resp.Status != 200 || x.ID == "" {
		t.Fatal(string(resp.Body))
	}
	resp = app.POST(base+"/activate?project_id=project-a", map[string]any{}, &p)
	if resp.Status != 200 {
		t.Fatal(string(resp.Body))
	}
	app.MCPAs("assignment_activate", map[string]any{"process_id": p.ID, "assignment_id": x.ID}, 7, "operator-thread", "project-a")
	var started struct {
		Run Run `json:"run"`
	}
	resp = app.POST(base+"/assignments/"+x.ID+"/start?project_id=project-a", map[string]any{"assignment_id": "assignment-" + p.ID, "idempotency_key": "today", "parameters": map[string]any{"page": "cooking-special"}}, &started)
	if resp.Status != 200 || started.Run.AssignmentID != x.ID || started.Run.Binding.OwnerAgentID != 8 || started.Run.Binding.Parameters["page"] != "cooking-special" {
		t.Fatalf("route or snapshot isolation: %s", resp.Body)
	}
	app.MCPAs("run_update", map[string]any{"process_id": p.ID, "run_id": started.Run.ID, "state": "completed", "result": "Patreon post URL"}, 8, "owner-thread", "project-a")
	app.MCPAs("start", map[string]any{"process_id": p.ID, "assignment_id": "assignment-" + p.ID, "idempotency_key": "today"}, 7, "owner-thread", "project-a")
	var history struct {
		Direct []Run `json:"direct_runs"`
	}
	resp = app.GET(base+"/runs?project_id=project-a", &history)
	if resp.Status != 200 || len(history.Direct) != 2 || delivered.Load() != 2 {
		t.Fatalf("history %s", resp.Body)
	}
	var saved Assignment
	app.GET(base+"/assignments/"+x.ID+"?project_id=project-a", &saved)
	if saved.Parameters["page"] != "cooking" {
		t.Fatal("run override mutated assignment")
	}
}

func TestSidecarWorkflowAgentHandoffsAndHumanApproval(t *testing.T) {
	var delivered atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/apps/callback/agents/7" || r.URL.Path == "/api/apps/callback/agents/8" {
			id := int64(7)
			if strings.HasSuffix(r.URL.Path, "/8") {
				id = 8
			}
			json.NewEncoder(w).Encode(sdk.PlatformInstance{ID: id, ProjectID: "project-a", DefaultThreadID: "owner-thread"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/event") {
			var input sdk.AgentEventRequest
			json.NewDecoder(r.Body).Decode(&input)
			delivered.Add(1)
			json.NewEncoder(w).Encode(sdk.AgentEventReceipt{Accepted: true, ExecutionID: input.SourceEventID, SourceEventID: input.SourceEventID, ThreadID: input.ThreadID})
			return
		}
		http.NotFound(w, r)
	}))
	defer gateway.Close()
	app := tk.SpawnSidecar(t, ".", tk.WithProjectID("project-a"), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL))
	var p Process
	resp := app.POST("/processes?project_id=project-a", map[string]any{"definition": workflowDefinition()}, &p)
	if resp.Status != 200 {
		t.Fatal(string(resp.Body))
	}
	base := "/processes/" + p.ID
	var x Assignment
	resp = app.POST(base+"/assignments?project_id=project-a", map[string]any{"assignment": AssignmentConfig{Name: "Team", OwnerAgentID: 7, ExecutionMode: "agent", FollowLatest: true, Roles: map[string]Executor{"researcher": {Kind: "agent", AgentID: 8}, "writer": {Kind: "agent", AgentID: 7}, "reviewer": {Kind: "human"}, "publisher": {Kind: "agent", AgentID: 8}}}}, &x)
	if resp.Status != 200 {
		t.Fatal(string(resp.Body))
	}
	app.POST(base+"/activate?project_id=project-a", map[string]any{}, &p)
	app.MCPAs("assignment_activate", map[string]any{"process_id": p.ID, "assignment_id": x.ID}, 7, "owner-thread", "project-a")
	var started struct {
		Run Run `json:"run"`
	}
	resp = app.POST(base+"/start?project_id=project-a", map[string]any{"assignment_id": x.ID, "idempotency_key": "team-day"}, &started)
	if resp.Status != 200 || delivered.Load() != 1 {
		t.Fatalf("start %s", resp.Body)
	}
	var detail struct {
		Run   Run       `json:"run"`
		Steps []StepRun `json:"steps"`
	}
	runpath := base + "/runs/" + started.Run.ID
	read := func() {
		t.Helper()
		resp := app.GET(runpath+"?project_id=project-a", &detail)
		if resp.Status != 200 || len(detail.Steps) != 4 {
			t.Fatalf("detail %s", resp.Body)
		}
	}
	read()
	for i, agent := range []int64{8, 7} {
		app.MCPAs("step_get", map[string]any{"process_id": p.ID, "run_id": started.Run.ID, "step_id": detail.Steps[i].ID}, agent, "owner-thread", "project-a")
		app.MCPAs("step_update", map[string]any{"process_id": p.ID, "run_id": started.Run.ID, "step_id": detail.Steps[i].ID, "state": "completed", "output": "Evidence"}, agent, "owner-thread", "project-a")
	}
	read()
	if delivered.Load() != 2 || detail.Steps[2].State != "waiting" || detail.Steps[3].State != "pending" {
		t.Fatal("human gate bypassed")
	}
	// Payload identifiers cannot override route scope.
	resp = app.POST(runpath+"/steps/"+detail.Steps[2].ID+"?project_id=project-a", map[string]any{"state": "completed", "decision": "approved", "output": "Approved draft", "step_id": detail.Steps[3].ID, "run_id": "other"}, nil)
	if resp.Status != 200 || delivered.Load() != 3 {
		t.Fatalf("approval %s", resp.Body)
	}
	app.MCPAs("step_update", map[string]any{"process_id": p.ID, "run_id": started.Run.ID, "step_id": detail.Steps[3].ID, "state": "completed", "output": "Published URL"}, 8, "owner-thread", "project-a")
	read()
	if detail.Run.State != "completed" || detail.Steps[2].Decision != "approved" || detail.Steps[2].UpdatedBy != "operator" {
		t.Fatal("workflow did not complete", detail)
	}
}
