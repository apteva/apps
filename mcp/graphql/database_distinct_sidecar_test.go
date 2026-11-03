package main

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// TestRealDatabaseDistinctAccess runs a centre policy whose sale-centre value
// is a distinct read against a real Database:
//
//	centres = home centre UNION (sale centres of granted offers,
//	          intersected with the grant's centres when it lists any)
//
//	GRAPHQL_TEST_DATABASE_DIR=<database>/mcp/database GOWORK=off go test -run TestRealDatabaseDistinctAccess -v
func TestRealDatabaseDistinctAccess(t *testing.T) {
	r := newRealDatabase(t, "graphql-database-distinct-access")
	r.collection("profiles", "id:integer", "user_id:text", "centre_id:text")
	r.collection("grants", "id:integer", "user_id:text", "offer_ids:json", "centre_ids:json")
	r.collection("sales", "id:integer", "offer_id:text", "centre_id:text")
	r.collection("centres", "id:text")
	r.operator("db_access_grant", map[string]any{"app": "graphql", "access": "read"})
	r.requireAuth()
	const maxCentres = 3
	plan := decodeConfig(t, fmt.Sprintf(`{
	  "params": {"user": {"from": "$identity.subject", "required": true}},
	  "access": {"values": {
	    "home": {"optional": true, "otherwise": {"collection": "profiles", "field": "centre_id",
	      "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}},
	    "grantOffers": {"optional": true, "otherwise": {"collection": "grants", "field": "offer_ids",
	      "where": {"field": "user_id", "op": "eq", "value": "$param.user"}, "orderBy": [{"field": "id", "direction": "desc"}], "limit": 1}},
	    "grantCentres": {"optional": true, "otherwise": {"collection": "grants", "field": "centre_ids",
	      "where": {"field": "user_id", "op": "eq", "value": "$param.user"}, "orderBy": [{"field": "id", "direction": "desc"}], "limit": 1}},
	    "saleCentres": {"optional": true, "otherwise": {"collection": "sales", "field": "centre_id", "distinct": true, "max": %d,
	      "where": {"field": "offer_id", "op": "in", "value": "$scope.grantOffers"}}},
	    "restricted": {"optional": true, "intersect": ["saleCentres", "grantCentres"]},
	    "effective": {"optional": true, "expr": {"op": "if",
	      "condition": {"op": "gt", "args": [{"op": "length", "args": ["$scope.grantCentres"]}, {"const": 0}]},
	      "then": "$scope.restricted", "else": "$scope.saleCentres"}},
	    "centres": {"optional": true, "union": ["home", "effective"]}
	  }},
	  "steps": [{"id": "rows", "op": "find", "args": {"collection": "centres", "where": {"field": "id", "op": "in", "value": "$scope.centres"}, "orderBy": [{"field": "id"}]}}],
	  "result": "$steps.rows"
	}`, maxCentres))
	plan["database"] = "crm"
	validated, err := validateDatabasePipeline(plan)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(validated.Collections()); !strings.Contains(got, "sales") || !strings.Contains(got, "grants") || !strings.Contains(got, "profiles") {
		t.Fatalf("subscriptions must watch every access collection: %s", got)
	}
	if failure := r.publish("type Centre { id: ID }\ntype Query { centres: [Centre!]! }",
		map[string]map[string]any{"Query.centres": {"operation": "database_pipeline", "config": plan}}); failure != "" {
		t.Fatal(failure)
	}

	nextID := 0
	id := func() string { nextID++; return fmt.Sprint(nextID) }
	reset := func() {
		// db_delete affects at most 1000 records per call; ids are sequential.
		for _, name := range []string{"profiles", "grants", "sales"} {
			for lo := 0; lo <= nextID; lo += 1000 {
				r.operator("db_delete", map[string]any{"collection": name, "maxAffected": 1000, "where": map[string]any{"and": []any{
					map[string]any{"field": "id", "op": "gte", "value": fmt.Sprint(lo)}, map[string]any{"field": "id", "op": "lt", "value": fmt.Sprint(lo + 1000)}}}})
			}
		}
		r.operator("db_delete", map[string]any{"collection": "centres", "all": true, "maxAffected": 1000})
		centres := []map[string]any{}
		for _, c := range []string{"a", "b", "c", "d", "e"} {
			centres = append(centres, map[string]any{"id": "centre-" + c})
		}
		r.insert("centres", centres...)
	}
	sales := func(offer, centre string, n int) {
		for start := 0; start < n; start += 500 {
			rows := []map[string]any{}
			for i := start; i < n && i < start+500; i++ {
				rows = append(rows, map[string]any{"id": id(), "offer_id": offer, "centre_id": centre})
			}
			r.insert("sales", rows...)
		}
	}
	subject := 7000
	query := func() (string, string) {
		t.Helper()
		subject++
		user := fmt.Sprint(subject)
		r.permissions[user] = []string{}
		// Rows written for "me" belong to this request's subject.
		r.operator("db_update", map[string]any{"collection": "profiles", "where": map[string]any{"field": "user_id", "op": "eq", "value": "me"}, "set": map[string]any{"user_id": user}, "maxAffected": 100})
		r.operator("db_update", map[string]any{"collection": "grants", "where": map[string]any{"field": "user_id", "op": "eq", "value": "me"}, "set": map[string]any{"user_id": user}, "maxAffected": 100})
		out := r.queryAs(user, "{ centres { id } }", nil)
		ids := []string{}
		if rows, ok := out.Data["centres"].([]any); ok {
			for _, raw := range rows {
				ids = append(ids, fmt.Sprint(raw.(map[string]any)["id"]))
			}
		}
		sort.Strings(ids)
		// Hand the rows back to "me" for the next query.
		r.operator("db_update", map[string]any{"collection": "profiles", "where": map[string]any{"field": "user_id", "op": "eq", "value": user}, "set": map[string]any{"user_id": "me"}, "maxAffected": 100})
		r.operator("db_update", map[string]any{"collection": "grants", "where": map[string]any{"field": "user_id", "op": "eq", "value": user}, "set": map[string]any{"user_id": "me"}, "maxAffected": 100})
		return strings.Join(ids, ","), out.code()
	}
	expect := func(name, want, wantCode string) {
		t.Helper()
		got, code := query()
		if got != want || code != wantCode {
			t.Fatalf("%s: got %q (%s), want %q (%s)", name, got, code, want, wantCode)
		}
	}

	// The reproduced case: thousands of sales in one centre are one centre.
	reset()
	r.insert("profiles", map[string]any{"id": id(), "user_id": "me", "centre_id": "centre-a"})
	r.insert("grants", map[string]any{"id": id(), "user_id": "me", "offer_ids": []any{"offer-a"}, "centre_ids": []any{}})
	sales("offer-a", "centre-b", 2500)
	expect("2,500 sales in one centre", "centre-a,centre-b", "")

	// Unrelated offers never widen access, whatever their volume.
	sales("offer-z", "centre-e", 1200)
	expect("unrelated offer", "centre-a,centre-b", "")

	// Exactly max distinct sale centres pass; max+1 fail closed.
	sales("offer-a", "centre-c", 10)
	sales("offer-a", "centre-d", 10)
	expect("max distinct centres", "centre-a,centre-b,centre-c,centre-d", "")
	sales("offer-a", "centre-e", 1)
	expect("max+1 distinct centres", "", "source_result_truncated")

	// The grant's centre restriction narrows the sale centres.
	r.insert("grants", map[string]any{"id": id(), "user_id": "me", "offer_ids": []any{"offer-a"}, "centre_ids": []any{"centre-c"}})
	r.operator("db_delete", map[string]any{"collection": "sales", "where": map[string]any{"and": []any{map[string]any{"field": "offer_id", "op": "eq", "value": "offer-a"}, map[string]any{"field": "centre_id", "op": "eq", "value": "centre-e"}}}, "maxAffected": 1})
	expect("grant centre restriction", "centre-a,centre-c", "")

	// Latest enabled grant wins; an unknown offer adds nothing and never
	// becomes unrestricted.
	r.insert("grants", map[string]any{"id": id(), "user_id": "me", "offer_ids": []any{"offer-missing"}, "centre_ids": []any{}})
	expect("unknown offer", "centre-a", "")
	// An empty latest grant keeps the home centre only.
	r.insert("grants", map[string]any{"id": id(), "user_id": "me", "offer_ids": []any{}, "centre_ids": []any{}})
	expect("empty grant", "centre-a", "")

	// No profile and no grant: an empty, optional scope.
	reset()
	sales("offer-a", "centre-b", 50)
	expect("no profile or grant", "", "")
}

// TestRealDatabaseDistinctKeepsRecordCardinality checks that single_record
// still counts records: two profiles on one centre are two records.
func TestRealDatabaseDistinctKeepsRecordCardinality(t *testing.T) {
	r := newRealDatabase(t, "graphql-database-distinct-records")
	r.collection("profiles", "id:integer", "user_id:text", "centre_id:text")
	r.operator("db_access_grant", map[string]any{"app": "graphql", "access": "read"})
	r.requireAuth()
	plan := decodeConfig(t, `{"params": {"user": {"from": "$identity.subject", "required": true}},
	  "access": {"values": {"home": {"single_record": true, "otherwise": {"collection": "profiles", "field": "centre_id",
	    "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}}}},
	  "steps": [{"id": "n", "op": "count", "args": {"collection": "profiles", "where": {"field": "centre_id", "op": "in", "value": "$scope.home"}}}],
	  "result": "$steps.n"}`)
	plan["database"] = "crm"
	if failure := r.publish("type Query { n: Int }", map[string]map[string]any{"Query.n": {"operation": "database_pipeline", "config": plan}}); failure != "" {
		t.Fatal(failure)
	}
	r.insert("profiles", map[string]any{"id": "1", "user_id": "41", "centre_id": "centre-a"},
		map[string]any{"id": "2", "user_id": "42", "centre_id": "centre-a"}, map[string]any{"id": "3", "user_id": "42", "centre_id": "centre-a"})
	r.permissions["41"], r.permissions["42"] = []string{}, []string{}
	if out := r.queryAs("41", "{ n }", nil); out.code() != "" || fmt.Sprint(out.Data["n"]) != "3" {
		t.Fatalf("one profile: %v", out)
	}
	if out := r.queryAs("42", "{ n }", nil); out.code() != "permission_denied" {
		t.Fatalf("duplicate profiles on one centre must fail single_record: %v", out)
	}
	// distinct cannot be combined with single_record at all.
	values := plan["access"].(map[string]any)["values"].(map[string]any)
	values["home"].(map[string]any)["otherwise"].(map[string]any)["distinct"] = true
	if failure := r.publish("type Query { n: Int }", map[string]map[string]any{"Query.n": {"operation": "database_pipeline", "config": plan}}); !strings.Contains(failure, "single_record") {
		t.Fatalf("distinct with single_record published: %q", failure)
	}
}
