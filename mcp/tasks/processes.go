package main

import (
	"context"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

// Only authenticated Processes installs may use this API. It deliberately does
// not inherit an agent identity or expose arbitrary Tasks mutation operations.
func (a *App) processTool() sdk.Tool {
	return sdk.Tool{Name: "process_task", Description: "Private Processes execution and schedule API.", Exposure: sdk.ToolExposureAppOnly,
		InputSchema: map[string]any{"type": "object"}, HandlerCtx: a.toolProcessTask}
}

func (a *App) toolProcessTask(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	c := sdk.CallerFrom(ctx)
	if c == nil || c.AppInstallID <= 0 || c.AppName != "processes" {
		return nil, errors.New("bound Processes app required")
	}
	project := app.CurrentProject()
	if project == "" {
		return nil, errors.New("project required")
	}
	process := stringArg(args, "process_id")
	if process == "" {
		return nil, errors.New("process_id required")
	}
	action := stringArg(args, "action")
	if action == "list" {
		// Include schedule definitions and their occurrences; the source link is
		// inherited from the parent without copying execution state into Processes.
		rows, err := a.store.db.Query(`SELECT t.id,l.version,l.run_key FROM tasks t JOIN process_task_links l ON (l.task_id=t.id OR l.task_id=t.parent_task_id) WHERE l.install_id=? AND l.project_id=? AND l.process_id=? AND t.project_id=? ORDER BY t.created_at DESC,t.id DESC LIMIT 201`, c.AppInstallID, project, process, project)
		if err != nil {
			return nil, err
		}
		type ref struct {
			id      string
			version int
			key     string
		}
		refs := []ref{}
		for rows.Next() {
			var r ref
			if err = rows.Scan(&r.id, &r.version, &r.key); err != nil {
				rows.Close()
				return nil, err
			}
			refs = append(refs, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		more := len(refs) > 200
		if more {
			refs = refs[:200]
		}
		out := []any{}
		for _, r := range refs {
			t, e := a.store.Get(r.id)
			if e != nil {
				return nil, e
			}
			out = append(out, map[string]any{"task": t, "version": r.version, "run_key": r.key})
		}
		return map[string]any{"runs": out, "has_more": more}, nil
	}
	if action == "create" {
		key := stringArg(args, "run_key")
		version := intArg(args, "version")
		agentID := int64(intArg(args, "agent_id"))
		if key == "" || len(key) > 200 || version < 1 {
			return nil, errors.New("run_key and positive version required")
		}
		agent, err := app.GetAgent(agentID)
		if err != nil {
			return nil, err
		}
		if agent.ProjectID != project {
			return nil, errors.New("agent is outside this project")
		}
		assigned := strings.TrimSpace(agent.DefaultThreadID)
		if assigned == "" {
			return nil, errors.New("agent has no default thread")
		}
		schedule, err := decodeSchedule(args["schedule"])
		if err != nil {
			return nil, err
		}
		task, _, err := a.store.Create(CreateTaskInput{AgentID: agentID, ProjectID: project, Title: stringArg(args, "title"), Description: stringArg(args, "description"), AssignedThreadID: assigned, IdempotencyKey: fmt.Sprintf("processes:%d:%s:%s", c.AppInstallID, project, key), Schedule: schedule, ScheduleInitiallyPaused: schedule != nil})
		if err != nil {
			return nil, err
		}
		_, err = a.store.db.Exec(`INSERT INTO process_task_links(task_id,install_id,project_id,process_id,version,run_key) VALUES(?,?,?,?,?,?) ON CONFLICT(task_id) DO NOTHING`, task.ID, c.AppInstallID, project, process, version, key)
		if err != nil {
			return nil, err
		}
		var linkedProcess string
		var linkedVersion int
		err = a.store.db.QueryRow(`SELECT process_id,version FROM process_task_links WHERE task_id=? AND install_id=? AND project_id=?`, task.ID, c.AppInstallID, project).Scan(&linkedProcess, &linkedVersion)
		if err != nil || linkedProcess != process || linkedVersion != version {
			return nil, errors.New("run key belongs to another procedure version")
		}
		warning := ""
		if schedule == nil && !terminalState(task.State) {
			if e := a.notifyAssigned(task, task.AssignedThreadID, "task.assigned"); e != nil {
				warning = e.Error()
			}
		}
		return map[string]any{"task": task, "delivery_warning": warning}, nil
	}
	id := stringArg(args, "task_id")
	var owned int
	err := a.store.db.QueryRow(`SELECT 1 FROM process_task_links WHERE task_id=? AND install_id=? AND project_id=? AND process_id=?`, id, c.AppInstallID, project, process).Scan(&owned)
	if err != nil {
		return nil, errors.New("process task not found")
	}
	task, err := a.store.Get(id)
	if err != nil {
		return nil, err
	}
	switch action {
	case "get":
	case "pause":
		if task.ScheduleEnabled {
			task, err = a.store.Pause(id, "processes")
		}
	case "resume":
		if !task.ScheduleEnabled {
			task, err = a.store.Resume(id, "processes")
		}
	default:
		return nil, errors.New("unsupported process task action")
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"task": task}, nil
}
