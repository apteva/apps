package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// This provider double models an accepted send followed by a lost response.
// All deliveries are fake, and repeat operation keys return the same message.
type draftPlatform struct {
	crmRecordingPlatform
	mu                                                     sync.Mutex
	sends                                                  []map[string]any
	accepted                                               map[string]int
	timeout, reject, suppressed, wrongSender, closedWindow bool
	install                                                int64
	started, release                                       chan struct{}
}

func (p *draftPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	id := p.install
	if id == 0 {
		id = 42
	}
	return &sdk.InstallIdentity{AppName: "crm", InstallID: 99, ProjectID: "test-proj", Bindings: map[string]any{"messaging": float64(id)}}, nil
}
func (p *draftPlatform) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	return &sdk.PlatformInstance{ID: id, Name: "messaging", Status: "running", ProjectID: "test-proj"}, nil
}
func (p *draftPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	payload := map[string]any{"ok": true}
	switch tool {
	case "senders_list":
		senders := []map[string]any{}
		if !p.wrongSender {
			senders = []map[string]any{{"channel": "email", "address": "support@example.test"}, {"channel": "sms", "address": "+15550002222"}, {"channel": "whatsapp", "address": "+15550001111"}}
		}
		payload = map[string]any{"senders": senders}
	case "identities_list":
		payload = map[string]any{"identities": []map[string]any{{"kind": "email_domain", "address": "example.test"}}}
	case "suppression_check":
		payload = map[string]any{"suppressed": p.suppressed, "reason": "unsubscribed"}
	case "message_list":
		messages := []map[string]any{}
		if !p.closedWindow {
			messages = append(messages, map[string]any{"from": args["address"], "matched_recipient": "+15550001111", "received_at": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)})
		}
		payload = map[string]any{"messages": messages}
	case "send_message":
		p.sends = append(p.sends, args)
		if p.started != nil {
			close(p.started)
			p.started = nil
			<-p.release
		}
		if p.reject {
			payload = map[string]any{"id": 100, "status": "rejected", "status_reason": "rejected by provider"}
			break
		}
		if p.accepted == nil {
			p.accepted = map[string]int{}
		}
		key := strArg(args, "idempotency_key")
		id, seen := p.accepted[key]
		if !seen {
			id = 200 + len(p.accepted)
			p.accepted[key] = id
		}
		if p.timeout {
			p.timeout = false
			return errors.New("provider response timed out after accepting delivery")
		}
		payload = map[string]any{"id": id, "status": "sent", "provider_message_id": "draft-provider", "message_id_header": "<draft-provider@example.test>", "deduped": seen}
	}
	raw, _ := json.Marshal(payload)
	return json.Unmarshal(raw, out)
}

func draftFixture(t *testing.T, channel string) (*sdk.AppCtx, *draftPlatform, *Contact, map[string]any) {
	t.Helper()
	p := &draftPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(p))
	from, local, kind := "private@example.test", "support@example.test", "email"
	primary := "primary@example.test"
	if phoneTransport(channel) {
		from, local, kind, primary = "+15551234567", "+15550001111", "phone", "+15551110000"
	}
	c := mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": kind, "value": primary, "is_primary": true}, map[string]any{"kind": kind, "value": from}}})
	in, err := ingestInbound(ctx, "test-proj", inboundPayload{Channel: channel, From: from, MatchedRecipient: local, MessageID: 700, Subject: "Question", BodyText: "Please reply here", MessageIDHeader: "<original@example.test>"})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, p, c, in
}
func mustDraft(t *testing.T, ctx *sdk.AppCtx, args map[string]any) *conversationDraft {
	t.Helper()
	out, err := (&App{}).toolDraftCreate(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)["draft"].(*conversationDraft)
}
func sendDraft(t *testing.T, ctx *sdk.AppCtx, d *conversationDraft) map[string]any {
	t.Helper()
	out, err := (&App{}).toolDraftSend(ctx, map[string]any{"id": d.ID, "expected_revision": d.Revision})
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)
}

func TestDraftSaveNeverSendsOrChangesConversation(t *testing.T) {
	ctx, p, c, in := draftFixture(t, "email")
	a := &App{}
	before, _ := dbConversationGet(ctx.AppDB(), "test-proj", in["conversation_id"].(int64))
	var count int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_activities`).Scan(&count)
	d := mustDraft(t, ctx, map[string]any{"conversation_id": in["conversation_id"], "client_key": "same-create", "body": "Proposed reply", "body_html": "<b>Proposed reply</b>", "attachments": []any{map[string]any{"filename": "notes.txt", "content_base64": "bm90ZXM="}}, "source": "agent:Writer"})
	if d.ContactID != c.ID || d.Content.To != "private@example.test" || d.Content.From != "support@example.test" || d.CreatedBy != "agent:Writer" {
		t.Fatalf("draft=%+v", d)
	}
	again := mustDraft(t, ctx, map[string]any{"conversation_id": in["conversation_id"], "client_key": "same-create", "body": "must not overwrite"})
	if again.ID != d.ID || again.Content.Body != "Proposed reply" {
		t.Fatal("create retry overwrote draft")
	}
	second := mustDraft(t, ctx, map[string]any{"conversation_id": in["conversation_id"]})
	if second.ID == d.ID || second.Content.Body != "" {
		t.Fatal("multiple/incomplete drafts not supported")
	}
	got, err := a.toolDraftUpdate(ctx, map[string]any{"id": d.ID, "expected_revision": d.Revision, "body": "Edited", "source": "human"})
	if err != nil {
		t.Fatal(err)
	}
	updated := got.(map[string]any)["draft"].(*conversationDraft)
	if updated.Content.BodyHTML != d.Content.BodyHTML || len(updated.Content.Attachments) != 1 || updated.CreatedBy != d.CreatedBy || updated.UpdatedBy != "human" {
		t.Fatal("partial edit lost content/authorship")
	}
	list, err := a.toolDraftList(ctx, map[string]any{"conversation_id": in["conversation_id"], "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	listing := list.(map[string]any)
	if listing["total"] != 2 || len(listing["drafts"].([]map[string]any)) != 1 || listing["content_included"] != false {
		t.Fatalf("listing=%v", listing)
	}
	if _, ok := listing["drafts"].([]map[string]any)[0]["content"]; ok {
		t.Fatal("full content leaked into summaries")
	}
	_, err = a.toolDraftDiscard(ctx, map[string]any{"id": updated.ID, "expected_revision": updated.Revision})
	if err != nil {
		t.Fatal(err)
	}
	retained, _ := getDraft(ctx.AppDB(), "test-proj", d.ID)
	if retained.Status != "discarded" || retained.Content.Body != "Edited" || len(retained.Content.Attachments) != 1 {
		t.Fatal("discard removed content")
	}
	var afterCount int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_activities`).Scan(&afterCount)
	after, _ := dbConversationGet(ctx.AppDB(), "test-proj", before.ID)
	if count != afterCount || before.Status != after.Status || before.LastActivityAt != after.LastActivityAt || len(p.sends) != 0 {
		t.Fatal("saving/discarding mutated real messages or thread")
	}
	if _, err = getDraft(ctx.AppDB(), "foreign-project", d.ID); !errors.Is(err, errDraftNotFound) {
		t.Fatalf("foreign draft read: %v", err)
	}
}

func TestDraftCASAndReplyOwnership(t *testing.T) {
	ctx, _, c, in := draftFixture(t, "email")
	a := &App{}
	d := mustDraft(t, ctx, map[string]any{"conversation_id": in["conversation_id"], "body": "Original"})
	t.Setenv("APTEVA_PROJECT_ID", "")
	if _, err := a.toolDraftCreate(ctx, map[string]any{"_project_id": "foreign", "conversation_id": in["conversation_id"]}); err == nil {
		t.Fatal("cross-project create accepted")
	}
	t.Setenv("APTEVA_PROJECT_ID", "test-proj")
	if _, err := a.toolDraftCreate(ctx, map[string]any{"conversation_id": in["conversation_id"], "contact_id": c.ID + 100}); err == nil {
		t.Fatal("wrong contact accepted")
	}
	other := mkConversation(t, ctx, "test-proj", c.ID, "email")
	if _, err := a.toolDraftCreate(ctx, map[string]any{"conversation_id": other, "reply_to_activity_id": in["activity_id"]}); err == nil {
		t.Fatal("foreign conversation anchor accepted")
	}
	for _, patch := range []map[string]any{{"to": "primary@example.test"}, {"channel": "sms"}, {"reply_to_activity_id": 12345}, {"template_id": 1.5}, {"body": 123}} {
		patch["id"] = d.ID
		patch["expected_revision"] = d.Revision
		if _, err := a.toolDraftUpdate(ctx, patch); err == nil {
			t.Fatalf("invalid patch accepted: %v", patch)
		}
	}
	if _, err := a.toolDraftUpdate(ctx, map[string]any{"id": d.ID, "expected_revision": d.Revision, "body": "Newest"}); err != nil {
		t.Fatal(err)
	}
	for _, op := range []func(*sdk.AppCtx, map[string]any) (any, error){a.toolDraftUpdate, a.toolDraftDiscard, a.toolDraftSend} {
		if _, err := op(ctx, map[string]any{"id": d.ID, "expected_revision": d.Revision, "body": "stale"}); !errors.Is(err, errDraftConflict) {
			t.Fatalf("stale operation: %v", err)
		}
	}
	latest, _ := getDraft(ctx.AppDB(), "test-proj", d.ID)
	if latest.Content.Body != "Newest" || latest.Status != "draft" {
		t.Fatal("CAS lost latest edit")
	}
}

func TestDraftExplicitSendThreadingAndReplay(t *testing.T) {
	ctx, p, _, in := draftFixture(t, "email")
	d := mustDraft(t, ctx, map[string]any{"conversation_id": in["conversation_id"], "body": "Reply", "body_html": "<b>Reply</b>", "attachments": []any{map[string]any{"content_base64": "bm90ZXM=", "filename": "notes.txt"}}})
	out := sendDraft(t, ctx, d)
	if out["sent"] != true {
		t.Fatalf("send=%v", out)
	}
	if len(p.sends) != 1 {
		t.Fatalf("sends=%d", len(p.sends))
	}
	sent := p.sends[0]
	if sent["to"] != "private@example.test" || sent["from"] != "support@example.test" || sent["conversation_id"] != d.ConversationID || sent["body_html"] != "<b>Reply</b>" || sent["in_reply_to"] != "<original@example.test>" || sent["attachments"] == nil {
		t.Fatalf("wrong sent payload: %v", sent)
	}
	stored := out["draft"].(*conversationDraft)
	if stored.Status != "sent" || stored.Content.Body != "Reply" || len(stored.Content.Attachments) != 1 {
		t.Fatal("sent draft content lost")
	}
	repeat := sendDraft(t, ctx, d)
	if repeat["sent"] != true || repeat["deduped"] != true || len(p.sends) != 1 {
		t.Fatal("repeated send duplicated delivery")
	}
	if _, err := (&App{}).toolDraftDiscard(ctx, map[string]any{"id": stored.ID, "expected_revision": stored.Revision}); err == nil {
		t.Fatal("sent draft discarded")
	}
	convo, _ := dbConversationGet(ctx.AppDB(), "test-proj", d.ConversationID)
	if convo.Status != "pending" {
		t.Fatal("successful reply did not use existing workflow")
	}
}

func TestDraftFailureRetentionAndSafeRetry(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "known rejection", true: "accepted but timed out"}[uncertain], func(t *testing.T) {
			ctx, p, _, in := draftFixture(t, "email")
			p.reject = !uncertain
			p.timeout = uncertain
			d := mustDraft(t, ctx, map[string]any{"conversation_id": in["conversation_id"], "body": "Retain me"})
			out := sendDraft(t, ctx, d)
			failed := out["draft"].(*conversationDraft)
			want := "draft"
			if uncertain {
				want = "send_failed"
			}
			if out["sent"] != false || failed.Status != want || failed.Content.Body != "Retain me" || failed.LastError == "" {
				t.Fatalf("failure=%v", out)
			}
			if uncertain {
				for _, op := range []func(*sdk.AppCtx, map[string]any) (any, error){(&App{}).toolDraftUpdate, (&App{}).toolDraftDiscard} {
					if _, err := op(ctx, map[string]any{"id": failed.ID, "expected_revision": failed.Revision, "body": "different"}); err == nil {
						t.Fatal("uncertain content was editable")
					}
				}
			}
			p.reject = false
			retry := sendDraft(t, ctx, failed)
			if retry["sent"] != true {
				t.Fatalf("retry=%v", retry)
			}
			if uncertain && p.sends[0]["idempotency_key"] != p.sends[1]["idempotency_key"] {
				t.Fatal("uncertain retry changed operation key")
			}
			if len(p.accepted) != 1 {
				t.Fatalf("accepted deliveries=%d", len(p.accepted))
			}
			var acts int
			ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_activities WHERE kind='email_sent'`).Scan(&acts)
			if acts != 1 {
				t.Fatalf("outbound activities=%d", acts)
			}
		})
	}
}

func TestDraftConcurrentSendOnlyDispatchesOnce(t *testing.T) {
	ctx, p, _, in := draftFixture(t, "email")
	p.started = make(chan struct{})
	p.release = make(chan struct{})
	started := p.started
	d := mustDraft(t, ctx, map[string]any{"conversation_id": in["conversation_id"], "body": "Once"})
	done := make(chan error, 1)
	go func() {
		_, err := (&App{}).toolDraftSend(ctx, map[string]any{"id": d.ID, "expected_revision": d.Revision})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("send did not start")
	}
	current, err := getDraft(ctx.AppDB(), "test-proj", d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{}).toolDraftSend(ctx, map[string]any{"id": current.ID, "expected_revision": current.Revision}); err == nil {
		t.Fatal("overlapping lease accepted")
	}
	if _, err := (&App{}).toolDraftUpdate(ctx, map[string]any{"id": current.ID, "expected_revision": current.Revision, "body": "changed"}); err == nil {
		t.Fatal("in-progress content changed")
	}
	close(p.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(p.sends) != 1 {
		t.Fatal("concurrent send duplicated delivery")
	}
}

func TestDraftRecoversCrashAfterActivityRecorded(t *testing.T) {
	ctx, p, c, in := draftFixture(t, "email")
	d := mustDraft(t, ctx, map[string]any{"conversation_id": in["conversation_id"], "body": "Accepted before crash"})
	act, err := logMessageActivity(ctx.AppDB(), logMessageActivityInput{ProjectID: "test-proj", ContactID: c.ID, ConversationID: d.ConversationID, Kind: "email_sent", Body: d.Content.Body, IdempotencyKey: "crash-key", MessagingID: 999, MessagingInstallID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ctx.AppDB().Exec(`UPDATE conversation_drafts SET status='sending',attempt_key='crash-key',dispatched=1,lease_until='2000-01-01T00:00:00Z' WHERE id=?`, d.ID); err != nil {
		t.Fatal(err)
	}
	// Changed eligibility must not hide a delivery that is already committed.
	p.suppressed = true
	p.wrongSender = true
	out := sendDraft(t, ctx, d)
	if out["sent"] != true || len(p.sends) != 0 || out["result"].(map[string]any)["activity"].(*Activity).ID != act.ID {
		t.Fatalf("crash recovery=%v", out)
	}
}

func TestDraftSendRevalidatesSafety(t *testing.T) {
	for _, scenario := range []string{"unverified sender", "suppressed", "do not contact", "source rebound", "recipient changed", "archived"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, p, c, in := draftFixture(t, "email")
			d := mustDraft(t, ctx, map[string]any{"conversation_id": in["conversation_id"], "body": "Must not send"})
			switch scenario {
			case "unverified sender":
				p.wrongSender = true
			case "suppressed":
				p.suppressed = true
			case "do not contact":
				if err := dbSetAttribute(ctx.AppDB(), "test-proj", c.ID, "do_not_contact", true, "human"); err != nil {
					t.Fatal(err)
				}
			case "source rebound":
				ctx.AppDB().Exec(`UPDATE conversation_drafts SET messaging_install_id=123 WHERE id=?`, d.ID)
			case "recipient changed":
				ctx.AppDB().Exec(`UPDATE contact_activities SET source_detail=json_set(source_detail,'$.reply_to','someone-else@example.test') WHERE id=?`, d.ReplyToActivityID)
			case "archived":
				ctx.AppDB().Exec(`UPDATE contacts SET status='archived' WHERE id=?`, c.ID)
			}
			out := sendDraft(t, ctx, d)
			if out["sent"] != false || len(p.sends) != 0 || out["draft"].(*conversationDraft).Content.Body != "Must not send" {
				t.Fatalf("unsafe send=%v", out)
			}
		})
	}
}

func TestDraftWhatsAppWindowAndExplicitSMSFallback(t *testing.T) {
	ctx, p, _, in := draftFixture(t, "whatsapp")
	p.closedWindow = true
	d := mustDraft(t, ctx, map[string]any{"conversation_id": in["conversation_id"], "body": "Freeform draft"})
	out := sendDraft(t, ctx, d)
	blocked := out["draft"].(*conversationDraft)
	if out["sent"] != false || len(p.sends) != 0 || !strings.Contains(blocked.LastError, "window") {
		t.Fatalf("closed window=%v", out)
	}
	updated, err := (&App{}).toolDraftUpdate(ctx, map[string]any{"id": blocked.ID, "expected_revision": blocked.Revision, "channel": "sms", "from": "+15550002222"})
	if err != nil {
		t.Fatal(err)
	}
	sms := updated.(map[string]any)["draft"].(*conversationDraft)
	if out = sendDraft(t, ctx, sms); out["sent"] != true || p.sends[0]["to"] != "+15551234567" || p.sends[0]["channel"] != "sms" || p.sends[0]["conversation_id"] != d.ConversationID {
		t.Fatalf("SMS fallback=%v", out)
	}
	// A template send preserves authored freeform notes but sends only template.
	template := mustDraft(t, ctx, map[string]any{"conversation_id": in["conversation_id"], "channel": "whatsapp", "from": "+15550001111", "body": "Notes must not be sent", "template_id": 7, "template_vars": map[string]any{"name": "Alice"}})
	if out = sendDraft(t, ctx, template); out["sent"] != true {
		t.Fatalf("template=%v", out)
	}
	last := p.sends[len(p.sends)-1]
	if last["body"] != "" || last["template_id"] != int64(7) || last["vars"] == nil {
		t.Fatalf("mixed template payload=%v", last)
	}
}

func TestDraftsSurviveContactMerge(t *testing.T) {
	ctx, _, loser, in := draftFixture(t, "whatsapp")
	winner := mustCreate(t, ctx, map[string]any{"first_name": "Winner", "channels": []any{map[string]any{"kind": "phone", "value": "+15559999999"}}})
	winningThread := mkConversation(t, ctx, "test-proj", winner.ID, "whatsapp")
	d := mustDraft(t, ctx, map[string]any{"conversation_id": in["conversation_id"], "body": "Do not lose me"})
	if err := dbMerge(ctx.AppDB(), "test-proj", loser.ID, winner.ID, "", "human"); err != nil {
		t.Fatal(err)
	}
	saved, err := getDraft(ctx.AppDB(), "test-proj", d.ID)
	if err != nil || saved.ContactID != winner.ID || saved.ConversationID != winningThread || saved.Content.Body != d.Content.Body || saved.Revision <= d.Revision {
		t.Fatalf("merge draft=%v %v", saved, err)
	}
	var contact, conversation int64
	ctx.AppDB().QueryRow(`SELECT contact_id,conversation_id FROM contact_activities WHERE id=?`, d.ReplyToActivityID).Scan(&contact, &conversation)
	if contact != winner.ID || conversation != winningThread {
		t.Fatal("merge lost reply anchor")
	}
	next := mustCreate(t, ctx, map[string]any{"first_name": "Next"})
	ctx.AppDB().Exec(`UPDATE conversation_drafts SET status='send_failed',dispatched=1 WHERE id=?`, d.ID)
	if err := dbMerge(ctx.AppDB(), "test-proj", winner.ID, next.ID, "", "human"); err == nil {
		t.Fatal("merge accepted uncertain delivery")
	}
}

func TestDraftHTTPAndMCPContracts(t *testing.T) {
	ctx, p, _, in := draftFixture(t, "email")
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	a := &App{}
	raw, _ := json.Marshal(map[string]any{"conversation_id": in["conversation_id"], "body": "HTTP draft"})
	w := httptest.NewRecorder()
	a.handleHTTPDrafts(w, httptest.NewRequest("POST", "/drafts?project_id=test-proj", bytes.NewReader(raw)))
	if w.Code != 200 {
		t.Fatalf("HTTP create: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Draft conversationDraft `json:"draft"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	a.handleHTTPDrafts(w, httptest.NewRequest("POST", "/drafts/"+anyString(result.Draft.ID)+"/send?project_id=test-proj", strings.NewReader(`{"expected_revision":999}`)))
	if w.Code != 409 {
		t.Fatalf("HTTP CAS=%d %s", w.Code, w.Body.String())
	}
	t.Setenv("APTEVA_PROJECT_ID", "")
	w = httptest.NewRecorder()
	a.handleHTTPDrafts(w, httptest.NewRequest("GET", "/drafts/"+anyString(result.Draft.ID)+"?project_id=other", nil))
	if w.Code != 404 {
		t.Fatalf("foreign get=%d", w.Code)
	}
	if len(p.sends) != 0 {
		t.Fatal("save/invalid send dispatched")
	}
	for _, tool := range a.draftTools() {
		read := tool.Name == "conversation_drafts_get" || tool.Name == "conversation_drafts_list"
		send := tool.Name == "conversation_drafts_send"
		if tool.Annotations["readOnlyHint"] != read || tool.Annotations["destructiveHint"] != send {
			t.Fatalf("unsafe tool contract %s: %v", tool.Name, tool.Annotations)
		}
		if !send && !strings.Contains(strings.ToLower(tool.Description), "send") {
			t.Fatalf("save semantics missing: %s", tool.Name)
		}
	}
}
