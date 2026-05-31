package main

// Tests for conversation status/priority (migration 005): the
// contacts_set_conversation_status tool, validation, and the
// inbound-reply auto-reopen wiring in handleInbound.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type crmCallAppCall struct {
	AppName string
	Tool    string
	Input   map[string]any
}

type crmRecordingPlatform struct {
	tk.BasePlatformClient
	calls []crmCallAppCall
}

func (p *crmRecordingPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{
		AppName:   "crm",
		InstallID: 99,
		ProjectID: "test-proj",
		Bindings:  map[string]any{"messaging": float64(42)},
	}, nil
}

func (p *crmRecordingPlatform) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	return &sdk.PlatformInstance{ID: id, Name: "messaging", Status: "running", ProjectID: "test-proj"}, nil
}

func (p *crmRecordingPlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	p.calls = append(p.calls, crmCallAppCall{AppName: appName, Tool: tool, Input: input})
	if out != nil {
		b, _ := json.Marshal(map[string]any{"ok": true})
		_ = json.Unmarshal(b, out)
	}
	return nil
}

// mkConversation inserts a conversation directly (the normal creators
// need a send/inbound round-trip; here we just want a row to mutate).
func mkConversation(t *testing.T, ctx *sdk.AppCtx, pid string, contactID int64, channel string) int64 {
	t.Helper()
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	id, err := dbConversationCreate(tx, pid, contactID, channel, "Subject", "", "2026-05-20T00:00:00Z")
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSetConversationStatus_DefaultsToOpenNormal(t *testing.T) {
	ctx := newTestCtx(t)
	c := mustCreate(t, ctx, map[string]any{"first_name": "Alice"})
	convoID := mkConversation(t, ctx, "test-proj", c.ID, "email")

	got, err := dbConversationGet(ctx.AppDB(), "test-proj", convoID)
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "open" || got.Priority != "normal" {
		t.Fatalf("new conversation status=%q priority=%q, want open/normal", got.Status, got.Priority)
	}
}

func TestSetConversationStatus_FlipsStatusAndPriority(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCreate(t, ctx, map[string]any{"first_name": "Alice"})
	convoID := mkConversation(t, ctx, "test-proj", c.ID, "email")

	// status only
	out, err := app.toolSetConversationStatus(ctx, map[string]any{
		"conversation_id": convoID, "status": "pending",
	})
	if err != nil {
		t.Fatal(err)
	}
	conv := out.(map[string]any)["conversation"].(*Conversation)
	if conv.Status != "pending" {
		t.Fatalf("status=%q, want pending", conv.Status)
	}
	if conv.StatusChangedAt == "" {
		t.Errorf("status_changed_at should be stamped on a status change")
	}
	if conv.Priority != "normal" {
		t.Errorf("priority should be unchanged at normal, got %q", conv.Priority)
	}

	// priority only — must not reset status
	out, err = app.toolSetConversationStatus(ctx, map[string]any{
		"conversation_id": convoID, "priority": "urgent",
	})
	if err != nil {
		t.Fatal(err)
	}
	conv = out.(map[string]any)["conversation"].(*Conversation)
	if conv.Priority != "urgent" {
		t.Fatalf("priority=%q, want urgent", conv.Priority)
	}
	if conv.Status != "pending" {
		t.Errorf("priority-only change clobbered status to %q", conv.Status)
	}
}

func TestSetConversationStatus_Validates(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCreate(t, ctx, map[string]any{"first_name": "Alice"})
	convoID := mkConversation(t, ctx, "test-proj", c.ID, "email")

	cases := []struct {
		name string
		args map[string]any
	}{
		{"bad status", map[string]any{"conversation_id": convoID, "status": "snoozed"}},
		{"bad priority", map[string]any{"conversation_id": convoID, "priority": "critical"}},
		{"nothing to set", map[string]any{"conversation_id": convoID}},
		{"missing convo id", map[string]any{"status": "closed"}},
		{"unknown convo", map[string]any{"conversation_id": int64(999999), "status": "closed"}},
		{"wrong contact", map[string]any{"conversation_id": convoID, "id": int64(424242), "status": "closed"}},
	}
	for _, tc := range cases {
		if _, err := app.toolSetConversationStatus(ctx, tc.args); err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
	}
}

func TestSetConversationStatus_FilterByStatus(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCreate(t, ctx, map[string]any{"first_name": "Alice"})
	openID := mkConversation(t, ctx, "test-proj", c.ID, "email")
	closedID := mkConversation(t, ctx, "test-proj", c.ID, "email")
	if _, err := app.toolSetConversationStatus(ctx, map[string]any{"conversation_id": closedID, "status": "closed"}); err != nil {
		t.Fatal(err)
	}

	out, err := app.toolListConversations(ctx, map[string]any{"id": c.ID, "status": "open"})
	if err != nil {
		t.Fatal(err)
	}
	convos := out.(map[string]any)["conversations"].([]*Conversation)
	if len(convos) != 1 || convos[0].ID != openID {
		t.Fatalf("status=open filter returned %d convos, want just the open one", len(convos))
	}
}

func TestSetConversationStatus_MarkSpamSuppressesSender(t *testing.T) {
	pf := &crmRecordingPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	app := &App{}
	c := mustCreate(t, ctx, map[string]any{
		"first_name":    "Spammy",
		"primary_email": "fallback@bad.test",
	})
	convoID := mkConversation(t, ctx, "test-proj", c.ID, "email")
	if err := dbConversationParticipantsAdd(ctx.AppDB(), "test-proj", convoID, "email", []conversationParticipant{
		{Role: "from", Address: "Spammer <spammer@bad.test>", ContactID: c.ID},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := app.toolSetConversationStatus(ctx, map[string]any{
		"conversation_id": convoID,
		"status":          "spam",
	})
	if err != nil {
		t.Fatal(err)
	}
	conv := out.(map[string]any)["conversation"].(*Conversation)
	if conv.Status != "spam" {
		t.Fatalf("conversation status=%q, want spam", conv.Status)
	}
	got, err := dbGetByID(ctx.AppDB(), "test-proj", c.ID)
	if err != nil || got == nil {
		t.Fatalf("get contact: %v", err)
	}
	if got.Status != "spam" {
		t.Fatalf("contact status=%q, want spam", got.Status)
	}
	if !contactHasTag(t, ctx, c.ID, "spam") {
		t.Fatalf("contact missing spam tag")
	}
	if len(pf.calls) != 1 {
		t.Fatalf("suppression calls=%d, want 1", len(pf.calls))
	}
	call := pf.calls[0]
	if call.AppName != "messaging" || call.Tool != "suppression_add" {
		t.Fatalf("call=%s/%s, want messaging/suppression_add", call.AppName, call.Tool)
	}
	if call.Input["kind"] != "address" || call.Input["address"] != "spammer@bad.test" || call.Input["source"] != "crm" {
		t.Fatalf("suppression input=%#v", call.Input)
	}
}

func TestSetConversationStatus_MarkSpamSuppressesDomain(t *testing.T) {
	pf := &crmRecordingPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	app := &App{}
	c := mustCreate(t, ctx, map[string]any{"primary_email": "person@example.test"})
	convoID := mkConversation(t, ctx, "test-proj", c.ID, "email")
	if err := dbConversationParticipantsAdd(ctx.AppDB(), "test-proj", convoID, "email", []conversationParticipant{
		{Role: "from", Address: "person@example.test", ContactID: c.ID},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := app.toolSetConversationStatus(ctx, map[string]any{
		"conversation_id": convoID,
		"status":          "spam",
		"spam_scope":      "domain",
	}); err != nil {
		t.Fatal(err)
	}
	if len(pf.calls) != 1 {
		t.Fatalf("suppression calls=%d, want 1", len(pf.calls))
	}
	if pf.calls[0].Input["kind"] != "domain" || pf.calls[0].Input["address"] != "example.test" {
		t.Fatalf("suppression input=%#v", pf.calls[0].Input)
	}
}

// TestInbound_ReopensClosedConversation drives two inbound messages
// through handleInbound: the first establishes a contact + conversation,
// then we close it, and the second (same sender) must auto-reopen it.
func TestInbound_ReopensClosedConversation(t *testing.T) {
	ctx := newTestCtx(t)
	globalCtx = ctx // handleInbound reads getAppCtx() == globalCtx
	app := &App{}

	post := func(t *testing.T) map[string]any {
		t.Helper()
		payload := `{"channel":"sms","from":"+15551230000","to":["+15559990000"],"body_text":"hello"}`
		r := httptest.NewRequest("POST", "/inbound?project_id=test-proj", bytes.NewBufferString(payload))
		w := httptest.NewRecorder()
		app.handleInbound(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("inbound status=%d body=%s", w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	first := post(t)
	convoID := int64(first["conversation_id"].(float64))
	if convoID == 0 {
		t.Fatalf("first inbound produced no conversation: %v", first)
	}

	// Close it, then a new inbound reply should reopen it.
	if _, err := app.toolSetConversationStatus(ctx, map[string]any{
		"conversation_id": convoID, "status": "closed",
	}); err != nil {
		t.Fatal(err)
	}

	second := post(t)
	if int64(second["conversation_id"].(float64)) != convoID {
		t.Fatalf("second inbound landed on a different conversation: %v", second)
	}
	got, err := dbConversationGet(ctx.AppDB(), "test-proj", convoID)
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "open" {
		t.Errorf("conversation status=%q after inbound reply, want open (auto-reopen)", got.Status)
	}
}

func TestInbound_FromSpamContactStaysSpam(t *testing.T) {
	ctx := newTestCtx(t)
	globalCtx = ctx // handleInbound reads getAppCtx() == globalCtx
	app := &App{}
	created, err := app.toolUpsertByChannel(ctx, map[string]any{
		"kind":  "email",
		"value": "spammer@bad.test",
		"defaults": map[string]any{
			"first_name": "Spammer",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := created.(map[string]any)["contact"].(*Contact)
	if err := dbContactMarkSpam(ctx.AppDB(), "test-proj", c.ID); err != nil {
		t.Fatal(err)
	}

	payload := `{"channel":"email","from":"spammer@bad.test","to":["inbox@example.com"],"subject":"buy now","body_text":"no thanks"}`
	r := httptest.NewRequest("POST", "/inbound?project_id=test-proj", bytes.NewBufferString(payload))
	w := httptest.NewRecorder()
	app.handleInbound(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("inbound status=%d body=%s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	convoID := int64(out["conversation_id"].(float64))
	got, err := dbConversationGet(ctx.AppDB(), "test-proj", convoID)
	if err != nil || got == nil {
		t.Fatalf("get conversation: %v", err)
	}
	if got.Status != "spam" {
		t.Fatalf("conversation status=%q, want spam", got.Status)
	}
}

func TestMessagingInboundReceiveTool_AttachesInbound(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}

	out, err := app.toolMessagingInboundReceive(ctx, map[string]any{
		"message_id": int64(4201),
		"channel":    "whatsapp",
		"from":       "+15551230000",
		"to":         []any{"+15559990000"},
		"body_text":  "hello from messaging",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	contactID := res["contact_id"].(int64)
	if contactID == 0 {
		t.Fatalf("no contact_id in output: %#v", res)
	}

	var kind, body string
	if err := ctx.AppDB().QueryRow(
		`SELECT kind, body FROM contact_activities
		 WHERE project_id = ? AND contact_id = ?
		 ORDER BY id DESC LIMIT 1`,
		"test-proj", contactID,
	).Scan(&kind, &body); err != nil {
		t.Fatal(err)
	}
	if kind != "whatsapp_received" {
		t.Fatalf("activity kind=%q, want whatsapp_received", kind)
	}
	if body != "hello from messaging" {
		t.Fatalf("activity body=%q", body)
	}
}
