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

type twitterAccountCreds struct {
	AccountID int64
	ConnID    int64
	UserID    string
	Username  string
}

type twitterSyncReport struct {
	NewMentions int      `json:"new_mentions"`
	NewDMs      int      `json:"new_dms"`
	Warnings    []string `json:"warnings,omitempty"`
}

const (
	twitterDMCursorKind       = "dm"
	twitterDMPermissionPrefix = "permission_required:"
	twitterDMPermissionHelp   = "X direct messages require dm.read and dm.write; reconnect this account to grant the new permissions"
)

type twitterUserNode struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Username        string `json:"username"`
	ProfileImageURL string `json:"profile_image_url"`
}

type twitterTweetNode struct {
	ID                  string           `json:"id"`
	Text                string           `json:"text"`
	AuthorID            string           `json:"author_id"`
	CreatedAt           string           `json:"created_at"`
	ConversationID      string           `json:"conversation_id"`
	InReplyToUserID     string           `json:"in_reply_to_user_id"`
	ReferencedTweets    []twitterRefNode `json:"referenced_tweets"`
	EditHistoryTweetIDs []string         `json:"edit_history_tweet_ids"`
}

type twitterRefNode struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type twitterDMEventNode struct {
	ID               string `json:"id"`
	Text             string `json:"text"`
	EventType        string `json:"event_type"`
	CreatedAt        string `json:"created_at"`
	DMConversationID string `json:"dm_conversation_id"`
	SenderID         string `json:"sender_id"`
}

func loadTwitterAccountCreds(ctx *sdk.AppCtx, projectID string, accountID int64) (*twitterAccountCreds, error) {
	var connID int64
	var platform string
	err := ctx.AppDB().QueryRow(
		`SELECT connection_id, platform
		   FROM social_accounts
		  WHERE id=? AND project_id=? AND status='active'`,
		accountID, projectID,
	).Scan(&connID, &platform)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("social_account %d not found or inactive", accountID)
	}
	if err != nil {
		return nil, err
	}
	if platform != "twitter" {
		return nil, fmt.Errorf("social_account %d is not an X/Twitter account (got %q)", accountID, platform)
	}
	userID, username, err := twitterAuthenticatedUser(ctx, connID)
	if err != nil {
		return nil, err
	}
	if userID == "" {
		return nil, errors.New("authenticated X user id missing")
	}
	return &twitterAccountCreds{AccountID: accountID, ConnID: connID, UserID: userID, Username: username}, nil
}

func syncTwitterAccount(ctx *sdk.AppCtx, projectID string, accountID int64) (*twitterSyncReport, error) {
	creds, err := loadTwitterAccountCreds(ctx, projectID, accountID)
	if err != nil {
		return nil, err
	}
	report := &twitterSyncReport{}

	n, warns := syncTwitterMentions(ctx, projectID, creds)
	if n >= 0 {
		report.NewMentions = n
	}
	report.Warnings = append(report.Warnings, warns...)
	n, warns = syncTwitterDMs(ctx, projectID, creds)
	if n >= 0 {
		report.NewDMs = n
	}
	report.Warnings = append(report.Warnings, warns...)

	lastError := strings.Join(report.Warnings, "; ")
	_, _ = ctx.AppDB().Exec(
		`INSERT INTO inbox_cursors (social_account_id, kind, cursor, last_sync_at, last_error)
		 VALUES (?, ?, '', CURRENT_TIMESTAMP, ?)
		 ON CONFLICT(social_account_id, kind) DO UPDATE SET
		   last_sync_at=excluded.last_sync_at, last_error=excluded.last_error`,
		accountID, "all", nullable(lastError),
	)
	return report, nil
}

func syncTwitterMentions(ctx *sdk.AppCtx, projectID string, creds *twitterAccountCreds) (int, []string) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "get_user_mentions", map[string]any{
		"user_id":      creds.UserID,
		"max_results":  100,
		"tweet.fields": "id,text,author_id,created_at,public_metrics,conversation_id,in_reply_to_user_id,referenced_tweets,edit_history_tweet_ids",
		"expansions":   "author_id",
		"user.fields":  "id,name,username,profile_image_url,verified",
	})
	if err != nil {
		return -1, []string{"get_user_mentions: " + err.Error()}
	}
	if res == nil || !res.Success {
		return -1, []string{"get_user_mentions: " + upstreamError(res).Error()}
	}
	var resp struct {
		Data     []twitterTweetNode `json:"data"`
		Includes struct {
			Users []twitterUserNode `json:"users"`
		} `json:"includes"`
	}
	if err := json.Unmarshal(res.Data, &resp); err != nil {
		return -1, []string{"decode X mentions: " + err.Error()}
	}
	users := twitterUsersByID(resp.Includes.Users)
	added := 0
	for _, tw := range resp.Data {
		if tw.ID == "" || tw.AuthorID == creds.UserID {
			continue
		}
		author := users[tw.AuthorID]
		parentID := twitterReplyParentID(tw)
		externalPostID := tw.ConversationID
		if externalPostID == "" {
			externalPostID = tw.ID
		}
		_, inserted, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
			ProjectID:        projectID,
			SocialAccountID:  creds.AccountID,
			Platform:         "twitter",
			Kind:             inboxKindMention,
			ExternalID:       tw.ID,
			ParentExternalID: parentID,
			ExternalPostID:   externalPostID,
			AuthorExternalID: tw.AuthorID,
			AuthorName:       twitterDisplayName(author),
			AuthorHandle:     author.Username,
			AuthorAvatarURL:  author.ProfileImageURL,
			Body:             tw.Text,
			Permalink:        "https://twitter.com/i/web/status/" + tw.ID,
			OccurredAt:       parseTwitterTimestamp(tw.CreatedAt),
			RawJSON:          marshalSafe(tw),
		})
		if err != nil {
			ctx.Logger().Warn("x mention upsert failed", "external_id", tw.ID, "err", err)
			continue
		}
		if inserted {
			added++
		}
	}
	return added, nil
}

func syncTwitterDMs(ctx *sdk.AppCtx, projectID string, creds *twitterAccountCreds) (int, []string) {
	if twitterDMBackoffActive(ctx.AppDB(), creds.AccountID) {
		return -1, []string{twitterDMPermissionHelp}
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "get_dm_events", map[string]any{
		"max_results":     100,
		"event_types":     "MessageCreate",
		"dm_event.fields": "id,text,event_type,created_at,dm_conversation_id,sender_id",
		"expansions":      "sender_id",
		"user.fields":     "id,name,username,profile_image_url,verified",
	})
	if err != nil {
		return -1, []string{"get_dm_events: " + err.Error()}
	}
	if res == nil || !res.Success {
		upstream := upstreamError(res).Error()
		if res != nil && res.Status == 403 {
			recordTwitterDMPermissionFailure(ctx.AppDB(), creds.AccountID)
			return -1, []string{twitterDMPermissionHelp}
		}
		return -1, []string{"get_dm_events: " + upstream}
	}
	var resp struct {
		Data     []twitterDMEventNode `json:"data"`
		Includes struct {
			Users []twitterUserNode `json:"users"`
		} `json:"includes"`
	}
	if err := json.Unmarshal(res.Data, &resp); err != nil {
		return -1, []string{"decode X DMs: " + err.Error()}
	}
	clearTwitterDMSyncError(ctx.AppDB(), creds.AccountID)
	users := twitterUsersByID(resp.Includes.Users)
	added := 0
	for _, dm := range resp.Data {
		if dm.ID == "" || dm.SenderID == "" || dm.SenderID == creds.UserID {
			continue
		}
		author := users[dm.SenderID]
		_, inserted, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
			ProjectID:        projectID,
			SocialAccountID:  creds.AccountID,
			Platform:         "twitter",
			Kind:             inboxKindDM,
			ExternalID:       dm.ID,
			ExternalPostID:   dm.DMConversationID,
			AuthorExternalID: dm.SenderID,
			AuthorName:       twitterDisplayName(author),
			AuthorHandle:     author.Username,
			AuthorAvatarURL:  author.ProfileImageURL,
			Body:             dm.Text,
			OccurredAt:       parseTwitterTimestamp(dm.CreatedAt),
			RawJSON:          marshalSafe(dm),
		})
		if err != nil {
			ctx.Logger().Warn("x dm upsert failed", "external_id", dm.ID, "err", err)
			continue
		}
		if inserted {
			added++
		}
	}
	return added, nil
}

func twitterDMBackoffActive(db *sql.DB, accountID int64) bool {
	var active int
	_ = db.QueryRow(
		`SELECT EXISTS(
		   SELECT 1 FROM inbox_cursors
		    WHERE social_account_id=? AND kind=?
		      AND last_error LIKE ?
		      AND last_sync_at >= datetime('now', '-24 hours')
		 )`,
		accountID, twitterDMCursorKind, twitterDMPermissionPrefix+"%",
	).Scan(&active)
	return active == 1
}

func recordTwitterDMPermissionFailure(db *sql.DB, accountID int64) {
	_, _ = db.Exec(
		`INSERT INTO inbox_cursors (social_account_id, kind, cursor, last_sync_at, last_error)
		 VALUES (?, ?, '', CURRENT_TIMESTAMP, ?)
		 ON CONFLICT(social_account_id, kind) DO UPDATE SET
		   last_sync_at=excluded.last_sync_at, last_error=excluded.last_error`,
		accountID, twitterDMCursorKind, twitterDMPermissionPrefix+" "+twitterDMPermissionHelp,
	)
}

func clearTwitterDMSyncError(db *sql.DB, accountID int64) {
	_, _ = db.Exec(
		`INSERT INTO inbox_cursors (social_account_id, kind, cursor, last_sync_at, last_error)
		 VALUES (?, ?, '', CURRENT_TIMESTAMP, NULL)
		 ON CONFLICT(social_account_id, kind) DO UPDATE SET
		   last_sync_at=excluded.last_sync_at, last_error=NULL`,
		accountID, twitterDMCursorKind,
	)
}

func twitterInboxReply(ctx *sdk.AppCtx, item *inboxItem, body string) inboxOutcome {
	out := inboxOutcome{
		InboxItemID:     item.ID,
		SocialAccountID: item.SocialAccountID,
		Platform:        item.Platform,
	}
	if strings.TrimSpace(body) == "" {
		out.Status, out.Error = "failed", "body required"
		return out
	}
	creds, err := loadTwitterAccountCreds(ctx, item.ProjectID, item.SocialAccountID)
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	switch item.Kind {
	case inboxKindMention, inboxKindComment:
		return twitterReplyToPost(ctx, out, creds, item, body)
	case inboxKindDM:
		return twitterSendDM(ctx, out, creds, item, body)
	default:
		out.Status = "unsupported"
		out.Reason = fmt.Sprintf("X inbox_reply: kind %q has no reply path", item.Kind)
		return out
	}
}

func twitterReplyToPost(ctx *sdk.AppCtx, out inboxOutcome, creds *twitterAccountCreds, item *inboxItem, body string) inboxOutcome {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "post_tweet", map[string]any{
		"text": body,
		"reply": map[string]any{
			"in_reply_to_tweet_id": item.ExternalID,
		},
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	id, url := extractPostIdentity("twitter", res.Data)
	out.Status = "ok"
	out.ExternalID = id
	out.Permalink = url
	_ = markInboxRepliedByExternalID(ctx.AppDB(), creds.AccountID, item.Kind, item.ExternalID)
	if id != "" {
		_, _, _ = upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
			ProjectID:        item.ProjectID,
			SocialAccountID:  creds.AccountID,
			Platform:         "twitter",
			Kind:             item.Kind,
			ExternalID:       id,
			ParentExternalID: item.ExternalID,
			ExternalPostID:   item.ExternalPostID,
			Body:             body,
			Permalink:        url,
			OccurredAt:       time.Now().UTC(),
		})
	}
	return out
}

func twitterSendDM(ctx *sdk.AppCtx, out inboxOutcome, creds *twitterAccountCreds, item *inboxItem, body string) inboxOutcome {
	args := map[string]any{"text": body}
	tool := "send_dm"
	if item.ExternalPostID != "" {
		tool = "send_dm_to_conversation"
		args["dm_conversation_id"] = item.ExternalPostID
	} else if item.AuthorExternalID != "" {
		args["participant_id"] = item.AuthorExternalID
	} else {
		out.Status, out.Error = "failed", "X DM target requires dm_conversation_id or author_external_id"
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, tool, args)
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	out.Status = "ok"
	out.ExternalID = twitterDMResponseID(res.Data)
	_ = markInboxRepliedByExternalID(ctx.AppDB(), creds.AccountID, inboxKindDM, item.ExternalID)
	if out.ExternalID != "" {
		_, _, _ = upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
			ProjectID:        item.ProjectID,
			SocialAccountID:  creds.AccountID,
			Platform:         "twitter",
			Kind:             inboxKindDM,
			ExternalID:       out.ExternalID,
			ParentExternalID: item.ExternalID,
			ExternalPostID:   item.ExternalPostID,
			Body:             body,
			OccurredAt:       time.Now().UTC(),
		})
	}
	return out
}

func twitterUsersByID(users []twitterUserNode) map[string]twitterUserNode {
	out := make(map[string]twitterUserNode, len(users))
	for _, u := range users {
		if u.ID != "" {
			out[u.ID] = u
		}
	}
	return out
}

func twitterDisplayName(u twitterUserNode) string {
	if u.Name != "" {
		return u.Name
	}
	if u.Username != "" {
		return u.Username
	}
	return "X user"
}

func twitterReplyParentID(tw twitterTweetNode) string {
	for _, ref := range tw.ReferencedTweets {
		if ref.Type == "replied_to" && ref.ID != "" {
			return ref.ID
		}
	}
	return ""
}

func parseTwitterTimestamp(s string) time.Time {
	if s == "" {
		return time.Now().UTC()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse("2006-01-02T15:04:05.000Z", s); err == nil {
		return t.UTC()
	}
	return time.Now().UTC()
}

func twitterDMResponseID(data []byte) string {
	var resp struct {
		Data struct {
			DMEventID string `json:"dm_event_id"`
			ID        string `json:"id"`
		} `json:"data"`
		DMEventID string `json:"dm_event_id"`
		ID        string `json:"id"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return ""
	}
	if resp.Data.DMEventID != "" {
		return resp.Data.DMEventID
	}
	if resp.Data.ID != "" {
		return resp.Data.ID
	}
	if resp.DMEventID != "" {
		return resp.DMEventID
	}
	return resp.ID
}
