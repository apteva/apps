package main

import (
	"context"
	_ "embed"
	"errors"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed skills/how-to-use-tasks.md
var taskSkillBody string

const manifestYAML = `schema: apteva-app/v1
name: tasks
display_name: Tasks
version: 3.2.9
description: Durable work, progress, schedules, occurrences, and thread assignment for Apteva agents.
author: Apteva
homepage: https://github.com/apteva/apps/tree/main/mcp/tasks
icon: /ui/icon.svg
icon_style: monochrome
tags: [productivity, agents, scheduling, automation]
scopes: [project, global]
min_apteva_version: "0.28.0"
requires:
  permissions:
    - db.write.app
    - platform.instances.read
    - platform.threads.write
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: create, description: "Create one durable task for multi-step, multi-source, delegated, scheduled, or resumable work." }
    - { name: list, description: List durable tasks visible to the calling agent. }
    - { name: get, description: Get one task and its event history. }
    - { name: update, description: Record meaningful task-level progress and state. }
    - { name: assign, description: Assign a task to an existing opaque agent thread. }
    - { name: complete, description: Complete a task with a concrete result. }
    - { name: cancel, description: Cancel a task or schedule. }
    - { name: pause, description: Pause a scheduled task. }
    - { name: resume, description: Resume a scheduled task. }
    - { name: run_now, description: Run a scheduled task now. }
  ui_panels:
    - slot: project.page
      label: Tasks
      icon: list-checks
      entry: /ui/TasksPanel.mjs
      suggested: true
  ui_surfaces:
    - id: tasks
      label: Tasks
      icon: list-checks
      schema: apteva-native-surface/v1
      entry: /ui/surfaces/tasks.json
      slots: [mobile.project_app]
  ui_components:
    - name: task-overview
      label: Tasks
      description: Live work, upcoming schedules, and recent outcomes.
      entry: /ui/TaskOverviewWidget.mjs
      slots: [dashboard.home]
      suggested: true
      supported_sizes: [half, full]
      default_size: half
      visibility: project
      refresh_topics: [task.created, task.updated, task.state_changed, task.schedule_updated, task.schedule_paused, task.schedule_resumed, task.schedule_run_requested, task.occurrence_skipped_overlap]
      native:
        schema: apteva-native-surface/v1
        entry: /ui/surfaces/task-overview.json
      settings_schema:
        type: object
        properties:
          show_active:
            type: boolean
            title: Active and attention
            default: true
          show_upcoming:
            type: boolean
            title: Upcoming schedules
            default: true
          show_recent:
            type: boolean
            title: Recent outcomes
            default: true
          recent_limit:
            type: integer
            title: Recent task limit
            description: Maximum recent outcomes displayed in this widget.
            default: 4
            minimum: 1
            maximum: 12
    - name: agent-tasks
      label: Agent tasks
      description: Work and schedules associated with an agent.
      entry: /ui/AgentTasksWidget.mjs
      slots: [dashboard.agent_card, dashboard.agent_detail, dashboard.thread_sidebar]
      suggested: true
      visibility: attached
      refresh_topics: [task.created, task.updated, task.state_changed, task.schedule_updated, task.schedule_paused, task.schedule_resumed, task.schedule_run_requested, task.occurrence_skipped_overlap]
      default_width: 1
    - name: task-card
      entry: /ui/TaskCard.mjs
      slots: [chat.message_attachment]
  skills:
    - name: how-to-use-tasks
      command: /tasks
      body_file: skills/how-to-use-tasks.md
      description: |
        Task-selection, ownership, progress, delegation, schedules, and terminal
        receipts. Load before multi-step or multi-source reviews, scheduled,
        delegated, or resumable work.
runtime:
  kind: source
  source: { repo: github.com/apteva/apps, ref: main, entry: mcp/tasks }
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/tasks.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct {
	ctx       *sdk.AppCtx
	store     *taskStore
	scheduler *scheduler
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic(err)
	}
	for index := range m.Provides.Skills {
		if m.Provides.Skills[index].Name == "how-to-use-tasks" {
			m.Provides.Skills[index].Body = taskSkillBody
			m.Provides.Skills[index].BodyFile = ""
		}
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx == nil || ctx.AppDB() == nil {
		return errors.New("tasks app requires its database")
	}
	a.ctx = ctx
	a.store = newTaskStore(ctx.AppDB(), func(event TaskEvent) {
		ctx.EmitWithProject("task."+event.EventType, eventProjectID(a.store, event.TaskID), event)
	})
	a.scheduler = &scheduler{store: a.store, app: a}
	ctx.Logger().Info("tasks app mounted", "version", "3.2.9")
	return nil
}

func eventProjectID(store *taskStore, taskID string) string {
	if store == nil {
		return ""
	}
	task, err := store.Get(taskID)
	if err != nil {
		return ""
	}
	return task.ProjectID
}

func (a *App) OnUnmount(*sdk.AppCtx) error { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/mobile/summary", Handler: a.handleMobileSummary},
		{Pattern: "/tasks", Handler: a.handleTasks},
		{Pattern: "/tasks/", Handler: a.handleTask},
	}
}

func (a *App) MCPTools() []sdk.Tool              { return a.tools() }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{Name: "scheduler", Schedule: "@every 2s", Run: func(_ context.Context, ctx *sdk.AppCtx) error {
		if a.scheduler == nil {
			return nil
		}
		return a.scheduler.Tick(nowUTC(), ctx.CurrentProject())
	}}}
}

func (a *App) logger() sdk.Logger {
	if a.ctx == nil {
		return discardLogger{}
	}
	return a.ctx.Logger()
}

type discardLogger struct{}

func (discardLogger) Info(string, ...any)  {}
func (discardLogger) Warn(string, ...any)  {}
func (discardLogger) Error(string, ...any) {}

// notifyAssigned wakes threadID with an event about task. The wake target is an
// explicit argument, never re-read from the store: a concurrent reassign landing
// between the caller's write and a re-read would redirect the event and leave
// the thread this call just assigned paused forever.
func (a *App) notifyAssigned(task *Task, threadID, eventType string) error {
	if a.ctx == nil || a.ctx.ThreadAPI() == nil {
		return errors.New("platform thread API unavailable")
	}
	payload := map[string]any{"type": eventType, "task_id": task.ID, "title": task.Title, "description": task.Description, "state": task.State, "scheduled_for": task.ScheduledFor, "parent_task_id": task.ParentTaskID}
	return a.ctx.ThreadAPI().SendThreadEvent(sdk.ThreadRef{AgentID: task.AgentID, ThreadID: threadID}, payload)
}

func (a *App) notifyCreator(task *Task) error {
	if task == nil || strings.TrimSpace(task.CreatedByThreadID) == "" || task.CreatedByThreadID == task.AssignedThreadID {
		return nil
	}
	if a.ctx == nil || a.ctx.ThreadAPI() == nil {
		return errors.New("platform thread API unavailable")
	}
	payload := map[string]any{"type": "task.terminal", "task_id": task.ID, "title": task.Title, "state": task.State, "result": task.Result, "error": task.Error, "parent_task_id": task.ParentTaskID}
	return a.ctx.ThreadAPI().SendThreadEvent(sdk.ThreadRef{AgentID: task.AgentID, ThreadID: task.CreatedByThreadID}, payload)
}

func nowUTC() time.Time { return time.Now().UTC() }

func main() { sdk.Run(&App{}) }
