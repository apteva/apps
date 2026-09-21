package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func appendInboxFixture(t *testing.T, app *App, projectID string, ownerUserID, agentID int64, kind, severity string) int64 {
	t.Helper()
	leadAgentID := agentID
	if leadAgentID == 0 {
		leadAgentID = 41
	}
	conv, err := app.store.CreateConversation(CreateConversationInput{
		ProjectID: projectID, LeadAgentID: leadAgentID, Title: projectID + " inbox", OwnerUserID: ownerUserID,
	})
	if err != nil {
		t.Fatal(err)
	}
	message := &Message{
		ConversationID: conv.ID,
		Role:           "agent",
		AgentID:        agentID,
		Content:        kind + " fixture",
		ComponentKind:  kind,
		Severity:       severity,
		Status:         "complete",
	}
	switch kind {
	case kindApproval:
		message.ActionStatus = "pending"
		message.Components = []Component{approvalCard("Approve fixture", "", nil)}
	case kindAlert:
		message.Components = []Component{alertCard("Alert fixture", severity)}
	case kindReport:
		message.Components = []Component{reportCard("Report fixture", "Body", "", nil)}
	}
	inserted, err := app.store.AppendMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	return inserted.ID
}

func globalInboxRequest(t *testing.T, app *App, target string) (*httptest.ResponseRecorder, InboxPage) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("X-User-ID", "1")
	rec := httptest.NewRecorder()
	app.handleInbox(rec, req)
	var page InboxPage
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode global inbox: %v body=%s", err, rec.Body.String())
		}
	}
	return rec, page
}

func TestGlobalInboxUsesVisibleProjectsAndExistingOwnershipRules(t *testing.T) {
	app, _, platform := newTestEnv(t)
	platform.projects = []sdk.PlatformProject{
		{ID: "proj-1", Name: "Alpha"},
		{ID: "proj-2", Name: "Beta"},
	}

	approvalID := appendInboxFixture(t, app, "proj-1", 1, 41, kindApproval, "")
	errorID := appendInboxFixture(t, app, "proj-2", 1, 43, kindAlert, "error")
	systemID := appendInboxFixture(t, app, "proj-2", 0, 0, kindReport, "")
	appendInboxFixture(t, app, "proj-hidden", 1, 41, kindReport, "")
	appendInboxFixture(t, app, "proj-2", 2, 55, kindReport, "")

	rec, page := globalInboxRequest(t, app, "/inbox?page=1&scope=global&limit=100")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if page.Total != 3 || len(page.Items) != 3 {
		t.Fatalf("global inbox=%+v", page)
	}
	wantIDs := []int64{approvalID, errorID, systemID}
	for i, wantID := range wantIDs {
		if page.Items[i].Message.ID != wantID {
			t.Fatalf("item %d id=%d want=%d", i, page.Items[i].Message.ID, wantID)
		}
		if page.Items[i].ProjectID == "" || page.Items[i].ProjectName == "" {
			t.Fatalf("item %d missing project metadata: %+v", i, page.Items[i])
		}
	}
	if page.Items[0].AgentName != "Research" || page.Items[1].AgentName != "Comms" {
		t.Fatalf("agent names=%q,%q", page.Items[0].AgentName, page.Items[1].AgentName)
	}
	if len(page.Projects) != 2 || page.Projects[0].Name != "Alpha" || page.Projects[1].Name != "Beta" {
		t.Fatalf("projects=%+v", page.Projects)
	}
}

func TestGlobalInboxProjectSelectorIsAllowlisted(t *testing.T) {
	app, _, platform := newTestEnv(t)
	platform.projects = []sdk.PlatformProject{{ID: "proj-1", Name: "Alpha"}, {ID: "proj-2", Name: "Beta"}}
	appendInboxFixture(t, app, "proj-1", 1, 41, kindApproval, "")
	selectedID := appendInboxFixture(t, app, "proj-2", 1, 43, kindAlert, "warn")

	rec, page := globalInboxRequest(t, app, "/inbox?page=1&scope=global&project_id=proj-2")
	if rec.Code != http.StatusOK || page.Total != 1 || len(page.Items) != 1 || page.Items[0].Message.ID != selectedID || page.SelectedProjectID != "proj-2" {
		t.Fatalf("selected status=%d page=%+v body=%s", rec.Code, page, rec.Body.String())
	}

	forbidden, _ := globalInboxRequest(t, app, "/inbox?page=1&scope=global&project_id=proj-hidden")
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("forged project status=%d body=%s", forbidden.Code, forbidden.Body.String())
	}
}

func TestGlobalInboxCursorPreservesPriorityAcrossProjects(t *testing.T) {
	app, _, platform := newTestEnv(t)
	platform.projects = []sdk.PlatformProject{{ID: "proj-1", Name: "Alpha"}, {ID: "proj-2", Name: "Beta"}}
	firstID := appendInboxFixture(t, app, "proj-2", 1, 43, kindApproval, "")
	secondID := appendInboxFixture(t, app, "proj-1", 1, 41, kindAlert, "error")

	rec, first := globalInboxRequest(t, app, "/inbox?page=1&scope=global&limit=1")
	if rec.Code != http.StatusOK || len(first.Items) != 1 || first.Items[0].Message.ID != firstID || first.NextCursor == "" {
		t.Fatalf("first page status=%d page=%+v", rec.Code, first)
	}
	rec, second := globalInboxRequest(t, app, "/inbox?page=1&scope=global&limit=1&cursor="+first.NextCursor)
	if rec.Code != http.StatusOK || len(second.Items) != 1 || second.Items[0].Message.ID != secondID || second.NextCursor != "" {
		t.Fatalf("second page status=%d page=%+v", rec.Code, second)
	}
}

func TestGlobalInboxRequiresOperatorIdentity(t *testing.T) {
	app, _, _ := newTestEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/inbox?page=1&scope=global", nil)
	rec := httptest.NewRecorder()
	app.handleInbox(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d body=%s", rec.Code, rec.Body.String())
	}

	delegated := httptest.NewRequest(http.MethodGet, "/inbox?page=1&scope=global", nil)
	delegated.Header.Set("X-User-ID", "1")
	delegated = delegated.WithContext(context.WithValue(delegated.Context(), delegatedContextKey{}, &delegatedPrincipal{UserID: -1, Agents: map[int64]bool{41: true}}))
	delegatedRec := httptest.NewRecorder()
	app.handleInbox(delegatedRec, delegated)
	if delegatedRec.Code != http.StatusForbidden {
		t.Fatalf("delegated status=%d body=%s", delegatedRec.Code, delegatedRec.Body.String())
	}

	invalid := httptest.NewRequest(http.MethodGet, "/inbox?page=1&scope=workspace", nil)
	invalid.Header.Set("X-User-ID", "1")
	invalidRec := httptest.NewRecorder()
	app.handleInbox(invalidRec, invalid)
	if invalidRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid scope status=%d body=%s", invalidRec.Code, invalidRec.Body.String())
	}
}
