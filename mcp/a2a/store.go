package main

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Task is one agent-to-agent exchange. Statuses mirror the A2A
// protocol lifecycle so this ledger can back real cross-server A2A
// later without a migration.
type Task struct {
	ID            int64  `json:"id"`
	ProjectID     string `json:"project_id,omitempty"`
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	FromAgentID   int64  `json:"from_agent_id"`
	FromAgentName string `json:"from_agent_name,omitempty"`
	FromThreadID  string `json:"-"`
	ToAgentID     int64  `json:"to_agent_id"`
	ToAgentName   string `json:"to_agent_name,omitempty"`
	ToThreadID    string `json:"-"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// openStatuses are the states in which a task still awaits resolution.
var openStatuses = map[string]bool{
	"submitted":      true,
	"working":        true,
	"input_required": true,
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func createTask(db *sql.DB, t *Task) (*Task, error) {
	now := nowUTC()
	res, err := db.Exec(`
		INSERT INTO a2a_tasks
			(project_id, kind, status, from_agent_id, from_agent_name, from_thread_id,
			 to_agent_id, to_agent_name, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ProjectID, t.Kind, t.Status, t.FromAgentID, t.FromAgentName, t.FromThreadID,
		t.ToAgentID, t.ToAgentName, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getTask(db, t.ProjectID, id)
}

func getTask(db *sql.DB, projectID string, id int64) (*Task, error) {
	row := db.QueryRow(`
		SELECT id, project_id, kind, status, from_agent_id, from_agent_name, from_thread_id,
		       to_agent_id, to_agent_name, COALESCE(to_thread_id,''), created_at, updated_at
		FROM a2a_tasks
		WHERE id = ? AND project_id = ?`,
		id, projectID)
	var t Task
	err := row.Scan(&t.ID, &t.ProjectID, &t.Kind, &t.Status, &t.FromAgentID, &t.FromAgentName,
		&t.FromThreadID, &t.ToAgentID, &t.ToAgentName, &t.ToThreadID, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// setTaskResponderThread records which responder-side thread owns the
// task. Written once, on the responder's first reply.
func setTaskResponderThread(db *sql.DB, projectID string, id int64, threadID string) error {
	_, err := db.Exec(
		`UPDATE a2a_tasks SET to_thread_id = ? WHERE id = ? AND project_id = ? AND COALESCE(to_thread_id,'') = ''`,
		threadID, id, projectID)
	return err
}

func setTaskStatus(db *sql.DB, projectID string, id int64, status string) error {
	res, err := db.Exec(
		`UPDATE a2a_tasks SET status = ?, updated_at = ? WHERE id = ? AND project_id = ?`,
		status, nowUTC(), id, projectID)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func recordMessage(db *sql.DB, taskID, fromAgentID, toAgentID int64, body, statusAfter string) error {
	_, err := db.Exec(`
		INSERT INTO a2a_messages (task_id, from_agent_id, to_agent_id, body, status_after, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		taskID, fromAgentID, toAgentID, body, statusAfter, nowUTC())
	return err
}

// deliveriesInWindow counts messages from one agent to another within
// the trailing window — the per-pair rate-limit input.
func deliveriesInWindow(db *sql.DB, fromAgentID, toAgentID int64, window time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-window).Format(time.RFC3339)
	var n int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM a2a_messages
		WHERE from_agent_id = ? AND to_agent_id = ? AND created_at > ?`,
		fromAgentID, toAgentID, cutoff).Scan(&n)
	return n, err
}

func openAskCount(db *sql.DB, projectID string, fromAgentID, toAgentID int64) (int, error) {
	var n int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM a2a_tasks
		WHERE project_id = ? AND from_agent_id = ? AND to_agent_id = ?
		  AND kind = 'ask' AND status IN ('submitted','working','input_required')`,
		projectID, fromAgentID, toAgentID).Scan(&n)
	return n, err
}

// Message is one delivered exchange row within a task.
type Message struct {
	ID          int64  `json:"id"`
	TaskID      int64  `json:"task_id"`
	FromAgentID int64  `json:"from_agent_id"`
	ToAgentID   int64  `json:"to_agent_id"`
	Body        string `json:"body"`
	StatusAfter string `json:"status_after,omitempty"`
	CreatedAt   string `json:"created_at"`
}

func listMessages(db *sql.DB, taskID int64) ([]*Message, error) {
	rows, err := db.Query(`
		SELECT id, task_id, from_agent_id, to_agent_id, body, status_after, created_at
		FROM a2a_messages
		WHERE task_id = ?
		ORDER BY id ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.TaskID, &m.FromAgentID, &m.ToAgentID, &m.Body, &m.StatusAfter, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

type taskFilter struct {
	AgentID int64  // participant (either side); required for the tool view
	Role    string // "", "sent", "received"
	Status  string // "", "open", or an exact status
	Limit   int
}

func listTasks(db *sql.DB, projectID string, f taskFilter) ([]*Task, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := []string{"project_id = ?"}
	args := []any{projectID}
	switch f.Role {
	case "sent":
		where = append(where, "from_agent_id = ?")
		args = append(args, f.AgentID)
	case "received":
		where = append(where, "to_agent_id = ?")
		args = append(args, f.AgentID)
	default:
		if f.AgentID != 0 {
			where = append(where, "(from_agent_id = ? OR to_agent_id = ?)")
			args = append(args, f.AgentID, f.AgentID)
		}
	}
	switch f.Status {
	case "":
	case "open":
		where = append(where, "status IN ('submitted','working','input_required')")
	default:
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	args = append(args, limit)
	rows, err := db.Query(`
		SELECT id, project_id, kind, status, from_agent_id, from_agent_name, from_thread_id,
		       to_agent_id, to_agent_name, COALESCE(to_thread_id,''), created_at, updated_at
		FROM a2a_tasks
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY updated_at DESC, id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Task{}
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Kind, &t.Status, &t.FromAgentID, &t.FromAgentName,
			&t.FromThreadID, &t.ToAgentID, &t.ToAgentName, &t.ToThreadID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}
