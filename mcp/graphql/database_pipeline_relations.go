package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Relational filter nodes test related records of another collection, on
// Database 0.8.0 or later:
//
//	{"exists": {"collection": "sales",
//	  "anyOf": [{"on": [{"localField": "sale_ref", "foreignField": "id", "comparison": "text"}]},
//	            {"on": [{"localField": "id", "foreignField": "call_id"}]}],
//	  "where": {"field": "offer_id", "op": "in", "value": "$scope.offers"}}}
//
// Structure and collections are resolver configuration. Only filter values are
// bound at run time, so a permission over a large relationship no longer has to
// list its matching ids in an access value.
//
// On Database 0.10.0 or later a clause's where can hold further clauses, so a
// filter can follow a chain of relationships. A nested clause's localField is
// read from its immediate parent, which makes the node meaningless on its own:
// it is only ever planned as part of the clause that holds it.
//
// An access_when node selects policy with trusted values only:
//
//	{"access_when": {"op": "gt", "args": [{"op": "length", "args": ["$scope.grants"]}, {"const": 0}]},
//	 "filter": {"exists": {...}}}
//
// It is separate from the client's {when, filter}: its condition and its filter
// never read client input, so a client can't decide whether a restriction
// applies.

const (
	relationExists    = "exists"
	relationNotExists = "not_exists"
	accessWhenKey     = "access_when"

	// Database's limits for one filter. Before 0.11.1 it took 8 relationships.
	maxRelationNodes        = 24
	olderRelationNodes      = 8
	maxRelationDepth        = 4
	maxRelationAlternatives = 16
	maxRelationPairs        = 8
)

// relationClause returns the clause of an exists or not_exists node.
func relationClause(raw any) (string, map[string]any) {
	node, ok := raw.(map[string]any)
	if !ok {
		return "", nil
	}
	for _, key := range []string{relationExists, relationNotExists} {
		if value, exists := node[key]; exists {
			clause, _ := value.(map[string]any)
			return key, clause
		}
	}
	return "", nil
}

func isRelationFilter(raw any) bool {
	key, _ := relationClause(raw)
	return key != ""
}

func isAccessWhenFilter(raw any) bool {
	node, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	_, found := node[accessWhenKey]
	return found
}

// walkFilter visits every node of a filter: groups, optional filters and the
// where of relational nodes.
func walkFilter(raw any, visit func(node map[string]any)) {
	walkOwnFilter(raw, func(node map[string]any) {
		visit(node)
		if _, clause := relationClause(node); clause != nil {
			walkFilter(clause["where"], visit)
		}
	})
}

// walkOwnFilter is walkFilter without the where of relational nodes, whose fields
// belong to another collection. A relational node itself is still visited.
func walkOwnFilter(raw any, visit func(node map[string]any)) {
	node, ok := raw.(map[string]any)
	if !ok {
		return
	}
	visit(node)
	for _, group := range []string{"and", "or"} {
		for _, child := range asAnyList(node[group]) {
			walkOwnFilter(child, visit)
		}
	}
	if isAccessWhenFilter(node) || isConditionalFilter(node) {
		walkOwnFilter(node["filter"], visit)
	}
}

// relationDepth is how many relational nodes the deepest chain in a filter has.
func relationDepth(raw any) int {
	deepest := 0
	walkOwnFilter(raw, func(node map[string]any) {
		if _, clause := relationClause(node); clause != nil {
			deepest = max(deepest, 1+relationDepth(clause["where"]))
		}
	})
	return deepest
}

func filterHasRelation(raw any) bool {
	found := false
	walkFilter(raw, func(node map[string]any) { found = found || isRelationFilter(node) })
	return found
}

// filterHasOptional reports whether a filter holds a when or access_when node.
func filterHasOptional(raw any) bool {
	found := false
	walkFilter(raw, func(node map[string]any) { found = found || isAccessWhenFilter(node) || isConditionalFilter(node) })
	return found
}

// filterHasDirectOptional is filterHasOptional outside the where of a relational
// node, which validateRelation checks on its own.
func filterHasDirectOptional(raw any) bool {
	node, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	if isAccessWhenFilter(node) || isConditionalFilter(node) {
		return true
	}
	for _, group := range []string{"and", "or"} {
		for _, child := range asAnyList(node[group]) {
			if filterHasDirectOptional(child) {
				return true
			}
		}
	}
	return false
}

// relations lists a step's relational nodes at every depth.
func (step databasePipelineStep) relations() []map[string]any {
	found := []map[string]any{}
	walkFilter(step.Args["where"], func(node map[string]any) {
		if isRelationFilter(node) {
			found = append(found, node)
		}
	})
	return found
}

// ownRelations lists the relational nodes that test the step's own collection.
// Each carries any nodes nested in it.
func (step databasePipelineStep) ownRelations() []map[string]any {
	found := []map[string]any{}
	walkOwnFilter(step.Args["where"], func(node map[string]any) {
		if isRelationFilter(node) {
			found = append(found, node)
		}
	})
	return found
}

// untrustedFilterHasRelation looks through filter JSON from outside a validated
// plan, such as a client's where. Any depth and any letter case counts, because
// Database's JSON decoder matches field names case-insensitively.
func untrustedFilterHasRelation(raw any) bool {
	switch value := raw.(type) {
	case map[string]any:
		for key, item := range value {
			if strings.EqualFold(key, relationExists) || strings.EqualFold(key, relationNotExists) || untrustedFilterHasRelation(item) {
				return true
			}
		}
	case []any:
		for _, item := range value {
			if untrustedFilterHasRelation(item) {
				return true
			}
		}
	}
	return false
}

// validateRelation checks an exists or not_exists node.
func (plan *databasePipelinePlan) validateRelation(node map[string]any, place filterPlace, path string) error {
	label := "database_pipeline " + path
	if len(node) != 1 {
		return invalid("%s: exists and not_exists must be the only key", label)
	}
	key, clause := relationClause(node)
	if clause == nil {
		return invalid("%s.%s must be an object", label, key)
	}
	label += "." + key
	if err := onlyKeys(clause, label, "collection", "on", "anyOf", "where"); err != nil {
		return err
	}
	if !literalFieldName(clause["collection"]) {
		return invalid("%s requires a literal collection", label)
	}
	_, hasOn := clause["on"]
	_, hasAnyOf := clause["anyOf"]
	if hasOn == hasAnyOf {
		return invalid("%s requires exactly one of on and anyOf", label)
	}
	if hasOn {
		if err := validateRelationPairs(clause["on"], label+".on"); err != nil {
			return err
		}
	} else {
		alternatives, ok := clause["anyOf"].([]any)
		if !ok || len(alternatives) == 0 || len(alternatives) > maxRelationAlternatives {
			return invalid("%s.anyOf requires 1 to %d alternatives", label, maxRelationAlternatives)
		}
		for i, raw := range alternatives {
			alternative, _ := raw.(map[string]any)
			path := fmt.Sprintf("%s.anyOf[%d]", label, i)
			if alternative == nil {
				return invalid("%s must be an object", path)
			}
			if err := onlyKeys(alternative, path, "on"); err != nil {
				return err
			}
			if err := validateRelationPairs(alternative["on"], path+".on"); err != nil {
				return err
			}
		}
	}
	where, hasWhere := clause["where"]
	if !hasWhere {
		return nil
	}
	// A clause a client when holds only narrows, so a condition may select a branch
	// of an or in it. Anywhere else, client input must not decide what is related.
	narrowing := place.underWhen && key == relationExists
	if !narrowing {
		found := false
		walkFilter(where, func(node map[string]any) { found = found || isConditionalFilter(node) })
		if found {
			return invalid("%s.where cannot hold a client when; client input must not change what counts as related", label)
		}
	}
	if filterHasLikeSearch(where) {
		return invalid("%s.where cannot hold %s; an empty search matches every related record", label, databaseSearchOp)
	}
	// Inside a negation a larger client value would remove more of the
	// exclusion, so client values could widen the result.
	if key == relationNotExists {
		if err := plan.requireTrustedValues(where, label+".where"); err != nil {
			return err
		}
	}
	// A check on the related records stays allowed inside a client when: it is
	// ANDed with what the clause tests, so it can only exclude more.
	return plan.validateFilterIn(where, filterPlace{allowScope: true, underWhen: narrowing}, strings.TrimPrefix(label, "database_pipeline ")+".where")
}

func validateRelationPairs(raw any, label string) error {
	pairs, ok := raw.([]any)
	if !ok || len(pairs) == 0 || len(pairs) > maxRelationPairs {
		return invalid("%s requires 1 to %d pairs", label, maxRelationPairs)
	}
	for i, item := range pairs {
		pair, _ := item.(map[string]any)
		path := fmt.Sprintf("%s[%d]", label, i)
		if pair == nil {
			return invalid("%s must be an object", path)
		}
		if err := onlyKeys(pair, path, "localField", "foreignField", "comparison"); err != nil {
			return err
		}
		if !literalFieldName(pair["localField"]) || !literalFieldName(pair["foreignField"]) {
			return invalid("%s requires a literal localField and foreignField", path)
		}
		if comparison, found := pair["comparison"]; found && comparison != "text" {
			return invalid("%s comparison must be text when set", path)
		}
	}
	return nil
}

func literalFieldName(raw any) bool {
	name, ok := raw.(string)
	return ok && strings.TrimSpace(name) != "" && !strings.HasPrefix(name, "$")
}

// validateAccessWhen checks an access_when node.
func (plan *databasePipelinePlan) validateAccessWhen(node map[string]any, path string) error {
	label := "database_pipeline " + path
	if err := onlyKeys(node, label, accessWhenKey, "filter"); err != nil {
		return err
	}
	if node[accessWhenKey] == nil || node["filter"] == nil {
		return invalid("%s requires both access_when and filter", label)
	}
	kind, err := plan.conditionType(node[accessWhenKey], 0, true)
	if err != nil {
		return invalid("%s.access_when: %s", label, err)
	}
	if kind != conditionBoolean {
		return invalid("%s.access_when must be Boolean, not %s", label, kind)
	}
	if filterHasDirectOptional(node["filter"]) {
		return invalid("%s.filter cannot hold another when or access_when", label)
	}
	if err := plan.requireTrustedValues(node["filter"], label+".filter"); err != nil {
		return err
	}
	return plan.validateFilter(node["filter"], true, path+".filter")
}

// requireTrustedValues refuses client input in a filter: every value must be a
// literal, an access value, a fixed param or a verified identity param.
func (plan *databasePipelinePlan) requireTrustedValues(raw any, label string) error {
	var failure error
	var check func(value any)
	check = func(value any) {
		switch typed := value.(type) {
		case string:
			if name, ok := strings.CutPrefix(typed, "$param."); ok && failure == nil {
				if param, declared := plan.Params[name]; declared && !param.HasValue && !strings.HasPrefix(param.From, "$identity.") {
					failure = invalid("%s reads %s, which comes from client input; use access values, identity params or fixed values", label, typed)
				}
			}
		case []any:
			for _, item := range typed {
				check(item)
			}
		case map[string]any:
			for _, item := range typed {
				check(item)
			}
		}
	}
	walkFilter(raw, func(node map[string]any) { check(node["value"]) })
	return failure
}

// probeFilter keeps a relational filter's structure and fields for validation,
// with no values: those are only known once a request binds them.
func probeFilter(raw any) any {
	node, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	if isAccessWhenFilter(node) || isConditionalFilter(node) {
		return probeFilter(node["filter"])
	}
	if isRelationFilter(node) {
		return probeRelation(node)
	}
	// Keeps the operator, so Database checks it and the field's type against the
	// collection the field belongs to.
	if isLiteralSearch(node) {
		return map[string]any{"field": node["field"], "op": databaseLiteralSearchOp, "value": ""}
	}
	for _, group := range []string{"and", "or"} {
		if children, found := node[group].([]any); found {
			kept := []any{}
			for _, child := range children {
				if probe := probeFilter(child); probe != nil {
					kept = append(kept, probe)
				}
			}
			if len(kept) == 0 {
				return nil
			}
			return map[string]any{group: kept}
		}
	}
	return map[string]any{"field": node["field"], "op": "is_null"}
}

// probeRelation is a relational node without bound values.
func probeRelation(node map[string]any) map[string]any {
	key, clause := relationClause(node)
	copied := make(map[string]any, len(clause))
	for name, value := range clause {
		copied[name] = value
	}
	delete(copied, "where")
	if probe := probeFilter(clause["where"]); probe != nil {
		copied["where"] = probe
	}
	return map[string]any{key: copied}
}

// explainRelation asks Database to plan a relational node without running it:
// the related collection, every field, comparison types and the index a probe
// needs are all checked there.
func (run *databasePipelineRun) explainRelation(ctx context.Context, collection any, node map[string]any) error {
	return run.explainFilter(ctx, collection, probeRelation(node))
}

func (run *databasePipelineRun) explainFilter(ctx context.Context, collection, where any) error {
	var out map[string]any
	return sdk.CallAppResultContext(ctx, run.app.ctx.WithProject(run.project).PlatformAPI(), "database", "db_explain", map[string]any{
		"database":    run.plan.Database,
		"scope":       run.plan.Scope,
		"collection":  collection,
		"where":       where,
		"_project_id": run.project,
	}, &out)
}

// verifyRelations checks each relational node of a validated plan on Database:
// nested nodes within the clause that holds them, against its collection.
func (run *databasePipelineRun) verifyRelations(ctx context.Context) error {
	for _, step := range run.plan.Steps {
		for _, node := range step.ownRelations() {
			key, clause := relationClause(node)
			probe, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := run.explainRelation(probe, step.Args["collection"], node)
			cancel()
			if err != nil {
				return fmt.Errorf("Database cannot plan its %s on %v (requires Database 0.8.0 or later, 0.10.0 for nested relationships, 0.11.0 for %s, existing fields of the right type and an index on a related field of each match): %w", key, clause["collection"], databaseLiteralSearchOp, err)
			}
		}
		// A request can use every relationship at once, and an older Database takes
		// fewer than this limit, so it is asked about them together. Plans within
		// the older limit never needed this.
		if count := len(step.relations()); count > olderRelationNodes {
			whole := make([]any, 0, count)
			for _, node := range step.ownRelations() {
				whole = append(whole, probeRelation(node))
			}
			probe, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := run.explainFilter(probe, step.Args["collection"], map[string]any{"and": whole})
			cancel()
			if err != nil {
				return fmt.Errorf("Database cannot take the %d exists and not_exists filters one request can use (requires Database 0.11.1 or later): %w", count, err)
			}
		}
	}
	return nil
}
