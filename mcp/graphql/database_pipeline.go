package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// database_pipeline answers a whole report from Database: an optional access
// phase resolves what the caller may read, every step then runs in ONE
// read-only db_batch (a single snapshot), and combine joins step rows and
// computes derived fields with resolver-module expressions. Collections,
// filters and metrics are resolver configuration; clients only supply values
// through declared params.
const databasePipelineOperation = "database_pipeline"

const (
	maxDatabasePipelineSteps   = 100 // Database's batch limit
	maxDatabasePipelineTimeout = 60000
)

type databasePipelineParam struct {
	From     string
	Type     string
	Required bool
	Value    any
	HasValue bool
}

// databasePipelineRead is an access-phase lookup: the distinct values of one
// field over the matching records (array fields are flattened). With limit it
// reads the first records in orderBy order instead of failing past max.
type databasePipelineRead struct {
	Collection string
	Where      any
	Field      string
	Max        int
	OrderBy    any
	Limit      int
	// Distinct reads the field's distinct values as a Database aggregate, so
	// max bounds the values rather than the matching records.
	Distinct bool
}

type databasePipelineAccessValue struct {
	Name                   string
	Requested              string // param holding the client's requested values
	UnrestrictedPermission string
	RequireAny             []string              // permissions, one of which the caller must hold
	Default                *databasePipelineRead // unrestricted caller, nothing requested
	Otherwise              *databasePipelineRead // restricted caller
	Single                 bool                  // restricted: exactly one distinct value
	SingleRecord           bool                  // restricted: exactly one matching record
	Union                  []string              // derived from earlier values, no read
	Intersect              []string
	Expr                   any  // derived: a server-owned expression returning a list
	Optional               bool // may resolve to an empty list instead of refusing
	Exact                  bool // type exact_string_list: identifiers stay as stored
	deps                   []string
}

func (value databasePipelineAccessValue) derived() bool {
	return value.Union != nil || value.Intersect != nil || value.Expr != nil
}

// accessList collects an access value's identifiers without duplicates. By
// default they are trimmed and blanks dropped. An exact list keeps them as
// stored, so [""] stays distinct from [] and " a " from "a".
type accessList struct {
	exact  bool
	values []string
	seen   map[string]bool
}

func newAccessList(exact bool) *accessList {
	return &accessList{exact: exact, values: []string{}, seen: map[string]bool{}}
}

func (list *accessList) add(item string) {
	if !list.exact {
		if item = strings.TrimSpace(item); item == "" {
			return
		}
	}
	if !list.seen[item] {
		list.seen[item] = true
		list.values = append(list.values, item)
	}
}

// Access values form a dependency graph: a read may filter on an earlier
// value ($scope.<name>); union, intersect and expr derive values in memory.
// Each level of reads is one read batch, so chains are bounded; derived values
// run after the reads they need and do not add a level.
const maxDatabasePipelineAccessLevels = 4

type databasePipelineStep struct {
	ID      string
	Op      string
	Args    map[string]any
	MaxRows int
	TopN    bool            // find: max_rows is the wanted size, not a bound
	Metrics map[string]bool // aggregate metrics with integer results
	Page    *databasePipelinePage
	CountOf string // count: the find step whose collection and filter it counts
}

// databasePipelinePage pages a find step: size is a number or a number
// $param (bounded by max_rows), cursor a string $param holding Database's
// nextCursor. A paged step returns its page instead of failing on hasMore.
type databasePipelinePage struct {
	Size   any
	Cursor string
}

type databasePipelineCombine struct {
	ID         string
	Join       []string
	On         []string
	Missing    string
	TotalKey   string
	TotalSteps map[string]string
	Fields     map[string]any
	FieldOrder []string
	Keep       []string // joined steps whose columns are copied unprefixed
}

type databasePipelinePlan struct {
	Database     string
	Scope        string
	Params       map[string]databasePipelineParam
	RequireAny   []string
	Access       []databasePipelineAccessValue
	AccessLevels [][]int // indexes into Access, in dependency order
	Steps        []databasePipelineStep
	Combines     []databasePipelineCombine
	Result       map[string]string
	ResultRoot   string // a single reference returned as the field value
	TimeoutMS    int
	stepIndex    map[string]int
	accessIndex  map[string]bool
	combineIndex map[string]bool
}

var databasePipelineStepArgs = map[string]map[string]bool{
	"aggregate": {"collection": true, "where": true, "groupBy": true, "metrics": true, "lookups": true, "buckets": true, "orderBy": true},
	"find":      {"collection": true, "where": true, "select": true, "orderBy": true, "lookups": true},
	"count":     {"collection": true, "where": true},
	"get":       {"collection": true, "key": true},
}

func validateDatabasePipeline(config map[string]any) (*databasePipelinePlan, error) {
	for key := range config {
		switch key {
		case "database", "scope", "params", "access", "steps", "combine", "result", "timeout_ms", "watch_collections",
			"collection", "read_only", // source-level settings; steps name their own collections
			"_project_id", "_source_id", "graphql_args", "parent", "kind", "description":
		default:
			return nil, invalid("database_pipeline does not accept %q", key)
		}
	}
	plan := &databasePipelinePlan{
		Database:     stringValue(config["database"], "default"),
		Scope:        stringValue(config["scope"], "project"),
		Params:       map[string]databasePipelineParam{},
		Result:       map[string]string{},
		TimeoutMS:    15000,
		stepIndex:    map[string]int{},
		accessIndex:  map[string]bool{},
		combineIndex: map[string]bool{},
	}
	if raw, ok := config["timeout_ms"]; ok {
		var integral bool
		plan.TimeoutMS, integral = strictInt(raw)
		if !integral || plan.TimeoutMS < 1 || plan.TimeoutMS > maxDatabasePipelineTimeout {
			return nil, invalid("database_pipeline timeout_ms must be between 1 and %d", maxDatabasePipelineTimeout)
		}
	}
	if err := plan.parseParams(config["params"]); err != nil {
		return nil, err
	}
	if err := plan.parseAccess(config["access"]); err != nil {
		return nil, err
	}
	if err := plan.parseSteps(config["steps"]); err != nil {
		return nil, err
	}
	if err := plan.parseCombine(config["combine"]); err != nil {
		return nil, err
	}
	if err := plan.parseResult(config["result"]); err != nil {
		return nil, err
	}
	return plan, nil
}

func (plan *databasePipelinePlan) parseParams(raw any) error {
	if raw == nil {
		return nil
	}
	params, ok := raw.(map[string]any)
	if !ok || len(params) > 200 {
		return invalid("database_pipeline params must be an object of at most 200 entries")
	}
	for name, item := range params {
		if !graphqlName(name) {
			return invalid("database_pipeline param name %q is invalid", name)
		}
		spec, ok := item.(map[string]any)
		if !ok {
			return invalid("database_pipeline param %s must be an object", name)
		}
		if err := onlyKeys(spec, "database_pipeline param "+name, "type", "required", "from", "value"); err != nil {
			return err
		}
		if raw, exists := spec["type"]; exists && !nonEmptyString(raw) {
			return invalid("database_pipeline param %s type must be a string", name)
		}
		p := databasePipelineParam{Type: stringValue(spec["type"], "string")}
		switch p.Type {
		case "string", "number", "boolean", "datetime", "json", "string_list", "exact_string_list":
		default:
			return invalid("database_pipeline param %s type must be string, number, boolean, datetime, json, string_list or exact_string_list", name)
		}
		if required, exists := spec["required"]; exists {
			value, ok := required.(bool)
			if !ok {
				return invalid("database_pipeline param %s required must be Boolean", name)
			}
			p.Required = value
		}
		from, hasFrom := spec["from"].(string)
		value, hasValue := spec["value"]
		if _, exists := spec["from"]; exists && !hasFrom {
			return invalid("database_pipeline param %s from must be a string", name)
		}
		if hasFrom == hasValue {
			return invalid("database_pipeline param %s requires exactly one of from or value", name)
		}
		if hasFrom && from == "$identity.permissions" && p.Type != "string_list" {
			return invalid("database_pipeline param %s: $identity.permissions is a string_list", name)
		}
		if hasFrom && from != "$identity.permissions" && !validAggregatePipelinePath(from) {
			return invalid("database_pipeline param %s has an invalid source path", name)
		}
		p.From, p.Value, p.HasValue = from, value, hasValue
		plan.Params[name] = p
	}
	return nil
}

func (plan *databasePipelinePlan) parseRead(raw any, label string) (*databasePipelineRead, error) {
	spec, ok := raw.(map[string]any)
	if !ok {
		return nil, invalid("database_pipeline access %s must be an object", label)
	}
	if err := onlyKeys(spec, "database_pipeline access "+label, "collection", "field", "max", "where", "orderBy", "limit", "distinct"); err != nil {
		return nil, err
	}
	// One extra record detects overflow, within Database's 1000-record page.
	read := &databasePipelineRead{Max: 999}
	read.Collection, _ = spec["collection"].(string)
	read.Field, _ = spec["field"].(string)
	if strings.TrimSpace(read.Collection) == "" || strings.TrimSpace(read.Field) == "" || strings.HasPrefix(read.Collection, "$") || strings.HasPrefix(read.Field, "$") {
		return nil, invalid("database_pipeline access %s requires a literal collection and field", label)
	}
	if raw, ok := spec["max"]; ok {
		var integral bool
		read.Max, integral = strictInt(raw)
		if !integral || read.Max < 1 || read.Max > 999 {
			return nil, invalid("database_pipeline access %s max must be between 1 and 999", label)
		}
	}
	if raw, ok := spec["limit"]; ok {
		var integral bool
		read.Limit, integral = strictInt(raw)
		if !integral || read.Limit < 1 || read.Limit > 999 {
			return nil, invalid("database_pipeline access %s limit must be between 1 and 999", label)
		}
		if _, ok := spec["max"]; ok {
			return nil, invalid("database_pipeline access %s takes max or limit, not both", label)
		}
		if spec["orderBy"] == nil {
			return nil, invalid("database_pipeline access %s limit requires orderBy, so the records kept are deterministic", label)
		}
	}
	if raw, ok := spec["orderBy"]; ok {
		if err := validateDatabaseOrderBy(raw, "database_pipeline access "+label+" orderBy"); err != nil {
			return nil, err
		}
		read.OrderBy = raw
	}
	if raw, ok := spec["distinct"]; ok {
		if read.Distinct, ok = raw.(bool); !ok {
			return nil, invalid("database_pipeline access %s distinct must be a boolean", label)
		}
		if read.Distinct && (spec["orderBy"] != nil || spec["limit"] != nil) {
			return nil, invalid("database_pipeline access %s distinct does not take orderBy or limit", label)
		}
	}
	if where, ok := spec["where"]; ok {
		if filterHasOptional(where) {
			return nil, invalid("database_pipeline access %s cannot use a conditional filter: access reads are resolved before filters are selected", label)
		}
		if filterHasRelation(where) {
			return nil, invalid("database_pipeline access %s cannot use exists or not_exists; put the relationship in the step's filter", label)
		}
		if err := plan.validateFilter(where, true, "access "+label+".where"); err != nil {
			return nil, err
		}
		if filterHasSearch(where) {
			return nil, invalid("database_pipeline access %s cannot use %s: a search narrows results, it never decides authorization", label, databaseSearchOp)
		}
		read.Where = where
	}
	return read, nil
}

// accessExpressionOps are the expression ops an expr access value may use:
// logic, comparisons, membership, length and coalesce. Nothing that calls out,
// formats or computes dates.
var accessExpressionOps = map[string]bool{"if": true, "and": true, "or": true, "not": true, "in": true, "length": true,
	"eq": true, "neq": true, "lt": true, "lte": true, "gt": true, "gte": true, "coalesce": true}

// validateAccessExpression accepts $scope.<value> (another access value) and
// $param.<name> where the param is verified identity or a fixed value, never
// client input: an authorization decision must not take a client flag.
func (plan *databasePipelinePlan) validateAccessExpression(raw any, self string, deps map[string]bool, depth int) error {
	if depth > 32 {
		return errors.New("expression is too deep")
	}
	ref, isRef := raw.(string)
	node, isNode := raw.(map[string]any)
	if isNode {
		if input, ok := node["input"].(string); ok && len(node) == 1 {
			ref, isRef = input, true
		}
	}
	if isRef {
		if name, ok := strings.CutPrefix(ref, "$scope."); ok {
			if !plan.accessIndex[name] || name == self {
				return fmt.Errorf("references unknown access value %q", ref)
			}
			deps[name] = true
			return nil
		}
		if name, ok := strings.CutPrefix(ref, "$param."); ok {
			p, declared := plan.Params[name]
			if !declared {
				return fmt.Errorf("references undeclared %q", ref)
			}
			if !p.HasValue && !strings.HasPrefix(p.From, "$identity.") {
				return fmt.Errorf("%q comes from client input; use $identity params or fixed values", ref)
			}
			return nil
		}
		return fmt.Errorf("input %q must be $scope.<value> or $param.<name>", ref)
	}
	if !isNode {
		return errors.New("must be a reference or an expression object")
	}
	if _, ok := node["const"]; ok {
		if len(node) != 1 {
			return errors.New("const takes no other keys")
		}
		return nil
	}
	op, _ := node["op"].(string)
	if !accessExpressionOps[op] {
		return fmt.Errorf("unsupported op %q", op)
	}
	allowed := []string{"op", "args"}
	if op == "if" {
		allowed = []string{"op", "condition", "then", "else"}
	}
	for key := range node {
		if !contains(allowed, key) {
			return fmt.Errorf("%s does not accept %q", op, key)
		}
	}
	if op == "if" {
		for _, key := range []string{"condition", "then", "else"} {
			if _, ok := node[key]; !ok {
				return fmt.Errorf("if requires %s", key)
			}
			if err := plan.validateAccessExpression(node[key], self, deps, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	args, ok := node["args"].([]any)
	if !ok || len(args) == 0 {
		return fmt.Errorf("%s requires args", op)
	}
	for _, arg := range args {
		if err := plan.validateAccessExpression(arg, self, deps, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// scopeRefs lists the access values a configuration fragment references.
func scopeRefs(raw any, out map[string]bool) {
	switch value := raw.(type) {
	case string:
		if name, ok := strings.CutPrefix(value, "$scope."); ok {
			out[name] = true
		}
	case []any:
		for _, item := range value {
			scopeRefs(item, out)
		}
	case map[string]any:
		for _, item := range value {
			scopeRefs(item, out)
		}
	}
}

func (plan *databasePipelinePlan) parseAccess(raw any) error {
	if raw == nil {
		return nil
	}
	access, ok := raw.(map[string]any)
	if !ok {
		return invalid("database_pipeline access must be an object")
	}
	if err := onlyKeys(access, "database_pipeline access", "require_any_permission", "values"); err != nil {
		return err
	}
	if list, exists := access["require_any_permission"]; exists {
		values, err := pipelineStringList(list)
		if err != nil || len(values) == 0 {
			return invalid("database_pipeline require_any_permission must be a non-empty list of strings")
		}
		plan.RequireAny = values
	}
	values, _ := access["values"].(map[string]any)
	if access["values"] != nil && values == nil {
		return invalid("database_pipeline access values must be an object")
	}
	names := make([]string, 0, len(values))
	for name := range values {
		if !graphqlName(name) {
			return invalid("database_pipeline access value name %q is invalid", name)
		}
		names = append(names, name)
		plan.accessIndex[name] = true // values may reference each other
	}
	sort.Strings(names)
	for _, name := range names {
		spec, ok := values[name].(map[string]any)
		if !ok {
			return invalid("database_pipeline access value %s must be an object", name)
		}
		if err := onlyKeys(spec, "database_pipeline access value "+name, "requested", "unrestricted_if_permission", "require_any_permission", "single", "single_record", "default", "otherwise", "union", "intersect", "expr", "optional", "type"); err != nil {
			return err
		}
		for _, key := range []string{"requested", "unrestricted_if_permission"} {
			if raw, exists := spec[key]; exists && !nonEmptyString(raw) {
				return invalid("database_pipeline access value %s %s must be a non-empty string", name, key)
			}
		}
		value := databasePipelineAccessValue{Name: name}
		requestedType := "string_list"
		if raw, exists := spec["type"]; exists {
			switch raw {
			case "string_list":
			case "exact_string_list":
				value.Exact, requestedType = true, "exact_string_list"
			default:
				return invalid("database_pipeline access value %s type must be string_list or exact_string_list", name)
			}
		}
		value.Requested, _ = spec["requested"].(string)
		if value.Requested != "" {
			p, ok := plan.Params[strings.TrimPrefix(value.Requested, "$param.")]
			if !strings.HasPrefix(value.Requested, "$param.") || !ok || p.Type != requestedType {
				return invalid("database_pipeline access value %s requested must name a %s $param", name, requestedType)
			}
		}
		value.UnrestrictedPermission, _ = spec["unrestricted_if_permission"].(string)
		if list, exists := spec["require_any_permission"]; exists {
			permissions, err := pipelineStringList(list)
			if err != nil || len(permissions) == 0 {
				return invalid("database_pipeline access value %s require_any_permission must be a non-empty list of strings", name)
			}
			value.RequireAny = permissions
		}
		for key, target := range map[string]*bool{"single": &value.Single, "single_record": &value.SingleRecord, "optional": &value.Optional} {
			if raw, exists := spec[key]; exists {
				if *target, ok = raw.(bool); !ok {
					return invalid("database_pipeline access value %s %s must be Boolean", name, key)
				}
			}
		}
		deps := map[string]bool{}
		_, hasUnion := spec["union"]
		_, hasIntersect := spec["intersect"]
		if expr, hasExpr := spec["expr"]; hasExpr {
			if hasUnion || hasIntersect || spec["default"] != nil || spec["otherwise"] != nil || value.UnrestrictedPermission != "" || value.Requested != "" || value.Single || value.SingleRecord {
				return invalid("database_pipeline access value %s: expr stands alone (no reads, set operations, requested, unrestricted_if_permission or single assertions)", name)
			}
			if err := plan.validateAccessExpression(expr, name, deps, 0); err != nil {
				return invalid("database_pipeline access value %s expr: %s", name, err)
			}
			value.Expr = expr
		} else if hasUnion || hasIntersect {
			if hasUnion == hasIntersect || spec["default"] != nil || spec["otherwise"] != nil || value.UnrestrictedPermission != "" || value.Requested != "" {
				return invalid("database_pipeline access value %s: union or intersect stands alone (no reads, requested or unrestricted_if_permission)", name)
			}
			key, target := "union", &value.Union
			if hasIntersect {
				key, target = "intersect", &value.Intersect
			}
			list, err := pipelineStringList(spec[key])
			if err != nil || len(list) < 2 {
				return invalid("database_pipeline access value %s %s must list at least two access values", name, key)
			}
			for _, ref := range list {
				if !plan.accessIndex[ref] || ref == name {
					return invalid("database_pipeline access value %s %s references unknown %q", name, key, ref)
				}
				deps[ref] = true
			}
			*target = list
		} else {
			var err error
			if spec["default"] != nil {
				if value.Default, err = plan.parseRead(spec["default"], name+".default"); err != nil {
					return err
				}
				scopeRefs(value.Default.Where, deps)
			}
			if spec["otherwise"] != nil {
				if value.Otherwise, err = plan.parseRead(spec["otherwise"], name+".otherwise"); err != nil {
					return err
				}
				scopeRefs(value.Otherwise.Where, deps)
			}
			if value.Otherwise == nil && value.UnrestrictedPermission == "" {
				return invalid("database_pipeline access value %s needs otherwise or unrestricted_if_permission", name)
			}
			if value.Default != nil && value.UnrestrictedPermission == "" {
				return invalid("database_pipeline access value %s default applies only with unrestricted_if_permission", name)
			}
			if value.SingleRecord && (value.Otherwise == nil || value.Otherwise.Limit > 0) {
				return invalid("database_pipeline access value %s single_record needs an otherwise read without limit", name)
			}
			if value.SingleRecord && value.Otherwise.Distinct {
				return invalid("database_pipeline access value %s single_record counts records, so its otherwise read cannot be distinct", name)
			}
		}
		if deps[name] {
			return invalid("database_pipeline access value %s references itself", name)
		}
		for dep := range deps {
			value.deps = append(value.deps, dep)
		}
		sort.Strings(value.deps)
		plan.Access = append(plan.Access, value)
	}
	// A normalised value derived from an exact one would normalise it again,
	// which can turn [""] into [] and select a fallback. A normalised read may
	// still filter on an exact value: filter values aren't changed.
	exact := map[string]bool{}
	for _, value := range plan.Access {
		exact[value.Name] = value.Exact
	}
	for _, value := range plan.Access {
		if !value.derived() || value.Exact {
			continue
		}
		for _, dep := range value.deps {
			if exact[dep] {
				return invalid("database_pipeline access value %s reads exact value %s, so it must also be type exact_string_list", value.Name, dep)
			}
		}
	}
	return plan.orderAccess()
}

// orderAccess groups access values into dependency levels, rejecting cycles
// and chains deeper than maxDatabasePipelineAccessLevels.
func (plan *databasePipelinePlan) orderAccess() error {
	index := make(map[string]int, len(plan.Access))
	for i, value := range plan.Access {
		index[value.Name] = i
	}
	// level counts read batches: a read runs one level after what it filters
	// on; a derived value runs in the level of its deepest dependency, after
	// that level's reads. topo is a dependency-first order for evaluation.
	level := make([]int, len(plan.Access))
	state := make([]int, len(plan.Access)) // 0 new, 1 visiting, 2 done
	topo := make([]int, 0, len(plan.Access))
	var visit func(int) error
	visit = func(i int) error {
		switch state[i] {
		case 1:
			return invalid("database_pipeline access values have a cycle through %s", plan.Access[i].Name)
		case 2:
			return nil
		}
		state[i] = 1
		step := 1
		if plan.Access[i].derived() {
			step = 0
		}
		for _, dep := range plan.Access[i].deps {
			j := index[dep]
			if err := visit(j); err != nil {
				return err
			}
			if level[j]+step > level[i] {
				level[i] = level[j] + step
			}
		}
		state[i] = 2
		topo = append(topo, i)
		return nil
	}
	for i := range plan.Access {
		if err := visit(i); err != nil {
			return err
		}
		if level[i] >= maxDatabasePipelineAccessLevels {
			return invalid("database_pipeline access chain through %s needs more than %d read levels", plan.Access[i].Name, maxDatabasePipelineAccessLevels)
		}
	}
	// Within a level, reads keep name order (plan.Access is sorted) and
	// derived values follow in dependency order.
	plan.AccessLevels = nil
	place := func(i int) {
		for len(plan.AccessLevels) <= level[i] {
			plan.AccessLevels = append(plan.AccessLevels, nil)
		}
		plan.AccessLevels[level[i]] = append(plan.AccessLevels[level[i]], i)
	}
	for i := range plan.Access {
		if !plan.Access[i].derived() {
			place(i)
		}
	}
	for _, i := range topo {
		if plan.Access[i].derived() {
			place(i)
		}
	}
	return nil
}

// validateFilter accepts the Database filter language: {field,op,value} leaves
// and {and:[...]}/{or:[...]} groups. Placeholders are permitted only as values.
func (plan *databasePipelinePlan) validateFilter(raw any, allowScope bool, path string) error {
	node, ok := raw.(map[string]any)
	if !ok {
		return invalid("database_pipeline %s must be a filter object", path)
	}
	for _, group := range []string{"and", "or"} {
		if children, exists := node[group]; exists {
			list, ok := children.([]any)
			if !ok || len(node) != 1 {
				return invalid("database_pipeline %s.%s must be the only key and a list", path, group)
			}
			for i, child := range list {
				childPath := fmt.Sprintf("%s.%s[%d]", path, group, i)
				if group == "or" && filterHasOptional(child) {
					return invalid("database_pipeline %s cannot hold a conditional filter inside or; removing a branch of an or would change what the others match", childPath)
				}
				if group == "and" && isAccessWhenFilter(child) {
					if err := plan.validateAccessWhen(child.(map[string]any), childPath); err != nil {
						return err
					}
					continue
				}
				if group == "and" && isConditionalFilter(child) {
					if err := plan.validateConditional(child.(map[string]any), childPath); err != nil {
						return err
					}
					continue
				}
				if err := plan.validateFilter(child, allowScope, childPath); err != nil {
					return err
				}
				if group == "or" && filterHasSearch(child) {
					return invalid("database_pipeline %s: %s cannot be inside or; an empty search matches every record, so it must be ANDed with the other filters", path, databaseSearchOp)
				}
			}
			return nil
		}
	}
	if isConditionalFilter(node) || isAccessWhenFilter(node) {
		return invalid("database_pipeline %s: a conditional filter must be a child of an and group", path)
	}
	if isRelationFilter(node) {
		return plan.validateRelation(node, allowScope, path)
	}
	if _, hasFields := node["fields"]; hasFields || node["op"] == databaseSearchOp {
		return plan.validateSearch(node, path)
	}
	for key := range node {
		if key != "field" && key != "op" && key != "value" {
			return invalid("database_pipeline %s has unknown key %q", path, key)
		}
	}
	if field, _ := node["field"].(string); strings.TrimSpace(field) == "" || strings.HasPrefix(field, "$") {
		return invalid("database_pipeline %s requires a literal field", path)
	}
	op, _ := node["op"].(string)
	if strings.TrimSpace(op) == "" {
		return invalid("database_pipeline %s requires op", path)
	}
	if !databaseFilterOps[op] {
		return invalid("database_pipeline %s has unsupported op %q", path, op)
	}
	return plan.validatePlaceholders(node["value"], allowScope, path+".value")
}

// databaseFilterOps are the field operators Database accepts; like_contains_ascii
// has its own node shape.
var databaseFilterOps = map[string]bool{"eq": true, "ne": true, "gt": true, "gte": true, "lt": true, "lte": true,
	"in": true, "not_in": true, "starts_with": true, "is_null": true, "is_not_null": true}

const (
	databaseSearchOp        = "like_contains_ascii"
	maxDatabaseSearchFields = 16
	maxDatabaseSearchValue  = 1024
)

// validateSearch checks a {fields, op, value} text search. Fields and op are
// resolver configuration; only the value may come from the client, and only as
// a top-level string argument.
func (plan *databasePipelinePlan) validateSearch(node map[string]any, path string) error {
	for key := range node {
		if key != "fields" && key != "op" && key != "value" {
			return invalid("database_pipeline %s has unknown key %q", path, key)
		}
	}
	if node["op"] != databaseSearchOp {
		return invalid("database_pipeline %s: fields requires op %s", path, databaseSearchOp)
	}
	fields, ok := node["fields"].([]any)
	if !ok || len(fields) == 0 || len(fields) > maxDatabaseSearchFields {
		return invalid("database_pipeline %s: %s requires 1 to %d fields", path, databaseSearchOp, maxDatabaseSearchFields)
	}
	for _, raw := range fields {
		if field, _ := raw.(string); strings.TrimSpace(field) == "" || strings.HasPrefix(field, "$") {
			return invalid("database_pipeline %s: %s fields must be literal names", path, databaseSearchOp)
		}
	}
	value, ok := node["value"].(string)
	if !ok {
		return invalid("database_pipeline %s: %s requires a string value", path, databaseSearchOp)
	}
	name, isParam := strings.CutPrefix(value, "$param.")
	if !isParam {
		if strings.HasPrefix(value, "$") {
			return invalid("database_pipeline %s: %s value must be a string or a string $param", path, databaseSearchOp)
		}
		if len(value) > maxDatabaseSearchValue {
			return invalid("database_pipeline %s: %s value is limited to %d bytes", path, databaseSearchOp, maxDatabaseSearchValue)
		}
		return nil
	}
	param, ok := plan.Params[name]
	if !ok {
		return invalid("database_pipeline %s references unknown %s", path, value)
	}
	if param.Type != "string" {
		return invalid("database_pipeline %s: %s must be a string param", path, value)
	}
	if param.HasValue {
		if fixed, isString := param.Value.(string); !isString || len(fixed) > maxDatabaseSearchValue {
			return invalid("database_pipeline %s: %s must be a string of at most %d bytes", path, value, maxDatabaseSearchValue)
		}
		return nil
	}
	if argument, ok := strings.CutPrefix(param.From, "$args."); !ok || !graphqlName(argument) {
		return invalid("database_pipeline %s: %s must come from a top-level $args value or a fixed value", path, value)
	}
	return nil
}

// filterHasSearch reports whether a filter contains a text search.
func filterHasSearch(raw any) bool {
	return len(filterSearches(raw)) > 0
}

// filterSearches lists the text-search leaves of a filter.
func filterSearches(raw any) []map[string]any {
	node, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	if isConditionalFilter(node) || isAccessWhenFilter(node) {
		return filterSearches(node["filter"])
	}
	found := []map[string]any{}
	for _, group := range []string{"and", "or"} {
		for _, child := range asAnyList(node[group]) {
			found = append(found, filterSearches(child)...)
		}
	}
	if _, hasFields := node["fields"]; hasFields || node["op"] == databaseSearchOp {
		found = append(found, node)
	}
	return found
}

// searchArguments lists the GraphQL arguments that supply text-search values.
func (plan *databasePipelinePlan) searchArguments() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, step := range plan.Steps {
		for _, search := range step.searches() {
			value, _ := search["value"].(string)
			name, isParam := strings.CutPrefix(value, "$param.")
			if !isParam {
				continue
			}
			if argument, ok := strings.CutPrefix(plan.Params[name].From, "$args."); ok && !seen[argument] {
				seen[argument] = true
				out = append(out, argument)
			}
		}
	}
	sort.Strings(out)
	return out
}

// searches lists a step's text searches, in where and in metric filters.
func (step databasePipelineStep) searches() []map[string]any {
	found := filterSearches(step.Args["where"])
	for _, raw := range asAnyList(step.Args["metrics"]) {
		metric, _ := raw.(map[string]any)
		found = append(found, filterSearches(metric["where"])...)
	}
	return found
}

func (plan *databasePipelinePlan) validatePlaceholders(raw any, allowScope bool, path string) error {
	switch value := raw.(type) {
	case string:
		if !strings.HasPrefix(value, "$") {
			return nil
		}
		switch {
		case strings.HasPrefix(value, "$param."):
			if _, ok := plan.Params[strings.TrimPrefix(value, "$param.")]; !ok {
				return invalid("database_pipeline %s references unknown %s", path, value)
			}
		case strings.HasPrefix(value, "$scope.") && allowScope:
			if !plan.accessIndex[strings.TrimPrefix(value, "$scope.")] {
				return invalid("database_pipeline %s references unknown %s", path, value)
			}
		default:
			return invalid("database_pipeline %s: values starting with $ must be $param.<name> or (in steps) $scope.<name>", path)
		}
	case []any:
		for i, item := range value {
			if err := plan.validatePlaceholders(item, allowScope, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, item := range value {
			if err := plan.validatePlaceholders(item, allowScope, path+"."+key); err != nil {
				return err
			}
		}
	}
	return nil
}

// noPlaceholders rejects $-strings outside filter values and get keys, so a
// client can never choose a field, metric, lookup or collection.
func noPlaceholders(raw any, path string) error {
	switch value := raw.(type) {
	case string:
		if strings.HasPrefix(value, "$") {
			return invalid("database_pipeline %s: placeholders are only allowed in filter values and get keys", path)
		}
	case []any:
		for i, item := range value {
			if err := noPlaceholders(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, item := range value {
			if err := noPlaceholders(item, path+"."+key); err != nil {
				return err
			}
		}
	}
	return nil
}

func (plan *databasePipelinePlan) parseSteps(raw any) error {
	list, ok := raw.([]any)
	if !ok || len(list) == 0 || len(list) > maxDatabasePipelineSteps {
		return invalid("database_pipeline requires 1 to %d steps", maxDatabasePipelineSteps)
	}
	for i, item := range list {
		spec, ok := item.(map[string]any)
		if !ok {
			return invalid("database_pipeline step %d must be an object", i)
		}
		if err := onlyKeys(spec, fmt.Sprintf("database_pipeline step %d", i), "id", "op", "args", "max_rows", "top_n", "page", "count_of"); err != nil {
			return err
		}
		step := databasePipelineStep{Metrics: map[string]bool{}}
		step.ID, _ = spec["id"].(string)
		step.Op, _ = spec["op"].(string)
		if !graphqlName(step.ID) || strings.Contains(step.ID, "__") {
			return invalid("database_pipeline step %d id must be a GraphQL name without __", i)
		}
		if _, dup := plan.stepIndex[step.ID]; dup {
			return invalid("database_pipeline step id %s is duplicated", step.ID)
		}
		allowed, ok := databasePipelineStepArgs[step.Op]
		if !ok {
			return invalid("database_pipeline step %s op must be aggregate, find, count or get", step.ID)
		}
		args, _ := spec["args"].(map[string]any)
		if raw, exists := spec["count_of"]; exists {
			// Counts the same collection and filter as a find step, in the
			// same snapshot, for "page N of total".
			step.CountOf, _ = raw.(string)
			if step.Op != "count" || step.CountOf == "" || spec["args"] != nil {
				return invalid("database_pipeline step %s count_of applies to count steps without args", step.ID)
			}
			args = map[string]any{}
		} else if args == nil {
			return invalid("database_pipeline step %s requires args", step.ID)
		}
		for key, value := range args {
			if !allowed[key] {
				return invalid("database_pipeline step %s (%s) does not accept %q", step.ID, step.Op, key)
			}
			switch key {
			case "where":
				if err := plan.validateFilter(value, true, "step "+step.ID+".where"); err != nil {
					return err
				}
				relations := 0
				walkFilter(value, func(node map[string]any) {
					if isRelationFilter(node) {
						relations++
					}
				})
				if relations > maxRelationNodes {
					return invalid("database_pipeline step %s.where holds %d exists or not_exists filters; Database takes at most %d", step.ID, relations, maxRelationNodes)
				}
				if depth := relationDepth(value); depth > maxRelationDepth {
					return invalid("database_pipeline step %s.where nests exists and not_exists %d levels deep; Database takes at most %d", step.ID, depth, maxRelationDepth)
				}
			case "key":
				if err := plan.validatePlaceholders(value, true, "step "+step.ID+".key"); err != nil {
					return err
				}
			case "lookups":
				if err := validateDatabaseLookups(value, "database_pipeline step "+step.ID+" lookups"); err != nil {
					return err
				}
			case "buckets":
				if err := validateDatabaseBuckets(value, "database_pipeline step "+step.ID+" buckets"); err != nil {
					return err
				}
			case "metrics":
				metrics, _ := value.([]any)
				if len(metrics) == 0 {
					return invalid("database_pipeline step %s metrics must be a non-empty list", step.ID)
				}
				for _, rawMetric := range metrics {
					metric, ok := rawMetric.(map[string]any)
					if !ok {
						return invalid("database_pipeline step %s metrics must be objects", step.ID)
					}
					name, _ := metric["name"].(string)
					if name == "" {
						return invalid("database_pipeline step %s metric requires a name", step.ID)
					}
					if err := onlyKeys(metric, "database_pipeline step "+step.ID+" metric "+name, "name", "op", "field", "where"); err != nil {
						return err
					}
					switch metric["op"] {
					case "count", "count_distinct", "sum", "avg", "min", "max":
					default:
						return invalid("database_pipeline step %s metric %s op must be count, count_distinct, sum, avg, min or max", step.ID, name)
					}
					for metricKey, metricValue := range metric {
						var err error
						if metricKey == "where" {
							// Metric filters are Database filters: they may take values.
							if filterHasRelation(metricValue) {
								return invalid("database_pipeline step %s metric %s: Database does not accept exists or not_exists in a metric filter", step.ID, name)
							}
							err = plan.validateFilter(metricValue, true, "step "+step.ID+".metric "+name+".where")
						} else {
							err = noPlaceholders(metricValue, "step "+step.ID+".metric "+name+"."+metricKey)
						}
						if err != nil {
							return err
						}
					}
					switch metric["op"] {
					case "count", "count_distinct", "sum":
						step.Metrics[name] = true
					}
				}
			default:
				if err := noPlaceholders(value, "step "+step.ID+"."+key); err != nil {
					return err
				}
			}
		}
		if collection, _ := args["collection"].(string); strings.TrimSpace(collection) == "" && step.CountOf == "" {
			return invalid("database_pipeline step %s requires a collection", step.ID)
		}
		if step.Op == "get" && args["key"] == nil {
			return invalid("database_pipeline step %s get requires key", step.ID)
		}
		if step.Op == "aggregate" && args["metrics"] == nil {
			return invalid("database_pipeline step %s aggregate requires metrics", step.ID)
		}
		step.MaxRows = 1000
		integral := true
		if raw, exists := spec["max_rows"]; exists {
			step.MaxRows, integral = strictInt(raw)
		}
		if !integral || step.MaxRows < 1 || step.MaxRows > 1000 {
			return invalid("database_pipeline step %s max_rows must be between 1 and 1000", step.ID)
		}
		if raw, exists := spec["top_n"]; exists {
			topN, ok := raw.(bool)
			if !ok || step.Op != "find" {
				return invalid("database_pipeline step %s top_n applies to find steps and must be Boolean", step.ID)
			}
			step.TopN = topN
		}
		if raw, exists := spec["page"]; exists {
			if err := plan.parsePage(&step, raw); err != nil {
				return err
			}
		}
		step.Args = args
		plan.stepIndex[step.ID] = len(plan.Steps)
		plan.Steps = append(plan.Steps, step)
	}
	for i, step := range plan.Steps {
		if step.CountOf == "" {
			continue
		}
		index, ok := plan.stepIndex[step.CountOf]
		if !ok || plan.Steps[index].Op != "find" {
			return invalid("database_pipeline step %s count_of must name a find step", step.ID)
		}
		target := plan.Steps[index].Args
		plan.Steps[i].Args = map[string]any{"collection": target["collection"]}
		if target["where"] != nil {
			plan.Steps[i].Args["where"] = target["where"]
		}
	}
	return nil
}

func (plan *databasePipelinePlan) parsePage(step *databasePipelineStep, raw any) error {
	spec, ok := raw.(map[string]any)
	if !ok || step.Op != "find" || step.TopN {
		return invalid("database_pipeline step %s page applies to find steps without top_n and must be an object", step.ID)
	}
	if err := onlyKeys(spec, "database_pipeline step "+step.ID+" page", "size", "cursor"); err != nil {
		return err
	}
	page := &databasePipelinePage{Size: step.MaxRows}
	switch size := spec["size"].(type) {
	case nil:
	case string:
		p, ok := plan.Params[strings.TrimPrefix(size, "$param.")]
		if !strings.HasPrefix(size, "$param.") || !ok || p.Type != "number" {
			return invalid("database_pipeline step %s page size must be an integer or a number $param", step.ID)
		}
		page.Size = size
	default:
		value, integral := strictInt(size)
		if !integral || value < 1 || value > step.MaxRows {
			return invalid("database_pipeline step %s page size must be between 1 and max_rows %d", step.ID, step.MaxRows)
		}
		page.Size = value
	}
	if raw, exists := spec["cursor"]; exists {
		cursor, _ := raw.(string)
		p, ok := plan.Params[strings.TrimPrefix(cursor, "$param.")]
		if !strings.HasPrefix(cursor, "$param.") || !ok || p.Type != "string" {
			return invalid("database_pipeline step %s page cursor must be a string $param", step.ID)
		}
		page.Cursor = cursor
	}
	step.Page = page
	return nil
}

func (plan *databasePipelinePlan) parseCombine(raw any) error {
	if raw == nil {
		return nil
	}
	combines, ok := raw.(map[string]any)
	if !ok {
		return invalid("database_pipeline combine must be an object")
	}
	ids := make([]string, 0, len(combines))
	for id := range combines {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !graphqlName(id) {
			return invalid("database_pipeline combine name %q is invalid", id)
		}
		spec, ok := combines[id].(map[string]any)
		if !ok {
			return invalid("database_pipeline combine %s must be an object", id)
		}
		if err := onlyKeys(spec, "database_pipeline combine "+id, "join", "on", "missing", "total", "fields", "keep"); err != nil {
			return err
		}
		if raw, exists := spec["missing"]; exists && !nonEmptyString(raw) {
			return invalid("database_pipeline combine %s missing must be null or zero", id)
		}
		c := databasePipelineCombine{ID: id, Missing: stringValue(spec["missing"], "null"), TotalSteps: map[string]string{}, Fields: map[string]any{}}
		var err error
		if c.Join, err = pipelineStringList(spec["join"]); err != nil || len(c.Join) == 0 {
			return invalid("database_pipeline combine %s join must list steps", id)
		}
		if c.On, err = pipelineStringList(spec["on"]); err != nil || len(c.On) == 0 {
			return invalid("database_pipeline combine %s on must list key fields", id)
		}
		for _, stepID := range c.Join {
			index, ok := plan.stepIndex[stepID]
			if !ok || (plan.Steps[index].Op != "aggregate" && plan.Steps[index].Op != "find") {
				return invalid("database_pipeline combine %s joins unknown or non-row step %s", id, stepID)
			}
		}
		if raw, exists := spec["keep"]; exists {
			if c.Keep, err = pipelineStringList(raw); err != nil || len(c.Keep) == 0 {
				return invalid("database_pipeline combine %s keep must list joined steps", id)
			}
			for _, stepID := range c.Keep {
				if !contains(c.Join, stepID) {
					return invalid("database_pipeline combine %s keeps %s, which it does not join", id, stepID)
				}
			}
		}
		if c.Missing != "null" && c.Missing != "zero" {
			return invalid("database_pipeline combine %s missing must be null or zero", id)
		}
		if total, ok := spec["total"].(map[string]any); ok {
			if err := onlyKeys(total, "database_pipeline combine "+id+" total", "key", "steps"); err != nil {
				return err
			}
			c.TotalKey, _ = total["key"].(string)
			steps, _ := total["steps"].(map[string]any)
			if c.TotalKey == "" || len(steps) == 0 {
				return invalid("database_pipeline combine %s total requires key and steps", id)
			}
			for joined, rawTotal := range steps {
				totalStep, _ := rawTotal.(string)
				index, ok := plan.stepIndex[totalStep]
				if !contains(c.Join, joined) || !ok || plan.Steps[index].Op != "aggregate" || plan.Steps[index].Args["groupBy"] != nil {
					return invalid("database_pipeline combine %s total for %s must be an ungrouped aggregate step", id, joined)
				}
				c.TotalSteps[joined] = totalStep
			}
		} else if spec["total"] != nil {
			return invalid("database_pipeline combine %s total must be an object", id)
		}
		fields, _ := spec["fields"].(map[string]any)
		if spec["fields"] != nil && fields == nil {
			return invalid("database_pipeline combine %s fields must be an object", id)
		}
		for name, expression := range fields {
			if !graphqlName(name) || contains(c.On, name) {
				return invalid("database_pipeline combine %s field %q is invalid or shadows a key", id, name)
			}
			if err := plan.validateCombineExpression(expression, c.Join, 0); err != nil {
				return invalid("database_pipeline combine %s field %s: %s", id, name, err)
			}
			c.Fields[name] = expression
			c.FieldOrder = append(c.FieldOrder, name)
		}
		sort.Strings(c.FieldOrder)
		plan.Combines = append(plan.Combines, c)
		plan.combineIndex[id] = true
	}
	return nil
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// Combine expressions are resolver-module expressions whose inputs name a
// joined step's column ("calls.received"). A bare string is shorthand for
// {"input": ...}.
func (plan *databasePipelinePlan) validateCombineExpression(raw any, join []string, depth int) error {
	if depth > 64 {
		return errors.New("expression is too deep")
	}
	if ref, ok := raw.(string); ok {
		return plan.validateCombineInput(ref, join)
	}
	node, ok := raw.(map[string]any)
	if !ok {
		return errors.New("must be an input reference or expression object")
	}
	if input, ok := node["input"].(string); ok {
		return plan.validateCombineInput(input, join)
	}
	if _, ok := node["const"]; ok {
		return nil
	}
	op, _ := node["op"].(string)
	if !moduleExpressionOps[op] || op == "call" || op == "input" {
		return fmt.Errorf("unsupported op %q", op)
	}
	if args, ok := node["args"].([]any); ok {
		for _, arg := range args {
			if err := plan.validateCombineExpression(arg, join, depth+1); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"value", "condition", "then", "else"} {
		if value, exists := node[key]; exists {
			if err := plan.validateCombineExpression(value, join, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// Combine inputs are joined step columns, declared params ("$param.perms",
// such as verified permissions) or access values ("$scope.centres").
func (plan *databasePipelinePlan) validateCombineInput(ref string, join []string) error {
	if name, ok := strings.CutPrefix(ref, "$param."); ok {
		if _, declared := plan.Params[name]; !declared {
			return fmt.Errorf("input %q is not a declared param", ref)
		}
		return nil
	}
	if name, ok := strings.CutPrefix(ref, "$scope."); ok {
		if !plan.accessIndex[name] {
			return fmt.Errorf("input %q is not an access value", ref)
		}
		return nil
	}
	parts := strings.SplitN(ref, ".", 2)
	if len(parts) != 2 || !contains(join, parts[0]) || parts[1] == "" {
		return fmt.Errorf("input %q must be <joined step>.<column>", ref)
	}
	return nil
}

// combineExpression rewrites bare-string references into module inputs.
func combineExpression(raw any) any {
	switch value := raw.(type) {
	case string:
		return map[string]any{"input": value}
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			switch key {
			case "value", "condition", "then", "else":
				out[key] = combineExpression(item)
			case "args":
				list, _ := item.([]any)
				args := make([]any, len(list))
				for i, arg := range list {
					args[i] = combineExpression(arg)
				}
				out[key] = args
			default:
				out[key] = item
			}
		}
		return out
	}
	return raw
}

// parseResult accepts either an object mapping output fields to references
// or one reference returned as the field value itself, so a list field such as
// commercials: [Commercial!]! can take "$steps.rows" directly.
func (plan *databasePipelinePlan) parseResult(raw any) error {
	if ref, ok := raw.(string); ok {
		if !plan.validResultRef(ref) {
			return invalid("database_pipeline result references unknown %q", ref)
		}
		plan.ResultRoot = ref
		return nil
	}
	result, ok := raw.(map[string]any)
	if !ok || len(result) == 0 {
		return invalid("database_pipeline result must be a $steps, $combine or $scope reference, or an object mapping output fields to them")
	}
	for name, rawRef := range result {
		ref, _ := rawRef.(string)
		if !graphqlName(name) {
			return invalid("database_pipeline result field %q is invalid", name)
		}
		if !plan.validResultRef(ref) {
			return invalid("database_pipeline result %s references unknown %q", name, ref)
		}
		plan.Result[name] = ref
	}
	return nil
}

func (plan *databasePipelinePlan) validResultRef(ref string) bool {
	parts := strings.Split(ref, ".")
	switch {
	case len(parts) == 2 && parts[0] == "$steps":
		_, ok := plan.stepIndex[parts[1]]
		return ok
	case len(parts) == 3 && parts[0] == "$steps" && parts[2] == "first":
		index, ok := plan.stepIndex[parts[1]]
		return ok && (plan.Steps[index].Op == "aggregate" || plan.Steps[index].Op == "find")
	case len(parts) == 3 && parts[0] == "$steps" && (parts[2] == "hasMore" || parts[2] == "nextCursor"):
		index, ok := plan.stepIndex[parts[1]]
		return ok && plan.Steps[index].Page != nil
	case len(parts) == 2 && parts[0] == "$combine":
		return plan.combineIndex[parts[1]]
	case len(parts) == 2 && parts[0] == "$scope":
		return plan.accessIndex[parts[1]]
	}
	return false
}

// Collections reports every collection a run reads, including lookup targets
// and access reads, so a subscription can watch all of them.
func (plan *databasePipelinePlan) Collections() []string {
	seen := map[string]bool{}
	add := func(value any) {
		if name, ok := value.(string); ok && strings.TrimSpace(name) != "" {
			seen[strings.TrimSpace(name)] = true
		}
	}
	for _, step := range plan.Steps {
		add(step.Args["collection"])
		lookups, _ := step.Args["lookups"].([]any)
		for _, raw := range lookups {
			if lookup, ok := raw.(map[string]any); ok {
				add(lookup["collection"])
			}
		}
		// A related record can change what a filter matches.
		for _, node := range step.relations() {
			_, clause := relationClause(node)
			add(clause["collection"])
		}
	}
	for _, value := range plan.Access {
		for _, read := range []*databasePipelineRead{value.Default, value.Otherwise} {
			if read != nil {
				add(read.Collection)
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (plan *databasePipelinePlan) validateLimits(limits releaseLimits) error {
	for _, step := range plan.Steps {
		if step.MaxRows > limits.MaxRows {
			return invalid("database_pipeline step %s max_rows %d exceeds release max_rows %d", step.ID, step.MaxRows, limits.MaxRows)
		}
	}
	return nil
}

// ---- execution ----

type databasePipelineRun struct {
	app     *App
	plan    *databasePipelinePlan
	project string
	params  map[string]any
	scope   map[string][]string
	pages   map[string]map[string]any // paged find steps: hasMore, nextCursor
	trace   *pipelineTraceRecorder
}

func (a *App) callDatabasePipeline(ctx context.Context, config map[string]any) (value any, err error) {
	trace := startPipelineTrace(ctx)
	defer func() { trace.finish(err) }()
	plan, err := validateDatabasePipeline(config)
	if err != nil {
		return nil, err
	}
	limits, ok := ctx.Value(executionLimitsKey{}).(releaseLimits)
	if !ok {
		limits = defaultReleaseLimits()
	}
	if err := plan.validateLimits(limits); err != nil {
		return nil, err
	}
	project, _ := config["_project_id"].(string)
	run := &databasePipelineRun{app: a, plan: plan, project: project, scope: map[string][]string{}, trace: trace}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(plan.TimeoutMS)*time.Millisecond)
	defer cancel()
	if run.params, err = plan.bindParams(ctx, resolverArgs(config), config["parent"]); err != nil {
		return nil, err
	}
	if err := run.resolveAccess(ctx); err != nil {
		return nil, err
	}
	results, err := run.readSteps(ctx)
	if err != nil {
		return nil, err
	}
	return run.assemble(results)
}

func (plan *databasePipelinePlan) bindParams(ctx context.Context, args map[string]any, parent any) (map[string]any, error) {
	out := make(map[string]any, len(plan.Params))
	for name, p := range plan.Params {
		value, found := p.Value, p.HasValue
		if p.From != "" {
			value, found = aggregatePipelineValue(ctx, p.From, args, parent)
		}
		if !found || value == nil {
			if p.Required {
				if strings.HasPrefix(p.From, "$identity.") {
					return nil, forbidden("required identity value " + p.From + " is missing")
				}
				return nil, invalid("database_pipeline param %s is required", name)
			}
			out[name] = nil
			continue
		}
		if p.Type == "string_list" || p.Type == "exact_string_list" {
			list, ok := value.([]any)
			if !ok {
				if typed, isStrings := value.([]string); isStrings {
					for _, item := range typed {
						list = append(list, item)
					}
					ok = true
				}
			}
			if !ok || len(list) > 1000 {
				return nil, invalid("database_pipeline param %s must be a list of at most 1000 strings", name)
			}
			values := []any{}
			seen := map[string]bool{}
			for _, item := range list {
				text, isString := item.(string)
				if !isString {
					return nil, invalid("database_pipeline param %s must contain strings", name)
				}
				// Authorization lists are normalised; an exact list keeps
				// identifiers as sent, blanks included.
				if p.Type == "string_list" {
					if text = strings.TrimSpace(text); text == "" {
						continue
					}
				}
				if !seen[text] {
					seen[text] = true
					values = append(values, text)
				}
			}
			out[name] = values
			continue
		}
		coerced, err := coerceAggregatePipelineValue(value, p.Type)
		if err != nil {
			if strings.HasPrefix(p.From, "$identity.") {
				return nil, forbidden("authorization claim cannot be applied")
			}
			return nil, invalid("database_pipeline param %s: %s", name, strings.TrimPrefix(err.Error(), "aggregate_pipeline parameter "))
		}
		out[name] = coerced
	}
	return out, nil
}

// substitute replaces $param/$scope placeholders. It is only applied to
// positions validation allowed them in (filter values and get keys).
func (run *databasePipelineRun) substitute(raw any) any {
	switch value := raw.(type) {
	case string:
		if name, ok := strings.CutPrefix(value, "$param."); ok {
			return run.params[name]
		}
		if name, ok := strings.CutPrefix(value, "$scope."); ok {
			list := make([]any, len(run.scope[name]))
			for i, item := range run.scope[name] {
				list[i] = item
			}
			return list
		}
		return value
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = run.substitute(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, item := range value {
			out[key] = run.substitute(item)
		}
		return out
	}
	return raw
}

func (run *databasePipelineRun) filter(raw any) any {
	node, ok := raw.(map[string]any)
	if !ok {
		return raw
	}
	out := make(map[string]any, len(node))
	for key, item := range node {
		switch key {
		case "and", "or":
			list, _ := item.([]any)
			children := make([]any, len(list))
			for i, child := range list {
				children[i] = run.filter(child)
			}
			out[key] = children
		case "value":
			out[key] = run.substitute(item)
		case relationExists, relationNotExists:
			clause, _ := item.(map[string]any)
			copied := make(map[string]any, len(clause))
			for name, value := range clause {
				copied[name] = value
			}
			if where, found := clause["where"]; found {
				copied["where"] = run.filter(where)
			}
			out[key] = copied
		default:
			out[key] = item
		}
	}
	return normalizeInLeaf(out)
}

// Database accepts 1 to 100 values for in. A bound list outside that range is
// rewritten, never dropped: an empty list becomes a predicate that matches
// nothing (the same field both null and not null), so and/or, counts and
// aggregates keep their meaning; a longer list becomes an or of chunks.
const maxDatabaseInValues = 100

func normalizeInLeaf(leaf map[string]any) map[string]any {
	op := leaf["op"]
	if op != "in" && op != "not_in" {
		return leaf
	}
	values, ok := leaf["value"].([]any)
	if !ok {
		return leaf
	}
	field := leaf["field"]
	if len(values) == 0 {
		if op == "in" {
			return matchNothing(field)
		}
		// A null never satisfies not_in, whatever the list holds.
		return map[string]any{"field": field, "op": "is_not_null"}
	}
	if len(values) <= maxDatabaseInValues {
		return leaf
	}
	chunks := []any{}
	for start := 0; start < len(values); start += maxDatabaseInValues {
		end := min(start+maxDatabaseInValues, len(values))
		chunks = append(chunks, map[string]any{"field": field, "op": op, "value": values[start:end]})
	}
	if op == "in" {
		return map[string]any{"or": chunks}
	}
	return map[string]any{"and": chunks}
}

func matchNothing(field any) map[string]any {
	return map[string]any{"and": []any{
		map[string]any{"field": field, "op": "is_null"},
		map[string]any{"field": field, "op": "is_not_null"},
	}}
}

// filterMatchesNothing reports whether a bound filter is provably empty, so a
// find or count can be answered without reading the collection.
func filterMatchesNothing(raw any) bool {
	node, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	// No related record can match, so the clause can't hold.
	if key, clause := relationClause(node); key == relationExists && clause != nil {
		return filterMatchesNothing(clause["where"])
	}
	if children, ok := node["and"].([]any); ok {
		if len(children) == 2 {
			a, _ := children[0].(map[string]any)
			b, _ := children[1].(map[string]any)
			if a != nil && b != nil && a["field"] != nil && a["field"] == b["field"] && a["op"] == "is_null" && b["op"] == "is_not_null" {
				return true
			}
		}
		for _, child := range children {
			if filterMatchesNothing(child) {
				return true
			}
		}
		return false
	}
	if children, ok := node["or"].([]any); ok && len(children) > 0 {
		for _, child := range children {
			if !filterMatchesNothing(child) {
				return false
			}
		}
		return true
	}
	return false
}

// batch sends one read batch. Finds and counts whose filter matches nothing
// are answered locally with Database's empty shapes; aggregates are always
// sent so their summary-row semantics stay Database's.
func (run *databasePipelineRun) batch(ctx context.Context, stage string, operations []any) ([]any, error) {
	run.trace.beginBatch(stage, run.plan.Steps, operations)
	results := make([]any, len(operations))
	sent := []any{}
	positions := []int{}
	for i, raw := range operations {
		op, _ := raw.(map[string]any)
		args, _ := op["args"].(map[string]any)
		if filterMatchesNothing(args["where"]) {
			switch op["op"] {
			case "find":
				results[i] = map[string]any{"records": []any{}, "hasMore": false}
				run.trace.answeredLocally(i)
				continue
			case "count":
				results[i] = map[string]any{"count": "0"}
				run.trace.answeredLocally(i)
				continue
			}
		}
		sent = append(sent, raw)
		positions = append(positions, i)
	}
	if len(sent) == 0 {
		run.trace.endBatch(results, nil)
		return results, nil
	}
	answered, err := run.send(ctx, sent)
	if err != nil {
		run.trace.endBatch(nil, nil)
		return nil, err
	}
	taken := takeDatabaseStatistics(answered)
	statistics := make([]map[string]any, len(operations))
	for j, i := range positions {
		results[i] = answered[j]
		if taken != nil {
			statistics[i] = taken[j]
		}
	}
	run.trace.endBatch(results, statistics)
	return results, nil
}

// send makes one logical request. A Database that doesn't know the statistics
// option refuses the batch while decoding it, before any read has run, and gets
// the same batch again without the option.
func (run *databasePipelineRun) send(ctx context.Context, operations []any) ([]any, error) {
	run.trace.noteStatistics(operations)
	results, err := run.sendOnce(ctx, operations)
	if databaseRefusedStatistics(err) && operationsRequestStatistics(operations) {
		run.app.rememberStatisticsRefused(run.project)
		run.trace.statisticsRefused()
		return run.sendOnce(ctx, withoutStatistics(operations))
	}
	return results, err
}

func (run *databasePipelineRun) sendOnce(ctx context.Context, operations []any) ([]any, error) {
	input := map[string]any{
		"database":    run.plan.Database,
		"scope":       run.plan.Scope,
		"operations":  operations,
		"_project_id": run.project,
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := int(time.Until(deadline).Milliseconds())
		if remaining < 1 {
			return nil, runtimeError("timeout", "database pipeline deadline exceeded")
		}
		if remaining > maxDatabasePipelineTimeout {
			remaining = maxDatabasePipelineTimeout
		}
		input["timeoutMs"] = remaining
	}
	callCtx, wire := run.trace.attachWire(ctx)
	out := databaseBatchResponse{wire: wire}
	err := sdk.CallAppResultContext(callCtx, run.app.ctx.WithProject(run.project).PlatformAPI(), "database", "db_batch", input, &out)
	wire.returned()
	if err != nil {
		return nil, databasePipelineError(ctx, err)
	}
	if len(out.Results) != len(operations) {
		return nil, runtimeError("source_error", "database batch returned an unexpected number of results")
	}
	return out.Results, nil
}

// databasePipelineError keeps Database's failure class so callers can tell a
// timeout or budget overrun from a bad request. Nothing partial is returned.
func databasePipelineError(ctx context.Context, err error) error {
	message := err.Error()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return runtimeError("timeout", "database pipeline timed out: "+message)
	}
	var coded *graphqlError
	if errors.As(databaseCallError(err), &coded) {
		return coded
	}
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), strings.Contains(message, "deadline"), strings.Contains(message, "timeout"):
		return runtimeError("timeout", "database pipeline timed out: "+message)
	case strings.Contains(message, "resource_limit"):
		return runtimeError("resource_limit", message)
	case strings.Contains(message, "permission") || strings.Contains(message, "not granted") || strings.Contains(message, "access"):
		return runtimeError("source_permission_denied", message)
	}
	return runtimeError("source_error", message)
}

// identityPermissions merges the verified identity's permissions with a
// permissions claim, as every permission check in this app does.
func identityPermissions(identity *requestIdentity) map[string]bool {
	held := map[string]bool{}
	if identity == nil {
		return held
	}
	for _, permission := range identity.Permissions {
		held[strings.TrimSpace(permission)] = true
	}
	if claimed, ok := identity.Claims["permissions"].([]any); ok {
		for _, permission := range claimed {
			if text, ok := permission.(string); ok {
				held[strings.TrimSpace(text)] = true
			}
		}
	}
	return held
}

// verifiedPermissionList exposes the same merged permissions as
// $identity.permissions, sorted, for params and module inputs.
func verifiedPermissionList(identity *requestIdentity) []any {
	held := identityPermissions(identity)
	names := make([]string, 0, len(held))
	for name := range held {
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]any, len(names))
	for i, name := range names {
		out[i] = name
	}
	return out
}

func holdsAny(held map[string]bool, permissions []string) bool {
	for _, permission := range permissions {
		if held[permission] {
			return true
		}
	}
	return false
}

func (run *databasePipelineRun) resolveAccess(ctx context.Context) error {
	plan := run.plan
	if len(plan.RequireAny) == 0 && len(plan.Access) == 0 {
		return nil
	}
	identity := securityIdentity(ctx)
	if identity == nil {
		return forbidden("database pipeline requires an authenticated caller")
	}
	held := identityPermissions(identity)
	if len(plan.RequireAny) > 0 && !holdsAny(held, plan.RequireAny) {
		return forbidden("caller lacks a permission required by this report")
	}
	requested := map[string][]string{}
	for _, value := range plan.Access {
		for _, item := range asAnyList(run.params[strings.TrimPrefix(value.Requested, "$param.")]) {
			requested[value.Name] = append(requested[value.Name], item.(string))
		}
	}
	// Each dependency level is one read batch; a later level filters on the
	// values resolved before it.
	for _, level := range plan.AccessLevels {
		if err := run.resolveAccessLevel(ctx, level, held, requested); err != nil {
			return err
		}
		for _, i := range level {
			// An optional value may be a known empty list; reads that failed
			// have already returned their error.
			if value := plan.Access[i]; len(run.scope[value.Name]) == 0 && !value.Optional {
				return forbidden("caller has no accessible " + value.Name)
			}
		}
	}
	return nil
}

type databasePipelinePendingRead struct {
	value databasePipelineAccessValue
	read  *databasePipelineRead
	index int
}

// deriveAccessValue computes union, intersect and expr values in memory from
// values already resolved.
func (run *databasePipelineRun) deriveAccessValue(value databasePipelineAccessValue) error {
	list := newAccessList(value.Exact)
	add := list.add
	switch {
	case value.Union != nil:
		for _, ref := range value.Union {
			for _, item := range run.scope[ref] {
				add(item)
			}
		}
	case value.Intersect != nil:
		for _, item := range run.scope[value.Intersect[0]] {
			inAll := true
			for _, ref := range value.Intersect[1:] {
				inAll = inAll && contains(run.scope[ref], item)
			}
			if inAll {
				add(item)
			}
		}
	case value.Expr != nil:
		inputs := map[string]any{}
		for name, param := range run.params {
			inputs["$param."+name] = param
		}
		for name, list := range run.scope {
			items := make([]any, len(list))
			for i, item := range list {
				items[i] = item
			}
			inputs["$scope."+name] = items
		}
		result, err := moduleRuntime{}.eval(combineExpression(value.Expr), inputs, 0)
		if err != nil {
			return runtimeError("invalid_result", "database pipeline access value "+value.Name+": "+err.Error())
		}
		switch items := result.(type) {
		case nil:
		case []any:
			for _, item := range items {
				if item == nil {
					continue
				}
				switch item.(type) {
				case map[string]any, []any:
					return runtimeError("invalid_result", "database pipeline access value "+value.Name+" must be a list of scalar values")
				}
				add(fmt.Sprint(item))
			}
		case []string:
			for _, item := range items {
				add(item)
			}
		default:
			return runtimeError("invalid_result", "database pipeline access value "+value.Name+" must evaluate to a list")
		}
	}
	run.scope[value.Name] = list.values
	return nil
}

func (run *databasePipelineRun) resolveAccessLevel(ctx context.Context, level []int, held map[string]bool, requested map[string][]string) error {
	var reads []databasePipelinePendingRead
	var operations []any
	for _, i := range level {
		value := run.plan.Access[i]
		if len(value.RequireAny) > 0 && !holdsAny(held, value.RequireAny) {
			return forbidden("caller lacks a permission required for " + value.Name)
		}
		if value.derived() {
			continue // evaluated after this level's reads
		}
		unrestricted := value.UnrestrictedPermission != "" && held[value.UnrestrictedPermission]
		var read *databasePipelineRead
		switch {
		case unrestricted && len(requested[value.Name]) > 0:
			run.scope[value.Name] = requested[value.Name]
			continue
		case unrestricted && value.Default == nil:
			return invalid("database pipeline access value %s requires requested values", value.Name)
		case unrestricted:
			read = value.Default
		case value.Otherwise == nil:
			return forbidden("caller has no access to " + value.Name)
		default:
			read = value.Otherwise
		}
		if read.Distinct {
			// Null groups carry no value, so they are excluded rather than
			// counted against max; the filter only narrows.
			where := any(map[string]any{"field": read.Field, "op": "is_not_null"})
			if read.Where != nil {
				where = map[string]any{"and": []any{run.filter(read.Where), where}}
			}
			args := map[string]any{"collection": read.Collection, "where": where, "groupBy": []any{read.Field},
				"metrics": []any{map[string]any{"name": distinctAccessMetric(read.Field), "op": "count"}}, "limit": read.Max + 1}
			reads = append(reads, databasePipelinePendingRead{value: value, read: read, index: len(operations)})
			operations = append(operations, map[string]any{"op": "aggregate", "args": args})
			continue
		}
		args := map[string]any{"collection": read.Collection, "select": []any{read.Field}, "limit": read.Max + 1}
		if read.Limit > 0 {
			args["limit"] = read.Limit
		}
		if read.OrderBy != nil {
			args["orderBy"] = read.OrderBy
		}
		if read.Where != nil {
			args["where"] = run.filter(read.Where)
		}
		reads = append(reads, databasePipelinePendingRead{value: value, read: read, index: len(operations)})
		operations = append(operations, map[string]any{"op": "find", "args": args})
	}
	if len(operations) > 0 {
		results, err := run.batch(ctx, pipelineStageAccess, operations)
		if err != nil {
			return err
		}
		if err := run.applyAccessReads(reads, results, requested); err != nil {
			return err
		}
	}
	for _, i := range level {
		if value := run.plan.Access[i]; value.derived() {
			if err := run.deriveAccessValue(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func (run *databasePipelineRun) applyAccessReads(reads []databasePipelinePendingRead, results []any, requested map[string][]string) error {
	for _, p := range reads {
		page, _ := results[p.index].(map[string]any)
		records, _ := page["records"].([]any)
		if p.read.Distinct {
			var ok bool
			if records, ok = page["rows"].([]any); !ok {
				return runtimeError("source_error", "access read for "+p.value.Name+" returned an invalid result")
			}
			if page["truncated"] == true || len(records) > p.read.Max {
				return runtimeError("source_result_truncated", "distinct access values for "+p.value.Name+" exceed their max")
			}
		} else if p.read.Limit == 0 && (page["hasMore"] == true || len(records) > p.read.Max) {
			return runtimeError("source_result_truncated", "access values for "+p.value.Name+" exceed their max")
		}
		restricted := p.read == p.value.Otherwise
		if restricted && p.value.SingleRecord && len(records) != 1 {
			return forbidden("caller does not resolve to exactly one " + p.value.Name + " record")
		}
		collected := newAccessList(p.value.Exact)
		add := func(raw any) {
			if raw != nil {
				collected.add(fmt.Sprint(raw))
			}
		}
		for _, raw := range records {
			record, _ := raw.(map[string]any)
			// An array field (a grant's offer ids, say) contributes each element.
			if list, ok := record[p.read.Field].([]any); ok {
				for _, item := range list {
					add(item)
				}
				continue
			}
			add(record[p.read.Field])
		}
		values := collected.values
		if restricted && p.value.Single && len(values) != 1 {
			return forbidden("caller does not resolve to exactly one " + p.value.Name)
		}
		if restricted && len(requested[p.value.Name]) > 0 {
			for _, want := range requested[p.value.Name] {
				if !collected.seen[want] {
					return forbidden("requested " + p.value.Name + " are outside the caller's access")
				}
			}
			values = requested[p.value.Name]
		}
		run.scope[p.value.Name] = values
	}
	return nil
}

// distinctAccessMetric names the count a distinct access read needs (Database
// aggregates require a metric) without colliding with the grouped field.
func distinctAccessMetric(field string) string {
	if field == "access_count" {
		return "access_count_n"
	}
	return "access_count"
}

func asAnyList(value any) []any {
	list, _ := value.([]any)
	return list
}

func (run *databasePipelineRun) readSteps(ctx context.Context) (map[string]any, error) {
	operations := make([]any, len(run.plan.Steps))
	statistics := run.app.wantsDatabaseStatistics(run.project)
	for i, step := range run.plan.Steps {
		args := make(map[string]any, len(step.Args)+1)
		for key, value := range step.Args {
			switch key {
			case "where":
				where, err := run.where(value)
				if err != nil {
					return nil, err
				}
				if where != nil {
					args[key] = where
				}
			case "key":
				args[key] = run.substitute(value)
			case "metrics":
				metrics, _ := value.([]any)
				bound := make([]any, len(metrics))
				for j, raw := range metrics {
					metric, _ := raw.(map[string]any)
					copied := make(map[string]any, len(metric))
					for k, v := range metric {
						copied[k] = v
					}
					if metric["where"] != nil {
						where, err := run.where(metric["where"])
						if err != nil {
							return nil, err
						}
						copied["where"] = where
						if where == nil {
							delete(copied, "where")
						}
					}
					bound[j] = copied
				}
				args[key] = bound
			default:
				args[key] = value
			}
		}
		if step.Op == "aggregate" || step.Op == "find" {
			args["limit"] = step.MaxRows
		}
		if step.Page != nil {
			size, err := run.pageSize(step)
			if err != nil {
				return nil, err
			}
			args["limit"] = size
			if cursor, _ := run.substitute(step.Page.Cursor).(string); step.Page.Cursor != "" && strings.TrimSpace(cursor) != "" {
				args["cursor"] = cursor
			}
		}
		if step.Op == "count" && statistics {
			args["diagnostics"] = true
		}
		operations[i] = map[string]any{"op": step.Op, "args": args}
	}
	results, err := run.batch(ctx, pipelineStageBatch, operations)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(results))
	run.pages = map[string]map[string]any{}
	for i, step := range run.plan.Steps {
		if step.Page != nil {
			page, _ := results[i].(map[string]any)
			records, _ := page["records"].([]any)
			if page == nil {
				return nil, runtimeError("source_error", "database step "+step.ID+" returned an invalid result")
			}
			if records == nil {
				records = []any{}
			}
			meta := map[string]any{"hasMore": page["hasMore"] == true, "nextCursor": nil}
			if cursor, ok := page["nextCursor"].(string); ok && cursor != "" && page["hasMore"] == true {
				meta["nextCursor"] = cursor
			}
			run.pages[step.ID] = meta
			out[step.ID] = records
			continue
		}
		value, err := decodeDatabasePipelineStep(step, results[i])
		if err != nil {
			return nil, err
		}
		out[step.ID] = value
	}
	return out, nil
}

// pageSize binds a paged step's size: a client may ask for fewer records than
// max_rows, never more.
func (run *databasePipelineRun) pageSize(step databasePipelineStep) (int, error) {
	raw := step.Page.Size
	if ref, ok := raw.(string); ok {
		raw = run.substitute(ref)
	}
	if raw == nil {
		return step.MaxRows, nil
	}
	size, integral := strictInt(raw)
	if !integral || size < 1 {
		return 0, invalid("database_pipeline step %s page size must be a positive integer", step.ID)
	}
	if size > step.MaxRows {
		size = step.MaxRows
	}
	return size, nil
}

// decodeDatabasePipelineStep fails rather than return a partial result.
func decodeDatabasePipelineStep(step databasePipelineStep, raw any) (any, error) {
	result, ok := raw.(map[string]any)
	if !ok {
		return nil, runtimeError("source_error", "database step "+step.ID+" returned an invalid result")
	}
	switch step.Op {
	case "aggregate":
		if result["truncated"] == true {
			return nil, runtimeError("source_result_truncated", "database step "+step.ID+" exceeded max_rows")
		}
		rows, _ := result["rows"].([]any)
		for _, rawRow := range rows {
			row, _ := rawRow.(map[string]any)
			for name := range step.Metrics {
				row[name] = databaseInteger(row[name])
			}
		}
		if rows == nil {
			rows = []any{}
		}
		return rows, nil
	case "find":
		if result["hasMore"] == true && !step.TopN {
			return nil, runtimeError("source_result_truncated", "database step "+step.ID+" exceeded max_rows")
		}
		records, _ := result["records"].([]any)
		if records == nil {
			records = []any{}
		}
		return records, nil
	case "count":
		return databaseInteger(result["count"]), nil
	case "get":
		return result["record"], nil
	}
	return nil, internal("unknown database pipeline step op")
}

// databaseInteger turns Database's decimal-string integers into JSON numbers
// while they stay exact in a float64 client; larger values remain strings.
func databaseInteger(value any) any {
	text, ok := value.(string)
	if !ok {
		return value
	}
	number, ok := new(big.Int).SetString(text, 10)
	if !ok {
		return value
	}
	if number.IsInt64() && number.Int64() <= 1<<53 && number.Int64() >= -(1<<53) {
		return number.Int64()
	}
	return value
}

func (run *databasePipelineRun) assemble(steps map[string]any) (any, error) {
	combined := map[string]any{}
	for _, c := range run.plan.Combines {
		rows, err := run.combine(c, steps)
		if err != nil {
			return nil, err
		}
		combined[c.ID] = rows
	}
	resolve := func(ref string) any {
		parts := strings.Split(ref, ".")
		switch parts[0] {
		case "$steps":
			value := steps[parts[1]]
			if len(parts) == 3 && parts[2] != "first" {
				return run.pages[parts[1]][parts[2]]
			}
			if len(parts) == 3 {
				rows, _ := value.([]any)
				value = nil
				if len(rows) > 0 {
					value = rows[0]
				}
			}
			return value
		case "$combine":
			return combined[parts[1]]
		case "$scope":
			return run.scope[parts[1]]
		}
		return nil
	}
	if run.plan.ResultRoot != "" {
		return resolve(run.plan.ResultRoot), nil
	}
	out := make(map[string]any, len(run.plan.Result))
	for name, ref := range run.plan.Result {
		out[name] = resolve(ref)
	}
	return out, nil
}

func (run *databasePipelineRun) combine(c databasePipelineCombine, steps map[string]any) ([]any, error) {
	type joined struct {
		keys  map[string]any
		parts map[string]map[string]any
	}
	order := []string{}
	byKey := map[string]*joined{}
	for _, stepID := range c.Join {
		rows, _ := steps[stepID].([]any)
		for _, raw := range rows {
			row, _ := raw.(map[string]any)
			keyValues := make([]any, len(c.On))
			for i, field := range c.On {
				keyValues[i] = row[field]
			}
			encoded, _ := json.Marshal(keyValues)
			key := string(encoded)
			entry := byKey[key]
			if entry == nil {
				entry = &joined{keys: map[string]any{}, parts: map[string]map[string]any{}}
				for i, field := range c.On {
					entry.keys[field] = keyValues[i]
				}
				byKey[key] = entry
				order = append(order, key)
			}
			if entry.parts[stepID] != nil {
				return nil, runtimeError("invalid_result", "database pipeline combine "+c.ID+": step "+stepID+" has duplicate keys")
			}
			entry.parts[stepID] = row
		}
	}
	entries := make([]*joined, 0, len(order)+1)
	for _, key := range order {
		entries = append(entries, byKey[key])
	}
	if c.TotalKey != "" {
		total := &joined{keys: map[string]any{}, parts: map[string]map[string]any{}}
		for _, field := range c.On {
			total.keys[field] = c.TotalKey
		}
		for stepID, totalID := range c.TotalSteps {
			if rows, _ := steps[totalID].([]any); len(rows) > 0 {
				total.parts[stepID], _ = rows[0].(map[string]any)
			}
		}
		entries = append(entries, total)
	}
	runtime := moduleRuntime{}
	out := make([]any, 0, len(entries))
	for _, entry := range entries {
		inputs := map[string]any{}
		for name, value := range run.params {
			inputs["$param."+name] = value
		}
		for name, values := range run.scope {
			list := make([]any, len(values))
			for i, item := range values {
				list[i] = item
			}
			inputs["$scope."+name] = list
		}
		for _, stepID := range c.Join {
			step := run.plan.Steps[run.plan.stepIndex[stepID]]
			row := entry.parts[stepID]
			for name, value := range row {
				inputs[stepID+"."+name] = value
			}
			if c.Missing == "zero" {
				for name := range step.Metrics {
					if inputs[stepID+"."+name] == nil {
						inputs[stepID+"."+name] = int64(0)
					}
				}
			}
		}
		row := make(map[string]any, len(entry.keys)+len(c.Fields))
		for field, value := range entry.keys {
			row[field] = value
		}
		for _, stepID := range c.Keep {
			for name, value := range entry.parts[stepID] {
				if !contains(c.On, name) {
					row[name] = value
				}
			}
		}
		if len(c.Fields) == 0 && len(c.Keep) == 0 {
			for name, value := range inputs {
				if strings.HasPrefix(name, "$") {
					continue
				}
				if field := strings.SplitN(name, ".", 2); !contains(c.On, field[1]) {
					row[field[0]+"_"+field[1]] = value
				}
			}
		}
		for _, name := range c.FieldOrder {
			value, err := runtime.eval(combineExpression(c.Fields[name]), inputs, 0)
			if err != nil {
				return nil, runtimeError("invalid_result", "database pipeline combine "+c.ID+"."+name+": "+err.Error())
			}
			row[name] = value
		}
		out = append(out, row)
	}
	return out, nil
}
