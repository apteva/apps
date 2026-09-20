package main

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

var objectSchema = map[string]any{"type": "object", "additionalProperties": true}

func readSchema(properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
}

func requiredReadSchema(field, description string) map[string]any {
	schema := readSchema(map[string]any{field: stringField(description)})
	schema["required"] = []string{field}
	return schema
}

func stringField(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func integerField(description string, minimum int) map[string]any {
	return map[string]any{"type": "integer", "minimum": minimum, "description": description}
}

func boolField(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

var benchDraftTargetSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"name":      map[string]any{"type": "string", "minLength": 1, "description": "Candidate setup name shown in results and leaderboards"},
		"directive": map[string]any{"type": "string", "minLength": 1, "description": "Complete directive for the hidden runtime agent"},
		"mode":      map[string]any{"type": "string", "enum": []string{"autonomous", "cautious", "learn"}},
		"config":    map[string]any{"type": "string", "description": "JSON object encoded as a string; defaults to {}"},
	},
	"required":             []string{"name", "directive"},
	"additionalProperties": false,
}

var benchTargetSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"agent_id": map[string]any{"type": "integer", "minimum": 1, "description": "Persistent project agent to benchmark"},
		"draft":    benchDraftTargetSchema,
		"provider": map[string]any{"type": "string", "description": "Optional provider override"},
		"model":    map[string]any{"type": "string", "description": "Optional model override"},
	},
	"oneOf": []any{
		map[string]any{"required": []string{"agent_id"}, "not": map[string]any{"required": []string{"draft"}}},
		map[string]any{"required": []string{"draft"}, "not": map[string]any{"required": []string{"agent_id"}}},
	},
	"additionalProperties": false,
}

var benchRunCreateSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"pack_id": map[string]any{"type": "string", "description": "Sealed benchmark pack id"},
		"name":    map[string]any{"type": "string", "description": "Optional run name"},
		"targets": map[string]any{"type": "array", "minItems": 1, "items": benchTargetSchema, "description": "One or more persistent agents or hidden draft setups to compare"},
		"trials":  map[string]any{"type": "integer", "minimum": 1, "description": "Trials per scenario and target"},
	},
	"required":             []string{"pack_id", "targets"},
	"additionalProperties": false,
}

func packWriteSchema(fields ...string) map[string]any {
	schema := requiredSchema(fields...)
	properties := schema["properties"].(map[string]any)
	properties["description"] = stringField("Benchmark pack description")
	properties["category"] = stringField("Stable category slug such as coding; human-readable input is normalized")
	properties["profile_digest"] = stringField("Optional sealed scoring profile digest")
	return schema
}

func scenarioPutSchema() map[string]any {
	schema := requiredSchema("pack_id", "name", "prompt")
	properties := schema["properties"].(map[string]any)
	properties["tags"] = map[string]any{
		"type": "array", "items": stringField("Scenario tag such as bug-fix or typescript"),
		"description": "Searchable scenario tags; values are normalized, deduplicated, and sealed into the pack digest",
	}
	return schema
}

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
		{Name: "bench_pack_list", Description: "List benchmark packs and their scenarios, optionally filtering by category, state, scoring profile, or text.",
			InputSchema: readSchema(map[string]any{
				"state": stringField("draft or sealed"), "profile_digest": stringField("Scoring profile digest"),
				"category": stringField("Category slug such as coding"),
				"query":    stringField("Case-insensitive text matched against pack, category, scenario names, and tags"),
			}), Handler: a.toolListPacks},
		{Name: "bench_category_list", Description: "List benchmark categories with pack, scenario, and tag counts.",
			InputSchema: readSchema(map[string]any{}), Handler: a.toolListCategories},
		{Name: "bench_pack_get", Description: "Get one benchmark pack and its scenarios by id or content digest.",
			InputSchema: readSchema(map[string]any{"id": stringField("Pack id"), "digest": stringField("Sealed pack content digest")}),
			Handler:     a.toolGetPack},
		{Name: "bench_pack_create", Description: "Create a categorized draft benchmark pack.", InputSchema: packWriteSchema("name"),
			Handler: a.toolSavePack(true)},
		{Name: "bench_pack_update", Description: "Update a draft benchmark pack and its category. Sealed packs are immutable.", InputSchema: packWriteSchema("id", "name"),
			Handler: a.toolSavePack(false)},
		{Name: "bench_pack_delete", Description: "Delete a draft pack, or a sealed pack that has no runs.", InputSchema: requiredSchema("id"),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
				err := a.svc.deletePack(str(args, "id"))
				return map[string]bool{"ok": err == nil}, err
			}},

		{Name: "bench_scenario_put", Description: "Add or replace one tagged scenario in a draft pack.", InputSchema: scenarioPutSchema(),
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

		{Name: "bench_run_create", Description: "Run a sealed pack against one or more persistent agents or hidden draft setups for N trials.", InputSchema: benchRunCreateSchema,
			Handler: a.toolCreateRun},
		{Name: "bench_run_list", Description: "List the newest benchmark runs. Use bench_run_search for filtering and pagination.",
			InputSchema: readSchema(map[string]any{"limit": integerField("Maximum runs to return; defaults to 50", 1)}),
			Handler:     func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.svc.db.listRuns(intArg(args, "limit")) }},
		{Name: "bench_run_get", Description: "Get one benchmark run with its scored results.", InputSchema: requiredReadSchema("id", "Bench run id"),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.svc.db.getRun(str(args, "id")) }},
		{Name: "bench_run_search", Description: "Search and paginate benchmark runs with optional pack, status, profile, and text filters.",
			InputSchema: readSchema(map[string]any{
				"pack_id": stringField("Pack id"), "pack_digest": stringField("Sealed pack digest"),
				"category":       stringField("Pack category slug"),
				"profile_digest": stringField("Scoring profile digest"), "status": stringField("Run status"),
				"query": stringField("Text matched against run name, pack name, and error"),
				"limit": integerField("Page size, 1-500; defaults to 100", 1), "offset": integerField("Zero-based result offset", 0),
				"include_results": boolField("Include every scored result under each returned run"),
			}), Handler: a.toolSearchRuns},
		{Name: "bench_run_cancel", Description: "Cancel a queued or running benchmark run.", InputSchema: requiredSchema("id"),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.svc.cancelRun(str(args, "id")) }},
		{Name: "bench_result_list", Description: "Search and paginate raw scored results across runs and packs, including invalid harness results.",
			InputSchema: readSchema(map[string]any{
				"run_id": stringField("Bench run id"), "pack_id": stringField("Pack id"), "pack_digest": stringField("Sealed pack digest"),
				"category": stringField("Pack category slug"), "scenario_id": stringField("Scenario id"),
				"tag": stringField("Scenario tag"), "admission": stringField("verified, diagnostic, or invalid"),
				"provider": stringField("Target provider"), "model": stringField("Target model"),
				"passed": boolField("Filter by task pass/fail"), "limit": integerField("Page size, 1-500; defaults to 100", 1),
				"offset": integerField("Zero-based result offset", 0),
			}), Handler: a.toolListResults},
		{Name: "bench_result_get", Description: "Get one raw scored result by id.", InputSchema: requiredReadSchema("id", "Result id"),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.svc.db.getResult(str(args, "id")) }},

		{Name: "bench_leaderboard", Description: "Rank targets across every run of one sealed pack, selected by id or digest.",
			InputSchema: readSchema(map[string]any{"pack_id": stringField("Sealed pack id"), "pack_digest": stringField("Sealed pack digest")}),
			Handler:     a.toolLeaderboard},
		{Name: "bench_leaderboard_global", Description: "Rank targets across every sealed pack, optionally scoped to one category, under one scoring profile.",
			InputSchema: readSchema(map[string]any{
				"profile_digest": stringField("Scoring profile digest; defaults to Verified v1"),
				"category":       stringField("Optional category slug; when set, aggregate only packs in that category"),
			}),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
				return a.svc.globalLeaderboard(str(args, "profile_digest"), str(args, "category"))
			}},
		{Name: "bench_baseline_set", Description: "Pin a run's result as the baseline for a pack scenario.", InputSchema: requiredSchema("run_id", "scenario_id"),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
				return a.svc.setBaseline(str(args, "run_id"), str(args, "scenario_id"), intArg(args, "target_index"), str(args, "label"))
			}},
		{Name: "bench_baseline_compare", Description: "Compare a run against pinned baselines.", InputSchema: requiredReadSchema("run_id", "Bench run id"),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
				return a.svc.compareToBaselines(str(args, "run_id"))
			}},
		{Name: "bench_baseline_list", Description: "List and paginate pinned baselines, optionally scoped to a pack or scenario.",
			InputSchema: readSchema(map[string]any{
				"pack_id": stringField("Sealed pack id"), "pack_digest": stringField("Sealed pack digest"),
				"scenario_id": stringField("Scenario id"), "limit": integerField("Page size, 1-500; defaults to 100", 1),
				"offset": integerField("Zero-based result offset", 0),
			}), Handler: a.toolListBaselines},
		{Name: "bench_baseline_get", Description: "Get one pinned baseline by id.", InputSchema: requiredReadSchema("id", "Baseline id"),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.svc.db.getBaseline(str(args, "id")) }},
		{Name: "bench_evidence_export", Description: "Export a run's full provenance and scoring evidence bundle.", InputSchema: requiredReadSchema("run_id", "Bench run id"),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) { return a.svc.evidence(str(args, "run_id")) }},
		{Name: "bench_profile_list", Description: "List every built-in and custom scoring profile.",
			InputSchema: readSchema(map[string]any{
				"state": stringField("draft or sealed"), "query": stringField("Text matched against profile name and description"),
				"builtin": boolField("Filter built-in versus custom profiles"),
			}), Handler: a.toolListProfiles},
		{Name: "bench_profile_get", Description: "Get one scoring profile by id or digest.",
			InputSchema: readSchema(map[string]any{"id": stringField("Profile id"), "digest": stringField("Sealed profile digest")}),
			Handler:     a.toolGetProfile},
		{Name: "bench_scoring_get", Description: "Read a scoring profile together with all supported metrics, curves, admissions, and run states.",
			InputSchema: readSchema(map[string]any{"profile_id": stringField("Profile id"), "profile_digest": stringField("Profile digest; defaults to Verified v1")}),
			Handler: func(_ *sdk.AppCtx, args map[string]any) (any, error) {
				return a.svc.scoringData(str(args, "profile_id"), str(args, "profile_digest"))
			}},
		{Name: "bench_pack_suite_list", Description: "List materialized Evals suites and their case-to-scenario mappings.",
			InputSchema: readSchema(map[string]any{"pack_id": stringField("Sealed pack id"), "pack_digest": stringField("Sealed pack digest")}),
			Handler:     a.toolListPackSuites},
		{Name: "bench_data_export", Description: "Export every persisted Bench record in one snapshot: packs, profiles, runs, results, baselines, and Evals suite mappings.",
			InputSchema: readSchema(map[string]any{}), Handler: func(*sdk.AppCtx, map[string]any) (any, error) { return a.svc.dataExport() }},
		{Name: "bench_catalog", Description: "List agents, models, and Environments available as targets.", InputSchema: readSchema(map[string]any{}),
			Handler: func(*sdk.AppCtx, map[string]any) (any, error) { return a.svc.catalog() }},
	}
}

func (a *App) toolListPacks(_ *sdk.AppCtx, args map[string]any) (any, error) {
	packs, err := a.svc.db.listPacks()
	if err != nil {
		return nil, err
	}
	state, digest := str(args, "state"), str(args, "profile_digest")
	category := normalizeTaxonomyValue(str(args, "category"))
	query := strings.ToLower(strings.TrimSpace(str(args, "query")))
	out := make([]Pack, 0, len(packs))
	for _, pack := range packs {
		if state != "" && pack.State != state {
			continue
		}
		if digest != "" && pack.ProfileDigest != digest {
			continue
		}
		if category != "" && pack.Category != category {
			continue
		}
		searchable := pack.Name + "\n" + pack.Description + "\n" + pack.Category
		for _, scenario := range pack.Scenarios {
			searchable += "\n" + scenario.Name + "\n" + strings.Join(scenario.Tags, " ")
		}
		if query != "" && !strings.Contains(strings.ToLower(searchable), query) {
			continue
		}
		out = append(out, pack)
	}
	return out, nil
}

type categorySummary struct {
	Category  string   `json:"category"`
	Drafts    int      `json:"drafts"`
	Sealed    int      `json:"sealed"`
	Scenarios int      `json:"scenarios"`
	Tags      []string `json:"tags"`
}

func (a *App) toolListCategories(_ *sdk.AppCtx, _ map[string]any) (any, error) {
	packs, err := a.svc.db.listPacks()
	if err != nil {
		return nil, err
	}
	byCategory := map[string]*categorySummary{}
	tagSets := map[string]map[string]struct{}{}
	for _, pack := range packs {
		if pack.Category == "" {
			continue
		}
		item := byCategory[pack.Category]
		if item == nil {
			item = &categorySummary{Category: pack.Category, Tags: []string{}}
			byCategory[pack.Category] = item
			tagSets[pack.Category] = map[string]struct{}{}
		}
		if pack.State == PackStateSealed {
			item.Sealed++
		} else {
			item.Drafts++
		}
		item.Scenarios += len(pack.Scenarios)
		for _, scenario := range pack.Scenarios {
			for _, tag := range scenario.Tags {
				tagSets[pack.Category][tag] = struct{}{}
			}
		}
	}
	out := make([]categorySummary, 0, len(byCategory))
	for category, item := range byCategory {
		for tag := range tagSets[category] {
			item.Tags = append(item.Tags, tag)
		}
		sort.Strings(item.Tags)
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Category < out[j].Category })
	return out, nil
}

func (a *App) toolGetPack(_ *sdk.AppCtx, args map[string]any) (any, error) {
	if id := str(args, "id"); id != "" {
		return a.svc.db.getPack(id)
	}
	if digest := str(args, "digest"); digest != "" {
		return a.svc.db.getPackByDigest(digest)
	}
	return nil, errors.New("id or digest is required")
}

func (a *App) toolSearchRuns(_ *sdk.AppCtx, args map[string]any) (any, error) {
	var input runQuery
	if err := decodeArgs(args, &input); err != nil {
		return nil, err
	}
	return a.svc.db.searchRuns(input)
}

func (a *App) toolListResults(_ *sdk.AppCtx, args map[string]any) (any, error) {
	var input resultQuery
	if err := decodeArgs(args, &input); err != nil {
		return nil, err
	}
	return a.svc.db.searchResults(input)
}

func (a *App) resolvePackDigest(args map[string]any) (string, error) {
	if digest := str(args, "pack_digest"); digest != "" {
		return digest, nil
	}
	if id := str(args, "pack_id"); id != "" {
		pack, err := a.svc.db.getPack(id)
		if err != nil {
			return "", err
		}
		if pack == nil {
			return "", errors.New("pack not found")
		}
		if pack.Digest == "" {
			return "", errors.New("pack is not sealed and has no digest")
		}
		return pack.Digest, nil
	}
	return "", nil
}

func (a *App) toolListBaselines(_ *sdk.AppCtx, args map[string]any) (any, error) {
	var input baselineQuery
	if err := decodeArgs(args, &input); err != nil {
		return nil, err
	}
	if input.PackDigest == "" {
		digest, err := a.resolvePackDigest(args)
		if err != nil {
			return nil, err
		}
		input.PackDigest = digest
	}
	return a.svc.db.searchBaselines(input)
}

func (a *App) toolListProfiles(_ *sdk.AppCtx, args map[string]any) (any, error) {
	profiles, err := a.svc.db.listProfiles()
	if err != nil {
		return nil, err
	}
	var builtin *bool
	if _, ok := args["builtin"]; ok {
		value, valid := args["builtin"].(bool)
		if !valid {
			return nil, errors.New("builtin must be a boolean")
		}
		builtin = &value
	}
	state, query := str(args, "state"), strings.ToLower(strings.TrimSpace(str(args, "query")))
	out := make([]Profile, 0, len(profiles))
	for _, profile := range profiles {
		if state != "" && profile.State != state {
			continue
		}
		if builtin != nil && profile.Builtin != *builtin {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(profile.Name+"\n"+profile.Description), query) {
			continue
		}
		out = append(out, profile)
	}
	return out, nil
}

func (a *App) toolGetProfile(_ *sdk.AppCtx, args map[string]any) (any, error) {
	if id := str(args, "id"); id != "" {
		return a.svc.db.getProfile(id)
	}
	if digest := str(args, "digest"); digest != "" {
		return a.svc.db.getProfileByDigest(digest)
	}
	return nil, errors.New("id or digest is required")
}

func (a *App) toolListPackSuites(_ *sdk.AppCtx, args map[string]any) (any, error) {
	digest, err := a.resolvePackDigest(args)
	if err != nil {
		return nil, err
	}
	return a.svc.db.listPackSuiteRecords(digest)
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
