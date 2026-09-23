package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestToolActivityLifecycleIsolationAndRecovery(t *testing.T) {
	a, _, _ := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	thread := conversationThreadID(conv.ID)
	now := time.Now().UTC()
	ingest := func(event, thread, body string, ts time.Time) {
		t.Helper()
		if err := a.ingestToolActivity(event, 41, thread, body, ts); err != nil {
			t.Fatal(err)
		}
	}
	start := `{"id":"shared","name":"tasks_list","reason":"Checking tasks","args":{"secret":"not for chat"}}`
	ingest("tool.call", "main", start, now)
	ingest("tool.call", thread, `{"id":"hidden","name":"pace"}`, now)
	rows, _ := a.store.toolActivities(conv.ID)
	if len(rows) != 0 {
		t.Fatal("unrelated tools leaked")
	}
	feed, cancel := a.hub.subscribeFrames(conv.ID)
	defer cancel()
	ingest("tool.call", thread, start, now)
	ingest("tool.call", thread, start, now)
	rows, _ = a.store.toolActivities(conv.ID)
	if len(rows) != 1 || rows[0].Status != "running" {
		t.Fatalf("rows=%+v", rows)
	}
	select {
	case f := <-feed:
		if f.Activity == nil || f.Activity.Name != "tasks_list" {
			t.Fatal(f)
		}
	default:
		t.Fatal("no live activity")
	}
	ingest("tool.result", thread, `{"id":"shared","is_error":true,"result":"private result"}`, now.Add(time.Second))
	rows, _ = a.store.toolActivities(conv.ID)
	if rows[0].Status != "failed" || rows[0].Revision != 2 {
		t.Fatal(rows)
	}
	ingest("tool.result", thread, `{"id":"shared"}`, now.Add(time.Second))
	ingest("tool.call", thread, start, now.Add(2*time.Second))
	ingest("tool.result", thread, `{"id":"shared"}`, now.Add(time.Second))
	rows, _ = a.store.toolActivities(conv.ID)
	if len(rows) != 2 || rows[1].Status != "running" {
		t.Fatal(rows)
	}
	if err := a.store.interruptToolActivities(); err != nil {
		t.Fatal(err)
	}
	rows, _ = a.store.toolActivities(conv.ID)
	if rows[1].Status != "interrupted" {
		t.Fatal(rows)
	}
	ingest("tool.result", thread, `{"id":"shared"}`, now.Add(3*time.Second))
	rows, _ = a.store.toolActivities(conv.ID)
	if rows[1].Status != "completed" {
		t.Fatal(rows)
	}
	req := httptest.NewRequest("GET", "/activity?chat_id="+conv.ID, nil)
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	a.handleToolActivity(rec, req)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "private result") {
		t.Fatal(rec.Body.String())
	}
	var history []ToolActivity
	if err := json.Unmarshal(rec.Body.Bytes(), &history); err != nil || len(history) != 2 {
		t.Fatal(history, err)
	}
	req.Header.Set("X-User-ID", "9999")
	rec = httptest.NewRecorder()
	a.handleToolActivity(rec, req)
	if rec.Code != 404 {
		t.Fatal(rec.Code)
	}
	msgs, err := a.store.Transcript(conv.ID, 0, 100)
	if err != nil || len(msgs) != 0 {
		t.Fatalf("activity became messages: %v %v", msgs, err)
	}
}

func TestMessageTimestampKeepsSubsecondPrecision(t *testing.T) {
	a, _, _ := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	message, err := a.store.AppendMessage(&Message{ConversationID: conv.ID, Role: "agent", AgentID: 41, Content: "I found it"})
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := a.store.db.QueryRow(`SELECT created_at FROM messages WHERE id=?`, message.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, ".") || message.CreatedAt.Nanosecond() == 0 {
		t.Fatalf("message timestamp lost subsecond ordering: %q / %s", stored, message.CreatedAt)
	}
}

func TestConversationsToolsHiddenFromLiveAndHistory(t *testing.T) {
	a, _, _ := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	thread := conversationThreadID(conv.ID)
	for _, name := range []string{"conversations_request_approval", "conversations_conversations_request_approval", "conversations_report", "conversations_alert", "conversations_history", "conversations_read_attachment"} {
		t.Run(name, func(t *testing.T) {
			if visibleActivityTool(name) {
				t.Fatal("internal tool is visible")
			}
			data, _ := json.Marshal(map[string]string{"id": name, "name": name})
			if err := a.ingestToolActivity("tool.call", 41, thread, string(data), time.Now()); err != nil {
				t.Fatal(err)
			}
		})
	}
	rows, err := a.store.toolActivities(conv.ID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("live rows: %+v, %v", rows, err)
	}
	// Simulate an approval call persisted by a previous version.
	_, err = a.store.db.Exec(`INSERT INTO conversation_tool_activity(conversation_id,agent_id,thread_id,call_id,name,started_at) VALUES(?,?,?,?,?,?)`, conv.ID, 41, thread, "legacy", "conversations_request_approval", activityTime(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	rows, err = a.store.toolActivities(conv.ID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("history rows: %+v, %v", rows, err)
	}
	if !visibleActivityTool("tickets_create") || !visibleActivityTool("code_delete_repository") {
		t.Fatal("unrelated tools hidden")
	}
}

func TestToolActivityHonorsCoreFailureFlag(t *testing.T) {
	a, _, _ := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	thread := conversationThreadID(conv.ID)
	now := time.Now()
	if err := a.ingestToolActivity("tool.call", 41, thread, `{"id":"wrong-tool","name":"code_repos_archive","reason":"Oops wrong tool"}`, now); err != nil {
		t.Fatal(err)
	}
	if err := a.ingestToolActivity("tool.result", 41, thread, `{"id":"wrong-tool","name":"code_repos_archive","success":false,"duration_ms":3,"result":"slug required"}`, now.Add(3*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	rows, err := a.store.toolActivities(conv.ID)
	if err != nil || len(rows) != 1 || rows[0].Status != "failed" {
		t.Fatalf("failure lost: %+v %v", rows, err)
	}
}

func TestSearchToolsHiddenWithoutHidingOtherQueries(t *testing.T) {
	a, _, _ := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	thread := conversationThreadID(conv.ID)
	now := time.Now()
	for _, event := range []string{"tool.call", "tool.result"} {
		if err := a.ingestToolActivity(event, 41, thread, `{"id":"lookup","name":"search_tools"}`, now); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM conversation_tool_activity WHERE conversation_id=?`, conv.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("lookup persisted: %d, %v", count, err)
	}
	_, err := a.store.db.Exec(`INSERT INTO conversation_tool_activity(conversation_id,agent_id,thread_id,call_id,name,started_at) VALUES(?,?,?,?,?,?)`, conv.ID, 41, thread, "old-lookup", "search_tools", activityTime(now))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := a.store.toolActivities(conv.ID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("old lookup visible: %+v, %v", rows, err)
	}
	for _, name := range []string{"search_tools", " SEARCH_TOOLS "} {
		if visibleActivityTool(name) {
			t.Fatalf("internal lookup visible: %s", name)
		}
	}
	for _, name := range []string{"tickets_search", "agent_query", "search_tools_extra", "custom_search_tools"} {
		if !visibleActivityTool(name) {
			t.Fatalf("unrelated tool hidden: %s", name)
		}
	}
}
