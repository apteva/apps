package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// These tests express the desired invariants. Failures reproduce defects in
// the unmodified published v0.18.2 sources copied into this audit directory.
func TestAuditPrivateCardDoesNotReachUnrelatedUser(t *testing.T) {
	a, ctx, _ := newTestEnv(t)
	c := mkConversation(t, a, 41)
	allowed, err := a.store.UserCanAccessConversation(c.ID, testProject, 2)
	if err != nil || allowed {
		t.Fatal("invalid private-conversation fixture", err)
	}
	ch, cancel := a.hub.subscribeUser(testProject + ":2")
	defer cancel()
	_, _, err = a.appendAndDeliver(ctx, c, &Message{ConversationID: c.ID, Role: "agent", AgentID: 41, Content: "private details", ComponentKind: kindAlert, Components: []Component{alertCard("private details", "info")}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-ch:
		t.Fatalf("user 2 cannot access the conversation but received full private message %d: %q", m.ID, m.Content)
	default:
	}
}

func TestAuditInboxPriorityBeforeLimit(t *testing.T) {
	a, _, _ := newTestEnv(t)
	c := mkConversation(t, a, 41)
	approval, err := a.store.AppendMessage(&Message{ConversationID: c.ID, Role: "agent", ComponentKind: kindApproval, Components: []Component{approvalCard("Critical decision", "Approve?", defaultApprovalActions())}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if _, err := a.store.AppendMessage(&Message{ConversationID: c.ID, Role: "agent", ComponentKind: kindReport, Components: []Component{reportCard("Routine", "FYI", "", nil)}}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := a.store.Inbox(testProject, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Message.ID == approval.ID {
			return
		}
	}
	t.Fatalf("pending approval %d is absent behind %d newer routine reports", approval.ID, len(items))
}

func TestAuditTelegramNewPreservesOwnerAndDirective(t *testing.T) {
	a, _, p := newTestEnv(t)
	enableTelegramForTest(t, a, p)
	c := mkConversation(t, a, 41)
	c, err := a.store.UpdateConversationDirective(c.ID, "Keep customer replies concise")
	if err != nil {
		t.Fatal(err)
	}
	b := bindTelegramForTest(t, a, c, "123", []int64{123})
	next, err := a.rotateTelegramConversation(b, telegramMessage{Chat: telegramChat{ID: 123, Type: "private"}, From: telegramUser{ID: 123}})
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := a.store.UserCanAccessConversation(next.ID, testProject, 2)
	if err != nil {
		t.Fatal(err)
	}
	if next.OwnerUserID != c.OwnerUserID || allowed || next.Directive != c.Directive {
		t.Fatalf("/new changed owner %d -> %d, unrelated-user access=%v, directive=%q", c.OwnerUserID, next.OwnerUserID, allowed, next.Directive)
	}
}

func TestAuditTelegramClaimSurvivesCrashBeforeMessage(t *testing.T) {
	a, _, p := newTestEnv(t)
	cfg := enableTelegramForTest(t, a, p)
	c := mkConversation(t, a, 41)
	bindTelegramForTest(t, a, c, "123", []int64{123})
	// Crash immediately after the persisted claim: no message has been stored.
	claimed, err := a.store.ClaimTelegramUpdate(cfg.ConnectionID, 501)
	if err != nil || !claimed {
		t.Fatal(err)
	}
	r := postTelegramUpdate(a, cfg, cfg.WebhookSecret, `{"update_id":501,"message":{"message_id":11,"from":{"id":123},"chat":{"id":123,"type":"private"},"text":"must survive restart"}}`)
	rows, err := a.store.Transcript(c.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("retry acknowledged with %s but transcript contains %d messages", r.Body.String(), len(rows))
	}
}

func TestAuditExclusiveTelegramDelivery(t *testing.T) {
	a, ctx, p := newTestEnv(t)
	enableTelegramForTest(t, a, p)
	c := mkConversation(t, a, 41)
	b := bindTelegramForTest(t, a, c, "123", []int64{123})
	target := "telegram:" + b.ID
	m, _, err := a.store.AppendMessageWithDeliveries(&Message{ConversationID: c.ID, Role: "agent", Content: "Only once"}, []string{target})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan struct{}, 2)
	var sends atomic.Int64
	p.integrationHandler = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool != "send_message" {
			t.Errorf("unexpected tool %s", tool)
			return telegramResult(`{"ok":true,"result":true}`), nil
		}
		id := sends.Add(1)
		entered <- struct{}{}
		<-release
		return telegramResult(fmt.Sprintf(`{"ok":true,"result":{"message_id":%d}}`, 700+id)), nil
	}
	go func() { a.attemptDelivery(ctx, target, c, m); done <- struct{}{} }()
	<-entered
	// The retry worker sees the initial dispatch's still-pending ledger row.
	go func() { _, _ = a.redeliverPending(ctx); done <- struct{}{} }()
	select {
	case <-entered:
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-done
	<-done
	if sends.Load() != 1 {
		t.Fatalf("one ledger row generated %d concurrent Telegram send_message calls", sends.Load())
	}
}

func TestAuditArchivedDeliveryDoesNotStayDueForever(t *testing.T) {
	a, ctx, _ := newTestEnv(t)
	c := mkConversation(t, a, 41)
	_, _, err := a.store.AppendMessageWithDeliveries(&Message{ConversationID: c.ID, Role: "user", Content: "pending"}, []string{"agent-inbound:41"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.SetConversationArchived(c.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = a.redeliverPending(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err := a.store.PendingDeliveries(500)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) > 0 {
		t.Fatalf("archived conversation retains immediately due delivery with attempts=%d", pending[0].Attempts)
	}
}

func TestAuditCreateKeyCannotMutateAnotherOwnersConversation(t *testing.T) {
	a, _, _ := newTestEnv(t)
	c, err := a.store.CreateConversation(CreateConversationInput{ProjectID: testProject, LeadAgentID: 41, OwnerUserID: 1, Title: "Private", ConversationKey: "private-key"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/chats", strings.NewReader(`{"agent_ids":[41,42],"conversation_key":"private-key"}`))
	authorizeTestRequest(req)
	req.Header.Set("X-User-ID", "2")
	rr := httptest.NewRecorder()
	a.handleChats(rr, req)
	added, err := a.store.IsParticipantAgent(c.ID, 42)
	if err != nil {
		t.Fatal(err)
	}
	if rr.Code == 200 || added {
		t.Fatalf("user 2 received status=%d and added agent 42=%v to user 1's private conversation: %s", rr.Code, added, rr.Body.String())
	}
}

func TestAuditOtherBotCommandIsNotAddressedToThisBot(t *testing.T) {
	cfg := &TelegramConnectionConfig{BotUsername: "our_bot"}
	incoming := telegramMessage{Chat: telegramChat{ID: -123, Type: "supergroup"}, Text: "/new@other_bot"}
	if telegramMessageAddressesBot(cfg, incoming) {
		t.Fatal("command explicitly addressed to other_bot passes our_bot's mention gate")
	}
}

func TestAuditStreamingRespectsConversationAndAgentBinding(t *testing.T) {
	a, _, _ := newTestEnv(t)
	c := mkConversation(t, a, 41)
	ch, cancel := a.hub.subscribeFrames(c.ID)
	defer cancel()
	payload, _ := json.Marshal(map[string]any{"tool": "conversations_send", "id": "call1", "chunk": `{"conversation_id":"different-private-conversation","text":"wrong destination content"}`})
	a.streamer.Ingest("llm.tool_chunk", 999, conversationThreadID(c.ID), string(payload), time.Now())
	select {
	case f := <-ch:
		t.Fatalf("unbound agent 999 leaked a frame into %s: %q", f.ConversationID, f.Text)
	default:
	}
}

func TestAuditArchivedConversationIsReadOnly(t *testing.T) {
	a, _, p := newTestEnv(t)
	c := mkConversation(t, a, 41)
	if _, err := a.store.SetConversationArchived(c.ID, true); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/messages?chat_id="+c.ID, strings.NewReader(`{"content":"run despite archive"}`))
	authorizeTestRequest(req)
	rr := httptest.NewRecorder()
	a.handleMessages(rr, req)
	if rr.Code == 200 || len(p.ensures) > 0 {
		t.Fatalf("archived chat accepted message: status=%d, agent ensure calls=%d", rr.Code, len(p.ensures))
	}
}

// Characterization only: inbox_post does not currently advertise a retry key.
func TestAuditInboxPostRetriesReuseIdentity(t *testing.T) {
	a, ctx, _ := newTestEnv(t)
	args := map[string]any{"kind": "approval", "title": "Same request", "callback_tool": "resolve", "client_message_id": "retry-1"}
	first, err := a.toolInboxPost(appCallerCtx("tasks"), ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.toolInboxPost(appCallerCtx("tasks"), ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if first.(map[string]any)["message_id"] != second.(map[string]any)["message_id"] {
		t.Fatalf("retry duplicated: %v %v", first, second)
	}
	args["title"] = "Changed intent"
	if _, err := a.toolInboxPost(appCallerCtx("tasks"), ctx, args); err == nil {
		t.Fatal("changed payload accepted under existing request key")
	}
}

type auditThreadFailurePlatform struct{ *recordingPlatform }

func (p *auditThreadFailurePlatform) SendTrackedAgentEvent(sdk.AgentEventRequest) (*sdk.AgentEventReceipt, error) {
	return nil, fmt.Errorf("temporary original destination failure")
}

func (p *auditThreadFailurePlatform) SendThreadEvent(sdk.ThreadRef, any) error {
	return fmt.Errorf("temporary thread endpoint failure")
}

func (p *auditThreadFailurePlatform) SpawnThread(sdk.ThreadSpawnRequest) (*sdk.ThreadSpawnResult, error) {
	return nil, fmt.Errorf("temporary thread endpoint failure")
}

func TestAuditApprovalNeverFallsBackToDifferentThread(t *testing.T) {
	p := &auditThreadFailurePlatform{&recordingPlatform{}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProject), tk.WithPlatform(p))
	target := "41:" + base64.RawURLEncoding.EncodeToString([]byte("chat-origin"))
	m := &Message{ID: 1, ActionStatus: "approve", ComponentKind: kindApproval}
	err := (&agentAdapter{}).Deliver(ctx, target, nil, m)
	if err == nil || len(p.events) > 0 {
		t.Fatalf("failed origin delivery was treated as success=%v; main-thread events=%d", err == nil, len(p.events))
	}
}

func TestAuditConversationSearchFindsOlderTopic(t *testing.T) {
	a, _, _ := newTestEnv(t)
	wanted := mkConversation(t, a, 41)
	if _, err := a.store.db.Exec(`UPDATE conversations SET title='Important topic',updated_at='2020-01-01 00:00:00' WHERE id=?`, wanted.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 51; i++ {
		mkConversation(t, a, 41)
	}
	out, err := a.toolList(callerCtx(41, "main"), nil, map[string]any{"query": "Important topic"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.(map[string]any)["conversations"].([]Conversation)) != 1 {
		t.Fatal("query failed to find an older matching topic outside the default recent-50 limit")
	}
}

func BenchmarkAuditStreamSizes(b *testing.B) {
	for _, size := range []int{4096, 16384, 65536} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			chunks := []string{}
			raw := `{"text":"` + strings.Repeat("a", size) + `"}`
			for i := 0; i < len(raw); i += 32 {
				end := min(i+32, len(raw))
				v, _ := json.Marshal(map[string]any{"tool": "conversations_send", "id": "1", "chunk": raw[i:end]})
				chunks = append(chunks, string(v))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				s := newStreamer(newHub())
				for _, chunk := range chunks {
					s.Ingest("llm.tool_chunk", 41, "chat-conv-benchmark", chunk, time.Time{})
				}
			}
		})
	}
}

func TestAuditArchivedFullBatchTerminates(t *testing.T) {
	a, ctx, _ := newTestEnv(t)
	c := mkConversation(t, a, 41)
	for i := 0; i < 500; i++ {
		if _, _, err := a.store.AppendMessageWithDeliveries(&Message{ConversationID: c.ID, Role: "system", Content: "pending"}, []string{"web:conv"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.store.SetConversationArchived(c.ID, true); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := a.redeliverPending(ctx); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(150 * time.Millisecond):
		// Release the hot loop by making its 500 rows processable again.
		if _, err := a.store.SetConversationArchived(c.ID, false); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("cleanup did not drain worker")
		}
		t.Fatal("500 archived pending deliveries keep redeliverPending looping until conversation is unarchived")
	}
}
