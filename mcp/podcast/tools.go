package main

// tools.go — MCP tool handlers. The agent-facing surface; the panel's
// REST routes in handlers.go share the same store + integration logic.

import (
	"errors"
	"fmt"
	"os"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── shows ─────────────────────────────────────────────────────────

func (a *App) toolShowCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	show, err := dbInsertShow(ctx.AppDB(), args, projectFromArgs(args))
	if err != nil {
		return nil, err
	}
	warning := wireHostname(ctx, show.Hostname)
	return map[string]any{"show": show, "feed_url": feedURL(show), "warning": warning}, nil
}

func (a *App) toolShowUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	before, err := dbGetShow(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	show, err := dbUpdateShow(ctx.AppDB(), id, args)
	if err != nil {
		return nil, err
	}
	bustFeed(show.ID)
	var warning string
	if _, ok := args["hostname"]; ok && show.Hostname != before.Hostname {
		warning = wireHostname(ctx, show.Hostname)
		if before.Hostname != "" {
			maybeUnwireHostname(ctx, before.Hostname, show.ProjectID)
		}
	}
	return map[string]any{"show": show, "feed_url": feedURL(show), "warning": warning}, nil
}

func (a *App) toolShowGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	show, err := dbGetShow(ctx.AppDB(), int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"show": show, "feed_url": feedURL(show)}, nil
}

func (a *App) toolShowList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	shows, err := dbListShows(ctx.AppDB(), projectFromArgs(args),
		intArg(args, "limit", 100), intArg(args, "offset", 0))
	if err != nil {
		return nil, err
	}
	return map[string]any{"shows": shows, "count": len(shows)}, nil
}

func (a *App) toolShowDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	show, err := dbGetShow(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if err := dbDeleteShow(ctx.AppDB(), id); err != nil {
		return nil, err
	}
	bustFeed(id)
	maybeUnwireHostname(ctx, show.Hostname, show.ProjectID)
	return map[string]any{"removed": true}, nil
}

// ─── episodes ──────────────────────────────────────────────────────

func (a *App) toolEpisodeCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ep, err := dbInsertEpisode(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"episode": ep}, nil
}

func (a *App) toolEpisodeUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	ep, err := dbUpdateEpisode(ctx.AppDB(), id, args)
	if err != nil {
		return nil, err
	}
	bustFeed(ep.ShowID)
	return map[string]any{"episode": ep}, nil
}

func (a *App) toolEpisodeGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ep, err := dbGetEpisode(ctx.AppDB(), int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"episode": ep}, nil
}

func (a *App) toolEpisodeList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	eps, err := dbListEpisodes(ctx.AppDB(), int64Arg(args, "show_id"),
		strArg(args, "status"), intArg(args, "limit", 100), intArg(args, "offset", 0))
	if err != nil {
		return nil, err
	}
	return map[string]any{"episodes": eps, "count": len(eps)}, nil
}

func (a *App) toolEpisodeDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	ep, err := dbGetEpisode(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if err := dbDeleteEpisode(ctx.AppDB(), id); err != nil {
		return nil, err
	}
	bustFeed(ep.ShowID)
	return map[string]any{"removed": true}, nil
}

func (a *App) toolEpisodeSetAudio(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	ep, err := dbGetEpisode(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	fileID := strArg(args, "audio_file_id")
	probe, err := probeAudio(ctx, fileID)
	if err != nil {
		return nil, err
	}
	if err := dbSetEpisodeAudio(ctx.AppDB(), id, fileID, probe.URL,
		probe.Bytes, probe.DurationSeconds, probe.MimeType); err != nil {
		return nil, err
	}
	bustFeed(ep.ShowID)
	updated, err := dbGetEpisode(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"episode": updated, "warning": probe.Warning}, nil
}

func (a *App) toolEpisodePublish(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	ep, err := dbGetEpisode(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if err := assertPublishable(ep); err != nil {
		return nil, err
	}
	now := sqliteTime(time.Now())
	if err := dbSetEpisodeStatus(ctx.AppDB(), id, "published", nil, &now); err != nil {
		return nil, err
	}
	bustFeed(ep.ShowID)
	updated, _ := dbGetEpisode(ctx.AppDB(), id)
	var warning string
	if updated.DurationSeconds == 0 {
		warning = "published, but duration is unknown — re-run episode_set_audio once media has probed the file so <itunes:duration> is set"
	}
	return map[string]any{"episode": updated, "warning": warning}, nil
}

func (a *App) toolEpisodeUnpublish(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	ep, err := dbGetEpisode(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if err := dbSetEpisodeStatus(ctx.AppDB(), id, "draft", nil, nil); err != nil {
		return nil, err
	}
	bustFeed(ep.ShowID)
	updated, _ := dbGetEpisode(ctx.AppDB(), id)
	return map[string]any{"episode": updated}, nil
}

func (a *App) toolEpisodeSchedule(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	ep, err := dbGetEpisode(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	raw := strArg(args, "publish_at")
	when, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("publish_at must be RFC3339 (e.g. 2026-06-01T09:00:00Z): %w", err)
	}
	if when.Before(time.Now()) {
		return nil, errors.New("publish_at is in the past — use episode_publish to publish now")
	}
	if err := assertPublishable(ep); err != nil {
		return nil, err
	}
	at := sqliteTime(when)
	if err := dbSetEpisodeStatus(ctx.AppDB(), id, "scheduled", &at, nil); err != nil {
		return nil, err
	}
	bustFeed(ep.ShowID)
	updated, _ := dbGetEpisode(ctx.AppDB(), id)
	return map[string]any{"episode": updated}, nil
}

// ─── feed ──────────────────────────────────────────────────────────

func (a *App) toolFeedGetURL(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	show, err := dbGetShow(ctx.AppDB(), int64Arg(args, "show_id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"feed_url": feedURL(show), "show_id": show.ID, "slug": show.Slug}, nil
}

func (a *App) toolFeedValidate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	show, err := dbGetShow(ctx.AppDB(), int64Arg(args, "show_id"))
	if err != nil {
		return nil, err
	}
	issues := []string{}
	if show.Author == "" {
		issues = append(issues, "show: missing author (<itunes:author>)")
	}
	if show.OwnerEmail == "" {
		issues = append(issues, "show: missing owner_email — Apple requires it at submission")
	}
	if show.Category == "" {
		issues = append(issues, "show: missing category (<itunes:category>)")
	}
	if show.ImageFileID == "" {
		issues = append(issues, "show: missing cover art — Apple needs a 1400-3000px square image")
	}
	if show.Description == "" {
		issues = append(issues, "show: missing description")
	}
	eps, err := dbListEpisodes(ctx.AppDB(), show.ID, "", 500, 0)
	if err != nil {
		return nil, err
	}
	var published int
	for i := range eps {
		ep := &eps[i]
		if ep.Status == "published" {
			published++
		}
		label := fmt.Sprintf("episode %d (%q)", ep.ID, ep.Title)
		if ep.AudioFileID == "" {
			issues = append(issues, label+": no audio attached")
			continue
		}
		if ep.AudioBytes == 0 {
			issues = append(issues, label+": audio byte length unknown — re-run episode_set_audio")
		}
		if ep.DurationSeconds == 0 {
			issues = append(issues, label+": duration unknown — media may not have probed the file yet")
		}
	}
	if published == 0 {
		issues = append(issues, "feed has no published episodes — directories reject empty feeds")
	}
	return map[string]any{
		"ok":        len(issues) == 0,
		"feed_url":  feedURL(show),
		"published": published,
		"issues":    issues,
	}, nil
}

// ─── shared validation ─────────────────────────────────────────────

// assertPublishable rejects episodes that would produce a broken feed
// item — a missing or unprobed enclosure. Duration is allowed to be
// unknown (it's a warning, not a blocker) since media probes async.
func assertPublishable(ep *Episode) error {
	if ep.AudioFileID == "" {
		return errors.New("episode has no audio attached — call episode_set_audio first")
	}
	if ep.AudioBytes == 0 {
		return errors.New("episode audio byte length is unknown — re-run episode_set_audio (storage probe failed)")
	}
	return nil
}

// ─── arg helpers ───────────────────────────────────────────────────

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func intArg(args map[string]any, key string, def int) int {
	switch n := args[key].(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return def
}

func int64Arg(args map[string]any, key string) int64 {
	switch n := args[key].(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	}
	return 0
}

func boolArg(args map[string]any, key string) bool {
	b, _ := args[key].(bool)
	return b
}

// nullableInt returns an *int64 when the key is present and numeric,
// else nil — so the column is written NULL. Used for season/episode
// numbers, which are optional.
func nullableInt(args map[string]any, key string) any {
	if _, ok := args[key]; !ok {
		return nil
	}
	v := int64Arg(args, key)
	return v
}

// projectFromArgs resolves the owning project: the platform-injected
// APTEVA_PROJECT_ID for project-scoped installs, else an explicit
// _project_id arg (global-scope installs pass it per call).
func projectFromArgs(args map[string]any) string {
	if v := os.Getenv("APTEVA_PROJECT_ID"); v != "" {
		return v
	}
	if v, ok := args["_project_id"].(string); ok {
		return v
	}
	return ""
}
