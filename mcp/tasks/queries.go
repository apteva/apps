package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type TaskPage struct {
	Tasks      []Task `json:"tasks"`
	NextCursor string `json:"next_cursor,omitempty"`
	HasMore    bool   `json:"has_more"`
}
type EventPage struct {
	Events     []TaskEvent `json:"events"`
	NextCursor string      `json:"next_cursor,omitempty"`
}
type pageCursor struct {
	Time string `json:"time"`
	ID   string `json:"id"`
	View string `json:"view"`
	Rank int    `json:"rank,omitempty"`
}

func encodeCursor(value pageCursor) string {
	raw, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodeCursor(raw, view string) (pageCursor, error) {
	var value pageCursor
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err == nil {
		err = json.Unmarshal(data, &value)
	}
	if err != nil || value.ID == "" || value.View != view {
		return value, validationError("invalid pagination cursor")
	}
	if _, err = time.Parse(time.RFC3339Nano, value.Time); err != nil {
		return value, validationError("invalid pagination cursor time")
	}
	return value, nil
}
func pageLimit(n int) int {
	if n <= 0 {
		return 200
	}
	if n > 500 {
		return 500
	}
	return n
}

const definitionPredicate = `(schedule_kind<>'' AND scheduled_for IS NULL AND parent_task_id='')`
const queueRankExpression = `CASE WHEN state='failed' OR last_occurrence_status='failed' THEN 0 WHEN state='blocked' OR last_occurrence_status='blocked' THEN 1 WHEN state='running' THEN 2 WHEN state='queued' THEN 3 WHEN schedule_kind='' OR scheduled_for IS NOT NULL THEN 4 ELSE 5 END`

const attentionPredicate = `(state IN ('blocked','failed') OR (schedule_kind<>'' AND last_occurrence_status IN ('blocked','failed')))`

func (s *taskStore) ListPage(filter TaskFilter) (TaskPage, error) {
	page := TaskPage{Tasks: []Task{}}
	where := ` WHERE 1=1`
	args := []any{}
	if filter.ProjectID != "" {
		where += ` AND project_id=?`
		args = append(args, filter.ProjectID)
	}
	if filter.AgentID > 0 {
		where += ` AND agent_id=?`
		args = append(args, filter.AgentID)
	}
	if filter.ParentTaskID != nil {
		where += ` AND parent_task_id=?`
		args = append(args, *filter.ParentTaskID)
	}
	if len(filter.States) > 0 {
		marks := []string{}
		for _, state := range filter.States {
			if !validState(state) {
				return page, fmt.Errorf("%w: %s", errInvalidState, state)
			}
			marks = append(marks, "?")
			args = append(args, state)
		}
		where += ` AND state IN (` + strings.Join(marks, ",") + `)`
	}
	switch filter.View {
	case "", "all":
	case "operational":
		where += ` AND state NOT IN ('completed','cancelled')`
	case "active":
		where += ` AND state IN ('queued','running','waiting','blocked') AND NOT ` + definitionPredicate
	case "work":
		where += ` AND ((state IN ('queued','running','waiting','blocked') AND NOT ` + definitionPredicate + `) OR ` + attentionPredicate + `)`
	case "attention":
		where += ` AND ` + attentionPredicate
	case "scheduled":
		where += ` AND ` + definitionPredicate + ` AND state='waiting'`
	case "upcoming":
		where += ` AND ` + definitionPredicate + ` AND state='waiting' AND schedule_enabled=1 AND next_run_at IS NOT NULL`
	case "completed":
		where += ` AND state='completed'`
	case "recent":
		where += ` AND state IN ('completed','failed','cancelled')`
	default:
		return page, validationError("invalid task view")
	}
	if query := strings.TrimSpace(filter.Search); query != "" {
		where += ` AND instr(lower(title||' '||description||' '||current_step||' '||result||' '||error),lower(?))>0`
		args = append(args, query)
	}
	column, direction, comparison := "updated_at", "DESC", "<"
	if filter.View == "upcoming" {
		column, direction, comparison = "next_run_at", "ASC", ">"
	}
	ranked := filter.View == "work" || filter.View == "operational"
	if filter.Cursor != "" {
		cursor, err := decodeCursor(filter.Cursor, filter.View)
		if err != nil {
			return page, err
		}
		condition := `(` + column + comparison + `? OR (` + column + `=? AND id` + comparison + `?))`
		if ranked {
			where += ` AND (` + queueRankExpression + `>? OR (` + queueRankExpression + `=? AND ` + condition + `))`
			args = append(args, cursor.Rank, cursor.Rank)
		} else {
			where += ` AND ` + condition
		}
		args = append(args, cursor.Time, cursor.Time, cursor.ID)
	}
	limit := pageLimit(filter.Limit)
	args = append(args, limit+1)
	order := column + ` ` + direction + `,id ` + direction
	if ranked {
		order = queueRankExpression + ` ASC,` + order
	}
	rows, err := s.db.Query(`SELECT `+taskColumns+` FROM tasks`+where+` ORDER BY `+order+` LIMIT ?`, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return page, err
		}
		page.Tasks = append(page.Tasks, *task)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Tasks) > limit {
		page.HasMore = true
		page.Tasks = page.Tasks[:limit]
		last := page.Tasks[limit-1]
		at := last.UpdatedAt
		if filter.View == "upcoming" {
			at = *last.NextRunAt
		}
		page.NextCursor = encodeCursor(pageCursor{Time: at.Format(timeFormat), ID: last.ID, View: filter.View, Rank: taskQueueRank(last)})
	}
	return page, nil
}

func (s *taskStore) EventsPage(taskID, cursor string, limit int) (EventPage, error) {
	page := EventPage{Events: []TaskEvent{}}
	where := ` WHERE task_id=?`
	args := []any{taskID}
	if cursor != "" {
		value, err := decodeCursor(cursor, "events")
		if err != nil {
			return page, err
		}
		where += ` AND (created_at<? OR (created_at=? AND id<?))`
		args = append(args, value.Time, value.Time, value.ID)
	}
	limit = pageLimit(limit)
	args = append(args, limit+1)
	rows, err := s.db.Query(`SELECT id,task_id,agent_id,event_type,thread_id,from_state,to_state,data_json,created_at FROM task_events`+where+` ORDER BY created_at DESC,id DESC LIMIT ?`, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var event TaskEvent
		var data, at string
		if err = rows.Scan(&event.ID, &event.TaskID, &event.AgentID, &event.EventType, &event.ThreadID, &event.FromState, &event.ToState, &data, &at); err != nil {
			return page, err
		}
		if err = json.Unmarshal([]byte(data), &event.Data); err != nil {
			return page, err
		}
		event.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
		page.Events = append(page.Events, event)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Events) > limit {
		page.Events = page.Events[:limit]
		last := page.Events[limit-1]
		page.NextCursor = encodeCursor(pageCursor{Time: last.CreatedAt.Format(timeFormat), ID: last.ID, View: "events"})
	}
	for left, right := 0, len(page.Events)-1; left < right; left, right = left+1, right-1 {
		page.Events[left], page.Events[right] = page.Events[right], page.Events[left]
	}
	return page, nil
}

func taskQueueRank(task Task) int {
	if task.State == stateFailed || task.LastOccurrenceStatus == stateFailed {
		return 0
	}
	if task.State == stateBlocked || task.LastOccurrenceStatus == stateBlocked {
		return 1
	}
	if task.State == stateRunning {
		return 2
	}
	if task.State == stateQueued {
		return 3
	}
	if task.ScheduleKind == "" || task.ScheduledFor != nil {
		return 4
	}
	return 5
}
