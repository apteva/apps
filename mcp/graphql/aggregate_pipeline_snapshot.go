package main

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
)

// Version 3 uses native Tables dependencies. Each stage has its own SQLite
// compilation budget; no SQL from another stage is included in its statement.
func validPipelineStagePath(path string) bool {
	if !strings.HasPrefix(path, "$stage.") || len(path) > 512 {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "$stage."), ".")
	if len(parts) != 4 || !graphqlName(parts[0]) || parts[1] != "rows" || !graphqlName(parts[3]) {
		return false
	}
	n, err := strconv.Atoi(parts[2])
	return err == nil && n >= 0 && strconv.Itoa(n) == parts[2]
}

func compileSnapshotPipeline(config map[string]any, sources []sourceRecord, currentID int64, stages []aggregatePipelineStage, order []int, final string) (*aggregatePipelinePlan, error) {
	var plan *aggregatePipelinePlan
	ordered := make([]aggregatePipelineStage, 0, len(stages))
	for _, i := range order {
		s := stages[i]
		// Validate each statement's declared table access using the same source
		// checks as v1, including rejection of implicit identity/row scopes.
		// A value-only dependent stage need not query a table of its own.
		c := mergeMaps(config, map[string]any{"version": 1, "engine": "tables_query", "sql": s.SQL, "params": nil})
		p, err := validatePipelineConfig(aggregatePipelineOperation, c, sources, currentID, false)
		if err != nil {
			return nil, err
		}
		cols := make([]string, len(s.Columns))
		for j, col := range s.Columns {
			cols[j] = pipelineQuote(col)
		}
		// Fetch one extra row to detect overflow; never silently turn a partial
		// intermediate read into a successful final aggregate.
		s.SQL = "WITH gql_stage_value(" + strings.Join(cols, ",") + ") AS (\n" + s.SQL + "\n) SELECT * FROM gql_stage_value LIMIT " + strconv.Itoa(s.MaxRows+1)
		if len(s.SQL) > 64<<10 {
			return nil, invalid("pipeline stage SQL exceeds 65536 bytes including bounds")
		}
		ordered = append(ordered, s)
		if s.ID == final {
			plan = p
			plan.FinalColumns = s.Columns
		}
	}
	plan.Stages, plan.SeparateStages, plan.FinalStage = ordered, true, final
	return plan, nil
}

func buildSnapshotPipelineInput(ctx context.Context, plan *aggregatePipelinePlan, args map[string]any, parent any, project string) (map[string]any, error) {
	operations := make([]map[string]any, 0, len(plan.Stages))
	for _, stage := range plan.Stages {
		bindings, err := parsePipelineParams(stage.Params, true)
		if err != nil {
			return nil, err
		}
		params := make([]any, 0, len(bindings))
		for _, binding := range bindings {
			if strings.HasPrefix(binding.From, "$stage.") {
				params = append(params, map[string]any{"$ref": strings.TrimPrefix(binding.From, "$stage.")})
				continue
			}
			bound, err := buildAggregatePipelineInput(ctx, &aggregatePipelinePlan{Params: []aggregatePipelineParam{binding}}, args, parent, project)
			if err != nil {
				return nil, err
			}
			value := bound["params"].([]any)[0]
			// Tables interprets $ref maps recursively. All non-stage parameters
			// must be bound scalars, never caller-provided dependency objects.
			params = append(params, value)
		}
		operations = append(operations, map[string]any{"id": stage.ID, "operation": "tables_query", "args": map[string]any{"sql": stage.SQL, "params": params, "_project_id": project}})
	}
	return map[string]any{"mode": "read_snapshot", "operations": operations, "_project_id": project}, nil
}

func (a *App) readPipelineStages(ctx context.Context, project string, plan *aggregatePipelinePlan, input map[string]any) (any, error) {
	reads := []upstreamRead{{ID: "final", Operation: "tables_query", Arguments: input}}
	if plan.SeparateStages {
		reads = nil
		for _, op := range input["operations"].([]map[string]any) {
			reads = append(reads, upstreamRead{ID: op["id"].(string), Operation: "tables_query", Arguments: op["args"].(map[string]any)})
		}
	}
	out, err := (&tablesUpstream{app: a, project: project, consistency: "batch"}).Read(ctx, nil, reads)
	if err != nil {
		return nil, err
	}
	if !plan.SeparateStages {
		return decodeStagedPipeline(plan, out["final"].Value)
	}
	return decodeSnapshotPipeline(plan, out)
}

func decodeSnapshotPipeline(plan *aggregatePipelinePlan, out map[string]upstreamResult) (any, error) {
	metadata := []map[string]any{}
	seen := map[string]bool{}
	for _, stage := range plan.Stages {
		entry, exists := out[stage.ID]
		if !exists || entry.Error != nil {
			return nil, internal("pipeline stage failed or was omitted")
		}
		envelope, ok := entry.Value.(map[string]any)
		if !ok {
			return nil, internal("invalid pipeline stage envelope")
		}
		if envelope["truncated"] == true {
			return nil, runtimeError("source_result_truncated", "pipeline stage was truncated")
		}
		rows, ok := envelope["rows"].([]any)
		if !ok {
			return nil, internal("invalid pipeline stage rows")
		}
		if len(rows) > stage.MaxRows {
			return nil, runtimeError("row_limit_exceeded", "pipeline stage row budget exceeded")
		}
		bytes := 0
		for _, row := range rows {
			encoded, err := json.Marshal(row)
			if err != nil {
				return nil, internal("invalid pipeline stage row")
			}
			bytes += len(encoded)
			if bytes > stage.MaxBytes {
				return nil, runtimeError("response_limit_exceeded", "pipeline stage byte budget exceeded")
			}
		}
		bindings, _ := parsePipelineParams(stage.Params, true)
		for _, binding := range bindings {
			if !strings.HasPrefix(binding.From, "$stage.") {
				continue
			}
			parts := strings.Split(strings.TrimPrefix(binding.From, "$stage."), ".")
			dependency, ok := out[parts[0]].Value.(map[string]any)
			if !ok {
				return nil, internal("invalid pipeline dependency")
			}
			depRows, ok := dependency["rows"].([]any)
			index, _ := strconv.Atoi(parts[2])
			if !ok || index >= len(depRows) {
				return nil, runtimeError("stage_binding_invalid", "pipeline dependency row is missing")
			}
			row, ok := depRows[index].(map[string]any)
			if !ok {
				return nil, internal("invalid pipeline dependency row")
			}
			value, found := row[parts[3]]
			if !found || value == nil {
				if binding.Required || !found {
					return nil, runtimeError("stage_binding_invalid", "required pipeline dependency is missing")
				}
				continue
			}
			// JSON intermediates are serialized SQLite values. Validate their
			// content instead of encoding the text as a JSON string a second time.
			if binding.Type == "json" {
				text, ok := value.(string)
				if !ok || !json.Valid([]byte(text)) {
					return nil, runtimeError("stage_binding_invalid", "pipeline dependency must contain JSON text")
				}
			} else if binding.Type == "boolean" {
				// SQLite represents Boolean SQL values as integer 0/1. JSON
				// transport decodes these as float64; Tables binds either form.
				if value != true && value != false && value != int64(0) && value != int64(1) && value != float64(0) && value != float64(1) {
					return nil, runtimeError("stage_binding_invalid", "pipeline dependency must be a Boolean or SQL 0/1")
				}
			} else if _, err := coerceAggregatePipelineValue(value, binding.Type); err != nil {
				return nil, runtimeError("stage_binding_invalid", "pipeline dependency has an invalid type")
			}
		}
		for _, item := range projectionMetadata(envelope["projections"]) {
			key := runtimeDigest(item)
			if !seen[key] {
				seen[key] = true
				metadata = append(metadata, item)
			}
		}
	}
	final := out[plan.FinalStage].Value.(map[string]any)
	return map[string]any{"rows": final["rows"], "columns": plan.FinalColumns, "truncated": false, "projections": projectionMetadata(metadata)}, nil
}
