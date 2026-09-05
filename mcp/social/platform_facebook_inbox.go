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

type facebookAccountCreds struct {
	AccountID int64
	ConnID    int64
	PageID    string
	PageToken string
}

type facebookSyncReport struct {
	NewComments int      `json:"new_comments"`
	NewDMs      int      `json:"new_dms"`
	NewMentions int      `json:"new_mentions"`
	NewReviews  int      `json:"new_reviews"`
	Warnings    []string `json:"warnings,omitempty"`
}

func loadFacebookAccountCreds(db *sql.DB, projectID string, accountID int64) (*facebookAccountCreds, error) {
	var (
		connID    int64
		pageID    string
		pageCreds string
		platform  string
	)
	err := db.QueryRow(
		`SELECT connection_id, COALESCE(external_account_id,''),
		        COALESCE(page_credentials,''), platform
		   FROM social_accounts
		  WHERE id=? AND project_id=? AND status='active'`,
		accountID, projectID,
	).Scan(&connID, &pageID, &pageCreds, &platform)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("social_account %d not found or inactive", accountID)
	}
	if err != nil {
		return nil, err
	}
	if platform != "facebook" {
		return nil, fmt.Errorf("social_account %d is not a facebook account (got %q)", accountID, platform)
	}
	token := extractPageToken(pageCreds)
	if token == "" {
		return nil, errors.New("facebook page access_token missing — reconnect the account to grant inbox scopes")
	}
	if pageID == "" {
		return nil, errors.New("facebook page id missing on social_accounts row")
	}
	return &facebookAccountCreds{AccountID: accountID, ConnID: connID, PageID: pageID, PageToken: token}, nil
}

func syncFacebookAccount(ctx *sdk.AppCtx, projectID string, accountID int64) (*facebookSyncReport, error) {
	done := beginInboxSync(ctx, accountID)
	defer done()
	creds, err := loadFacebookAccountCreds(ctx.AppDB(), projectID, accountID)
	if err != nil {
		return nil, err
	}
	report := &facebookSyncReport{}
	if n, warns := syncFacebookComments(ctx, projectID, creds); n >= 0 {
		report.NewComments = n
		report.Warnings = append(report.Warnings, warns...)
	} else {
		report.Warnings = append(report.Warnings, warns...)
	}
	if n, warns := syncFacebookDMs(ctx, projectID, creds); n >= 0 {
		report.NewDMs = n
		report.Warnings = append(report.Warnings, warns...)
	} else {
		report.Warnings = append(report.Warnings, warns...)
	}
	if n, warns := syncFacebookMentions(ctx, projectID, creds); n >= 0 {
		report.NewMentions = n
		report.Warnings = append(report.Warnings, warns...)
	} else {
		report.Warnings = append(report.Warnings, warns...)
	}
	if n, warns := syncFacebookReviews(ctx, projectID, creds); n >= 0 {
		report.NewReviews = n
		report.Warnings = append(report.Warnings, warns...)
	} else {
		report.Warnings = append(report.Warnings, warns...)
	}
	_, _ = ctx.AppDB().Exec(`INSERT INTO inbox_cursors(social_account_id,kind,cursor,last_sync_at,last_error) VALUES (?,'all','',CURRENT_TIMESTAMP,?) ON CONFLICT(social_account_id,kind) DO UPDATE SET last_sync_at=excluded.last_sync_at,last_error=excluded.last_error`, accountID, nullable(strings.Join(report.Warnings, "; ")))

	return report, nil
}

type fbUserNode struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Picture struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	} `json:"picture"`
}

type fbCommentNode struct {
	ID          string `json:"id"`
	Message     string `json:"message"`
	Text        string `json:"text"`
	CreatedTime string `json:"created_time"`
	Timestamp   string `json:"timestamp"`
	LikeCount   int    `json:"like_count"`
	Parent      *struct {
		ID string `json:"id"`
	} `json:"parent"`
	ParentID string     `json:"parent_id"`
	From     fbUserNode `json:"from"`
	Replies  struct {
		Data []fbCommentNode `json:"data"`
	} `json:"replies"`
}

func syncFacebookComments(ctx *sdk.AppCtx, projectID string, creds *facebookAccountCreds) (int, []string) {
	rows, err := ctx.AppDB().Query(
		`SELECT pt.id, pt.post_id, COALESCE(pt.platform_post_id,'')
		   FROM post_targets pt
		   JOIN posts p ON p.id = pt.post_id
		  WHERE pt.social_account_id=?
		    AND pt.status='published'
		    AND COALESCE(pt.platform_post_id,'') != ''
		    AND p.project_id=?
		    AND p.published_at IS NOT NULL
		    AND datetime(p.published_at) >= datetime('now', '-90 days')
		  ORDER BY pt.published_at DESC
		  LIMIT 100`,
		creds.AccountID, projectID,
	)
	if err != nil {
		return -1, []string{"list facebook post_targets for comments: " + err.Error()}
	}
	defer rows.Close()

	type target struct {
		postID int64
		fbID   string
	}
	var targets []target
	for rows.Next() {
		var targetID int64
		var t target
		if err := rows.Scan(&targetID, &t.postID, &t.fbID); err == nil {
			targets = append(targets, t)
		}
	}

	added := 0
	var warns []string
	for _, t := range targets {
		n, w := fetchAndUpsertFBComments(ctx, projectID, creds, t.postID, t.fbID)
		added += n
		if w != "" {
			warns = append(warns, w)
		}
	}
	return added, warns
}

func fetchAndUpsertFBComments(ctx *sdk.AppCtx, projectID string, creds *facebookAccountCreds, postID int64, fbPostID string) (int, string) {
	res, err := collectInboxPages(ctx, creds.AccountID, creds.ConnID, "list_media_comments", map[string]any{
		"mediaId":      fbPostID,
		"fields":       "id,message,from{id,name,picture},created_time,like_count,parent,replies{id,message,from{id,name,picture},created_time,like_count}",
		"limit":        50,
		"access_token": creds.PageToken,
	})
	if err != nil {
		return 0, fmt.Sprintf("list_media_comments(%s): %v", fbPostID, err)
	}
	if res == nil || !res.Success {
		return 0, fmt.Sprintf("list_media_comments(%s): %v", fbPostID, upstreamError(res))
	}
	var resp struct {
		Data []fbCommentNode `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &resp); err != nil {
		return 0, fmt.Sprintf("decode facebook comments(%s): %v", fbPostID, err)
	}
	added := 0
	for _, c := range resp.Data {
		if upsertFBComment(ctx, projectID, creds, postID, fbPostID, "", c) {
			added++
		}
		for _, r := range c.Replies.Data {
			if upsertFBComment(ctx, projectID, creds, postID, fbPostID, c.ID, r) {
				added++
			}
		}
	}
	return added, finishInboxPages(ctx, creds.AccountID, res)
}

func upsertFBComment(ctx *sdk.AppCtx, projectID string, creds *facebookAccountCreds, postID int64, fbPostID, parentID string, c fbCommentNode) bool {
	if c.ID == "" {
		return false
	}
	if parentID == "" {
		parentID = c.ParentID
		if parentID == "" && c.Parent != nil {
			parentID = c.Parent.ID
		}
	}
	_, inserted, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:        projectID,
		SocialAccountID:  creds.AccountID,
		Platform:         "facebook",
		Kind:             inboxKindComment,
		ExternalID:       c.ID,
		ParentExternalID: parentID,
		PostID:           postID,
		ExternalPostID:   fbPostID,
		AuthorExternalID: c.From.ID,
		AuthorName:       c.From.Name,
		AuthorAvatarURL:  c.From.Picture.Data.URL,
		Body:             firstNonEmpty(c.Message, c.Text),
		OccurredAt:       parseIGTimestamp(firstNonEmpty(c.CreatedTime, c.Timestamp)),
		RawJSON:          marshalSafe(c),
	})
	if err != nil {
		ctx.Logger().Warn("facebook comment upsert failed", "external_id", c.ID, "err", err)
		return false
	}
	return inserted
}

type fbConversationNode struct {
	ID           string `json:"id"`
	UpdatedTime  string `json:"updated_time"`
	Snippet      string `json:"snippet"`
	UnreadCount  int    `json:"unread_count"`
	Participants struct {
		Data []fbUserNode `json:"data"`
	} `json:"participants"`
	Senders struct {
		Data []fbUserNode `json:"data"`
	} `json:"senders"`
	Messages struct {
		Data []fbMessageNode `json:"data"`
	} `json:"messages"`
}

type fbMessageNode struct {
	ID          string          `json:"id"`
	Message     string          `json:"message"`
	CreatedTime string          `json:"created_time"`
	From        fbUserNode      `json:"from"`
	To          json.RawMessage `json:"to,omitempty"`
	Attachments json.RawMessage `json:"attachments,omitempty"`
}

func syncFacebookDMs(ctx *sdk.AppCtx, projectID string, creds *facebookAccountCreds) (int, []string) {
	res, err := collectInboxPages(ctx, creds.AccountID, creds.ConnID, "facebook_list_conversations", map[string]any{
		"pageId":       creds.PageID,
		"limit":        50,
		"access_token": creds.PageToken,
	})
	if err != nil {
		return -1, []string{"facebook_list_conversations: " + err.Error()}
	}
	if res == nil || !res.Success {
		return -1, []string{"facebook_list_conversations: " + upstreamError(res).Error()}
	}
	var resp struct {
		Data []fbConversationNode `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &resp); err != nil {
		return -1, []string{"decode facebook conversations: " + err.Error()}
	}
	added := 0
	var warns []string
	for _, conv := range resp.Data {
		n, w := expandAndUpsertFBConversation(ctx, projectID, creds, conv.ID)
		added += n
		if w != "" {
			warns = append(warns, w)
		}
	}
	if warning := finishInboxPages(ctx, creds.AccountID, res); warning != "" {
		warns = append(warns, warning)
	}
	return added, warns
}

func expandAndUpsertFBConversation(ctx *sdk.AppCtx, projectID string, creds *facebookAccountCreds, conversationID string) (int, string) {
	res, err := collectInboxPages(ctx, creds.AccountID, creds.ConnID, "facebook_get_conversation", map[string]any{
		"conversationId": conversationID,
		"access_token":   creds.PageToken,
	})
	if err != nil {
		return 0, fmt.Sprintf("facebook_get_conversation(%s): %v", conversationID, err)
	}
	if res == nil || !res.Success {
		return 0, fmt.Sprintf("facebook_get_conversation(%s): %v", conversationID, upstreamError(res))
	}
	var conv fbConversationNode
	if err := json.Unmarshal(res.Data, &conv); err != nil {
		return 0, fmt.Sprintf("decode facebook conversation(%s): %v", conversationID, err)
	}
	msgs := append([]fbMessageNode(nil), conv.Messages.Data...)
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	added := 0
	var prevID string
	for _, m := range msgs {
		if m.ID == "" {
			continue
		}

		mediaJSON := normalizeInboxMediaJSON(m.Attachments)
		_, inserted, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
			ProjectID:        projectID,
			SocialAccountID:  creds.AccountID,
			Platform:         "facebook",
			Kind:             inboxKindDM,
			ExternalID:       m.ID,
			Outbound:         m.From.ID == creds.PageID,
			ParentExternalID: prevID,
			ExternalPostID:   conversationID,
			AuthorExternalID: m.From.ID,
			AuthorName:       m.From.Name,
			AuthorAvatarURL:  m.From.Picture.Data.URL,
			Body:             m.Message,
			MediaJSON:        mediaJSON,
			OccurredAt:       parseIGTimestamp(m.CreatedTime),
			RawJSON:          marshalSafe(m),
		})
		if err != nil {
			ctx.Logger().Warn("facebook dm upsert failed", "external_id", m.ID, "err", err)
		} else if inserted {
			added++
		}
		prevID = m.ID
	}
	return added, finishInboxPages(ctx, creds.AccountID, res)
}

type fbTaggedNode struct {
	ID          string          `json:"id"`
	Message     string          `json:"message"`
	Story       string          `json:"story"`
	CreatedTime string          `json:"created_time"`
	From        fbUserNode      `json:"from"`
	Permalink   string          `json:"permalink_url"`
	FullPicture string          `json:"full_picture"`
	Attachments json.RawMessage `json:"attachments,omitempty"`
}

func syncFacebookMentions(ctx *sdk.AppCtx, projectID string, creds *facebookAccountCreds) (int, []string) {
	res, err := collectInboxPages(ctx, creds.AccountID, creds.ConnID, "facebook_list_tagged", map[string]any{
		"pageId":       creds.PageID,
		"limit":        50,
		"access_token": creds.PageToken,
	})
	if err != nil {
		return -1, []string{"facebook_list_tagged: " + err.Error()}
	}
	if res == nil || !res.Success {
		return -1, []string{"facebook_list_tagged: " + upstreamError(res).Error()}
	}
	var resp struct {
		Data []fbTaggedNode `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &resp); err != nil {
		return -1, []string{"decode facebook tagged: " + err.Error()}
	}
	added := 0
	for _, t := range resp.Data {
		if t.ID == "" {
			continue
		}
		mediaJSON := normalizeInboxMediaJSON(t.Attachments)
		_, inserted, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
			ProjectID:        projectID,
			SocialAccountID:  creds.AccountID,
			Platform:         "facebook",
			Kind:             inboxKindMention,
			ExternalID:       t.ID,
			ExternalPostID:   t.ID,
			AuthorExternalID: t.From.ID,
			AuthorName:       t.From.Name,
			Body:             firstNonEmpty(t.Message, t.Story),
			MediaJSON:        mediaJSON,
			Permalink:        t.Permalink,
			OccurredAt:       parseIGTimestamp(t.CreatedTime),
			RawJSON:          marshalSafe(t),
		})
		if err != nil {
			ctx.Logger().Warn("facebook mention upsert failed", "external_id", t.ID, "err", err)
			continue
		}
		if inserted {
			added++
		}
	}
	if warning := finishInboxPages(ctx, creds.AccountID, res); warning != "" {
		return added, []string{warning}
	}
	return added, nil
}

type fbReviewNode struct {
	ID                 string     `json:"id"`
	CreatedTime        string     `json:"created_time"`
	ReviewText         string     `json:"review_text"`
	RecommendationType string     `json:"recommendation_type"`
	Rating             int        `json:"rating"`
	Reviewer           fbUserNode `json:"reviewer"`
}

func syncFacebookReviews(ctx *sdk.AppCtx, projectID string, creds *facebookAccountCreds) (int, []string) {
	res, err := collectInboxPages(ctx, creds.AccountID, creds.ConnID, "facebook_list_reviews", map[string]any{
		"pageId":       creds.PageID,
		"limit":        50,
		"access_token": creds.PageToken,
	})
	if err != nil {
		return -1, []string{"facebook_list_reviews: " + err.Error()}
	}
	if res == nil || !res.Success {
		return -1, []string{"facebook_list_reviews: " + upstreamError(res).Error()}
	}
	var resp struct {
		Data []fbReviewNode `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &resp); err != nil {
		return -1, []string{"decode facebook reviews: " + err.Error()}
	}
	added := 0
	for _, r := range resp.Data {
		if r.ID == "" {
			continue
		}
		_, inserted, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
			ProjectID:        projectID,
			SocialAccountID:  creds.AccountID,
			Platform:         "facebook",
			Kind:             inboxKindReview,
			ExternalID:       r.ID,
			AuthorExternalID: r.Reviewer.ID,
			AuthorName:       r.Reviewer.Name,
			AuthorAvatarURL:  r.Reviewer.Picture.Data.URL,
			Body:             firstNonEmpty(r.ReviewText, r.RecommendationType),
			Rating:           r.Rating,
			OccurredAt:       parseIGTimestamp(r.CreatedTime),
			RawJSON:          marshalSafe(r),
		})
		if err != nil {
			ctx.Logger().Warn("facebook review upsert failed", "external_id", r.ID, "err", err)
			continue
		}
		if inserted {
			added++
		}
	}
	if warning := finishInboxPages(ctx, creds.AccountID, res); warning != "" {
		return added, []string{warning}
	}
	return added, nil
}

func facebookInboxReply(ctx *sdk.AppCtx, item *inboxItem, message inboxMessage, mode string) inboxOutcome {
	out := inboxOutcome{InboxItemID: item.ID, SocialAccountID: item.SocialAccountID, Platform: item.Platform}
	creds, err := loadFacebookAccountCreds(ctx.AppDB(), item.ProjectID, item.SocialAccountID)
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	switch item.Kind {
	case inboxKindComment:
		if mode == inboxReplyModePrivate {
			return fbPrivateReplyToComment(ctx, out, creds, item, message.Body)
		}
		return fbReplyToComment(ctx, out, creds, item, message.Body)
	case inboxKindDM:
		if mode == inboxReplyModePrivate {
			out.Status = "unsupported"
			out.Reason = "facebook DM replies are already private; use mode=public or mode=auto"
			return out
		}
		return fbSendDM(ctx, out, creds, item, message)
	default:
		out.Status = "unsupported"
		out.Reason = fmt.Sprintf("facebook inbox_reply: kind %q has no reply path", item.Kind)
		return out
	}
}

func fbReplyToComment(ctx *sdk.AppCtx, out inboxOutcome, creds *facebookAccountCreds, item *inboxItem, body string) inboxOutcome {
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
	_ = markInboxRepliedByExternalID(ctx.AppDB(), creds.AccountID, inboxKindComment, item.ExternalID)
	if resp.ID != "" {
		_, _, _ = upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
			Outbound:         true,
			ProjectID:        item.ProjectID,
			SocialAccountID:  creds.AccountID,
			Platform:         "facebook",
			Kind:             inboxKindComment,
			ExternalID:       resp.ID,
			ParentExternalID: item.ExternalID,
			PostID:           item.PostID,
			ExternalPostID:   item.ExternalPostID,
			Body:             body,
			OccurredAt:       time.Now().UTC(),
		})
	}
	return out
}

func fbPrivateReplyToComment(ctx *sdk.AppCtx, out inboxOutcome, creds *facebookAccountCreds, item *inboxItem, body string) inboxOutcome {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "facebook_private_reply_to_comment", map[string]any{
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
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal(res.Data, &resp)
	out.Status = "ok"
	out.ExternalID = resp.MessageID
	_ = markInboxRepliedByExternalID(ctx.AppDB(), creds.AccountID, inboxKindComment, item.ExternalID)
	return out
}

func fbSendDM(ctx *sdk.AppCtx, out inboxOutcome, creds *facebookAccountCreds, item *inboxItem, message inboxMessage) inboxOutcome {
	if item.AuthorExternalID == "" {
		out.Status, out.Error = "failed", "facebook DM target requires author_external_id (PSID)"
		return out
	}
	deliveries := make([]inboxDelivery, 0, len(message.Attachments)+1)
	parentID := item.ExternalID
	for _, part := range inboxMessageParts(message) {
		payload := map[string]any{}
		if part.Attachment != nil {
			payload["attachment"] = map[string]any{
				"type": part.Attachment.Kind,
				"payload": map[string]any{
					"url":         part.Attachment.URL,
					"is_reusable": false,
				},
			}
		} else {
			payload["text"] = part.Body
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "facebook_send_message", map[string]any{
			"pageId":         creds.PageID,
			"recipient":      map[string]any{"id": item.AuthorExternalID},
			"message":        payload,
			"messaging_type": "RESPONSE",
			"access_token":   creds.PageToken,
		})
		delivery := inboxDelivery{Kind: part.Kind}
		if err != nil {
			delivery.Status, delivery.Error = "failed", err.Error()
			deliveries = append(deliveries, delivery)
			continue
		}
		if res == nil || !res.Success {
			delivery.Status, delivery.Error = "failed", upstreamError(res).Error()
			deliveries = append(deliveries, delivery)
			continue
		}
		var resp struct {
			MessageID string `json:"message_id"`
		}
		_ = json.Unmarshal(res.Data, &resp)
		delivery.Status, delivery.ExternalID = "ok", resp.MessageID
		deliveries = append(deliveries, delivery)
		mediaJSON := ""
		if part.Attachment != nil {
			mediaJSON = marshalInboxAttachments([]inboxAttachment{*part.Attachment})
		}
		_, _, _ = upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
			Outbound:         true,
			ProjectID:        item.ProjectID,
			SocialAccountID:  creds.AccountID,
			Platform:         "facebook",
			Kind:             inboxKindDM,
			ExternalID:       resp.MessageID,
			ParentExternalID: parentID,
			ExternalPostID:   item.ExternalPostID,
			AuthorExternalID: creds.PageID,
			Body:             part.Body,
			MediaJSON:        mediaJSON,
			OccurredAt:       time.Now().UTC(),
		})
		if resp.MessageID != "" {
			parentID = resp.MessageID
		}
	}
	out = outcomeFromInboxDeliveries(out, deliveries)
	if out.Status == "ok" || out.Status == "partial" {
		_ = markInboxRepliedByExternalID(ctx.AppDB(), creds.AccountID, inboxKindDM, item.ExternalID)
	}
	return out
}

func facebookInboxHide(ctx *sdk.AppCtx, item *inboxItem, hide bool) inboxOutcome {
	out := inboxOutcome{InboxItemID: item.ID, SocialAccountID: item.SocialAccountID, Platform: item.Platform}
	if item.Kind != inboxKindComment {
		out.Status, out.Error = "failed", "hide/unhide target must be a comment"
		return out
	}
	creds, err := loadFacebookAccountCreds(ctx.AppDB(), item.ProjectID, item.SocialAccountID)
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
	_ = setInboxStatus(ctx.AppDB(), item.ProjectID, item.ID, target)
	return out
}

func facebookInboxDelete(ctx *sdk.AppCtx, item *inboxItem) inboxOutcome {
	out := inboxOutcome{InboxItemID: item.ID, SocialAccountID: item.SocialAccountID, Platform: item.Platform}
	if item.Kind != inboxKindComment {
		out.Status, out.Error = "failed", "delete target must be a comment"
		return out
	}
	creds, err := loadFacebookAccountCreds(ctx.AppDB(), item.ProjectID, item.SocialAccountID)
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
	_ = setInboxStatus(ctx.AppDB(), item.ProjectID, item.ID, inboxStatusArchived)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
