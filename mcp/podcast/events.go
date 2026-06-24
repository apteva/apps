package main

import sdk "github.com/apteva/app-sdk"

func emitShowEvent(ctx *sdk.AppCtx, topic string, show *Show) {
	if ctx == nil || show == nil {
		return
	}
	ctx.EmitWithProject(topic, show.ProjectID, map[string]any{
		"id":         show.ID,
		"slug":       show.Slug,
		"title":      show.Title,
		"project_id": show.ProjectID,
		"hostname":   show.Hostname,
	})
}

func emitEpisodeEvent(ctx *sdk.AppCtx, topic string, ep *Episode) {
	if ctx == nil || ep == nil {
		return
	}
	projectID := ""
	if show, err := dbGetShow(ctx.AppDB(), ep.ShowID); err == nil && show != nil {
		projectID = show.ProjectID
	}
	payload := map[string]any{
		"id":               ep.ID,
		"show_id":          ep.ShowID,
		"guid":             ep.GUID,
		"title":            ep.Title,
		"status":           ep.Status,
		"project_id":       projectID,
		"audio_file_id":    ep.AudioFileID,
		"audio_bytes":      ep.AudioBytes,
		"duration_seconds": ep.DurationSeconds,
		"mime_type":        ep.MimeType,
	}
	if ep.PublishAt != nil {
		payload["publish_at"] = *ep.PublishAt
	}
	if ep.PublishedAt != nil {
		payload["published_at"] = *ep.PublishedAt
	}
	ctx.EmitWithProject(topic, projectID, payload)
}
