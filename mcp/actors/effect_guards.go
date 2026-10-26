package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

var errActorEffectReserved = errors.New("once_key already reserved")
var errActorClickNotDispatched = errors.New("click was not dispatched")

// Reserve before attempting a consequential click. An uncertain result remains
// reserved across retries, restarts and revisions; inspect the original run
// before deliberately issuing another request with a different once_key.
func (e *actorExecution) clickOnce(step actorStep) error {
	if err := e.guardExistingRecovery(step); err != nil {
		return err
	}
	if e.definition.RecoveryOnly {
		var trigger map[string]any
		json.Unmarshal([]byte(e.run.TriggerJSON), &trigger)
		if trigger["kind"] != "recovery" || len(mapFromAny(trigger["existing_resource"])) == 0 {
			return errors.New("operation requires verified existing-resource recovery")
		}
	}
	if step.OnceKey == "" {
		return e.click(step)
	}
	var input struct {
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal([]byte(e.run.InputJSON), &input); err != nil {
		return err
	}
	if input.Operation == "" {
		input.Operation = "run"
	}
	// Editable definitions may share a logical effect across named entry points.
	// The default remains the original operation namespace for compatibility.
	if step.OnceScope != "" {
		input.Operation = step.OnceScope
	}
	result, err := e.ctx.AppDB().Exec(`INSERT INTO actors_effect_guards(project_id,actor_id,operation,once_key,run_id,expected_effect,state) VALUES(?,?,?,?,?,?,'reserved') ON CONFLICT DO NOTHING`, projectID(e.ctx), e.run.ActorID, input.Operation, step.OnceKey, e.run.ID, step.ExpectedEffect)
	if err != nil {
		return fmt.Errorf("reserve once_key: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		var previousRun int64
		if err := e.ctx.AppDB().QueryRow(`SELECT run_id FROM actors_effect_guards WHERE project_id=? AND actor_id=? AND operation=? AND once_key=?`, projectID(e.ctx), e.run.ActorID, input.Operation, step.OnceKey).Scan(&previousRun); err != nil {
			return err
		}
		return fmt.Errorf("%w by run %d; refusing a duplicate consequential click", errActorEffectReserved, previousRun)
	}
	effect := map[string]any{"once_key": step.OnceKey, "expected_effect": step.ExpectedEffect, "state": "reserved"}
	if step.EffectPhase != "" {
		effect["effect_phase"] = step.EffectPhase
	}
	e.effects = append(e.effects, effect)
	err = e.click(step)
	state := "completed"
	if err != nil {
		state = "uncertain"
		if errors.Is(err, errActorClickNotDispatched) {
			// Keep the reservation. Resolution failures prove no click was sent,
			// but do not authorize an automatic retry with a new identity.
			state = "not_dispatched"
		}
	}
	effect["state"] = state
	_, persistErr := e.ctx.AppDB().Exec(`UPDATE actors_effect_guards SET state=? WHERE project_id=? AND actor_id=? AND operation=? AND once_key=?`, state, projectID(e.ctx), e.run.ActorID, input.Operation, step.OnceKey)
	if err != nil {
		return err
	}
	if persistErr != nil {
		return fmt.Errorf("click completed but recording its state failed: %w", persistErr)
	}
	return nil
}
