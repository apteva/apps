package main

import (
	sdk "github.com/apteva/app-sdk"
)

type sessionDurationResult struct {
	Durations   map[string]int64 `json:"durations"`
	Unavailable int              `json:"unavailable"`
	Failed      int              `json:"failed"`
}

// Read Media metadata only for video/audio files explicitly linked to this
// session. This does not scan Storage or create Catalog records.
func (a *App) sessionDurations(ctx *sdk.AppCtx, sessionID string, scopes ...string) (sessionDurationResult, error) {
	out := sessionDurationResult{Durations: map[string]int64{}}
	pid, err := project(ctx)
	if err != nil {
		return out, err
	}
	if _, err := sessionByID(ctx.AppDB(), pid, sessionID); err != nil {
		return out, err
	}
	scope := "active"
	if len(scopes) > 0 && scopes[0] != "" {
		scope = scopes[0]
	}
	if _, err := lifecycleScope(map[string]any{"lifecycle": scope}); err != nil {
		return out, err
	}
	rows, err := ctx.AppDB().Query(`SELECT a.id,a.storage_file_id FROM assets a JOIN sessions s ON s.id=a.session_id AND s.project_id=a.project_id WHERE a.project_id=? AND a.session_id=?`+lifecyclePredicate(scope, "a", "s")+` AND (a.kind IN ('video','audio') OR a.content_type LIKE 'video/%' OR a.content_type LIKE 'audio/%')`, pid, sessionID)
	if err != nil {
		return out, err
	}
	type file struct{ assetID, storageFileID string }
	files := []file{}
	for rows.Next() {
		var f file
		if err := rows.Scan(&f.assetID, &f.storageFileID); err != nil {
			rows.Close()
			return out, err
		}
		files = append(files, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(files) == 0 {
		return out, nil
	}
	if ctx.IntegrationFor("media") == nil {
		out.Failed = len(files)
		return out, nil
	}

	assets := make([]Asset, len(files))
	refs := make([]*Asset, len(files))
	for i, f := range files {
		assets[i] = Asset{ID: f.assetID, StorageFileID: f.storageFileID}
		refs[i] = &assets[i]
	}
	a.loadAssetMedia(ctx, refs)
	for _, asset := range assets {
		if asset.MediaError != "" {
			out.Failed++
		} else if asset.DurationMS > 0 {
			out.Durations[asset.ID] = asset.DurationMS
		} else {
			out.Unavailable++
		}
	}
	return out, nil
}
