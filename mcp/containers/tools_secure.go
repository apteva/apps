package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) toolRunCtx(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	owner := ownerFromCaller(callCtx, app)
	ctx, cancel := context.WithTimeout(callCtx, 2*time.Minute)
	defer cancel()
	workload, err := a.createOwnedWorkload(ctx, app, app.AppDB(), parseRunSpec(args), owner)
	if err != nil {
		return nil, err
	}
	return map[string]any{"workload": workload}, nil
}

func (a *App) toolGetCtx(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	workload, err := requireOwnedWorkload(app.AppDB(), workloadIDArg(args), ownerFromCaller(callCtx, app))
	if err != nil {
		return nil, err
	}
	return map[string]any{"workload": workload}, nil
}

func (a *App) toolListCtx(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	rows, err := listWorkloads(app.AppDB(), getStr(args, "status"))
	if err != nil {
		return nil, err
	}
	owner := ownerFromCaller(callCtx, app)
	filtered := make([]*Workload, 0, len(rows))
	for _, workload := range rows {
		if ownerCanAccess(owner, workload) {
			filtered = append(filtered, workload)
		}
	}
	return map[string]any{"workloads": filtered, "count": len(filtered)}, nil
}

func (a *App) toolStartCtx(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	workload, err := requireOwnedWorkload(app.AppDB(), workloadIDArg(args), ownerFromCaller(callCtx, app))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(callCtx, 30*time.Second)
	defer cancel()
	workload, err = a.startWorkload(ctx, app, app.AppDB(), workload.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"workload": workload}, nil
}

func (a *App) toolStopCtx(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	workload, err := requireOwnedWorkload(app.AppDB(), workloadIDArg(args), ownerFromCaller(callCtx, app))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(callCtx, 30*time.Second)
	defer cancel()
	workload, err = a.stopWorkload(ctx, app, app.AppDB(), workload.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"workload": workload}, nil
}

func (a *App) toolRestartCtx(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	workload, err := requireOwnedWorkload(app.AppDB(), workloadIDArg(args), ownerFromCaller(callCtx, app))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(callCtx, 45*time.Second)
	defer cancel()
	workload, err = a.restartWorkload(ctx, app, app.AppDB(), workload.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"workload": workload}, nil
}

func (a *App) toolDestroyCtx(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	workload, err := requireOwnedWorkload(app.AppDB(), workloadIDArg(args), ownerFromCaller(callCtx, app))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(callCtx, 45*time.Second)
	defer cancel()
	if err := a.destroyWorkload(ctx, app, app.AppDB(), workload.ID, boolArg(args, "delete_volumes")); err != nil {
		return nil, err
	}
	return map[string]any{"destroyed": true, "workload_id": workload.ID}, nil
}

func (a *App) toolLogsCtx(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	workload, err := requireOwnedWorkload(app.AppDB(), workloadIDArg(args), ownerFromCaller(callCtx, app))
	if err != nil {
		return nil, err
	}
	backend, err := a.backendForWorkload(app, workload)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(callCtx, 15*time.Second)
	defer cancel()
	logs, err := backend.Logs(ctx, workload.ContainerName, intArg(args, "tail", 200))
	if err != nil {
		return nil, err
	}
	return map[string]any{"workload_id": workload.ID, "logs": logs}, nil
}

func (a *App) toolHealthCtx(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	workload, err := requireOwnedWorkload(app.AppDB(), workloadIDArg(args), ownerFromCaller(callCtx, app))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(callCtx, 10*time.Second)
	defer cancel()
	if err := a.probeWorkload(ctx, app, app.AppDB(), workload.ID); err != nil {
		return nil, err
	}
	workload, _ = getWorkload(app.AppDB(), workload.ID)
	return map[string]any{"workload": workload}, nil
}

func (a *App) toolUsageGetCtx(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	workload, err := requireOwnedWorkload(app.AppDB(), workloadIDArg(args), ownerFromCaller(callCtx, app))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(callCtx, 2*time.Minute)
	defer cancel()
	usage, err := a.workloadUsage(ctx, app, app.AppDB(), workload.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"usage": usage, "metrics": usage.Metrics}, nil
}

func (a *App) toolExecutionStart(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	owner, err := requireAppOwner(callCtx, app)
	if err != nil {
		return nil, err
	}
	workload, err := requireOwnedWorkload(app.AppDB(), getStr(args, "workload_id"), owner)
	if err != nil {
		return nil, err
	}
	var in executionInput
	raw, _ := json.Marshal(args)
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	execution, err := a.startExecution(callCtx, app, workload, owner, in)
	if err != nil {
		return nil, err
	}
	return map[string]any{"execution_id": execution.ID, "status": execution.Status, "execution": execution}, nil
}

func (a *App) ownedExecution(callCtx context.Context, app *sdk.AppCtx, id string) (*Execution, error) {
	owner, err := requireAppOwner(callCtx, app)
	if err != nil {
		return nil, err
	}
	execution, err := getExecution(app.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if execution == nil || execution.OwnerAppInstallID != owner.InstallID || (execution.ProjectID != "" && owner.ProjectID != "" && execution.ProjectID != owner.ProjectID) {
		return nil, errors.New("execution not found")
	}
	return execution, nil
}

func (a *App) toolExecutionGet(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	execution, err := a.ownedExecution(callCtx, app, getStr(args, "execution_id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"execution": execution}, nil
}

func (a *App) toolExecutionLogs(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	execution, err := a.ownedExecution(callCtx, app, getStr(args, "execution_id"))
	if err != nil {
		return nil, err
	}
	logs := ""
	outputBytes := execution.OutputBytes
	outputTruncated := execution.OutputTruncated
	if !executionTerminal(execution.Status) && execution.RuntimeContainerName != "" {
		backend, backendErr := a.executionBackendFor(app, execution)
		if backendErr != nil {
			return nil, backendErr
		}
		ctx, cancel := context.WithTimeout(callCtx, 15*time.Second)
		logs, err = backend.ExecutionLogs(ctx, execution.RuntimeContainerName, intArg(args, "tail", 200))
		cancel()
		outputBytes = len(logs)
		logs, outputTruncated = capExecutionOutput(logs, configInt(app, "max_execution_output_bytes", 1048576))
	} else {
		logs, err = executionLogs(app.AppDB(), execution)
		logs = tailLines(logs, intArg(args, "tail", 200))
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"execution_id": execution.ID, "status": execution.Status, "logs": logs,
		"output_bytes": outputBytes, "output_truncated": outputTruncated,
	}, nil
}

func (a *App) toolExecutionCancel(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	execution, err := a.ownedExecution(callCtx, app, getStr(args, "execution_id"))
	if err != nil {
		return nil, err
	}
	execution, err = a.cancelExecution(app, execution)
	if err != nil {
		return nil, err
	}
	return map[string]any{"execution": execution, "cancelled": execution.Status == executionCancelled}, nil
}

func (a *App) toolVolumeImport(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	owner, err := requireAppOwner(callCtx, app)
	if err != nil {
		return nil, err
	}
	workload, err := requireOwnedWorkload(app.AppDB(), getStr(args, "workload_id"), owner)
	if err != nil {
		return nil, err
	}
	return a.importVolumeArchive(app, workload, getStr(args, "volume"), getStr(args, "path"), getStr(args, "archive_base64"))
}

func (a *App) toolVolumeExport(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	owner, err := requireAppOwner(callCtx, app)
	if err != nil {
		return nil, err
	}
	workload, err := requireOwnedWorkload(app.AppDB(), getStr(args, "workload_id"), owner)
	if err != nil {
		return nil, err
	}
	return a.exportVolumeArchive(app, workload, getStr(args, "volume"), getStr(args, "path"))
}
