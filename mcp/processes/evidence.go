package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

const detailBudget = 48 * 1024

// Large sections are lossless JSON, fetched in UTF-8 chunks with a content hash.
// Hash checks prevent a model concatenating pages from different checkpoints.
func (a *App) runEvidence(project, process string, args map[string]any) (any, error) {
	r, e := a.getRun(project, process, str(args, "run_id"))
	if e != nil {
		return nil, e
	}
	section := str(args, "section")
	var value any
	switch section {
	case "run":
		r.Steps = nil
		value = r
	case "definition":
		value, e = a.runDefinition(r)
	case "context":
		var d Definition
		d, e = a.runDefinition(r)
		if e == nil {
			value = executionContext(r, d)
		}
	case "steps":
		value, e = a.steps(r.ID)
	case "step":
		var all []StepRun
		all, e = a.steps(r.ID)
		found := false
		for _, s := range all {
			if s.ID == str(args, "step_id") {
				value = s
				found = true
				break
			}
		}
		if !found && e == nil {
			e = errNotFound
		}
	default:
		return nil, errors.New("section must be run, definition, context, steps or step")
	}
	if e != nil {
		return nil, e
	}
	b, e := json.Marshal(value)
	if e != nil {
		return nil, e
	}
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:])
	if expected := str(args, "sha256"); expected != "" && expected != hash {
		return nil, errors.New("evidence changed; restart from offset 0")
	}
	offset := number(args, "offset")
	if v, ok := args["offset"]; ok {
		f, valid := v.(float64)
		if !valid || f != float64(offset) {
			return nil, errors.New("offset must be an integer")
		}
	}
	if offset < 0 || offset > len(b) || !utf8.Valid(b[:offset]) {
		return nil, errors.New("invalid UTF-8 byte offset")
	}
	end := offset + 2048
	if end > len(b) {
		end = len(b)
	}
	for end > offset && !utf8.Valid(b[offset:end]) {
		end--
	}
	result := map[string]any{"process_id": process, "run_id": r.ID, "section": section, "encoding": "json-utf8", "offset": offset, "total_bytes": len(b), "sha256": hash, "data": string(b[offset:end]), "complete": end == len(b)}
	if end < len(b) {
		next := map[string]any{"process_id": process, "run_id": r.ID, "section": section, "offset": end, "sha256": hash}
		if section == "step" {
			next["step_id"] = str(args, "step_id")
		}
		result["next"] = RereadReference{Tool: "processes_run_evidence", Args: next}
	}
	return result, nil
}
func evidenceReference(process, run, section, step string, value any) map[string]any {
	b, _ := json.Marshal(value)
	sum := sha256.Sum256(b)
	args := map[string]any{"process_id": process, "run_id": run, "section": section, "offset": 0, "sha256": hex.EncodeToString(sum[:])}
	if step != "" {
		args["step_id"] = step
	}
	return map[string]any{"complete": false, "total_bytes": len(b), "reread": RereadReference{Tool: "processes_run_evidence", Args: args}}
}
func boundedRunRead(process string, r Run, d Definition, steps []StepRun, args map[string]any) (any, error) {
	result := mcpRunRead(process, r, d, steps)
	if section := str(args, "section"); section != "" && section != "all" {
		switch section {
		case "run", "definition", "steps":
			result = map[string]any{section: result[section], "reread": runReread(process, r.ID)}
		default:
			return nil, errors.New("section must be all, run, definition or steps")
		}
	}
	deferred := map[string]any{}
	for _, key := range []string{"steps", "run", "definition"} {
		b, _ := json.Marshal(result)
		if len(b) <= detailBudget-2048 {
			break
		}
		value, ok := result[key]
		if !ok {
			continue
		}
		delete(result, key)
		// Use the same exact stored section shape as run_evidence.
		exact := value
		if key == "steps" {
			exact = steps
		}
		if key == "run" {
			r.Steps = nil
			exact = r
		}
		deferred[key] = evidenceReference(process, r.ID, key, "", exact)
	}
	result["complete"] = len(deferred) == 0
	if len(deferred) > 0 {
		result["deferred_sections"] = deferred
		result["next_action"] = "Read every required deferred section through run_evidence before acting. Concatenate pages in byte-offset order and verify the hash; evidence is never summarized or truncated."
	}
	return result, nil
}

// Worker claims defer named values rather than asking the model to reconstruct
// whole step/run JSON through byte-offset pages. No evidence is truncated.
func (a *App) boundWorkerResponse(process string, r Run, current StepRun, response map[string]any, steps []StepRun) (map[string]any, error) {
	if len(current.Output) > handoffBudget {
		compact := workerStep(current)
		compact.Output = ""
		response["step"] = compact
		response["checkpoint_ref"] = targetedStepRef(process, r.ID, current.ID, "result", nil)
	}
	b, _ := json.Marshal(response)
	if len(b) <= workerDetailBudget-2048 {
		return response, nil
	}
	deps := workerDependencyEvidence(process, r, current, steps)
	for key, dep := range deps {
		dep.Output = ""
		dep.Deferred = true
		deps[key] = dep
	}
	response["dependencies"] = deps
	response["complete"] = false
	b, _ = json.Marshal(response)
	if len(b) > workerDetailBudget-2048 {
		refs := map[string]any{}
		for _, key := range []string{"instructions", "required_inputs", "default_inputs", "completion_criteria", "approval_requirements", "inputs"} {
			if _, exists := response[key]; exists {
				refs[key] = targetedStepRef(process, r.ID, current.ID, "shared", map[string]any{"field": key})
				delete(response, key)
			}
		}
		if _, exists := response["parameters"]; exists {
			refs["parameters"] = targetedStepRef(process, r.ID, current.ID, "parameters", nil)
			delete(response, "parameters")
		}

		delete(response, "context_values")
		delete(response, "context_encoding")
		response["context_refs"] = refs
		b, _ = json.Marshal(response)
		if len(b) > workerDetailBudget-2048 {
			response["step"] = map[string]any{"id": current.ID, "key": current.Key, "state": current.State, "executor": current.Executor, "instructions_ref": targetedStepRef(process, r.ID, current.ID, "instructions", nil)}
			if current.Output != "" || current.Error != "" {
				response["checkpoint_ref"] = targetedStepRef(process, r.ID, current.ID, "result", nil)
			}
		}
	}
	b, _ = json.Marshal(response)
	if len(b) > workerDetailBudget-2048 {
		response["dependencies"] = map[string]DependencyEvidence{}
		response["dependencies_ref"] = targetedStepRef(process, r.ID, current.ID, "dependencies", map[string]any{"direct_only": true})
	}

	response["next_action"] = "Recover required named instructions/context/dependency values with processes_step_read before acting; fetch only the evidence needed. Then follow done and ready_steps. No whole-run JSON reconstruction is needed."
	return response, nil
}

// Exact shared execution fields, excluding unrelated step definitions and run
// aggregates. This is the same policy/context supplied in ordinary claims.
func executionContext(r Run, d Definition) map[string]any {
	context := map[string]any{"instructions": d.Instructions, "required_inputs": d.RequiredInputs, "default_inputs": d.DefaultInputs, "completion_criteria": d.CompletionCriteria, "approval_requirements": d.ApprovalRequirements, "inputs": r.Inputs, "parameters": r.Binding.Parameters, "assignment": WorkerAssignment{ID: r.AssignmentID, Revision: r.AssignmentRevision, Name: r.Binding.Name, OwnerAgentID: r.Binding.OwnerAgentID}}
	if len(d.SummaryFields) > 0 {
		context["summary_fields"] = d.SummaryFields
	}
	return context
}
