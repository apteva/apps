package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// helperAgent stands for the Apteva Helper: an agent without a project.
const helperAgent int64 = 501

func projectlessCaller(agentID int64, threadID string) context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: agentID, ThreadID: threadID})
}

// Live 0.27.3 bug: the Helper's conversations_send failed with "authenticated
// project required", so it could never answer a conversation it was added to.
func TestProjectlessParticipantRepliesInConversationProject(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	mountedCtx = ctx
	conv := mkAgentConversation(t, app, CreateConversationInput{Title: "Helper", LeadAgentID: helperAgent})
	postUserMessage(t, app, conv, "Hi")
	helper := projectlessCaller(helperAgent, mainThreadID)
	if _, err := app.toolSend(helper, ctx, map[string]any{
		"conversation_id": conv.ID, "text": "Hi! How can I help?", "phase": "final"}); err != nil {
		t.Fatalf("project-less participant send: %v", err)
	}
	transcript, err := app.store.Transcript(conv.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	last := transcript[len(transcript)-1]
	if last.Role != "agent" || last.AgentID != helperAgent || last.Content != "Hi! How can I help?" {
		t.Fatalf("reply not stored in the conversation: %+v", last)
	}
	if ids := app.streamer.activeConversationIDs(); len(ids) != 0 {
		t.Fatalf("Thinking stayed on after the Helper's reply: %v", ids)
	}
	if _, err := app.toolHistory(helper, ctx, map[string]any{"conversation_id": conv.ID}); err != nil {
		t.Fatalf("project-less participant history: %v", err)
	}
	// Writes run in the conversation's project: the report lands in its inbox.
	worker := projectlessCaller(helperAgent, "worker-report")
	if _, err := app.toolReport(worker, ctx, map[string]any{
		"conversation_id": conv.ID, "title": "Summary", "summary": "All good"}); err != nil {
		t.Fatalf("project-less participant report: %v", err)
	}
	items, err := app.store.Inbox(testProject, 1, 20)
	if err != nil || len(items) != 1 {
		t.Fatalf("report not in the conversation's project inbox: items=%+v err=%v", items, err)
	}
}

func TestProjectlessNonParticipantIsRefused(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	helper := projectlessCaller(helperAgent, mainThreadID)
	if _, err := app.toolSend(helper, ctx, map[string]any{"conversation_id": conv.ID, "text": "hello"}); err == nil ||
		!strings.Contains(err.Error(), "not a participant") {
		t.Fatalf("project-less non-participant send: err=%v", err)
	}
	if _, err := app.toolHistory(helper, ctx, map[string]any{"conversation_id": conv.ID}); err == nil ||
		!strings.Contains(err.Error(), "not a participant") {
		t.Fatalf("project-less non-participant history: err=%v", err)
	}
	if transcript, _ := app.store.Transcript(conv.ID, 0, 20); len(transcript) != 0 {
		t.Fatalf("refused call wrote the transcript: %+v", transcript)
	}
}

func TestProjectCallerCannotReachAnotherProjectsConversation(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	other := mkAgentConversation(t, app, CreateConversationInput{ProjectID: "other-project", LeadAgentID: 41, Title: "Other"})
	if _, err := app.toolSend(callerCtx(41, "worker-1"), ctx, map[string]any{"conversation_id": other.ID, "text": "hello"}); err == nil ||
		!strings.Contains(err.Error(), "conversation not found") {
		t.Fatalf("cross-project send: err=%v", err)
	}
}

func TestProjectlessCallerCannotListOrCreate(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	mkAgentConversation(t, app, CreateConversationInput{Title: "Helper", LeadAgentID: helperAgent})
	helper := projectlessCaller(helperAgent, mainThreadID)
	if _, err := app.toolList(helper, ctx, map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "conversations_list needs an authenticated project") {
		t.Fatalf("project-less list: err=%v", err)
	}
	if _, err := app.toolCreate(helper, ctx, map[string]any{"title": "New work"}); err == nil ||
		!strings.Contains(err.Error(), "conversations_create needs an authenticated project") {
		t.Fatalf("project-less create: err=%v", err)
	}
	var count int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM conversations WHERE title='New work'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("refused create wrote a conversation: count=%d err=%v", count, err)
	}
}

// Without a project to narrow the lookup, a thread's binding is found in any
// project, so a Helper conversation thread stays inside its conversation.
func TestProjectlessConversationThreadStaysInItsConversation(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	owned := mkAgentConversation(t, app, CreateConversationInput{ProjectID: "helper-project-a", LeadAgentID: helperAgent, Title: "A"})
	target := mkAgentConversation(t, app, CreateConversationInput{LeadAgentID: helperAgent, Title: "B"})
	thread := conversationThreadID(owned.ID)
	if err := app.store.RecordAgentThread(owned.ID, helperAgent, thread, "test", "test", ""); err != nil {
		t.Fatal(err)
	}
	from := projectlessCaller(helperAgent, thread)
	if _, err := app.toolSend(from, ctx, map[string]any{"conversation_id": owned.ID, "text": "in place"}); err != nil {
		t.Fatalf("send in own conversation: %v", err)
	}
	if _, err := app.toolSend(from, ctx, map[string]any{"conversation_id": target.ID, "text": "escape"}); err == nil ||
		!strings.Contains(err.Error(), "belongs to conversation "+owned.ID) {
		t.Fatalf("cross-project escape: err=%v", err)
	}
	if err := app.store.DeleteConversation(owned.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolSend(from, ctx, map[string]any{"conversation_id": target.ID, "text": "escape"}); err == nil ||
		!strings.Contains(err.Error(), "thread is retired") {
		t.Fatalf("retired thread escape: err=%v", err)
	}
	if transcript, _ := app.store.Transcript(target.ID, 0, 20); len(transcript) != 0 {
		t.Fatalf("escaped calls wrote the target: %+v", transcript)
	}
}

func settledEvent(sourceEventID string) string {
	raw, _ := json.Marshal(map[string]any{"source_app": "conversations", "source_event_id": sourceEventID, "reason": "event_wait"})
	return string(raw)
}

// The live symptom of the same bug: the refused reply left Thinking on. Core
// settles the tracked event when main reaches its wait boundary.
func TestTurnEndWithoutReplySettlesThinking(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	app.streamer.telemetryConnected = true
	app.streamer.mainTelemetryConnected = true
	conv := mkAgentConversation(t, app, CreateConversationInput{Title: "Helper", LeadAgentID: helperAgent})
	postUserMessage(t, app, conv, "Hi")
	if len(platform.trackedEvents) != 1 {
		t.Fatalf("main deliveries=%+v", platform.trackedEvents)
	}
	event := platform.trackedEvents[0]
	_, messageID, _, ok := parseConversationThreadEventID(event.SourceEventID)
	if !ok {
		t.Fatalf("unparsed source event id %q", event.SourceEventID)
	}
	text, _ := event.Message.(string)
	received, _ := json.Marshal(map[string]string{"source": "console", "message": coreEventPreview(text)})
	at := time.Now()
	step := func(agent int64, typ, data string) {
		t.Helper()
		at = at.Add(time.Millisecond)
		app.streamer.Ingest(typ, agent, mainThreadID, data, at)
	}
	step(helperAgent, "event.received", string(received))
	step(helperAgent, "llm.start", `{}`)
	step(helperAgent, "tool.call", `{"id":"send-1","name":"conversations_send"}`)
	step(helperAgent, "tool.result", `{"id":"send-1","name":"conversations_send","success":false,"result":"error: refused"}`)
	if !app.streamer.hasActiveResponse(conv.ID, helperAgent) {
		t.Fatal("a failed tool call settled the response; the model may still retry")
	}
	for name, ev := range map[string]struct {
		agent int64
		id    string
	}{
		"another agent":   {41, event.SourceEventID},
		"idle check":      {helperAgent, "conversation:" + conv.ID + ":idle-check:501:20261122"},
		"another message": {helperAgent, conversationThreadEventID(conv.ID, messageID+1, helperAgent)},
		"other agent id":  {helperAgent, conversationThreadEventID(conv.ID, messageID, 41)},
	} {
		step(ev.agent, "event.settled", settledEvent(ev.id))
		app.streamer.mu.Lock()
		_, ackPending := app.streamer.pendingAcks[responseProgressKey(conv.ID, helperAgent)]
		app.streamer.mu.Unlock()
		if !app.streamer.hasActiveResponse(conv.ID, helperAgent) || !ackPending {
			t.Fatalf("%s settled the response (ack pending=%v)", name, ackPending)
		}
	}
	step(helperAgent, "event.settled", settledEvent(event.SourceEventID))
	if ids := app.streamer.activeConversationIDs(); len(ids) != 0 {
		t.Fatalf("Thinking stayed on after the turn ended without a reply: %v", ids)
	}
}

// A project-scoped install's feeds never carry an agent without a project, so
// its receipt cannot arrive. Its reply must still settle Thinking.
func TestUnseenAgentSettlesOnDurableReply(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	mountedCtx = ctx
	app.streamer.telemetryConnected = true
	app.streamer.mainTelemetryConnected = true
	conv := mkAgentConversation(t, app, CreateConversationInput{Title: "Helper", LeadAgentID: helperAgent})
	postUserMessage(t, app, conv, "Hi")
	if _, err := app.toolSend(projectlessCaller(helperAgent, mainThreadID), ctx, map[string]any{
		"conversation_id": conv.ID, "text": "Hi!", "phase": "final"}); err != nil {
		t.Fatal(err)
	}
	if ids := app.streamer.activeConversationIDs(); len(ids) != 0 {
		t.Fatalf("Thinking stayed on for an agent the feeds never showed: %v", ids)
	}
}

// Once the feed has shown the agent, a reply that arrives before Core's
// receipt (the previous turn's housekeeping) still cannot settle the new
// response.
func TestSeenAgentReplyBeforeReceiptKeepsThinking(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	app.streamer.telemetryConnected = true
	app.streamer.mainTelemetryConnected = true
	conv := mkAgentConversation(t, app, CreateConversationInput{Title: "Helper", LeadAgentID: helperAgent})
	app.streamer.Ingest("llm.start", helperAgent, mainThreadID, `{}`, time.Now())
	postUserMessage(t, app, conv, "Hi")
	helper := projectlessCaller(helperAgent, mainThreadID)
	if _, err := app.toolSend(helper, ctx, map[string]any{"conversation_id": conv.ID, "text": "Earlier answer", "phase": "final"}); err != nil {
		t.Fatal(err)
	}
	if !app.streamer.hasActiveResponse(conv.ID, helperAgent) {
		t.Fatal("a reply before Core's receipt settled the new response")
	}
	text, _ := platform.trackedEvents[0].Message.(string)
	received, _ := json.Marshal(map[string]string{"source": "console", "message": coreEventPreview(text)})
	app.streamer.Ingest("event.received", helperAgent, mainThreadID, string(received), time.Now())
	if _, err := app.toolSend(helper, ctx, map[string]any{"conversation_id": conv.ID, "text": "Hi!", "phase": "final"}); err != nil {
		t.Fatal(err)
	}
	if ids := app.streamer.activeConversationIDs(); len(ids) != 0 {
		t.Fatalf("Thinking stayed on after the reply: %v", ids)
	}
}

func TestParseConversationThreadEventID(t *testing.T) {
	chat, message, agent, ok := parseConversationThreadEventID(conversationThreadEventID("conv-abc", 2261, 1))
	if !ok || chat != "conv-abc" || message != 2261 || agent != 1 {
		t.Fatalf("round trip = %q %d %d %v", chat, message, agent, ok)
	}
	for _, id := range []string{"", "conversation:conv-abc:idle-check:1:20261121", "conversation:conv-abc:dormant:1:2026-11-21T090151",
		"conversation::message:1:agent:1", "conversation:conv-abc:message:0:agent:1", "conversation:conv-abc:message:x:agent:1",
		"conversation:conv-abc:message:1:agent:-2", "conversation:conv-abc:message:1:agent:1:extra"} {
		if _, _, _, ok := parseConversationThreadEventID(id); ok {
			t.Fatalf("parsed %q", id)
		}
	}
}
