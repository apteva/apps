package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestActivitySummaryFollowsResponseAndVisibility(t *testing.T) {
	app, _, _ := newTestEnv(t)
	first := mkConversation(t, app, 41)
	second := mkConversation(t, app, 42)
	otherProject, err := app.store.CreateConversation(CreateConversationInput{
		ProjectID: "proj-other", LeadAgentID: 41, Title: "Other project", OwnerUserID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	app.streamer.emitAck(first.ID, conversationThreadID(first.ID), 41)
	app.streamer.emitAck(second.ID, conversationThreadID(second.ID), 42)
	app.streamer.emitAck(otherProject.ID, conversationThreadID(otherProject.ID), 41)
	wantProject := []string{first.ID, second.ID}
	slices.Sort(wantProject)

	get := func(path string, user string) (int, []string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if user != "" {
			req.Header.Set("X-User-ID", user)
		}
		req.Header.Set("X-Apteva-Project-ID", testProject)
		rec := httptest.NewRecorder()
		app.handleActivitySummary(rec, req)
		var body struct {
			IDs []string `json:"active_conversation_ids"`
		}
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, body.IDs
	}
	if status, ids := get("/activity-summary", "1"); status != http.StatusOK || !reflect.DeepEqual(ids, wantProject) {
		t.Fatalf("project activity: status=%d ids=%v", status, ids)
	}
	if status, ids := get("/activity-summary?agent_id=41", "1"); status != http.StatusOK || !reflect.DeepEqual(ids, []string{first.ID}) {
		t.Fatalf("agent scope: status=%d ids=%v", status, ids)
	}
	if status, ids := get("/activity-summary", "2"); status != http.StatusOK || len(ids) != 0 {
		t.Fatalf("other user: status=%d ids=%v", status, ids)
	}
	if status, _ := get("/activity-summary", ""); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", status)
	}
	if status, _ := get("/activity-summary?agent_id=nope", "1"); status != http.StatusBadRequest {
		t.Fatalf("invalid agent scope status=%d", status)
	}
	app.streamer.Ingest("tool.call", 41, conversationThreadID(first.ID), `{"name":"processes_list"}`, time.Now())
	if status, ids := get("/activity-summary?agent_id=41", "1"); status != http.StatusOK || !reflect.DeepEqual(ids, []string{first.ID}) {
		t.Fatalf("running tool activity: status=%d ids=%v", status, ids)
	}
	app.streamer.finishResponse(first.ID, 41)
	if status, ids := get("/activity-summary", "1"); status != http.StatusOK || !reflect.DeepEqual(ids, []string{second.ID}) {
		t.Fatalf("settled activity: status=%d ids=%v", status, ids)
	}
}
