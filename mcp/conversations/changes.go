package main

import (
	"net/http"
	"strconv"
)

type ChangePage struct {
	Messages []Message `json:"messages"`
	Cursor   int64     `json:"cursor"`
	HasMore  bool      `json:"has_more"`
	Before   int64     `json:"before"`
}

// Snapshot and replay cursor are read in one transaction. Live events never
// advance this cursor: only a completed durable replay page does.
func (s *store) MessagePage(id string, before int64, limit int) (ChangePage, error) {
	out := ChangePage{Messages: []Message{}}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	tx, err := s.db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if err := tx.QueryRow(`SELECT COALESCE(MAX(id),0) FROM message_changes WHERE conversation_id=?`, id).Scan(&out.Cursor); err != nil {
		return out, err
	}
	rows, err := tx.Query(`SELECT `+messageCols+` FROM messages WHERE conversation_id=? AND inbox_only=0 AND (?=0 OR id<?) ORDER BY id DESC LIMIT ?`, id, before, before, limit+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			rows.Close()
			return out, err
		}
		out.Messages = append(out.Messages, *m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Messages) > limit {
		out.HasMore = true
		out.Messages = out.Messages[:limit]
	}
	for i, j := 0, len(out.Messages)-1; i < j; i, j = i+1, j-1 {
		out.Messages[i], out.Messages[j] = out.Messages[j], out.Messages[i]
	}
	if len(out.Messages) > 0 {
		out.Before = out.Messages[0].ID
	}
	return out, tx.Commit()
}
func (s *store) MessageChanges(id string, since int64, limit int) (ChangePage, error) {
	out := ChangePage{Messages: []Message{}, Cursor: since}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT ch.id,`+prefixCols("m.", messageCols)+` FROM message_changes ch JOIN messages m ON m.id=ch.message_id WHERE ch.conversation_id=? AND ch.id>? ORDER BY ch.id LIMIT ?`, id, since, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		// Prefix scan adapter avoids duplicating the complete message decoder.
		var cursor int64
		m, err := scanMessage(changeScanner{rows, &cursor})
		if err != nil {
			return out, err
		}
		if len(out.Messages) == limit {
			out.HasMore = true
			break
		}
		out.Cursor = cursor
		out.Messages = append(out.Messages, *m)
	}
	return out, rows.Err()
}

type changeScanner struct {
	row    interface{ Scan(...any) error }
	cursor *int64
}

func (s changeScanner) Scan(values ...any) error {
	return s.row.Scan(append([]any{s.cursor}, values...)...)
}
func (a *App) handleChanges(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "GET only", 405)
		return
	}
	id := r.URL.Query().Get("chat_id")
	if _, err := a.authorizeConversation(r, id); err != nil {
		http.Error(w, "conversation not found", 404)
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
	page, err := a.store.MessageChanges(id, since, 200)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, page)
}
