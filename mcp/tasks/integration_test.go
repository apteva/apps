//go:build integration

package main

// Tier 2 boots the real sidecar binary and exercises the v3 MCP + HTTP
// contract. Durable scheduling and thread delivery use a recording platform in
// the default unit suite; this test owns process boot, migrations, trusted
// caller propagation, persistence, and route shapes.
//
// Run with: go test -tags integration ./...

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

const (
	testProject = "test-proj"
	testThread  = "opaque-thread-a7"
)

func TestSidecar_BootsAndHealthOK(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(testProject))
	var got map[string]any
	resp := sc.GET("/health", &got)
	if resp.Status != 200 || got["ok"] != true {
		t.Fatalf("health status=%d body=%s", resp.Status, resp.Body)
	}
}

func TestSidecar_DurableLifecycleAcrossMCPAndHTTP(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(testProject))

	created := sc.MCPAs("create", map[string]any{
		"title": "Ship social v3", "description": "Verify the durable app boundary",
	}, 7, testThread, testProject)
	task, ok := created["task"].(map[string]any)
	if !ok {
		t.Fatalf("create response missing task: %#v", created)
	}
	id, _ := task["id"].(string)
	if id == "" || task["created_by_thread_id"] != testThread || task["assigned_thread_id"] != testThread {
		t.Fatalf("trusted caller provenance missing: %#v", task)
	}

	updated := sc.MCPAs("update", map[string]any{
		"task_id": id, "state": "running", "progress": 40, "current_step": "Checking persistence",
	}, 7, testThread, testProject)
	if updated["state"] != "running" || updated["current_step"] != "Checking persistence" || updated["execution_thread_id"] != testThread {
		t.Fatalf("update response=%#v", updated)
	}

	var detail struct {
		Task   Task        `json:"task"`
		Events []TaskEvent `json:"events"`
	}
	path := "/tasks/" + url.PathEscape(id) + "?project_id=" + url.QueryEscape(testProject)
	resp := sc.GET(path, &detail)
	if resp.Status != 200 || detail.Task.ID != id || len(detail.Events) != 2 {
		t.Fatalf("detail status=%d task=%+v events=%+v body=%s", resp.Status, detail.Task, detail.Events, resp.Body)
	}

	done := sc.MCPAs("complete", map[string]any{
		"task_id": id, "result": "Durable app boundary verified",
	}, 7, testThread, testProject)
	if done["state"] != "completed" || done["progress"] != float64(100) || done["current_step"] != "Completed" || done["result"] != "Durable app boundary verified" {
		t.Fatalf("complete response=%#v", done)
	}
	resp = sc.GET(path, &detail)
	if resp.Status != 200 || len(detail.Events) != 3 || detail.Events[2].Data["progress"] != float64(100) || detail.Events[2].Data["current_step"] != "Completed" {
		t.Fatalf("completion detail status=%d events=%+v body=%s", resp.Status, detail.Events, resp.Body)
	}

	var listed struct {
		Tasks  []Task     `json:"tasks"`
		Counts TaskCounts `json:"counts"`
	}
	listPath := "/tasks?project_id=" + url.QueryEscape(testProject)
	resp = sc.GET(listPath, &listed)
	if resp.Status != 200 || len(listed.Tasks) != 1 || listed.Counts.Completed != 1 {
		t.Fatalf("list status=%d payload=%+v body=%s", resp.Status, listed, resp.Body)
	}
}

func TestSidecar_RejectsMissingTrustedCallerAndUnknownTool(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(testProject))
	if _, err := sc.MCPRaw("tools/call", map[string]any{
		"name": "create", "arguments": map[string]any{"title": "untrusted"},
	}); err == nil {
		t.Fatal("expected create without trusted caller to fail")
	}
	got, err := sc.MCPRaw("tools/call", map[string]any{
		"name": "does_not_exist", "arguments": map[string]any{},
	})
	if err == nil && got["error"] == nil {
		t.Errorf("expected unknown tool rejection, got %#v", got)
	}
}

func TestSidecar_ListIsAgentWideAcrossThreads(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(testProject))
	for _, item := range []struct {
		thread string
		title  string
	}{
		{thread: "opaque-default", title: "Default-thread work"},
		{thread: "conversation-a", title: "Conversation-created work"},
	} {
		sc.MCPAs("create", map[string]any{"title": item.title}, 7, item.thread, testProject)
	}
	sc.MCPAs("create", map[string]any{"title": "Other-agent work"}, 8, "other-default", testProject)

	listed := sc.MCPAs("list", map[string]any{}, 7, "conversation-reader", testProject)
	tasks, ok := listed["tasks"].([]any)
	if !ok || len(tasks) != 2 {
		t.Fatalf("agent-wide list from unrelated thread=%#v", listed)
	}
	for _, value := range tasks {
		task, _ := value.(map[string]any)
		if task["agent_id"] != float64(7) {
			t.Fatalf("cross-agent task leaked into inventory: %#v", task)
		}
	}
}

func TestSidecar_CrossProjectHTTPIsolation(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(testProject))
	created := sc.MCPAs("create", map[string]any{"title": "Scoped"}, 7, testThread, testProject)
	task := created["task"].(map[string]any)
	id := task["id"].(string)
	resp := sc.GET("/tasks/"+url.PathEscape(id)+"?project_id=other", nil)
	if resp.Status != 403 {
		t.Fatalf("cross-project status=%d body=%s", resp.Status, resp.Body)
	}
}

func TestSidecar_ServesNativeSurfacesAndMobileSummary(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(testProject))
	for _, path := range []string{"/ui/surfaces/task-overview.json", "/ui/surfaces/tasks.json"} {
		response := sc.GET(path, nil)
		if response.Status != http.StatusOK {
			t.Fatalf("surface %s status=%d body=%s", path, response.Status, response.Body)
		}
		if _, err := sdk.ParseNativeSurface(response.Body); err != nil {
			t.Fatalf("surface %s: %v", path, err)
		}
	}

	sc.MCPAs("create", map[string]any{"title": "Visible in native summary"}, 7, testThread, testProject)
	request, err := http.NewRequest(http.MethodGet, sc.URL()+"/mobile/summary?project_id=other", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+sc.Token())
	request.Header.Set("X-Apteva-Project-ID", testProject)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var summary mobileTaskSummary
	if err := json.NewDecoder(response.Body).Decode(&summary); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || summary.Counts.Active != 1 || len(summary.Active) != 1 || summary.Active[0].ProjectID != testProject {
		t.Fatalf("summary status=%d payload=%+v", response.StatusCode, summary)
	}
}
