package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func browserReleaseConfig() *ReleaseConfig {
	return &ReleaseConfig{
		EnvironmentID: "env_browser", EnvironmentRevision: 1, GatewayApp: "api", GatewayPath: "/gw/browser",
		AppVersions:   map[string]string{"api": "1.0.0"},
		IdentityProbe: ToolProbe{App: "api", Tool: "digest", Path: "/digest"},
		ReadTools:     map[string][]string{"api": {"digest"}},
		Browser:       &BrowserConfig{},
	}
}
func TestBrowserChecksRequireIsolationAndSequentialInput(t *testing.T) {
	_, _, s := fixture(t)
	plain, err := s.saveSuite(Suite{Name: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	check := Check{SuiteID: plain.ID, Kind: "browser", Name: "browser", Enabled: true, Definition: Definition{Steps: []Step{
		{ID: "open", Browser: &BrowserAction{Action: "navigate", Target: "frontend", Path: "/"}},
	}}}
	if _, err = s.saveCheck(check); err == nil {
		t.Fatal("browser accepted without an environment")
	}
	suite, err := s.saveSuite(Suite{Name: "browser", Release: browserReleaseConfig()})
	if err != nil {
		t.Fatal(err)
	}
	check.SuiteID = suite.ID
	saved, err := s.saveCheck(check)
	if err != nil || saved.Definition.Steps[0].Kind != "browser" {
		t.Fatal(saved, err)
	}
	check.Kind = "scenario"
	check.Definition.Steps = []Step{{ID: "parallel", Parallel: []Step{
		{ID: "one", Kind: "browser", Browser: &BrowserAction{Action: "reload"}},
		{ID: "two", Kind: "browser", Browser: &BrowserAction{Action: "reload"}},
	}}}
	if _, err = s.saveCheck(check); err == nil {
		t.Fatal("parallel browser input accepted")
	}
}
func testScreenshot(t *testing.T) browserArtifact {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buffer.Bytes())
	return browserArtifact{MIME: "image/png", SHA256: hex.EncodeToString(sum[:]), Base64: base64.StdEncoding.EncodeToString(buffer.Bytes())}
}
func TestBrowserDiagnosticsBlockVerdictAndRetainOwnedScreenshot(t *testing.T) {
	clicks := 0
	var artifact browserArtifact
	platform := &scenarioPlatform{call: func(_ context.Context, app, tool string, args map[string]any, out any) error {
		if app != "computer" || args["_project_id"] != "project-a" {
			t.Fatalf("unexpected browser binding %s", app)
		}
		var value any
		switch tool {
		case "browser_test_capabilities":
			value = map[string]any{"protocol": browserProtocol, "backend": "local", "available": true, "fresh_context": true, "runtime_network_boundary": true, "screenshots": true,
				"actions": []string{"navigate", "reload", "observe", "click", "fill", "select", "check", "upload"}}
		case "browser_test_session":
			switch args["action"] {
			case "open":
				value = map[string]any{"id": args["id"], "state": "active", "protocol": browserProtocol, "engine": "Chromium test"}
			case "status":
				value = map[string]any{"diagnostics": map[string]any{"blocked": 1}}
			case "close":
				value = map[string]any{"id": args["id"], "state": "closed", "diagnostics": map[string]any{"blocked": 1}}
			}
		case "browser_test_step":
			command := args["command"].(map[string]any)
			clicked := command["action"] == "click"
			if clicked {
				clicks++
			}
			value = map[string]any{"action_dispatched": clicked, "observation": map[string]any{"count": 1, "visible": true, "enabled": true, "text": "Saved"}}
		case "browser_test_capture":
			value = map[string]any{"artifact": artifact}
		default:
			t.Fatalf("unknown browser tool %s", tool)
		}
		return json.Unmarshal([]byte(encode(value)), out)
	}}
	a, app, s := fixture(t, tk.WithPlatform(platform))
	artifact = testScreenshot(t)
	config := browserReleaseConfig()
	suite, err := s.saveSuite(Suite{Name: "browser", Release: config})
	if err != nil {
		t.Fatal(err)
	}
	check, err := s.saveCheck(Check{SuiteID: suite.ID, Name: "save", Kind: "browser", Enabled: true, Definition: Definition{Steps: []Step{
		{ID: "save", Browser: &BrowserAction{Action: "click", Locator: BrowserLocator{TestID: "save"}}},
		{ID: "result", Browser: &BrowserAction{Action: "observe", Locator: BrowserLocator{TestID: "result"}}, Assertions: []Assertion{{Path: "/text", Op: "equals", Value: "Saved"}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	def := environmentDefinition{ID: config.EnvironmentID, SpecVersion: 1, Spec: json.RawMessage(`{"version":1,"ttl_seconds":300,"network_mode":"block","integration_mode":"mock","app_install_ids":[1]}`)}
	r, err := s.queueRelease(suite.ID, "tool", "browser", QueueOptions{CandidateDigest: digest}, def)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.claim()
	if err != nil {
		t.Fatal(err)
	}
	r.Release.RuntimeID = "rt_browser"
	r.Release.EnvironmentRun = "run_browser"
	r.Release.ObservedDigest = digest
	if err = s.saveReleaseRecord(r.ID, r.Release, false); err != nil {
		t.Fatal(err)
	}
	services := &appRunContext{ctx: app, runID: r.ID, release: &releaseSession{
		config: config, runtimeID: "rt_browser", targets: map[string]string{"frontend": "http://127.0.0.1:5280/api/environment-app-public/rt_browser/api/apps/api/_install/1/ui"},
	}}
	result := a.runScenario(context.Background(), check, services, map[string]bool{})
	if result.Status != "error" || clicks != 1 {
		t.Fatalf("diagnostics passed or click repeated: %s clicks=%d", encode(result), clicks)
	}
	if err = s.saveResult(r.ID, result); err != nil {
		t.Fatal(err)
	}
	if err = s.finish(r.ID, ""); err != nil {
		t.Fatal(err)
	}
	pins := QueueOptions{CandidateDigest: digest, SuiteRevision: r.Release.SuiteRevision, FixtureRevision: r.Release.FixtureRevision}
	if verdict, err := s.verdict(suite.ID, pins); err != nil || verdict.Allowed {
		t.Fatal(verdict, err)
	}
	var id int64
	if err = s.db.QueryRow("SELECT id FROM test_artifacts WHERE run_id=?", r.ID).Scan(&id); err != nil {
		t.Fatal("missing screenshot", err)
	}
	request := httptest.NewRequest("GET", "/artifacts/"+encode(id)+"?project_id=project-a", nil)
	response := httptest.NewRecorder()
	a.httpBrowserArtifact(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" {
		t.Fatal(response.Code)
	}
	foreign := httptest.NewRequest("GET", "/artifacts/"+encode(id)+"?project_id=project-b", nil)
	response = httptest.NewRecorder()
	a.httpBrowserArtifact(response, foreign)
	if response.Code != http.StatusForbidden {
		t.Fatal("cross-project artifact", response.Code)
	}
	if _, err = s.db.Exec("UPDATE test_artifacts SET created_at=?", time.Now().Add(-8*24*time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err = pruneBrowserArtifacts(app); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRow("SELECT count(*) FROM test_artifacts").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if _, err = s.run(r.ID); err != nil {
		t.Fatal("retention removed run history", err)
	}
}

func TestBrowserUpgradePreservesChecksAndDeletedIDWatermark(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	apply := func(file string) {
		t.Helper()
		raw, err := os.ReadFile("migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	apply("001_init.sql")
	apply("002_scenarios.sql")
	if _, err = db.Exec(`INSERT INTO test_suites(project_id,name,created_at,updated_at) VALUES('one','Old suite','now','now'); INSERT INTO test_checks(id,project_id,suite_id,name,kind,definition,updated_at) VALUES(1,'one',1,'Kept','scenario','{}','now'),(100,'one',1,'Deleted','http','{}','now'); DELETE FROM test_checks WHERE id=100`); err != nil {
		t.Fatal(err)
	}
	apply("003_browser.sql")
	var name string
	if err = db.QueryRow("SELECT name FROM test_checks WHERE id=1").Scan(&name); err != nil || name != "Kept" {
		t.Fatal(name, err)
	}
	row, err := db.Exec(`INSERT INTO test_checks(project_id,suite_id,name,kind,definition,updated_at) VALUES('one',1,'Browser','browser','{}','now')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := row.LastInsertId()
	if id != 101 {
		t.Fatalf("reused historical check ID: %d", id)
	}
}
