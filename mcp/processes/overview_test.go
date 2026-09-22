package main

import (
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
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

type globalOverviewPlatform struct {
	directPlatform
	projects     []sdk.PlatformProject
	agentProject string
}

func (f *globalOverviewPlatform) ListProjects() ([]sdk.PlatformProject, error) {
	return append([]sdk.PlatformProject(nil), f.projects...), nil
}

func (f *globalOverviewPlatform) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	project := f.agentProject
	if project == "" {
		project = "project-a"
	}
	return &sdk.PlatformInstance{ID: id, ProjectID: project, DefaultThreadID: "owner-default"}, nil
}

func createOverviewProcess(t *testing.T, a *App, project string) *Process {
	t.Helper()
	d := workflowDefinition()
	d.ExecutionMode = "agent"
	p, err := a.save(project, "", "operator", 0, d)
	if err != nil {
		t.Fatal(err)
	}
	c := AssignmentConfig{FollowLatest: true, Name: "Overview assignment", OwnerAgentID: d.OwnerAgentID, ExecutionMode: "agent", ProcedureVersion: p.Version, Parameters: map[string]any{}, Roles: map[string]Executor{"researcher": {Kind: "agent", AgentID: 8}, "writer": {Kind: "agent", AgentID: 7}, "reviewer": {Kind: "human"}, "publisher": {Kind: "agent", AgentID: 8}}}
	if _, err = a.db.Exec(`INSERT INTO process_assignments(id,process_id,body_json,status,created_at,updated_at) VALUES(?,?,?,'active',?,?)`, "assignment-"+p.ID, p.ID, jsonText(c), timestamp(), timestamp()); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`UPDATE process_versions SET body_json=? WHERE process_id=?`, jsonText(d), p.ID); err != nil {
		t.Fatal(err)
	}
	p, err = a.get(project, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func globalOverviewSetup(t *testing.T) (*App, *globalOverviewPlatform) {
	t.Helper()
	f := &globalOverviewPlatform{projects: []sdk.PlatformProject{{ID: "project-a", Name: "Alpha"}, {ID: "project-b", Name: "Beta"}}, agentProject: "project-a"}
	a := &App{}
	if err := a.OnMount(tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(f))); err != nil {
		t.Fatal(err)
	}
	alpha := createOverviewProcess(t, a, "project-a")
	if _, err := a.changeStatus("project-a", alpha.ID, "active"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.start("project-a", alpha.ID, "alpha-run", ""); err != nil {
		t.Fatal(err)
	}
	beta := createOverviewProcess(t, a, "project-b")
	f.agentProject = "project-b"
	if _, err := a.changeStatus("project-b", beta.ID, "active"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE process_assignments SET body_json=?,next_run_at=? WHERE process_id=?`, jsonText(AssignmentConfig{FollowLatest: true, Name: "Beta schedule", OwnerAgentID: 7, ExecutionMode: "agent", ProcedureVersion: beta.Version, Schedule: &Schedule{Kind: "interval", Every: "1h"}}), "2030-01-01T12:00:00Z", beta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO process_runs(id,process_id,version,kind,request_key,created_at,backend,state,assignment_id,assignment_json) VALUES(?,?,1,'manual',?,'2026-09-22T10:00:00Z','agent','completed',?,?)`, "beta-run", beta.ID, "beta-run", beta.Assignments[0].ID, jsonText(beta.Assignments[0].AssignmentConfig)); err != nil {
		t.Fatal(err)
	}
	return a, f
}

func TestGlobalOverviewAggregatesVisibleProjectsAndFilters(t *testing.T) {
	a, _ := globalOverviewSetup(t)
	// Keep a real run in a project omitted by ListProjects. An unfiltered
	// global response must not discover it merely because it shares the
	// global installation's database.
	secret := createOverviewProcess(t, a, "project-secret")
	if _, err := a.db.Exec(`INSERT INTO process_runs(id,process_id,version,kind,request_key,created_at,backend,state,assignment_id,assignment_json) VALUES(?,?,1,'manual',?,'2026-09-22T10:00:00Z','agent','running',?,?)`, "secret-run", secret.ID, "secret-run", secret.Assignments[0].ID, jsonText(secret.Assignments[0].AssignmentConfig)); err != nil {
		t.Fatal(err)
	}
	out, err := a.overviewGlobal("")
	if err != nil {
		t.Fatal(err)
	}
	if out.Scope != "global" || len(out.Projects) != 2 || out.Counts.Active != 1 || out.Counts.Scheduled != 1 || out.Counts.Recent != 1 {
		t.Fatalf("global overview did not aggregate visible projects: %+v", out)
	}
	if out.Active[0].ProjectID != "project-a" || out.Active[0].ProjectName != "Alpha" || out.Recent[0].ProjectID != "project-b" || out.Recent[0].ProjectName != "Beta" || out.Upcoming[0].ProjectID != "project-b" {
		t.Fatalf("global items lost project identity: active=%+v recent=%+v upcoming=%+v", out.Active, out.Recent, out.Upcoming)
	}
	for _, items := range [][]overviewItem{out.Active, out.Upcoming, out.Recent, out.Attention} {
		for _, item := range items {
			if item.ProjectID == "project-secret" {
				t.Fatalf("global overview leaked an inaccessible project: %+v", item)
			}
		}
	}
	selected, err := a.overviewGlobal("project-b")
	if err != nil || selected.Counts.Active != 0 || selected.Counts.Scheduled != 1 || selected.Counts.Recent != 1 || len(selected.Active) != 0 {
		t.Fatalf("project selector did not filter global overview: %+v err=%v", selected, err)
	}
	if w := overviewGET(t, a, "/overview", ""); w.Code != http.StatusOK {
		t.Fatalf("global HTTP overview status=%d body=%s", w.Code, w.Body.String())
	}
	if w := overviewGET(t, a, "/overview?project_id=project-b", ""); w.Code != http.StatusOK {
		t.Fatalf("global selected HTTP overview status=%d body=%s", w.Code, w.Body.String())
	}
	if w := overviewGET(t, a, "/overview?project_id=secret", ""); w.Code != http.StatusForbidden {
		t.Fatalf("global overview exposed an inaccessible selector: %d %s", w.Code, w.Body.String())
	}
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
