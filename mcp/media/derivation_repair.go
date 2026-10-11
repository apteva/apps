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
	rows      []DerivationRow
	failed    bool
	sourceSHA string
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
	if stage.sourceSHA != "" {
		var current string
		if err := tx.QueryRow(`SELECT source_sha256 FROM media WHERE project_id=? AND file_id=?`, project, file).Scan(&current); err != nil {
			return err
		}
		if current != stage.sourceSHA {
			return fmt.Errorf("derivation_source_changed")
		}
	}
	if !stage.failed {
		if err := clearIndexDerivations(tx, project, file); err != nil {
			return err
		}
	}
	for _, d := range stage.rows {
		if d.Status == "ok" {
			id, _ := strconv.ParseInt(d.StorageFileID, 10, 64)
			err = upsertDerivation(tx, project, file, d.Kind, id, d.Width, d.Height, d.PositionMs)
			if err == nil && stage.sourceSHA != "" {
				_, err = tx.Exec(`UPDATE derivations SET source_sha256=? WHERE project_id=? AND file_id=? AND kind=? AND position_ms=?`, stage.sourceSHA, project, file, d.Kind, d.PositionMs)
			}
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
		_, err = tx.Exec(`UPDATE media SET derivation_retry_at=datetime('now','+10 minutes'), derivation_retry_reason=? WHERE project_id=? AND file_id=?`, indexGenerationRetryReason(stage), project, file)
	} else {
		_, err = tx.Exec(`UPDATE media SET derivation_retry_at=NULL, derivation_retry_reason=NULL, derivation_source_sha256=source_sha256 WHERE project_id=? AND file_id=?`, project, file)
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
		if !indexDerivationKind(d.Kind) || d.StorageFileID == "" {
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

func indexDerivationKind(kind string) bool {
	return kind == "thumbnail" || kind == "waveform" || kind == "keyframe"
}
func clearIndexDerivations(db sqlExecer, project, file string) error {
	_, err := db.Exec(`DELETE FROM derivations WHERE project_id=? AND file_id=? AND kind IN ('thumbnail','waveform','keyframe')`, project, file)
	return err
}

// Repair only legacy unclassified retries whose entire current generation is
// source-current, post-probe and still resolves to owned Storage artifacts.
func repairLegacyDerivationRetry(ctx context.Context, app *sdk.AppCtx, sc *storageClient, row *MediaRow, source StorageFile) bool {
	if source.SHA256 == "" || source.SHA256 != row.SourceSHA256 || row.ProbeStatus != "ok" {
		return false
	}
	var eligible bool
	err := app.AppDB().QueryRow(`SELECT derivation_retry_at IS NOT NULL AND derivation_retry_reason IS NULL AND NOT EXISTS(SELECT 1 FROM derivations d WHERE d.project_id=media.project_id AND d.file_id=media.file_id AND d.kind IN ('thumbnail','waveform','keyframe') AND (d.status<>'ok' OR (d.source_sha256 IS NOT NULL AND d.source_sha256<>media.source_sha256) OR julianday(d.generated_at)<julianday(media.probe_at))) FROM media WHERE project_id=? AND file_id=?`, row.ProjectID, row.FileID).Scan(&eligible)
	if err != nil || !eligible {
		return false
	}
	ds := []DerivationRow{}
	for _, d := range row.Derivations {
		if indexDerivationKind(d.Kind) {
			ds = append(ds, d)
		}
	}
	valid, err := resolveValidDerivations(ctx, sc, row.ProjectID, ds)
	if err != nil || len(valid) != len(ds) {
		return false
	}
	has := map[string]bool{}
	positions := map[int64]bool{}
	for _, d := range valid {
		if d.Status == "ok" {
			has[d.Kind] = true
			if d.Kind == "keyframe" {
				positions[d.PositionMs] = true
			}
		}
	}
	if (row.HasVideo || row.IsImage) && !has["thumbnail"] {
		return false
	}
	if row.HasAudio && !row.HasVideo && !has["waveform"] {
		return false
	}
	if row.HasVideo && !row.IsImage && keyframesEnabled(app) {
		for _, pos := range keyframePositions(row.DurationMs, app) {
			if !positions[pos] {
				return false
			}
		}
	}
	tx, err := app.AppDB().Begin()
	if err != nil {
		return false
	}
	defer tx.Rollback()
	var count int
	if tx.QueryRow(`SELECT count(*) FROM derivations WHERE project_id=? AND file_id=? AND kind IN ('thumbnail','waveform','keyframe')`, row.ProjectID, row.FileID).Scan(&count) != nil || count != len(ds) {
		return false
	}
	for _, d := range ds {
		var n int
		if tx.QueryRow(`SELECT count(*) FROM derivations WHERE project_id=? AND file_id=? AND kind=? AND position_ms=? AND storage_file_id=? AND status='ok' AND julianday(generated_at)>=julianday(?)`, row.ProjectID, row.FileID, d.Kind, d.PositionMs, d.StorageFileID, row.ProbeAt).Scan(&n) != nil || n != 1 {
			return false
		}
	}
	result, err := tx.Exec(`UPDATE media SET derivation_retry_at=NULL,derivation_retry_reason=NULL,derivation_source_sha256=source_sha256 WHERE project_id=? AND file_id=? AND source_sha256=? AND julianday(probe_at)=julianday(?) AND derivation_retry_at IS NOT NULL AND derivation_retry_reason IS NULL`, row.ProjectID, row.FileID, source.SHA256, row.ProbeAt)
	if err != nil {
		return false
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return false
	}
	return tx.Commit() == nil
}

func indexGenerationRetryReason(stage *derivationStage) string {
	failures := []map[string]any{}
	for _, d := range stage.rows {
		if d.Status != "ok" {
			failures = append(failures, map[string]any{"kind": d.Kind, "position_ms": d.PositionMs, "reason": truncate(d.Error, 512)})
		}
	}
	raw, _ := json.Marshal(map[string]any{"code": "partial_index_generation", "failed_derivations": failures, "retry_cooldown_seconds": 600})
	return string(raw)
}
