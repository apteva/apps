package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	sdk "github.com/apteva/app-sdk"
)

var errLocalExecutionPermission = errors.New("local execution is not allowed for this repository; use Allow and run in Code, or repos_execution_configure with enabled=true and confirm=true after authorization")

type ExecutionPermission struct {
	Enabled                bool   `json:"enabled"`
	Source                 string `json:"source"`
	RequiresLocalExecution bool   `json:"requires_local_execution"`
	UpdatedBy              string `json:"updated_by,omitempty"`
	UpdatedAt              string `json:"updated_at,omitempty"`
}

func loadExecutionPermission(ctx *sdk.AppCtx, repo *Repo) (*ExecutionPermission, error) {
	p := &ExecutionPermission{Enabled: localExecutionEnabled(ctx), Source: "default"}
	if p.Enabled {
		p.Source = "installation"
	}
	err := ctx.AppDB().QueryRow(`SELECT enabled, updated_by, updated_at FROM repo_execution_permissions WHERE repo_id=?`, repo.ID).Scan(&p.Enabled, &p.UpdatedBy, &p.UpdatedAt)
	if err == nil {
		p.Source = "repository"
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return p, nil
}
func requireLocalExecution(ctx *sdk.AppCtx, repo *Repo) error {
	p, err := loadExecutionPermission(ctx, repo)
	if err != nil {
		return err
	}
	if !p.Enabled {
		return errLocalExecutionPermission
	}
	return nil
}
func (a *App) executionPermission(ctx *sdk.AppCtx, repo *Repo) (*ExecutionPermission, error) {
	p, err := loadExecutionPermission(ctx, repo)
	if err != nil {
		return nil, err
	}
	fw := detectDevFramework(a.storeFor(repo), repo.Slug)
	def := devFrameworkByName(fw)
	p.RequiresLocalExecution = fw != "static" && (def == nil || def.RemoteRunner == "")
	return p, nil
}
func (a *App) configureExecution(ctx *sdk.AppCtx, repo *Repo, enabled, confirm bool, actor string) (*ExecutionPermission, error) {
	if enabled && !confirm {
		return nil, errors.New("confirm=true is required to allow repository scripts and dependencies to run with the Code service's OS permissions")
	}
	// SELECT ties the grant to the existing project/repository identity. A deleted
	// and recreated slug must never inherit permission from the old repository.
	result, err := ctx.AppDB().Exec(`INSERT INTO repo_execution_permissions(repo_id,enabled,updated_by,updated_at)
 SELECT id,?,?,CURRENT_TIMESTAMP FROM repositories WHERE id=? AND project_id=? AND archived_at IS NULL
 ON CONFLICT(repo_id) DO UPDATE SET enabled=excluded.enabled,updated_by=excluded.updated_by,updated_at=excluded.updated_at`, enabled, actor, repo.ID, repo.ProjectID)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errors.New("local execution permission requires an active repository")
	}
	p, err := a.executionPermission(ctx, repo)
	if err == nil {
		ctx.EmitWithProject("repo.execution.updated", repo.ProjectID, map[string]any{"slug": repo.Slug, "repo_id": repo.ID, "execution_permission": p})
	}
	return p, err
}
func (a *App) executionTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "repos_execution_status", Description: "Read the saved local execution permission for this repository and whether its Run preview needs it. Args: slug.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}}, []string{"slug"}), Handler: a.toolExecutionStatus},
		{Name: "repos_execution_configure", Description: "Allow or revoke local execution for this repository. Enabling requires explicit user authorization and confirm=true: repository scripts and dependency installs run with the Code service's OS permissions. The grant persists across restarts and applies to Run previews and runtime=local commands. Revocation prevents future starts; use repos_dev_stop to stop an existing preview. Args: slug, enabled, confirm?.", InputSchema: schemaObject(map[string]any{"slug": map[string]any{"type": "string"}, "enabled": map[string]any{"type": "boolean"}, "confirm": map[string]any{"type": "boolean"}}, []string{"slug", "enabled"}), Handler: a.toolExecutionConfigure},
	}
}
func (a *App) toolExecutionStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
		return nil, err
	}
	return a.executionPermission(ctx, repo)
}
func (a *App) toolExecutionConfigure(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	repo, err := requireRepo(ctx, pid, strArg(args, "slug"))
	if err != nil {
		return nil, err
	}
	enabled, ok := args["enabled"].(bool)
	if !ok {
		return nil, errors.New("enabled boolean required")
	}
	return a.configureExecution(ctx, repo, enabled, boolArg(args, "confirm"), strArg(args, "actor"))
}
func (a *App) httpExecutionPermission(w http.ResponseWriter, r *http.Request, repo *Repo) {
	var p *ExecutionPermission
	var err error
	switch r.Method {
	case http.MethodGet:
		p, err = a.executionPermission(globalCtx, repo)
	case http.MethodPatch:
		var body struct {
			Enabled *bool `json:"enabled"`
			Confirm bool  `json:"confirm"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Enabled == nil {
			httpErr(w, 400, "enabled boolean required")
			return
		}
		p, err = a.configureExecution(globalCtx, repo, *body.Enabled, body.Confirm, httpActor(r))
	default:
		httpErr(w, 405, "GET or PATCH")
		return
	}
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, p)
}
