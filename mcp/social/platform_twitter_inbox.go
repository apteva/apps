package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type twitterUserNode struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Username        string `json:"username"`
	ProfileImageURL string `json:"profile_image_url"`
}

type twitterTweetNode struct {
	ID               string           `json:"id"`
	Text             string           `json:"text"`
	AuthorID         string           `json:"author_id"`
	CreatedAt        string           `json:"created_at"`
	ConversationID   string           `json:"conversation_id"`
	ReferencedTweets []twitterRefNode `json:"referenced_tweets"`
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

func syncTwitterInbox(ctx *sdk.AppCtx, acct inboxAccount, opts inboxSyncOptions, res *inboxSyncResult) {
	userID, _, err := twitterAuthenticatedUser(ctx, acct.ConnID)
	if err != nil {
		res.Status = "failed"
		res.Error = err.Error()
		return
	}
	if userID == "" {
		res.Status = "failed"
		res.Error = "authenticated X user id missing"
		return
	}
	res.Mentions += syncTwitterMentions(ctx, acct, userID, opts, res)
	res.DMs += syncTwitterDMs(ctx, acct, userID, opts, res)
}

func syncTwitterMentions(ctx *sdk.AppCtx, acct inboxAccount, userID string, opts inboxSyncOptions, res *inboxSyncResult) int {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	if limit < 5 {
		limit = 5
	}
	out, err := ctx.PlatformAPI().ExecuteIntegrationTool(acct.ConnID, "get_user_mentions", map[string]any{
		"user_id":      userID,
		"max_results":  limit,
		"tweet.fields": "id,text,author_id,created_at,public_metrics,conversation_id,in_reply_to_user_id,referenced_tweets",
		"expansions":   "author_id",
		"user.fields":  "id,name,username,profile_image_url,verified",
	})
	if err != nil {
		res.Warnings = append(res.Warnings, "get_user_mentions: "+err.Error())
		return 0
	}
	if out == nil || !out.Success {
		res.Warnings = append(res.Warnings, "get_user_mentions: "+upstreamError(out).Error())
		return 0
	}
	var resp struct {
		Data     []twitterTweetNode `json:"data"`
		Includes struct {
			Users []twitterUserNode `json:"users"`
		} `json:"includes"`
	}
	if err := json.Unmarshal(out.Data, &resp); err != nil {
		res.Warnings = append(res.Warnings, "decode X mentions: "+err.Error())
		return 0
	}
	users := twitterUsersByID(resp.Includes.Users)
	added := 0
	for _, tw := range resp.Data {
		if tw.ID == "" || tw.AuthorID == userID {
			continue
		}
		author := users[tw.AuthorID]
		threadID := firstNonEmpty(tw.ConversationID, twitterReplyParentID(tw), tw.ID)
		_, inserted, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
			ProjectID:        acct.ProjectID,
			SocialAccountID:  acct.ID,
			Platform:         "twitter",
			Kind:             inboxKindMention,
			ExternalID:       tw.ID,
			ThreadExternalID: threadID,
			ParentExternalID: twitterReplyParentID(tw),
			ExternalPostID:   threadID,
			AuthorExternalID: tw.AuthorID,
			AuthorName:       twitterDisplayName(author),
			AuthorHandle:     author.Username,
			AuthorAvatarURL:  author.ProfileImageURL,
			Body:             tw.Text,
			Permalink:        "https://twitter.com/i/web/status/" + tw.ID,
			OccurredAt:       parseTwitterTimestamp(tw.CreatedAt),
			RawJSON:          marshalSafe(tw),
			Direction:        "inbound",
		})
		if err != nil {
			ctx.Logger().Warn("x mention upsert failed", "external_id", tw.ID, "err", err)
			continue
		}
		if inserted {
			added++
		}
	}
	return added
}

func syncTwitterDMs(ctx *sdk.AppCtx, acct inboxAccount, userID string, opts inboxSyncOptions, res *inboxSyncResult) int {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	out, err := ctx.PlatformAPI().ExecuteIntegrationTool(acct.ConnID, "get_dm_events", map[string]any{
		"max_results":     limit,
		"event_types":     "MessageCreate",
		"dm_event.fields": "id,text,event_type,created_at,dm_conversation_id,sender_id",
		"expansions":      "sender_id",
		"user.fields":     "id,name,username,profile_image_url,verified",
	})
	if err != nil {
		res.Warnings = append(res.Warnings, "get_dm_events: "+err.Error())
		return 0
	}
	if out == nil || !out.Success {
		res.Warnings = append(res.Warnings, "get_dm_events: "+upstreamError(out).Error())
		return 0
	}
	var resp struct {
		Data     []twitterDMEventNode `json:"data"`
		Includes struct {
			Users []twitterUserNode `json:"users"`
		} `json:"includes"`
	}
	if err := json.Unmarshal(out.Data, &resp); err != nil {
		res.Warnings = append(res.Warnings, "decode X DMs: "+err.Error())
		return 0
	}
	users := twitterUsersByID(resp.Includes.Users)
	added := 0
	for _, dm := range resp.Data {
		if dm.ID == "" || dm.SenderID == "" || dm.SenderID == userID {
			continue
		}
		author := users[dm.SenderID]
		_, inserted, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
			ProjectID:        acct.ProjectID,
			SocialAccountID:  acct.ID,
			Platform:         "twitter",
			Kind:             inboxKindDM,
			ExternalID:       dm.ID,
			ThreadExternalID: dm.DMConversationID,
			ExternalPostID:   dm.DMConversationID,
			AuthorExternalID: dm.SenderID,
			AuthorName:       twitterDisplayName(author),
			AuthorHandle:     author.Username,
			AuthorAvatarURL:  author.ProfileImageURL,
			Body:             dm.Text,
			OccurredAt:       parseTwitterTimestamp(dm.CreatedAt),
			RawJSON:          marshalSafe(dm),
			Direction:        "inbound",
		})
		if err != nil {
			ctx.Logger().Warn("x dm upsert failed", "external_id", dm.ID, "err", err)
			continue
		}
		if inserted {
			added++
		}
	}
	return added
}

func twitterReplyInboxItem(ctx *sdk.AppCtx, item *inboxItem, creds *inboxAccountCreds, out inboxOutcome, body string) inboxOutcome {
	switch item.Kind {
	case inboxKindMention, inboxKindComment:
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
		_ = markInboxRepliedByExternalID(ctx.AppDB(), item.SocialAccountID, item.Kind, item.ExternalID)
		insertOutboundInboxRow(ctx, item, id, body)
		return out
	case inboxKindDM:
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
		_ = markInboxRepliedByExternalID(ctx.AppDB(), item.SocialAccountID, inboxKindDM, item.ExternalID)
		insertOutboundInboxRow(ctx, item, out.ExternalID, body)
		return out
	default:
		out.Status = "unsupported"
		out.Reason = fmt.Sprintf("X reply is not supported for %s items", item.Kind)
		return out
	}
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
	for _, v := range []string{resp.Data.DMEventID, resp.Data.ID, resp.DMEventID, resp.ID} {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func marshalSafe(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
