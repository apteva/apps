package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type gmailMessageRef struct {
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
}
type gmailMessagePage struct {
	Messages      []gmailMessageRef `json:"messages"`
	NextPageToken string            `json:"nextPageToken"`
}
type gmailHistoryPage struct {
	History []struct {
		MessagesAdded []struct {
			Message gmailMessageRef `json:"message"`
		} `json:"messagesAdded"`
		LabelsAdded []struct {
			Message  gmailMessageRef `json:"message"`
			LabelIDs []string        `json:"labelIds"`
		} `json:"labelsAdded"`
	} `json:"history"`
	HistoryID     string `json:"historyId"`
	NextPageToken string `json:"nextPageToken"`
}

const gmailBackfillWindow = 7 * 24 * time.Hour

func (a *App) syncGmailMailboxes(ctx *sdk.AppCtx) error {
	var firstErr error
	for _, bound := range ctx.IntegrationsFor("email_provider") {
		if bound.AppSlug != "gmail" {
			continue
		}
		rows, err := ctx.AppDB().Query(`SELECT DISTINCT project_id FROM senders WHERE provider='gmail' AND provider_connection_id=? AND channel='email' AND deleted_at IS NULL`, bound.ConnectionID)
		if err != nil {
			return err
		}
		var projects []string
		for rows.Next() {
			var pid string
			if err := rows.Scan(&pid); err != nil {
				rows.Close()
				return err
			}
			projects = append(projects, pid)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, pid := range projects {
			if scoped := os.Getenv("APTEVA_PROJECT_ID"); scoped != "" && pid != scoped {
				continue
			}
			// The SDK dispatches global workers once per project. Do not scan
			// every project's mailbox again on each project's tick.
			if scoped := ctx.CurrentProject(); scoped != "" && pid != scoped {
				continue
			}
			if err := a.syncGmailMailbox(ctx.WithProject(pid), pid, bound.ConnectionID); err != nil {
				ctx.Logger().Warn("gmail sync failed", "project_id", pid, "connection_id", bound.ConnectionID, "err", err)
				_, _ = ctx.AppDB().Exec(`UPDATE gmail_sync_state SET last_error=? WHERE project_id=? AND connection_id=?`, truncate(err.Error(), 500), pid, bound.ConnectionID)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

func gmailProfile(ctx *sdk.AppCtx, connectionID int64) (string, string, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "get_profile", map[string]any{})
	if err != nil {
		return "", "", err
	}
	if res == nil || !res.Success {
		return "", "", fmt.Errorf("Gmail profile: %s", truncateResData(res))
	}
	var profile struct {
		EmailAddress string `json:"emailAddress"`
		HistoryID    string `json:"historyId"`
	}
	if err := json.Unmarshal(res.Data, &profile); err != nil {
		return "", "", err
	}
	if profile.EmailAddress == "" || profile.HistoryID == "" {
		return "", "", errors.New("Gmail profile lacks email or history ID")
	}
	return strings.ToLower(profile.EmailAddress), profile.HistoryID, nil
}

func gmailMailboxRecipients(ctx *sdk.AppCtx, pid string, connectionID int64) ([]string, error) {
	var mailbox string
	err := ctx.AppDB().QueryRow(`SELECT mailbox FROM gmail_sync_state WHERE project_id=? AND connection_id=?`, pid, connectionID).Scan(&mailbox)
	if err == sql.ErrNoRows {
		mailbox, _, err = gmailProfile(ctx, connectionID)
	}
	if err != nil {
		return nil, err
	}
	return canonicalSMTPRecipients([]string{mailbox})
}

func (a *App) syncGmailMailbox(ctx *sdk.AppCtx, pid string, connectionID int64) error {
	var cursor, mailbox string
	err := ctx.AppDB().QueryRow(`SELECT history_id,mailbox FROM gmail_sync_state WHERE project_id=? AND connection_id=?`, pid, connectionID).Scan(&cursor, &mailbox)
	if err == sql.ErrNoRows {
		mailbox, cursor, err = gmailProfile(ctx, connectionID)
		if err != nil {
			return err
		}
		// Capture history before importing so arrivals during backfill are
		// covered by the incremental poll, even across worker restarts.
		_, err = ctx.AppDB().Exec(`INSERT OR IGNORE INTO gmail_sync_state(project_id,connection_id,mailbox,history_id) VALUES(?,?,?,?)`, pid, connectionID, mailbox, cursor)
	}
	if err != nil {
		return err
	}
	// One bounded, durable backfill page per tick. Existing installations
	// receive this catch-up too; fresh-mail history is still polled each tick.
	backfillErr := a.backfillGmailMailbox(ctx, pid, connectionID)
	// A failed historical page must not starve newly received messages.
	return errors.Join(backfillErr, a.syncGmailHistory(ctx, pid, connectionID, cursor))
}

func (a *App) syncGmailHistory(ctx *sdk.AppCtx, pid string, connectionID int64, cursor string) error {
	pageToken := ""
	seenTokens := map[string]bool{}
	for page := 0; page < 100; page++ {
		args := map[string]any{"startHistoryId": cursor, "maxResults": 100, "historyTypes": []string{"messageAdded", "labelAdded"}}
		if pageToken != "" {
			args["pageToken"] = pageToken
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "list_history", args)
		if err != nil {
			return err
		}
		if res == nil || !res.Success {
			if res != nil && (res.Status == 404 || strings.Contains(strings.ToLower(truncateResData(res)), "404")) {
				return a.resyncExpiredGmailHistory(ctx, pid, connectionID)
			}
			return fmt.Errorf("Gmail history: %s", truncateResData(res))
		}
		var data gmailHistoryPage
		if err := json.Unmarshal(res.Data, &data); err != nil {
			return err
		}
		for _, record := range data.History {
			for _, added := range record.MessagesAdded {
				if added.Message.ID == "" {
					continue
				}
				if err := a.ingestGmailMessage(ctx, pid, connectionID, added.Message); err != nil {
					return err
				}
			}
			// A draft can keep its ID when Gmail sends it. Its messageAdded
			// event was skipped as DRAFT; the later SENT label must be synced.
			for _, added := range record.LabelsAdded {
				for _, label := range added.LabelIDs {
					if label == "SENT" || label == "INBOX" {
						if err := a.ingestGmailMessage(ctx, pid, connectionID, added.Message); err != nil {
							return err
						}
						break
					}
				}
			}
		}
		if data.NextPageToken == "" {
			if data.HistoryID == "" {
				return errors.New("Gmail history response lacks cursor")
			}
			_, err := ctx.AppDB().Exec(`UPDATE gmail_sync_state SET history_id=?,last_synced_at=CURRENT_TIMESTAMP,last_error='' WHERE project_id=? AND connection_id=?`, data.HistoryID, pid, connectionID)
			return err
		}
		if seenTokens[data.NextPageToken] {
			return errors.New("Gmail history repeated page token")
		}
		seenTokens[data.NextPageToken] = true
		pageToken = data.NextPageToken
	}
	return errors.New("Gmail history page limit reached; cursor unchanged")
}

func (a *App) resyncExpiredGmailHistory(ctx *sdk.AppCtx, pid string, connectionID int64) error {
	_, newCursor, err := gmailProfile(ctx, connectionID)
	if err != nil {
		return err
	}
	// Restart the same resumable catch-up after an expired cursor. No hard
	// 500-message ceiling that permanently stalls busy mailboxes.
	_, err = ctx.AppDB().Exec(`UPDATE gmail_sync_state SET history_id=?,backfill_complete=0,backfill_until=0,backfill_page_token='' WHERE project_id=? AND connection_id=?`, newCursor, pid, connectionID)
	return err
}

func (a *App) backfillGmailMailbox(ctx *sdk.AppCtx, pid string, connectionID int64) error {
	var complete int
	var until int64
	var pageToken string
	err := ctx.AppDB().QueryRow(`SELECT backfill_complete,backfill_until,backfill_page_token FROM gmail_sync_state WHERE project_id=? AND connection_id=?`, pid, connectionID).Scan(&complete, &until, &pageToken)
	if err != nil || complete != 0 {
		return err
	}
	if until == 0 {
		until = time.Now().Unix() + 1
		if _, err := ctx.AppDB().Exec(`UPDATE gmail_sync_state SET backfill_until=? WHERE project_id=? AND connection_id=?`, until, pid, connectionID); err != nil {
			return err
		}
	}
	args := map[string]any{
		"q":          fmt.Sprintf("after:%d before:%d -in:drafts", until-int64(gmailBackfillWindow/time.Second), until),
		"maxResults": 100,
	}
	if pageToken != "" {
		args["pageToken"] = pageToken
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "list_messages", args)
	if err != nil {
		return err
	}
	if res == nil || !res.Success {
		return fmt.Errorf("Gmail backfill: %s", truncateResData(res))
	}
	var data gmailMessagePage
	if err := json.Unmarshal(res.Data, &data); err != nil {
		return err
	}
	if data.NextPageToken != "" && data.NextPageToken == pageToken {
		return errors.New("Gmail backfill repeated page token")
	}
	for _, msg := range data.Messages {
		if err := a.ingestGmailMessage(ctx, pid, connectionID, msg); err != nil {
			return err // retry this page; provider IDs deduplicate partial work
		}
	}
	complete = 0
	if data.NextPageToken == "" {
		complete = 1
	}
	_, err = ctx.AppDB().Exec(`UPDATE gmail_sync_state SET backfill_complete=?,backfill_page_token=? WHERE project_id=? AND connection_id=?`, complete, data.NextPageToken, pid, connectionID)
	return err
}

// Ownership of BOTH directions comes from the authenticated Gmail mailbox,
// never from From/To headers, a route wildcard, or a supplied project hint.
func gmailOwnedMailbox(ctx *sdk.AppCtx, pid string, connectionID int64) ([]string, bool, error) {
	if scoped := os.Getenv("APTEVA_PROJECT_ID"); scoped != "" && scoped != pid {
		return nil, false, nil
	}
	if scoped := ctx.CurrentProject(); scoped != "" && scoped != pid {
		return nil, false, nil
	}
	bound, err := emailBinding(ctx, connectionID)
	if err != nil || bound.AppSlug != "gmail" {
		return nil, false, errors.New("Gmail sync connection is not bound")
	}
	addresses, err := gmailMailboxRecipients(ctx, pid, connectionID)
	if err != nil {
		return nil, false, err
	}
	owner, _, err := inboundEmailOwnership(ctx, addresses, "gmail", connectionID)
	if errors.Is(err, errInboundOwnership) {
		return nil, false, nil
	}
	return addresses, err == nil && owner == pid, err
}

func (a *App) ingestGmailMessage(ctx *sdk.AppCtx, pid string, connectionID int64, ref gmailMessageRef) error {
	if ref.ID == "" {
		return nil
	}
	addresses, owned, err := gmailOwnedMailbox(ctx, pid, connectionID)
	if err != nil || !owned {
		return err
	}
	var existing int64
	err = ctx.AppDB().QueryRow(`SELECT id FROM messages WHERE project_id=? AND provider_slug='gmail' AND provider_connection_id=? AND provider_message_id=?`, pid, connectionID, ref.ID).Scan(&existing)
	if err == nil {
		return scheduleInboundRetry(ctx, pid, existing)
	}
	if err != sql.ErrNoRows {
		return err
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "get_raw_message", map[string]any{"messageId": ref.ID, "format": "raw"})
	if err != nil {
		return err
	}
	if res == nil || !res.Success {
		if res != nil && res.Status == 404 {
			return nil
		} // deleted between history and fetch
		return fmt.Errorf("Gmail message %s: %s", ref.ID, truncateResData(res))
	}
	var data struct {
		Raw          string   `json:"raw"`
		LabelIDs     []string `json:"labelIds"`
		ThreadID     string   `json:"threadId"`
		InternalDate string   `json:"internalDate"`
	}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		return err
	}
	sent := false
	for _, label := range data.LabelIDs {
		if label == "DRAFT" || label == "TRASH" || label == "SPAM" {
			return nil
		}
		if label == "SENT" {
			sent = true
		}
	}
	if data.Raw == "" {
		return fmt.Errorf("Gmail message %s has no raw MIME", ref.ID)
	}
	raw, err := base64.RawURLEncoding.DecodeString(data.Raw)
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(data.Raw)
		if err != nil {
			return err
		}
	}
	parsed, err := parseRawEml(raw, ref.ID)
	if err != nil {
		return err
	}
	from := normaliseEmailFromHeader(parsed.From)
	if from == "" {
		from = "unknown@invalid"
	}
	toJSON, _ := json.Marshal(normaliseEmailListPlain(parsed.To))
	ccJSON, _ := json.Marshal(normaliseEmailListPlain(parsed.Cc))
	now := time.Now().UTC().Format(time.RFC3339)
	occurred := gmailMessageTime(data.InternalDate, parsed.Headers, now)
	threadID := data.ThreadID
	if threadID == "" {
		threadID = ref.ThreadID
	}
	if sent {
		return persistGmailSent(ctx, pid, connectionID, ref.ID, threadID, from, occurred, now, parsed)
	}
	result, err := persistInbound(ctx, pid, "email", parsed.Attachments,
		`INSERT OR IGNORE INTO messages(project_id,channel,direction,from_addr,to_addrs,cc_addrs,subject,body_text,body_html,headers,message_id_header,in_reply_to,references_json,status,route_status,created_at,received_at,last_event_at,provider_message_id,provider_slug,provider_connection_id,provider_thread_id,envelope_recipients)
		 VALUES (?, 'email', 'in', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'received', 'pending', ?, ?, ?, ?, 'gmail', ?, ?, ?)`,
		pid, from, string(toJSON), string(ccJSON), parsed.Subject, parsed.BodyText, parsed.BodyHTML, string(mustJSON(parsed.Headers)), parsed.MessageID, parsed.InReplyTo, string(mustJSON(parsed.References)), occurred, occurred, now, ref.ID, connectionID, threadID, string(mustJSON(addresses)))
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil
	}
	id, _ := result.LastInsertId()
	return processGmailImport(ctx, pid, id)
}

func gmailMessageTime(internalDate string, headers map[string]string, fallback string) string {
	if millis, err := strconv.ParseInt(internalDate, 10, 64); err == nil && millis > 0 {
		return time.UnixMilli(millis).UTC().Format(time.RFC3339Nano)
	}
	if date, err := mail.ParseDate(headers["Date"]); err == nil {
		return date.UTC().Format(time.RFC3339Nano)
	}
	return fallback
}

// Import an already-sent record, never call the send transport. It has a
// durable attachment job but must NEVER enter inbound CRM routing/events.
func persistGmailSent(ctx *sdk.AppCtx, pid string, connectionID int64, providerID, threadID, from, occurred, now string, parsed *parsedInbound) error {
	// The worker can observe SENT before the send request has saved its
	// provider response (or after that response was lost). Apteva stores its
	// RFC Message-ID before sending; reconcile that pending row rather than
	// racing the send path to create a second record for the same delivery.
	if parsed.MessageID != "" {
		res, err := ctx.AppDB().Exec(`UPDATE messages SET status='sent',status_reason='',provider_message_id=?,provider_thread_id=?,sent_at=?,last_event_at=?
			WHERE project_id=? AND direction='out' AND provider_slug='gmail' AND provider_connection_id=? AND message_id_header=?
			AND status='pending' AND (provider_message_id IS NULL OR provider_message_id='')`,
			providerID, threadID, occurred, now, pid, connectionID, parsed.MessageID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			emitMessagingEvent(ctx, pid, "message.event", map[string]any{"channel": "email", "direction": "out", "kind": "synced", "status": "sent", "provider": "gmail"})
			return nil
		}
	}
	to := normaliseEmailListPlain(parsed.To)
	cc := normaliseEmailListPlain(parsed.Cc)
	bcc := normaliseEmailListPlain(splitAddrList(parsed.Headers["Bcc"]))
	result, err := persistInbound(ctx, pid, "gmail-sent", parsed.Attachments,
		`INSERT OR IGNORE INTO messages(project_id,channel,direction,from_addr,to_addrs,cc_addrs,bcc_addrs,subject,body_text,body_html,headers,message_id_header,in_reply_to,references_json,status,route_status,created_at,sent_at,last_event_at,provider_message_id,provider_slug,provider_connection_id,provider_thread_id)
		 VALUES (?, 'email', 'out', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'sent', 'pending', ?, ?, ?, ?, 'gmail', ?, ?)`,
		pid, from, string(mustJSON(to)), string(mustJSON(cc)), string(mustJSON(bcc)), parsed.Subject, parsed.BodyText, parsed.BodyHTML, string(mustJSON(parsed.Headers)), parsed.MessageID, parsed.InReplyTo, string(mustJSON(parsed.References)), occurred, occurred, now, providerID, connectionID, threadID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil
	}
	id, _ := result.LastInsertId()
	return processGmailImport(ctx, pid, id)
}

func processGmailImport(ctx *sdk.AppCtx, pid string, id int64) error {
	// Insertion and its recoverable attachment/routing job already committed
	// together. Downstream outages must not hold the entire mailbox cursor
	// hostage; the SDK recovery worker retries this individual durable job.
	if err := processInboundJob(ctx, pid, id, false); err != nil {
		ctx.Logger().Warn("Gmail import processing pending", "message_id", id, "err", err)
	}
	return nil
}
