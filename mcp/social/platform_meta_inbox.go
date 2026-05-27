package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type metaPaging struct {
	Cursors struct {
		After string `json:"after"`
	} `json:"cursors"`
}

type metaComment struct {
	ID          string `json:"id"`
	Message     string `json:"message"`
	Text        string `json:"text"`
	CreatedTime string `json:"created_time"`
	Timestamp   string `json:"timestamp"`
	Permalink   string `json:"permalink_url"`
	From        struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"from"`
	Username string `json:"username"`
	Replies  struct {
		Data []metaComment `json:"data"`
	} `json:"replies"`
}

type metaMessage struct {
	ID          string `json:"id"`
	Message     string `json:"message"`
	CreatedTime string `json:"created_time"`
	From        struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"from"`
	Attachments any `json:"attachments"`
}

type metaReview struct {
	ID                 string `json:"id"`
	CreatedTime        string `json:"created_time"`
	ReviewText         string `json:"review_text"`
	RecommendationType string `json:"recommendation_type"`
	Rating             int    `json:"rating"`
	Reviewer           struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"reviewer"`
	OpenGraphStory struct {
		ID        string `json:"id"`
		Permalink string `json:"permalink_url"`
	} `json:"open_graph_story"`
}

type metaTaggedItem struct {
	ID           string `json:"id"`
	Message      string `json:"message"`
	Story        string `json:"story"`
	CreatedTime  string `json:"created_time"`
	PermalinkURL string `json:"permalink_url"`
	FullPicture  string `json:"full_picture"`
	From         struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"from"`
	Attachments any `json:"attachments"`
}

func syncFacebookInbox(ctx *sdk.AppCtx, acct inboxAccount, opts inboxSyncOptions, out *inboxSyncResult) {
	if acct.ExtID == "" {
		out.Status, out.Error = "failed", "facebook page id missing"
		return
	}
	posts, err := fetchFacebookPagePostsForInbox(ctx, acct, opts.Limit)
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return
	}
	for _, p := range posts {
		n, warns := syncMetaPostComments(ctx, acct, "facebook", p.ID, p.LocalPostID, p.Permalink, opts.Limit)
		out.Comments += n
		out.Warnings = append(out.Warnings, warns...)
	}
	if n, warns := syncFacebookDMs(ctx, acct, opts.Limit); n >= 0 {
		out.DMs += n
		out.Warnings = append(out.Warnings, warns...)
	} else {
		out.Warnings = append(out.Warnings, warns...)
	}
	if n, warns := syncFacebookMentions(ctx, acct, opts.Limit); n >= 0 {
		out.Mentions += n
		out.Warnings = append(out.Warnings, warns...)
	} else {
		out.Warnings = append(out.Warnings, warns...)
	}
	if n, warns := syncFacebookReviews(ctx, acct, opts.Limit); n >= 0 {
		out.Reviews += n
		out.Warnings = append(out.Warnings, warns...)
	} else {
		out.Warnings = append(out.Warnings, warns...)
	}
}

type inboxPostRef struct {
	ID          string
	Permalink   string
	LocalPostID int64
}

func fetchFacebookPagePostsForInbox(ctx *sdk.AppCtx, acct inboxAccount, limit int) ([]inboxPostRef, error) {
	if limit <= 0 {
		limit = 50
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(acct.ConnID, "get_page_posts", map[string]any{
		"pageId":       acct.ExtID,
		"limit":        limit,
		"access_token": acct.PageToken,
		"fields":       "id,message,created_time,permalink_url",
	})
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		return nil, upstreamError(res)
	}
	var resp struct {
		Data []struct {
			ID           string `json:"id"`
			PermalinkURL string `json:"permalink_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &resp); err != nil {
		return nil, fmt.Errorf("decode facebook posts: %w", err)
	}
	out := make([]inboxPostRef, 0, len(resp.Data))
	for _, p := range resp.Data {
		if p.ID == "" {
			continue
		}
		out = append(out, inboxPostRef{ID: p.ID, Permalink: p.PermalinkURL, LocalPostID: findLocalPostID(ctx, acct.ID, p.ID)})
	}
	return out, nil
}

func syncFacebookDMs(ctx *sdk.AppCtx, acct inboxAccount, limit int) (int, []string) {
	args := map[string]any{
		"pageId":       acct.ExtID,
		"limit":        minInt(limit, 50),
		"fields":       "id,updated_time,participants{id,name,email},senders{id,name,email},snippet,unread_count,can_reply,link",
		"access_token": acct.PageToken,
	}
	inserted := 0
	for page := 0; page < 5; page++ {
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(acct.ConnID, "facebook_list_conversations", args)
		if err != nil {
			return -1, []string{"facebook dms: " + err.Error()}
		}
		if res == nil || !res.Success {
			return -1, []string{"facebook dms: " + upstreamError(res).Error()}
		}
		var resp struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Paging metaPaging `json:"paging"`
		}
		if err := json.Unmarshal(res.Data, &resp); err != nil {
			return -1, []string{"decode facebook conversations: " + err.Error()}
		}
		for _, conv := range resp.Data {
			if conv.ID == "" {
				continue
			}
			n, warn := syncMetaConversationMessages(ctx, acct, "facebook", conv.ID, limit)
			inserted += n
			if warn != "" {
				return inserted, []string{warn}
			}
		}
		if resp.Paging.Cursors.After == "" || len(resp.Data) == 0 {
			break
		}
		args["after"] = resp.Paging.Cursors.After
	}
	return inserted, nil
}

func syncFacebookMentions(ctx *sdk.AppCtx, acct inboxAccount, limit int) (int, []string) {
	args := map[string]any{
		"pageId":       acct.ExtID,
		"limit":        minInt(limit, 50),
		"fields":       "id,message,story,created_time,from,permalink_url,full_picture,attachments",
		"access_token": acct.PageToken,
	}
	inserted := 0
	for page := 0; page < 5; page++ {
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(acct.ConnID, "facebook_list_tagged", args)
		if err != nil {
			return -1, []string{"facebook mentions: " + err.Error()}
		}
		if res == nil || !res.Success {
			return -1, []string{"facebook mentions: " + upstreamError(res).Error()}
		}
		var resp struct {
			Data   []metaTaggedItem `json:"data"`
			Paging metaPaging       `json:"paging"`
		}
		if err := json.Unmarshal(res.Data, &resp); err != nil {
			return -1, []string{"decode facebook mentions: " + err.Error()}
		}
		for _, tag := range resp.Data {
			if tag.ID == "" {
				continue
			}
			if upsertFacebookMention(ctx, acct, tag) {
				inserted++
			}
		}
		if resp.Paging.Cursors.After == "" || len(resp.Data) == 0 {
			break
		}
		args["after"] = resp.Paging.Cursors.After
	}
	return inserted, nil
}

func syncFacebookReviews(ctx *sdk.AppCtx, acct inboxAccount, limit int) (int, []string) {
	args := map[string]any{
		"pageId":       acct.ExtID,
		"limit":        minInt(limit, 50),
		"fields":       "id,created_time,review_text,recommendation_type,rating,reviewer{id,name},open_graph_story{id,permalink_url}",
		"access_token": acct.PageToken,
	}
	inserted := 0
	for page := 0; page < 5; page++ {
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(acct.ConnID, "facebook_list_reviews", args)
		if err != nil {
			return -1, []string{"facebook reviews: " + err.Error()}
		}
		if res == nil || !res.Success {
			return -1, []string{"facebook reviews: " + upstreamError(res).Error()}
		}
		var resp struct {
			Data   []metaReview `json:"data"`
			Paging metaPaging   `json:"paging"`
		}
		if err := json.Unmarshal(res.Data, &resp); err != nil {
			return -1, []string{"decode facebook reviews: " + err.Error()}
		}
		for _, review := range resp.Data {
			if review.ID == "" {
				continue
			}
			if upsertFacebookReview(ctx, acct, review) {
				inserted++
			}
		}
		if resp.Paging.Cursors.After == "" || len(resp.Data) == 0 {
			break
		}
		args["after"] = resp.Paging.Cursors.After
	}
	return inserted, nil
}

func syncInstagramInbox(ctx *sdk.AppCtx, acct inboxAccount, opts inboxSyncOptions, out *inboxSyncResult) {
	if acct.ExtID == "" {
		out.Status, out.Error = "failed", "instagram account id missing"
		return
	}
	media, err := fetchInstagramMediaForInbox(ctx, acct, opts.Limit)
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return
	}
	for _, m := range media {
		n, warns := syncMetaPostComments(ctx, acct, "instagram", m.ID, m.LocalPostID, m.Permalink, opts.Limit)
		out.Comments += n
		out.Warnings = append(out.Warnings, warns...)
	}
	if n, warns := syncInstagramDMs(ctx, acct, opts.Limit); n >= 0 {
		out.DMs += n
		out.Warnings = append(out.Warnings, warns...)
	} else {
		out.Warnings = append(out.Warnings, warns...)
	}
	if n, warns := syncInstagramMentions(ctx, acct, opts.Limit); n >= 0 {
		out.Mentions += n
		out.Warnings = append(out.Warnings, warns...)
	} else {
		out.Warnings = append(out.Warnings, warns...)
	}
}

func fetchInstagramMediaForInbox(ctx *sdk.AppCtx, acct inboxAccount, limit int) ([]inboxPostRef, error) {
	if limit <= 0 {
		limit = 50
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(acct.ConnID, "get_account_media", map[string]any{
		"instagramAccountId": acct.ExtID,
		"limit":              limit,
		"fields":             "id,permalink,timestamp,caption,comments_count",
		"access_token":       acct.PageToken,
	})
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		return nil, upstreamError(res)
	}
	var resp struct {
		Data []struct {
			ID        string `json:"id"`
			Permalink string `json:"permalink"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &resp); err != nil {
		return nil, fmt.Errorf("decode instagram media: %w", err)
	}
	out := make([]inboxPostRef, 0, len(resp.Data))
	for _, m := range resp.Data {
		if m.ID == "" {
			continue
		}
		out = append(out, inboxPostRef{ID: m.ID, Permalink: m.Permalink, LocalPostID: findLocalPostID(ctx, acct.ID, m.ID)})
	}
	return out, nil
}

func syncMetaPostComments(ctx *sdk.AppCtx, acct inboxAccount, platform, externalPostID string, localPostID int64, permalink string, limit int) (int, []string) {
	if externalPostID == "" {
		return 0, nil
	}
	args := map[string]any{
		"mediaId":      externalPostID,
		"limit":        limit,
		"fields":       "id,message,text,from,username,created_time,timestamp,permalink_url,replies{id,message,text,from,username,created_time,timestamp,permalink_url}",
		"access_token": acct.PageToken,
	}
	inserted := 0
	for page := 0; page < 5; page++ {
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(acct.ConnID, "list_media_comments", args)
		if err != nil {
			return inserted, []string{fmt.Sprintf("comments %s: %v", externalPostID, err)}
		}
		if res == nil || !res.Success {
			return inserted, []string{fmt.Sprintf("comments %s: %v", externalPostID, upstreamError(res))}
		}
		var resp struct {
			Data   []metaComment `json:"data"`
			Paging metaPaging    `json:"paging"`
		}
		if err := json.Unmarshal(res.Data, &resp); err != nil {
			return inserted, []string{fmt.Sprintf("decode comments %s: %v", externalPostID, err)}
		}
		for _, c := range resp.Data {
			if upsertMetaComment(ctx, acct, platform, c, "", externalPostID, localPostID, permalink) {
				inserted++
			}
			for _, reply := range c.Replies.Data {
				if upsertMetaComment(ctx, acct, platform, reply, c.ID, externalPostID, localPostID, permalink) {
					inserted++
				}
			}
		}
		if resp.Paging.Cursors.After == "" || len(resp.Data) == 0 {
			break
		}
		args["after"] = resp.Paging.Cursors.After
	}
	return inserted, nil
}

func upsertMetaComment(ctx *sdk.AppCtx, acct inboxAccount, platform string, c metaComment, parentID, externalPostID string, localPostID int64, permalink string) bool {
	if c.ID == "" {
		return false
	}
	body := c.Message
	if body == "" {
		body = c.Text
	}
	occurred := time.Now().UTC()
	if t, err := parsePlatformTime(firstNonEmpty(c.CreatedTime, c.Timestamp)); err == nil {
		occurred = t
	}
	authorID := c.From.ID
	authorName := c.From.Name
	authorHandle := c.Username
	if authorName == "" {
		authorName = authorHandle
	}
	_, inserted, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:        acct.ProjectID,
		SocialAccountID:  acct.ID,
		Platform:         platform,
		Kind:             inboxKindComment,
		ExternalID:       c.ID,
		ThreadExternalID: firstNonEmpty(parentID, c.ID),
		ParentExternalID: parentID,
		PostID:           localPostID,
		ExternalPostID:   externalPostID,
		AuthorExternalID: authorID,
		AuthorName:       authorName,
		AuthorHandle:     authorHandle,
		Body:             body,
		Permalink:        firstNonEmpty(c.Permalink, permalink),
		OccurredAt:       occurred,
		RawJSON:          rawJSON(c),
		Direction:        directionForAuthor(acct, authorID, authorHandle),
	})
	if err != nil {
		ctx.Logger().Warn("inbox: upsert comment failed", "account", acct.ID, "comment", c.ID, "err", err)
		return false
	}
	if inserted {
		ctx.Emit("inbox.item.created", map[string]any{"social_account_id": acct.ID, "kind": inboxKindComment, "id": c.ID})
	}
	return inserted
}

func syncInstagramDMs(ctx *sdk.AppCtx, acct inboxAccount, limit int) (int, []string) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(acct.ConnID, "list_conversations", map[string]any{
		"instagramAccountId": acct.ExtID,
		"platform":           "instagram",
		"limit":              minInt(limit, 50),
		"access_token":       acct.PageToken,
	})
	if err != nil {
		return -1, []string{"instagram dms: " + err.Error()}
	}
	if res == nil || !res.Success {
		return -1, []string{"instagram dms: " + upstreamError(res).Error()}
	}
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &resp); err != nil {
		return -1, []string{"decode instagram conversations: " + err.Error()}
	}
	inserted := 0
	for _, conv := range resp.Data {
		if conv.ID == "" {
			continue
		}
		n, warn := syncInstagramConversation(ctx, acct, conv.ID, limit)
		inserted += n
		if warn != "" {
			return inserted, []string{warn}
		}
	}
	return inserted, nil
}

func syncInstagramConversation(ctx *sdk.AppCtx, acct inboxAccount, conversationID string, limit int) (int, string) {
	return syncMetaConversationMessages(ctx, acct, "instagram", conversationID, limit)
}

func syncMetaConversationMessages(ctx *sdk.AppCtx, acct inboxAccount, platform, conversationID string, limit int) (int, string) {
	args := map[string]any{
		"conversationId": conversationID,
		"limit":          minInt(limit, 50),
		"fields":         "id,from,to,message,created_time,attachments",
		"access_token":   acct.PageToken,
	}
	inserted := 0
	for page := 0; page < 5; page++ {
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(acct.ConnID, "list_conversation_messages", args)
		if err != nil {
			return inserted, fmt.Sprintf("%s conversation %s: %v", platform, conversationID, err)
		}
		if res == nil || !res.Success {
			return inserted, fmt.Sprintf("%s conversation %s: %v", platform, conversationID, upstreamError(res))
		}
		var resp struct {
			Data   []metaMessage `json:"data"`
			Paging metaPaging    `json:"paging"`
		}
		if err := json.Unmarshal(res.Data, &resp); err != nil {
			return inserted, fmt.Sprintf("decode %s conversation %s: %v", platform, conversationID, err)
		}
		for _, m := range resp.Data {
			if upsertMetaMessage(ctx, acct, platform, conversationID, m) {
				inserted++
			}
		}
		if resp.Paging.Cursors.After == "" || len(resp.Data) == 0 {
			break
		}
		args["after"] = resp.Paging.Cursors.After
	}
	return inserted, ""
}

func upsertMetaMessage(ctx *sdk.AppCtx, acct inboxAccount, platform, conversationID string, m metaMessage) bool {
	if m.ID == "" {
		return false
	}
	occurred := time.Now().UTC()
	if t, err := parsePlatformTime(m.CreatedTime); err == nil {
		occurred = t
	}
	media := ""
	if m.Attachments != nil {
		media = rawJSON(m.Attachments)
	}
	author := firstNonEmpty(m.From.Username, m.From.Name)
	_, didInsert, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:        acct.ProjectID,
		SocialAccountID:  acct.ID,
		Platform:         platform,
		Kind:             inboxKindDM,
		ExternalID:       m.ID,
		ThreadExternalID: conversationID,
		ParentExternalID: conversationID,
		AuthorExternalID: m.From.ID,
		AuthorName:       author,
		AuthorHandle:     m.From.Username,
		Body:             m.Message,
		MediaJSON:        media,
		OccurredAt:       occurred,
		RawJSON:          rawJSON(m),
		Direction:        directionForAuthor(acct, m.From.ID, m.From.Username),
	})
	if err != nil {
		ctx.Logger().Warn("inbox: upsert dm failed", "account", acct.ID, "message", m.ID, "err", err)
		return false
	}
	if didInsert {
		ctx.Emit("inbox.item.created", map[string]any{"social_account_id": acct.ID, "kind": inboxKindDM, "id": m.ID})
	}
	return didInsert
}

func syncInstagramMentions(ctx *sdk.AppCtx, acct inboxAccount, limit int) (int, []string) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(acct.ConnID, "list_my_tags", map[string]any{
		"instagramAccountId": acct.ExtID,
		"limit":              minInt(limit, 50),
		"fields":             "id,caption,media_type,media_url,permalink,username,timestamp",
		"access_token":       acct.PageToken,
	})
	if err != nil {
		return -1, []string{"instagram mentions: " + err.Error()}
	}
	if res == nil || !res.Success {
		return -1, []string{"instagram mentions: " + upstreamError(res).Error()}
	}
	var resp struct {
		Data []struct {
			ID        string `json:"id"`
			Caption   string `json:"caption"`
			MediaType string `json:"media_type"`
			MediaURL  string `json:"media_url"`
			Permalink string `json:"permalink"`
			Username  string `json:"username"`
			Timestamp string `json:"timestamp"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &resp); err != nil {
		return -1, []string{"decode instagram mentions: " + err.Error()}
	}
	inserted := 0
	for _, tag := range resp.Data {
		if tag.ID == "" {
			continue
		}
		occurred := time.Now().UTC()
		if t, err := parsePlatformTime(tag.Timestamp); err == nil {
			occurred = t
		}
		mediaJSON := ""
		if tag.MediaURL != "" {
			mediaJSON = rawJSON([]map[string]string{{"type": strings.ToLower(tag.MediaType), "url": tag.MediaURL}})
		}
		_, didInsert, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
			ProjectID:        acct.ProjectID,
			SocialAccountID:  acct.ID,
			Platform:         "instagram",
			Kind:             inboxKindMention,
			ExternalID:       tag.ID,
			ThreadExternalID: tag.ID,
			ExternalPostID:   tag.ID,
			AuthorName:       tag.Username,
			AuthorHandle:     tag.Username,
			Body:             tag.Caption,
			MediaJSON:        mediaJSON,
			Permalink:        tag.Permalink,
			OccurredAt:       occurred,
			RawJSON:          rawJSON(tag),
			Direction:        "inbound",
		})
		if err != nil {
			ctx.Logger().Warn("inbox: upsert mention failed", "account", acct.ID, "tag", tag.ID, "err", err)
			continue
		}
		if didInsert {
			inserted++
			ctx.Emit("inbox.item.created", map[string]any{"social_account_id": acct.ID, "kind": inboxKindMention, "id": tag.ID})
		}
	}
	return inserted, nil
}

func upsertFacebookMention(ctx *sdk.AppCtx, acct inboxAccount, tag metaTaggedItem) bool {
	occurred := time.Now().UTC()
	if t, err := parsePlatformTime(tag.CreatedTime); err == nil {
		occurred = t
	}
	mediaJSON := ""
	if tag.FullPicture != "" {
		mediaJSON = rawJSON([]map[string]string{{"type": "image", "url": tag.FullPicture}})
	} else if tag.Attachments != nil {
		mediaJSON = rawJSON(tag.Attachments)
	}
	body := firstNonEmpty(tag.Message, tag.Story)
	_, didInsert, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:        acct.ProjectID,
		SocialAccountID:  acct.ID,
		Platform:         "facebook",
		Kind:             inboxKindMention,
		ExternalID:       tag.ID,
		ThreadExternalID: tag.ID,
		ExternalPostID:   tag.ID,
		AuthorExternalID: tag.From.ID,
		AuthorName:       tag.From.Name,
		Body:             body,
		MediaJSON:        mediaJSON,
		Permalink:        tag.PermalinkURL,
		OccurredAt:       occurred,
		RawJSON:          rawJSON(tag),
		Direction:        directionForAuthor(acct, tag.From.ID, tag.From.Name),
	})
	if err != nil {
		ctx.Logger().Warn("inbox: upsert facebook mention failed", "account", acct.ID, "tag", tag.ID, "err", err)
		return false
	}
	if didInsert {
		ctx.Emit("inbox.item.created", map[string]any{"social_account_id": acct.ID, "kind": inboxKindMention, "id": tag.ID})
	}
	return didInsert
}

func upsertFacebookReview(ctx *sdk.AppCtx, acct inboxAccount, review metaReview) bool {
	occurred := time.Now().UTC()
	if t, err := parsePlatformTime(review.CreatedTime); err == nil {
		occurred = t
	}
	body := firstNonEmpty(review.ReviewText, review.RecommendationType)
	_, didInsert, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:        acct.ProjectID,
		SocialAccountID:  acct.ID,
		Platform:         "facebook",
		Kind:             inboxKindReview,
		ExternalID:       review.ID,
		ThreadExternalID: review.ID,
		ExternalPostID:   firstNonEmpty(review.OpenGraphStory.ID, review.ID),
		AuthorExternalID: review.Reviewer.ID,
		AuthorName:       review.Reviewer.Name,
		Body:             body,
		Permalink:        review.OpenGraphStory.Permalink,
		Rating:           review.Rating,
		OccurredAt:       occurred,
		RawJSON:          rawJSON(review),
		Direction:        "inbound",
	})
	if err != nil {
		ctx.Logger().Warn("inbox: upsert facebook review failed", "account", acct.ID, "review", review.ID, "err", err)
		return false
	}
	if didInsert {
		ctx.Emit("inbox.item.created", map[string]any{"social_account_id": acct.ID, "kind": inboxKindReview, "id": review.ID})
	}
	return didInsert
}

func findLocalPostID(ctx *sdk.AppCtx, accountID int64, platformPostID string) int64 {
	var id int64
	_ = ctx.AppDB().QueryRow(
		`SELECT post_id FROM post_targets WHERE social_account_id=? AND platform_post_id=?`,
		accountID, platformPostID,
	).Scan(&id)
	return id
}

func directionForAuthor(acct inboxAccount, authorID, authorHandle string) string {
	if authorID != "" && authorID == acct.ExtID {
		return "outbound"
	}
	if authorHandle != "" && strings.EqualFold(authorHandle, acct.Name) {
		return "outbound"
	}
	return "inbound"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func minInt(a, b int) int {
	if a <= 0 || a > b {
		return b
	}
	return a
}
