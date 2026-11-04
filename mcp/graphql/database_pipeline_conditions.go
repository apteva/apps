package main

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Conditional filter nodes make an additional query restriction optional:
//
//	{"and": [{"field": "centre_id", "op": "in", "value": "$scope.centres"},
//	         {"when": {"op": "gt", "args": [{"op": "length", "args": ["$param.selected"]}, {"const": 0}]},
//	          "filter": {"field": "centre_id", "op": "in", "value": "$param.selected"}}]}
//
// A false condition removes the node from its and group, so it can only ever
// lift a restriction written next to the condition. Conditions take client
// input, which is why neither part may read $scope or an identity param: every
// authorization predicate stays an unconditional sibling. An ordinary empty in
// keeps meaning "match nothing".

const maxConditionDepth = 16

const (
	conditionBoolean = "boolean"
	conditionNumber  = "number"
	conditionString  = "string"
	conditionList    = "list"
)

// isConditionalFilter reports whether a filter node has the {when, filter} shape.
func isConditionalFilter(raw any) bool {
	node, ok := raw.(map[string]any)
	if !ok || isAccessWhenFilter(node) {
		return false
	}
	_, hasWhen := node["when"]
	_, hasFilter := node["filter"]
	return hasWhen || hasFilter
}

// filterConditionals lists the client conditional nodes of a filter.
func filterConditionals(raw any) []map[string]any {
	found := []map[string]any{}
	walkFilter(raw, func(node map[string]any) {
		if isConditionalFilter(node) {
			found = append(found, node)
		}
	})
	return found
}

// conditionals lists a step's conditional nodes, in where and in metric filters.
func (step databasePipelineStep) conditionals() []map[string]any {
	found := filterConditionals(step.Args["where"])
	for _, raw := range asAnyList(step.Args["metrics"]) {
		metric, _ := raw.(map[string]any)
		found = append(found, filterConditionals(metric["where"])...)
	}
	return found
}

// validateConditional checks a {when, filter} node. The nested filter is
// validated whatever the condition would evaluate to.
func (plan *databasePipelinePlan) validateConditional(node map[string]any, path string) error {
	label := "database_pipeline " + path
	if err := onlyKeys(node, label, "when", "filter"); err != nil {
		return err
	}
	if node["when"] == nil || node["filter"] == nil {
		return invalid("%s requires both when and filter", label)
	}
	kind, err := plan.conditionType(node["when"], 0, false)
	if err != nil {
		return invalid("%s.when: %s", label, err)
	}
	if kind != conditionBoolean {
		return invalid("%s.when must be Boolean, not %s", label, kind)
	}
	if filterHasOptional(node["filter"]) {
		return invalid("%s.filter cannot contain another conditional filter", label)
	}
	if filterHasRelation(node["filter"]) {
		return invalid("%s.filter cannot hold exists or not_exists; relationships are policy, so use access_when if one needs selecting", label)
	}
	if err := plan.validateConditionalValues(node["filter"], label+".filter"); err != nil {
		return err
	}
	return plan.validateFilter(node["filter"], false, path+".filter")
}

// conditionalParam resolves a param a conditional node may read: a top-level
// client argument or a fixed value, never identity or parent data.
func (plan *databasePipelinePlan) conditionalParam(reference string) (databasePipelineParam, error) {
	name := strings.TrimPrefix(reference, "$param.")
	param, declared := plan.Params[name]
	if !declared {
		return param, fmt.Errorf("references undeclared %s", reference)
	}
	if strings.HasPrefix(param.From, "$identity.") {
		return param, fmt.Errorf("%s is an identity value; a conditional filter must not decide authorization", reference)
	}
	if !param.HasValue {
		if argument, ok := strings.CutPrefix(param.From, "$args."); !ok || !graphqlName(argument) {
			return param, fmt.Errorf("%s must come from a top-level $args value or a fixed value", reference)
		}
	}
	return param, nil
}

// validateConditionalValues applies the rules of a conditional filter's leaves.
func (plan *databasePipelinePlan) validateConditionalValues(raw any, label string) error {
	node, ok := raw.(map[string]any)
	if !ok {
		return invalid("%s must be a filter object", label)
	}
	for _, group := range []string{"and", "or"} {
		if children, exists := node[group]; exists {
			for i, child := range asAnyList(children) {
				if err := plan.validateConditionalValues(child, fmt.Sprintf("%s.%s[%d]", label, group, i)); err != nil {
					return err
				}
			}
			return nil
		}
	}
	op, _ := node["op"].(string)
	list := false
	references := []string{}
	switch value := node["value"].(type) {
	case string:
		if strings.HasPrefix(value, "$") {
			references = append(references, value)
		}
	case []any:
		list = true
		for _, item := range value {
			switch entry := item.(type) {
			case string:
				if strings.HasPrefix(entry, "$") {
					references = append(references, entry)
				}
			case []any, map[string]any:
				return invalid("%s: list values must be scalars", label)
			}
		}
	case map[string]any:
		return invalid("%s: value must be a scalar, a list or a $param", label)
	}
	for _, reference := range references {
		if strings.HasPrefix(reference, "$scope.") {
			return invalid("%s cannot read %s; authorization filters must stay unconditional", label, reference)
		}
		if !strings.HasPrefix(reference, "$param.") {
			continue // reported by validateFilter
		}
		param, err := plan.conditionalParam(reference)
		if err != nil {
			return invalid("%s: %s", label, err)
		}
		if param.Type == "string_list" {
			return invalid("%s: %s must be exact_string_list; string_list trims values and drops blanks, which can remove the filter", label, reference)
		}
		if param.Type == "exact_string_list" {
			if list {
				return invalid("%s: %s cannot be a list item", label, reference)
			}
			list = true
		}
	}
	if (op == "in" || op == "not_in") && !list {
		return invalid("%s: %s requires a list or an exact_string_list $param", label, op)
	}
	if op != "in" && op != "not_in" && list {
		return invalid("%s: %s does not take a list", label, op)
	}
	return nil
}

// trustedConditionParam resolves a param an access_when may read: a verified
// identity value or a fixed value, never client input.
func (plan *databasePipelinePlan) trustedConditionParam(reference string) (databasePipelineParam, error) {
	param, declared := plan.Params[strings.TrimPrefix(reference, "$param.")]
	if !declared {
		return param, fmt.Errorf("references undeclared %s", reference)
	}
	if !param.HasValue && !strings.HasPrefix(param.From, "$identity.") {
		return param, fmt.Errorf("%s comes from client input; use $scope values, identity params or fixed values", reference)
	}
	return param, nil
}

// conditionType returns the type an expression produces. Types are checked
// here so a mismatch refuses the release instead of depending on the data.
// A trusted expression (access_when) reads access values and identity params; a
// client condition (when) reads client arguments and neither of those.
func (plan *databasePipelinePlan) conditionType(raw any, depth int, trusted bool) (string, error) {
	if depth > maxConditionDepth {
		return "", fmt.Errorf("expression is too deep")
	}
	switch node := raw.(type) {
	case string:
		if name, isScope := strings.CutPrefix(node, "$scope."); isScope {
			if !trusted {
				return "", fmt.Errorf("cannot read %s; a condition must not depend on authorization", node)
			}
			if !plan.accessIndex[name] {
				return "", fmt.Errorf("references unknown %s", node)
			}
			return conditionList, nil
		}
		if !strings.HasPrefix(node, "$param.") {
			return "", fmt.Errorf("%q is not a $param reference; write constants as {\"const\": ...}", node)
		}
		if trusted {
			param, err := plan.trustedConditionParam(node)
			if err != nil {
				return "", err
			}
			switch param.Type {
			case conditionString, conditionNumber, conditionBoolean:
				return param.Type, nil
			case "string_list", "exact_string_list":
				return conditionList, nil
			}
			return "", fmt.Errorf("%s has type %s, which conditions cannot use", node, param.Type)
		}
		param, err := plan.conditionalParam(node)
		if err != nil {
			return "", err
		}
		switch param.Type {
		case conditionString, conditionNumber, conditionBoolean:
			return param.Type, nil
		case "exact_string_list":
			return conditionList, nil
		case "string_list":
			return "", fmt.Errorf("%s must be exact_string_list; string_list trims values and drops blanks, which can remove the filter", node)
		}
		return "", fmt.Errorf("%s has type %s, which conditions cannot use; use a string param for optional dates", node, param.Type)
	case map[string]any:
		if value, ok := node["const"]; ok {
			if len(node) != 1 {
				return "", fmt.Errorf("const must stand alone")
			}
			return conditionConstType(value)
		}
		return plan.conditionOpType(node, depth, trusted)
	}
	return "", fmt.Errorf("a condition is a $param reference, {\"const\": ...} or {\"op\": ..., \"args\": [...]}")
}

func conditionConstType(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return conditionString, nil
	case bool:
		return conditionBoolean, nil
	case []any:
		for _, item := range typed {
			if _, ok := item.(string); !ok {
				return "", fmt.Errorf("a const list holds only strings")
			}
		}
		return conditionList, nil
	case nil:
		return "", fmt.Errorf("const cannot be null; use a default or coalesce")
	}
	if _, err := moduleNumber(value); err != nil {
		return "", fmt.Errorf("const must be a string, number, Boolean or list of strings")
	}
	return conditionNumber, nil
}

func (plan *databasePipelinePlan) conditionOpType(node map[string]any, depth int, trusted bool) (string, error) {
	if err := onlyKeys(node, "condition", "op", "args"); err != nil {
		return "", err
	}
	op, _ := node["op"].(string)
	args, ok := node["args"].([]any)
	if !ok {
		return "", fmt.Errorf("op %q requires an args list", op)
	}
	kinds := make([]string, len(args))
	switch {
	case op == "in" && trusted:
	case op == "length", op == "not", op == "and", op == "or", op == "coalesce", op == "eq", op == "neq", op == "lt", op == "lte", op == "gt", op == "gte":
	default:
		return "", fmt.Errorf("op %q is not allowed in a condition", op)
	}
	for i, arg := range args {
		kind, err := plan.conditionType(arg, depth+1, trusted)
		if err != nil {
			return "", err
		}
		kinds[i] = kind
	}
	switch op {
	case "in":
		if len(kinds) != 2 || kinds[1] != conditionList || (kinds[0] != conditionString && kinds[0] != conditionNumber) {
			return "", fmt.Errorf("in requires a value and a list")
		}
		return conditionBoolean, nil
	case "length":
		if len(kinds) != 1 || (kinds[0] != conditionList && kinds[0] != conditionString) {
			return "", fmt.Errorf("length requires one list or string")
		}
		return conditionNumber, nil
	case "not":
		if len(kinds) != 1 || kinds[0] != conditionBoolean {
			return "", fmt.Errorf("not requires one Boolean")
		}
		return conditionBoolean, nil
	case "and", "or":
		if len(kinds) == 0 {
			return "", fmt.Errorf("%s requires Booleans", op)
		}
		for _, kind := range kinds {
			if kind != conditionBoolean {
				return "", fmt.Errorf("%s requires Booleans, not %s", op, kind)
			}
		}
		return conditionBoolean, nil
	case "coalesce":
		if len(kinds) < 2 {
			return "", fmt.Errorf("coalesce requires at least two values")
		}
		for _, kind := range kinds[1:] {
			if kind != kinds[0] {
				return "", fmt.Errorf("coalesce mixes %s and %s", kinds[0], kind)
			}
		}
		return kinds[0], nil
	}
	if len(kinds) != 2 || kinds[0] != kinds[1] {
		return "", fmt.Errorf("%s requires two values of the same type", op)
	}
	if kinds[0] == conditionList || (kinds[0] == conditionBoolean && op != "eq" && op != "neq") {
		return "", fmt.Errorf("%s cannot compare %s values", op, kinds[0])
	}
	return conditionBoolean, nil
}

// evalCondition evaluates a validated expression. Every operand is evaluated,
// so a missing value fails the request whichever way the others came out.
// Access values are lists, and an empty one is not a missing value.
func evalCondition(raw any, params map[string]any, scope map[string][]string) (any, error) {
	switch node := raw.(type) {
	case string:
		if name, isScope := strings.CutPrefix(node, "$scope."); isScope {
			list := make([]any, len(scope[name]))
			for i, item := range scope[name] {
				list[i] = item
			}
			return list, nil
		}
		return params[strings.TrimPrefix(node, "$param.")], nil
	case map[string]any:
		if value, ok := node["const"]; ok {
			return value, nil
		}
		op, _ := node["op"].(string)
		args := asAnyList(node["args"])
		values := make([]any, len(args))
		for i, arg := range args {
			value, err := evalCondition(arg, params, scope)
			if err != nil {
				return nil, err
			}
			values[i] = value
		}
		if op == "coalesce" {
			for _, value := range values {
				if value != nil {
					return value, nil
				}
			}
			return nil, nil
		}
		for i, value := range values {
			if value == nil {
				if name, ok := args[i].(string); ok {
					return nil, invalid("database_pipeline condition: %s has no value; give the argument a default or use coalesce", name)
				}
				return nil, invalid("database_pipeline condition: %s received no value", op)
			}
		}
		return applyConditionOp(op, values)
	}
	return nil, invalid("database_pipeline condition is invalid")
}

func applyConditionOp(op string, values []any) (any, error) {
	switch op {
	case "in":
		list, ok := values[1].([]any)
		if !ok {
			return nil, invalid("database_pipeline condition: in requires a list")
		}
		for _, item := range list {
			if fmt.Sprint(item) == fmt.Sprint(values[0]) {
				return true, nil
			}
		}
		return false, nil
	case "length":
		switch value := values[0].(type) {
		case string:
			return int64(utf8.RuneCountInString(value)), nil
		case []any:
			return int64(len(value)), nil
		}
		return nil, invalid("database_pipeline condition: length requires a list or string")
	case "not", "and", "or":
		result := op == "and"
		for _, value := range values {
			flag, ok := value.(bool)
			if !ok {
				return nil, invalid("database_pipeline condition: %s requires Booleans", op)
			}
			switch op {
			case "not":
				result = !flag
			case "and":
				result = result && flag
			case "or":
				result = result || flag
			}
		}
		return result, nil
	}
	comparison, err := compareConditionValues(values[0], values[1])
	if err != nil {
		return nil, err
	}
	switch op {
	case "eq":
		return comparison == 0, nil
	case "neq":
		return comparison != 0, nil
	case "lt":
		return comparison < 0, nil
	case "lte":
		return comparison <= 0, nil
	case "gt":
		return comparison > 0, nil
	case "gte":
		return comparison >= 0, nil
	}
	return nil, invalid("database_pipeline condition: op %q is not allowed", op)
}

// compareConditionValues compares values of one type. Unlike resolver modules
// it never falls back to comparing their text.
func compareConditionValues(left, right any) (int, error) {
	mismatch := invalid("database_pipeline condition compares %T with %T", left, right)
	switch l := left.(type) {
	case string:
		r, ok := right.(string)
		if !ok {
			return 0, mismatch
		}
		return strings.Compare(l, r), nil
	case bool:
		r, ok := right.(bool)
		if !ok {
			return 0, mismatch
		}
		if l == r {
			return 0, nil
		}
		return 1, nil
	}
	switch right.(type) {
	case string, bool:
		return 0, mismatch
	}
	l, err := moduleNumber(left)
	if err != nil {
		return 0, mismatch
	}
	r, err := moduleNumber(right)
	if err != nil {
		return 0, mismatch
	}
	return l.Cmp(r), nil
}

// conditionHolds evaluates a condition that must produce a Boolean.
func (run *databasePipelineRun) conditionHolds(condition any) (bool, error) {
	value, err := evalCondition(condition, run.params, run.scope)
	if err != nil {
		return false, err
	}
	holds, ok := value.(bool)
	if !ok {
		return false, invalid("database_pipeline condition must evaluate to a Boolean")
	}
	return holds, nil
}

// resolveConditions removes false conditional children and replaces true ones
// with their filter, including in the where of a relational node. It returns nil
// only when conditions emptied the group; an empty group written in the config
// is left for Database to refuse.
func (run *databasePipelineRun) resolveConditions(raw any) (any, error) {
	node, ok := raw.(map[string]any)
	if !ok {
		return raw, nil
	}
	if key, clause := relationClause(node); clause != nil {
		where, found := clause["where"]
		if !found {
			return raw, nil
		}
		resolved, err := run.resolveConditions(where)
		if err != nil {
			return nil, err
		}
		copied := make(map[string]any, len(clause))
		for name, value := range clause {
			copied[name] = value
		}
		copied["where"] = resolved
		if resolved == nil {
			delete(copied, "where")
		}
		return map[string]any{key: copied}, nil
	}
	children, isAnd := node["and"].([]any)
	if !isAnd || len(children) == 0 {
		return raw, nil
	}
	kept := make([]any, 0, len(children))
	for _, child := range children {
		optional, _ := child.(map[string]any)
		if isConditionalFilter(child) || isAccessWhenFilter(child) {
			condition := optional["when"]
			if isAccessWhenFilter(child) {
				condition = optional[accessWhenKey]
			}
			holds, err := run.conditionHolds(condition)
			if err != nil {
				return nil, err
			}
			if !holds {
				continue
			}
			child = optional["filter"]
		}
		resolved, err := run.resolveConditions(child)
		if err != nil {
			return nil, err
		}
		if resolved != nil {
			kept = append(kept, resolved)
		}
	}
	if len(kept) == 0 {
		return nil, nil
	}
	return map[string]any{"and": kept}, nil
}

// where binds a step or metric filter: conditions first, then values. It
// returns nil when conditions removed every predicate.
func (run *databasePipelineRun) where(raw any) (any, error) {
	resolved, err := run.resolveConditions(raw)
	if err != nil || resolved == nil {
		return nil, err
	}
	return run.filter(resolved), nil
}

// conditionalArguments lists the GraphQL arguments conditional filters read,
// with whether each must be a list.
func (plan *databasePipelinePlan) conditionalArguments() map[string]bool {
	out := map[string]bool{}
	var visit func(raw any)
	visit = func(raw any) {
		switch value := raw.(type) {
		case string:
			if param, ok := plan.Params[strings.TrimPrefix(value, "$param.")]; ok && strings.HasPrefix(value, "$param.") {
				if argument, ok := strings.CutPrefix(param.From, "$args."); ok {
					out[argument] = param.Type == "exact_string_list"
				}
			}
		case []any:
			for _, item := range value {
				visit(item)
			}
		case map[string]any:
			for key, item := range value {
				if key != "field" && key != "fields" && key != "op" && key != "const" {
					visit(item)
				}
			}
		}
	}
	for _, step := range plan.Steps {
		for _, conditional := range step.conditionals() {
			visit(conditional)
		}
	}
	return out
}

// conditionalFields lists the fields a step's conditional filters use, without
// searches, which are probed on their own, or relational nodes, which are
// planned on their own. A false condition never sends these to Database.
func (step databasePipelineStep) conditionalFields() []any {
	seen := map[string]bool{}
	var visit func(raw any)
	visit = func(raw any) {
		node, _ := raw.(map[string]any)
		for _, group := range []string{"and", "or"} {
			for _, child := range asAnyList(node[group]) {
				visit(child)
			}
		}
		if field, ok := node["field"].(string); ok {
			seen[field] = true
		}
	}
	for _, conditional := range step.conditionals() {
		visit(conditional["filter"])
	}
	// Inside a relational node the fields are the related collection's, and are
	// planned with that node.
	walkOwnFilter(step.Args["where"], func(node map[string]any) {
		if isAccessWhenFilter(node) {
			visit(node["filter"])
		}
	})
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]any, len(names))
	for i, name := range names {
		out[i] = name
	}
	return out
}
