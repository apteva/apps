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

//go:embed apteva.yaml
var manifestYAML string

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
	if err := a.reconcileLegacyTasks(); err != nil {
		return err
	}
	if err := a.reconcileUndispatchedTasks(); err != nil {
		return err
	}
	a.scheduler = &scheduler{store: a.store, app: a}
	ctx.Logger().Info("tasks app mounted", "version", a.Manifest().Version)
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
		{Pattern: "/delivery-health", Handler: a.handleDeliveryHealth},
		{Pattern: "/mobile/summary", Handler: a.handleMobileSummary},
		{Pattern: "/tasks", Handler: a.handleTasks},
		{Pattern: "/tasks/", Handler: a.handleTask},
	}
}

func (a *App) MCPTools() []sdk.Tool           { return a.tools() }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) EventHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{{Event: sdk.AgentEventLifecycleEvent, Handler: a.handleAgentEventLifecycle}}
}
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{Name: "legacy-resolution", Schedule: "@every 5m", Run: func(_ context.Context, _ *sdk.AppCtx) error { return a.reconcileLegacyTasks() }}, {Name: "scheduler", Schedule: "@every 2s", Run: func(_ context.Context, ctx *sdk.AppCtx) error {
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

func taskWakePayload(task *Task, threadID, eventType string) map[string]any {
	var parentTaskID any
	if strings.TrimSpace(task.ParentTaskID) != "" {
		parentTaskID = task.ParentTaskID
	}
	if eventType == "task.assigned" {
		return map[string]any{
			"type": eventType, "origin": "tasks.assignment", "task_id": task.ID,
			"title": task.Title, "description": task.Description, "state": task.State,
			"assigned_thread_id": threadID, "parent_task_id": parentTaskID,
			"required_first_action": map[string]any{"tool": "tasks_get", "task_id": task.ID},
			"instruction":           "First call tasks_get with the supplied task_id before any domain action. Report through the parent-thread mechanism required by your directive.",
		}
	}
	origin := "tasks.scheduler"
	instruction := "This is a scheduler event, not a message from another thread. Do not call send. First call tasks_get for the occurrence before any domain action."
	if eventType == "task.recovery.ready" {
		origin = "tasks.recovery"
		instruction = "This is a reconciliation-only recovery event, not a message from another thread. Do not call send and do not repeat an ambiguous external action. First call tasks_get for the recovery occurrence, reconcile external state, and record a concrete terminal result."
	}
	payload := map[string]any{
		"type": eventType, "origin": origin, "task_id": task.ID,
		"occurrence_id": task.ID, "parent_task_id": parentTaskID,
		"assigned_thread_id": threadID, "reply_required": false, "reply_thread_id": nil,
		"required_first_action": map[string]any{"tool": "tasks_get", "task_id": task.ID},
		"instruction":           instruction,
		"scheduled_for":         task.ScheduledFor,
	}
	if task.RecoveryOfTaskID != "" {
		payload["recovery_of_task_id"] = task.RecoveryOfTaskID
		payload["original_occurrence_key"] = task.OriginalOccurrenceKey
		payload["recovery_attempt"] = task.RecoveryAttempt
		payload["recovery_reason"] = task.RecoveryReason
		payload["operation_key"] = task.OperationKey
		payload["recovery_mode"] = "reconcile_only"
	}
	return payload
}

// notifyAssigned wakes threadID with an event about task. The wake target is an
// explicit argument, never re-read from the store: a concurrent reassign landing
// between the caller's write and a re-read would redirect the event and leave
// the thread this call just assigned paused forever.
func (a *App) notifyAssigned(task *Task, threadID, eventType string) error {
	return a.drainDeliveries(task.ID, task.ProjectID, a.store.now())
}
func (a *App) notifyTerminalization(task *Task, at time.Time) error {
	return a.drainDeliveries(task.ID, task.ProjectID, at)
}
func (a *App) notifyCreator(task *Task) error {
	return a.drainDeliveries(task.ID, task.ProjectID, a.store.now())
}
func (a *App) notifyDispatchFailure(task *Task) error                 { return a.notifyCreator(task) }
func (a *App) notifyExecutionFailure(task *Task, reason string) error { return a.notifyCreator(task) }

func nowUTC() time.Time { return time.Now().UTC() }

func main() { sdk.Run(&App{}) }
