package main

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"
)

// Database query options that only resolver configuration may set: lookups
// join the record of another collection and buckets derive time keys for
// groupBy. They are forwarded as configured and never read from GraphQL
// arguments, so a client cannot join or bucket anything new.
const maxDatabaseQueryOptions = 4 // Database's own limit for both

var databaseBucketUnits = map[string]bool{"hour": true, "day": true, "week": true, "month": true, "year": true}

// onlyKeys rejects configuration keys outside allowed, so a misspelt or
// unsupported option fails validation instead of being silently ignored.
func onlyKeys(spec map[string]any, path string, allowed ...string) error {
	permitted := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		permitted[key] = true
	}
	unknown := []string{}
	for key := range spec {
		if !permitted[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return invalid("%s does not accept %q (allowed: %s)", path, unknown[0], strings.Join(allowed, ", "))
}

// strictInt accepts integral JSON numbers only.
func strictInt(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case int64:
		return int(number), true
	case float64:
		if number == math.Trunc(number) && math.Abs(number) < 1<<31 {
			return int(number), true
		}
	case json.Number:
		if parsed, err := number.Int64(); err == nil {
			return int(parsed), true
		}
	}
	return 0, false
}

func nonEmptyString(value any) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != ""
}

func validateStringList(value any, path string) error {
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return invalid("%s must be a non-empty list of strings", path)
	}
	for _, item := range list {
		if !nonEmptyString(item) || strings.HasPrefix(item.(string), "$") {
			return invalid("%s must be a non-empty list of literal strings", path)
		}
	}
	return nil
}

func validateDatabaseLookups(value any, path string) error {
	list, ok := value.([]any)
	if !ok || len(list) == 0 || len(list) > maxDatabaseQueryOptions {
		return invalid("%s must be a list of 1 to %d lookups", path, maxDatabaseQueryOptions)
	}
	names := map[string]bool{}
	for _, raw := range list {
		lookup, ok := raw.(map[string]any)
		if !ok {
			return invalid("%s entries must be objects", path)
		}
		if err := onlyKeys(lookup, path+" entry", "as", "collection", "localField", "select"); err != nil {
			return err
		}
		for _, key := range []string{"as", "collection", "localField"} {
			if !nonEmptyString(lookup[key]) || strings.HasPrefix(lookup[key].(string), "$") {
				return invalid("%s entries require a literal %s", path, key)
			}
		}
		if names[lookup["as"].(string)] {
			return invalid("%s repeats as %q", path, lookup["as"])
		}
		names[lookup["as"].(string)] = true
		if lookup["select"] != nil {
			if err := validateStringList(lookup["select"], path+" select"); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateDatabaseBuckets(value any, path string) error {
	list, ok := value.([]any)
	if !ok || len(list) == 0 || len(list) > maxDatabaseQueryOptions {
		return invalid("%s must be a list of 1 to %d buckets", path, maxDatabaseQueryOptions)
	}
	for _, raw := range list {
		bucket, ok := raw.(map[string]any)
		if !ok {
			return invalid("%s entries must be objects", path)
		}
		if err := onlyKeys(bucket, path+" entry", "name", "field", "unit", "timezone", "weekStart"); err != nil {
			return err
		}
		if !nonEmptyString(bucket["name"]) || !nonEmptyString(bucket["field"]) {
			return invalid("%s entries require name and field", path)
		}
		if unit, _ := bucket["unit"].(string); !databaseBucketUnits[unit] {
			return invalid("%s unit must be hour, day, week, month or year", path)
		}
		if zone, exists := bucket["timezone"]; exists && !nonEmptyString(zone) {
			return invalid("%s timezone must be an IANA name", path)
		}
		if start, exists := bucket["weekStart"]; exists && start != "monday" && start != "sunday" {
			return invalid("%s weekStart must be monday or sunday", path)
		}
	}
	return nil
}

// validateDatabaseResolverOptions checks the lookups and buckets of a plain
// Database resolver against the tool its operation calls.
func validateDatabaseResolverOptions(operation string, config map[string]any) error {
	if operation == databasePipelineOperation {
		return nil
	}
	if advancedDatabaseRead(config) {
		configured, err := canonicalResolverPlan(operation, config)
		if err != nil {
			return err
		}
		_, err = validateDatabasePipeline(configured)
		return err
	}
	tool, err := databaseTool(operation)
	if err != nil {
		return nil // reported when the resolver runs
	}
	if filterHasOptional(config["where"]) || untrustedFilterHasRelation(config["where"]) {
		return invalid("database %s does not accept conditional or relational filters; use database_pipeline", operation)
	}
	if value, ok := config["lookups"]; ok {
		if tool != "db_find" && tool != "db_aggregate" {
			return invalid("database %s does not accept lookups", operation)
		}
		if err := validateDatabaseLookups(value, "database "+operation+" lookups"); err != nil {
			return err
		}
	}
	if value, ok := config["buckets"]; ok {
		if tool != "db_aggregate" {
			return invalid("database %s does not accept buckets; use aggregate", operation)
		}
		if err := validateDatabaseBuckets(value, "database "+operation+" buckets"); err != nil {
			return err
		}
	}
	return nil
}

// databaseLookupCollections lists the collections a lookups option joins.
func databaseLookupCollections(value any) []string {
	list, _ := value.([]any)
	out := []string{}
	for _, raw := range list {
		if lookup, ok := raw.(map[string]any); ok {
			if name, ok := lookup["collection"].(string); ok && strings.TrimSpace(name) != "" {
				out = append(out, strings.TrimSpace(name))
			}
		}
	}
	return out
}

func validateDatabaseOrderBy(value any, path string) error {
	list, ok := value.([]any)
	if !ok || len(list) == 0 || len(list) > maxDatabaseQueryOptions {
		return invalid("%s must be a list of 1 to %d {field, direction} entries", path, maxDatabaseQueryOptions)
	}
	for _, raw := range list {
		order, ok := raw.(map[string]any)
		if !ok {
			return invalid("%s entries must be objects", path)
		}
		if err := onlyKeys(order, path+" entry", "field", "direction"); err != nil {
			return err
		}
		if !nonEmptyString(order["field"]) || strings.HasPrefix(order["field"].(string), "$") {
			return invalid("%s entries require a literal field", path)
		}
		if direction, exists := order["direction"]; exists && direction != "asc" && direction != "desc" {
			return invalid("%s direction must be asc or desc", path)
		}
	}
	return nil
}

// maxDatabaseBatchOperations is Database's limit per db_batch.
const maxDatabaseBatchOperations = 100

// verifyPublishDatabaseFilters asks Database to validate what could otherwise
// only fail once a user reaches it, before a release can publish it.
//
// Text searches: the Database version must support the predicate and its fields
// must exist and be text. An empty value is checked like any other and then
// dropped from the plan, so the probe reads almost nothing.
//
// Conditional filters: their fields must exist, because a false condition means
// Database never sees them. Selecting them reads one record.
//
// Relational filters: Database plans each one without running it, whichever
// access_when selects it.
//
// Aggregates with lookups or buckets may filter on derived fields a plain find
// cannot see, so those fail at run time instead, closed.
func (a *App) verifyPublishDatabaseFilters(ctx context.Context, project, api string) error {
	sources, err := listSourcesForAPI(a.ctx.AppDB(), project, api)
	if err != nil {
		return err
	}
	resolvers, err := listResolversForAPI(a.ctx.AppDB(), project, api)
	if err != nil {
		return err
	}
	sourceByID := map[int64]sourceRecord{}
	for _, source := range sources {
		sourceByID[source.ID] = source
	}
	for _, resolver := range resolvers {
		source := sourceByID[resolver.SourceID]
		if source.Kind != "database" || (resolver.Operation != databasePipelineOperation && !advancedDatabaseRead(mergeMaps(source.Config, resolver.Config))) {
			continue
		}
		configured := mergeMaps(source.Config, resolver.Config)
		if resolver.Operation != databasePipelineOperation {
			configured, err = canonicalResolverPlan(resolver.Operation, configured)
			if err != nil {
				return err
			}
		}
		plan, err := validateDatabasePipeline(configured)
		if err != nil {
			continue // publishing reports it with the release checks
		}
		run := &databasePipelineRun{app: a, plan: plan, project: project}
		searches, conditionals, joined := []any{}, []any{}, []any{}
		for _, step := range plan.Steps {
			if isJoinedOp(step.Op) {
				probe, err := run.canonicalProbe(ctx, step)
				if err != nil {
					return invalid("resolver %s.%s: %s", resolver.ParentType, resolver.FieldName, err)
				}
				joined = append(joined, probe)
				continue
			}
			if step.Args["lookups"] != nil || step.Args["buckets"] != nil {
				continue
			}
			// A search on a related collection is planned with its relationship below.
			for _, search := range step.ownSearches() {
				where := map[string]any{"field": search["field"], "op": databaseLiteralSearchOp, "value": ""}
				first := search["field"]
				if !isLiteralSearch(search) {
					fields := asAnyList(search["fields"])
					where = map[string]any{"fields": fields, "op": databaseSearchOp, "value": ""}
					first = fields[0]
				}
				searches = append(searches, map[string]any{"op": "find", "args": map[string]any{
					"collection": step.Args["collection"], "select": []any{first}, "limit": 1, "where": where,
				}})
			}
			if fields := step.conditionalFields(); len(fields) > 0 {
				conditionals = append(conditionals, map[string]any{"op": "find", "args": map[string]any{
					"collection": step.Args["collection"], "select": fields, "limit": 1,
				}})
			}
		}
		for _, check := range []struct {
			operations []any
			problem    string
		}{
			{searches, "Database cannot run its text search (" + databaseSearchOp + " requires Database 0.7.0 or later, " + databaseLiteralSearchOp + " Database 0.11.0 or later, and both existing text fields)"},
			{conditionals, "Database cannot read the fields of its conditional filters"},
			{joined, "Database cannot run its joined reads (related_read, joined_page and joined_aggregate require Database 0.12.0 or later, canonical find/aggregate pipelines require Database 0.14.0 or later; existing collections and fields, join keys of one type or a stated comparison, an index on each related field, and a bounded relation)"},
		} {
			for start := 0; start < len(check.operations); start += maxDatabaseBatchOperations {
				probe, cancel := context.WithTimeout(ctx, 15*time.Second)
				_, err := run.send(probe, check.operations[start:min(start+maxDatabaseBatchOperations, len(check.operations))])
				cancel()
				if err != nil {
					return invalid("resolver %s.%s: %s: %s", resolver.ParentType, resolver.FieldName, check.problem, err)
				}
			}
		}
		if err := run.verifyRelations(ctx); err != nil {
			return invalid("resolver %s.%s: %s", resolver.ParentType, resolver.FieldName, err)
		}
	}
	return nil
}
