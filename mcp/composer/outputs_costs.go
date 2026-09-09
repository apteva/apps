package main

import (
	"database/sql"
	sdk "github.com/apteva/app-sdk"
)

// One row per generated dependency prevents the shared song being counted once
// per export. Zero/missing provider quotes remain unknown, not free generation.
func outputCosts(ctx *sdk.AppCtx, cid int64) (map[string]any, error) {
	rows, err := ctx.AppDB().Query(`SELECT media_kind,cost_usd FROM output_asset_jobs WHERE composition_id=?`, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	shared, visual := 0.0, 0.0
	sharedUnknown, visualUnknown := false, false
	for rows.Next() {
		var kind string
		var cost sql.NullFloat64
		if err = rows.Scan(&kind, &cost); err != nil {
			return nil, err
		}
		if assetTypeForAI(kind) == "audio" {
			if cost.Valid {
				shared += cost.Float64
			} else {
				sharedUnknown = true
			}
		} else {
			if cost.Valid {
				visual += cost.Float64
			} else {
				visualUnknown = true
			}
		}
	}
	out := map[string]any{"shared_audio_usd": shared, "visual_generation_usd": visual}
	if sharedUnknown {
		out["shared_audio_usd"] = nil
	}
	if visualUnknown {
		out["visual_generation_usd"] = nil
	}
	return out, rows.Err()
}
func (a *App) toolOutputEstimate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	row, err := outputComposition(ctx, args)
	if err != nil {
		return nil, err
	}
	if err = ensureOutputs(ctx, row); err != nil {
		return nil, err
	}
	id := row["id"].(int64)
	o, err := loadOutput(ctx, id, strArg(args, "kind", ""))
	if err != nil {
		return nil, err
	}
	master, _, err := loadSharedMaster(ctx, id)
	if err != nil {
		return nil, err
	}
	snap, err := buildOutputSnapshot(row, o.Kind, o.Settings, string(o.Plan), master)
	if err != nil {
		return nil, err
	}
	needed := map[string]int{}
	seen := map[string]bool{}
	if snap.Edit != nil {
		for _, t := range snap.Edit.Timeline.Tracks {
			for i, c := range t.Clips {
				if c.AI == nil || c.AI.StorageID > 0 || c.Asset.Src != "" {
					continue
				}
				ai := c.AI
				version := ai.CacheKey
				if len(version) >= 9 && version[:9] == "composer:" {
					version = ""
				}
				key := outputHash([]string{aiCacheKeyWithOptions(ai, planTTSContinuity(&t, i).CacheOptions), version})
				if seen[key] {
					continue
				}
				seen[key] = true
				var state string
				err = ctx.AppDB().QueryRow(`SELECT state FROM output_asset_jobs WHERE project_id=? AND composition_id=? AND cache_key=?`, row["project_id"], id, key).Scan(&state)
				if err != nil && err != sql.ErrNoRows {
					return nil, err
				}
				if err == sql.ErrNoRows {
					needed[ai.MediaKind]++
				}
			}
		}
	}
	var generation any = 0.0
	if len(needed) > 0 {
		generation = nil
	}
	return map[string]any{"composition_id": id, "output_id": o.ID, "output_revision": o.Revision, "missing_dependencies": needed, "additional_generation_cost_usd": generation, "render_cost_usd": nil, "message": "Provider quotes are not available for all models. Unknown costs are null; existing shared assets are reused."}, nil
}
