package main

import (
	"encoding/json"
	"testing"
)

func TestProjectAssignmentsAndProcessFilteredRuns(t *testing.T) {
	a, _, p := directSetup(t)
	x := addAssignment(t, a, p, "Daily digest", 7, "agent", nil, nil)
	other, err := a.save("project-a", "", "operator", 0, def())
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := a.save("project-b", "", "operator", 0, def())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`INSERT INTO process_assignments(id,process_id,revision,body_json,status,created_at,updated_at) VALUES('hidden-assignment',?,1,'{"name":"Other project"}','paused','2026-11-01T09:00:00Z','2026-11-01T09:00:00Z')`, hidden.ID); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ id, process, assignment, state, at string }{
		{"run-old", p.ID, x.ID, "completed", "2026-11-01T09:00:00Z"},
		{"run-new", p.ID, x.ID, "running", "2026-11-02T09:00:00Z"},
		{"run-other", other.ID, "", "completed", "2026-11-03T09:00:00Z"},
	} {
		if _, err = a.db.Exec(`INSERT INTO process_runs(id,process_id,version,kind,request_key,created_at,backend,state,assignment_id,assignment_json,overrides_json) VALUES(?,?,1,'manual',?,?,'agent',?,?,'{}','{}')`, r.id, r.process, r.id, r.at, r.state, r.assignment); err != nil {
			t.Fatal(err)
		}
	}

	w := overviewGET(t, a, "/processes/assignments?project_id=project-a", "project-a")
	if w.Code != 200 {
		t.Fatalf("assignments route: %d %s", w.Code, w.Body.String())
	}
	var list struct {
		Assignments []projectAssignment `json:"assignments"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var got projectAssignment
	for _, y := range list.Assignments {
		if y.ID == "hidden-assignment" {
			t.Fatalf("project scope leaked: %+v", y)
		}
		if y.ID == x.ID {
			got = y
		}
	}
	if got.ID != x.ID || got.Name != "Daily digest" || got.ProcessName != p.Name || got.ProcessVersion != p.Version || got.RunCount != 2 || got.OngoingCount != 1 || got.LastRunID != "run-new" || got.LastRunState != "running" {
		t.Fatalf("assignment summary: %+v", got)
	}

	w = overviewGET(t, a, "/processes/runs?project_id=project-a&view=compact&process_id="+other.ID, "project-a")
	var runs struct {
		Runs []struct {
			ID        string `json:"id"`
			ProcessID string `json:"process_id"`
		} `json:"runs"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &runs); err != nil || w.Code != 200 {
		t.Fatalf("filtered runs: %d %s", w.Code, w.Body.String())
	}
	if len(runs.Runs) != 1 || runs.Runs[0].ID != "run-other" {
		t.Fatalf("process filter: %+v", runs.Runs)
	}
	if w = overviewGET(t, a, "/processes/runs?project_id=project-a&view=compact&process_id="+hidden.ID, "project-a"); w.Code == 200 {
		t.Fatal("another project's process was accepted as a filter")
	}
}
