package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// relationalPolicyRows holds calls linked to sales in six ways. A caller with an
// offer grant sees a call only if a related sale has a granted offer (in the
// grant's centres, when it lists any) and no related sale has another offer.
const relationalPolicyRows = `{"calls":[{"id":"1","local_id":"call-1","raw_call_id":"raw-1","centre_id":"centre-a","sale_ref":"201"},
{"id":"2","local_id":"call-2","raw_call_id":"raw-2","centre_id":"centre-a","sale_ref":"sale-202"},
{"id":"3","local_id":"call-3","raw_call_id":"raw-3","centre_id":"centre-a","sale_ref":null},
{"id":"4","local_id":"call-4","raw_call_id":"raw-4","centre_id":"centre-a","sale_ref":null},
{"id":"5","local_id":"call-5","raw_call_id":"raw-5","centre_id":"centre-a","sale_ref":null},
{"id":"6","local_id":"call-6","raw_call_id":"raw-6","centre_id":"centre-a","sale_ref":null},
{"id":"7","local_id":"call-7","raw_call_id":"raw-7","centre_id":"centre-a","sale_ref":null},
{"id":"8","local_id":"call-8","raw_call_id":"raw-8","centre_id":"centre-a","sale_ref":null},
{"id":"9","local_id":"call-9","raw_call_id":"raw-9","centre_id":"centre-b","sale_ref":null},
{"id":"10","local_id":"call-10","raw_call_id":"raw-10","centre_id":"centre-a","sale_ref":null},
{"id":"11","local_id":"call-11","raw_call_id":"raw-11","centre_id":"centre-a","sale_ref":null},
{"id":"12","local_id":"call-12","raw_call_id":"raw-12","centre_id":"centre-a","sale_ref":null}],"sales":[{"id":"201","local_id":"sale-201","offer_id":"101","centre_id":"centre-a","call_id":null,"call_source_id":null},
{"id":"202","local_id":"sale-202","offer_id":"offer-a","centre_id":"centre-a","call_id":null,"call_source_id":null},
{"id":"203","local_id":null,"offer_id":"101","centre_id":"centre-a","call_id":"call-3","call_source_id":null},
{"id":"204","local_id":null,"offer_id":"offer-a","centre_id":"centre-a","call_id":null,"call_source_id":"raw-4"},
{"id":"205","local_id":null,"offer_id":"101","centre_id":"centre-a","call_id":"call-5","call_source_id":null},
{"id":"206","local_id":null,"offer_id":"offer-b","centre_id":"centre-a","call_id":"call-5","call_source_id":null},
{"id":"207","local_id":null,"offer_id":"102","centre_id":"centre-a","call_id":"call-6","call_source_id":null},
{"id":"208","local_id":null,"offer_id":"101","centre_id":"centre-b","call_id":"call-8","call_source_id":null},
{"id":"209","local_id":null,"offer_id":"101","centre_id":"centre-a","call_id":"call-9","call_source_id":null},
{"id":"210","local_id":null,"offer_id":"101","centre_id":"centre-a","call_id":"call-10","call_source_id":null},
{"id":"211","local_id":null,"offer_id":"offer-a","centre_id":"centre-a","call_id":null,"call_source_id":"raw-10"},
{"id":"212","local_id":null,"offer_id":"101","centre_id":"centre-a","call_id":"call-11","call_source_id":null},
{"id":"213","local_id":null,"offer_id":"offer-b","centre_id":"centre-b","call_id":"call-11","call_source_id":null},
{"id":"214","local_id":null,"offer_id":"101","centre_id":"centre-a","call_id":"call-12","call_source_id":null},
{"id":"215","local_id":null,"offer_id":null,"centre_id":"centre-a","call_id":"call-12","call_source_id":null}],"offers":[{"id":"101","local_id":"offer-a"},
{"id":"102","local_id":"offer-b"}],"centres":[{"id":"1","local_id":"centre-a"},
{"id":"2","local_id":"centre-b"}]}`

// relationalPolicyPlan compiles that policy into a mandatory predicate. Trusted
// values decide whether it applies; the client only adds ordinary filters.
func relationalPolicyPlan(t *testing.T) map[string]any {
	t.Helper()
	alternatives := `[
	  {"on": [{"localField": "sale_ref", "foreignField": "id", "comparison": "text"}]},
	  {"on": [{"localField": "sale_ref", "foreignField": "local_id"}]},
	  {"on": [{"localField": "local_id", "foreignField": "call_id"}]},
	  {"on": [{"localField": "local_id", "foreignField": "call_source_id"}]},
	  {"on": [{"localField": "raw_call_id", "foreignField": "call_id"}]},
	  {"on": [{"localField": "raw_call_id", "foreignField": "call_source_id"}]}]`
	const hasAll = `{"op": "or", "args": [{"op": "in", "args": [{"const": "calls:all_centres"}, "$param.permissions"]}]}`
	const hasRead = `{"op": "in", "args": [{"const": "calls:read"}, "$param.permissions"]}`
	const listed = `{"op": "gt", "args": [{"op": "length", "args": ["$scope.grantCentres"]}, {"const": 0}]}`
	latest := func(field string) string {
		return `{"optional": true, "otherwise": {"collection": "grants", "field": "` + field + `", "orderBy": [{"field": "id", "direction": "desc"}], "limit": 1,
		  "where": {"and": [{"field": "user_id", "op": "eq", "value": "$param.user"}, {"field": "enabled", "op": "eq", "value": "1"}]}}}`
	}
	plan := decodeConfig(t, `{
	  "params": {
	    "user": {"from": "$identity.subject", "required": true},
	    "permissions": {"from": "$identity.permissions", "type": "string_list"},
	    "centres": {"from": "$args.centres", "type": "exact_string_list", "required": true},
	    "q": {"from": "$args.q"}, "first": {"from": "$args.first", "type": "number"}, "after": {"from": "$args.after"}},
	  "access": {"values": {
	    "home": {"optional": true, "otherwise": {"collection": "profiles", "field": "centre_id", "where": {"and": [
	      {"field": "user_id", "op": "eq", "value": "$param.user"}, {"field": "active", "op": "eq", "value": "1"}]}}},
	    "grantOffers": `+latest("offer_ids")+`,
	    "grantCentres": `+latest("centre_ids")+`,
	    "offerIds": {"optional": true, "otherwise": {"collection": "offers", "field": "id", "distinct": true,
	      "where": {"field": "local_id", "op": "in", "value": "$scope.grantOffers"}}},
	    "offerRefs": {"optional": true, "union": ["grantOffers", "offerIds"]},
	    "allSaleCentres": {"optional": true, "otherwise": {"collection": "sales", "field": "centre_id", "distinct": true,
	      "where": {"field": "offer_id", "op": "in", "value": "$scope.offerRefs"}}},
	    "grantedSaleCentres": {"optional": true, "intersect": ["allSaleCentres", "grantCentres"]},
	    "saleCentres": {"optional": true, "expr": {"op": "if", "condition": `+listed+`, "then": "$scope.grantedSaleCentres", "else": "$scope.allSaleCentres"}},
	    "normalCentres": {"optional": true, "union": ["home", "saleCentres"]},
	    "everyCentre": {"optional": true, "otherwise": {"collection": "centres", "field": "local_id"}},
	    "authorised": {"optional": true, "expr": {"op": "if",
	      "condition": {"op": "and", "args": [`+hasRead+`, {"op": "or", "args": [`+hasAll+`, {"op": "eq", "args": [{"op": "length", "args": ["$scope.home"]}, {"const": 1}]}]}]},
	      "then": {"op": "if", "condition": `+hasAll+`, "then": "$scope.everyCentre", "else": "$scope.normalCentres"},
	      "else": {"const": []}}}
	  }},
	  "steps": [
	    {"id": "rows", "op": "find", "max_rows": 1000, "page": {"size": "$param.first", "cursor": "$param.after"},
	     "args": {"collection": "calls", "orderBy": [{"field": "id", "direction": "desc"}], "where": {"and": [
	       {"field": "centre_id", "op": "in", "value": "$scope.authorised"},
	       {"access_when": {"op": "and", "args": [{"op": "gt", "args": [{"op": "length", "args": ["$scope.grantOffers"]}, {"const": 0}]}, {"op": "not", "args": [`+hasAll+`]}]},
	        "filter": {"and": [
	          {"exists": {"collection": "sales", "anyOf": `+alternatives+`, "where": {"and": [
	            {"field": "offer_id", "op": "in", "value": "$scope.offerRefs"},
	            {"access_when": `+listed+`, "filter": {"field": "centre_id", "op": "in", "value": "$scope.grantCentres"}}]}}},
	          {"not_exists": {"collection": "sales", "anyOf": `+alternatives+`, "where": {"field": "offer_id", "op": "not_in", "value": "$scope.offerRefs"}}}]}},
	       {"when": {"op": "gt", "args": [{"op": "length", "args": ["$param.centres"]}, {"const": 0}]},
	        "filter": {"field": "centre_id", "op": "in", "value": "$param.centres"}},
	       {"fields": ["local_id", "raw_call_id"], "op": "like_contains_ascii", "value": "$param.q"}]}}},
	    {"id": "total", "op": "count", "count_of": "rows"}],
	  "result": {"rows": "$steps.rows", "total": "$steps.total", "next": "$steps.rows.nextCursor"}}`)
	plan["database"] = "crm"
	return plan
}

// relationalFixture creates the collections and the indexes relationships need.
func relationalFixture(t *testing.T, r *realDatabase) {
	t.Helper()
	r.collection("calls", "id:integer", "local_id:text", "raw_call_id:text", "centre_id:text", "sale_ref:text")
	r.collection("sales", "id:integer", "local_id:text", "offer_id:text", "centre_id:text", "call_id:text", "call_source_id:text")
	r.collection("offers", "id:integer", "local_id:text")
	r.collection("centres", "id:integer", "local_id:text")
	r.collection("profiles", "id:text", "user_id:text", "centre_id:text", "active:integer")
	r.collection("grants", "id:integer", "user_id:text", "enabled:integer", "offer_ids:json", "centre_ids:json")
	for _, field := range []string{"local_id", "call_id", "call_source_id"} {
		r.operator("db_index_create", map[string]any{"collection": "sales", "index": map[string]any{"name": "sales_" + field, "fields": []any{map[string]any{"field": field, "direction": "asc"}}}})
	}
	r.operator("db_access_grant", map[string]any{"app": "graphql", "access": "read"})
	r.requireAuth()
}

const relationalSDL = `type Row { id: ID! } type Page { rows: [Row!]! total: Int! next: String }
type Query { calls(centres: [String!]! = [], q: String = "", first: Int = 100, after: String): Page! }
type Subscription { liveCalls(centres: [String!]! = [], q: String = "", first: Int = 100, after: String): Page! }`

func publishRelational(r *realDatabase, plan map[string]any) string {
	return r.publish(relationalSDL, map[string]map[string]any{
		"Query.calls":            {"operation": "database_pipeline", "config": plan},
		"Subscription.liveCalls": {"operation": "database_pipeline", "config": plan},
	})
}

// TestRealDatabaseRelationalPolicy runs a mandatory relational permission on a
// real Database (0.8.0 or later): selection by trusted values, totals, paging,
// cursors, publication and live re-evaluation.
//
//	GRAPHQL_TEST_DATABASE_DIR=<database>/mcp/database GOWORK=off go test -run TestRealDatabaseRelationalPolicy -v
func TestRealDatabaseRelationalPolicy(t *testing.T) {
	r := newRealDatabase(t, "graphql-database-relational-policy")
	relationalFixture(t, r)
	var rows map[string][]map[string]any
	if err := json.Unmarshal([]byte(relationalPolicyRows), &rows); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"calls", "sales", "offers", "centres"} {
		r.insert(name, rows[name]...)
	}
	const reader, overrider, unprivileged, outsider = "42", "43", "44", "45"
	r.permissions[reader] = []string{"calls:read"}
	r.permissions[overrider] = []string{"calls:read", "calls:all_centres"}
	r.permissions[unprivileged] = []string{}
	r.permissions[outsider] = []string{"calls:read"}
	for i, user := range []string{reader, overrider, unprivileged} {
		r.insert("profiles", map[string]any{"id": "p" + user, "user_id": user, "centre_id": "centre-a", "active": "1"})
		r.insert("grants", map[string]any{"id": fmt.Sprint(i + 1), "user_id": user, "enabled": "1", "offer_ids": []any{"offer-a"}, "centre_ids": []any{"centre-a"}})
	}

	// Publication refuses what would otherwise only fail once a user reached it.
	replace := func(edit func(where []any)) map[string]any {
		plan := relationalPolicyPlan(t)
		edit(plan["steps"].([]any)[0].(map[string]any)["args"].(map[string]any)["where"].(map[string]any)["and"].([]any))
		return plan
	}
	restricted := func(where []any) []any {
		return where[1].(map[string]any)["filter"].(map[string]any)["and"].([]any)
	}
	clause := func(where []any, index int, key string) map[string]any {
		return restricted(where)[index].(map[string]any)[key].(map[string]any)
	}
	for name, c := range map[string]struct {
		plan map[string]any
		want string
	}{
		"unknown related collection": {replace(func(where []any) { clause(where, 1, "not_exists")["collection"] = "nope" }), "not_exists"},
		"unknown related field": {replace(func(where []any) {
			clause(where, 0, "exists")["anyOf"] = []any{map[string]any{"on": []any{map[string]any{"localField": "local_id", "foreignField": "nope"}}}}
		}), "exists"},
		"unknown local field": {replace(func(where []any) {
			clause(where, 0, "exists")["anyOf"] = []any{map[string]any{"on": []any{map[string]any{"localField": "nope", "foreignField": "call_id"}}}}
		}), "exists"},
		"mismatched types": {replace(func(where []any) {
			clause(where, 0, "exists")["anyOf"] = []any{map[string]any{"on": []any{map[string]any{"localField": "sale_ref", "foreignField": "id"}}}}
		}), "comparison text"},
		"no index": {replace(func(where []any) {
			clause(where, 0, "exists")["anyOf"] = []any{map[string]any{"on": []any{map[string]any{"localField": "centre_id", "foreignField": "centre_id"}}}}
		}), "index"},
		"unknown field in related filter": {replace(func(where []any) {
			clause(where, 1, "not_exists")["where"] = map[string]any{"field": "nope", "op": "not_in", "value": "$scope.offerRefs"}
		}), "not_exists"},
		"unknown field behind trusted selection": {replace(func(where []any) {
			where[1].(map[string]any)["filter"] = map[string]any{"field": "nope", "op": "in", "value": "$scope.offerRefs"}
		}), "conditional filters"},
	} {
		if failure := publishRelational(r, c.plan); !strings.Contains(failure, c.want) {
			t.Errorf("%s: want %q, got %q", name, c.want, failure)
		}
	}
	plan := relationalPolicyPlan(t)
	if failure := publishRelational(r, plan); failure != "" {
		t.Fatal(failure)
	}
	validated, err := validateDatabasePipeline(plan)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(validated.Collections()); got != "[calls centres grants offers profiles sales]" {
		t.Fatalf("subscriptions must watch every collection the filter reads: %s", got)
	}

	const document = `query($centres: [String!]!, $q: String, $first: Int, $after: String) {
	  calls(centres: $centres, q: $q, first: $first, after: $after) { rows { id } total next } }`
	query := func(user string, variables map[string]any) (string, string, string, string) {
		t.Helper()
		merged := map[string]any{"centres": []any{}, "q": "", "first": 100}
		for key, value := range variables {
			merged[key] = value
		}
		out := r.queryAs(user, document, merged)
		data, _ := out.Data["calls"].(map[string]any)
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
	expect := func(name, user string, variables map[string]any, want string) {
		t.Helper()
		ids, total, _, code := query(user, variables)
		count := 0
		if want != "" {
			count = strings.Count(want, ",") + 1
		}
		if code != "" || ids != want || total != fmt.Sprint(count) {
			t.Errorf("%s: got %q total %s (%s), want %q", name, ids, total, code, want)
		}
	}
	update := func(collection, field string, id any, set map[string]any) {
		t.Helper()
		r.operator("db_update", map[string]any{"collection": collection, "where": map[string]any{"field": field, "op": "eq", "value": id}, "set": set, "maxAffected": 10})
	}
	grant := func(set map[string]any) { update("grants", "user_id", reader, set) }
	const allowed = "12,10,4,3,2,1"
	const normal = "12,11,10,8,7,6,5,4,3,2,1"

	// Every positive and negative relationship rule.
	expect("restricted", reader, nil, allowed)
	// Trusted bypass rules.
	expect("all-centres override", overrider, nil, "12,11,10,9,8,7,6,5,4,3,2,1")
	expect("no read permission", unprivileged, nil, "")
	expect("no profile", outsider, nil, "")
	grant(map[string]any{"enabled": "0"})
	expect("no grant keeps the normal scope", reader, nil, normal)
	grant(map[string]any{"enabled": "1", "offer_ids": []any{}})
	expect("empty grant keeps the normal scope", reader, nil, normal)
	grant(map[string]any{"offer_ids": []any{"unknown"}})
	expect("nonempty unknown grant is never a fallback", reader, nil, "")
	grant(map[string]any{"offer_ids": []any{"offer-a"}})
	update("profiles", "user_id", reader, map[string]any{"active": "0"})
	expect("an offer grant alone is not enough", reader, nil, "")
	update("profiles", "user_id", reader, map[string]any{"active": "1"})

	// The grant's centres narrow the positive sale, and only that.
	grant(map[string]any{"centre_ids": []any{}})
	expect("grant without centres", reader, nil, "12,10,9,8,4,3,2,1")
	grant(map[string]any{"centre_ids": []any{"centre-a"}})

	// Client filters narrow and can't lift the restriction.
	expect("search", reader, map[string]any{"q": "call-1"}, "12,10,1")
	expect("wildcard search stays restricted", reader, map[string]any{"q": "%"}, allowed)
	expect("own centre", reader, map[string]any{"centres": []any{"centre-a"}}, allowed)
	expect("other centre", reader, map[string]any{"centres": []any{"centre-b"}}, "")
	expect("own and other centre", reader, map[string]any{"centres": []any{"centre-a", "centre-b"}}, allowed)

	// Paging runs after authorisation, each call counted once.
	seen := []string{}
	after := any(nil)
	for pages := 0; ; pages++ {
		ids, total, next, code := query(reader, map[string]any{"first": 2, "after": after})
		if code != "" || total != "6" {
			t.Fatalf("page %d: %q total %s (%s)", pages, ids, total, code)
		}
		seen = append(seen, ids)
		if next == "" {
			break
		}
		after = next
		if pages > 5 {
			t.Fatal("paging does not end")
		}
	}
	if strings.Join(seen, "|") != "12,10|4,3|2,1" {
		t.Errorf("pages: %v", seen)
	}

	// Every page is authorised again. A row that stops being visible disappears
	// from the next page and its total, on the same cursor.
	_, _, cursor, _ := query(reader, map[string]any{"first": 2})
	r.insert("sales", map[string]any{"id": "301", "offer_id": "offer-b", "centre_id": "centre-a", "call_id": "call-4"})
	if ids, total, _, code := query(reader, map[string]any{"first": 2, "after": cursor}); code != "" || ids != "3,2" || total != "5" {
		t.Errorf("next page after a sale change: %q total %s (%s)", ids, total, code)
	}
	r.operator("db_delete", map[string]any{"collection": "sales", "where": map[string]any{"field": "id", "op": "eq", "value": "301"}, "maxAffected": 1})
	// A cursor belongs to the resolved query: a changed policy or filter can't
	// continue it, and restoring them can.
	grant(map[string]any{"offer_ids": []any{"offer-a", "offer-b"}})
	if ids, _, _, code := query(reader, map[string]any{"first": 2, "after": cursor}); code == "" || ids != "" {
		t.Errorf("cursor after a grant change: %q (%s)", ids, code)
	}
	grant(map[string]any{"offer_ids": []any{"offer-a"}})
	if ids, _, _, code := query(reader, map[string]any{"first": 2, "after": cursor, "centres": []any{"centre-a"}}); code == "" || ids != "" {
		t.Errorf("cursor with another client filter: %q (%s)", ids, code)
	}
	if ids, _, _, code := query(overrider, map[string]any{"first": 2, "after": cursor}); code == "" || ids != "" {
		t.Errorf("cursor under another policy: %q (%s)", ids, code)
	}
	if ids, _, _, code := query(reader, map[string]any{"first": 2, "after": cursor}); code != "" || ids != "4,3" {
		t.Errorf("cursor after restoring: %q (%s)", ids, code)
	}

	// Live: each collection the policy reads re-evaluates it. Database's change
	// capture exists on AptevaDB only.
	if adapter := os.Getenv("GRAPHQL_TEST_DATABASE_ADAPTER"); adapter != "" && adapter != "aptevadb" {
		return
	}
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
	if err := conn.WriteJSON(map[string]any{"id": "live", "type": "subscribe", "payload": map[string]any{"query": `subscription { liveCalls { rows { id } total } }`}}); err != nil {
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
					LiveCalls struct {
						Rows []struct {
							ID string `json:"id"`
						} `json:"rows"`
						Total int `json:"total"`
					} `json:"liveCalls"`
				} `json:"data"`
				Errors []any `json:"errors"`
			}
			if json.Unmarshal(message.Payload, &payload) != nil || len(payload.Errors) > 0 {
				t.Fatalf("%s: payload %s", step, message.Payload)
			}
			ids := []string{}
			for _, row := range payload.Data.LiveCalls.Rows {
				ids = append(ids, row.ID)
			}
			if payload.Data.LiveCalls.Total != len(ids) {
				t.Fatalf("%s: total %d for rows %v", step, payload.Data.LiveCalls.Total, ids)
			}
			latest = strings.Join(ids, ",")
		}
	}
	await("initial", allowed)
	r.insert("sales", map[string]any{"id": "302", "offer_id": "offer-b", "centre_id": "centre-a", "call_id": "call-1"})
	await("a sale with another offer", "12,10,4,3,2")
	r.operator("db_delete", map[string]any{"collection": "sales", "where": map[string]any{"field": "id", "op": "eq", "value": "302"}, "maxAffected": 1})
	await("that sale removed", allowed)
	r.insert("calls", map[string]any{"id": "13", "local_id": "call-13", "raw_call_id": "raw-13", "centre_id": "centre-a", "sale_ref": "sale-202"})
	await("a new call on an allowed sale", "13,"+allowed)
	r.operator("db_delete", map[string]any{"collection": "calls", "where": map[string]any{"field": "id", "op": "eq", "value": "13"}, "maxAffected": 1})
	await("that call removed", allowed)
	update("offers", "id", "101", map[string]any{"local_id": "offer-z"})
	await("the offer behind the numeric references changes", "4,2")
	update("offers", "id", "101", map[string]any{"local_id": "offer-a"})
	await("that offer restored", allowed)
	grant(map[string]any{"enabled": "0"})
	await("grant removed", normal)
	grant(map[string]any{"enabled": "1"})
	await("grant restored", allowed)
	update("profiles", "user_id", reader, map[string]any{"active": "0"})
	await("profile deactivated", "")
	update("profiles", "user_id", reader, map[string]any{"active": "1"})
	await("profile restored", allowed)
}

// TestRealDatabaseRelationalPolicyScale covers what listing eligible ids in an
// access value could not: more than 999 eligible records, with more than 100
// granted offers on both the positive and the negative side.
func TestRealDatabaseRelationalPolicyScale(t *testing.T) {
	r := newRealDatabase(t, "graphql-database-relational-scale")
	relationalFixture(t, r)
	r.permissions["42"] = []string{"calls:read"}
	offers := []any{"offer-a"}
	for i := 0; i < 150; i++ {
		offers = append(offers, fmt.Sprintf("offer-%03d", i))
	}
	r.insert("profiles", map[string]any{"id": "p42", "user_id": "42", "centre_id": "centre-a", "active": "1"})
	r.insert("grants", map[string]any{"id": "1", "user_id": "42", "enabled": "1", "offer_ids": offers, "centre_ids": []any{"centre-a"}})
	const eligible = 1001
	for start := 1; start <= eligible; start += 500 {
		calls, sales := []map[string]any{}, []map[string]any{}
		for i := start; i < start+500 && i <= eligible; i++ {
			local := fmt.Sprintf("call-%05d", i)
			calls = append(calls, map[string]any{"id": fmt.Sprint(i), "local_id": local, "raw_call_id": "raw-" + local, "centre_id": "centre-a"})
			sales = append(sales, map[string]any{"id": fmt.Sprint(i), "offer_id": offers[i%len(offers)], "centre_id": "centre-a", "call_id": local})
		}
		r.insert("calls", calls...)
		r.insert("sales", sales...)
	}
	// One call with a sale outside the grant and one with no sale stay hidden.
	r.insert("calls",
		map[string]any{"id": "2001", "local_id": "call-denied", "raw_call_id": "raw-denied", "centre_id": "centre-a"},
		map[string]any{"id": "2002", "local_id": "call-unlinked", "raw_call_id": "raw-unlinked", "centre_id": "centre-a"})
	r.insert("sales", map[string]any{"id": "2001", "offer_id": "offer-b", "centre_id": "centre-a", "call_id": "call-denied"})
	if failure := publishRelational(r, relationalPolicyPlan(t)); failure != "" {
		t.Fatal(failure)
	}
	out := r.queryAs("42", `{ calls(first: 1000) { rows { id } total next } }`, nil)
	if out.code() != "" {
		t.Fatalf("%v", out.Errors)
	}
	data := out.Data["calls"].(map[string]any)
	rows := data["rows"].([]any)
	if len(rows) != 1000 || fmt.Sprint(data["total"]) != fmt.Sprint(eligible) || fmt.Sprint(rows[0].(map[string]any)["id"]) != fmt.Sprint(eligible) {
		t.Fatalf("got %d rows, total %v, first %v", len(rows), data["total"], rows[0])
	}
	second := r.queryAs("42", `query($after: String) { calls(first: 1000, after: $after) { rows { id } total next } }`, map[string]any{"after": data["next"]})
	if second.code() != "" {
		t.Fatalf("%v", second.Errors)
	}
	data = second.Data["calls"].(map[string]any)
	rows = data["rows"].([]any)
	if len(rows) != 1 || fmt.Sprint(rows[0].(map[string]any)["id"]) != "1" || data["next"] != nil {
		t.Fatalf("second page: %v", data)
	}
}

// TestRealDatabaseRelationalClientFilterRefused checks a plain resolver: Database
// accepts exists from anyone who can filter, so a client's where must not reach
// it with one.
func TestRealDatabaseRelationalClientFilterRefused(t *testing.T) {
	r := newRealDatabase(t, "graphql-database-relational-client")
	r.collection("records", "id:text", "owner:text")
	r.collection("private", "id:text", "record_id:text", "secret:text")
	r.operator("db_access_grant", map[string]any{"app": "graphql", "access": "read"})
	r.insert("records", map[string]any{"id": "r1", "owner": "a"})
	r.insert("private", map[string]any{"id": "p1", "record_id": "r1", "secret": "x"})
	if failure := r.publish(`scalar JSON type Row { id: ID! } type Query { rows(where: JSON): [Row!]! }`, map[string]map[string]any{
		"Query.rows": {"operation": "find", "config": map[string]any{"collection": "records"}},
	}); failure != "" {
		t.Fatal(failure)
	}
	if out := r.query(`query($w: JSON) { rows(where: $w) { id } }`, map[string]any{"w": map[string]any{"field": "owner", "op": "eq", "value": "a"}}); out.code() != "" {
		t.Fatalf("ordinary client filter: %v", out.Errors)
	}
	probe := map[string]any{"collection": "private", "on": []any{map[string]any{"localField": "id", "foreignField": "record_id"}}, "where": map[string]any{"field": "secret", "op": "eq", "value": "x"}}
	keys := []string{"exists", "not_exists", "Exists", "NOT_EXISTS", "Not_Exists"}
	sort.Strings(keys)
	for _, key := range keys {
		for name, where := range map[string]any{
			"root":   map[string]any{key: probe},
			"nested": map[string]any{"and": []any{map[string]any{"or": []any{map[string]any{key: probe}}}}},
			"casing": map[string]any{"AND": []any{map[string]any{key: probe}}},
		} {
			out := r.query(`query($w: JSON) { rows(where: $w) { id } }`, map[string]any{"w": where})
			if len(out.Errors) == 0 || !strings.Contains(fmt.Sprint(out.Errors), "cannot hold exists") {
				t.Errorf("%s %s reached Database: %+v", key, name, out)
			}
		}
	}
	// A plain resolver's own filter can't use one either: it has no way to
	// watch the related collection.
	if failure := r.publish(`scalar JSON type Row { id: ID! } type Query { rows(where: JSON): [Row!]! }`, map[string]map[string]any{
		"Query.rows": {"operation": "find", "config": map[string]any{"collection": "records", "where": map[string]any{"exists": probe}}},
	}); !strings.Contains(failure, "database_pipeline") {
		t.Errorf("plain resolver filter: %q", failure)
	}
}
