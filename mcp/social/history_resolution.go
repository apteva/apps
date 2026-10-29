package main

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var youtubeVideoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

const youtubeDerivedURLSource = "derived_from_verified_native_id"

// youtubeWatchURL is the canonical watch link for a YouTube video ID. It is
// derived from the ID alone: it says nothing about public or Members access.
func youtubeWatchURL(id string) string {
	if !youtubeVideoIDPattern.MatchString(id) {
		return ""
	}
	return "https://www.youtube.com/watch?v=" + id
}

// youtubeOmissionResolution classifies a listed video ID that the video
// lookup keeps omitting, using the uploads-playlist entry for that exact ID.
type youtubeOmissionResolution struct {
	Resolution string         `json:"resolution"` // unavailable | unresolved
	Reason     string         `json:"reason"`
	ObservedAt string         `json:"observed_at"`
	Evidence   map[string]any `json:"evidence"`
}

// Playlist entries survive their video resource: YouTube keeps a placeholder
// entry (title "Deleted video"/"Private video", no videoPublishedAt) when the
// video itself is gone or unreadable. Only that marker counts as explicit
// unavailability. An absent entry or a normal-looking one stays unresolved.
func (a *App) diagnoseOmittedYouTubeVideo(ctx *sdk.AppCtx, b historyAccount, run *historyRun, id string) (youtubeOmissionResolution, error) {
	out := youtubeOmissionResolution{Resolution: "unresolved", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Evidence: map[string]any{"video_lookup": "omitted_after_account_bound_readback", "uploads_playlist": run.Resource}}
	raw, err := historyExecute(ctx, b, "list_playlist_items", map[string]any{"playlistId": run.Resource, "videoId": id, "part": "snippet,contentDetails,status", "maxResults": 5})
	if err != nil {
		var providerErr *historyReadError
		if errors.As(err, &providerErr) && (providerErr.status == 401 || providerErr.status == 403) {
			return out, err
		}
		// YouTube answers the videoId filter with 404 videoNotFound when the
		// listed ID has no video resource: an explicit provider statement,
		// distinct from a failed lookup. The ID is kept, not deleted.
		if errors.As(err, &providerErr) && providerErr.status == 404 && strings.Contains(err.Error(), `"reason":"videoNotFound"`) {
			out.Resolution, out.Reason = "unavailable", "provider_video_not_found"
			out.Evidence["playlist_lookup"] = map[string]any{"status": 404, "provider_reason": "videoNotFound", "provider_domain": "youtube.playlistItem", "location": "videoId"}
			return out, nil
		}
		out.Reason = "playlist_lookup_failed"
		out.Evidence["playlist_lookup_error"] = redactDeliverySecrets(err.Error())
		return out, nil
	}
	items, err := historyItems(raw, "items")
	if err != nil {
		return out, err
	}
	for _, item := range items {
		details := historyMap(item, "contentDetails")
		if historyString(details, "videoId") != id {
			continue
		}
		snippet := historyMap(item, "snippet")
		if owner := historyString(snippet, "channelId"); owner != "" && owner != run.Owner {
			return out, errors.New("YouTube returned an uploads-playlist entry outside the selected Social channel")
		}
		videoOwner := historyString(snippet, "videoOwnerChannelId")
		if videoOwner != "" && videoOwner != run.Owner {
			return out, errors.New("YouTube playlist entry belongs to a different channel")
		}
		title := historyString(snippet, "title")
		privacy := historyString(historyMap(item, "status"), "privacyStatus")
		publishedAt := historyString(details, "videoPublishedAt")
		out.Evidence["playlist_entry"] = map[string]any{"present": true, "title": title, "privacy_status": privacy, "video_published_at": publishedAt, "video_owner_channel_id": videoOwner}
		if title == "Deleted video" || title == "Private video" || privacy == "privacyStatusUnspecified" || publishedAt == "" && videoOwner == "" {
			out.Resolution, out.Reason = "unavailable", "playlist_entry_without_video_resource"
		} else {
			out.Reason = "playlist_entry_present_video_lookup_omitted"
		}
		return out, nil
	}
	out.Reason = "playlist_entry_absent"
	out.Evidence["playlist_entry"] = map[string]any{"present": false}
	return out, nil
}

// After the last bounded readback of a still-omitted ID, record why. Earlier
// attempts are left alone so a later readback can still resolve it.
func (a *App) resolveOmittedYouTubeMetadata(ctx *sdk.AppCtx, b historyAccount, run *historyRun, id string) error {
	var missing, attempts int
	if err := ctx.AppDB().QueryRow(`SELECT metadata_missing,metadata_attempts FROM social_history_seen WHERE run_id=? AND native_post_id=?`, run.ID, id).Scan(&missing, &attempts); err != nil {
		return err
	}
	if missing == 0 || attempts < historyMetadataAttemptsPerPost {
		return nil
	}
	r, err := a.diagnoseOmittedYouTubeVideo(ctx, b, run, id)
	if err != nil {
		return err
	}
	resolution := ""
	if r.Resolution == "unavailable" {
		resolution = "unavailable"
	} else {
		resolution = "unresolved"
	}
	evidence, _ := json.Marshal(r)
	_, err = ctx.AppDB().Exec(`UPDATE social_history_seen SET metadata_resolution=?,metadata_evidence=?,metadata_observed_at=? WHERE run_id=? AND native_post_id=? AND metadata_missing=1`, resolution, string(evidence), r.ObservedAt, run.ID, id)
	return err
}

var facebookNonexistingField = regexp.MustCompile(`Tried accessing nonexisting field \(([A-Za-z_]+)\)`)

// facebookReadWithFieldFallback reads one Graph object. If Graph rejects one
// optional field as nonexistent on this object type, it retries once without
// it. Authentication, permission and any other errors stay failures.
func facebookReadWithFieldFallback(ctx *sdk.AppCtx, b historyAccount, tool string, args map[string]any, fields []string, optional map[string]bool) (map[string]any, []string, error) {
	read := func(fields []string) (map[string]any, error) {
		call := map[string]any{"fields": strings.Join(fields, ",")}
		for k, v := range args {
			call[k] = v
		}
		return historyExecute(ctx, b, tool, call)
	}
	raw, err := read(fields)
	if err == nil {
		return raw, nil, nil
	}
	match := facebookNonexistingField.FindStringSubmatch(err.Error())
	if match == nil || !optional[match[1]] {
		return nil, nil, err
	}
	kept := []string{}
	for _, field := range fields {
		if field != match[1] {
			kept = append(kept, field)
		}
	}
	raw, err = read(kept)
	return raw, []string{match[1]}, err
}

// facebookNativeObjectKind picks the read contract for a native ID from the
// saved publishing format. Feed posts are always pageId_postId. /videos and
// /photos both return a bare object ID; a video publish leaves upload
// evidence on its target, a photo publish does not. The ID is never rewritten.
func facebookNativeObjectKind(ctx *sdk.AppCtx, b historyAccount, id string) string {
	if strings.Contains(id, "_") {
		return "page_post"
	}
	var diagnostics string
	err := ctx.AppDB().QueryRow(`SELECT COALESCE(t.delivery_diagnostics,'') FROM post_targets t JOIN posts p ON p.id=t.post_id WHERE p.project_id=? AND t.social_account_id=? AND t.platform_post_id=?`, b.Project, b.ID, id).Scan(&diagnostics)
	if err == nil && (strings.TrimSpace(diagnostics) == "" || diagnostics == "{}" || diagnostics == "null") {
		return "photo"
	}
	return "video"
}

func inspectFacebookNative(ctx *sdk.AppCtx, b historyAccount, run *historyRun, id, token string) ([]nativePost, error) {
	kind := facebookNativeObjectKind(ctx, b, id)
	var raw map[string]any
	var dropped []string
	var err error
	switch kind {
	case "video":
		raw, dropped, err = facebookReadWithFieldFallback(ctx, b, "facebook_get_video", map[string]any{"videoId": id, "access_token": token},
			[]string{"id", "from", "description", "title", "created_time", "permalink_url", "published"},
			map[string]bool{"description": true, "title": true, "permalink_url": true, "published": true})
	case "photo":
		raw, dropped, err = facebookReadWithFieldFallback(ctx, b, "facebook_get_post", map[string]any{"postId": id, "access_token": token},
			[]string{"id", "from", "name", "created_time", "link"},
			map[string]bool{"name": true, "link": true})
	default:
		raw, dropped, err = facebookReadWithFieldFallback(ctx, b, "facebook_get_post", map[string]any{"postId": id, "access_token": token},
			[]string{"id", "from", "message", "created_time", "permalink_url", "is_published"},
			map[string]bool{"message": true, "permalink_url": true, "is_published": true})
	}
	if err != nil {
		return nil, err
	}
	if historyString(raw, "id") != id {
		return nil, errors.New("Facebook native ID mismatch")
	}
	owner := historyString(historyMap(raw, "from"), "id")
	if kind != "page_post" {
		// Video and photo IDs carry no Page prefix, so the author is the only
		// ownership evidence and is required.
		if owner == "" || owner != b.ExternalID {
			return nil, errors.New("Facebook video is not verified as owned by the selected Page")
		}
	} else if owner != b.ExternalID && !(owner == "" && strings.HasPrefix(id, b.ExternalID+"_")) {
		return nil, errors.New("Facebook author mismatch")
	}
	run.Scope = "facebook_" + kind
	for _, field := range dropped {
		addHistoryLimitation(run, "Facebook does not expose "+field+" on this object type; it was omitted from the read.")
	}
	at := nativeTimestamp(historyString(raw, "created_time"))
	body, link, published := historyString(raw, "message"), historyString(raw, "permalink_url"), raw["is_published"]
	switch kind {
	case "video":
		body, published = historyString(raw, "description"), raw["published"]
	case "photo":
		body, link, published = historyString(raw, "name"), historyString(raw, "link"), nil
	}
	if p, ok := published.(bool); ok && !p {
		at = nil
	}
	post := nativePost{ID: id, Body: optionalText(body), URL: optionalText(link), PublishedAt: at}
	if kind == "video" {
		post.Title = optionalText(historyString(raw, "title"))
	}
	return []nativePost{post}, nil
}
