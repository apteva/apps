package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"time"
)

type scanner interface {
	Scan(dest ...any) error
}

func createEnvelope(db *sql.DB, projectID string, source StorageFile, sourceHash, title, senderName, message string, expiresAt time.Time) (*Envelope, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, errors.New("title required")
	}
	if source.ID == 0 || sourceHash == "" {
		return nil, errors.New("valid source PDF required")
	}
	publicID, err := randomToken(12)
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	res, err := db.Exec(`
		INSERT INTO envelopes
			(public_id, project_id, source_file_id, source_name, source_sha256,
			 title, sender_name, message, status, delivery_mode, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'draft', 'manual', ?, ?, ?)`,
		publicID, projectID, source.ID, source.Name, sourceHash, title,
		strings.TrimSpace(senderName), strings.TrimSpace(message), expiresAt.UTC().Format(time.RFC3339), now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := addAudit(db, id, projectID, 0, "envelope.created", map[string]any{"source_file_id": source.ID}); err != nil {
		return nil, err
	}
	return getEnvelopeRequired(db, projectID, id)
}

func updateEnvelope(db *sql.DB, projectID string, id int64, args map[string]any) (*Envelope, error) {
	env, err := getEnvelopeRequired(db, projectID, id)
	if err != nil {
		return nil, err
	}
	if env.Status != "draft" {
		return nil, errors.New("only draft envelopes can be updated")
	}
	sets := []string{}
	values := []any{}
	if raw, ok := args["title"]; ok {
		title := strings.TrimSpace(fmt.Sprint(raw))
		if title == "" {
			return nil, errors.New("title cannot be empty")
		}
		sets = append(sets, "title = ?")
		values = append(values, title)
	}
	for _, key := range []string{"sender_name", "message"} {
		if raw, ok := args[key]; ok {
			sets = append(sets, key+" = ?")
			values = append(values, strings.TrimSpace(fmt.Sprint(raw)))
		}
	}
	if raw, ok := args["expires_at"]; ok {
		expiresAt, err := parseRFC3339(fmt.Sprint(raw))
		if err != nil {
			return nil, err
		}
		if expiresAt.Before(time.Now().UTC().Add(15 * time.Minute)) {
			return nil, errors.New("expires_at must be at least 15 minutes in the future")
		}
		sets = append(sets, "expires_at = ?")
		values = append(values, expiresAt.Format(time.RFC3339))
	}
	if len(sets) == 0 {
		return env, nil
	}
	sets = append(sets, "updated_at = ?")
	values = append(values, nowUTC(), id, projectID)
	res, err := db.Exec(`UPDATE envelopes SET `+strings.Join(sets, ", ")+` WHERE id = ? AND project_id = ? AND status = 'draft'`, values...)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, sql.ErrNoRows
	}
	_ = addAudit(db, id, projectID, 0, "envelope.updated", nil)
	return getEnvelopeRequired(db, projectID, id)
}

func getEnvelope(db *sql.DB, projectID string, id int64) (*Envelope, error) {
	row := db.QueryRow(`
		SELECT id, public_id, project_id, source_file_id, source_name, source_sha256,
		       COALESCE(completed_file_id, 0), COALESCE(completed_sha256, ''), COALESCE(audit_file_id, 0),
		       title, sender_name, message, status, delivery_mode, expires_at,
		       COALESCE(sent_at, ''), COALESCE(completed_at, ''), terminal_reason, created_at, updated_at
		FROM envelopes WHERE id = ? AND project_id = ?`, id, projectID)
	env, err := scanEnvelope(row)
	if err != nil || env == nil {
		return env, err
	}
	return expireIfNeeded(db, env)
}

func getEnvelopeRequired(db *sql.DB, projectID string, id int64) (*Envelope, error) {
	env, err := getEnvelope(db, projectID, id)
	if err != nil {
		return nil, err
	}
	if env == nil {
		return nil, sql.ErrNoRows
	}
	return env, nil
}

func scanEnvelope(s scanner) (*Envelope, error) {
	var env Envelope
	err := s.Scan(&env.ID, &env.PublicID, &env.ProjectID, &env.SourceFileID, &env.SourceName, &env.SourceSHA256,
		&env.CompletedFileID, &env.CompletedSHA256, &env.AuditFileID, &env.Title, &env.SenderName,
		&env.Message, &env.Status, &env.DeliveryMode, &env.ExpiresAt, &env.SentAt,
		&env.CompletedAt, &env.TerminalReason, &env.CreatedAt, &env.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &env, err
}

func expireIfNeeded(db *sql.DB, env *Envelope) (*Envelope, error) {
	if env == nil || env.Status != "sent" {
		return env, nil
	}
	expires, err := time.Parse(time.RFC3339, env.ExpiresAt)
	if err != nil || time.Now().UTC().Before(expires) {
		return env, nil
	}
	now := nowUTC()
	res, err := db.Exec(`UPDATE envelopes SET status = 'expired', terminal_reason = 'expired', updated_at = ? WHERE id = ? AND project_id = ? AND status = 'sent'`, now, env.ID, env.ProjectID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		_, _ = db.Exec(`UPDATE recipients SET token_hash = NULL, token_expires_at = NULL, updated_at = ? WHERE envelope_id = ?`, now, env.ID)
		_ = addAudit(db, env.ID, env.ProjectID, 0, "envelope.expired", nil)
		env.Status = "expired"
		env.TerminalReason = "expired"
		env.UpdatedAt = now
	}
	return env, nil
}

func listEnvelopes(db *sql.DB, projectID, status string, limit int) ([]Envelope, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := "project_id = ?"
	args := []any{projectID}
	if status != "" && status != "all" {
		where += " AND status = ?"
		args = append(args, status)
	}
	args = append(args, limit)
	rows, err := db.Query(`
		SELECT id, public_id, project_id, source_file_id, source_name, source_sha256,
		       COALESCE(completed_file_id, 0), COALESCE(completed_sha256, ''), COALESCE(audit_file_id, 0),
		       title, sender_name, message, status, delivery_mode, expires_at,
		       COALESCE(sent_at, ''), COALESCE(completed_at, ''), terminal_reason, created_at, updated_at
		FROM envelopes WHERE `+where+` ORDER BY updated_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Envelope{}
	for rows.Next() {
		env, err := scanEnvelope(rows)
		if err != nil {
			return nil, err
		}
		if env != nil {
			out = append(out, *env)
		}
	}
	return out, rows.Err()
}

func getEnvelopeDetail(db *sql.DB, projectID string, id int64, includeAudit bool) (*EnvelopeDetail, error) {
	env, err := getEnvelopeRequired(db, projectID, id)
	if err != nil {
		return nil, err
	}
	recipients, err := listRecipients(db, projectID, id)
	if err != nil {
		return nil, err
	}
	fields, err := listFields(db, projectID, id, 0)
	if err != nil {
		return nil, err
	}
	detail := &EnvelopeDetail{Envelope: *env, Recipients: recipients, Fields: fields}
	if includeAudit {
		detail.Audit, err = listAudit(db, projectID, id)
	}
	return detail, err
}

func setRecipients(db *sql.DB, projectID string, envelopeID int64, specs []map[string]any) ([]Recipient, error) {
	if len(specs) == 0 || len(specs) > 50 {
		return nil, errors.New("recipients must contain 1 to 50 entries")
	}
	env, err := getEnvelopeRequired(db, projectID, envelopeID)
	if err != nil {
		return nil, err
	}
	if env.Status != "draft" {
		return nil, errors.New("recipients can only be changed on a draft")
	}
	type parsed struct {
		Name  string
		Email string
		Role  string
		Order int
	}
	parsedSpecs := make([]parsed, 0, len(specs))
	orders := map[int]bool{}
	for i, spec := range specs {
		p := parsed{Name: stringArg(spec, "name"), Email: strings.ToLower(stringArg(spec, "email")), Role: strings.ToLower(stringArg(spec, "role")), Order: intArg(spec, "signing_order", i+1)}
		if p.Name == "" {
			return nil, fmt.Errorf("recipients[%d].name required", i)
		}
		if p.Email != "" {
			address, err := mail.ParseAddress(p.Email)
			if err != nil || address.Address != p.Email || !strings.Contains(p.Email, "@") {
				return nil, fmt.Errorf("recipients[%d].email is invalid", i)
			}
		}
		if p.Role == "" {
			p.Role = "signer"
		}
		if p.Role != "signer" && p.Role != "approver" {
			return nil, fmt.Errorf("recipients[%d].role must be signer or approver", i)
		}
		if p.Order < 1 || orders[p.Order] {
			return nil, errors.New("signing_order values must be unique positive integers")
		}
		orders[p.Order] = true
		parsedSpecs = append(parsedSpecs, p)
	}
	sort.Slice(parsedSpecs, func(i, j int) bool { return parsedSpecs[i].Order < parsedSpecs[j].Order })
	for i, p := range parsedSpecs {
		if p.Order != i+1 {
			return nil, errors.New("signing_order values must form a contiguous sequence starting at 1")
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM fields WHERE envelope_id = ? AND project_id = ?`, envelopeID, projectID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM recipients WHERE envelope_id = ? AND project_id = ?`, envelopeID, projectID); err != nil {
		return nil, err
	}
	now := nowUTC()
	for _, p := range parsedSpecs {
		if _, err := tx.Exec(`
			INSERT INTO recipients (envelope_id, project_id, name, email, role, signing_order, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, envelopeID, projectID, p.Name, p.Email, p.Role, p.Order, now, now); err != nil {
			return nil, err
		}
	}
	if err := addAuditTx(tx, envelopeID, projectID, 0, "recipients.set", map[string]any{"count": len(parsedSpecs)}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return listRecipients(db, projectID, envelopeID)
}

func listRecipients(db *sql.DB, projectID string, envelopeID int64) ([]Recipient, error) {
	rows, err := db.Query(`
		SELECT id, envelope_id, project_id, name, email, role, signing_order, status,
		       COALESCE(token_expires_at, ''), COALESCE(viewed_at, ''), COALESCE(completed_at, ''),
		       declined_reason, created_at, updated_at
		FROM recipients WHERE envelope_id = ? AND project_id = ? ORDER BY signing_order`, envelopeID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Recipient{}
	for rows.Next() {
		var r Recipient
		if err := rows.Scan(&r.ID, &r.EnvelopeID, &r.ProjectID, &r.Name, &r.Email, &r.Role, &r.SigningOrder,
			&r.Status, &r.TokenExpiresAt, &r.ViewedAt, &r.CompletedAt, &r.DeclinedReason, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func getRecipient(db *sql.DB, projectID string, envelopeID, recipientID int64) (*Recipient, error) {
	row := db.QueryRow(`
		SELECT id, envelope_id, project_id, name, email, role, signing_order, status,
		       COALESCE(token_expires_at, ''), COALESCE(viewed_at, ''), COALESCE(completed_at, ''),
		       declined_reason, created_at, updated_at
		FROM recipients WHERE id = ? AND envelope_id = ? AND project_id = ?`, recipientID, envelopeID, projectID)
	var r Recipient
	err := row.Scan(&r.ID, &r.EnvelopeID, &r.ProjectID, &r.Name, &r.Email, &r.Role, &r.SigningOrder,
		&r.Status, &r.TokenExpiresAt, &r.ViewedAt, &r.CompletedAt, &r.DeclinedReason, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}

func setFields(db *sql.DB, projectID string, envelopeID int64, specs []map[string]any) ([]Field, error) {
	if len(specs) == 0 || len(specs) > 500 {
		return nil, errors.New("fields must contain 1 to 500 entries")
	}
	env, err := getEnvelopeRequired(db, projectID, envelopeID)
	if err != nil {
		return nil, err
	}
	if env.Status != "draft" {
		return nil, errors.New("fields can only be changed on a draft")
	}
	recipients, err := listRecipients(db, projectID, envelopeID)
	if err != nil {
		return nil, err
	}
	validRecipients := map[int64]bool{}
	for _, r := range recipients {
		validRecipients[r.ID] = true
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM fields WHERE envelope_id = ? AND project_id = ?`, envelopeID, projectID); err != nil {
		return nil, err
	}
	now := nowUTC()
	allowedTypes := map[string]bool{"signature": true, "initials": true, "date_signed": true, "text": true, "checkbox": true}
	for i, spec := range specs {
		recipientID := int64Arg(spec, "recipient_id")
		fieldType := strings.ToLower(stringArg(spec, "field_type"))
		page := intArg(spec, "page", 0)
		x, xok := numberArg(spec, "x")
		y, yok := numberArg(spec, "y")
		width, wok := numberArg(spec, "width")
		height, hok := numberArg(spec, "height")
		if !validRecipients[recipientID] {
			return nil, fmt.Errorf("fields[%d].recipient_id is not in this envelope", i)
		}
		if !allowedTypes[fieldType] {
			return nil, fmt.Errorf("fields[%d].field_type is invalid", i)
		}
		if page < 1 {
			return nil, fmt.Errorf("fields[%d].page must be positive", i)
		}
		if !xok || !yok || !wok || !hok || x < 0 || y < 0 || width <= 0 || height <= 0 || x+width > 1 || y+height > 1 {
			return nil, fmt.Errorf("fields[%d] coordinates must be normalized inside the page", i)
		}
		required := boolArg(spec, "required", true)
		if _, err := tx.Exec(`
			INSERT INTO fields (envelope_id, recipient_id, project_id, field_type, page, x, y, width, height, label, required, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, envelopeID, recipientID, projectID, fieldType, page,
			x, y, width, height, stringArg(spec, "label"), boolInt(required), now); err != nil {
			return nil, err
		}
	}
	if err := addAuditTx(tx, envelopeID, projectID, 0, "fields.set", map[string]any{"count": len(specs)}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return listFields(db, projectID, envelopeID, 0)
}

func numberArg(args map[string]any, key string) (float64, bool) {
	switch v := args[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		n, err := v.Float64()
		return n, err == nil
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return n, err == nil
	default:
		return 0, false
	}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func listFields(db *sql.DB, projectID string, envelopeID, recipientID int64) ([]Field, error) {
	where := "envelope_id = ? AND project_id = ?"
	args := []any{envelopeID, projectID}
	if recipientID != 0 {
		where += " AND recipient_id = ?"
		args = append(args, recipientID)
	}
	rows, err := db.Query(`
		SELECT id, envelope_id, recipient_id, project_id, field_type, page, x, y, width, height, label, required, created_at
		FROM fields WHERE `+where+` ORDER BY page, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Field{}
	for rows.Next() {
		var f Field
		var required int
		if err := rows.Scan(&f.ID, &f.EnvelopeID, &f.RecipientID, &f.ProjectID, &f.FieldType, &f.Page,
			&f.X, &f.Y, &f.Width, &f.Height, &f.Label, &required, &f.CreatedAt); err != nil {
			return nil, err
		}
		f.Required = required != 0
		out = append(out, f)
	}
	return out, rows.Err()
}

func validateEnvelope(db *sql.DB, projectID string, envelopeID int64, pageCount int, deliveryMode string) []string {
	errorsOut := []string{}
	env, err := getEnvelopeRequired(db, projectID, envelopeID)
	if err != nil {
		return []string{err.Error()}
	}
	if env.Status != "draft" {
		errorsOut = append(errorsOut, "envelope is not a draft")
	}
	if expires, err := time.Parse(time.RFC3339, env.ExpiresAt); err != nil || !expires.After(time.Now().UTC()) {
		errorsOut = append(errorsOut, "envelope expiry must be in the future")
	}
	recipients, err := listRecipients(db, projectID, envelopeID)
	if err != nil || len(recipients) == 0 {
		errorsOut = append(errorsOut, "at least one recipient is required")
		return errorsOut
	}
	fields, err := listFields(db, projectID, envelopeID, 0)
	if err != nil || len(fields) == 0 {
		errorsOut = append(errorsOut, "at least one field is required")
	}
	signatures := map[int64]bool{}
	for _, f := range fields {
		if f.Page < 1 || (pageCount > 0 && f.Page > pageCount) {
			errorsOut = append(errorsOut, fmt.Sprintf("field %d references invalid page %d", f.ID, f.Page))
		}
		if f.FieldType == "signature" {
			signatures[f.RecipientID] = true
		}
	}
	for _, recipient := range recipients {
		if recipient.Role == "signer" && !signatures[recipient.ID] {
			errorsOut = append(errorsOut, fmt.Sprintf("signer %s needs a signature field", recipient.Name))
		}
		if deliveryMode == "messaging" && recipient.Email == "" {
			errorsOut = append(errorsOut, fmt.Sprintf("recipient %s needs an email for messaging delivery", recipient.Name))
		}
	}
	return errorsOut
}

func activateEnvelope(db *sql.DB, projectID string, envelopeID int64, deliveryMode, idempotencyKey string) (*Envelope, *Recipient, error) {
	if deliveryMode != "manual" && deliveryMode != "messaging" {
		return nil, nil, errors.New("delivery_mode must be manual or messaging")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var status, existingKey string
	if err := tx.QueryRow(`SELECT status, COALESCE(idempotency_key, '') FROM envelopes WHERE id = ? AND project_id = ?`, envelopeID, projectID).Scan(&status, &existingKey); err != nil {
		return nil, nil, err
	}
	if status != "draft" {
		if idempotencyKey != "" && existingKey == idempotencyKey {
			if err := tx.Commit(); err != nil {
				return nil, nil, err
			}
			env, err := getEnvelopeRequired(db, projectID, envelopeID)
			if err != nil {
				return nil, nil, err
			}
			recipient, err := currentRecipient(db, projectID, envelopeID)
			return env, recipient, err
		}
		return nil, nil, errors.New("only draft envelopes can be sent")
	}
	now := nowUTC()
	if _, err := tx.Exec(`UPDATE envelopes SET status = 'sent', delivery_mode = ?, idempotency_key = NULLIF(?, ''), sent_at = ?, updated_at = ? WHERE id = ? AND project_id = ? AND status = 'draft'`, deliveryMode, idempotencyKey, now, now, envelopeID, projectID); err != nil {
		return nil, nil, err
	}
	res, err := tx.Exec(`UPDATE recipients SET status = 'ready', updated_at = ? WHERE id = (SELECT id FROM recipients WHERE envelope_id = ? AND project_id = ? ORDER BY signing_order LIMIT 1)`, now, envelopeID, projectID)
	if err != nil {
		return nil, nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, nil, errors.New("envelope has no recipients")
	}
	if err := addAuditTx(tx, envelopeID, projectID, 0, "envelope.sent", map[string]any{"delivery_mode": deliveryMode}); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	env, err := getEnvelopeRequired(db, projectID, envelopeID)
	if err != nil {
		return nil, nil, err
	}
	recipient, err := currentRecipient(db, projectID, envelopeID)
	return env, recipient, err
}

func sendIdempotencyMatches(db *sql.DB, projectID string, envelopeID int64, key string) (bool, error) {
	if strings.TrimSpace(key) == "" {
		return false, nil
	}
	var stored string
	err := db.QueryRow(`SELECT COALESCE(idempotency_key, '') FROM envelopes WHERE id = ? AND project_id = ?`, envelopeID, projectID).Scan(&stored)
	if err != nil {
		return false, err
	}
	return stored == key, nil
}

func currentRecipient(db *sql.DB, projectID string, envelopeID int64) (*Recipient, error) {
	row := db.QueryRow(`
		SELECT id, envelope_id, project_id, name, email, role, signing_order, status,
		       COALESCE(token_expires_at, ''), COALESCE(viewed_at, ''), COALESCE(completed_at, ''),
		       declined_reason, created_at, updated_at
		FROM recipients WHERE envelope_id = ? AND project_id = ? AND status IN ('ready','viewed')
		ORDER BY signing_order LIMIT 1`, envelopeID, projectID)
	var r Recipient
	err := row.Scan(&r.ID, &r.EnvelopeID, &r.ProjectID, &r.Name, &r.Email, &r.Role, &r.SigningOrder,
		&r.Status, &r.TokenExpiresAt, &r.ViewedAt, &r.CompletedAt, &r.DeclinedReason, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}

func createRecipientToken(db *sql.DB, projectID string, envelopeID, recipientID int64) (*Envelope, *Recipient, string, error) {
	env, err := getEnvelopeRequired(db, projectID, envelopeID)
	if err != nil {
		return nil, nil, "", err
	}
	if env.Status != "sent" {
		return nil, nil, "", errors.New("envelope is not active")
	}
	recipient, err := getRecipient(db, projectID, envelopeID, recipientID)
	if err != nil {
		return nil, nil, "", err
	}
	if recipient == nil {
		return nil, nil, "", sql.ErrNoRows
	}
	if recipient.Status != "ready" && recipient.Status != "viewed" {
		return nil, nil, "", errors.New("recipient is not the current signer")
	}
	token, err := randomToken(32)
	if err != nil {
		return nil, nil, "", err
	}
	now := nowUTC()
	res, err := db.Exec(`UPDATE recipients SET token_hash = ?, token_expires_at = ?, updated_at = ? WHERE id = ? AND envelope_id = ? AND project_id = ? AND status IN ('ready','viewed')`, tokenHash(token), env.ExpiresAt, now, recipientID, envelopeID, projectID)
	if err != nil {
		return nil, nil, "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, nil, "", errors.New("recipient is no longer active")
	}
	_ = addAudit(db, envelopeID, projectID, recipientID, "recipient.link_created", map[string]any{"expires_at": env.ExpiresAt})
	recipient.TokenExpiresAt = env.ExpiresAt
	recipient.UpdatedAt = now
	return env, recipient, token, nil
}

func sessionByToken(db *sql.DB, token string) (*SigningSession, error) {
	hash := tokenHash(token)
	var recipientID, envelopeID int64
	var projectID string
	err := db.QueryRow(`SELECT id, envelope_id, project_id FROM recipients WHERE token_hash = ?`, hash).Scan(&recipientID, &envelopeID, &projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	env, err := getEnvelopeRequired(db, projectID, envelopeID)
	if err != nil {
		return nil, err
	}
	if env.Status != "sent" {
		return nil, nil
	}
	recipient, err := getRecipient(db, projectID, envelopeID, recipientID)
	if err != nil || recipient == nil {
		return nil, err
	}
	if recipient.Status != "ready" && recipient.Status != "viewed" {
		return nil, nil
	}
	if recipient.TokenExpiresAt == "" {
		return nil, nil
	}
	expires, err := time.Parse(time.RFC3339, recipient.TokenExpiresAt)
	if err != nil || !time.Now().UTC().Before(expires) {
		return nil, nil
	}
	fields, err := listFields(db, projectID, envelopeID, recipientID)
	if err != nil {
		return nil, err
	}
	return &SigningSession{Envelope: *env, Recipient: *recipient, Fields: fields}, nil
}

func markRecipientViewed(db *sql.DB, session *SigningSession) (bool, error) {
	if session == nil || session.Recipient.Status != "ready" {
		return false, nil
	}
	now := nowUTC()
	res, err := db.Exec(`UPDATE recipients SET status = 'viewed', viewed_at = COALESCE(viewed_at, ?), updated_at = ? WHERE id = ? AND envelope_id = ? AND project_id = ? AND status = 'ready'`, now, now, session.Recipient.ID, session.Envelope.ID, session.Envelope.ProjectID)
	if err != nil {
		return false, err
	}
	changed, _ := res.RowsAffected()
	if changed == 1 {
		_ = addAudit(db, session.Envelope.ID, session.Envelope.ProjectID, session.Recipient.ID, "recipient.viewed", nil)
		session.Recipient.Status = "viewed"
		session.Recipient.ViewedAt = now
		return true, nil
	}
	return false, nil
}

func completeRecipient(db *sql.DB, token, legalName string, values map[int64]string) (*Envelope, *Recipient, *Recipient, bool, error) {
	session, err := sessionByToken(db, token)
	if err != nil || session == nil {
		if err == nil {
			err = errors.New("signing link is invalid or expired")
		}
		return nil, nil, nil, false, err
	}
	legalName = strings.TrimSpace(legalName)
	if session.Recipient.Role == "signer" && legalName == "" {
		return nil, nil, nil, false, errors.New("legal name required")
	}
	prepared := make(map[int64]string, len(session.Fields))
	for _, field := range session.Fields {
		value := strings.TrimSpace(values[field.ID])
		switch field.FieldType {
		case "signature":
			if value == "" {
				value = legalName
			}
		case "initials":
			if value == "" && legalName != "" {
				parts := strings.Fields(legalName)
				for _, part := range parts {
					value += strings.ToUpper(string([]rune(part)[0]))
				}
			}
		case "date_signed":
			value = time.Now().UTC().Format("2006-01-02")
		case "checkbox":
			if value == "on" || value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes") {
				value = "true"
			} else {
				value = ""
			}
		}
		if field.Required && value == "" {
			return nil, nil, nil, false, fmt.Errorf("field %d (%s) is required", field.ID, field.Label)
		}
		if field.FieldType == "signature" && strings.HasPrefix(value, drawnSignaturePrefix) {
			if err := validateDrawnSignature(value); err != nil {
				return nil, nil, nil, false, fmt.Errorf("field %d: %w", field.ID, err)
			}
		} else if len(value) > 2000 {
			return nil, nil, nil, false, fmt.Errorf("field %d value is too long", field.ID)
		}
		prepared[field.ID] = value
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, nil, false, err
	}
	defer tx.Rollback()
	var status, hash string
	if err := tx.QueryRow(`SELECT status, COALESCE(token_hash, '') FROM recipients WHERE id = ? AND envelope_id = ? AND project_id = ?`, session.Recipient.ID, session.Envelope.ID, session.Envelope.ProjectID).Scan(&status, &hash); err != nil {
		return nil, nil, nil, false, err
	}
	if status != "ready" && status != "viewed" || hash != tokenHash(token) {
		return nil, nil, nil, false, errors.New("recipient is no longer active")
	}
	now := nowUTC()
	for _, field := range session.Fields {
		if _, err := tx.Exec(`INSERT INTO field_values (field_id, envelope_id, recipient_id, project_id, value_text, signed_at) VALUES (?, ?, ?, ?, ?, ?)`, field.ID, session.Envelope.ID, session.Recipient.ID, session.Envelope.ProjectID, prepared[field.ID], now); err != nil {
			return nil, nil, nil, false, err
		}
	}
	newStatus := "signed"
	eventType := "recipient.signed"
	if session.Recipient.Role == "approver" {
		newStatus = "approved"
		eventType = "recipient.approved"
	}
	if _, err := tx.Exec(`UPDATE recipients SET status = ?, token_hash = NULL, token_expires_at = NULL, completed_at = ?, updated_at = ? WHERE id = ?`, newStatus, now, now, session.Recipient.ID); err != nil {
		return nil, nil, nil, false, err
	}
	if err := addAuditTx(tx, session.Envelope.ID, session.Envelope.ProjectID, session.Recipient.ID, eventType, map[string]any{"legal_name": legalName}); err != nil {
		return nil, nil, nil, false, err
	}
	var nextID int64
	err = tx.QueryRow(`SELECT id FROM recipients WHERE envelope_id = ? AND project_id = ? AND status = 'pending' ORDER BY signing_order LIMIT 1`, session.Envelope.ID, session.Envelope.ProjectID).Scan(&nextID)
	needFinalize := errors.Is(err, sql.ErrNoRows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil, false, err
	}
	if nextID != 0 {
		if _, err := tx.Exec(`UPDATE recipients SET status = 'ready', updated_at = ? WHERE id = ? AND status = 'pending'`, now, nextID); err != nil {
			return nil, nil, nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, nil, false, err
	}
	env, err := getEnvelopeRequired(db, session.Envelope.ProjectID, session.Envelope.ID)
	if err != nil {
		return nil, nil, nil, false, err
	}
	completed, _ := getRecipient(db, env.ProjectID, env.ID, session.Recipient.ID)
	var next *Recipient
	if nextID != 0 {
		next, _ = getRecipient(db, env.ProjectID, env.ID, nextID)
	}
	return env, completed, next, needFinalize, nil
}

func declineRecipient(db *sql.DB, token, reason string) (*Envelope, *Recipient, error) {
	session, err := sessionByToken(db, token)
	if err != nil || session == nil {
		if err == nil {
			err = errors.New("signing link is invalid or expired")
		}
		return nil, nil, err
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 1000 {
		return nil, nil, errors.New("decline reason is too long")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	now := nowUTC()
	res, err := tx.Exec(`UPDATE recipients SET status = 'declined', declined_reason = ?, token_hash = NULL, token_expires_at = NULL, completed_at = ?, updated_at = ? WHERE id = ? AND status IN ('ready','viewed')`, reason, now, now, session.Recipient.ID)
	if err != nil {
		return nil, nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, nil, errors.New("recipient is no longer active")
	}
	if _, err := tx.Exec(`UPDATE envelopes SET status = 'declined', terminal_reason = ?, updated_at = ? WHERE id = ? AND project_id = ? AND status = 'sent'`, reason, now, session.Envelope.ID, session.Envelope.ProjectID); err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(`UPDATE recipients SET token_hash = NULL, token_expires_at = NULL, updated_at = ? WHERE envelope_id = ?`, now, session.Envelope.ID); err != nil {
		return nil, nil, err
	}
	if err := addAuditTx(tx, session.Envelope.ID, session.Envelope.ProjectID, session.Recipient.ID, "envelope.declined", map[string]any{"reason": reason}); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	env, err := getEnvelopeRequired(db, session.Envelope.ProjectID, session.Envelope.ID)
	if err != nil {
		return nil, nil, err
	}
	recipient, _ := getRecipient(db, env.ProjectID, env.ID, session.Recipient.ID)
	return env, recipient, nil
}

func voidEnvelope(db *sql.DB, projectID string, envelopeID int64, reason string) (*Envelope, error) {
	reason = strings.TrimSpace(reason)
	if len(reason) > 1000 {
		return nil, errors.New("void reason is too long")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := nowUTC()
	res, err := tx.Exec(`UPDATE envelopes SET status = 'voided', terminal_reason = ?, updated_at = ? WHERE id = ? AND project_id = ? AND status IN ('draft','sent')`, reason, now, envelopeID, projectID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errors.New("only draft or sent envelopes can be voided")
	}
	if _, err := tx.Exec(`UPDATE recipients SET token_hash = NULL, token_expires_at = NULL, updated_at = ? WHERE envelope_id = ? AND project_id = ?`, now, envelopeID, projectID); err != nil {
		return nil, err
	}
	if err := addAuditTx(tx, envelopeID, projectID, 0, "envelope.voided", map[string]any{"reason": reason}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return getEnvelopeRequired(db, projectID, envelopeID)
}

func addAudit(db *sql.DB, envelopeID int64, projectID string, recipientID int64, eventType string, detail any) error {
	body := "{}"
	if detail != nil {
		b, err := json.Marshal(detail)
		if err != nil {
			return err
		}
		body = string(b)
	}
	var rec any
	if recipientID != 0 {
		rec = recipientID
	}
	_, err := db.Exec(`INSERT INTO audit_events (envelope_id, project_id, recipient_id, event_type, detail_json, occurred_at) VALUES (?, ?, ?, ?, ?, ?)`, envelopeID, projectID, rec, eventType, body, nowUTC())
	return err
}

func addAuditTx(tx *sql.Tx, envelopeID int64, projectID string, recipientID int64, eventType string, detail any) error {
	body := "{}"
	if detail != nil {
		b, err := json.Marshal(detail)
		if err != nil {
			return err
		}
		body = string(b)
	}
	var rec any
	if recipientID != 0 {
		rec = recipientID
	}
	_, err := tx.Exec(`INSERT INTO audit_events (envelope_id, project_id, recipient_id, event_type, detail_json, occurred_at) VALUES (?, ?, ?, ?, ?, ?)`, envelopeID, projectID, rec, eventType, body, nowUTC())
	return err
}

func listAudit(db *sql.DB, projectID string, envelopeID int64) ([]AuditEvent, error) {
	rows, err := db.Query(`SELECT id, envelope_id, COALESCE(recipient_id, 0), event_type, detail_json, occurred_at FROM audit_events WHERE envelope_id = ? AND project_id = ? ORDER BY id`, envelopeID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEvent{}
	for rows.Next() {
		var event AuditEvent
		var detail string
		if err := rows.Scan(&event.ID, &event.EnvelopeID, &event.RecipientID, &event.EventType, &detail, &event.OccurredAt); err != nil {
			return nil, err
		}
		event.Detail = json.RawMessage(detail)
		out = append(out, event)
	}
	return out, rows.Err()
}

func loadCompletionValues(db *sql.DB, projectID string, envelopeID int64) ([]completionValue, error) {
	rows, err := db.Query(`
		SELECT f.id, f.envelope_id, f.recipient_id, f.project_id, f.field_type, f.page,
		       f.x, f.y, f.width, f.height, f.label, f.required, f.created_at,
		       fv.value_text, r.name, fv.signed_at
		FROM fields f
		JOIN field_values fv ON fv.field_id = f.id
		JOIN recipients r ON r.id = f.recipient_id
		WHERE f.envelope_id = ? AND f.project_id = ?
		ORDER BY f.page, f.id`, envelopeID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []completionValue{}
	for rows.Next() {
		var v completionValue
		var required int
		if err := rows.Scan(&v.ID, &v.EnvelopeID, &v.RecipientID, &v.ProjectID, &v.FieldType, &v.Page,
			&v.X, &v.Y, &v.Width, &v.Height, &v.Label, &required, &v.CreatedAt,
			&v.ValueText, &v.RecipientName, &v.SignedAt); err != nil {
			return nil, err
		}
		v.Required = required != 0
		out = append(out, v)
	}
	return out, rows.Err()
}

func markEnvelopeCompleted(db *sql.DB, projectID string, envelopeID int64, completed, audit StorageUpload) (*Envelope, error) {
	now := nowUTC()
	res, err := db.Exec(`
		UPDATE envelopes SET status = 'completed', completed_file_id = ?, completed_sha256 = ?, audit_file_id = ?, completed_at = ?, updated_at = ?
		WHERE id = ? AND project_id = ? AND status = 'sent'`, completed.ID, completed.SHA256, audit.ID, now, now, envelopeID, projectID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		env, err := getEnvelopeRequired(db, projectID, envelopeID)
		if err == nil && env.Status == "completed" {
			return env, nil
		}
		return nil, errors.New("envelope is not ready for completion")
	}
	_ = addAudit(db, envelopeID, projectID, 0, "envelope.completed", map[string]any{"completed_file_id": completed.ID, "audit_file_id": audit.ID, "sha256": completed.SHA256})
	return getEnvelopeRequired(db, projectID, envelopeID)
}
