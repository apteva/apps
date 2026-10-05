package main

import (
	"fmt"
	"time"
)

const defaultParallelSteps = 4

func aiParallel(r Run) bool {
	return r.Workflow && r.Backend == "agent" && r.Binding.WorkerContinuity == "per_executor" && r.Binding.ParallelExecution == "auto"
}

func parallelLimit(r Run) int {
	if r.Binding.MaxParallelSteps > 0 {
		return r.Binding.MaxParallelSteps
	}
	return defaultParallelSteps
}

// executeResponse and the reconciliation worker hold App.mu: count and claim
// happen in one critical section. An accepted claim is a durable running step,
// so slots and ownership survive sidecar restart, delivery retry and lost replies.
func (a *App) workerClaimAvailable(r Run, s StepRun) (bool, error) {
	if !aiParallel(r) {
		return a.workerStepAvailable(r, s)
	}
	if s.State != "ready" {
		return true, nil // reread an existing claim without consuming another slot
	}
	var count int
	err := a.db.QueryRow(`SELECT count(*) FROM process_step_runs WHERE run_id=? AND id<>? AND origin='process_step' AND json_extract(executor_json,'$.kind')='agent' AND json_extract(executor_json,'$.agent_id')=? AND state IN ('running','waiting','blocked')`, r.ID, s.ID, s.Executor.AgentID).Scan(&count)
	return count < parallelLimit(r), err
}

type WorkerWorkItem struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	State    string `json:"state"`
	Revision int    `json:"revision"`
}

// Hints include only this owner's ready work and durable in-flight checkpoints.
// They do not resend instructions or receipts. Claim/read is authoritative.
func parallelWorkHints(result map[string]any, r Run, agent int64, worker string, all []StepRun) {
	if !aiParallel(r) || worker == "" {
		return
	}
	ready, active := []WorkerWorkItem{}, []WorkerWorkItem{}
	if !terminal(r.State) {
		for _, s := range all {
			if s.Origin != "process_step" || s.Executor.Kind != "agent" || s.Executor.AgentID != agent || s.ThreadID != worker {
				continue
			}
			item := WorkerWorkItem{ID: s.ID, Key: s.Key, State: s.State, Revision: s.Revision}
			if s.State == "ready" && !s.DeliverySuspended && stepTimeReady(s, time.Now()) && dependenciesReady(s, all) {
				ready = append(ready, item)
			} else if s.State == "running" || s.State == "waiting" || s.State == "blocked" {
				active = append(active, item)
			}
		}
	}
	result["ready_steps"], result["active_steps"] = ready, active
	result["parallel_execution"], result["max_parallel_steps"] = "auto", parallelLimit(r)
}

func parallelWorkerDirective(r Run) string {
	return fmt.Sprintf(`You are the persistent Processes worker for run %s. You own this run's steps for your executor, including their saved evidence. parallel_execution=auto permits up to %d unfinished claimed steps. Processes sends events for eligible steps; ready_steps and active_steps in step responses are compact discovery/checkpoint hints. Claim each ready step with processes_step_claim before doing or delegating any domain work. Never act on pending, scheduled or unassigned steps.
Assess ready steps for useful parallel execution. Delegate independent, substantial work to Core subthreads when inputs and resources permit safe parallel use. Keep dependencies, shared mutable resources and thread-bound sessions sequential. Use Core's existing thread tools to inspect, reuse, spawn and manage your children; Processes does not choose your thread arrangement. Retain shared context and tools here, and give children only the frozen instructions, exact dependency receipts and transferable resource references needed for their task. Children return their exact results to you; they must not claim or update Processes steps, self-approve, or execute downstream work. You remain responsible for checking each result and recording its separate step output/checkpoint with processes_step_update. Keep a durable checkpoint naming any delegated child and operation identity before waiting. After recovery inspect those children and saved evidence before retrying actions; do not create replacements for pending work.
Read step_update acknowledgements and their ready_steps/active_steps. When done=false, continue eligible work or await events and child results without polling. Never finish this parent while delegated work is outstanding. When done=true, stop or settle remaining child work, then call native done once. Final validation and human approval still gate publication.`, r.ID, parallelLimit(r))
}
