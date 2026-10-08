//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Legacy assignment values are normalized to native dispatch; no Tasks app is required.
func TestSidecarLegacyTasksAssignmentUsesNativeRun(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveExecutorAttachment(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/apps/callback/agents/7":
			json.NewEncoder(w).Encode(sdk.PlatformInstance{ID: 7, ProjectID: "project-a", DefaultThreadID: "owner-thread"})
		case "/api/apps/callback/agents/7/event":
			json.NewEncoder(w).Encode(sdk.AgentEventReceipt{Accepted: true, ExecutionID: "native-execution"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	app := tk.SpawnSidecar(t, ".", tk.WithProjectID("project-a"), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL))
	var p Process
	if resp := app.POST("/processes?project_id=project-a", map[string]any{"definition": def().procedureOnly()}, &p); resp.Status != 200 {
		t.Fatal(string(resp.Body))
	}
	x := sidecarAssignment(t, app, p, AssignmentConfig{Name: "Legacy", OwnerAgentID: 7, ExecutionMode: "tasks", FollowLatest: true})
	app.POST("/processes/"+p.ID+"/activate?project_id=project-a", map[string]any{}, &p)
	app.POST("/processes/"+p.ID+"/assignments/"+x.ID+"/activate?project_id=project-a", map[string]any{}, nil)
	var started struct {
		Run Run `json:"run"`
	}
	resp := app.POST("/processes/"+p.ID+"/start?project_id=project-a", map[string]any{"assignment_id": x.ID, "idempotency_key": "legacy"}, &started)
	if resp.Status != 200 || started.Run.DeliveredAt == "" {
		t.Fatalf("native dispatch: %s", resp.Body)
	}
}

// Processes alone: no Tasks sidecar or inter-app gateway exists.
func TestSidecarDirectWithoutTasks(t *testing.T) {
	var delivered atomic.Int32
	var interApp atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveExecutorAttachment(w, r) {
			return
		}
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
	resp := app.POST("/processes?project_id=project-a", map[string]any{"definition": d.procedureOnly()}, &p)
	if resp.Status != 200 {
		t.Fatalf("create %s", resp.Body)
	}
	if len(p.Assignments) != 0 || p.OwnerAgentID != 0 {
		t.Fatal("process creation assigned an agent")
	}
	initial := sidecarAssignment(t, app, p, AssignmentConfig{Name: "Primary", OwnerAgentID: 7, ExecutionMode: "agent", FollowLatest: true})
	resp = app.POST("/processes/"+p.ID+"/activate?project_id=project-a", map[string]any{}, &p)
	if resp.Status != 200 || p.SyncPending {
		t.Fatalf("activate %s", resp.Body)
	}
	resp = app.POST("/processes/"+p.ID+"/assignments/"+initial.ID+"/activate?project_id=project-a", map[string]any{}, nil)
	if resp.Status != 200 {
		t.Fatal(string(resp.Body))
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
		if serveExecutorAttachment(w, r) {
			return
		}
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
	resp := app.POST("/processes?project_id=project-a", map[string]any{"definition": d.procedureOnly()}, &p)
	if resp.Status != 200 {
		t.Fatal(string(resp.Body))
	}
	initial := sidecarAssignment(t, app, p, AssignmentConfig{Name: "Photography", OwnerAgentID: 7, ExecutionMode: "agent", FollowLatest: true})
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
	app.MCPAs("assignment_activate", map[string]any{"process_id": p.ID, "assignment_id": initial.ID}, 7, "operator-thread", "project-a")
	app.MCPAs("assignment_activate", map[string]any{"process_id": p.ID, "assignment_id": x.ID}, 7, "operator-thread", "project-a")
	var started struct {
		Run Run `json:"run"`
	}
	resp = app.POST(base+"/assignments/"+x.ID+"/start?project_id=project-a", map[string]any{"assignment_id": initial.ID, "idempotency_key": "today", "parameters": map[string]any{"page": "cooking-special"}}, &started)
	if resp.Status != 200 || started.Run.AssignmentID != x.ID || started.Run.Binding.OwnerAgentID != 8 || started.Run.Binding.Parameters["page"] != "cooking-special" {
		t.Fatalf("route or snapshot isolation: %s", resp.Body)
	}
	app.MCPAs("run_update", map[string]any{"process_id": p.ID, "run_id": started.Run.ID, "state": "completed", "result": "Patreon post URL"}, 8, "owner-thread", "project-a")
	app.MCPAs("start", map[string]any{"process_id": p.ID, "assignment_id": initial.ID, "idempotency_key": "today"}, 7, "owner-thread", "project-a")
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

func TestSidecarWorkflowAgentHandoffsAndGenericHumanStep(t *testing.T) {
	var delivered atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveExecutorAttachment(w, r) {
			return
		}
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
	resp := app.POST("/processes?project_id=project-a", map[string]any{"definition": workflowDefinition().procedureOnly()}, &p)
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
		app.MCPAs("step_get", map[string]any{"process_id": p.ID, "run_id": started.Run.ID, "step_id": detail.Steps[i].ID}, agent, detail.Steps[i].ThreadID, "project-a")
		app.MCPAs("step_update", map[string]any{"process_id": p.ID, "run_id": started.Run.ID, "step_id": detail.Steps[i].ID, "state": "completed", "output": "Evidence"}, agent, detail.Steps[i].ThreadID, "project-a")
		read()
	}
	read()
	if delivered.Load() != 2 || detail.Steps[2].State != "waiting" || detail.Steps[3].State != "pending" {
		t.Fatal("human step bypassed")
	}
	// Payload identifiers cannot override route scope.
	resp = app.POST(runpath+"/steps/"+detail.Steps[2].ID+"?project_id=project-a", map[string]any{"state": "completed", "output": "Reviewed draft", "step_id": detail.Steps[3].ID, "run_id": "other"}, nil)
	if resp.Status != 200 || delivered.Load() != 3 {
		t.Fatalf("human completion %s", resp.Body)
	}
	read()
	app.MCPAs("step_update", map[string]any{"process_id": p.ID, "run_id": started.Run.ID, "step_id": detail.Steps[3].ID, "state": "completed", "output": "Published URL"}, 8, detail.Steps[3].ThreadID, "project-a")
	read()
	if detail.Run.State != "completed" || detail.Steps[2].UpdatedBy != "operator" {
		t.Fatal("workflow did not complete", detail)
	}
}

// Standalone task tools were removed; real MCP discovery must match the manifest.
func TestSidecarDoesNotExposeRemovedTaskTools(t *testing.T) {
	app := tk.SpawnSidecar(t, ".", tk.WithProjectID("project-a"))
	var result struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	resp := app.RequestWithHeaders("POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}, &result, nil)
	if resp.Status != 200 || len(result.Result.Tools) == 0 {
		t.Fatalf("discovery: %s", resp.Body)
	}
	for _, tool := range result.Result.Tools {
		if strings.HasPrefix(tool.Name, "task") {
			t.Fatal("removed task tool exported", tool.Name)
		}
	}
}

func sidecarAssignment(t *testing.T, app *tk.Sidecar, p Process, c AssignmentConfig) Assignment {
	t.Helper()
	var x Assignment
	resp := app.POST("/processes/"+p.ID+"/assignments?project_id=project-a", map[string]any{"assignment": c}, &x)
	if resp.Status != 200 || x.ID == "" || x.Status != "paused" {
		t.Fatalf("create assignment: %s", resp.Body)
	}
	return x
}

// The real SDK now verifies the caller's current MCP attachment before delivery.
func serveExecutorAttachment(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/api/apps/callback/threads/spawn" {
		var req sdk.ThreadSpawnRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return true
		}
		if req.MCP != nil {
			http.Error(w, "worker excluded inherited domain tools", 400)
			return true
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sdk.ThreadSpawnResult{Status: "created", Thread: sdk.ThreadRef{AgentID: req.AgentID, ThreadID: req.ThreadID}})
		return true
	}
	if r.URL.Path != "/api/apps/callback/agent-tools/ensure-attached" {
		return false
	}
	var req sdk.EnsureAppToolsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sdk.EnsureAppToolsResult{AgentID: req.AgentID, Applied: true, MCPServerIDs: []int64{394}, AttachedInstallIDs: []int64{52804}})
	return true
}

func TestSidecarCompactHistoryCheckpointsAndLedger(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveExecutorAttachment(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/agents/7") {
			json.NewEncoder(w).Encode(sdk.PlatformInstance{ID: 7, ProjectID: "project-a", DefaultThreadID: "owner"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/event") {
			json.NewEncoder(w).Encode(sdk.AgentEventReceipt{Accepted: true, ExecutionID: "memory-test"})
			return
		}
		http.NotFound(w, r)
	}))
	defer gateway.Close()
	app := tk.SpawnSidecar(t, ".", tk.WithProjectID("project-a"), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL))
	var p Process
	app.POST("/processes?project_id=project-a", map[string]any{"definition": def().procedureOnly()}, &p)
	x := sidecarAssignment(t, app, p, AssignmentConfig{Name: "History", OwnerAgentID: 7, FollowLatest: true})
	base := "/processes/" + p.ID
	app.POST(base+"/activate?project_id=project-a", map[string]any{}, nil)
	app.POST(base+"/assignments/"+x.ID+"/activate?project_id=project-a", map[string]any{}, nil)
	for i := 0; i < 12; i++ {
		var started struct {
			Run Run `json:"run"`
		}
		app.POST(base+"/start?project_id=project-a", map[string]any{"idempotency_key": fmt.Sprint(i)}, &started)
		app.MCPAs("run_update", map[string]any{"process_id": p.ID, "run_id": started.Run.ID, "state": "completed", "result": strings.Repeat("receipt ", 4000)}, 7, "owner", "project-a")
		checkpoint := map[string]any{"process_id": p.ID, "run_id": started.Run.ID, "expected_revision": 0, "idempotency_key": "saved", "summary": map[string]any{"outcome": "Checked north", "references": []string{"catalog:9007199254740993"}}}
		result := app.MCPAs("summary_update", checkpoint, 7, "owner", "project-a")
		if strings.Contains(jsonText(result), `"isError":true`) {
			t.Fatal(result)
		}
		app.MCPAs("summary_update", checkpoint, 7, "owner", "project-a")
		app.MCPAs("memory_upsert", map[string]any{"process_id": p.ID, "run_id": started.Run.ID, "scope": "campaign-a", "key": fmt.Sprint("query:", i), "expected_revision": 0, "entry": map[string]any{"kind": "searched", "content": "North checked", "references": []string{"catalog:9007199254740993"}}}, 7, "owner", "project-a")
	}
	response := app.MCPAs("runs", map[string]any{"process_id": p.ID}, 7, "owner", "project-a")
	if len(jsonText(response)) > 16*1024 || strings.Contains(jsonText(response), "receipt receipt") {
		t.Fatal("MCP history oversized", len(jsonText(response)))
	}
	var compact struct {
		Runs    []HistoryRow `json:"runs"`
		HasMore bool         `json:"has_more"`
		Next    string       `json:"next_cursor"`
	}
	resp := app.GET(base+"/runs?project_id=project-a&view=compact", &compact)
	if resp.Status != 200 || len(compact.Runs) != 10 || !compact.HasMore || compact.Next == "" {
		t.Fatalf("UI compact page: %s", resp.Body)
	}
	var original struct {
		Runs []Run `json:"direct_runs"`
	}
	resp = app.GET(base+"/runs?project_id=project-a", &original)
	if resp.Status != 200 || len(original.Runs) != 12 || !strings.Contains(original.Runs[0].Result, "receipt") {
		t.Fatal("full HTTP inspection changed")
	}
	var ledger struct {
		Entries []SavedMemory `json:"entries"`
		HasMore bool          `json:"has_more"`
	}
	resp = app.GET(base+"/memory?project_id=project-a&assignment_id="+x.ID+"&scope=campaign-a", &ledger)
	if resp.Status != 200 || len(ledger.Entries) != 10 || !ledger.HasMore {
		t.Fatalf("ledger pagination: %s", resp.Body)
	}
	var denied map[string]any
	app.RequestWithHeaders("POST", "/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "summary_update", "arguments": map[string]any{"process_id": p.ID, "run_id": original.Runs[0].ID, "expected_revision": 1, "idempotency_key": "denied", "summary": map[string]any{"outcome": "Forged", "references": []string{}}}}}, &denied, map[string]string{"X-Apteva-Caller-Agent": "8", "X-Apteva-Caller-Thread": "other", "X-Apteva-Project-ID": "project-a"})
	if !strings.Contains(jsonText(denied), "only the run owner") {
		t.Fatal("unauthorized write accepted", denied)
	}
}
