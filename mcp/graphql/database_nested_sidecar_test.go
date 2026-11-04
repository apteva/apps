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

// nestedPolicyPlan pages reports whose call has a sale on a granted offer and no
// sale on any other offer. A sale's centre narrows the positive match when the
// grant lists centres. sale_centre exists on sales only, so publication has to
// plan it through the relationship rather than on reports.
func nestedPolicyPlan(t *testing.T, reportsToCalls, callsToSales string) map[string]any {
	t.Helper()
	latest := func(field string) string {
		return `{"optional": true, "otherwise": {"collection": "grants", "field": "` + field + `", "orderBy": [{"field": "id", "direction": "desc"}], "limit": 1,
		  "where": {"and": [{"field": "user_id", "op": "eq", "value": "$param.user"}, {"field": "enabled", "op": "eq", "value": "1"}]}}}`
	}
	const listed = `{"op": "gt", "args": [{"op": "length", "args": ["$scope.centres"]}, {"const": 0}]}`
	plan := decodeConfig(t, `{
	  "params": {"user": {"from": "$identity.subject", "required": true},
	    "first": {"from": "$args.first", "type": "number"}, "after": {"from": "$args.after"}},
	  "access": {"values": {"offers": `+latest("offer_ids")+`, "centres": `+latest("centre_ids")+`}},
	  "steps": [
	    {"id": "rows", "op": "find", "max_rows": 1000, "page": {"size": "$param.first", "cursor": "$param.after"},
	     "args": {"collection": "reports", "orderBy": [{"field": "id", "direction": "desc"}], "where": {"and": [
	       {"exists": {"collection": "calls", "on": [`+reportsToCalls+`], "where": {"and": [
	         {"exists": {"collection": "sales", "on": [`+callsToSales+`], "where": {"and": [
	           {"field": "offer_id", "op": "in", "value": "$scope.offers"},
	           {"access_when": `+listed+`, "filter": {"field": "sale_centre", "op": "in", "value": "$scope.centres"}}]}}},
	         {"not_exists": {"collection": "sales", "on": [`+callsToSales+`],
	           "where": {"field": "offer_id", "op": "not_in", "value": "$scope.offers"}}}]}}}]}}},
	    {"id": "total", "op": "count", "count_of": "rows"}],
	  "result": {"rows": "$steps.rows", "total": "$steps.total", "next": "$steps.rows.nextCursor"}}`)
	plan["database"] = "crm"
	return plan
}

const (
	nestedReportsToCalls = `{"localField": "call_ref", "foreignField": "local_id"}`
	nestedCallsToSales   = `{"localField": "local_id", "foreignField": "call_id"}`
	nestedPolicySDL      = `type Row { id: ID! } type Page { rows: [Row!]! total: Int! next: String }
type Query { reports(first: Int = 100, after: String): Page! }
type Subscription { liveReports(first: Int = 100, after: String): Page! }`
)

func publishNested(r *realDatabase, plan map[string]any) string {
	return r.publish(nestedPolicySDL, map[string]map[string]any{
		"Query.reports":            {"operation": "database_pipeline", "config": plan},
		"Subscription.liveReports": {"operation": "database_pipeline", "config": plan},
	})
}

// TestRealDatabaseNestedRelationalPolicy runs a permission that follows a chain
// of relationships on a real Database (0.10.0 or later): publication, selection
// by trusted values, totals, paging, cursors and live re-evaluation.
//
//	GRAPHQL_TEST_DATABASE_DIR=<database>/mcp/database GOWORK=off go test -run TestRealDatabaseNestedRelationalPolicy -v
func TestRealDatabaseNestedRelationalPolicy(t *testing.T) {
	r := newRealDatabase(t, "graphql-database-nested-policy")
	r.collection("reports", "id:integer", "call_ref:text")
	r.collection("calls", "id:integer", "local_id:text", "note:text")
	r.collection("sales", "id:integer", "call_id:text", "offer_id:text", "sale_centre:text")
	r.collection("grants", "id:integer", "user_id:text", "enabled:integer", "offer_ids:json", "centre_ids:json")
	r.operator("db_index_create", map[string]any{"collection": "calls", "index": map[string]any{"name": "calls_local", "fields": []any{map[string]any{"field": "local_id", "direction": "asc"}}}})
	r.operator("db_index_create", map[string]any{"collection": "sales", "index": map[string]any{"name": "sales_call", "fields": []any{map[string]any{"field": "call_id", "direction": "asc"}}}})
	r.operator("db_access_grant", map[string]any{"app": "graphql", "access": "read"})
	r.requireAuth()

	for i := 1; i <= 5; i++ {
		r.insert("calls", map[string]any{"id": fmt.Sprint(i), "local_id": fmt.Sprintf("c%d", i)})
	}
	r.insert("sales",
		// c1 has two matching sales and must still count once.
		map[string]any{"id": "1", "call_id": "c1", "offer_id": "offer-a", "sale_centre": "centre-a"},
		map[string]any{"id": "2", "call_id": "c1", "offer_id": "offer-a", "sale_centre": "centre-a"},
		// c2 is mixed: the other offer excludes it for a grant on either one.
		map[string]any{"id": "3", "call_id": "c2", "offer_id": "offer-a", "sale_centre": "centre-a"},
		map[string]any{"id": "4", "call_id": "c2", "offer_id": "offer-b", "sale_centre": "centre-a"},
		map[string]any{"id": "5", "call_id": "c3", "offer_id": "offer-b", "sale_centre": "centre-a"},
		// c4 has no sale.
		map[string]any{"id": "6", "call_id": "c5", "offer_id": "offer-a", "sale_centre": "centre-b"})
	for id, call := range map[string]string{"1": "c1", "2": "c1", "3": "c2", "4": "c3", "5": "c4", "6": "c5", "7": "missing", "8": "c5"} {
		r.insert("reports", map[string]any{"id": id, "call_ref": call})
	}
	const reader = "42"
	r.permissions[reader] = []string{}
	r.insert("grants", map[string]any{"id": "1", "user_id": reader, "enabled": "1", "offer_ids": []any{"offer-a"}, "centre_ids": []any{}})

	// A Database older than 0.10.0 can't run this: the release says so at publish.
	if os.Getenv("GRAPHQL_TEST_DATABASE_NESTING") == "refused" {
		if failure := publishNested(r, nestedPolicyPlan(t, nestedReportsToCalls, nestedCallsToSales)); !strings.Contains(failure, "0.10.0 for nested relationships") {
			t.Errorf("older Database: %q", failure)
		}
		return
	}

	// Publication plans the whole chain on Database, so a level it can't run
	// refuses the release rather than the first user who reaches it.
	for name, c := range map[string]struct {
		plan map[string]any
		want string
	}{
		"no index at the second level": {nestedPolicyPlan(t, nestedReportsToCalls, `{"localField": "local_id", "foreignField": "offer_id"}`), "cannot plan"},
		"no index at the first level":  {nestedPolicyPlan(t, `{"localField": "call_ref", "foreignField": "note"}`, nestedCallsToSales), "cannot plan"},
		"unknown local field":          {nestedPolicyPlan(t, nestedReportsToCalls, `{"localField": "call_ref", "foreignField": "call_id"}`), "cannot plan"},
	} {
		if failure := publishNested(r, c.plan); !strings.Contains(failure, c.want) {
			t.Errorf("%s: %q", name, failure)
		}
	}
	if failure := publishNested(r, nestedPolicyPlan(t, nestedReportsToCalls, nestedCallsToSales)); failure != "" {
		t.Fatal(failure)
	}

	const document = `query($first: Int, $after: String) { reports(first: $first, after: $after) { rows { id } total next } }`
	query := func(variables map[string]any) (string, string, string, string) {
		t.Helper()
		out := r.queryAs(reader, document, variables)
		data, _ := out.Data["reports"].(map[string]any)
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
	expect := func(name, want string) {
		t.Helper()
		ids, total, _, code := query(map[string]any{})
		count := 0
		if want != "" {
			count = strings.Count(want, ",") + 1
		}
		if code != "" || ids != want || total != fmt.Sprint(count) {
			t.Errorf("%s: got %q total %s (%s), want %q", name, ids, total, code, want)
		}
	}
	grant := func(set map[string]any) {
		t.Helper()
		r.operator("db_update", map[string]any{"collection": "grants", "where": map[string]any{"field": "user_id", "op": "eq", "value": reader}, "set": set, "maxAffected": 10})
	}

	// Reports 1 and 2 share c1, 6 and 8 share c5. A report with a mixed call, no
	// sale, another offer or a missing call never appears.
	expect("one offer", "8,6,2,1")
	grant(map[string]any{"offer_ids": []any{"offer-b"}})
	expect("the other offer leaves mixed calls out too", "4")
	grant(map[string]any{"offer_ids": []any{"offer-a", "offer-b"}})
	expect("both offers bring mixed calls in", "8,6,4,3,2,1")
	grant(map[string]any{"offer_ids": []any{"offer-unknown"}})
	expect("an unknown offer is never unrestricted", "")
	grant(map[string]any{"offer_ids": []any{}})
	expect("no offer", "")

	// Trusted selection, two levels down.
	grant(map[string]any{"offer_ids": []any{"offer-a"}, "centre_ids": []any{"centre-a"}})
	expect("centres listed", "2,1")
	grant(map[string]any{"centre_ids": []any{"centre-z"}})
	expect("no sale in the listed centre", "")
	grant(map[string]any{"centre_ids": []any{}})
	expect("no centres listed", "8,6,2,1")

	// Paging runs after the whole chain, on one cursor per resolved filter.
	first, total, cursor, code := query(map[string]any{"first": 2})
	if first != "8,6" || total != "4" || cursor == "" || code != "" {
		t.Fatalf("first page: %q total %s next %q (%s)", first, total, cursor, code)
	}
	if ids, total, next, code := query(map[string]any{"first": 2, "after": cursor}); ids != "2,1" || total != "4" || next != "" || code != "" {
		t.Errorf("second page: %q total %s next %q (%s)", ids, total, next, code)
	}
	grant(map[string]any{"offer_ids": []any{"offer-b"}})
	if ids, _, _, code := query(map[string]any{"first": 2, "after": cursor}); code == "" || ids != "" {
		t.Errorf("a cursor must not survive a grant change: %q (%s)", ids, code)
	}
	grant(map[string]any{"offer_ids": []any{"offer-a"}})

	// A change at the deepest level reaches an open page's rows and total.
	r.insert("sales", map[string]any{"id": "20", "call_id": "c5", "offer_id": "offer-b", "sale_centre": "centre-a"})
	expect("a sale makes c5 mixed", "2,1")
	r.operator("db_delete", map[string]any{"collection": "sales", "where": map[string]any{"field": "id", "op": "eq", "value": "20"}, "maxAffected": 1})
	expect("that sale removed", "8,6,2,1")

	// Live: every level re-evaluates. Database's change capture exists on AptevaDB only.
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
	if err := conn.WriteJSON(map[string]any{"id": "live", "type": "subscribe", "payload": map[string]any{"query": `subscription { liveReports { rows { id } total } }`}}); err != nil {
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
					LiveReports struct {
						Rows []struct {
							ID string `json:"id"`
						} `json:"rows"`
						Total int `json:"total"`
					} `json:"liveReports"`
				} `json:"data"`
				Errors []any `json:"errors"`
			}
			if json.Unmarshal(message.Payload, &payload) != nil || len(payload.Errors) > 0 {
				t.Fatalf("%s: payload %s", step, message.Payload)
			}
			ids := []string{}
			for _, row := range payload.Data.LiveReports.Rows {
				ids = append(ids, row.ID)
			}
			if payload.Data.LiveReports.Total != len(ids) {
				t.Fatalf("%s: total %d for rows %v", step, payload.Data.LiveReports.Total, ids)
			}
			latest = strings.Join(ids, ",")
		}
	}
	await("initial", "8,6,2,1")
	r.insert("sales", map[string]any{"id": "21", "call_id": "c5", "offer_id": "offer-b", "sale_centre": "centre-a"})
	await("a sale at the deepest level", "2,1")
	r.operator("db_delete", map[string]any{"collection": "sales", "where": map[string]any{"field": "id", "op": "eq", "value": "21"}, "maxAffected": 1})
	await("that sale removed", "8,6,2,1")
	r.insert("calls", map[string]any{"id": "6", "local_id": "c6"})
	r.insert("sales", map[string]any{"id": "22", "call_id": "c6", "offer_id": "offer-a", "sale_centre": "centre-a"})
	r.insert("reports", map[string]any{"id": "9", "call_ref": "c6"})
	await("a new chain", "9,8,6,2,1")
	r.operator("db_delete", map[string]any{"collection": "calls", "where": map[string]any{"field": "local_id", "op": "eq", "value": "c6"}, "maxAffected": 1})
	await("the middle of a chain removed", "8,6,2,1")
	grant(map[string]any{"enabled": "0"})
	await("the grant removed", "")
}
