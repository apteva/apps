package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func requireWorkspaceForActor(db *sql.DB, actor Actor, id string) (*Workspace, error) {
	w, err := requireWorkspace(db, actor.ProjectID, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if !canAccess(actor, w) {
		return nil, errors.New("workspace not found")
	}
	return w, nil
}

func (a *App) createWorkspace(callCtx context.Context, app *sdk.AppCtx, args map[string]any, allowArchive bool) (*Workspace, error) {
	actor, err := actorFrom(callCtx, app)
	if err != nil {
		return nil, err
	}
	if allowArchive && (actor.InstallID <= 0 || actor.AppName == "") {
		return nil, errors.New("authenticated app caller required")
	}
	name, err := normalizeWorkspaceName(strArg(args, "name"))
	if err != nil {
		return nil, err
	}
	profile, image, err := resolveProfile(app, strArg(args, "profile"))
	if err != nil {
		return nil, err
	}
	maxTTL := configInt(app, "max_ttl_minutes", 480)
	ttl := intArg(args, "ttl_minutes", configInt(app, "default_ttl_minutes", 120))
	if ttl < 1 || ttl > maxTTL {
		return nil, fmt.Errorf("ttl_minutes must be between 1 and %d", maxTTL)
	}
	originHref, err := normalizeOriginHref(strArg(args, "origin_href"))
	if err != nil {
		return nil, err
	}
	sourceArchive := strArg(args, "source_archive_base64")
	if sourceArchive != "" && !allowArchive {
		return nil, errors.New("source archives are accepted only from authenticated app callers")
	}
	idempotencyKey := callerIdempotencyKey(callCtx, actor)
	if idempotencyKey != "" {
		existing, err := getWorkspaceByIdempotency(app.AppDB(), actor.ProjectID, idempotencyKey)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
	}
	now := time.Now().UTC()
	expires := now.Add(time.Duration(ttl) * time.Minute)
	deleteAt := expires.Add(time.Duration(configInt(app, "expired_retention_minutes", 1440)) * time.Minute)
	consumerApp := "workspaces"
	consumerInstallID := int64(0)
	ownerAgentID := actor.AgentID
	ownerThreadID := actor.ThreadID
	if actor.InstallID > 0 {
		consumerApp = actor.AppName
		consumerInstallID = actor.InstallID
		ownerAgentID = int64Arg(args, "owner_agent_id")
		ownerThreadID = strArg(args, "owner_thread_id")
	}
	ownerLabel := firstNonEmpty(strArg(args, "owner_label"), actor.Label)
	w := &Workspace{
		ID: newID("wsp"), ProjectID: actor.ProjectID, Name: name,
		Purpose: strArg(args, "purpose"), Profile: profile, Image: image,
		LifecycleStatus: statusProvisioning, ActivityStatus: activityIdle,
		HostLabel: "Local Docker", NetworkPolicy: "isolated-egress",
		CPU: configFloat(app, "default_cpu", 2), MemoryMB: configInt(app, "default_memory_mb", 4096),
		ConsumerApp: consumerApp, ConsumerInstallID: consumerInstallID,
		OwnerAgentID: ownerAgentID, OwnerThreadID: ownerThreadID, OwnerLabel: ownerLabel,
		ResourceKind: strArg(args, "resource_kind"), ResourceID: strArg(args, "resource_id"),
		RepoLabel: strArg(args, "repo_label"), BranchLabel: strArg(args, "branch_label"),
		OriginLabel: strArg(args, "origin_label"), OriginHref: originHref,
		DirtyState: "unknown", UnpushedState: "unknown",
		CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339),
		LastActivityAt: now.Format(time.RFC3339), ExpiresAt: expires.Format(time.RFC3339),
		DeleteAt: deleteAt.Format(time.RFC3339),
	}
	if err := insertWorkspace(app.AppDB(), w, idempotencyKey); err != nil {
		if idempotencyKey != "" {
			if existing, getErr := getWorkspaceByIdempotency(app.AppDB(), actor.ProjectID, idempotencyKey); getErr == nil && existing != nil {
				return existing, nil
			}
		}
		return nil, err
	}
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "workspace.provisioning", actor, "Workspace provisioning started", map[string]any{"profile": profile})

	input := map[string]any{
		"name":  "workspace-" + strings.TrimPrefix(w.ID, "wsp_")[:12],
		"image": image,
		"volumes": []map[string]any{
			{"name": "workspace", "mount_path": "/workspace"},
			{"name": "cache", "mount_path": "/cache"},
		},
		"env": map[string]string{
			"HOME": "/workspace/.home", "XDG_CACHE_HOME": "/cache/xdg",
			"GOCACHE": "/cache/go/build", "GOMODCACHE": "/cache/go/pkg/mod",
			"BUN_INSTALL_CACHE_DIR": "/cache/bun", "PIP_CACHE_DIR": "/cache/pip",
		},
		"resources":      map[string]any{"cpu": w.CPU, "memory_mb": w.MemoryMB},
		"restart_policy": "unless-stopped", "pull_policy": "missing",
		"working_directory": "/workspace",
		"command":           []string{"/bin/sh", "-c", "trap 'exit 0' TERM INT; while :; do sleep 3600; done"},
	}
	var runOut workloadResponse
	if err := app.PlatformAPI().CallAppResult("containers", "containers_run", input, &runOut); err != nil {
		_ = updateWorkspace(app.AppDB(), w.ID, map[string]any{
			"lifecycle_status": statusFailed, "last_error": err.Error(), "updated_at": nowUTC(),
		})
		_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "workspace.failed", Actor{Kind: "runtime", Label: "Containers"}, "Workspace provisioning failed", map[string]any{"error": err.Error()})
		return nil, err
	}
	if runOut.Workload.ID == "" {
		err := errors.New("containers_run returned an empty workload id")
		_ = updateWorkspace(app.AppDB(), w.ID, map[string]any{"lifecycle_status": statusFailed, "last_error": err.Error(), "updated_at": nowUTC()})
		return nil, err
	}
	w.WorkloadID = runOut.Workload.ID
	if sourceArchive != "" {
		var imported map[string]any
		if err := app.PlatformAPI().CallAppResult("containers", "containers_volume_import", map[string]any{
			"workload_id": w.WorkloadID, "volume": "workspace", "path": ".", "archive_base64": sourceArchive,
		}, &imported); err != nil {
			var destroyed map[string]any
			_ = app.PlatformAPI().CallAppResult("containers", "containers_destroy", map[string]any{"workload_id": w.WorkloadID, "delete_volumes": true}, &destroyed)
			_ = updateWorkspace(app.AppDB(), w.ID, map[string]any{
				"workload_id": w.WorkloadID, "lifecycle_status": statusFailed,
				"runtime_status": "destroyed", "last_error": "source import: " + err.Error(), "updated_at": nowUTC(),
			})
			_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "source.import_failed", Actor{Kind: "runtime", Label: "Containers"}, "Source archive import failed", map[string]any{"error": err.Error()})
			return nil, err
		}
		_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "source.imported", actor, "Source archive imported", nil)
	}
	updates := map[string]any{
		"workload_id": w.WorkloadID, "lifecycle_status": statusRunning,
		"runtime_status": firstNonEmpty(runOut.Workload.Status, "running"),
		"health_status":  runOut.Workload.HealthStatus, "last_error": "", "updated_at": nowUTC(),
	}
	if err := updateWorkspace(app.AppDB(), w.ID, updates); err != nil {
		return nil, err
	}
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "workspace.created", actor, "Workspace is ready", map[string]any{"workload_id": w.WorkloadID})
	app.EmitWithProject("workspace.created", w.ProjectID, map[string]any{"workspace_id": w.ID, "consumer_app": w.ConsumerApp})
	return requireWorkspace(app.AppDB(), w.ProjectID, w.ID)
}

func (a *App) startCommand(callCtx context.Context, app *sdk.AppCtx, args map[string]any, actor Actor, w *Workspace) (*Command, error) {
	if w.LifecycleStatus != statusRunning {
		return nil, fmt.Errorf("workspace is not executable in status %q", w.LifecycleStatus)
	}
	if expires := parseTime(w.ExpiresAt); !expires.IsZero() && !expires.After(time.Now().UTC()) {
		return nil, errors.New("workspace TTL has expired; extend it before starting another command")
	}
	idempotencyKey := callerIdempotencyKey(callCtx, actor)
	if idempotencyKey != "" {
		existing, err := getCommandByIdempotency(app.AppDB(), actor.ProjectID, idempotencyKey)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
	}
	if err := ensureNoActiveCommand(app.AppDB(), w.ID); err != nil {
		return nil, err
	}
	argv, display, workingDirectory, timeout, err := normalizeCommand(args, configInt(app, "default_command_timeout_seconds", 1800))
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	c := &Command{
		ID: newID("cmd"), WorkspaceID: w.ID, ProjectID: w.ProjectID,
		DisplayCommand: display, Argv: argv, WorkingDirectory: workingDirectory,
		TimeoutSeconds: timeout, ActorKind: actor.Kind, ActorID: actor.ID,
		ActorLabel: actor.Label, Status: "queued", CreatedAt: now, UpdatedAt: now,
	}
	if err := insertCommand(app.AppDB(), c, idempotencyKey); err != nil {
		return nil, err
	}
	var out executionResponse
	err = app.PlatformAPI().CallAppResult("containers", "containers_exec_start", map[string]any{
		"workload_id": w.WorkloadID, "argv": argv, "working_directory": workingDirectory,
		"timeout_s": timeout, "idempotency_key": c.ID,
	}, &out)
	if err != nil {
		_ = updateCommand(app.AppDB(), c.ID, map[string]any{
			"status": "failed", "error_code": "start_failed", "error": err.Error(),
			"finished_at": nowUTC(), "updated_at": nowUTC(),
		})
		_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "command.failed", actor, "Command failed to start: "+display, map[string]any{"command_id": c.ID})
		return nil, err
	}
	if out.Execution.ID == "" {
		out.Execution.ID = out.ExecutionID
	}
	if out.Execution.ID == "" {
		err := errors.New("containers_exec_start returned an empty execution id")
		_ = updateCommand(app.AppDB(), c.ID, map[string]any{
			"status": "failed", "error_code": "invalid_runtime_response", "error": err.Error(),
			"finished_at": nowUTC(), "updated_at": nowUTC(),
		})
		return nil, err
	}
	status := firstNonEmpty(out.Execution.Status, out.Status, "queued")
	_ = updateCommand(app.AppDB(), c.ID, map[string]any{
		"execution_id": out.Execution.ID, "status": status,
		"started_at": out.Execution.StartedAt, "updated_at": nowUTC(),
	})
	_ = updateWorkspace(app.AppDB(), w.ID, map[string]any{
		"activity_status": activityExecuting, "last_activity_at": nowUTC(), "updated_at": nowUTC(),
	})
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "command.started", actor, "Command started: "+display, map[string]any{"command_id": c.ID, "execution_id": out.Execution.ID})
	app.EmitWithProject("workspace.command.started", w.ProjectID, map[string]any{"workspace_id": w.ID, "command_id": c.ID})
	return requireCommand(app.AppDB(), w.ProjectID, w.ID, c.ID)
}

func (a *App) refreshCommand(app *sdk.AppCtx, c *Command) (*Command, error) {
	if c == nil || c.ExecutionID == "" || commandTerminal(c.Status) {
		return c, nil
	}
	var out executionResponse
	if err := app.PlatformAPI().CallAppResult("containers", "containers_exec_get", map[string]any{"execution_id": c.ExecutionID}, &out); err != nil {
		return c, err
	}
	exec := out.Execution
	if exec.ID == "" {
		return c, errors.New("containers_exec_get returned no execution")
	}
	wasTerminal := commandTerminal(c.Status)
	fields := map[string]any{
		"status": exec.Status, "error_code": exec.ErrorCode, "error": exec.Error,
		"output_bytes": exec.OutputBytes, "output_truncated": boolInt(exec.OutputTruncated),
		"started_at": exec.StartedAt, "finished_at": exec.FinishedAt, "updated_at": nowUTC(),
	}
	if exec.ExitCode != nil {
		fields["exit_code"] = *exec.ExitCode
	}
	if err := updateCommand(app.AppDB(), c.ID, fields); err != nil {
		return c, err
	}
	current, err := requireCommand(app.AppDB(), c.ProjectID, c.WorkspaceID, c.ID)
	if err != nil {
		return nil, err
	}
	if !wasTerminal && commandTerminal(current.Status) {
		actor := Actor{Kind: "runtime", Label: "Containers"}
		summary := "Command " + current.Status + ": " + current.DisplayCommand
		_ = recordActivity(app.AppDB(), current.WorkspaceID, current.ProjectID, "command."+current.Status, actor, summary, map[string]any{"command_id": current.ID, "exit_code": current.ExitCode})
		app.EmitWithProject("workspace.command.completed", current.ProjectID, map[string]any{"workspace_id": current.WorkspaceID, "command_id": current.ID, "status": current.Status, "exit_code": current.ExitCode})
	}
	active, _ := listActiveCommands(app.AppDB(), c.WorkspaceID)
	activity := activityIdle
	if len(active) > 0 {
		activity = activityExecuting
	}
	_ = updateWorkspace(app.AppDB(), c.WorkspaceID, map[string]any{"activity_status": activity, "last_activity_at": nowUTC(), "updated_at": nowUTC()})
	return current, nil
}

func (a *App) refreshWorkspace(app *sdk.AppCtx, w *Workspace) (*ContainerWorkload, error) {
	if w == nil || w.WorkloadID == "" || w.LifecycleStatus == statusDestroyed || w.LifecycleStatus == statusDestroying {
		return nil, nil
	}
	active, err := listActiveCommands(app.AppDB(), w.ID)
	if err != nil {
		return nil, err
	}
	for _, command := range active {
		_, _ = a.refreshCommand(app, command)
	}
	var out workloadResponse
	if err := app.PlatformAPI().CallAppResult("containers", "containers_get", map[string]any{"workload_id": w.WorkloadID}, &out); err != nil {
		_ = updateWorkspace(app.AppDB(), w.ID, map[string]any{"last_error": err.Error(), "updated_at": nowUTC()})
		return nil, err
	}
	runtime := out.Workload
	fields := map[string]any{
		"runtime_status": runtime.Status, "health_status": runtime.HealthStatus,
		"last_error": runtime.LastError, "updated_at": nowUTC(),
	}
	if w.LifecycleStatus != statusExpired && w.LifecycleStatus != statusDestroying && w.LifecycleStatus != statusDestroyed {
		switch runtime.Status {
		case "running":
			fields["lifecycle_status"] = statusRunning
		case "stopped":
			fields["lifecycle_status"] = statusSuspended
		case "error", "unhealthy", "destroyed":
			fields["lifecycle_status"] = statusFailed
		}
	}
	active, _ = listActiveCommands(app.AppDB(), w.ID)
	if len(active) > 0 {
		fields["activity_status"] = activityExecuting
	} else {
		fields["activity_status"] = activityIdle
	}
	if err := updateWorkspace(app.AppDB(), w.ID, fields); err != nil {
		return nil, err
	}
	return &runtime, nil
}

func (a *App) commandLogs(app *sdk.AppCtx, c *Command, tail int) (map[string]any, error) {
	if c.ExecutionID == "" {
		return map[string]any{"command_id": c.ID, "status": c.Status, "logs": "", "output_bytes": 0}, nil
	}
	var out executionLogsResponse
	if err := app.PlatformAPI().CallAppResult("containers", "containers_exec_logs", map[string]any{
		"execution_id": c.ExecutionID, "tail": tail,
	}, &out); err != nil {
		return nil, err
	}
	return map[string]any{
		"command_id": c.ID, "execution_id": c.ExecutionID, "status": out.Status,
		"logs": out.Logs, "output_bytes": out.OutputBytes, "output_truncated": out.OutputTruncated,
	}, nil
}

func (a *App) cancelCommand(app *sdk.AppCtx, actor Actor, w *Workspace, c *Command) (*Command, error) {
	if commandTerminal(c.Status) {
		return c, nil
	}
	if c.ExecutionID == "" {
		_ = updateCommand(app.AppDB(), c.ID, map[string]any{"status": "cancelled", "finished_at": nowUTC(), "updated_at": nowUTC()})
		return requireCommand(app.AppDB(), c.ProjectID, c.WorkspaceID, c.ID)
	}
	var out executionResponse
	if err := app.PlatformAPI().CallAppResult("containers", "containers_exec_cancel", map[string]any{"execution_id": c.ExecutionID}, &out); err != nil {
		return nil, err
	}
	status := firstNonEmpty(out.Execution.Status, "cancelling")
	fields := map[string]any{"status": status, "updated_at": nowUTC()}
	if out.Execution.FinishedAt != "" {
		fields["finished_at"] = out.Execution.FinishedAt
	}
	_ = updateCommand(app.AppDB(), c.ID, fields)
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "command.cancel_requested", actor, "Command cancellation requested: "+c.DisplayCommand, map[string]any{"command_id": c.ID})
	return requireCommand(app.AppDB(), c.ProjectID, c.WorkspaceID, c.ID)
}

func (a *App) stopWorkspace(app *sdk.AppCtx, actor Actor, w *Workspace, eventType string) (*Workspace, error) {
	if w.LifecycleStatus == statusDestroyed || w.LifecycleStatus == statusDestroying {
		return nil, errors.New("workspace has been destroyed")
	}
	active, err := listActiveCommands(app.AppDB(), w.ID)
	if err != nil {
		return nil, err
	}
	for _, command := range active {
		if _, err := a.cancelCommand(app, actor, w, command); err != nil {
			return nil, fmt.Errorf("cancel active command: %w", err)
		}
	}
	if w.RuntimeStatus != "stopped" && w.WorkloadID != "" {
		var out workloadResponse
		if err := app.PlatformAPI().CallAppResult("containers", "containers_stop", map[string]any{"workload_id": w.WorkloadID}, &out); err != nil {
			return nil, err
		}
	}
	status := statusSuspended
	if eventType == "workspace.expired" {
		status = statusExpired
	}
	_ = updateWorkspace(app.AppDB(), w.ID, map[string]any{
		"lifecycle_status": status, "activity_status": activityIdle,
		"runtime_status": "stopped", "last_error": "", "updated_at": nowUTC(),
	})
	summary := "Workspace stopped; container and volumes preserved"
	if status == statusExpired {
		summary = "Workspace TTL expired; stopped with container and volumes preserved"
	}
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, eventType, actor, summary, nil)
	app.EmitWithProject(eventType, w.ProjectID, map[string]any{"workspace_id": w.ID})
	return requireWorkspace(app.AppDB(), w.ProjectID, w.ID)
}

func (a *App) resumeWorkspace(app *sdk.AppCtx, actor Actor, w *Workspace) (*Workspace, error) {
	if w.LifecycleStatus != statusSuspended && w.LifecycleStatus != statusFailed {
		return nil, fmt.Errorf("workspace cannot resume from status %q", w.LifecycleStatus)
	}
	if expires := parseTime(w.ExpiresAt); expires.IsZero() || !expires.After(time.Now().UTC()) {
		return nil, errors.New("workspace TTL has expired; extend it before resuming")
	}
	var out workloadResponse
	if err := app.PlatformAPI().CallAppResult("containers", "containers_start", map[string]any{"workload_id": w.WorkloadID}, &out); err != nil {
		return nil, err
	}
	_ = updateWorkspace(app.AppDB(), w.ID, map[string]any{
		"lifecycle_status": statusRunning, "runtime_status": firstNonEmpty(out.Workload.Status, "running"),
		"health_status": out.Workload.HealthStatus, "last_error": "", "updated_at": nowUTC(),
	})
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "workspace.resumed", actor, "Workspace resumed", nil)
	return requireWorkspace(app.AppDB(), w.ProjectID, w.ID)
}

func (a *App) extendWorkspace(app *sdk.AppCtx, actor Actor, w *Workspace, minutes int) (*Workspace, error) {
	maxTTL := configInt(app, "max_ttl_minutes", 480)
	if minutes < 1 || minutes > maxTTL {
		return nil, fmt.Errorf("ttl_minutes must be between 1 and %d", maxTTL)
	}
	now := time.Now().UTC()
	expires := now.Add(time.Duration(minutes) * time.Minute)
	deleteAt := expires.Add(time.Duration(configInt(app, "expired_retention_minutes", 1440)) * time.Minute)
	fields := map[string]any{"expires_at": expires.Format(time.RFC3339), "delete_at": deleteAt.Format(time.RFC3339), "updated_at": nowUTC()}
	if w.LifecycleStatus == statusExpired {
		fields["lifecycle_status"] = statusSuspended
	}
	if err := updateWorkspace(app.AppDB(), w.ID, fields); err != nil {
		return nil, err
	}
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "workspace.ttl_extended", actor, fmt.Sprintf("TTL extended by %d minutes", minutes), nil)
	return requireWorkspace(app.AppDB(), w.ProjectID, w.ID)
}

func (a *App) destroyWorkspace(app *sdk.AppCtx, actor Actor, w *Workspace) (*Workspace, error) {
	if w.LifecycleStatus == statusDestroyed {
		return w, nil
	}
	_ = updateWorkspace(app.AppDB(), w.ID, map[string]any{"lifecycle_status": statusDestroying, "updated_at": nowUTC()})
	if w.WorkloadID != "" {
		var out map[string]any
		if err := app.PlatformAPI().CallAppResult("containers", "containers_destroy", map[string]any{
			"workload_id": w.WorkloadID, "delete_volumes": true,
		}, &out); err != nil {
			_ = updateWorkspace(app.AppDB(), w.ID, map[string]any{"lifecycle_status": statusFailed, "last_error": err.Error(), "updated_at": nowUTC()})
			return nil, err
		}
	}
	now := nowUTC()
	_ = updateWorkspace(app.AppDB(), w.ID, map[string]any{
		"lifecycle_status": statusDestroyed, "activity_status": activityIdle,
		"runtime_status": "destroyed", "destroyed_at": now, "updated_at": now, "last_error": "",
	})
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "workspace.destroyed", actor, "Workspace and volumes permanently destroyed", nil)
	app.EmitWithProject("workspace.destroyed", w.ProjectID, map[string]any{"workspace_id": w.ID})
	return requireWorkspace(app.AppDB(), w.ProjectID, w.ID)
}

func (a *App) reconcile(ctx context.Context, app *sdk.AppCtx) error {
	rows, err := listReconcileWorkspaces(app.AppDB())
	if err != nil {
		return err
	}
	var errs []error
	for _, w := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := a.refreshWorkspace(app, w); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", w.ID, err))
		}
	}
	return errors.Join(errs...)
}

func (a *App) expire(ctx context.Context, app *sdk.AppCtx) error {
	rows, err := listReconcileWorkspaces(app.AppDB())
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	system := Actor{Kind: "system", ID: "ttl", Label: "TTL policy"}
	var errs []error
	for _, w := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		system.ProjectID = w.ProjectID
		if w.LifecycleStatus == statusExpired && !parseTime(w.DeleteAt).After(now) {
			if _, err := a.destroyWorkspace(app, system, w); err != nil {
				errs = append(errs, fmt.Errorf("destroy expired %s: %w", w.ID, err))
			}
			continue
		}
		if w.LifecycleStatus != statusExpired && w.LifecycleStatus != statusDestroyed && !parseTime(w.ExpiresAt).After(now) {
			if _, err := a.stopWorkspace(app, system, w, "workspace.expired"); err != nil {
				errs = append(errs, fmt.Errorf("expire %s: %w", w.ID, err))
			}
		}
	}
	return errors.Join(errs...)
}
