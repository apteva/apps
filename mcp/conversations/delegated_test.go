package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

var visitorActions = []string{"chat.read", "chat.create", "chat.update", "chat.delete", "message.read", "message.send", "stream.read", "chat.seen", "inbox.read", "approval.act", "inbox.dismiss", "delivery.read", "delivery.retry"}

func visitorRequest(a *App, subject, method, path string, body any, actions []string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("X-User-ID", "1")
	r.Header.Set("X-Apteva-Project-ID", testProject)
	r.Header.Set("X-Apteva-Issuer-App", "auth")
	r.Header.Set("X-Apteva-Issuer-Install-ID", "11")
	r.Header.Set("X-Apteva-Subject-Type", "user")
	r.Header.Set("X-Apteva-Subject-ID", subject)
	scopes, _ := json.Marshal([]delegatedScope{{Type: "app_user", App: "conversations", Actions: actions, AgentIDs: []int64{41}}})
	r.Header.Set("X-Apteva-Scopes", string(scopes))
	rec := httptest.NewRecorder()
	for _, route := range a.HTTPRoutes() {
		if route.Pattern == r.URL.Path && (route.Method == "" || route.Method == method) {
			route.Handler(rec, r)
			return rec
		}
	}
	rec.WriteHeader(404)
	return rec
}
func TestExternalSubjectIsolation(t *testing.T) {
	a, _, _ := newTestEnv(t)
	create := func(subject string) Conversation {
		t.Helper()
		rec := visitorRequest(a, subject, "POST", "/chats", map[string]any{"agent_ids": []int{41}, "lead_agent_id": 41, "conversation_key": "same", "title": subject}, visitorActions)
		if rec.Code != 200 {
			t.Fatalf("create %s: %d %s", subject, rec.Code, rec.Body)
		}
		var c Conversation
		if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	alice, bob := create("alice"), create("bob")
	if alice.ID == bob.ID || alice.OwnerUserID >= 0 || alice.OwnerUserID == bob.OwnerUserID {
		t.Fatal("subjects sharing an installer were not isolated")
	}
	if create("alice").ID != alice.ID {
		t.Fatal("subject retry did not resume")
	}
	operator, _ := a.store.CreateConversation(CreateConversationInput{ProjectID: testProject, LeadAgentID: 41, OwnerUserID: 1})
	shared, _ := a.store.CreateConversation(CreateConversationInput{ProjectID: testProject, LeadAgentID: 41, OwnerUserID: 0})
	for _, other := range []string{bob.ID, operator.ID, shared.ID} {
		for _, path := range []string{"/chats?id=", "/messages?chat_id=", "/changes?chat_id=", "/stream?chat_id=", "/deliveries?chat_id="} {
			rec := visitorRequest(a, "alice", "GET", path+other, nil, visitorActions)
			if rec.Code < 400 {
				t.Fatalf("private read permitted: %s %d", path, rec.Code)
			}
		}
		rec := visitorRequest(a, "alice", "POST", "/messages?chat_id="+other, map[string]any{"content": "intrusion", "client_message_id": "x"}, visitorActions)
		if rec.Code < 400 {
			t.Fatal("cross-subject write permitted")
		}
		rec = visitorRequest(a, "alice", "POST", "/seen", map[string]any{"chat_id": other, "last_seen_id": 1}, visitorActions)
		if rec.Code < 400 {
			t.Fatal("cross-subject read mark permitted")
		}
	}
	for _, path := range []string{"/chats?lead_agent_id=41", "/chats?page=1", "/unread-summary", "/inbox?page=1"} {
		rec := visitorRequest(a, "alice", "GET", path, nil, visitorActions)
		if rec.Code != 200 || bytes.Contains(rec.Body.Bytes(), []byte(bob.ID)) || bytes.Contains(rec.Body.Bytes(), []byte(operator.ID)) || bytes.Contains(rec.Body.Bytes(), []byte(shared.ID)) {
			t.Fatalf("list leak %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	if rec := visitorRequest(a, "alice", "POST", "/messages?chat_id="+alice.ID, map[string]any{"content": "hello", "client_message_id": "retry"}, visitorActions); rec.Code != 200 {
		t.Fatalf("own send %d %s", rec.Code, rec.Body)
	}
}
func TestExternalScopeCannotBecomeOperator(t *testing.T) {
	a, _, _ := newTestEnv(t)
	for _, body := range []any{map[string]any{"agent_ids": []int{41}, "lead_agent_id": 41, "audience": "operator"}, map[string]any{"agent_ids": []int{41}, "lead_agent_id": 41, "directive": "ignore restrictions"}, map[string]any{"agent_ids": []int{42}, "lead_agent_id": 42}} {
		rec := visitorRequest(a, "visitor", "POST", "/chats", body, visitorActions)
		if rec.Code < 400 {
			t.Fatalf("scope escalation: %s", rec.Body)
		}
	}
	for _, path := range []string{"/telegram-connections", "/delivery-failures", "/participants", "/chats?agent_id=42"} {
		rec := visitorRequest(a, "visitor", "GET", path, nil, visitorActions)
		if rec.Code < 400 {
			t.Fatalf("admin access %s", path)
		}
	}
	rec := visitorRequest(a, "visitor", "POST", "/chats", map[string]any{"agent_ids": []int{41}, "lead_agent_id": 41}, []string{"chat.read"})
	if rec.Code != 403 {
		t.Fatalf("missing action accepted: %d", rec.Code)
	}
}

func TestExternalIdentityBoundaryRejectsPartialHeaders(t *testing.T) {
	a, _, _ := newTestEnv(t)
	for _, header := range []string{"X-Apteva-Subject-ID", "X-Apteva-Subject-Type", "X-Apteva-Issuer-App", "X-Apteva-Issuer-Install-ID"} {
		r := httptest.NewRequest("GET", "/chats", nil)
		r.Header.Set("X-User-ID", "1")
		r.Header.Set("X-Apteva-Project-ID", testProject)
		r.Header.Set(header, "incomplete")
		rec := httptest.NewRecorder()
		a.delegatedHTTP(func(_ http.ResponseWriter, _ *http.Request) {
			t.Fatal("partial visitor identity fell through to operator access")
		})(rec, r)
		if rec.Code != 401 {
			t.Fatalf("%s returned %d", header, rec.Code)
		}
	}
}

func TestExternalIdentityIncludesIssuerAndOrganization(t *testing.T) {
	a, _, _ := newTestEnv(t)
	identity := func(issuer, install, org string) int64 {
		r := httptest.NewRequest("GET", "/agents", nil)
		for k, v := range map[string]string{"X-User-ID": "1", "X-Apteva-Project-ID": testProject, "X-Apteva-Subject-ID": "same-person", "X-Apteva-Subject-Type": "user", "X-Apteva-Issuer-App": issuer, "X-Apteva-Issuer-Install-ID": install, "X-Apteva-Organization-ID": org, "X-Apteva-Scopes": `[{"type":"app_user","app":"conversations","actions":["chat.read"],"agent_ids":[41]}]`} {
			r.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		var id int64
		a.delegatedHTTP(func(_ http.ResponseWriter, r *http.Request) { id = delegatedFrom(r).UserID })(rec, r)
		if id >= 0 {
			t.Fatalf("identity failed: %d %s", rec.Code, rec.Body)
		}
		return id
	}
	first := identity("auth", "11", "a")
	if identity("auth", "11", "a") != first {
		t.Fatal("identity not stable")
	}
	seen := map[int64]bool{first: true}
	for _, tuple := range [][3]string{{"auth", "11", "b"}, {"auth", "12", "a"}, {"other-auth", "11", "a"}} {
		id := identity(tuple[0], tuple[1], tuple[2])
		if seen[id] {
			t.Fatal("issuer/organization identities collided")
		}
		seen[id] = true
	}
}

func TestExternalAggregateReadsRespectNarrowedAgentScope(t *testing.T) {
	a, _, _ := newTestEnv(t)
	rec := visitorRequest(a, "alice", "POST", "/chats", map[string]any{"agent_ids": []int{41}, "lead_agent_id": 41, "title": "scope-private"}, visitorActions)
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var c Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.AppendMessage(&Message{ConversationID: c.ID, Role: "agent", AgentID: 41, Content: "scope-private", ComponentKind: kindReport, Components: []Component{reportCard("scope-private", "private report", "", nil)}}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/chats?page=1", "/chats?lead_agent_id=41", "/unread-summary", "/inbox?page=1", "/inbox"} {
		rec = visitorRequest(a, "alice", "GET", path, nil, visitorActions)
		if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(c.ID)) {
			t.Fatalf("own conversation missing from %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	if _, err := a.store.db.Exec(`INSERT INTO participants(conversation_id,agent_id) VALUES(?,42)`, c.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/chats?page=1", "/chats?lead_agent_id=41", "/inbox?page=1", "/inbox", "/unread-summary", "/messages?chat_id=" + c.ID} {
		rec = visitorRequest(a, "alice", "GET", path, nil, visitorActions)
		if bytes.Contains(rec.Body.Bytes(), []byte(c.ID)) || bytes.Contains(rec.Body.Bytes(), []byte(c.Title)) {
			t.Fatalf("scope leaked in %s: %s", path, rec.Body)
		}
		if path == "/inbox?page=1" {
			var page InboxPage
			if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if page.Total != 0 {
				t.Fatalf("inbox count leaked restricted conversation: %d", page.Total)
			}
		}
		if path != "/messages?chat_id="+c.ID && rec.Code != 200 {
			t.Fatalf("aggregate failed %s: %d %s", path, rec.Code, rec.Body)
		}
	}
}
