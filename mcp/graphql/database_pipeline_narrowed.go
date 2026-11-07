package main

import (
	"fmt"
	"sort"
	"strings"
)

// A client can narrow an access value with `requested`, directly or through a value
// derived from it. That's safe where a smaller list only returns fewer rows, as in
// `centre in $scope.centres`. Everywhere else a smaller list can return more: it can
// switch off a condition, shrink what a not_exists rules out, or lift a mask. So a
// narrowed value only ever reaches positive filters.

// markClientNarrowed records which access values a client can narrow, and through
// which params, then refuses the uses it can already see in the access values.
func (plan *databasePipelinePlan) markClientNarrowed() error {
	plan.narrowedBy = map[string][]string{}
	for changed := true; changed; {
		changed = false
		for _, value := range plan.Access {
			params := map[string]bool{}
			for _, name := range plan.narrowedBy[value.Name] {
				params[name] = true
			}
			before := len(params)
			if name, ok := strings.CutPrefix(value.Requested, "$param."); ok && plan.isClientParam(name) {
				params[name] = true
			}
			for _, dep := range value.deps {
				for _, name := range plan.narrowedBy[dep] {
					params[name] = true
				}
			}
			if len(params) > before {
				changed = true
				plan.narrowedBy[value.Name] = sortedNames(params)
			}
		}
	}
	for _, value := range plan.Access {
		label := "database_pipeline access value " + value.Name
		if name := plan.narrowedInCondition(value.Expr); name != "" {
			return invalid("%s decides on $scope.%s, which a client can narrow; a condition must not depend on client input", label, name)
		}
		for _, read := range []*databasePipelineRead{value.Default, value.Otherwise} {
			if read == nil {
				continue
			}
			if err := plan.refuseNarrowedNegation(read.Where, label); err != nil {
				return err
			}
		}
	}
	return nil
}

func sortedNames(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (plan *databasePipelinePlan) isClientParam(name string) bool {
	param, declared := plan.Params[name]
	return declared && !param.HasValue && !strings.HasPrefix(param.From, "$identity.")
}

// walkStrings visits every string inside a value.
func walkStrings(raw any, visit func(text string)) {
	switch typed := raw.(type) {
	case string:
		visit(typed)
	case []any:
		for _, item := range typed {
			walkStrings(item, visit)
		}
	case map[string]any:
		for _, item := range typed {
			walkStrings(item, visit)
		}
	}
}

// narrowedIn names a client-narrowable access value that a value reads, if any.
func (plan *databasePipelinePlan) narrowedIn(raw any) string {
	found := ""
	walkStrings(raw, func(text string) {
		if name, ok := strings.CutPrefix(text, "$scope."); ok && found == "" && len(plan.narrowedBy[name]) > 0 {
			found = name
		}
	})
	return found
}

// narrowedInCondition finds a narrowable value an expr decides on. Passing a list
// through as an if's result only narrows; reading it anywhere else may not.
func (plan *databasePipelinePlan) narrowedInCondition(raw any) string {
	node, isNode := raw.(map[string]any)
	if !isNode {
		return ""
	}
	if _, isInput := node["input"]; isInput {
		return ""
	}
	if node["op"] != "if" {
		return plan.narrowedIn(node)
	}
	if name := plan.narrowedIn(node["condition"]); name != "" {
		return name
	}
	if name := plan.narrowedInCondition(node["then"]); name != "" {
		return name
	}
	return plan.narrowedInCondition(node["else"])
}

// refuseNarrowedNegation refuses a narrowable value where a shorter list returns more
// rows. A relationship's where is covered by requireTrustedValues, including a
// not_exists, and access reads can't hold relationships.
func (plan *databasePipelinePlan) refuseNarrowedNegation(raw any, label string) error {
	var failure error
	walkFilter(raw, func(node map[string]any) {
		op, _ := node["op"].(string)
		if failure != nil || (op != "not_in" && op != "ne") {
			return
		}
		if name := plan.narrowedIn(node["value"]); name != "" {
			failure = invalid("%s reads $scope.%s in %s, where a client narrowing it would return more rows; use an access value without requested", label, name, op)
		}
	})
	return failure
}

// requireExactConditions refuses an access condition on a normalised list. A normal
// list drops blanks, so a grant holding only a blank would read as no grant at all,
// and a "no grant means unrestricted" condition would lift the restriction.
func (plan *databasePipelinePlan) requireExactConditions(condition any, label string) error {
	var failure error
	walkStrings(condition, func(text string) {
		name, ok := strings.CutPrefix(text, "$scope.")
		if !ok || failure != nil {
			return
		}
		for _, value := range plan.Access {
			if value.Name == name && !value.Exact {
				failure = invalid("%s decides on $scope.%s, which must be type exact_string_list: a normal list drops blanks, so a blank grant would read as no grant", label, name)
			}
		}
	})
	return failure
}

// clientInputsOf lists the client params a filter depends on, directly or through a
// narrowable access value.
func (plan *databasePipelinePlan) clientInputsOf(raw any) []string {
	found := map[string]bool{}
	walkStrings(raw, func(text string) {
		if name, ok := strings.CutPrefix(text, "$param."); ok && plan.isClientParam(name) {
			found[name] = true
		}
		if name, ok := strings.CutPrefix(text, "$scope."); ok {
			for _, param := range plan.narrowedBy[name] {
				found[param] = true
			}
		}
	})
	return sortedNames(found)
}

// distinctKeepsAWinner reports whether a distinct keeps one row per field value, the
// way a pick does. Collapsing the joined rows of one physical record picks nothing.
func distinctKeepsAWinner(owner map[string]any) bool {
	alias, _ := owner["as"].(string)
	if alias == "" {
		alias = "root"
	}
	records := map[string]bool{alias: true}
	for _, raw := range asAnyList(owner["joins"]) {
		join, _ := raw.(map[string]any)
		if name, isText := join["as"].(string); isText {
			records[name] = true
		}
	}
	for _, raw := range asAnyList(owner["distinct"]) {
		if name, _ := raw.(string); !records[name] {
			return true
		}
	}
	return false
}

// validateJoinedStages checks what feeds pick, rollup and a distinct by field. Client
// input in the where or filter before them doesn't only narrow: it can change which
// row wins, or make a mixed record look single to a rollup. A plan can name params it
// means to allow, such as a report's date window, in stage_params.
func (plan *databasePipelinePlan) validateJoinedStages(step *databasePipelineStep, args map[string]any, label string) error {
	allowed := map[string]bool{}
	for _, name := range step.StageParams {
		if !plan.isClientParam(name) {
			return invalid("%s stage_params names %q, which is not a client param", label, name)
		}
		allowed[name] = true
	}
	used := args["pipeline"] != nil
	check := func(owner map[string]any, path string) error {
		used = true
		for _, key := range []string{"where", "filter"} {
			for _, param := range plan.clientInputsOf(owner[key]) {
				if !allowed[param] {
					return invalid("%s.%s reads client param %s before a pick, rollup or distinct, where it could change which row wins or which records pass. List it in stage_params if the plan means it, such as a date window", path, key, param)
				}
			}
		}
		return nil
	}
	top := args["pick"] != nil || args["rollup"] != nil || distinctKeepsAWinner(args)
	if top {
		if err := check(args, label); err != nil {
			return err
		}
	}
	for i, raw := range asAnyList(args["branches"]) {
		branch, _ := raw.(map[string]any)
		if branch != nil && (top || branch["pick"] != nil || distinctKeepsAWinner(branch)) {
			if err := check(branch, fmt.Sprintf("%s.branches[%d]", label, i)); err != nil {
				return err
			}
		}
	}
	if len(step.StageParams) > 0 && !used {
		return invalid("%s stage_params needs a pick, rollup or distinct by field in the step", label)
	}
	return nil
}

// validateJoinedRoots refuses a read whose only restrictions are conditional: a viewer
// none of them applies to would read every record. A branch answers for itself, along
// with any filter over all branches.
func (plan *databasePipelinePlan) validateJoinedRoots(args map[string]any, label string) error {
	check := func(path string, owners ...map[string]any) error {
		restricted, standing := false, false
		for _, owner := range owners {
			for _, key := range []string{"where", "filter"} {
				walkOwnFilter(owner[key], func(node map[string]any) { restricted = restricted || isAccessWhenFilter(node) })
				standing = standing || plan.filterAlwaysApplies(owner[key])
			}
		}
		if restricted && !standing {
			return invalid("%s only holds conditional restrictions, so a viewer none applies to would read every record; add a term that always applies, such as the viewer's scope", path)
		}
		return nil
	}
	if pipeline, ok := args["pipeline"].(map[string]any); ok {
		for i, raw := range asAnyList(pipeline["nodes"]) {
			node, _ := raw.(map[string]any)
			if node["op"] == "scan" {
				if err := check(fmt.Sprintf("%s.pipeline.nodes[%d]", label, i), node); err != nil {
					return err
				}
			}
		}
	}
	branches := asAnyList(args["branches"])
	if len(branches) == 0 {
		return check(label, args)
	}
	for i, raw := range branches {
		branch, _ := raw.(map[string]any)
		if err := check(fmt.Sprintf("%s.branches[%d]", label, i), branch, args); err != nil {
			return err
		}
	}
	return nil
}

// filterAlwaysApplies reports whether part of a filter stays whatever conditions
// decide, and isn't just a value the client chose.
func (plan *databasePipelinePlan) filterAlwaysApplies(raw any) bool {
	node, isNode := raw.(map[string]any)
	if !isNode || isAccessWhenFilter(node) || isConditionalFilter(node) {
		return false
	}
	if children, isAnd := node["and"].([]any); isAnd {
		for _, child := range children {
			if plan.filterAlwaysApplies(child) {
				return true
			}
		}
		return false
	}
	if children, isOr := node["or"].([]any); isOr {
		for _, child := range children {
			if !plan.filterAlwaysApplies(child) {
				return false
			}
		}
		return len(children) > 0
	}
	applies := true
	walkStrings(node, func(text string) {
		if name, ok := strings.CutPrefix(text, "$param."); ok && plan.isClientParam(name) {
			applies = false
		}
	})
	return applies
}
