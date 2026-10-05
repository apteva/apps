package main

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func normalizeControlMode(mode string) (string, error) {
	switch mode {
	case "", "automatic":
		return "automatic", nil
	case "step_by_step":
		return mode, nil
	default:
		return "", errors.New("control_mode must be automatic or step_by_step")
	}
}
func effectiveControlMode(mode string) string {
	if mode == "step_by_step" {
		return mode
	}
	return "automatic"
}
func controlMode(r Run) string { return effectiveControlMode(r.Binding.ControlMode) }
func controlModeSchema() map[string]any {
	return map[string]any{"type": "string", "enum": []string{"automatic", "step_by_step"}, "description": "Frozen run control. automatic (default) dispatches ready work; step_by_step holds each ready step until the controller explicitly calls run_advance. Requires structured steps."}
}
func stepReleased(r Run, s StepRun) bool {
	return controlMode(r) != "step_by_step" || s.Origin != "process_step" || s.ReleasedAt != ""
}
func eligibleForAdvance(r Run, s StepRun, all []StepRun) bool {
	return controlMode(r) == "step_by_step" && !terminal(r.State) && s.Origin == "process_step" && s.ReleasedAt == "" && s.State == "ready" && !s.DeliverySuspended && dependenciesReady(s, all) && stepTimeReady(s, time.Now())
}
func setRunControl(r *Run, all []StepRun) {
	r.ControlMode = controlMode(*r)
	r.EligibleSteps, r.ActiveSteps = []WorkerWorkItem{}, []WorkerWorkItem{}
	r.WaitingForAdvance = false
	if !r.Workflow || terminal(r.State) {
		return
	}
	for _, s := range all {
		item := WorkerWorkItem{ID: s.ID, Key: s.Key, State: s.State, Revision: s.Revision}
		if eligibleForAdvance(*r, s, all) {
			r.EligibleSteps = append(r.EligibleSteps, item)
		}
		if stepReleased(*r, s) && (s.State == "ready" || s.State == "running" || s.State == "waiting" || s.State == "blocked") {
			r.ActiveSteps = append(r.ActiveSteps, item)
		}
	}
	r.WaitingForAdvance = len(r.EligibleSteps) > 0 && len(r.ActiveSteps) == 0
}

// Called under App.mu, just like start, claim, update and reconciliation. Commit
// authorization before network delivery: retries use the existing event identity.
func (a *App) advanceRun(project, actor, process, run, step, key string) (any, error) {
	if len(key) == 0 || len(key) > 160 {
		return nil, errors.New("idempotency_key must contain 1–160 characters")
	}
	r, err := a.getRun(project, process, run)
	if err != nil {
		return nil, err
	}
	if !r.Workflow || controlMode(r) != "step_by_step" {
		return nil, errors.New("run_advance requires a step_by_step structured run")
	}
	if actor != "operator" {
		owner, e := a.ctx.GetAgent(r.Binding.OwnerAgentID)
		if e != nil {
			return nil, e
		}
		if owner.ProjectID != project || owner.DefaultThreadID == "" || (actor != fmt.Sprintf("agent:%d:%s", r.Binding.OwnerAgentID, owner.DefaultThreadID) && actor != fmt.Sprintf("agent:%d:main", r.Binding.OwnerAgentID)) {
			return nil, errors.New("only the operator or coordinator's default thread may advance; workers cannot release steps")
		}
	}
	var saved string
	err = a.db.QueryRow(`SELECT step_id FROM process_run_advances WHERE run_id=? AND request_key=?`, run, key).Scan(&saved)
	duplicate := err == nil
	if duplicate && saved != step {
		return nil, errors.New("idempotency key already used for another step")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	p, err := a.get(project, process)
	if err != nil {
		return nil, err
	}
	if !duplicate {
		if terminal(r.State) {
			return nil, errors.New("run is terminal")
		}
		// Eligibility comes from the saved ready state. Do not dispatch unrelated
		// work before committing this release or let its delivery failures block
		// an independent branch. Reconciliation follows the durable authorization.
		all, e := a.steps(run)
		if e != nil {
			return nil, e
		}
		var selected StepRun
		for _, s := range all {
			if s.ID == step {
				selected = s
			}
		}
		if selected.ID == "" {
			return nil, errNotFound
		}
		if !eligibleForAdvance(r, selected, all) {
			return nil, errors.New("step is not eligible for advancement; read run_get eligible_steps")
		}
		tx, e := a.db.Begin()
		if e != nil {
			return nil, e
		}
		defer tx.Rollback()
		now := timestamp()
		if _, e = tx.Exec(`INSERT INTO process_run_advances(run_id,request_key,step_id,actor,created_at) VALUES(?,?,?,?,?)`, run, key, step, actor, now); e != nil {
			return nil, e
		}
		if _, e = tx.Exec(`UPDATE process_step_runs SET released_at=?,revision=revision+1,updated_at=? WHERE id=? AND released_at=''`, now, now, step); e != nil {
			return nil, e
		}
		if e = tx.Commit(); e != nil {
			return nil, e
		}
	}
	// Even an ambiguous failed delivery leaves one durable authorization. The
	// regular retry worker and this retry both reuse its frozen delivery envelope.
	deliveryErr := a.reconcileWorkflow(p, &r)
	all, err := a.steps(run)
	if err != nil {
		return nil, err
	}
	setRunControl(&r, all)
	var selected StepRun
	for _, s := range all {
		if s.ID == step {
			selected = s
		}
	}
	result := map[string]any{"process_id": process, "run_id": run, "step_id": step, "control_mode": controlMode(r), "state": r.State, "progress": r.Progress, "duplicate": duplicate, "step": map[string]any{"id": selected.ID, "key": selected.Key, "state": selected.State, "progress": selected.Progress, "revision": selected.Revision, "released_at": selected.ReleasedAt}, "eligible_steps": r.EligibleSteps, "active_steps": r.ActiveSteps, "waiting_for_advance": r.WaitingForAdvance, "reread": runReread(process, run)}
	result["next_action"] = "Review saved outputs; release only an eligible step when ready. Human steps require separate operator completion evidence."
	if deliveryErr != nil {
		result["delivery_warning"] = deliveryErr.Error()
	}
	return result, nil
}
