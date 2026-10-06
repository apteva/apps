package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestAssignmentTargetRemovalPreservesParametersRunsAndDelivery(t *testing.T) {
	a, f, p := directSetup(t)
	p = status(t, a, p.ID, "paused")
	d := p.Definition
	d.Parameters = []Parameter{{Key: "target", Type: "string", Required: true, Default: "parameter value"}, {Key: "page", Type: "string", Required: true, Default: "photography"}}
	var err error
	p, err = a.save(p.ProjectID, p.ID, "operator", p.Version, d)
	if err != nil {
		t.Fatal(err)
	}
	x := addAssignment(t, a, p, "Parameter assignment", 7, "agent", nil, map[string]any{"target": "parameter value", "page": "photography"})
	p = status(t, a, p.ID, "active")
	x = enableAssignment(t, a, p, x)
	r := startOn(t, a, p, x, "without-label", nil)
	if r.Binding.Parameters["target"] != "parameter value" || strings.Contains(f.events[0].Message.(string), "\nTarget:") || !strings.Contains(f.events[0].Message.(string), `"page":"photography"`) {
		t.Fatal("execution did not use parameters", r, f.events)
	}
	// Simulate existing stored labels from an earlier version.
	for _, q := range []string{`UPDATE process_assignments SET body_json=json_set(body_json,'$.target','obsolete label') WHERE id=?`, `UPDATE process_runs SET assignment_json=json_set(assignment_json,'$.target','obsolete frozen label') WHERE id=?`} {
		id := x.ID
		if strings.Contains(q, "process_runs") {
			id = r.ID
		}
		if _, err = a.db.Exec(q, id); err != nil {
			t.Fatal(err)
		}
	}
	var before string
	if err = a.db.QueryRow(`SELECT request_json FROM process_delivery_envelopes WHERE event_id=?`, "processes:"+r.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("migrations/017_remove_assignment_target.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	for _, query := range []struct{ sql, id string }{{`SELECT body_json FROM process_assignments WHERE id=?`, x.ID}, {`SELECT assignment_json FROM process_runs WHERE id=?`, r.ID}} {
		var stored string
		if err = a.db.QueryRow(query.sql, query.id).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if err = json.Unmarshal([]byte(stored), &cfg); err != nil {
			t.Fatal(err)
		}
		if _, exists := cfg["target"]; exists {
			t.Fatal("obsolete label still stored", cfg)
		}
		params := cfg["parameters"].(map[string]any)
		if params["target"] != "parameter value" || params["page"] != "photography" {
			t.Fatal("migration damaged parameters", cfg)
		}
	}
	saved, err := a.getRun(p.ProjectID, p.ID, r.ID)
	if err != nil || saved.ID != r.ID || saved.AssignmentRevision != r.AssignmentRevision || saved.Binding.Parameters["target"] != "parameter value" {
		t.Fatal("run identity or parameters changed", saved, err)
	}
	var after string
	if err = a.db.QueryRow(`SELECT request_json FROM process_delivery_envelopes WHERE event_id=?`, "processes:"+r.ID).Scan(&after); err != nil || before != after {
		t.Fatal("migration changed immutable delivery", err)
	}
	changes := totalChanges(t, a)
	if _, err = a.db.Exec(string(raw)); err != nil || totalChanges(t, a) != changes {
		t.Fatal("migration not idempotent", err)
	}
	// MCP has no separate label; custom parameters remain part of the contract.
	props := assignmentSchema()["properties"].(map[string]any)
	if _, exists := props["target"]; exists {
		t.Fatal("MCP still advertises target label")
	}
	if _, exists := props["parameters"]; !exists {
		t.Fatal("parameter inputs removed")
	}
}
