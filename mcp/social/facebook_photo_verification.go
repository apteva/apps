package main

import (
	"database/sql"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	facebookPhotoWindow        = 10 * time.Minute
	facebookPhotoMaxPages      = 5
	facebookPagePostFields     = "id,from,message,created_time,permalink_url,is_published,is_hidden,attachments{target,type}"
	facebookPagePostTimeSource = "facebook.page_post.created_time:published"
)

type facebookPhotoTarget struct {
	postID, targetID int64
	photoID          string
	pagePostID       string
}

type facebookPagePostEvidence struct {
	Status       string  `json:"status"`
	Reason       string  `json:"reason,omitempty"`
	PageID       string  `json:"page_id"`
	PhotoID      string  `json:"photo_id"`
	PagePostID   *string `json:"page_post_id"`
	IsPublished  *bool   `json:"is_published"`
	IsHidden     *bool   `json:"is_hidden"`
	PublishedAt  *string `json:"published_at"`
	PermalinkURL *string `json:"permalink_url"`
	ObservedAt   string  `json:"observed_at"`
}

// A saved Page post ID is an alias of the photo's existing managed target.
// Legacy native videos have delivery diagnostics, while legacy photos do not.
func loadFacebookPhotoTarget(ctx *sdk.AppCtx, b historyAccount, id string) (facebookPhotoTarget, error) {
	var target facebookPhotoTarget
	var diagnostics string
	var matches int
	err := ctx.AppDB().QueryRow(`
		SELECT p.id, t.id, COALESCE(t.platform_post_id,''), COALESCE(t.facebook_page_post_id,''),
		       COALESCE(t.delivery_diagnostics,''), COUNT(*) OVER ()
		FROM post_targets t JOIN posts p ON p.id=t.post_id
		WHERE p.project_id=? AND t.social_account_id=? AND (t.platform_post_id=? OR t.facebook_page_post_id=?)
		LIMIT 1`, b.Project, b.ID, id, id).Scan(&target.postID, &target.targetID, &target.photoID, &target.pagePostID, &diagnostics, &matches)
	if errors.Is(err, sql.ErrNoRows) {
		return facebookPhotoTarget{}, nil
	}
	if err != nil {
		return facebookPhotoTarget{}, err
	}
	if matches != 1 {
		return facebookPhotoTarget{}, errors.New("Facebook identity matches multiple delivery targets")
	}
	diagnostics = strings.TrimSpace(diagnostics)
	legacyPhoto := !strings.Contains(id, "_") && (diagnostics == "" || diagnostics == "{}" || diagnostics == "null")
	if target.photoID == "" || (target.pagePostID == "" && !legacyPhoto) {
		return facebookPhotoTarget{}, nil
	}
	return target, nil
}

func inspectFacebookPhoto(ctx *sdk.AppCtx, b historyAccount, run *historyRun, target facebookPhotoTarget, token string) ([]nativePost, error) {
	evidence := &facebookPagePostEvidence{
		Status: "unverified", PageID: b.ExternalID, PhotoID: target.photoID,
		PagePostID: optionalText(target.pagePostID),
	}
	run.facebookPhoto, run.facebookPagePost = target, evidence
	run.Scope = "facebook_photo"
	defer func() { evidence.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano) }()

	if token == "" || b.ExternalID == "" {
		evidence.Reason = "Connected Page identity or credentials unavailable"
		return nil, errors.New(evidence.Reason)
	}

	photo := nativePost{ID: target.photoID, expectedTargetID: target.targetID}
	var post map[string]any
	var err error
	if target.pagePostID != "" {
		post, err = readStoredFacebookPagePost(ctx, b, target, token)
		if err != nil {
			evidence.Reason = err.Error()
			return nil, err
		}
		photo.Body = nativeText(post, "message")
	} else {
		raw, dropped, readErr := facebookReadWithFieldFallback(ctx, b, "facebook_get_post",
			map[string]any{"postId": target.photoID, "access_token": token},
			[]string{"id", "from", "name", "created_time", "link"},
			map[string]bool{"name": true, "link": true})
		if readErr != nil {
			evidence.Reason = "Facebook photo lookup failed: " + redactDeliverySecrets(readErr.Error())
			return nil, readErr
		}
		if historyString(raw, "id") != target.photoID || historyString(historyMap(raw, "from"), "id") != b.ExternalID {
			evidence.Reason = "Facebook photo identity or Page ownership did not match"
			return nil, errors.New(evidence.Reason)
		}
		for _, field := range dropped {
			addHistoryLimitation(run, "Facebook does not expose "+field+" on this photo")
		}
		photo.Body = nativeText(raw, "name")
		photo.URL = optionalText(historyString(raw, "link"))
		photo.CreatedAt = nativeTimestamp(historyString(raw, "created_time"))
		post, err = findLegacyFacebookPagePost(ctx, b, photo, token)
		if err != nil {
			evidence.Reason = err.Error()
			return []nativePost{photo}, nil
		}
	}

	evidence.PagePostID = optionalText(historyString(post, "id"))
	evidence.PermalinkURL = optionalText(safePublicationURL("facebook", historyString(post, "permalink_url")))
	if published, ok := post["is_published"].(bool); ok {
		evidence.IsPublished = &published
		if published {
			at := nativeTimestamp(historyString(post, "created_time"))
			if created, err := time.Parse(time.RFC3339Nano, valueOrEmpty(at)); err == nil && !created.After(time.Now().UTC()) {
				evidence.PublishedAt = at
			}
		}
	}
	if hidden, ok := post["is_hidden"].(bool); ok {
		evidence.IsHidden = &hidden
	}
	switch {
	case evidence.IsPublished == nil || evidence.IsHidden == nil:
		evidence.Reason = "Facebook omitted a Page post publication flag"
	case *evidence.IsPublished && (evidence.PublishedAt == nil || evidence.PermalinkURL == nil):
		evidence.Reason = "Facebook omitted a valid publication timestamp or permalink"
	default:
		evidence.Status = "verified"
		photo.PublishedAt = evidence.PublishedAt
		if photo.PublishedAt != nil {
			photo.PublicationTimeSource = facebookPagePostTimeSource
		}
	}
	return []nativePost{photo}, nil
}

func readStoredFacebookPagePost(ctx *sdk.AppCtx, b historyAccount, target facebookPhotoTarget, token string) (map[string]any, error) {
	post, err := historyExecute(ctx, b, "facebook_get_post", map[string]any{
		"postId": target.pagePostID, "access_token": token, "fields": facebookPagePostFields,
	})
	if err != nil {
		return nil, errors.New("Facebook Page post lookup failed: " + redactDeliverySecrets(err.Error()))
	}
	if historyString(post, "id") != target.pagePostID || !facebookPostOwnedByPage(post, b.ExternalID) {
		return nil, errors.New("Facebook Page post identity or Page ownership did not match")
	}
	hasPhoto, err := facebookPostHasPhoto(post, target.photoID)
	if err != nil {
		return nil, err
	}
	if !hasPhoto {
		return nil, errors.New("Stored Facebook Page post did not contain the exact photo")
	}
	return post, nil
}

func findLegacyFacebookPagePost(ctx *sdk.AppCtx, b historyAccount, photo nativePost, token string) (map[string]any, error) {
	created, err := time.Parse(time.RFC3339Nano, valueOrEmpty(photo.CreatedAt))
	if err != nil {
		return nil, errors.New("Facebook photo creation time unavailable for the bounded Page post lookup")
	}
	since, until := created.Add(-facebookPhotoWindow), created.Add(facebookPhotoWindow)
	args := map[string]any{
		"pageId": b.ExternalID, "access_token": token, "limit": 50, "include_hidden": true,
		"since": strconv.FormatInt(since.Unix(), 10), "until": strconv.FormatInt(until.Unix(), 10),
		"fields": facebookPagePostFields,
	}
	var match map[string]any
	seen := make(map[string]bool)
	for range facebookPhotoMaxPages {
		raw, err := historyExecute(ctx, b, "get_page_posts", args)
		if err != nil {
			return nil, errors.New("Facebook Page post lookup failed: " + redactDeliverySecrets(err.Error()))
		}
		posts, err := historyItems(raw, "data")
		if err != nil {
			return nil, err
		}
		for _, post := range posts {
			hasPhoto, err := facebookPostHasPhoto(post, photo.ID)
			if err != nil {
				return nil, err
			}
			if !hasPhoto {
				continue
			}
			if !facebookPostOwnedByPage(post, b.ExternalID) {
				return nil, errors.New("Matching Facebook photo attachment was returned on the wrong or unknown Page")
			}
			if match != nil && historyString(match, "id") != historyString(post, "id") {
				return nil, errors.New("Multiple Facebook Page posts matched the exact photo")
			}
			match = post
		}
		paging := historyMap(raw, "paging")
		next := historyString(paging, "next")
		if next == "" {
			return crossCheckFacebookPhotoPost(match, photo, since, until)
		}
		cursor := historyString(historyMap(paging, "cursors"), "after")
		if cursor == "" {
			if u, err := url.Parse(next); err == nil {
				cursor = u.Query().Get("after")
			}
		}
		if cursor == "" || seen[cursor] {
			break
		}
		seen[cursor], args["after"] = true, cursor
	}
	return nil, errors.New("Bounded Facebook Page post lookup returned incomplete results")
}

func facebookPostOwnedByPage(post map[string]any, pageID string) bool {
	id := historyString(post, "id")
	return strings.HasPrefix(id, pageID+"_") && len(id) > len(pageID)+1 &&
		historyString(historyMap(post, "from"), "id") == pageID
}

func facebookPostHasPhoto(post map[string]any, photoID string) (bool, error) {
	edge := historyMap(post, "attachments")
	attachments, err := historyItems(edge, "data")
	if err != nil {
		return false, errors.New("Facebook Page post attachment data was incomplete")
	}
	for _, attachment := range attachments {
		id := historyString(historyMap(attachment, "target"), "id")
		if id == photoID {
			return true, nil
		}
		if id == "" && historyString(attachment, "type") == "photo" {
			return false, errors.New("Facebook Page post photo attachment ID was omitted")
		}
	}
	if historyString(historyMap(edge, "paging"), "next") != "" {
		return false, errors.New("Facebook Page post attachment data was incomplete")
	}
	return false, nil
}

func crossCheckFacebookPhotoPost(post map[string]any, photo nativePost, since, until time.Time) (map[string]any, error) {
	if post == nil {
		return nil, errors.New("No Facebook Page post matched the exact photo in the bounded window")
	}
	at := nativeTimestamp(historyString(post, "created_time"))
	created, err := time.Parse(time.RFC3339Nano, valueOrEmpty(at))
	if err != nil || created.Before(since) || created.After(until) {
		return nil, errors.New("Facebook Page post creation time did not match the photo's search window")
	}
	if photo.Body != nil && historyString(post, "message") != *photo.Body {
		return nil, errors.New("Facebook Page post caption did not match the photo")
	}
	return post, nil
}
