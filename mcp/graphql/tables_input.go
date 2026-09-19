package main

import (
	"fmt"
	"sort"
	"strings"
)

// graphqlWhere accepts the ordinary typed input-object shapes API authors
// declare in SDL and lowers the subset Tables can execute natively. The
// existing [{col,op,value}] form remains supported for compatibility.
func graphqlWhere(raw any, columns map[string]string) ([]any, error) {
	if raw == nil {
		return nil, nil
	}
	switch value := raw.(type) {
	case []any:
		out := []any{}
		for _, item := range value {
			predicates, err := graphqlWhere(item, columns)
			if err != nil {
				return nil, err
			}
			out = append(out, predicates...)
		}
		return boundedPredicates(out)
	case map[string]any:
		if _, legacy := value["col"]; legacy {
			col, _ := value["col"].(string)
			op, _ := value["op"].(string)
			if !graphqlName(col) || strings.TrimSpace(op) == "" {
				return nil, invalid("where predicate requires col and op")
			}
			return []any{map[string]any{"col": col, "op": normalizeFilterOperator(op), "value": value["value"]}}, nil
		}
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := []any{}
		for _, key := range keys {
			item := value[key]
			switch strings.ToLower(key) {
			case "and":
				predicates, err := graphqlWhere(item, columns)
				if err != nil {
					return nil, err
				}
				out = append(out, predicates...)
			case "or":
				predicate, err := collapseGraphQLOr(item, columns)
				if err != nil {
					return nil, err
				}
				out = append(out, predicate)
			case "not":
				predicates, err := graphqlWhere(item, columns)
				if err != nil || len(predicates) != 1 {
					return nil, invalid("not must contain exactly one native predicate")
				}
				predicate := predicates[0].(map[string]any)
				inverse, ok := inverseFilterOperator(fmt.Sprint(predicate["op"]))
				if !ok {
					return nil, invalid("not cannot invert operator %q", predicate["op"])
				}
				predicate["op"] = inverse
				out = append(out, predicate)
			default:
				col := key
				if mapped := columns[key]; mapped != "" {
					col = mapped
				}
				if !graphqlName(col) {
					return nil, invalid("invalid filter field %q", key)
				}
				predicates, err := graphqlFieldPredicates(col, item)
				if err != nil {
					return nil, err
				}
				out = append(out, predicates...)
			}
		}
		return boundedPredicates(out)
	default:
		return nil, invalid("where must be an input object or list")
	}
}

func graphqlFieldPredicates(column string, raw any) ([]any, error) {
	operators, ok := raw.(map[string]any)
	if !ok {
		return []any{map[string]any{"col": column, "op": "eq", "value": raw}}, nil
	}
	keys := make([]string, 0, len(operators))
	for key := range operators {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := []any{}
	for _, key := range keys {
		value := operators[key]
		op := normalizeFilterOperator(key)
		switch op {
		case "eq", "neq", "lt", "lte", "gt", "gte", "contains", "in", "between":
			out = append(out, map[string]any{"col": column, "op": op, "value": value})
		case "not_in":
			values, ok := value.([]any)
			if !ok || len(values) == 0 {
				return nil, invalid("filter %s.notIn requires a non-empty list", column)
			}
			for _, item := range values {
				out = append(out, map[string]any{"col": column, "op": "neq", "value": item})
			}
		case "is_null":
			flag, ok := value.(bool)
			if !ok {
				return nil, invalid("filter %s.isNull requires Boolean", column)
			}
			if flag {
				out = append(out, map[string]any{"col": column, "op": "is_null"})
			} else {
				out = append(out, map[string]any{"col": column, "op": "is_not_null"})
			}
		case "is_not_null":
			flag, ok := value.(bool)
			if !ok {
				return nil, invalid("filter %s.isNotNull requires Boolean", column)
			}
			if flag {
				out = append(out, map[string]any{"col": column, "op": "is_not_null"})
			} else {
				out = append(out, map[string]any{"col": column, "op": "is_null"})
			}
		default:
			return nil, invalid("filter %s uses unsupported operator %q", column, key)
		}
	}
	if len(out) == 0 {
		return nil, invalid("filter %s cannot be empty", column)
	}
	return boundedPredicates(out)
}

func normalizeFilterOperator(value string) string {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "")) {
	case "eq", "equals":
		return "eq"
	case "ne", "neq", "notequals":
		return "neq"
	case "lt":
		return "lt"
	case "lte":
		return "lte"
	case "gt":
		return "gt"
	case "gte":
		return "gte"
	case "contains":
		return "contains"
	case "in":
		return "in"
	case "notin":
		return "not_in"
	case "between":
		return "between"
	case "isnull":
		return "is_null"
	case "isnotnull":
		return "is_not_null"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func inverseFilterOperator(op string) (string, bool) {
	switch normalizeFilterOperator(op) {
	case "eq":
		return "neq", true
	case "neq":
		return "eq", true
	case "lt":
		return "gte", true
	case "lte":
		return "gt", true
	case "gt":
		return "lte", true
	case "gte":
		return "lt", true
	case "is_null":
		return "is_not_null", true
	case "is_not_null":
		return "is_null", true
	default:
		return "", false
	}
}

// Tables currently has an AND predicate list. A useful standard GraphQL OR
// case still pushes down exactly: equality alternatives for one column become
// one native IN predicate. Other disjunctions fail explicitly, never broaden.
func collapseGraphQLOr(raw any, columns map[string]string) (any, error) {
	branches, ok := raw.([]any)
	if !ok || len(branches) == 0 {
		return nil, invalid("or requires a non-empty list")
	}
	column := ""
	values := []any{}
	for _, branch := range branches {
		predicates, err := graphqlWhere(branch, columns)
		if err != nil || len(predicates) != 1 {
			return nil, invalid("or is natively supported for one field's equality alternatives")
		}
		predicate := predicates[0].(map[string]any)
		col, _ := predicate["col"].(string)
		if column == "" {
			column = col
		}
		if col != column {
			return nil, invalid("or alternatives must filter the same field")
		}
		switch predicate["op"] {
		case "eq":
			values = append(values, predicate["value"])
		case "in":
			items, ok := predicate["value"].([]any)
			if !ok {
				return nil, invalid("in filter requires a list")
			}
			values = append(values, items...)
		default:
			return nil, invalid("or alternatives must use eq or in")
		}
	}
	return map[string]any{"col": column, "op": "in", "value": values}, nil
}

func boundedPredicates(predicates []any) ([]any, error) {
	if len(predicates) > 100 {
		return nil, invalid("where exceeds maximum of 100 predicates")
	}
	return predicates, nil
}

func filterColumnMap(raw any) map[string]string {
	out := map[string]string{}
	if values, ok := raw.(map[string]any); ok {
		for key, value := range values {
			if column, ok := value.(string); ok && graphqlName(key) && graphqlName(column) {
				out[key] = column
			}
		}
	}
	return out
}
