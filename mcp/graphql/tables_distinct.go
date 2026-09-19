package main

import (
	"encoding/json"
	"fmt"
)

type tablesDistinctPlan struct {
	keys      []string
	defaults  map[string]any
	limit     int
	scanLimit int
}

// tablesDistinct compiles optional ordered distinct selection for Tables list
// resolvers. Tables performs filtering and ordering; GraphQL keeps the first
// row for each configured key tuple and applies the GraphQL list limit. This is
// equivalent to the common "latest row per group" resolver pattern while the
// public request remains ordinary schema-defined GraphQL.
func tablesDistinct(operation string, config, args map[string]any) (*tablesDistinctPlan, error) {
	raw, exists := config["distinct_by"]
	if !exists {
		return nil, nil
	}
	if operation != "find" && operation != "list" {
		return nil, invalid("distinct_by requires a Tables find or list resolver")
	}
	values, ok := raw.([]any)
	if !ok || len(values) == 0 || len(values) > 8 {
		return nil, invalid("distinct_by must contain 1 to 8 column names")
	}
	plan := &tablesDistinctPlan{scanLimit: 1000, defaults: map[string]any{}}
	seen := map[string]bool{}
	for _, value := range values {
		key, ok := value.(string)
		if !ok || !graphqlName(key) || seen[key] {
			return nil, invalid("distinct_by contains an invalid or duplicate column")
		}
		seen[key] = true
		plan.keys = append(plan.keys, key)
	}
	if rawDefaults, exists := config["distinct_defaults"]; exists {
		defaults, ok := rawDefaults.(map[string]any)
		if !ok {
			return nil, invalid("distinct_defaults must be an object")
		}
		for key, value := range defaults {
			if !seen[key] {
				return nil, invalid("distinct_defaults key %q is not in distinct_by", key)
			}
			switch value.(type) {
			case string, bool, float64, json.Number:
				plan.defaults[key] = value
			default:
				return nil, invalid("distinct_defaults values must be scalar")
			}
		}
	}
	if value, exists := config["distinct_scan_limit"]; exists {
		plan.scanLimit = intValue(value)
		if plan.scanLimit <= 0 || plan.scanLimit > 1000 {
			return nil, invalid("distinct_scan_limit must be between 1 and 1000")
		}
	}
	plan.limit = intValue(args["first"])
	if plan.limit <= 0 {
		plan.limit = intValue(args["limit"])
	}
	if plan.limit <= 0 {
		plan.limit = plan.scanLimit
	}
	if plan.limit > plan.scanLimit {
		plan.limit = plan.scanLimit
	}
	return plan, nil
}

func intValue(value any) int {
	switch number := value.(type) {
	case int:
		return number
	case int32:
		return int(number)
	case int64:
		return int(number)
	case float64:
		return int(number)
	case json.Number:
		value, _ := number.Int64()
		return int(value)
	default:
		return 0
	}
}

func (p *tablesDistinctPlan) apply(value any) (any, error) {
	if p == nil {
		return value, nil
	}
	rows, ok := value.([]any)
	if !ok {
		return nil, internal("distinct Tables result is not a row list")
	}
	out := make([]any, 0, min(p.limit, len(rows)))
	seen := map[string]bool{}
	for _, value := range rows {
		row, ok := value.(map[string]any)
		if !ok {
			return nil, internal("distinct Tables row is invalid")
		}
		tuple := make([]any, len(p.keys))
		for index, key := range p.keys {
			item, exists := row[key]
			if !exists {
				return nil, invalid("distinct key %q is missing from the Tables projection", key)
			}
			if fallback, configured := p.defaults[key]; configured && (item == nil || item == "") {
				item = fallback
			}
			tuple[index] = item
		}
		encoded, err := json.Marshal(tuple)
		if err != nil {
			return nil, fmt.Errorf("encode distinct key: %w", err)
		}
		key := string(encoded)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, row)
		if len(out) == p.limit {
			break
		}
	}
	return out, nil
}
