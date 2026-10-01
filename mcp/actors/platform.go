package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var operationNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func (e *actorExecution) lockContext() error {
	res, err := e.ctx.AppDB().Exec(`INSERT INTO actors_context_locks(project_id,context_id,run_id,expires_at) VALUES(?,?,?,?) ON CONFLICT(project_id,context_id) DO UPDATE SET run_id=excluded.run_id,expires_at=excluded.expires_at WHERE actors_context_locks.expires_at<? OR actors_context_locks.run_id=excluded.run_id`, projectID(e.ctx), e.definition.Browser.ContextID, e.run.ID, e.deadline.Add(time.Minute).Unix(), time.Now().Unix())
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errors.New("saved Computer context is in use by another actor run; retry after it completes")
	}
	return nil
}

type actorOperation struct {
	Steps        []actorStep       `json:"steps"`
	OutputSchema map[string]string `json:"output_schema"`
}

func selectOperation(def actorDefinition, name string) (actorDefinition, error) {
	if len(def.Operations) == 0 {
		if name != "run" {
			return def, fmt.Errorf("operation %q not found; this actor provides run", name)
		}
		return def, nil
	}
	op, ok := def.Operations[name]
	if !ok {
		return def, fmt.Errorf("operation %q not found", name)
	}
	def.Operations = nil
	def.Steps = op.Steps
	def.OutputSchema = op.OutputSchema
	return def, nil
}

func requireActorJob(ctx *sdk.AppCtx, id int64) error {
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("jobs", "jobs_get", withProjectID(ctx, map[string]any{"id": id}), &out); err != nil {
		return err
	}
	job := mapFromAny(out["job"])
	target := mapFromAny(job["target"])
	if job["owner_app"] != "actors" || target["app"] != "actors" || target["tool"] != "actors_run" {
		return errors.New("schedule not found or not owned by Actors")
	}
	return nil
}

func (e *actorExecution) interact(step actorStep) error {
	if e.session == nil {
		return errors.New("interaction requires an open browser")
	}
	args := map[string]any{"session_id": e.session.SessionID, "action": step.Action}
	switch step.Action {
	case "fill":
		args["action"] = "set_text"
		args["selector"] = step.Locator.Selector
		args["text"] = step.Text
		args["mode"] = "replace"
	case "key":
		args["key"] = step.Key
	case "scroll":
		args["direction"] = step.Direction
		args["amount"] = boundedInt(step.Amount, 300, 1, 10000)
	}
	var out map[string]any
	if err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, args), &out); err != nil {
		return err
	}
	return e.finishInteraction(out)
}

func (a *App) platformTools() []sdk.Tool {
	integer := map[string]any{"type": "integer", "minimum": 1}
	return []sdk.Tool{
		{Name: "actors_task_save", Description: "Create or update a saved task, pinning an actor revision, named operation and input. No execution occurs.", InputSchema: schemaObject(map[string]any{"id": integer, "name": map[string]any{"type": "string"}, "actor_id": integer, "revision": integer, "operation": map[string]any{"type": "string"}, "preset": map[string]any{"type": "string"}, "input": map[string]any{"type": "object"}}, []string{"name", "actor_id"}), Handler: a.toolTaskSave},
		{Name: "actors_task_list", Description: "List saved tasks in the current project.", InputSchema: schemaObject(map[string]any{}, nil), Handler: a.toolTaskList},
		{Name: "actors_task_run", Description: "Queue a saved task using its pinned revision and inputs.", InputSchema: schemaObject(map[string]any{"id": integer}, []string{"id"}), Handler: a.toolTaskRun},
		{Name: "actors_task_delete", Description: "Delete a saved task; actor definitions and historical runs remain.", InputSchema: schemaObject(map[string]any{"id": integer}, []string{"id"}), Handler: a.toolTaskDelete},
		{Name: "actors_dataset_read", Description: "Read committed dataset items for a run, including partial results after failure. Pass next_cursor as after for the next page.", InputSchema: schemaObject(map[string]any{"run_id": integer, "after": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}, []string{"run_id"}), Handler: a.toolDatasetRead},
	}
}

type actorTask struct {
	ID        int64          `json:"id"`
	Name      string         `json:"name"`
	ActorID   int64          `json:"actor_id"`
	Revision  int            `json:"revision"`
	Operation string         `json:"operation"`
	Preset    string         `json:"preset"`
	Input     map[string]any `json:"input"`
}

func (a *App) toolTaskSave(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" || len(name) > 120 {
		return nil, errors.New("name required, at most 120 characters")
	}
	actor, err := getActor(ctx, int64ArgLocal(args, "actor_id"), "")
	if err != nil {
		return nil, err
	}
	if actor == nil {
		return nil, errors.New("actor not found")
	}
	revision := intArg(args, "revision")
	if revision <= 0 {
		revision = actor.Revision
	}
	if revision != actor.Revision {
		var raw string
		if err := ctx.AppDB().QueryRow(`SELECT definition_json FROM actors_versions WHERE project_id=? AND actor_id=? AND revision=?`, projectID(ctx), actor.ID, revision).Scan(&raw); err != nil {
			return nil, errors.New("actor revision not found")
		}
		if err := json.Unmarshal([]byte(raw), &actor.Definition); err != nil {
			return nil, err
		}
	}
	operation := firstNonEmpty(stringArg(args, "operation"), "run")
	def, err := selectOperation(actor.Definition, operation)
	if err != nil {
		return nil, err
	}
	input := mapFromAny(args["input"])
	inputBytes, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	if len(inputBytes) > maxActorInputBytes {
		return nil, errors.New("task input too large")
	}
	// Task saves use the same template/definition validation as run admission.
	snapshot, _ := json.Marshal(def)
	runInput, _ := json.Marshal(map[string]any{"input": input, "preset": stringArg(args, "preset")})
	if _, err := newActorExecution(context.Background(), a, ctx, &actorQueuedRun{InputJSON: string(runInput), DefinitionSnapshot: string(snapshot)}); err != nil {
		return nil, err
	}
	id := int64ArgLocal(args, "id")
	if id > 0 {
		res, err := ctx.AppDB().Exec(`UPDATE actors_tasks SET name=?,actor_id=?,revision=?,operation=?,preset=?,input_json=? WHERE id=? AND project_id=?`, name, actor.ID, revision, operation, stringArg(args, "preset"), string(inputBytes), id, projectID(ctx))
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return nil, errors.New("task not found")
		}
	} else {
		res, err := ctx.AppDB().Exec(`INSERT INTO actors_tasks(project_id,name,actor_id,revision,operation,preset,input_json) VALUES(?,?,?,?,?,?,?)`, projectID(ctx), name, actor.ID, revision, operation, stringArg(args, "preset"), string(inputBytes))
		if err != nil {
			return nil, err
		}
		id, _ = res.LastInsertId()
	}
	return map[string]any{"task": actorTask{ID: id, Name: name, ActorID: actor.ID, Revision: revision, Operation: operation, Preset: stringArg(args, "preset"), Input: input}}, nil
}

func (a *App) toolTaskList(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	rows, err := ctx.AppDB().Query(`SELECT id,name,actor_id,revision,operation,preset,input_json FROM actors_tasks WHERE project_id=? ORDER BY id DESC LIMIT 500`, projectID(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []actorTask{}
	for rows.Next() {
		var task actorTask
		var raw string
		if err := rows.Scan(&task.ID, &task.Name, &task.ActorID, &task.Revision, &task.Operation, &task.Preset, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &task.Input); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return map[string]any{"tasks": tasks}, rows.Err()
}

func (a *App) toolTaskRun(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	var actorID int64
	var revision int
	var operation, preset, raw string
	err := ctx.AppDB().QueryRow(`SELECT actor_id,revision,operation,preset,input_json FROM actors_tasks WHERE id=? AND project_id=?`, int64ArgLocal(args, "id"), projectID(ctx)).Scan(&actorID, &revision, &operation, &preset, &raw)
	if err != nil {
		return nil, fmt.Errorf("task not found: %w", err)
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return nil, err
	}
	return a.toolActorRun(ctx, map[string]any{"actor_id": actorID, "revision": revision, "operation": operation, "preset": preset, "input": input})
}

func (a *App) toolTaskDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	res, err := ctx.AppDB().Exec(`DELETE FROM actors_tasks WHERE id=? AND project_id=?`, int64ArgLocal(args, "id"), projectID(ctx))
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	return map[string]any{"deleted": n == 1}, nil
}

// Commit each validated page independently so a later failed step does not hide results.
func persistDatasetPage(ctx *sdk.AppCtx, runID int64, items []map[string]any) error {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range items {
		raw, err := json.Marshal(item)
		if err != nil {
			return err
		}
		if len(raw) > maxActorItemBytes {
			return fmt.Errorf("output item exceeds %d bytes", maxActorItemBytes)
		}
		if _, err := tx.Exec(`INSERT INTO actors_dataset_items(project_id,run_id,item_json) VALUES(?,?,?)`, projectID(ctx), runID, string(raw)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) toolDatasetRead(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64ArgLocal(args, "run_id")
	var status string
	if err := ctx.AppDB().QueryRow(`SELECT status FROM actors_runs WHERE id=? AND project_id=?`, id, projectID(ctx)).Scan(&status); err != nil {
		return nil, errors.New("run not found")
	}
	limit := boundedInt(intArg(args, "limit"), 50, 1, 200)
	rows, err := ctx.AppDB().Query(`SELECT id,item_json FROM actors_dataset_items WHERE run_id=? AND project_id=? AND id>? ORDER BY id LIMIT ?`, id, projectID(ctx), int64ArgLocal(args, "after"), limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	cursor := int64ArgLocal(args, "after")
	more := false
	size := 0
	for rows.Next() {
		var rowID int64
		var raw string
		if err := rows.Scan(&rowID, &raw); err != nil {
			return nil, err
		}
		if len(items) == limit || (len(items) > 0 && size+len(raw) > 256*1024) {
			more = true
			break
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
		cursor = rowID
		size += len(raw)
	}
	return map[string]any{"run_id": id, "status": status, "items": items, "next_cursor": cursor, "has_more": more}, rows.Err()
}

func (a *App) platformRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: "GET", Pattern: "/tasks", Handler: a.toolRoute(a.toolTaskList)},
		{Method: "POST", Pattern: "/tasks", Handler: a.toolRoute(a.toolTaskSave)},
		{Method: "POST", Pattern: "/tasks/{id}/run", Handler: a.toolRoute(a.toolTaskRun)},
		{Method: "DELETE", Pattern: "/tasks/{id}", Handler: a.toolRoute(a.toolTaskDelete)},
		{Method: "GET", Pattern: "/runs/{id}/dataset", Handler: a.toolRoute(a.toolDatasetRead)},
	}
}

func (a *App) toolRoute(handler func(*sdk.AppCtx, map[string]any) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, err := actorHTTPContext(r)
		if err != nil {
			httpErr(w, 503, err.Error())
			return
		}
		args := map[string]any{}
		if r.Method == "POST" {
			var ok bool
			args, ok = decodeActorHTTPArgs(w, r)
			if !ok {
				return
			}
		}
		if id := actorHTTPID(r); id > 0 {
			args["id"] = id
			args["run_id"] = id
		}
		for _, key := range []string{"after", "limit"} {
			if value := r.URL.Query().Get(key); value != "" {
				args[key] = value
			}
		}
		out, err := handler(ctx, args)
		writeJSON(w, out, err)
	}
}
