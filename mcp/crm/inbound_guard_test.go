package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type inboundGuardPlatform struct {
	tk.BasePlatformClient
	failTool string
}

func (p *inboundGuardPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{
		AppName: "crm", InstallID: 99, ProjectID: "test-proj",
		Bindings: map[string]any{"messaging": float64(42)},
	}, nil
}

func (p *inboundGuardPlatform) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	return &sdk.PlatformInstance{ID: id, Name: "messaging", Status: "running", ProjectID: "test-proj"}, nil
}

func (p *inboundGuardPlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	if tool == p.failTool {
		return errors.New("ownership lookup unavailable")
	}
	if appName != "messaging" || input["_project_id"] != "test-proj" {
		return errors.New("ownership lookup must be project scoped")
	}
	var payload any = map[string]any{"ok": true}
	switch tool {
	case "senders_list":
		payload = map[string]any{"senders": []map[string]any{{"address": "contact@owned.test"}}}
	case "identities_list":
		payload = map[string]any{"identities": []map[string]any{{"kind": "email_domain", "address": "owned.test"}}}
	}
	b, _ := json.Marshal(payload)
	return json.Unmarshal(b, out)
}

func TestInboundEmailMissingRecipientDoesNotCreateCRMData(t *testing.T) {
	ctx := newTestCtx(t)
	out, err := ingestInbound(ctx, "test-proj", inboundPayload{
		MessageID: 7001, Channel: channelEmail, From: "person@example.test",
		To: []string{"contact@owned.test"}, BodyText: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["ignored"] != true {
		t.Fatalf("result=%v, want ignored", out)
	}
	var contacts, conversations, activities int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contacts`).Scan(&contacts)
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_conversations`).Scan(&conversations)
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_activities`).Scan(&activities)
	if contacts != 0 || conversations != 0 || activities != 0 {
		t.Fatalf("created CRM data contacts=%d conversations=%d activities=%d", contacts, conversations, activities)
	}
	assertInboundGuardNoWrites(t, ctx)
}

func TestInboundEmailSuppressedOrUnmatchedDoesNotCreateCRMData(t *testing.T) {
	for _, status := range []string{"suppressed", "no_match", "no-match"} {
		t.Run(status, func(t *testing.T) {
			ctx := newTestCtx(t)
			out, err := ingestInbound(ctx, "test-proj", inboundPayload{
				MessageID: 7100, Channel: channelEmail, From: "person@example.test",
				To: []string{"contact@owned.test"}, RouteStatus: status,
				MatchedRecipient: "contact@owned.test",
				BodyText:         "should not enter CRM",
			})
			if err != nil {
				t.Fatal(err)
			}
			if out["ignored"] != true {
				t.Fatalf("result=%v, want ignored", out)
			}
			var contacts, activities int
			_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contacts`).Scan(&contacts)
			_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_activities`).Scan(&activities)
			if contacts != 0 || activities != 0 {
				t.Fatalf("created CRM data contacts=%d activities=%d", contacts, activities)
			}
		})
	}
}

func TestInboundEmailRejectsForeignReceivingIdentity(t *testing.T) {
	ctx := newTestCtx(t, tk.WithPlatform(&inboundGuardPlatform{}))
	out, err := ingestInbound(ctx, "test-proj", inboundPayload{
		MessageID: 7002, Channel: channelEmail, From: "person@example.test",
		To: []string{"contact@foreign.test"}, MatchedRecipient: "contact@foreign.test",
		BodyText: "wrong project",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["ignored"] != true {
		t.Fatalf("result=%v, want ignored", out)
	}
	var contacts int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contacts`).Scan(&contacts)
	if contacts != 0 {
		t.Fatalf("created %d contacts for foreign receiving identity", contacts)
	}
	assertInboundGuardNoWrites(t, ctx)
}

func TestInboundEmailUsesCanonicalReceivingIdentityForParticipant(t *testing.T) {
	ctx := newTestCtx(t, tk.WithPlatform(&inboundGuardPlatform{}))
	out, err := ingestInbound(ctx, "test-proj", inboundPayload{
		MessageID: 7003, Channel: channelEmail, From: "person@example.test",
		To: []string{"person@example.test"}, MatchedRecipient: "contact@owned.test",
		BodyText: "valid delivery",
	})
	if err != nil {
		t.Fatal(err)
	}
	conversationID := out["conversation_id"].(int64)
	var recipient string
	if err := ctx.AppDB().QueryRow(`SELECT address FROM conversation_participants WHERE conversation_id=? AND role='to'`, conversationID).Scan(&recipient); err != nil {
		t.Fatal(err)
	}
	if recipient != "contact@owned.test" {
		t.Fatalf("recipient=%q, want contact@owned.test", recipient)
	}
	var detail string
	if err := ctx.AppDB().QueryRow(`SELECT source_detail FROM contact_activities WHERE messaging_id=7003`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(detail), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["receiving_identity"] != "contact@owned.test" {
		t.Fatalf("source_detail=%v", decoded)
	}
	if decoded["to"].([]any)[0] != "contact@owned.test" || decoded["header_to"].([]any)[0] != "person@example.test" {
		t.Fatalf("canonical display recipient or original header lost: %v", decoded)
	}
}

func TestInboundEmailOwnershipErrorsDoNotWriteCRMData(t *testing.T) {
	for _, tool := range []string{"senders_list", "identities_list"} {
		t.Run(tool, func(t *testing.T) {
			ctx := newTestCtx(t, tk.WithPlatform(&inboundGuardPlatform{failTool: tool}))
			_, err := ingestInbound(ctx, "test-proj", inboundPayload{
				Channel: channelEmail, From: "person@example.test", MatchedRecipient: "alias@owned.test",
			})
			if err == nil {
				t.Fatal("expected retryable ownership error")
			}
			assertInboundGuardNoWrites(t, ctx)
		})
	}
}

func TestInboundEmailRejectsInvalidOrConflictingDeliveryRecipients(t *testing.T) {
	for _, test := range []struct {
		name, matched string
		envelope      []string
	}{
		{"malformed", "not-an-email", nil},
		{"domain only", "owned.test", nil},
		{"ambiguous envelope", "", []string{"contact@owned.test", "other@owned.test"}},
		{"conflicting envelope", "contact@owned.test", []string{"contact@foreign.test"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := newTestCtx(t, tk.WithPlatform(&inboundGuardPlatform{}))
			out, err := ingestInbound(ctx, "test-proj", inboundPayload{
				Channel: channelEmail, From: "person@example.test", To: []string{"contact@owned.test"},
				MatchedRecipient: test.matched, EnvelopeRecipients: test.envelope,
			})
			if err != nil || out["ignored"] != true {
				t.Fatalf("result=%v err=%v, want ignored", out, err)
			}
			assertInboundGuardNoWrites(t, ctx)
		})
	}
}

func TestInboundEmailDomainAliasesRemainSupported(t *testing.T) {
	ctx := newTestCtx(t, tk.WithPlatform(&inboundGuardPlatform{}))
	out, err := ingestInbound(ctx, "test-proj", inboundPayload{
		Channel: " EMAIL ", From: "person@example.test", MatchedRecipient: "mailto:Sales+Reply@OWNED.test",
		BodyText: "valid domain-owned alias",
	})
	if err != nil || out["activity_id"] == nil {
		t.Fatalf("result=%v err=%v, want stored domain alias", out, err)
	}
}

func assertInboundGuardNoWrites(t *testing.T, ctx *sdk.AppCtx) {
	t.Helper()
	for _, table := range []string{"contacts", "contact_conversations", "contact_activities", "conversation_participants", "contact_list_members", "contact_tags", "contact_activity_attachments", "crm_event_outbox"} {
		var count int
		if err := ctx.AppDB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("guard wrote %d rows to %s", count, table)
		}
	}
}

func TestInboundGuardHTTPAndMCPContracts(t *testing.T) {
	for _, transport := range []string{"http", "mcp"} {
		t.Run(transport, func(t *testing.T) {
			ctx := newTestCtx(t, tk.WithPlatform(&inboundGuardPlatform{}))
			app := &App{}
			args := map[string]any{"channel": "email", "from": "person@example.test", "to": []string{"person@example.test"}, "envelope_recipients": []string{"contact@owned.test"}, "route_status": "suppressed"}
			var out map[string]any
			if transport == "http" {
				prior := globalCtx
				globalCtx = ctx
				defer func() { globalCtx = prior }()
				raw, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				w := httptest.NewRecorder()
				app.handleInbound(w, httptest.NewRequest("POST", "/inbound?project_id=test-proj", bytes.NewReader(raw)))
				if w.Code != 200 {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
			} else {
				result, err := app.toolMessagingInboundReceive(ctx, args)
				if err != nil {
					t.Fatal(err)
				}
				out = result.(map[string]any)
			}
			if out["ignored"] != true || out["reason"] != "inbound route status: suppressed" {
				t.Fatalf("contract dropped route status: %v", out)
			}
			assertInboundGuardNoWrites(t, ctx)
			delete(args, "route_status")
			result, err := app.toolMessagingInboundReceive(ctx, args)
			if err != nil || result.(map[string]any)["activity_id"] == nil {
				t.Fatalf("contract dropped envelope recipients: %v err=%v", result, err)
			}
		})
	}
}

func TestInboundEmailUsesSingleEnvelopeRecipientWhenMatchIsAbsent(t *testing.T) {
	ctx := newTestCtx(t, tk.WithPlatform(&inboundGuardPlatform{}))
	out, err := ingestInbound(ctx, "test-proj", inboundPayload{
		MessageID: 7004, Channel: channelEmail, From: "person@example.test",
		To: []string{"person@example.test"}, EnvelopeRecipients: []string{"contact@owned.test"},
		BodyText: "canonical envelope recipient",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["ignored"] == true {
		t.Fatalf("result=%v, want materialized inbound", out)
	}
	conversationID := out["conversation_id"].(int64)
	var recipient string
	if err := ctx.AppDB().QueryRow(`SELECT address FROM conversation_participants WHERE conversation_id=? AND role='to'`, conversationID).Scan(&recipient); err != nil {
		t.Fatal(err)
	}
	if recipient != "contact@owned.test" {
		t.Fatalf("recipient=%q, want contact@owned.test", recipient)
	}
}
