package main

// A response's duration and token usage, shown under its last agent message
// ("Worked for 1m 12s · 48k tokens"). Kept in that message's metadata so
// every read path and the changes feed carry it.

import (
	"encoding/json"
	"time"
)

// recordTurnWork stores work on the response's completion message, or on the
// last message the agent wrote in it.
func (a *App) recordTurnWork(chat string, agent, messageID, after int64, work TurnWork) {
	if work.DurationMS < int64(time.Second/time.Millisecond) && work.Tokens == 0 {
		return
	}
	target := messageID
	if target == 0 {
		_ = a.store.db.QueryRow(`SELECT COALESCE(MAX(id),0) FROM messages WHERE conversation_id=? AND role='agent' AND agent_id=? AND id>?`, chat, agent, after).Scan(&target)
	}
	if target == 0 {
		return
	}
	msg, err := a.store.setMessageWork(target, work)
	if err != nil || msg == nil {
		return
	}
	a.hub.publish(chat, *msg)
}

// setMessageWork adds work to an agent message that has none yet. It returns
// nil when nothing changed.
func (s *store) setMessageWork(id int64, work TurnWork) (*Message, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var raw, role string
	if err := tx.QueryRow(`SELECT metadata_json,role FROM messages WHERE id=?`, id).Scan(&raw, &role); err != nil {
		return nil, err
	}
	meta := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &meta)
	if _, has := meta["work"]; has || role != "agent" {
		return nil, nil
	}
	meta["work"] = work
	encoded, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE messages SET metadata_json=? WHERE id=?`, string(encoded), id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO message_changes(message_id,conversation_id) SELECT id,conversation_id FROM messages WHERE id=?`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE messages SET revision=last_insert_rowid() WHERE id=?`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetMessage(id)
}
