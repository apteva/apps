package main

import (
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestChangeReplayRecoversOldApprovalAndRequeuesSurface(t *testing.T) {
	a, ctx, _ := newTestEnv(t)
	c := mkConversation(t, a, 41)
	m, _, err := a.appendAndDeliver(ctx, c, &Message{ConversationID: c.ID, Role: "agent", AgentID: 41, Content: "approve", ComponentKind: kindApproval, Components: []Component{approvalCard("approve", "", defaultApprovalActions())}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := a.store.MessagePage(c.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 250; i++ {
		if _, err := a.store.AppendMessage(&Message{ConversationID: c.ID, Role: "system", Content: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.store.ResolveApproval(m.ID, m.Components, "approve", ""); err != nil {
		t.Fatal(err)
	}
	found := false
	cursor := page.Cursor
	for {
		changes, err := a.store.MessageChanges(c.ID, cursor, 100)
		if err != nil {
			t.Fatal(err)
		}
		if changes.Cursor < cursor {
			t.Fatal("cursor moved backwards")
		}
		cursor = changes.Cursor
		for _, row := range changes.Messages {
			if row.ID == m.ID && row.ActionStatus == "approve" {
				found = true
			}
		}
		if !changes.HasMore {
			break
		}
	}
	if !found {
		t.Fatal("old approval resolution missing from durable replay")
	}
	d, err := a.store.DeliveryFor(m.ID, "web:conv")
	if err != nil || d.Status != "pending" {
		t.Fatalf("surface was not atomically requeued: %+v %v", d, err)
	}
}
func TestMessageSnapshotPagesDoNotSkipLiveRows(t *testing.T) {
	a, _, _ := newTestEnv(t)
	c := mkConversation(t, a, 41)
	for i := 0; i < 501; i++ {
		if _, err := a.store.AppendMessage(&Message{ConversationID: c.ID, Role: "system", Content: "row"}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int64]bool{}
	before := int64(0)
	for {
		page, err := a.store.MessagePage(c.ID, before, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range page.Messages {
			if seen[m.ID] {
				t.Fatal("duplicate page row")
			}
			seen[m.ID] = true
		}
		if !page.HasMore {
			break
		}
		before = page.Before
	}
	if len(seen) != 501 {
		t.Fatalf("only %d rows", len(seen))
	}
}
func TestMessageRequestKeyRejectsChangedAttachmentsAndCard(t *testing.T) {
	a, _, _ := newTestEnv(t)
	c := mkConversation(t, a, 41)
	m := &Message{ConversationID: c.ID, Role: "agent", Content: "same", ClientID: "key", Components: []Component{reportCard("A", "summary", "", nil)}}
	if _, _, err := a.store.AppendMessageWithDeliveries(m, nil); err != nil {
		t.Fatal(err)
	}
	if _, inserted, err := a.store.AppendMessageWithDeliveries(m, nil); err != nil || inserted {
		t.Fatalf("identical retry: %v %v", inserted, err)
	}
	m.Components = []Component{reportCard("B", "summary", "", nil)}
	if _, _, err := a.store.AppendMessageWithDeliveries(m, nil); err == nil {
		t.Fatal("changed card accepted under reused key")
	}
}
func TestOldLeaseCannotCompleteNewGeneration(t *testing.T) {
	a, _, _ := newTestEnv(t)
	c := mkConversation(t, a, 41)
	m, _, err := a.store.AppendMessageWithDeliveries(&Message{ConversationID: c.ID, Role: "agent", ComponentKind: kindReport, Components: []Component{reportCard("A", "B", "", nil)}}, []string{"web:conv"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := a.store.ClaimDelivery(m.ID, "web:conv")
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	if _, err := a.store.UpdateMessageComponents(m.ID, []Component{reportCard("new", "new", "", nil)}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.FinishDelivery(m.ID, "web:conv", claim, nil); err != nil {
		t.Fatal(err)
	}
	d, _ := a.store.DeliveryFor(m.ID, "web:conv")
	if d.Status != "pending" {
		t.Fatalf("new version lost: %+v", d)
	}
}
func TestStreamIncrementalEscapesAndLateDestination(t *testing.T) {
	h := newHub()
	s := newStreamer(h)
	s.resolve = func(int64, string) string { return "conv-1" }
	ch, cancel := h.subscribeFrames("conv-1")
	defer cancel()
	raw := `{"text":"line\n\ud83d\ude03","conversation_id":"conv-OTHER"}`
	for _, c := range raw {
		s.onChunk(41, "chat-conv-1", "conv-1", fmt.Sprintf(`{"tool":"conversations_send","id":"c","chunk":%q}`, string(c)), time.Now())
	}
	select {
	case f := <-ch:
		t.Fatalf("foreign destination leaked: %+v", f)
	default:
	}
	parser := flatStringParser{}
	var b strings.Builder
	for _, c := range `{"text":"line\n\ud83d\ude03","conversation_id":"conv-1"}` {
		b.WriteRune(c)
		parser.consume(b.String())
	}
	if parser.text.String() != "line\n😃" || parser.destination != "conv-1" {
		t.Fatalf("text=%q destination=%q", parser.text.String(), parser.destination)
	}
}

func TestTelegramPartsPreserveUnicodeAndFormatterCode(t *testing.T) {
	text := strings.Repeat("😃abc", 2000)
	parts := telegramTextParts(text)
	if strings.Join(parts, "") != text {
		t.Fatal("split lost content")
	}
	for _, p := range parts {
		units := 0
		for _, r := range p {
			units++
			if r > 0xffff {
				units++
			}
		}
		if units > 3500 {
			t.Fatal("part exceeds Telegram UTF16 limit")
		}
	}
	got := telegramMarkdownInlineHTML("**use `x < y`** and [a `code`](https://example.com)")
	if !strings.Contains(got, "<b>use <code>x &lt; y</code></b>") || !strings.Contains(got, "a <code>code</code>") {
		t.Fatalf("nested formatting corrupt: %s", got)
	}
}
func TestTelegramNewRetryDoesNotRotateTwice(t *testing.T) {
	a, _, p := newTestEnv(t)
	enableTelegramForTest(t, a, p)
	c := mkConversation(t, a, 41)
	b := bindTelegramForTest(t, a, c, "123", []int64{123})
	next, err := a.rotateTelegramConversation(b, telegramMessage{Chat: telegramChat{ID: 123, Type: "private"}, From: telegramUser{ID: 123}}, 900)
	if err != nil {
		t.Fatal(err)
	}
	b.ConversationID = next.ID
	retried, err := a.rotateTelegramConversation(b, telegramMessage{Chat: telegramChat{ID: 123, Type: "private"}, From: telegramUser{ID: 123}}, 900)
	if err != nil {
		t.Fatal(err)
	}
	if retried.ID != next.ID {
		t.Fatal("command retry rotated again")
	}
}
func TestConversationPagesAndSearchCoverOlderRows(t *testing.T) {
	a, _, _ := newTestEnv(t)
	for i := 0; i < 205; i++ {
		mkConversation(t, a, 41)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := a.store.ListConversationPage(testProject, 1, 41, 0, false, "", cursor, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range page.Conversations {
			if seen[c.ID] {
				t.Fatal("duplicate page row")
			}
			seen[c.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 205 {
		t.Fatalf("page count %d", len(seen))
	}
}

func TestInboxPagesHaveAccurateTotalsAndPriority(t *testing.T) {
	a, _, _ := newTestEnv(t)
	c := mkConversation(t, a, 41)
	for i := 0; i < 121; i++ {
		kind := kindReport
		if i == 0 {
			kind = kindApproval
		}
		_, err := a.store.AppendMessage(&Message{ConversationID: c.ID, Role: "agent", AgentID: 41, Content: fmt.Sprint(i), ComponentKind: kind, ActionStatus: "pending"})
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int64]bool{}
	cursor := ""
	for {
		page, err := a.store.InboxPage(testProject, 1, 41, 25, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 121 {
			t.Fatalf("total=%d", page.Total)
		}
		if cursor == "" && page.Items[0].Message.ComponentKind != kindApproval {
			t.Fatal("old approval missing from first position")
		}
		for _, item := range page.Items {
			if seen[item.Message.ID] {
				t.Fatal("duplicate item")
			}
			seen[item.Message.ID] = true
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 121 {
		t.Fatalf("recovered %d items", len(seen))
	}
}
func TestArchivedCardsAndSettingsAreReadOnly(t *testing.T) {
	a, _, _ := newTestEnv(t)
	c := mkConversation(t, a, 41)
	m, err := a.store.AppendMessage(&Message{ConversationID: c.ID, Role: "agent", Content: "approve", ComponentKind: kindApproval, ActionStatus: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.SetConversationArchived(c.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.ResolveApproval(m.ID, nil, "approve", ""); err == nil {
		t.Fatal("archived approval changed")
	}
	if _, err = a.store.UpdateMessageComponents(m.ID, nil); err == nil {
		t.Fatal("archived card changed")
	}
	if _, err = a.store.UpdateConversationTitle(c.ID, "new"); err == nil {
		t.Fatal("archived title changed")
	}
	if _, err = a.store.UpdateConversationDirective(c.ID, "new"); err == nil {
		t.Fatal("archived directive changed")
	}
}
func TestAmbiguousDeliveryRemainsBlockedAcrossMutations(t *testing.T) {
	a, _, _ := newTestEnv(t)
	c := mkConversation(t, a, 41)
	m, _, err := a.store.AppendMessageWithDeliveries(&Message{ConversationID: c.ID, Role: "agent", Content: "old"}, []string{"telegram:route"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := a.store.ClaimDelivery(m.ID, "telegram:route")
	if err != nil || claim == nil {
		t.Fatalf("claim %v", err)
	}
	a.store.db.Exec(`UPDATE messages SET content='new' WHERE id=?`, m.ID)
	if err = a.store.FinishDelivery(m.ID, "telegram:route", claim, &ambiguousDeliveryError{fmt.Errorf("lost response")}); err != nil {
		t.Fatal(err)
	}
	a.store.db.Exec(`UPDATE messages SET content='newer' WHERE id=?`, m.ID)
	var status string
	a.store.db.QueryRow(`SELECT status FROM deliveries WHERE message_id=?`, m.ID).Scan(&status)
	if status != "ambiguous" {
		t.Fatalf("unsafe automatic retry: %s", status)
	}
}
func TestTelegramCleanupKeepsUnfinishedClaims(t *testing.T) {
	a, _, _ := newTestEnv(t)
	if err := a.store.UpsertTelegramConnection(TelegramConnectionConfig{ConnectionID: 1, WebhookKey: "key", WebhookSecret: "secret", WebhookURL: "url"}); err != nil {
		t.Fatal(err)
	}
	_, err := a.store.db.Exec(`INSERT INTO telegram_updates(connection_id,update_id,completed,received_at) VALUES(1,1,0,'2000-01-01'),(1,2,1,'2000-01-01')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.PruneTelegramState(); err != nil {
		t.Fatal(err)
	}
	var count int
	a.store.db.QueryRow(`SELECT COUNT(*) FROM telegram_updates WHERE update_id=1`).Scan(&count)
	if count != 1 {
		t.Fatal("unfinished work pruned")
	}
	a.store.db.QueryRow(`SELECT COUNT(*) FROM telegram_updates WHERE update_id=2`).Scan(&count)
	if count != 0 {
		t.Fatal("completed history not pruned")
	}
}
func TestStreamRunIdentityAndFinalOnlyExpiry(t *testing.T) {
	s := newStreamer(newHub())
	var frames []StreamFrame
	s.onFrame = func(f StreamFrame) { frames = append(frames, f) }
	data := `{"name":"conversations_send","id":"reused","args":{"text":"hi"}}`
	s.Ingest("tool.call", 41, "chat-a", data, time.Now())
	s.Ingest("tool.result", 41, "chat-a", `{"name":"conversations_send","id":"reused"}`, time.Now())
	s.Ingest("tool.call", 41, "chat-a", data, time.Now())
	if len(frames) != 3 || frames[0].RunID == frames[2].RunID || frames[0].RunID != frames[1].RunID {
		t.Fatalf("run identity %+v", frames)
	}
	s.mu.Lock()
	for key := range s.touched {
		s.touched[key] = time.Now().Add(-10 * time.Minute)
	}
	s.pruneLocked()
	s.mu.Unlock()
	if len(s.buffers) != 0 || len(s.lastEmit) != 0 {
		t.Fatal("abandoned final call retained")
	}
}

type snapshotAdapter struct{ content string }

func (*snapshotAdapter) ID() string { return "snapshot" }
func (s *snapshotAdapter) Deliver(_ *sdk.AppCtx, _ string, _ *Conversation, m *Message) error {
	s.content = m.Content
	return nil
}
func TestClaimReadsCurrentPayloadNotRecoverySnapshot(t *testing.T) {
	a, ctx, _ := newTestEnv(t)
	c := mkConversation(t, a, 41)
	capture := &snapshotAdapter{}
	a.adapters.register(capture)
	old, _, err := a.store.AppendMessageWithDeliveries(&Message{ConversationID: c.ID, Role: "agent", Content: "old"}, []string{"snapshot:route"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec(`UPDATE messages SET content='current' WHERE id=?`, old.ID); err != nil {
		t.Fatal(err)
	}
	a.attemptDelivery(ctx, "snapshot:route", c, old)
	if capture.content != "current" {
		t.Fatalf("dispatched stale snapshot %q", capture.content)
	}
}
func TestApprovalLifecycleIsIdempotentAndMonotonic(t *testing.T) {
	a, ctx, _ := newTestEnv(t)
	c := mkConversation(t, a, 41)
	m, err := a.store.AppendMessage(&Message{ConversationID: c.ID, Role: "agent", Content: "approval", ComponentKind: kindApproval})
	if err != nil {
		t.Fatal(err)
	}
	event := func(sequence int, state string) sdk.Event {
		return sdk.Event{Event: sdk.AgentEventLifecycleEvent, DeliveryID: fmt.Sprint(sequence), Data: map[string]any{"source_event_id": fmt.Sprintf("conversation:approval:%d:result", m.ID), "execution_id": "execution", "sequence": sequence, "type": state}}
	}
	for _, e := range []sdk.Event{event(1, sdk.AgentEventActive), event(1, sdk.AgentEventActive), event(2, sdk.AgentEventSettled), event(1, sdk.AgentEventActive)} {
		if err := a.handleApprovalLifecycle(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	var state string
	var count int
	if err := a.store.db.QueryRow(`SELECT state FROM approval_lifecycle WHERE message_id=?`, m.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM approval_lifecycle`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if state != sdk.AgentEventSettled || count != 1 {
		t.Fatalf("lifecycle regressed/duplicated: %s %d", state, count)
	}
}

func TestBlockedTelegramRouteCannotStarveOtherTargets(t *testing.T) {
	a, _, _ := newTestEnv(t)
	c := mkConversation(t, a, 41)
	first, _, err := a.store.AppendMessageWithDeliveries(&Message{ConversationID: c.ID, Role: "agent", Content: "uncertain"}, []string{"telegram:blocked"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec(`UPDATE deliveries SET status='ambiguous' WHERE message_id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 501; i++ {
		if _, _, err := a.store.AppendMessageWithDeliveries(&Message{ConversationID: c.ID, Role: "agent", Content: "waiting"}, []string{"telegram:blocked"}); err != nil {
			t.Fatal(err)
		}
	}
	other, _, err := a.store.AppendMessageWithDeliveries(&Message{ConversationID: c.ID, Role: "agent", Content: "independent"}, []string{"telegram:independent"})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := a.store.PendingDeliveries(500)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].MessageID != other.ID {
		t.Fatalf("blocked route consumed runnable window: %d", len(pending))
	}
}

func TestDeliveryMetricsApplyConversationAuthorization(t *testing.T) {
	a, _, _ := newTestEnv(t)
	c, err := a.store.CreateConversation(CreateConversationInput{ProjectID: testProject, LeadAgentID: 41, OwnerUserID: 1, Title: "Private"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, _, err := a.store.AppendMessageWithDeliveries(&Message{ConversationID: c.ID, Role: "agent", Content: "queued"}, []string{"telegram:route"}); err != nil {
			t.Fatal(err)
		}
	}
	request := func(user string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/deliveries?chat_id="+c.ID+"&stats=1", nil)
		authorizeTestRequest(req)
		req.Header.Set("X-User-ID", user)
		rr := httptest.NewRecorder()
		a.handleDeliveryStatus(rr, req)
		return rr
	}
	allowed := request("1")
	if allowed.Code != 200 || !strings.Contains(allowed.Body.String(), `"count":3`) {
		t.Fatalf("metrics %d %s", allowed.Code, allowed.Body.String())
	}
	denied := request("2")
	if denied.Code != 404 {
		t.Fatalf("private queue metadata leaked: %d", denied.Code)
	}
}

func TestMessageRevisionsIncreaseOnEdits(t *testing.T) {
	a, _, _ := newTestEnv(t)
	c := mkConversation(t, a, 41)
	m, err := a.store.AppendMessage(&Message{ConversationID: c.ID, Role: "agent", Content: "report"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := a.store.UpdateMessageComponents(m.ID, []Component{{App: appName, Name: "report-card", Props: map[string]any{"title": "edited"}}})
	if err != nil {
		t.Fatal(err)
	}
	if m.Revision <= 0 || updated.Revision <= m.Revision {
		t.Fatalf("revision did not advance: %d -> %d", m.Revision, updated.Revision)
	}
}
