package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type participantsModels struct {
	AgentIDs []int64          `json:"agent_ids"`
	Agents   []AgentModelInfo `json:"agents"`
}

func readParticipantModels(t *testing.T, app *App, convID string) participantsModels {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/participants?id="+convID, nil)
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleParticipants(rec, req)
	if rec.Code != 200 {
		t.Fatalf("participants status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out participantsModels
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode participants: %v", err)
	}
	return out
}

func storedGeneratedBy(t *testing.T, app *App, id int64) string {
	t.Helper()
	var raw string
	if err := app.store.db.QueryRow(`SELECT generated_by FROM messages WHERE id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRepliesRecordTheModelThatProducedThem(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	mountedCtx = ctx
	conv := mkConversation(t, app, 41)
	caller := boundConversationCaller(t, app, conv, 41)
	thread := conversationThreadID(conv.ID)

	// Before any reply, Details shows the bound thread and no model.
	got := readParticipantModels(t, app, conv.ID)
	if len(got.Agents) != 1 || got.Agents[0].AgentID != 41 || got.Agents[0].ThreadID != thread || got.Agents[0].LastReply != nil {
		t.Fatalf("before reply: %+v", got.Agents)
	}

	// Another thread's model must not attach to this conversation's reply.
	app.streamer.Ingest("llm.start", 41, "chat-other", `{"provider":"openai","model":"gpt-other"}`, time.Now())
	app.streamer.Ingest("llm.start", 41, thread, `{"provider":"anthropic","model":"claude-a"}`, time.Now())
	receipt, err := app.toolSend(caller, ctx, map[string]any{"conversation_id": conv.ID, "text": "First", "phase": "final"})
	if err != nil {
		t.Fatal(err)
	}
	first := receipt.(map[string]any)["message_id"].(int64)
	if raw := storedGeneratedBy(t, app, first); raw != `{"provider":"anthropic","model":"claude-a"}` {
		t.Fatalf("first reply generated_by=%q", raw)
	}

	// The model is not part of the message payload anyone receives.
	msg, err := app.store.GetMessage(first)
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(msg)
	if strings.Contains(string(wire), "claude-a") || strings.Contains(string(wire), "generated_by") {
		t.Fatalf("message payload exposes the model: %s", wire)
	}

	got = readParticipantModels(t, app, conv.ID)
	a := got.Agents[0]
	if a.LastReply == nil || a.LastReply.Provider != "anthropic" || a.LastReply.Model != "claude-a" || a.ReplyThread != thread || a.RepliedAt == nil {
		t.Fatalf("after first reply: %+v", a)
	}

	// The primary provider fails: until the fallback reports, the model is
	// unknown, so a reply sent in that window records none.
	app.streamer.Ingest("llm.start", 41, thread, `{"provider":"anthropic","model":"claude-a"}`, time.Now())
	app.streamer.Ingest("llm.provider_error", 41, thread, `{"provider":"anthropic","model":"claude-a","role":"primary","error":"overloaded"}`, time.Now())
	receipt, err = app.toolSend(caller, ctx, map[string]any{"conversation_id": conv.ID, "text": "Second", "phase": "final"})
	if err != nil {
		t.Fatal(err)
	}
	second := receipt.(map[string]any)["message_id"].(int64)
	if raw := storedGeneratedBy(t, app, second); raw != "" {
		t.Fatalf("reply during fallback recorded %q", raw)
	}
	// Details keeps the latest reply that did record a model.
	if got = readParticipantModels(t, app, conv.ID); got.Agents[0].LastReply == nil || got.Agents[0].LastReply.Model != "claude-a" {
		t.Fatalf("after unknown reply: %+v", got.Agents[0])
	}

	// llm.done names the provider that actually answered.
	app.streamer.Ingest("llm.done", 41, thread, `{"provider":"openai","model":"gpt-fallback"}`, time.Now())
	receipt, err = app.toolSend(caller, ctx, map[string]any{"conversation_id": conv.ID, "text": "Third", "phase": "final"})
	if err != nil {
		t.Fatal(err)
	}
	third := receipt.(map[string]any)["message_id"].(int64)
	if raw := storedGeneratedBy(t, app, third); raw != `{"provider":"openai","model":"gpt-fallback"}` {
		t.Fatalf("third reply generated_by=%q", raw)
	}
	if got = readParticipantModels(t, app, conv.ID); got.Agents[0].LastReply == nil || got.Agents[0].LastReply.Model != "gpt-fallback" {
		t.Fatalf("after fallback reply: %+v", got.Agents[0])
	}
}

func TestRetriedReplyStaysADuplicateWhenTheModelChanges(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	mountedCtx = ctx
	conv := mkConversation(t, app, 41)
	boundConversationCaller(t, app, conv, 41)
	thread := conversationThreadID(conv.ID)
	caller := callerCtxCall(41, thread, "call_7")
	args := map[string]any{"conversation_id": conv.ID, "text": "Once", "phase": "final"}

	app.streamer.Ingest("llm.start", 41, thread, `{"provider":"anthropic","model":"claude-a"}`, time.Now())
	receipt, err := app.toolSend(caller, ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	app.streamer.Ingest("llm.start", 41, thread, `{"provider":"openai","model":"gpt-b"}`, time.Now())
	retry, err := app.toolSend(caller, ctx, args)
	if err != nil {
		t.Fatalf("retry after a model change failed: %v", err)
	}
	r := retry.(map[string]any)
	if r["inserted"] != false || r["message_id"] != receipt.(map[string]any)["message_id"] {
		t.Fatalf("retry was not a duplicate: %+v", r)
	}
	if raw := storedGeneratedBy(t, app, r["message_id"].(int64)); raw != `{"provider":"anthropic","model":"claude-a"}` {
		t.Fatalf("retry rewrote generated_by: %q", raw)
	}
}

func TestMainThreadModelIsTrackedWithoutAnOpenResponse(t *testing.T) {
	app, _, _ := newTestEnv(t)
	// Main's events reach a conversation only while main answers it, but its
	// model is recorded either way: main's next reply comes from that call.
	app.streamer.Ingest("llm.start", 41, mainThreadID, `{"provider":"anthropic","model":"claude-main"}`, time.Now())
	if use := app.streamer.modelFor(41, mainThreadID); use == nil || use.Model != "claude-main" {
		t.Fatalf("main model=%+v", use)
	}
	if use := app.streamer.modelFor(42, mainThreadID); use != nil {
		t.Fatalf("another agent's main took agent 41's model: %+v", use)
	}
	// A done event without a model keeps the last known one.
	app.streamer.Ingest("llm.done", 41, mainThreadID, `{"provider":"anthropic"}`, time.Now())
	if use := app.streamer.modelFor(41, mainThreadID); use == nil || use.Model != "claude-main" {
		t.Fatalf("main model after empty done=%+v", use)
	}
}
