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
	Limits       *actorLimits      `json:"limits,omitempty"`
	ReadOnly     bool              `json:"read_only,omitempty"`
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
	def.ReadOnly = def.ReadOnly || op.ReadOnly
	if op.Limits != nil {
		def.Limits = *op.Limits
	}
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
	if step.Action == "fill" || step.Action == "set_text" {
		args := map[string]any{
			"session_id": e.session.SessionID,
			"action":     "set_text",
			"text":       step.Text,
			"mode":       firstNonEmpty(step.Mode, "replace"),
		}
		if step.NewlineMode != "" {
			args["newline_mode"] = step.NewlineMode
		}
		return e.dispatchSemantic(step, args)
	}
	if step.Action == "set_checked" || step.Action == "select_option" || step.Action == "set_temporal" {
		args := map[string]any{"session_id": e.session.SessionID, "action": step.Action}
		if step.Action == "set_checked" {
			checked, ok := step.Checked.(bool)
			if !ok {
				return errors.New("set_checked requires a boolean checked value")
			}
			args["checked"] = checked
			if step.Labels != nil {
				labels, err := actorLabelList(step.Labels)
				if err != nil {
					return err
				}
				for _, label := range labels {
					if err := e.checkpoint(); err != nil {
						return err
					}
					one := step
					one.Labels = nil
					one.Locator.Text = label
					oneArgs := map[string]any{"session_id": e.session.SessionID, "action": step.Action, "checked": checked}
					if err := e.dispatchSemantic(one, oneArgs); err != nil {
						return err
					}
				}
				return nil
			}
		} else if len(step.Values) > 0 && step.Action == "select_option" {
			args["values"] = step.Values
		} else {
			args["value"] = step.Value
		}
		return e.dispatchSemantic(step, args)
	}
	args := map[string]any{"session_id": e.session.SessionID, "action": step.Action}
	switch step.Action {
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

// dispatchSemantic sends a DOM-targeted Computer action using the current
// semantic observation. It deliberately avoids coordinate fallbacks: media
// controls and composers must be tied to the live SOM target.
func (e *actorExecution) dispatchSemantic(step actorStep, args map[string]any) error {
	locator := step.Locator
	if selector := strings.TrimSpace(locator.Selector); selector != "" && !locator.SOMOnly {
		args["selector"] = selector
		var out map[string]any
		if err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, args), &out); err != nil {
			return err
		}
		e.recordMediaResult(out)
		return e.finishInteraction(out)
	}
	var shot computerSOMScreenshot
	if err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, map[string]any{
		"session_id": e.session.SessionID, "action": "screenshot", "annotate": true, "include_som": true,
	}), &shot); err != nil {
		return fmt.Errorf("observe SOM: %w", err)
	}
	var match *setOfMarkTarget
	for i := range shot.SOM {
		target := &shot.SOM[i]
		if target.Disabled || !somTargetMatches(locator, *target) {
			continue
		}
		if match != nil {
			return fmt.Errorf("ambiguous SOM locator: multiple targets match text=%q role=%q", locator.Text, locator.Role)
		}
		match = target
	}
	if match == nil {
		return fmt.Errorf("locator not found in SOM: text=%q role=%q", locator.Text, locator.Role)
	}
	if match.ID != "" {
		args["target_id"] = match.ID
	} else {
		args["label"] = match.Label
	}
	if shot.SOMRevision != nil {
		args["som_revision"] = shot.SOMRevision
	}
	// Metadata names may differ in whitespace between SOM and the native
	// accessible-name implementation. Keep the fresh target/revision and role
	// guards; the actor must verify the saved selection independently.
	if name := firstNonEmpty(match.AccessibleName, match.Text); name != "" && locator.TextSuffixPattern == "" {
		args["expected_name"] = name
	}
	if match.Role != "" {
		args["expected_role"] = match.Role
	}
	var out map[string]any
	if err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, args), &out); err != nil {
		return err
	}
	e.recordMediaResult(out)
	return e.finishInteraction(out)
}

func (e *actorExecution) upload(step actorStep) error {
	if e.session == nil {
		return errors.New("upload_file requires an open browser")
	}
	args := map[string]any{"session_id": e.session.SessionID, "action": "upload_file"}
	for key, value := range map[string]string{
		"source_url": step.SourceURL, "base64": step.Base64, "file_path": step.FilePath,
		"filename": step.Filename, "mime_type": step.MIMEType,
	} {
		if strings.TrimSpace(value) != "" {
			args[key] = value
		}
	}
	return e.dispatchSemantic(step, args)
}

func (e *actorExecution) recordMediaResult(out map[string]any) {
	uploaded, _ := out["uploaded"].(bool)
	if !uploaded {
		return
	}
	media := map[string]any{"uploaded": true}
	for _, key := range []string{"filename", "size_bytes", "mime_type", "file_source"} {
		if value, ok := out[key]; ok {
			media[key] = value
		}
	}
	e.media = append(e.media, media)
}

func validateWaitStep(step actorStep) error {
	if len(step.Conditions) < 1 || len(step.Conditions) > 8 {
		return errors.New("wait_for requires between 1 and 8 conditions")
	}
	if step.Match != "" && step.Match != "any" && step.Match != "all" {
		return errors.New("wait_for match must be any or all")
	}
	for _, condition := range step.Conditions {
		switch condition.Type {
		case "url_changed", "url_equals", "url_contains", "text_present", "text_absent":
			if strings.TrimSpace(condition.Value) == "" {
				return fmt.Errorf("wait_for %s requires value", condition.Type)
			}
		case "selector_present", "selector_absent":
			if strings.TrimSpace(condition.Selector) == "" {
				return fmt.Errorf("wait_for %s requires selector", condition.Type)
			}
		case "target_present", "target_absent", "target_state":
			if strings.TrimSpace(condition.TargetID) == "" {
				return fmt.Errorf("wait_for %s requires target_id", condition.Type)
			}
			if condition.Type == "target_state" {
				switch condition.State {
				case "ready", "loading", "enabled", "disabled", "checked", "unchecked":
				default:
					return errors.New("wait_for target_state requires a valid state")
				}
			}
		case "media_present", "media_error":
		default:
			return fmt.Errorf("unsupported wait_for condition %q", condition.Type)
		}
	}
	return nil
}

// A structured Computer timeout is a failed actor assertion. Do not continue
// into a publish step when the required player or page state did not appear.
func (e *actorExecution) waitFor(step actorStep) error {
	if e.session == nil {
		return errors.New("wait_for requires an open browser")
	}
	var out map[string]any
	args := map[string]any{
		"session_id": e.session.SessionID, "action": "wait_for",
		"conditions": step.Conditions, "match": firstNonEmpty(step.Match, "any"),
		"timeout_ms": boundedInt(templateInt(step.TimeoutMS), 10000, 500, 30000),
	}
	if err := sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "computer_use", withProjectID(e.ctx, args), &out); err != nil {
		return err
	}
	e.currentURL = firstNonEmpty(stringFromAny(out["current_url"]), e.currentURL)
	if !hostAllowed(e.currentURL, e.definition.AllowedHosts) {
		return fmt.Errorf("browser navigated outside allowed_hosts: %s", e.currentURL)
	}
	matched, _ := out["matched"].(bool)
	timedOut, _ := out["timed_out"].(bool)
	if !matched || timedOut {
		return fmt.Errorf("wait_for required conditions not met (timed_out=%t, media_embed_status=%q)", timedOut, stringFromAny(out["media_embed_status"]))
	}
	if out["media_embed_status"] == "loaded" {
		media := map[string]any{"kind": "embed", "status": "loaded"}
		for from, to := range map[string]string{"media_provider": "provider", "media_iframe_src": "iframe_url", "media_thumbnail_url": "thumbnail_url"} {
			if value := stringFromAny(out[from]); value != "" {
				media[to] = value
			}
		}
		for _, existing := range e.media {
			if existing["kind"] == "embed" && existing["iframe_url"] == media["iframe_url"] {
				return nil
			}
		}
		e.media = append(e.media, media)
	}
	return nil
}

func (a *App) platformTools() []sdk.Tool {
	integer := map[string]any{"type": "integer", "minimum": 1}
	return []sdk.Tool{
		{Name: "actors_task_save", Description: "Create or update a saved task, pinning an actor revision, named operation and input. No execution occurs.", InputSchema: schemaObject(map[string]any{"id": integer, "name": map[string]any{"type": "string"}, "actor_id": integer, "revision": integer, "operation": map[string]any{"type": "string"}, "preset": map[string]any{"type": "string"}, "input": map[string]any{"type": "object"}}, []string{"name", "actor_id"}), Handler: a.toolTaskSave},
		{Name: "actors_task_list", Description: "List saved tasks in the current project.", InputSchema: schemaObject(map[string]any{}, nil), Handler: a.toolTaskList},
		{Name: "actors_task_run", Description: "Queue a saved task using its pinned revision and inputs.", InputSchema: schemaObject(map[string]any{"id": integer}, []string{"id"}), Handler: a.toolTaskRun},
		{Name: "actors_task_delete", Description: "Delete a saved task; actor definitions and historical runs remain.", InputSchema: schemaObject(map[string]any{"id": integer}, []string{"id"}), Handler: a.toolTaskDelete},
		{Name: "actors_dataset_read", Description: "Read committed dataset items for a run, including partial results after failure. Pass next_cursor as after for the next page.", InputSchema: schemaObject(map[string]any{"run_id": integer, "dataset": map[string]any{"type": "string", "description": "Optional named dataset within a crawl run."}, "after": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}, []string{"run_id"}), Handler: a.toolDatasetRead},
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
	var status, snapshot string
	if err := ctx.AppDB().QueryRow(`SELECT status,COALESCE(definition_snapshot_json,'{}') FROM actors_runs WHERE id=? AND project_id=?`, id, projectID(ctx)).Scan(&status, &snapshot); err != nil {
		return nil, errors.New("run not found")
	}
	var definition actorDefinition
	if err := json.Unmarshal([]byte(snapshot), &definition); err != nil {
		return nil, err
	}
	dataset := strings.TrimSpace(stringArg(args, "dataset"))
	datasets := []map[string]any{}
	table := "actors_dataset_items"
	if definition.SchemaVersion == 2 {
		table = "actors_crawl_records"
		counts, err := ctx.AppDB().Query(`SELECT dataset,COUNT(*) FROM actors_crawl_records WHERE run_id=? AND project_id=? GROUP BY dataset ORDER BY dataset`, id, projectID(ctx))
		if err != nil {
			return nil, err
		}
		for counts.Next() {
			var name string
			var count int
			if err := counts.Scan(&name, &count); err != nil {
				counts.Close()
				return nil, err
			}
			datasets = append(datasets, map[string]any{"name": name, "count": count, "schema": definition.Crawl.Datasets[name].Schema})
		}
		err = counts.Err()
		counts.Close()
		if err != nil {
			return nil, err
		}
	} else {
		var count int
		if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM actors_dataset_items WHERE run_id=? AND project_id=?`, id, projectID(ctx)).Scan(&count); err != nil {
			return nil, err
		}
		datasets = append(datasets, map[string]any{"name": "default", "count": count, "schema": definition.OutputSchema})
		if dataset != "" && dataset != "default" {
			return nil, errors.New("dataset not found in run")
		}
	}
	total := 0
	found := dataset == ""
	for _, entry := range datasets {
		if dataset == "" || entry["name"] == dataset {
			total += entry["count"].(int)
			found = true
		}
	}
	if !found {
		return nil, errors.New("dataset not found in run")
	}
	limit := boundedInt(intArg(args, "limit"), 50, 1, 200)
	query := "SELECT id,item_json FROM " + table + " WHERE run_id=? AND project_id=? AND id>?"
	params := []any{id, projectID(ctx), int64ArgLocal(args, "after")}
	if definition.SchemaVersion == 2 && dataset != "" {
		query += " AND dataset=?"
		params = append(params, dataset)
	}
	query += " ORDER BY id LIMIT ?"
	params = append(params, limit+1)
	rows, err := ctx.AppDB().Query(query, params...)
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
	return map[string]any{"run_id": id, "status": status, "items": items, "next_cursor": cursor, "has_more": more, "dataset": dataset, "datasets": datasets, "total": total}, rows.Err()
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
		for _, key := range []string{"after", "limit", "dataset"} {
			if value := r.URL.Query().Get(key); value != "" {
				args[key] = value
			}
		}
		out, err := handler(ctx, args)
		writeJSON(w, out, err)
	}
}
