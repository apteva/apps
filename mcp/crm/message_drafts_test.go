package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestMessageDraftFirstOutreachLifecycle(t *testing.T) {
	p := &draftPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(p))
	c := mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": "first@example.test", "is_primary": true}}})
	a := &App{}
	d := mustDraft(t, ctx, map[string]any{"contact_id": c.ID, "channel": "email", "from": "support@example.test", "body": "Incomplete", "client_key": "first"})
	if d.Mode != "message" || d.ConversationID != 0 || d.ReplyToActivityID != 0 || d.Content.To != "first@example.test" {
		t.Fatalf("draft=%+v", d)
	}
	for _, table := range []string{"contact_conversations", "contact_activities"} {
		var count int
		if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("save changed %s: %d %v", table, count, err)
		}
	}
	if len(p.sends) != 0 {
		t.Fatal("save sent")
	}
	retry := mustDraft(t, ctx, map[string]any{"contact_id": c.ID, "channel": "email", "client_key": "first", "body": "do not overwrite"})
	if retry.ID != d.ID || retry.Content.Body != "Incomplete" {
		t.Fatal("create retry lost content")
	}
	list, err := a.toolDraftList(ctx, map[string]any{"contact_id": c.ID})
	if err != nil || list.(map[string]any)["total"] != 1 {
		t.Fatalf("contact listing: %v %v", list, err)
	}
	out := sendDraft(t, ctx, d) // Email subject required only at send.
	if out["sent"] != false || len(p.sends) != 0 {
		t.Fatalf("incomplete send: %v", out)
	}
	d = out["draft"].(*conversationDraft)
	updated, err := a.toolDraftUpdate(ctx, map[string]any{"id": d.ID, "expected_revision": d.Revision, "subject": "Hello", "body": "First outreach"})
	if err != nil {
		t.Fatal(err)
	}
	d = updated.(map[string]any)["draft"].(*conversationDraft)
	out = sendDraft(t, ctx, d)
	sent := out["draft"].(*conversationDraft)
	if out["sent"] != true || sent.ConversationID == 0 || sent.ReplyToActivityID != 0 || len(p.sends) != 1 || p.sends[0]["to"] != "first@example.test" {
		t.Fatalf("send=%v draft=%+v", out, sent)
	}
	out = sendDraft(t, ctx, sent)
	if out["sent"] != true || out["deduped"] != true || len(p.sends) != 1 {
		t.Fatal("sent retry duplicated")
	}
	retry = mustDraft(t, ctx, map[string]any{"contact_id": c.ID, "channel": "email", "client_key": "first"})
	if retry.ID != sent.ID || retry.Status != "sent" || retry.ConversationID != sent.ConversationID {
		t.Fatal("create retry after send did not return original")
	}
}

func TestMessageDraftsSurviveContactMerge(t *testing.T) {
	ctx, _, loser, _ := draftFixture(t, "email")
	d := mustDraft(t, ctx, map[string]any{"contact_id": loser.ID, "channel": "email", "body": "Preserve", "attachments": []any{map[string]any{"filename": "notes.txt", "content_base64": "bm90ZXM="}}})
	winner := mustCreate(t, ctx, map[string]any{"first_name": "Winner"})
	if err := dbMerge(ctx.AppDB(), "test-proj", loser.ID, winner.ID, "", "human"); err != nil {
		t.Fatal(err)
	}
	got, err := getDraft(ctx.AppDB(), "test-proj", d.ID)
	if err != nil || got.ContactID != winner.ID || got.Mode != "message" || got.ConversationID != 0 || got.Content.To != d.Content.To || got.Content.Body != "Preserve" || len(got.Content.Attachments) != 1 {
		t.Fatalf("merge lost draft: %+v %v", got, err)
	}
	if _, err := messageDraftAddress(ctx.AppDB(), "test-proj", got); err != nil {
		t.Fatal(err)
	}
}

func TestMessageDraftOutboundFollowupKeepsThreadAndRecipient(t *testing.T) {
	ctx, p, c, _ := draftFixture(t, "email")
	first := mustDraft(t, ctx, map[string]any{"mode": "message", "contact_id": c.ID, "channel": "email", "to": "private@example.test", "from": "support@example.test", "subject": "Intro", "body": "Hello"})
	result := sendDraft(t, ctx, first)
	if result["sent"] != true {
		t.Fatal(result)
	}
	sent := result["draft"].(*conversationDraft)
	if _, err := (&App{}).toolDraftCreate(ctx, map[string]any{"conversation_id": sent.ConversationID, "mode": "reply"}); err == nil {
		t.Fatal("reply accepted without inbound")
	}
	follow := mustDraft(t, ctx, map[string]any{"mode": "message", "conversation_id": sent.ConversationID, "body": "Following up"})
	if follow.Content.To != "private@example.test" || follow.Content.From != "support@example.test" || follow.ReplyToActivityID != 0 {
		t.Fatalf("follow=%+v", follow)
	}
	result = sendDraft(t, ctx, follow)
	if result["sent"] != true || len(p.sends) != 2 || p.sends[1]["to"] != "private@example.test" || p.sends[1]["conversation_id"] != sent.ConversationID || p.sends[1]["in_reply_to"] != "<draft-provider@example.test>" {
		t.Fatalf("follow send=%v payload=%v", result, p.sends)
	}
}

func TestMessageDraftSafetyAndPinnedIdentity(t *testing.T) {
	for _, scenario := range []string{"suppressed", "removed", "inactive", "wrong sender", "binding", "DNC"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, p, c, _ := draftFixture(t, "email")
			d := mustDraft(t, ctx, map[string]any{"contact_id": c.ID, "channel": "email", "from": "support@example.test", "subject": "Hello", "body": "Cannot send"})
			switch scenario {
			case "suppressed":
				p.suppressed = true
			case "removed":
				_, err := ctx.AppDB().Exec(`DELETE FROM contact_channels WHERE contact_id=? AND value=?`, c.ID, d.Content.To)
				if err != nil {
					t.Fatal(err)
				}
			case "inactive":
				_, err := ctx.AppDB().Exec(`UPDATE contacts SET status='archived' WHERE id=?`, c.ID)
				if err != nil {
					t.Fatal(err)
				}
			case "wrong sender":
				p.wrongSender = true
			case "binding":
				p.install = 43
			case "DNC":
				if err := dbSetAttribute(ctx.AppDB(), "test-proj", c.ID, "do_not_contact", true, "human"); err != nil {
					t.Fatal(err)
				}
			}
			out := sendDraft(t, ctx, d)
			if out["sent"] != false || len(p.sends) != 0 || out["draft"].(*conversationDraft).Status != "draft" {
				t.Fatalf("unsafe send: %v", out)
			}
		})
	}
	ctx, _, c, in := draftFixture(t, "email")
	a := &App{}
	d := mustDraft(t, ctx, map[string]any{"contact_id": c.ID, "channel": "email"})
	for _, args := range []map[string]any{{"mode": "invalid", "contact_id": c.ID, "channel": "email"}, {"mode": "message", "contact_id": c.ID, "channel": "email", "to": "foreign@example.test"}, {"mode": "message", "conversation_id": in["conversation_id"], "reply_to_activity_id": in["activity_id"]}, {"mode": "message", "conversation_id": in["conversation_id"], "contact_id": c.ID + 100}} {
		if _, err := a.toolDraftCreate(ctx, args); err == nil {
			t.Fatalf("unsafe create: %v", args)
		}
	}
	for _, patch := range []map[string]any{{"mode": "reply"}, {"to": "private@example.test"}, {"channel": "sms"}, {"conversation_id": in["conversation_id"]}} {
		patch["id"], patch["expected_revision"] = d.ID, d.Revision
		if _, err := a.toolDraftUpdate(ctx, patch); err == nil {
			t.Fatalf("identity mutation: %v", patch)
		}
	}
	t.Setenv("APTEVA_PROJECT_ID", "")
	if _, err := a.toolDraftCreate(ctx, map[string]any{"_project_id": "foreign", "contact_id": c.ID, "channel": "email"}); err == nil {
		t.Fatal("foreign contact accepted")
	}
	if _, err := a.toolDraftList(ctx, map[string]any{"_project_id": "foreign", "contact_id": c.ID}); err == nil {
		t.Fatal("foreign list accepted")
	}
}

func TestMessageDraftUncertainSendRetry(t *testing.T) {
	ctx, p, c, _ := draftFixture(t, "email")
	d := mustDraft(t, ctx, map[string]any{"contact_id": c.ID, "channel": "email", "from": "support@example.test", "subject": "Hello", "body": "One delivery"})
	p.timeout = true
	out := sendDraft(t, ctx, d)
	d = out["draft"].(*conversationDraft)
	if out["sent"] != false || d.Status != "send_failed" || d.AttemptKey == "" || d.ConversationID != 0 {
		t.Fatalf("uncertain=%v", out)
	}
	if _, err := (&App{}).toolDraftDiscard(ctx, map[string]any{"id": d.ID, "expected_revision": d.Revision}); err == nil {
		t.Fatal("uncertain draft discarded")
	}
	out = sendDraft(t, ctx, d)
	if out["sent"] != true || len(p.accepted) != 1 || len(p.sends) != 2 || p.sends[0]["idempotency_key"] != p.sends[1]["idempotency_key"] || out["draft"].(*conversationDraft).ConversationID == 0 {
		t.Fatalf("retry=%v sends=%v", out, p.sends)
	}
}

func TestMessageDraftRecoveryAdoptsOnlyMatchingDelivery(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(map[bool]string{false: "matching", true: "wrong recipient"}[wrong], func(t *testing.T) {
			ctx, p, c, _ := draftFixture(t, "email")
			d := mustDraft(t, ctx, map[string]any{"contact_id": c.ID, "channel": "email", "from": "support@example.test", "subject": "Hello", "body": "Accepted"})
			convo := mkConversation(t, ctx, "test-proj", c.ID, "email")
			to := d.Content.To
			if wrong {
				to = "foreign@example.test"
			}
			_, err := logMessageActivity(ctx.AppDB(), logMessageActivityInput{ProjectID: "test-proj", ContactID: c.ID, ConversationID: convo, Kind: "email_sent", Body: d.Content.Body, IdempotencyKey: "crash", MessagingID: 999, MessagingInstallID: 42, SourceDetail: map[string]any{"to": to}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ctx.AppDB().Exec(`UPDATE conversation_drafts SET status='sending',attempt_key='crash',dispatched=1,lease_until='2000-01-01T00:00:00Z' WHERE id=?`, d.ID); err != nil {
				t.Fatal(err)
			}
			p.suppressed, p.wrongSender = true, true
			out := sendDraft(t, ctx, d)
			if out["sent"] != !wrong || len(p.sends) != 0 {
				t.Fatalf("recovery=%v", out)
			}
			if !wrong && out["draft"].(*conversationDraft).ConversationID != convo {
				t.Fatal("conversation not adopted")
			}
		})
	}
}

func TestMessageDraftPhoneFirstOutreach(t *testing.T) {
	for _, channel := range []string{"sms", "whatsapp"} {
		t.Run(channel, func(t *testing.T) {
			ctx, p, c, _ := draftFixture(t, channel)
			from := "+15550002222"
			if channel == "whatsapp" {
				from = "+15550001111"
				p.closedWindow = true
			}
			d := mustDraft(t, ctx, map[string]any{"contact_id": c.ID, "channel": channel, "from": from, "body": "Saved freeform"})
			out := sendDraft(t, ctx, d)
			if channel == "whatsapp" {
				if out["sent"] != false || len(p.sends) != 0 {
					t.Fatal("closed WA window sent")
				}
				d = out["draft"].(*conversationDraft)
				update, err := (&App{}).toolDraftUpdate(ctx, map[string]any{"id": d.ID, "expected_revision": d.Revision, "content_sid": "HXapproved"})
				if err != nil {
					t.Fatal(err)
				}
				d = update.(map[string]any)["draft"].(*conversationDraft)
				out = sendDraft(t, ctx, d)
			}
			if out["sent"] != true || len(p.sends) != 1 || out["draft"].(*conversationDraft).ConversationID == 0 || out["draft"].(*conversationDraft).Content.Body != "Saved freeform" {
				t.Fatalf("phone send=%v", out)
			}
			if channel == "whatsapp" && p.sends[0]["body"] != "" {
				t.Fatal("freeform sent with template")
			}
		})
	}
}

func TestMessageDraftMigrationPreservesEveryLegacyColumn(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE contacts(id INTEGER PRIMARY KEY,project_id TEXT); CREATE TABLE contact_conversations(id INTEGER PRIMARY KEY,project_id TEXT,contact_id INTEGER); CREATE TABLE contact_activities(id INTEGER PRIMARY KEY,project_id TEXT,contact_id INTEGER,conversation_id INTEGER,kind TEXT); INSERT INTO contacts VALUES(1,'p'); INSERT INTO contact_conversations VALUES(2,'p',1); INSERT INTO contact_activities VALUES(3,'p',1,2,'email_received');`)
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile("migrations/022_conversation_drafts.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(old)); err != nil {
		t.Fatal(err)
	}
	for i, status := range []string{"draft", "sending", "send_failed", "sent", "discarded"} {
		_, err = db.Exec(`INSERT INTO conversation_drafts(id,project_id,contact_id,conversation_id,reply_to_activity_id,content,status,revision,client_key,created_by,updated_by,created_at,updated_at,messaging_install_id,attempt_key,lease_until,dispatched,last_error,sent_result) VALUES(?,'p',1,2,3,?,?,17,?,'agent:Writer','human','created','updated',42,'durable-retry-key','lease',1,'error','{"sent":true}')`, i+1, `{"channel":"email","body":"retain exact JSON","attachments":[{"filename":"notes.txt","content_base64":"bm90ZXM="}]}`, status, status)
		if err != nil {
			t.Fatal(err)
		}
	}
	columns := `id,project_id,contact_id,conversation_id,reply_to_activity_id,content,status,revision,client_key,created_by,updated_by,created_at,updated_at,messaging_install_id,attempt_key,lease_until,dispatched,last_error,sent_result`
	snapshot := func() [][]any {
		rows, err := db.Query(`SELECT ` + columns + ` FROM conversation_drafts ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var result [][]any
		for rows.Next() {
			values := make([]any, 19)
			ptr := make([]any, 19)
			for i := range values {
				ptr[i] = &values[i]
			}
			if err := rows.Scan(ptr...); err != nil {
				t.Fatal(err)
			}
			result = append(result, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := snapshot()
	next, err := os.ReadFile("migrations/023_message_drafts.sql")
	if err != nil {
		t.Fatal(err)
	}
	failed, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = failed.Exec(string(next) + `; SELECT * FROM deliberately_missing_table;`); err == nil {
		t.Fatal("injected migration failure succeeded")
	}
	if err = failed.Rollback(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("failed upgrade partially changed legacy drafts")
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(string(next)); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		b, _ := json.Marshal(before)
		a, _ := json.Marshal(after)
		t.Fatalf("migration lost data: %s -> %s", b, a)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM conversation_drafts WHERE mode='reply'`).Scan(&count); err != nil || count != 5 {
		t.Fatalf("legacy modes=%d %v", count, err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Fatal("foreign keys broken")
	}
	rows.Close()
	if _, err = db.Exec(`INSERT INTO conversation_drafts(project_id,contact_id,mode,content,client_key,created_by,updated_by,created_at,updated_at) VALUES('p',1,'message','{}','new','human','human','now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO conversation_drafts(project_id,contact_id,mode,content,client_key,created_by,updated_by,created_at,updated_at) VALUES('foreign',1,'message','{}','bad','human','human','now','now')`); err == nil || !strings.Contains(err.Error(), "project") {
		t.Fatalf("owner trigger: %v", err)
	}
}
