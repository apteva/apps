package main

import (
	"encoding/json"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type inboxAccountCreds struct {
	AccountID int64
	Platform  string
	ConnID    int64
	ExtID     string
	Token     string
}

func loadInboxAccountCreds(ctx *sdk.AppCtx, accountID int64) (*inboxAccountCreds, error) {
	var c inboxAccountCreds
	var pageCreds string
	err := ctx.AppDB().QueryRow(
		`SELECT id, platform, connection_id, COALESCE(external_account_id,''), COALESCE(page_credentials,'')
		 FROM social_accounts WHERE id=?`,
		accountID,
	).Scan(&c.AccountID, &c.Platform, &c.ConnID, &c.ExtID, &pageCreds)
	if err != nil {
		return nil, err
	}
	c.Token = extractPageToken(pageCreds)
	return &c, nil
}

func performInboxAction(ctx *sdk.AppCtx, item *inboxItem, action, body string) inboxOutcome {
	out := inboxOutcome{
		InboxItemID:     item.ID,
		SocialAccountID: item.SocialAccountID,
		Platform:        item.Platform,
	}
	creds, err := loadInboxAccountCreds(ctx, item.SocialAccountID)
	if err != nil {
		out.Status = "failed"
		out.Error = err.Error()
		return out
	}
	if creds.Token == "" && (item.Platform == "facebook" || item.Platform == "instagram") {
		out.Status = "failed"
		out.Error = "page access_token missing — reconnect the account"
		return out
	}
	switch action {
	case "reply":
		return replyInboxItem(ctx, item, creds, out, body)
	case "private_reply":
		return privateReplyInboxItem(ctx, item, creds, out, body)
	case "hide":
		return hideInboxItem(ctx, item, creds, out, true)
	case "unhide":
		return hideInboxItem(ctx, item, creds, out, false)
	case "delete":
		return deleteInboxItem(ctx, item, creds, out)
	case "like":
		out.Status = "unsupported"
		out.Reason = "comment like is not exposed by the current Meta integration"
		return out
	default:
		out.Status = "unsupported"
		out.Reason = "unknown inbox action"
		return out
	}
}

func replyInboxItem(ctx *sdk.AppCtx, item *inboxItem, creds *inboxAccountCreds, out inboxOutcome, body string) inboxOutcome {
	if body == "" {
		out.Status = "failed"
		out.Error = "body required"
		return out
	}
	if item.Platform == "twitter" {
		return twitterReplyInboxItem(ctx, item, creds, out, body)
	}
	switch item.Kind {
	case inboxKindComment:
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "reply_to_comment", map[string]any{
			"commentId":    item.ExternalID,
			"message":      body,
			"access_token": creds.Token,
		})
		if err != nil {
			out.Status, out.Error = "failed", err.Error()
			return out
		}
		if res == nil || !res.Success {
			out.Status, out.Error = "failed", upstreamError(res).Error()
			return out
		}
		out.ExternalID = extractIDField(res.Data)
		_ = markInboxRepliedByExternalID(ctx.AppDB(), item.SocialAccountID, item.Kind, item.ExternalID)
		insertOutboundInboxRow(ctx, item, out.ExternalID, body)
		out.Status = "ok"
		return out
	case inboxKindDM:
		if item.Platform != "instagram" && item.Platform != "facebook" {
			out.Status = "unsupported"
			out.Reason = "DM replies are currently wired for Instagram and Facebook only"
			return out
		}
		if item.AuthorExternalID == "" {
			out.Status = "failed"
			out.Error = "inbox item has no recipient id"
			return out
		}
		tool := "send_message"
		args := map[string]any{
			"recipient":    map[string]any{"id": item.AuthorExternalID},
			"message":      map[string]any{"text": body},
			"access_token": creds.Token,
		}
		if item.Platform == "instagram" {
			args["instagramAccountId"] = creds.ExtID
		} else {
			tool = "facebook_send_message"
			args["pageId"] = creds.ExtID
			args["messaging_type"] = "RESPONSE"
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
		out.ExternalID = extractMessageIDField(res.Data)
		_ = markInboxRepliedByExternalID(ctx.AppDB(), item.SocialAccountID, item.Kind, item.ExternalID)
		insertOutboundInboxRow(ctx, item, out.ExternalID, body)
		out.Status = "ok"
		return out
	default:
		out.Status = "unsupported"
		out.Reason = fmt.Sprintf("reply is not supported for %s items", item.Kind)
		return out
	}
}

func privateReplyInboxItem(ctx *sdk.AppCtx, item *inboxItem, creds *inboxAccountCreds, out inboxOutcome, body string) inboxOutcome {
	if (item.Platform != "instagram" && item.Platform != "facebook") || item.Kind != inboxKindComment {
		out.Status = "unsupported"
		out.Reason = "private replies are Facebook/Instagram comment-only"
		return out
	}
	if body == "" {
		out.Status = "failed"
		out.Error = "body required"
		return out
	}
	tool := "send_message"
	args := map[string]any{
		"message":      map[string]any{"text": body},
		"access_token": creds.Token,
	}
	if item.Platform == "instagram" {
		args["instagramAccountId"] = creds.ExtID
		args["recipient"] = map[string]any{"comment_id": item.ExternalID}
	} else {
		tool = "facebook_private_reply_to_comment"
		args["commentId"] = item.ExternalID
		args["message"] = body
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
	out.ExternalID = extractMessageIDField(res.Data)
	_ = markInboxRepliedByExternalID(ctx.AppDB(), item.SocialAccountID, item.Kind, item.ExternalID)
	out.Status = "ok"
	return out
}

func hideInboxItem(ctx *sdk.AppCtx, item *inboxItem, creds *inboxAccountCreds, out inboxOutcome, hide bool) inboxOutcome {
	if item.Kind != inboxKindComment {
		out.Status = "unsupported"
		out.Reason = "hide only applies to comments"
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "hide_comment", map[string]any{
		"commentId":    item.ExternalID,
		"hide":         hide,
		"access_token": creds.Token,
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	status := inboxStatusUnread
	if hide {
		status = inboxStatusHidden
	}
	_ = setInboxStatus(ctx.AppDB(), item.ProjectID, item.ID, status)
	out.Status = "ok"
	return out
}

func deleteInboxItem(ctx *sdk.AppCtx, item *inboxItem, creds *inboxAccountCreds, out inboxOutcome) inboxOutcome {
	if item.Kind != inboxKindComment {
		out.Status = "unsupported"
		out.Reason = "delete only applies to comments"
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(creds.ConnID, "delete_comment", map[string]any{
		"commentId":    item.ExternalID,
		"access_token": creds.Token,
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	_ = setInboxStatus(ctx.AppDB(), item.ProjectID, item.ID, inboxStatusArchived)
	out.Status = "ok"
	return out
}

func insertOutboundInboxRow(ctx *sdk.AppCtx, parent *inboxItem, externalID, body string) {
	if externalID == "" {
		externalID = parent.ExternalID + ":local-reply"
	}
	_, _, _ = upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:        parent.ProjectID,
		SocialAccountID:  parent.SocialAccountID,
		Platform:         parent.Platform,
		Kind:             parent.Kind,
		ExternalID:       externalID,
		ThreadExternalID: firstNonEmpty(parent.ThreadExternalID, parent.ParentExternalID, parent.ExternalID),
		ParentExternalID: parent.ExternalID,
		Body:             body,
		OccurredAt:       nowUTC(),
		Direction:        "outbound",
	})
}

func extractIDField(raw json.RawMessage) string {
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	return toString(obj["id"])
}

func extractMessageIDField(raw json.RawMessage) string {
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	if id := toString(obj["message_id"]); id != "" {
		return id
	}
	return toString(obj["id"])
}

func nowUTC() time.Time {
	return time.Now().UTC()
}
