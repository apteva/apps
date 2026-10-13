package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// An explicitly reviewed resume operation must contain no creation, upload or
// editing actions. Its inputs and durable effect scope remain the original run.
var errActorRecoveryBinding = errors.New("recovery effect was not dispatched")

type actorExistingRecovery struct {
	Bindings            actorRecoveryBindings `json:"bindings"`
	ResumeOperation     string                `json:"resume_operation"`
	InspectionOperation string                `json:"inspection_operation"`
	URLPattern          string                `json:"url_pattern"`
	InspectionLocators  []actorLocator        `json:"inspection_locators,omitempty"`
	PreparationLocators []actorLocator        `json:"preparation_locators,omitempty"`
}

type actorRecoveryBindings struct {
	ResourceIDField      string                    `json:"resource_id_field"`
	ResourceIDInput      string                    `json:"resource_id_input"`
	ResourceURLInput     string                    `json:"resource_url_input"`
	CreatorURLInput      string                    `json:"creator_url_input"`
	ExpectedIDsInput     string                    `json:"expected_ids_input"`
	ExpectedNamesInput   string                    `json:"expected_names_input"`
	ExpectedSizesInput   string                    `json:"expected_sizes_input"`
	IDsField             string                    `json:"ids_field"`
	NamesField           string                    `json:"names_field"`
	SizesField           string                    `json:"sizes_field"`
	StatesField          string                    `json:"states_field"`
	TypesField           string                    `json:"types_field"`
	ReadyValue           string                    `json:"ready_value"`
	TypeValue            string                    `json:"type_value"`
	DuplicateIDField     string                    `json:"duplicate_id_field"`
	DuplicateURLField    string                    `json:"duplicate_url_field"`
	DuplicateStatusField string                    `json:"duplicate_status_field"`
	DraftStatus          string                    `json:"draft_status"`
	StateAssertions      map[string]actorAssertion `json:"state_assertions"`
}

func validateRecoveryBindings(b actorRecoveryBindings) error {
	for _, v := range []string{b.ResourceIDField, b.ResourceIDInput, b.ResourceURLInput, b.CreatorURLInput, b.ExpectedIDsInput, b.ExpectedNamesInput, b.ExpectedSizesInput, b.IDsField, b.NamesField, b.SizesField, b.StatesField, b.TypesField, b.ReadyValue, b.TypeValue, b.DuplicateIDField, b.DuplicateURLField, b.DuplicateStatusField, b.DraftStatus} {
		if v == "" || len(v) > 100 || actorTemplateValue(v) {
			return errors.New("existing recovery requires literal resource/upload/status field bindings")
		}
	}
	if len(b.StateAssertions) == 0 || len(b.StateAssertions) > 20 {
		return errors.New("existing recovery requires explicit uncommitted resource state assertions")
	}
	for field, a := range b.StateAssertions {
		if field == "" || len(field) > 100 || a.EqualsExact == nil || a.EqualsField != "" || a.Equals != nil || a.Matches != "" {
			return errors.New("resource state must be asserted by literal equals_exact")
		}
	}
	return nil
}
func validateExistingRecovery(p *actorExistingRecovery) error {
	if err := validateRecoveryBindings(p.Bindings); err != nil {
		return err
	}
	if !operationNamePattern.MatchString(p.ResumeOperation) || !operationNamePattern.MatchString(p.InspectionOperation) || len(p.PreparationLocators) > 10 || len(p.InspectionLocators) > 10 {
		return errors.New("existing recovery requires named resume and inspection operations")
	}
	r, err := regexp.Compile(p.URLPattern)
	if err != nil || r.NumSubexp() != 1 {
		return errors.New("existing recovery URL pattern requires one resource-ID capture")
	}
	return nil
}
func validateResumeSteps(d actorDefinition, p *actorExistingRecovery) error {
	commits := 0
	for _, s := range d.Steps {
		switch s.Action {
		case "goto":
			if s.URLFromField != "current_url" && s.URL != "{{"+p.Bindings.CreatorURLInput+"}}" && s.URL != "{{"+p.Bindings.ResourceURLInput+"}}" {
				return errors.New("resume navigation must use the original creator or verified saved draft")
			}
		case "inspect_control":
			allowed := false
			for _, l := range p.InspectionLocators {
				if reflect.DeepEqual(l, s.Locator) {
					allowed = true
				}
			}
			if !allowed {
				return errors.New("resume inspection opener is not explicitly approved")
			}
		case "wait", "wait_for", "observe_page", "observe_json", "dismiss_inspection", "extract", "assert_url", "assert_element", "assert_values", "inspect_views", "select_record", "screenshot":
		case "key":
			if s.Key != "ESC" {
				return errors.New("same-resource recovery cannot send editor keys")
			}
		case "click", "click_verified":
			if s.OnceKey != "" && s.ExpectedEffect == "immediate_external_commit" && s.ConfirmConsequence == s.ExpectedEffect {
				if !s.Locator.Exact || !s.Locator.SOMOnly || s.Locator.Selector != "" {
					return errors.New("resume commit requires an exact semantic target")
				}
				commits++
			} else {
				allowed := false
				for _, l := range p.InspectionLocators {
					if reflect.DeepEqual(l, s.Locator) {
						allowed = true
					}
				}
				if !allowed || s.ExpectedEffect != "open_configuration" {
					return errors.New("resume click is not an approved inspection control")
				}
			}
		default:
			return fmt.Errorf("same-resource recovery cannot execute %s", s.Action)
		}
	}
	if commits != 1 {
		return errors.New("same-resource recovery requires exactly one guarded commit")
	}
	return nil
}
func (a *App) prepareExistingRecovery(ctx *sdk.AppCtx, args map[string]any, rec *actorRecord, original map[string]any, old *actorExecution, selected actorDefinition, rawInput []byte) ([]byte, actorDefinition, map[string]any, error) {
	fail := func(err error) ([]byte, actorDefinition, map[string]any, error) { return nil, selected, nil, err }
	p := selected.Recovery.Existing
	b := p.Bindings
	// Explicitly reviewed preparation locators repair missing historical effect
	// annotations only; terminal reads and all guarded commit checks still apply.
	proof := old.definition
	proof.Steps = append([]actorStep(nil), proof.Steps...)
	progress := mapFromAny(mapFromAny(original["output"])["execution_progress"])
	last := intArg(progress, "last_started_step")
	if last < 0 || last >= len(proof.Steps) || !readOnlyActorAction(proof.Steps[last].Action) {
		return fail(errors.New("same-resource recovery needs a terminal read failure"))
	}
	for i, s := range proof.Steps {
		if s.Action == "click" && s.ExpectedEffect == "" && s.OnceKey == "" && s.ConfirmConsequence == "" {
			for _, l := range p.PreparationLocators {
				if reflect.DeepEqual(l, s.Locator) {
					proof.Steps[i].ExpectedEffect = "open_configuration"
				}
			}
		}
	}
	if err := proveUncommitted(original, proof); err != nil {
		return fail(err)
	}
	url := stringFromAny(mapFromAny(original["output"])["current_url"])
	match := regexp.MustCompile(p.URLPattern).FindStringSubmatch(url)
	if len(match) != 2 || match[0] != url || !hostAllowed(url, selected.AllowedHosts) {
		return fail(errors.New("failed run has no exact saved-resource URL evidence"))
	}
	check, err := getActorRun(ctx, int64ArgLocal(args, "resource_check_run_id"))
	if err != nil || check == nil {
		return fail(errors.New("resource_check_run_id is required for existing recovery"))
	}
	ci := mapFromAny(check["input"])
	if check["status"] != "completed" || int64ArgLocal(check, "actor_id") != rec.ID || intArg(check, "actor_revision") != rec.Revision || ci["operation"] != p.InspectionOperation {
		return fail(errors.New("resource inspection must use the reviewed actor revision and operation"))
	}
	completed, err := time.Parse(time.RFC3339, stringFromAny(check["completed_at"]))
	failed, fer := time.Parse(time.RFC3339, stringFromAny(original["completed_at"]))
	if err != nil || fer != nil || completed.Before(failed) || time.Since(completed) > 15*time.Minute || completed.After(time.Now().Add(time.Second)) {
		return fail(errors.New("saved-resource inspection is stale"))
	}
	inspect, err := selectOperation(rec.Definition, p.InspectionOperation)
	if err != nil || !inspect.ReadOnly {
		return fail(errors.New("resource inspection must be read-only"))
	}
	canonical, _ := json.Marshal(inspect)
	actual, _ := json.Marshal(check["definition_snapshot"])
	var cv, av any
	json.Unmarshal(canonical, &cv)
	json.Unmarshal(actual, &av)
	if !reflect.DeepEqual(cv, av) {
		return fail(errors.New("resource inspection snapshot differs from reviewed revision"))
	}
	ciRaw, _ := json.Marshal(ci)
	ex, err := newActorExecution(context.Background(), a, ctx, &actorQueuedRun{InputJSON: string(ciRaw), DefinitionSnapshot: string(canonical)})
	if err != nil || ex.definition.Browser.ContextID != old.definition.Browser.ContextID {
		return fail(errors.New("resource inspection context differs from original"))
	}
	for key, originalValue := range old.variables {
		if !reflect.DeepEqual(ex.variables[key], originalValue) {
			return fail(fmt.Errorf("resource inspection changes original %s", key))
		}
	}
	evidence := mapFromAny(mapFromAny(check["output"])["document_evidence"])
	if stringFromAny(evidence[b.ResourceIDField]) != match[1] || ex.variables[b.ResourceIDInput] != match[1] || !sameRecoveryResourceURL(p, stringFromAny(ex.variables[b.ResourceURLInput]), url) {
		return fail(errors.New("resource inspection is not the original unscheduled draft"))
	}
	stateCheck := actorExecution{lastValues: evidence}
	if err := stateCheck.assertValues(actorStep{Assertions: b.StateAssertions}); err != nil {
		return fail(err)
	}
	names, _ := evidence[b.NamesField].([]any)
	sizes, _ := evidence[b.SizesField].([]any)
	ids, _ := evidence[b.IDsField].([]any)
	states, _ := evidence[b.StatesField].([]any)
	types, _ := evidence[b.TypesField].([]any)
	media, _ := mapFromAny(original["output"])["media"].([]any)
	if len(media) == 0 || len(media) != len(names) || len(ids) != len(media) || len(sizes) != len(media) || len(states) != len(media) || len(types) != len(media) {
		return fail(errors.New("saved attachment cardinality differs from original uploads"))
	}
	seen := map[string]bool{}
	for i, m := range media {
		v := mapFromAny(m)
		id := stringFromAny(ids[i])
		if v["uploaded"] != true || names[i] != v["filename"] || !actorValuesEqual(sizes[i], v["size_bytes"], 0) || states[i] != b.ReadyValue || types[i] != b.TypeValue || id == "" || seen[id] {
			return fail(errors.New("saved attachment identity/order does not match original upload evidence"))
		}
		seen[id] = true
	}
	var ri map[string]any
	json.Unmarshal(rawInput, &ri)
	inputs := mapFromAny(ri["input"])
	for _, key := range []string{b.ResourceIDInput, b.ResourceURLInput, b.ExpectedIDsInput, b.ExpectedNamesInput, b.ExpectedSizesInput, "recovery_resource_id", "recovery_resource_url"} {
		if _, exists := inputs[key]; exists {
			return fail(errors.New("original request already uses protected recovery inputs"))
		}
	}
	url = stringFromAny(ex.variables[b.ResourceURLInput])
	inputs[b.ResourceIDInput] = match[1]
	inputs[b.ResourceURLInput] = url
	inputs[b.ExpectedIDsInput] = ids
	inputs[b.ExpectedNamesInput] = names
	inputs[b.ExpectedSizesInput] = sizes
	inputs["recovery_resource_id"] = match[1]
	inputs["recovery_resource_url"] = url
	ri["input"] = inputs
	resume, err := selectOperation(rec.Definition, p.ResumeOperation)
	if err != nil {
		return fail(err)
	}
	resume.Recovery = selected.Recovery
	if err := validateResumeSteps(resume, p); err != nil {
		return fail(err)
	}
	next, _ := json.Marshal(ri)
	binding := map[string]any{"resource_id": match[1], "resource_url": url, "attachment_ids": ids, "id_field": b.ResourceIDField, "ids_field": b.IDsField, "resource_check_run_id": check["id"], "resume_operation": p.ResumeOperation}
	return next, resume, binding, nil
}
func (e *actorExecution) guardExistingRecovery(step actorStep) error {
	var trigger map[string]any
	json.Unmarshal([]byte(e.run.TriggerJSON), &trigger)
	binding := mapFromAny(trigger["existing_resource"])
	if len(binding) == 0 || !consequentialActorStep(step) {
		return nil
	}
	if e.currentURL != binding["resource_url"] || e.lastValues[stringFromAny(binding["id_field"])] != binding["resource_id"] || !reflect.DeepEqual(e.lastValues[stringFromAny(binding["ids_field"])], binding["attachment_ids"]) {
		return fmt.Errorf("%w: commit is not bound to the original saved resource and ordered attachments", errActorRecoveryBinding)
	}
	return nil
}

func sameRecoveryResourceURL(p *actorExistingRecovery, a, b string) bool {
	r := regexp.MustCompile(p.URLPattern)
	x, y := r.FindStringSubmatch(a), r.FindStringSubmatch(b)
	return len(x) == 2 && len(y) == 2 && x[0] == a && y[0] == b && x[1] == y[1]
}
