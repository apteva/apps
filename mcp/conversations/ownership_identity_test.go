package main

import (
	"context"
	"encoding/json"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestVisitorIdentityDoesNotFollowDelegatedWork(t *testing.T) {
	a, app, _ := newTestEnv(t)
	rec := visitorRequest(a, "visitor-42", "POST", "/chats", map[string]any{"agent_ids": []int{41}, "lead_agent_id": 41, "title": "Visitor CRM request"}, visitorActions)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var conv Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &conv); err != nil {
		t.Fatal(err)
	}
	if err := a.store.RecordAgentThread(conv.ID, 41, "original-chat", "", "", ""); err != nil {
		t.Fatal(err)
	}
	app.Config()["identity_resolver_install_ids"] = "99"
	backend := sdk.WithCaller(context.Background(), &sdk.Caller{AppName: "crm", AppInstallID: 99, ProjectID: testProject})
	for _, thread := range []string{"main", "worker-crm", "worker-original-chat"} {
		if _, err := a.toolResolveThreadIdentity(backend, app, map[string]any{"agent_id": float64(41), "thread_id": thread, "subject_id": "visitor-42", "conversation_id": conv.ID}); err == nil {
			t.Fatalf("unbound %s borrowed visitor identity", thread)
		}
	}
	out, err := a.toolResolveThreadIdentity(backend, app, map[string]any{"agent_id": float64(41), "thread_id": "original-chat"})
	if err != nil || out.(map[string]any)["subject_id"] != "visitor-42" {
		t.Fatalf("original authenticated chat no longer resolves: %v %v", out, err)
	}
}
