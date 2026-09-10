package main

import (
	"fmt"
	"testing"
	"time"
)

func TestAllInventoryKeepsOldWorkAndSchedulesAheadOfHistoryAcrossPages(t *testing.T) {
	app, _, _ := newTestApp(t)
	states := []string{stateRunning, stateQueued, stateBlocked, stateWaiting, stateWaiting, stateFailed, stateCompleted, stateCancelled}
	ids := []string{}
	for i, state := range states {
		task, _, err := app.store.Create(CreateTaskInput{AgentID: 7, ProjectID: "project-a", Title: fmt.Sprint(i), AssignedThreadID: "main"})
		if err != nil {
			t.Fatal(err)
		}
		kind := ""
		if i == 4 {
			kind = "once"
		}
		// History is newer than every active task and schedule.
		at := time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC).Format(timeFormat)
		if _, err := app.store.db.Exec("UPDATE tasks SET state=?,schedule_kind=?,updated_at=? WHERE id=?", state, kind, at, task.ID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
	}
	// Completed and cancelled outcomes share one history group, newest first.
	ids[6], ids[7] = ids[7], ids[6]
	cursor := ""
	actual := []string{}
	for {
		page, err := app.store.ListPage(TaskFilter{ProjectID: "project-a", View: "all", Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range page.Tasks {
			actual = append(actual, task.ID)
		}
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
		if len(actual) > len(ids) {
			t.Fatal("pagination repeated tasks")
		}
	}
	if fmt.Sprint(actual) != fmt.Sprint(ids) {
		t.Fatalf("inventory order = %v, want live work → schedules → history (%v)", actual, ids)
	}
}
