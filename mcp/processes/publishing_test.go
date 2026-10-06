package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReturnToDraftPreservesWorkAndStopsNewRuns(t *testing.T) {
	a, f, p := directSetup(t)
	x := enableAssignment(t, a, p, addAssignment(t, a, p, "Scheduled", 7, "agent", &Schedule{Kind: "interval", Every: "1m"}, nil))
	paused := addAssignment(t, a, p, "Paused", 8, "agent", nil, nil)
	r := startOn(t, a, p, x, "existing", nil)
	before, err := a.dispatches(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := a.versions(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/processes/"+p.ID+"/draft?project_id="+p.ProjectID, strings.NewReader(`{}`))
	a.handleHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("draft HTTP: %d %s", response.Code, response.Body.String())
	}
	var draft Process
	if err := json.Unmarshal(response.Body.Bytes(), &draft); err != nil {
		t.Fatal(err)
	}
	if draft.Status != "draft" || draft.SyncPending || draft.Version != p.Version {
		t.Fatalf("draft=%+v", draft)
	}
	afterVersions, err := a.versions(p.ID)
	if err != nil || jsonText(versions) != jsonText(afterVersions) {
		t.Fatal("returning to draft changed immutable versions", err)
	}
	after, err := a.dispatches(p.ID)
	if err != nil || jsonText(before) != jsonText(after) {
		t.Fatal("returning to draft changed existing run", err)
	}
	current, err := a.assignment(p.ProjectID, p.ID, x.ID)
	if err != nil || current.NextRunAt != "" || current.Status != "active" || jsonText(current.AssignmentConfig) != jsonText(x.AssignmentConfig) {
		t.Fatal("draft did not suspend scheduling while preserving assignment", err)
	}
	if _, err := a.startAssignment(p.ProjectID, p.ID, x.ID, "new", "", nil); err == nil {
		t.Fatal("draft allowed a new manual run")
	}
	if err := a.tickDirect(context.Background(), time.Now().Add(24*time.Hour)); err != nil || len(f.events) != 1 {
		t.Fatal("draft scheduled new work", err)
	}
	if _, err := a.directRun(p.ProjectID, "agent:7:thread", p.ID, r.ID, "run_update", map[string]any{"state": "completed", "result": "Existing run completed with receipt.png"}); err != nil {
		t.Fatal("existing work could not finish in draft", err)
	}
	// Repeating the MCP action is safe, and publishing restores enabled intent.
	if _, err := a.executeMCP(p.ProjectID, "agent:7:thread", "draft", map[string]any{"process_id": p.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.executeMCP(p.ProjectID, "agent:7:thread", "activate", map[string]any{"process_id": p.ID}); err != nil {
		t.Fatal(err)
	}
	if err := a.tickDirect(context.Background(), time.Now().Add(2*time.Minute)); err != nil || len(f.events) != 2 {
		t.Fatal("publishing did not restore scheduling", err)
	}
	stillPaused, err := a.assignment(p.ProjectID, p.ID, paused.ID)
	if err != nil || stillPaused.Status != "paused" {
		t.Fatal("publishing enabled a paused assignment", err)
	}
	status(t, a, p.ID, "draft")
	if _, err := a.save(p.ProjectID, p.ID, "operator", p.Version, p.Definition); err != nil {
		t.Fatal("returned draft could not be edited", err)
	}
	status(t, a, p.ID, "archived")
	if _, err := a.executeMCP(p.ProjectID, "agent:7:thread", "draft", map[string]any{"process_id": p.ID}); err == nil {
		t.Fatal("archived process returned to draft")
	}
}
