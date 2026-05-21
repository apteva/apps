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
)

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
