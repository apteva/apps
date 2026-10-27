package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type historyReadError struct {
	status int
	err    error
}

func (e *historyReadError) Error() string { return e.err.Error() }

type nativePage struct {
	Posts       []nativePost
	Next        string
	Total       *int64
	TotalSource string
	ListedIDs   []string
}

func historyExecute(ctx *sdk.AppCtx, b historyAccount, tool string, args map[string]any) (map[string]any, error) {
	bounded := map[string]any{}
	for k, v := range args {
		bounded[k] = v
	}
	bounded["_apteva"] = map[string]any{"timeout_ms": 8000}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(b.ConnID, tool, bounded)
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		status := 0
		if res != nil {
			status = res.Status
		}
		return nil, &historyReadError{status: status, err: upstreamError(res)}
	}
	var out map[string]any
	dec := json.NewDecoder(bytes.NewReader(res.Data))
	dec.UseNumber()
	if dec.Decode(&out) != nil || out == nil {
		return nil, errors.New("invalid native history response")
	}
	return out, nil
}
func historyItems(raw map[string]any, key string) ([]map[string]any, error) {
	value, ok := raw[key]
	if !ok {
		return nil, fmt.Errorf("provider omitted %s history list", key)
	}
	array, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("invalid provider %s list", key)
	}
	out := make([]map[string]any, 0, len(array))
	for _, v := range array {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("invalid provider history item")
		}
		out = append(out, m)
	}
	return out, nil
}
func historyString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
		if v, ok := m[k].(json.Number); ok {
			return v.String()
		}
	}
	return ""
}
func historyMap(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	if v == nil {
		return map[string]any{}
	}
	return v
}
func nativeTimestamp(raw string) *string {
	t, e := time.Parse(time.RFC3339Nano, raw)
	if e != nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}
func nativeNumber(v any) *float64 {
	var n float64
	var e error
	switch x := v.(type) {
	case json.Number:
		n, e = x.Float64()
	case float64:
		n = x
	case string:
		n, e = strconv.ParseFloat(x, 64)
	default:
		return nil
	}
	if e != nil || n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
		return nil
	}
	return &n
}
func nativeBytes(v any) *int64 {
	if v == nil {
		return nil
	}
	s := fmt.Sprint(v)
	n, e := strconv.ParseInt(s, 10, 64)
	if e != nil || n < 0 {
		return nil
	}
	return &n
}
func youtubeDuration(raw string) *float64 {
	// YouTube duration is ISO 8601 (days/hours/minutes/seconds); never replace
	// absent or malformed duration with zero.
	if !strings.HasPrefix(raw, "P") {
		return nil
	}
	var total, value float64
	var digits string
	inTime := false
	seen := false
	for _, r := range raw[1:] {
		if r == 'T' {
			inTime = true
			continue
		}
		if r >= '0' && r <= '9' || r == '.' {
			digits += string(r)
			continue
		}
		if digits == "" {
			return nil
		}
		var e error
		value, e = strconv.ParseFloat(digits, 64)
		if e != nil {
			return nil
		}
		digits = ""
		seen = true
		switch r {
		case 'D':
			if inTime {
				return nil
			}
			total += value * 86400
		case 'H':
			if !inTime {
				return nil
			}
			total += value * 3600
		case 'M':
			if !inTime {
				return nil
			}
			total += value * 60
		case 'S':
			if !inTime {
				return nil
			}
			total += value
		default:
			return nil
		}
	}
	if !seen || digits != "" {
		return nil
	}
	return &total
}
func (a *App) youtubeHistoryIdentity(ctx *sdk.AppCtx, b historyAccount, run *historyRun) error {
	raw, err := historyExecute(ctx, b, "get_my_channel", map[string]any{"part": "id,contentDetails"})
	if err != nil {
		return err
	}
	items, err := historyItems(raw, "items")
	if err != nil || len(items) != 1 {
		return errors.New("YouTube connection must resolve exactly one owned channel")
	}
	owner := historyString(items[0], "id")
	if owner == "" {
		return errors.New("YouTube omitted the owned channel ID")
	}
	if b.ExternalID != "" && owner != b.ExternalID {
		return errors.New("YouTube connection does not match this Social account's channel")
	}
	if run.Owner != "" && run.Owner != owner {
		return errors.New("YouTube owner changed; start a new account-bound sync")
	}
	if err := verifyHistoryOwner(ctx, b, run, owner); err != nil {
		return err
	}
	resource := historyString(historyMap(historyMap(items[0], "contentDetails"), "relatedPlaylists"), "uploads")
	if resource == "" {
		return errors.New("YouTube uploads playlist unavailable")
	}
	run.Owner = owner
	run.Resource = resource
	return nil
}
func (a *App) youtubeNativePosts(ctx *sdk.AppCtx, b historyAccount, run *historyRun, ids []string) ([]nativePost, error) {
	if len(ids) == 0 {
		return []nativePost{}, nil
	}
	raw, err := historyExecute(ctx, b, "get_video", map[string]any{"id": strings.Join(ids, ","), "part": "snippet,status,contentDetails,fileDetails,processingDetails"})
	if err != nil {
		var providerErr *historyReadError
		if !errors.As(err, &providerErr) || providerErr.status != 400 && providerErr.status != 403 {
			return nil, err
		}
		// Optional owner-only metadata may be denied while normal metadata remains
		// readable. Authentication failures must still surface on the basic read.
		raw, err = historyExecute(ctx, b, "get_video", map[string]any{"id": strings.Join(ids, ","), "part": "snippet,status,contentDetails"})
		if err != nil {
			return nil, err
		}
		addHistoryLimitation(run, "YouTube owner-only filename, byte size or processing metadata was unavailable.")
	}
	items, err := historyItems(raw, "items")
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	seen := map[string]bool{}
	out := []nativePost{}
	for _, item := range items {
		id := historyString(item, "id")
		if !wanted[id] || seen[id] {
			continue
		}
		seen[id] = true
		snippet := historyMap(item, "snippet")
		if historyString(snippet, "channelId") != run.Owner {
			return nil, errors.New("YouTube returned a video outside the selected Social channel")
		}
		status := historyMap(item, "status")
		details := historyMap(item, "contentDetails")
		file := historyMap(item, "fileDetails")
		processing := historyMap(item, "processingDetails")
		out = append(out, nativePost{ID: id, URL: optionalText("https://www.youtube.com/watch?v=" + id), Title: nativeText(snippet, "title"), Body: nativeText(snippet, "description"), PublishedAt: nativeTimestamp(historyString(snippet, "publishedAt")), Visibility: optionalText(historyString(status, "privacyStatus")), ProcessingState: optionalText(nonEmpty(historyString(processing, "processingStatus"), historyString(status, "uploadStatus"))), OriginalFilename: optionalText(historyString(file, "fileName")), Bytes: nativeBytes(file["fileSize"]), DurationSeconds: youtubeDuration(historyString(details, "duration")), MediaURLs: []string{nativeThumbnail(historyMap(snippet, "thumbnails"))}})
	}
	if len(seen) != len(wanted) {
		run.Incomplete = true
		addHistoryLimitation(run, "YouTube omitted some listed video IDs; inaccessible/deleted status and metadata remain unknown.")
		for _, id := range ids {
			if !seen[id] {
				out = append(out, nativePost{ID: id})
			}
		}
	}
	return out, nil
}
func (a *App) fetchHistoryPage(ctx *sdk.AppCtx, b historyAccount, run *historyRun, size int) (nativePage, error) {
	if b.Provider == zernioProviderSlug {
		return a.fetchZernioHistoryPage(ctx, b, run, size)
	}
	switch b.Platform {
	case "youtube":
		if err := a.youtubeHistoryIdentity(ctx, b, run); err != nil {
			return nativePage{}, err
		}
		args := map[string]any{"playlistId": run.Resource, "part": "contentDetails", "maxResults": size}
		if run.Token != "" {
			args["pageToken"] = run.Token
		}
		raw, err := historyExecute(ctx, b, "list_playlist_items", args)
		if err != nil {
			return nativePage{}, err
		}
		items, err := historyItems(raw, "items")
		if err != nil {
			return nativePage{}, err
		}
		ids := []string{}
		listedIDs := []string{}
		seen := map[string]bool{}
		for _, item := range items {
			id := historyString(historyMap(item, "contentDetails"), "videoId")
			if id == "" {
				id = historyString(historyMap(historyMap(item, "snippet"), "resourceId"), "videoId")
			}
			if id == "" {
				run.Incomplete = true
				addHistoryLimitation(run, "A playlist item had no native video ID.")
				continue
			}
			listedIDs = append(listedIDs, id)
			if !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
		posts, err := a.youtubeNativePosts(ctx, b, run, ids)
		return nativePage{Posts: posts, Next: historyString(raw, "nextPageToken"), Total: nativeBytes(historyMap(raw, "pageInfo")["totalResults"]), TotalSource: "youtube.uploads.pageInfo.totalResults", ListedIDs: listedIDs}, err
	case "pinterest":
		tool := "list_pins"
		args := map[string]any{"page_size": size}
		if b.Kind == "destination" || b.ExternalID != "" {
			tool = "list_board_pins"
			args["board_id"] = b.ExternalID
			run.Scope = "destination_history"
			addHistoryLimitation(run, "Legacy board account: coverage includes this board's accessible Pins only.")
		}
		if run.Token != "" {
			args["bookmark"] = run.Token
		}
		raw, err := historyExecute(ctx, b, tool, args)
		if err != nil {
			return nativePage{}, err
		}
		items, err := historyItems(raw, "items")
		if err != nil {
			return nativePage{}, err
		}
		page := nativePage{Next: historyString(raw, "bookmark")}
		for _, m := range items {
			id := historyString(m, "id")
			page.Posts = append(page.Posts, nativePost{ID: id, URL: optionalText("https://www.pinterest.com/pin/" + id + "/"), Title: nativeText(m, "title"), Body: nativeText(m, "description"), PublishedAt: nativeTimestamp(historyString(m, "created_at")), Options: map[string]any{"destination_id": historyString(m, "board_id"), "board_section_id": historyString(m, "board_section_id"), "link": historyString(m, "link"), "alt_text": historyString(m, "alt_text")}, MediaURLs: []string{firstDeepString(m["media"], "url")}})
		}
		return page, nil
	case "facebook", "instagram":
		token := extractPageToken(b.PageCredentials)
		if token == "" || b.ExternalID == "" {
			return nativePage{}, errors.New("page/account identity or access token unavailable; reconnect account")
		}
		tool := "get_page_posts"
		itemsKey := "data"
		args := map[string]any{"pageId": b.ExternalID, "limit": size, "fields": "id,message,created_time,permalink_url,full_picture", "access_token": token}
		if b.Platform == "instagram" {
			tool = "get_account_media"
			args = map[string]any{"instagramAccountId": b.ExternalID, "limit": size, "fields": "id,caption,timestamp,permalink,media_url,thumbnail_url,media_type", "access_token": token}
		}
		if run.Token != "" {
			args["after"] = run.Token
		}
		raw, err := historyExecute(ctx, b, tool, args)
		if err != nil {
			return nativePage{}, err
		}
		items, err := historyItems(raw, itemsKey)
		if err != nil {
			return nativePage{}, err
		}
		page := nativePage{Total: nativeBytes(historyMap(raw, "summary")["total_count"]), TotalSource: b.Platform + ".posts.summary.total_count"}
		paging := historyMap(raw, "paging")
		if next := historyString(paging, "next"); next != "" {
			page.Next = historyString(historyMap(paging, "cursors"), "after")
			if page.Next == "" {
				u, e := url.Parse(next)
				if e == nil {
					page.Next = u.Query().Get("after")
				}
			}
			if page.Next == "" {
				return nativePage{}, errors.New("provider returned a next page without a safe cursor")
			}
		}
		for _, m := range items {
			page.Posts = append(page.Posts, nativePost{ID: historyString(m, "id"), Body: nativeText(m, "message", "caption"), URL: optionalText(historyString(m, "permalink_url", "permalink")), PublishedAt: nativeTimestamp(historyString(m, "created_time", "timestamp")), MediaURLs: []string{historyString(m, "media_url", "full_picture", "thumbnail_url")}})
		}
		return page, nil
	case "tiktok":
		user, e := historyExecute(ctx, b, "get_user_info", map[string]any{"fields": "open_id"})
		if e != nil {
			return nativePage{}, e
		}
		owner := historyString(historyMap(historyMap(user, "data"), "user"), "open_id")
		if err := verifyHistoryOwner(ctx, b, run, owner); err != nil {
			return nativePage{}, err
		}

		run.Scope = "public_video_history"
		addHistoryLimitation(run, "TikTok's Display API exposes accessible public videos, not private or deleted lifetime history.")
		if size > 20 {
			size = 20
		}
		args := map[string]any{"max_count": size, "fields": "id,title,video_description,create_time,share_url,cover_image_url,duration"}
		if run.Token != "" {
			v, e := strconv.ParseInt(run.Token, 10, 64)
			if e != nil {
				return nativePage{}, e
			}
			args["cursor"] = v
		}
		raw, err := historyExecute(ctx, b, "list_videos", args)
		if err != nil {
			return nativePage{}, err
		}
		data := historyMap(raw, "data")
		items, err := historyItems(data, "videos")
		if err != nil {
			return nativePage{}, err
		}
		more, ok := data["has_more"].(bool)
		if !ok {
			return nativePage{}, errors.New("TikTok omitted pagination completeness")
		}
		page := nativePage{}
		if more {
			page.Next = historyString(data, "cursor")
			if page.Next == "" || page.Next == "0" {
				return nativePage{}, errors.New("TikTok pagination cursor unavailable")
			}
		}
		for _, m := range items {
			var at *string
			if seconds := nativeBytes(m["create_time"]); seconds != nil {
				value := time.Unix(*seconds, 0).UTC().Format(time.RFC3339)
				at = &value
			}
			page.Posts = append(page.Posts, nativePost{ID: historyString(m, "id"), Title: nativeText(m, "title"), Body: nativeText(m, "video_description"), URL: optionalText(historyString(m, "share_url")), PublishedAt: at, DurationSeconds: nativeNumber(m["duration"]), Visibility: optionalText("public"), MediaURLs: []string{historyString(m, "cover_image_url")}})
		}
		return page, nil
	case "twitter":
		me, err := historyExecute(ctx, b, "get_me", map[string]any{"user.fields": "id"})
		if err != nil {
			return nativePage{}, err
		}
		owner := historyString(historyMap(me, "data"), "id")
		if owner == "" || b.ExternalID != "" && owner != b.ExternalID || run.Owner != "" && run.Owner != owner {
			return nativePage{}, errors.New("X connection/account identity mismatch")
		}
		if err := verifyHistoryOwner(ctx, b, run, owner); err != nil {
			return nativePage{}, err
		}
		args := map[string]any{"user_id": owner, "max_results": size, "tweet.fields": "id,text,created_at,author_id,entities"}
		if run.Token != "" {
			args["pagination_token"] = run.Token
		}
		raw, err := historyExecute(ctx, b, "get_user_tweets", args)
		if err != nil {
			return nativePage{}, err
		}
		items, err := historyItems(raw, "data")
		if err != nil {
			if n := nativeBytes(historyMap(raw, "meta")["result_count"]); n != nil && *n == 0 {
				items = []map[string]any{}
			} else {
				return nativePage{}, err
			}
		}
		page := nativePage{Next: historyString(historyMap(raw, "meta"), "next_token")}
		addHistoryLimitation(run, "X timeline coverage is limited by provider retention, plan and authorization.")
		for _, m := range items {
			if author := historyString(m, "author_id"); author != "" && author != owner {
				return nativePage{}, errors.New("X returned another account's post")
			}
			id := historyString(m, "id")
			page.Posts = append(page.Posts, nativePost{ID: id, Body: nativeText(m, "text"), URL: optionalText("https://x.com/i/web/status/" + id), PublishedAt: nativeTimestamp(historyString(m, "created_at")), MediaURLs: nativeTweetMedia(m)})
		}
		return page, nil
	case "linkedin":
		if !strings.HasPrefix(b.ExternalID, "urn:li:") {
			return nativePage{}, errors.New("LinkedIn history requires a selected author URN and permitted read access")
		}
		offset := 0
		if run.Token != "" {
			n, e := strconv.Atoi(run.Token)
			if e != nil {
				return nativePage{}, e
			}
			offset = n
		}
		raw, err := historyExecute(ctx, b, "list_posts", map[string]any{"author": b.ExternalID, "start": offset, "count": size})
		if err != nil {
			return nativePage{}, err
		}
		items, err := historyItems(raw, "elements")
		if err != nil {
			return nativePage{}, err
		}
		page := nativePage{Total: nativeBytes(historyMap(raw, "paging")["total"]), TotalSource: "linkedin.author_posts.paging.total"}
		if len(items) == size {
			page.Next = strconv.Itoa(offset + len(items))
		}
		addHistoryLimitation(run, "LinkedIn history is limited by author permissions and provider retention.")
		for _, m := range items {
			if historyString(m, "author") != b.ExternalID {
				return nativePage{}, errors.New("LinkedIn author mismatch")
			}
			id := historyString(m, "id")
			var at *string
			if ms := nativeBytes(m["publishedAt"]); ms != nil {
				value := time.UnixMilli(*ms).UTC().Format(time.RFC3339)
				at = &value
			}
			page.Posts = append(page.Posts, nativePost{ID: id, Body: nativeText(m, "commentary"), PublishedAt: at, Visibility: optionalText(historyString(m, "visibility"))})
		}
		return page, nil
	}
	return nativePage{}, errors.New("history adapter unavailable")
}
func (a *App) fetchZernioHistoryPage(ctx *sdk.AppCtx, b historyAccount, run *historyRun, size int) (nativePage, error) {
	run.Scope = "provider_indexed_published_history"
	addHistoryLimitation(run, "Zernio coverage is provider-indexed published history; this does not prove a complete native backfill.")
	if b.ProviderID == "" {
		return nativePage{}, errors.New("provider account identity missing")
	}
	number := 1
	if run.Token != "" {
		n, e := strconv.Atoi(run.Token)
		if e != nil {
			return nativePage{}, e
		}
		number = n
	}
	args := map[string]any{"accountId": b.ProviderID, "status": "published", "page": number, "limit": size}
	raw, err := historyExecute(ctx, b, "list_posts", args)
	if err != nil {
		return nativePage{}, err
	}
	if data, ok := raw["data"].(map[string]any); ok {
		raw = data
	}
	var items []map[string]any
	found := false
	for _, key := range []string{"posts", "data", "items", "results"} {
		if _, ok := raw[key]; ok {
			items, err = historyItems(raw, key)
			found = true
			break
		}
	}
	if !found || err != nil {
		return nativePage{}, errors.New("provider omitted a valid post list")
	}
	page := nativePage{}
	// A short page is not enough when the provider explicitly supplies hasMore.
	more, explicit := raw["hasMore"].(bool)
	pagination := historyMap(raw, "pagination")
	page.Total = nativeBytes(pagination["total"])
	page.TotalSource = "zernio.account_posts.pagination.total"
	if value, ok := pagination["hasMore"].(bool); ok {
		more = value
		explicit = true
	}
	if value, ok := pagination["has_more"].(bool); ok {
		more = value
		explicit = true
	}
	if total := nativeBytes(pagination["totalPages"]); total != nil {
		more = int64(number) < *total
		explicit = true
	}
	if total := nativeBytes(pagination["total_pages"]); total != nil {
		more = int64(number) < *total
		explicit = true
	}
	if explicit && more || !explicit && len(items) == size {
		page.Next = strconv.Itoa(number + 1)
	}
	for _, m := range items {
		part := zernioPlatformPost(m, b.ProviderID, b.Platform)
		if firstString(part, "accountId._id", "accountId.id", "account_id._id", "account_id.id", "accountId", "account_id") != b.ProviderID {
			run.Incomplete = true
			addHistoryLimitation(run, "Provider returned an item without the requested account binding.")
			continue
		}
		id := firstString(part, "platformPostId", "platform_post_id", "externalId", "external_id")
		if id == "" {
			run.Incomplete = true
			addHistoryLimitation(run, "Provider omitted a native ID; provider operation IDs are not native post IDs.")
			continue
		}
		page.Posts = append(page.Posts, nativePost{ID: id, ProviderPostID: optionalText(firstString(m, "id", "_id", "postId")), Title: optionalText(firstString(m, "title")), Body: optionalText(firstString(m, "content", "text", "caption", "description", "body")), URL: optionalText(firstString(part, "platformPostUrl", "platform_post_url", "platformUrl", "platform_url", "permalink", "url")), PublishedAt: nativeTimestamp(firstString(part, "publishedAt", "published_at")), Visibility: optionalText(firstString(part, "visibility", "privacyStatus")), ProcessingState: optionalText(firstString(part, "status"))})
	}
	return page, nil
}

func (a *App) toolPostNativeInspect(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	b, err := loadHistoryAccount(ctx, int64(intArg(args, "social_account_id", 0)))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	id := strings.TrimSpace(toString(args["native_post_id"]))
	if id == "" || len(id) > 512 {
		return mcpError("native_post_id required (max 512 characters)"), nil
	}
	if historyCapabilities(b.Platform, b.Provider, b.ExternalID)["inspection"] != true {
		return map[string]any{"status": "unsupported", "social_account_id": b.ID, "limitation": "Fresh native inspection is not exposed for this channel/provider; do not substitute stale history for native verification."}, nil
	}
	unlock := lockHistory(b)
	defer unlock()
	run := historyRun{Limitations: []string{}}
	posts, err := a.inspectNativeHistory(ctx, b, &run, id)
	if err != nil {
		return map[string]any{"status": "failed", "social_account_id": b.ID, "error": redactDeliverySecrets(err.Error())}, nil
	}
	if len(posts) != 1 || run.Incomplete {
		return map[string]any{"status": "not_found", "social_account_id": b.ID, "native_post_id": id, "limitation": "Provider did not return this ID under the connected account; existence/access remains unknown."}, nil
	}
	n := posts[0]
	n.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	_, post, target, err := a.upsertNativeHistory(ctx, b, n)
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "social_account_id": b.ID, "post_id": post, "target_id": target, "native_metadata": n, "observed_at": n.ObservedAt, "verification_scope": nonEmpty(run.Scope, "provider_metadata"), "public_live_verified": false, "limitations": run.Limitations}, nil
}

func nativeText(m map[string]any, keys ...string) *string {
	for _, key := range keys {
		if s, ok := m[key].(string); ok {
			return &s
		}
	}
	return nil
}

func nativeThumbnail(m map[string]any) string {
	for _, key := range []string{"maxres", "standard", "high", "medium", "default"} {
		if s := historyString(historyMap(m, key), "url"); s != "" {
			return s
		}
	}
	return ""
}
func nativeTweetMedia(m map[string]any) []string {
	out := []string{}
	items, _ := historyMap(m, "entities")["urls"].([]any)
	for _, item := range items {
		if v, ok := item.(map[string]any); ok {
			if u := historyString(v, "expanded_url", "url"); u != "" {
				out = append(out, u)
			}
		}
	}
	return out
}

func verifyHistoryOwner(ctx *sdk.AppCtx, b historyAccount, run *historyRun, owner string) error {
	if owner == "" || b.ExternalID != "" && b.ExternalID != owner || run.Owner != "" && run.Owner != owner {
		return errors.New("connection owner does not match this Social account/history run")
	}
	if run.Owner == "" && b.ExternalID == "" {
		var state string
		e := ctx.AppDB().QueryRow(`SELECT state FROM social_history_runs WHERE project_id=? AND social_account_id=? AND json_extract(state,'$.owner')<>'' ORDER BY rowid DESC LIMIT 1`, b.Project, b.ID).Scan(&state)
		if e == nil {
			var old historyRun
			_ = json.Unmarshal([]byte(state), &old)
			if old.Owner != "" && old.Owner != owner {
				return errors.New("connection now belongs to a different native owner; use a separate Social account")
			}
		}
	}
	_, err := ctx.AppDB().Exec(`INSERT OR IGNORE INTO social_history_owners(project_id,social_account_id,platform,native_owner_id) VALUES(?,?,?,?)`, b.Project, b.ID, b.Platform, owner)
	if err != nil {
		return err
	}
	var stored, platform string
	err = ctx.AppDB().QueryRow(`SELECT native_owner_id,platform FROM social_history_owners WHERE project_id=? AND social_account_id=?`, b.Project, b.ID).Scan(&stored, &platform)
	if err != nil {
		return err
	}
	if stored != owner || platform != b.Platform {
		return errors.New("Social account is bound to another native owner; reconnect as a separate Social account")
	}
	run.Owner = owner
	return nil
}
