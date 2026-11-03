package main

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// TestRealDatabaseAccessChains runs the authorization chain
//
//	verified user -> latest enabled grant -> authorised offers ->
//	related grids and permitted centres -> filtered, paged records
//
// against a real Database, with amounts masked for callers lacking
// finance:read:
//
//	GRAPHQL_TEST_DATABASE_DIR=<database>/mcp/database GOWORK=off go test -run TestRealDatabaseAccessChains -v
func TestRealDatabaseAccessChains(t *testing.T) {
	r := newRealDatabase(t, "graphql-database-chain-test")
	r.collection("grants", "id:text", "user_id:text", "enabled:boolean", "granted_at:datetime")
	r.collection("grant_offers", "id:text", "grant_id:text", "offer_id:text")
	r.collection("offers", "id:text", "centre_id:text", "grid_ids:json")
	r.collection("profiles", "id:text", "user_id:text", "centre_id:text")
	r.collection("sales", "id:text", "centre_id:text", "grid_id:text", "client:text", "amount:integer")
	r.insert("grants",
		map[string]any{"id": "g1", "user_id": "42", "enabled": true, "granted_at": "2026-01-01T00:00:00Z"},
		map[string]any{"id": "g2", "user_id": "42", "enabled": true, "granted_at": "2026-06-01T00:00:00Z"},  // latest enabled
		map[string]any{"id": "g3", "user_id": "42", "enabled": false, "granted_at": "2026-09-01T00:00:00Z"}, // newer but disabled
		map[string]any{"id": "g4", "user_id": "43", "enabled": true, "granted_at": "2026-06-01T00:00:00Z"},
		map[string]any{"id": "g5", "user_id": "44", "enabled": true, "granted_at": "2026-06-01T00:00:00Z"},
	)
	r.insert("grant_offers",
		map[string]any{"id": "go1", "grant_id": "g1", "offer_id": "o1"},
		map[string]any{"id": "go2", "grant_id": "g2", "offer_id": "o2"},
		map[string]any{"id": "go3", "grant_id": "g2", "offer_id": "o3"},
		map[string]any{"id": "go4", "grant_id": "g3", "offer_id": "o4"},
		map[string]any{"id": "go5", "grant_id": "g4", "offer_id": "o4"},
		map[string]any{"id": "go6", "grant_id": "g5", "offer_id": "o2"},
	)
	r.insert("offers",
		map[string]any{"id": "o1", "centre_id": "c1", "grid_ids": []any{"grid1"}},
		map[string]any{"id": "o2", "centre_id": "c1", "grid_ids": []any{"grid2", "grid3"}},
		map[string]any{"id": "o3", "centre_id": "c2", "grid_ids": []any{"grid3"}},
		map[string]any{"id": "o4", "centre_id": "c3", "grid_ids": []any{"grid4"}},
	)
	r.insert("profiles",
		map[string]any{"id": "p42", "user_id": "42", "centre_id": "c1"},
		map[string]any{"id": "p43a", "user_id": "43", "centre_id": "c3"}, // two profiles, one centre
		map[string]any{"id": "p43b", "user_id": "43", "centre_id": "c3"},
		map[string]any{"id": "p44", "user_id": "44", "centre_id": "c1"},
	)
	centres, grids := []string{"c1", "c2", "c3"}, []string{"grid1", "grid2", "grid3", "grid4"}
	sales := []map[string]any{}
	want := map[string]int{} // visible to 42 and 44: centre c1, grids 2 or 3
	for i := 0; i < 60; i++ {
		sale := map[string]any{"id": fmt.Sprintf("s%02d", i), "centre_id": centres[i%3], "grid_id": grids[(i/3)%4], "client": fmt.Sprintf("client-%d", i), "amount": fmt.Sprint(100 + i)}
		sales = append(sales, sale)
		if sale["centre_id"] == "c1" && (sale["grid_id"] == "grid2" || sale["grid_id"] == "grid3") {
			want[sale["id"].(string)] = 100 + i
		}
	}
	r.insert("sales", sales...)
	r.operator("db_access_grant", map[string]any{"app": "graphql", "access": "read"})

	r.permissions = map[string][]string{
		"42": {"sales:read", "finance:read"},
		"43": {"sales:read", "finance:read"},
		"44": {"sales:read"},
		"9":  {"other"},
	}
	r.requireAuth()
	sdl := `type Sale { id: ID! centre_id: String grid_id: String client: String amount: Int }
type SalePage { rows: [Sale!]! hasMore: Boolean! nextCursor: String total: Int! }
type Query { sales(first: Int, after: String): SalePage! }`
	config := decodeConfig(t, `{
  "params": {
    "user": {"from": "$identity.subject", "required": true},
    "perms": {"from": "$identity.permissions", "type": "string_list"},
    "first": {"from": "$args.first", "type": "number"},
    "after": {"from": "$args.after"}
  },
  "access": {"require_any_permission": ["sales:read"], "values": {
    "grant": {"otherwise": {"collection": "grants", "field": "id",
      "where": {"and": [{"field": "user_id", "op": "eq", "value": "$param.user"}, {"field": "enabled", "op": "eq", "value": true}]},
      "orderBy": [{"field": "granted_at", "direction": "desc"}], "limit": 1}},
    "offers": {"otherwise": {"collection": "grant_offers", "field": "offer_id", "where": {"field": "grant_id", "op": "in", "value": "$scope.grant"}}},
    "grids": {"otherwise": {"collection": "offers", "field": "grid_ids", "where": {"field": "id", "op": "in", "value": "$scope.offers"}}},
    "offerCentres": {"otherwise": {"collection": "offers", "field": "centre_id", "where": {"field": "id", "op": "in", "value": "$scope.offers"}}},
    "home": {"otherwise": {"collection": "profiles", "field": "centre_id", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}, "single_record": true},
    "centres": {"intersect": ["offerCentres", "home"]}
  }},
  "steps": [
    {"id": "list", "op": "find", "max_rows": 50, "page": {"size": "$param.first", "cursor": "$param.after"},
     "args": {"collection": "sales", "orderBy": [{"field": "id"}], "where": {"and": [
       {"field": "grid_id", "op": "in", "value": "$scope.grids"},
       {"field": "centre_id", "op": "in", "value": "$scope.centres"}]}}},
    {"id": "total", "op": "count", "count_of": "list"}
  ],
  "combine": {"view": {"join": ["list"], "on": ["id"], "keep": ["list"], "fields": {
    "amount": {"op": "if", "condition": {"op": "in", "args": [{"const": "finance:read"}, "$param.perms"]}, "then": "list.amount", "else": {"const": null}}}}},
  "result": {"rows": "$combine.view", "hasMore": "$steps.list.hasMore", "nextCursor": "$steps.list.nextCursor", "total": "$steps.total"}
}`)
	if failure := r.publish(sdl, map[string]map[string]any{"Query.sales": {"operation": "database_pipeline", "config": config}}); failure != "" {
		t.Fatal(failure)
	}
	const query = `query($first: Int, $after: String) { sales(first: $first, after: $after) { rows { id centre_id grid_id amount } hasMore nextCursor total } }`

	// Walk every page; the union of pages is exactly the authorised records.
	walk := func(user string) (map[string]any, int) {
		t.Helper()
		seen, pages := map[string]any{}, 0
		var after any
		for {
			out := r.queryAs(user, query, map[string]any{"first": 4, "after": after})
			if len(out.Errors) > 0 {
				t.Fatalf("user %s page %d: %v", user, pages, out.Errors)
			}
			page := out.Data["sales"].(map[string]any)
			pages++
			if total := int(page["total"].(float64)); total != len(want) {
				t.Fatalf("total %d, want %d", total, len(want))
			}
			rows := page["rows"].([]any)
			if len(rows) > 4 {
				t.Fatalf("page of %d rows", len(rows))
			}
			for _, raw := range rows {
				row := raw.(map[string]any)
				if row["centre_id"] != "c1" || (row["grid_id"] != "grid2" && row["grid_id"] != "grid3") {
					t.Fatalf("user %s saw an unauthorised record %v", user, row)
				}
				if _, dup := seen[row["id"].(string)]; dup {
					t.Fatalf("record %s on two pages", row["id"])
				}
				seen[row["id"].(string)] = row["amount"]
			}
			if page["hasMore"] != true {
				if page["nextCursor"] != nil {
					t.Fatalf("last page has a cursor")
				}
				return seen, pages
			}
			after = page["nextCursor"]
		}
	}
	finance, pages := walk("42")
	if len(finance) != len(want) || pages != (len(want)+3)/4 {
		t.Fatalf("finance caller: %d records over %d pages, want %d", len(finance), pages, len(want))
	}
	for id, amount := range want {
		if finance[id] != float64(amount) {
			t.Fatalf("amount for %s: %v, want %d", id, finance[id], amount)
		}
	}
	masked, _ := walk("44")
	for id, amount := range masked {
		if amount != nil {
			t.Fatalf("44 lacks finance:read but saw %s amount %v", id, amount)
		}
	}
	if len(masked) != len(want) {
		t.Fatalf("masked caller saw %d records, want %d", len(masked), len(want))
	}

	// Refusals return no data: two profiles on one centre, a caller without
	// sales:read, an unauthenticated caller.
	for user, code := range map[string]string{"43": "permission_denied", "9": "permission_denied"} {
		out := r.queryAs(user, query, map[string]any{"first": 4})
		if out.code() != code || out.Data["sales"] != nil {
			t.Fatalf("user %s: %s %v", user, out.code(), out.Data)
		}
	}
	if out := r.queryAs("1000", query, nil); len(out.Errors) == 0 || out.Data["sales"] != nil {
		t.Fatalf("unauthenticated: %v", out)
	}
	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	t.Logf("grant g2 -> offers o2,o3 -> grids grid2,grid3 x centre c1: %d records over %d pages (%s); amounts masked without finance:read; two-profile and unpermitted callers refused",
		len(want), pages, strings.Join(ids, ","))
}
