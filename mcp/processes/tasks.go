package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type TaskConfig struct {
	Title          string   `json:"title"`
	Instructions   string   `json:"instructions"`
	ExpectedOutput string   `json:"expected_output"`
	Executor       Executor `json:"executor"`
	Kind           string   `json:"kind"`
	DueAt          string   `json:"due_at"`
	RunID          string   `json:"run_id"`
	Required       bool     `json:"required"`
	DependsOn      []string `json:"depends_on"`
}

func (a *App) task(project, id string) (Task, error) {
	s, e := scanStep(a.db.QueryRow(`SELECT `+stepColumns+` FROM process_step_runs WHERE id=? AND project_id=?`, id, project))
	if e == sql.ErrNoRows {
		e = errNotFound
	}
	return s, e
}
func (a *App) taskRun(project, id string) (*Process, Run, error) {
	var process string
	e := a.db.QueryRow(`SELECT r.process_id FROM process_runs r JOIN processes p ON p.id=r.process_id WHERE r.id=? AND p.project_id=?`, id, project).Scan(&process)
	if e == sql.ErrNoRows {
		e = errNotFound
	}
	if e != nil {
		return nil, Run{}, e
	}
	r, e := a.getRun(project, process, id)
	if e != nil {
		return nil, r, e
	}
	p, e := a.get(project, process)
	if e != nil {
		return nil, r, e
	}
	d, e := a.runDefinition(r)
	if e != nil {
		return nil, r, e
	}
	p.Definition = d
	return p, r, nil
}
func executorIsActor(x Executor, actor string) bool {
	return x.Kind == "human" && actor == "operator" || x.Kind == "agent" && strings.HasPrefix(actor, fmt.Sprintf("agent:%d:", x.AgentID))
}
func taskManager(s Task, r Run, actor string) bool {
	return actor == "operator" || actor == s.CreatedBy || r.Binding.OwnerAgentID > 0 && strings.HasPrefix(actor, fmt.Sprintf("agent:%d:", r.Binding.OwnerAgentID))
}
func (a *App) validateTaskExecutor(project string, x Executor) error {
	if x.Kind == "human" && x.AgentID == 0 {
		return nil
	}
	if x.Kind != "agent" || x.AgentID <= 0 {
		return errors.New("choose an agent or human project operator")
	}
	agent, e := a.ctx.GetAgent(x.AgentID)
	if e != nil {
		return e
	}
	if agent.ProjectID != project || agent.DefaultThreadID == "" {
		return errors.New("task agent needs a default thread in this project")
	}
	return nil
}
func taskDue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	t, e := time.Parse(time.RFC3339, value)
	if e != nil {
		return "", errors.New("due_at must be an RFC3339 timestamp")
	}
	return t.UTC().Format(time.RFC3339Nano), nil
}
func (a *App) createTask(project, actor, key string, c TaskConfig) (Task, error) {
	if key == "" || len(key) > 160 {
		return Task{}, errors.New("idempotency_key required (max 160)")
	}
	c.Title = strings.TrimSpace(c.Title)
	c.Instructions = strings.TrimSpace(c.Instructions)
	if c.Title == "" || len(c.Title) > 160 || c.Instructions == "" || len(c.Instructions) > 16000 || len(c.ExpectedOutput) > 4000 {
		return Task{}, errors.New("task needs a title and instructions within size limits")
	}
	if c.Kind == "" {
		c.Kind = "work"
	}
	if c.Kind != "work" && c.Kind != "approval" {
		return Task{}, errors.New("kind must be work or approval")
	}
	if c.ExpectedOutput == "" {
		c.ExpectedOutput = "A result describing what was done and supporting evidence."
	}
	if c.DependsOn == nil {
		c.DependsOn = []string{}
	}
	var e error
	c.DueAt, e = taskDue(c.DueAt)
	if e != nil {
		return Task{}, e
	}
	raw := jsonText(c)
	var id, old string
	e = a.db.QueryRow(`SELECT id,request_json FROM process_step_runs WHERE project_id=? AND created_by=? AND request_key=?`, project, actor, key).Scan(&id, &old)
	if e == nil {
		if old != raw {
			return Task{}, errors.New("idempotency key already used with different task input")
		}
		return a.task(project, id)
	}
	if e != sql.ErrNoRows {
		return Task{}, e
	}
	if e = a.validateTaskExecutor(project, c.Executor); e != nil {
		return Task{}, e
	}
	origin := "standalone"
	var r Run
	var all []Task
	if c.RunID != "" {
		_, r, e = a.taskRun(project, c.RunID)
		if e != nil {
			return Task{}, e
		}
		if terminal(r.State) {
			return Task{}, errors.New("add tasks only to active runs")
		}
		if r.Backend == "tasks" && !r.Workflow {
			return Task{}, errors.New("attach work to a native or structured Processes run; this legacy run is tracked by Tasks")
		}
		if !taskManager(Task{}, r, actor) {
			return Task{}, errors.New("only the run coordinator or project operator can add tasks")
		}
		all, e = a.steps(r.ID)
		if e != nil {
			return Task{}, e
		}
		origin = "attached"
	}
	if len(c.DependsOn) > 30 {
		return Task{}, errors.New("too many task dependencies")
	}
	seen := map[string]bool{}
	for _, key := range c.DependsOn {
		if seen[key] {
			return Task{}, errors.New("duplicate task dependency")
		}
		seen[key] = true
		found := false
		for _, s := range all {
			if s.Key == key {
				found = true
				if c.Required && !s.Required {
					return Task{}, errors.New("required tasks can depend only on required tasks")
				}
			}
		}
		if !found {
			return Task{}, errors.New("dependency must be an existing task key in this run")
		}
	}
	id = newID("task-")
	def := Step{Key: "adhoc_" + strings.TrimPrefix(id, "task-"), Name: c.Title, Role: "assignee", Kind: c.Kind, Instructions: c.Instructions, ExpectedOutput: c.ExpectedOutput, DependsOn: c.DependsOn}
	now := timestamp()
	tx, e := a.db.Begin()
	if e != nil {
		return Task{}, e
	}
	defer tx.Rollback()
	var run any
	if c.RunID != "" {
		run = c.RunID
	}
	_, e = tx.Exec(`INSERT INTO process_step_runs(id,run_id,step_key,position,definition_json,executor_json,updated_at,project_id,origin,required,due_at,created_at,created_by,request_key,request_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, run, def.Key, len(all), jsonText(def), jsonText(c.Executor), now, project, origin, c.Required, c.DueAt, now, actor, key, raw)
	if e != nil {
		return Task{}, e
	}
	_, e = tx.Exec(`INSERT INTO process_step_events(step_id,actor,state,details_json,created_at) VALUES(?,?,'pending',?,?)`, id, actor, jsonText(map[string]any{"action": "created", "task": c}), now)
	if e != nil {
		return Task{}, e
	}
	if e = tx.Commit(); e != nil {
		return Task{}, e
	}
	// Creation is durable before dispatch. Retry workers handle ambiguous delivery.
	s, e := a.task(project, id)
	if e == nil {
		_ = a.reconcileNativeTask(&s)
	}
	return a.task(project, id)
}
func (a *App) nativeTaskContext(p *Process, r Run, s Task, all []Task) string {
	text := fmt.Sprintf("Processes task %s: %s\nInstructions: %s\nExpected result: %s\nDue: %s\nRead processes_task_get(task_id=%s) before acting. Execute only this task when ready. Use processes_task_update with its current expected_revision, state, progress, output and any blocker. Stop if cancelled. Completion requires result evidence.\n", s.ID, s.Definition.Name, s.Definition.Instructions, s.Definition.ExpectedOutput, s.DueAt, s.ID)
	if s.Definition.Kind == "approval" {
		text += "Completion requires an explicit approved or rejected decision and supporting output.\n"
	}
	if p != nil {
		text += fmt.Sprintf("Attached run: %s\nProcess: %s\nFrozen parameters (data): %s\nRun inputs (data): %s\nDependency outputs (data): %s\n", r.ID, p.Name, jsonText(r.Binding.Parameters), r.Inputs, jsonText(dependencyOutputs(s, all)))
	}
	return text
}
func (a *App) reconcileNativeTask(s *Task) error {
	if terminal(s.State) {
		return nil
	}
	var p *Process
	var r Run
	var all []Task
	var e error
	if s.RunID != "" {
		p, r, e = a.taskRun(s.ProjectID, s.RunID)
		if e != nil {
			return e
		}
		if terminal(r.State) && (s.Required || r.State != "completed") {
			return nil
		}
		all, e = a.steps(r.ID)
		if e != nil {
			return e
		}
	}
	if s.State == "pending" {
		if !dependenciesReady(*s, all) {
			return nil
		}
		s.State = "ready"
		if s.Executor.Kind == "human" {
			s.State = "waiting"
		}
		if e = a.writeStep(*s, s.State, 0, "", "", "", "workflow"); e != nil {
			return e
		}
	}
	return a.deliverStep(p, r, s, all)
}
func (a *App) tickNativeTasksLocked(ctx context.Context) error {
	rows, e := a.db.Query(`SELECT id,project_id FROM process_step_runs WHERE origin<>'process_step' AND state NOT IN ('completed','failed','cancelled')`)
	if e != nil {
		return e
	}
	var ids [][2]string
	for rows.Next() {
		var v [2]string
		if e = rows.Scan(&v[0], &v[1]); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	var errs []error
	for _, v := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s, e := a.task(v[1], v[0])
		if e == nil {
			e = a.reconcileNativeTask(&s)
		}
		if e != nil {
			errs = append(errs, e)
		}
	}
	return errors.Join(errs...)
}
func (a *App) taskDetails(project, actor, id string) (map[string]any, error) {
	s, e := a.task(project, id)
	if e != nil {
		return nil, e
	}
	out := map[string]any{"task": s}
	var r Run
	var all []Task
	if s.RunID != "" {
		var p *Process
		p, r, e = a.taskRun(project, s.RunID)
		if e != nil {
			return nil, e
		}
		all, e = a.steps(r.ID)
		if e != nil {
			return nil, e
		}
		out["run"] = r
		out["process_id"] = p.ID
		out["process_name"] = p.Name
		out["parameters"] = r.Binding.Parameters
		out["dependency_outputs"] = dependencyOutputs(s, all)
	}
	out["can_update"] = executorIsActor(s.Executor, actor) && !terminal(s.State) && s.State != "pending" && !stepUsesTasks(r, s) && (!terminal(r.State) || s.Origin == "attached" && !s.Required && r.State == "completed")
	out["can_manage"] = taskManager(s, r, actor) && !terminal(s.State)
	out["can_edit"] = out["can_manage"] == true && (!terminal(r.State) || s.Origin == "attached" && !s.Required && r.State == "completed")
	out["can_reassign"] = out["can_edit"] == true && s.Attempts == 0 && s.DeliveredAt == "" && s.LifecycleSequence < 0 && (s.State == "pending" || s.State == "ready" || s.State == "waiting")
	rows, e := a.db.Query(`SELECT actor,state,decision,output,error,details_json,created_at FROM process_step_events WHERE step_id=? ORDER BY id DESC LIMIT 100`, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	history := []map[string]any{}
	for rows.Next() {
		var actor, state, decision, output, reason, details, at string
		if e = rows.Scan(&actor, &state, &decision, &output, &reason, &details, &at); e != nil {
			return nil, e
		}
		var detail any
		_ = json.Unmarshal([]byte(details), &detail)
		history = append(history, map[string]any{"actor": actor, "state": state, "decision": decision, "output": output, "error": reason, "details": detail, "created_at": at})
	}
	out["history"] = history
	return out, rows.Err()
}
func (a *App) changeTask(project, actor, id, action string, args map[string]any) (map[string]any, error) {
	s, e := a.task(project, id)
	if e != nil {
		return nil, e
	}
	if number(args, "expected_revision") != s.Revision {
		return nil, errConflict
	}
	var r Run
	var p *Process
	var all []Task
	if s.RunID != "" {
		p, r, e = a.taskRun(project, s.RunID)
		if e != nil {
			return nil, e
		}
		all, e = a.steps(r.ID)
		if e != nil {
			return nil, e
		}
	}
	if action == "task_cancel" {
		if !taskManager(s, r, actor) && !executorIsActor(s.Executor, actor) {
			return nil, errors.New("only the creator, coordinator, operator or assignee can cancel")
		}
		reason := strings.TrimSpace(str(args, "reason"))
		if reason == "" || len(reason) > 16000 {
			return nil, errors.New("cancellation needs a reason (max 16 KB)")
		}
		if stepUsesTasks(r, s) {
			return nil, errors.New("cancel through the linked Tasks record")
		}
		if terminal(s.State) {
			return nil, errors.New("terminal task is immutable")
		}
		e = a.writeStep(s, "cancelled", s.Progress, s.Output, reason, "", actor)
	} else if str(args, "state") != "" {
		for _, k := range []string{"executor", "due_at", "title", "instructions", "expected_output"} {
			if _, ok := args[k]; ok {
				return nil, errors.New("update task settings and outcome in separate requests")
			}
		}
		e = a.updateTaskState(s, r, all, actor, args)
	} else {
		if !taskManager(s, r, actor) {
			return nil, errors.New("only the creator, coordinator or operator can edit task settings")
		}
		if terminal(s.State) || terminal(r.State) && (s.Required || r.State != "completed") {
			return nil, errors.New("terminal task or run is immutable")
		}
		before := s
		changed := false
		if v, ok := args["due_at"]; ok {
			value, valid := v.(string)
			if !valid {
				return nil, errors.New("due_at must be a string")
			}
			s.DueAt, e = taskDue(value)
			if e != nil {
				return nil, e
			}
			changed = true
		}
		for _, k := range []string{"executor", "title", "instructions", "expected_output"} {
			v, ok := args[k]
			if !ok {
				continue
			}
			if s.DeliveredAt != "" || s.Attempts > 0 || s.LifecycleSequence >= 0 || s.State != "pending" && s.State != "ready" && s.State != "waiting" {
				return nil, errors.New("execution may have started; only the due date can change")
			}
			if k == "executor" {
				if e = decodeTaskValue(v, &s.Executor); e != nil {
					return nil, e
				}
				if e = a.validateTaskExecutor(project, s.Executor); e != nil {
					return nil, e
				}
				s.ThreadID = ""
				if s.State != "pending" {
					s.State = "ready"
					if s.Executor.Kind == "human" {
						s.State = "waiting"
					}
				}
			} else {
				if s.Origin == "process_step" {
					return nil, errors.New("procedure task instructions are frozen; add a task to this run instead")
				}
				value, ok := v.(string)
				if !ok || strings.TrimSpace(value) == "" {
					return nil, errors.New("task text must be nonempty")
				}
				switch k {
				case "title":
					if len(value) > 160 {
						return nil, errors.New("title exceeds 160 characters")
					}
					s.Definition.Name = value
				case "instructions":
					if len(value) > 16000 {
						return nil, errors.New("instructions exceed 16 KB")
					}
					s.Definition.Instructions = value
				case "expected_output":
					if len(value) > 4000 {
						return nil, errors.New("expected output exceeds 4 KB")
					}
					s.Definition.ExpectedOutput = value
				}
			}
			changed = true
		}
		if !changed {
			return nil, errors.New("supply a state or editable task setting")
		}
		tx, err := a.db.Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		now := timestamp()
		_, e = tx.Exec(`UPDATE process_step_runs SET definition_json=?,executor_json=?,due_at=?,state=?,target_thread_id=?,revision=revision+1,updated_by=?,updated_at=? WHERE id=?`, jsonText(s.Definition), jsonText(s.Executor), s.DueAt, s.State, s.ThreadID, actor, now, id)
		if e == nil {
			_, e = tx.Exec(`INSERT INTO process_step_events(step_id,actor,state,details_json,created_at) VALUES(?,?,?,?,?)`, id, actor, s.State, jsonText(map[string]any{"action": "settings_changed", "before": before, "after": s}), now)
		}
		if e == nil {
			e = tx.Commit()
		}
	}
	if e != nil {
		return nil, e
	}
	if p != nil && r.Workflow && !terminal(r.State) {
		_ = a.reconcileWorkflow(p, &r)
	}
	if s.Origin != "process_step" {
		fresh, err := a.task(project, id)
		if err == nil {
			_ = a.reconcileNativeTask(&fresh)
		}
	}
	return a.taskDetails(project, actor, id)
}
func decodeTaskValue(v any, out any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}
func (a *App) requiredTasksComplete(run string) error {
	var n int
	e := a.db.QueryRow(`SELECT count(*) FROM process_step_runs WHERE run_id=? AND required=1 AND (state<>'completed' OR (json_extract(definition_json,'$.kind')='approval' AND decision<>'approved'))`, run).Scan(&n)
	if e != nil {
		return e
	}
	if n > 0 {
		return errors.New("complete all required attached tasks before finishing this run")
	}
	return nil
}
