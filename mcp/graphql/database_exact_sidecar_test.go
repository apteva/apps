package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// exactPolicyValues is a grant chain like a partner policy: a call is visible
// when its centre is the caller's home, or holds a sale on a granted offer, and
// no grant at all selects the normal catalogue. The home is trimmed either way.
func exactPolicyValues(mode string) string {
	latest := func(field string) string {
		return `{"collection": "grants", "field": "` + field + `", "orderBy": [{"field": "id", "direction": "desc"}], "limit": 1,
		  "where": {"and": [{"field": "user_id", "op": "eq", "value": "$param.user"}, {"field": "enabled", "op": "eq", "value": "1"}]}}`
	}
	const noOffers = `{"op": "eq", "args": [{"op": "length", "args": ["$scope.grantOffers"]}, {"const": 0}]}`
	const noCentres = `{"op": "eq", "args": [{"op": "length", "args": ["$scope.grantCentres"]}, {"const": 0}]}`
	return `{
	  "home": {"optional": true, "otherwise": {"collection": "profiles", "field": "centre", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}},
	  "catalogue": {"optional": true, "otherwise": {"collection": "catalogue", "field": "name"}},
	  "grantOffers": {` + mode + `"optional": true, "otherwise": ` + latest("offer_ids") + `},
	  "grantCentres": {` + mode + `"optional": true, "otherwise": ` + latest("centre_ids") + `},
	  "saleCentres": {` + mode + `"optional": true, "otherwise": {"collection": "sales", "field": "sale_centre", "distinct": true, "where": {"field": "offer_id", "op": "in", "value": "$scope.grantOffers"}}},
	  "restricted": {` + mode + `"optional": true, "intersect": ["saleCentres", "grantCentres"]},
	  "effective": {` + mode + `"optional": true, "expr": {"op": "if", "condition": ` + noCentres + `, "then": "$scope.saleCentres", "else": "$scope.restricted"}},
	  "homeAndSales": {` + mode + `"optional": true, "union": ["home", "effective"]},
	  "authorised": {` + mode + `"optional": true, "expr": {"op": "if", "condition": ` + noOffers + `, "then": "$scope.catalogue", "else": "$scope.homeAndSales"}}}`
}

func exactCallsPlan(t *testing.T, params, values, scope string) map[string]any {
	t.Helper()
	plan := decodeConfig(t, `{
	  "params": {"user": {"from": "$identity.subject", "required": true}, "first": {"from": "$args.first", "type": "number"}, "after": {"from": "$args.after"}`+params+`},
	  "access": {"values": `+values+`},
	  "steps": [
	    {"id": "rows", "op": "find", "max_rows": 1000, "page": {"size": "$param.first", "cursor": "$param.after"},
	     "args": {"collection": "calls", "orderBy": [{"field": "id", "direction": "desc"}], "where": {"field": "centre", "op": "in", "value": "$scope.`+scope+`"}}},
	    {"id": "total", "op": "count", "count_of": "rows"}],
	  "result": {"rows": "$steps.rows", "total": "$steps.total", "next": "$steps.rows.nextCursor"}}`)
	plan["database"] = "crm"
	return plan
}

// TestRealDatabaseExactAccessValues runs exact identifiers through reads,
// distinct reads, set operations, expressions, requested values, paging and live
// updates on a real Database, with the default mode alongside it.
//
//	GRAPHQL_TEST_DATABASE_DIR=<database>/mcp/database GOWORK=off go test -run TestRealDatabaseExactAccessValues -v
func TestRealDatabaseExactAccessValues(t *testing.T) {
	r := newRealDatabase(t, "graphql-database-exact-values")
	r.collection("calls", "id:integer", "centre:text")
	r.collection("sales", "id:integer", "offer_id:text", "sale_centre:text")
	r.collection("profiles", "id:integer", "user_id:text", "centre:text")
	r.collection("catalogue", "id:integer", "name:text")
	r.collection("grants", "id:integer", "user_id:text", "enabled:integer", "offer_ids:json", "centre_ids:json")
	r.operator("db_access_grant", map[string]any{"app": "graphql", "access": "read"})
	r.requireAuth()

	const reader = "42"
	r.permissions[reader] = []string{}
	r.insert("calls",
		map[string]any{"id": "1", "centre": "centre-a"},
		map[string]any{"id": "2", "centre": "centre-a"},
		map[string]any{"id": "3", "centre": "centre-b"},
		map[string]any{"id": "4", "centre": " centre-a "},
		map[string]any{"id": "5", "centre": ""},
		map[string]any{"id": "6", "centre": "home"},
		map[string]any{"id": "7", "centre": "centre-c"},
		map[string]any{"id": "8", "centre": nil})
	r.insert("sales",
		map[string]any{"id": "1", "offer_id": "offer-a", "sale_centre": "centre-a"},
		map[string]any{"id": "2", "offer_id": "offer-a", "sale_centre": "centre-a"}, // a duplicate centre
		map[string]any{"id": "3", "offer_id": "offer-a", "sale_centre": "centre-b"},
		map[string]any{"id": "4", "offer_id": " offer-a ", "sale_centre": " centre-a "},
		map[string]any{"id": "5", "offer_id": "", "sale_centre": ""})
	r.insert("profiles", map[string]any{"id": "1", "user_id": reader, "centre": "  home "})
	r.insert("catalogue",
		map[string]any{"id": "1", "name": "centre-a"}, map[string]any{"id": "2", "name": "centre-b"},
		map[string]any{"id": "3", "name": "centre-c"}, map[string]any{"id": "4", "name": "home"})
	r.insert("grants", map[string]any{"id": "1", "user_id": reader, "enabled": "1", "offer_ids": []any{"offer-a"}, "centre_ids": []any{"centre-a"}})

	const exact = `"type": "exact_string_list", `
	const exactParam = `, "centres": {"from": "$args.centres", "type": "exact_string_list"}`
	const normalParam = `, "centres": {"from": "$args.centres", "type": "string_list"}`
	requested := func(mode, param string) string {
		return `{"visible": {` + mode + `"requested": "$param.centres", "otherwise": {"collection": "grants", "field": "centre_ids", "orderBy": [{"field": "id", "direction": "desc"}], "limit": 1,
		  "where": {"and": [{"field": "user_id", "op": "eq", "value": "$param.user"}, {"field": "enabled", "op": "eq", "value": "1"}]}}}}`
	}
	exactPolicy := exactCallsPlan(t, "", exactPolicyValues(exact), "authorised")
	failure := r.publish(`type Row { id: ID! } type Page { rows: [Row!]! total: Int! next: String }
type Query {
  exactCalls(first: Int = 100, after: String): Page!
  normalCalls(first: Int = 100, after: String): Page!
  exactRequested(centres: [String!]! = [], first: Int = 100, after: String): Page!
  normalRequested(centres: [String!]! = [], first: Int = 100, after: String): Page!
}
type Subscription { liveExact(first: Int = 100, after: String): Page! }`, map[string]map[string]any{
		"Query.exactCalls":       {"operation": "database_pipeline", "config": exactPolicy},
		"Query.normalCalls":      {"operation": "database_pipeline", "config": exactCallsPlan(t, "", exactPolicyValues(""), "authorised")},
		"Query.exactRequested":   {"operation": "database_pipeline", "config": exactCallsPlan(t, exactParam, requested(exact, exactParam), "visible")},
		"Query.normalRequested":  {"operation": "database_pipeline", "config": exactCallsPlan(t, normalParam, requested("", normalParam), "visible")},
		"Subscription.liveExact": {"operation": "database_pipeline", "config": exactPolicy},
	})
	if failure != "" {
		t.Fatal(failure)
	}

	ask := func(field string, variables map[string]any) (string, string, string, string) {
		t.Helper()
		out := r.queryAs(reader, fmt.Sprintf(`query($first: Int, $after: String%s) { %s(first: $first, after: $after%s) { rows { id } total next } }`,
			map[bool]string{true: ", $centres: [String!]!"}[strings.HasSuffix(field, "Requested")], field,
			map[bool]string{true: ", centres: $centres"}[strings.HasSuffix(field, "Requested")]), variables)
		data, _ := out.Data[field].(map[string]any)
		ids := []string{}
		list, _ := data["rows"].([]any)
		for _, raw := range list {
			ids = append(ids, fmt.Sprint(raw.(map[string]any)["id"]))
		}
		next := ""
		if data["next"] != nil {
			next = fmt.Sprint(data["next"])
		}
		return strings.Join(ids, ","), fmt.Sprint(data["total"]), next, out.code()
	}
	grant := func(offers, centres []any) {
		t.Helper()
		r.operator("db_update", map[string]any{"collection": "grants", "where": map[string]any{"field": "id", "op": "eq", "value": "1"},
			"set": map[string]any{"offer_ids": offers, "centre_ids": centres}, "maxAffected": 1})
	}
	list := func(items ...any) []any { return append([]any{}, items...) }

	const (
		onlyHome     = "6"
		fallback     = "7,6,3,2,1"
		offerA       = "6,3,2,1"
		offerACentre = "6,2,1"
	)
	for _, c := range []struct {
		name          string
		offers        []any
		centres       []any
		exact, normal string
	}{
		{"valid grant", list("offer-a"), list("centre-a"), offerACentre, offerACentre},
		{"offer alone", list("offer-a"), list(), offerA, offerA},
		{"truly empty grant", list(), list("centre-a"), fallback, fallback},
		{"blank offer is not an empty grant", list(""), list("centre-a"), onlyHome, fallback},
		{"spaces are not an empty grant", list("   "), list(), onlyHome, fallback},
		{"padded offer does not match an unpadded one", list(" offer-a "), list("centre-a"), onlyHome, offerACentre},
		{"padded grant centre stays exact", list("offer-a"), list(" centre-a "), onlyHome, offerACentre},
		{"blank grant centre keeps the restriction", list("offer-a"), list(""), onlyHome, offerA},
		{"a real blank identifier matches", list(""), list(), "6,5", fallback},
		{"a real padded identifier matches", list(" offer-a "), list(), "6,4", offerA},
		{"padded through both lists", list(" offer-a "), list(" centre-a "), "6,4", offerACentre},
		{"unknown offer", list("no-such-offer"), list(), onlyHome, onlyHome},
		{"duplicates change nothing", list("offer-a", "offer-a"), list("centre-a", "centre-a"), offerACentre, offerACentre},
	} {
		t.Run(c.name, func(t *testing.T) {
			grant(c.offers, c.centres)
			for field, want := range map[string]string{"exactCalls": c.exact, "normalCalls": c.normal} {
				ids, total, _, code := ask(field, map[string]any{})
				count := strings.Count(want, ",") + 1
				if code != "" || ids != want || total != fmt.Sprint(count) {
					t.Errorf("%s: got %q total %s (%s), want %q", field, ids, total, code, want)
				}
			}
		})
	}

	// Requested values: exact membership, and never wider than the grant.
	for _, c := range []struct {
		name      string
		granted   []any
		requested []any
		exact     string // "" means refused
		normal    string
	}{
		{"nothing requested", list("centre-a", " centre-a ", ""), list(), "5,4,2,1", "2,1"},
		{"one", list("centre-a", " centre-a ", ""), list("centre-a"), "2,1", "2,1"},
		{"padded and granted", list("centre-a", " centre-a ", ""), list(" centre-a "), "4", "2,1"},
		{"blank and granted", list("centre-a", " centre-a ", ""), list(""), "5", "2,1"},
		{"padded, not granted", list("centre-a"), list(" centre-a "), "", "2,1"},
		{"blank, not granted", list("centre-a"), list(""), "", "2,1"},
		{"spaces, not granted", list(""), list("  "), "", ""},
		{"unknown", list("centre-a"), list("no-such-centre"), "", ""},
		{"outside", list("centre-a"), list("centre-b"), "", ""},
	} {
		t.Run("requested: "+c.name, func(t *testing.T) {
			grant(list("offer-a"), c.granted)
			for field, want := range map[string]string{"exactRequested": c.exact, "normalRequested": c.normal} {
				ids, _, _, code := ask(field, map[string]any{"centres": c.requested})
				if want == "" && (code == "" || ids != "") {
					t.Errorf("%s: must be refused, got %q (%s)", field, ids, code)
				}
				if want != "" && (code != "" || ids != want) {
					t.Errorf("%s: got %q (%s), want %q", field, ids, code, want)
				}
			}
		})
	}

	// A cursor belongs to the resolved filter, so a padded grant isn't the same scope.
	grant(list("offer-a"), list())
	first, total, cursor, code := ask("exactCalls", map[string]any{"first": 2})
	if first != "6,3" || total != "4" || cursor == "" || code != "" {
		t.Fatalf("first page: %q total %s next %q (%s)", first, total, cursor, code)
	}
	if ids, _, _, code := ask("exactCalls", map[string]any{"first": 2, "after": cursor}); ids != "2,1" || code != "" {
		t.Errorf("second page: %q (%s)", ids, code)
	}
	grant(list(" offer-a "), list())
	if ids, _, _, code := ask("exactCalls", map[string]any{"first": 2, "after": cursor}); code == "" || ids != "" {
		t.Errorf("a cursor must not survive the grant changing: %q (%s)", ids, code)
	}

	// Live: each grant edit re-evaluates at once. Database's change capture exists on AptevaDB only.
	if adapter := os.Getenv("GRAPHQL_TEST_DATABASE_ADAPTER"); adapter != "" && adapter != "aptevadb" {
		return
	}
	grant(list("offer-a"), list("centre-a"))
	r.operator("db_realtime_configure", map[string]any{"enabled": true})
	header := http.Header{}
	header.Set("Sec-WebSocket-Protocol", "graphql-transport-ws")
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(r.graph.URL(), "http")+"/public/realtime/default", header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var ack wsMessage
	if conn.WriteJSON(map[string]any{"type": "connection_init", "payload": map[string]any{"Authorization": "Bearer " + pipelineToken(reader)}}) != nil || conn.ReadJSON(&ack) != nil || ack.Type != "connection_ack" {
		t.Fatalf("realtime init: %+v", ack)
	}
	if err := conn.WriteJSON(map[string]any{"id": "live", "type": "subscribe", "payload": map[string]any{"query": `subscription { liveExact { rows { id } total } }`}}); err != nil {
		t.Fatal(err)
	}
	latest := "?"
	await := func(step, want string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for latest != want {
			_ = conn.SetReadDeadline(deadline)
			var message wsMessage
			if err := conn.ReadJSON(&message); err != nil {
				t.Fatalf("%s: have %q, want %q: %v", step, latest, want, err)
			}
			if message.Type == "error" {
				t.Fatalf("%s: %s", step, message.Payload)
			}
			if message.Type != "next" {
				continue
			}
			var payload struct {
				Data struct {
					LiveExact struct {
						Rows []struct {
							ID string `json:"id"`
						} `json:"rows"`
						Total int `json:"total"`
					} `json:"liveExact"`
				} `json:"data"`
				Errors []any `json:"errors"`
			}
			if json.Unmarshal(message.Payload, &payload) != nil || len(payload.Errors) > 0 {
				t.Fatalf("%s: payload %s", step, message.Payload)
			}
			ids := []string{}
			for _, row := range payload.Data.LiveExact.Rows {
				ids = append(ids, row.ID)
			}
			if payload.Data.LiveExact.Total != len(ids) {
				t.Fatalf("%s: total %d for rows %v", step, payload.Data.LiveExact.Total, ids)
			}
			latest = strings.Join(ids, ",")
		}
	}
	await("initial", offerACentre)
	grant(list(""), list("centre-a"))
	await("a blank offer revokes", onlyHome)
	grant(list("offer-a"), list("centre-a"))
	await("restored", offerACentre)
	grant(list(" offer-a "), list("centre-a"))
	await("a padded offer revokes", onlyHome)
	grant(list("offer-a"), list("centre-a"))
	await("restored again", offerACentre)
	grant(list("offer-a"), list(""))
	await("a blank centre keeps the restriction", onlyHome)
	grant(list("offer-a"), list("centre-a"))
	await("restored once more", offerACentre)
	r.operator("db_delete", map[string]any{"collection": "sales", "where": map[string]any{"field": "sale_centre", "op": "eq", "value": "centre-a"}, "maxAffected": 10})
	await("the sale behind the grant removed", onlyHome)
	grant(list(), list("centre-a"))
	await("a truly empty grant selects normal access", fallback)
}
