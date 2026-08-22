package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// recordingPlatform captures everything the app pushes at the platform:
// agent events (the approval round-trip), thread events, and sibling-app
// callbacks (inbox_post's callback_tool).
type recordingPlatform struct {
	tk.BasePlatformClient
	events                []capturedEvent
	threadEvents          []capturedThreadEvent
	appCalls              []capturedAppCall
	spawns                []sdk.ThreadSpawnRequest
	ensures               []sdk.ThreadEnsureRequest
	identity              *sdk.InstallIdentity
	connections           map[int64]*sdk.PlatformConnection
	integrationMu         sync.Mutex
	integrationCalls      []capturedIntegrationCall
	integrationHandler    func(int64, string, map[string]any) (*sdk.ExecuteResult, error)
	failSend              bool
	failSpawn             bool
	duplicateSpawnReceipt bool
	omitSpawnReceipt      bool
}

type capturedEvent struct {
	AgentID int64
	Message string
}

type capturedThreadEvent struct {
	Ref     sdk.ThreadRef
	Message string
}

type capturedAppCall struct {
	App   string
	Tool  string
	Input map[string]any
}

type capturedIntegrationCall struct {
	ConnectionID int64
	Tool         string
	Input        map[string]any
}

func (p *recordingPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	if p.identity == nil {
		return &sdk.InstallIdentity{AppName: appName, Version: "test", ProjectID: testProject, Bindings: map[string]any{}}, nil
	}
	return p.identity, nil
}

func (p *recordingPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	if conn := p.connections[id]; conn != nil {
		copy := *conn
		return &copy, nil
	}
	return nil, fmt.Errorf("connection %d not found", id)
}

func (p *recordingPlatform) ExecuteIntegrationTool(connectionID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.integrationMu.Lock()
	p.integrationCalls = append(p.integrationCalls, capturedIntegrationCall{ConnectionID: connectionID, Tool: tool, Input: input})
	p.integrationMu.Unlock()
	if p.integrationHandler != nil {
		return p.integrationHandler(connectionID, tool, input)
	}
	return nil, fmt.Errorf("integration tool %s unavailable", tool)
}

func (p *recordingPlatform) capturedIntegrationCalls() []capturedIntegrationCall {
	p.integrationMu.Lock()
	defer p.integrationMu.Unlock()
	return append([]capturedIntegrationCall(nil), p.integrationCalls...)
}

func (p *recordingPlatform) SendEvent(instanceID int64, message string) error {
	if p.failSend {
		return fmt.Errorf("agent %d offline", instanceID)
	}
	p.events = append(p.events, capturedEvent{AgentID: instanceID, Message: message})
	return nil
}

func (p *recordingPlatform) SendThreadEvent(target sdk.ThreadRef, message any) error {
	text, _ := message.(string)
	p.threadEvents = append(p.threadEvents, capturedThreadEvent{Ref: target, Message: text})
	return nil
}

func (p *recordingPlatform) SpawnThread(req sdk.ThreadSpawnRequest) (*sdk.ThreadSpawnResult, error) {
	if p.failSpawn {
		return nil, fmt.Errorf("agent %d not running", req.AgentID)
	}
	p.spawns = append(p.spawns, req)
	result := &sdk.ThreadSpawnResult{Status: "created",
		Thread: sdk.ThreadRef{AgentID: req.AgentID, ThreadID: req.ThreadID}}
	if !p.omitSpawnReceipt {
		for _, event := range req.Events {
			if p.duplicateSpawnReceipt {
				result.Events.Duplicates = append(result.Events.Duplicates, event.ID)
			} else {
				result.Events.Accepted = append(result.Events.Accepted, event.ID)
			}
		}
	}
	return result, nil
}

func (p *recordingPlatform) EnsureThread(req sdk.ThreadEnsureRequest) (*sdk.ThreadEnsureResult, error) {
	p.ensures = append(p.ensures, req)
	result := &sdk.ThreadEnsureResult{Status: "reconciled", Reconciled: true,
		Thread: sdk.ThreadRef{AgentID: req.AgentID, ThreadID: req.ThreadID}, ProfileHash: req.ProfileHash}
	for _, event := range req.Events {
		result.Events.Accepted = append(result.Events.Accepted, event.ID)
	}
	return result, nil
}

func (p *recordingPlatform) KillThread(agentID int64, threadID string) error { return nil }

func (p *recordingPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	p.appCalls = append(p.appCalls, capturedAppCall{App: app, Tool: tool, Input: input})
	return nil
}

const testProject = "proj-1"

func newTestEnv(t *testing.T) (*App, *sdk.AppCtx, *recordingPlatform) {
	t.Helper()
	platform := &recordingPlatform{}
	spawnedThreads = sync.Map{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProject), tk.WithPlatform(platform))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatalf("OnMount: %v", err)
	}
	t.Cleanup(func() { _ = app.OnUnmount(ctx) })
	return app, ctx, platform
}

func callerCtx(agentID int64, threadID string) context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{
		AgentID: agentID, ThreadID: threadID, ProjectID: testProject,
	})
}

func callerCtxCall(agentID int64, threadID, toolCallID string) context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{
		AgentID: agentID, ThreadID: threadID, ToolCallID: toolCallID, ProjectID: testProject,
	})
}

func appCallerCtx(appName string) context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{
		AppInstallID: 77, AppName: appName, ProjectID: testProject,
	})
}

func authorizeTestRequest(req *http.Request) {
	req.Header.Set("X-User-ID", "1")
	req.Header.Set("X-Apteva-Project-ID", testProject)
}

func mkConversation(t *testing.T, app *App, agentID int64) *Conversation {
	t.Helper()
	conv, err := app.store.CreateConversation(CreateConversationInput{
		ProjectID: testProject, LeadAgentID: agentID, Title: "Test", OwnerUserID: 1,
	})
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	return conv
}

func boundConversationCaller(t *testing.T, app *App, conv *Conversation, agentID int64) context.Context {
	t.Helper()
	threadID := conversationThreadID(conv.ID)
	if err := app.store.RecordAgentThread(conv.ID, agentID, threadID, "test", "test", ""); err != nil {
		t.Fatalf("bind conversation thread: %v", err)
	}
	if agentID == conv.LeadAgentID {
		if err := app.store.SetConversationThread(conv.ID, threadID); err != nil {
			t.Fatalf("record lead conversation thread: %v", err)
		}
		conv.ThreadID = threadID
	}
	return callerCtx(agentID, threadID)
}

// ─── identity & participation ────────────────────────────────────────

func TestToolsFailClosedWithoutCaller(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)

	// The SDK treats a nil caller as full access for back-compat; a
	// conversation store must not — a toolless context writes nothing.
	_, err := app.toolSend(context.Background(), ctx, map[string]any{
		"conversation_id": conv.ID, "text": "hello",
	})
	if err == nil {
		t.Fatal("expected caller identity to be required")
	}
	_, err = app.toolAlert(callerCtx(41, ""), ctx, map[string]any{
		"conversation_id": conv.ID, "text": "missing thread",
	})
	if err == nil || !strings.Contains(err.Error(), "thread context") {
		t.Fatalf("missing thread context: err=%v", err)
	}
}

func TestNonParticipantCannotWrite(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)

	if _, err := app.toolSend(callerCtx(99, "worker-intruder"), ctx, map[string]any{
		"conversation_id": conv.ID, "text": "intruding",
	}); err == nil {
		t.Fatal("expected participant check to refuse agent 99")
	}
}

func TestBoundConversationThreadCanOperateOnlyOnItsConversation(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	from := boundConversationCaller(t, app, conv, 41)

	if _, err := app.toolSend(from, ctx, map[string]any{
		"conversation_id": conv.ID, "text": "hello",
	}); err != nil {
		t.Fatalf("send to own conversation: %v", err)
	}
	if _, err := app.toolAlert(from, ctx, map[string]any{
		"conversation_id": conv.ID, "text": "urgent local issue", "severity": "warn",
	}); err != nil {
		t.Fatalf("alert in own conversation: %v", err)
	}
	if _, err := app.toolRequestApproval(from, ctx, map[string]any{
		"conversation_id": conv.ID, "title": "Continue?",
	}); err != nil {
		t.Fatalf("approval in own conversation: %v", err)
	}
	if _, err := app.toolHistory(from, ctx, map[string]any{"conversation_id": conv.ID}); err != nil {
		t.Fatalf("history for own conversation: %v", err)
	}

	for tool, call := range map[string]func() (any, error){
		"create": func() (any, error) {
			return app.toolCreate(from, ctx, map[string]any{"title": "Wrong place"})
		},
		"list": func() (any, error) {
			return app.toolList(from, ctx, nil)
		},
		"report": func() (any, error) {
			return app.toolReport(from, ctx, map[string]any{
				"conversation_id": conv.ID, "title": "Global", "summary": "Wrong owner"})
		},
	} {
		if _, err := call(); err == nil || !strings.Contains(err.Error(), "parent/main") {
			t.Fatalf("bound conversation %s: err=%v, want parent/main guidance", tool, err)
		}
	}
}

func TestBoundConversationThreadCannotCrossConversation(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	owned := mkConversation(t, app, 41)
	target := mkConversation(t, app, 41)
	from := boundConversationCaller(t, app, owned, 41)

	calls := map[string]func() (any, error){
		"send": func() (any, error) {
			return app.toolSend(from, ctx, map[string]any{"conversation_id": target.ID, "text": "escape"})
		},
		"alert": func() (any, error) {
			return app.toolAlert(from, ctx, map[string]any{
				"conversation_id": target.ID, "text": "escape", "severity": "error"})
		},
		"approval": func() (any, error) {
			return app.toolRequestApproval(from, ctx, map[string]any{
				"conversation_id": target.ID, "title": "Escape?"})
		},
		"history": func() (any, error) {
			return app.toolHistory(from, ctx, map[string]any{"conversation_id": target.ID})
		},
	}
	for tool, call := range calls {
		if _, err := call(); err == nil || !strings.Contains(err.Error(), "belongs to conversation "+owned.ID) {
			t.Fatalf("cross-conversation %s: err=%v", tool, err)
		}
	}
	transcript, err := app.store.Transcript(target.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript) != 0 {
		t.Fatalf("cross-conversation calls wrote target transcript: %+v", transcript)
	}
	items, err := app.store.Inbox(testProject, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("cross-conversation calls wrote inbox effects: %+v", items)
	}
}

func TestOpaqueUnboundThreadIsNotAssignedAPlatformRole(t *testing.T) {
	app, _, _ := newTestEnv(t)
	from, err := requireAgentCaller(callerCtx(41, "opaque-child-17"))
	if err != nil {
		t.Fatal(err)
	}
	bound, err := app.boundConversation(from)
	if err != nil {
		t.Fatal(err)
	}
	if bound != nil {
		t.Fatalf("opaque unbound thread was classified as conversation %+v", bound)
	}
}

// ─── messages & idempotency ──────────────────────────────────────────

func TestIdempotentUserMessages(t *testing.T) {
	app, _, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)

	first, inserted, err := app.store.AppendMessageIdempotent(&Message{
		ConversationID: conv.ID, Role: "user", Content: "hi", UserID: 1, ClientID: "c-1",
	})
	if err != nil || !inserted {
		t.Fatalf("first append: inserted=%v err=%v", inserted, err)
	}
	second, inserted, err := app.store.AppendMessageIdempotent(&Message{
		ConversationID: conv.ID, Role: "user", Content: "hi", UserID: 1, ClientID: "c-1",
	})
	if err != nil {
		t.Fatalf("retry append: %v", err)
	}
	// A retried send (network blip, remount) must resolve to the same
	// row, never a duplicate bubble.
	if inserted || second.ID != first.ID {
		t.Fatalf("retry produced a new row: first=%d second=%d inserted=%v", first.ID, second.ID, inserted)
	}
}

// ─── inbox semantics ─────────────────────────────────────────────────

func TestOneConversationSystemOwnsChatAlertsReportsAndApprovals(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	from := callerCtx(41, "thread-9")

	if _, err := app.toolSend(from, ctx, map[string]any{
		"conversation_id": conv.ID, "text": "ordinary reply",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolAlert(from, ctx, map[string]any{
		"conversation_id": conv.ID, "text": "disk nearly full", "severity": "warn",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolReport(from, ctx, map[string]any{
		"conversation_id": conv.ID, "title": "Weekly", "summary": "Everything is healthy",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolRequestApproval(from, ctx, map[string]any{
		"conversation_id": conv.ID, "title": "Delete the oldest archive?",
	}); err != nil {
		t.Fatal(err)
	}

	transcript, err := app.store.Transcript(conv.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript) != 4 {
		t.Fatalf("transcript has %d messages, want ordinary+alert+report+approval", len(transcript))
	}
	transcriptKinds := map[string]int{}
	transcriptIDs := map[int64]bool{}
	for _, message := range transcript {
		if message.ConversationID != conv.ID {
			t.Fatalf("message %d escaped conversation %s into %s", message.ID, conv.ID, message.ConversationID)
		}
		transcriptKinds[message.ComponentKind]++
		transcriptIDs[message.ID] = true
	}
	for kind, want := range map[string]int{"": 1, kindAlert: 1, kindReport: 1, kindApproval: 1} {
		if transcriptKinds[kind] != want {
			t.Fatalf("transcript kinds=%v", transcriptKinds)
		}
	}

	items, err := app.store.Inbox(testProject, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("inbox has %d items, want alert+report+approval projection", len(items))
	}
	inboxKinds := map[string]int{}
	for _, item := range items {
		if item.Message.ConversationID != conv.ID || !transcriptIDs[item.Message.ID] {
			t.Fatalf("inbox item is not a projection of the same transcript: %+v", item.Message)
		}
		if item.Message.ComponentKind == "" {
			t.Fatal("ordinary reply leaked into inbox")
		}
		inboxKinds[item.Message.ComponentKind]++
	}
	for _, kind := range []string{kindAlert, kindReport, kindApproval} {
		if inboxKinds[kind] != 1 {
			t.Fatalf("inbox kinds=%v", inboxKinds)
		}
	}
}

func TestReportsAppearInTranscriptAndInbox(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)

	if _, err := app.toolReport(callerCtx(41, "main"), ctx, map[string]any{
		"conversation_id": conv.ID, "title": "Weekly", "summary": "All good",
	}); err != nil {
		t.Fatalf("report: %v", err)
	}
	if _, err := app.toolSend(boundConversationCaller(t, app, conv, 41), ctx, map[string]any{
		"conversation_id": conv.ID, "text": "plain message",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}

	// 0.5.1: a report is visible in the conversation it lives in — a
	// "Reports" conversation hiding its own reports reads as broken.
	transcript, err := app.store.Transcript(conv.ID, 0, 50)
	if err != nil {
		t.Fatalf("transcript: %v", err)
	}
	reportInTranscript := false
	for _, m := range transcript {
		if m.ComponentKind == kindReport {
			reportInTranscript = true
		}
	}
	if !reportInTranscript {
		t.Fatal("report missing from its conversation's transcript")
	}

	items, err := app.store.Inbox(testProject, 1, 50)
	if err != nil {
		t.Fatalf("inbox: %v", err)
	}
	foundReport := false
	for _, item := range items {
		if item.Message.ComponentKind == kindReport {
			foundReport = true
		}
		if item.Message.ComponentKind == "" {
			t.Fatal("plain message leaked into the inbox")
		}
	}
	if !foundReport {
		t.Fatal("report missing from inbox")
	}
}

func TestInboxPriorityOrdering(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	from := callerCtx(41, "main")

	// Deliberately created in reverse-priority order: the newest row is
	// the lowest priority, so ordering by recency alone would fail.
	if _, err := app.toolAlert(from, ctx, map[string]any{
		"conversation_id": conv.ID, "text": "disk almost full", "severity": "warn",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolAlert(from, ctx, map[string]any{
		"conversation_id": conv.ID, "text": "provider down", "severity": "error",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolRequestApproval(from, ctx, map[string]any{
		"conversation_id": conv.ID, "title": "Spend $50 on ads?",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolReport(from, ctx, map[string]any{
		"conversation_id": conv.ID, "title": "Weekly", "summary": "fine",
	}); err != nil {
		t.Fatal(err)
	}

	items, err := app.store.Inbox(testProject, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, item := range items {
		kinds = append(kinds, item.Message.ComponentKind+"/"+item.Message.Severity)
	}
	want := []string{"approval/", "alert/error", "alert/warn", "report/"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Fatalf("inbox order = %v, want %v", kinds, want)
	}
}

// ─── the approval round-trip ─────────────────────────────────────────

func TestApprovalRoundTripForwardsToAgentThread(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	conv := mkConversation(t, app, 41)

	out, err := app.toolRequestApproval(callerCtx(41, "thread-9"), ctx, map[string]any{
		"conversation_id": conv.ID, "title": "Send the campaign?",
	})
	if err != nil {
		t.Fatalf("request approval: %v", err)
	}
	messageID := out.(map[string]any)["message_id"].(int64)

	msg, err := app.store.GetMessage(messageID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := app.resolveApproval(ctx, msg, "approve", "go ahead", 1)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cardStatus(updated) != "approve" {
		t.Fatalf("card status = %q, want approve", cardStatus(updated))
	}
	// The verdict must land on the thread that asked.
	if len(platform.threadEvents) != 1 {
		t.Fatalf("thread events = %d, want 1", len(platform.threadEvents))
	}
	ev := platform.threadEvents[0]
	if ev.Ref.AgentID != 41 || ev.Ref.ThreadID != "thread-9" {
		t.Fatalf("forwarded to %+v, want agent 41 thread-9", ev.Ref)
	}
	if !strings.Contains(ev.Message, "action=approve") || !strings.Contains(ev.Message, "go ahead") {
		t.Fatalf("event payload missing verdict/note: %q", ev.Message)
	}

	// Second action on a resolved card must be refused, not re-forwarded.
	if _, err := app.resolveApproval(ctx, updated, "deny", "", 1); err == nil {
		t.Fatal("expected already-resolved refusal")
	}
	// Resolved approvals leave the pending inbox view.
	items, _ := app.store.Inbox(testProject, 1, 50)
	for _, item := range items {
		if item.Message.ID == messageID {
			t.Fatal("resolved approval still listed in inbox")
		}
	}
}

// ─── sibling apps (inbox_post) ───────────────────────────────────────

func TestInboxPostCallbackReachesPostingApp(t *testing.T) {
	app, ctx, platform := newTestEnv(t)

	out, err := app.toolInboxPost(appCallerCtx("certs"), ctx, map[string]any{
		"kind": "approval", "title": "Renew certificate?",
		"source_app": "certs", "callback_tool": "certs_renew_decision",
	})
	if err != nil {
		t.Fatalf("inbox_post: %v", err)
	}
	messageID := out.(map[string]any)["message_id"].(int64)

	msg, err := app.store.GetMessage(messageID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.resolveApproval(ctx, msg, "approve", "", 1); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// The verdict goes back into the POSTING app, not an agent thread.
	if len(platform.appCalls) != 1 {
		t.Fatalf("app calls = %d, want 1", len(platform.appCalls))
	}
	call := platform.appCalls[0]
	if call.App != "certs" || call.Tool != "certs_renew_decision" {
		t.Fatalf("callback went to %s/%s", call.App, call.Tool)
	}
	if call.Input["action_id"] != "approve" {
		t.Fatalf("callback action = %v", call.Input["action_id"])
	}
	if len(platform.events) != 0 {
		t.Fatal("inbox_post approval must not forward to an agent")
	}

	// Repeated posts from the same app group into one system conversation.
	again, err := app.toolInboxPost(appCallerCtx("certs"), ctx, map[string]any{
		"kind": "alert", "title": "Cert expiring", "source_app": "certs", "severity": "warn",
	})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := app.store.GetMessage(messageID)
	second, _ := app.store.GetMessage(again.(map[string]any)["message_id"].(int64))
	if first.ConversationID != second.ConversationID {
		t.Fatal("inbox_post items from one app should share the system conversation")
	}
}

// ─── delivery ledger ─────────────────────────────────────────────────

func TestUnknownDeliveryTargetDeadLettersAndCanBeRequeued(t *testing.T) {
	app, _, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	msg, err := app.store.AppendMessage(&Message{ConversationID: conv.ID, Role: "agent", Content: "test"})
	if err != nil {
		t.Fatal(err)
	}
	const target = "future-transport:recipient"
	if err := app.store.EnsureDelivery(msg.ID, target); err != nil {
		t.Fatal(err)
	}
	app.attemptDelivery(nil, target, conv, msg)
	delivery, err := app.store.DeliveryFor(msg.ID, target)
	if err != nil || delivery.Status != "failed" {
		t.Fatalf("delivery = %+v err=%v, want failed", delivery, err)
	}
	if err := app.store.RetryFailedDelivery(testProject, delivery.ID); err != nil {
		t.Fatal(err)
	}
	delivery, err = app.store.DeliveryFor(msg.ID, target)
	if err != nil || delivery.Status != "pending" || delivery.Attempts != 0 {
		t.Fatalf("requeued delivery = %+v err=%v, want pending attempts=0", delivery, err)
	}
}

// ─── HTTP surface ────────────────────────────────────────────────────

func TestUserMessageForwardFailureIsDurablyQueued(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	platform.failSend = true
	platform.failSpawn = true
	mountedCtx = ctx
	conv := mkConversation(t, app, 41)

	req := httptest.NewRequest("POST", "/messages?chat_id="+conv.ID,
		strings.NewReader(`{"content":"are you there?"}`))
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleMessages(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	// The user's row persists once. Failure state belongs in the delivery
	// ledger rather than being duplicated into the human transcript.
	transcript, _ := app.store.Transcript(conv.ID, 0, 10)
	if len(transcript) != 1 {
		t.Fatalf("transcript rows = %d, want one user message", len(transcript))
	}
	delivery, err := app.store.DeliveryFor(transcript[0].ID, "agent-inbound:41")
	if err != nil || delivery.Status != "pending" || delivery.Attempts != 1 {
		t.Fatalf("inbound delivery = %+v err=%v, want pending attempt", delivery, err)
	}
	state, err := app.store.AgentThread(conv.ID, 41)
	if err != nil || state.ThreadID != conversationThreadID(conv.ID) || state.LastError == "" {
		t.Fatalf("failed spawn did not retain safe binding: state=%+v err=%v", state, err)
	}
}

// ─── thread-per-conversation (channel-chat parity) ───────────────────

func postUserMessage(t *testing.T, app *App, conv *Conversation, content string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/messages?chat_id="+conv.ID,
		strings.NewReader(`{"content":"`+content+`"}`))
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleMessages(rec, req)
	if rec.Code != 200 {
		t.Fatalf("post status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFirstMessageSpawnsConversationThread(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	conv := mkConversation(t, app, 41)

	postUserMessage(t, app, conv, "hello")

	if len(platform.spawns) != 1 {
		t.Fatalf("spawns = %d, want 1", len(platform.spawns))
	}
	spawn := platform.spawns[0]
	// channel-chat's naming convention, so thread identities survive the
	// eventual data migration.
	if spawn.ThreadID != "chat-"+conv.ID || spawn.AgentID != 41 {
		t.Fatalf("spawned %s on agent %d, want chat-%s on 41", spawn.ThreadID, spawn.AgentID, conv.ID)
	}
	// The suffix must carry the reply contract and the conversation id;
	// composition with main's directive happens core-side.
	if !strings.Contains(spawn.DirectiveSuffix, conv.ID) ||
		!strings.Contains(spawn.DirectiveSuffix, "conversations_send") {
		t.Fatalf("directive suffix missing reply contract: %q", spawn.DirectiveSuffix)
	}
	// MCP nil → the platform fills the agent's spawnable set server-side.
	if spawn.MCP != nil {
		t.Fatalf("MCP should be nil (platform default), got %v", spawn.MCP)
	}
	if got, want := strings.Join(spawn.Tools, ","), strings.Join(conversationThreadTools, ","); got != want {
		t.Fatalf("conversation tool preload=%q want=%q", got, want)
	}
	for _, forbidden := range []string{"conversations_create", "conversations_list", "conversations_report", "conversations_inbox_post"} {
		if slices.Contains(spawn.Tools, forbidden) {
			t.Fatalf("conversation preload contains global/app-only tool %q: %v", forbidden, spawn.Tools)
		}
	}
	for _, required := range []string{"bound only to conversation " + conv.ID, "never send, read, approve, or alert against another conversation id", "do not grant the child Conversations tools"} {
		if !strings.Contains(spawn.DirectiveSuffix, required) {
			t.Fatalf("conversation directive missing %q: %s", required, spawn.DirectiveSuffix)
		}
	}

	// The initial event was included in the atomic spawn, not sent in a
	// second request and not sent to main.
	if len(platform.events) != 0 {
		t.Fatalf("SendEvent(main) called %d times, want 0", len(platform.events))
	}
	if len(platform.threadEvents) != 0 {
		t.Fatalf("thread events = %+v, want none after atomic spawn", platform.threadEvents)
	}
	if len(spawn.Events) != 1 {
		t.Fatalf("spawn events = %+v, want one initial event", spawn.Events)
	}
	wantEventID := fmt.Sprintf("conversation:%s:message:1:agent:41", conv.ID)
	if spawn.Events[0].ID != wantEventID || spawn.Events[0].Message != "[chat] hello" {
		t.Fatalf("initial event = %+v, want id=%q message=%q", spawn.Events[0], wantEventID, "[chat] hello")
	}

	// The thread id persists on the conversation row.
	stored, err := app.store.GetConversation(conv.ID)
	if err != nil || stored.ThreadID != "chat-"+conv.ID {
		t.Fatalf("stored thread = %q err=%v, want chat-%s", stored.ThreadID, err, conv.ID)
	}
}

func TestEveryMessageUsesIdempotentSpawnEvent(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	conv := mkConversation(t, app, 41)

	postUserMessage(t, app, conv, "one")
	postUserMessage(t, app, conv, "two")

	// Every inbound turn uses SpawnThread.Events with a stable id. This gives
	// later messages the same accepted-or-duplicate receipt as the first.
	if len(platform.spawns) != 2 {
		t.Fatalf("spawns = %d, want 2", len(platform.spawns))
	}
	if len(platform.spawns[1].Events) != 1 || platform.spawns[1].Events[0].Message != "[chat] two" {
		t.Fatalf("second spawn events = %+v", platform.spawns[1].Events)
	}
	if len(platform.threadEvents) != 0 {
		t.Fatalf("non-idempotent thread events = %+v, want none", platform.threadEvents)
	}
}

func TestInboundDeliveryRecordsPerAgentThreadState(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	conv := mkConversation(t, app, 41)
	if err := app.store.AddAgentParticipant(conv.ID, 43); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/messages?chat_id="+conv.ID,
		strings.NewReader(`{"content":"@all check this"}`))
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleMessages(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(platform.spawns) != 2 {
		t.Fatalf("spawns=%d want 2", len(platform.spawns))
	}
	for _, agentID := range []int64{41, 43} {
		state, err := app.store.AgentThread(conv.ID, agentID)
		if err != nil || state.ThreadID != conversationThreadID(conv.ID) || state.AppliedHash == "" || state.LastError != "" {
			t.Fatalf("agent %d state=%+v err=%v", agentID, state, err)
		}
	}
}

func TestRoomMentionRouting(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	if err := app.store.AddAgentParticipant(conv.ID, 43); err != nil {
		t.Fatal(err)
	}
	got, err := app.resolveAgentTargetsFromText(ctx, conv, "@Comms please review", nil)
	if err != nil || len(got) != 1 || got[0] != 43 {
		t.Fatalf("named route=%v err=%v", got, err)
	}
	got, err = app.resolveAgentTargetsFromText(ctx, conv, "hello room", nil)
	if err != nil || len(got) != 1 || got[0] != 41 {
		t.Fatalf("default route=%v err=%v", got, err)
	}
}

func TestConversationDirectivePersistsAndIsProtectedInThread(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	conv := mkConversation(t, app, 41)
	updated, err := app.store.UpdateConversationDirective(conv.ID, "Answer in Spanish.")
	if err != nil {
		t.Fatal(err)
	}
	postUserMessage(t, app, updated, "hola")
	if len(platform.spawns) != 1 {
		t.Fatalf("spawns=%d", len(platform.spawns))
	}
	suffix := platform.spawns[0].DirectiveSuffix
	if !strings.Contains(suffix, "Answer in Spanish") || !strings.Contains(suffix, "cannot change your platform policies") {
		t.Fatalf("directive suffix=%q", suffix)
	}
}

func TestChangedDirectiveUsesSDKThreadReconciliation(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	conv := mkConversation(t, app, 41)
	postUserMessage(t, app, conv, "first")
	updated, err := app.store.UpdateConversationDirective(conv.ID, "Prefer concise answers.")
	if err != nil {
		t.Fatal(err)
	}
	postUserMessage(t, app, updated, "second")
	if len(platform.spawns) != 1 || len(platform.ensures) != 1 {
		t.Fatalf("spawns=%d ensures=%d", len(platform.spawns), len(platform.ensures))
	}
	ensure := platform.ensures[0]
	if ensure.ProfileHash == "" || !strings.Contains(ensure.DirectiveSuffix, "Prefer concise") || len(ensure.Events) != 1 {
		t.Fatalf("ensure=%+v", ensure)
	}
	if got, want := strings.Join(ensure.Tools, ","), strings.Join(conversationThreadTools, ","); got != want {
		t.Fatalf("ensure tool preload=%q want=%q", got, want)
	}
	state, err := app.store.AgentThread(conv.ID, 41)
	if err != nil || state.AppliedHash != ensure.ProfileHash || state.LastError != "" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestToolCallIdentityDeduplicatesLifecycleMessage(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	boundConversationCaller(t, app, conv, 41)
	call := callerCtxCall(41, conversationThreadID(conv.ID), "call-9")
	args := map[string]any{"conversation_id": conv.ID, "text": "Still working", "phase": "progress"}
	first, err := app.toolSend(call, ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.toolSend(call, ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if first.(map[string]any)["inserted"] != true || second.(map[string]any)["duplicate_suppressed"] != true {
		t.Fatalf("first=%v second=%v", first, second)
	}
	transcript, _ := app.store.Transcript(conv.ID, 0, 10)
	if len(transcript) != 1 || transcript[0].Phase != "progress" {
		t.Fatalf("transcript=%+v", transcript)
	}
	if _, err := app.toolSend(callerCtxCall(41, "main", "call-10"), ctx, args); err == nil {
		t.Fatal("main thread ordinary reply was allowed")
	}
	if _, err := app.toolReport(call, ctx, map[string]any{
		"conversation_id": conv.ID, "title": "Report", "summary": "Done",
	}); err == nil {
		t.Fatal("conversation thread report was allowed")
	}
}

func TestDuplicateInitialEventReceiptIsSuccess(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	platform.duplicateSpawnReceipt = true
	mountedCtx = ctx
	conv := mkConversation(t, app, 41)

	postUserMessage(t, app, conv, "safe retry")

	if len(platform.spawns) != 1 || len(platform.spawns[0].Events) != 1 {
		t.Fatalf("spawns = %+v, want one atomic event", platform.spawns)
	}
	if len(platform.threadEvents) != 0 || len(platform.events) != 0 {
		t.Fatalf("duplicate receipt resent event: thread=%+v main=%+v", platform.threadEvents, platform.events)
	}
	transcript, _ := app.store.Transcript(conv.ID, 0, 10)
	if len(transcript) != 1 {
		t.Fatalf("transcript rows = %d, want only user message", len(transcript))
	}
}

func TestMissingEventReceiptStaysPendingWithoutDuplicateFallback(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	platform.omitSpawnReceipt = true
	mountedCtx = ctx
	conv := mkConversation(t, app, 41)

	postUserMessage(t, app, conv, "receipt required")

	if len(platform.threadEvents) != 0 || len(platform.events) != 0 {
		t.Fatalf("unacknowledged event was rerouted: thread=%+v main=%+v", platform.threadEvents, platform.events)
	}
	transcript, _ := app.store.Transcript(conv.ID, 0, 10)
	if len(transcript) != 1 {
		t.Fatalf("transcript = %+v, want only user row", transcript)
	}
	delivery, err := app.store.DeliveryFor(transcript[0].ID, "agent-inbound:41")
	if err != nil || delivery.Status != "pending" || !strings.Contains(delivery.LastError, "acknowledge") {
		t.Fatalf("delivery = %+v err=%v, want retryable receipt failure", delivery, err)
	}
}

func TestSpawnFailureNeverFallsBackToMainAndRetryKeepsEventID(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	platform.failSpawn = true
	mountedCtx = ctx
	conv := mkConversation(t, app, 41)

	postUserMessage(t, app, conv, "agent is stopped")

	// Conversation messages must never pollute main.
	if len(platform.events) != 0 {
		t.Fatalf("SendEvent calls = %d, want 0", len(platform.events))
	}
	if len(platform.threadEvents) != 0 {
		t.Fatalf("thread events = %d, want 0", len(platform.threadEvents))
	}
	transcript, _ := app.store.Transcript(conv.ID, 0, 10)
	delivery, err := app.store.DeliveryFor(transcript[0].ID, "agent-inbound:41")
	if err != nil || delivery.Status != "pending" {
		t.Fatalf("queued delivery = %+v err=%v", delivery, err)
	}
	// Retry the same durable row after the agent returns.
	platform.failSpawn = false
	app.attemptDelivery(ctx, "agent-inbound:41", conv, &transcript[0])
	if len(platform.spawns) != 1 || len(platform.spawns[0].Events) != 1 {
		t.Fatalf("recovery spawns=%+v", platform.spawns)
	}
	wantID := conversationThreadEventID(conv.ID, transcript[0].ID, 41)
	if platform.spawns[0].Events[0].ID != wantID {
		t.Fatalf("retry event id=%q want %q", platform.spawns[0].Events[0].ID, wantID)
	}
}

// ─── dismissal, statuses, broadcast ──────────────────────────────────

func TestDismissRemovesFromInboxKeepsHistory(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)

	out, err := app.toolAlert(callerCtx(41, "main"), ctx, map[string]any{
		"conversation_id": conv.ID, "text": "low disk", "severity": "warn",
	})
	if err != nil {
		t.Fatal(err)
	}
	messageID := out.(map[string]any)["message_id"].(int64)

	if _, err := app.DismissMessage(messageID); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	items, _ := app.store.Inbox(testProject, 1, 50)
	for _, item := range items {
		if item.Message.ID == messageID {
			t.Fatal("dismissed alert still in inbox")
		}
	}
	// History keeps the row — dismissal hides, never deletes.
	transcript, _ := app.store.Transcript(conv.ID, 0, 50)
	found := false
	for _, m := range transcript {
		found = found || m.ID == messageID
	}
	if !found {
		t.Fatal("dismissal deleted the message from history")
	}
	// Repeat dismissal is idempotent-ish (already-dismissed card just
	// rewrites the flag); a plain message is refused.
	plain, _ := app.store.AppendMessage(&Message{ConversationID: conv.ID, Role: "user", Content: "hi"})
	if _, err := app.DismissMessage(plain.ID); err == nil {
		t.Fatal("plain message must not be dismissable")
	}
}

func TestPendingApprovalsCannotBeDismissed(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)

	out, err := app.toolRequestApproval(callerCtx(41, "main"), ctx, map[string]any{
		"conversation_id": conv.ID, "title": "Delete everything?",
	})
	if err != nil {
		t.Fatal(err)
	}
	messageID := out.(map[string]any)["message_id"].(int64)

	// Silently hiding a pending question would strand the asking agent.
	if _, err := app.DismissMessage(messageID); err == nil {
		t.Fatal("pending approval must not be dismissable")
	}
	// Resolved ones may be dismissed.
	msg, _ := app.store.GetMessage(messageID)
	if _, err := app.resolveApproval(ctx, msg, "deny", "", 1); err != nil {
		t.Fatal(err)
	}
	resolved, _ := app.store.GetMessage(messageID)
	if _, err := app.DismissMessage(resolved.ID); err != nil {
		t.Fatalf("resolved approval should be dismissable: %v", err)
	}
}

func TestInboxItemsBroadcastToEveryUserScope(t *testing.T) {
	app, ctx, _ := newTestEnv(t)

	// A user who owns nothing, subscribed to their bell stream.
	ch, cancel := app.hub.subscribeUser(testProject + ":7")
	defer cancel()

	// inbox_post creates an ownerless system conversation — the exact
	// shape that used to deliver to "web:user:0" and ping nobody.
	if _, err := app.toolInboxPost(appCallerCtx("backup"), ctx, map[string]any{
		"kind": "alert", "title": "Backup failed", "severity": "error", "source_app": "backup",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-ch:
		if m.ComponentKind != kindAlert {
			t.Fatalf("received %q, want the alert", m.ComponentKind)
		}
	default:
		t.Fatal("inbox item did not reach a non-owner user scope")
	}

	// Plain conversation traffic must NOT broadcast — only the owner's
	// scope and the conversation panel see it.
	conv := mkConversation(t, app, 41) // owner user 1
	if _, err := app.toolSend(boundConversationCaller(t, app, conv, 41), ctx, map[string]any{
		"conversation_id": conv.ID, "text": "private chatter",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-ch:
		t.Fatalf("plain message leaked to another user's bell: %q", m.Content)
	default:
	}
}

// ─── agent system conversations (inbox without an open chat) ─────────

// The 0.5.0 contract: no hidden fallback bucket. Background items
// require an explicit conversation; the agent lists (with query) or
// creates one, and create is title-idempotent so stable titles
// converge instead of sprawling.
func TestCreateListAlertFlow(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	from := callerCtx(41, "main")

	// Omitting the conversation is an error that teaches the flow.
	if _, err := app.toolAlert(from, ctx, map[string]any{
		"text": "disk almost full", "severity": "warn",
	}); err == nil || !strings.Contains(err.Error(), "conversations_create") {
		t.Fatalf("err = %v, want teaching error", err)
	}

	// Create a topical conversation, then alert into it.
	created, err := app.toolCreate(from, ctx, map[string]any{"title": "Infra monitoring"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	convID := created.(map[string]any)["conversation_id"].(string)
	if created.(map[string]any)["created"] != true {
		t.Fatal("first create should report created=true")
	}
	out, err := app.toolAlert(from, ctx, map[string]any{
		"conversation_id": convID, "text": "disk almost full", "severity": "warn",
	})
	if err != nil {
		t.Fatalf("alert: %v", err)
	}
	messageID := out.(map[string]any)["message_id"].(int64)
	items, _ := app.store.Inbox(testProject, 1, 50)
	found := false
	for _, item := range items {
		found = found || item.Message.ID == messageID
	}
	if !found {
		t.Fatal("alert missing from inbox")
	}

	// Same title, different case, different thread: the same
	// conversation comes back — the anti-sprawl guarantee.
	again, err := app.toolCreate(callerCtx(41, "main"), ctx, map[string]any{"title": "infra MONITORING"})
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if again.(map[string]any)["conversation_id"].(string) != convID ||
		again.(map[string]any)["created"] != false {
		t.Fatalf("second create = %+v, want reuse of %s", again, convID)
	}

	// list with query finds it; an unrelated query does not.
	listed, err := app.toolList(from, ctx, map[string]any{"query": "infra"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	matches := listed.(map[string]any)["conversations"].([]Conversation)
	if len(matches) != 1 || matches[0].ID != convID {
		t.Fatalf("query match = %+v, want the monitoring conversation", matches)
	}
	if listed, _ = app.toolList(from, ctx, map[string]any{"query": "zzz-nope"}); len(listed.(map[string]any)["conversations"].([]Conversation)) != 0 {
		t.Fatal("unrelated query must match nothing")
	}
}

// The approval verdict still reaches the raising thread even when the
// approval was born outside any conversation.
func TestBackgroundApprovalRoundTrip(t *testing.T) {
	app, ctx, platform := newTestEnv(t)

	created, err := app.toolCreate(callerCtx(41, "main"), ctx, map[string]any{"title": "Credential rotation"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	out, err := app.toolRequestApproval(callerCtx(41, "main"), ctx, map[string]any{
		"conversation_id": created.(map[string]any)["conversation_id"].(string),
		"title":           "Rotate the credentials?",
	})
	if err != nil {
		t.Fatalf("background approval: %v", err)
	}
	msg, _ := app.store.GetMessage(out.(map[string]any)["message_id"].(int64))
	if _, err := app.resolveApproval(ctx, msg, "approve", "", 1); err != nil {
		t.Fatal(err)
	}
	if len(platform.threadEvents) != 1 || platform.threadEvents[0].Ref.ThreadID != "main" {
		t.Fatalf("verdict routing = %+v, want thread main", platform.threadEvents)
	}
}

// conversations_send keeps requiring a conversation: a chat message
// with no audience is a mis-used alert, not a send.
func TestSendStillRequiresConversation(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	if _, err := app.toolSend(callerCtx(41, "unbound-thread"), ctx, map[string]any{"text": "hello?"}); err == nil {
		t.Fatal("send without conversation_id must fail")
	}
}

// ─── dashboard-parity HTTP surface ───────────────────────────────────

// ListAgents overrides the Base stub so /agents has a directory to
// serve — the shape the panel's pickers consume. Agent 41 has this
// app's MCP bound, 42 does not (the annotation the panel filters on).
func (p *recordingPlatform) ListAgents(projectID string) ([]sdk.PlatformAgent, error) {
	return []sdk.PlatformAgent{
		{ID: 41, Name: "Research", Status: "running", ProjectID: projectID, AttachedToCaller: true},
		{ID: 42, Name: "Ops", Status: "stopped", ProjectID: projectID},
		{ID: 43, Name: "Comms", Status: "running", ProjectID: projectID, AttachedToCaller: true},
		{ID: 55, Name: "Finance", Status: "running", ProjectID: projectID, AttachedToCaller: true},
	}, nil
}

func (p *recordingPlatform) GetAgent(id int64) (*sdk.PlatformAgent, error) {
	agents, _ := p.ListAgents(testProject)
	for i := range agents {
		if agents[i].ID == id {
			return &agents[i], nil
		}
	}
	return nil, fmt.Errorf("agent %d not found", id)
}

func doChats(t *testing.T, app *App, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("{}")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleChats(rec, req)
	return rec
}

func TestCreateRoomConversationViaHTTP(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	mountedCtx = ctx

	rec := doChats(t, app, "POST", "/chats",
		`{"agent_ids":[41,42,43],"lead_agent_id":42,"title":"War room","project_id":"proj-1"}`)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		ID          string `json:"id"`
		Kind        string `json:"kind"`
		LeadAgentID int64  `json:"lead_agent_id"`
		Title       string `json:"title"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Kind != "room" || got.LeadAgentID != 42 || got.Title != "War room" {
		t.Fatalf("created = %+v, want room led by 42", got)
	}
	for _, agentID := range []int64{41, 42, 43} {
		ok, err := app.store.IsParticipantAgent(got.ID, agentID)
		if err != nil || !ok {
			t.Fatalf("agent %d not a participant (err=%v)", agentID, err)
		}
	}

	// The original single-agent shape still works and stays direct.
	rec = doChats(t, app, "POST", "/chats", `{"agent_id":41}`)
	if rec.Code != 200 {
		t.Fatalf("legacy create status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Kind != "direct" {
		t.Fatalf("legacy create kind=%q err=%v, want direct", got.Kind, err)
	}
}

func TestRenameArchiveUnarchiveDeleteViaHTTP(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	mountedCtx = ctx
	conv := mkConversation(t, app, 41)
	msg, err := app.store.AppendMessage(&Message{ConversationID: conv.ID, Role: "user", Content: "keep?"})
	if err != nil {
		t.Fatalf("seed message: %v", err)
	}

	// Rename.
	rec := doChats(t, app, "PATCH", "/chats?id="+conv.ID, `{"title":"Renamed"}`)
	if rec.Code != 200 {
		t.Fatalf("rename status=%d body=%s", rec.Code, rec.Body.String())
	}
	if updated, _ := app.store.GetConversation(conv.ID); updated.Title != "Renamed" {
		t.Fatalf("title=%q, want Renamed", updated.Title)
	}
	// Empty titles are refused, not silently stored.
	if rec = doChats(t, app, "PATCH", "/chats?id="+conv.ID, `{"title":"  "}`); rec.Code != 400 {
		t.Fatalf("blank rename status=%d, want 400", rec.Code)
	}

	// Archive: hidden from the active list and from GetConversation,
	// visible in the archived list.
	if rec = doChats(t, app, "PATCH", "/chats?id="+conv.ID, `{"archived":true}`); rec.Code != 200 {
		t.Fatalf("archive status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := app.store.GetConversation(conv.ID); err == nil {
		t.Fatal("archived conversation still visible to GetConversation")
	}
	if active, _ := app.store.ListConversationsForUser(testProject, 1, 50); len(active) != 0 {
		t.Fatalf("active list has %d rows, want 0", len(active))
	}
	archived, _ := app.store.ListArchivedForUser(testProject, 1, 50)
	if len(archived) != 1 || archived[0].ID != conv.ID {
		t.Fatalf("archived list = %+v, want the conversation", archived)
	}

	// Unarchive restores it.
	if rec = doChats(t, app, "PATCH", "/chats?id="+conv.ID, `{"archived":false}`); rec.Code != 200 {
		t.Fatalf("unarchive status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := app.store.GetConversation(conv.ID); err != nil {
		t.Fatalf("unarchived conversation not visible: %v", err)
	}

	// Delete removes the conversation and its messages.
	if rec = doChats(t, app, "DELETE", "/chats?id="+conv.ID, ""); rec.Code != 200 {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := app.store.getConversationAny(conv.ID); err == nil {
		t.Fatal("deleted conversation still present")
	}
	if _, err := app.store.GetMessage(msg.ID); err == nil {
		t.Fatal("deleted conversation's message still present")
	}
	if rec = doChats(t, app, "DELETE", "/chats?id="+conv.ID, ""); rec.Code == 200 {
		t.Fatal("double delete must not report success")
	}
}

func TestParticipantsEndpointGuardsLead(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	mountedCtx = ctx
	conv := mkConversation(t, app, 41)

	do := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		authorizeTestRequest(req)
		rec := httptest.NewRecorder()
		app.handleParticipants(rec, req)
		return rec
	}

	rec := do("POST", "/participants?id="+conv.ID, `{"agent_id":55}`)
	if rec.Code != 200 {
		t.Fatalf("add status=%d body=%s", rec.Code, rec.Body.String())
	}
	var roster struct {
		AgentIDs    []int64 `json:"agent_ids"`
		LeadAgentID int64   `json:"lead_agent_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &roster); err != nil {
		t.Fatalf("decode roster: %v", err)
	}
	if len(roster.AgentIDs) != 2 || roster.LeadAgentID != 41 {
		t.Fatalf("roster = %+v, want [41 55] led by 41", roster)
	}
	if updated, _ := app.store.GetConversation(conv.ID); updated.Kind != "room" {
		t.Fatalf("kind=%q after second agent, want room", updated.Kind)
	}

	// Removing the lead is refused; removing the other agent works and
	// flips the kind back.
	if rec = do("DELETE", "/participants?id="+conv.ID+"&agent_id=41", ""); rec.Code != 400 {
		t.Fatalf("lead removal status=%d, want 400", rec.Code)
	}
	if rec = do("DELETE", "/participants?id="+conv.ID+"&agent_id=55", ""); rec.Code != 200 {
		t.Fatalf("remove status=%d body=%s", rec.Code, rec.Body.String())
	}
	if updated, _ := app.store.GetConversation(conv.ID); updated.Kind != "direct" {
		t.Fatalf("kind=%q after removal, want direct", updated.Kind)
	}
}

func TestAgentsEndpointServesDirectory(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	mountedCtx = ctx

	req := httptest.NewRequest("GET", "/agents?project_id=proj-1", nil)
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleAgents(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var agents []struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Status   string `json:"status"`
		Attached bool   `json:"attached"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &agents); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(agents) != 4 || agents[0].Name != "Research" || agents[1].Status != "stopped" {
		t.Fatalf("agents = %+v", agents)
	}
	// The binding annotation must survive the trip — the panel scopes
	// its pickers to attached agents.
	if !agents[0].Attached || agents[1].Attached {
		t.Fatalf("attached flags = %v/%v, want true/false", agents[0].Attached, agents[1].Attached)
	}
}

// Observed live: main called inbox_post (its description read like
// exactly what a background thread wants). With no fallback bucket in
// 0.5.0 the call is refused with the flow spelled out.
func TestInboxPostFromAgentIsRefused(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	_, err := app.toolInboxPost(callerCtx(576, "main"), ctx, map[string]any{
		"kind": "alert", "severity": "error",
		"title": "Backup failed", "body": "rsync exit 23 on build-server",
	})
	if err == nil || !strings.Contains(err.Error(), "conversations_create") {
		t.Fatalf("err = %v, want refusal teaching the agent tools", err)
	}
}

// App callers (no agent id) must self-identify — anonymity minted
// "unknown-app" buckets before.
func TestInboxPostWithoutSourceAppRejected(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	_, err := app.toolInboxPost(context.Background(), ctx, map[string]any{
		"kind": "alert", "title": "anon",
	})
	if err == nil || !strings.Contains(err.Error(), "authenticated sibling app identity") {
		t.Fatalf("err = %v, want authenticated app identity requirement", err)
	}
}

// ─── public audience (0.6.0) ─────────────────────────────────────────

// Inbox-kind items are structurally refused in public conversations —
// a site visitor must never see approvals or error alerts. Replying
// stays allowed; that is the whole point of the conversation.
func TestPublicConversationRefusesInboxKinds(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	conv, err := app.store.CreateConversation(CreateConversationInput{
		ProjectID: testProject, LeadAgentID: 41, Title: "Visitor chat",
		ConversationKey: "app:webchat:visitor-1", Origin: "app", Audience: "public",
	})
	if err != nil {
		t.Fatalf("create public conversation: %v", err)
	}
	from := callerCtx(41, "chat-"+conv.ID)

	for tool, call := range map[string]func() (any, error){
		"alert": func() (any, error) {
			return app.toolAlert(from, ctx, map[string]any{
				"conversation_id": conv.ID, "text": "internal detail", "severity": "error"})
		},
		"report": func() (any, error) {
			return app.toolReport(from, ctx, map[string]any{
				"conversation_id": conv.ID, "title": "Weekly", "summary": "internal"})
		},
		"approval": func() (any, error) {
			return app.toolRequestApproval(from, ctx, map[string]any{
				"conversation_id": conv.ID, "title": "Delete data?"})
		},
	} {
		if _, err := call(); err == nil || !strings.Contains(err.Error(), "operator conversation") {
			t.Fatalf("%s into public conversation: err = %v, want operator-conversation refusal", tool, err)
		}
	}

	if _, err := app.toolSend(from, ctx, map[string]any{
		"conversation_id": conv.ID, "text": "Happy to help!",
	}); err != nil {
		t.Fatalf("send into public conversation must work: %v", err)
	}
}

// POST /chats with a conversation_key is find-or-create (one
// conversation per visitor) and defaults the audience to public;
// agent-side conversations_create stays operator.
func TestCreateChatWithKeyIsPublicFindOrCreate(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	mountedCtx = ctx

	create := func() map[string]any {
		rec := doChats(t, app, "POST", "/chats",
			`{"agent_id":41,"title":"Visitor 7","conversation_key":"app:webchat:visitor-7"}`)
		if rec.Code != 200 {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	first := create()
	if first["audience"] != "public" || first["origin"] != "app" {
		t.Fatalf("keyed create = %v, want public/app", first)
	}
	second := create()
	if second["id"] != first["id"] {
		t.Fatalf("keyed create minted a second conversation: %v vs %v", second["id"], first["id"])
	}

	created, err := app.toolCreate(callerCtx(41, "main"), ctx, map[string]any{"title": "Ops topic"})
	if err != nil {
		t.Fatal(err)
	}
	conv, _ := app.store.GetConversation(created.(map[string]any)["conversation_id"].(string))
	if conv.Audience != "operator" {
		t.Fatalf("agent-created audience = %q, want operator", conv.Audience)
	}
}
