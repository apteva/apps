package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Only display metadata crosses into a chat; arguments and results stay in telemetry.
type ToolActivity struct {
	ID             int64  `json:"id"`
	ConversationID string `json:"chat_id"`
	AgentID        int64  `json:"agent_id"`
	ThreadID       string `json:"thread_id"`
	CallID         string `json:"call_id"`
	Name           string `json:"name"`
	Reason         string `json:"reason"`
	Status         string `json:"status"`
	StartedAt      string `json:"started_at"`
	EndedAt        string `json:"ended_at"`
	Revision       int64  `json:"revision"`
}

const activityColumns = `id,conversation_id,agent_id,thread_id,call_id,name,reason,status,started_at,ended_at,revision`

func scanActivity(row interface{ Scan(...any) error }) (ToolActivity, error) {
	var a ToolActivity
	err := row.Scan(&a.ID, &a.ConversationID, &a.AgentID, &a.ThreadID, &a.CallID, &a.Name, &a.Reason, &a.Status, &a.StartedAt, &a.EndedAt, &a.Revision)
	return a, err
}
func (s *store) toolActivities(chat string) ([]ToolActivity, error) {
	rows, err := s.db.Query(`SELECT `+activityColumns+` FROM conversation_tool_activity WHERE conversation_id=? ORDER BY id`, chat)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ToolActivity{}
	for rows.Next() {
		a, err := scanActivity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (a *App) handleToolActivity(w http.ResponseWriter, r *http.Request) {
	chat := r.URL.Query().Get("chat_id")
	if _, err := a.authorizeConversation(r, chat); err != nil {
		http.Error(w, "conversation not found", 404)
		return
	}
	rows, err := a.store.toolActivities(chat)
	if err != nil {
		http.Error(w, "activity unavailable", 500)
		return
	}
	writeJSON(w, rows)
}
func visibleActivityTool(name string) bool {
	return name != "" && !visibleConversationTool(name) && name != "pace" && name != "done" && name != "wait" && name != "think"
}
func activityTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }
func (a *App) ingestToolActivity(event string, agent int64, thread, data string, ts time.Time) error {
	if event != "tool.call" && event != "tool.result" {
		return nil
	}
	// Use the same verified participant/thread binding as reply streaming.
	if a.streamer == nil || a.streamer.resolve == nil {
		return nil
	}
	chat := a.streamer.resolve(agent, thread)
	if chat == "" {
		return nil
	}
	var d struct {
		Name       string `json:"name"`
		Tool       string `json:"tool"`
		ID         string `json:"id"`
		CallID     string `json:"call_id"`
		ToolCallID string `json:"tool_call_id"`
		Reason     string `json:"reason"`
		IsError    bool   `json:"is_error"`
	}
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		return nil
	}
	name := firstNonEmptyString(d.Name, d.Tool)
	call := firstNonEmptyString(d.ID, d.CallID, d.ToolCallID)
	if call == "" || len(call) > 1024 || (event == "tool.call" && !visibleActivityTool(name)) {
		return nil
	}
	at := activityTime(ts)
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var item ToolActivity
	if event == "tool.call" {
		// Replayed starts reuse the row; provider call IDs may be reused in later turns.
		_, err = tx.Exec(`INSERT INTO conversation_tool_activity(conversation_id,agent_id,thread_id,call_id,name,reason,started_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, chat, agent, thread, call, truncateActivity(name, 256), truncateActivity(d.Reason, 500), at)
		if err != nil {
			return err
		}
		item, err = scanActivity(tx.QueryRow(`SELECT `+activityColumns+` FROM conversation_tool_activity WHERE conversation_id=? AND agent_id=? AND thread_id=? AND call_id=? AND started_at=?`, chat, agent, thread, call, at))
	} else {
		item, err = scanActivity(tx.QueryRow(`SELECT `+activityColumns+` FROM conversation_tool_activity WHERE conversation_id=? AND agent_id=? AND thread_id=? AND call_id=? AND started_at<=? ORDER BY started_at DESC LIMIT 1`, chat, agent, thread, call, at))
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if item.Status == "completed" || item.Status == "failed" {
			return nil
		}
		item.Status = "completed"
		if d.IsError {
			item.Status = "failed"
		}
		item.EndedAt = at
		item.Revision++
		_, err = tx.Exec(`UPDATE conversation_tool_activity SET status=?,ended_at=?,revision=? WHERE id=?`, item.Status, item.EndedAt, item.Revision, item.ID)
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.hub.publishStream(StreamFrame{Type: "stream", ConversationID: chat, AgentID: agent, ThreadID: thread, CallID: call, CreatedAt: ts, Activity: &item})
	return nil
}
func truncateActivity(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

// A lost telemetry feed must not leave a permanent running indicator after restart.
func (s *store) interruptToolActivities() error {
	_, err := s.db.Exec(`UPDATE conversation_tool_activity SET status='interrupted',revision=revision+1 WHERE status='running'`)
	return err
}
