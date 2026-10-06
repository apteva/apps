package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func saveStepHTTP(t *testing.T, a *App, p *Process, d Definition, version int) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"definition": d, "expected_version": version, "save_as_draft": true})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPut, "/processes/"+p.ID+"?project_id="+p.ProjectID, bytes.NewReader(body))
	w := httptest.NewRecorder()
	a.handleHTTP(w, r)
	return w
}

func TestInlineStepSaveCreatesDraftWithoutChangingExistingRun(t *testing.T) {
	a, f, p, run := sequentialSetup(t)
	before, err := a.runDefinition(run)
	if err != nil {
		t.Fatal(err)
	}
	stepsBefore, _ := a.steps(run.ID)
	events := len(f.events)
	pinned, err := a.saveAssignment(p.ProjectID, p.ID, "", 0, AssignmentConfig{Name: "Pinned", OwnerAgentID: 7, ProcedureVersion: 1, FollowLatest: false})
	if err != nil {
		t.Fatal(err)
	}
	d := p.Definition
	d.Steps = append([]Step(nil), d.Steps...)
	d.Steps[0].Instructions = "Updated instructions for future runs only"
	// MCP callers retain the existing explicit pause/edit lifecycle.
	if _, err = a.executeMCP(p.ProjectID, "agent:7:main", "update", map[string]any{"process_id": p.ID, "expected_version": 1, "definition": d, "save_as_draft": true}); err == nil {
		t.Fatal("MCP bypassed publication lifecycle")
	}
	w := saveStepHTTP(t, a, p, d, 1)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	updated, err := a.get(p.ProjectID, p.ID)
	if err != nil || updated.Status != "draft" || updated.Version != 2 || updated.Steps[0].Instructions != d.Steps[0].Instructions {
		t.Fatal("new draft not saved", updated, err)
	}
	frozen, err := a.runDefinition(run)
	stepsAfter, _ := a.steps(run.ID)
	if err != nil || jsonText(frozen) != jsonText(before) || jsonText(stepsBefore) != jsonText(stepsAfter) || len(f.events) != events {
		t.Fatal("editing changed frozen run or dispatched work", err)
	}
	for _, x := range updated.Assignments {
		if x.FollowLatest && x.ProcedureVersion != 2 || x.ID == pinned.ID && x.ProcedureVersion != 1 {
			t.Fatal("assignment version changed incorrectly", x)
		}
	}
	if _, err := a.start(p.ProjectID, p.ID, "draft-cannot-run", ""); err == nil {
		t.Fatal("save left new draft executable")
	}
	if w = saveStepHTTP(t, a, p, d, 1); w.Code != http.StatusConflict {
		t.Fatal("stale editor overwrote newer version", w.Code, w.Body.String())
	}
}

func TestInvalidInlineStepSaveKeepsPublishedProcedure(t *testing.T) {
	a, _, p, _ := sequentialSetup(t)
	d := p.Definition
	d.Steps = append([]Step(nil), d.Steps...)
	d.Steps[0].Instructions = ""
	w := saveStepHTTP(t, a, p, d, 1)
	if w.Code != http.StatusBadRequest {
		t.Fatal(w.Code, w.Body.String())
	}
	current, err := a.get(p.ProjectID, p.ID)
	if err != nil || current.Status != "active" || current.Version != 1 {
		t.Fatal("failed save changed publication state", current, err)
	}
}
