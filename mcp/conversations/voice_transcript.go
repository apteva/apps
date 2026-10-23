package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// These are Core transcript identities, not telemetry delivery identities.
// Continuous speech emits cumulative checkpoints and revises them on shutdown.
type voiceTranscript struct {
	ID                 string                    `json:"id"`
	Generation         int                       `json:"generation"`
	ResponseID         string                    `json:"response_id,omitempty"`
	ItemID             string                    `json:"item_id,omitempty"`
	Status             string                    `json:"status"`
	FinalizationReason string                    `json:"finalization_reason"`
	StartMS            int                       `json:"start_ms,omitempty"`
	EndMS              int                       `json:"end_ms,omitempty"`
	Fragments          []voiceTranscriptFragment `json:"fragments,omitempty"`
}

type voiceTranscriptFragment struct {
	Text    string `json:"text"`
	StartMS int    `json:"start_ms"`
	EndMS   int    `json:"end_ms"`
}

func (v *voiceTranscript) validate() error {
	if len(v.Fragments) > 4096 {
		return fmt.Errorf("too many voice transcript fragments")
	}
	for _, f := range v.Fragments {
		if f.StartMS < 0 || f.EndMS < f.StartMS {
			return fmt.Errorf("invalid voice fragment timeline")
		}
	}
	if v.ID == "" || len(v.ID) > 2048 || v.Generation < 0 || v.StartMS < 0 || v.EndMS < 0 || (v.EndMS > 0 && v.EndMS < v.StartMS) || len(v.ItemID) > 1024 || len(v.ResponseID) > 1024 || len(v.FinalizationReason) > 256 {
		return fmt.Errorf("invalid voice transcript metadata")
	}
	switch v.Status {
	case "application_grouped", "provider_final", "interrupted", "blocked":
		return nil
	default:
		return fmt.Errorf("invalid voice transcript status")
	}
}

// One atomic statement serializes concurrent updates without a read/write race.
// Terminal metadata outranks checkpoints; event time orders updates within a
// status. A delayed checkpoint cannot roll a completed transcript backwards.
func (s *store) upsertVoiceTranscript(m *Message, transcript *voiceTranscript, at time.Time) (*Message, bool, error) {
	if err := transcript.validate(); err != nil {
		return nil, false, err
	}
	identity, _ := json.Marshal([]any{m.ThreadID, m.Role, transcript.Generation, transcript.ID})
	m.ClientID = fmt.Sprintf("voice-transcript:%x", sha256.Sum256(identity))
	if at.IsZero() {
		at = time.Now().UTC()
	}
	normalizeMessage(m)
	m.Metadata["voice_session_id"] = m.ThreadID
	m.Metadata["transcript"] = transcript
	m.Metadata["voice_event_at"] = at.UTC().Format("2006-01-02T15:04:05.000000000Z")
	if err := validateMessageSize(m); err != nil {
		return nil, false, err
	}
	metadata, err := json.Marshal(m.Metadata)
	if err != nil {
		return nil, false, err
	}
	rank := func(prefix string) string {
		return `CASE json_extract(` + prefix + `.metadata_json,'$.transcript.status') WHEN 'blocked' THEN 4 WHEN 'provider_final' THEN 3 WHEN 'interrupted' THEN 2 ELSE 1 END`
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`INSERT INTO messages
		(conversation_id,role,content,agent_id,user_id,thread_id,status,phase,metadata_json,client_message_id,created_at)
		SELECT ?,?,?,?,?,?,'final','final',?,?,? WHERE EXISTS
		(SELECT 1 FROM conversations WHERE id=? AND archived_at IS NULL)
		ON CONFLICT(conversation_id,client_message_id) WHERE client_message_id!='' DO UPDATE SET
		content=excluded.content,metadata_json=excluded.metadata_json
		WHERE messages.thread_id=excluded.thread_id AND messages.agent_id=excluded.agent_id AND messages.role=excluded.role
		AND (`+rank("excluded")+`>`+rank("messages")+` OR (`+rank("excluded")+`=`+rank("messages")+`
		AND json_extract(excluded.metadata_json,'$.voice_event_at')>=json_extract(messages.metadata_json,'$.voice_event_at')))
		AND (messages.content!=excluded.content OR messages.metadata_json!=excluded.metadata_json)`,
		m.ConversationID, m.Role, m.Content, m.AgentID, m.UserID, m.ThreadID, string(metadata), m.ClientID,
		at.UTC().Format(time.RFC3339Nano), m.ConversationID)
	if err != nil {
		return nil, false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if n == 0 {
		return nil, false, nil
	}
	if _, err := tx.Exec(`UPDATE conversations SET updated_at=CURRENT_TIMESTAMP WHERE id=?`, m.ConversationID); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	stored, err := s.messageByClientID(m.ConversationID, m.ClientID)
	return stored, true, err
}

func voiceTranscriptText(text string, transcript *voiceTranscript) string {
	if transcript != nil && transcript.Status == "blocked" {
		return ""
	}
	return strings.TrimSpace(text)
}
