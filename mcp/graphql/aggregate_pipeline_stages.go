package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Stage names, SQL, columns and edges are immutable resolver configuration.
// Materialized CTEs let existing Tables run the entire graph in one statement:
// no intermediate rows cross the app boundary and no backend extension is needed.
type aggregatePipelineStage struct {
	ID           string
	SQL          string
	Columns      []string
	Params       []any
	MaxRows      int
	MaxBytes     int
	Dependencies []string
}

func pipelineQuote(s string) string   { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func pipelineLiteral(s string) string { return `'` + strings.ReplaceAll(s, `'`, `''`) + `'` }

func pipelineStageLimit(raw any, fallback int) (int, error) {
	if raw == nil {
		return fallback, nil
	}
	switch raw.(type) {
	case int, int64, float64, json.Number:
	default:
		return 0, invalid("stage limits must be integers")
	}
	n, err := strconv.ParseInt(fmt.Sprint(raw), 10, 32)
	if err != nil {
		return 0, invalid("stage limits must be integers")
	}
	return int(n), nil
}
func pipelineRowJSON(columns []string) string {
	parts := []string{}
	for _, column := range columns {
		parts = append(parts, pipelineLiteral(column), pipelineQuote(column))
	}
	return "json_object(" + strings.Join(parts, ",") + ")"
}

// Keep validation tied to SQL syntax rather than characters inside literals,
// quoted names or comments. Tables performs the native SQL access verification.
func pipelineSQLMask(raw string) (string, error) {
	masked := []byte(raw)
	for i := 0; i < len(raw); {
		start := i
		if i+1 < len(raw) && raw[i:i+2] == "--" {
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
		} else if i+1 < len(raw) && raw[i:i+2] == "/*" {
			end := strings.Index(raw[i+2:], "*/")
			if end < 0 {
				return "", invalid("unterminated pipeline SQL comment")
			}
			i += end + 4
		} else if raw[i] == '\'' || raw[i] == '"' || raw[i] == '`' || raw[i] == '[' {
			quote := raw[i]
			endQuote := quote
			if quote == '[' {
				endQuote = ']'
			}
			i++
			closed := false
			for i < len(raw) {
				if raw[i] == endQuote {
					i++
					if quote != '[' && i < len(raw) && raw[i] == endQuote {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return "", invalid("unterminated pipeline SQL quote")
			}
		} else {
			i++
			continue
		}
		for j := start; j < i; j++ {
			masked[j] = ' '
		}
	}
	return string(masked), nil
}

// Replaces stage references only outside SQL strings, identifiers and comments.
// All value bindings use anonymous ?. Named/numbered bindings would make the
// flattened per-stage parameter order ambiguous, so version 2 rejects them.
func stagedSQL(raw string, names map[string]string) (string, []string, int, error) {
	var out strings.Builder
	deps := []string{}
	seen := map[string]bool{}
	params := 0
	for i := 0; i < len(raw); {
		start := i
		c := raw[i]
		if i+1 < len(raw) && raw[i:i+2] == "--" {
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
		} else if i+1 < len(raw) && raw[i:i+2] == "/*" {
			end := strings.Index(raw[i+2:], "*/")
			if end < 0 {
				return "", nil, 0, invalid("unterminated pipeline SQL comment")
			}
			i += end + 4
		} else if c == '\'' || c == '"' || c == '`' || c == '[' {
			endQuote := c
			if c == '[' {
				endQuote = ']'
			}
			i++
			closed := false
			for i < len(raw) {
				if raw[i] == endQuote {
					i++
					if c != '[' && i < len(raw) && raw[i] == endQuote {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return "", nil, 0, invalid("unterminated pipeline SQL quote")
			}
			if c != '\'' && strings.Contains(strings.ToLower(raw[start:i]), "gql_stage_") {
				return "", nil, 0, invalid("reserved pipeline SQL identifier")
			}
		} else if c == '{' {
			end := strings.IndexByte(raw[i:], '}')
			if end < 0 {
				return "", nil, 0, invalid("unclosed pipeline placeholder")
			}
			i += end + 1
			name := raw[start+1 : i-1]
			if strings.HasPrefix(name, "stage:") {
				id := strings.TrimPrefix(name, "stage:")
				replacement, ok := names[id]
				if !ok {
					return "", nil, 0, invalid("unknown pipeline stage %q", id)
				}
				out.WriteString(pipelineQuote(replacement))
				if !seen[id] {
					deps = append(deps, id)
					seen[id] = true
				}
				continue
			}
			if !graphqlName(name) {
				return "", nil, 0, invalid("invalid pipeline table placeholder")
			}
		} else if c == ';' || c == ':' || c == '@' || c == '$' {
			return "", nil, 0, invalid("pipeline stages require one SELECT/WITH and anonymous ? bindings")
		} else if c == '?' {
			params++
			i++
			if i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
				return "", nil, 0, invalid("numbered pipeline bindings are unsupported")
			}
		} else {
			i++
			if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
				for i < len(raw) && (raw[i] == '_' || raw[i] >= 'a' && raw[i] <= 'z' || raw[i] >= 'A' && raw[i] <= 'Z' || raw[i] >= '0' && raw[i] <= '9') {
					i++
				}
				if strings.HasPrefix(strings.ToLower(raw[start:i]), "gql_stage_") {
					return "", nil, 0, invalid("reserved pipeline SQL identifier")
				}
			}
		}
		out.WriteString(raw[start:i])
	}
	return out.String(), deps, params, nil
}

func validateStagedPipeline(config map[string]any, sources []sourceRecord, currentID int64) (*aggregatePipelinePlan, error) {
	if config["engine"] != "tables_batch" {
		return nil, invalid("aggregate_pipeline version 2 requires engine tables_batch")
	}
	if config["sql"] != nil || config["params"] != nil {
		return nil, invalid("version 2 SQL and params belong to stages")
	}
	raw, ok := config["stages"].([]any)
	if !ok || len(raw) == 0 || len(raw) > 16 {
		return nil, invalid("pipeline requires 1 to 16 fixed stages")
	}
	stages := make([]aggregatePipelineStage, len(raw))
	names := map[string]string{}
	indices := map[string]int{}
	for i, item := range raw {
		s, ok := item.(map[string]any)
		if !ok {
			return nil, invalid("pipeline stage must be an object")
		}
		id, _ := s["id"].(string)
		if !graphqlName(id) || names[id] != "" {
			return nil, invalid("pipeline stage id must be unique and valid")
		}
		columns, err := pipelineStringList(s["columns"])
		if err != nil || len(columns) == 0 || len(columns) > 64 {
			return nil, invalid("pipeline stage columns must contain 1 to 64 names")
		}
		seenColumns := map[string]bool{}
		for _, col := range columns {
			if !graphqlName(col) || seenColumns[strings.ToLower(col)] {
				return nil, invalid("pipeline stage columns must be valid and unique")
			}
			seenColumns[strings.ToLower(col)] = true
		}
		sql, ok := s["sql"].(string)
		if !ok {
			return nil, invalid("pipeline stage requires fixed SQL")
		}
		sql = strings.TrimSpace(sql)
		lower := strings.ToLower(sql)
		if !(strings.HasPrefix(lower, "select ") || strings.HasPrefix(lower, "select\n") || strings.HasPrefix(lower, "select\t") || strings.HasPrefix(lower, "with ") || strings.HasPrefix(lower, "with\n") || strings.HasPrefix(lower, "with\t")) {
			return nil, invalid("pipeline stage must be SELECT or WITH")
		}
		params, err := parseAggregatePipelineParams(s["params"])
		if err != nil {
			return nil, err
		}
		p := []any{}
		if len(params) > 0 {
			p = s["params"].([]any)
		}
		rows, err := pipelineStageLimit(s["max_rows"], 100)
		if err != nil {
			return nil, err
		}
		bytes, err := pipelineStageLimit(s["max_bytes"], 256<<10)
		if err != nil {
			return nil, err
		}
		if rows < 1 || rows > 10000 || bytes < 1 || bytes > 4<<20 {
			return nil, invalid("stage limits require max_rows 1..10000 and max_bytes 1..4194304")
		}
		stages[i] = aggregatePipelineStage{ID: id, SQL: sql, Columns: columns, Params: p, MaxRows: rows, MaxBytes: bytes}
		names[id] = fmt.Sprint("gql_stage_", i)
		indices[id] = i
	}
	for i := range stages {
		sql, deps, count, err := stagedSQL(stages[i].SQL, names)
		if err != nil {
			return nil, err
		}
		if count != len(stages[i].Params) {
			return nil, invalid("stage %s binding count does not match params", stages[i].ID)
		}
		stages[i].SQL, stages[i].Dependencies = sql, deps
	}
	final, _ := config["final_stage"].(string)
	if names[final] == "" {
		return nil, invalid("final_stage must identify a fixed stage")
	}
	visited := map[string]int{}
	order := []int{}
	var visit func(string) error
	visit = func(id string) error {
		if visited[id] == 1 {
			return invalid("pipeline dependency cycle")
		}
		if visited[id] == 2 {
			return nil
		}
		visited[id] = 1
		for _, dep := range stages[indices[id]].Dependencies {
			if err := visit(dep); err != nil {
				return err
			}
		}
		visited[id] = 2
		order = append(order, indices[id])
		return nil
	}
	if err := visit(final); err != nil {
		return nil, err
	}
	if len(order) != len(stages) {
		return nil, invalid("every stage must contribute to final_stage")
	}
	ctes, checks, params := []string{}, []string{}, []any{}
	for _, i := range order {
		s := stages[i]
		name := names[s.ID]
		cols := []string{}
		for _, col := range s.Columns {
			cols = append(cols, pipelineQuote(col))
		}
		rowJSON := pipelineRowJSON(s.Columns)
		ctes = append(ctes, pipelineQuote(name+"_raw")+"("+strings.Join(cols, ",")+") AS MATERIALIZED (SELECT * FROM (\n"+s.SQL+"\n) LIMIT "+strconv.Itoa(s.MaxRows+1)+")")
		ctes = append(ctes, pipelineQuote(name+"_check")+" AS MATERIALIZED (SELECT COUNT(*) AS n, COALESCE(SUM(length(CAST("+rowJSON+" AS BLOB))),0) AS b FROM "+pipelineQuote(name+"_raw")+")")
		ctes = append(ctes, pipelineQuote(name)+" AS MATERIALIZED (SELECT * FROM "+pipelineQuote(name+"_raw")+" WHERE (SELECT n <= "+strconv.Itoa(s.MaxRows)+" AND b <= "+strconv.Itoa(s.MaxBytes)+" FROM "+pipelineQuote(name+"_check")+"))")
		checks = append(checks, "(SELECT json_object('id',"+pipelineLiteral(s.ID)+",'rows',n,'bytes',b) FROM "+pipelineQuote(name+"_check")+")")
		params = append(params, s.Params...)
	}
	finalColumns := stages[indices[final]].Columns
	sql := "WITH " + strings.Join(ctes, ",\n") + "\nSELECT json_object('rows',json((SELECT COALESCE(json_group_array(" + pipelineRowJSON(finalColumns) + "),'[]') FROM " + pipelineQuote(names[final]) + ")), 'checks',json_array(" + strings.Join(checks, ",") + ")) AS __graphql_pipeline"
	// Reuse the existing source authorization and result contract validator.
	c := mergeMaps(config, map[string]any{"version": 1, "engine": "tables_query", "sql": sql, "params": params})
	plan, err := validateAggregatePipeline(aggregatePipelineOperation, c, sources, currentID)
	if err != nil {
		return nil, err
	}
	plan.Stages, plan.FinalColumns = stages, finalColumns
	return plan, nil
}

func (p *aggregatePipelinePlan) validateStageLimits(limits releaseLimits) error {
	rows, bytes := 0, 0
	for _, s := range p.Stages {
		rows += s.MaxRows
		bytes += s.MaxBytes
	}
	if rows > limits.MaxRows || bytes > limits.MaxResponseBytes {
		return invalid("pipeline cumulative stage row/byte budgets exceed release limits")
	}
	return nil
}

func decodeStagedPipeline(plan *aggregatePipelinePlan, value any) (any, error) {
	envelope, ok := value.(map[string]any)
	if !ok {
		return nil, internal("invalid pipeline result")
	}
	if envelope["truncated"] == true {
		return nil, runtimeError("source_result_truncated", "pipeline backend envelope was truncated")
	}
	rows, ok := envelope["rows"].([]any)
	if !ok || len(rows) != 1 {
		return nil, internal("pipeline backend omitted final envelope")
	}
	row, ok := rows[0].(map[string]any)
	if !ok {
		return nil, internal("invalid pipeline envelope")
	}
	encoded, ok := row["__graphql_pipeline"].(string)
	if !ok {
		return nil, internal("invalid pipeline encoded result")
	}
	var out struct {
		Rows   []any `json:"rows"`
		Checks []struct {
			ID    string `json:"id"`
			Rows  int    `json:"rows"`
			Bytes int    `json:"bytes"`
		} `json:"checks"`
	}
	if json.Unmarshal([]byte(encoded), &out) != nil || out.Rows == nil || len(out.Checks) != len(plan.Stages) {
		return nil, internal("invalid pipeline checks")
	}
	byID := map[string]aggregatePipelineStage{}
	for _, stage := range plan.Stages {
		byID[stage.ID] = stage
	}
	for _, check := range out.Checks {
		stage, ok := byID[check.ID]
		if !ok || check.Rows < 0 || check.Bytes < 0 {
			return nil, internal("invalid pipeline stage check")
		}
		delete(byID, check.ID)
		if check.Rows > stage.MaxRows {
			return nil, runtimeError("row_limit_exceeded", "pipeline stage row limit exceeded")
		}
		if check.Bytes > stage.MaxBytes {
			return nil, runtimeError("response_limit_exceeded", "pipeline stage byte limit exceeded")
		}
	}
	return map[string]any{"rows": out.Rows, "columns": plan.FinalColumns, "truncated": false, "projections": projectionMetadata(envelope["projections"])}, nil
}

func (l *resolverLoader) loadStagedPipeline(plan *aggregatePipelinePlan, input map[string]any) func() (any, error) {
	return l.deferCall(runtimeDigest([]any{"staged-pipeline", input, plan.Result, plan.MaxRows, plan.OnTruncated}), func() (any, error) {
		started := time.Now()
		ctx := l.ctx
		if state, ok := l.ctx.Value(standardRequestKey{}).(*standardRequest); ok {
			defer func() { state.tablesNanos.Add(time.Since(started).Nanoseconds()) }()
			if err := state.reservePipeline(plan); err != nil {
				return nil, err
			}
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(state.limits.MaxSnapshotMS)*time.Millisecond)
			defer cancel()
		}
		l.backendMetrics(1, len(plan.Stages))
		out, err := (&tablesUpstream{app: l.app, project: l.project, consistency: "batch"}).Read(ctx, nil, []upstreamRead{{ID: "final", Operation: "tables_query", Arguments: input}})
		if err != nil {
			return nil, err
		}
		value, err := decodeStagedPipeline(plan, out["final"].Value)
		if err != nil {
			return nil, err
		}
		if state, ok := l.ctx.Value(standardRequestKey{}).(*standardRequest); ok {
			state.sourceMetadata("tables", "batch", projectionMetadata(value.(map[string]any)["projections"]))
		}
		return transformAggregatePipelineResult(plan, value)
	})
}

// Reserve the declared stage budgets once per distinct loader execution.
// This bounds all pipelines in the request, including aliases and list fanout.
func (s *standardRequest) reservePipeline(plan *aggregatePipelinePlan) error {
	rows, bytes := 0, 0
	for _, stage := range plan.Stages {
		rows += stage.MaxRows
		bytes += stage.MaxBytes
	}
	s.timingMu.Lock()
	defer s.timingMu.Unlock()
	if s.pipelineRows+rows > s.limits.MaxRows {
		return runtimeError("row_limit_exceeded", "request pipeline row budget exceeded")
	}
	if s.pipelineBytes+bytes > s.limits.MaxResponseBytes {
		return runtimeError("response_limit_exceeded", "request pipeline byte budget exceeded")
	}
	if s.pipelineCost+rows > s.limits.MaxCost {
		return runtimeError("query_cost_exceeded", "request pipeline cost budget exceeded")
	}
	s.pipelineRows += rows
	s.pipelineBytes += bytes
	s.pipelineCost += rows
	return nil
}

func (a *App) callStagedPipeline(ctx context.Context, project string, plan *aggregatePipelinePlan, input map[string]any) (any, error) {
	limits, ok := ctx.Value(executionLimitsKey{}).(releaseLimits)
	if !ok {
		limits = defaultReleaseLimits()
	}
	if err := plan.validateStageLimits(limits); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(limits.MaxSnapshotMS)*time.Millisecond)
	defer cancel()
	out, err := (&tablesUpstream{app: a, project: project, consistency: "batch"}).Read(ctx, nil, []upstreamRead{{ID: "final", Operation: "tables_query", Arguments: input}})
	if err != nil {
		return nil, err
	}
	value, err := decodeStagedPipeline(plan, out["final"].Value)
	if err != nil {
		return nil, err
	}
	return transformAggregatePipelineResult(plan, value)
}
