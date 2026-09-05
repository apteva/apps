package main

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strconv"
)

type derivationStageKey struct{}
type derivationStage struct {
	rows   []DerivationRow
	failed bool
}

func stageDerivationFailure(ctx context.Context, app *sdk.AppCtx, project, file, kind string, pos int64, cause error) error {
	if stage, ok := ctx.Value(derivationStageKey{}).(*derivationStage); ok {
		stage.failed = true
		stage.rows = append(stage.rows, DerivationRow{FileID: file, Kind: kind, PositionMs: pos, Status: "failed", Error: cause.Error()})
		return nil
	}
	return upsertDerivationFailed(app.AppDB(), project, file, kind, pos, cause)
}
func commitDerivationStage(ctx context.Context, app *sdk.AppCtx, sc *storageClient, project, file string, stage *derivationStage, previous []DerivationRow) error {
	tx, err := app.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !stage.failed {
		if err := clearDerivations(tx, project, file); err != nil {
			return err
		}
	}
	for _, d := range stage.rows {
		if d.Status == "ok" {
			id, _ := strconv.ParseInt(d.StorageFileID, 10, 64)
			err = upsertDerivation(tx, project, file, d.Kind, id, d.Width, d.Height, d.PositionMs)
		} else {
			// Failed replacement keeps the last working pointer available.
			keep := false
			for _, old := range previous {
				if old.Kind == d.Kind && old.PositionMs == d.PositionMs && old.Status == "ok" {
					keep = true
					break
				}
			}
			if !keep {
				err = upsertDerivationFailed(tx, project, file, d.Kind, d.PositionMs, fmt.Errorf("%s", d.Error))
			}
		}
		if err != nil {
			return err
		}
	}
	if stage.failed {
		_, err = tx.Exec(`UPDATE media SET derivation_retry_at=datetime('now','+10 minutes') WHERE project_id=? AND file_id=?`, project, file)
	} else {
		_, err = tx.Exec(`UPDATE media SET derivation_retry_at=NULL WHERE project_id=? AND file_id=?`, project, file)
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	current, err := listDerivations(app.AppDB(), project, file)
	if err != nil {
		return err
	}
	keep := map[string]struct{}{}
	for _, d := range current {
		keep[d.StorageFileID] = struct{}{}
	}
	deleteObsoleteDerivationFiles(ctx, app, sc, project, previous, keep)
	return nil
}
func mediaNeedsDerivationRepair(app *sdk.AppCtx, row *MediaRow) bool {
	var retry string
	_ = app.AppDB().QueryRow(`SELECT COALESCE(derivation_retry_at,'') FROM media WHERE project_id=? AND file_id=?`, row.ProjectID, row.FileID).Scan(&retry)
	if retry != "" {
		var due bool
		_ = app.AppDB().QueryRow(`SELECT julianday(?) <= julianday('now')`, retry).Scan(&due)
		return due
	}
	has := map[string]bool{}
	for _, d := range row.Derivations {
		if d.Status == "ok" && d.StorageFileID != "" {
			has[d.Kind] = true
		}
	}
	if (row.HasVideo || row.IsImage) && !has["thumbnail"] {
		return true
	}
	if row.HasAudio && !row.HasVideo && !has["waveform"] {
		return true
	}
	return row.HasVideo && !row.IsImage && row.DurationMs > 0 && keyframesEnabled(app) && !has["keyframe"]
}
func enqueueDerivationCleanup(app *sdk.AppCtx, project string, derivs []DerivationRow) {
	if app == nil {
		return
	}
	for _, d := range derivs {
		if d.StorageFileID == "" {
			continue
		}
		raw, _ := json.Marshal(d)
		_, _ = app.AppDB().Exec(`INSERT OR IGNORE INTO derivation_cleanup_queue VALUES(?,?,?)`, project, d.StorageFileID, string(raw))
	}
}
func retryDerivationCleanup(ctx context.Context, app *sdk.AppCtx, sc *storageClient, project string) {
	rows, err := app.AppDB().Query(`SELECT derivation FROM derivation_cleanup_queue WHERE project_id=? LIMIT 50`, project)
	if err != nil {
		return
	}
	var ds []DerivationRow
	for rows.Next() {
		var raw string
		if rows.Scan(&raw) == nil {
			var d DerivationRow
			if json.Unmarshal([]byte(raw), &d) == nil {
				ds = append(ds, d)
			}
		}
	}
	rows.Close()
	deleteOwnedDerivationFiles(ctx, app, sc, project, ds)
}
