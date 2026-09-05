package main

import (
	"context"
	"errors"
	"time"
)

func (a *App) resumeBuildActions(ctx context.Context) {
	rows, err := globalCtx.AppDB().Query(`SELECT id FROM builds WHERE (status='succeeded' AND release_requested=1) OR (status='cancelled' AND external_status='cancel_pending' AND external_job_id!='') ORDER BY id LIMIT 100`)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		b, err := dbGetBuild(globalCtx.AppDB(), id)
		if err != nil {
			continue
		}
		if b.Status == "cancelled" {
			_ = a.finishCloudCancellation(ctx, b)
			continue
		}
		d, err := a.deploymentForBuild(b)
		if err == nil {
			_ = a.runRequestedCloudRelease(d, b)
		}
	}
}
func (a *App) registerLocalBuild(id int64) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(atoiOr(configOr(globalCtx, "build_timeout_seconds", "1800"), 1800))*time.Second)
	a.localBuildMu.Lock()
	if a.localBuilds == nil {
		a.localBuilds = map[int64]context.CancelFunc{}
	}
	a.localBuilds[id] = cancel
	a.localBuildMu.Unlock()
	return ctx, func() { cancel(); a.localBuildMu.Lock(); delete(a.localBuilds, id); a.localBuildMu.Unlock() }
}
func (a *App) cancelLocalBuild(build *Build) (*Build, error) {
	if err := dbUpdateBuild(globalCtx.AppDB(), build.ID, map[string]any{"status": "cancelled", "release_requested": false, "finished_at": nowUTC(), "error": "cancelled by operator"}); err != nil {
		return dbGetBuild(globalCtx.AppDB(), build.ID)
	}
	a.localBuildMu.Lock()
	cancel := a.localBuilds[build.ID]
	a.localBuildMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return dbGetBuild(globalCtx.AppDB(), build.ID)
}
func (a *App) cancelDeploymentBuilds(d *Deployment) {
	builds, err := dbListBuildsForEnv(globalCtx.AppDB(), d.ID, d.EnvironmentID, 100000)
	if err != nil {
		return
	}
	for i := range builds {
		b := &builds[i]
		if b.Status == "pending" || b.Status == "running" {
			_, _ = a.cancelCloudBuild(context.Background(), b)
		}
	}
}
func activeBuild(id int64) bool {
	b, err := dbGetBuild(globalCtx.AppDB(), id)
	return err == nil && b != nil && (b.Status == "pending" || b.Status == "running")
}

var errBuildCancelled = errors.New("build cancelled or interrupted")
