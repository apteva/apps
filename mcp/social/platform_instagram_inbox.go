// Instagram inbox — comments, DMs and mentions via the Meta Graph
// API. Calls flow through the facebook-api integration (IG Business
// is gated through a linked FB Page, hence the shared catalog), and
// every write needs the page-level access_token from
// social_accounts.page_credentials.
//
// The poll side (syncInstagramAccount) is invoked by inbox_sync; the
// action side (instagramInbox*) is invoked from dispatchInboxAction
// in inbox_tools.go. Both share igAccountCreds for the connection +
// IG user id + page token triplet.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// igAccountCreds is the minimum every IG Graph call needs: connection
// id (for the executor), IG user id (for endpoints that take
// {instagramAccountId}), and the page-level access_token (Meta rejects
// user-level tokens for these endpoints with error 210).
type igAccountCreds struct {
	AccountID int64
	ConnID    int64
	IGUserID  string
	PageToken string
}

// loadIGAccountCreds fetches the credential triplet for an active IG
// social_accounts row, project-scoped. Returns a clear "needs_reauth"
// error when the page token is missing — most likely the account was
// connected before the IG inbox scopes were added and the user needs
// to reconnect.
func loadIGAccountCreds(db *sql.DB, projectID string, accountID int64) (*igAccountCreds, error) {
	var (
		connID    int64
		igUserID  string
		pageCreds string
		platform  string
	)
	err := db.QueryRow(
		`SELECT connection_id, COALESCE(external_account_id,''),
		        COALESCE(page_credentials,''), platform
		   FROM social_accounts
		  WHERE id=? AND project_id=? AND status='active'`,
		accountID, projectID,
	).Scan(&connID, &igUserID, &pageCreds, &platform)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("social_account %d not found or inactive", accountID)
	}
	if err != nil {
		return nil, err
	}
	if platform != "instagram" {
		return nil, fmt.Errorf("social_account %d is not an instagram account (got %q)", accountID, platform)
	}
	token := extractPageToken(pageCreds)
	if token == "" {
		return nil, errors.New("instagram page access_token missing — reconnect the account to grant inbox scopes")
	}
	if igUserID == "" {
		return nil, errors.New("instagram user id missing on social_accounts row")
	}
	return &igAccountCreds{
		AccountID: accountID,
		ConnID:    connID,
		IGUserID:  igUserID,
		PageToken: token,
	}, nil
}

// ─── poll: comments + DMs + mentions ───────────────────────────────

// instagramSyncReport is what syncInstagramAccount returns to the
// caller of inbox_sync. Per-kind counts let the UI show "12 new
// comments, 3 new DMs" without re-querying the table.
type instagramSyncReport struct {
	NewComments int      `json:"new_comments"`
	NewDMs      int      `json:"new_dms"`
	NewMentions int      `json:"new_mentions"`
	Warnings    []string `json:"warnings,omitempty"`
}

// syncInstagramAccount runs the full IG inbox pull for one account:
// recent comments on our own posts, recent DM conversations, and
// tagged-in media (mentions). Best-effort — a comment fetch failing
// on one post doesn't abort the run; the failure surfaces in
// Warnings. Cursors get bumped at the end so the next sync only
// fetches deltas where Meta's pagination supports it.
func syncInstagramAccount(ctx *sdk.AppCtx, pid string, accountID int64) (*instagramSyncReport, error) {
	if pid == "" {
		return nil, errors.New("project_id required")
	}
	creds, err := loadIGAccountCreds(ctx.AppDB(), pid, accountID)
	if err != nil {
		return nil, err
	}
	report := &instagramSyncReport{}

	if n, warns := syncInstagramComments(ctx, pid, creds); n >= 0 {
		report.NewComments = n
		report.Warnings = append(report.Warnings, warns...)
	}
	if n, warns := syncInstagramDMs(ctx, pid, creds); n >= 0 {
		report.NewDMs = n
		report.Warnings = append(report.Warnings, warns...)
	}
	if n, warns := syncInstagramMentions(ctx, pid, creds); n >= 0 {
		report.NewMentions = n
		report.Warnings = append(report.Warnings, warns...)
	}
	_, _ = ctx.AppDB().Exec(
		`INSERT INTO inbox_cursors (social_account_id, kind, cursor, last_sync_at)
		 VALUES (?, ?, '', CURRENT_TIMESTAMP)
		 ON CONFLICT(social_account_id, kind) DO UPDATE SET
		   last_sync_at=excluded.last_sync_at, last_error=NULL`,
		accountID, "all",
	)
	return report, nil
}

// igCommentNode mirrors what list_media_comments and a comment's
// inline replies edge return. timestamp is Graph API format
// (ISO 8601). The replies edge is one level deep in the default
// fields selector; deeper nesting needs separate calls.
type igCommentNode struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Username  string `json:"username"`
	Timestamp string `json:"timestamp"`
	LikeCount int    `json:"like_count"`
	Hidden    bool   `json:"hidden"`
	ParentID  string `json:"parent_id"`
	Replies   struct {
		Data []igCommentNode `json:"data"`
	} `json:"replies"`
}

// syncInstagramComments iterates this account's own posts (the
// post_targets rows for the account) and fetches comments on each.
// Returns (count, warnings); count is -1 only if the whole loop
// couldn't start (rare).
func syncInstagramComments(ctx *sdk.AppCtx, projectID string, creds *igAccountCreds) (int, []string) {
	rows, err := ctx.AppDB().Query(
		`SELECT pt.id, pt.post_id, COALESCE(pt.platform_post_id,'')
		   FROM post_targets pt
		   JOIN posts p ON p.id = pt.post_id
		  WHERE pt.social_account_id=?
		    AND pt.status='published'
		    AND COALESCE(pt.platform_post_id,'') != ''
		    AND p.project_id=?
		    AND p.published_at IS NOT NULL
		    AND datetime(p.published_at) >= datetime('now', '-30 days')
		  ORDER BY pt.published_at DESC
		  LIMIT 50`,
		creds.AccountID, projectID,
	)
	if err != nil {
		return -1, []string{"list ig post_targets for comments: " + err.Error()}
	}
	defer rows.Close()

	type mediaTarget struct {
		targetID, postID int64
		mediaID          string
	}
	var targets []mediaTarget
	for rows.Next() {
		var t mediaTarget
		if err := rows.Scan(&t.targetID, &t.postID, &t.mediaID); err == nil {
			targets = append(targets, t)
		}
	}

	added := 0
	var warns []string
	for _, t := range targets {
		n, w := fetchAndUpsertIGComments(ctx, projectID, creds, t.postID, t.mediaID)
		added += n
		if w != "" {
			warns = append(warns, w)
		}
	}
	return added, warns
}

func fetchAndUpsertIGComments(ctx *sdk.AppCtx, projectID string, creds *igAccountCreds, postID int64, mediaID string) (int, string) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "list_media_comments", map[string]any{
		"mediaId":      mediaID,
		"access_token": creds.PageToken,
		"limit":        25,
	})
	if err != nil {
		return 0, fmt.Sprintf("list_media_comments(%s): %v", mediaID, err)
	}
	if res == nil || !res.Success {
		return 0, fmt.Sprintf("list_media_comments(%s): %v", mediaID, upstreamError(res))
	}
	var resp struct {
		Data []igCommentNode `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &resp); err != nil {
		return 0, fmt.Sprintf("decode comments(%s): %v", mediaID, err)
	}
	added := 0
	for _, c := range resp.Data {
		if upsertIGComment(ctx, projectID, creds, postID, mediaID, "", c) {
			added++
		}
		for _, r := range c.Replies.Data {
			if upsertIGComment(ctx, projectID, creds, postID, mediaID, c.ID, r) {
				added++
			}
		}
	}
	return added, ""
}

func upsertIGComment(ctx *sdk.AppCtx, projectID string, creds *igAccountCreds, postID int64, mediaID, parentID string, c igCommentNode) bool {
	if c.ID == "" {
		return false
	}
	occurred := parseIGTimestamp(c.Timestamp)
	_, inserted, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:        projectID,
		SocialAccountID:  creds.AccountID,
		Platform:         "instagram",
		Kind:             inboxKindComment,
		ExternalID:       c.ID,
		ParentExternalID: parentID,
		PostID:           postID,
		ExternalPostID:   mediaID,
		AuthorHandle:     c.Username,
		AuthorName:       c.Username,
		Body:             c.Text,
		Permalink:        "", // IG comments don't have permalinks via Graph
		OccurredAt:       occurred,
		RawJSON:          marshalSafe(c),
	})
	if err != nil {
		ctx.Logger().Warn("ig comment upsert failed", "external_id", c.ID, "err", err)
		return false
	}
	return inserted
}

// igConversationNode is what list_conversations + get_conversation
// return. participants.data[].id is the IGSID; the IG account's own
// id appears in the same list and must be filtered out to find the
// "other" participant.
type igConversationNode struct {
	ID           string `json:"id"`
	UpdatedTime  string `json:"updated_time"`
	Participants struct {
		Data []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"data"`
	} `json:"participants"`
	Messages struct {
		Data []igMessageNode `json:"data"`
	} `json:"messages"`
}

type igMessageNode struct {
	ID          string `json:"id"`
	CreatedTime string `json:"created_time"`
	Message     string `json:"message"`
	From        struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"from"`
	To struct {
		Data []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"data"`
	} `json:"to"`
	Attachments json.RawMessage `json:"attachments,omitempty"`
}

// syncInstagramDMs pulls the conversation list, then expands each
// thread via get_conversation to surface the actual messages. We
// store one inbox row per message (kind=dm) so reply threads render
// naturally; parent_external_id links replies to their parent
// message when the conversation has more than one.
func syncInstagramDMs(ctx *sdk.AppCtx, projectID string, creds *igAccountCreds) (int, []string) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "list_conversations", map[string]any{
		"instagramAccountId": creds.IGUserID,
		"platform":           "instagram",
		"access_token":       creds.PageToken,
		"limit":              25,
	})
	if err != nil {
		return -1, []string{"list_conversations: " + err.Error()}
	}
	if res == nil || !res.Success {
		return -1, []string{"list_conversations: " + upstreamError(res).Error()}
	}
	var resp struct {
		Data []igConversationNode `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &resp); err != nil {
		return -1, []string{"decode conversations: " + err.Error()}
	}
	added := 0
	var warns []string
	for _, conv := range resp.Data {
		n, w := expandAndUpsertIGConversation(ctx, projectID, creds, conv.ID)
		added += n
		if w != "" {
			warns = append(warns, w)
		}
	}
	return added, warns
}

func expandAndUpsertIGConversation(ctx *sdk.AppCtx, projectID string, creds *igAccountCreds, conversationID string) (int, string) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "get_conversation", map[string]any{
		"conversationId": conversationID,
		"access_token":   creds.PageToken,
	})
	if err != nil {
		return 0, fmt.Sprintf("get_conversation(%s): %v", conversationID, err)
	}
	if res == nil || !res.Success {
		return 0, fmt.Sprintf("get_conversation(%s): %v", conversationID, upstreamError(res))
	}
	var conv igConversationNode
	if err := json.Unmarshal(res.Data, &conv); err != nil {
		return 0, fmt.Sprintf("decode conversation(%s): %v", conversationID, err)
	}

	// Messages arrive newest-first from the Graph API; sorting ASC
	// gives a stable parent → child chain for the next message in the
	// same thread (each message points to its immediate predecessor).
	msgs := append([]igMessageNode(nil), conv.Messages.Data...)
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}

	added := 0
	var prevID string
	for _, m := range msgs {
		// Skip our own outbound — we already wrote a 'replied' row when
		// we sent it via instagramInboxReply.
		if m.From.ID == creds.IGUserID {
			prevID = m.ID
			continue
		}
		occurred := parseIGTimestamp(m.CreatedTime)
		mediaJSON := ""
		if len(m.Attachments) > 0 {
			mediaJSON = string(m.Attachments)
		}
		_, inserted, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
			ProjectID:        projectID,
			SocialAccountID:  creds.AccountID,
			Platform:         "instagram",
			Kind:             inboxKindDM,
			ExternalID:       m.ID,
			ParentExternalID: prevID,
			AuthorExternalID: m.From.ID,
			AuthorName:       m.From.Username,
			AuthorHandle:     m.From.Username,
			Body:             m.Message,
			MediaJSON:        mediaJSON,
			OccurredAt:       occurred,
			RawJSON:          marshalSafe(m),
		})
		if err != nil {
			ctx.Logger().Warn("ig dm upsert failed", "external_id", m.ID, "err", err)
		} else if inserted {
			added++
		}
		prevID = m.ID
	}
	return added, ""
}

// igTagNode is the shape of /{ig-user-id}/tags response items —
// media the IG account has been tagged in.
type igTagNode struct {
	ID         string `json:"id"`
	Caption    string `json:"caption"`
	MediaType  string `json:"media_type"`
	MediaURL   string `json:"media_url"`
	Permalink  string `json:"permalink"`
	Username   string `json:"username"`
	Timestamp  string `json:"timestamp"`
	LikeCount  int    `json:"like_count"`
	CommentCnt int    `json:"comments_count"`
}

func syncInstagramMentions(ctx *sdk.AppCtx, projectID string, creds *igAccountCreds) (int, []string) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "list_my_tags", map[string]any{
		"instagramAccountId": creds.IGUserID,
		"access_token":       creds.PageToken,
		"limit":              25,
	})
	if err != nil {
		return -1, []string{"list_my_tags: " + err.Error()}
	}
	if res == nil || !res.Success {
		return -1, []string{"list_my_tags: " + upstreamError(res).Error()}
	}
	var resp struct {
		Data []igTagNode `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &resp); err != nil {
		return -1, []string{"decode tags: " + err.Error()}
	}
	added := 0
	for _, t := range resp.Data {
		if t.ID == "" {
			continue
		}
		occurred := parseIGTimestamp(t.Timestamp)
		_, inserted, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
			ProjectID:       projectID,
			SocialAccountID: creds.AccountID,
			Platform:        "instagram",
			Kind:            inboxKindMention,
			ExternalID:      t.ID,
			ExternalPostID:  t.ID,
			AuthorHandle:    t.Username,
			AuthorName:      t.Username,
			Body:            t.Caption,
			Permalink:       t.Permalink,
			OccurredAt:      occurred,
			RawJSON:         marshalSafe(t),
		})
		if err != nil {
			ctx.Logger().Warn("ig mention upsert failed", "external_id", t.ID, "err", err)
			continue
		}
		if inserted {
			added++
		}
	}
	return added, nil
}

// ─── actions: reply / private_reply / hide / unhide / delete ───────

// instagramInboxReply dispatches based on the item's kind. Comment
// → reply_to_comment; DM → send_message with recipient.id. Returns
// the standard inbox outcome envelope.
func instagramInboxReply(ctx *sdk.AppCtx, item *inboxItem, body string) inboxOutcome {
	out := inboxOutcome{
		InboxItemID:     item.ID,
		SocialAccountID: item.SocialAccountID,
		Platform:        item.Platform,
	}
	if body == "" {
		out.Status, out.Error = "failed", "body required"
		return out
	}
	creds, err := loadIGAccountCreds(ctx.AppDB(), item.ProjectID, item.SocialAccountID)
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	switch item.Kind {
	case inboxKindComment:
		return igReplyToComment(ctx, out, creds, item, body)
	case inboxKindDM:
		return igSendDM(ctx, out, creds, item, body)
	default:
		out.Status = "unsupported"
		out.Reason = fmt.Sprintf("instagram inbox_reply: kind %q has no reply path", item.Kind)
		return out
	}
}

func igReplyToComment(ctx *sdk.AppCtx, out inboxOutcome, creds *igAccountCreds, item *inboxItem, body string) inboxOutcome {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "reply_to_comment", map[string]any{
		"commentId":    item.ExternalID,
		"message":      body,
		"access_token": creds.PageToken,
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	var resp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(res.Data, &resp)
	out.Status = "ok"
	out.ExternalID = resp.ID
	if err := markInboxRepliedByExternalID(ctx.AppDB(), creds.AccountID, inboxKindComment, item.ExternalID); err != nil {
		ctx.Logger().Warn("mark replied failed", "err", err)
	}
	// Record our own reply as an inbox row so the thread renders
	// without waiting for the next poll.
	_, _, _ = upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:        item.ProjectID,
		SocialAccountID:  creds.AccountID,
		Platform:         "instagram",
		Kind:             inboxKindComment,
		ExternalID:       resp.ID,
		ParentExternalID: item.ExternalID,
		PostID:           item.PostID,
		ExternalPostID:   item.ExternalPostID,
		Body:             body,
		OccurredAt:       time.Now().UTC(),
	})
	return out
}

func igSendDM(ctx *sdk.AppCtx, out inboxOutcome, creds *igAccountCreds, item *inboxItem, body string) inboxOutcome {
	if item.AuthorExternalID == "" {
		out.Status, out.Error = "failed", "no IGSID for recipient — DM target requires author_external_id"
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "send_message", map[string]any{
		"instagramAccountId": creds.IGUserID,
		"recipient":          map[string]any{"id": item.AuthorExternalID},
		"message":            map[string]any{"text": body},
		"access_token":       creds.PageToken,
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	var resp struct {
		MessageID   string `json:"message_id"`
		RecipientID string `json:"recipient_id"`
	}
	_ = json.Unmarshal(res.Data, &resp)
	out.Status = "ok"
	out.ExternalID = resp.MessageID
	_ = markInboxRepliedByExternalID(ctx.AppDB(), creds.AccountID, inboxKindDM, item.ExternalID)
	return out
}

// instagramInboxPrivateReply uses send_message with
// recipient.comment_id to DM the author of a public comment. 7-day
// single-use window from Meta — the only way to initiate to a user
// who hasn't DM'd the account first.
func instagramInboxPrivateReply(ctx *sdk.AppCtx, item *inboxItem, body string) inboxOutcome {
	out := inboxOutcome{
		InboxItemID:     item.ID,
		SocialAccountID: item.SocialAccountID,
		Platform:        item.Platform,
	}
	if body == "" {
		out.Status, out.Error = "failed", "body required"
		return out
	}
	if item.Kind != inboxKindComment {
		out.Status, out.Error = "failed", "private_reply target must be a comment"
		return out
	}
	creds, err := loadIGAccountCreds(ctx.AppDB(), item.ProjectID, item.SocialAccountID)
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "send_message", map[string]any{
		"instagramAccountId": creds.IGUserID,
		"recipient":          map[string]any{"comment_id": item.ExternalID},
		"message":            map[string]any{"text": body},
		"access_token":       creds.PageToken,
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	var resp struct {
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal(res.Data, &resp)
	out.Status = "ok"
	out.ExternalID = resp.MessageID
	_ = markInboxRepliedByExternalID(ctx.AppDB(), creds.AccountID, inboxKindComment, item.ExternalID)
	return out
}

func instagramInboxHide(ctx *sdk.AppCtx, item *inboxItem, hide bool) inboxOutcome {
	out := inboxOutcome{
		InboxItemID:     item.ID,
		SocialAccountID: item.SocialAccountID,
		Platform:        item.Platform,
	}
	if item.Kind != inboxKindComment {
		out.Status, out.Error = "failed", "hide/unhide target must be a comment"
		return out
	}
	creds, err := loadIGAccountCreds(ctx.AppDB(), item.ProjectID, item.SocialAccountID)
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "hide_comment", map[string]any{
		"commentId":    item.ExternalID,
		"hide":         hide,
		"access_token": creds.PageToken,
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	out.Status = "ok"
	target := inboxStatusUnread
	if hide {
		target = inboxStatusHidden
	}
	if err := setInboxStatus(ctx.AppDB(), item.ProjectID, item.ID, target); err != nil {
		ctx.Logger().Warn("setInboxStatus after hide failed", "err", err)
	}
	return out
}

func instagramInboxDelete(ctx *sdk.AppCtx, item *inboxItem) inboxOutcome {
	out := inboxOutcome{
		InboxItemID:     item.ID,
		SocialAccountID: item.SocialAccountID,
		Platform:        item.Platform,
	}
	if item.Kind != inboxKindComment {
		out.Status, out.Error = "failed", "delete target must be a comment"
		return out
	}
	creds, err := loadIGAccountCreds(ctx.AppDB(), item.ProjectID, item.SocialAccountID)
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "delete_comment", map[string]any{
		"commentId":    item.ExternalID,
		"access_token": creds.PageToken,
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	out.Status = "ok"
	if err := setInboxStatus(ctx.AppDB(), item.ProjectID, item.ID, inboxStatusArchived); err != nil {
		ctx.Logger().Warn("setInboxStatus after delete failed", "err", err)
	}
	return out
}

// ─── helpers ───────────────────────────────────────────────────────

// parseIGTimestamp accepts the formats Meta returns across endpoints.
// IG uses RFC3339 with offset (`2026-05-19T14:30:00+0000`); FB
// sometimes returns Unix seconds as a string. Returns Now() on
// failure so callers don't have to special-case it.
func parseIGTimestamp(s string) time.Time {
	if s == "" {
		return time.Now().UTC()
	}
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05-0700",
		"2006-01-02T15:04:05+0000",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0).UTC()
	}
	return time.Now().UTC()
}

// marshalSafe returns "" on marshal failure so callers can store it
// in a nullable TEXT column without an error-handling tax. Inbox
// raw_json is for debug; losing one row's blob isn't worth aborting
// a poll over.
func marshalSafe(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
