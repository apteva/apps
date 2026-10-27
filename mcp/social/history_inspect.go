package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) inspectNativeHistory(ctx *sdk.AppCtx, b historyAccount, run *historyRun, id string) ([]nativePost, error) {
	if b.Provider == zernioProviderSlug {
		var providerID string
		err := ctx.AppDB().QueryRow(`SELECT json_extract(metadata,'$.provider_post_id') FROM social_native_posts WHERE project_id=? AND social_account_id=? AND native_post_id=?`, b.Project, b.ID, id).Scan(&providerID)
		if err != nil || providerID == "" {
			return nil, errors.New("sync this account first; no account-bound provider post identity is stored")
		}
		raw, err := historyExecute(ctx, b, "get_post", map[string]any{"postId": providerID})
		if err != nil {
			return nil, err
		}
		item := raw
		for _, key := range []string{"post", "data"} {
			if m, ok := item[key].(map[string]any); ok {
				item = m
			}
		}
		part := zernioPlatformPost(item, b.ProviderID, b.Platform)
		actualAccount := firstString(part, "accountId._id", "accountId.id", "account_id", "accountId")
		actualID := firstString(part, "platformPostId", "platform_post_id", "externalId", "external_id")
		if actualAccount != b.ProviderID || actualID != id {
			return nil, errors.New("provider post/account binding mismatch")
		}
		run.Scope = "provider_indexed_metadata"
		addHistoryLimitation(run, "Provider-indexed metadata is not an independent live native/player/access verification.")
		return []nativePost{{ID: id, ProviderPostID: &providerID, Title: optionalText(firstString(item, "title")), Body: optionalText(firstString(item, "content", "text", "caption", "body")), URL: optionalText(firstString(part, "platformPostUrl", "platformUrl", "permalink", "url")), PublishedAt: nativeTimestamp(firstString(part, "publishedAt", "published_at")), Visibility: optionalText(firstString(part, "visibility", "privacyStatus")), ProcessingState: optionalText(firstString(part, "status"))}}, nil
	}
	if b.Provider != "native" && b.Provider != "" {
		return nil, errors.New("inspection adapter unsupported")
	}
	switch b.Platform {
	case "youtube":
		if err := a.youtubeHistoryIdentity(ctx, b, run); err != nil {
			return nil, err
		}
		return a.youtubeNativePosts(ctx, b, run, []string{id})
	case "pinterest":
		raw, err := historyExecute(ctx, b, "get_pin", map[string]any{"pin_id": id})
		if err != nil {
			return nil, err
		}
		if historyString(raw, "id") != id {
			return nil, errors.New("Pinterest returned a different native ID")
		}
		if b.ExternalID != "" && historyString(raw, "board_id") != b.ExternalID {
			return nil, errors.New("Pin is not on this legacy board account")
		}
		profile, err := historyExecute(ctx, b, "get_user_account", map[string]any{})
		if err != nil {
			return nil, err
		}
		owner := historyString(profile, "username")
		if owner == "" || historyString(historyMap(raw, "board_owner"), "username") != owner {
			return nil, errors.New("Pinterest Pin ownership was not verified for this account")
		}
		if err := verifyHistoryOwner(ctx, b, run, owner); err != nil {
			return nil, err
		}

		return []nativePost{{ID: id, URL: optionalText(historyString(raw, "permalink_url", "share_url")), Title: optionalText(historyString(raw, "title")), Body: optionalText(historyString(raw, "description")), PublishedAt: nativeTimestamp(historyString(raw, "created_at"))}}, nil
	case "facebook":
		if b.ExternalID == "" {
			return nil, errors.New("Facebook native post ID does not match the selected Page")
		}
		token := extractPageToken(b.PageCredentials)
		if token == "" {
			return nil, errors.New("Page access token unavailable")
		}
		raw, err := historyExecute(ctx, b, "facebook_get_post", map[string]any{"postId": id, "fields": "id,from,message,created_time,permalink_url,is_published", "access_token": token})
		if err != nil {
			return nil, err
		}
		if historyString(raw, "id") != id {
			return nil, errors.New("Facebook native ID mismatch")
		}
		if owner := historyString(historyMap(raw, "from"), "id"); owner != b.ExternalID && !(owner == "" && strings.HasPrefix(id, b.ExternalID+"_")) {
			return nil, errors.New("Facebook author mismatch")
		}
		at := nativeTimestamp(historyString(raw, "created_time"))
		if published, ok := raw["is_published"].(bool); ok && !published {
			at = nil
		}
		return []nativePost{{ID: id, Body: optionalText(historyString(raw, "message")), URL: optionalText(historyString(raw, "permalink_url")), PublishedAt: at}}, nil
	case "instagram":
		token := extractPageToken(b.PageCredentials)
		if token == "" || b.ExternalID == "" {
			return nil, errors.New("Instagram account credentials unavailable")
		}
		raw, err := historyExecute(ctx, b, "get_media", map[string]any{"mediaId": id, "fields": "id,owner,caption,timestamp,permalink", "access_token": token})
		if err != nil {
			return nil, err
		}
		if historyString(raw, "id") != id || historyString(historyMap(raw, "owner"), "id") != b.ExternalID {
			return nil, errors.New("Instagram could not verify native ID and selected account ownership")
		}
		return []nativePost{{ID: id, Body: optionalText(historyString(raw, "caption")), URL: optionalText(historyString(raw, "permalink")), PublishedAt: nativeTimestamp(historyString(raw, "timestamp"))}}, nil
	case "twitter":
		me, err := historyExecute(ctx, b, "get_me", map[string]any{"user.fields": "id"})
		if err != nil {
			return nil, err
		}
		owner := historyString(historyMap(me, "data"), "id")
		if owner == "" || b.ExternalID != "" && b.ExternalID != owner {
			return nil, errors.New("X account identity mismatch")
		}
		raw, err := historyExecute(ctx, b, "get_tweet", map[string]any{"tweet_id": id, "tweet.fields": "id,text,created_at,author_id"})
		if err != nil {
			return nil, err
		}
		m := historyMap(raw, "data")
		if historyString(m, "id") != id || historyString(m, "author_id") != owner {
			return nil, errors.New("X post is not owned by this Social account")
		}
		return []nativePost{{ID: id, Body: optionalText(historyString(m, "text")), URL: optionalText(historyString(m, "permalink_url", "share_url")), PublishedAt: nativeTimestamp(historyString(m, "created_at"))}}, nil
	case "tiktok":
		raw, err := historyExecute(ctx, b, "query_videos", map[string]any{"fields": "id,title,video_description,create_time,share_url,duration", "filters": map[string]any{"video_ids": []string{id}}})
		if err != nil {
			return nil, err
		}
		items, err := historyItems(historyMap(raw, "data"), "videos")
		if err != nil {
			return nil, err
		}
		for _, m := range items {
			if historyString(m, "id") == id {
				var at *string
				if seconds := nativeBytes(m["create_time"]); seconds != nil && *seconds > 0 {
					value := time.Unix(*seconds, 0).UTC().Format(time.RFC3339)
					at = &value
				}
				run.Scope = "public_video_metadata"
				return []nativePost{{ID: id, Title: optionalText(historyString(m, "title")), Body: optionalText(historyString(m, "video_description")), URL: optionalText(historyString(m, "share_url")), PublishedAt: at, Visibility: optionalText("public"), DurationSeconds: nativeNumber(m["duration"])}}, nil
			}
		}
		return nil, fmt.Errorf("TikTok did not return this ID under the connected account")
	}
	return nil, errors.New("fresh native inspection is unsupported for this channel/provider")
}
