package main

import (
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Storage's checksum-ready event is the fast path for attached assets. The
// update is conditional on the exact project/file link, so replayed events
// and events from another Storage install are harmless.
func (a *App) onStorageChecksumReady(ctx *sdk.AppCtx, event sdk.Event) error {
	pid := strings.TrimSpace(event.ProjectID)
	if pid == "" || event.Data == nil {
		return nil
	}
	bound := ctx.WithProject(pid).IntegrationFor("storage")
	sourceInstall := event.SourceInstallID
	if sourceInstall == 0 {
		sourceInstall, _ = strconv.ParseInt(strings.TrimSpace(fmt.Sprint(event.Data["install_id"])), 10, 64)
	}
	if bound == nil || bound.InstallID <= 0 || (sourceInstall > 0 && sourceInstall != bound.InstallID) {
		return nil
	}
	fileID := strings.TrimSpace(fmt.Sprint(event.Data["file_id"]))
	sha := strings.TrimSpace(fmt.Sprint(event.Data["sha256"]))
	if fileID == "" || fileID == "<nil>" || sha == "" || sha == "<nil>" {
		return nil
	}
	_, err := ctx.AppDB().Exec(`UPDATE assets SET sha256=?,revision=revision+1,updated_at=? WHERE project_id=? AND storage_install_id=? AND storage_file_id=? AND sha256<>?`, sha, now(), pid, bound.InstallID, fileID, sha)
	if err != nil {
		return err
	}
	return a.resumeChecksumIntents(ctx, pid, fileID)
}
