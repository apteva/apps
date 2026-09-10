package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) autoSyncTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "repos_git_sync_configure", Description: "Enable or pause automatic commits and remote synchronization for a repository. Enabling pins the current origin-tracking branch. Debounce 10 seconds, checkpoint and fetch every 60 seconds; divergence pauses sync. Args: slug, enabled.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "enabled": map[string]any{"type": "boolean"}, "branch": map[string]any{"type": "string", "description": "Expected local branch when enabling; reject if it changed."}}, []string{"slug", "enabled"}), Handler: a.toolSyncConfigure},
		{Name: "repos_git_sync_status", Description: "Read auto-sync state, branch, last successful sync, retry time, and any required action. Times are Unix milliseconds. Args: slug.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}}, []string{"slug"}), Handler: a.toolSyncStatus},
		{Name: "repos_git_sync_now", Description: "Checkpoint saved changes and synchronize the enabled repository now. Retry after resolving a conflict or authentication issue. Never force-pushes. Args: slug.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}}, []string{"slug"}), Handler: a.toolSyncNow},
	}
}
func (a *App) toolSyncConfigure(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	_, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	enabled, ok := args["enabled"].(bool)
	if !ok {
		return nil, errors.New("enabled boolean required")
	}
	if a.syncer == nil {
		return nil, errors.New("auto-sync unavailable")
	}
	return a.syncer.configure(ctx.WithProject(repo.ProjectID), repo, enabled, strArg(args, "branch"))
}
func (a *App) toolSyncStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	_, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	return loadAutoSync(ctx.AppDB(), repo.ID)
}
func (a *App) toolSyncNow(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	_, repo, err := a.gitRepoFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	if a.syncer == nil {
		return nil, errors.New("auto-sync unavailable")
	}
	return a.syncer.tick(ctx.WithProject(repo.ProjectID), repo, time.Now(), true)
}
func (a *App) httpRepoSync(w http.ResponseWriter, r *http.Request, repo *Repo, action string) {
	if a.syncer == nil {
		httpErr(w, http.StatusServiceUnavailable, "auto-sync unavailable")
		return
	}
	ctx := globalCtx.WithProject(repo.ProjectID)
	var result *AutoSyncState
	var err error
	switch {
	case action == "sync" && r.Method == http.MethodGet:
		result, err = loadAutoSync(ctx.AppDB(), repo.ID)
	case action == "sync" && r.Method == http.MethodPatch:
		var body struct {
			Enabled *bool  `json:"enabled"`
			Branch  string `json:"branch"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Enabled == nil {
			httpErr(w, 400, "enabled boolean required")
			return
		}
		result, err = a.syncer.configure(ctx, repo, *body.Enabled, body.Branch)
	case action == "sync/now" && r.Method == http.MethodPost:
		result, err = a.syncer.tick(ctx, repo, time.Now(), true)
	default:
		httpErr(w, 405, "GET/PATCH sync or POST sync/now")
		return
	}
	if err != nil {
		writeGitHTTPError(w, err)
		return
	}
	httpJSON(w, result)
}
