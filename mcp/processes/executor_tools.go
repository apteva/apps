package main

import (
	"encoding/json"
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

// The calling installation is resolved by the platform. An executor may still
// carry an endpoint from a removed installation; requesting a thread scope
// alone cannot make that endpoint usable.
func (a *App) ensureExecutorTools(project string, agent int64) error {
	api := a.ctx.WithProject(project).AgentToolsAPI()
	if api == nil {
		return errors.New("Processes tools attachment API unavailable; worker was not dispatched")
	}
	result, err := api.EnsureAppToolsAttached(sdk.EnsureAppToolsRequest{AgentID: agent})
	if err != nil {
		return fmt.Errorf("ensure Processes tools for agent %d: %w", agent, err)
	}
	if result == nil || result.AgentID != agent || !result.Applied || len(result.MCPServerIDs) == 0 {
		return fmt.Errorf("Processes tools were not applied to agent %d; worker was not dispatched", agent)
	}
	return nil
}

// Repair an existing app-owned worker before redelivery, without replacing its
// history or changing the immutable event/spawn envelopes. Older profiles
// explicitly restricted MCP to Processes and excluded the agent's domain tools.
func (a *App) repairWorkerTools(project string, agent int64, worker string) error {
	if err := a.ensureExecutorTools(project, agent); err != nil {
		return err
	}
	return a.reconcileWorkerTools(project, agent, worker, false)
}

func (a *App) reconcileWorkerTools(project string, agent int64, worker string, legacyOnly bool) error {
	var raw string
	err := a.db.QueryRow(`SELECT spawn_json FROM process_delivery_envelopes WHERE project_id=? AND spawned=1 AND spawn_json<>'' AND json_extract(spawn_json,'$.thread_id')=? LIMIT 1`, project, worker).Scan(&raw)
	if err != nil {
		return fmt.Errorf("load worker provisioning checkpoint: %w", err)
	}
	var request sdk.ThreadSpawnRequest
	if err = json.Unmarshal([]byte(raw), &request); err != nil {
		return err
	}
	if request.AgentID != agent || request.ThreadID != worker {
		return errors.New("worker provisioning checkpoint target mismatch")
	}
	if legacyOnly && request.MCP == nil {
		return nil
	}
	request.MCP = nil
	request.Events = nil
	api, ok := a.ctx.WithProject(project).ThreadAPI().(sdk.ThreadProfileClient)
	if !ok {
		return errors.New("worker profile reconciliation unavailable")
	}
	result, err := api.EnsureThread(sdk.ThreadEnsureRequest{ThreadSpawnRequest: request})
	if err != nil {
		return fmt.Errorf("reconcile worker tools: %w", err)
	}
	if result == nil || result.Thread.AgentID != agent || result.Thread.ThreadID != worker {
		return errors.New("worker profile reconciliation did not confirm the target")
	}
	return nil
}
