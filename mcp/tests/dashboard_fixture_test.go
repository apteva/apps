package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Local browser harness only: no platform credentials, workers or remote calls.
func TestWidgetsBrowserFixture(t *testing.T) {
	if os.Getenv("TESTS_WIDGET_FIXTURE") != "1" {
		t.Skip("local browser fixture")
	}
	a, app, s := fixture(t)
	var unavailable atomic.Bool
	seedData := func(mode string) map[string]int64 {
		for _, table := range []string{"test_artifacts", "test_browser_leases", "test_results", "test_outbox", "test_runs", "test_checks", "test_suites"} {
			if _, err := s.db.Exec("DELETE FROM " + table); err != nil {
				t.Fatal(err)
			}
		}
		ids := map[string]int64{}
		if mode == "empty" {
			return ids
		}
		at := time.Now().UTC()
		browser := dashboardTestSuite(t, s, "Browser checkout")
		api := dashboardTestSuite(t, s, "API contracts")
		archived := dashboardTestSuite(t, s, "Archived release")
		run := func(suite int64, env, status string, cancel bool, offset time.Duration, name string) int64 {
			id := dashboardTestRun(t, s, suite, env, status, cancel, at.Add(offset))
			s.db.Exec(`UPDATE test_runs SET suite_name=? WHERE id=?`, name, id)
			return id
		}
		for i := 0; i < 125; i++ {
			run(browser, "stage", "passed", false, -2*time.Hour, "Browser checkout")
		}
		ids["passed"] = run(api, "prod", "passed", false, -30*time.Minute, "API contracts")
		for _, offset := range []time.Duration{-10 * time.Minute, -5 * time.Minute} {
			id := run(browser, "stage", "failed", false, offset, "Browser checkout")
			ids["failure"] = id
			if err := s.saveResult(id, Result{CheckID: 42, Name: "Save sale", Kind: "browser", Status: "failed", Error: "Assertion failed", Assertions: []AssertionResult{}, Output: map[string]any{"steps": []any{map[string]any{"id": "save", "kind": "browser", "status": "failed", "error": "Saved total did not match the expected value", "assertions": []any{}, "artifact_id": 0}}}}); err != nil {
				t.Fatal(err)
			}
		}
		artifact, err := saveBrowserArtifact(app, ids["failure"], 42, "save", testScreenshot(t))
		if err != nil {
			t.Fatal(err)
		}
		s.db.Exec(`UPDATE test_results SET output=json_set(output,'$.steps[0].artifact_id',?) WHERE run_id=?`, artifact, ids["failure"])
		run(browser, "stage", "failed", true, -15*time.Minute, "Browser checkout")
		run(api, "prod", "running", false, -time.Minute, "API contracts")
		run(api, "prod", "queued", false, -8*24*time.Hour, "API contracts")
		old := run(api, "prod", "failed", false, -3*24*time.Hour, "API contracts")
		s.db.Exec(`UPDATE test_runs SET error='Contract unavailable' WHERE id=?`, old)
		ids["archived"] = run(archived, "stage", "failed", false, -20*time.Minute, "Archived release")
		s.db.Exec(`UPDATE test_runs SET error='Environment unavailable' WHERE id=?`, ids["archived"])
		if err = s.archiveSuite(archived); err != nil {
			t.Fatal(err)
		}
		other := store{s.db, "project-b"}
		private := dashboardTestSuite(t, other, "Private project result")
		dashboardTestRun(t, other, private, "hidden", "failed", false, at.Add(-time.Hour))
		ids["browser_suite"] = browser
		ids["api_suite"] = api
		return ids
	}
	ids := seedData("full")
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__health":
			w.Write([]byte("ready"))
			return
		case "/__fixture":
			ids = seedData(r.URL.Query().Get("mode"))
			unavailable.Store(false)
			json.NewEncoder(w).Encode(ids)
			return
		case "/__unavailable":
			unavailable.Store(r.URL.Query().Get("on") == "1")
			w.Write([]byte("ok"))
			return
		}
		if unavailable.Load() && strings.Contains(r.URL.Path, "/dashboard/") {
			http.Error(w, "fixture unavailable", 503)
			return
		}
		r.Header.Set("X-Apteva-Project-ID", "project-a")
		for _, route := range a.HTTPRoutes() {
			if strings.HasPrefix(r.URL.Path, route.Pattern) {
				route.Handler(w, r)
				return
			}
		}
		http.NotFound(w, r)
	})
	port, _ := strconv.Atoi(os.Getenv("TESTS_WIDGET_FIXTURE_PORT"))
	if port < 1 {
		t.Fatal("fixture port required")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	t.Logf("Local test dashboard fixture at %s", listener.Addr())
	// The browser harness owns the process and terminates it after the tests.
	if err = server.Serve(listener); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
