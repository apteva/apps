package main

import (
	"bytes"
	"encoding/json"
	"errors"

	sdk "github.com/apteva/app-sdk"
)

var objectSchema = map[string]any{"type": "object", "additionalProperties": true}

func requiredSchema(fields ...string) map[string]any {
	props := map[string]any{}
	for _, field := range fields {
		props[field] = map[string]any{"type": "string"}
	}
	return map[string]any{"type": "object", "properties": props, "required": fields, "additionalProperties": true}
}
func decodeStrictArgs(args map[string]any, value any) error {
	clean := make(map[string]any, len(args))
	for key, value := range args {
		if key != "_project_id" {
			clean[key] = value
		}
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}
func str(args map[string]any, key string) string { value, _ := args[key].(string); return value }

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "eval_suite_list", Description: "List evaluation suites and cases.", InputSchema: objectSchema, Handler: func(*sdk.AppCtx, map[string]any) (any, error) { return a.svc.db.listSuites() }},
		{Name: "eval_suite_get", Description: "Get one evaluation suite.", InputSchema: requiredSchema("id"), Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.svc.db.getSuite(str(args, "id")) }},
		{Name: "eval_suite_create", Description: "Create an evaluation suite. Add cases with eval_case_create, then execute them with eval_experiment_create.", InputSchema: evalSuiteCreateSchema, Handler: a.toolSaveSuite(true)},
		{Name: "eval_suite_update", Description: "Update an evaluation suite.", InputSchema: requiredSchema("id", "name"), Handler: a.toolSaveSuite(false)},
		{Name: "eval_suite_delete", Description: "Delete an evaluation suite without history.", InputSchema: requiredSchema("id"), Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
			err := a.svc.db.deleteSuite(str(args, "id"))
			return map[string]bool{"ok": err == nil}, err
		}},
		{Name: "eval_case_create", Description: "Add a behavioral case to a suite. Execute cases with eval_experiment_create; do not call environment_run_create once per case.", InputSchema: evalCaseCreateSchema, Handler: a.toolSaveCase(true)},
		{Name: "eval_case_update", Description: "Update a behavioral case.", InputSchema: requiredSchema("id", "suite_id", "name", "prompt"), Handler: a.toolSaveCase(false)},
		{Name: "eval_case_delete", Description: "Delete a case without run history.", InputSchema: requiredSchema("id"), Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
			err := a.svc.db.deleteCase(str(args, "id"))
			return map[string]bool{"ok": err == nil}, err
		}},
		{Name: "eval_experiment_create", Description: "Execute a suite's enabled cases against agent/model targets. Create cases first; Evals creates and stops the isolated runtimes.", InputSchema: evalExperimentCreateSchema, Handler: a.toolCreateExperiment},
		{Name: "eval_experiment_list", Description: "List evaluation experiments.", InputSchema: objectSchema, Handler: func(*sdk.AppCtx, map[string]any) (any, error) { return a.svc.db.listExperiments(100) }},
		{Name: "eval_experiment_get", Description: "Get an experiment and all runs.", InputSchema: requiredSchema("id"), Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.svc.db.getExperiment(str(args, "id")) }},
		{Name: "eval_experiment_cancel", Description: "Cancel queued experiment runs.", InputSchema: requiredSchema("id"), Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
			err := a.svc.db.cancelExperiment(str(args, "id"))
			return map[string]bool{"ok": err == nil}, err
		}},
		{Name: "eval_run_get", Description: "Inspect one evaluation run.", InputSchema: requiredSchema("id"), Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.svc.db.getRun(str(args, "id")) }},
		{Name: "eval_run_retry", Description: "Queue a fresh retry of one run.", InputSchema: requiredSchema("id"), Handler: a.toolRetryRun},
		{Name: "eval_compare", Description: "Compare targets in an experiment.", InputSchema: requiredSchema("experiment_id"), Handler: a.toolCompare},
		{Name: "eval_catalog", Description: "List agents, models, and Environments.", InputSchema: objectSchema, Handler: func(*sdk.AppCtx, map[string]any) (any, error) { return a.svc.catalog() }},
		{Name: "eval_suggestion_apply", Description: "Apply an accepted directive suggestion.", InputSchema: requiredSchema("id"), Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.svc.applySuggestion(str(args, "id")) }},
	}
}

func (a *App) toolSaveSuite(creating bool) sdk.ToolHandler {
	return func(_ *sdk.AppCtx, args map[string]any) (any, error) {
		var item Suite
		if err := decodeStrictArgs(args, &item); err != nil {
			return nil, err
		}
		return a.svc.saveSuite(&item, creating)
	}
}
func (a *App) toolSaveCase(creating bool) sdk.ToolHandler {
	return func(_ *sdk.AppCtx, args map[string]any) (any, error) {
		var item Case
		if err := decodeStrictArgs(args, &item); err != nil {
			return nil, err
		}
		return a.svc.saveCase(&item, creating)
	}
}
func (a *App) toolCreateExperiment(_ *sdk.AppCtx, args map[string]any) (any, error) {
	var input experimentInput
	if err := decodeStrictArgs(args, &input); err != nil {
		return nil, err
	}
	return a.svc.createExperiment(input.SuiteID, input.Name, "manual", input.Targets, input.Repetitions, input.BaselineTarget, input.JudgeModel)
}
func (a *App) toolRetryRun(_ *sdk.AppCtx, args map[string]any) (any, error) {
	run, err := a.svc.db.getRun(str(args, "id"))
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, errors.New("run not found")
	}
	return a.svc.db.retryRun(run)
}
func (a *App) toolCompare(_ *sdk.AppCtx, args map[string]any) (any, error) {
	exp, err := a.svc.db.getExperiment(str(args, "experiment_id"))
	if err != nil {
		return nil, err
	}
	if exp == nil {
		return nil, errors.New("experiment not found")
	}
	return exp.Summary, nil
}
