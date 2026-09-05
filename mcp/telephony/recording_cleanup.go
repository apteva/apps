package main

import (
	"database/sql"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"time"
)

func (a *App) queueOrphanRecordingFile(ctx *sdk.AppCtx, id int64) {
	if id <= 0 {
		return
	}
	if _, err := ctx.AppDB().Exec(`INSERT OR IGNORE INTO recording_orphan_files(project_id,file_id) VALUES(?,?)`, ctx.CurrentProject(), id); err != nil {
		ctx.Logger().Error("persist orphan recording cleanup", "file_id", id, "err", err)
	}
}

func (a *App) cleanupRecordingJobs(ctx *sdk.AppCtx) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var fileID int64
	err := ctx.AppDB().QueryRow(`SELECT file_id FROM recording_orphan_files WHERE project_id=? AND next_attempt_at<=? ORDER BY next_attempt_at LIMIT 1`, ctx.CurrentProject(), now).Scan(&fileID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if fileID > 0 {
		_, _ = ctx.AppDB().Exec(`UPDATE recording_orphan_files SET next_attempt_at=? WHERE project_id=? AND file_id=?`, time.Now().UTC().Add(time.Minute).Format(time.RFC3339), ctx.CurrentProject(), fileID)
		var result map[string]any
		err := ctx.PlatformAPI().CallAppResult("storage", "files_delete", map[string]any{"id": fileID}, &result)
		if err == nil || strings.Contains(err.Error(), "404") || strings.Contains(strings.ToLower(err.Error()), "not found") {
			_, _ = ctx.AppDB().Exec(`DELETE FROM recording_orphan_files WHERE project_id=? AND file_id=?`, ctx.CurrentProject(), fileID)
		}
	}
	row, err := scanRecording(ctx.AppDB().QueryRow(`SELECT `+recordingSelectColumns+` FROM recordings WHERE project_id=? AND deleted_at='' AND storage_status='stored' AND provider_deleted_at='' AND cleanup_next_at<=? AND call_id IN (SELECT id FROM calls WHERE recording_storage_mode='copy_then_delete_provider') ORDER BY cleanup_next_at LIMIT 1`, ctx.CurrentProject(), now))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, _ = ctx.AppDB().Exec(`UPDATE recordings SET cleanup_next_at=? WHERE id=?`, time.Now().UTC().Add(time.Minute).Format(time.RFC3339), row.ID)
	return a.deleteProviderRecording(ctx, row)
}
