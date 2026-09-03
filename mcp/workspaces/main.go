// Workspaces is the product control plane for durable development
// environments. Containers owns runtime mechanics; this app owns identity,
// assignment, TTL, command history, safety context, and operator UX.
package main

import (
	"context"
	_ "embed"
	"errors"
	"os"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML string

type App struct{}

var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	manifest, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *manifest
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("workspaces requires a db block")
	}
	if ctx.PlatformAPI() == nil {
		return errors.New("workspaces requires platform app calls")
	}
	globalCtx = ctx
	ctx.Logger().Info("workspaces mounted", "version", "0.1.0", "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{Name: "workspace-reconcile", Schedule: "@every 5s", Run: a.reconcile},
		{Name: "workspace-expiry", Schedule: "@every 1m", Run: a.expire},
	}
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/api/workspaces", Handler: a.handleWorkspaces},
		{Pattern: "/api/workspaces/", Handler: a.handleWorkspaceItem},
		{Pattern: "/api/profiles", Handler: a.handleProfiles},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	workspaceID := map[string]any{"workspace_id": strSchema()}
	commandID := map[string]any{"workspace_id": strSchema(), "command_id": strSchema()}
	createProps := map[string]any{
		"name": strSchema(), "purpose": strSchema(),
		"profile":     map[string]any{"type": "string", "enum": []string{"go", "bun", "python", "apteva"}},
		"ttl_minutes": intSchema(), "owner_label": strSchema(), "repo_label": strSchema(),
		"branch_label": strSchema(), "origin_label": strSchema(), "origin_href": strSchema(),
	}
	appCreateProps := make(map[string]any, len(createProps)+6)
	for key, value := range createProps {
		appCreateProps[key] = value
	}
	appCreateProps["owner_agent_id"] = intSchema()
	appCreateProps["owner_thread_id"] = strSchema()
	appCreateProps["resource_kind"] = strSchema()
	appCreateProps["resource_id"] = strSchema()
	appCreateProps["source_archive_base64"] = strSchema()
	return []sdk.Tool{
		{Name: "workspaces_create", Description: "Create a local workspace from an approved profile.", InputSchema: schemaObject(createProps, []string{"name"}), HandlerCtx: a.toolCreate},
		{Name: "workspaces_list", Description: "List accessible workspaces in the current project.", InputSchema: schemaObject(map[string]any{"status": strSchema(), "include_destroyed": boolSchema(), "limit": intSchema()}, nil), HandlerCtx: a.toolList},
		{Name: "workspaces_get", Description: "Fetch a workspace, runtime state, commands, usage, and activity.", InputSchema: schemaObject(workspaceID, []string{"workspace_id"}), HandlerCtx: a.toolGet},
		{Name: "workspace_command_start", Description: "Start one non-interactive asynchronous command inside the persistent workspace container.", InputSchema: schemaObject(map[string]any{
			"workspace_id": strSchema(), "argv": map[string]any{"type": "array", "items": strSchema(), "maxItems": 256},
			"shell_command": strSchema(), "working_directory": strSchema(), "timeout_s": intSchema(),
		}, []string{"workspace_id"}), HandlerCtx: a.toolCommandStart},
		{Name: "workspace_command_get", Description: "Refresh and fetch one workspace command.", InputSchema: schemaObject(commandID, []string{"workspace_id", "command_id"}), HandlerCtx: a.toolCommandGet},
		{Name: "workspace_command_logs", Description: "Tail bounded logs for one workspace command.", InputSchema: schemaObject(map[string]any{"workspace_id": strSchema(), "command_id": strSchema(), "tail": intSchema()}, []string{"workspace_id", "command_id"}), HandlerCtx: a.toolCommandLogs},
		{Name: "workspace_command_cancel", Description: "Cancel one queued or running workspace command.", InputSchema: schemaObject(commandID, []string{"workspace_id", "command_id"}), HandlerCtx: a.toolCommandCancel},
		{Name: "workspace_stop", Description: "Cancel active work and stop a workspace while preserving volumes.", InputSchema: schemaObject(workspaceID, []string{"workspace_id"}), HandlerCtx: a.toolStop},
		{Name: "workspace_resume", Description: "Resume a stopped workspace whose TTL is active.", InputSchema: schemaObject(workspaceID, []string{"workspace_id"}), HandlerCtx: a.toolResume},
		{Name: "workspace_extend", Description: "Set a new workspace TTL relative to now.", InputSchema: schemaObject(map[string]any{"workspace_id": strSchema(), "ttl_minutes": intSchema()}, []string{"workspace_id", "ttl_minutes"}), HandlerCtx: a.toolExtend},
		{Name: "workspace_destroy", Description: "Permanently destroy a workspace and its volumes after explicit confirmation.", InputSchema: schemaObject(map[string]any{"workspace_id": strSchema(), "confirm": boolSchema()}, []string{"workspace_id", "confirm"}), HandlerCtx: a.toolDestroy},
		{Name: "workspace_create_for_resource", Description: "Create a workspace for a caller-owned resource with an optional source archive.", InputSchema: schemaObject(appCreateProps, []string{"name"}), Exposure: sdk.ToolExposureAppOnly, HandlerCtx: a.toolCreateForResource},
		{Name: "workspace_context_update", Description: "Update origin navigation and consumer-reported Git safety context.", InputSchema: schemaObject(map[string]any{
			"workspace_id": strSchema(), "repo_label": strSchema(), "branch_label": strSchema(),
			"origin_label": strSchema(), "origin_href": strSchema(), "dirty_state": strSchema(), "unpushed_state": strSchema(),
		}, []string{"workspace_id"}), Exposure: sdk.ToolExposureAppOnly, HandlerCtx: a.toolContextUpdate},
		{Name: "workspace_source_export", Description: "Export a bounded tar.gz snapshot of /workspace for the originating app.", InputSchema: schemaObject(map[string]any{"workspace_id": strSchema(), "path": strSchema()}, []string{"workspace_id"}), Exposure: sdk.ToolExposureAppOnly, HandlerCtx: a.toolSourceExport},
	}
}

func main() { sdk.Run(&App{}) }

func (a *App) toolCreate(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	w, err := a.createWorkspace(callCtx, app, args, false)
	if err != nil {
		return nil, err
	}
	return map[string]any{"workspace": w}, nil
}

func (a *App) toolCreateForResource(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	w, err := a.createWorkspace(callCtx, app, args, true)
	if err != nil {
		return nil, err
	}
	return map[string]any{"workspace": w}, nil
}

func (a *App) toolList(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, err := actorFrom(callCtx, app)
	if err != nil {
		return nil, err
	}
	rows, err := listWorkspaces(app.AppDB(), actor.ProjectID, strArg(args, "status"), boolArg(args, "include_destroyed"), intArg(args, "limit", 100))
	if err != nil {
		return nil, err
	}
	filtered := make([]*Workspace, 0, len(rows))
	for _, w := range rows {
		if !canAccess(actor, w) {
			continue
		}
		active, _ := listActiveCommands(app.AppDB(), w.ID)
		if len(active) > 0 {
			w.CurrentCommand = active[0]
		}
		filtered = append(filtered, w)
	}
	return map[string]any{"workspaces": filtered, "count": len(filtered), "summary": workspaceSummary(filtered)}, nil
}

func (a *App) toolGet(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, err := actorFrom(callCtx, app)
	if err != nil {
		return nil, err
	}
	w, err := requireWorkspaceForActor(app.AppDB(), actor, strArg(args, "workspace_id"))
	if err != nil {
		return nil, err
	}
	return a.workspaceDetail(app, w)
}

func (a *App) toolCommandStart(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, err := actorFrom(callCtx, app)
	if err != nil {
		return nil, err
	}
	w, err := requireWorkspaceForActor(app.AppDB(), actor, strArg(args, "workspace_id"))
	if err != nil {
		return nil, err
	}
	c, err := a.startCommand(callCtx, app, args, actor, w)
	if err != nil {
		return nil, err
	}
	return map[string]any{"command": c, "workspace_id": w.ID}, nil
}

func (a *App) toolCommandGet(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, w, c, err := commandContext(callCtx, app, args)
	_ = actor
	if err != nil {
		return nil, err
	}
	c, err = a.refreshCommand(app, c)
	if err != nil {
		return nil, err
	}
	return map[string]any{"command": c, "workspace_id": w.ID}, nil
}

func (a *App) toolCommandLogs(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	_, _, c, err := commandContext(callCtx, app, args)
	if err != nil {
		return nil, err
	}
	_, _ = a.refreshCommand(app, c)
	return a.commandLogs(app, c, intArg(args, "tail", 300))
}

func (a *App) toolCommandCancel(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, w, c, err := commandContext(callCtx, app, args)
	if err != nil {
		return nil, err
	}
	c, err = a.cancelCommand(app, actor, w, c)
	if err != nil {
		return nil, err
	}
	return map[string]any{"command": c}, nil
}

func commandContext(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (Actor, *Workspace, *Command, error) {
	actor, err := actorFrom(callCtx, app)
	if err != nil {
		return Actor{}, nil, nil, err
	}
	w, err := requireWorkspaceForActor(app.AppDB(), actor, strArg(args, "workspace_id"))
	if err != nil {
		return actor, nil, nil, err
	}
	c, err := requireCommand(app.AppDB(), actor.ProjectID, w.ID, strArg(args, "command_id"))
	return actor, w, c, err
}

func (a *App) toolStop(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, err := actorFrom(callCtx, app)
	if err != nil {
		return nil, err
	}
	w, err := requireWorkspaceForActor(app.AppDB(), actor, strArg(args, "workspace_id"))
	if err != nil {
		return nil, err
	}
	w, err = a.stopWorkspace(app, actor, w, "workspace.suspended")
	if err != nil {
		return nil, err
	}
	return map[string]any{"workspace": w}, nil
}

func (a *App) toolResume(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, err := actorFrom(callCtx, app)
	if err != nil {
		return nil, err
	}
	w, err := requireWorkspaceForActor(app.AppDB(), actor, strArg(args, "workspace_id"))
	if err != nil {
		return nil, err
	}
	w, err = a.resumeWorkspace(app, actor, w)
	if err != nil {
		return nil, err
	}
	return map[string]any{"workspace": w}, nil
}

func (a *App) toolExtend(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, err := actorFrom(callCtx, app)
	if err != nil {
		return nil, err
	}
	w, err := requireWorkspaceForActor(app.AppDB(), actor, strArg(args, "workspace_id"))
	if err != nil {
		return nil, err
	}
	w, err = a.extendWorkspace(app, actor, w, intArg(args, "ttl_minutes", 0))
	if err != nil {
		return nil, err
	}
	return map[string]any{"workspace": w}, nil
}

func (a *App) toolDestroy(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	if !boolArg(args, "confirm") {
		return nil, errors.New("confirm=true is required because destruction permanently deletes workspace volumes")
	}
	actor, err := actorFrom(callCtx, app)
	if err != nil {
		return nil, err
	}
	w, err := requireWorkspaceForActor(app.AppDB(), actor, strArg(args, "workspace_id"))
	if err != nil {
		return nil, err
	}
	w, err = a.destroyWorkspace(app, actor, w)
	if err != nil {
		return nil, err
	}
	return map[string]any{"workspace": w, "destroyed": true}, nil
}

func (a *App) toolContextUpdate(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, err := actorFrom(callCtx, app)
	if err != nil {
		return nil, err
	}
	if actor.InstallID <= 0 || actor.AppName == "" {
		return nil, errors.New("authenticated app caller required")
	}
	w, err := requireWorkspaceForActor(app.AppDB(), actor, strArg(args, "workspace_id"))
	if err != nil {
		return nil, err
	}
	fields := map[string]any{"updated_at": nowUTC()}
	for _, key := range []string{"repo_label", "branch_label", "origin_label"} {
		if _, ok := args[key]; ok {
			fields[key] = strArg(args, key)
		}
	}
	if _, ok := args["origin_href"]; ok {
		href, err := normalizeOriginHref(strArg(args, "origin_href"))
		if err != nil {
			return nil, err
		}
		fields["origin_href"] = href
	}
	if _, ok := args["dirty_state"]; ok {
		state := strings.ToLower(strArg(args, "dirty_state"))
		if state != "unknown" && state != "clean" && state != "dirty" {
			return nil, errors.New("dirty_state must be unknown, clean, or dirty")
		}
		fields["dirty_state"] = state
	}
	if _, ok := args["unpushed_state"]; ok {
		state := strings.ToLower(strArg(args, "unpushed_state"))
		if state != "unknown" && state != "none" && state != "present" {
			return nil, errors.New("unpushed_state must be unknown, none, or present")
		}
		fields["unpushed_state"] = state
	}
	if err := updateWorkspace(app.AppDB(), w.ID, fields); err != nil {
		return nil, err
	}
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "workspace.context_updated", actor, "Origin context updated", nil)
	w, err = requireWorkspace(app.AppDB(), w.ProjectID, w.ID)
	return map[string]any{"workspace": w}, err
}

func (a *App) toolSourceExport(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, err := actorFrom(callCtx, app)
	if err != nil {
		return nil, err
	}
	if actor.InstallID <= 0 || actor.AppName == "" {
		return nil, errors.New("authenticated app caller required")
	}
	w, err := requireWorkspaceForActor(app.AppDB(), actor, strArg(args, "workspace_id"))
	if err != nil {
		return nil, err
	}
	rel := strings.TrimSpace(strArg(args, "path"))
	if rel == "" {
		rel = "."
	}
	if strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
		return nil, errors.New("path must be relative to /workspace and must not contain '..'")
	}
	var out map[string]any
	if err := app.PlatformAPI().CallAppResult("containers", "containers_volume_export", map[string]any{
		"workload_id": w.WorkloadID, "volume": "workspace", "path": rel,
	}, &out); err != nil {
		return nil, err
	}
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "source.exported", actor, "Workspace source archive exported", map[string]any{"path": rel})
	return out, nil
}

func (a *App) workspaceDetail(app *sdk.AppCtx, w *Workspace) (map[string]any, error) {
	runtime, runtimeErr := a.refreshWorkspace(app, w)
	w, err := requireWorkspace(app.AppDB(), w.ProjectID, w.ID)
	if err != nil {
		return nil, err
	}
	commands, err := listCommands(app.AppDB(), w.ProjectID, w.ID, 100)
	if err != nil {
		return nil, err
	}
	activity, err := listActivity(app.AppDB(), w.ProjectID, w.ID, 100)
	if err != nil {
		return nil, err
	}
	var usage usageResponse
	if w.WorkloadID != "" && w.LifecycleStatus != statusDestroyed {
		_ = app.PlatformAPI().CallAppResult("containers", "containers_usage_get", map[string]any{"workload_id": w.WorkloadID}, &usage)
		for _, metric := range usage.Metrics {
			if metric.FeatureKey == "containers.storage.bytes" && metric.Source == "docker_volume_total" {
				w.StorageBytes = metric.Quantity
			}
		}
	}
	result := map[string]any{"workspace": w, "runtime": runtime, "commands": commands, "activity": activity, "usage": usage}
	if runtimeErr != nil {
		result["runtime_error"] = runtimeErr.Error()
	}
	return result, nil
}

func workspaceSummary(rows []*Workspace) map[string]int {
	out := map[string]int{"total": len(rows), "running": 0, "busy": 0, "suspended": 0, "failed": 0, "expired": 0}
	for _, w := range rows {
		out[w.DisplayStatus]++
	}
	return out
}

func describeDestroyRisk(w *Workspace) string {
	parts := make([]string, 0, 2)
	if w.DirtyState == "dirty" {
		parts = append(parts, "the originating app reports uncommitted changes")
	} else if w.DirtyState == "unknown" {
		parts = append(parts, "uncommitted-change state is unknown")
	}
	if w.UnpushedState == "present" {
		parts = append(parts, "the originating app reports unpushed commits")
	} else if w.UnpushedState == "unknown" {
		parts = append(parts, "unpushed-commit state is unknown")
	}
	return strings.Join(parts, "; ")
}
