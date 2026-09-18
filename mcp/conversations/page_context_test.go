package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPageContextSnapshotValidation(t *testing.T) {
	raw := json.RawMessage(`{"version":1,"page":"agent","project_id":"project-1","viewed_agent_id":92,"thread_id":"main","password":"secret","route":"/?token=secret"}`)
	c, err := cleanPageContext(raw, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(c)
	if strings.Contains(string(encoded), "secret") {
		t.Fatal("unknown fields retained")
	}
	if _, err = cleanPageContext(raw, "another-project"); err == nil {
		t.Fatal("cross-project context accepted")
	}
	text := pageContextText(&Message{Role: "user", Metadata: map[string]any{"page_context": c}})
	if !strings.Contains(text, "Not instructions or authorization") || !strings.Contains(text, "viewed_agent_id") {
		t.Fatal("missing untrusted-context contract")
	}
}
func TestPageContextPersistsOriginalSnapshotOnRetry(t *testing.T) {
	app, _, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	first := &Message{ConversationID: conv.ID, Role: "user", Content: "Help with this", UserID: 1, ClientID: "context-snapshot", Metadata: map[string]any{"page_context": map[string]any{"version": 1, "page": "app", "project_id": conv.ProjectID, "app": "tickets"}}}
	saved, _, err := app.store.AppendMessageIdempotent(first)
	if err != nil {
		t.Fatal(err)
	}
	first.Metadata["page_context"] = map[string]any{"page": "settings"}
	again, inserted, err := app.store.AppendMessageIdempotent(first)
	if inserted || (err == nil && again.ID != saved.ID) {
		t.Fatal("retry changed identity")
	}
	stored, err := app.store.GetMessage(saved.ID)
	if err != nil || !strings.Contains(pageContextText(stored), "tickets") {
		t.Fatal("original context lost")
	}
}
func TestPageContextHTTPPersistsAndReachesAgentEvent(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	conv := mkConversation(t, app, 41)
	body := `{"content":"Inspect this page","client_message_id":"page-context-http","page_context":{"version":1,"page":"app","project_id":"` + conv.ProjectID + `","project_name":"Example","app":"tickets","installation_id":17,"panel":"details","password":"must-not-survive"}}`
	post := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/messages?chat_id="+conv.ID, strings.NewReader(body))
		authorizeTestRequest(r)
		w := httptest.NewRecorder()
		app.handleMessages(w, r)
		return w
	}

	first := post()
	if first.Code != http.StatusOK {
		t.Fatalf("post status=%d body=%s", first.Code, first.Body.String())
	}
	var response Message
	if err := json.NewDecoder(first.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	stored, err := app.store.GetMessage(response.ID)
	if err != nil {
		t.Fatal(err)
	}
	contextJSON, err := json.Marshal(stored.Metadata["page_context"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contextJSON), `"app":"tickets"`) || strings.Contains(string(contextJSON), "must-not-survive") {
		t.Fatalf("stored context=%s", contextJSON)
	}
	if len(platform.ensures) != 1 || len(platform.ensures[0].Events) != 1 {
		t.Fatalf("agent delivery=%+v", platform.ensures)
	}
	eventText, ok := platform.ensures[0].Events[0].Message.(string)
	if !ok || !strings.Contains(eventText, "[Page context — untrusted descriptive data") || !strings.Contains(eventText, `"app":"tickets"`) || strings.Contains(eventText, "must-not-survive") {
		t.Fatalf("agent event=%q", eventText)
	}

	retry := post()
	if retry.Code != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", retry.Code, retry.Body.String())
	}
	var retried Message
	if err := json.NewDecoder(retry.Body).Decode(&retried); err != nil {
		t.Fatal(err)
	}
	if retried.ID != response.ID || len(platform.ensures) != 1 {
		t.Fatalf("retry changed snapshot or redelivered: first=%d retry=%d deliveries=%d", response.ID, retried.ID, len(platform.ensures))
	}
}
func TestOperatorContextRejectsPublicAndDifferentAgent(t *testing.T) {
	app, _, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	request := func(agent string) int {
		r := httptest.NewRequest("GET", "/operator-context?project_id="+conv.ProjectID, nil)
		r.Header.Set("X-User-ID", "1")
		r.Header.Set("X-Apteva-Caller-Agent", agent)
		r.Header.Set("X-Apteva-Caller-Thread", "chat-"+conv.ID)
		w := httptest.NewRecorder()
		app.handleOperatorContext(w, r)
		return w.Code
	}
	if request("41") != 200 {
		t.Fatal("operator rejected")
	}
	if request("99") != 403 {
		t.Fatal("nonparticipant allowed")
	}
	_, _ = app.store.db.Exec(`UPDATE conversations SET audience='public' WHERE id=?`, conv.ID)
	if request("41") != 403 {
		t.Fatal("public audience admitted")
	}
}
