package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Reuse only a sequential procedure with one agent. Parallel branches,
// Tasks runs, and different agents retain independent step deliveries.
func sequentialAgent(r Run, all []StepRun) int64 {
	if !r.Workflow || r.Backend != "agent" || r.Binding.WorkerContinuity == "isolated" {
		return 0
	}
	var agent int64
	previous := ""
	count := 0
	for _, s := range all {
		if s.Origin != "process_step" {
			continue
		}
		// Timed runs release workers between steps; the app owns every wake-up.
		if s.Definition.StartAfter != nil {
			return 0
		}
		deps := s.Definition.DependsOn
		if previous == "" && len(deps) != 0 || previous != "" && (len(deps) != 1 || deps[0] != previous) {
			return 0
		}
		previous = s.Key
		if s.Executor.Kind == "human" {
			continue
		}
		if agent != 0 && agent != s.Executor.AgentID {
			return 0
		}
		agent = s.Executor.AgentID
		count++
	}
	if count < 2 {
		return 0
	}
	return agent
}

func (a *App) runWorker(run string, agent int64) (string, error) {
	var thread string
	err := a.db.QueryRow(`SELECT thread_id FROM process_run_workers WHERE run_id=? AND agent_id=?`, run, agent).Scan(&thread)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return thread, err
}

func (a *App) claimStep(r Run, s StepRun, all []StepRun, actor string) error {
	if !stepReleased(r, s) {
		return errors.New("step is held for controller advancement")
	}
	agent := persistentWorkerAgent(r, s, all)
	parts := strings.SplitN(actor, ":", 3)
	if agent == 0 || s.Executor.Kind != "agent" || s.Executor.AgentID != agent || len(parts) != 3 || parts[0] != "agent" || parts[1] != fmt.Sprint(agent) || parts[2] == "" || parts[2] == "main" {
		return errors.New("step_claim requires the assigned agent's persistent run worker")
	}
	if s.State == "pending" || s.State == "scheduled" || !stepTimeReady(s, time.Now()) || !dependenciesReady(s, all) {
		return errors.New("step dependencies are not complete")
	}
	if aiParallel(r) && terminal(r.State) && !terminal(s.State) {
		return errors.New("run is terminal")
	}
	if !terminal(s.State) && !terminal(r.State) {
		available, err := a.workerClaimAvailable(r, s)
		if err != nil {
			return err
		}
		if !available {
			return errors.New("this worker has reached its unfinished step limit")
		}
	}
	thread, err := a.runWorker(r.ID, agent)
	if err != nil {
		return err
	}
	if r.Binding.WorkerContinuity == "per_executor" && (thread == "" || s.ThreadID != parts[2]) {
		return errors.New("claim requires the app-provisioned worker assigned to this step")
	}
	if thread != "" && thread != parts[2] {
		return errors.New("this run already belongs to another worker")
	}
	if terminal(s.State) || terminal(r.State) {
		return nil
	}
	if _, err = a.db.Exec(`INSERT INTO process_run_workers(run_id,agent_id,thread_id,created_at) VALUES(?,?,?,?) ON CONFLICT(run_id,agent_id) DO NOTHING`, r.ID, agent, parts[2], timestamp()); err != nil {
		return err
	}
	if s.State == "ready" {
		return a.writeStepClaim(s, "running", s.Progress, s.Output, s.Error, actor, true)
	}
	return nil
}

// Worker thread ownership is independent of dependency topology when explicitly
// selected on the frozen assignment. Automatic mode retains legacy scheduling.
func persistentWorkerAgent(r Run, s StepRun, all []StepRun) int64 {
	if !r.Workflow || r.Backend != "agent" || s.Origin != "process_step" || s.Executor.Kind != "agent" {
		return 0
	}
	if r.Binding.WorkerContinuity == "per_executor" {
		return s.Executor.AgentID
	}
	return sequentialAgent(r, all)
}

// Reserve a worker at provisioning, before even an ambiguous delivery attempt.
// Query fresh storage: reconciliation's step slice predates earlier dispatches.
func (a *App) workerStepAvailable(r Run, s StepRun) (bool, error) {
	// Auto mode delivers all eligible steps to the same owner. The model decides
	// which ones to claim and delegate; only claims consume concurrency slots.
	if aiParallel(r) {
		return true, nil
	}
	if r.Binding.WorkerContinuity != "per_executor" {
		return true, nil
	}
	var count int
	err := a.db.QueryRow(`SELECT count(*) FROM process_step_runs WHERE run_id=? AND id<>? AND origin='process_step' AND json_extract(executor_json,'$.kind')='agent' AND json_extract(executor_json,'$.agent_id')=? AND target_thread_id<>'' AND state IN ('ready','running','waiting','blocked')`, r.ID, s.ID, s.Executor.AgentID).Scan(&count)
	return count == 0, err
}

func persistentWorkerDone(r Run, agent int64, all []StepRun) bool {
	if terminal(r.State) {
		return true
	}
	if r.Binding.WorkerContinuity != "per_executor" {
		return false
	}
	for _, s := range all {
		if s.Origin == "process_step" && s.Executor.Kind == "agent" && s.Executor.AgentID == agent && !terminal(s.State) {
			return false
		}
	}
	return true
}
