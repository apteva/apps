package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Alerts and reports are kinds of conversations_send, so a model that picks
// the one send tool can always file them. The legacy tools remain aliases.

func TestSendFilesAlertAndReportCards(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	from := callerCtx(41, "main")
	created, err := app.toolCreate(from, ctx, map[string]any{"title": "Backup recovery"})
	if err != nil {
		t.Fatal(err)
	}
	convID := created.(map[string]any)["conversation_id"].(string)

	out, err := app.toolSend(from, ctx, map[string]any{"conversation_id": convID, "kind": "alert", "text": "Nightly backup failed", "severity": "error"})
	if err != nil {
		t.Fatalf("send kind=alert: %v", err)
	}
	alert, err := app.store.GetMessage(out.(map[string]any)["message_id"].(int64))
	if err != nil || alert.ComponentKind != kindAlert || alert.Severity != "error" || alert.Content != "Nightly backup failed" {
		t.Fatalf("alert = %+v, %v", alert, err)
	}

	// A model that writes a report's summary as text is understood.
	out, err = app.toolSend(from, ctx, map[string]any{"conversation_id": convID, "kind": "report", "title": "Backups restored",
		"text": "Both backups are verified.", "sections": []any{map[string]any{"title": "Verification", "body": "- Checked both archives."}}})
	if err != nil {
		t.Fatalf("send kind=report: %v", err)
	}
	report, err := app.store.GetMessage(out.(map[string]any)["message_id"].(int64))
	if err != nil || report.ComponentKind != kindReport || report.Content != "Report: Backups restored" {
		t.Fatalf("report = %+v, %v", report, err)
	}
	if props := report.Components[0].Props; props["summary"] != "Both backups are verified." {
		t.Fatalf("report props = %+v", props)
	}

	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"conversation_id": convID, "kind": "report", "title": "No summary"}, "title and summary"},
		{map[string]any{"conversation_id": convID, "kind": "alert"}, "needs text"},
		{map[string]any{"conversation_id": convID, "kind": "alert", "text": "x", "severity": "fatal"}, "invalid severity"},
		{map[string]any{"conversation_id": convID, "kind": "card", "text": "x"}, "invalid kind"},
		{map[string]any{"conversation_id": convID, "kind": "alert", "text": "x", "components": []any{map[string]any{"app": "conversations", "name": "question-card", "props": map[string]any{}}}}, "belong to kind=message"},
	} {
		if _, err := app.toolSend(from, ctx, tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("send %v: err=%v, want %q", tc.args, err, tc.want)
		}
	}
}

// Main may not reply where it is not routed; the refusal points at the cards
// it may post instead.
func TestMainReplyRefusalPointsAtCards(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	_, err := app.toolSend(callerCtx(41, "main"), ctx, map[string]any{"conversation_id": conv.ID, "text": "Backup failed"})
	if err == nil || !strings.Contains(err.Error(), "kind=alert or kind=report") {
		t.Fatalf("err = %v, want refusal naming the card kinds", err)
	}
	if _, err := app.toolSend(callerCtx(41, "main"), ctx, map[string]any{"conversation_id": conv.ID, "kind": "alert", "text": "Backup failed", "severity": "error"}); err != nil {
		t.Fatalf("main alert: %v", err)
	}
}

func TestSendTopicReusesOrCreatesTheWorkConversation(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	from := callerCtx(41, "main")
	out, err := app.toolSend(from, ctx, map[string]any{"topic": "Disk capacity", "kind": "alert", "text": "Disk at 91%", "severity": "warn"})
	if err != nil {
		t.Fatalf("alert by topic: %v", err)
	}
	convID := out.(map[string]any)["conversation_id"].(string)
	conv, err := app.store.GetConversation(convID)
	if err != nil || conv.Title != "Disk capacity" || conv.Origin != "agent" {
		t.Fatalf("topic conversation = %+v, %v", conv, err)
	}

	again, err := app.toolSend(from, ctx, map[string]any{"topic": "disk CAPACITY", "kind": "report", "title": "Disk cleaned", "summary": "Usage is back to 60%."})
	if err != nil || again.(map[string]any)["conversation_id"] != convID {
		t.Fatalf("report by topic = %+v, %v; want reuse of %s", again, err, convID)
	}
	approval, err := app.toolRequestApproval(from, ctx, map[string]any{"topic": "Disk capacity", "title": "Delete old archives?"})
	if err != nil {
		t.Fatalf("approval by topic: %v", err)
	}
	if msg, _ := app.store.GetMessage(approval.(map[string]any)["message_id"].(int64)); msg == nil || msg.ConversationID != convID {
		t.Fatalf("approval landed in %+v, want %s", msg, convID)
	}
	created, err := app.toolCreate(from, ctx, map[string]any{"title": "Disk capacity"})
	if err != nil || created.(map[string]any)["created"] != false || created.(map[string]any)["conversation_id"] != convID {
		t.Fatalf("create after topic = %+v, %v", created, err)
	}

	// Neither id nor topic: the error teaches both.
	if _, err := app.toolSend(from, ctx, map[string]any{"kind": "alert", "text": "x"}); err == nil || !strings.Contains(err.Error(), "conversation_id or topic required") {
		t.Fatalf("err = %v", err)
	}
}

// A conversation thread posts only in its own conversation: it may omit the
// id, and a topic cannot move it elsewhere.
func TestConversationThreadPostsInItsOwnConversation(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	thread := boundConversationCaller(t, app, conv, 41)
	out, err := app.toolSend(thread, ctx, map[string]any{"kind": "report", "topic": "Elsewhere", "title": "Import done", "summary": "All 40 rows imported."})
	if err != nil {
		t.Fatalf("report without id: %v", err)
	}
	if got := out.(map[string]any)["conversation_id"]; got != conv.ID {
		t.Fatalf("report landed in %v, want %s", got, conv.ID)
	}
	if found, _ := app.store.FindAgentConversationByTitle(testProject, 41, "Elsewhere"); found != nil {
		t.Fatal("a conversation thread created a topic conversation")
	}
	if _, err := app.toolSend(thread, ctx, map[string]any{"text": "Done."}); err != nil {
		t.Fatalf("reply without id: %v", err)
	}
}

func TestPublicConversationRefusesCardKinds(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	public := mkAgentConversation(t, app, CreateConversationInput{Title: "Visitors", Audience: "public", LeadAgentID: 41})
	thread := boundConversationCaller(t, app, public, 41)
	for _, kind := range []string{"alert", "report"} {
		_, err := app.toolSend(thread, ctx, map[string]any{"conversation_id": public.ID, "kind": kind, "text": "x", "title": "x", "summary": "x"})
		if err == nil || !strings.Contains(err.Error(), "public (visitor) conversation") {
			t.Errorf("%s in public conversation: err=%v", kind, err)
		}
	}
}

// The legacy tools keep working, including the new topic routing.
func TestAlertAndReportAliasesStillWork(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	from := callerCtx(41, "main")
	if _, err := app.toolAlert(from, ctx, map[string]any{"topic": "Payments", "text": "Webhook failing", "severity": "error"}); err != nil {
		t.Fatalf("alert alias: %v", err)
	}
	out, err := app.toolReport(from, ctx, map[string]any{"topic": "Payments", "title": "Webhook fixed", "summary": "Payments flow again."})
	if err != nil {
		t.Fatalf("report alias: %v", err)
	}
	if out.(map[string]any)["status"] != "sent" {
		t.Fatalf("report receipt = %+v", out)
	}
}

// An alert or report send files a card; its text must never stream into a
// reply bubble.
func TestStreamerSkipsCardKindSends(t *testing.T) {
	h := newHub()
	s := newStreamer(h)
	ch, cancel := h.subscribeFrames("conv-1")
	defer cancel()
	for _, c := range []string{
		`{"tool":"conversations_send","id":"call-1","chunk":"{\"conversation_id\":\"conv-1\",\"kind\":\"alert\",\"text\":\"Disk"}`,
		`{"tool":"conversations_send","id":"call-1","chunk":" full\"}"}`,
	} {
		s.Ingest("llm.tool_chunk", 41, "chat-conv-1", c, time.Now())
	}
	s.Ingest("tool.call", 41, "chat-conv-1", `{"name":"conversations_send","id":"call-1","args":{"conversation_id":"conv-1","kind":"report","text":"Disk full"}}`, time.Now())
	select {
	case f := <-ch:
		if f.Type == "stream" && f.Text != "" {
			t.Fatalf("card send streamed as a reply: %+v", f)
		}
	case <-time.After(150 * time.Millisecond):
	}
	if text, ok := decodeSendArgs(json.RawMessage(`{"conversation_id":"conv-1","kind":"message","text":"Hi"}`), "conv-1"); !ok || text != "Hi" {
		t.Fatalf("explicit message kind = %q, %v", text, ok)
	}
}

func TestRarelyUsedToolsLoadOnlyWhenNamed(t *testing.T) {
	named := map[string]bool{}
	for _, tool := range (&App{}).MCPTools() {
		named[tool.Name] = tool.Meta["io.apteva/autoload"] == "named"
	}
	for _, name := range []string{"create", "list", "history", "keep_thread", "report", "alert"} {
		if !named[name] {
			t.Errorf("%s should load only when named", name)
		}
	}
	for _, name := range []string{"send", "request_approval"} {
		if named[name] {
			t.Errorf("%s must stay available by relevance", name)
		}
	}
}

// send replaced three tools. Keep it small enough that it does not crowd
// other tools out of a model's tool budget; details belong in the skill.
func TestSendToolStaysSmall(t *testing.T) {
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name != "send" {
			continue
		}
		schema, _ := json.Marshal(tool.InputSchema)
		if len(tool.Description) > 1100 || len(tool.Description)+len(schema) > 4600 {
			t.Fatalf("send description=%d schema=%d bytes; move guidance to the skill", len(tool.Description), len(schema))
		}
		return
	}
	t.Fatal("send tool missing")
}

// A plain reply by topic never creates a conversation: nothing there waits for
// a reply, and main could not answer it anyway.
func TestReplyByTopicNeverCreatesAConversation(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	from := callerCtx(41, "main")
	if _, err := app.toolSend(from, ctx, map[string]any{"topic": "Weekly digest", "text": "Hello"}); err == nil || !strings.Contains(err.Error(), "kind=alert or kind=report") {
		t.Fatalf("err = %v", err)
	}
	if found, _ := app.store.FindAgentConversationByTitle(testProject, 41, "Weekly digest"); found != nil {
		t.Fatal("a plain reply created a conversation")
	}
	// Empty optional arrays on a card are not refused.
	if _, err := app.toolSend(from, ctx, map[string]any{"topic": "Weekly digest", "kind": "alert", "text": "x", "components": []any{}, "attachments": []any{}}); err != nil {
		t.Fatalf("alert with empty arrays: %v", err)
	}
}

// A Core in search mode lists each tool by the first sentence of its
// description. send's must say what it posts.
func TestSendDirectoryLineNamesItsKinds(t *testing.T) {
	first := strings.SplitN(sendToolDescription, ". ", 2)[0]
	for _, want := range []string{"reply", "alert", "report"} {
		if !strings.Contains(first, want) || len(first) > 90 {
			t.Fatalf("first sentence %q must name %q within 90 characters", first, want)
		}
	}
}
