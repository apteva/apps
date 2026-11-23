package main

import (
	"encoding/json"
	"errors"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// archivePlatform answers message_thread_archive with a fixed reply or error.
type archivePlatform struct {
	crmRecordingPlatform
	archiveReply map[string]any
	archiveErr   error
}

func (p *archivePlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	if tool != "message_thread_archive" {
		return p.crmRecordingPlatform.CallAppResult(appName, tool, input, out)
	}
	p.calls = append(p.calls, crmCallAppCall{AppName: appName, Tool: tool, Input: input})
	if p.archiveErr != nil {
		return p.archiveErr
	}
	b, _ := json.Marshal(p.archiveReply)
	return json.Unmarshal(b, out)
}

func (p *archivePlatform) archiveCalls() []crmCallAppCall {
	var out []crmCallAppCall
	for _, c := range p.calls {
		if c.Tool == "message_thread_archive" {
			out = append(out, c)
		}
	}
	return out
}

func addMessagingActivity(t *testing.T, ctx *sdk.AppCtx, contactID, convoID, messagingID, install int64) {
	t.Helper()
	if _, err := ctx.AppDB().Exec(`INSERT INTO contact_activities
		(project_id, contact_id, kind, body, occurred_at, source, conversation_id, messaging_id, messaging_install_id)
		VALUES ('test-proj', ?, 'email_received', 'hi', '2026-11-23T10:00:00Z', 'messaging', ?, ?, ?)`,
		contactID, convoID, messagingID, install); err != nil {
		t.Fatal(err)
	}
}

func archiveFixture(t *testing.T, pf *archivePlatform, channel string) (*sdk.AppCtx, int64) {
	t.Helper()
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	c := mustCreate(t, ctx, map[string]any{"primary_email": "person@example.test"})
	convoID := mkConversation(t, ctx, "test-proj", c.ID, channel)
	addMessagingActivity(t, ctx, c.ID, convoID, 501, 42)
	addMessagingActivity(t, ctx, c.ID, convoID, 502, 42)
	addMessagingActivity(t, ctx, c.ID, convoID, 503, 0)  // legacy row, before install ids
	addMessagingActivity(t, ctx, c.ID, convoID, 900, 77) // another Messaging install
	return ctx, convoID
}

func setStatus(t *testing.T, ctx *sdk.AppCtx, convoID int64, status string) map[string]any {
	t.Helper()
	out, err := (&App{}).toolSetConversationStatus(ctx, map[string]any{"conversation_id": convoID, "status": status})
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)
}

func TestCloseEmailConversation_ArchivesMailboxThreads(t *testing.T) {
	pf := &archivePlatform{archiveReply: map[string]any{
		"archived": []map[string]any{{"connection_id": 44, "thread_id": "t-1"}},
		"skipped":  1,
	}}
	ctx, convoID := archiveFixture(t, pf, "email")
	out := setStatus(t, ctx, convoID, "closed")

	calls := pf.archiveCalls()
	if len(calls) != 1 || calls[0].AppName != "messaging" || calls[0].Input["_project_id"] != "test-proj" || calls[0].Input["action"] != "mark_read" {
		t.Fatalf("archive calls=%+v", calls)
	}
	ids, _ := calls[0].Input["message_ids"].([]int64)
	if len(ids) != 3 || ids[0] != 503 || ids[1] != 502 || ids[2] != 501 {
		t.Fatalf("message_ids=%v, want this install's and legacy rows, newest first", calls[0].Input["message_ids"])
	}
	archive, ok := out["mailbox_update"].(*mailboxArchiveResult)
	if !ok || len(archive.Archived) != 1 || archive.Skipped != 1 || archive.Error != "" {
		t.Fatalf("mailbox_update=%#v", out["mailbox_update"])
	}
	if got := out["conversation"].(*Conversation).Status; got != "closed" {
		t.Fatalf("status=%s", got)
	}
}

func TestCloseConversation_OnlyOnTransitionToClosedEmail(t *testing.T) {
	pf := &archivePlatform{archiveReply: map[string]any{"archived": []any{}}}
	ctx, convoID := archiveFixture(t, pf, "email")
	setStatus(t, ctx, convoID, "pending")
	setStatus(t, ctx, convoID, "spam")
	setStatus(t, ctx, convoID, "closed")
	setStatus(t, ctx, convoID, "closed") // already closed: no second archive
	if n := len(pf.archiveCalls()); n != 1 {
		t.Fatalf("archive calls=%d, want 1", n)
	}
	// Priority-only updates never archive.
	if _, err := (&App{}).toolSetConversationStatus(ctx, map[string]any{"conversation_id": convoID, "priority": "high"}); err != nil {
		t.Fatal(err)
	}
	if n := len(pf.archiveCalls()); n != 1 {
		t.Fatalf("archive calls after priority=%d", n)
	}

	smsPF := &archivePlatform{}
	smsCtx, smsConvo := archiveFixture(t, smsPF, "sms")
	setStatus(t, smsCtx, smsConvo, "closed")
	if n := len(smsPF.archiveCalls()); n != 0 {
		t.Fatalf("sms conversation archived: %d calls", n)
	}
}

func TestCloseConversation_ArchiveErrorsNeverBlockClose(t *testing.T) {
	pf := &archivePlatform{archiveErr: errors.New("messaging: gmail token expired")}
	ctx, convoID := archiveFixture(t, pf, "email")
	out := setStatus(t, ctx, convoID, "closed")
	archive, ok := out["mailbox_update"].(*mailboxArchiveResult)
	if !ok || archive.Error == "" {
		t.Fatalf("mailbox_update=%#v", out["mailbox_update"])
	}
	if got := out["conversation"].(*Conversation).Status; got != "closed" {
		t.Fatalf("status=%s", got)
	}
}

func TestCloseConversation_OlderMessagingWithoutToolIsSilent(t *testing.T) {
	pf := &archivePlatform{archiveErr: errors.New(`call messaging: {"code":-32601,"message":"tool not found: message_thread_archive"}`)}
	ctx, convoID := archiveFixture(t, pf, "email")
	out := setStatus(t, ctx, convoID, "closed")
	if _, present := out["mailbox_update"]; present {
		t.Fatalf("mailbox_update=%#v, want omitted", out["mailbox_update"])
	}
}

func TestCloseConversation_NothingArchivableIsOmitted(t *testing.T) {
	pf := &archivePlatform{archiveReply: map[string]any{"archived": []any{}, "skipped": 3}}
	ctx, convoID := archiveFixture(t, pf, "email")
	out := setStatus(t, ctx, convoID, "closed")
	if _, present := out["mailbox_update"]; present {
		t.Fatalf("SES-only conversation should not report mailbox_update: %#v", out["mailbox_update"])
	}
}
