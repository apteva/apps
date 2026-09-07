package main

import (
	"context"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"testing"
)

func TestThreadIdentityResolutionIsBackendOnlyAndScoped(t *testing.T) {
	a, app, _ := newTestEnv(t)
	rec := visitorRequest(a, "42", "POST", "/chats", map[string]any{"agent_ids": []int{41}, "lead_agent_id": 41, "title": "Identity"}, visitorActions)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var conv Conversation
	if err := json.Unmarshal(rec.Body.Bytes(), &conv); err != nil {
		t.Fatal(err)
	}
	if err := a.store.RecordAgentThread(conv.ID, 41, "opaque-thread", "", "", ""); err != nil {
		t.Fatal(err)
	}
	caller := &sdk.Caller{AppName: "crm", AppInstallID: 99, ProjectID: testProject}
	p, err := a.resolveThreadIdentity(caller, "99", 41, "opaque-thread")
	if err != nil || p["subject_id"] != "42" || p["issuer_app"] != "auth" || p["issuer_install_id"] != "11" {
		t.Fatalf("%v %v", p, err)
	}
	app.Config()["identity_resolver_install_ids"] = "99"
	bound := &sdk.Caller{AppName: "crm", AppInstallID: 99}
	resolved, err := a.toolResolveThreadIdentity(sdk.WithCaller(context.Background(), bound), app, map[string]any{"agent_id": float64(41), "thread_id": "opaque-thread"})
	if err != nil || resolved.(map[string]any)["subject_id"] != "42" || bound.ProjectID != "" {
		t.Fatalf("verified AppCtx project fallback: %v %v", resolved, err)
	}
	if _, err := a.toolResolveThreadIdentity(context.Background(), app, map[string]any{"agent_id": float64(41), "thread_id": "opaque-thread"}); err == nil {
		t.Fatal("AppCtx alone must not authenticate a caller")
	}
	for _, c := range []*sdk.Caller{nil, {AgentID: 41, ThreadID: "opaque-thread", ProjectID: testProject}, {AppName: "crm", AppInstallID: 98, ProjectID: testProject}, {AppName: "crm", AppInstallID: 99, ProjectID: "other"}} {
		if _, err := a.resolveThreadIdentity(c, "99", 41, "opaque-thread"); err == nil {
			t.Fatal("unauthorized resolution allowed")
		}
	}
	if _, err := a.resolveThreadIdentity(caller, "", 41, "opaque-thread"); err == nil {
		t.Fatal("empty allowlist allowed")
	}
	if _, err := a.resolveThreadIdentity(caller, "99", 42, "opaque-thread"); err == nil {
		t.Fatal("wrong agent allowed")
	}
	if _, err := a.resolveThreadIdentity(caller, "99", 41, "main"); err == nil {
		t.Fatal("unbound thread allowed")
	}
	if _, err := a.store.SetConversationArchived(conv.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := a.resolveThreadIdentity(caller, "99", 41, "opaque-thread"); err == nil {
		t.Fatal("archived chat allowed")
	}
}
