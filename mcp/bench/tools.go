package main

import (
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

func decodeArgs(args map[string]any, value any) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}

func str(args map[string]any, key string) string { value, _ := args[key].(string); return value }

func intArg(args map[string]any, key string) int {
	switch value := args[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0
		}
		return int(parsed)
	}
	return 0
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "bench_pack_list", Description: "List benchmark packs, draft and sealed.", InputSchema: objectSchema,
			Handler: func(*sdk.AppCtx, map[string]any) (any, error) { return a.svc.db.listPacks() }},
		{Name: "bench_pack_get", Description: "Get one benchmark pack and its scenarios.", InputSchema: requiredSchema("id"),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.svc.db.getPack(str(args, "id")) }},
		{Name: "bench_pack_create", Description: "Create a draft benchmark pack.", InputSchema: requiredSchema("name"),
			Handler: a.toolSavePack(true)},
		{Name: "bench_pack_update", Description: "Update a draft benchmark pack. Sealed packs are immutable.", InputSchema: requiredSchema("id", "name"),
			Handler: a.toolSavePack(false)},
		{Name: "bench_pack_delete", Description: "Delete a draft pack, or a sealed pack that has no runs.", InputSchema: requiredSchema("id"),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
				err := a.svc.deletePack(str(args, "id"))
				return map[string]bool{"ok": err == nil}, err
			}},

		{Name: "bench_scenario_put", Description: "Add or replace one scenario in a draft pack.", InputSchema: requiredSchema("pack_id", "name", "prompt"),
			Handler: a.toolPutScenario},
		{Name: "bench_scenario_delete", Description: "Remove a scenario from a draft pack.", InputSchema: requiredSchema("pack_id", "scenario_id"),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
				return a.svc.deleteScenario(str(args, "pack_id"), str(args, "scenario_id"))
			}},

		{Name: "bench_pack_seal", Description: "Seal a draft into a new immutable, content-hashed version.", InputSchema: requiredSchema("id"),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
				return a.svc.seal(str(args, "id"), str(args, "version"))
			}},
		{Name: "bench_pack_fork", Description: "Fork any pack into a fresh editable draft.", InputSchema: requiredSchema("id"),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
				return a.svc.fork(str(args, "id"), str(args, "name"))
			}},

		{Name: "bench_run_create", Description: "Run a sealed pack against agent, provider, and model targets for N trials.", InputSchema: requiredSchema("pack_id"),
			Handler: a.toolCreateRun},
		{Name: "bench_run_list", Description: "List benchmark runs.", InputSchema: objectSchema,
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.svc.db.listRuns(intArg(args, "limit")) }},
		{Name: "bench_run_get", Description: "Get one benchmark run with its scored results.", InputSchema: requiredSchema("id"),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.svc.db.getRun(str(args, "id")) }},
		{Name: "bench_run_cancel", Description: "Cancel a queued or running benchmark run.", InputSchema: requiredSchema("id"),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.svc.cancelRun(str(args, "id")) }},

		{Name: "bench_leaderboard", Description: "Rank targets across every run of one sealed pack.", InputSchema: requiredSchema("pack_digest"),
			Handler: a.toolLeaderboard},
		{Name: "bench_leaderboard_global", Description: "Rank targets across every sealed pack under one scoring contract.", InputSchema: objectSchema,
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
				return a.svc.globalLeaderboard(str(args, "scoring_version"))
			}},
		{Name: "bench_baseline_set", Description: "Pin a run's result as the baseline for a pack scenario.", InputSchema: requiredSchema("run_id", "scenario_id"),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
				return a.svc.setBaseline(str(args, "run_id"), str(args, "scenario_id"), intArg(args, "target_index"), str(args, "label"))
			}},
		{Name: "bench_baseline_compare", Description: "Compare a run against pinned baselines.", InputSchema: requiredSchema("run_id"),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
				return a.svc.compareToBaselines(str(args, "run_id"))
			}},
		{Name: "bench_evidence_export", Description: "Export a run's full provenance and scoring evidence bundle.", InputSchema: requiredSchema("run_id"),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.svc.evidence(str(args, "run_id")) }},
		{Name: "bench_catalog", Description: "List agents, models, and Environments available as targets.", InputSchema: objectSchema,
			Handler: func(*sdk.AppCtx, map[string]any) (any, error) { return a.svc.catalog() }},
	}
}

func (a *App) toolSavePack(creating bool) sdk.ToolHandler {
	return func(_ *sdk.AppCtx, args map[string]any) (any, error) {
		var pack Pack
		if err := decodeArgs(args, &pack); err != nil {
			return nil, err
		}
		return a.svc.savePack(&pack, creating)
	}
}

func (a *App) toolPutScenario(_ *sdk.AppCtx, args map[string]any) (any, error) {
	var input struct {
		PackID string `json:"pack_id"`
		Scenario
	}
	if err := decodeArgs(args, &input); err != nil {
		return nil, err
	}
	if input.PackID == "" {
		return nil, errors.New("pack_id is required")
	}
	return a.svc.putScenario(input.PackID, input.Scenario)
}

type runInput struct {
	PackID  string   `json:"pack_id"`
	Name    string   `json:"name"`
	Targets []Target `json:"targets"`
	Trials  int      `json:"trials"`
}

func (a *App) toolCreateRun(_ *sdk.AppCtx, args map[string]any) (any, error) {
	var input runInput
	if err := decodeArgs(args, &input); err != nil {
		return nil, err
	}
	return a.svc.createRun(input.PackID, input.Name, input.Targets, input.Trials)
}

func (a *App) toolLeaderboard(_ *sdk.AppCtx, args map[string]any) (any, error) {
	digest := str(args, "pack_digest")
	// Accept a pack id too: callers holding a sealed pack should not have to
	// look its digest up first.
	if digest == "" {
		if id := str(args, "pack_id"); id != "" {
			pack, err := a.svc.db.getPack(id)
			if err != nil {
				return nil, err
			}
			if pack == nil {
				return nil, errors.New("pack not found")
			}
			digest = pack.Digest
		}
	}
	if digest == "" {
		return nil, errors.New("pack_digest or pack_id is required")
	}
	return a.svc.leaderboard(digest)
}
