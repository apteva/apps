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
	ChannelID  string    `json:"channel_id,omitempty"`
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

func newStore(db *sql.DB) *store {
	db.SetMaxOpenConns(1)
	_, _ = db.Exec(`PRAGMA foreign_keys = ON`)
	return &store{db: db}
}

func defaultChatID(agentID int64) string {
	return fmt.Sprintf("default-%d", agentID)
}

func defaultChatChannelID(agentID int64) string {
	return fmt.Sprintf("chat-default-%d", agentID)
}

func defaultNtfyID(agentID int64) string {
	return fmt.Sprintf("ntfy-default-%d", agentID)
}

func (s *store) EnsureDefaultChat(agentID int64, projectID string) (*Chat, error) {
	conversationID := defaultChatID(agentID)
	channelID := defaultChatChannelID(agentID)
	if err := s.upsertChannel(channelID, projectID, "chat", "Chat", "active", agentID, nil); err != nil {
		return nil, fmt.Errorf("ensure default chat channel: %w", err)
	}
	if err := s.upsertConversation(conversationID, channelID, projectID, agentID, "Chat", ""); err != nil {
		return nil, fmt.Errorf("ensure default chat conversation: %w", err)
	}
	return s.GetChat(conversationID)
}

func (s *store) EnsureDefaultNtfy(agentID int64, projectID string, topic string) (*Chat, error) {
	conversationID := defaultNtfyID(agentID)
	if topic == "" {
		if existing, err := s.GetChat(conversationID); err == nil {
			return existing, nil
		}
		topic = randomTopic(agentID)
	}
	return s.upsertNtfy(conversationID, projectID, agentID, "Ntfy", topic)
}

func (s *store) GetChat(id string) (*Chat, error) {
	var c Chat
	err := s.db.QueryRow(
		`SELECT v.id, v.channel_id, v.agent_id, v.project_id, v.title,
		        ch.type, v.external_thread_id, v.created_at, v.updated_at
		   FROM conversations v
		   JOIN channels ch ON ch.id = v.channel_id
		  WHERE v.id = ?`,
		id,
	).Scan(&c.ID, &c.ChannelID, &c.AgentID, &c.ProjectID, &c.Title, &c.Channel, &c.ThreadID, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	c.InstanceID = c.AgentID
	return &c, nil
}

func (s *store) GetNtfyByTopic(topic string) (*Chat, error) {
	var c Chat
	err := s.db.QueryRow(
		`SELECT v.id, v.channel_id, v.agent_id, v.project_id, v.title,
		        ch.type, v.external_thread_id, v.created_at, v.updated_at
		   FROM conversations v
		   JOIN channels ch ON ch.id = v.channel_id
		  WHERE ch.type = 'ntfy'
		    AND json_extract(ch.config_json, '$.topic') = ?
		  ORDER BY v.created_at ASC
		  LIMIT 1`,
		topic,
	).Scan(&c.ID, &c.ChannelID, &c.AgentID, &c.ProjectID, &c.Title, &c.Channel, &c.ThreadID, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	c.InstanceID = c.AgentID
	return &c, nil
}

func (s *store) SetNtfyTopic(agentID int64, projectID string, topic string) (*Chat, error) {
	if topic == "" {
		topic = randomTopic(agentID)
	}
	return s.upsertNtfy(defaultNtfyID(agentID), projectID, agentID, "Ntfy", topic)
}

func (s *store) UpsertNtfyChannel(agentID int64, projectID, title, topic string) (*Chat, error) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		topic = randomTopic(agentID)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "ntfy"
	}
	conversationID := "ntfy-" + topic
	if existing, err := s.GetNtfyByTopic(topic); err == nil {
		conversationID = existing.ID
	}
	return s.upsertNtfy(conversationID, projectID, agentID, title, topic)
}

func (s *store) upsertNtfy(conversationID, projectID string, agentID int64, title, topic string) (*Chat, error) {
	channelID := conversationID
	if existing, err := s.GetChat(conversationID); err == nil && existing.ChannelID != "" {
		channelID = existing.ChannelID
	}
	if byTopic, err := s.GetNtfyByTopic(topic); err == nil && byTopic.ChannelID != "" {
		channelID = byTopic.ChannelID
		conversationID = byTopic.ID
	}
	if err := s.upsertChannel(channelID, projectID, "ntfy", title, "active", agentID, map[string]any{"topic": topic}); err != nil {
		return nil, fmt.Errorf("upsert ntfy channel: %w", err)
	}
	if err := s.upsertConversation(conversationID, channelID, projectID, agentID, title, topic); err != nil {
		return nil, fmt.Errorf("upsert ntfy conversation: %w", err)
	}
	return s.GetChat(conversationID)
}

func (s *store) upsertChannel(id, projectID, typ, name, status string, defaultAgentID int64, config map[string]any) error {
	if status == "" {
		status = "active"
	}
	if config == nil {
		config = map[string]any{}
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO channels (id, project_id, type, name, status, default_agent_id, config_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   project_id = excluded.project_id,
		   type = excluded.type,
		   name = excluded.name,
		   status = excluded.status,
		   default_agent_id = excluded.default_agent_id,
		   config_json = excluded.config_json,
		   updated_at = CURRENT_TIMESTAMP`,
		id, projectID, typ, name, status, defaultAgentID, string(raw),
	)
	return err
}

func (s *store) upsertConversation(id, channelID, projectID string, agentID int64, title, externalThreadID string) error {
	_, err := s.db.Exec(
		`INSERT INTO conversations (id, channel_id, project_id, agent_id, title, external_thread_id)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   channel_id = excluded.channel_id,
		   project_id = excluded.project_id,
		   agent_id = excluded.agent_id,
		   title = excluded.title,
		   external_thread_id = excluded.external_thread_id,
		   updated_at = CURRENT_TIMESTAMP`,
		id, channelID, projectID, agentID, title, externalThreadID,
	)
	return err
}

func (s *store) ListChannelsForAgent(agentID int64, projectID string) ([]Chat, error) {
	rows, err := s.db.Query(
		`SELECT v.id, v.channel_id, v.agent_id, v.project_id, v.title,
		        ch.type, v.external_thread_id, v.created_at, v.updated_at
		   FROM conversations v
		   JOIN channels ch ON ch.id = v.channel_id
		  WHERE v.project_id = ?
		    AND ch.status = 'active'
		    AND (ch.default_agent_id = 0 OR ch.default_agent_id = ? OR v.agent_id = ?)
		  ORDER BY ch.type ASC, v.updated_at DESC`,
		projectID, agentID, agentID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChats(rows)
}

func (s *store) ListChats(agentID int64, projectID string) ([]Chat, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if agentID > 0 {
		rows, err = s.db.Query(
			`SELECT v.id, v.channel_id, v.agent_id, v.project_id, v.title,
			        ch.type, v.external_thread_id, v.created_at, v.updated_at
			   FROM conversations v
			   JOIN channels ch ON ch.id = v.channel_id
			  WHERE v.agent_id = ?
			  ORDER BY v.created_at ASC`,
			agentID,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT v.id, v.channel_id, v.agent_id, v.project_id, v.title,
			        ch.type, v.external_thread_id, v.created_at, v.updated_at
			   FROM conversations v
			   JOIN channels ch ON ch.id = v.channel_id
			  WHERE v.project_id = ?
			  ORDER BY v.updated_at DESC`,
			projectID,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChats(rows)
}

func (s *store) DeleteChat(id string) (*Chat, error) {
	ch, err := s.GetChat(id)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`DELETE FROM channels WHERE id = ?`, ch.ChannelID); err != nil {
		return nil, err
	}
	return ch, nil
}

func scanChats(rows *sql.Rows) ([]Chat, error) {
	var out []Chat
	for rows.Next() {
		var c Chat
		if err := rows.Scan(&c.ID, &c.ChannelID, &c.AgentID, &c.ProjectID, &c.Title, &c.Channel, &c.ThreadID, &c.CreatedAt, &c.UpdatedAt); err != nil {
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
		`INSERT INTO messages (conversation_id, role, content, user_id, thread_id, status, components_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		chatID, role, content, userID, threadID, status, string(raw),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	_, _ = s.db.Exec(`UPDATE conversations SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, chatID)
	_, _ = s.db.Exec(`UPDATE channels SET updated_at = CURRENT_TIMESTAMP WHERE id = (SELECT channel_id FROM conversations WHERE id = ?)`, chatID)
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
		`SELECT id, conversation_id, role, content, user_id, thread_id, status, created_at, COALESCE(components_json, '[]')
		   FROM messages WHERE id = ?`,
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
		`SELECT id, conversation_id, role, content, user_id, thread_id, status, created_at, COALESCE(components_json, '[]')
		   FROM messages
		  WHERE conversation_id = ? AND id > ?
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
	res, err := s.db.Exec(`DELETE FROM messages WHERE conversation_id = ?`, chatID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *store) LatestID(chatID string) (int64, error) {
	var id sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(id) FROM messages WHERE conversation_id = ?`, chatID).Scan(&id); err != nil {
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
		`UPDATE conversations SET last_seen_id = ? WHERE id = ? AND last_seen_id < ?`,
		lastSeenID, chatID, lastSeenID,
	); err != nil {
		return 0, err
	}
	var current int64
	if err := s.db.QueryRow(`SELECT last_seen_id FROM conversations WHERE id = ?`, chatID).Scan(&current); err != nil {
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
		where = "WHERE v.agent_id = ?"
		args = append(args, agentID)
	case projectID != "":
		where = "WHERE v.project_id = ?"
		args = append(args, projectID)
	default:
		where = ""
	}
	q := `
		SELECT v.id, v.agent_id, v.project_id, v.title,
		       COALESCE(m.id, 0),
		       COALESCE(m.role, ''),
		       COALESCE(m.content, ''),
		       COALESCE(m.created_at, v.updated_at),
		       v.last_seen_id
		  FROM conversations v
		  LEFT JOIN messages m
		    ON m.id = (SELECT MAX(id) FROM messages WHERE conversation_id = v.id)
		` + where + `
		 ORDER BY COALESCE(m.created_at, v.updated_at) DESC`
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
