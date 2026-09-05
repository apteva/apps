package main

// store.go — the one message store every surface reads.
//
// The transcript, the inbox, dashboard stream, and unread counts are all
// views over `messages`. That single-store rule is what makes the
// approval round-trip trustworthy: acting on a card mutates one row and
// every surface agrees instantly.

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const appName = "conversations"

const (
	kindApproval = "approval"
	kindReport   = "report"
	kindAlert    = "alert"
	kindStatus   = "status"
)

type Conversation struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	LeadAgentID     int64  `json:"lead_agent_id"`
	Title           string `json:"title"`
	Kind            string `json:"kind"`   // direct | room
	Origin          string `json:"origin"` // web | agent | app; transports are separate bindings
	ConversationKey string `json:"conversation_key,omitempty"`
	// Audience: "operator" (dashboard, agent topics, operator channels)
	// or "public" (end users behind a gateway). Inbox-kind tools refuse
	// public conversations.
	Audience    string    `json:"audience"`
	Directive   string    `json:"directive,omitempty"`
	ThreadID    string    `json:"thread_id,omitempty"`
	OwnerUserID int64     `json:"owner_user_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Component mirrors the typed cards the dashboard renders (approval-card,
// report-card, alert-card, status-card). Kept schema-compatible with
// channel-chat's shape so the dashboard's existing renderers port over.
type Component struct {
	App   string         `json:"app"`
	Name  string         `json:"name"`
	Props map[string]any `json:"props"`
}

type Message struct {
	Revision       int64          `json:"revision,omitempty"`
	ID             int64          `json:"id"`
	ConversationID string         `json:"conversation_id"`
	Role           string         `json:"role"`
	Content        string         `json:"content"`
	AgentID        int64          `json:"agent_id,omitempty"`
	UserID         int64          `json:"user_id,omitempty"`
	ExternalSender string         `json:"external_sender,omitempty"`
	ThreadID       string         `json:"thread_id,omitempty"`
	Status         string         `json:"status"`
	Phase          string         `json:"phase"`
	ActionStatus   string         `json:"action_status,omitempty"`
	ComponentKind  string         `json:"component_kind,omitempty"`
	Severity       string         `json:"severity,omitempty"`
	InboxOnly      bool           `json:"inbox_only,omitempty"`
	Components     []Component    `json:"components"`
	Attachments    []Attachment   `json:"attachments"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	ClientID       string         `json:"client_message_id,omitempty"`
	SourceApp      string         `json:"source_app,omitempty"`
	CallbackTool   string         `json:"callback_tool,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

type Attachment struct {
	ID        string `json:"id,omitempty"`
	Type      string `json:"type"`
	DataURL   string `json:"data_url,omitempty"`
	Name      string `json:"name,omitempty"`
	MimeType  string `json:"mime_type,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Ephemeral bool   `json:"ephemeral,omitempty"`
}

type store struct{ db *sql.DB }

func newStore(db *sql.DB) *store { return &store{db: db} }

func newConversationID() string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return "conv-" + hex.EncodeToString(buf)
}

// ─── conversations ───────────────────────────────────────────────────

type CreateConversationInput struct {
	ProjectID       string
	LeadAgentID     int64
	Title           string
	Origin          string // '' → web
	ConversationKey string
	OwnerUserID     int64
	// ExternalIdentity optionally adds a gateway- or transport-supplied
	// participant at creation time.
	ExternalIdentity string
	ExternalName     string
	// Audience: '' → operator. See Conversation.Audience.
	Audience  string
	Directive string
	AgentIDs  []int64
}

func (s *store) CreateConversation(in CreateConversationInput) (*Conversation, error) {
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	if in.ProjectID == "" {
		return nil, fmt.Errorf("project_id required")
	}
	if in.LeadAgentID == 0 && in.Origin != "app" {
		return nil, fmt.Errorf("lead_agent_id required")
	}
	if in.Origin == "" {
		in.Origin = "web"
	}
	if in.Title == "" {
		in.Title = "Chat"
	}
	if in.Audience == "" {
		in.Audience = "operator"
	}
	if in.Origin == "agent" && in.ConversationKey == "" {
		in.ConversationKey = fmt.Sprintf("topic:%d:%x", in.LeadAgentID, sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(in.Title)))))
	}
	if len(in.ConversationKey) > 512 {
		return nil, fmt.Errorf("conversation key too long")
	}
	if len([]rune(in.Title)) > 120 || len([]rune(in.Directive)) > 8000 {
		return nil, fmt.Errorf("title or directive too long")
	}
	if in.Audience != "operator" && in.Audience != "public" {
		return nil, fmt.Errorf("invalid audience")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if in.ConversationKey != "" {
		existing, lookupErr := scanConversation(tx.QueryRow(`SELECT `+conversationCols+` FROM conversations WHERE project_id=? AND conversation_key=?`, in.ProjectID, in.ConversationKey))
		if lookupErr == nil {
			var archived bool
			if err := tx.QueryRow(`SELECT archived_at IS NOT NULL FROM conversations WHERE id=?`, existing.ID).Scan(&archived); err != nil {
				return nil, err
			}
			if archived {
				return nil, fmt.Errorf("conversation key belongs to an archived conversation; unarchive it first")
			}
			if in.OwnerUserID > 0 && existing.OwnerUserID != 0 && existing.OwnerUserID != in.OwnerUserID {
				var count int
				if err := tx.QueryRow(`SELECT COUNT(*) FROM participants WHERE conversation_id=? AND user_id=?`, existing.ID, in.OwnerUserID).Scan(&count); err != nil {
					return nil, err
				}
				if count == 0 {
					return nil, fmt.Errorf("conversation not found")
				}
			}
			return existing, nil
		}
		if lookupErr != sql.ErrNoRows {
			return nil, lookupErr
		}
	}
	id, err := insertConversationTx(tx, in)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetConversation(id)
}

func insertConversationTx(tx *sql.Tx, in CreateConversationInput) (string, error) {
	id := newConversationID()
	_, err := tx.Exec(`INSERT INTO conversations(id,project_id,lead_agent_id,title,kind,origin,conversation_key,audience,directive,owner_user_id) VALUES(?,?,?,?,'direct',?,?,?,?,?)`, id, in.ProjectID, in.LeadAgentID, in.Title, in.Origin, in.ConversationKey, in.Audience, in.Directive, in.OwnerUserID)
	if err != nil {
		return "", err
	}
	agents := map[int64]bool{}
	if in.LeadAgentID > 0 {
		agents[in.LeadAgentID] = true
	}
	for _, v := range in.AgentIDs {
		if v <= 0 {
			return "", fmt.Errorf("invalid agent id")
		}
		agents[v] = true
	}
	if len(agents) > 8 {
		return "", fmt.Errorf("at most 8 agents allowed")
	}
	for v := range agents {
		if _, err := tx.Exec(`INSERT INTO participants(conversation_id,agent_id) VALUES(?,?)`, id, v); err != nil {
			return "", err
		}
	}
	if len(agents) > 1 {
		if _, err := tx.Exec(`UPDATE conversations SET kind='room' WHERE id=?`, id); err != nil {
			return "", err
		}
	}
	if in.OwnerUserID > 0 {
		if _, err := tx.Exec(`INSERT INTO participants(conversation_id,user_id) VALUES(?,?)`, id, in.OwnerUserID); err != nil {
			return "", err
		}
	}
	if in.ExternalIdentity != "" {
		if _, err := tx.Exec(`INSERT INTO participants(conversation_id,external_identity,display_name) VALUES(?,?,?)`, id, in.ExternalIdentity, in.ExternalName); err != nil {
			return "", err
		}
	}
	return id, nil
}

const conversationCols = `id, project_id, lead_agent_id, title, kind, origin, conversation_key, audience, directive, thread_id, owner_user_id, created_at, updated_at`

func scanConversation(row interface{ Scan(...any) error }) (*Conversation, error) {
	var c Conversation
	var created, updated string
	if err := row.Scan(&c.ID, &c.ProjectID, &c.LeadAgentID, &c.Title, &c.Kind, &c.Origin,
		&c.ConversationKey, &c.Audience, &c.Directive, &c.ThreadID, &c.OwnerUserID, &created, &updated); err != nil {
		return nil, err
	}
	c.CreatedAt, _ = parseSQLiteTime(created)
	c.UpdatedAt, _ = parseSQLiteTime(updated)
	return &c, nil
}

func (s *store) GetConversation(id string) (*Conversation, error) {
	return scanConversation(s.db.QueryRow(
		`SELECT `+conversationCols+` FROM conversations WHERE id = ? AND archived_at IS NULL`, id))
}

func (s *store) ConversationByKey(projectID, key string) (*Conversation, error) {
	return scanConversation(s.db.QueryRow(
		`SELECT `+conversationCols+` FROM conversations
		 WHERE project_id = ? AND conversation_key = ? AND archived_at IS NULL`, projectID, key))
}

func (s *store) ListConversationsForAgent(projectID string, agentID int64, limit int) ([]Conversation, error) {
	return s.SearchConversationsForAgent(projectID, agentID, "", limit)
}

func (s *store) SearchConversationsForAgent(projectID string, agentID int64, query string, limit int) ([]Conversation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT `+prefixCols("c.", conversationCols)+`
		FROM conversations c
		JOIN participants p ON p.conversation_id = c.id AND p.agent_id = ?
		WHERE c.project_id = ? AND c.archived_at IS NULL AND instr(lower(c.title), lower(?)) > 0
		ORDER BY c.updated_at DESC, c.id DESC LIMIT ?`, agentID, projectID, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *store) ListConversationsForUser(projectID string, userID int64, limit int) ([]Conversation, error) {
	return s.listConversationsForUser(projectID, userID, 0, false, limit)
}

// ListConversationsForUserAndAgent applies the panel's optional agent scope
// after the ordinary user/project visibility check. Participation is the
// authority here, deliberately not lead_agent_id: rooms include every agent
// participant, and a stale lead value cannot keep a removed agent visible.
func (s *store) ListConversationsForUserAndAgent(projectID string, userID, agentID int64, limit int) ([]Conversation, error) {
	return s.listConversationsForUser(projectID, userID, agentID, false, limit)
}

// ListConversationsForUserAndLeadAgent is the focused-chat projection. A
// matching lead id is not sufficient by itself: the agent must remain an
// explicit participant so a stale lead value cannot keep a removed agent's
// conversation visible.
func (s *store) ListConversationsForUserAndLeadAgent(projectID string, userID, leadAgentID int64, limit int) ([]Conversation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT DISTINCT `+prefixCols("c.", conversationCols)+`
		FROM conversations c
		LEFT JOIN participants p ON p.conversation_id = c.id
		WHERE c.project_id = ? AND c.archived_at IS NULL
		  AND c.lead_agent_id = ?
		  AND EXISTS (
		      SELECT 1 FROM participants ap
		      WHERE ap.conversation_id = c.id AND ap.agent_id = ?
		  )
		  AND (c.owner_user_id = ? OR p.user_id = ? OR c.owner_user_id = 0)
		ORDER BY c.updated_at DESC, c.id DESC LIMIT ?`,
		projectID, leadAgentID, leadAgentID, userID, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *store) listConversationsForUser(projectID string, userID, agentID int64, archived bool, limit int) ([]Conversation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	archivePredicate := "c.archived_at IS NULL"
	if archived {
		archivePredicate = "c.archived_at IS NOT NULL"
	}
	// Owner OR explicit participant. External-origin conversations have
	// owner 0 and surface to every project member through the dashboard
	// list endpoint (authz there is the platform's, not ours).
	rows, err := s.db.Query(`
		SELECT DISTINCT `+prefixCols("c.", conversationCols)+`
		FROM conversations c
		LEFT JOIN participants p ON p.conversation_id = c.id
		WHERE c.project_id = ? AND `+archivePredicate+`
		  AND (c.owner_user_id = ? OR p.user_id = ? OR c.owner_user_id = 0)
		  AND (? = 0 OR EXISTS (
		      SELECT 1 FROM participants ap
		      WHERE ap.conversation_id = c.id AND ap.agent_id = ?
		  ))
		ORDER BY c.updated_at DESC LIMIT ?`, projectID, userID, userID, agentID, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// FindAgentConversationByTitle backs conversations_create's title
// idempotency: an exact (case-insensitive) title match among the
// agent's unarchived conversations, newest activity first. Nil when
// none matches — that is not an error.
func (s *store) FindAgentConversationByTitle(projectID string, agentID int64, title string) (*Conversation, error) {
	c, err := scanConversation(s.db.QueryRow(`
		SELECT DISTINCT `+prefixCols("c.", conversationCols)+`
		FROM conversations c
		JOIN participants p ON p.conversation_id = c.id
		WHERE c.project_id = ? AND c.archived_at IS NULL AND p.agent_id = ? AND lower(c.title) = lower(?)
		ORDER BY c.updated_at DESC LIMIT 1`, projectID, agentID, title))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ConversationForAgentThread resolves the conversation that owns an opaque
// Core thread for one agent. The relationship is app data, not a platform
// thread role: room participants each have their own row, while the legacy
// conversations.thread_id fallback keeps pre-v0.14 lead-agent threads bound.
// Archived conversations remain authoritative owners until they are deleted,
// so an old live thread cannot silently become an unbound global caller.
func (s *store) ConversationForAgentThread(projectID string, agentID int64, threadID string) (*Conversation, error) {
	threadID = strings.TrimSpace(threadID)
	if strings.TrimSpace(projectID) == "" || agentID <= 0 || threadID == "" {
		return nil, nil
	}
	conv, err := scanConversation(s.db.QueryRow(`
		SELECT `+prefixCols("c.", conversationCols)+`
		FROM conversation_agent_threads t
		JOIN conversations c ON c.id = t.conversation_id
		WHERE c.project_id = ? AND t.agent_id = ? AND t.thread_id = ?
		LIMIT 1`, projectID, agentID, threadID))
	if err == nil {
		return conv, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	conv, err = scanConversation(s.db.QueryRow(`
		SELECT `+conversationCols+`
		FROM conversations
		WHERE project_id = ? AND lead_agent_id = ? AND thread_id = ?
		LIMIT 1`, projectID, agentID, threadID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return conv, nil
}

func (s *store) IsParticipantAgent(conversationID string, agentID int64) (bool, error) {
	var one int
	err := s.db.QueryRow(`
		SELECT 1 FROM participants WHERE conversation_id = ? AND agent_id = ?`,
		conversationID, agentID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (s *store) changeAgentParticipant(id string, agent int64, remove bool) error {
	if agent <= 0 {
		return fmt.Errorf("invalid agent id")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var lead int64
	var archived bool
	if err := tx.QueryRow(`SELECT lead_agent_id,archived_at IS NOT NULL FROM conversations WHERE id=?`, id).Scan(&lead, &archived); err != nil {
		return err
	}
	if archived {
		return fmt.Errorf("conversation is archived")
	}
	if remove {
		if lead == agent {
			return fmt.Errorf("cannot remove the lead agent")
		}
		if _, err := tx.Exec(`DELETE FROM participants WHERE conversation_id=? AND agent_id=?`, id, agent); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE deliveries SET status='cancelled',lease_token='',last_error='Agent removed from conversation' WHERE target=? AND message_id IN(SELECT id FROM messages WHERE conversation_id=?) AND status IN('pending','processing')`, fmt.Sprintf("agent-inbound:%d", agent), id); err != nil {
			return err
		}
	} else {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM participants WHERE conversation_id=? AND agent_id>0 AND agent_id!=?`, id, agent).Scan(&count); err != nil {
			return err
		}
		if count >= 8 {
			return fmt.Errorf("at most 8 agents allowed")
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO participants(conversation_id,agent_id) VALUES(?,?)`, id, agent); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE conversations SET kind=CASE WHEN (SELECT COUNT(*) FROM participants WHERE conversation_id=? AND agent_id>0)>1 THEN 'room' ELSE 'direct' END,updated_at=CURRENT_TIMESTAMP WHERE id=?`, id, id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *store) AddAgentParticipant(id string, agent int64) error {
	return s.changeAgentParticipant(id, agent, false)
}
func (s *store) RemoveAgentParticipant(id string, agent int64) error {
	return s.changeAgentParticipant(id, agent, true)
}

func (s *store) AgentParticipants(conversationID string) ([]int64, error) {
	rows, err := s.db.Query(`
		SELECT agent_id FROM participants
		WHERE conversation_id = ? AND agent_id != 0 ORDER BY added_at, agent_id`,
		conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// getConversationAny fetches regardless of archive state — the
// unarchive path has to be able to see the row GetConversation hides.
func (s *store) getConversationAny(id string) (*Conversation, error) {
	return scanConversation(s.db.QueryRow(
		`SELECT `+conversationCols+` FROM conversations WHERE id = ?`, id))
}

func (s *store) UpdateConversationTitle(id, title string) (*Conversation, error) {
	if len([]rune(title)) > 120 {
		return nil, fmt.Errorf("title is too long")
	}
	res, err := s.db.Exec(`
		UPDATE conversations SET title = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND archived_at IS NULL`,
		title, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("conversation not found")
	}
	return s.getConversationAny(id)
}

func (s *store) UpdateConversationDirective(id, directive string) (*Conversation, error) {
	if len([]rune(directive)) > 8000 {
		return nil, fmt.Errorf("directive is too long")
	}
	res, err := s.db.Exec(`UPDATE conversations SET directive=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND archived_at IS NULL`, directive, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("conversation not found")
	}
	return s.getConversationAny(id)
}

func (s *store) SetConversationArchived(id string, archived bool) (*Conversation, error) {
	expr := "CURRENT_TIMESTAMP"
	if !archived {
		expr = "NULL"
	}
	res, err := s.db.Exec(`
		UPDATE conversations SET archived_at = `+expr+`, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("conversation not found")
	}
	return s.getConversationAny(id)
}

func (s *store) ListArchivedForUser(projectID string, userID int64, limit int) ([]Conversation, error) {
	return s.listConversationsForUser(projectID, userID, 0, true, limit)
}

func (s *store) ListArchivedForUserAndAgent(projectID string, userID, agentID int64, limit int) ([]Conversation, error) {
	return s.listConversationsForUser(projectID, userID, agentID, true, limit)
}

// UserCanAccessConversation is the resource-level authorization check used by
// every HTTP read and mutation. The platform proxy has already established
// project membership; this narrows access to the selected project and either
// the owning user, an explicit user participant, or a project-visible system
// conversation (owner 0).
func (s *store) UserCanAccessConversation(conversationID, projectID string, userID int64) (bool, error) {
	if strings.TrimSpace(projectID) == "" || userID <= 0 {
		return false, nil
	}
	var one int
	err := s.db.QueryRow(`
		SELECT 1
		FROM conversations c
		LEFT JOIN participants p ON p.conversation_id = c.id AND p.user_id = ?
		WHERE c.id = ? AND c.project_id = ?
		  AND (c.owner_user_id = ? OR p.user_id = ? OR c.owner_user_id = 0)
		LIMIT 1`, userID, conversationID, projectID, userID, userID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// DeleteConversation removes the conversation and every dependent row.
// Children go explicitly — FK cascade depends on a pragma the SDK's DB
// open may not set.
func (s *store) DeleteConversation(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		DELETE FROM deliveries WHERE message_id IN
			(SELECT id FROM messages WHERE conversation_id = ?)`, id); err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM messages WHERE conversation_id = ?`,
		`DELETE FROM participants WHERE conversation_id = ?`,
		`DELETE FROM read_marks WHERE conversation_id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	res, err := tx.Exec(`DELETE FROM conversations WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("conversation not found")
	}
	return tx.Commit()
}

// ─── messages ────────────────────────────────────────────────────────

// AppendMessage inserts a message. When ClientID is set and a row with
// the same (conversation, client id) exists, the existing row is
// returned with inserted=false — idempotent sends survive retries and
// remounts.
func (s *store) AppendMessage(m *Message) (*Message, error) {
	msg, _, err := s.AppendMessageIdempotent(m)
	return msg, err
}

func (s *store) AppendMessageIdempotent(m *Message) (*Message, bool, error) {
	return s.AppendMessageWithDeliveries(m, nil)
}

func normalizeMessage(m *Message) {
	if m.Status == "" {
		m.Status = "final"
	}
	if m.Phase == "" {
		m.Phase = "final"
	}
	if m.ComponentKind == kindApproval && m.ActionStatus == "" {
		m.ActionStatus = "pending"
	}
	if m.Components == nil {
		m.Components = []Component{}
	}
	if m.Attachments == nil {
		m.Attachments = []Attachment{}
	}
	if m.Metadata == nil {
		m.Metadata = map[string]any{}
	}
}

// AppendMessageWithDeliveries commits the durable message and every initial
// outbox row in one transaction. There is therefore no state in which a
// message exists but a crash recovery scan has nothing to replay.
func (s *store) AppendMessageWithDeliveries(m *Message, targets []string) (*Message, bool, error) {
	normalizeMessage(m)
	if err := validateMessageSize(m); err != nil {
		return nil, false, err
	}
	request := *m
	request.ID = 0
	request.CreatedAt = time.Time{}
	request.Revision = 0
	fingerprint, _ := json.Marshal(&request)
	requestHash := fmt.Sprintf("%x", sha256.Sum256(fingerprint))
	componentsJSON, err := json.Marshal(m.Components)
	if err != nil {
		return nil, false, err
	}
	attachmentsJSON, err := json.Marshal(m.Attachments)
	if err != nil {
		return nil, false, err
	}
	metadataJSON, err := json.Marshal(m.Metadata)
	if err != nil {
		return nil, false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	if m.ClientID != "" {
		var existingID int64
		var existingHash string
		if err := tx.QueryRow(`SELECT id,request_hash FROM messages WHERE conversation_id=? AND client_message_id=?`, m.ConversationID, m.ClientID).Scan(&existingID, &existingHash); err == nil {
			if existingHash != "" && existingHash != requestHash {
				return nil, false, fmt.Errorf("client_message_id already belongs to a different request")
			}
			_ = tx.Rollback()
			existing, lookupErr := s.GetMessage(existingID)
			if lookupErr == nil {
				lookupErr = validateDuplicateMessage(existing, m)
			}
			return existing, false, lookupErr
		} else if err != sql.ErrNoRows {
			return nil, false, err
		}
	}
	var active int
	if err := tx.QueryRow(`SELECT 1 FROM conversations WHERE id=? AND archived_at IS NULL`, m.ConversationID).Scan(&active); err != nil {
		return nil, false, fmt.Errorf("conversation is archived or unavailable")
	}
	res, err := tx.Exec(`
		INSERT INTO messages (conversation_id, role, content, agent_id, user_id, external_sender,
			thread_id, status, phase, action_status, component_kind, severity, inbox_only, components_json,
			attachments_json, metadata_json, client_message_id, source_app, callback_tool)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ConversationID, m.Role, m.Content, m.AgentID, m.UserID, m.ExternalSender,
		m.ThreadID, m.Status, m.Phase, m.ActionStatus, m.ComponentKind, m.Severity, boolToInt(m.InboxOnly),
		string(componentsJSON), string(attachmentsJSON), string(metadataJSON), m.ClientID, m.SourceApp, m.CallbackTool)
	if err != nil {
		if m.ClientID != "" && strings.Contains(err.Error(), "UNIQUE") {
			_ = tx.Rollback()
			existing, lookupErr := s.messageByClientID(m.ConversationID, m.ClientID)
			return existing, false, lookupErr
		}
		return nil, false, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.Exec(`UPDATE messages SET request_hash=? WHERE id=?`, requestHash, id); err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(`UPDATE conversations SET updated_at=CURRENT_TIMESTAMP WHERE id=?`, m.ConversationID); err != nil {
		return nil, false, err
	}
	seen := map[string]bool{}
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" || seen[target] {
			continue
		}
		seen[target] = true
		// Ownership commits with the inbound job, before any async execution.
		if raw, ok := strings.CutPrefix(target, "agent-inbound:"); ok {
			agentID, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || agentID <= 0 {
				return nil, false, fmt.Errorf("invalid inbound agent")
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO conversation_agent_threads(conversation_id,agent_id,thread_id) VALUES(?,?,?)`, m.ConversationID, agentID, conversationThreadID(m.ConversationID)); err != nil {
				return nil, false, err
			}
		}
		if _, err := tx.Exec(`INSERT INTO deliveries (message_id,target,next_attempt_at,updated_at)
			VALUES (?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, id, target); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	stored, err := s.GetMessage(id)
	return stored, true, err
}

const messageCols = `id, conversation_id, role, content, agent_id, user_id, external_sender, thread_id,
	status, phase, action_status, component_kind, severity, inbox_only, components_json, attachments_json,
	metadata_json, client_message_id, source_app, callback_tool, created_at, revision`

func scanMessage(row interface{ Scan(...any) error }) (*Message, error) {
	var m Message
	var inboxOnly int
	var componentsJSON, attachmentsJSON, metadataJSON, created string
	if err := row.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Content, &m.AgentID, &m.UserID,
		&m.ExternalSender, &m.ThreadID, &m.Status, &m.Phase, &m.ActionStatus, &m.ComponentKind, &m.Severity, &inboxOnly,
		&componentsJSON, &attachmentsJSON, &metadataJSON, &m.ClientID, &m.SourceApp, &m.CallbackTool, &created, &m.Revision); err != nil {
		return nil, err
	}
	m.InboxOnly = inboxOnly != 0
	m.Components = []Component{}
	_ = json.Unmarshal([]byte(componentsJSON), &m.Components)
	m.Attachments = []Attachment{}
	_ = json.Unmarshal([]byte(attachmentsJSON), &m.Attachments)
	m.Metadata = map[string]any{}
	_ = json.Unmarshal([]byte(metadataJSON), &m.Metadata)
	m.CreatedAt, _ = parseSQLiteTime(created)
	return &m, nil
}

func (s *store) GetMessage(id int64) (*Message, error) {
	return scanMessage(s.db.QueryRow(`SELECT `+messageCols+` FROM messages WHERE id = ?`, id))
}

func (s *store) messageByClientID(conversationID, clientID string) (*Message, error) {
	return scanMessage(s.db.QueryRow(`
		SELECT `+messageCols+` FROM messages
		WHERE conversation_id = ? AND client_message_id = ?`, conversationID, clientID))
}

// Transcript excludes inbox-only rows. Since 0.5.1 nothing new sets
// the flag — items are visible in the conversation they live in — but
// the filter stays so legacy inbox-only rows keep their old behavior.
func (s *store) Transcript(conversationID string, sinceID int64, limit int) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT `+messageCols+` FROM messages
		WHERE conversation_id = ? AND id > ? AND inbox_only = 0
		ORDER BY id ASC LIMIT ?`, conversationID, sinceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (s *store) UpdateMessageComponents(id int64, components []Component) (*Message, error) {
	encoded, err := json.Marshal(components)
	if err != nil {
		return nil, err
	}
	res, err := s.db.Exec(`UPDATE messages SET components_json = ? WHERE id = ? AND conversation_id IN (SELECT id FROM conversations WHERE archived_at IS NULL)`, string(encoded), id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("conversation unavailable or archived")
	}
	return s.GetMessage(id)
}

// ResolveApproval atomically performs the pending -> verdict transition and
// enqueues the result delivery. A competing click can never overwrite the
// first verdict or notify the requester twice.
func (s *store) ResolveApproval(id int64, components []Component, verdict, resultTarget string) (*Message, error) {
	encoded, err := json.Marshal(components)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`
		UPDATE messages
		SET components_json=?, action_status=?
		WHERE id=? AND component_kind=? AND action_status='pending' AND conversation_id IN (SELECT id FROM conversations WHERE archived_at IS NULL)`, string(encoded), verdict, id, kindApproval)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("approval already resolved")
	}
	if resultTarget != "" {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO deliveries (message_id,target,next_attempt_at,updated_at)
			VALUES (?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, id, resultTarget); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetMessage(id)
}

// ─── read marks ──────────────────────────────────────────────────────

func (s *store) MarkSeen(userID int64, conversationID string, lastSeenID int64) error {
	_, err := s.db.Exec(`
		INSERT INTO read_marks (user_id, conversation_id, last_seen_id)
		SELECT ?, ?, MIN(?, COALESCE(MAX(id), 0)) FROM messages WHERE conversation_id = ?
		ON CONFLICT (user_id, conversation_id) DO UPDATE SET
			last_seen_id = MAX(read_marks.last_seen_id, excluded.last_seen_id)`,
		userID, conversationID, lastSeenID, conversationID)
	return err
}

type UnreadEntry struct {
	ConversationID string `json:"conversation_id"`
	LatestID       int64  `json:"latest_id"`
	LastSeenID     int64  `json:"last_seen_id"`
	Unread         int64  `json:"unread"`
}

func (s *store) UnreadSummary(projectID string, userID int64) ([]UnreadEntry, error) {
	return s.UnreadSummaryForAgent(projectID, userID, 0)
}

func (s *store) UnreadSummaryForAgent(projectID string, userID, agentID int64) ([]UnreadEntry, error) {
	rows, err := s.db.Query(`
		SELECT c.id,
		       COALESCE((SELECT MAX(m.id) FROM messages m
		                 WHERE m.conversation_id = c.id AND m.inbox_only = 0), 0),
		       COALESCE(r.last_seen_id, 0),
		       (SELECT COUNT(*) FROM messages m
		        WHERE m.conversation_id = c.id AND m.inbox_only = 0
		          AND m.id > COALESCE(r.last_seen_id, 0) AND m.role != 'user')
		FROM conversations c
		LEFT JOIN read_marks r ON r.conversation_id = c.id AND r.user_id = ?
		LEFT JOIN participants p ON p.conversation_id = c.id AND p.user_id = ?
		WHERE c.project_id = ? AND c.archived_at IS NULL
		  AND (c.owner_user_id = ? OR p.user_id = ? OR c.owner_user_id = 0)
		  AND (? = 0 OR EXISTS (
		      SELECT 1 FROM participants ap
		      WHERE ap.conversation_id = c.id AND ap.agent_id = ?
		  ))`,
		userID, userID, projectID, userID, userID, agentID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnreadEntry
	for rows.Next() {
		var e UnreadEntry
		if err := rows.Scan(&e.ConversationID, &e.LatestID, &e.LastSeenID, &e.Unread); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ─── helpers ─────────────────────────────────────────────────────────

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func prefixCols(prefix, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

func parseSQLiteTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable time %q", raw)
}

// SetConversationThread records the spawned core thread for a
// conversation. Written once on first successful spawn; the id is
// deterministic, so a rewrite with the same value is harmless.
func (s *store) SetConversationThread(conversationID, threadID string) error {
	_, err := s.db.Exec(`UPDATE conversations SET thread_id = ? WHERE id = ?`, threadID, conversationID)
	return err
}
