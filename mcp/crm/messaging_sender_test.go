package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func crmCallsTo(p *crmRecordingPlatform, tool string) []crmCallAppCall {
	out := []crmCallAppCall{}
	for _, c := range p.calls {
		if c.AppName == "messaging" && c.Tool == tool {
			out = append(out, c)
		}
	}
	return out
}

func TestSendMessageMissingSenderStructuredError(t *testing.T) {
	platform := &crmRecordingPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	c := mustCreate(t, ctx, map[string]any{
		"display_name": "Alice",
		"channels": []any{
			map[string]any{"kind": "email", "value": "alice@example.com", "is_primary": true},
		},
	})

	_, err := app.sendMessageImpl(ctx, map[string]any{
		"id":      c.ID,
		"subject": "Hello",
		"body":    "Hello",
	}, false)
	if err == nil {
		t.Fatal("expected missing sender error")
	}

	var got actionableToolError
	if unmarshalErr := json.Unmarshal([]byte(err.Error()), &got); unmarshalErr != nil {
		t.Fatalf("expected JSON error, got %q: %v", err.Error(), unmarshalErr)
	}
	if got.Code != "missing_sender" {
		t.Fatalf("error_code=%q, want missing_sender", got.Code)
	}
	if got.RequiredField != "from" || got.DefaultConfigKey != "default_sender_email" {
		t.Fatalf("unexpected sender guidance: %#v", got)
	}
	if !strings.Contains(got.Message, "from required") {
		t.Fatalf("message=%q, want old human-readable wording", got.Message)
	}
	if sends := crmCallsTo(platform, "send_message"); len(sends) != 0 {
		t.Fatalf("send_message calls=%d, want 0", len(sends))
	}
}

func TestReplySchemaIncludesSenderControls(t *testing.T) {
	var schema map[string]any
	var description string
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "contacts_reply" {
			schema = tool.InputSchema
			description = tool.Description
			break
		}
	}
	if schema == nil {
		t.Fatal("contacts_reply tool not found")
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties missing: %#v", schema)
	}
	for _, field := range []string{"from", "list_id", "body_html", "idempotency_key"} {
		if _, ok := props[field]; !ok {
			t.Fatalf("contacts_reply schema missing %q", field)
		}
	}
	if !strings.Contains(description, "Sender precedence") {
		t.Fatalf("description does not explain sender precedence: %q", description)
	}
}

func TestReplyForwardsSenderControls(t *testing.T) {
	platform := &crmRecordingPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	c := mustCreate(t, ctx, map[string]any{
		"display_name": "Alice",
		"channels": []any{
			map[string]any{"kind": "email", "value": "alice@example.com", "is_primary": true},
		},
	})
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	convoID, err := dbConversationCreate(tx, "test-proj", c.ID, channelEmail, "Welcome", "root-msg", time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	_, err = app.toolReply(ctx, map[string]any{
		"id":              c.ID,
		"conversation_id": convoID,
		"body":            "Thanks",
		"from":            "contact@apteva.ai",
		"body_html":       "<p>Thanks</p>",
		"idempotency_key": "reply-1",
	})
	if err != nil {
		t.Fatalf("reply: %v", err)
	}

	sends := crmCallsTo(platform, "send_message")
	if len(sends) != 1 {
		t.Fatalf("send_message calls=%d, want 1", len(sends))
	}
	if got := sends[0].Input["from"]; got != "contact@apteva.ai" {
		t.Fatalf("from=%#v, want contact@apteva.ai", got)
	}
	if got := sends[0].Input["subject"]; got != "Re: Welcome" {
		t.Fatalf("subject=%#v, want Re: Welcome", got)
	}
	if got := sends[0].Input["body_html"]; got != "<p>Thanks</p>" {
		t.Fatalf("body_html=%#v, want forwarded HTML body", got)
	}
	if got := sends[0].Input["idempotency_key"]; got != "reply-1" {
		t.Fatalf("idempotency_key=%#v, want reply-1", got)
	}
}
