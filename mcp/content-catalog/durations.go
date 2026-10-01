package main

import (
	"sync"

	sdk "github.com/apteva/app-sdk"
)

type sessionDurationResult struct {
	Durations   map[string]int64 `json:"durations"`
	Unavailable int              `json:"unavailable"`
	Failed      int              `json:"failed"`
}

// Read Media metadata only for video/audio files explicitly linked to this
// session. This does not scan Storage or create Catalog records.
func (a *App) sessionDurations(ctx *sdk.AppCtx, sessionID string) (sessionDurationResult, error) {
	out := sessionDurationResult{Durations: map[string]int64{}}
	pid, err := project(ctx)
	if err != nil {
		return out, err
	}
	if _, err := sessionByID(ctx.AppDB(), pid, sessionID); err != nil {
		return out, err
	}
	rows, err := ctx.AppDB().Query(`SELECT id,storage_file_id FROM assets WHERE project_id=? AND session_id=? AND (kind IN ('video','audio') OR content_type LIKE 'video/%' OR content_type LIKE 'audio/%')`, pid, sessionID)
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

	// Keep a large session from sending an unbounded burst to Media.
	workers := 4
	if len(files) < workers {
		workers = len(files)
	}
	queue := make(chan file)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range queue {
				var result struct {
					Found bool `json:"found"`
					Media struct {
						DurationMS int64 `json:"duration_ms"`
					} `json:"media"`
				}
				err := ctx.PlatformAPI().CallAppResult("media", "media_get", map[string]any{"_project_id": pid, "file_id": f.storageFileID}, &result)
				mu.Lock()
				if err != nil {
					out.Failed++
				} else if result.Found && result.Media.DurationMS > 0 {
					out.Durations[f.assetID] = result.Media.DurationMS
				} else {
					out.Unavailable++
				}
				mu.Unlock()
			}
		}()
	}
	for _, f := range files {
		queue <- f
	}
	close(queue)
	wg.Wait()
	return out, nil
}
