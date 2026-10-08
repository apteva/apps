package main

import (
	"context"
	"encoding/json"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type smartCropImageScene struct {
	Source   *MediaRow
	AtMs     int64
	RenderID int64
}

// Only a native, untransformed extract_frame shares coordinates with its parent.
// User-supplied lineage and same-size cropped/upscaled outputs are insufficient.
func nativeSmartCropImageScene(app *sdk.AppCtx, project string, row *MediaRow) *smartCropImageScene {
	if !row.IsImage {
		return nil
	}
	var id int64
	var sources, params, resolved, completed string
	err := app.AppDB().QueryRow(`SELECT id,source_file_ids,params,COALESCE(resolved_params,''),completed_at FROM renders WHERE project_id=? AND output_file_id=? AND operation='extract_frame' AND status='ok' ORDER BY id DESC LIMIT 1`, project, row.FileID).Scan(&id, &sources, &params, &resolved, &completed)
	if err != nil {
		return nil
	}
	if resolved != "" {
		params = resolved
	}
	var ids []string
	var p extractFrameParams
	if json.Unmarshal([]byte(sources), &ids) != nil || len(ids) != 1 || json.Unmarshal([]byte(params), &p) != nil || p.AtMs < 0 || p.Width != 0 || p.OutputWidth != 0 || p.CropW != 0 || p.CropH != 0 || strings.TrimSpace(p.TargetRatio) != "" {
		return nil
	}
	parent, e := getMedia(app.AppDB(), project, ids[0])
	if e != nil || parent.IsImage || !parent.HasVideo || parent.ProbeStatus != "ok" || parent.Width != row.Width || parent.Height != row.Height || parent.DurationMs <= p.AtMs {
		return nil
	}
	// An old output cannot safely borrow scene evidence after the source was reindexed.
	if parent.ProbeAt != "" && parent.ProbeAt > completed {
		return nil
	}
	return &smartCropImageScene{Source: parent, AtMs: p.AtMs, RenderID: id}
}

func refineSmartCropNativeImage(ctx context.Context, app *sdk.AppCtx, sc *storageClient, project string, row *MediaRow, sample *smartCropV2Sample, x, cropW int) (int, bool) {
	scene := nativeSmartCropImageScene(app, project, row)
	if scene == nil || sample == nil {
		return x, false
	}
	ds, e := resolveValidDerivations(ctx, sc, project, scene.Source.Derivations)
	if e != nil {
		return x, false
	}
	refs := downloadSmartCropBackgroundImages(ctx, sc, project, selectSmartCropBackgroundDerivations(ds, scene.AtMs-30000, scene.AtMs+30000, 12))
	bg, result, changed := backgroundAwareNarrowSmartCropX(sample.img, refs, x, row.Width, cropW)
	// Large isolated room features must lose to strongly concentrated scene foreground.
	supported := result.References >= 4 && result.Concentration >= 0.68 && result.RowCoverage >= 0.24
	if !supported {
		recordSmartCropFallback(ctx, "native_scene_foreground_unresolved")
		return x, false
	}
	if changed {
		x = bg
	}
	s := *sample
	s.point.X = x
	s.point.AtMs = scene.AtMs
	s.motionTracked = true
	s.sceneForeground = true
	if a := cropAudit(ctx); a != nil {
		a.SceneSourceID = scene.Source.FileID
		a.SceneAtMs = &scene.AtMs
		a.SceneRenderID = scene.RenderID
	}
	if extent, ok := supportedSmartCropSubjectExtent(s, refs, row.Width, row.Height, cropW); ok {
		recordSmartCropExtent(ctx, s, extent, cropW)
		if extent.Bounds.W > cropW && extent.Head != nil && extent.Head.Quality == 0 {
			x = clampInt(roundEven(extent.Head.CenterX-cropW/2), 0, row.Width-cropW)
		}
		x = containSmartCropSubjectExtentX(x, extent, row.Width, cropW)
		if upper := extent.UpperPose; upper != nil {
			margin := maxInt(12, cropW/16)
			if upper.W+2*margin <= cropW {
				x = clampInt(x, upper.X+upper.W+margin-cropW, upper.X-margin)
			}
		}
		if extent.Bounds.W <= cropW {
			margin := minInt(maxInt(12, cropW/16), (cropW-extent.Bounds.W)/2)
			x = clampInt(x, extent.Bounds.X+extent.Bounds.W+margin-cropW, extent.Bounds.X-margin)
		}
		if extent.Head != nil {
			x = containSmartCropFaceX(x, *extent.Head, row.Width, cropW)
		}
	}
	if sample.face != nil {
		x = containSmartCropFaceX(x, *sample.face, row.Width, cropW)
	}
	return clampInt(roundEven(x), 0, row.Width-cropW), true
}

func nativeSmartCropSceneCacheIdentity(app *sdk.AppCtx, project string, row *MediaRow) any {
	scene := nativeSmartCropImageScene(app, project, row)
	if scene == nil {
		return nil
	}
	p := scene.Source
	return []any{scene.RenderID, scene.AtMs, p.FileID, p.SourceSHA256, p.Width, p.Height, p.Rotation, p.ProbeAt, p.Derivations}
}
