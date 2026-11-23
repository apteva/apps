package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// SaaS v0.13.1 -- email outbox.
//
// Every account email is stored in saas_email_outbox before it is sent, then
// sent through Messaging's send_message with channel "email". The sender is
// the plan's email_from / email_from_name metadata; without it Messaging uses
// the project's default email sender. The address must be a verified,
// sending-enabled sender in Messaging.
//
// Messaging's answer decides what happens next:
//   - an error: Messaging stored nothing (or returns the stored row on a repeat),
//     so the retry reuses the same idempotency key;
//   - status "failed": Messaging stored a failed message under the key and
//     returns it on every repeat, so the retry uses a new ":retry:N" key;
//   - any other status ("sent", "queued", "pending", ...): the email is done.
//     "pending" means the provider's answer was unclear; retrying could send
//     the email twice, so it is never retried.
//
// The email-outbox worker retries failed emails with backoff and gives up
// after emailOutboxMaxAttempts.

const (
	emailOutboxPending   = "pending"
	emailOutboxSent      = "sent"
	emailOutboxAbandoned = "abandoned"

	emailOutboxMaxAttempts  = 10
	emailOutboxBaseBackoff  = 2 * time.Minute
	emailOutboxMaxBackoff   = 6 * time.Hour
	emailOutboxSweepLimit   = 50
	emailSenderNameMaxChars = 200
)

type emailOutboxRow struct {
	ID             int64
	ProjectID      string
	AccountID      string
	Kind           string
	Recipient      string
	Subject        string
	Body           string
	BodyHTML       string
	Detail         map[string]any
	IdempotencyKey string
	SendKey        string
	Status         string
	Attempts       int64
	// Due is true when next_attempt_at has passed.
	Due bool
}

func emailEventDetail(kind, recipient string, extra map[string]any) map[string]any {
	detail := map[string]any{"kind": kind, "recipient_email": recipient}
	for k, v := range extra {
		detail[k] = v
	}
	return detail
}

// validatePlanEmailSender checks the optional email_from / email_from_name
// plan metadata. Whether the address is a verified Messaging sender is only
// known when an email is sent.
func validatePlanEmailSender(metadata map[string]any) error {
	if raw, ok := metadata["email_from"]; ok && raw != nil {
		from, isString := raw.(string)
		from = strings.TrimSpace(from)
		if !isString || (from != "" && !plainEmailAddress(from)) {
			return errors.New("metadata.email_from must be a plain email address such as billing@example.com")
		}
	}
	if raw, ok := metadata["email_from_name"]; ok && raw != nil {
		name, isString := raw.(string)
		if !isString || strings.ContainsAny(name, "\r\n") || len(name) > emailSenderNameMaxChars {
			return fmt.Errorf("metadata.email_from_name must be a single line of at most %d characters", emailSenderNameMaxChars)
		}
	}
	return nil
}

func plainEmailAddress(value string) bool {
	if strings.ContainsAny(value, " \t\r\n<>,;\"") {
		return false
	}
	at := strings.LastIndex(value, "@")
	return at > 0 && at < len(value)-1 && strings.Contains(value[at+1:], ".")
}

// accountEmailSender returns the plan's sender, or empty values to let
// Messaging use the project's default email sender.
func accountEmailSender(db *sql.DB, pid, planKey string) (string, string) {
	if planKey == "" {
		return "", ""
	}
	plan, err := dbPlanGet(db, pid, planKey)
	if err != nil || plan == nil {
		return "", ""
	}
	meta := mapFromAny(plan.Metadata)
	return strings.TrimSpace(strArg(meta, "email_from")), strings.TrimSpace(strArg(meta, "email_from_name"))
}

func dbEmailOutboxEnqueue(db *sql.DB, pid string, acct *Account, msg accountEmail) (*emailOutboxRow, error) {
	key := strings.TrimSpace(msg.IdempotencyKey)
	if key == "" {
		key = fmt.Sprintf("saas:%s:%s:%d", acct.ID, msg.Kind, time.Now().UTC().UnixNano())
	}
	detail := jsonOrEmpty(msg.Detail, "{}")
	if _, err := db.Exec(`INSERT OR IGNORE INTO saas_email_outbox
		(project_id, account_id, kind, recipient, subject, body, body_html, detail_json, idempotency_key, send_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, acct.ID, msg.Kind, strings.TrimSpace(acct.OwnerEmail), msg.Subject, msg.Body, msg.BodyHTML, detail, key, key); err != nil {
		return nil, err
	}
	rows, err := dbEmailOutboxQuery(db, `WHERE project_id=? AND idempotency_key=?`, pid, key)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return rows[0], nil
}

func dbEmailOutboxQuery(db *sql.DB, where string, args ...any) ([]*emailOutboxRow, error) {
	rows, err := db.Query(`SELECT id, project_id, account_id, kind, recipient, subject, body, body_html, detail_json,
		idempotency_key, send_key, status, attempts, COALESCE(next_attempt_at <= CURRENT_TIMESTAMP, 1)
		FROM saas_email_outbox `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*emailOutboxRow
	for rows.Next() {
		var r emailOutboxRow
		var detail string
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.AccountID, &r.Kind, &r.Recipient, &r.Subject, &r.Body, &r.BodyHTML, &detail,
			&r.IdempotencyKey, &r.SendKey, &r.Status, &r.Attempts, &r.Due); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(detail), &r.Detail)
		out = append(out, &r)
	}
	return out, rows.Err()
}

func emailOutboxBackoff(attempts int64) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := emailOutboxBaseBackoff
	for i := int64(1); i < attempts; i++ {
		d *= 2
		if d >= emailOutboxMaxBackoff {
			return emailOutboxMaxBackoff
		}
	}
	return d
}

// attemptOutboxEmail sends one outbox email and records the outcome. It
// returns true when Messaging accepted the email.
func (a *App) attemptOutboxEmail(ctx *sdk.AppCtx, row *emailOutboxRow) bool {
	db := ctx.AppDB()
	attempt := row.Attempts + 1
	input := map[string]any{
		"_project_id":     row.ProjectID,
		"channel":         "email",
		"to":              row.Recipient,
		"subject":         row.Subject,
		"body":            row.Body,
		"idempotency_key": row.SendKey,
	}
	if row.BodyHTML != "" {
		input["body_html"] = row.BodyHTML
	}
	planKey := ""
	if acct, err := dbAccountGet(db, row.ProjectID, row.AccountID); err == nil && acct != nil {
		planKey = acct.PlanKey
	}
	from, fromName := accountEmailSender(db, row.ProjectID, planKey)
	if from != "" {
		input["from"] = from
	}
	if fromName != "" {
		input["from_name"] = fromName
	}

	detail := emailEventDetail(row.Kind, row.Recipient, row.Detail)
	detail["outbox_id"] = row.ID
	detail["attempt"] = attempt
	if from != "" {
		detail["from"] = from
	}

	var out map[string]any
	callErr := ctx.PlatformAPI().CallAppResult("messaging", "send_message", input, &out)
	messageID := int64Arg(out, "id")
	messageStatus := strings.ToLower(strings.TrimSpace(strArg(out, "status")))

	if callErr == nil && messageStatus != "failed" {
		res, err := db.Exec(`UPDATE saas_email_outbox SET status=?, attempts=?, last_error='', message_id=?, message_status=?,
			sent_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id=? AND attempts=? AND status=?`,
			emailOutboxSent, attempt, nullableInt64(messageID), messageStatus, row.ID, row.Attempts, emailOutboxPending)
		if err != nil || rowsAffected(res) == 0 {
			// Another attempt already recorded this email.
			return err == nil
		}
		if messageID != 0 {
			detail["message_id"] = messageID
		}
		if messageStatus != "" {
			detail["message_status"] = messageStatus
		}
		_ = recordEvent(db, row.ProjectID, row.AccountID, "notification.sent", "saas", detail)
		return true
	}

	nextKey := row.SendKey
	var reason string
	if callErr != nil {
		reason = callErr.Error()
	} else {
		reason = firstNonEmpty(strArg(out, "status_reason"), "messaging reported status failed")
		// Messaging stored a failed message under this key and returns it on
		// every repeat, so the next attempt needs a new key.
		nextKey = fmt.Sprintf("%s:retry:%d", row.IdempotencyKey, attempt+1)
		if messageID != 0 {
			detail["message_id"] = messageID
		}
	}
	if len(reason) > 1000 {
		reason = reason[:1000]
	}
	status := emailOutboxPending
	final := attempt >= emailOutboxMaxAttempts
	if final {
		status = emailOutboxAbandoned
	}
	backoff := int64(emailOutboxBackoff(attempt) / time.Second)
	res, err := db.Exec(`UPDATE saas_email_outbox SET status=?, attempts=?, last_error=?, send_key=?, message_id=?, message_status=?,
		next_attempt_at=datetime('now', ?), updated_at=CURRENT_TIMESTAMP WHERE id=? AND attempts=? AND status=?`,
		status, attempt, reason, nextKey, nullableInt64(messageID), messageStatus,
		fmt.Sprintf("+%d seconds", backoff), row.ID, row.Attempts, emailOutboxPending)
	if err != nil || rowsAffected(res) == 0 {
		return false
	}
	detail["error"] = reason
	detail["final"] = final
	if !final {
		detail["retry_in_seconds"] = backoff
	}
	_ = recordEvent(db, row.ProjectID, row.AccountID, "notification.failed", "saas", detail)
	return false
}

// retryOutboxEmails is the email-outbox worker: it retries pending emails
// whose backoff has passed.
func (a *App) retryOutboxEmails(ctx *sdk.AppCtx) error {
	where := `WHERE status=? AND next_attempt_at <= CURRENT_TIMESTAMP`
	args := []any{emailOutboxPending}
	if pid := projectID(ctx, nil); pid != "" {
		where += ` AND project_id=?`
		args = append(args, pid)
	}
	where += ` ORDER BY next_attempt_at, id LIMIT ?`
	args = append(args, emailOutboxSweepLimit)
	rows, err := dbEmailOutboxQuery(ctx.AppDB(), where, args...)
	if err != nil {
		return err
	}
	for _, row := range rows {
		a.attemptOutboxEmail(ctx, row)
	}
	return nil
}

func rowsAffected(res sql.Result) int64 {
	if res == nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}
