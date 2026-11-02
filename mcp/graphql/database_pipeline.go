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
// field over the matching records.
type databasePipelineRead struct {
	Collection string
	Where      any
	Field      string
	Max        int
}

type databasePipelineAccessValue struct {
	Name                   string
	Requested              string // param holding the client's requested values
	UnrestrictedPermission string
	Default                *databasePipelineRead // unrestricted caller, nothing requested
	Otherwise              *databasePipelineRead // restricted caller
	Single                 bool
}

type databasePipelineStep struct {
	ID      string
	Op      string
	Args    map[string]any
	MaxRows int
	TopN    bool            // find: max_rows is the wanted size, not a bound
	Metrics map[string]bool // aggregate metrics with integer results
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
}

type databasePipelinePlan struct {
	Database     string
	Scope        string
	Params       map[string]databasePipelineParam
	RequireAny   []string
	Access       []databasePipelineAccessValue
	Steps        []databasePipelineStep
	Combines     []databasePipelineCombine
	Result       map[string]string
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
		plan.TimeoutMS = intValue(raw)
		if plan.TimeoutMS < 1 || plan.TimeoutMS > maxDatabasePipelineTimeout {
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
		p := databasePipelineParam{Type: stringValue(spec["type"], "string")}
		switch p.Type {
		case "string", "number", "boolean", "datetime", "json", "string_list":
		default:
			return invalid("database_pipeline param %s type must be string, number, boolean, datetime, json or string_list", name)
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
		if hasFrom == hasValue {
			return invalid("database_pipeline param %s requires exactly one of from or value", name)
		}
		if hasFrom && !validAggregatePipelinePath(from) {
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
	// One extra record detects overflow, within Database's 1000-record page.
	read := &databasePipelineRead{Max: 999}
	read.Collection, _ = spec["collection"].(string)
	read.Field, _ = spec["field"].(string)
	if strings.TrimSpace(read.Collection) == "" || strings.TrimSpace(read.Field) == "" {
		return nil, invalid("database_pipeline access %s requires collection and field", label)
	}
	if raw, ok := spec["max"]; ok {
		read.Max = intValue(raw)
		if read.Max < 1 || read.Max > 999 {
			return nil, invalid("database_pipeline access %s max must be between 1 and 999", label)
		}
	}
	if where, ok := spec["where"]; ok {
		if err := plan.validateFilter(where, false, "access "+label+".where"); err != nil {
			return nil, err
		}
		read.Where = where
	}
	return read, nil
}

func (plan *databasePipelinePlan) parseAccess(raw any) error {
	if raw == nil {
		return nil
	}
	access, ok := raw.(map[string]any)
	if !ok {
		return invalid("database_pipeline access must be an object")
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
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !graphqlName(name) {
			return invalid("database_pipeline access value name %q is invalid", name)
		}
		spec, ok := values[name].(map[string]any)
		if !ok {
			return invalid("database_pipeline access value %s must be an object", name)
		}
		value := databasePipelineAccessValue{Name: name}
		value.Requested, _ = spec["requested"].(string)
		if value.Requested != "" {
			p, ok := plan.Params[strings.TrimPrefix(value.Requested, "$param.")]
			if !strings.HasPrefix(value.Requested, "$param.") || !ok || p.Type != "string_list" {
				return invalid("database_pipeline access value %s requested must name a string_list $param", name)
			}
		}
		value.UnrestrictedPermission, _ = spec["unrestricted_if_permission"].(string)
		if single, exists := spec["single"]; exists {
			value.Single, _ = single.(bool)
		}
		var err error
		if spec["default"] != nil {
			if value.Default, err = plan.parseRead(spec["default"], name+".default"); err != nil {
				return err
			}
		}
		if spec["otherwise"] != nil {
			if value.Otherwise, err = plan.parseRead(spec["otherwise"], name+".otherwise"); err != nil {
				return err
			}
		}
		if value.Otherwise == nil && value.UnrestrictedPermission == "" {
			return invalid("database_pipeline access value %s needs otherwise or unrestricted_if_permission", name)
		}
		if value.Default != nil && value.UnrestrictedPermission == "" {
			return invalid("database_pipeline access value %s default applies only with unrestricted_if_permission", name)
		}
		plan.Access = append(plan.Access, value)
		plan.accessIndex[name] = true
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
				if err := plan.validateFilter(child, allowScope, fmt.Sprintf("%s.%s[%d]", path, group, i)); err != nil {
					return err
				}
			}
			return nil
		}
	}
	for key := range node {
		if key != "field" && key != "op" && key != "value" {
			return invalid("database_pipeline %s has unknown key %q", path, key)
		}
	}
	if field, _ := node["field"].(string); strings.TrimSpace(field) == "" || strings.HasPrefix(field, "$") {
		return invalid("database_pipeline %s requires a literal field", path)
	}
	if op, _ := node["op"].(string); strings.TrimSpace(op) == "" {
		return invalid("database_pipeline %s requires op", path)
	}
	return plan.validatePlaceholders(node["value"], allowScope, path+".value")
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
		if args == nil {
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
			case "key":
				if err := plan.validatePlaceholders(value, true, "step "+step.ID+".key"); err != nil {
					return err
				}
			case "metrics":
				metrics, _ := value.([]any)
				if len(metrics) == 0 {
					return invalid("database_pipeline step %s metrics must be a non-empty list", step.ID)
				}
				for _, rawMetric := range metrics {
					metric, _ := rawMetric.(map[string]any)
					name, _ := metric["name"].(string)
					if name == "" {
						return invalid("database_pipeline step %s metric requires a name", step.ID)
					}
					for metricKey, metricValue := range metric {
						var err error
						if metricKey == "where" {
							// Metric filters are Database filters: they may take values.
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
		if collection, _ := args["collection"].(string); strings.TrimSpace(collection) == "" {
			return invalid("database_pipeline step %s requires a collection", step.ID)
		}
		if step.Op == "get" && args["key"] == nil {
			return invalid("database_pipeline step %s get requires key", step.ID)
		}
		if step.Op == "aggregate" && args["metrics"] == nil {
			return invalid("database_pipeline step %s aggregate requires metrics", step.ID)
		}
		step.MaxRows = 1000
		if raw, exists := spec["max_rows"]; exists {
			step.MaxRows = intValue(raw)
		}
		if step.MaxRows < 1 || step.MaxRows > 1000 {
			return invalid("database_pipeline step %s max_rows must be between 1 and 1000", step.ID)
		}
		if raw, exists := spec["top_n"]; exists {
			topN, ok := raw.(bool)
			if !ok || step.Op != "find" {
				return invalid("database_pipeline step %s top_n applies to find steps and must be Boolean", step.ID)
			}
			step.TopN = topN
		}
		step.Args = args
		plan.stepIndex[step.ID] = len(plan.Steps)
		plan.Steps = append(plan.Steps, step)
	}
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
		if c.Missing != "null" && c.Missing != "zero" {
			return invalid("database_pipeline combine %s missing must be null or zero", id)
		}
		if total, ok := spec["total"].(map[string]any); ok {
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
			if err := validateCombineExpression(expression, c.Join, 0); err != nil {
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
func validateCombineExpression(raw any, join []string, depth int) error {
	if depth > 64 {
		return errors.New("expression is too deep")
	}
	if ref, ok := raw.(string); ok {
		return validateCombineInput(ref, join)
	}
	node, ok := raw.(map[string]any)
	if !ok {
		return errors.New("must be an input reference or expression object")
	}
	if input, ok := node["input"].(string); ok {
		return validateCombineInput(input, join)
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
			if err := validateCombineExpression(arg, join, depth+1); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"value", "condition", "then", "else"} {
		if value, exists := node[key]; exists {
			if err := validateCombineExpression(value, join, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCombineInput(ref string, join []string) error {
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

func (plan *databasePipelinePlan) parseResult(raw any) error {
	result, ok := raw.(map[string]any)
	if !ok || len(result) == 0 {
		return invalid("database_pipeline result must map output fields to $steps, $combine or $scope references")
	}
	for name, rawRef := range result {
		ref, _ := rawRef.(string)
		if !graphqlName(name) {
			return invalid("database_pipeline result field %q is invalid", name)
		}
		parts := strings.Split(ref, ".")
		valid := false
		switch {
		case len(parts) == 2 && parts[0] == "$steps":
			_, valid = plan.stepIndex[parts[1]]
		case len(parts) == 3 && parts[0] == "$steps" && parts[2] == "first":
			index, ok := plan.stepIndex[parts[1]]
			valid = ok && (plan.Steps[index].Op == "aggregate" || plan.Steps[index].Op == "find")
		case len(parts) == 2 && parts[0] == "$combine":
			valid = plan.combineIndex[parts[1]]
		case len(parts) == 2 && parts[0] == "$scope":
			valid = plan.accessIndex[parts[1]]
		}
		if !valid {
			return invalid("database_pipeline result %s references unknown %q", name, ref)
		}
		plan.Result[name] = ref
	}
	return nil
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
}

func (a *App) callDatabasePipeline(ctx context.Context, config map[string]any) (any, error) {
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
	run := &databasePipelineRun{app: a, plan: plan, project: project, scope: map[string][]string{}}
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
		if p.Type == "string_list" {
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
				if text = strings.TrimSpace(text); text != "" && !seen[text] {
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
		default:
			out[key] = item
		}
	}
	return out
}

func (run *databasePipelineRun) batch(ctx context.Context, operations []any) ([]any, error) {
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
	var out struct {
		Results []any `json:"results"`
	}
	err := sdk.CallAppResultContext(ctx, run.app.ctx.WithProject(run.project).PlatformAPI(), "database", "db_batch", input, &out)
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

func (run *databasePipelineRun) resolveAccess(ctx context.Context) error {
	plan := run.plan
	if len(plan.RequireAny) == 0 && len(plan.Access) == 0 {
		return nil
	}
	identity := securityIdentity(ctx)
	if identity == nil {
		return forbidden("database pipeline requires an authenticated caller")
	}
	held := map[string]bool{}
	for _, permission := range identity.Permissions {
		held[permission] = true
	}
	if claimed, ok := identity.Claims["permissions"].([]any); ok {
		for _, permission := range claimed {
			if text, ok := permission.(string); ok {
				held[strings.TrimSpace(text)] = true
			}
		}
	}
	if len(plan.RequireAny) > 0 {
		allowed := false
		for _, permission := range plan.RequireAny {
			allowed = allowed || held[permission]
		}
		if !allowed {
			return forbidden("caller lacks a permission required by this report")
		}
	}
	// Decide which reads each value needs, then run them in one read batch.
	type pending struct {
		value databasePipelineAccessValue
		read  *databasePipelineRead
		index int
	}
	requested := map[string][]string{}
	var reads []pending
	var operations []any
	for _, value := range plan.Access {
		for _, item := range asAnyList(run.params[strings.TrimPrefix(value.Requested, "$param.")]) {
			requested[value.Name] = append(requested[value.Name], item.(string))
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
		args := map[string]any{"collection": read.Collection, "select": []any{read.Field}, "limit": read.Max + 1}
		if read.Where != nil {
			args["where"] = run.filter(read.Where)
		}
		reads = append(reads, pending{value: value, read: read, index: len(operations)})
		operations = append(operations, map[string]any{"op": "find", "args": args})
	}
	if len(operations) == 0 {
		return nil
	}
	results, err := run.batch(ctx, operations)
	if err != nil {
		return err
	}
	for _, p := range reads {
		page, _ := results[p.index].(map[string]any)
		records, _ := page["records"].([]any)
		if page["hasMore"] == true || len(records) > p.read.Max {
			return runtimeError("source_result_truncated", "access values for "+p.value.Name+" exceed their max")
		}
		values := []string{}
		seen := map[string]bool{}
		for _, raw := range records {
			record, _ := raw.(map[string]any)
			if record[p.read.Field] == nil {
				continue
			}
			text := strings.TrimSpace(fmt.Sprint(record[p.read.Field]))
			if text != "" && !seen[text] {
				seen[text] = true
				values = append(values, text)
			}
		}
		restricted := p.read == p.value.Otherwise
		if restricted && p.value.Single && len(values) != 1 {
			return forbidden("caller does not resolve to exactly one " + p.value.Name)
		}
		if restricted && len(requested[p.value.Name]) > 0 {
			for _, want := range requested[p.value.Name] {
				if !seen[want] {
					return forbidden("requested " + p.value.Name + " are outside the caller's access")
				}
			}
			values = requested[p.value.Name]
		}
		run.scope[p.value.Name] = values
	}
	for _, value := range plan.Access {
		if len(run.scope[value.Name]) == 0 {
			return forbidden("caller has no accessible " + value.Name)
		}
	}
	return nil
}

func asAnyList(value any) []any {
	list, _ := value.([]any)
	return list
}

func (run *databasePipelineRun) readSteps(ctx context.Context) (map[string]any, error) {
	operations := make([]any, len(run.plan.Steps))
	for i, step := range run.plan.Steps {
		args := make(map[string]any, len(step.Args)+1)
		for key, value := range step.Args {
			switch key {
			case "where":
				args[key] = run.filter(value)
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
						copied["where"] = run.filter(metric["where"])
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
		operations[i] = map[string]any{"op": step.Op, "args": args}
	}
	results, err := run.batch(ctx, operations)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(results))
	for i, step := range run.plan.Steps {
		value, err := decodeDatabasePipelineStep(step, results[i])
		if err != nil {
			return nil, err
		}
		out[step.ID] = value
	}
	return out, nil
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
	out := make(map[string]any, len(run.plan.Result))
	for name, ref := range run.plan.Result {
		parts := strings.Split(ref, ".")
		switch parts[0] {
		case "$steps":
			value := steps[parts[1]]
			if len(parts) == 3 {
				rows, _ := value.([]any)
				value = nil
				if len(rows) > 0 {
					value = rows[0]
				}
			}
			out[name] = value
		case "$combine":
			out[name] = combined[parts[1]]
		case "$scope":
			out[name] = run.scope[parts[1]]
		}
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
		if len(c.Fields) == 0 {
			for name, value := range inputs {
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
