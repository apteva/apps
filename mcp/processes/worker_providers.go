package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// workerProviders are the names Core accepts. The SDK has no call that lists an
// agent's providers, so this catches typos at save time; whether this agent can
// use the provider is only known when its worker is created.
var workerProviders = []string{"anthropic", "fireworks", "google", "grok-build", "managed", "nvidia", "ollama", "openai", "openai-codex", "opencode-go", "venice", "xai"}

func knownWorkerProvider(name string) bool {
	for _, known := range workerProviders {
		if name == known {
			return true
		}
	}
	return false
}

// stepProvider is the provider this assignment asks for on a step. Empty means
// the worker follows its agent.
func (c AssignmentConfig) stepProvider(key string) string {
	if provider := c.StepProviders[key]; provider != "" {
		return provider
	}
	return c.Provider
}

func validateProviderNames(c AssignmentConfig) error {
	if c.Provider != "" && !knownWorkerProvider(c.Provider) {
		return fmt.Errorf("unknown provider %q; use one of %s", c.Provider, strings.Join(workerProviders, ", "))
	}
	for key, provider := range c.StepProviders {
		if !knownWorkerProvider(provider) {
			return fmt.Errorf("unknown provider %q for step %s; use one of %s", provider, key, strings.Join(workerProviders, ", "))
		}
	}
	return nil
}

// validateWorkerProviders checks the assignment against one procedure version.
// It runs at save and again at run start, because follow_latest can adopt a
// version without a step that step_providers names.
func validateWorkerProviders(d Definition, c AssignmentConfig) error {
	if c.Provider == "" && len(c.StepProviders) == 0 {
		return nil
	}
	if len(d.Steps) == 0 {
		return errors.New("provider requires structured steps: a procedure without steps runs on its owner's existing thread")
	}
	steps := map[string]Step{}
	for _, s := range d.Steps {
		steps[s.Key] = s
	}
	overridden := make([]string, 0, len(c.StepProviders))
	for key := range c.StepProviders {
		overridden = append(overridden, key)
	}
	sort.Strings(overridden)
	for _, key := range overridden {
		s, ok := steps[key]
		if !ok {
			return fmt.Errorf("step_providers names step %s, which this procedure version doesn't have", key)
		}
		if c.Roles[s.Role].Kind == "human" {
			return fmt.Errorf("step_providers names step %s, which a human performs", key)
		}
	}
	if c.WorkerContinuity != "per_executor" {
		return nil
	}
	// A running thread can't change provider, and per_executor promises one
	// worker per agent. Auto splits such steps instead, in sequentialAgent.
	first := map[int64]Step{}
	for _, s := range d.Steps {
		executor, ok := c.Roles[s.Role]
		if ok && executor.Kind == "human" {
			continue
		}
		agent := executor.AgentID
		if !ok || agent == 0 {
			agent = c.OwnerAgentID
		}
		seen, ok := first[agent]
		if !ok {
			first[agent] = s
			continue
		}
		if c.stepProvider(seen.Key) != c.stepProvider(s.Key) {
			return fmt.Errorf("worker_continuity per_executor keeps one worker per agent, so steps %s and %s need the same provider; use auto or isolated to give them different ones", seen.Key, s.Key)
		}
	}
	return nil
}

func (a *App) recordWorkerProvider(run, thread, pin string, status *sdk.ThreadProviderStatus) error {
	if pin == "" {
		return nil
	}
	if status == nil {
		status = &sdk.ThreadProviderStatus{Pin: pin}
	}
	_, err := a.db.Exec(`INSERT INTO process_worker_providers(run_id,thread_id,pin,active,missing,updated_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(run_id,thread_id) DO UPDATE SET pin=excluded.pin,active=excluded.active,missing=excluded.missing,updated_at=excluded.updated_at`,
		run, thread, pin, status.Active, status.Missing, timestamp())
	return err
}

// attachWorkerProviders reports each step's worker provider. Workers are shared
// across steps, so the status is keyed by thread rather than stored per step.
func (a *App) attachWorkerProviders(run string, steps []StepRun) error {
	rows, err := a.db.Query(`SELECT thread_id,pin,active,missing FROM process_worker_providers WHERE run_id=?`, run)
	if err != nil {
		return err
	}
	defer rows.Close()
	workers := map[string]*sdk.ThreadProviderStatus{}
	for rows.Next() {
		var thread string
		var status sdk.ThreadProviderStatus
		if err = rows.Scan(&thread, &status.Pin, &status.Active, &status.Missing); err != nil {
			return err
		}
		workers[thread] = &status
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for i := range steps {
		steps[i].Provider = workers[steps[i].ThreadID]
	}
	return nil
}
