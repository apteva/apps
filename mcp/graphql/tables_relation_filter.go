package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	relationFilterDefaultScanLimit = 10000
	relationFilterMaxScanLimit     = 100000
	relationFilterChunkSize        = 500
	relationFilterMaxNodes         = 200
	relationFilterMaxDepth         = 16
	relationFilterMaxExistsDepth   = 2
	relationCursorPrefix           = "grf1."
)

// relation_filter is server-owned resolver configuration. It is intentionally
// not sourced from GraphQL arguments: callers may add filters but cannot weaken
// the configured relationship or authorization scope.
func hasRelationFilter(config map[string]any) bool {
	return config != nil && config["relation_filter"] != nil
}

type relationValidation struct {
	nodes       int
	existsDepth int
	sources     map[string]sourceRecord
}

func validateRelationFilter(operation string, config map[string]any, sources []sourceRecord) error {
	raw := config["relation_filter"]
	if raw == nil {
		return nil
	}
	switch strings.ToLower(operation) {
	case "find", "list", "search":
	default:
		return invalid("relation_filter is supported only for Tables find, list, and search resolvers")
	}
	if _, err := relationScanLimit(config); err != nil {
		return err
	}
	byName := map[string]sourceRecord{}
	for _, source := range sources {
		if source.Status == "active" && source.Kind == "tables" {
			byName[source.Name] = source
		}
	}
	state := &relationValidation{sources: byName}
	if err := state.boolean(raw, 1, 0); err != nil {
		return invalid("invalid relation_filter: %s", err)
	}
	return nil
}

func (v *relationValidation) count(depth int) error {
	v.nodes++
	if v.nodes > relationFilterMaxNodes {
		return fmt.Errorf("exceeds %d nodes", relationFilterMaxNodes)
	}
	if depth > relationFilterMaxDepth {
		return fmt.Errorf("exceeds maximum depth %d", relationFilterMaxDepth)
	}
	return nil
}

func (v *relationValidation) boolean(raw any, depth, existsDepth int) error {
	if err := v.count(depth); err != nil {
		return err
	}
	node, ok := raw.(map[string]any)
	if !ok || len(node) != 1 {
		return fmt.Errorf("each predicate must be an object with exactly one operator")
	}
	for op, value := range node {
		switch strings.ToLower(op) {
		case "and", "or":
			items, ok := value.([]any)
			if !ok || len(items) == 0 || len(items) > 100 {
				return fmt.Errorf("%s requires 1 to 100 predicates", op)
			}
			for _, item := range items {
				if err := v.boolean(item, depth+1, existsDepth); err != nil {
					return err
				}
			}
		case "not":
			return v.boolean(value, depth+1, existsDepth)
		case "eq", "neq", "lt", "lte", "gt", "gte", "contains", "in", "between":
			values, ok := value.([]any)
			if !ok || len(values) != 2 {
				return fmt.Errorf("%s requires exactly two value expressions", op)
			}
			for _, item := range values {
				if err := v.value(item, depth+1); err != nil {
					return err
				}
			}
		case "is_null", "is_not_null":
			if err := v.value(value, depth+1); err != nil {
				return err
			}
		case "exists", "not_exists":
			if existsDepth >= relationFilterMaxExistsDepth {
				return fmt.Errorf("correlated existence nesting exceeds %d", relationFilterMaxExistsDepth)
			}
			if err := v.exists(value, depth+1, existsDepth+1); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported predicate operator %q", op)
		}
	}
	return nil
}

func (v *relationValidation) value(raw any, depth int) error {
	if err := v.count(depth); err != nil {
		return err
	}
	node, ok := raw.(map[string]any)
	if !ok || len(node) != 1 {
		return fmt.Errorf("value expressions must contain exactly one operator")
	}
	for op, value := range node {
		switch strings.ToLower(op) {
		case "column", "outer", "parent", "argument":
			path, ok := value.(string)
			if !ok || !validRelationPath(path) {
				return fmt.Errorf("%s requires a valid field path", op)
			}
		case "const":
			// JSON constants are already bounded by resolver configuration size.
		case "coalesce":
			items, ok := value.([]any)
			if !ok || len(items) < 2 || len(items) > 10 {
				return fmt.Errorf("coalesce requires 2 to 10 value expressions")
			}
			for _, item := range items {
				if err := v.value(item, depth+1); err != nil {
					return err
				}
			}
		case "cast":
			cast, ok := value.(map[string]any)
			kind, _ := cast["type"].(string)
			if !ok || len(cast) != 2 || cast["value"] == nil || (kind != "string" && kind != "number" && kind != "boolean") {
				return fmt.Errorf("cast requires value and type string, number, or boolean")
			}
			if err := v.value(cast["value"], depth+1); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported value operator %q", op)
		}
	}
	return nil
}

func (v *relationValidation) exists(raw any, depth, existsDepth int) error {
	node, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("exists requires an object")
	}
	for key := range node {
		if key != "source" && key != "correlate" && key != "where" {
			return fmt.Errorf("exists contains unsupported property %q", key)
		}
	}
	name, _ := node["source"].(string)
	source, found := v.sources[name]
	if name == "" || !found {
		return fmt.Errorf("exists source %q is not an active Tables source in this API", name)
	}
	if hasRelationFilter(source.Config) {
		return fmt.Errorf("exists source %q cannot itself define relation_filter", name)
	}
	pairs, ok := node["correlate"].([]any)
	if !ok || len(pairs) == 0 || len(pairs) > 4 {
		return fmt.Errorf("exists correlate requires 1 to 4 key pairs")
	}
	for _, rawPair := range pairs {
		pair, ok := rawPair.(map[string]any)
		outer, _ := pair["outer"].(string)
		inner, _ := pair["inner"].(string)
		if !ok || len(pair) != 2 || !validRelationPath(outer) || !graphqlName(inner) {
			return fmt.Errorf("exists correlation requires valid outer and inner fields")
		}
	}
	if where := node["where"]; where != nil {
		if err := v.boolean(where, depth+1, existsDepth); err != nil {
			return err
		}
	}
	return nil
}

func validRelationPath(path string) bool {
	parts := strings.Split(path, ".")
	if len(parts) == 0 || len(parts) > 8 {
		return false
	}
	for _, part := range parts {
		if !graphqlName(part) {
			return false
		}
	}
	return true
}

func relationScanLimit(config map[string]any) (int, error) {
	if config["relation_scan_limit"] == nil {
		return relationFilterDefaultScanLimit, nil
	}
	limit, ok := positiveInt(config["relation_scan_limit"])
	if !ok || limit > relationFilterMaxScanLimit {
		return 0, invalid("relation_scan_limit must be between 1 and %d", relationFilterMaxScanLimit)
	}
	return limit, nil
}

func positiveInt(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, value > 0
	case int64:
		return int(value), value > 0 && value <= math.MaxInt
	case float64:
		return int(value), value > 0 && value <= math.MaxInt && value == math.Trunc(value)
	case json.Number:
		n, err := strconv.Atoi(string(value))
		return n, err == nil && n > 0
	default:
		return 0, false
	}
}

type relationEval struct {
	app            *App
	ctx            context.Context
	project        string
	bindings       *executionBindings
	parent         any
	args           map[string]any
	cache          map[string]bool
	maxScan        int
	existsLookups  int
	relatedScanned int
}

type relationScope struct {
	row   map[string]any
	outer map[string]any
}

func (a *App) callTablesRelationFilter(ctx context.Context, operation string, config map[string]any) (any, error) {
	bindings := bindingsFromContext(ctx)
	if bindings == nil {
		return nil, internal("relation_filter requires immutable execution bindings")
	}
	sources := make([]sourceRecord, 0, len(bindings.sources))
	for _, source := range bindings.sources {
		sources = append(sources, source)
	}
	if err := validateRelationFilter(operation, config, sources); err != nil {
		return nil, err
	}
	if input, supported, err := a.nativeRelationFilterInput(ctx, config, resolverArgs(config), bindings); err != nil {
		return nil, err
	} else if supported {
		input["_project_id"] = config["_project_id"]
		page, err := a.callTablesSearch(ctx, fmt.Sprint(config["_project_id"]), input)
		if err != nil {
			return nil, err
		}
		return unwrapSourceResult(operation, page), nil
	}
	input, err := mappedTablesInput(config, resolverArgs(config))
	if err != nil {
		return nil, err
	}
	delete(input, "select") // residual predicates may depend on unselected fields

	wanted := 50
	if value, ok := positiveInt(input["limit"]); ok {
		wanted = value
	}
	filteredOffset := 0
	if raw := input["offset"]; raw != nil {
		var ok bool
		filteredOffset, ok = nonNegativeInt(raw)
		if !ok {
			return nil, invalid("offset must be a non-negative integer")
		}
	}
	planHash := relationPlanHash(config)
	if raw := input["cursor"]; raw != nil {
		if filteredOffset != 0 {
			return nil, invalid("relation_filter cursor cannot be combined with offset")
		}
		cursor, ok := raw.(string)
		if !ok {
			return nil, invalid("relation_filter cursor must be a string")
		}
		filteredOffset, err = decodeRelationCursor(cursor, planHash)
		if err != nil {
			return nil, err
		}
	}
	includeTotal, _ := input["include_total"].(bool)
	delete(input, "limit")
	delete(input, "offset")
	delete(input, "cursor")
	delete(input, "include_total")

	eval := &relationEval{app: a, ctx: ctx, project: fmt.Sprint(config["_project_id"]), bindings: bindings, parent: config["parent"], args: resolverArgs(config), cache: map[string]bool{}}
	eval.maxScan, _ = relationScanLimit(config)
	if pushed := eval.pushdown(config["relation_filter"], relationScope{}); len(pushed) > 0 {
		where, _ := input["where"].([]any)
		input["where"] = append(where, pushed...)
	}

	need := filteredOffset + wanted + 1
	matched := make([]any, 0, min(need, 1024))
	scanned := 0
	nativeCursor := ""
	exhausted := false
	for !exhausted {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := eval.maxScan - scanned
		if remaining <= 0 {
			return nil, relationScanLimitError(eval.maxScan)
		}
		chunk := min(relationFilterChunkSize, remaining)
		request := cloneMap(input)
		request["limit"] = chunk
		request["include_total"] = false
		if nativeCursor != "" {
			request["cursor"] = nativeCursor
		}
		page, err := a.callTablesSearch(ctx, eval.project, request)
		if err != nil {
			return nil, err
		}
		rows := relationRows(page["rows"])
		scanned += len(rows)
		for _, raw := range rows {
			row, ok := raw.(map[string]any)
			if !ok {
				return nil, internal("Tables returned a non-object row")
			}
			match, err := eval.boolean(config["relation_filter"], relationScope{row: row})
			if err != nil {
				return nil, err
			}
			if match {
				matched = append(matched, row)
			}
		}
		hasMore, _ := page["has_more"].(bool)
		next, _ := page["next_cursor"].(string)
		exhausted = !hasMore || next == "" || len(rows) == 0
		if !includeTotal && len(matched) >= need {
			break
		}
		if !exhausted && scanned >= eval.maxScan {
			return nil, relationScanLimitError(eval.maxScan)
		}
		nativeCursor = next
	}

	total := len(matched)
	start := min(filteredOffset, len(matched))
	end := min(start+wanted, len(matched))
	rows := append([]any(nil), matched[start:end]...)
	hasMore := len(matched) > end
	if !includeTotal && !exhausted && len(matched) >= need {
		hasMore = true
	}
	if strings.EqualFold(operation, "search") {
		out := map[string]any{"rows": rows, "has_more": hasMore}
		if includeTotal {
			out["total"] = total
		}
		if hasMore {
			out["next_cursor"] = encodeRelationCursor(end, planHash)
		}
		return out, nil
	}
	return rows, nil
}

// nativeRelationFilterInput compiles the saved, transport-neutral resolver
// plan to Tables filter_ast v1. A capability probe is cached only after a
// successful response, so a transient unavailable Tables sidecar cannot pin
// the process to the fallback path.
func (a *App) nativeRelationFilterInput(ctx context.Context, config, args map[string]any, bindings *executionBindings) (map[string]any, bool, error) {
	project := fmt.Sprint(config["_project_id"])
	if !a.tablesSupportsFilterAST(ctx, project) {
		return nil, false, nil
	}
	sources := make([]sourceRecord, 0, len(bindings.sources))
	for _, source := range bindings.sources {
		sources = append(sources, source)
	}
	if err := validateRelationFilter("search", config, sources); err != nil {
		return nil, true, err
	}
	input, err := mappedTablesInput(config, args)
	if err != nil {
		return nil, true, err
	}
	compiled, err := compileTablesFilterAST(config["relation_filter"], config["parent"], args, bindings)
	if err != nil {
		return nil, true, err
	}
	input["filter_ast"] = compiled
	return input, true, nil
}

func (a *App) tablesSupportsFilterAST(ctx context.Context, project string) bool {
	a.tablesCapsMu.Lock()
	defer a.tablesCapsMu.Unlock()
	if a.tablesCapsKnown {
		return a.tablesFilterAST
	}
	var capabilities map[string]any
	err := sdk.CallAppResultContext(ctx, a.ctx.WithProject(project).PlatformAPI(), "tables", "tables_capabilities", map[string]any{"_project_id": project}, &capabilities)
	if err != nil {
		return false
	}
	filter, _ := capabilities["filter_ast"].(map[string]any)
	supported, _ := filter["supported"].(bool)
	version := fmt.Sprint(filter["version"])
	supported = supported && version == "1"
	a.tablesCapsKnown = true
	a.tablesFilterAST = supported
	return supported
}

func compileTablesFilterAST(raw, parent any, args map[string]any, bindings *executionBindings) (any, error) {
	compiler := tablesFilterCompiler{parent: parent, args: args, bindings: bindings}
	return compiler.boolean(raw)
}

type tablesFilterCompiler struct {
	parent   any
	args     map[string]any
	bindings *executionBindings
}

func (c tablesFilterCompiler) boolean(raw any) (any, error) {
	node, ok := raw.(map[string]any)
	if !ok || len(node) != 1 {
		return nil, invalid("relation_filter predicate is invalid")
	}
	for op, value := range node {
		op = strings.ToLower(op)
		switch op {
		case "and", "or":
			items := value.([]any)
			compiled := make([]any, 0, len(items))
			for _, item := range items {
				child, err := c.boolean(item)
				if err != nil {
					return nil, err
				}
				compiled = append(compiled, child)
			}
			return map[string]any{op: compiled}, nil
		case "not":
			child, err := c.boolean(value)
			if err != nil {
				return nil, err
			}
			return map[string]any{"not": child}, nil
		case "exists", "not_exists":
			exists, err := c.exists(value.(map[string]any))
			if err != nil {
				return nil, err
			}
			return map[string]any{op: exists}, nil
		case "is_null", "is_not_null":
			left, err := c.value(value)
			if err != nil {
				return nil, err
			}
			return map[string]any{"compare": map[string]any{"op": op, "left": left}}, nil
		default:
			values := value.([]any)
			left, err := c.value(values[0])
			if err != nil {
				return nil, err
			}
			right, err := c.value(values[1])
			if err != nil {
				return nil, err
			}
			if op == "in" || op == "between" {
				literal, ok := right.(map[string]any)["literal"].([]any)
				if !ok {
					return nil, invalid("relation_filter %s requires a literal or argument list", op)
				}
				right = map[string]any{"values": literal}
			}
			return map[string]any{"compare": map[string]any{"op": op, "left": left, "right": right}}, nil
		}
	}
	return nil, invalid("relation_filter predicate is empty")
}

func (c tablesFilterCompiler) value(raw any) (any, error) {
	node := raw.(map[string]any)
	for op, value := range node {
		switch strings.ToLower(op) {
		case "column":
			return map[string]any{"column": value}, nil
		case "outer":
			return map[string]any{"outer_column": value}, nil
		case "const":
			return map[string]any{"literal": value}, nil
		case "parent":
			resolved, found, err := relationAnyPath(c.parent, value.(string))
			if err != nil || !found {
				return nil, invalid("parent relationship key %q is missing", value)
			}
			return map[string]any{"literal": resolved}, nil
		case "argument":
			resolved, found, err := relationPath(c.args, value.(string))
			if err != nil || !found {
				return nil, invalid("relation_filter argument %q is missing", value)
			}
			return map[string]any{"literal": resolved}, nil
		case "coalesce":
			items := value.([]any)
			compiled := make([]any, 0, len(items))
			for _, item := range items {
				child, err := c.value(item)
				if err != nil {
					// A missing parent/argument is NULL for coalesce, allowing
					// the next configured fallback to be selected natively.
					child = map[string]any{"literal": nil}
				}
				compiled = append(compiled, child)
			}
			return map[string]any{"coalesce": compiled}, nil
		case "cast":
			cast := value.(map[string]any)
			child, err := c.value(cast["value"])
			if err != nil {
				return nil, err
			}
			kind := map[string]string{"string": "text", "number": "number", "boolean": "bool"}[cast["type"].(string)]
			return map[string]any{"cast": map[string]any{"type": kind, "expr": child}}, nil
		}
	}
	return nil, invalid("relation_filter value expression is empty")
}

func (c tablesFilterCompiler) exists(node map[string]any) (map[string]any, error) {
	name := node["source"].(string)
	var source *sourceRecord
	if c.bindings != nil {
		for _, candidate := range c.bindings.sources {
			if candidate.Name == name && candidate.Kind == "tables" && candidate.Status == "active" {
				copy := candidate
				source = &copy
				break
			}
		}
	}
	if source == nil {
		return nil, invalid("relation_filter exists source %q is unavailable", name)
	}
	base, err := mappedTablesInput(source.Config, map[string]any{})
	if err != nil {
		return nil, err
	}
	table, _ := base["table"].(string)
	if table == "" {
		return nil, invalid("relation_filter exists source %q has no table", name)
	}
	out := map[string]any{"table": table}
	if where, ok := base["where"].([]any); ok && len(where) > 0 {
		out["where"] = where
	}
	pairs := node["correlate"].([]any)
	correlation := make([]any, 0, len(pairs))
	for _, rawPair := range pairs {
		pair := rawPair.(map[string]any)
		correlation = append(correlation, map[string]any{"outer": pair["outer"], "inner": pair["inner"]})
	}
	out["correlation"] = correlation
	if predicate := node["where"]; predicate != nil {
		compiled, err := c.boolean(predicate)
		if err != nil {
			return nil, err
		}
		out["filter"] = compiled
	}
	return out, nil
}

func relationScanLimitError(limit int) error {
	return &graphqlError{Code: "relation_filter_scan_limit_exceeded", Message: fmt.Sprintf("relation filter exceeded its candidate scan limit of %d rows", limit)}
}

func nonNegativeInt(raw any) (int, bool) {
	if raw == nil {
		return 0, true
	}
	switch value := raw.(type) {
	case int:
		return value, value >= 0
	case int64:
		return int(value), value >= 0 && value <= math.MaxInt
	case float64:
		return int(value), value >= 0 && value <= math.MaxInt && value == math.Trunc(value)
	default:
		return 0, false
	}
}

func cloneMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func relationRows(raw any) []any {
	switch rows := raw.(type) {
	case []any:
		return rows
	case nil:
		return []any{}
	default:
		return []any{}
	}
}

func (a *App) callTablesSearch(ctx context.Context, project string, input map[string]any) (map[string]any, error) {
	input["_project_id"] = project
	var out map[string]any
	if err := sdk.CallAppResultContext(ctx, a.ctx.WithProject(project).PlatformAPI(), "tables", "rows_search", input, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func (e *relationEval) boolean(raw any, scope relationScope) (bool, error) {
	node := raw.(map[string]any) // validated before execution
	for op, value := range node {
		switch strings.ToLower(op) {
		case "and":
			for _, child := range value.([]any) {
				ok, err := e.boolean(child, scope)
				if err != nil || !ok {
					return ok, err
				}
			}
			return true, nil
		case "or":
			for _, child := range value.([]any) {
				ok, err := e.boolean(child, scope)
				if err != nil {
					return false, err
				}
				if ok {
					return true, nil
				}
			}
			return false, nil
		case "not":
			ok, err := e.boolean(value, scope)
			return !ok, err
		case "is_null", "is_not_null":
			resolved, found, err := e.value(value, scope)
			if err != nil {
				return false, err
			}
			isNull := !found || resolved == nil
			return isNull == (op == "is_null"), nil
		case "exists", "not_exists":
			found, err := e.exists(value.(map[string]any), scope)
			if op == "not_exists" {
				found = !found
			}
			return found, err
		default:
			values := value.([]any)
			left, leftFound, err := e.value(values[0], scope)
			if err != nil {
				return false, err
			}
			right, rightFound, err := e.value(values[1], scope)
			if err != nil || !leftFound || !rightFound {
				return false, err // missing dynamic inputs fail closed
			}
			return relationCompare(strings.ToLower(op), left, right)
		}
	}
	return false, nil
}

func (e *relationEval) value(raw any, scope relationScope) (any, bool, error) {
	node := raw.(map[string]any)
	for op, value := range node {
		switch strings.ToLower(op) {
		case "const":
			return value, true, nil
		case "column":
			return relationPath(scope.row, value.(string))
		case "outer":
			return relationPath(scope.outer, value.(string))
		case "parent":
			return relationAnyPath(e.parent, value.(string))
		case "argument":
			return relationPath(e.args, value.(string))
		case "coalesce":
			for _, child := range value.([]any) {
				resolved, found, err := e.value(child, scope)
				if err != nil {
					return nil, false, err
				}
				if found && resolved != nil {
					return resolved, true, nil
				}
			}
			return nil, false, nil
		case "cast":
			cast := value.(map[string]any)
			resolved, found, err := e.value(cast["value"], scope)
			if err != nil || !found {
				return nil, found, err
			}
			converted, err := relationCast(resolved, cast["type"].(string))
			return converted, err == nil, err
		}
	}
	return nil, false, nil
}

func relationAnyPath(raw any, path string) (any, bool, error) {
	value, ok := raw.(map[string]any)
	if !ok {
		return nil, false, nil
	}
	resolved, found, err := relationPath(value, path)
	return resolved, found, err
}

func relationPath(value map[string]any, path string) (any, bool, error) {
	if value == nil {
		return nil, false, nil
	}
	var current any = value
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false, nil
		}
		current, ok = object[part]
		if !ok {
			return nil, false, nil
		}
	}
	return current, true, nil
}

func relationCast(value any, kind string) (any, error) {
	switch kind {
	case "string":
		return fmt.Sprint(value), nil
	case "number":
		if n, ok := relationNumber(value); ok {
			return n, nil
		}
		return nil, invalid("relation_filter could not cast value to number")
	case "boolean":
		switch v := value.(type) {
		case bool:
			return v, nil
		case string:
			parsed, err := strconv.ParseBool(v)
			if err == nil {
				return parsed, nil
			}
		}
		return nil, invalid("relation_filter could not cast value to boolean")
	default:
		return nil, invalid("unsupported relation_filter cast %q", kind)
	}
}

func relationCompare(op string, left, right any) (bool, error) {
	switch op {
	case "eq", "neq":
		equal := relationEqual(left, right)
		if op == "neq" {
			equal = !equal
		}
		return equal, nil
	case "lt", "lte", "gt", "gte":
		cmp, ok := relationOrder(left, right)
		if !ok {
			return false, nil
		}
		switch op {
		case "lt":
			return cmp < 0, nil
		case "lte":
			return cmp <= 0, nil
		case "gt":
			return cmp > 0, nil
		default:
			return cmp >= 0, nil
		}
	case "contains":
		switch container := left.(type) {
		case string:
			return strings.Contains(container, fmt.Sprint(right)), nil
		case []any:
			for _, item := range container {
				if relationEqual(item, right) {
					return true, nil
				}
			}
		}
		return false, nil
	case "in":
		items, ok := right.([]any)
		if !ok {
			return false, invalid("relation_filter in requires a list on the right")
		}
		for _, item := range items {
			if relationEqual(left, item) {
				return true, nil
			}
		}
		return false, nil
	case "between":
		items, ok := right.([]any)
		if !ok || len(items) != 2 {
			return false, invalid("relation_filter between requires a two-value list")
		}
		low, okLow := relationOrder(left, items[0])
		high, okHigh := relationOrder(left, items[1])
		return okLow && okHigh && low >= 0 && high <= 0, nil
	default:
		return false, invalid("unsupported relation_filter comparison %q", op)
	}
}

func relationEqual(left, right any) bool {
	if l, ok := relationNumber(left); ok {
		if r, ok := relationNumber(right); ok {
			return l == r
		}
	}
	return reflect.DeepEqual(left, right)
}

func relationOrder(left, right any) (int, bool) {
	if l, ok := relationNumber(left); ok {
		if r, ok := relationNumber(right); ok {
			switch {
			case l < r:
				return -1, true
			case l > r:
				return 1, true
			default:
				return 0, true
			}
		}
	}
	l, lok := left.(string)
	r, rok := right.(string)
	if !lok || !rok {
		return 0, false
	}
	if lt, ok := relationTime(l); ok {
		if rt, ok := relationTime(r); ok {
			switch {
			case lt.Before(rt):
				return -1, true
			case lt.After(rt):
				return 1, true
			default:
				return 0, true
			}
		}
	}
	return strings.Compare(l, r), true
}

func relationTime(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.DateOnly} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func relationNumber(value any) (float64, bool) {
	switch n := value.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		value, err := n.Float64()
		return value, err == nil
	default:
		return 0, false
	}
}

func (e *relationEval) exists(node map[string]any, scope relationScope) (bool, error) {
	name := node["source"].(string)
	source, found := e.source(name)
	if !found {
		return false, invalid("relation_filter exists source %q is unavailable", name)
	}
	input, err := mappedTablesInput(source.Config, map[string]any{})
	if err != nil {
		return false, err
	}
	delete(input, "select")
	delete(input, "offset")
	delete(input, "cursor")
	where, _ := input["where"].([]any)
	keyParts := []string{name}
	for _, rawPair := range node["correlate"].([]any) {
		pair := rawPair.(map[string]any)
		value, ok, err := relationPath(scope.row, pair["outer"].(string))
		if err != nil || !ok || value == nil {
			return false, err
		}
		inner := pair["inner"].(string)
		where = append(where, map[string]any{"col": inner, "op": "eq", "value": value})
		encoded, _ := json.Marshal(value)
		keyParts = append(keyParts, inner+"="+string(encoded))
	}
	sort.Strings(keyParts[1:])
	predicateJSON, _ := json.Marshal(node["where"])
	keyParts = append(keyParts, "where="+string(predicateJSON))
	cacheKey := strings.Join(keyParts, "\x00")
	if cached, ok := e.cache[cacheKey]; ok {
		return cached, nil
	}
	e.existsLookups++
	if e.existsLookups > e.maxScan {
		return false, relationScanLimitError(e.maxScan)
	}
	input["where"] = where
	input["include_total"] = false
	input["limit"] = min(relationFilterChunkSize, e.maxScan)
	outer := scope.row
	scanned := 0
	cursor := ""
	for {
		request := cloneMap(input)
		if cursor != "" {
			request["cursor"] = cursor
		}
		page, err := e.app.callTablesSearch(e.ctx, e.project, request)
		if err != nil {
			return false, err
		}
		rows := relationRows(page["rows"])
		scanned += len(rows)
		e.relatedScanned += len(rows)
		if e.relatedScanned > e.maxScan {
			return false, relationScanLimitError(e.maxScan)
		}
		for _, raw := range rows {
			row, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			match := true
			if predicate := node["where"]; predicate != nil {
				match, err = e.boolean(predicate, relationScope{row: row, outer: outer})
				if err != nil {
					return false, err
				}
			}
			if match {
				e.cache[cacheKey] = true
				return true, nil
			}
		}
		hasMore, _ := page["has_more"].(bool)
		next, _ := page["next_cursor"].(string)
		if !hasMore || next == "" || len(rows) == 0 {
			e.cache[cacheKey] = false
			return false, nil
		}
		if scanned >= e.maxScan {
			return false, relationScanLimitError(e.maxScan)
		}
		cursor = next
	}
}

func (e *relationEval) source(name string) (sourceRecord, bool) {
	for _, source := range e.bindings.sources {
		if source.Name == name && source.Kind == "tables" && source.Status == "active" {
			return source, true
		}
	}
	return sourceRecord{}, false
}

// Push down only predicates whose right-hand side is fixed before scanning.
// The complete expression is still evaluated after the read, so this is an
// optimization and cannot change authorization semantics.
func (e *relationEval) pushdown(raw any, scope relationScope) []any {
	node, ok := raw.(map[string]any)
	if !ok || len(node) != 1 {
		return nil
	}
	for op, value := range node {
		op = strings.ToLower(op)
		if op == "and" {
			var out []any
			for _, child := range value.([]any) {
				out = append(out, e.pushdown(child, scope)...)
			}
			return out
		}
		if op == "is_null" || op == "is_not_null" {
			if column := relationColumn(value); column != "" {
				return []any{map[string]any{"col": column, "op": op}}
			}
			return nil
		}
		if op != "eq" && op != "neq" && op != "lt" && op != "lte" && op != "gt" && op != "gte" && op != "contains" && op != "in" && op != "between" {
			return nil
		}
		values := value.([]any)
		column := relationColumn(values[0])
		if column == "" {
			return nil
		}
		resolved, found, err := e.value(values[1], scope)
		if err != nil || !found {
			return nil
		}
		return []any{map[string]any{"col": column, "op": op, "value": resolved}}
	}
	return nil
}

func relationColumn(raw any) string {
	node, ok := raw.(map[string]any)
	if !ok || len(node) != 1 {
		return ""
	}
	value, ok := node["column"].(string)
	if !ok || !graphqlName(value) {
		return ""
	}
	return value
}

func relationPlanHash(config map[string]any) string {
	encoded, _ := json.Marshal([]any{config["table"], config["where"], config["identity_where"], config["relation"], config["relation_filter"], config["order_by"]})
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:8])
}

type relationCursor struct {
	Version int    `json:"v"`
	Offset  int    `json:"o"`
	Plan    string `json:"p"`
}

func encodeRelationCursor(offset int, plan string) string {
	encoded, _ := json.Marshal(relationCursor{Version: 1, Offset: offset, Plan: plan})
	return relationCursorPrefix + base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeRelationCursor(value, plan string) (int, error) {
	if !strings.HasPrefix(value, relationCursorPrefix) {
		return 0, invalid("cursor was not issued for this relation_filter")
	}
	encoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, relationCursorPrefix))
	if err != nil {
		return 0, invalid("invalid relation_filter cursor")
	}
	var cursor relationCursor
	if json.Unmarshal(encoded, &cursor) != nil || cursor.Version != 1 || cursor.Offset < 0 || cursor.Plan != plan {
		return 0, invalid("relation_filter cursor does not match this resolver plan")
	}
	return cursor.Offset, nil
}
