package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Fetch stored deliveries, including posts beyond Social's first page.
func fetchSocialPosts(ctx *sdk.AppCtx, wanted map[int64]bool) (map[int64]map[string]any, error) {
	found := map[int64]map[string]any{}
	for offset := int64(0); len(found) < len(wanted); {
		var out map[string]any
		err := ctx.PlatformAPI().CallAppResult("social", "post_list", map[string]any{"limit": 200, "offset": offset, "compact": true}, &out)
		if err != nil {
			return nil, err
		}
		posts, _ := out["posts"].([]any)
		for _, value := range posts {
			post, ok := value.(map[string]any)
			if ok && wanted[number(post, "id")] {
				found[number(post, "id")] = post
			}
		}
		if more, _ := out["has_more"].(bool); !more {
			break
		}
		next := number(out, "next_offset")
		if len(posts) == 0 || next <= offset {
			return nil, invalid("Social returned invalid pagination; previous state preserved")
		}
		offset = next
	}
	return found, nil
}

func socialPlatform(channel string) string {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "x", "twitter", "x/twitter":
		return "twitter"
	default:
		return strings.ToLower(strings.TrimSpace(channel))
	}
}

func socialReleaseTarget(r Release, post map[string]any) (map[string]any, error) {
	raw, _ := json.Marshal(post["targets"])
	var targets []map[string]any
	if err := json.Unmarshal(raw, &targets); err != nil {
		return nil, invalid("linked Social post has no readable targets")
	}
	account := number(r.Results, "social_account_id")
	if account <= 0 {
		account = number(r.Results, "account_id")
	}
	var matches []map[string]any
	for _, target := range targets {
		if socialPlatform(str(target, "platform")) == socialPlatform(r.Channel) && (account <= 0 || number(target, "social_account_id") == account) {
			matches = append(matches, target)
		}
	}
	if len(matches) != 1 {
		return nil, invalid("linked Social destination is missing or ambiguous; previous state preserved")
	}
	return matches[0], nil
}

func socialPublishedTime(target map[string]any) string {
	native, _ := target["native_metadata"].(map[string]any)
	facts, _ := target["publication_facts"].(map[string]any)
	for _, value := range []string{str(native, "published_at"), str(facts, "native_published_at"), str(facts, "provider_reported_published_at"), str(target, "published_at")} {
		if at, err := time.Parse(time.RFC3339, value); err == nil {
			return at.UTC().Format(time.RFC3339Nano)
		}
	}
	// Submission and local delivery-record timestamps are not publication times.
	return ""
}

func socialPublishedURL(target map[string]any) string {
	facts, _ := target["publication_facts"].(map[string]any)
	native, _ := target["native_metadata"].(map[string]any)
	for _, value := range []string{str(target, "platform_url"), str(facts, "url"), str(native, "url")} {
		if value != "" && validateURL(value) == nil {
			return value
		}
	}
	return ""
}

// Caller holds writes. Re-read after Social responds to detect a concurrent rebind.
func (a *App) syncSocialRelease(ctx *sdk.AppCtx, original Release, post map[string]any) (Release, bool, error) {
	pid := ctx.CurrentProject()
	r, err := readRelease(ctx.AppDB(), pid, original.ID)
	if err != nil {
		return r, false, err
	}
	if r.App != original.App || r.ExternalID != original.ExternalID || number(post, "id") != r.ExternalID {
		return r, false, errConflict
	}
	parent, err := readItem(ctx.AppDB(), pid, r.ItemID)
	if err != nil {
		return r, false, err
	}
	if parent.Archived || r.Archived || r.Status == "cancelled" {
		return r, false, nil
	}
	settings, err := getSettings(ctx.AppDB(), pid)
	if err != nil {
		return r, false, err
	}
	if !brandAllows(findBrand(settings, parent.BrandID), "social", post) {
		return r, false, invalid("linked Social record is outside this brand's mappings")
	}
	target, err := socialReleaseTarget(r, post)
	if err != nil {
		return r, false, err
	}
	status := str(target, "status")
	patch := map[string]any{}
	switch status {
	case "published":
		patch["status"] = "published"
		if value := socialPublishedTime(target); value != "" {
			patch["published_at"] = value
		}
		if value := socialPublishedURL(target); value != "" {
			patch["url"] = value
		}
	case "failed", "cancelled":
		if r.Status != "published" {
			patch["status"] = status
		}
	case "pending", "scheduled":
		if str(post, "status") == "scheduled" && r.Status != "published" {
			patch["status"] = "scheduled"
		}
	}
	snapshot := map[string]any{"post_id": r.ExternalID, "target_id": number(target, "id"), "social_account_id": number(target, "social_account_id"), "platform": str(target, "platform"), "status": status, "post_status": str(post, "status"), "native_post_id": str(target, "platform_post_id"), "url": socialPublishedURL(target), "published_at": socialPublishedTime(target)}
	results := mergeResultObject(mergeResultObject(r.Results, post), map[string]any{"social_delivery": snapshot})
	changed := !reflect.DeepEqual(jsonObject(r.Results), jsonObject(results))
	current := jsonObject(r)
	for key, value := range patch {
		if current[key] != value {
			changed = true
		}
	}
	if !changed {
		return r, false, nil
	}
	patch["results"] = results
	patch["synced_at"] = a.clock().UTC().Format(time.RFC3339)
	saved, err := saveRelease(ctx.AppDB(), pid, r.ID, 0, r.Revision, patch, true)
	return saved, err == nil, err
}

// Retry parent aggregation even when release data is unchanged after a restart.
// Status-only saves preserve the content and its existing approval.
func (a *App) syncSocialParent(ctx *sdk.AppCtx, itemID int64) error {
	pid := ctx.CurrentProject()
	parent, err := readItem(ctx.AppDB(), pid, itemID)
	if err != nil || parent.Archived {
		return err
	}
	rows, err := ctx.AppDB().Query("SELECT data FROM editorial_releases WHERE project_id=? AND item_id=?", pid, itemID)
	if err != nil {
		return err
	}
	var deliveries []ReleaseData
	for rows.Next() {
		var raw string
		var delivery ReleaseData
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal([]byte(raw), &delivery)
		}
		if err != nil {
			break
		}
		if !delivery.Archived {
			deliveries = append(deliveries, delivery)
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	if len(deliveries) == 0 {
		return nil
	}
	allPublished, allScheduled := true, true
	for _, delivery := range deliveries {
		allPublished = allPublished && delivery.Status == "published"
		allScheduled = allScheduled && (delivery.Status == "scheduled" || delivery.Status == "published")
	}
	status := ""
	if allPublished {
		status = "published"
	} else if allScheduled && parent.Status != "published" {
		status = "scheduled"
	}
	if status == "" || status == parent.Status {
		return nil
	}
	settings, err := getSettings(ctx.AppDB(), pid)
	if err != nil || !has(settings.Statuses, status) {
		return err
	}
	saved, err := saveItem(ctx.AppDB(), pid, parent.ID, parent.Revision, map[string]any{"status": status})
	if err == nil {
		a.emitItem(ctx, "content.updated", saved)
	}
	return err
}

func (a *App) runSocialSync(runCtx context.Context, ctx *sdk.AppCtx) error {
	if strings.TrimSpace(ctx.CurrentProject()) == "" || boundApp(ctx, "social") != nil {
		return nil
	}
	rows, err := ctx.AppDB().Query(`SELECT r.id,r.item_id,r.revision,r.data,r.created_at,r.updated_at
 FROM editorial_releases r JOIN editorial_items i ON i.project_id=r.project_id AND i.id=r.item_id
 WHERE r.project_id=? AND json_extract(r.data,'$.app')='social'
 AND COALESCE(json_extract(r.data,'$.archived'),0)=0 AND COALESCE(json_extract(i.data,'$.archived'),0)=0
 AND json_extract(r.data,'$.status')<>'cancelled' ORDER BY r.id`, ctx.CurrentProject())
	if err != nil {
		return err
	}
	var releases []Release
	wanted := map[int64]bool{}
	for rows.Next() {
		var r Release
		var raw string
		if err = rows.Scan(&r.ID, &r.ItemID, &r.Revision, &raw, &r.CreatedAt, &r.UpdatedAt); err == nil {
			err = json.Unmarshal([]byte(raw), &r.ReleaseData)
		}
		if err != nil {
			break
		}
		releases = append(releases, r)
		wanted[r.ExternalID] = true
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil || len(releases) == 0 {
		return rowErr
	}
	posts, err := fetchSocialPosts(ctx, wanted)
	if err != nil {
		return err
	}
	var failures []error
	for _, r := range releases {
		if err := runCtx.Err(); err != nil {
			return err
		}
		post := posts[r.ExternalID]
		if post == nil {
			continue
		}
		a.writes.Lock()
		saved, changed, syncErr := a.syncSocialRelease(ctx, r, post)
		if syncErr == nil {
			if changed {
				a.emitRelease(ctx, "release.refreshed", saved)
			}
			syncErr = a.syncSocialParent(ctx, r.ItemID)
		}
		a.writes.Unlock()
		if syncErr != nil {
			failures = append(failures, fmt.Errorf("Social release %d: %w", r.ID, syncErr))
		}
	}
	return errors.Join(failures...)
}

func (a *App) runMaintenance(runCtx context.Context, ctx *sdk.AppCtx) error {
	syncErr := a.runSocialSync(runCtx, ctx)
	dueErr := a.runDueScanner(runCtx, ctx)
	return errors.Join(syncErr, dueErr)
}
