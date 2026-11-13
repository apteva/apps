package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

var dashboardNow = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func dashboardTestSuite(t *testing.T, s store, name string) int64 {
	t.Helper()
	v, err := s.saveSuite(Suite{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return v.ID
}
func dashboardTestRun(t *testing.T, s store, suite int64, environment, status string, cancel bool, at time.Time) int64 {
	t.Helper()
	stamp := at.Format(time.RFC3339Nano)
	end := ""
	if status == "passed" || status == "failed" {
		end = at.Add(2 * time.Second).Format(time.RFC3339Nano)
	}
	r, err := s.db.Exec(`INSERT INTO test_runs(project_id,suite_id,suite_name,environment,status,trigger,snapshot,passed,failed,created_at,started_at,finished_at,cancel_requested) VALUES(?,?,'Saved suite name',?,?,'tool','[]',?,?,?,?,?,?)`, s.project, suite, environment, status, map[bool]int{true: 1}[status == "passed"], map[bool]int{true: 1}[status == "failed" && !cancel], stamp, stamp, end, cancel)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	return id
}
func TestDashboardCountsFullWindowAndScopesFilters(t *testing.T) {
	_, _, s := fixture(t)
	suite := dashboardTestSuite(t, s, "Browser acceptance")
	for i := 0; i < 125; i++ {
		dashboardTestRun(t, s, suite, "stage", "passed", false, dashboardNow.Add(-time.Hour))
	}
	dashboardTestRun(t, s, suite, "stage", "failed", false, dashboardNow.Add(-2*time.Hour))
	dashboardTestRun(t, s, suite, "stage", "failed", true, dashboardNow.Add(-3*time.Hour))
	dashboardTestRun(t, s, suite, "stage", "running", true, dashboardNow.Add(-time.Minute))
	dashboardTestRun(t, s, suite, "stage", "queued", false, dashboardNow.Add(-8*24*time.Hour))
	dashboardTestRun(t, s, suite, "stage", "failed", false, dashboardNow.Add(-24*time.Hour-time.Second))
	dashboardTestRun(t, s, suite, "stage", "failed", false, dashboardNow.Add(time.Hour))
	dashboardTestRun(t, s, suite, "prod", "failed", false, dashboardNow.Add(-time.Hour))
	other := store{s.db, "project-b"}
	foreign := dashboardTestSuite(t, other, "Private foreign suite")
	dashboardTestRun(t, other, foreign, "secret environment", "failed", false, dashboardNow.Add(-time.Hour))
	out, err := s.dashboard(context.Background(), dashboardFilter{Hours: 24, SuiteID: suite, Environment: "stage", Limit: 3}, false, dashboardNow)
	if err != nil {
		t.Fatal(err)
	}
	if out.Stats.Runs != 128 || out.Stats.Passed != 125 || out.Stats.Failed != 1 || out.Stats.Cancelled != 1 || out.Stats.Running != 1 || out.Stats.Queued != 1 {
		t.Fatal(out.Stats)
	}
	if out.Stats.PassRate == nil || *out.Stats.PassRate != 100*float64(125)/126 {
		t.Fatal("cancelled/running runs changed pass rate", out.Stats)
	}
	if len(out.Recent) != 3 || out.Recent[0].Status != "cancelling" {
		t.Fatal(out.Recent)
	}
	if out.Recent[1].DurationMS == nil || *out.Recent[1].DurationMS != 2000 {
		t.Fatal("duration not based on run timestamps", out.Recent[1])
	}
	total := 0
	for _, d := range out.Trend {
		total += d.Runs
	}
	if total != 128 {
		t.Fatal(out.Trend)
	}
	raw := encode(out)
	if strings.Contains(raw, "Private foreign") || strings.Contains(raw, "secret environment") {
		t.Fatal("cross-project dashboard leak", raw)
	}
	out, err = s.dashboard(context.Background(), dashboardFilter{Hours: 168, SuiteID: suite, Environment: "stage", Limit: 10}, false, dashboardNow)
	if err != nil || out.Stats.Failed != 2 || len(out.Trend) != 8 {
		t.Fatal(out, err)
	}
}
func TestDashboardEmptyAndCancelledAreNotPassing(t *testing.T) {
	_, _, s := fixture(t)
	suite := dashboardTestSuite(t, s, "Empty")
	for _, cancel := range []bool{false, true} {
		if cancel {
			dashboardTestRun(t, s, suite, "test", "failed", true, dashboardNow.Add(-time.Hour))
		}
		out, err := s.dashboard(context.Background(), dashboardFilter{Hours: 24, Limit: 3}, false, dashboardNow)
		if err != nil || out.Stats.PassRate != nil || out.Stats.Passed != 0 {
			t.Fatal(out, err)
		}
	}
}
func TestDashboardFailureGroupingEvidenceAndRunErrors(t *testing.T) {
	_, app, s := fixture(t)
	suite := dashboardTestSuite(t, s, "Checkout")
	addFailure := func(environment string, offset time.Duration) int64 {
		run := dashboardTestRun(t, s, suite, environment, "failed", false, dashboardNow.Add(offset))
		output := `{"steps":[{"id":"parallel","status":"failed","parallel":[{"id":"save","status":"failed","assertions":[{"path":"/count","op":"equals","passed":false,"value":"private assertion value"}],"evidence":{"token":"secret evidence"}}]}],"browser":{"secret":"private browser"}}`
		if err := s.saveResult(run, Result{CheckID: 42, Name: "Save sale", Kind: "scenario", Status: "failed", Output: json.RawMessage(output), Assertions: []AssertionResult{}}); err != nil {
			t.Fatal(err)
		}
		return run
	}
	addFailure("prod", -3*time.Hour)
	latest := addFailure("prod", -2*time.Hour)
	addFailure("stage", -time.Hour)
	cancelled := dashboardTestRun(t, s, suite, "prod", "failed", true, dashboardNow.Add(-30*time.Minute))
	if err := s.saveResult(cancelled, Result{CheckID: 42, Name: "Cancelled", Kind: "scenario", Status: "error", Output: map[string]any{}, Assertions: []AssertionResult{}}); err != nil {
		t.Fatal(err)
	}
	startup := dashboardTestRun(t, s, suite, "prod", "failed", false, dashboardNow.Add(-10*time.Minute))
	s.db.Exec(`UPDATE test_runs SET error='Environment unavailable' WHERE id=?`, startup)
	if _, err := saveBrowserArtifact(app, latest, 42, "save", testScreenshot(t)); err != nil {
		t.Fatal(err)
	}
	out, err := s.dashboard(context.Background(), dashboardFilter{Hours: 24, Limit: 10, Group: true}, true, dashboardNow)
	if err != nil {
		t.Fatal(err)
	}
	if out.Total != 3 || len(out.Failures) != 3 {
		t.Fatal(out)
	}
	if out.Failures[0].RunID != startup || out.Failures[0].Message != "Environment unavailable" {
		t.Fatal(out.Failures)
	}
	var repeated dashboardFailure
	for _, v := range out.Failures {
		if v.Environment == "prod" && v.CheckID == 42 {
			repeated = v
		}
	}
	if repeated.RunID != latest || repeated.Occurrences != 2 || repeated.StepID != "save" || repeated.Message != "/count equals assertion failed" || !repeated.HasScreenshot {
		t.Fatal(repeated)
	}
	for _, secret := range []string{"private assertion value", "secret evidence", "private browser"} {
		if strings.Contains(encode(out), secret) {
			t.Fatal("raw output leaked", secret)
		}
	}
	out, err = s.dashboard(context.Background(), dashboardFilter{Hours: 24, Limit: 1, Group: false}, true, dashboardNow)
	if err != nil || out.Total != 4 || len(out.Failures) != 1 {
		t.Fatal(out, err)
	}
	if _, err = s.db.Exec(`DELETE FROM test_artifacts WHERE run_id=?`, latest); err != nil {
		t.Fatal(err)
	}
	out, err = s.dashboard(context.Background(), dashboardFilter{Hours: 24, Environment: "prod", Limit: 10, Group: true}, true, dashboardNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range out.Failures {
		if v.CheckID == 42 && v.HasScreenshot {
			t.Fatal("expired screenshot still advertised")
		}
	}
}
func TestDashboardHTTPReadOnlyAndProjectOwnership(t *testing.T) {
	a, app, s := fixture(t)
	suite := dashboardTestSuite(t, s, "Dashboard")
	dashboardTestRun(t, s, suite, "test", "passed", false, time.Now().UTC().Add(-time.Hour))
	for _, tc := range []struct {
		method, path, project string
		status                int
	}{
		{"GET", "/dashboard/overview?project_id=project-a", "project-a", 200},
		{"GET", "/dashboard/failures?project_id=project-a", "project-a", 200},
		{"GET", "/dashboard/overview?project_id=project-b", "project-a", 403},
		{"GET", "/dashboard/overview?project_id=project-a", "project-b", 403},
		{"POST", "/dashboard/overview?project_id=project-a", "project-a", 405},
		{"GET", "/dashboard/overview?hours=48", "project-a", 400},
		{"GET", "/dashboard/overview?suite_id=-1", "project-a", 400},
		{"GET", "/dashboard/failures?group=maybe", "project-a", 400},
		{"GET", "/dashboard/overview?limit=100", "project-a", 400},
		{"GET", "/dashboard/unknown", "project-a", 404},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("X-Apteva-Project-ID", tc.project)
		w := httptest.NewRecorder()
		a.httpDashboard(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
		if w.Code == 200 && w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("dashboard response cacheable across projects")
		}
	}
	a.ctx = app.WithProject("")
	r := httptest.NewRequest("GET", "/dashboard/overview", nil)
	w := httptest.NewRecorder()
	a.httpDashboard(w, r)
	if w.Code != 403 {
		t.Fatal("global widget accepted missing project")
	}
}

func TestDashboardArchivedRunRemainsReadableWithoutEnablingRuns(t *testing.T) {
	a, app, s := fixture(t)
	suite, check := seed(t, s, "http", "https://example.com")
	run := dashboardTestRun(t, s, suite.ID, "stage", "failed", false, dashboardNow.Add(-time.Hour))
	if err := s.archiveSuite(suite.ID); err != nil {
		t.Fatal(err)
	}
	read, err := a.dispatch(context.Background(), app, "tests_suite_get", map[string]any{"id": suite.ID})
	if err != nil {
		t.Fatal("archived run link cannot read its suite", err)
	}
	if !read.(map[string]any)["suite"].(Suite).Archived {
		t.Fatal(read)
	}
	if _, err = s.queue(suite.ID, "tool", ""); err == nil {
		t.Fatal("archived suite became runnable")
	}
	if _, err = s.saveCheck(check); err == nil {
		t.Fatal("archived checks became editable")
	}
	out, err := s.dashboard(context.Background(), dashboardFilter{Hours: 24, Limit: 3, Group: true}, false, dashboardNow)
	if err != nil || len(out.Recent) != 1 || out.Recent[0].ID != run || !out.Options.Suites[0].Archived {
		t.Fatal(out, err)
	}
}

func TestDashboardWidgetsAreDiscoverable(t *testing.T) {
	a, _, _ := fixture(t)
	manifest := a.Manifest()
	if len(manifest.Provides.UIComponents) != 2 {
		t.Fatal("dashboard widgets missing", manifest.Provides.UIComponents)
	}
	for _, component := range manifest.Provides.UIComponents {
		if component.Visibility != "project" || len(component.Slots) != 1 || component.Slots[0] != "dashboard.home" || len(component.SupportedSizes) != 2 || component.SettingsSchema == nil {
			t.Fatalf("invalid widget contract: %+v", component)
		}
		if _, err := os.Stat(strings.TrimPrefix(component.Entry, "/")); err != nil {
			t.Fatalf("widget entry is not built: %s", component.Entry)
		}
	}
}
