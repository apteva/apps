package main

import (
	"strings"
	"testing"
)

func TestNativeOnlyManifestAndTools(t *testing.T) {
	a, _ := setup(t)
	m := a.Manifest()
	if len(m.Requires.Apps) != 1 || m.Requires.Apps[0].Name != "evals" || !m.Requires.Apps[0].Optional {
		t.Fatalf("manifest must declare only optional evals: %#v", m.Requires.Apps)
	}
	for _, tool := range a.MCPTools() {
		if strings.HasPrefix(tool.Name, "task") || tool.Name == "tasks" {
			t.Fatalf("legacy task tool still exported: %s", tool.Name)
		}
	}
}

func TestNewRunsAlwaysUseNativeBackend(t *testing.T) {
	a, _ := setup(t)
	p := create(t, a, def())
	x := addAssignment(t, a, p, "Native", 7, "", nil, nil)
	if x.ExecutionMode != "agent" {
		t.Fatalf("assignment execution mode=%q", x.ExecutionMode)
	}
	if _, err := a.db.Exec(`UPDATE process_assignments SET body_json=json_set(body_json,'$.execution_mode','tasks') WHERE id=?`, x.ID); err != nil {
		t.Fatal(err)
	}
	read, err := a.assignment(p.ProjectID, p.ID, x.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.ExecutionMode != "agent" {
		t.Fatalf("legacy assignment was not normalized: %q", read.ExecutionMode)
	}
}
