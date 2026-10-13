package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type actorRecoveryPolicy struct {
	InspectionOperation string                 `json:"inspection_operation"`
	RequiredViews       []string               `json:"required_views"`
	MatchFields         map[string]string      `json:"match_fields"`
	Existing            *actorExistingRecovery `json:"existing,omitempty"`
}

func recoveryPolicyValid(p *actorRecoveryPolicy) error {
	if p == nil || !operationNamePattern.MatchString(p.InspectionOperation) || len(p.RequiredViews) == 0 || len(p.MatchFields) == 0 {
		return errors.New("recovery requires an inspection operation, required views and duplicate match fields")
	}
	if p.Existing != nil {
		if err := validateExistingRecovery(p.Existing); err != nil {
			return err
		}
	}
	if len(p.RequiredViews) > 20 || len(p.MatchFields) > 20 {
		return errors.New("recovery policy too large")
	}
	for k, v := range p.MatchFields {
		if k == "" || v == "" {
			return errors.New("empty recovery match field")
		}
	}
	return nil
}

func proveUncommitted(run map[string]any, def actorDefinition) error {
	if run["status"] != "failed" {
		return errors.New("recovery requires a failed run; successful, active, cancelled or unknown outcomes block")
	}
	output := mapFromAny(run["output"])
	if effects, ok := output["effects"].([]any); ok && len(effects) > 0 {
		return errors.New("original run attempted a guarded consequence; reconcile its result instead")
	}
	if progress := mapFromAny(output["execution_progress"]); len(progress) > 0 {
		attempted, ok := progress["commit_attempted"].(bool)
		if !ok || attempted {
			return errors.New("original commit outcome is unknown or attempted")
		}
		started := intArg(progress, "last_started_step")
		if started < 0 || started >= len(def.Steps) {
			return errors.New("original execution progress is incomplete")
		}
		for index, step := range def.Steps[:started+1] {
			if step.OnceKey != "" || step.ConfirmConsequence != "" || consequentialActorStep(step) {
				return errors.New("original execution reached a consequence")
			}
			// Missing effect annotations cannot prove that a dispatched click
			// was harmless. Only the terminal engine SOM lookup failure proves
			// that an unclassified click never dispatched.
			if (step.Action == "click" || step.Action == "click_verified") && step.ExpectedEffect == "" && !(index == started && strings.HasPrefix(stringFromAny(run["error"]), "locator not found in SOM:")) {
				return errors.New("original execution contains an interaction with an unknown effect")
			}
			if step.Action == "key" && step.ExpectedEffect == "" && step.Key != "ESC" && step.Key != "CTRL+R" {
				return errors.New("original key interaction has an unknown commit outcome")
			}
		}
		return nil
	}
	// Legacy evidence is accepted only for a complete contiguous read-only prefix
	// ending in an engine locator failure before dispatch. A preview cut off before
	// its terminal failure is never proof of an uncommitted outcome.
	trace, _ := output["trace_preview"].([]any)
	next := 0
	for _, raw := range trace {
		event := mapFromAny(raw)
		step, ok := event["step"]
		if !ok {
			continue
		}
		n := intFromAny(step)
		if n != next || n < 0 || n >= len(def.Steps) {
			return errors.New("legacy trace is incomplete")
		}
		original := def.Steps[n]
		if event["status"] == "failed" {
			message := stringFromAny(event["message"])
			if original.Action == "click" && original.OnceKey == "" && original.ConfirmConsequence == "" && !consequentialActorStep(original) && strings.HasPrefix(message, "locator not found in SOM:") && message == run["error"] {
				return nil
			}
			return errors.New("legacy failure does not prove no dispatch")
		}
		if event["status"] != "completed" || !readOnlyActorAction(original.Action) {
			return errors.New("legacy trace includes an unverified interaction")
		}
		next++
	}
	return errors.New("original outcome lacks complete no-commit evidence")
}

func consequentialActorStep(s actorStep) bool {
	switch s.ExpectedEffect {
	case "", "navigation_only", "open_configuration", "save_draft":
		return false
	default:
		return true
	}
}

func guardedKeys(def actorDefinition) map[string]string {
	out := map[string]string{}
	for _, s := range def.Steps {
		if s.OnceKey != "" {
			out[s.OnceKey] = s.ExpectedEffect
		}
	}
	return out
}

func checkRecoveryCoverage(output map[string]any, policy *actorRecoveryPolicy, contextID, identity string) error {
	b, _ := json.Marshal(output["coverage"])
	var cov actorReadCoverage
	if json.Unmarshal(b, &cov) != nil || cov.ResumedFrom != "" || !cov.ReadOnly || !cov.Complete || cov.MoreRemaining || cov.ContextID != contextID || cov.IdentityURL != identity {
		return errors.New("duplicate inspection must be complete, read-only and in the original creator context")
	}
	seen := map[string]bool{}
	for _, v := range cov.CheckedViews {
		seen[v] = true
	}
	for _, v := range cov.Views {
		if v.Checked && v.Complete && !v.MoreRemaining && v.Error == "" && v.EndEvidence != "" {
			seen[v.Name] = true
		}
	}
	for _, v := range policy.RequiredViews {
		if !seen[v] {
			return fmt.Errorf("duplicate inspection did not cover %s", v)
		}
	}
	for _, v := range cov.Views {
		if !v.Checked || !v.Complete || v.MoreRemaining || v.Error != "" || v.EndEvidence == "" {
			return errors.New("duplicate view coverage is incomplete")
		}
	}
	return nil
}

func checkRecoveryDuplicates(items []map[string]any, policy *actorRecoveryPolicy, vars map[string]any) error {
	for _, inputField := range policy.MatchFields {
		if stringFromAny(vars[inputField]) == "" {
			return fmt.Errorf("logical duplicate identity %s is missing", inputField)
		}
	}
	existingMatches := 0
	for _, item := range items {
		if policy.Existing != nil && stringFromAny(item[policy.Existing.Bindings.DuplicateIDField]) == stringFromAny(vars["recovery_resource_id"]) {
			if item[policy.Existing.Bindings.DuplicateStatusField] != policy.Existing.Bindings.DraftStatus || !sameRecoveryResourceURL(policy.Existing, stringFromAny(item[policy.Existing.Bindings.DuplicateURLField]), stringFromAny(vars["recovery_resource_url"])) {
				return errors.New("saved recovery resource is not the same draft")
			}
			existingMatches++
			continue
		}
		matches := true
		for field, inputField := range policy.MatchFields {
			actual, ok := item[field]
			if !ok {
				return fmt.Errorf("duplicate inspection item lacks %s", field)
			}
			if stringFromAny(actual) != stringFromAny(vars[inputField]) {
				matches = false
			}
		}
		if matches {
			return errors.New("matching existing record found; reconcile it instead of creating another")
		}
	}
	if policy.Existing != nil && existingMatches != 1 {
		return errors.New("complete inspection must contain exactly one original draft")
	}
	return nil
}

func (a *App) toolActorRunRecover(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, revision, checkID := int64ArgLocal(args, "id"), intArg(args, "revision"), int64ArgLocal(args, "duplicate_check_run_id")
	if id <= 0 || revision <= 0 || checkID <= 0 || !boolArgDefault(args, "reviewed", false) {
		return nil, errors.New("id, explicitly reviewed revision and duplicate_check_run_id required")
	}
	original, err := getActorRun(ctx, id)
	if err != nil || original == nil {
		return nil, errors.New("original run not found")
	}
	actorID := int64ArgLocal(original, "actor_id")
	rec, err := getActor(ctx, actorID, "")
	if err != nil || rec == nil || !rec.Enabled {
		return nil, errors.New("original actor is missing or disabled")
	}
	if err := loadActorRevision(ctx, rec, revision); err != nil {
		return nil, err
	}
	rawInput, _ := json.Marshal(original["input"])
	input := mapFromAny(original["input"])
	operation := firstNonEmpty(stringFromAny(input["operation"]), "run")
	selected, err := selectOperation(rec.Definition, operation)
	if err != nil {
		return nil, err
	}
	if err := recoveryPolicyValid(selected.Recovery); err != nil {
		return nil, err
	}
	oldRaw, _ := json.Marshal(original["definition_snapshot"])
	// Recovery policy is supplied by the reviewed revision. Historical step,
	// browser, default and input semantics remain unchanged.
	var oldSnapshot actorDefinition
	json.Unmarshal(oldRaw, &oldSnapshot)
	oldSnapshot.Recovery = nil
	oldRaw, _ = json.Marshal(oldSnapshot)
	oldExec, err := newActorExecution(context.Background(), a, ctx, &actorQueuedRun{InputJSON: string(rawInput), DefinitionSnapshot: string(oldRaw)})
	if err != nil {
		return nil, err
	}
	var existingBinding map[string]any
	if selected.Recovery.Existing != nil {
		rawInput, selected, existingBinding, err = a.prepareExistingRecovery(ctx, args, rec, original, oldExec, selected, rawInput)
		if err != nil {
			return nil, err
		}
	} else if err := proveUncommitted(original, oldExec.definition); err != nil {
		return nil, err
	}
	rawNew, _ := json.Marshal(selected)
	newExec, err := newActorExecution(context.Background(), a, ctx, &actorQueuedRun{InputJSON: string(rawInput), DefinitionSnapshot: string(rawNew)})
	if err != nil {
		return nil, err
	}
	if oldExec.definition.Browser.ContextID != newExec.definition.Browser.ContextID {
		return nil, errors.New("replacement revision changes original creator context")
	}
	for _, inputField := range selected.Recovery.MatchFields {
		if !reflect.DeepEqual(oldExec.variables[inputField], newExec.variables[inputField]) {
			return nil, errors.New("replacement revision changes logical request identity")
		}
	}
	keys := guardedKeys(oldExec.definition)
	if len(keys) == 0 || !reflect.DeepEqual(keys, guardedKeys(newExec.definition)) || !reflect.DeepEqual(guardedScopes(oldExec.definition), guardedScopes(newExec.definition)) {
		return nil, errors.New("recovery must preserve the original publish-once keys and effects")
	}
	for key := range keys {
		scope := operation
		for _, step := range oldExec.definition.Steps {
			if step.OnceKey == key && step.OnceScope != "" {
				scope = step.OnceScope
			}
		}
		var count int
		if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM actors_effect_guards WHERE project_id=? AND actor_id=? AND operation=? AND once_key=?`, projectID(ctx), actorID, scope, key).Scan(&count); err != nil {
			return nil, err
		}
		if count > 0 {
			return nil, errors.New("original logical request has a reserved/uncertain/completed consequence")
		}
	}
	check, err := getActorRun(ctx, checkID)
	if err != nil || check == nil {
		return nil, errors.New("duplicate inspection run not found")
	}
	checkInput := mapFromAny(check["input"])
	if check["status"] != "completed" || int64ArgLocal(check, "actor_id") != actorID || intArg(check, "actor_revision") != revision || checkInput["operation"] != selected.Recovery.InspectionOperation {
		return nil, errors.New("duplicate inspection must use the reviewed revision and configured read-only operation")
	}
	completed, err := time.Parse(time.RFC3339, stringFromAny(check["completed_at"]))
	if err != nil || time.Since(completed) > 15*time.Minute || completed.After(time.Now().Add(time.Second)) {
		return nil, errors.New("duplicate inspection is stale")
	}
	originalCompleted, err := time.Parse(time.RFC3339, stringFromAny(original["completed_at"]))
	if err != nil || completed.Before(originalCompleted) {
		return nil, errors.New("duplicate inspection predates the failure")
	}
	checkRaw, _ := json.Marshal(check["definition_snapshot"])
	checkInputRaw, _ := json.Marshal(check["input"])
	checkExec, err := newActorExecution(context.Background(), a, ctx, &actorQueuedRun{InputJSON: string(checkInputRaw), DefinitionSnapshot: string(checkRaw)})
	if err != nil {
		return nil, err
	}
	configured, err := selectOperation(rec.Definition, selected.Recovery.InspectionOperation)
	if err != nil || !configured.ReadOnly || !checkExec.definition.ReadOnly {
		return nil, errors.New("inspection operation is not read-only")
	}
	canonical, _ := json.Marshal(configured)
	var canonicalValue, checkedValue any
	json.Unmarshal(canonical, &canonicalValue)
	json.Unmarshal(checkRaw, &checkedValue)
	if !reflect.DeepEqual(canonicalValue, checkedValue) {
		return nil, errors.New("inspection definition is not the reviewed immutable snapshot")
	}
	identity := ""
	for _, s := range checkExec.definition.Steps {
		if s.ReadViews != nil {
			identity = s.ReadViews.IdentityURL
		}
	}
	// Re-render the inspection with the preserved publishing inputs to prove the
	// supplied read did not inspect a different account through overrides.
	wantedCheck, err := newActorExecution(context.Background(), a, ctx, &actorQueuedRun{InputJSON: string(rawInput), DefinitionSnapshot: string(checkRaw)})
	if err != nil {
		return nil, err
	}
	wantedIdentity := ""
	for _, s := range wantedCheck.definition.Steps {
		if s.ReadViews != nil {
			wantedIdentity = s.ReadViews.IdentityURL
		}
	}
	if identity == "" || identity != wantedIdentity || checkExec.definition.Browser.ContextID != newExec.definition.Browser.ContextID || wantedCheck.definition.Browser.ContextID != newExec.definition.Browser.ContextID {
		return nil, errors.New("duplicate inspection creator/context differs from original request")
	}
	if err := checkRecoveryCoverage(mapFromAny(check["output"]), selected.Recovery, newExec.definition.Browser.ContextID, identity); err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(`SELECT item_json FROM actors_dataset_items WHERE project_id=? AND run_id=? ORDER BY id`, projectID(ctx), checkID)
	if err != nil {
		return nil, err
	}
	items := []map[string]any{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			break
		}
		var item map[string]any
		err = json.Unmarshal([]byte(raw), &item)
		if err != nil {
			break
		}
		items = append(items, item)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}
	if len(items) != intArg(mapFromAny(check["output"]), "item_count") {
		return nil, errors.New("duplicate inspection dataset is incomplete")
	}
	if err := checkRecoveryDuplicates(items, selected.Recovery, newExec.variables); err != nil {
		return nil, err
	}
	trigger := mapFromAny(original["trigger"])
	root := int64ArgLocal(trigger, "recovery_root")
	if root == 0 {
		root = id
	}
	recovery := map[string]any{"kind": "recovery", "recovery_of": id, "recovery_root": root, "reviewed_revision": revision, "duplicate_check_run_id": checkID, "original_trigger_key": trigger["trigger_key"], "inspection_definition": json.RawMessage(checkRaw), "trigger_key": fmt.Sprintf("recovery:%d", id), "queued_at": time.Now().UTC().Format(time.RFC3339)}
	if existingBinding != nil {
		recovery["existing_resource"] = existingBinding
	}
	newID, status, duplicate, err := enqueueActorSnapshot(ctx, actorID, revision, string(rawInput), string(rawNew), recovery)
	if err != nil {
		return nil, err
	}
	if !duplicate {
		ctx.Emit("actor.run.queued", map[string]any{"run_id": newID, "actor_id": actorID, "recovery_of": id, "revision": revision})
	}
	return map[string]any{"run_id": newID, "status": status, "duplicate": duplicate, "recovery_of": id, "recovery_root": root, "actor_revision": revision}, nil
}

func (e *actorExecution) recoveryPrecheck() error {
	var trigger map[string]any
	if json.Unmarshal([]byte(e.run.TriggerJSON), &trigger) != nil || trigger["kind"] != "recovery" {
		return nil
	}
	if err := recoveryPolicyValid(e.definition.Recovery); err != nil {
		return err
	}
	raw, _ := json.Marshal(trigger["inspection_definition"])
	var input map[string]any
	if err := json.Unmarshal([]byte(e.run.InputJSON), &input); err != nil {
		return err
	}
	input["operation"] = e.definition.Recovery.InspectionOperation
	inputRaw, _ := json.Marshal(input)
	inspectionTrigger, _ := json.Marshal(map[string]any{"kind": "recovery_inspection", "parent_run_id": e.run.ID})
	inspection, err := newActorExecution(e.workerCtx, e.app, e.ctx, &actorQueuedRun{InputJSON: string(inputRaw), DefinitionSnapshot: string(raw)})
	if err != nil || !inspection.definition.ReadOnly {
		return errors.New("recovery inspection snapshot is invalid")
	}
	// A separate immutable child run keeps full inspection records/artifacts out
	// of the publishing dataset and records fresh evidence in ordinary run history.
	result, err := e.ctx.AppDB().Exec(`INSERT INTO actors_runs(project_id,kind,input_json,status,actor_id,actor_revision,definition_snapshot_json,trigger_json) VALUES(?,?,?,'running',?,?,?,?)`, projectID(e.ctx), "actor", string(inputRaw), e.run.ActorID, e.run.ActorRevision, string(raw), string(inspectionTrigger))
	if err != nil {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	queued := &actorQueuedRun{ID: id, ActorID: e.run.ActorID, ActorRevision: e.run.ActorRevision, InputJSON: string(inputRaw), DefinitionSnapshot: string(raw), TriggerJSON: string(inspectionTrigger)}
	if err := e.app.executeActorRun(e.workerCtx, e.ctx, queued); err != nil {
		return err
	}
	checked, err := getActorRun(e.ctx, id)
	if err != nil {
		return err
	}
	output := mapFromAny(checked["output"])
	e.recoveryCheck = map[string]any{"run_id": id, "status": checked["status"], "coverage": output["coverage"], "duplicate_check_run_id": trigger["duplicate_check_run_id"]}
	if checked["status"] != "completed" {
		return fmt.Errorf("fresh recovery duplicate inspection failed: %v", checked["error"])
	}
	identity := ""
	for _, step := range inspection.definition.Steps {
		if step.ReadViews != nil {
			identity = step.ReadViews.IdentityURL
		}
	}
	if err := checkRecoveryCoverage(output, e.definition.Recovery, e.definition.Browser.ContextID, identity); err != nil {
		return err
	}
	rows, err := e.ctx.AppDB().Query(`SELECT item_json FROM actors_dataset_items WHERE project_id=? AND run_id=? ORDER BY id`, projectID(e.ctx), id)
	if err != nil {
		return err
	}
	var items []map[string]any
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			break
		}
		var item map[string]any
		err = json.Unmarshal([]byte(raw), &item)
		if err != nil {
			break
		}
		items = append(items, item)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	if len(items) != intArg(output, "item_count") {
		return errors.New("fresh inspection dataset incomplete")
	}
	if err := checkRecoveryDuplicates(items, e.definition.Recovery, e.variables); err != nil {
		return err
	}
	e.addTrace("recovery.duplicate_check", "completed", "fresh complete duplicate inspection found no matching record", map[string]any{"inspection_run_id": id})
	return nil
}

func guardedScopes(def actorDefinition) map[string]string {
	out := map[string]string{}
	for _, s := range def.Steps {
		if s.OnceKey != "" {
			out[s.OnceKey] = s.OnceScope
		}
	}
	return out
}
