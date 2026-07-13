package main

import (
	"database/sql"
	"os"
	"reflect"
	"sort"
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
	migration, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
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
}
