//go:build integration

package main

import (
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// Real sidecar, real scheduler, real SQLite and HTTP delivery. The platform
// callback records agent notifications without invoking a model or sending mail.
func TestSidecarTimedNotificationSurvivesRestart(t *testing.T) {
	type delivery struct {
		at      time.Time
		request sdk.AgentEventRequest
	}
	events := make(chan delivery, 8)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/apps/callback/agents/7":
			json.NewEncoder(w).Encode(sdk.PlatformInstance{ID: 7, ProjectID: "project-a", DefaultThreadID: "owner-thread"})
		case "/api/apps/callback/agents/7/event":
			var request sdk.AgentEventRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			events <- delivery{time.Now(), request}
			json.NewEncoder(w).Encode(sdk.AgentEventReceipt{Accepted: true, ExecutionID: request.SourceEventID, SourceEventID: request.SourceEventID, ThreadID: request.ThreadID})
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	db := filepath.Join(t.TempDir(), "timing.db")
	spawn := func() *tk.Sidecar {
		return tk.SpawnSidecar(t, ".", tk.WithProjectID("project-a"), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL), tk.WithEnv("DB_PATH", db))
	}
	app := spawn()
	d := workflowDefinition().procedureOnly()
	d.Steps = d.Steps[:2]
	d.Steps[1].StartAfter = &TimingRule{After: "step_completed", StepKey: "research", Offset: 1, Unit: "minutes"}
	var p Process
	if resp := app.POST("/processes?project_id=project-a", map[string]any{"definition": d}, &p); resp.Status != 200 {
		t.Fatal(string(resp.Body))
	}
	x := sidecarAssignment(t, app, p, AssignmentConfig{Name: "Timer test", OwnerAgentID: 7, ExecutionMode: "agent", FollowLatest: true})
	base := "/processes/" + p.ID
	if resp := app.POST(base+"/activate?project_id=project-a", map[string]any{}, &p); resp.Status != 200 {
		t.Fatal(string(resp.Body))
	}
	app.MCPAs("assignment_activate", map[string]any{"process_id": p.ID, "assignment_id": x.ID}, 7, "owner-thread", "project-a")
	var started struct {
		Run Run `json:"run"`
	}
	if resp := app.POST(base+"/start?project_id=project-a", map[string]any{"assignment_id": x.ID, "idempotency_key": "one-minute"}, &started); resp.Status != 200 {
		t.Fatal(string(resp.Body))
	}
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("first notification missing")
	}
	var detail struct {
		Run   Run       `json:"run"`
		Steps []StepRun `json:"steps"`
	}
	runpath := base + "/runs/" + started.Run.ID + "?project_id=project-a"
	read := func() {
		t.Helper()
		if resp := app.GET(runpath, &detail); resp.Status != 200 || len(detail.Steps) != 2 {
			t.Fatal(string(resp.Body))
		}
	}
	read()
	app.MCPAs("step_update", map[string]any{"process_id": p.ID, "run_id": started.Run.ID, "step_id": detail.Steps[0].ID, "state": "completed", "output": "First action recorded"}, 7, "owner-thread", "project-a")
	read()
	saved := detail.Steps[1].StartAt
	first := detail.Steps[0].CompletedAt
	if detail.Run.State != "scheduled" || saved == "" {
		t.Fatal("timer not persisted")
	}
	app.Stop()
	app = spawn()
	read()
	if detail.Steps[1].StartAt != saved || detail.Steps[1].State != "scheduled" {
		t.Fatal("restart lost timer")
	}
	select {
	case <-events:
		t.Fatal("early notification after restart")
	default:
	}
	var next delivery
	select {
	case next = <-events:
	case <-time.After(75 * time.Second):
		t.Fatal("scheduler did not notify after restart")
	}
	due, err := time.Parse(time.RFC3339Nano, saved)
	if err != nil {
		t.Fatal(err)
	}
	if next.at.Before(due) || next.request.SourceEventID != "process-step:"+detail.Steps[1].ID {
		t.Fatal("early or incorrect notification")
	}
	completed, _ := time.Parse(time.RFC3339Nano, first)
	t.Logf("Follow-up notification arrived %.2fs after completion (%.2fs after due), across an app restart", next.at.Sub(completed).Seconds(), next.at.Sub(due).Seconds())
	app.MCPAs("step_update", map[string]any{"process_id": p.ID, "run_id": started.Run.ID, "step_id": detail.Steps[1].ID, "state": "completed", "output": "Follow-up action recorded"}, 7, "owner-thread", "project-a")
	read()
	if detail.Run.State != "completed" {
		t.Fatal("delayed workflow did not complete")
	}
}
