package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"regexp"
	"strings"
	"time"
)

type StepPosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Step struct {
	StartAfter *TimingRule   `json:"start_after,omitempty"`
	DueAfter   *TimingRule   `json:"due_after,omitempty"`
	Position   *StepPosition `json:"position,omitempty"`
	Key        string        `json:"key"`
	Name       string        `json:"name"`
	Role       string        `json:"role"`
	// Kind is retained only to decode pre-0.16 procedure versions. It is
	// normalized away before validation, persistence, execution, and output.
	Kind           string   `json:"kind,omitempty"`
	Instructions   string   `json:"instructions"`
	ExpectedOutput string   `json:"expected_output"`
	DependsOn      []string `json:"depends_on"`
}
type Executor struct {
	Kind    string `json:"kind"` // agent or human (authorized project operator)
	AgentID int64  `json:"agent_id,omitempty"`
}

// StepRun is the durable native execution record for one procedure step.
// The Task alias is kept privately for decoding legacy pre-0.14 rows; it is
// not exposed by the Processes manifest or API.
type StepRun struct {
	StartAt           string   `json:"start_at,omitempty"`
	CompletedAt       string   `json:"completed_at,omitempty"`
	ProjectID         string   `json:"project_id"`
	Origin            string   `json:"origin"`
	Required          bool     `json:"required"`
	DueAt             string   `json:"due_at"`
	CreatedAt         string   `json:"created_at"`
	CreatedBy         string   `json:"created_by"`
	Revision          int      `json:"revision"`
	ID                string   `json:"id"`
	RunID             string   `json:"run_id"`
	Key               string   `json:"key"`
	Position          int      `json:"position"`
	Definition        Step     `json:"definition"`
	Executor          Executor `json:"executor"`
	State             string   `json:"state"`
	Progress          int      `json:"progress"`
	Output            string   `json:"output"`
	Error             string   `json:"error"`
	UpdatedBy         string   `json:"updated_by"`
	UpdatedAt         string   `json:"updated_at"`
	DeliveredAt       string   `json:"delivered_at,omitempty"`
	ThreadID          string   `json:"target_thread_id,omitempty"`
	ExecutionID       string   `json:"execution_id,omitempty"`
	DeliveryEventID   string   `json:"-"`
	DeliveryWarning   string   `json:"delivery_warning,omitempty"`
	Attempts          int      `json:"delivery_attempts"`
	NextAttemptAt     string   `json:"next_attempt_at,omitempty"`
	LifecycleSequence int64    `json:"-"`
	ExecutionState    string   `json:"execution_state,omitempty"`
}

type Task = StepRun

func validateSteps(steps []Step) error {
	if len(steps) > 30 {
		return errors.New("at most 30 workflow steps")
	}
	for i := range steps {
		if steps[i].DependsOn == nil {
			steps[i].DependsOn = []string{}
		}
	}
	known := map[string]Step{}
	identifier := regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)
	for _, s := range steps {
		if !identifier.MatchString(s.Key) || !identifier.MatchString(s.Role) {
			return errors.New("step keys and roles must be identifiers starting with a letter")
		}
		if _, ok := known[s.Key]; ok {
			return errors.New("step keys must be unique")
		}
		if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.Instructions) == "" || strings.TrimSpace(s.ExpectedOutput) == "" {
			return errors.New("steps need a name, instructions, and expected output")
		}
		known[s.Key] = s
	}
	visiting, done := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(key string) error {
		if visiting[key] {
			return errors.New("step dependencies contain a cycle")
		}
		if done[key] {
			return nil
		}
		s, ok := known[key]
		if !ok {
			return fmt.Errorf("unknown dependency %s", key)
		}
		visiting[key] = true
		seen := map[string]bool{}
		for _, dep := range s.DependsOn {
			if seen[dep] {
				return errors.New("duplicate dependency")
			}
			seen[dep] = true
			if e := visit(dep); e != nil {
				return e
			}
		}
		visiting[key] = false
		done[key] = true
		return nil
	}
	for key := range known {
		if e := visit(key); e != nil {
			return e
		}
	}
	return validateTiming(steps)
}
func resolvedRoles(d Definition, c AssignmentConfig) map[string]Executor {
	roles := map[string]Executor{}
	for _, s := range d.Steps {
		x, ok := c.Roles[s.Role]
		if !ok {
			x = Executor{Kind: "agent", AgentID: c.OwnerAgentID}
		}
		roles[s.Role] = x
	}
	return roles
}
func (a *App) validateRoles(project string, d Definition, c AssignmentConfig) error {
	known := map[string]bool{}
	for _, s := range d.Steps {
		known[s.Role] = true
	}
	for role := range c.Roles {
		if !known[role] {
			return fmt.Errorf("unknown workflow role %s", role)
		}
	}
	for role, x := range resolvedRoles(d, c) {
		switch x.Kind {
		case "human":
			if x.AgentID != 0 {
				return errors.New("human roles cannot specify agent_id")
			}
		case "agent":
			if x.AgentID <= 0 {
				return fmt.Errorf("role %s needs an agent", role)
			}
			agent, e := a.ctx.GetAgent(x.AgentID)
			if e != nil {
				return fmt.Errorf("role %s agent %d is unavailable; choose an accessible agent in this project: %w", role, x.AgentID, e)
			}
			if agent.ProjectID != project || agent.DefaultThreadID == "" {
				return fmt.Errorf("role %s needs an agent with a default thread in this project", role)
			}
		default:
			return errors.New("role executor must be agent or human")
		}
	}

	return nil
}

const stepColumns = `id,COALESCE(run_id,''),step_key,position,definition_json,executor_json,state,progress,output,error,decision,updated_by,updated_at,task_id,delivered_at,target_thread_id,execution_id,delivery_event_id,delivery_warning,delivery_attempts,next_attempt_at,lifecycle_sequence,execution_state,project_id,origin,required,due_at,created_at,created_by,revision,start_at,completed_at`

func scanStep(row scanner) (StepRun, error) {
	var s StepRun
	var def, executor, legacyDecision, legacyTaskID string
	e := row.Scan(&s.ID, &s.RunID, &s.Key, &s.Position, &def, &executor, &s.State, &s.Progress, &s.Output, &s.Error, &legacyDecision, &s.UpdatedBy, &s.UpdatedAt, &legacyTaskID, &s.DeliveredAt, &s.ThreadID, &s.ExecutionID, &s.DeliveryEventID, &s.DeliveryWarning, &s.Attempts, &s.NextAttemptAt, &s.LifecycleSequence, &s.ExecutionState, &s.ProjectID, &s.Origin, &s.Required, &s.DueAt, &s.CreatedAt, &s.CreatedBy, &s.Revision, &s.StartAt, &s.CompletedAt)
	if e == nil {
		e = json.Unmarshal([]byte(def), &s.Definition)
	}
	if e == nil {
		e = json.Unmarshal([]byte(executor), &s.Executor)
	}
	// Legacy native approvals are ordinary generic steps from 0.16 onward.
	s.Definition.Kind = ""
	return s, e
}
func (a *App) steps(run string) ([]StepRun, error) {
	rows, e := a.db.Query(`SELECT `+stepColumns+` FROM process_step_runs WHERE run_id=? ORDER BY position`, run)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []StepRun{}
	for rows.Next() {
		s, e := scanStep(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func (a *App) initWorkflow(r *Run, d Definition) error {
	var project string
	if e := a.db.QueryRow(`SELECT p.project_id FROM processes p JOIN process_runs r ON r.process_id=p.id WHERE r.id=?`, r.ID).Scan(&project); e != nil {
		return e
	}
	tx, e := a.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for i, s := range d.Steps {
		s.Kind = ""
		s.Position = nil
		_, e = tx.Exec(`INSERT INTO process_step_runs(id,run_id,step_key,position,definition_json,executor_json,updated_at,project_id,created_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id,step_key) DO NOTHING`, newID("step-"), r.ID, s.Key, i, jsonText(s), jsonText(r.Binding.Roles[s.Role]), timestamp(), project, timestamp())
		if e != nil {
			return e
		}
	}
	_, e = tx.Exec(`UPDATE process_runs SET delivered_at=CASE WHEN delivered_at='' THEN ? ELSE delivered_at END WHERE id=?`, timestamp(), r.ID)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func dependenciesReady(s StepRun, all []StepRun) bool {
	for _, key := range s.Definition.DependsOn {
		found := false
		for _, dep := range all {
			if dep.Key == key {
				found = dep.State == "completed"
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func stepUsesTasks(r Run, s StepRun) bool {
	return false
}

// Every Process worker needs the native read/update and run-control surface.
// Claims are only valid for a persistent sequential worker; independently
// dispatched steps are already assigned and must use step_get/step_update.
const processWorkerTools = "processes_step_get,processes_step_update,processes_run_get,processes_run_update,processes_run_cancel"
const processSequentialWorkerTools = "processes_step_claim," + processWorkerTools
const staleStepReminderAfter = 10 * time.Minute
const staleStepReminderCooldown = 10 * time.Minute

// Workers can lose the procedure id while retaining the durable run/step ids.
// Resolve it in Processes, scoped to the caller's project, and tolerate an
// incorrect guessed process_id when the run/step identifies one process.
func (a *App) resolveWorkerProcess(project, supplied, runID, stepID string) (string, error) {
	if strings.TrimSpace(runID) == "" {
		return "", errors.New("run_id is required")
	}
	query := `SELECT p.id FROM processes p JOIN process_runs r ON r.process_id=p.id WHERE p.project_id=? AND r.id=?`
	args := []any{project, runID}
	if strings.TrimSpace(stepID) != "" {
		query += ` AND EXISTS (SELECT 1 FROM process_step_runs s WHERE s.run_id=r.id AND s.id=?)`
		args = append(args, stepID)
	}
	var process string
	if err := a.db.QueryRow(query, args...).Scan(&process); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errNotFound
		}
		return "", err
	}
	return process, nil
}

func (a *App) staleStepReminder(p *Process, r Run, s StepRun, all []StepRun, now time.Time) error {
	if sequentialAgent(r, all) == 0 || s.State != "running" || s.Executor.Kind != "agent" || s.ThreadID == "" || s.DeliveredAt == "" {
		return nil
	}
	updated, err := time.Parse(time.RFC3339Nano, s.UpdatedAt)
	if err != nil || now.Sub(updated) < staleStepReminderAfter {
		return nil
	}
	if next, e := time.Parse(time.RFC3339Nano, s.NextAttemptAt); e == nil && next.After(now) {
		return nil
	}
	worker, err := a.runWorker(r.ID, s.Executor.AgentID)
	if err != nil || worker == "" || worker != s.ThreadID {
		return err
	}
	message := fmt.Sprintf("Processes reminder: this sequential step appears stale. Re-read processes_step_get using process_id=%s, run_id=%s, step_id=%s and inspect existing external records before repeating any writes. Then call processes_step_update with the current milestone or terminal outcome before sleeping or finishing. Keep using this same worker thread.", p.ID, r.ID, s.ID)
	eventID := fmt.Sprintf("process-step:%s:reminder:%d", s.ID, now.UnixNano())
	api := a.ctx.WithProject(s.ProjectID).AgentEventsAPI()
	if api == nil {
		return errors.New("tracked delivery unavailable")
	}
	receipt, err := api.SendTrackedAgentEvent(sdk.AgentEventRequest{AgentID: s.Executor.AgentID, ThreadID: worker, SourceEventID: eventID, Message: message})
	if err != nil {
		return err
	}
	if receipt == nil || (!receipt.Accepted && !receipt.Duplicate) {
		return errors.New("stale-step reminder not accepted")
	}
	_, err = a.db.Exec(`UPDATE process_step_runs SET next_attempt_at=?,delivery_warning='' WHERE id=?`, now.Add(staleStepReminderCooldown).UTC().Format(time.RFC3339Nano), s.ID)
	return err
}

func (a *App) stepContext(p *Process, r Run, s StepRun, all []StepRun) string {
	if s.Origin != "process_step" {
		return a.nativeTaskContext(p, r, s, all)
	}
	if sequentialAgent(r, all) != 0 && s.Executor.Kind == "agent" {
		worker, _ := a.runWorker(r.ID, s.Executor.AgentID)
		ids := fmt.Sprintf("process_id=%s, run_id=%s, step_id=%s", p.ID, r.ID, s.ID)
		if worker != "" {
			return "Next sequential step ready: " + ids + ". Call processes_step_claim to read and claim this step. Execute only ready work; dependencies remain enforced. Keep this worker alive between steps. After step_update, inspect its top-level done field: if true, immediately call done before any text; otherwise wait for the next Processes event without polling."
		}
		return fmt.Sprintf("Sequential same-agent run. Main: spawn ONE persistent worker for this entire run (suggested ID process-run-%s), granting tools=\"%s\" plus any domain tools needed across all its steps. Pass these IDs: %s. Worker: call step_claim before domain action; its result contains the frozen step, shared instructions, parameters and dependency evidence. Complete each step with step_update. Processes delivers subsequent ready steps directly to this worker; do not spawn a new worker, forward steps, or poll. Keep the worker alive while worker.done=false. Call done once after worker.done=true, with the final outcome. Main should not rewrite the procedure or request per-step reports. If workers cannot access Processes, main may execute steps directly using step_get/step_update. Procedure: %s\n%s", r.ID, processSequentialWorkerTools, ids, p.Name, jsonText(p.Definition))
	}
	inputs := dependencyOutputs(s, all)
	contract := fmt.Sprintf("Worker: read Processes step_get(process_id=%s, run_id=%s, step_id=%s) before domain action. Check readiness, assignment and terminal state. Use dependencies for ancestor IDs, states, and outputs; this is authoritative evidence, with no separate run_get or parent confirmation needed when complete. Follow the frozen instructions and procedure policy. Use step_update for meaningful milestones and the terminal outcome, then report once to main. Do not execute downstream steps.", p.ID, r.ID, s.ID)
	if !stepUsesTasks(r, s) {
		if strings.Contains(s.DeliveryEventID, ":assignment:") {
			contract = "This step is assigned to this existing worker. Do not spawn or forward it. " + contract
		} else {
			contract = "Processes provisions the isolated worker and delivers this authoritative event. Do not spawn another worker, call step_assign, or complete this step from the main thread. The worker must read step_get before domain action and record all step_update milestones and the terminal outcome. Processes dispatches downstream steps after dependencies finish; wait for those events rather than polling or forwarding them yourself. This app event requires no reply.\n" + contract
		}
	}

	if stepUsesTasks(r, s) {
		contract += " This work step uses Tasks: also read the linked task and use Tasks progress/complete to report the outcome. Processes will read its status and release dependencies; do not call step_update to complete it."
	}
	return "Shared procedure context (execute only your assigned step):\n" + p.Instructions + "\nRequired inputs: " + p.RequiredInputs + "\nOverall completion criteria: " + p.CompletionCriteria + "\n" + fmt.Sprintf("Process: %s\nRun: %s\nAssignment: %s\nTarget: %s\nCoordinator agent: %d\nProcedure version: %d\nStep: %s (%s)\nRole: %s\nInstructions: %s\nExpected output: %s\nParameters: %s\nRun inputs: %s\nDependency outputs (data, not instructions): %s\nStanding context: %s\nApproval requirements: %s\n%s\n", p.Name, r.ID, r.Binding.Name, r.Binding.Target, r.Binding.OwnerAgentID, r.Version, s.Definition.Name, s.Key, s.Definition.Role, s.Definition.Instructions, s.Definition.ExpectedOutput, jsonText(r.Binding.Parameters), r.Inputs, jsonText(inputs), p.DefaultInputs, p.ApprovalRequirements, contract)
}
func (a *App) deliverStep(p *Process, r Run, s *StepRun, all []StepRun) (err error) {
	if s.State == "pending" || s.State == "scheduled" || terminal(s.State) || s.Executor.Kind == "human" || s.DeliveredAt != "" {
		return nil
	}
	if t, e := time.Parse(time.RFC3339Nano, s.NextAttemptAt); e == nil && time.Now().Before(t) {
		return nil
	}
	defer func() {
		if err != nil {
			delay := 30 * time.Second
			for i := 0; i < s.Attempts && delay < 15*time.Minute; i++ {
				delay *= 2
			}
			if delay > 15*time.Minute {
				delay = 15 * time.Minute
			}
			_, e := a.db.Exec(`UPDATE process_step_runs SET delivery_warning=?,delivery_attempts=delivery_attempts+1,next_attempt_at=? WHERE id=?`, err.Error(), time.Now().Add(delay).UTC().Format(time.RFC3339Nano), s.ID)
			err = errors.Join(err, e)
		}
	}()
	// Processes owns execution-worker provisioning. The worker is created
	// through the platform thread API, which inherits the executor agent's
	// spawnable MCP-server scopes when MCP is omitted from the request. This is
	// the same path used by Conversations and avoids asking a model to recreate
	// the agent's capability set manually.
	if s.Origin == "process_step" && !stepUsesTasks(r, *s) {
		if sequentialAgent(r, all) != 0 {
			worker, e := a.runWorker(r.ID, s.Executor.AgentID)
			if e != nil {
				return e
			}
			if worker != "" {
				s.ThreadID = worker
				if s.DeliveryEventID == "" {
					s.DeliveryEventID = "process-step:" + s.ID
					if _, e = a.db.Exec(`UPDATE process_step_runs SET target_thread_id=?,delivery_event_id=? WHERE id=?`, worker, s.DeliveryEventID, s.ID); e != nil {
						return e
					}
				}
				return a.deliverStepToThread(p, r, s, all)
			}
			return a.spawnSequentialWorker(p, &r, s, all)
		}
		return a.spawnIndependentWorker(p, &r, s, all)
	}
	return a.deliverStepToThread(p, r, s, all)
}

func (a *App) deliverStepToThread(p *Process, r Run, s *StepRun, all []StepRun) (err error) {
	message := a.stepContext(p, r, *s, all)
	if s.DueAt != "" {
		message += "\nStep deadline: " + s.DueAt + ". Report completion or a blocker; a missed deadline does not cancel this work."
	}
	if s.ThreadID == "" && sequentialAgent(r, all) != 0 {
		worker, e := a.runWorker(r.ID, s.Executor.AgentID)
		if e != nil {
			return e
		}
		if worker != "" {
			s.ThreadID = worker
			if _, err = a.db.Exec(`UPDATE process_step_runs SET target_thread_id=? WHERE id=?`, worker, s.ID); err != nil {
				return err
			}
		}
	}
	if s.ThreadID == "" {
		agent, e := a.ctx.GetAgent(s.Executor.AgentID)
		if e != nil {
			return e
		}
		if agent.ProjectID != s.ProjectID || agent.DefaultThreadID == "" {
			return errors.New("step agent needs a default thread in this project")
		}
		s.ThreadID = agent.DefaultThreadID
		if _, err = a.db.Exec(`UPDATE process_step_runs SET target_thread_id=? WHERE id=?`, s.ThreadID, s.ID); err != nil {
			return err
		}
	}
	if s.DeliveryEventID == "" {
		s.DeliveryEventID = "process-step:" + s.ID
		if _, err = a.db.Exec(`UPDATE process_step_runs SET delivery_event_id=? WHERE id=?`, s.DeliveryEventID, s.ID); err != nil {
			return err
		}
	}
	api := a.ctx.WithProject(s.ProjectID).AgentEventsAPI()
	if api == nil {
		return errors.New("tracked delivery unavailable")
	}
	receipt, err := api.SendTrackedAgentEvent(sdk.AgentEventRequest{AgentID: s.Executor.AgentID, ThreadID: s.ThreadID, SourceEventID: s.DeliveryEventID, Message: message})
	if err != nil {
		return err
	}
	if receipt == nil || (!receipt.Accepted && !receipt.Duplicate) {
		return errors.New("step event not accepted")
	}
	_, err = a.db.Exec(`UPDATE process_step_runs SET delivered_at=?,execution_id=?,delivery_warning='',next_attempt_at='',delivery_attempts=delivery_attempts+1 WHERE id=?`, timestamp(), receipt.ExecutionID, s.ID)
	return err
}

func processWorkerToolList(sequential bool) []string {
	tools := strings.Split(processWorkerTools, ",")
	if sequential {
		tools = append([]string{"processes_step_claim"}, tools...)
	}
	return tools
}

func processWorkerID(r Run, s StepRun, sequential bool) string {
	if sequential {
		return "process-run-" + r.ID + "-worker"
	}
	return "process-run-" + r.ID + "-step-" + s.Key
}

func deliveryEventID(step StepRun, worker string) string {
	return "process-step:" + step.ID + ":assignment:" + worker
}

func (a *App) spawnProcessThread(project string, request sdk.ThreadSpawnRequest) error {
	threads := a.ctx.WithProject(project).ThreadAPI()
	if threads == nil {
		return errors.New("platform thread API unavailable")
	}
	_, err := threads.SpawnThread(request)
	if err != nil {
		return err
	}
	return nil
}

func (a *App) spawnIndependentWorker(p *Process, r *Run, s *StepRun, all []StepRun) error {
	worker := s.ThreadID
	if worker == "" {
		worker = processWorkerID(*r, *s, false)
	}
	eventID := deliveryEventID(*s, worker)
	workerStep := *s
	workerStep.ThreadID = worker
	workerStep.DeliveryEventID = eventID
	message := a.stepContext(p, *r, workerStep, all)
	if _, err := a.db.Exec(`UPDATE process_step_runs SET target_thread_id=?,delivery_event_id=?,delivered_at='',execution_id='',delivery_warning='',next_attempt_at='' WHERE id=?`, worker, eventID, s.ID); err != nil {
		return err
	}
	s.ThreadID, s.DeliveryEventID, s.DeliveredAt = worker, eventID, ""
	request := sdk.ThreadSpawnRequest{
		AgentID:         s.Executor.AgentID,
		ThreadID:        worker,
		ProjectID:       p.ProjectID,
		DirectiveSuffix: message,
		Tools:           processWorkerToolList(false),
		// A nil MCP slice deliberately means “inherit all spawnable MCP
		// servers attached to this agent”; the server filters no_spawn scopes.
		MCP: nil,
	}
	if err := a.spawnProcessThread(p.ProjectID, request); err != nil {
		return err
	}
	return a.deliverStepToThread(p, *r, s, all)
}

func (a *App) spawnSequentialWorker(p *Process, r *Run, s *StepRun, all []StepRun) error {
	worker := s.ThreadID
	if worker == "" {
		worker = processWorkerID(*r, *s, true)
		if _, err := a.db.Exec(`INSERT INTO process_run_workers(run_id,agent_id,thread_id,created_at) VALUES(?,?,?,?) ON CONFLICT(run_id) DO NOTHING`, r.ID, s.Executor.AgentID, worker, timestamp()); err != nil {
			return err
		}
	}
	eventID := deliveryEventID(*s, worker)
	workerStep := *s
	workerStep.ThreadID = worker
	workerStep.DeliveryEventID = eventID
	directive := fmt.Sprintf("You are the persistent Processes worker for run %s. Keep this thread alive across the run. For each authoritative ready step, call processes_step_claim before any domain action, use the returned frozen instructions and dependency evidence, then record milestones and the terminal outcome with processes_step_update. Do not execute unassigned work or create another worker. Inspect every step_update result: when its top-level done field is true, immediately call done before any text; when false, wait for the next Processes event without polling.", r.ID)
	if _, err := a.db.Exec(`UPDATE process_step_runs SET target_thread_id=?,delivery_event_id=?,delivered_at='',execution_id='',delivery_warning='',next_attempt_at='' WHERE id=?`, worker, eventID, s.ID); err != nil {
		return err
	}
	s.ThreadID, s.DeliveryEventID, s.DeliveredAt = worker, eventID, ""
	request := sdk.ThreadSpawnRequest{
		AgentID:         s.Executor.AgentID,
		ThreadID:        worker,
		ProjectID:       p.ProjectID,
		DirectiveSuffix: directive,
		Tools:           processWorkerToolList(true),
		MCP:             nil,
	}
	if err := a.spawnProcessThread(p.ProjectID, request); err != nil {
		return err
	}
	return a.deliverStepToThread(p, *r, s, all)
}

// assignStep mirrors Tasks assignment semantics: Processes stores the durable
// work, while the assigned agent's main thread creates a worker with the exact
// capabilities it needs. Only after that spawn succeeds does main call this
// method, which records worker ownership and sends the authoritative wake.
func (a *App) assignStep(project, actor, process, run, id, thread string) (any, error) {
	thread = strings.TrimSpace(thread)
	if !validWorkerThreadID(thread) {
		return nil, errors.New("thread_id must identify an existing non-main worker")
	}
	r, err := a.getRun(project, process, run)
	if err != nil {
		return nil, err
	}
	if !r.Workflow {
		return nil, errors.New("run has no structured steps")
	}
	all, err := a.steps(run)
	if err != nil {
		return nil, err
	}
	var step *StepRun
	for i := range all {
		if all[i].ID == id {
			step = &all[i]
			break
		}
	}
	if step == nil {
		return nil, errNotFound
	}
	if step.Executor.Kind != "agent" || !strings.HasPrefix(actor, fmt.Sprintf("agent:%d:", step.Executor.AgentID)) {
		return nil, errors.New("only this step's assigned agent can delegate it")
	}
	if sequentialAgent(r, all) != 0 {
		return nil, errors.New("sequential runs bind their persistent worker through step_claim")
	}
	switch step.State {
	case "ready", "running", "waiting", "blocked":
		// A coordinator may record a milestone before deciding to delegate.
	default:
		return nil, errors.New("only an actionable step can be assigned to a worker")
	}
	assignmentEvent := "process-step:" + step.ID + ":assignment:" + thread
	if strings.Contains(step.DeliveryEventID, ":assignment:") && step.ThreadID != thread {
		return nil, errors.New("step already belongs to another worker")
	}
	if step.ThreadID == thread && step.DeliveryEventID == assignmentEvent && step.DeliveredAt != "" {
		return map[string]any{"run": r, "step": *step, "worker": map[string]any{"thread_id": thread, "assigned": true}}, nil
	}
	_, err = a.db.Exec(`UPDATE process_step_runs SET target_thread_id=?,delivery_event_id=?,delivered_at='',execution_id='',delivery_warning='',next_attempt_at='' WHERE id=?`, thread, assignmentEvent, step.ID)
	if err != nil {
		return nil, err
	}
	step.ThreadID = thread
	step.DeliveryEventID = assignmentEvent
	step.DeliveredAt = ""
	step.ExecutionID = ""
	step.DeliveryWarning = ""
	step.NextAttemptAt = ""
	p, err := a.get(project, process)
	if err != nil {
		return nil, err
	}
	if err = a.deliverStep(p, r, step, all); err != nil {
		return nil, err
	}
	all, err = a.steps(run)
	if err != nil {
		return nil, err
	}
	for _, current := range all {
		if current.ID == id {
			return map[string]any{"run": r, "step": current, "worker": map[string]any{"thread_id": thread, "assigned": true}}, nil
		}
	}
	return nil, errNotFound
}

func validWorkerThreadID(id string) bool {
	if id == "" || id == "main" || len(id) > 128 || strings.HasPrefix(id, ".") {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}
func (a *App) writeStep(s StepRun, state string, progress int, output, reason, actor string) error {
	tx, e := a.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	now := timestamp()
	_, e = tx.Exec(`UPDATE process_step_runs SET state=?,progress=?,output=?,error=?,decision='',updated_by=?,updated_at=?,revision=revision+1,completed_at=CASE WHEN ?='completed' AND completed_at='' THEN ? ELSE completed_at END WHERE id=?`, state, progress, output, reason, actor, now, state, now, s.ID)
	if e != nil {
		return e
	}
	_, e = tx.Exec(`INSERT INTO process_step_events(step_id,actor,state,output,error,created_at) VALUES(?,?,?,?,?,?)`, s.ID, actor, state, output, reason, now)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (a *App) reconcileWorkflow(p *Process, r *Run) error {
	return a.reconcileWorkflowAt(p, r, time.Now().UTC())
}
func (a *App) reconcileWorkflowAt(p *Process, r *Run, now time.Time) error {
	if terminal(r.State) {
		return nil
	}
	d, e := a.runDefinition(*r)
	if e != nil {
		return e
	}
	view := *p
	view.Definition = d
	p = &view
	if len(d.Steps) == 0 {
		return errors.New("workflow run has no step definition")
	}
	if e = a.initWorkflow(r, d); e != nil {
		return e
	}
	all, e := a.steps(r.ID)
	if e != nil {
		return e
	}
	var failures []error
	// Step state and progress are authoritative in Processes. There is no
	// external task status to reconcile before evaluating the DAG.
	for _, s := range all {
		if s.Required && (s.State == "failed" || s.State == "cancelled") {
			_, e = a.db.Exec(`UPDATE process_runs SET state='failed',error=?,current_step=? WHERE id=?`, s.Definition.Name+": "+s.Output+" "+s.Error, s.Definition.Name, r.ID)
			if e == nil {
				fresh, readErr := a.getRun(p.ProjectID, p.ID, r.ID)
				if readErr != nil {
					return readErr
				}
				*r = fresh
			}
			return e
		}
	}
	for i := range all {
		s := &all[i]
		if e = a.resolveStepTiming(s, *r, all); e != nil {
			return e
		}
		if (s.State == "pending" || s.State == "scheduled") && dependenciesReady(*s, all) {
			if !stepTimeReady(*s, now) {
				if s.State != "scheduled" {
					if e = a.writeStep(*s, "scheduled", 0, "", "", "workflow"); e != nil {
						return e
					}
				}
				continue
			}
			s.State = "ready"
			if s.Executor.Kind == "human" {
				s.State = "waiting"
			}
			if e = a.writeStep(*s, s.State, 0, "", "", "workflow"); e != nil {
				return e
			}
		}
		if s.State == "running" {
			if e = a.staleStepReminder(p, *r, *s, all, now); e != nil {
				failures = append(failures, e)
			}
		}
		if s.State == "ready" || s.State == "running" || s.State == "waiting" || s.State == "blocked" {
			if e = a.deliverStep(p, *r, s, all); e != nil {
				failures = append(failures, e)
			}
		}
	}
	all, e = a.steps(r.ID)
	if e != nil {
		return e
	}
	done, total := 0, 0
	state := "running"
	runnable, scheduled := false, false
	names := []string{}
	outputs := map[string]string{}
	for _, s := range all {
		if !s.Required {
			continue
		}
		total++
		if s.State == "running" || s.State == "ready" {
			runnable = true
		}
		if s.State == "scheduled" {
			scheduled = true
		}
		if s.State == "completed" {
			done++
			outputs[s.Key] = s.Output
		} else if s.State != "pending" {
			names = append(names, s.Definition.Name)
			if s.State == "blocked" || s.DeliveryWarning != "" {
				state = "blocked"
			} else if state != "blocked" && s.State == "waiting" {
				state = "waiting"
			}
		}
	}
	if state == "running" && scheduled && !runnable {
		state = "scheduled"
	}
	result := ""
	if done == total {
		state = "completed"
		result = jsonText(outputs)
	}
	warning := ""
	if len(failures) > 0 {
		warning = errors.Join(failures...).Error()
	}
	_, e = a.db.Exec(`UPDATE process_runs SET state=?,progress=?,current_step=?,result=?,delivery_warning=? WHERE id=?`, state, done*100/total, strings.Join(names, ", "), result, warning, r.ID)
	if e != nil {
		return e
	}
	fresh, e := a.getRun(p.ProjectID, p.ID, r.ID)
	if e == nil {
		*r = fresh
	}
	return errors.Join(append(failures, e)...)
}
func (a *App) stepAction(project, actor, process, run, id, action string, args map[string]any) (any, error) {
	r, e := a.getRun(project, process, run)
	if e != nil {
		return nil, e
	}
	if !r.Workflow {
		return nil, errors.New("run has no structured steps")
	}
	all, e := a.steps(run)
	if e != nil {
		return nil, e
	}
	var s StepRun
	found := false
	for _, item := range all {
		if item.ID == id {
			s = item
			found = true
		}
	}
	if !found {
		return nil, errNotFound
	}
	if action == "step_claim" {
		if e = a.claimStep(r, s, all, actor); e != nil {
			return nil, e
		}
		all, e = a.steps(run)
		if e != nil {
			return nil, e
		}
		for _, item := range all {
			if item.ID == id {
				s = item
			}
		}
	}
	if action == "step_update" {
		if e = a.updateTaskState(s, r, all, actor, args); e != nil {
			return nil, e
		}
		p, e := a.get(project, process)
		if e != nil {
			return nil, e
		}
		_ = a.reconcileWorkflow(p, &r)
		all, e = a.steps(run)
		if e != nil {
			return nil, e
		}
		for _, item := range all {
			if item.ID == id {
				s = item
			}
		}
	}
	d, e := a.runDefinition(r)
	if e != nil {
		return nil, e
	}
	worker, e := a.runWorker(r.ID, s.Executor.AgentID)
	if e != nil {
		return nil, e
	}
	if worker != "" && actor == fmt.Sprintf("agent:%d:%s", s.Executor.AgentID, worker) {
		done := terminal(r.State)
		next := "Wait for the next Processes event without polling."
		if done {
			next = "Call the native done tool immediately before writing any text."
		}
		result := map[string]any{"done": done, "next_action": next, "process_id": process, "run_id": r.ID, "step_id": s.ID, "run": map[string]any{"id": r.ID, "process_id": process, "state": r.State, "version": r.Version}, "step": s, "dependencies": dependencyEvidence(s, all), "dependency_outputs": dependencyOutputs(s, all), "parameters": r.Binding.Parameters, "worker": map[string]any{"thread_id": worker, "done": done}}
		if action == "step_claim" {
			result["instructions"] = d.Instructions
			result["required_inputs"] = d.RequiredInputs
			result["default_inputs"] = d.DefaultInputs
			result["completion_criteria"] = d.CompletionCriteria
			result["approval_requirements"] = d.ApprovalRequirements
			result["inputs"] = r.Inputs
			result["assignment"] = r.Binding
		}
		return result, nil
	}
	return map[string]any{"process_id": process, "run_id": r.ID, "step_id": s.ID, "run": r, "step": s, "dependency_outputs": dependencyOutputs(s, all), "dependencies": dependencyEvidence(s, all), "parameters": r.Binding.Parameters, "definition": d}, nil
}

func (a *App) stepLifecycle(event sdk.Event, l *sdk.AgentEventLifecycle) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	id := strings.TrimPrefix(l.SourceEventID, "process-step:")
	if cut := strings.IndexByte(id, ':'); cut >= 0 {
		id = id[:cut]
	}
	s, e := scanStep(a.db.QueryRow(`SELECT `+stepColumns+` FROM process_step_runs WHERE id=?`, id))
	if e != nil {
		return nil
	}
	if event.ProjectID != s.ProjectID {
		return errors.New("task lifecycle project mismatch")
	}
	if event.SourceApp != "apteva-server" || event.InstanceID != s.Executor.AgentID || s.Executor.Kind != "agent" {
		return errors.New("step lifecycle source mismatch")
	}
	// A main-thread wake can settle after Processes has spawned the worker.
	// Validate the event source first, then ignore only a valid but stale
	// lifecycle so it cannot overwrite the worker delivery's execution state.
	if s.DeliveryEventID != "" && s.DeliveryEventID != l.SourceEventID {
		return nil
	}
	if int64(l.Sequence) <= s.LifecycleSequence {
		return nil
	}
	_, e = a.db.Exec(`UPDATE process_step_runs SET lifecycle_sequence=?,execution_state=?,execution_id=?,delivered_at=CASE WHEN delivered_at='' THEN ? ELSE delivered_at END,delivery_warning='',next_attempt_at='' WHERE id=?`, l.Sequence, l.Type, l.ExecutionID, timestamp(), id)
	return e
}

// DependencyEvidence is the frozen ancestor context needed to execute one step.
// Keep it separate from full task records: downstream workers need evidence,
// not predecessor instructions or delivery internals.
type DependencyEvidence struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	State  string `json:"state"`
	Output string `json:"output"`
	Direct bool   `json:"direct"`
}

func dependencyEvidence(s StepRun, all []StepRun) map[string]DependencyEvidence {
	byKey := map[string]StepRun{}
	for _, item := range all {
		byKey[item.Key] = item
	}
	direct := map[string]bool{}
	for _, key := range s.Definition.DependsOn {
		direct[key] = true
	}
	out := map[string]DependencyEvidence{}
	seen := map[string]bool{s.Key: true}
	var add func(string)
	add = func(key string) {
		if seen[key] {
			return
		}
		seen[key] = true
		if dep, ok := byKey[key]; ok {
			out[key] = DependencyEvidence{ID: dep.ID, Key: dep.Key, State: dep.State, Output: dep.Output, Direct: direct[key]}
			for _, parent := range dep.Definition.DependsOn {
				add(parent)
			}
		}
	}
	for _, key := range s.Definition.DependsOn {
		add(key)
	}
	return out
}

func dependencyOutputs(s StepRun, all []StepRun) map[string]string {
	out := map[string]string{}
	for key, dep := range dependencyEvidence(s, all) {
		if dep.State == "completed" {
			out[key] = dep.Output
		}
	}
	return out
}

func (a *App) cancelWorkflow(project, actor, process, run, reason string) (any, error) {
	r, e := a.getRun(project, process, run)
	if e != nil {
		return nil, e
	}
	if !r.Workflow {
		return nil, errors.New("use run_update for a single-agent run")
	}
	if actor != "operator" && !strings.HasPrefix(actor, fmt.Sprintf("agent:%d:", r.Binding.OwnerAgentID)) {
		return nil, errors.New("only the coordinator or project operator can cancel a run")
	}
	if strings.TrimSpace(reason) == "" || len(reason) > 16000 {
		return nil, errors.New("cancellation needs a reason (max 16 KB)")
	}
	if terminal(r.State) {
		if r.State != "cancelled" || r.Error != reason {
			return nil, errors.New("terminal run is immutable")
		}
		return r, nil
	}
	tx, e := a.db.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	now := timestamp()
	_, e = tx.Exec(`INSERT INTO process_step_events(step_id,actor,state,error,created_at) SELECT id,?,'cancelled',?,? FROM process_step_runs WHERE run_id=? AND state IN ('pending','scheduled')`, actor, reason, now, run)
	if e != nil {
		return nil, e
	}
	_, e = tx.Exec(`UPDATE process_step_runs SET state='cancelled',error=?,updated_by=?,updated_at=? WHERE run_id=? AND state IN ('pending','scheduled')`, reason, actor, now, run)
	if e != nil {
		return nil, e
	}
	_, e = tx.Exec(`UPDATE process_runs SET state='cancelled',error=?,current_step='Cancelled; already dispatched work may still finish' WHERE id=?`, reason, run)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return a.getRun(project, process, run)
}

func (a *App) updateTaskState(s Task, r Run, all []Task, actor string, args map[string]any) error {
	allowed := s.Executor.Kind == "human" && actor == "operator" || s.Executor.Kind == "agent" && strings.HasPrefix(actor, fmt.Sprintf("agent:%d:", s.Executor.AgentID))
	if !allowed {
		return errors.New("only this step's assigned executor can update it")
	}
	// Independent workflow steps are handed off by the assigned agent's main
	// thread to a focused worker. The main thread may inspect the authoritative
	// record and choose capabilities, but it must not perform the work itself.
	// Requiring the assignment marker here makes the handoff enforceable even if
	// a coordinator ignores the delivery contract or races the worker wake.
	if s.Origin == "process_step" && s.Executor.Kind == "agent" && sequentialAgent(r, all) == 0 {
		thread := ""
		if i := strings.LastIndex(actor, ":"); i >= 0 {
			thread = actor[i+1:]
		}
		if thread == "main" {
			return errors.New("independent process steps must be completed by the Processes worker, not the agent main thread")
		}
	}
	if s.State == "scheduled" || !stepTimeReady(s, time.Now()) {
		return errors.New("step is scheduled; Processes will notify its executor when the start time is reached")
	}
	if s.State == "pending" || !dependenciesReady(s, all) {
		return errors.New("step dependencies are not complete")
	}
	state := str(args, "state")
	switch state {
	case "running", "waiting", "blocked", "completed", "failed", "cancelled":
	default:
		return errors.New("invalid step state")
	}
	if _, legacy := args["decision"]; legacy {
		return errors.New("decision is not a Processes step field; record approval evidence in the generic step output")
	}
	output, reason := s.Output, s.Error
	if _, ok := args["output"]; ok {
		output = str(args, "output")
	}
	if _, ok := args["error"]; ok {
		reason = str(args, "error")
	}
	progress := s.Progress
	if v, ok := args["progress"]; ok {
		n, valid := v.(float64)
		if !valid || n < 0 || n > 100 || n != float64(int(n)) {
			return errors.New("progress must be 0–100")
		}
		progress = int(n)
	}
	if len(output)+len(reason) > 16000 {
		return errors.New("step output and error exceed 16 KB")
	}
	if state == "completed" {
		if strings.TrimSpace(output) == "" {
			return errors.New("completion needs output evidence")
		}
		progress = 100
	}
	if (state == "waiting" || state == "blocked" || state == "failed" || state == "cancelled") && strings.TrimSpace(reason) == "" {
		return errors.New("record a reason")
	}
	if terminal(s.State) {
		if state != s.State || progress != s.Progress || output != s.Output || reason != s.Error {
			return errors.New("completed step outputs are immutable")
		}
	} else {
		if terminal(r.State) && !(s.Origin == "attached" && !s.Required && r.State == "completed") {
			return errors.New("run is terminal")
		}
		if e := a.writeStep(s, state, progress, output, reason, actor); e != nil {
			return e
		}
	}
	return nil
}
