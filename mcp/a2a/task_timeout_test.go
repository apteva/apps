package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSaveReplyDoesNotReopenTerminalTask(t *testing.T) {
	app, _ := newTestEnv(t)
	task, err := createTask(app.AppDB(), &Task{ProjectID: testProject, Kind: "ask", Status: "submitted", Direction: "local", FromAgentID: 41, ToAgentID: 42})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.AppDB().Exec("UPDATE a2a_tasks SET status='failed' WHERE id=?", task.ID); err != nil {
		t.Fatal(err)
	}
	task.Status = "completed"
	if _, err := saveReply(app.AppDB(), task, 42, 41, "late reply", "event", nil); err != sql.ErrNoRows {
		t.Fatalf("late reply error = %v, want sql.ErrNoRows", err)
	}
}

func TestExpireStaleLocalAskPersistsFailureAndNotifiesRequester(t *testing.T) {
	app, platform := newTestEnvWithConfig(t, map[string]string{"task_timeout_seconds": "60"})
	task, err := createTask(app.AppDB(), &Task{
		ProjectID: testProject, Kind: "ask", Status: "submitted", Direction: "local",
		FromAgentID: 41, FromAgentName: "Research", FromThreadID: "requester-thread",
		ToAgentID: 42, ToAgentName: "CRM",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := recordMessage(app.AppDB(), task.ID, 41, 42, "Please check the report", "submitted"); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339Nano)
	if _, err := app.AppDB().Exec("UPDATE a2a_tasks SET updated_at=? WHERE id=?", old, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := expireStaleTasks(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	saved, err := getTask(app.AppDB(), testProject, task.ID)
	if err != nil || saved == nil || saved.Status != "failed" {
		t.Fatalf("timed out task = %+v, err %v", saved, err)
	}
	messages, err := listMessages(app.AppDB(), task.ID)
	if err != nil || len(messages) != 2 || !strings.Contains(messages[1].Body, "timed out") {
		t.Fatalf("timeout message = %+v, err %v", messages, err)
	}
	var delivered int
	if err := app.AppDB().QueryRow("SELECT delivered FROM a2a_deliveries WHERE task_id=?", task.ID).Scan(&delivered); err != nil || delivered != 1 {
		t.Fatalf("timeout delivery = %d, err %v", delivered, err)
	}
	if len(platform.threadEvents) != 1 || platform.threadEvents[0].Ref.ThreadID != "requester-thread" {
		t.Fatalf("requester notification = %+v", platform.threadEvents)
	}
}

func TestExpireStaleAskLeavesReplyDeliveryPending(t *testing.T) {
	app, _ := newTestEnvWithConfig(t, map[string]string{"task_timeout_seconds": "60"})
	task, err := createTask(app.AppDB(), &Task{
		ProjectID: testProject, Kind: "ask", Status: "working", Direction: "local",
		FromAgentID: 41, ToAgentID: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339Nano)
	if _, err := app.AppDB().Exec("UPDATE a2a_tasks SET updated_at=? WHERE id=?", old, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.AppDB().Exec("INSERT INTO a2a_deliveries(task_id,project_id,to_agent_id,event) VALUES(?,?,?,?)", task.ID, testProject, 41, "saved reply"); err != nil {
		t.Fatal(err)
	}
	if err := expireStaleTasks(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	saved, _ := getTask(app.AppDB(), testProject, task.ID)
	if saved == nil || saved.Status != "working" {
		t.Fatalf("pending-delivery task changed: %+v", saved)
	}
}

func TestPanelMarksOverdueOpenAskAsAttention(t *testing.T) {
	app, _ := newTestEnvWithConfig(t, map[string]string{"task_timeout_seconds": "60"})
	previous := globalCtx
	globalCtx = app
	t.Cleanup(func() { globalCtx = previous })
	task, err := createTask(app.AppDB(), &Task{
		ProjectID: testProject, Kind: "ask", Status: "submitted", Direction: "local",
		FromAgentID: 41, ToAgentID: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339Nano)
	if _, err := app.AppDB().Exec("UPDATE a2a_tasks SET updated_at=? WHERE id=?", old, task.ID); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	(&App{}).handlePanelTasks(w, httptest.NewRequest("GET", fmt.Sprintf("/tasks?status=attention&project_id=%s", testProject), nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var page struct {
		Total int `json:"total"`
		Tasks []struct {
			Overdue bool `json:"overdue"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Tasks) != 1 || !page.Tasks[0].Overdue {
		t.Fatalf("overdue panel response = %+v", page)
	}
}
