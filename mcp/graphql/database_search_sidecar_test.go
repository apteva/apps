package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestRealDatabaseTextSearch runs a searched, scoped, paged list against a real
// Database (0.7.0 or later):
//
//	GRAPHQL_TEST_DATABASE_DIR=<database>/mcp/database GOWORK=off go test -run TestRealDatabaseTextSearch -v
func TestRealDatabaseTextSearch(t *testing.T) {
	r := newRealDatabase(t, "graphql-database-text-search")
	r.collection("profiles", "id:text", "user_id:text", "centre_id:text")
	r.collection("assignments", "id:text", "centre_id:text", "resource_key:text", "opportunity_key:text", "commercial_id:text", "amount:integer", "status:text")
	r.operator("db_access_grant", map[string]any{"app": "graphql", "access": "read"})
	r.requireAuth()
	r.insert("profiles", map[string]any{"id": "p1", "user_id": "41", "centre_id": "centre-a"})
	row := func(id, centre, resource, opportunity, commercial, status string) map[string]any {
		return map[string]any{"id": id, "centre_id": centre, "resource_key": resource, "opportunity_key": opportunity, "commercial_id": commercial, "amount": "1", "status": status}
	}
	r.insert("assignments",
		row("a1", "centre-a", "ABC-23", "opp", "alice", "open"),
		row("a2", "centre-a", "xyz", "OPP-9", "bob", "open"),
		row("a3", "centre-a", "abc-77", "other", "carol", "closed"),
		row("b1", "centre-b", "ABC-23", "opp", "alice", "open"), // outside the caller's scope
	)
	config := func(where ...any) map[string]any {
		and := append([]any{map[string]any{"field": "centre_id", "op": "in", "value": "$scope.centres"}}, where...)
		plan := decodeConfig(t, `{
		  "params": {"user": {"from": "$identity.subject", "required": true}, "q": {"from": "$args.q"}, "status": {"from": "$args.status"}, "first": {"from": "$args.first", "type": "number"}, "after": {"from": "$args.after"}},
		  "access": {"values": {"centres": {"optional": true, "otherwise": {"collection": "profiles", "field": "centre_id",
		    "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}}}},
		  "steps": [
		    {"id": "rows", "op": "find", "max_rows": 100, "page": {"size": "$param.first", "cursor": "$param.after"},
		     "args": {"collection": "assignments", "orderBy": [{"field": "id"}]}},
		    {"id": "total", "op": "count", "count_of": "rows"}],
		  "result": {"rows": "$steps.rows", "total": "$steps.total", "next": "$steps.rows.nextCursor"}}`)
		plan["steps"].([]any)[0].(map[string]any)["args"].(map[string]any)["where"] = map[string]any{"and": and}
		plan["database"] = "crm"
		return plan
	}
	search := func(fields ...any) map[string]any {
		return map[string]any{"fields": fields, "op": "like_contains_ascii", "value": "$param.q"}
	}
	good := search("resource_key", "opportunity_key", "commercial_id")
	sdl := func(argument string) string {
		return `type Row { id: ID! } type Page { rows: [Row!]! total: Int! next: String }
		type Query { assignments(` + argument + `, first: Int, after: String): Page! }`
	}
	publish := func(argument string, plan map[string]any) string {
		return r.publish(sdl(argument), map[string]map[string]any{"Query.assignments": {"operation": "database_pipeline", "config": plan}})
	}

	// Publication refuses what could only fail when executed.
	for name, c := range map[string]struct {
		argument string
		plan     map[string]any
		want     string
	}{
		"missing field":  {`q: String = ""`, config(search("resource_key", "nope")), "cannot run"},
		"non-text field": {`q: String = ""`, config(search("resource_key", "amount")), "cannot run"},
		"no default":     {`q: String`, config(good), "must be String!"},
		"null default":   {`q: String = null`, config(good), "must be String!"},
		"not a string":   {`q: Int = 0`, config(good), "String argument"},
		"undeclared":     {`other: String = ""`, config(good), "String argument"},
	} {
		if failure := publish(c.argument, c.plan); !strings.Contains(failure, c.want) {
			t.Errorf("%s: publish returned %q, want %q", name, failure, c.want)
		}
	}

	filters := []any{good, map[string]any{"field": "status", "op": "eq", "value": "open"}}
	if failure := publish(`q: String = ""`, config(filters...)); failure != "" {
		t.Fatal(failure)
	}
	r.permissions["41"], r.permissions["42"] = []string{}, []string{}
	list := func(user, args string) (string, string, string) {
		t.Helper()
		out := r.queryAs(user, `{ assignments`+args+` { rows { id } total next } }`, nil)
		if out.code() != "" {
			return "", "", out.code()
		}
		page := out.Data["assignments"].(map[string]any)
		ids := []string{}
		for _, raw := range page["rows"].([]any) {
			ids = append(ids, fmt.Sprint(raw.(map[string]any)["id"]))
		}
		return strings.Join(ids, ","), fmt.Sprint(page["total"]), ""
	}
	for name, c := range map[string]struct{ user, args, ids, total string }{
		"default empty search":      {"41", "", "a1,a2", "2"},
		"explicit empty":            {"41", `(q: "")`, "a1,a2", "2"},
		"one field, folded":         {"41", `(q: "abc-")`, "a1", "1"},
		"second field":              {"41", `(q: "opp-9")`, "a2", "1"},
		"across the joined fields":  {"41", `(q: "23 opp")`, "a1", "1"},
		"wildcard":                  {"41", `(q: "a_c")`, "a1", "1"},
		"status filter still holds": {"41", `(q: "carol")`, "", "0"},
		"no match":                  {"41", `(q: "zzz")`, "", "0"},
		// The match in centre-b must stay out, and a wildcard cannot reach it.
		"wildcard stays in scope": {"41", `(q: "%")`, "a1,a2", "2"},
		// No profile: empty scope, whatever is searched.
		"no scope, no search": {"42", "", "", "0"},
		"no scope, search":    {"42", `(q: "ABC")`, "", "0"},
		"no scope, wildcard":  {"42", `(q: "%")`, "", "0"},
	} {
		ids, total, code := list(c.user, c.args)
		if code != "" || ids != c.ids || total != c.total {
			t.Errorf("%s: got rows %q total %s (%s), want rows %q total %s", name, ids, total, code, c.ids, c.total)
		}
	}
	// Paging keeps the total of the whole search.
	if ids, total, _ := list("41", `(q: "", first: 1)`); ids != "a1" || total != "2" {
		t.Errorf("first page: rows %q total %s", ids, total)
	}
	// Database's limit stays a client error, not a dropped filter.
	if _, _, code := list("41", `(q: "`+strings.Repeat("x", 1025)+`")`); code != "invalid_argument" {
		t.Errorf("oversized search: %s", code)
	}
	t.Log("publish rejections, narrowing inside the resolved scope, page and total agree on Database")
}
