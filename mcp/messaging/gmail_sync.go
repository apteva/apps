package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	} `json:"history"`
	HistoryID     string `json:"historyId"`
	NextPageToken string `json:"nextPageToken"`
}

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
			if err := a.syncGmailMailbox(ctx, pid, bound.ConnectionID); err != nil {
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

func (a *App) syncGmailMailbox(ctx *sdk.AppCtx, pid string, connectionID int64) error {
	var cursor, mailbox string
	err := ctx.AppDB().QueryRow(`SELECT history_id,mailbox FROM gmail_sync_state WHERE project_id=? AND connection_id=?`, pid, connectionID).Scan(&cursor, &mailbox)
	if err == sql.ErrNoRows {
		mailbox, cursor, err = gmailProfile(ctx, connectionID)
		if err != nil {
			return err
		}
		// First binding begins at the current cursor; historical mailbox
		// import is an explicit future operation, not a surprise on install.
		_, err = ctx.AppDB().Exec(`INSERT OR IGNORE INTO gmail_sync_state(project_id,connection_id,mailbox,history_id) VALUES(?,?,?,?)`, pid, connectionID, mailbox, cursor)
		return err
	}
	if err != nil {
		return err
	}
	pageToken := ""
	seenTokens := map[string]bool{}
	for page := 0; page < 100; page++ {
		args := map[string]any{"startHistoryId": cursor, "maxResults": 100, "historyTypes": []string{"messageAdded"}}
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
	// Search a bounded recent window before advancing the expired cursor.
	// Dedupe on Gmail's immutable message ID makes this safe to repeat.
	pageToken := ""
	for page := 0; page < 5; page++ {
		args := map[string]any{"q": "newer_than:7d -in:sent", "maxResults": 100}
		if pageToken != "" {
			args["pageToken"] = pageToken
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "list_messages", args)
		if err != nil {
			return err
		}
		if res == nil || !res.Success {
			return fmt.Errorf("Gmail resync: %s", truncateResData(res))
		}
		var data gmailMessagePage
		if err := json.Unmarshal(res.Data, &data); err != nil {
			return err
		}
		for _, msg := range data.Messages {
			if err := a.ingestGmailMessage(ctx, pid, connectionID, msg); err != nil {
				return err
			}
		}
		if data.NextPageToken == "" {
			_, err := ctx.AppDB().Exec(`UPDATE gmail_sync_state SET history_id=?,last_synced_at=CURRENT_TIMESTAMP,last_error='' WHERE project_id=? AND connection_id=?`, newCursor, pid, connectionID)
			return err
		}
		pageToken = data.NextPageToken
	}
	return errors.New("Gmail resync exceeds 500 messages; cursor unchanged")
}

func (a *App) ingestGmailMessage(ctx *sdk.AppCtx, pid string, connectionID int64, ref gmailMessageRef) error {
	if ref.ID == "" {
		return nil
	}
	var existing int64
	err := ctx.AppDB().QueryRow(`SELECT id FROM messages WHERE project_id=? AND direction='in' AND provider_slug='gmail' AND provider_connection_id=? AND provider_message_id=?`, pid, connectionID, ref.ID).Scan(&existing)
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
		Raw      string   `json:"raw"`
		LabelIDs []string `json:"labelIds"`
		ThreadID string   `json:"threadId"`
	}
	if err := json.Unmarshal(res.Data, &data); err != nil {
		return err
	}
	for _, label := range data.LabelIDs {
		if label == "SENT" {
			return nil
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
	addresses := normaliseEmailListPlain(append(append([]string{}, parsed.To...), parsed.Cc...))
	if delivered := parsed.Headers["Delivered-To"]; delivered != "" {
		addresses = append(addresses, normaliseEmailListPlain([]string{delivered})...)
	}
	matched := false
	for _, address := range addresses {
		var count int
		if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM senders WHERE project_id=? AND provider='gmail' AND provider_connection_id=? AND address=? AND deleted_at IS NULL`, pid, connectionID, strings.ToLower(address)).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			matched = true
			break
		}
	}
	if !matched {
		return nil
	}
	from := normaliseEmailFromHeader(parsed.From)
	if from == "" {
		from = "unknown@invalid"
	}
	toJSON, _ := json.Marshal(normaliseEmailListPlain(parsed.To))
	ccJSON, _ := json.Marshal(normaliseEmailListPlain(parsed.Cc))
	now := time.Now().UTC().Format(time.RFC3339)
	threadID := data.ThreadID
	if threadID == "" {
		threadID = ref.ThreadID
	}
	result, err := persistInbound(ctx, pid, "email", parsed.Attachments,
		`INSERT OR IGNORE INTO messages(project_id,channel,direction,from_addr,to_addrs,cc_addrs,subject,body_text,body_html,headers,message_id_header,in_reply_to,references_json,status,route_status,received_at,last_event_at,provider_message_id,provider_slug,provider_connection_id,provider_thread_id,envelope_recipients)
		 VALUES (?, 'email', 'in', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'received', 'pending', ?, ?, ?, 'gmail', ?, ?, ?)`,
		pid, from, string(toJSON), string(ccJSON), parsed.Subject, parsed.BodyText, parsed.BodyHTML, string(mustJSON(parsed.Headers)), parsed.MessageID, parsed.InReplyTo, string(mustJSON(parsed.References)), now, now, ref.ID, connectionID, threadID, string(mustJSON(addresses)))
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return nil
	}
	id, _ := result.LastInsertId()
	return processInboundJob(ctx, pid, id, false)
}
