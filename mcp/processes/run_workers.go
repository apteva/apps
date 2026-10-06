package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Reuse only a sequential procedure with one agent. Parallel branches,
// Tasks runs, and different agents retain independent step deliveries.
func sequentialAgent(r Run, all []StepRun) int64 {
	if !r.Workflow || r.Backend != "agent" {
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
	agent := sequentialAgent(r, all)
	parts := strings.SplitN(actor, ":", 3)
	if agent == 0 || s.Executor.Kind != "agent" || s.Executor.AgentID != agent || len(parts) != 3 || parts[0] != "agent" || parts[1] != fmt.Sprint(agent) || parts[2] == "" || parts[2] == "main" {
		return errors.New("step_claim requires the assigned agent's worker in a sequential run")
	}
	if s.State == "pending" || !dependenciesReady(s, all) {
		return errors.New("step dependencies are not complete")
	}
	thread, err := a.runWorker(r.ID, agent)
	if err != nil {
		return err
	}
	if thread != "" && thread != parts[2] {
		return errors.New("this run already belongs to another worker")
	}
	if terminal(s.State) || terminal(r.State) {
		return nil
	}
	if _, err = a.db.Exec(`INSERT INTO process_run_workers(run_id,agent_id,thread_id,created_at) VALUES(?,?,?,?) ON CONFLICT(run_id) DO NOTHING`, r.ID, agent, parts[2], timestamp()); err != nil {
		return err
	}
	if s.State == "ready" {
		return a.writeStep(s, "running", s.Progress, s.Output, s.Error, s.Decision, actor)
	}
	return nil
}
