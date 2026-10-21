package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// Everything stays in test SQLite and a fake Messaging provider. Never sends
// real mail or depends on the production examples cited in the bug report.
func emailSubjectFixture(t *testing.T, subject string) (*sdk.AppCtx, *idempotentMessagingPlatform, *Contact, int64) {
	t.Helper()
	p := newIdempotentMessagingPlatform()
	ctx := newTestCtx(t, tk.WithPlatform(p), tk.WithConfig(map[string]string{"email_verification_mode": "off"}))
	c := mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": "alice@example.test", "is_primary": true}}})
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	convo, err := dbConversationCreate(tx, "test-proj", c.ID, channelEmail, subject, "<original@example.test>", time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_, err = logMessageActivity(ctx.AppDB(), logMessageActivityInput{ProjectID: "test-proj", ContactID: c.ID, ConversationID: convo, Kind: "email_received", Body: "Original email", OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), SourceDetail: map[string]any{"from": "alice@example.test", "receiving_identity": "sender@example.test", "to": []string{"sender@example.test"}}, MessageIDHeader: "<original@example.test>"})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, p, c, convo
}

func emailSubjectSends(p *idempotentMessagingPlatform) []crmCallAppCall {
	_, calls := p.snapshot()
	var sends []crmCallAppCall
	for _, call := range calls {
		if call.Tool == "send_message" {
			sends = append(sends, call)
		}
	}
	return sends
}

func TestEmailConversationSubjectsBothTools(t *testing.T) {
	app := &App{}
	for _, tool := range []struct {
		name string
		call sdk.ToolHandler
	}{{"contacts_send_message", app.toolSendMessage}, {"contacts_reply", app.toolReply}} {
		for _, row := range []struct {
			name, original string
			explicit       any
			supplied       bool
			want           string
		}{
			{"omitted", "Partnership proposal", nil, false, "Re: Partnership proposal"},
			{"empty", "Partnership proposal", "", true, "Re: Partnership proposal"},
			{"whitespace", "Partnership proposal", " \t\n", true, "Re: Partnership proposal"},
			{"existing prefix", "Re: Partnership proposal", nil, false, "Re: Partnership proposal"},
			{"case-insensitive prefix", "RE: Partnership proposal", nil, false, "RE: Partnership proposal"},
			{"lowercase prefix", "re: Partnership proposal", nil, false, "re: Partnership proposal"},
			{"trim inherited", "  Partnership proposal  ", nil, false, "Re: Partnership proposal"},
			{"unicode", "Offre – intégration MAYU", nil, false, "Re: Offre – intégration MAYU"},
			{"explicit", "Partnership proposal", "A different subject", true, "A different subject"},
			{"explicit unchanged", "Partnership proposal", "  Keep exactly this  ", true, "  Keep exactly this  "},
			{"explicit without original", "", "New subject", true, "New subject"},
		} {
			t.Run(tool.name+"/"+row.name, func(t *testing.T) {
				ctx, p, c, convo := emailSubjectFixture(t, row.original)
				args := map[string]any{"id": c.ID, "conversation_id": convo, "body": "Follow up", "from": "sender@example.test"}
				if row.supplied {
					args["subject"] = row.explicit
				}
				before := make(map[string]any, len(args))
				for key, value := range args {
					before[key] = value
				}
				out, err := tool.call(ctx, args)
				if err != nil {
					t.Fatal(err)
				}
				sends := emailSubjectSends(p)
				if len(sends) != 1 {
					t.Fatalf("sends=%d", len(sends))
				}
				sent := sends[0].Input
				if sent["subject"] != row.want {
					t.Fatalf("subject=%#v, want %q", sent["subject"], row.want)
				}
				if sent["in_reply_to"] != "<original@example.test>" || sent["conversation_id"] != convo {
					t.Fatalf("threading lost: %v", sent)
				}
				headers := sent["headers"].(map[string]any)
				if headers["In-Reply-To"] != "<original@example.test>" || headers["References"] != "<original@example.test>" {
					t.Fatal("RFC reply headers lost")
				}
				fingerprint := outboundContentFingerprint("test-proj", c.ID, false, sent)
				if !strings.HasPrefix(strArg(sent, "idempotency_key"), "crm-auto-v1:"+fingerprint+":") {
					t.Fatal("idempotency fingerprint did not include resolved subject")
				}
				act := out.(map[string]any)["activity"].(*Activity)
				if act.ConversationID != convo || !strings.HasPrefix(act.Body, row.want+"\n\n") {
					t.Fatalf("incorrect saved sent content: %+v", act)
				}
				if !reflect.DeepEqual(before, args) {
					t.Fatal("caller args mutated")
				}
			})
		}
	}
}

func TestEmailConversationUnresolvedSubjectsRejectedBeforeDispatch(t *testing.T) {
	app := &App{}
	for _, tool := range []struct {
		name string
		call sdk.ToolHandler
	}{{"send", app.toolSendMessage}, {"reply", app.toolReply}} {
		for _, original := range []string{"", " \t\n"} {
			for _, template := range []int{0, 42} {
				t.Run(tool.name+"/"+anyString(template)+"/"+anyString(len(original)), func(t *testing.T) {
					ctx, p, c, convo := emailSubjectFixture(t, original)
					args := map[string]any{"id": c.ID, "conversation_id": convo, "body": "Do not send", "from": "sender@example.test", "subject": " \t"}
					if template > 0 {
						args["template_id"] = template
					}
					if _, err := tool.call(ctx, args); err == nil || !strings.Contains(err.Error(), "subject required") {
						t.Fatalf("unresolved subject: %v", err)
					}
					if len(emailSubjectSends(p)) != 0 {
						t.Fatal("blank subject was dispatched")
					}
					var count int
					if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_activities WHERE kind='email_sent'`).Scan(&count); err != nil || count != 0 {
						t.Fatalf("unexpected send activities: %d %v", count, err)
					}
				})
			}
		}
	}
}

func TestEmailResolvedSubjectsDeduplicateAcrossBothTools(t *testing.T) {
	ctx, p, c, convo := emailSubjectFixture(t, "Partnership proposal")
	app := &App{}
	base := map[string]any{"id": c.ID, "conversation_id": convo, "body": "Same follow up", "from": "sender@example.test"}
	if _, err := app.toolSendMessage(ctx, base); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []sdk.ToolHandler{app.toolSendMessage, app.toolReply} {
		for _, subject := range []string{"", " \t", "Re: Partnership proposal"} {
			args := map[string]any{}
			for key, value := range base {
				args[key] = value
			}
			args["subject"] = subject
			out, err := tool(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			if out.(map[string]any)["deduped"] != true {
				t.Fatalf("same effective subject duplicated: %q", subject)
			}
		}
	}
	if created, _ := p.snapshot(); created != 1 || len(emailSubjectSends(p)) != 1 {
		t.Fatalf("identical messages created=%d", created)
	}
	base["subject"] = "Intentionally different"
	if _, err := app.toolSendMessage(ctx, base); err != nil {
		t.Fatal(err)
	}
	if created, _ := p.snapshot(); created != 2 {
		t.Fatal("different explicit subject was deduplicated")
	}
	// The implicit latest-inbound conversation path must use the same fallback.
	delete(base, "subject")
	delete(base, "conversation_id")
	base["body"] = "Another follow up"
	if _, err := app.toolReply(ctx, base); err != nil {
		t.Fatal(err)
	}
	if sends := emailSubjectSends(p); sends[len(sends)-1].Input["subject"] != "Re: Partnership proposal" {
		t.Fatal("implicit conversation lost subject")
	}
}

func TestEmailTemplateSubjectCompatibility(t *testing.T) {
	app := &App{}
	ctx, p, c, convo := emailSubjectFixture(t, "Partnership proposal")
	if _, err := app.toolSendMessage(ctx, map[string]any{"id": c.ID, "conversation_id": convo, "from": "sender@example.test", "template_id": 42}); err != nil {
		t.Fatal(err)
	}
	if sent := emailSubjectSends(p)[0].Input; sent["subject"] != "Re: Partnership proposal" || int64Arg(sent, "template_id") != 42 {
		t.Fatalf("threaded template subject missing: %v", sent)
	}
	// A standalone template still owns its subject; CRM must not invent one.
	if _, err := app.toolSendMessage(ctx, map[string]any{"id": c.ID, "from": "sender@example.test", "template_id": 43}); err != nil {
		t.Fatal(err)
	}
	if sent := emailSubjectSends(p)[1].Input; sent["subject"] != nil || int64Arg(sent, "template_id") != 43 {
		t.Fatalf("standalone template contract changed: %v", sent)
	}
}

func TestEmailSubjectHTTPReplyPaths(t *testing.T) {
	for _, route := range []string{"messages", "reply"} {
		t.Run(route, func(t *testing.T) {
			ctx, p, c, convo := emailSubjectFixture(t, "Partnership proposal")
			old := globalCtx
			globalCtx = ctx
			t.Cleanup(func() { globalCtx = old })
			body, err := json.Marshal(map[string]any{"conversation_id": convo, "from": "sender@example.test", "body": "HTTP follow up"})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/contacts/"+anyString(c.ID)+"/"+route+"?project_id=test-proj", bytes.NewReader(body))
			w := httptest.NewRecorder()
			(&App{}).handleHTTPContactItem(w, request)
			if w.Code != http.StatusOK {
				t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
			}
			if sends := emailSubjectSends(p); len(sends) != 1 || sends[0].Input["subject"] != "Re: Partnership proposal" {
				t.Fatalf("HTTP subject fallback: %v", sends)
			}
		})
	}
}

func TestEmailSubjectForeignConversationsCannotSupplySubject(t *testing.T) {
	app := &App{}
	for _, tool := range []sdk.ToolHandler{app.toolSendMessage, app.toolReply} {
		ctx, p, c, convo := emailSubjectFixture(t, "Private subject")
		other := mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": "other@example.test"}}})
		if _, err := tool(ctx, map[string]any{"id": other.ID, "conversation_id": convo, "body": "No send", "from": "sender@example.test"}); err == nil {
			t.Fatal("foreign contact conversation accepted")
		}
		foreign, err := dbCreate(ctx.AppDB(), "foreign", map[string]any{"first_name": "Foreign"})
		if err != nil {
			t.Fatal(err)
		}
		foreignConvo := mkConversation(t, ctx, "foreign", foreign.ID, channelEmail)
		if _, err := tool(ctx, map[string]any{"id": c.ID, "conversation_id": foreignConvo, "body": "No send", "from": "sender@example.test"}); err == nil {
			t.Fatal("foreign project conversation accepted")
		}
		if len(emailSubjectSends(p)) != 0 {
			t.Fatal("foreign conversation was dispatched")
		}
	}
}

func TestEmailSubjectToolContractsExplainInheritance(t *testing.T) {
	found := 0
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name != "contacts_send_message" && tool.Name != "contacts_reply" {
			continue
		}
		found++
		subject := tool.InputSchema["properties"].(map[string]any)["subject"].(map[string]any)
		for _, guidance := range []string{"inherit", "nonblank", "conversation"} {
			if !strings.Contains(strings.ToLower(subject["description"].(string)), guidance) {
				t.Errorf("%s subject schema missing %q", tool.Name, guidance)
			}
		}
	}
	if found != 2 {
		t.Fatal("email tools not found")
	}
}
