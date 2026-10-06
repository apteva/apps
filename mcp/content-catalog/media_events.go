package main

import (
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Media owns indexing and editorial analysis. Catalog only caches the latest
// completion/rating for files already attached to a session. Replayed events
// are harmless, and unknown files never create inferred sessions or assets.
func (a *App) onMediaCompleted(ctx *sdk.AppCtx, event sdk.Event) error {
	pid := strings.TrimSpace(event.ProjectID)
	if pid == "" || event.Data == nil {
		return nil
	}
	bound := ctx.WithProject(pid).IntegrationFor("media")
	if bound == nil || bound.InstallID <= 0 || (event.SourceInstallID > 0 && event.SourceInstallID != bound.InstallID) {
		return nil
	}
	fileID := strings.TrimSpace(fmt.Sprint(event.Data["file_id"]))
	if fileID == "" || fileID == "<nil>" {
		return nil
	}
	rating := strings.TrimSpace(fmt.Sprint(event.Data["audience_rating"]))
	if rating == "<nil>" {
		rating = ""
	}
	_, err := ctx.AppDB().Exec(`UPDATE assets SET media_status='completed',media_rating=?,updated_at=? WHERE project_id=? AND storage_file_id=?`, rating, now(), pid, fileID)
	return err
}
