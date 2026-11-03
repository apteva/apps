package main

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
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
	tool, err := databaseTool(operation)
	if err != nil {
		return nil // reported when the resolver runs
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
