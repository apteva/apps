package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) tools() []sdk.Tool {
	wake := map[string]any{"io.apteva/wakeOnResult": "always"}
	return []sdk.Tool{
		{
			Name:        "goal_start",
			Description: "Create one durable Builder goal as soon as the operator has stated a concrete desired outcome. Use a stable idempotency_key so retries or a new Conversation turn recover the same goal. Capture measurable success criteria and material constraints before planning. Returns the goal and whether it was newly created.",
			InputSchema: objectSchema([]string{"title", "objective", "success_criteria"}, map[string]any{
				"title":            map[string]any{"type": "string", "description": "Short operator-facing goal title."},
				"objective":        map[string]any{"type": "string", "description": "Complete desired outcome, not an implementation task."},
				"success_criteria": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string"}},
				"constraints":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"idempotency_key":  map[string]any{"type": "string", "description": "Stable key derived from the outcome, reused across retries."},
			}), Meta: wake, HandlerCtx: a.toolGoalStart,
		},
		{
			Name:        "goal_list",
			Description: "List Builder goals owned by this Helper in the current project. Use this after a restart, compaction, or new Conversation before creating another goal. The result is authoritative for goal identity and current phase.",
			InputSchema: objectSchema(nil, map[string]any{
				"statuses": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": sortedKeys(validGoalStatuses)}},
				"limit":    map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
			}), HandlerCtx: a.toolGoalList,
		},
		{
			Name:        "goal_get",
			Description: "Read the complete durable state for one goal: ordered plan, success checks, managed resources, recent events, and the enforced completion gate. Call this at every new wake-up before continuing project work.",
			InputSchema: objectSchema([]string{"goal_id"}, map[string]any{
				"goal_id": map[string]any{"type": "string"},
			}), HandlerCtx: a.toolGoalGet,
		},
		{
			Name:        "plan_set",
			Description: "Set the initial ordered execution plan and concrete verification checks after inspecting authoritative platform state. This may replace a still-pending draft plan, but refuses once any step has started. Mark destructive, costly, externally visible, publishing, deployment, paid-resource, or credential-dependent steps as requires_approval.",
			InputSchema: objectSchema([]string{"goal_id", "steps", "checks"}, map[string]any{
				"goal_id": map[string]any{"type": "string"},
				"steps": map[string]any{"type": "array", "minItems": 1, "items": objectSchema([]string{"title"}, map[string]any{
					"title":             map[string]any{"type": "string"},
					"detail":            map[string]any{"type": "string"},
					"requires_approval": map[string]any{"type": "boolean"},
				})},
				"checks": map[string]any{"type": "array", "minItems": 1, "items": objectSchema([]string{"name"}, map[string]any{
					"key":         map[string]any{"type": "string", "description": "Stable machine key; derived from name when omitted."},
					"name":        map[string]any{"type": "string"},
					"description": map[string]any{"type": "string"},
				})},
			}), Meta: wake, HandlerCtx: a.toolPlanSet,
		},
		{
			Name:        "step_update",
			Description: "Update one existing plan step at meaningful phase boundaries. Set active before work; waiting_approval before asking through Conversations; completed only after the phase's changes are verified. blocked or failed requires blocking_reason. approval_state records required/requested/approved/denied without replacing the Conversation approval card.",
			InputSchema: objectSchema([]string{"goal_id", "step_id"}, map[string]any{
				"goal_id":         map[string]any{"type": "string"},
				"step_id":         map[string]any{"type": "string"},
				"status":          map[string]any{"type": "string", "enum": sortedKeys(validStepStatuses)},
				"approval_state":  map[string]any{"type": "string", "enum": sortedKeys(validApprovalStates)},
				"blocking_reason": map[string]any{"type": "string"},
				"note":            map[string]any{"type": "string", "description": "Concrete outcome or phase note; avoid per-tool narration."},
			}), Meta: wake, HandlerCtx: a.toolStepUpdate,
		},
		{
			Name:        "resource_upsert",
			Description: "Create or update the durable record for a platform resource managed by this goal. Use one stable key per intended agent, app, integration, credential requirement, connection, project setting, or other resource. desired_state is intent; observed_state must come from authoritative platform reads. Mark drift explicitly instead of silently overwriting intent.",
			InputSchema: objectSchema([]string{"goal_id", "key", "kind", "name", "status"}, map[string]any{
				"goal_id":        map[string]any{"type": "string"},
				"key":            map[string]any{"type": "string"},
				"kind":           map[string]any{"type": "string", "enum": sortedKeys(validResourceKinds)},
				"name":           map[string]any{"type": "string"},
				"external_id":    map[string]any{"type": "string", "description": "Authoritative Apteva/server identifier when one exists."},
				"status":         map[string]any{"type": "string", "enum": sortedKeys(validResourceStatuses)},
				"desired_state":  map[string]any{"type": "object"},
				"observed_state": map[string]any{"type": "object"},
				"note":           map[string]any{"type": "string"},
			}), Meta: wake, HandlerCtx: a.toolResourceUpsert,
		},
		{
			Name:        "check_record",
			Description: "Record a success check using an authoritative observation. passing, failing, or blocked checks require a concrete result; include structured evidence such as IDs, status values, counts, or URLs when safe. The goal cannot complete until every declared check is passing.",
			InputSchema: objectSchema([]string{"goal_id", "key", "name", "status"}, map[string]any{
				"goal_id":     map[string]any{"type": "string"},
				"key":         map[string]any{"type": "string"},
				"name":        map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"status":      map[string]any{"type": "string", "enum": sortedKeys(validCheckStatuses)},
				"result":      map[string]any{"type": "string"},
				"evidence":    map[string]any{"type": "object"},
			}), Meta: wake, HandlerCtx: a.toolCheckRecord,
		},
		{
			Name:        "event_record",
			Description: "Record one meaningful decision, risk, operator input, approval outcome, progress milestone, or note that must survive Conversation and thread boundaries. Do not mirror every tool call. Use data for safe structured references and never store raw secrets.",
			InputSchema: objectSchema([]string{"goal_id", "kind", "title"}, map[string]any{
				"goal_id": map[string]any{"type": "string"},
				"kind":    map[string]any{"type": "string", "enum": []string{"progress", "decision", "risk", "approval", "operator_input", "note"}},
				"title":   map[string]any{"type": "string"},
				"detail":  map[string]any{"type": "string"},
				"data":    map[string]any{"type": "object"},
			}), Meta: wake, HandlerCtx: a.toolEventRecord,
		},
		{
			Name:        "goal_update",
			Description: "Update the goal-level status, current phase, operator-facing summary, or next action. Use waiting_approval only when no work can proceed before a verdict, blocked only with a clear next action, and completed only after every non-skipped step is complete and every success check passes; Builder enforces that gate.",
			InputSchema: objectSchema([]string{"goal_id"}, map[string]any{
				"goal_id":       map[string]any{"type": "string"},
				"status":        map[string]any{"type": "string", "enum": sortedKeys(validGoalStatuses)},
				"current_phase": map[string]any{"type": "string"},
				"summary":       map[string]any{"type": "string"},
				"next_action":   map[string]any{"type": "string"},
			}), Meta: wake, HandlerCtx: a.toolGoalUpdate,
		},
	}
}

func builderIdentity(ctx context.Context, app *sdk.AppCtx) (*sdk.Caller, GoalIdentity, error) {
	caller := sdk.CallerFrom(ctx)
	if caller == nil || caller.AgentID <= 0 || strings.TrimSpace(caller.ThreadID) == "" {
		return nil, GoalIdentity{}, errors.New("trusted Helper thread context required")
	}
	projectID := strings.TrimSpace(caller.ProjectID)
	if projectID == "" && app != nil {
		projectID = strings.TrimSpace(app.CurrentProject())
	}
	if projectID == "" {
		return nil, GoalIdentity{}, errors.New("trusted project context required")
	}
	return caller, GoalIdentity{ProjectID: projectID, OwnerAgentID: caller.AgentID}, nil
}

func (a *App) toolGoalStart(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, identity, err := builderIdentity(ctx, app)
	if err != nil {
		return nil, err
	}
	criteria := stringSliceArg(args, "success_criteria")
	if len(cleanStrings(criteria)) == 0 {
		return nil, errors.New("at least one concrete success criterion is required")
	}
	key := stringArg(args, "idempotency_key")
	if strings.TrimSpace(key) == "" {
		key = caller.ToolCallID
	}
	goal, created, err := a.store.CreateGoal(CreateGoalInput{
		ProjectID: identity.ProjectID, OwnerAgentID: identity.OwnerAgentID, ThreadID: caller.ThreadID,
		Title: stringArg(args, "title"), Objective: stringArg(args, "objective"),
		SuccessCriteria: criteria, Constraints: stringSliceArg(args, "constraints"), IdempotencyKey: key,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"goal": goal, "created": created, "next": "Inspect authoritative platform state, then call builder_plan_set."}, nil
}

func (a *App) toolGoalList(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	_, identity, err := builderIdentity(ctx, app)
	if err != nil {
		return nil, err
	}
	goals, err := a.store.ListGoals(identity, stringSliceArg(args, "statuses"), intArg(args, "limit"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"goals": goals, "count": len(goals)}, nil
}

func (a *App) toolGoalGet(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	_, identity, err := builderIdentity(ctx, app)
	if err != nil {
		return nil, err
	}
	return a.store.GetBundle(identity, stringArg(args, "goal_id"))
}

func (a *App) toolPlanSet(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, identity, err := builderIdentity(ctx, app)
	if err != nil {
		return nil, err
	}
	var steps []PlanStepInput
	if err := decodeArg(args["steps"], &steps); err != nil {
		return nil, fmt.Errorf("steps: %w", err)
	}
	var checks []PlanCheckInput
	if err := decodeArg(args["checks"], &checks); err != nil {
		return nil, fmt.Errorf("checks: %w", err)
	}
	if len(checks) == 0 {
		return nil, errors.New("at least one concrete success check is required")
	}
	return a.store.SetPlan(identity, stringArg(args, "goal_id"), steps, checks, caller.AgentID, caller.ThreadID, caller.ToolCallID)
}

func (a *App) toolStepUpdate(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, identity, err := builderIdentity(ctx, app)
	if err != nil {
		return nil, err
	}
	status, hasStatus := optionalStringArg(args, "status")
	approval, hasApproval := optionalStringArg(args, "approval_state")
	blocking, hasBlocking := optionalStringArg(args, "blocking_reason")
	if !hasStatus && !hasApproval && !hasBlocking && strings.TrimSpace(stringArg(args, "note")) == "" {
		return nil, errors.New("step_update requires a state change or note")
	}
	input := UpdateStepInput{Note: stringArg(args, "note"), EventKey: caller.ToolCallID, ActorAgentID: caller.AgentID, ActorThreadID: caller.ThreadID}
	if hasStatus {
		input.Status = &status
	}
	if hasApproval {
		input.ApprovalState = &approval
	}
	if hasBlocking {
		input.BlockingReason = &blocking
	}
	return a.store.UpdateStep(identity, stringArg(args, "goal_id"), stringArg(args, "step_id"), input)
}

func (a *App) toolResourceUpsert(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, identity, err := builderIdentity(ctx, app)
	if err != nil {
		return nil, err
	}
	externalID, externalIDSet := optionalStringArg(args, "external_id")
	desiredState, desiredStateSet := optionalMapArg(args, "desired_state")
	observedState, observedStateSet := optionalMapArg(args, "observed_state")
	note, noteSet := optionalStringArg(args, "note")
	resource, err := a.store.UpsertResource(identity, stringArg(args, "goal_id"), UpsertResourceInput{
		Key: stringArg(args, "key"), Kind: stringArg(args, "kind"), Name: stringArg(args, "name"),
		ExternalID: externalID, ExternalIDSet: externalIDSet, Status: stringArg(args, "status"),
		DesiredState: desiredState, DesiredStateSet: desiredStateSet,
		ObservedState: observedState, ObservedStateSet: observedStateSet,
		Note: note, NoteSet: noteSet,
		ActorAgentID: caller.AgentID, ActorThreadID: caller.ThreadID, EventKey: caller.ToolCallID,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"resource": resource}, nil
}

func (a *App) toolCheckRecord(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, identity, err := builderIdentity(ctx, app)
	if err != nil {
		return nil, err
	}
	status := strings.TrimSpace(stringArg(args, "status"))
	result := strings.TrimSpace(stringArg(args, "result"))
	if status != "pending" && result == "" {
		return nil, errors.New("result is required for a checked success criterion")
	}
	check, err := a.store.RecordCheck(identity, stringArg(args, "goal_id"), RecordCheckInput{
		Key: stringArg(args, "key"), Name: stringArg(args, "name"), Description: stringArg(args, "description"),
		Status: status, Result: result, Evidence: mapArg(args, "evidence"),
		ActorAgentID: caller.AgentID, ActorThreadID: caller.ThreadID, EventKey: caller.ToolCallID,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"check": check}, nil
}

func (a *App) toolEventRecord(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, identity, err := builderIdentity(ctx, app)
	if err != nil {
		return nil, err
	}
	event, err := a.store.RecordEvent(identity, stringArg(args, "goal_id"), RecordEventInput{
		Kind: stringArg(args, "kind"), Title: stringArg(args, "title"), Detail: stringArg(args, "detail"), Data: mapArg(args, "data"),
		ActorAgentID: caller.AgentID, ActorThreadID: caller.ThreadID, EventKey: eventKey("event", caller.ToolCallID),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"event": event}, nil
}

func (a *App) toolGoalUpdate(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	caller, identity, err := builderIdentity(ctx, app)
	if err != nil {
		return nil, err
	}
	input := UpdateGoalInput{ActorAgentID: caller.AgentID, ActorThreadID: caller.ThreadID, EventKey: caller.ToolCallID}
	var changed bool
	if value, ok := optionalStringArg(args, "status"); ok {
		input.Status, changed = &value, true
	}
	if value, ok := optionalStringArg(args, "current_phase"); ok {
		input.CurrentPhase, changed = &value, true
	}
	if value, ok := optionalStringArg(args, "summary"); ok {
		input.Summary, changed = &value, true
	}
	if value, ok := optionalStringArg(args, "next_action"); ok {
		input.NextAction, changed = &value, true
	}
	if !changed {
		return nil, errors.New("goal_update requires at least one changed field")
	}
	return a.store.UpdateGoal(identity, stringArg(args, "goal_id"), input)
}

func objectSchema(required []string, properties map[string]any) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func optionalStringArg(args map[string]any, key string) (string, bool) {
	value, exists := args[key]
	if !exists || value == nil {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}

func stringSliceArg(args map[string]any, key string) []string {
	var values []string
	_ = decodeArg(args[key], &values)
	return values
}

func mapArg(args map[string]any, key string) map[string]any {
	value, _ := args[key].(map[string]any)
	return nonNilMap(value)
}

func optionalMapArg(args map[string]any, key string) (map[string]any, bool) {
	value, exists := args[key]
	if !exists || value == nil {
		return map[string]any{}, false
	}
	mapped, ok := value.(map[string]any)
	return nonNilMap(mapped), ok
}

func intArg(args map[string]any, key string) int {
	switch value := args[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, _ := value.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func decodeArg(value any, out any) error {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func sortedKeys(values map[string]bool) []string {
	order := make([]string, 0, len(values))
	for key := range values {
		order = append(order, key)
	}
	// These enums are small. A local insertion sort avoids another package-level
	// dependency and makes generated schemas deterministic.
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && order[j] < order[j-1]; j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}
	return order
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
