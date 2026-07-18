package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

func testStore(t *testing.T) store {
	t.Helper()
	db, err := sql.Open("sqlite", t.TempDir()+"/environments.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, name := range []string{"001_init.sql", "002_web_fixtures.sql"} {
		migration, err := os.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(migration)); err != nil {
			t.Fatal(err)
		}
	}
	return store{db: db}
}

func TestDefinitionAndRunPersistence(t *testing.T) {
	s := testStore(t)
	d := &Definition{ID: "env-one", Name: "One", Spec: EnvironmentSpec{Version: 1, TTLSeconds: 3600, AppInstallIDs: []int64{4}}, DesiredState: "stopped"}
	if err := s.saveDefinition(d); err != nil {
		t.Fatal(err)
	}
	got, err := s.getDefinition(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Name != "One" || !reflect.DeepEqual(got.Spec.AppInstallIDs, []int64{4}) {
		t.Fatalf("definition=%#v", got)
	}
	r := &Run{ID: "run-one", EnvironmentID: d.ID, RuntimeID: "rt-one", Kind: "interactive", Status: "starting", StartedAt: d.CreatedAt}
	if err := s.createRun(r); err != nil {
		t.Fatal(err)
	}
	if err := s.updateRun(r.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	active, err := s.activeRun(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active == nil || active.RuntimeID != "rt-one" || active.Status != "running" {
		t.Fatalf("run=%#v", active)
	}
	if err := s.updateRun(r.ID, "stopped", ""); err != nil {
		t.Fatal(err)
	}
	active, err = s.activeRun(d.ID)
	if err != nil || active != nil {
		t.Fatalf("active=%#v err=%v", active, err)
	}
}

func TestListDefinitionsWithSingleDatabaseConnection(t *testing.T) {
	s := testStore(t)
	s.db.SetMaxOpenConns(1)
	d := &Definition{ID: "env-list", Name: "List", Spec: EnvironmentSpec{Version: 1, TTLSeconds: 3600}, DesiredState: "running"}
	if err := s.saveDefinition(d); err != nil {
		t.Fatal(err)
	}
	if err := s.createRun(&Run{ID: "run-list", EnvironmentID: d.ID, RuntimeID: "rt-list", Kind: "interactive", Status: "running", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	type result struct {
		definitions []Definition
		err         error
	}
	done := make(chan result, 1)
	go func() {
		definitions, err := s.listDefinitions()
		done <- result{definitions: definitions, err: err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if len(got.definitions) != 1 || got.definitions[0].ActiveRun == nil || got.definitions[0].ActiveRun.ID != "run-list" {
			t.Fatalf("definitions=%#v", got.definitions)
		}
	case <-time.After(time.Second):
		// Release a nested-query implementation so test cleanup can complete.
		s.db.SetMaxOpenConns(2)
		<-done
		t.Fatal("listDefinitions blocked on its own open result set")
	}
}

func TestEmbeddedAndSourceManifestsExposeSameTools(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	source, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	embedded := (&App{}).Manifest()
	toolNames := func(m sdk.Manifest) []string {
		out := make([]string, 0, len(m.Provides.MCPTools))
		for _, tool := range m.Provides.MCPTools {
			out = append(out, tool.Name)
		}
		sort.Strings(out)
		return out
	}
	if source.Name != embedded.Name || source.Version != embedded.Version || !reflect.DeepEqual(toolNames(*source), toolNames(embedded)) {
		t.Fatalf("manifest drift: source=%s@%s embedded=%s@%s", source.Name, source.Version, embedded.Name, embedded.Version)
	}
}

func TestSpecValidation(t *testing.T) {
	if err := validateSpec(EnvironmentSpec{TTLSeconds: 59}); err == nil {
		t.Fatal("accepted short TTL")
	}
	if err := validateSpec(EnvironmentSpec{TTLSeconds: 3600}); err != nil {
		t.Fatal(err)
	}
	if err := validateSpec(EnvironmentSpec{WebFixtures: []WebFixtureSpec{{ID: "site", Pack: "unknown"}}}); err == nil {
		t.Fatal("accepted unknown web fixture pack")
	}
}

func TestPatreonFixtureLifecycleAndAssertions(t *testing.T) {
	svc := &service{db: testStore(t)}
	run := &Run{ID: "run-web", RuntimeID: "runtime-web", Kind: "test", Status: "starting", StartedAt: time.Now().UTC()}
	if err := svc.db.createRun(run); err != nil {
		t.Fatal(err)
	}
	spec := EnvironmentSpec{WebFixtures: []WebFixtureSpec{{ID: "patreon", Pack: "patreon", Scenario: "new-visitor", Seed: map[string]any{"creator_slug": "studio-north"}}}}
	if err := svc.createWebFixtures(run, spec); err != nil {
		t.Fatal(err)
	}
	if len(run.WebFixtures) != 1 || run.WebFixtures[0].TestURL == "" {
		t.Fatalf("fixtures=%#v", run.WebFixtures)
	}
	if _, _, err := svc.applyFixtureAction(run.ID, "patreon", "select_tier", map[string]any{"tier": "supporter"}); err != nil {
		t.Fatal(err)
	}
	if _, event, err := svc.applyFixtureAction(run.ID, "patreon", "checkout", nil); err != nil {
		t.Fatal(err)
	} else if event == nil || event.Type != "membership.created" {
		t.Fatalf("event=%#v", event)
	}
	stateResult, err := svc.assert(run.RuntimeID, Assertion{Type: "web_state", Fixture: "patreon", Path: "membership.tier", Equals: "supporter"})
	if err != nil || !stateResult.Passed {
		t.Fatalf("state assertion=%#v err=%v", stateResult, err)
	}
	eventResult, err := svc.assert(run.RuntimeID, Assertion{Type: "web_event", Fixture: "patreon", EventType: "membership.created", Path: "tier", Equals: "supporter"})
	if err != nil || !eventResult.Passed {
		t.Fatalf("event assertion=%#v err=%v", eventResult, err)
	}
	if err := svc.resetFixture(run.ID, "patreon"); err != nil {
		t.Fatal(err)
	}
	detail, err := svc.fixtureDetail(run.ID, "patreon")
	if err != nil {
		t.Fatal(err)
	}
	state := detail["state"].(map[string]any)
	if state["membership"] != nil || len(detail["events"].([]WebFixtureEvent)) != 0 {
		t.Fatalf("fixture did not reset: %#v", detail)
	}
}

func TestPatreonFixtureSignedRoute(t *testing.T) {
	svc := &service{db: testStore(t)}
	run := &Run{ID: "run-route", RuntimeID: "runtime-route", Kind: "test", Status: "starting", StartedAt: time.Now().UTC()}
	if err := svc.db.createRun(run); err != nil {
		t.Fatal(err)
	}
	if err := svc.createWebFixtures(run, EnvironmentSpec{WebFixtures: []WebFixtureSpec{{ID: "patreon", Pack: "patreon"}}}); err != nil {
		t.Fatal(err)
	}
	x, _ := svc.db.getWebFixture(run.ID, "patreon")
	app := &App{svc: svc}

	bad := httptest.NewRecorder()
	app.handleFixture(bad, httptest.NewRequest(http.MethodGet, "/fixtures/run-route/patreon/wrong/", nil))
	if bad.Code != http.StatusNotFound {
		t.Fatalf("bad token status=%d", bad.Code)
	}

	page := httptest.NewRecorder()
	app.handleFixture(page, httptest.NewRequest(http.MethodGet, "/fixtures/run-route/patreon/"+x.Token+"/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Choose your membership") {
		t.Fatalf("page status=%d body=%s", page.Code, page.Body.String())
	}

	body, _ := json.Marshal(map[string]any{"action": "select_tier", "input": map[string]any{"tier": "supporter"}})
	action := httptest.NewRecorder()
	app.handleFixture(action, httptest.NewRequest(http.MethodPost, "/fixtures/run-route/patreon/"+x.Token+"/api/action", bytes.NewReader(body)))
	if action.Code != http.StatusOK || !strings.Contains(action.Body.String(), "checkout.started") {
		t.Fatalf("action status=%d body=%s", action.Code, action.Body.String())
	}
	if err := svc.db.updateRun(run.ID, "stopped", ""); err != nil {
		t.Fatal(err)
	}
	stopped := httptest.NewRecorder()
	app.handleFixture(stopped, httptest.NewRequest(http.MethodGet, "/fixtures/run-route/patreon/"+x.Token+"/", nil))
	if stopped.Code != http.StatusNotFound {
		t.Fatalf("stopped fixture status=%d", stopped.Code)
	}
}
