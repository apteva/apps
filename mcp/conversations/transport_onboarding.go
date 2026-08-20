package main

// transport_onboarding.go owns provider-neutral intake, access-request, and
// invitation state. Adapters translate their native identities into these
// records, then route accepted traffic through the ordinary conversation store.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const telegramTransport = "telegram"

type TransportIntakePolicy struct {
	Transport           string `json:"transport"`
	ConnectionID        int64  `json:"connection_id"`
	ProjectID           string `json:"project_id"`
	Mode                string `json:"mode"` // pairing | public | closed
	DefaultAgentID      int64  `json:"default_agent_id"`
	DefaultTitle        string `json:"default_title"`
	RequireGroupMention bool   `json:"require_group_mention"`
	CreatedByUserID     int64  `json:"-"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
}

type TransportAccessRequest struct {
	ID             string     `json:"id"`
	Transport      string     `json:"transport"`
	ConnectionID   int64      `json:"connection_id"`
	ProjectID      string     `json:"project_id"`
	ExternalChatID string     `json:"external_chat_id"`
	ExternalUserID string     `json:"external_user_id"`
	ChatType       string     `json:"chat_type"`
	DisplayName    string     `json:"display_name"`
	Username       string     `json:"username,omitempty"`
	ChatTitle      string     `json:"chat_title,omitempty"`
	PairingCode    string     `json:"pairing_code"`
	State          string     `json:"state"`
	ConversationID string     `json:"conversation_id,omitempty"`
	NotifiedAt     *time.Time `json:"notified_at,omitempty"`
	ExpiresAt      time.Time  `json:"expires_at"`
	CreatedAt      string     `json:"created_at"`
	UpdatedAt      string     `json:"updated_at"`
}

type TransportInvite struct {
	ID              string
	TokenHash       string
	Transport       string
	ConnectionID    int64
	ProjectID       string
	ConversationID  string
	Audience        string
	ChatType        string
	DefaultAgentID  int64
	CreatedByUserID int64
	ExpiresAt       time.Time
	UsedAt          *time.Time
}

func normalizeIntakeMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "pairing", "public", "closed":
		return mode, nil
	default:
		return "", fmt.Errorf("mode must be pairing, public, or closed")
	}
}

func (s *store) UpsertTransportIntakePolicy(policy TransportIntakePolicy) (*TransportIntakePolicy, error) {
	mode, err := normalizeIntakeMode(policy.Mode)
	if err != nil {
		return nil, err
	}
	policy.Transport = strings.TrimSpace(policy.Transport)
	policy.ProjectID = strings.TrimSpace(policy.ProjectID)
	policy.DefaultTitle = strings.TrimSpace(policy.DefaultTitle)
	if policy.Transport == "" || policy.ConnectionID <= 0 || policy.ProjectID == "" {
		return nil, errors.New("transport, connection_id, and project_id are required")
	}
	if policy.DefaultAgentID <= 0 {
		return nil, errors.New("default_agent_id is required")
	}
	if policy.DefaultTitle == "" {
		policy.DefaultTitle = "New conversation"
	}
	if len([]rune(policy.DefaultTitle)) > 120 {
		return nil, errors.New("default_title is too long")
	}
	_, err = s.db.Exec(`
		INSERT INTO transport_intake_policies
		(transport,connection_id,project_id,mode,default_agent_id,default_title,require_group_mention,created_by_user_id,updated_at)
		VALUES (?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT(transport,connection_id) DO UPDATE SET
			project_id=excluded.project_id,mode=excluded.mode,
			default_agent_id=excluded.default_agent_id,default_title=excluded.default_title,
			require_group_mention=excluded.require_group_mention,updated_at=CURRENT_TIMESTAMP`,
		policy.Transport, policy.ConnectionID, policy.ProjectID, mode, policy.DefaultAgentID,
		policy.DefaultTitle, policy.RequireGroupMention, policy.CreatedByUserID)
	if err != nil {
		return nil, err
	}
	return s.GetTransportIntakePolicy(policy.Transport, policy.ConnectionID)
}

func (s *store) GetTransportIntakePolicy(transport string, connectionID int64) (*TransportIntakePolicy, error) {
	var policy TransportIntakePolicy
	err := s.db.QueryRow(`
		SELECT transport,connection_id,project_id,mode,default_agent_id,default_title,
		       require_group_mention,created_by_user_id,created_at,updated_at
		FROM transport_intake_policies WHERE transport=? AND connection_id=?`, transport, connectionID).Scan(
		&policy.Transport, &policy.ConnectionID, &policy.ProjectID, &policy.Mode,
		&policy.DefaultAgentID, &policy.DefaultTitle, &policy.RequireGroupMention,
		&policy.CreatedByUserID, &policy.CreatedAt, &policy.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &policy, nil
}

func newPairingCode() (string, error) {
	raw, err := randomTelegramSecret(6)
	if err != nil {
		return "", err
	}
	raw = strings.ToUpper(strings.NewReplacer("-", "", "_", "").Replace(raw))
	if len(raw) > 8 {
		raw = raw[:8]
	}
	return raw, nil
}

func scanTransportAccessRequest(scanner interface{ Scan(...any) error }) (*TransportAccessRequest, error) {
	var req TransportAccessRequest
	var notified sql.NullTime
	err := scanner.Scan(&req.ID, &req.Transport, &req.ConnectionID, &req.ProjectID,
		&req.ExternalChatID, &req.ExternalUserID, &req.ChatType, &req.DisplayName,
		&req.Username, &req.ChatTitle, &req.PairingCode, &req.State,
		&req.ConversationID, &notified, &req.ExpiresAt, &req.CreatedAt, &req.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if notified.Valid {
		req.NotifiedAt = &notified.Time
	}
	return &req, nil
}

const transportAccessColumns = `id,transport,connection_id,project_id,external_chat_id,
	external_user_id,chat_type,display_name,username,chat_title,pairing_code,state,
	conversation_id,notified_at,expires_at,created_at,updated_at`

func (s *store) EnsureTransportAccessRequest(input TransportAccessRequest) (*TransportAccessRequest, bool, error) {
	now := time.Now().UTC()
	existing, err := scanTransportAccessRequest(s.db.QueryRow(`SELECT `+transportAccessColumns+`
		FROM transport_access_requests
		WHERE transport=? AND connection_id=? AND external_chat_id=? AND external_user_id=?`,
		input.Transport, input.ConnectionID, input.ExternalChatID, input.ExternalUserID))
	if err == nil && existing.State == "blocked" {
		return existing, false, nil
	}
	if err == nil && existing.State == "pending" && existing.ExpiresAt.After(now) {
		_, _ = s.db.Exec(`UPDATE transport_access_requests SET
			display_name=?,username=?,chat_title=?,chat_type=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			input.DisplayName, input.Username, input.ChatTitle, input.ChatType, existing.ID)
		refreshed, refreshErr := s.GetTransportAccessRequest(existing.ID)
		return refreshed, existing.NotifiedAt == nil, refreshErr
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	code, err := newPairingCode()
	if err != nil {
		return nil, false, err
	}
	if input.ID == "" {
		input.ID, err = randomTelegramSecret(12)
		if err != nil {
			return nil, false, err
		}
	}
	expires := now.Add(time.Hour)
	_, err = s.db.Exec(`
		INSERT INTO transport_access_requests
		(id,transport,connection_id,project_id,external_chat_id,external_user_id,chat_type,
		 display_name,username,chat_title,pairing_code,state,expires_at,notified_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,'pending',?,NULL,CURRENT_TIMESTAMP)
		ON CONFLICT(transport,connection_id,external_chat_id,external_user_id) DO UPDATE SET
			project_id=excluded.project_id,chat_type=excluded.chat_type,
			display_name=excluded.display_name,username=excluded.username,chat_title=excluded.chat_title,
			pairing_code=excluded.pairing_code,state='pending',conversation_id='',
			expires_at=excluded.expires_at,notified_at=NULL,updated_at=CURRENT_TIMESTAMP`,
		input.ID, input.Transport, input.ConnectionID, input.ProjectID, input.ExternalChatID,
		input.ExternalUserID, input.ChatType, input.DisplayName, input.Username, input.ChatTitle, code, expires)
	if err != nil {
		return nil, false, err
	}
	created, err := scanTransportAccessRequest(s.db.QueryRow(`SELECT `+transportAccessColumns+`
		FROM transport_access_requests WHERE transport=? AND connection_id=? AND external_chat_id=? AND external_user_id=?`,
		input.Transport, input.ConnectionID, input.ExternalChatID, input.ExternalUserID))
	return created, true, err
}

func (s *store) GetTransportAccessRequest(id string) (*TransportAccessRequest, error) {
	return scanTransportAccessRequest(s.db.QueryRow(`SELECT `+transportAccessColumns+`
		FROM transport_access_requests WHERE id=?`, id))
}

func (s *store) ListTransportAccessRequests(transport, projectID string) ([]TransportAccessRequest, error) {
	rows, err := s.db.Query(`SELECT `+transportAccessColumns+`
		FROM transport_access_requests
		WHERE transport=? AND project_id=? AND state='pending' AND expires_at>CURRENT_TIMESTAMP
		ORDER BY created_at DESC`, transport, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TransportAccessRequest{}
	for rows.Next() {
		req, err := scanTransportAccessRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *req)
	}
	return out, rows.Err()
}

func (s *store) ListBlockedTransportAccess(transport, projectID string) ([]TransportAccessRequest, error) {
	rows, err := s.db.Query(`SELECT `+transportAccessColumns+`
		FROM transport_access_requests
		WHERE transport=? AND project_id=? AND state='blocked'
		ORDER BY updated_at DESC`, transport, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TransportAccessRequest{}
	for rows.Next() {
		req, err := scanTransportAccessRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *req)
	}
	return out, rows.Err()
}

func (s *store) MarkTransportAccessNotified(id string) {
	_, _ = s.db.Exec(`UPDATE transport_access_requests
		SET notified_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=?`, id)
}

func (s *store) ResolveTransportAccessRequest(id, state, conversationID string) error {
	switch state {
	case "approved", "dismissed", "blocked":
	default:
		return errors.New("invalid access-request state")
	}
	res, err := s.db.Exec(`UPDATE transport_access_requests
		SET state=?,conversation_id=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='pending'`,
		state, conversationID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *store) UnblockTransportAccessRequest(id string) error {
	res, err := s.db.Exec(`UPDATE transport_access_requests
		SET state='dismissed',updated_at=CURRENT_TIMESTAMP WHERE id=? AND state='blocked'`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func hashTransportToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *store) CreateTransportInvite(invite TransportInvite) (string, *TransportInvite, error) {
	raw, err := randomTelegramSecret(24)
	if err != nil {
		return "", nil, err
	}
	invite.ID, err = randomTelegramSecret(12)
	if err != nil {
		return "", nil, err
	}
	invite.TokenHash = hashTransportToken(raw)
	invite.ExpiresAt = time.Now().UTC().Add(15 * time.Minute)
	_, err = s.db.Exec(`INSERT INTO transport_invites
		(id,token_hash,transport,connection_id,project_id,conversation_id,audience,chat_type,
		 default_agent_id,created_by_user_id,expires_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, invite.ID, invite.TokenHash, invite.Transport,
		invite.ConnectionID, invite.ProjectID, invite.ConversationID, invite.Audience, invite.ChatType,
		invite.DefaultAgentID, invite.CreatedByUserID, invite.ExpiresAt)
	if err != nil {
		return "", nil, err
	}
	return raw, &invite, nil
}

func scanTransportInvite(scanner interface{ Scan(...any) error }) (*TransportInvite, error) {
	var invite TransportInvite
	var used sql.NullTime
	err := scanner.Scan(&invite.ID, &invite.TokenHash, &invite.Transport, &invite.ConnectionID,
		&invite.ProjectID, &invite.ConversationID, &invite.Audience, &invite.ChatType, &invite.DefaultAgentID,
		&invite.CreatedByUserID, &invite.ExpiresAt, &used)
	if err != nil {
		return nil, err
	}
	if used.Valid {
		invite.UsedAt = &used.Time
	}
	return &invite, nil
}

func (s *store) ConsumeTransportInvite(transport string, connectionID int64, raw string) (*TransportInvite, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	invite, err := scanTransportInvite(tx.QueryRow(`SELECT id,token_hash,transport,connection_id,
		project_id,conversation_id,audience,chat_type,default_agent_id,created_by_user_id,expires_at,used_at
		FROM transport_invites WHERE token_hash=? AND transport=? AND connection_id=?`,
		hashTransportToken(raw), transport, connectionID))
	if err != nil {
		return nil, err
	}
	if invite.UsedAt != nil || !invite.ExpiresAt.After(time.Now().UTC()) {
		return nil, errors.New("invite is expired or already used")
	}
	res, err := tx.Exec(`UPDATE transport_invites SET used_at=CURRENT_TIMESTAMP
		WHERE id=? AND used_at IS NULL`, invite.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errors.New("invite is already used")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return invite, nil
}

func (s *store) PruneTransportOnboarding() error {
	now := time.Now().UTC()
	cutoff := now.Add(-7 * 24 * time.Hour)
	if _, err := s.db.Exec(`DELETE FROM transport_invites
		WHERE expires_at<? OR (used_at IS NOT NULL AND used_at<?)`, now, cutoff); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM transport_access_requests
		WHERE (state NOT IN ('pending','blocked') AND updated_at<?) OR (state='pending' AND expires_at<?)`, cutoff, cutoff)
	return err
}
