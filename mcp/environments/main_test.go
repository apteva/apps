package main

import (
	"database/sql"
	"os"
	"reflect"
	"sort"
	"testing"

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
