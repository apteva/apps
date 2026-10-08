package main

import (
	"errors"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

type mediaBatchRow struct {
	DescriptionRecovery  *descriptionRecoveryState `json:"description_recovery,omitempty"`
	MetadataReady        bool                      `json:"metadata_ready"`
	FileID               string                    `json:"file_id"`
	Description          string                    `json:"description"`
	DescriptionSource    string                    `json:"description_source"`
	DescriptionUpdatedAt string                    `json:"description_updated_at"`
	ProbeStatus          string                    `json:"probe_status"`
	AudienceRating       string                    `json:"audience_rating"`
	DurationMS           int64                     `json:"duration_ms"`
}

// Only explicit IDs in the caller's project are read. This endpoint does not
// enrich from Storage, sign URLs, queue work, or copy metadata to another app.
func (a *App) toolGetBatch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	switch v := args["file_ids"].(type) {
	case []string:
		ids = v
	case []any:
		for _, value := range v {
			id, ok := value.(string)
			if !ok {
				return nil, errors.New("file_ids must contain strings")
			}
			ids = append(ids, id)
		}
	default:
		return nil, errors.New("file_ids must be an array")
	}
	if len(ids) < 1 || len(ids) > 100 {
		return nil, errors.New("file_ids must contain 1 to 100 IDs")
	}
	unique := []string{}
	seen := map[string]bool{}
	placeholders := []string{}
	values := []any{pid}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return nil, errors.New("file_ids must not contain empty IDs")
		}
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
			placeholders = append(placeholders, "?")
			values = append(values, id)
		}
	}
	rows, err := ctx.AppDB().Query(`SELECT file_id,description,description_source,COALESCE(description_updated_at,''),probe_status,audience_rating,COALESCE(duration_ms,0),source_sha256,has_audio,COALESCE((SELECT status FROM transcripts t WHERE t.project_id=media.project_id AND t.file_id=media.file_id),'') FROM media WHERE project_id=? AND file_id IN (`+strings.Join(placeholders, ",")+`)`, values...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []mediaBatchRow{}
	found := map[string]bool{}
	recoveryRows := []MediaRow{}
	for rows.Next() {
		var m mediaBatchRow
		var sha, transcript string
		var audio int
		if err := rows.Scan(&m.FileID, &m.Description, &m.DescriptionSource, &m.DescriptionUpdatedAt, &m.ProbeStatus, &m.AudienceRating, &m.DurationMS, &sha, &audio, &transcript); err != nil {
			return nil, err
		}
		m.MetadataReady = m.Description != "" && m.AudienceRating != "" && m.AudienceRating != "unrated"
		recoveryRows = append(recoveryRows, MediaRow{ProjectID: pid, FileID: m.FileID, SourceSHA256: sha, Description: m.Description, AudienceRating: m.AudienceRating, HasAudio: audio != 0, TranscriptStatus: transcript})
		items = append(items, m)
		found[m.FileID] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range items {
		items[i].DescriptionRecovery = getDescriptionRecovery(ctx.AppDB(), &recoveryRows[i])
	}
	missing := []string{}
	for _, id := range unique {
		if !found[id] {
			missing = append(missing, id)
		}
	}
	return map[string]any{"items": items, "missing_file_ids": missing}, nil
}
