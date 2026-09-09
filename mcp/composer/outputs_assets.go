package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// All asset references, including AI reference images, are checked in the owning
// project before paid generation. Storage remains the authority for asset access.
func validateOutputAssets(ctx *sdk.AppCtx, edit *Edit) error {
	seen := map[string]bool{}
	check := func(src string) error {
		if src == "" || seen[src] {
			return nil
		}
		seen[src] = true
		_, err := resolveAssetURL(ctx, src)
		return err
	}
	for _, t := range edit.Timeline.Tracks {
		for _, c := range t.Clips {
			if err := check(c.Asset.Src); err != nil {
				return err
			}
			if c.AI != nil {
				for _, src := range aiSourceImages(c.AI) {
					if err := check(src); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func prepareOutputAssets(ctx *sdk.AppCtx, edit *Edit, cid int64, renderID int64) ([]string, error) {
	pending := []string{}
	for ti := range edit.Timeline.Tracks {
		t := &edit.Timeline.Tracks[ti]
		for ci := range t.Clips {
			c := &t.Clips[ci]
			if c.AI == nil || c.Asset.Src != "" {
				continue
			}
			if renderID > 0 {
				var status string
				if err := ctx.AppDB().QueryRow(`SELECT status FROM renders WHERE id=?`, renderID).Scan(&status); err != nil {
					return pending, err
				}
				if status == "cancelled" {
					return pending, context.Canceled
				}
			}
			p, err := prepareOutputAsset(ctx, c.AI, cid, planTTSContinuity(t, ci), renderID)
			if err != nil {
				return pending, fmt.Errorf("%s: %w", c.UID, err)
			}
			if p != "" {
				pending = append(pending, p)
				continue
			}
			c.Asset.Src = fmt.Sprintf("storage:%d", c.AI.StorageID)
			c.Asset.Type = assetTypeForAI(c.AI.MediaKind)
		}
	}
	return pending, nil
}

func prepareOutputAsset(ctx *sdk.AppCtx, ai *AIAsset, cid int64, plan ttsContinuityPlan, renderIDs ...int64) (string, error) {
	if ai.StorageID > 0 {
		ai.Status = "ready"
		return "", nil
	}
	pid := ctx.CurrentProject()
	// A custom cache key is an explicit generation version; export formats,
	// excerpt lengths and output ids never enter the shared asset identity.
	version := ai.CacheKey
	if strings.HasPrefix(version, "composer:") {
		version = ""
	}
	key := outputHash([]string{aiCacheKeyWithOptions(ai, plan.CacheOptions), version})
	var renderID int64
	if len(renderIDs) > 0 {
		renderID = renderIDs[0]
	}
	claim, err := ctx.AppDB().Exec(`INSERT INTO output_asset_jobs(project_id,composition_id,cache_key,state,asset_json,media_kind,first_render_id) VALUES(?,?,?,'submitting',?,?,?) ON CONFLICT DO NOTHING`, pid, cid, key, outputJSON(ai), ai.MediaKind, renderID)
	if err != nil {
		return "", err
	}
	n, _ := claim.RowsAffected()
	if n == 1 && ai.JobID > 0 {
		if _, err = ctx.AppDB().Exec(`UPDATE output_asset_jobs SET state='waiting' WHERE project_id=? AND composition_id=? AND cache_key=?`, pid, cid, key); err != nil {
			return "", err
		}
		n = 0
	}
	if n == 1 {
		// A caller-selected asset version explicitly requests regeneration. The
		// canonical materializer normalizes cache keys, so bypass Media Studio's
		// previous cached generation for this one durably claimed submission.
		if version != "" {
			ai.CachePolicy = "refresh"
		}
		// Commit the claim BEFORE calling the provider. An interrupted/ambiguous
		// submission is never automatically repeated, even after process restart.
		_, pending, generateErr := materializeOneAIAsset(ctx, ai, "output asset", pid, plan.ProviderOptions, plan.CacheOptions)
		state := "ready"
		msg := ""
		if pending != "" {
			state = "waiting"
		}
		if generateErr != nil {
			state = "blocked"
			msg = redactSecrets(generateErr.Error())
		}
		ai.Error = redactSecrets(ai.Error)
		_, err = ctx.AppDB().Exec(`UPDATE output_asset_jobs SET state=?,asset_json=?,error=?,cost_usd=? WHERE project_id=? AND composition_id=? AND cache_key=?`, state, outputJSON(ai), msg, ai.RecordedCostUSD, pid, cid, key)
		if err != nil {
			return "", err
		}
		if generateErr != nil {
			return "", generateErr
		}
		return pending, nil
	}
	var state, raw, msg string
	if err = ctx.AppDB().QueryRow(`SELECT state,asset_json,error FROM output_asset_jobs WHERE project_id=? AND composition_id=? AND cache_key=?`, pid, cid, key).Scan(&state, &raw, &msg); err != nil {
		return "", err
	}
	if err = json.Unmarshal([]byte(raw), ai); err != nil {
		return "", err
	}
	switch state {
	case "ready":
		return "", nil
	case "blocked":
		return "", fmt.Errorf("previous generation failed or its result is uncertain: %s; inspect the provider job, or explicitly regenerate this asset with a new cache_key", msg)
	case "submitting":
		return "asset submission in progress or awaiting reconciliation; no duplicate request will be sent", nil
	case "waiting":
		var job struct {
			Status       string   `json:"status"`
			Error        string   `json:"error"`
			StorageID    int64    `json:"result_storage_id"`
			GenerationID int64    `json:"generation_id"`
			Cost         *float64 `json:"cost_usd"`
		}
		if err = ctx.PlatformAPI().CallAppResult("media-studio", "media_job_get", map[string]any{"job_id": ai.JobID, "_project_id": pid}, &job); err != nil {
			return "", err
		}
		if job.Status == "failed" || job.Status == "cancelled" {
			job.Error = redactSecrets(job.Error)
			_, err = ctx.AppDB().Exec(`UPDATE output_asset_jobs SET state='blocked',error=? WHERE project_id=? AND composition_id=? AND cache_key=?`, job.Error, pid, cid, key)
			if err != nil {
				return "", err
			}
			return "", fmt.Errorf("generation job %d failed: %s", ai.JobID, job.Error)
		}
		if job.Status != "complete" {
			return fmt.Sprintf("media-studio job #%d", ai.JobID), nil
		}
		if job.StorageID <= 0 {
			return "", errors.New("completed generation has no Storage artifact")
		}
		ai.StorageID = job.StorageID
		ai.GenerationID = job.GenerationID
		ai.Status = "ready"
		ai.Error = ""
		_, err = ctx.AppDB().Exec(`UPDATE output_asset_jobs SET state='ready',asset_json=?,cost_usd=? WHERE project_id=? AND composition_id=? AND cache_key=?`, outputJSON(ai), job.Cost, pid, cid, key)
		return "", err
	}
	return "", errors.New("invalid asset job state")
}
