package main

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) processTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "workspace_processes", Description: "List the processes running in a local workspace (pid, parent, memory, run time, command), and optionally signal one (kill: pid) or all of them (kill_all). Use it to find and stop servers or watchers a command left running. In container mode it covers every process of the workspace's own user, daemons included; under the OS sandbox it covers the process groups of running commands. Docker workspaces: run `ps aux` and `kill` with workspace_command_start.", InputSchema: schemaObject(map[string]any{
			"workspace_id": strSchema(),
			"kill":         map[string]any{"type": "integer", "description": "pid to signal; it must be a process of this workspace"},
			"kill_all":     boolSchema(),
			"signal":       map[string]any{"type": "string", "enum": []string{"TERM", "KILL", "INT", "HUP", "QUIT", "USR1", "USR2"}, "description": "Default TERM."},
		}, []string{"workspace_id"}), HandlerCtx: a.toolProcesses},
	}
}

func (a *App) toolProcesses(c context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, err := actorFrom(c, app)
	if err != nil {
		return nil, err
	}
	w, err := requireWorkspaceForActor(app.AppDB(), actor, strArg(args, "workspace_id"))
	if err != nil {
		return nil, err
	}
	if w.Driver != driverLocal {
		return nil, errors.New("workspace_processes covers local workspaces; in this Docker workspace run `ps aux` and `kill <pid>` with workspace_command_start")
	}
	if w.LifecycleStatus != statusRunning || w.WorkloadID == "" {
		return nil, errors.New("workspace must be running")
	}
	result := map[string]any{}
	pid, killAll := intArg(args, "kill", 0), boolArg(args, "kill_all")
	if pid > 0 || killAll {
		var killed map[string]any
		if err := runtimeCall(app, "containers_process_kill", map[string]any{
			"workload_id": w.WorkloadID, "pid": pid, "all": killAll, "signal": strArg(args, "signal"),
		}, &killed); err != nil {
			return nil, err
		}
		summary := fmt.Sprintf("Signalled process %d", pid)
		if killAll {
			summary = "Signalled all workspace processes"
		}
		_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "process.signalled", actor, summary, killed)
		result["killed"] = killed
	}
	var listed map[string]any
	if err := runtimeCall(app, "containers_processes", map[string]any{"workload_id": w.WorkloadID}, &listed); err != nil {
		return nil, err
	}
	for key, value := range listed {
		result[key] = value
	}
	return result, nil
}
