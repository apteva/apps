package main

// Regression coverage from the v0.9.1 audit. Messaging uses local test doubles.
import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestAuditGlobalHTTPEventProject(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithProjectID(""), tk.WithEmitter(rec))
	globalCtx = ctx
	w := httptest.NewRecorder()
	(&App{}).handleHTTPListsCreate(w, httptest.NewRequest(http.MethodPost, "/lists?project_id=project-a", bytes.NewBufferString(`{"name":"A"}`)))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	events := rec.EventsByTopic("list.created")
	if len(events) != 1 {
		t.Fatalf("events=%+v", events)
	}
	if events[0].ProjectID != "project-a" {
		t.Fatalf("global HTTP list event routed to project %q instead of project-a", events[0].ProjectID)
	}
}

func TestAuditMembershipEventsAreIdempotent(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithEmitter(rec))
	app := &App{}
	c := mustCreate(t, ctx, map[string]any{"first_name": "A"})
	l, err := dbListCreate(ctx.AppDB(), "test-proj", &List{Name: "A"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := app.toolListsAddContact(ctx, map[string]any{"contact_id": c.ID, "list_id": l.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(rec.EventsByTopic("list.member.added")); n != 1 {
		t.Fatalf("idempotent repeated membership add emitted %d events", n)
	}
}

func TestAuditFullMessageBody(t *testing.T) {
	ctx := newTestCtx(t)
	body := strings.Repeat("x", 4500) + " IMPORTANT FINAL INSTRUCTION"
	result, err := ingestInbound(ctx, "test-proj", inboundPayload{Channel: "email", From: "long@example.test", MessageID: 901, MessageIDHeader: "<long>", BodyText: body})
	if err != nil {
		t.Fatal(err)
	}
	activities, err := dbConversationActivities(ctx.AppDB(), "test-proj", int64FromAny(result["conversation_id"]), 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(activities) != 1 {
		t.Fatalf("activities=%d", len(activities))
	}
	if activities[0].Body != body {
		t.Fatalf("conversation API returns only %d of %d body bytes, without truncation metadata", len(activities[0].Body), len(body))
	}
}

func TestAuditSystemAttributeSeeding(t *testing.T) {
	ctx := newTestCtx(t)
	mustCreate(t, ctx, map[string]any{"first_name": "A"})
	defs, err := dbListAttrDefs(ctx.AppDB(), "test-proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) == 0 {
		t.Fatal("first contact creation did not seed any of the ten system attribute templates")
	}
}

func TestAuditDiagnosticsInboxQueryPlan(t *testing.T) {
	ctx := newTestCtx(t)
	rows, err := ctx.AppDB().Query(`EXPLAIN QUERY PLAN SELECT a.body FROM contact_activities a WHERE a.conversation_id = ? ORDER BY a.occurred_at DESC,a.id DESC LIMIT 1`, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Log(detail)
	}
}

func BenchmarkAuditSuppressionScan(b *testing.B) {
	for _, size := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("suppressions_%d", size), func(b *testing.B) {
			items := make([]messagingSuppression, size)
			for i := range items {
				items[i] = messagingSuppression{Channel: "email", Kind: "address", Address: fmt.Sprintf("blocked%d@example.test", i)}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				effectiveSuppression(items, "email", "healthy@example.test")
			}
		})
	}
}

func TestAuditMergePersistentConversations(t *testing.T) {
	ctx := newTestCtx(t)
	a := mustCreate(t, ctx, map[string]any{"first_name": "A"})
	b := mustCreate(t, ctx, map[string]any{"first_name": "B"})
	mkConversation(t, ctx, "test-proj", a.ID, "sms")
	mkConversation(t, ctx, "test-proj", b.ID, "sms")
	if err := dbMerge(ctx.AppDB(), "test-proj", a.ID, b.ID, "", "audit"); err != nil {
		t.Fatalf("merging two contacts with SMS history must consolidate conversations: %v", err)
	}
}

func TestAuditMergeLookupAndPrimary(t *testing.T) {
	ctx := newTestCtx(t)
	loser := mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": "old@example.test"}}})
	winner := mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": "winner@example.test"}}})
	if err := dbMerge(ctx.AppDB(), "test-proj", loser.ID, winner.ID, "", "audit"); err != nil {
		t.Fatal(err)
	}
	got, err := dbGetByPrimary(ctx.AppDB(), "test-proj", "email", "old@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != winner.ID {
		t.Errorf("merged address lookup returned %+v, want surviving contact %d", got, winner.ID)
	}
	fresh, err := dbGetByID(ctx.AppDB(), "test-proj", winner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.PrimaryEmail != "winner@example.test" {
		t.Errorf("winner primary overwritten: %q", fresh.PrimaryEmail)
	}
	var n int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_channels WHERE contact_id=? AND kind='email' AND is_primary=1`, winner.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("merged contact has %d primary email channels", n)
	}
}

func TestAuditArchivedContactRecovery(t *testing.T) {
	ctx := newTestCtx(t)
	globalCtx = ctx
	app := &App{}
	c := mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": "archived@example.test"}}})
	w := httptest.NewRecorder()
	app.handleHTTPArchive(w, httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/contacts/%d", c.ID), nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	rows, err := dbSearch(ctx.AppDB(), "test-proj", "", []any{map[string]any{"field": "status", "op": "eq", "value": "archived"}}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Errorf("Status=archived cannot retrieve archived contact; rows=%d", len(rows))
	}
	_, err = app.toolUpsertByChannel(ctx, map[string]any{"kind": "email", "value": "archived@example.test"})
	if err != nil {
		t.Errorf("archived address is permanently reserved but unresolvable: %v", err)
	}
}

func TestAuditActivityProjectOwnership(t *testing.T) {
	ctx := newTestCtx(t)
	foreign, err := dbCreate(ctx.AppDB(), "other-project", map[string]any{"first_name": "Foreign"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&App{}).toolLogActivity(ctx, map[string]any{"contact_id": foreign.ID, "kind": "note", "body": "wrong partition"})
	if err == nil {
		t.Fatal("test-proj successfully wrote an activity referencing another project's contact")
	}
}

func TestAuditReplyUsesThreadAddress(t *testing.T) {
	pf := &crmRecordingPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	c := mustCreate(t, ctx, map[string]any{"channels": []any{
		map[string]any{"kind": "email", "value": "work@example.test", "is_primary": true},
		map[string]any{"kind": "email", "value": "private@example.test"},
	}})
	in, err := ingestInbound(ctx, "test-proj", inboundPayload{Channel: "email", From: "private@example.test", To: []string{"support@example.test"}, MessageID: 900, MessageIDHeader: "<private-thread>", Subject: "Private", BodyText: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&App{}).toolReply(ctx, map[string]any{"id": c.ID, "conversation_id": in["conversation_id"], "body": "Confidential reply", "from": "support@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range pf.calls {
		if call.Tool == "send_message" && call.Input["to"] != "private@example.test" {
			t.Errorf("reply sent to %v instead of thread sender private@example.test", call.Input["to"])
		}
	}
}

func TestAuditCrossContactThreadLink(t *testing.T) {
	ctx := newTestCtx(t)
	first, err := ingestInbound(ctx, "test-proj", inboundPayload{Channel: "email", From: "a@example.test", MessageID: 801, MessageIDHeader: "<thread-a>", BodyText: "A"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ingestInbound(ctx, "test-proj", inboundPayload{Channel: "email", From: "b@example.test", MessageID: 802, MessageIDHeader: "<thread-b>", InReplyTo: "<thread-a>", BodyText: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if first["contact_id"] == second["contact_id"] {
		t.Fatal("fixture contacts should differ")
	}
	if first["conversation_id"] == second["conversation_id"] {
		t.Fatalf("B's activity attached to A's contact-owned conversation: %v", second)
	}
}

func TestAuditDateSegmentExecutes(t *testing.T) {
	ctx := newTestCtx(t)
	mustCreate(t, ctx, map[string]any{"first_name": "A"})
	s, err := dbSegmentCreate(ctx.AppDB(), "test-proj", &Segment{Name: "Created since", Definition: json.RawMessage(`[{"field":"created_at","op":"gte","value":"2020-01-01"}]`)})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = dbSegmentEval(ctx.AppDB(), "test-proj", s, 20)
	if err != nil {
		t.Fatalf("accepted date segment fails during evaluation: %v", err)
	}
}

func TestAuditTypedAttributeFilters(t *testing.T) {
	ctx := newTestCtx(t)
	for _, def := range []struct{ key, typ string }{{"renewal", "date"}, {"score", "number"}} {
		if _, err := dbDefineAttribute(ctx.AppDB(), "test-proj", def.key, def.key, def.typ, nil, false, 0); err != nil {
			t.Fatal(err)
		}
	}
	populated := mustCreate(t, ctx, map[string]any{"attributes": []any{map[string]any{"key": "renewal", "value": "2026-10-01"}, map[string]any{"key": "score", "value": 42.0}}})
	empty := mustCreate(t, ctx, map[string]any{"first_name": "Empty"})
	dates, err := dbSearch(ctx.AppDB(), "test-proj", "", []any{map[string]any{"attribute": "renewal", "op": "eq", "value": "2026-10-01"}}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(dates) != 1 || dates[0].ID != populated.ID {
		t.Errorf("stored date does not match date filter: %+v", dates)
	}
	missing, err := dbSearch(ctx.AppDB(), "test-proj", "", []any{map[string]any{"attribute": "score", "op": "is_null"}}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0].ID != empty.ID {
		t.Errorf("is_null returned %+v; want missing-value contact %d, not populated %d", missing, empty.ID, populated.ID)
	}
}

func TestAuditReconcilePreservesDeliveryEvidence(t *testing.T) {
	pf := &suppressionListPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	app := &App{}
	c := mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": "hard@example.test"}}})
	if err := app.handleMessagingDeliveryEvent(ctx, deliveryEvent("hard-evidence", "bounced", "hard@example.test", "2026-09-01T10:00:00Z", true)); err != nil {
		t.Fatal(err)
	}
	if err := app.reconcileProjectSuppressions(ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
	state := emailDeliveryState(t, ctx, c.Channels[0].ID)
	if state.Status != "hard_bounced" || state.Messageable {
		t.Fatalf("empty suppression snapshot erased hard-bounce evidence: %+v", state)
	}
}

func TestAuditRecencyDoesNotRegress(t *testing.T) {
	ctx := newTestCtx(t)
	c := mustCreate(t, ctx, map[string]any{"first_name": "A"})
	for _, when := range []string{"2026-09-05T12:00:00Z", "2026-09-01T12:00:00Z"} {
		if _, err := dbLogActivity(ctx.AppDB(), "test-proj", c.ID, "note", "history", when, "audit"); err != nil {
			t.Fatal(err)
		}
	}
	fresh, err := dbGetByID(ctx.AppDB(), "test-proj", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.LastContactAt != "2026-09-05T12:00:00Z" {
		t.Fatalf("late historical activity moved recency backwards: %s", fresh.LastContactAt)
	}
}

func TestAuditPrimaryScalarMaintainsChannels(t *testing.T) {
	ctx := newTestCtx(t)
	c := mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": "old@example.test"}}})
	_, err := (&App{}).toolUpdate(ctx, map[string]any{"id": c.ID, "patch": map[string]any{"primary_email": "new@example.test"}})
	if err == nil {
		t.Fatal("primary mirror patches must be rejected; edit channels instead")
	}
	fresh, err := dbGetByID(ctx.AppDB(), "test-proj", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := resolveContactAddress(ctx.AppDB(), "test-proj", fresh, "email")
	if err != nil {
		t.Fatal(err)
	}
	if addr.Address != fresh.PrimaryEmail {
		t.Fatalf("UI/API says %s but sending still targets %s", fresh.PrimaryEmail, addr.Address)
	}
}

func TestAuditAttributeWriteUpdatesContactTimestamp(t *testing.T) {
	ctx := newTestCtx(t)
	if _, err := dbDefineAttribute(ctx.AppDB(), "test-proj", "score", "Score", "number", nil, false, 0); err != nil {
		t.Fatal(err)
	}
	c := mustCreate(t, ctx, map[string]any{"first_name": "A"})
	if _, err := ctx.AppDB().Exec(`UPDATE contacts SET updated_at='2020-01-01T00:00:00Z' WHERE id=?`, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{}).toolSetAttribute(ctx, map[string]any{"contact_id": c.ID, "key": "score", "value": 42.0}); err != nil {
		t.Fatal(err)
	}
	fresh, err := dbGetByID(ctx.AppDB(), "test-proj", c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.UpdatedAt == "2020-01-01T00:00:00Z" {
		t.Fatal("custom attribute write leaves contact updated_at unchanged")
	}
}
