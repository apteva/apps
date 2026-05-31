package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var errNotFound = errors.New("channels: not found")

type ChatComponent struct {
	App   string         `json:"app"`
	Name  string         `json:"name"`
	Props map[string]any `json:"props,omitempty"`
}

type Chat struct {
	ID         string    `json:"id"`
	AgentID    int64     `json:"agent_id"`
	InstanceID int64     `json:"instance_id"`
	ProjectID  string    `json:"project_id"`
	Title      string    `json:"title"`
	Channel    string    `json:"channel"`
	ThreadID   string    `json:"thread_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Message struct {
	ID         int64           `json:"id"`
	ChatID     string          `json:"chat_id"`
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	UserID     *int64          `json:"user_id,omitempty"`
	ThreadID   string          `json:"thread_id,omitempty"`
	Status     string          `json:"status"`
	CreatedAt  time.Time       `json:"created_at"`
	Components []ChatComponent `json:"components"`
}

type ChatLatest struct {
	ChatID        string    `json:"chat_id"`
	AgentID       int64     `json:"agent_id"`
	InstanceID    int64     `json:"instance_id"`
	AgentName     string    `json:"agent_name"`
	InstanceName  string    `json:"instance_name"`
	ProjectID     string    `json:"project_id"`
	Title         string    `json:"title"`
	LatestID      int64     `json:"latest_id"`
	LatestRole    string    `json:"latest_role"`
	LatestPreview string    `json:"latest_preview"`
	LatestAt      time.Time `json:"latest_at"`
	LastSeenID    int64     `json:"last_seen_id"`
}

type store struct {
	db *sql.DB
}

func newStore(db *sql.DB) *store { return &store{db: db} }

func defaultChatID(agentID int64) string {
	return fmt.Sprintf("default-%d", agentID)
}

func (s *store) EnsureDefaultChat(agentID int64, projectID string) (*Chat, error) {
	chatID := defaultChatID(agentID)
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO channels_chats (id, agent_id, project_id, title, channel)
		 VALUES (?, ?, ?, 'Chat', 'chat')`,
		chatID, agentID, projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("ensure default chat: %w", err)
	}
	if projectID != "" {
		_, _ = s.db.Exec(`UPDATE channels_chats SET project_id = ? WHERE id = ? AND project_id = ''`, projectID, chatID)
	}
	return s.GetChat(chatID)
}

func (s *store) GetChat(id string) (*Chat, error) {
	var c Chat
	err := s.db.QueryRow(
		`SELECT id, agent_id, project_id, title, channel, thread_id, created_at, updated_at
		 FROM channels_chats WHERE id = ?`,
		id,
	).Scan(&c.ID, &c.AgentID, &c.ProjectID, &c.Title, &c.Channel, &c.ThreadID, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	c.InstanceID = c.AgentID
	return &c, nil
}

func (s *store) ListChats(agentID int64, projectID string) ([]Chat, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if agentID > 0 {
		rows, err = s.db.Query(
			`SELECT id, agent_id, project_id, title, channel, thread_id, created_at, updated_at
			 FROM channels_chats WHERE agent_id = ? ORDER BY created_at ASC`,
			agentID,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, agent_id, project_id, title, channel, thread_id, created_at, updated_at
			 FROM channels_chats WHERE project_id = ? ORDER BY updated_at DESC`,
			projectID,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chat
	for rows.Next() {
		var c Chat
		if err := rows.Scan(&c.ID, &c.AgentID, &c.ProjectID, &c.Title, &c.Channel, &c.ThreadID, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.InstanceID = c.AgentID
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *store) Append(chatID, role, content string, userID *int64, threadID, status string, components []ChatComponent) (*Message, error) {
	if role != "user" && role != "agent" && role != "system" {
		return nil, fmt.Errorf("invalid role %q", role)
	}
	if strings.TrimSpace(content) == "" {
		return nil, errors.New("content required")
	}
	if status == "" {
		status = "final"
	}
	if components == nil {
		components = []ChatComponent{}
	}
	raw, err := json.Marshal(components)
	if err != nil {
		return nil, err
	}
	res, err := s.db.Exec(
		`INSERT INTO channels_messages (chat_id, role, content, user_id, thread_id, status, components_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		chatID, role, content, userID, threadID, status, string(raw),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	_, _ = s.db.Exec(`UPDATE channels_chats SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, chatID)
	return s.GetMessage(id)
}

func (s *store) GetMessage(id int64) (*Message, error) {
	var (
		m          Message
		userID     sql.NullInt64
		threadID   sql.NullString
		components string
	)
	err := s.db.QueryRow(
		`SELECT id, chat_id, role, content, user_id, thread_id, status, created_at, COALESCE(components_json, '[]')
		 FROM channels_messages WHERE id = ?`,
		id,
	).Scan(&m.ID, &m.ChatID, &m.Role, &m.Content, &userID, &threadID, &m.Status, &m.CreatedAt, &components)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if userID.Valid {
		v := userID.Int64
		m.UserID = &v
	}
	if threadID.Valid {
		m.ThreadID = threadID.String
	}
	m.Components = decodeComponents(components)
	return &m, nil
}

func (s *store) ListMessages(chatID string, since int64, limit int) ([]Message, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.db.Query(
		`SELECT id, chat_id, role, content, user_id, thread_id, status, created_at, COALESCE(components_json, '[]')
		 FROM channels_messages
		 WHERE chat_id = ? AND id > ?
		 ORDER BY id ASC
		 LIMIT ?`,
		chatID, since, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var (
			m          Message
			userID     sql.NullInt64
			threadID   sql.NullString
			components string
		)
		if err := rows.Scan(&m.ID, &m.ChatID, &m.Role, &m.Content, &userID, &threadID, &m.Status, &m.CreatedAt, &components); err != nil {
			return nil, err
		}
		if userID.Valid {
			v := userID.Int64
			m.UserID = &v
		}
		if threadID.Valid {
			m.ThreadID = threadID.String
		}
		m.Components = decodeComponents(components)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *store) DeleteMessages(chatID string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM channels_messages WHERE chat_id = ?`, chatID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *store) LatestID(chatID string) (int64, error) {
	var id sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(id) FROM channels_messages WHERE chat_id = ?`, chatID).Scan(&id); err != nil {
		return 0, err
	}
	if !id.Valid {
		return 0, nil
	}
	return id.Int64, nil
}

func (s *store) MarkSeen(chatID string, lastSeenID int64) (int64, error) {
	maxID, err := s.LatestID(chatID)
	if err != nil {
		return 0, err
	}
	if lastSeenID > maxID {
		lastSeenID = maxID
	}
	if _, err := s.db.Exec(
		`UPDATE channels_chats SET last_seen_id = ? WHERE id = ? AND last_seen_id < ?`,
		lastSeenID, chatID, lastSeenID,
	); err != nil {
		return 0, err
	}
	var current int64
	if err := s.db.QueryRow(`SELECT last_seen_id FROM channels_chats WHERE id = ?`, chatID).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errNotFound
		}
		return 0, err
	}
	return current, nil
}

func (s *store) Latest(projectID string, agentID int64) ([]ChatLatest, error) {
	var (
		where string
		args  []any
	)
	switch {
	case agentID > 0:
		where = "WHERE c.agent_id = ?"
		args = append(args, agentID)
	case projectID != "":
		where = "WHERE c.project_id = ?"
		args = append(args, projectID)
	default:
		where = ""
	}
	q := `
		SELECT c.id, c.agent_id, c.project_id, c.title,
		       COALESCE(m.id, 0),
		       COALESCE(m.role, ''),
		       COALESCE(m.content, ''),
		       COALESCE(m.created_at, c.updated_at),
		       c.last_seen_id
		FROM channels_chats c
		LEFT JOIN channels_messages m
			ON m.id = (SELECT MAX(id) FROM channels_messages WHERE chat_id = c.id)
		` + where + `
		ORDER BY COALESCE(m.created_at, c.updated_at) DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChatLatest
	for rows.Next() {
		var cl ChatLatest
		var ts string
		if err := rows.Scan(&cl.ChatID, &cl.AgentID, &cl.ProjectID, &cl.Title, &cl.LatestID, &cl.LatestRole, &cl.LatestPreview, &ts, &cl.LastSeenID); err != nil {
			return nil, err
		}
		cl.InstanceID = cl.AgentID
		cl.LatestAt, _ = parseSQLiteTime(ts)
		if len(cl.LatestPreview) > 200 {
			cl.LatestPreview = cl.LatestPreview[:200]
		}
		out = append(out, cl)
	}
	return out, rows.Err()
}

func decodeComponents(raw string) []ChatComponent {
	if raw == "" {
		return []ChatComponent{}
	}
	var out []ChatComponent
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return []ChatComponent{}
	}
	return out
}

func parseSQLiteTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time format: %q", s)
}
