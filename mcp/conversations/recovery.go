package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const maxMessageBytes = 256 << 10
const maxAttachmentBytes = 768 << 10

func validateMessageSize(m *Message) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(m.Content) > maxMessageBytes || len(raw) > 1<<20 {
		return errors.New("message exceeds byte limit")
	}
	if len(m.ClientID) > 256 {
		return errors.New("client_message_id exceeds 256 bytes")
	}
	if len(m.Attachments) > 10 {
		return errors.New("at most 10 attachments allowed")
	}
	for _, a := range m.Attachments {
		if a.Type != "image" || !strings.HasPrefix(a.DataURL, "data:image/") || len(a.DataURL) > maxAttachmentBytes {
			return errors.New("unsupported or oversized attachment")
		}
	}
	return nil
}
func inboxClientID(installID int64, key string) string {
	if strings.TrimSpace(key) == "" {
		return ""
	}
	return fmt.Sprintf("app:%d:%s", installID, key)
}

type deliveryClaim struct {
	Token      string
	Generation int
	Attempts   int
}

func (s *store) ClaimDelivery(messageID int64, target string) (*deliveryClaim, error) {
	token := newConversationID()
	now := time.Now().Unix()
	c := &deliveryClaim{Token: token}
	err := s.db.QueryRow(`UPDATE deliveries SET status='processing',lease_token=?,lease_until=?,updated_at=CURRENT_TIMESTAMP
 WHERE message_id=? AND target=? AND status='pending' AND julianday(next_attempt_at)<=julianday('now')
 AND (target NOT LIKE 'telegram:%' OR NOT EXISTS(SELECT 1 FROM deliveries prior WHERE prior.target=deliveries.target AND prior.message_id<deliveries.message_id AND prior.status IN ('pending','processing','ambiguous')))
 RETURNING generation,attempts`, token, now+120, messageID, target).Scan(&c.Generation, &c.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}
func (s *store) FinishDelivery(messageID int64, target string, c *deliveryClaim, deliveryErr error) error {
	status, lastError := "delivered", ""
	if deliveryErr != nil {
		status = "pending"
		lastError = deliveryErr.Error()
		if len(lastError) > 1000 {
			lastError = lastError[:1000]
		}
		if c.Attempts+1 >= 10 || strings.Contains(lastError, "malformed") || strings.Contains(lastError, "no adapter") || strings.Contains(lastError, "unsupported") || strings.Contains(lastError, "no longer a participant") {
			status = "failed"
		}
	}
	var ambiguous *ambiguousDeliveryError
	if errors.As(deliveryErr, &ambiguous) {
		status = "ambiguous"
	}
	delay := time.Duration(1<<min(c.Attempts+1, 8)) * time.Second
	_, err := s.db.Exec(`UPDATE deliveries SET status=CASE WHEN ?='ambiguous' THEN 'ambiguous' WHEN generation!=? THEN 'pending' ELSE ? END,
 attempts=CASE WHEN generation!=? THEN 0 ELSE attempts+1 END,last_error=?,lease_token='',lease_until=0,
 delivered_at=CASE WHEN ?='delivered' THEN CURRENT_TIMESTAMP ELSE delivered_at END,
 next_attempt_at=CASE WHEN generation!=? THEN CURRENT_TIMESTAMP ELSE ? END,updated_at=CURRENT_TIMESTAMP
 WHERE message_id=? AND target=? AND lease_token=? AND status='processing'`, status, c.Generation, status, c.Generation, lastError, status, c.Generation, time.Now().UTC().Add(delay).Format(time.RFC3339Nano), messageID, target, c.Token)
	return err
}
func (s *store) RecoverExpiredDeliveries() error {
	_, err := s.db.Exec(`UPDATE deliveries SET status=CASE WHEN target LIKE 'telegram:%' THEN 'ambiguous' ELSE 'pending' END,
 last_error='Previous delivery lease expired; provider acceptance is unknown',lease_token='',lease_until=0,next_attempt_at=CURRENT_TIMESTAMP
 WHERE status='processing' AND lease_until<?`, time.Now().Unix())
	return err
}

func validateDuplicateMessage(old, next *Message) error {
	// Decisions/dismissals may have advanced since submission; compare the original
	// submission digest when available, otherwise stable author and content fields.
	if old.Role != next.Role || old.AgentID != next.AgentID || old.UserID != next.UserID || old.ExternalSender != next.ExternalSender || old.SourceApp != next.SourceApp || old.Content != next.Content || old.CallbackTool != next.CallbackTool {
		return errors.New("client_message_id already belongs to a different request")
	}
	return nil
}
func (s *store) ClaimTelegramProcessing(connectionID, updateID int64) (string, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO telegram_updates(connection_id,update_id) VALUES(?,?)`, connectionID, updateID); err != nil {
		return "", false, err
	}
	var completed bool
	if err := tx.QueryRow(`SELECT completed FROM telegram_updates WHERE connection_id=? AND update_id=?`, connectionID, updateID).Scan(&completed); err != nil {
		return "", false, err
	}
	if completed {
		return "", true, nil
	}
	token := newConversationID()
	res, err := tx.Exec(`UPDATE telegram_updates SET lease_token=?,lease_until=? WHERE connection_id=? AND update_id=? AND completed=0 AND lease_until<=?`, token, time.Now().Unix()+120, connectionID, updateID, time.Now().Unix())
	if err != nil {
		return "", false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", false, err
	}
	if n == 0 {
		return "", false, nil
	}
	return token, false, tx.Commit()
}
