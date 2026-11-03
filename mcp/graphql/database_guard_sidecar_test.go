package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// TestRealDatabaseGuardedEdits proves, against a real Database sidecar, that a
// client cannot replace or weaken a resolver's access condition on writes and
// that Database's failure classes reach errors[].extensions.code.
//
//	GRAPHQL_TEST_DATABASE_DIR=<database>/mcp/database GOWORK=off go test -run TestRealDatabaseGuardedEdits -v
//
// Database before 0.6.5 rejects a key combined with a guard; the test then
// checks that GraphQL reports Database's invalid_argument and changes nothing.
// From 0.6.5 it checks the guarded edits themselves.
func TestRealDatabaseGuardedEdits(t *testing.T) {
	databaseDir := os.Getenv("GRAPHQL_TEST_DATABASE_DIR")
	if testing.Short() || databaseDir == "" {
		t.Skip("set GRAPHQL_TEST_DATABASE_DIR to Database source")
	}
	adapter := os.Getenv("GRAPHQL_TEST_DATABASE_ADAPTER")
	if adapter == "" {
		adapter = "aptevadb"
	}
	const project = "graphql-database-guard-test"
	database := tk.SpawnSidecar(t, databaseDir, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	operator := func(tool string, args map[string]any) map[string]any {
		args["database"] = "crm"
		return database.MCPAs(tool, args, 1, "", project)
	}
	operator("db_database_create", map[string]any{"adapter": adapter})
	operator("db_collection_create", map[string]any{"collection": "records", "primaryKey": []any{"id"}, "fields": []any{
		map[string]any{"name": "id", "type": "text"}, map[string]any{"name": "centre_id", "type": "text"}, map[string]any{"name": "notes", "type": "text"},
	}})
	operator("db_access_grant", map[string]any{"app": "graphql", "access": "write"})
	bridge := newDatabaseBridge(t, database, project)

	// asGraphQL calls Database exactly as the GraphQL install does (same
	// grant), without failing the test on a Database error.
	asGraphQL := func(tool string, args map[string]any) (map[string]any, string) {
		t.Helper()
		args["database"], args["scope"] = "crm", "project"
		body, _ := json.Marshal(map[string]any{"tool": tool, "input": args})
		resp, err := http.Post(bridge.URL+"/api/apps/callback/apps/database/call", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var envelope struct {
			Result struct {
				Content []struct{ Text string } `json:"content"`
				IsError bool                    `json:"isError"`
			} `json:"result"`
			Error *struct{ Message string } `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Error != nil {
			return nil, envelope.Error.Message
		}
		if len(envelope.Result.Content) == 0 {
			t.Fatalf("%s: empty result", tool)
		}
		if envelope.Result.IsError {
			return nil, envelope.Result.Content[0].Text
		}
		var out map[string]any
		_ = json.Unmarshal([]byte(envelope.Result.Content[0].Text), &out)
		return out, ""
	}
	get := func(id string) map[string]any {
		t.Helper()
		out, failure := asGraphQL("db_get", map[string]any{"collection": "records", "key": map[string]any{"id": id}})
		if failure != "" {
			t.Fatal(failure)
		}
		record, _ := out["record"].(map[string]any)
		return record
	}
	version := func(id string) int {
		var v int
		_, _ = fmt.Sscan(fmt.Sprint(get(id)["_version"]), &v)
		return v
	}
	unchanged := func(label string, before map[string]any, id string) {
		t.Helper()
		if after := get(id); fmt.Sprint(after) != fmt.Sprint(before) {
			t.Fatalf("%s changed %s: %v -> %v", label, id, before, after)
		}
	}
	operator("db_insert", map[string]any{"collection": "records", "records": []any{
		map[string]any{"id": "own", "centre_id": "centre_a", "notes": "original"},
		map[string]any{"id": "other", "centre_id": "centre_b", "notes": "original"},
		map[string]any{"id": "own_delete", "centre_id": "centre_a", "notes": "original"},
		map[string]any{"id": "other_delete", "centre_id": "centre_b", "notes": "original"},
	}})

	guard := map[string]any{"field": "centre_id", "op": "eq", "value": "centre_a"}
	graph := spawnGraph(t, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", bridge.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "graphql-guard-test"))
	graph.MCP("graphql_source_add", map[string]any{"name": "crm", "kind": "database", "config": map[string]any{"database": "crm", "collection": "records"}})
	for field, resolver := range map[string]map[string]any{
		"Query.records":         {"operation": "find", "config": map[string]any{"where": guard, "limit": 100}},
		"Mutation.editRecord":   {"operation": "update", "config": map[string]any{"where": guard}},
		"Mutation.bulkEdit":     {"operation": "update", "config": map[string]any{"where": guard, "maxAffected": 20}},
		"Mutation.removeRecord": {"operation": "delete", "config": map[string]any{"where": guard}},
	} {
		parts := strings.Split(field, ".")
		graph.MCP("graphql_resolver_set", map[string]any{"parent_type": parts[0], "field_name": parts[1], "source": "crm", "operation": resolver["operation"], "config": resolver["config"]})
	}
	schema := graph.MCP("graphql_schema_create", map[string]any{"environment": "development", "sdl": `input Filter { field: String op: String value: JSON and: [Filter!] or: [Filter!] }
scalar JSON
input RecordKey { id: ID! }
input RecordPatch { notes: String centre_id: String }
type Record { id: ID! centre_id: String! notes: String }
type WriteResult { affected: Int! }
type Query { records(where: Filter): [Record!]! }
type Mutation {
 editRecord(key: RecordKey!, set: RecordPatch!, ifVersion: Int, where: Filter, maxAffected: Int, all: Boolean): WriteResult!
 bulkEdit(set: RecordPatch!, where: Filter, maxAffected: Int, all: Boolean): WriteResult!
 removeRecord(key: RecordKey!, ifVersion: Int, where: Filter): WriteResult!
}`})
	graph.MCP("graphql_release_publish", map[string]any{"environment": "development", "schema_version": schema["schema"].(map[string]any)["version"]})

	type response struct {
		Data   map[string]any   `json:"data"`
		Errors []map[string]any `json:"errors"`
	}
	run := func(query string, variables map[string]any) response {
		t.Helper()
		var out response
		if resp := graph.POST("/graphql", map[string]any{"query": query, "variables": variables}, &out); resp.Status != 200 {
			t.Fatalf("graphql status %d: %v", resp.Status, out)
		}
		return out
	}
	code := func(r response) string {
		if len(r.Errors) == 0 {
			return ""
		}
		extensions, _ := r.Errors[0]["extensions"].(map[string]any)
		return fmt.Sprint(extensions["code"])
	}
	affected := func(r response, field string) any {
		if r.Data == nil || r.Data[field] == nil {
			return nil
		}
		return r.Data[field].(map[string]any)["affected"]
	}
	const edit = `mutation($id: ID!, $notes: String, $centre: String, $v: Int, $where: Filter, $max: Int, $all: Boolean) {
 editRecord(key: {id: $id}, set: {notes: $notes, centre_id: $centre}, ifVersion: $v, where: $where, maxAffected: $max, all: $all) { affected } }`

	// Reads only ever return the authorised centre, whatever the client filters.
	read := run(`query($w: Filter) { records(where: $w) { id centre_id } }`, map[string]any{"w": map[string]any{"or": []any{
		map[string]any{"field": "centre_id", "op": "eq", "value": "centre_b"}, map[string]any{"field": "id", "op": "eq", "value": "other"}}}})
	if len(read.Errors) > 0 {
		t.Fatalf("read: %v", read.Errors)
	}
	for _, raw := range read.Data["records"].([]any) {
		if raw.(map[string]any)["centre_id"] != "centre_a" {
			t.Fatalf("widened read returned %v", raw)
		}
	}

	// Where-only edits work on every Database version: a client filter that
	// tries to include another centre, and a raised bound, change nothing
	// outside the guard.
	otherBefore := get("other")
	widen := run(`mutation($w: Filter, $max: Int) { bulkEdit(set: {notes: "bulk"}, where: $w, maxAffected: $max, all: true) { affected } }`, map[string]any{
		"w": map[string]any{"field": "id", "op": "in", "value": []any{"own", "other"}}, "max": 500})
	if len(widen.Errors) > 0 || affected(widen, "bulkEdit") != float64(1) || get("own")["notes"] != "bulk" {
		t.Fatalf("bulk edit: %+v", widen)
	}
	unchanged("widened bulk edit", otherBefore, "other")

	_, probe := asGraphQL("db_update", map[string]any{"collection": "records", "key": map[string]any{"id": "own"}, "where": guard, "set": map[string]any{"notes": "probe"}})
	if strings.Contains(probe, "use an exact key or a filter") {
		// Database < 0.6.5: the combination is refused, and GraphQL now says so
		// with Database's own code instead of execution_error.
		ownBefore := get("own")
		r := run(edit, map[string]any{"id": "own", "notes": "changed", "v": version("own")})
		if code(r) != "invalid_argument" || affected(r, "editRecord") != nil || !strings.Contains(fmt.Sprint(r.Errors[0]["message"]), "use an exact key or a filter") {
			t.Fatalf("unsupported guarded key edit: %+v", r)
		}
		unchanged("refused edit", ownBefore, "own")
		t.Log("Database rejects key + guard (before 0.6.5): GraphQL reports invalid_argument and changes nothing")
		return
	}
	if probe != "" {
		t.Fatalf("probe: %s", probe)
	}

	// Authorised edit with the current revision updates exactly one record.
	v := version("own")
	if r := run(edit, map[string]any{"id": "own", "notes": "changed", "v": v}); len(r.Errors) > 0 || affected(r, "editRecord") != float64(1) {
		t.Fatalf("authorised edit: %+v", r)
	}
	if got := get("own"); got["notes"] != "changed" || version("own") != v+1 {
		t.Fatalf("authorised edit result: %v", got)
	}
	// A stale revision is a version_conflict and changes nothing.
	ownBefore := get("own")
	if r := run(edit, map[string]any{"id": "own", "notes": "stale", "v": v}); code(r) != "version_conflict" || affected(r, "editRecord") != nil {
		t.Fatalf("stale edit: %+v", r)
	}
	unchanged("stale edit", ownBefore, "own")
	// Another centre's record, with its correct key and revision, reads like a
	// conflict, also when the client adds an OR filter naming that centre, asks
	// for all records or a larger bound.
	for label, extra := range map[string]map[string]any{
		"plain":       {},
		"or filter":   {"where": map[string]any{"or": []any{map[string]any{"field": "centre_id", "op": "eq", "value": "centre_b"}, map[string]any{"field": "id", "op": "eq", "value": "other"}}}},
		"all + bound": {"all": true, "max": 1000},
	} {
		variables := map[string]any{"id": "other", "notes": "cross-centre", "v": version("other")}
		for key, value := range extra {
			variables[key] = value
		}
		if r := run(edit, variables); code(r) != "version_conflict" || affected(r, "editRecord") != nil {
			t.Fatalf("cross-centre edit (%s): %+v", label, r)
		}
		unchanged("cross-centre edit ("+label+")", otherBefore, "other")
	}
	// Without a revision a guard miss affects nothing.
	if r := run(edit, map[string]any{"id": "other", "notes": "cross-centre"}); len(r.Errors) > 0 || affected(r, "editRecord") != float64(0) {
		t.Fatalf("unversioned cross-centre edit: %+v", r)
	}
	unchanged("unversioned cross-centre edit", otherBefore, "other")
	// Moving a record out of the guard is allowed only for records inside it;
	// it cannot pull another centre's record in.
	if r := run(edit, map[string]any{"id": "other", "centre": "centre_a", "v": version("other")}); code(r) != "version_conflict" {
		t.Fatalf("claiming another centre's record: %+v", r)
	}
	unchanged("claim", otherBefore, "other")
	// A missing key is indistinguishable from a forbidden one.
	if r := run(edit, map[string]any{"id": "missing", "notes": "x", "v": 1}); code(r) != "version_conflict" {
		t.Fatalf("missing key: %+v", r)
	}
	// Two clients holding the same revision: exactly one wins.
	v = version("own")
	results := make([]response, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = run(edit, map[string]any{"id": "own", "notes": fmt.Sprintf("race %d", i), "v": v})
		}(i)
	}
	wg.Wait()
	winners, conflicts := 0, 0
	for _, r := range results {
		switch {
		case len(r.Errors) == 0 && affected(r, "editRecord") == float64(1):
			winners++
		case code(r) == "version_conflict":
			conflicts++
		}
	}
	if winners != 1 || conflicts != 1 || version("own") != v+1 {
		t.Fatalf("race: winners=%d conflicts=%d %+v", winners, conflicts, results)
	}
	// Deletes follow the same guard.
	const remove = `mutation($id: ID!, $v: Int, $w: Filter) { removeRecord(key: {id: $id}, ifVersion: $v, where: $w) { affected } }`
	otherDelete := get("other_delete")
	if r := run(remove, map[string]any{"id": "other_delete", "v": version("other_delete"), "w": map[string]any{"field": "centre_id", "op": "eq", "value": "centre_b"}}); code(r) != "version_conflict" {
		t.Fatalf("cross-centre delete: %+v", r)
	}
	unchanged("cross-centre delete", otherDelete, "other_delete")
	if r := run(remove, map[string]any{"id": "own_delete", "v": version("own_delete")}); len(r.Errors) > 0 || affected(r, "removeRecord") != float64(1) || get("own_delete") != nil {
		t.Fatalf("authorised delete: %+v", r)
	}
	t.Log("guarded key edits: authorised, stale, cross-centre (plain, OR filter, all + bound), unversioned, claim, missing, race and deletes behave as required")
}
