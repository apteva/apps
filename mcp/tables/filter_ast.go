package main

import (
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Recursive filters are deliberately transport-neutral. GraphQL and other
// callers can send the same validated expression tree to any read operation.
const (
	maxFilterNodes      = 256
	maxFilterDepth      = 16
	maxFilterSubqueries = 8
)

type filterCompileState struct {
	app        *App
	ctx        *sdk.AppCtx
	projectID  string
	nodes      int
	depth      int
	subqueries int
	aliasSeq   int
}

type filterScope struct {
	table      *Table
	alias      string
	outerTable *Table
	outerAlias string
}

type filterExpr struct {
	sql          string
	args         []any
	typeName     string
	column       *Column
	literal      bool
	explicitCast bool
}

// compileFilter accepts both the legacy flat predicate list and the additive
// recursive filter_ast field. The legacy format remains byte-for-byte
// compatible; when both are present they are combined with AND.
func (a *App) compileFilter(ctx *sdk.AppCtx, projectID string, table *Table, legacy []any, ast any) (clause string, args []any, astUsed bool, err error) {
	legacyClause, legacyArgs, err := buildWhere(table, legacy)
	if err != nil {
		return "", nil, false, err
	}
	if ast == nil {
		return legacyClause, legacyArgs, false, nil
	}
	state := &filterCompileState{app: a, ctx: ctx, projectID: projectID}
	expr, err := state.node(ast, filterScope{table: table, alias: "root"})
	if err != nil {
		return "", nil, true, err
	}
	if expr.sql == "" {
		return "", nil, true, errf("filter_ast must contain a predicate")
	}
	parts := make([]string, 0, 2)
	if legacyClause != "" {
		parts = append(parts, strings.TrimPrefix(legacyClause, "WHERE "))
	}
	parts = append(parts, expr.sql)
	return "WHERE " + strings.Join(parts, " AND "), append(legacyArgs, expr.args...), true, nil
}

func (s *filterCompileState) visit() error {
	s.nodes++
	if s.nodes > maxFilterNodes {
		return errf("filter_ast exceeds maximum of %d nodes", maxFilterNodes)
	}
	return nil
}

func (s *filterCompileState) node(raw any, scope filterScope) (filterExpr, error) {
	if err := s.visit(); err != nil {
		return filterExpr{}, err
	}
	if s.depth >= maxFilterDepth {
		return filterExpr{}, errf("filter_ast exceeds maximum depth of %d", maxFilterDepth)
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return filterExpr{}, errf("filter_ast node must be an object")
	}
	s.depth++
	defer func() { s.depth-- }()
	keys := 0
	for _, k := range []string{"and", "or", "not", "compare", "exists", "not_exists"} {
		if _, ok := obj[k]; ok {
			keys++
		}
	}
	if keys != 1 {
		return filterExpr{}, errf("filter_ast node must contain exactly one predicate")
	}
	if rawAnd, ok := obj["and"]; ok {
		return s.boolean(rawAnd, scope, "AND")
	}
	if rawOr, ok := obj["or"]; ok {
		return s.boolean(rawOr, scope, "OR")
	}
	if rawNot, ok := obj["not"]; ok {
		e, err := s.node(rawNot, scope)
		if err != nil {
			return filterExpr{}, err
		}
		return filterExpr{sql: "NOT (" + e.sql + ")", args: e.args}, nil
	}
	if rawCmp, ok := obj["compare"]; ok {
		return s.compare(rawCmp, scope)
	}
	if rawExists, ok := obj["exists"]; ok {
		return s.exists(rawExists, scope, false)
	}
	return s.exists(obj["not_exists"], scope, true)
}

func (s *filterCompileState) boolean(raw any, scope filterScope, op string) (filterExpr, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return filterExpr{}, errf("filter_ast %s requires a non-empty array", strings.ToLower(op))
	}
	parts := make([]string, 0, len(items))
	var args []any
	for _, item := range items {
		e, err := s.node(item, scope)
		if err != nil {
			return filterExpr{}, err
		}
		parts = append(parts, "("+e.sql+")")
		args = append(args, e.args...)
	}
	return filterExpr{sql: strings.Join(parts, " "+op+" "), args: args}, nil
}

func (s *filterCompileState) compare(raw any, scope filterScope) (filterExpr, error) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return filterExpr{}, errf("compare must be an object")
	}
	op := strArg(obj, "op")
	left, err := s.value(obj["left"], scope, nil)
	if err != nil {
		return filterExpr{}, err
	}
	if op == "is_null" || op == "is_not_null" {
		return filterExpr{sql: left.sql + " " + strings.ReplaceAll(strings.ToUpper(op), "_", " ")}, nil
	}
	rightRaw := obj["right"]
	if rightRaw == nil && obj["value"] != nil {
		rightRaw = map[string]any{"literal": obj["value"]}
	}
	expected := left.column
	if expected == nil && left.typeName != "" {
		expected = &Column{Type: left.typeName}
	}
	switch op {
	case "in":
		vals := rightRaw
		if m, ok := rightRaw.(map[string]any); ok {
			vals = m["values"]
		}
		arr, ok := vals.([]any)
		if !ok || len(arr) == 0 || len(arr) > 1000 {
			return filterExpr{}, errf("in requires 1..1000 values")
		}
		ph := make([]string, len(arr))
		args := append([]any{}, left.args...)
		for i, v := range arr {
			e, err := s.value(map[string]any{"literal": v}, scope, expected)
			if err != nil {
				return filterExpr{}, err
			}
			if err := coerceFilterExpr(&e, expected); err != nil {
				return filterExpr{}, err
			}
			ph[i] = e.sql
			args = append(args, e.args...)
		}
		return filterExpr{sql: left.sql + " IN (" + strings.Join(ph, ",") + ")", args: args}, nil
	case "between":
		vals := rightRaw
		if m, ok := rightRaw.(map[string]any); ok {
			vals = m["values"]
		}
		arr, ok := vals.([]any)
		if !ok || len(arr) != 2 {
			return filterExpr{}, errf("between requires [low, high]")
		}
		lo, err := s.value(map[string]any{"literal": arr[0]}, scope, expected)
		if err != nil {
			return filterExpr{}, err
		}
		hi, err := s.value(map[string]any{"literal": arr[1]}, scope, expected)
		if err != nil {
			return filterExpr{}, err
		}
		if err := coerceFilterExpr(&lo, expected); err != nil {
			return filterExpr{}, err
		}
		if err := coerceFilterExpr(&hi, expected); err != nil {
			return filterExpr{}, err
		}
		return filterExpr{sql: left.sql + " BETWEEN " + lo.sql + " AND " + hi.sql, args: append(append(left.args, lo.args...), hi.args...)}, nil
	}
	right, err := s.value(rightRaw, scope, expected)
	if err != nil {
		return filterExpr{}, err
	}
	if expected == nil {
		expected = right.column
	}
	if err := coerceFilterExpr(&left, expected); err != nil {
		return filterExpr{}, err
	}
	if err := coerceFilterExpr(&right, expected); err != nil {
		return filterExpr{}, err
	}
	switch op {
	case "eq", "neq", "lt", "lte", "gt", "gte":
		return filterExpr{sql: left.sql + " " + sqlOp(op) + " " + right.sql, args: append(left.args, right.args...)}, nil
	case "contains":
		if expected != nil && expected.Type != "text" {
			return filterExpr{}, errf("contains requires a text column")
		}
		if lit, ok := obj["right"].(map[string]any); ok {
			if v, ok := lit["literal"].(string); ok {
				return filterExpr{sql: left.sql + ` LIKE ? ESCAPE '\'`, args: append(left.args, "%"+escapeLike(v)+"%")}, nil
			}
		}
		return filterExpr{sql: left.sql + ` LIKE '%' || ` + right.sql + ` || '%' ESCAPE '\'`, args: append(left.args, right.args...)}, nil
	default:
		return filterExpr{}, errf("unknown filter_ast comparison op %q", op)
	}
}

// coerceFilterExpr applies storage coercion only to an ordinary literal.
// Explicit casts own their operand's type and must not be coerced against the
// surrounding column: doing so both rejects valid cross-type comparisons and
// can force callers to cast the indexed database column instead of the value.
func coerceFilterExpr(expr *filterExpr, expected *Column) error {
	if expr == nil || expected == nil || !expr.literal || expr.explicitCast || len(expr.args) != 1 {
		return nil
	}
	v, err := coerceForStorage(*expected, expr.args[0])
	if err != nil {
		return err
	}
	expr.args[0] = v
	return nil
}

func (s *filterCompileState) value(raw any, scope filterScope, expected *Column) (filterExpr, error) {
	if err := s.visit(); err != nil {
		return filterExpr{}, err
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return filterExpr{}, errf("filter_ast value must be an object")
	}
	if colName := strArg(obj, "column"); colName != "" {
		return s.column(scope.table, scope.alias, colName, expected)
	}
	if colName := strArg(obj, "outer_column"); colName != "" {
		if scope.outerAlias == "" {
			return filterExpr{}, errf("outer_column is only valid inside exists")
		}
		return s.column(scope.outerTable, scope.outerAlias, colName, expected)
	}
	if literal, ok := obj["literal"]; ok {
		return filterExpr{sql: "?", args: []any{literal}, literal: true}, nil
	}
	if rawCast, ok := obj["cast"]; ok {
		m, ok := rawCast.(map[string]any)
		if !ok {
			return filterExpr{}, errf("cast must be an object")
		}
		typ := strArg(m, "type")
		if _, err := sqliteType(typ); err != nil && typ != "integer" {
			return filterExpr{}, errf("invalid cast type %q", typ)
		}
		e, err := s.value(m["expr"], scope, nil)
		if err != nil {
			return filterExpr{}, err
		}
		return filterExpr{sql: "CAST(" + e.sql + " AS " + strings.ToUpper(typ) + ")", args: e.args, typeName: typ, explicitCast: true}, nil
	}
	if rawCoal, ok := obj["coalesce"]; ok {
		arr, ok := rawCoal.([]any)
		if !ok || len(arr) == 0 || len(arr) > 16 {
			return filterExpr{}, errf("coalesce requires 1..16 values")
		}
		parts := make([]string, len(arr))
		var args []any
		for i, v := range arr {
			e, err := s.value(v, scope, expected)
			if err != nil {
				return filterExpr{}, err
			}
			parts[i] = e.sql
			args = append(args, e.args...)
		}
		return filterExpr{sql: "COALESCE(" + strings.Join(parts, ",") + ")", args: args}, nil
	}
	return filterExpr{}, errf("unknown filter_ast value expression")
}

func (s *filterCompileState) column(table *Table, alias, name string, _ *Column) (filterExpr, error) {
	if err := validateIdentifier("column", name); err != nil {
		return filterExpr{}, err
	}
	var col Column
	found := name == "id" || name == "_revision" || name == "created_at" || name == "updated_at"
	if found {
		col = Column{Name: name, Type: map[string]string{"id": "integer", "_revision": "integer", "created_at": "datetime", "updated_at": "datetime"}[name]}
	} else {
		for _, c := range table.Columns {
			if c.Name == name {
				col, found = c, true
				break
			}
		}
	}
	if !found {
		return filterExpr{}, errf("unknown column %q", name)
	}
	qualified := quote(alias) + "." + quote(name)
	if table.LegacyStorage && col.Type == "datetime" {
		qualified = "tables_datetime(" + qualified + ")"
	}
	return filterExpr{sql: qualified, typeName: col.Type, column: &col}, nil
}

func (s *filterCompileState) exists(raw any, scope filterScope, negate bool) (filterExpr, error) {
	if s.subqueries >= maxFilterSubqueries {
		return filterExpr{}, errf("filter_ast exceeds maximum of %d subqueries", maxFilterSubqueries)
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return filterExpr{}, errf("exists must be an object")
	}
	name := strArg(obj, "table")
	if err := validateIdentifier("table", name); err != nil {
		return filterExpr{}, err
	}
	inner, err := s.app.loadTableSchema(s.ctx.WithProject(s.projectID), s.projectID, name)
	if err != nil {
		return filterExpr{}, err
	}
	s.subqueries++
	alias := fmt.Sprintf("sub%d", s.aliasSeq)
	s.aliasSeq++
	innerScope := filterScope{table: inner, alias: alias, outerTable: scope.table, outerAlias: scope.alias}
	parts := []string{}
	var args []any
	if rawFilter := obj["filter"]; rawFilter != nil {
		e, err := s.node(rawFilter, innerScope)
		if err != nil {
			return filterExpr{}, err
		}
		parts = append(parts, e.sql)
		args = append(args, e.args...)
	}
	if rawFilter := obj["filter_ast"]; rawFilter != nil {
		e, err := s.node(rawFilter, innerScope)
		if err != nil {
			return filterExpr{}, err
		}
		parts = append(parts, e.sql)
		args = append(args, e.args...)
	}
	if rawFilter := obj["where"]; rawFilter != nil {
		if arr, ok := rawFilter.([]any); ok {
			clause, vals, err := buildWhere(inner, arr)
			if err != nil {
				return filterExpr{}, err
			}
			if clause != "" {
				parts = append(parts, strings.TrimPrefix(clause, "WHERE "))
				args = append(args, vals...)
			}
		}
	}
	correlations, _ := obj["correlation"].([]any)
	if len(correlations) > 32 {
		return filterExpr{}, errf("exists correlation exceeds maximum of 32 pairs")
	}
	for i, rawPair := range correlations {
		pair, ok := rawPair.(map[string]any)
		if !ok {
			return filterExpr{}, errf("correlation[%d] must be an object", i)
		}
		outerName := strArg(pair, "outer")
		innerName := strArg(pair, "inner")
		if outerName == "" || innerName == "" {
			return filterExpr{}, errf("correlation[%d] requires outer and inner columns", i)
		}
		outer, err := s.column(scope.table, scope.alias, outerName, nil)
		if err != nil {
			return filterExpr{}, err
		}
		in, err := s.column(inner, alias, innerName, nil)
		if err != nil {
			return filterExpr{}, err
		}
		parts = append(parts, in.sql+" = "+outer.sql)
		args = append(args, in.args...)
	}
	sql := "EXISTS (SELECT 1 FROM " + quote(inner.PhysicalName) + " AS " + quote(alias)
	if len(parts) > 0 {
		sql += " WHERE " + strings.Join(parts, " AND ")
	}
	sql += ")"
	if negate {
		sql = "NOT " + sql
	}
	return filterExpr{sql: sql, args: args}, nil
}

func escapeLike(v string) string {
	return strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(v)
}
