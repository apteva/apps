package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// TestRealDatabasePolicyVectors replays externally captured authorization
// vectors through a database_pipeline plan on a real Database, comparing the
// ids a caller receives with the expected ones:
//
//	GRAPHQL_TEST_DATABASE_DIR=<database>/mcp/database \
//	GRAPHQL_TEST_POLICY_PLAN=<plan.json> GRAPHQL_TEST_POLICY_VECTORS=<test-vectors.json> \
//	GOWORK=off go test -run TestRealDatabasePolicyVectors -v
//
// Vectors are {cases:[{name, actor:{id, permissions}, tables:{collection:[rows]},
// expected:{<resource>: [ids]}}]}; GRAPHQL_TEST_POLICY_RESOURCE (default
// offers) picks the resource and GRAPHQL_TEST_POLICY_ID (default local_id) the
// compared field. Collections are created from GRAPHQL_TEST_POLICY_SCHEMA
// ({collection: ["field:type", ...]}) or inferred from the rows. JSON-encoded
// array strings (legacy exports) are decoded into arrays before insertion.
func TestRealDatabasePolicyVectors(t *testing.T) {
	planPath, vectorsPath := os.Getenv("GRAPHQL_TEST_POLICY_PLAN"), os.Getenv("GRAPHQL_TEST_POLICY_VECTORS")
	if planPath == "" || vectorsPath == "" {
		t.Skip("set GRAPHQL_TEST_POLICY_PLAN and GRAPHQL_TEST_POLICY_VECTORS")
	}
	resource := envOr("GRAPHQL_TEST_POLICY_RESOURCE", "offers")
	idField := envOr("GRAPHQL_TEST_POLICY_ID", "local_id")
	var plan map[string]any
	var vectors struct {
		Cases []struct {
			Name  string `json:"name"`
			Actor struct {
				ID          string   `json:"id"`
				Permissions []string `json:"permissions"`
			} `json:"actor"`
			Tables   map[string][]map[string]any `json:"tables"`
			Expected map[string][]string         `json:"expected"`
		} `json:"cases"`
	}
	for path, out := range map[string]any{planPath: &plan, vectorsPath: &vectors} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	r := newRealDatabase(t, "graphql-database-policy-vectors")
	plan["database"] = "crm"

	// Collections the plan reads, typed from the schema file or the rows.
	validated, err := validateDatabasePipeline(plan)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	collections := validated.Collections()
	schema := map[string][]string{}
	if path := os.Getenv("GRAPHQL_TEST_POLICY_SCHEMA"); path != "" {
		raw, _ := os.ReadFile(path)
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range collections {
		if schema[name] == nil {
			rows := []map[string]any{}
			for _, c := range vectors.Cases {
				rows = append(rows, c.Tables[name]...)
			}
			schema[name] = inferPolicyFields(rows)
		}
		r.collection(name, schema[name]...)
	}
	types := map[string]map[string]string{}
	for name, fields := range schema {
		types[name] = map[string]string{}
		for _, field := range fields {
			parts := strings.SplitN(field, ":", 2)
			types[name][parts[0]] = parts[1]
		}
	}
	r.operator("db_access_grant", map[string]any{"app": "graphql", "access": "read"})
	r.requireAuth()
	sdl := fmt.Sprintf("type Row { %s: String }\ntype Query { %s: [Row!]! }", idField, resource)
	if failure := r.publish(sdl, map[string]map[string]any{"Query." + resource: {"operation": "database_pipeline", "config": plan}}); failure != "" {
		t.Fatal(failure)
	}

	checked := 0
	for i, c := range vectors.Cases {
		expected, ok := c.Expected[resource]
		if !ok {
			continue
		}
		// A fresh subject per case keeps identities independent.
		subject := fmt.Sprint(5000 + i)
		r.permissions[subject] = append([]string{}, c.Actor.Permissions...)
		for _, name := range collections {
			r.operator("db_delete", map[string]any{"collection": name, "all": true, "maxAffected": 1000})
			rows := []map[string]any{}
			for _, row := range c.Tables[name] {
				rows = append(rows, policyRow(row, types[name], c.Actor.ID, subject))
			}
			if len(rows) > 0 {
				r.insert(name, rows...)
			}
		}
		out := r.queryAs(subject, fmt.Sprintf("{ %s { %s } }", resource, idField), nil)
		got := []string{}
		if len(out.Errors) > 0 {
			t.Errorf("%s: %v", c.Name, out.Errors)
			continue
		}
		for _, raw := range out.Data[resource].([]any) {
			got = append(got, fmt.Sprint(raw.(map[string]any)[idField]))
		}
		sort.Strings(got)
		want := append([]string{}, expected...)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: got %v, want %v", c.Name, got, want)
		}
		checked++
	}
	if checked == 0 {
		t.Fatalf("no vector has a %s expectation", resource)
	}
	t.Logf("%d of %d vectors have an expectation for %s; all match on a real Database", checked, len(vectors.Cases), resource)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// inferPolicyFields types columns from vector rows: numbers as integer,
// "[..." strings as json, other strings as text.
func inferPolicyFields(rows []map[string]any) []string {
	kinds := map[string]string{"id": "integer"}
	for _, row := range rows {
		for field, value := range row {
			switch typed := value.(type) {
			case float64:
				if kinds[field] == "" {
					kinds[field] = "integer"
				}
			case string:
				if strings.HasPrefix(typed, "[") {
					kinds[field] = "json"
				} else if kinds[field] == "" || kinds[field] == "integer" && field != "id" {
					kinds[field] = "text"
				}
			}
		}
	}
	fields := []string{"id:" + kinds["id"]}
	names := []string{}
	for field := range kinds {
		if field != "id" {
			names = append(names, field)
		}
	}
	sort.Strings(names)
	for _, field := range names {
		fields = append(fields, field+":"+kinds[field])
	}
	return fields
}

// policyRow types a vector row for Database: integers as decimal strings,
// legacy JSON-encoded arrays decoded, and the actor's id replaced by subject.
func policyRow(row map[string]any, types map[string]string, actor, subject string) map[string]any {
	out := map[string]any{}
	for field, value := range row {
		if value == actor && strings.HasSuffix(field, "user_id") {
			value = subject
		}
		switch types[field] {
		case "integer":
			if value != nil {
				value = fmt.Sprint(value)
			}
		case "json":
			if text, ok := value.(string); ok {
				var decoded any
				if json.Unmarshal([]byte(text), &decoded) == nil {
					value = decoded
				}
			}
		case "text":
			if number, ok := value.(float64); ok {
				value = fmt.Sprint(number)
			}
		}
		out[field] = value
	}
	return out
}
