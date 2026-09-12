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
