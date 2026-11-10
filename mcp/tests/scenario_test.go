package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type scenarioPlatform struct {
	sdk.PlatformClient
	sdk.AppContextClient
	call func(context.Context, string, string, map[string]any, any) error
}

func (p *scenarioPlatform) CallAppResultContext(ctx context.Context, app, tool string, args map[string]any, out any) error {
	return p.call(ctx, app, tool, args, out)
}

func TestReleaseScenarioUsesFrozenEnvironmentAndExactVerdict(t *testing.T) {
	candidate := strings.Repeat("a", 64)
	other := strings.Repeat("b", 64)
	credential := "ephemeral-credential"
	basePath := "/api/environment-app-public/rt_test/api/apps/api/_install/42/gw/items/v1"
	var writes, reads atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/mcp") {
			name := strings.Split(r.URL.Path, "/")[6]
			fmt.Fprintf(w, `{"result":{"serverInfo":{"name":"%s","version":"1.0.0"}}}`, name)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+credential {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-Apteva-Runtime-Public-Auth", "preserved")
		switch r.Method + " " + strings.TrimPrefix(r.URL.Path, basePath) {
		case "POST /items":
			writes.Add(1)
			w.Header().Set("Location", basePath+"/items/17")
			w.Header().Set("X-Request-ID", "create-17")
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"id":17,"password":"%s"}`, credential)
		case "GET /items/17":
			if reads.Add(1) == 1 {
				fmt.Fprint(w, `{"state":"pending"}`)
			} else {
				fmt.Fprintf(w, `{"id":17,"state":"ready","echo":"%s"}`, credential)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gateway.Close()

	started, stopped := 0, 0
	frozenVersion := 3
	observed := candidate
	platform := &scenarioPlatform{call: func(_ context.Context, app, tool string, args map[string]any, out any) error {
		if app != "environments" || args["_project_id"] != "project-a" {
			t.Fatalf("unbound call: %s %s", app, tool)
		}
		var result any
		switch tool {
		case "environment_get":
			result = map[string]any{"id": "env_items", "spec_version": frozenVersion,
				"spec": json.RawMessage(`{"version":1,"ttl_seconds":600,"app_install_ids":[5,6],"network_mode":"block","integration_mode":"mock","seeds":[{"app":"loader","tool":"load","input":{"digest":"{{candidate_digest}}"}}]}`)}
		case "environment_run_create":
			started++
			spec := args["spec"].(map[string]any)
			seed := spec["seeds"].([]any)[0].(map[string]any)["input"].(map[string]any)
			if seed["digest"] != candidate && seed["digest"] != other {
				t.Fatalf("candidate substitution: %v", seed)
			}
			result = map[string]any{"id": args["request_id"], "runtime_id": "rt_test", "status": "running"}
		case "environment_run_get":
			result = map[string]any{"runtime": map[string]any{"id": "rt_test", "network_mode": "block", "integration_mode": "mock", "apps": []map[string]any{
				{"name": "api", "status": "running", "kind": "install"},
				{"name": "loader", "status": "running", "kind": "install"},
				{"name": "auth", "status": "running", "kind": "install"},
				{"name": "database", "status": "running", "kind": "install"},
			}}}
		case "environment_run_endpoint":
			result = map[string]any{"app_url": gateway.URL + "/api/environment-app-public/rt_test/api/apps/" + args["app"].(string) + "/_install/42"}
		case "environment_run_stop":
			stopped++
			result = map[string]any{"ok": true, "runtime_state": "stopped"}
		case "environment_call":
			switch args["tool"] {
			case "digest":
				result = map[string]any{"digest": observed}
			case "create_token":
				result = map[string]any{"token": credential}
			case "db_get":
				input := args["input"].(map[string]any)
				if input["id"] != float64(17) || writes.Load() < 1 {
					t.Fatalf("wrong database lookup: %v", input)
				}
				result = map[string]any{"id": 17}
			default:
				t.Fatalf("unexpected bound tool %v", args["tool"])
			}
		default:
			t.Fatalf("unexpected environment tool %s", tool)
		}
		return json.Unmarshal([]byte(encode(result)), out)
	}}
	a, app, s := fixture(t, tk.WithPlatform(platform))
	suite, err := s.saveSuite(Suite{Name: "Items", Release: &ReleaseConfig{
		EnvironmentID: "env_items", EnvironmentRevision: 3, GatewayApp: "api", GatewayPath: "/gw/items/v1",
		AppVersions:   map[string]string{"api": "1.0.0", "loader": "1.0.0", "auth": "1.0.0", "database": "1.0.0"},
		IdentityProbe: ToolProbe{App: "loader", Tool: "digest", Path: "/digest"},
		ReadTools:     map[string][]string{"loader": {"digest"}, "database": {"db_get"}},
		Identities:    map[string]Identity{"admin": {ToolProbe: ToolProbe{App: "auth", Tool: "create_token", Path: "/token"}, Header: "Authorization", Prefix: "Bearer "}},
		OpenAPI:       json.RawMessage(`{"openapi":"3.1.0","paths":{"/items":{"post":{"operationId":"createItem"}},"/items/{id}":{"get":{"operationId":"getItem"}}}}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	check, err := s.saveCheck(Check{SuiteID: suite.ID, Name: "Create and verify", Kind: "scenario", Enabled: true, Definition: Definition{Steps: []Step{
		{ID: "create", Kind: "http", Method: "POST", Path: "/items", Identity: "admin", JSON: map[string]any{"name": "synthetic"},
			Assertions: []Assertion{{Path: "/status_code", Op: "equals", Value: float64(201)}, {Path: "/json/id", Op: "exists"}},
			Capture:    map[string]string{"job": "/headers/location", "id": "/json/id"}, Evidence: []string{"/json", "/headers/location"}},
		{ID: "wait", Kind: "http", Path: "{{job}}", Identity: "admin", PollEveryMS: 100, TimeoutMS: 1000,
			Assertions: []Assertion{{Path: "/json/state", Op: "equals", Value: "ready"}}, Evidence: []string{"/json"},
			Schema: json.RawMessage(`{"type":"object","required":["id","state"],"properties":{"id":{"type":"integer"}}}`)},
		{ID: "persisted", Kind: "app_tool", App: "database", Tool: "db_get", Input: map[string]any{"id": "{{id}}"},
			Assertions: []Assertion{{Path: "/id", Op: "equals", Value: "{{id}}"}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r, err := a.queue(ctx, app, suite.ID, "tool", "first", QueueOptions{CandidateDigest: candidate})
	if err != nil {
		t.Fatal(err)
	}
	pins := QueueOptions{CandidateDigest: candidate, SuiteRevision: r.Release.SuiteRevision, FixtureRevision: r.Release.FixtureRevision}
	frozenVersion++
	if _, err := a.queue(ctx, app, suite.ID, "tool", "new-fixture", pins); err == nil {
		t.Fatal("changed fixture accepted")
	}
	if again, err := a.queue(ctx, app, suite.ID, "tool", "first", pins); err != nil || again.ID != r.ID {
		t.Fatal(again, err)
	}
	if err = a.work(ctx, app); err != nil {
		t.Fatal(err)
	}
	detail, err := s.run(r.ID)
	if err != nil || detail.Status != "passed" || len(detail.Release.Covered) != 2 || started != 1 || stopped != 1 || reads.Load() != 2 {
		t.Fatalf("run=%s starts=%d stops=%d reads=%d err=%v", encode(detail), started, stopped, reads.Load(), err)
	}
	if strings.Contains(encode(detail), credential) || !strings.Contains(encode(detail), "create-17") {
		t.Fatalf("incorrect evidence: %s", encode(detail))
	}
	if v, err := s.verdict(suite.ID, pins); err != nil || !v.Allowed {
		t.Fatal(v, err)
	}
	mismatch := pins
	mismatch.CandidateDigest = other
	if v, err := s.verdict(suite.ID, mismatch); err != nil || v.Allowed || v.State != "missing" {
		t.Fatal(v, err)
	}
	frozenVersion--
	bad, err := a.queue(ctx, app, suite.ID, "tool", "wrong-artifact", mismatch)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.work(ctx, app); err != nil {
		t.Fatal(err)
	}
	failed, err := s.run(bad.ID)
	if err != nil || failed.Status != "failed" || stopped != 2 || writes.Load() != 1 {
		t.Fatal(failed, err)
	}
	// A failed assertion after a valid candidate probe also blocks the verdict.
	check.Definition.Steps[2].Assertions[0].Value = float64(99)
	if _, err = s.saveCheck(check); err != nil {
		t.Fatal(err)
	}
	next, err := a.queue(ctx, app, suite.ID, "tool", "assertion", QueueOptions{CandidateDigest: candidate})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.work(ctx, app); err != nil {
		t.Fatal(err)
	}
	pins.SuiteRevision = next.Release.SuiteRevision
	if v, err := s.verdict(suite.ID, pins); err != nil || v.Allowed || v.State != "failed" {
		t.Fatal(v, err)
	}
}

func TestScenarioMigrationPreservesHistoryAndCheckIDs(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	apply := func(name string) {
		t.Helper()
		sql, err := os.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(sql)); err != nil {
			t.Fatal(err)
		}
	}
	apply("001_init.sql")
	if _, err = db.Exec(`
		INSERT INTO test_suites(id,project_id,name,created_at,updated_at) VALUES(1,'p','Old suite','1','1');
		INSERT INTO test_checks(id,project_id,suite_id,name,kind,definition,updated_at) VALUES(12,'p',1,'Legacy','http','{}','1');
		INSERT INTO test_checks(id,project_id,suite_id,name,kind,definition,updated_at) VALUES(25,'p',1,'Deleted','http','{}','1');
		DELETE FROM test_checks WHERE id=25;
		INSERT INTO test_runs(id,project_id,suite_id,suite_name,environment,status,trigger,snapshot,created_at) VALUES(7,'p',1,'Old suite','test','passed','tool','[]','1');
		INSERT INTO test_results(project_id,run_id,check_id,name,kind,status,duration_ms,output,assertions) VALUES('p',7,12,'Legacy','http','passed',1,'{}','[]');
	`); err != nil {
		t.Fatal(err)
	}
	apply("002_scenarios.sql")
	s := store{db, "p"}
	r, err := s.run(7)
	if err != nil || r.Release != nil || len(r.Results) != 1 || r.Results[0].CheckID != 12 {
		t.Fatal(r, err)
	}
	res, err := db.Exec(`INSERT INTO test_checks(project_id,suite_id,name,kind,definition,updated_at) VALUES('p',1,'new','scenario','{}','2')`)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := res.LastInsertId(); id != 26 {
		t.Fatalf("reused check id %d", id)
	}
}

func TestParallelScenarioAndRuntimeURLBoundaries(t *testing.T) {
	base, err := runtimeGateway("http://127.0.0.1:5280/api/environment-app-public/rt_test/api/apps/api/_install/42", "rt_test", "api", "/gw/items")
	if err != nil {
		t.Fatal(err)
	}
	session := &releaseSession{gateway: base}
	for _, escaped := range []string{"https://example.com/steal", "//example.com/steal", "/../private", "/%2e%2e/private", "/p%2fprivate", "/p%5cprivate"} {
		if _, _, err = scenarioURL("{{location}}", map[string]any{"location": escaped}, session); err == nil {
			t.Fatalf("accepted %s", escaped)
		}
	}
	var count atomic.Int32
	both := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 2 {
			close(both)
		}
		select {
		case <-both:
			fmt.Fprint(w, `{"parallel":true}`)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	a, app, _ := fixture(t)
	step := Step{ID: "one", Kind: "http", Path: server.URL, TimeoutMS: 1000, Assertions: []Assertion{{Path: "/json/parallel", Op: "equals", Value: true}}}
	second := step
	second.ID = "two"
	check := Check{SuiteID: 1, Kind: "scenario", Name: "race", Definition: Definition{Steps: []Step{{ID: "race", Parallel: []Step{step, second}}}}}
	if err = validateCheck(&check); err != nil {
		t.Fatal(err)
	}
	result := a.runScenario(context.Background(), check, &appRunContext{ctx: app}, nil)
	if result.Status != "passed" || count.Load() != 2 {
		t.Fatal(result)
	}
}
