package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
)

// Joined reads expose Database's related_read, joined_page and joined_aggregate
// (Database 0.12.0) as pipeline steps. Their args keep Database's request shape:
//
//	{"id": "page", "op": "related_read",
//	 "args": {"collection": "reports", "where": {...}, "select": ["id"],
//	          "relation": {"as": "context", "limit": 40, "sources": [...]},
//	          "match": {...}, "count": true},
//	 "page": {"size": "$param.limit", "offset": "$param.offset"},
//	 "shape": {"total_as": "total_count", "json_text": ["context"]}}
//
// Everything is server configuration by default. A client value can only reach
// the slots that narrow a result: the root where, match, the filter over joined
// rows, an outer metric's where and a branch's where and filter. Eligibility,
// that is the where of a join, a source's filter, pick, rollup and a computed
// value's if, takes literals, access values and identity only, because a client
// value there could change which rows take a slot or count as related.
const (
	joinedRelatedRead = "related_read"
	joinedPage        = "joined_page"
	joinedAggregate   = "joined_aggregate"

	// Database's limits, repeated so the report names the step.
	maxJoinedRelationLimit = 1000
	maxJoinedSources       = 4
	maxJoinedOffset        = 100000
)

func isJoinedOp(op string) bool {
	return op == joinedRelatedRead || op == joinedPage || op == joinedAggregate
}

func (plan *databasePipelinePlan) hasJoinedReads() bool {
	for _, step := range plan.Steps {
		if isJoinedOp(step.Op) {
			return true
		}
	}
	return false
}

// joinedSlot is one filter inside a joined read.
type joinedSlot struct {
	path   string
	client bool // may read client values, as narrowing only
	match  bool // tested on a parent's bounded related rows
	// condition is the if of a computed value, which can't be left without one.
	condition bool
}

// joinedVisitor walks a joined request. filter sees every filter slot and returns
// what to keep there (nil removes it). collection sees every collection named.
// The walk copies what it passes through, so a published plan stays untouched.
type joinedVisitor struct {
	filter     func(slot joinedSlot, raw any) (any, error)
	collection func(name any)
}

func (v joinedVisitor) slot(owner map[string]any, key string, slot joinedSlot) error {
	raw, found := owner[key]
	if !found || v.filter == nil {
		return nil
	}
	kept, err := v.filter(slot, raw)
	if err != nil {
		return err
	}
	if kept == nil {
		delete(owner, key)
	} else {
		owner[key] = kept
	}
	return nil
}

func copyObject(raw any) map[string]any {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	copied := make(map[string]any, len(object))
	for key, value := range object {
		copied[key] = value
	}
	return copied
}

// list rewrites a list of objects in place on a copied owner.
func (v joinedVisitor) list(owner map[string]any, key, path string, each func(item map[string]any, path string) error) error {
	items, ok := owner[key].([]any)
	if !ok {
		return nil
	}
	copied := make([]any, len(items))
	for i, raw := range items {
		item := copyObject(raw)
		if item == nil {
			copied[i] = raw
			continue
		}
		if err := each(item, fmt.Sprintf("%s.%s[%d]", path, key, i)); err != nil {
			return err
		}
		copied[i] = item
	}
	owner[key] = copied
	return nil
}

// object rewrites one nested object in place on a copied owner.
func (v joinedVisitor) object(owner map[string]any, key, path string, each func(item map[string]any, path string) error) error {
	item := copyObject(owner[key])
	if item == nil {
		return nil
	}
	if err := each(item, path+"."+key); err != nil {
		return err
	}
	owner[key] = item
	return nil
}

func (v joinedVisitor) request(args map[string]any, path string) (map[string]any, error) {
	out := copyObject(args)
	if out == nil {
		return nil, nil
	}
	v.named(out)
	for _, step := range []func() error{
		func() error {
			return v.slot(out, "where", joinedSlot{path: path + ".where", client: true})
		},
		func() error { return v.slot(out, "filter", joinedSlot{path: path + ".filter", client: true}) },
		func() error {
			return v.slot(out, "match", joinedSlot{path: path + ".match", client: true, match: true})
		},
		func() error { return v.joins(out, path) },
		func() error { return v.computed(out, path) },
		func() error { return v.pick(out, path) },
		func() error { return v.metrics(out, path, true) },
		func() error { return v.branches(out, path) },
		func() error { return v.rollup(out, path) },
		func() error { return v.relation(out, path) },
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (v joinedVisitor) named(owner map[string]any) {
	if v.collection != nil {
		v.collection(owner["collection"])
	}
}

func (v joinedVisitor) joins(owner map[string]any, path string) error {
	return v.list(owner, "joins", path, func(join map[string]any, path string) error {
		v.named(join)
		if err := v.slot(join, "where", joinedSlot{path: path + ".where"}); err != nil {
			return err
		}
		return v.metrics(join, path, false)
	})
}

func (v joinedVisitor) metrics(owner map[string]any, path string, client bool) error {
	return v.list(owner, "metrics", path, func(metric map[string]any, path string) error {
		return v.slot(metric, "where", joinedSlot{path: path + ".where", client: client})
	})
}

func (v joinedVisitor) computed(owner map[string]any, path string) error {
	return v.list(owner, "computed", path, func(item map[string]any, path string) error {
		return v.object(item, "expr", path, v.expression)
	})
}

func (v joinedVisitor) expression(expr map[string]any, path string) error {
	if err := v.slot(expr, "if", joinedSlot{path: path + ".if", condition: true}); err != nil {
		return err
	}
	for _, key := range []string{"nullIfEmpty", "trim", "strip", "json", "then", "else"} {
		if err := v.object(expr, key, path, v.expression); err != nil {
			return err
		}
	}
	for _, key := range []string{"coalesce", "concat"} {
		if err := v.list(expr, key, path, v.expression); err != nil {
			return err
		}
	}
	return nil
}

func (v joinedVisitor) pick(owner map[string]any, path string) error {
	return v.object(owner, "pick", path, func(pick map[string]any, path string) error {
		return v.slot(pick, "where", joinedSlot{path: path + ".where"})
	})
}

func (v joinedVisitor) branches(owner map[string]any, path string) error {
	return v.list(owner, "branches", path, func(branch map[string]any, path string) error {
		v.named(branch)
		if err := v.slot(branch, "where", joinedSlot{path: path + ".where", client: true}); err != nil {
			return err
		}
		if err := v.slot(branch, "filter", joinedSlot{path: path + ".filter", client: true}); err != nil {
			return err
		}
		if err := v.joins(branch, path); err != nil {
			return err
		}
		if err := v.computed(branch, path); err != nil {
			return err
		}
		return v.pick(branch, path)
	})
}

func (v joinedVisitor) rollup(owner map[string]any, path string) error {
	return v.object(owner, "rollup", path, func(rollup map[string]any, path string) error {
		if err := v.slot(rollup, "filter", joinedSlot{path: path + ".filter"}); err != nil {
			return err
		}
		if err := v.metrics(rollup, path, false); err != nil {
			return err
		}
		return v.computed(rollup, path)
	})
}

func (v joinedVisitor) relation(owner map[string]any, path string) error {
	return v.object(owner, "relation", path, func(relation map[string]any, path string) error {
		return v.list(relation, "sources", path, func(source map[string]any, path string) error {
			if err := v.slot(source, "filter", joinedSlot{path: path + ".filter"}); err != nil {
				return err
			}
			if err := v.joins(source, path); err != nil {
				return err
			}
			return v.computed(source, path)
		})
	})
}

// joinedFilters lists every filter of a joined step, with its slot.
func (step databasePipelineStep) joinedFilters() []joinedFilter {
	if !isJoinedOp(step.Op) {
		return nil
	}
	found := []joinedFilter{}
	_, _ = joinedVisitor{filter: func(slot joinedSlot, raw any) (any, error) {
		found = append(found, joinedFilter{slot: slot, raw: raw})
		return raw, nil
	}}.request(step.Args, "step "+step.ID)
	return found
}

type joinedFilter struct {
	slot joinedSlot
	raw  any
}

// ---- validation ----

func (plan *databasePipelinePlan) validateJoinedStep(step *databasePipelineStep, args map[string]any) error {
	label := "database_pipeline step " + step.ID
	if err := plan.validateJoinedStructure(step, args, label); err != nil {
		return err
	}
	// Without its filters a request is structure, and structure never takes a value.
	skeleton, err := joinedVisitor{filter: func(joinedSlot, any) (any, error) { return nil, nil }}.request(args, "step "+step.ID)
	if err != nil {
		return err
	}
	if err := noPlaceholders(skeleton, "step "+step.ID); err != nil {
		return err
	}
	if err := plan.validateJoinedStages(step, args, label); err != nil {
		return err
	}
	if err := plan.validateJoinedRoots(args, label); err != nil {
		return err
	}
	_, err = joinedVisitor{filter: func(slot joinedSlot, raw any) (any, error) {
		return nil, plan.validateJoinedFilter(slot, raw)
	}}.request(args, "step "+step.ID)
	return err
}

func (plan *databasePipelinePlan) validateJoinedFilter(slot joinedSlot, raw any) error {
	label := "database_pipeline " + slot.path
	conditional := false
	var conditionErr error
	walkFilter(raw, func(node map[string]any) {
		if isAccessWhenFilter(node) {
			conditional = true
			if err := plan.requireExactConditions(node["access_when"], label); err != nil && conditionErr == nil {
				conditionErr = err
			}
		}
	})
	if conditionErr != nil {
		return conditionErr
	}
	if slot.condition && conditional {
		return invalid("%s cannot hold access_when: when it doesn't apply, the value has no condition to decide on", label)
	}
	if !slot.client {
		found := false
		walkFilter(raw, func(node map[string]any) { found = found || isConditionalFilter(node) })
		if found {
			return invalid("%s cannot hold a client when; this filter decides eligibility, so use access_when if it needs selecting", label)
		}
		if err := plan.requireTrustedValues(raw, label); err != nil {
			return err
		}
	}
	if err := plan.validateFilter(raw, true, slot.path); err != nil {
		return err
	}
	if err := plan.checkSearchGuards(raw, nil, slot.match, slot.path); err != nil {
		return err
	}
	if slot.match && filterHasRelation(raw) {
		return invalid("%s cannot hold exists or not_exists; Database tests match on the rows a relation already selected", label)
	}
	relations := 0
	walkFilter(raw, func(node map[string]any) {
		if isRelationFilter(node) {
			relations++
		}
	})
	if relations > maxRelationNodes {
		return invalid("%s holds %d exists or not_exists filters; Database takes at most %d", label, relations, maxRelationNodes)
	}
	if depth := relationDepth(raw); depth > maxRelationDepth {
		return invalid("%s nests exists and not_exists %d levels deep; Database takes at most %d", label, depth, maxRelationDepth)
	}
	return plan.refuseWideningOr(raw, label)
}

// refuseWideningOr keeps client values from sitting beside authorization as an
// alternative, which would let a client widen what it may read. Inside a client
// when the whole group is optional narrowing, so it is not checked.
func (plan *databasePipelinePlan) refuseWideningOr(raw any, label string) error {
	node, ok := raw.(map[string]any)
	if !ok || isConditionalFilter(node) {
		return nil
	}
	if isAccessWhenFilter(node) {
		return plan.refuseWideningOr(node["filter"], label)
	}
	if _, clause := relationClause(node); clause != nil {
		return plan.refuseWideningOr(clause["where"], label)
	}
	if alternatives, isOr := node["or"].([]any); isOr {
		trusted, client := plan.filterValueSources(node)
		if trusted && client {
			return invalid("%s holds an or with both client values and authorization values; a client value there could widen access, so keep them in separate and branches", label)
		}
		// An alternative with no client value is a rule the plan fixed, and a client
		// value beside it could add rows that rule leaves out.
		if client {
			for _, child := range alternatives {
				if _, holds := plan.filterValueSources(child); !holds {
					return invalid("%s holds an or where only some alternatives read client values, which could add rows the others leave out; put the client values in a when or their own and branch", label)
				}
			}
		}
		for _, child := range alternatives {
			if err := plan.refuseWideningOr(child, label); err != nil {
				return err
			}
		}
	}
	for _, child := range asAnyList(node["and"]) {
		if err := plan.refuseWideningOr(child, label); err != nil {
			return err
		}
	}
	return nil
}

// filterValueSources reports whether a filter reads authorization values and
// whether it reads client values.
func (plan *databasePipelinePlan) filterValueSources(raw any) (trusted, client bool) {
	var read func(value any)
	read = func(value any) {
		switch typed := value.(type) {
		case string:
			// A list a client can narrow is still an access value here: beside a
			// client value it could widen, and shrinking it alone only returns less.
			if strings.HasPrefix(typed, "$scope.") {
				trusted = true
			}
			if name, ok := strings.CutPrefix(typed, "$param."); ok {
				param := plan.Params[name]
				switch {
				case strings.HasPrefix(param.From, "$identity."):
					trusted = true
				case !param.HasValue:
					client = true
				}
			}
		case []any:
			for _, item := range typed {
				read(item)
			}
		}
	}
	walkFilter(raw, func(node map[string]any) { read(node["value"]) })
	return trusted, client
}

func (plan *databasePipelinePlan) validateJoinedStructure(step *databasePipelineStep, args map[string]any, label string) error {
	_, hasCollection := args["collection"]
	_, hasBranches := args["branches"]
	switch {
	case step.Op == joinedAggregate:
		if hasCollection == hasBranches {
			return invalid("%s requires exactly one of collection and branches", label)
		}
		metrics, _ := args["metrics"].([]any)
		if len(metrics) == 0 {
			return invalid("%s (%s) requires metrics", label, step.Op)
		}
		for _, raw := range metrics {
			metric, _ := raw.(map[string]any)
			name, _ := metric["name"].(string)
			if name == "" {
				return invalid("%s metric requires a name", label)
			}
			switch metric["op"] {
			case "count", "count_distinct", "sum":
				step.Metrics[name] = true
			}
		}
	case !hasCollection:
		return invalid("%s requires a collection", label)
	}
	if hasCollection && !literalFieldName(args["collection"]) {
		return invalid("%s requires a literal collection", label)
	}
	if step.Op != joinedAggregate {
		if selected, err := pipelineStringList(args["select"]); err != nil || len(selected) == 0 {
			return invalid("%s (%s) requires select, a list of the fields it returns", label, step.Op)
		}
	}
	if raw, found := args["count"]; found {
		if _, isBool := raw.(bool); !isBool {
			return invalid("%s count must be Boolean", label)
		}
	}
	if raw, found := args["orderBy"]; found {
		if err := validateDatabaseOrderBy(raw, label+" orderBy"); err != nil {
			return err
		}
	}
	if step.Op == joinedRelatedRead {
		return validateJoinedRelation(args["relation"], label)
	}
	return nil
}

// validateJoinedRelation keeps the bound a literal: a client must never choose how
// many related rows a parent takes.
func validateJoinedRelation(raw any, label string) error {
	relation, ok := raw.(map[string]any)
	if !ok {
		return invalid("%s (related_read) requires relation", label)
	}
	if err := onlyKeys(relation, label+" relation", "as", "limit", "sources"); err != nil {
		return err
	}
	if !literalFieldName(relation["as"]) {
		return invalid("%s relation requires a literal as", label)
	}
	limit, integral := strictInt(relation["limit"])
	if !integral || limit < 1 || limit > maxJoinedRelationLimit {
		return invalid("%s relation limit must be a literal integer between 1 and %d; an unbounded or client-chosen relation is refused", label, maxJoinedRelationLimit)
	}
	sources, _ := relation["sources"].([]any)
	if len(sources) == 0 || len(sources) > maxJoinedSources {
		return invalid("%s relation requires 1 to %d sources", label, maxJoinedSources)
	}
	for i, raw := range sources {
		source, _ := raw.(map[string]any)
		if source == nil {
			return invalid("%s relation source %d must be an object", label, i)
		}
		if err := onlyKeys(source, fmt.Sprintf("%s relation source %d", label, i), "name", "joins", "computed", "filter", "distinct", "orderBy", "select"); err != nil {
			return err
		}
	}
	return nil
}

// databasePipelineShape maps a joined step's rows to the resolver's fields. It is
// configuration, applied after Database has answered.
type databasePipelineShape struct {
	Rename         map[string]string
	TotalAs        string
	JSONText       map[string]bool
	Numbers        map[string]bool
	RelationSource bool
}

// joinedOutputNames lists the names a joined read returns, once each.
func joinedOutputNames(args map[string]any) []string {
	seen := map[string]bool{}
	for _, key := range []string{"select", "groupBy"} {
		for _, raw := range asAnyList(args[key]) {
			if name, isText := raw.(string); isText {
				seen[name] = true
			}
		}
	}
	for _, key := range []string{"metrics", "derived"} {
		for _, raw := range asAnyList(args[key]) {
			item, _ := raw.(map[string]any)
			if name, isText := item["name"].(string); isText {
				seen[name] = true
			}
		}
	}
	if relation, isObject := args["relation"].(map[string]any); isObject {
		if name, isText := relation["as"].(string); isText {
			seen[name] = true
		}
	}
	return sortedNames(seen)
}

func parseJoinedShape(step *databasePipelineStep, raw any) error {
	label := "database_pipeline step " + step.ID + " shape"
	spec, ok := raw.(map[string]any)
	if !ok || !isJoinedOp(step.Op) {
		return invalid("%s applies to related_read, joined_page and joined_aggregate and must be an object", label)
	}
	if err := onlyKeys(spec, label, "rename", "total_as", "json_text", "numbers", "relation_source"); err != nil {
		return err
	}
	shape := &databasePipelineShape{Rename: map[string]string{}, JSONText: map[string]bool{}, Numbers: map[string]bool{}}
	if raw, found := spec["rename"]; found {
		rename, isObject := raw.(map[string]any)
		if !isObject {
			return invalid("%s rename must map returned names to field names", label)
		}
		seen := map[string]bool{}
		for from, value := range rename {
			to, _ := value.(string)
			if !graphqlName(to) || seen[to] {
				return invalid("%s rename target %q must be a unique GraphQL name", label, to)
			}
			seen[to] = true
			shape.Rename[from] = to
		}
	}
	if raw, found := spec["total_as"]; found {
		name, _ := raw.(string)
		if !graphqlName(name) || step.Op == joinedAggregate {
			return invalid("%s total_as must be a GraphQL name, on related_read or joined_page", label)
		}
		if step.Op == joinedRelatedRead && step.Args["count"] != true {
			return invalid("%s total_as needs count: true in args, because related_read only counts when asked", label)
		}
		shape.TotalAs = name
	}
	final := map[string]bool{}
	for _, name := range joinedOutputNames(step.Args) {
		if to, renamed := shape.Rename[name]; renamed {
			name = to
		}
		if final[name] {
			return invalid("%s would return two values named %s", label, name)
		}
		final[name] = true
	}
	if final[shape.TotalAs] {
		return invalid("%s total_as %q is already a returned name", label, shape.TotalAs)
	}
	for key, into := range map[string]map[string]bool{"json_text": shape.JSONText, "numbers": shape.Numbers} {
		if raw, found := spec[key]; found {
			names, err := pipelineStringList(raw)
			if err != nil {
				return invalid("%s %s must list field names", label, key)
			}
			for _, name := range names {
				into[name] = true
			}
		}
	}
	if raw, found := spec["relation_source"]; found {
		keep, isBool := raw.(bool)
		if !isBool {
			return invalid("%s relation_source must be Boolean", label)
		}
		shape.RelationSource = keep
	}
	step.Shape = shape
	return nil
}

// ---- binding ----

func (run *databasePipelineRun) joinedFilter(slot joinedSlot, raw any) (any, error) {
	resolved, err := run.resolveConditions(raw)
	if err != nil || resolved == nil {
		return nil, err
	}
	bound := run.filter(resolved)
	if filterExposesBlankSearch(bound, slot.match) {
		return nil, invalid("database_pipeline: an empty %s here would match every record; its when must remove it when the value is blank", databaseLiteralSearchOp)
	}
	return bound, nil
}

func (run *databasePipelineRun) joinedArgs(step databasePipelineStep) (map[string]any, error) {
	return joinedVisitor{filter: run.joinedFilter}.request(step.Args, "step "+step.ID)
}

// joinedPageArgs adds a joined step's page to its bound args. A size of zero asks
// for one record so totals stay real, and returns no rows, like LIMIT 0.
func (run *databasePipelineRun) joinedPageArgs(ctx context.Context, step databasePipelineStep, args map[string]any) error {
	args["limit"] = step.MaxRows
	if step.Op == joinedAggregate || step.Page == nil {
		return nil
	}
	raw := step.Page.Size
	if ref, isRef := raw.(string); isRef {
		raw = run.substitute(ref)
	}
	size := step.MaxRows
	if raw != nil {
		var integral bool
		if size, integral = strictInt(raw); !integral || size < 0 {
			return invalid("database_pipeline step %s page size must not be negative", step.ID)
		}
	}
	switch {
	case size == 0:
		run.emptyPages[step.ID] = true
		args["limit"] = 1
	case size < step.MaxRows:
		args["limit"] = size
	}
	binding, err := run.cursorBinding(ctx, step, args)
	if err != nil {
		return err
	}
	run.cursorBindings[step.ID] = binding
	offset := 0
	if step.Page.Offset != nil {
		raw := step.Page.Offset
		if ref, isRef := raw.(string); isRef {
			raw = run.substitute(ref)
		}
		if raw != nil {
			var integral bool
			if offset, integral = strictInt(raw); !integral {
				return invalid("database_pipeline step %s page offset must be an integer", step.ID)
			}
		}
		if offset > maxJoinedOffset {
			return invalid("database_pipeline step %s page offset must be at most %d", step.ID, maxJoinedOffset)
		}
	}
	cursor := ""
	if step.Page.Cursor != "" {
		cursor, _ = run.substitute(step.Page.Cursor).(string)
	}
	if strings.TrimSpace(cursor) != "" {
		if offset > 0 {
			return invalid("database_pipeline step %s takes a cursor or an offset, not both", step.ID)
		}
		native, err := run.app.openCursor(run.project, binding, cursor)
		if err != nil {
			return err
		}
		args["cursor"] = native
		return nil
	}
	// Like SQL OFFSET, a negative offset starts at the first record.
	if offset > 0 {
		args["offset"] = offset
	}
	return nil
}

// ---- results ----

// decodeJoinedStep fails rather than return a partial result. A joined_page's
// total counts its items, so a grouped page keeps counting groups (interactions,
// say) while it returns their rows.
func (run *databasePipelineRun) decodeJoinedStep(step databasePipelineStep, raw any) ([]any, error) {
	result, ok := raw.(map[string]any)
	if !ok {
		return nil, runtimeError("source_error", "database step "+step.ID+" returned an invalid result")
	}
	if step.Op == joinedAggregate {
		if result["truncated"] == true {
			return nil, runtimeError("source_result_truncated", "database step "+step.ID+" exceeded max_rows")
		}
		rows, _ := result["rows"].([]any)
		for _, item := range rows {
			if row, isRow := item.(map[string]any); isRow {
				for name := range step.Metrics {
					row[name] = databaseInteger(row[name])
				}
			}
		}
		return shapeJoinedRows(step, rows, nil)
	}
	if result["hasMore"] == true && step.Page == nil {
		return nil, runtimeError("source_result_truncated", "database step "+step.ID+" exceeded max_rows")
	}
	rows, _ := result["records"].([]any)
	if groups, isGrouped := result["groups"].([]any); isGrouped {
		rows = []any{}
		for _, item := range groups {
			group, _ := item.(map[string]any)
			rows = append(rows, asAnyList(group["rows"])...)
		}
	}
	total := databaseInteger(result["total"])
	meta := map[string]any{"hasMore": result["hasMore"] == true, "nextCursor": nil, "total": total}
	if run.emptyPages[step.ID] {
		rows = []any{}
		meta["hasMore"] = total != nil && total != int64(0)
	} else if native, isText := result["nextCursor"].(string); isText && native != "" && result["hasMore"] == true {
		sealed, err := run.app.sealCursor(run.project, run.cursorBindings[step.ID], native)
		if err != nil {
			return nil, err
		}
		meta["nextCursor"] = sealed
	}
	run.pages[step.ID] = meta
	return shapeJoinedRows(step, rows, total)
}

func shapeJoinedRows(step databasePipelineStep, rows []any, total any) ([]any, error) {
	if rows == nil {
		rows = []any{}
	}
	relationName := ""
	if relation, isObject := step.Args["relation"].(map[string]any); isObject {
		relationName, _ = relation["as"].(string)
	}
	for i, item := range rows {
		row, isRow := item.(map[string]any)
		if !isRow {
			continue
		}
		shaped := make(map[string]any, len(row)+1)
		for name, value := range row {
			if relationName != "" && name == relationName && (step.Shape == nil || !step.Shape.RelationSource) {
				value = withoutRelationSource(value)
			}
			if step.Shape != nil {
				if to, renamed := step.Shape.Rename[name]; renamed {
					name = to
				}
				if step.Shape.Numbers[name] {
					value = databaseInteger(value)
				}
				if step.Shape.JSONText[name] {
					text, err := jsonText(value)
					if err != nil {
						return nil, runtimeError("invalid_result", "database step "+step.ID+" field "+name+" is not JSON")
					}
					value = text
				}
			}
			shaped[name] = value
		}
		if step.Shape != nil && step.Shape.TotalAs != "" {
			shaped[step.Shape.TotalAs] = total
		}
		rows[i] = shaped
	}
	return rows, nil
}

// withoutRelationSource drops the source label Database adds to each related row;
// a resolver's output fields are the ones it selects.
func withoutRelationSource(raw any) any {
	entries, ok := raw.([]any)
	if !ok {
		return raw
	}
	out := make([]any, len(entries))
	for i, item := range entries {
		out[i] = item
		if entry := copyObject(item); entry != nil {
			delete(entry, "source")
			out[i] = entry
		}
	}
	return out
}

// jsonText encodes a value as JSON text. A value that is already text stays as it
// is, and null stays null.
func jsonText(value any) (any, error) {
	switch value.(type) {
	case nil, string:
		return value, nil
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return strings.TrimSuffix(buffer.String(), "\n"), nil
}

// ---- result templates ----

// A result can also be a template, for fields whose envelope is part of their
// contract:
//
//	"result": [{"payload": {"$json_text": "$combine.costs"}}]
//	"result": {"$if": <trusted condition>, "then": [...], "else": []}
//
// Strings are result references, objects and lists keep their shape, "$const"
// holds a literal, and "$if" reads identity and access values only.
func (plan *databasePipelinePlan) parseResultTemplate(raw any, depth int) error {
	if depth > 16 {
		return invalid("database_pipeline result is too deep")
	}
	switch node := raw.(type) {
	case string:
		if !plan.validResultRef(node) {
			return invalid("database_pipeline result references unknown %q", node)
		}
		return nil
	case []any:
		for _, item := range node {
			if err := plan.parseResultTemplate(item, depth+1); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		if inner, found := node["$json_text"]; found {
			if len(node) != 1 {
				return invalid("database_pipeline result $json_text must stand alone")
			}
			return plan.parseResultTemplate(inner, depth+1)
		}
		if _, found := node["$const"]; found {
			if len(node) != 1 {
				return invalid("database_pipeline result $const must stand alone")
			}
			return nil
		}
		if condition, found := node["$if"]; found {
			if err := onlyKeys(node, "database_pipeline result $if", "$if", "then", "else"); err != nil {
				return err
			}
			kind, err := plan.conditionType(condition, 0, true)
			if err != nil {
				return invalid("database_pipeline result $if: %s", err)
			}
			if err := plan.requireExactConditions(condition, "database_pipeline result $if"); err != nil {
				return err
			}
			if kind != conditionBoolean {
				return invalid("database_pipeline result $if must be Boolean, not %s", kind)
			}
			for _, branch := range []string{"then", "else"} {
				if err := plan.parseResultTemplate(node[branch], depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		for name, item := range node {
			if !graphqlName(name) {
				return invalid("database_pipeline result field %q is invalid", name)
			}
			if err := plan.parseResultTemplate(item, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return invalid("database_pipeline result templates hold references, lists, objects, $json_text, $const and $if")
}

func (run *databasePipelineRun) renderResult(raw any, resolve func(string) any) (any, error) {
	switch node := raw.(type) {
	case string:
		return resolve(node), nil
	case []any:
		out := make([]any, len(node))
		for i, item := range node {
			value, err := run.renderResult(item, resolve)
			if err != nil {
				return nil, err
			}
			out[i] = value
		}
		return out, nil
	case map[string]any:
		if inner, found := node["$json_text"]; found {
			value, err := run.renderResult(inner, resolve)
			if err != nil {
				return nil, err
			}
			text, err := jsonText(value)
			if err != nil {
				return nil, runtimeError("invalid_result", "database pipeline result is not JSON")
			}
			return text, nil
		}
		if value, found := node["$const"]; found {
			return value, nil
		}
		if condition, found := node["$if"]; found {
			holds, err := run.conditionHolds(condition)
			if err != nil {
				return nil, err
			}
			if holds {
				return run.renderResult(node["then"], resolve)
			}
			return run.renderResult(node["else"], resolve)
		}
		out := make(map[string]any, len(node))
		for name, item := range node {
			value, err := run.renderResult(item, resolve)
			if err != nil {
				return nil, err
			}
			out[name] = value
		}
		return out, nil
	}
	return nil, internal("unknown database pipeline result node")
}

// ---- one snapshot for scope and rows ----

// errAccessChanged reports that the grants a run resolved its scope from no longer
// match the snapshot its rows were read from.
var errAccessChanged = errors.New("database pipeline access changed during the read")

// maxAccessAttempts bounds re-resolving a scope that keeps changing; past it the
// request fails rather than return rows under a scope it could not confirm.
const maxAccessAttempts = 3

// sameAccessRead compares what an access read found, without its cursor, which
// differs from read to read.
func sameAccessRead(left, right any) bool {
	encode := func(raw any) string {
		page, _ := raw.(map[string]any)
		text, err := json.Marshal([]any{page["records"], page["rows"], page["hasMore"], page["truncated"]})
		if err != nil {
			return fmt.Sprint(raw)
		}
		return string(text)
	}
	return encode(left) == encode(right)
}

// ---- publication ----

// joinedProbe is a step's request with a root that matches nothing: Database
// resolves every collection, field, expression, comparison, index and bound,
// including branches a condition would remove, and reads no rows. Cost budgets
// depend on the data and still fail at run time, whole.
func (step databasePipelineStep) joinedProbe() (map[string]any, error) {
	args, err := joinedVisitor{filter: func(_ joinedSlot, raw any) (any, error) {
		if probe := probeFilterKeeping(raw, true); probe != nil {
			return probe, nil
		}
		return nil, nil
	}}.request(step.Args, "step "+step.ID)
	if err != nil {
		return nil, err
	}
	if err := matchNoRoots(args, step.ID); err != nil {
		return nil, err
	}
	if branches, isList := args["branches"].([]any); isList {
		for _, raw := range branches {
			if branch, isBranch := raw.(map[string]any); isBranch {
				if err := matchNoRoots(branch, step.ID); err != nil {
					return nil, err
				}
			}
		}
	}
	args["limit"] = 1
	return map[string]any{"op": step.Op, "args": args}, nil
}

func matchNoRoots(owner map[string]any, stepID string) error {
	if _, isCollection := owner["collection"]; !isCollection {
		return nil
	}
	field := joinedRootField(owner)
	if field == "" {
		return invalid("database_pipeline step %s has no root field to check it with; give it a where or a select", stepID)
	}
	none := matchNothing(field)
	if where, found := owner["where"]; found {
		owner["where"] = map[string]any{"and": []any{none, where}}
	} else {
		owner["where"] = none
	}
	return nil
}

// joinedRootField names a field of a read's root collection. The root where uses
// bare names; everything after the joins names root fields as "<as>.field".
func joinedRootField(owner map[string]any) string {
	found := ""
	walkOwnFilter(owner["where"], func(node map[string]any) {
		if field, isText := node["field"].(string); isText && found == "" {
			found = field
		}
	})
	if found != "" {
		return found
	}
	alias, _ := owner["as"].(string)
	if alias == "" {
		alias = "root"
	}
	bare := func(name string) string { return strings.TrimPrefix(name, alias+".") }
	rooted := func(name string) bool { return strings.HasPrefix(name, alias+".") }
	joined := owner["joins"] != nil || owner["computed"] != nil || owner["relation"] == nil
	take := func(name string) {
		if found != "" || name == "" {
			return
		}
		if rooted(name) || !joined {
			found = bare(name)
		}
	}
	for _, raw := range asAnyList(owner["orderBy"]) {
		order, _ := raw.(map[string]any)
		name, _ := order["field"].(string)
		take(name)
	}
	for _, raw := range asAnyList(owner["groupBy"]) {
		name, _ := raw.(string)
		take(name)
	}
	for _, raw := range asAnyList(owner["joins"]) {
		join, _ := raw.(map[string]any)
		alternatives := []any{join}
		alternatives = append(alternatives, asAnyList(join["anyOf"])...)
		for _, item := range alternatives {
			group, _ := item.(map[string]any)
			for _, pair := range asAnyList(group["on"]) {
				on, _ := pair.(map[string]any)
				name, _ := on["local"].(string)
				if rooted(name) {
					take(name)
				}
			}
		}
	}
	// A selected field might be JSON, which can't be filtered, so it comes last and
	// an id comes first.
	for _, raw := range asAnyList(owner["select"]) {
		if name, _ := raw.(string); name == "id" || name == alias+".id" {
			take(name)
		}
	}
	for _, raw := range asAnyList(owner["select"]) {
		name, _ := raw.(string)
		take(name)
	}
	return found
}

// maxDatabaseWatchCollections is Database's limit for one change-feed watch.
const maxDatabaseWatchCollections = 16

// validateJoinedArguments checks that every client value a joined plan reads is an
// argument of the field, of a type its param can take, so a mismatch refuses the
// release instead of failing a request.
func (plan *databasePipelinePlan) validateJoinedArguments(field *ast.FieldDefinition) error {
	if field == nil {
		return nil
	}
	names := make([]string, 0, len(plan.Params))
	for name := range plan.Params {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		param := plan.Params[name]
		argumentName, isArgument := strings.CutPrefix(param.From, "$args.")
		if !isArgument || strings.Contains(argumentName, ".") {
			continue
		}
		argument := field.Arguments.ForName(argumentName)
		if argument == nil {
			return fmt.Errorf("param %s reads argument %s, which the field does not declare", name, argumentName)
		}
		list := argument.Type.Elem != nil
		named := argument.Type.NamedType
		fits := true
		switch param.Type {
		case "string_list", "exact_string_list":
			if param.Encoding == "json" {
				fits = !list && named == "String"
			} else {
				fits = list
			}
		case "number":
			fits = !list && (named == "Int" || named == "Float")
		case "boolean":
			fits = !list && named == "Boolean"
		case "string", "datetime":
			fits = !list && named != "Int" && named != "Float" && named != "Boolean"
		}
		if !fits {
			return fmt.Errorf("param %s has type %s, which argument %s: %s cannot supply", name, param.Type, argumentName, argument.Type.String())
		}
	}
	return nil
}
