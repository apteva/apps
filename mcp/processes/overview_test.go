package main

import (
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func overviewGET(t *testing.T, a *App, path, project string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if project != "" {
		r.Header.Set("X-Apteva-Project-ID", project)
	}
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	for _, route := range a.HTTPRoutes() {
		mux.HandleFunc(route.Pattern, route.Handler)
	}
	mux.ServeHTTP(w, r)
	return w
}
func TestOverviewScopeAndLiveSteps(t *testing.T) {
	a, _, p, r := workflowSetup(t)
	for _, path := range []string{"/overview?project_id=project-a", "/processes/overview?project_id=project-a", "/processes/mobile/overview?project_id=project-a"} {
		w := overviewGET(t, a, path, "project-a")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var out processOverview
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		if out.Counts.Active != 1 || len(out.Active) != 1 || out.Active[0].StepsTotal != 4 || out.Active[0].Steps[0].Executor.AgentID != 8 {
			t.Fatalf("bad overview: %+v", out)
		}
	}
	for _, tc := range []struct{ path, header string }{{"/overview", ""}, {"/overview?project_id=other", ""}, {"/overview?project_id=other", "project-a"}} {
		if w := overviewGET(t, a, tc.path, tc.header); w.Code != 403 {
			t.Fatalf("scope accepted: %s", w.Body.String())
		}
	}
	finishStep(t, a, p, r, "research", "agent:8:worker", "notes", "")
	finishStep(t, a, p, r, "write", "agent:7:worker", "draft", "")
	out, e := a.overview("project-a")
	if e != nil {
		t.Fatal(e)
	}
	if out.Counts.Attention != 1 || out.Active[0].StepsCompleted != 2 {
		t.Fatalf("approval missing %+v", out)
	}
	other, e := a.overview("other")
	if e != nil || other.Counts.Active != 0 || len(other.Active) != 0 {
		t.Fatalf("scope leak %+v %v", other, e)
	}
}
func TestOverviewBoundedCountsAndSchedule(t *testing.T) {
	a, _, p, r := workflowSetup(t)
	for i := 0; i < 15; i++ {
		_, e := a.db.Exec(`INSERT INTO process_runs(id,process_id,version,kind,request_key,created_at,backend,state,assignment_id,assignment_json) VALUES(?,?,1,'manual',?,?,'agent','completed',?,?)`, fmt.Sprintf("old-%d", i), p.ID, fmt.Sprint(i), fmt.Sprintf("2026-09-%02dT12:00:00Z", i+1), r.AssignmentID, jsonText(r.Binding))
		if e != nil {
			t.Fatal(e)
		}
	}
	c := r.Binding
	c.Schedule = &Schedule{Kind: "interval", Every: "1h"}
	if _, e := a.db.Exec(`UPDATE process_assignments SET body_json=?,next_run_at='2026-09-14T12:00:00Z',status='active' WHERE id=?`, jsonText(c), r.AssignmentID); e != nil {
		t.Fatal(e)
	}
	out, e := a.overview("project-a")
	if e != nil {
		t.Fatal(e)
	}
	if out.Counts.Recent != 15 || len(out.Recent) != 12 || out.Recent[0].ID != "old-14" || out.Counts.Scheduled != 1 {
		t.Fatalf("counts/order %+v", out)
	}
	if _, e = a.db.Exec(`UPDATE process_assignments SET sync_pending=1,sync_error='test offline' WHERE id=?`, r.AssignmentID); e != nil {
		t.Fatal(e)
	}
	out, e = a.overview("project-a")
	if e != nil {
		t.Fatal(e)
	}
	if out.Counts.Scheduled != 0 || len(out.Attention) != 1 {
		t.Fatalf("sync issue treated as schedule %+v", out)
	}
}
func TestOverviewTasksOfflineDoesNotInventRunning(t *testing.T) {
	t.Skip("legacy Tasks overview removed in Processes 0.14")
	a, f := setup(t)
	p := create(t, a, def())
	p = status(t, a, p.ID, "active")
	if _, e := a.start(p.ProjectID, p.ID, "once", ""); e != nil {
		t.Fatal(e)
	}
	f.fail = "list"
	out, e := a.overview("project-a")
	if e != nil {
		t.Fatal(e)
	}
	if !out.Partial || out.Counts.Active != 0 || len(out.Warnings) == 0 {
		t.Fatalf("stale Tasks record reported as live %+v", out)
	}
}

func TestOverviewTasksHistoryUsesLiveStateAndSeparatesSchedules(t *testing.T) {
	t.Skip("legacy Tasks overview removed in Processes 0.14")
	a, f := setup(t)
	d := def()
	d.Schedule = &Schedule{Kind: "interval", Every: "1h"}
	p := create(t, a, d)
	p = status(t, a, p.ID, "active")
	raw, e := a.start(p.ProjectID, p.ID, "once", "")
	if e != nil {
		t.Fatal(e)
	}
	run := raw.(map[string]any)["run"].(Run)
	records, e := a.dispatches(p.ID)
	if e != nil {
		t.Fatal(e)
	}
	schedule := ""
	for _, r := range records {
		if r.Kind == "schedule" {
			schedule = r.ID
		}
	}
	f.overviewHistory = []any{
		map[string]any{"run_key": run.ID, "version": 1, "task": map[string]any{"id": "manual-task", "state": "completed", "agent_id": 7}},
		map[string]any{"run_key": schedule, "version": 1, "task": map[string]any{"id": "schedule-task", "state": "queued", "schedule_kind": "interval", "schedule_enabled": true, "next_run_at": "2026-09-15T12:00:00Z", "agent_id": 7}},
		map[string]any{"run_key": schedule, "version": 1, "task": map[string]any{"id": "occurrence", "state": "blocked", "agent_id": 7}},
		map[string]any{"run_key": "unowned", "version": 1, "task": map[string]any{"id": "foreign", "state": "running"}},
	}
	out, e := a.overview("project-a")
	if e != nil {
		t.Fatal(e)
	}
	if out.Counts.Active != 1 || out.Counts.Recent != 1 || out.Counts.Scheduled != 1 || out.Counts.Attention != 1 || out.Active[0].ID != "occurrence" {
		t.Fatalf("bad live history %+v", out)
	}
}
func TestOverviewNativeSurface(t *testing.T) {
	raw, e := os.ReadFile("ui/surfaces/process-overview.json")
	if e != nil {
		t.Fatal(e)
	}
	surface, e := sdk.ParseNativeSurface(raw)
	if e != nil {
		t.Fatal(e)
	}
	if surface.DataSources["summary"].Request.Path != "/processes/mobile/overview" {
		t.Fatal("native endpoint diverged")
	}
}

func TestOverviewDoesNotLetBlockedRunsHideRunningWork(t *testing.T) {
	a, _, p, r := workflowSetup(t)
	for i := 0; i < 15; i++ {
		_, e := a.db.Exec(`INSERT INTO process_runs(id,process_id,version,kind,request_key,created_at,backend,state,assignment_id,assignment_json) VALUES(?,?,1,'manual',?,'2030-01-01T00:00:00Z','agent','blocked',?,?)`, fmt.Sprintf("blocked-%d", i), p.ID, fmt.Sprintf("blocked-%d", i), r.AssignmentID, jsonText(r.Binding))
		if e != nil {
			t.Fatal(e)
		}
	}
	out, e := a.overview("project-a")
	if e != nil {
		t.Fatal(e)
	}
	if len(out.Active) != 12 || out.Active[0].ID != r.ID {
		t.Fatalf("live work hidden by blocked history: %+v", out.Active)
	}
}
