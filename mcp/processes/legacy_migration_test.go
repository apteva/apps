package main

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func nativeSetup(t *testing.T) (*App, *directPlatform) {
	t.Helper()
	f := &directPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(f))
	a := &App{}
	if e := a.OnMount(ctx); e != nil {
		t.Fatal(e)
	}
	return a, f
}

func TestNativeTaskMigrationPreservesLegacyWorkAndForeignKeys(t *testing.T) {
	db, e := sql.Open("sqlite", ":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, e = db.Exec(`PRAGMA foreign_keys=ON`); e != nil {
		t.Fatal(e)
	}
	for _, file := range []string{"001_init.sql", "002_direct_execution.sql", "003_assignments.sql", "004_workflow_steps.sql", "005_event_triggers.sql"} {
		raw, e := os.ReadFile("migrations/" + file)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(string(raw)); e != nil {
			t.Fatal(file, e)
		}
	}
	for _, q := range []string{
		`INSERT INTO processes(id,project_id,status,created_at,updated_at) VALUES('p','project-a','active','now','now')`,
		`INSERT INTO process_versions(process_id,version,body_json,created_by,created_at) VALUES('p',1,'{}','operator','now')`,
		`INSERT INTO process_runs(id,process_id,version,kind,request_key,created_at) VALUES('r','p',1,'manual','one','now')`,
		`INSERT INTO process_step_runs(id,run_id,step_key,position,definition_json,executor_json,state,output,updated_at,execution_id,delivered_at) VALUES('old-step','r','write',0,'{"name":"Write","kind":"work"}','{"kind":"agent","agent_id":7}','completed','Preserved result','now','exec-old','then')`,
		`INSERT INTO process_step_events(step_id,actor,state,output,created_at) VALUES('old-step','agent:7:t','completed','Preserved result','now')`,
	} {
		if _, e = db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	raw, e := os.ReadFile("migrations/006_native_tasks.sql")
	if e != nil {
		t.Fatal(e)
	}
	tx, e := db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(string(raw)); e != nil {
		tx.Rollback()
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	files, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range files {
		file := entry.Name()
		if file <= "006_native_tasks.sql" || !strings.HasSuffix(file, ".sql") {
			continue
		}
		raw, err := os.ReadFile("migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	a := &App{db: db}
	s, e := scanStep(a.db.QueryRow(`SELECT `+stepColumns+` FROM process_step_runs WHERE id=? AND project_id=?`, "old-step", "project-a"))
	if e != nil || s.ID != "old-step" || s.Output != "Preserved result" || s.ExecutionID != "exec-old" || !s.Required || s.Origin != "process_step" {
		t.Fatal(s, e)
	}
	if s.Definition.Kind != "" {
		t.Fatalf("legacy approval metadata survived migration: %+v", s)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM process_step_events WHERE step_id='old-step'`).Scan(&n)
	if n != 1 {
		t.Fatal("lost audit history")
	}
	if _, e = db.Exec(`INSERT INTO process_step_events(step_id,actor,state,created_at) VALUES('missing','operator','running','now')`); e == nil {
		t.Fatal("history foreign key was lost")
	}
	rows, e := db.Query(`PRAGMA foreign_key_check`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration broke a foreign key")
	}
}
