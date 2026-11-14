//go:build integration

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecarCheckpointWaitTimerReleasesAndResumesAfterRestart(t *testing.T) {
	type delivery struct {
		at      time.Time
		request sdk.AgentEventRequest
	}
	events := make(chan delivery, 8)
	var kills atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/apps/callback/threads/") && r.Method == http.MethodDelete {
			kills.Add(1)
		}
		if serveExecutorAttachment(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/apps/callback/agents/7":
			json.NewEncoder(w).Encode(sdk.PlatformInstance{ID: 7, ProjectID: "project-a", DefaultThreadID: "main"})
		case "/api/apps/callback/agents/7/event":
			var req sdk.AgentEventRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			events <- delivery{time.Now(), req}
			json.NewEncoder(w).Encode(sdk.AgentEventReceipt{Accepted: true, ExecutionID: req.SourceEventID, SourceEventID: req.SourceEventID, ThreadID: req.ThreadID})
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	db := filepath.Join(t.TempDir(), "checkpoint-wait.db")
	spawn := func() *tk.Sidecar {
		return tk.SpawnSidecar(t, ".", tk.WithProjectID("project-a"), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL), tk.WithEnv("DB_PATH", db))
	}
	app := spawn()
	d := workflowDefinition().procedureOnly()
	d.Steps = d.Steps[:1]
	var p Process
	if resp := app.POST("/processes?project_id=project-a", map[string]any{"definition": d}, &p); resp.Status != 200 {
		t.Fatal(string(resp.Body))
	}
	x := sidecarAssignment(t, app, p, AssignmentConfig{Name: "Checkpoint wait", OwnerAgentID: 7, WorkerContinuity: "per_executor", ExecutionMode: "agent", FollowLatest: true})
	base := "/processes/" + p.ID
	app.POST(base+"/activate?project_id=project-a", map[string]any{}, nil)
	app.MCPAs("assignment_activate", map[string]any{"process_id": p.ID, "assignment_id": x.ID}, 7, "main", "project-a")
	var started struct {
		Run Run `json:"run"`
	}
	if resp := app.POST(base+"/start?project_id=project-a", map[string]any{"assignment_id": x.ID, "idempotency_key": "checkpoint-wait"}, &started); resp.Status != 200 {
		t.Fatal(string(resp.Body))
	}
	select {
	case <-events:
	case <-time.After(3 * time.Second):
		t.Fatal("initial step missing")
	}
	var detail struct {
		Run   Run       `json:"run"`
		Steps []StepRun `json:"steps"`
	}
	read := func() {
		t.Helper()
		detail.Run = Run{}
		detail.Steps = nil
		if resp := app.GET(base+"/runs/"+started.Run.ID+"?project_id=project-a", &detail); resp.Status != 200 || len(detail.Steps) != 1 {
			t.Fatal(string(resp.Body))
		}
	}
	read()
	first := detail.Steps[0]
	app.MCPAs("step_claim", map[string]any{"process_id": p.ID, "run_id": started.Run.ID, "step_id": first.ID}, 7, first.ThreadID, "project-a")
	due := time.Now().UTC().Add(10 * time.Second)
	app.MCPAs("step_update", map[string]any{"process_id": p.ID, "run_id": started.Run.ID, "step_id": first.ID, "state": "waiting", "output": "accepted-mail-id-123; next action is read reply, do not resend", "error": "follow-up delay", "wait": map[string]any{"kind": "timer", "wake_at": due.Format(time.RFC3339Nano)}}, 7, first.ThreadID, "project-a")
	read()
	if detail.Steps[0].Wait == nil || detail.Run.State != "blocked" || !detail.Steps[0].DeliverySuspended {
		t.Fatal("checkpoint timer not saved", detail)
	}
	until := time.Now().Add(3 * time.Second)
	for kills.Load() == 0 && time.Now().Before(until) {
		time.Sleep(10 * time.Millisecond)
	}
	if kills.Load() != 1 {
		t.Fatal("waiting worker was not released")
	}
	app.Stop()
	app = spawn()
	read()
	if detail.Steps[0].Wait == nil || detail.Steps[0].Wait.WakeAt != due.Format(time.RFC3339Nano) {
		t.Fatal("restart changed timer")
	}
	var next delivery
	select {
	case next = <-events:
	case <-time.After(time.Until(due) + 5*time.Second):
		t.Fatal("checkpoint timer did not dispatch")
	}
	read()
	resumed := detail.Steps[0]
	if next.at.Before(due) || resumed.ThreadID == first.ThreadID || resumed.Output != "accepted-mail-id-123; next action is read reply, do not resend" || resumed.Wait != nil {
		t.Fatal("timer resumed incorrectly", resumed, next.at, due)
	}
	app.MCPAs("step_claim", map[string]any{"process_id": p.ID, "run_id": started.Run.ID, "step_id": first.ID}, 7, resumed.ThreadID, "project-a")
	app.MCPAs("step_update", map[string]any{"process_id": p.ID, "run_id": started.Run.ID, "step_id": first.ID, "state": "completed", "output": "verified reply to accepted-mail-id-123"}, 7, resumed.ThreadID, "project-a")
	read()
	if detail.Run.State != "completed" {
		t.Fatal("resumed step failed to finish")
	}
	t.Logf("Worker released before restart; exact checkpoint timer resumed %.3fs after deadline", next.at.Sub(due).Seconds())
}
