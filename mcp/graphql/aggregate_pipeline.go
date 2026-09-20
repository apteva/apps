package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const aggregatePipelineOperation = "aggregate_pipeline"

var aggregatePipelinePlaceholder = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\}`)

type aggregatePipelinePlan struct {
	SQL         string
	Params      []aggregatePipelineParam
	Result      string
	MaxRows     int
	OnTruncated string
	Tables      map[string]bool
}

type aggregatePipelineParam struct {
	From     string
	Type     string
	Required bool
	Literal  any
	HasValue bool
}

// validateAggregatePipeline compiles immutable resolver configuration. The
// public GraphQL document can supply values only; SQL, identifiers and source
// selection always come from the versioned resolver release.
func validateAggregatePipeline(operation string, config map[string]any, sources []sourceRecord, currentSourceID int64) (*aggregatePipelinePlan, error) {
	if operation != aggregatePipelineOperation {
		return nil, nil
	}
	version := intValue(config["version"])
	if version != 1 {
		return nil, invalid("aggregate_pipeline requires version 1")
	}
	engine, _ := config["engine"].(string)
	if engine != "tables_query" {
		return nil, invalid("aggregate_pipeline engine must be tables_query")
	}
	sqlText, ok := config["sql"].(string)
	sqlText = strings.TrimSpace(sqlText)
	if !ok || sqlText == "" {
		return nil, invalid("aggregate_pipeline requires fixed resolver SQL")
	}
	if len(sqlText) > 64<<10 {
		return nil, invalid("aggregate_pipeline SQL exceeds 65536 bytes")
	}
	lower := strings.ToLower(strings.TrimSpace(sqlText))
	if !strings.HasPrefix(lower, "select ") && !strings.HasPrefix(lower, "select\n") &&
		!strings.HasPrefix(lower, "select\t") && !strings.HasPrefix(lower, "with ") &&
		!strings.HasPrefix(lower, "with\n") && !strings.HasPrefix(lower, "with\t") {
		return nil, invalid("aggregate_pipeline SQL must be a SELECT or WITH query")
	}
	if index := strings.Index(sqlText, ";"); index >= 0 && strings.TrimSpace(sqlText[index+1:]) != "" {
		return nil, invalid("aggregate_pipeline SQL must contain one statement")
	}

	names, err := pipelineStringList(config["sources"])
	if err != nil || len(names) == 0 || len(names) > 32 {
		return nil, invalid("aggregate_pipeline sources must contain 1 to 32 source names")
	}
	byName := make(map[string]sourceRecord, len(sources))
	for _, source := range sources {
		byName[source.Name] = source
	}
	allowedTables := map[string]bool{}
	seenSources := map[string]bool{}
	currentDeclared := false
	for _, name := range names {
		if seenSources[name] {
			return nil, invalid("aggregate_pipeline contains duplicate source %q", name)
		}
		seenSources[name] = true
		source, found := byName[name]
		if !found || source.Status != "active" || source.Kind != "tables" {
			return nil, invalid("aggregate_pipeline source %q is not an active Tables source", name)
		}
		if source.ID == currentSourceID {
			currentDeclared = true
		}
		for _, key := range []string{"where", "identity_where", "relation", "relation_filter", "distinct_by"} {
			if source.Config[key] != nil {
				return nil, invalid("aggregate_pipeline source %q has %s scope that the pipeline cannot safely inherit", name, key)
			}
		}
		table, _ := source.Config["table"].(string)
		if !graphqlName(table) {
			return nil, invalid("aggregate_pipeline source %q requires a fixed table", name)
		}
		allowedTables[table] = true
	}
	if currentSourceID > 0 && !currentDeclared {
		return nil, invalid("aggregate_pipeline sources must include its bound Tables source")
	}
	matches := aggregatePipelinePlaceholder.FindAllStringSubmatch(sqlText, -1)
	if len(matches) == 0 {
		return nil, invalid("aggregate_pipeline SQL must use at least one {table_name} placeholder")
	}
	for _, match := range matches {
		if !allowedTables[match[1]] {
			return nil, invalid("aggregate_pipeline SQL references undeclared table %q", match[1])
		}
	}

	params, err := parseAggregatePipelineParams(config["params"])
	if err != nil {
		return nil, err
	}
	result, _ := config["result"].(string)
	if result == "" {
		result = "rows"
	}
	if result != "rows" && result != "single" && result != "envelope" {
		return nil, invalid("aggregate_pipeline result must be rows, single, or envelope")
	}
	maxRows := intValue(config["max_rows"])
	if maxRows == 0 {
		maxRows = 100
	}
	if maxRows < 1 || maxRows > 1_000_000 {
		return nil, invalid("aggregate_pipeline max_rows must be between 1 and 1000000")
	}
	onTruncated, _ := config["on_truncated"].(string)
	if onTruncated == "" {
		onTruncated = "error"
	}
	if onTruncated != "error" && onTruncated != "return" {
		return nil, invalid("aggregate_pipeline on_truncated must be error or return")
	}
	if onTruncated == "return" && result != "envelope" {
		return nil, invalid("aggregate_pipeline on_truncated=return requires result=envelope")
	}
	return &aggregatePipelinePlan{SQL: sqlText, Params: params, Result: result, MaxRows: maxRows, OnTruncated: onTruncated, Tables: allowedTables}, nil
}

func pipelineStringList(raw any) ([]string, error) {
	var out []string
	switch values := raw.(type) {
	case []any:
		for _, value := range values {
			text, ok := value.(string)
			if !ok || strings.TrimSpace(text) != text || text == "" {
				return nil, invalid("aggregate_pipeline source names must be strings")
			}
			out = append(out, text)
		}
	case []string:
		out = append(out, values...)
	default:
		return nil, invalid("aggregate_pipeline sources must be a list")
	}
	return out, nil
}

func parseAggregatePipelineParams(raw any) ([]aggregatePipelineParam, error) {
	if raw == nil {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok || len(values) > 1000 {
		return nil, invalid("aggregate_pipeline params must be a list of at most 1000 mappings")
	}
	out := make([]aggregatePipelineParam, 0, len(values))
	for _, rawValue := range values {
		value, ok := rawValue.(map[string]any)
		if !ok {
			return nil, invalid("aggregate_pipeline param must be an object")
		}
		param := aggregatePipelineParam{Required: true}
		if required, exists := value["required"]; exists {
			var valid bool
			param.Required, valid = required.(bool)
			if !valid {
				return nil, invalid("aggregate_pipeline param required must be Boolean")
			}
		}
		if from, exists := value["from"]; exists {
			param.From, ok = from.(string)
			if !ok || !validAggregatePipelinePath(param.From) {
				return nil, invalid("aggregate_pipeline param has an invalid source path")
			}
		}
		if literal, exists := value["value"]; exists {
			param.Literal, param.HasValue = literal, true
		}
		if (param.From == "") == !param.HasValue {
			return nil, invalid("aggregate_pipeline param requires exactly one of from or value")
		}
		param.Type, _ = value["type"].(string)
		if param.Type == "" {
			param.Type = "string"
		}
		switch param.Type {
		case "string", "number", "boolean", "datetime", "json":
		default:
			return nil, invalid("aggregate_pipeline param type must be string, number, boolean, datetime, or json")
		}
		out = append(out, param)
	}
	return out, nil
}

func validAggregatePipelinePath(path string) bool {
	if path == "$identity.subject" || path == "$identity.tenant" {
		return true
	}
	for _, prefix := range []string{"$args.", "$parent.", "$identity.claim."} {
		if strings.HasPrefix(path, prefix) {
			parts := strings.Split(strings.TrimPrefix(path, prefix), ".")
			if len(parts) == 0 {
				return false
			}
			for _, part := range parts {
				if !graphqlName(part) {
					return false
				}
			}
			return true
		}
	}
	return false
}

func aggregatePipelineParentDependencies(config map[string]any) []string {
	params, err := parseAggregatePipelineParams(config["params"])
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, param := range params {
		if !strings.HasPrefix(param.From, "$parent.") {
			continue
		}
		name := strings.Split(strings.TrimPrefix(param.From, "$parent."), ".")[0]
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func buildAggregatePipelineInput(ctx context.Context, plan *aggregatePipelinePlan, args map[string]any, parent any, project string) (map[string]any, error) {
	params := make([]any, 0, len(plan.Params))
	for _, binding := range plan.Params {
		value, found := binding.Literal, binding.HasValue
		if binding.From != "" {
			value, found = aggregatePipelineValue(ctx, binding.From, args, parent)
		}
		if !found || value == nil {
			if binding.Required {
				return nil, forbidden("required aggregate pipeline parameter is missing")
			}
			params = append(params, nil)
			continue
		}
		coerced, err := coerceAggregatePipelineValue(value, binding.Type)
		if err != nil {
			if strings.HasPrefix(binding.From, "$identity.") {
				return nil, forbidden("authorization claim cannot be applied")
			}
			return nil, err
		}
		params = append(params, coerced)
	}
	return map[string]any{"sql": plan.SQL, "params": params, "_project_id": project}, nil
}

func aggregatePipelineValue(ctx context.Context, path string, args map[string]any, parent any) (any, bool) {
	switch path {
	case "$identity.subject":
		identity := securityIdentity(ctx)
		if identity == nil || identity.Subject == "" {
			return nil, false
		}
		return identity.Subject, true
	case "$identity.tenant":
		identity := securityIdentity(ctx)
		if identity == nil || identity.Tenant == "" {
			return nil, false
		}
		return identity.Tenant, true
	}
	var root any
	var suffix string
	switch {
	case strings.HasPrefix(path, "$args."):
		root, suffix = args, strings.TrimPrefix(path, "$args.")
	case strings.HasPrefix(path, "$parent."):
		root, suffix = parent, strings.TrimPrefix(path, "$parent.")
	case strings.HasPrefix(path, "$identity.claim."):
		identity := securityIdentity(ctx)
		if identity == nil {
			return nil, false
		}
		root, suffix = identity.Claims, strings.TrimPrefix(path, "$identity.claim.")
	default:
		return nil, false
	}
	for _, part := range strings.Split(suffix, ".") {
		object, ok := root.(map[string]any)
		if !ok {
			return nil, false
		}
		root, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return root, true
}

func coerceAggregatePipelineValue(value any, kind string) (any, error) {
	switch kind {
	case "string":
		text, ok := value.(string)
		if !ok {
			return nil, invalid("aggregate_pipeline parameter must be a string")
		}
		return text, nil
	case "number":
		switch number := value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
			return number, nil
		case json.Number:
			if integer, err := number.Int64(); err == nil {
				return integer, nil
			}
			parsed, err := number.Float64()
			if err != nil {
				return nil, invalid("aggregate_pipeline parameter must be a number")
			}
			return parsed, nil
		case string:
			parsed, err := strconv.ParseFloat(number, 64)
			if err != nil {
				return nil, invalid("aggregate_pipeline parameter must be a number")
			}
			return parsed, nil
		default:
			return nil, invalid("aggregate_pipeline parameter must be a number")
		}
	case "boolean":
		boolean, ok := value.(bool)
		if !ok {
			return nil, invalid("aggregate_pipeline parameter must be a boolean")
		}
		return boolean, nil
	case "datetime":
		text, ok := value.(string)
		if !ok {
			return nil, invalid("aggregate_pipeline parameter must be an RFC3339 datetime")
		}
		parsed, err := time.Parse(time.RFC3339, text)
		if err != nil {
			return nil, invalid("aggregate_pipeline parameter must be an RFC3339 datetime")
		}
		return parsed.UTC().Format(time.RFC3339Nano), nil
	case "json":
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, invalid("aggregate_pipeline parameter must be valid JSON")
		}
		return string(encoded), nil
	default:
		return nil, internal("invalid aggregate pipeline parameter type")
	}
}

func transformAggregatePipelineResult(plan *aggregatePipelinePlan, value any) (any, error) {
	envelope, ok := value.(map[string]any)
	if !ok {
		return nil, internal("aggregate pipeline returned an invalid Tables result")
	}
	rows, ok := envelope["rows"].([]any)
	if !ok {
		// Direct unit-test values and a few SDK decoders can retain the concrete
		// map slice. Normalize without another JSON encode/decode.
		if typed, typedOK := envelope["rows"].([]map[string]any); typedOK {
			rows = make([]any, len(typed))
			for index := range typed {
				rows[index] = typed[index]
			}
		} else {
			return nil, internal("aggregate pipeline returned invalid rows")
		}
	}
	truncated, _ := envelope["truncated"].(bool)
	if truncated && plan.OnTruncated == "error" {
		return nil, &graphqlError{Code: "source_result_truncated", Message: "aggregate pipeline result was truncated by Tables"}
	}
	if len(rows) > plan.MaxRows {
		return nil, &graphqlError{Code: "row_limit_exceeded", Message: fmt.Sprintf("aggregate pipeline returned %d rows, exceeding its limit of %d", len(rows), plan.MaxRows)}
	}
	switch plan.Result {
	case "rows":
		return rows, nil
	case "single":
		if len(rows) == 0 {
			return nil, nil
		}
		if len(rows) != 1 {
			return nil, &graphqlError{Code: "cardinality_error", Message: "aggregate pipeline expected at most one row"}
		}
		return rows[0], nil
	case "envelope":
		return envelope, nil
	default:
		return nil, internal("invalid aggregate pipeline result mode")
	}
}
