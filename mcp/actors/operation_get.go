package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// This is a bound on the complete MCP text payload, not a truncation policy.
// Oversized operation/configuration responses fail rather than dropping steps.
const maxActorOperationResponseBytes = 32 * 1024

var actorInputReferencePattern = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

func (a *App) getActorOperation(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name, ok := args["operation"].(string)
	if !ok || !operationNamePattern.MatchString(name) {
		return nil, errors.New("operation must be a nonempty operation name")
	}
	revision := intArg(args, "revision")
	if raw, supplied := args["revision"]; supplied {
		if revision <= 0 || fmt.Sprint(raw) != fmt.Sprint(revision) {
			return nil, errors.New("revision must be a positive integer")
		}
	}
	rec, err := getActorOperationIdentity(ctx, int64ArgLocal(args, "id"), stringArg(args, "name"))
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, errors.New("actor not found")
	}
	latest := rec.Revision
	if revision == 0 {
		revision = latest
	}
	// Even latest-with-operation comes from the immutable version row.
	var raw string
	err = ctx.AppDB().QueryRow(`SELECT definition_json FROM actors_versions WHERE project_id=? AND actor_id=? AND revision=?`, projectID(ctx), rec.ID, revision).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("actor %d revision %d unavailable", rec.ID, revision)
	}
	if err != nil {
		return nil, err
	}
	var root map[string]json.RawMessage
	if err = json.Unmarshal([]byte(raw), &root); err != nil {
		return nil, fmt.Errorf("decode immutable actor revision: %w", err)
	}
	var ops map[string]json.RawMessage
	if err = json.Unmarshal(root["operations"], &ops); err != nil && len(root["operations"]) > 0 {
		return nil, err
	}
	operation := ops[name]
	if len(ops) == 0 && name == "run" {
		implicit := map[string]json.RawMessage{}
		for _, key := range []string{"steps", "crawl", "output_schema", "read_only", "recovery", "limits"} {
			if value, exists := root[key]; exists {
				implicit[key] = value
			}
		}
		operation, err = json.Marshal(implicit)
		if err != nil {
			return nil, err
		}
	}
	if len(operation) == 0 || strings.TrimSpace(string(operation)) == "null" {
		return nil, fmt.Errorf("operation %q not found at actor %d revision %d", name, rec.ID, revision)
	}
	// Preserve the stored operation JSON, including unknown metadata and explicit
	// zero/false values; do not round-trip it through the typed step structs.
	var storedOp map[string]json.RawMessage
	if err = json.Unmarshal(operation, &storedOp); err != nil {
		return nil, err
	}
	// Decode shared configuration and only the requested operation for execution
	// introspection. Unrelated/latest operation schemas cannot break this read.
	shared := make(map[string]json.RawMessage, len(root))
	for key, value := range root {
		if key != "operations" {
			shared[key] = value
		}
	}
	configRaw, _ := json.Marshal(shared)
	var def actorDefinition
	if err = json.Unmarshal(configRaw, &def); err != nil {
		return nil, err
	}
	if len(ops) > 0 {
		var typed actorOperation
		if err = json.Unmarshal(operation, &typed); err != nil {
			return nil, err
		}
		def.Operations = map[string]actorOperation{name: typed}
	}
	selected, err := selectOperation(def, name)
	if err != nil {
		return nil, err
	}
	var steps []json.RawMessage
	if err = json.Unmarshal(storedOp["steps"], &steps); err != nil && len(storedOp["steps"]) > 0 {
		return nil, err
	}
	config := map[string]any{"read_only": selected.ReadOnly, "recovery_only": selected.RecoveryOnly, "limits": selected.Limits}
	for _, key := range []string{"schema_version", "defaults", "presets", "browser", "allowed_hosts"} {
		if value, exists := root[key]; exists {
			config[key] = value
		}
	}
	config["browser"] = selected.Browser
	config["required_explicit_inputs"] = selected.RequiredExplicitInputs
	if selected.Recovery != nil {
		config["recovery"] = selected.Recovery
	}
	if selected.Crawl != nil {
		config["crawl"] = root["crawl"]
	}
	// SDK serializes JSON compactly. Hash that same complete stored operation.
	canonical, err := json.Marshal(operation)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(canonical)
	out := map[string]any{
		"actor": map[string]any{"id": rec.ID, "name": rec.Name}, "found": true,
		"retrieved_revision": revision, "latest_revision": latest, "operation_name": name,
		"operation": operation, "execution": config, "effective_limits": actorOperationEffectiveLimits(selected),
		"input_requirements": actorOperationInputs(selected),
		"complete":           true, "step_count": len(steps), "operation_sha256": hex.EncodeToString(digest[:]),
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxActorOperationResponseBytes {
		return nil, fmt.Errorf("incomplete-response: complete operation %q at revision %d requires %d bytes, exceeding the %d-byte compact response limit; no definition was returned", name, revision, len(encoded), maxActorOperationResponseBytes)
	}
	return out, nil
}

// Execution renders global defaults and presets too, so retaining all shared
// configuration is necessary. Only unrelated operation definitions are removed.
func actorOperationInputs(def actorDefinition) map[string]any {
	raw, _ := json.Marshal(def)
	var value any
	_ = json.Unmarshal(raw, &value)
	refs := map[string]bool{}
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case string:
			for _, m := range actorInputReferencePattern.FindAllStringSubmatch(x, -1) {
				refs[strings.TrimSpace(m[1])] = true
			}
		case map[string]any:
			for _, item := range x {
				visit(item)
			}
		case []any:
			for _, item := range x {
				visit(item)
			}
		}
	}
	visit(value)
	requiredKeys := map[string]bool{}
	for key := range refs {
		requiredKeys[key] = true
	}
	assertions := []int{}
	for i, step := range def.Steps {
		if step.Action == "assert_inputs" {
			assertions = append(assertions, i)
			for key := range step.Assertions {
				requiredKeys[key] = true
			}
		}
	}
	for _, key := range def.RequiredExplicitInputs {
		requiredKeys[key] = true
	}
	explicit := map[string]bool{}
	for _, key := range def.RequiredExplicitInputs {
		explicit[key] = true
	}
	missing := func(vars map[string]any) []string {
		out := []string{}
		for key := range requiredKeys {
			if _, ok := actorVariable(vars, key); !ok || explicit[key] {
				out = append(out, key)
			}
		}
		sort.Strings(out)
		return out
	}
	references := []string{}
	for key := range refs {
		references = append(references, key)
	}
	sort.Strings(references)
	byPreset := map[string][]string{}
	for name, values := range def.Presets {
		byPreset[name] = missing(mergeActorMaps(def.Defaults, values))
	}
	return map[string]any{"required_explicit_inputs": def.RequiredExplicitInputs, "template_variables": references, "required_inputs": missing(def.Defaults), "required_inputs_by_preset": byPreset, "assertion_steps": assertions, "precedence": []string{"defaults", "preset", "schedule_overrides", "input"}}
}

func actorOperationLimits(limits actorLimits) map[string]any {
	limit := func(value any, fallback, min, max int, retries bool) any {
		if actorTemplateValue(value) {
			return map[string]any{"template": value, "default": fallback, "minimum": min, "maximum": max}
		}
		if retries && value != nil {
			return clampInt(templateInt(value), min, max)
		}
		return boundedInt(templateInt(value), fallback, min, max)
	}
	return map[string]any{
		"max_pages":            limit(limits.MaxPages, defaultActorMaxPages, 1, maxActorPages, false),
		"max_items":            limit(limits.MaxItems, defaultActorMaxItems, 1, maxActorItems, false),
		"max_duration_seconds": limit(limits.MaxDurationSeconds, defaultActorMaxSeconds, 1, maxActorSeconds, false),
		"step_retries":         limit(limits.StepRetries, defaultActorRetries, 0, 10, true),
	}
}

// Crawl's frontier is the primary budget; configured actor limits can reduce it.
func actorOperationEffectiveLimits(def actorDefinition) map[string]any {
	out := actorOperationLimits(def.Limits)
	if def.Crawl == nil {
		return out
	}
	for key, frontier := range map[string]int{"max_pages": boundedInt(def.Crawl.Frontier.MaxPages, maxCrawlFrontierSize, 1, maxCrawlFrontierSize), "max_items": boundedInt(def.Crawl.Frontier.MaxItems, maxActorItems, 1, maxActorItems)} {
		configured := def.Limits.MaxPages
		if key == "max_items" {
			configured = def.Limits.MaxItems
		}
		if configured == nil {
			out[key] = frontier
			continue
		}
		if policy, ok := out[key].(map[string]any); ok {
			policy["maximum"] = minInt(policy["maximum"].(int), frontier)
		} else {
			out[key] = minInt(out[key].(int), frontier)
		}
	}
	return out
}

func getActorOperationIdentity(ctx *sdk.AppCtx, id int64, name string) (*actorRecord, error) {
	rec := &actorRecord{}
	query := `SELECT id,name,revision FROM actors_definitions WHERE project_id=? AND id=?`
	var lookup any = id
	if id <= 0 {
		query = `SELECT id,name,revision FROM actors_definitions WHERE project_id=? AND name=?`
		lookup = strings.TrimSpace(name)
	}
	err := ctx.AppDB().QueryRow(query, projectID(ctx), lookup).Scan(&rec.ID, &rec.Name, &rec.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return rec, nil
}
