package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestRealDatabaseConditionalFilters runs an authorised page with optional
// client filters against a real Database (0.7.0 or later), over queries,
// paging, totals, publication and live subscriptions:
//
//	GRAPHQL_TEST_DATABASE_DIR=<database>/mcp/database GOWORK=off go test -run TestRealDatabaseConditionalFilters -v
func TestRealDatabaseConditionalFilters(t *testing.T) {
	r := newRealDatabase(t, "graphql-database-conditional-filters")
	r.collection("profiles", "id:text", "user_id:text", "centre_id:text")
	r.collection("leads", "id:integer", "centre_id:text", "status:text", "reference:text", "created:datetime")
	r.operator("db_access_grant", map[string]any{"app": "graphql", "access": "read"})
	r.operator("db_realtime_configure", map[string]any{"enabled": true})
	r.requireAuth()
	r.permissions["41"], r.permissions["42"] = []string{}, []string{}
	r.insert("profiles", map[string]any{"id": "p1", "user_id": "41", "centre_id": "centre-a"})
	row := func(id int, centre, status, reference, created string) map[string]any {
		return map[string]any{"id": fmt.Sprint(id), "centre_id": centre, "status": status, "reference": reference, "created": created}
	}
	r.insert("leads",
		row(1, "centre-a", "open", "alpha", "2026-01-01T00:00:00Z"),
		row(2, "centre-a", "closed", "beta", "2026-02-01T00:00:00Z"),
		row(3, "centre-b", "open", "alpha", "2026-03-01T00:00:00Z"),
		row(4, "centre-a", "open", "100% done", "2026-04-01T00:00:00Z"),
		row(5, "centre-a", "closed", "gamma", "2026-05-01T00:00:00Z"),
		row(6, "centre-a", "open", "delta", "2026-06-01T00:00:00Z"),
		row(7, "centre-a", "open", "ALPHA", "2026-07-01T00:00:00Z"),
		row(8, "centre-a", "closed", "epsilon", "2026-08-01T00:00:00Z"),
	)

	config := func(mutate func(where []any) []any) map[string]any {
		optional := func(parameter, empty string) map[string]any {
			return map[string]any{"op": "neq", "args": []any{"$param." + parameter, map[string]any{"const": empty}}}
		}
		listed := func(parameter string) map[string]any {
			return map[string]any{"op": "gt", "args": []any{map[string]any{"op": "length", "args": []any{"$param." + parameter}}, map[string]any{"const": 0}}}
		}
		where := []any{
			map[string]any{"field": "centre_id", "op": "in", "value": "$scope.allowed"},
			map[string]any{"when": listed("centres"), "filter": map[string]any{"field": "centre_id", "op": "in", "value": "$param.centres"}},
			map[string]any{"when": listed("ids"), "filter": map[string]any{"field": "id", "op": "in", "value": "$param.ids"}},
			map[string]any{"when": optional("status", ""), "filter": map[string]any{"field": "status", "op": "eq", "value": "$param.status"}},
			map[string]any{"when": optional("since", ""), "filter": map[string]any{"field": "created", "op": "gte", "value": "$param.since"}},
			map[string]any{"fields": []any{"reference"}, "op": "like_contains_ascii", "value": "$param.q"},
		}
		if mutate != nil {
			where = mutate(where)
		}
		plan := decodeConfig(t, `{
		  "params": {
		    "user": {"from": "$identity.subject", "required": true},
		    "centres": {"from": "$args.centres", "type": "exact_string_list", "required": true},
		    "ids": {"from": "$args.ids", "type": "exact_string_list", "required": true},
		    "status": {"from": "$args.status"}, "since": {"from": "$args.since"}, "q": {"from": "$args.q"},
		    "first": {"from": "$args.first", "type": "number"}, "after": {"from": "$args.after"}},
		  "access": {"values": {"allowed": {"optional": true, "otherwise": {"collection": "profiles", "field": "centre_id",
		    "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}}}},
		  "steps": [
		    {"id": "rows", "op": "find", "max_rows": 100, "page": {"size": "$param.first", "cursor": "$param.after"},
		     "args": {"collection": "leads", "orderBy": [{"field": "id", "direction": "desc"}]}},
		    {"id": "total", "op": "count", "count_of": "rows"}],
		  "result": {"rows": "$steps.rows", "total": "$steps.total", "next": "$steps.rows.nextCursor"}}`)
		plan["steps"].([]any)[0].(map[string]any)["args"].(map[string]any)["where"] = map[string]any{"and": where}
		plan["database"] = "crm"
		return plan
	}
	sdl := func(centres string) string {
		arguments := centres + `, ids: [ID!]! = [], status: String = "", since: String = "", q: String = "", first: Int, after: String`
		return `type Row { id: ID! } type Page { rows: [Row!]! total: Int! next: String }
		type Query { page(` + arguments + `): Page! } type Subscription { livePage(` + arguments + `): Page! }`
	}
	const goodCentres = `centres: [String!]! = []`
	publish := func(centres string, plan map[string]any) string {
		return r.publish(sdl(centres), map[string]map[string]any{
			"Query.page":            {"operation": "database_pipeline", "config": plan},
			"Subscription.livePage": {"operation": "database_pipeline", "config": plan},
		})
	}

	// Publication refuses what would otherwise only fail once a user reached it.
	replaceFirstConditional := func(filter map[string]any) func([]any) []any {
		return func(where []any) []any {
			where[1].(map[string]any)["filter"] = filter
			return where
		}
	}
	for name, c := range map[string]struct {
		centres string
		plan    map[string]any
		want    string
	}{
		"mistyped field": {goodCentres, config(replaceFirstConditional(map[string]any{"field": "centre", "op": "in", "value": "$param.centres"})), "cannot read the fields of its conditional filters"},
		"no default":     {`centres: [String!]`, config(nil), "must be non-null or have a default"},
		"null default":   {`centres: [String!] = null`, config(nil), "must be non-null or have a default"},
		"single value":   {`centres: String = ""`, config(nil), "does not match its param"},
		"not declared":   {`other: [String!]! = []`, config(nil), "does not declare"},
		"scope":          {goodCentres, config(replaceFirstConditional(map[string]any{"field": "centre_id", "op": "in", "value": "$scope.allowed"})), "$scope.allowed"},
	} {
		if failure := publish(c.centres, c.plan); !strings.Contains(failure, c.want) {
			t.Errorf("%s: want %q, got %q", name, c.want, failure)
		}
	}
	if failure := publish(goodCentres, config(nil)); failure != "" {
		t.Fatal(failure)
	}

	const document = `query($centres: [String!]!, $ids: [ID!]!, $status: String, $since: String, $q: String, $first: Int, $after: String) {
	  page(centres: $centres, ids: $ids, status: $status, since: $since, q: $q, first: $first, after: $after) { rows { id } total next } }`
	type page struct {
		ids, next string
		total     string
	}
	read := func(out graphResponse) page {
		data, _ := out.Data["page"].(map[string]any)
		ids := []string{}
		rows, _ := data["rows"].([]any)
		for _, raw := range rows {
			ids = append(ids, fmt.Sprint(raw.(map[string]any)["id"]))
		}
		next := ""
		if data["next"] != nil {
			next = fmt.Sprint(data["next"])
		}
		return page{ids: strings.Join(ids, ","), next: next, total: fmt.Sprint(data["total"])}
	}
	query := func(user string, overrides map[string]any) (page, string) {
		t.Helper()
		variables := map[string]any{"centres": []any{}, "ids": []any{}, "status": "", "since": "", "q": "", "first": 100}
		for key, value := range overrides {
			variables[key] = value
		}
		out := r.queryAs(user, document, variables)
		return read(out), out.code()
	}
	expect := func(name, user string, overrides map[string]any, ids string, total int) {
		t.Helper()
		got, code := query(user, overrides)
		if code != "" || got.ids != ids || got.total != fmt.Sprint(total) {
			t.Errorf("%s: got %+v (%s), want %q total %d", name, got, code, ids, total)
		}
	}
	const seven = "8,7,6,5,4,2,1"

	// The acceptance table.
	selection := func(values ...any) map[string]any { return map[string]any{"centres": append([]any{}, values...)} }
	expect("no selection", "41", selection(), seven, 7)
	expect("own centre", "41", selection("centre-a"), seven, 7)
	expect("other centre", "41", selection("centre-b"), "", 0)
	expect("unknown centre", "41", selection("unknown"), "", 0)
	expect("own and other", "41", selection("centre-a", "centre-b"), seven, 7)
	expect("no authorisation, no selection", "42", selection(), "", 0)
	expect("no authorisation, own selection", "42", selection("centre-a"), "", 0)
	expect("no authorisation, unknown selection", "42", selection("unknown"), "", 0)

	// An omitted argument takes its default.
	if out := r.queryAs("41", `{ page { rows { id } total } }`, nil); out.code() != "" || read(out).ids != seven {
		t.Errorf("omitted arguments: %+v", out)
	}
	// Identifiers are compared exactly: blanks and spaces select nothing, and
	// never turn the selection off.
	expect("blank selection", "41", selection(""), "", 0)
	expect("spaced selection", "41", selection(" centre-a "), "", 0)
	expect("blank beside own", "41", selection("", "centre-a"), seven, 7)

	// Other optional filters.
	expect("status", "41", map[string]any{"status": "closed"}, "8,5,2", 3)
	expect("unknown status", "41", map[string]any{"status": "nope"}, "", 0)
	expect("status and centre", "41", map[string]any{"status": "open", "centres": []any{"centre-a"}}, "7,6,4,1", 4)
	expect("status never widens", "41", map[string]any{"status": "open", "centres": []any{"centre-b"}}, "", 0)
	expect("since", "41", map[string]any{"since": "2026-06-15T00:00:00Z"}, "8,7", 2)
	expect("ids", "41", map[string]any{"ids": []any{"1", "3"}}, "1", 1) // 3 is another centre's
	if _, code := query("41", map[string]any{"ids": []any{"abc"}}); code != "invalid_argument" {
		t.Errorf("non-numeric id on an integer field: %q", code)
	}
	if _, code := query("41", map[string]any{"status": nil}); code == "" {
		t.Error("an explicit null reached a condition without failing")
	}

	// Search: % is a wildcard, and stays inside the scope and the selection.
	expect("search", "41", map[string]any{"q": "alpha"}, "7,1", 2)
	expect("search %", "41", map[string]any{"q": "%"}, seven, 7)
	expect("search and centre", "41", map[string]any{"q": "%", "centres": []any{"centre-a"}}, seven, 7)
	expect("search other centre", "41", map[string]any{"q": "%", "centres": []any{"centre-b"}}, "", 0)
	expect("search, no authorisation", "42", map[string]any{"q": "%"}, "", 0)
	expect("search and status", "41", map[string]any{"q": "alpha", "status": "closed"}, "", 0)

	// Paging, with the total from the same predicate.
	seen := []string{}
	after := any(nil)
	for pages := 0; ; pages++ {
		got, code := query("41", map[string]any{"first": 3, "after": after, "status": "open"})
		if code != "" || got.total != "4" {
			t.Fatalf("page %d: %+v (%s)", pages, got, code)
		}
		seen = append(seen, got.ids)
		if got.next == "" {
			break
		}
		after = got.next
		if pages > 3 {
			t.Fatal("paging does not end")
		}
	}
	if strings.Join(seen, "|") != "7,6,4|1" {
		t.Errorf("pages: %v", seen)
	}

	// With only optional filters, a false condition must read everything rather
	// than send Database an empty group.
	onlyOptional := config(func(where []any) []any { return where[1:5] })
	onlyOptional["access"] = map[string]any{"values": map[string]any{}}
	if failure := publish(goodCentres, onlyOptional); failure != "" {
		t.Fatal(failure)
	}
	expect("nothing left", "41", nil, "8,7,6,5,4,3,2,1", 8)
	expect("first filter on", "41", selection("centre-b"), "3", 1)
	if failure := publish(goodCentres, config(nil)); failure != "" {
		t.Fatal(failure)
	}

	// Live: edits and authorisation changes both re-run conditions and scope.
	header := http.Header{}
	header.Set("Sec-WebSocket-Protocol", "graphql-transport-ws")
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(r.graph.URL(), "http")+"/public/realtime/default", header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var ack wsMessage
	if conn.WriteJSON(map[string]any{"type": "connection_init", "payload": map[string]any{"Authorization": "Bearer " + pipelineToken("41")}}) != nil || conn.ReadJSON(&ack) != nil || ack.Type != "connection_ack" {
		t.Fatalf("realtime init: %+v", ack)
	}
	subscribe := func(id string, centres ...any) {
		if err := conn.WriteJSON(map[string]any{"id": id, "type": "subscribe", "payload": map[string]any{
			"query":     `subscription($centres: [String!]!) { livePage(centres: $centres, first: 100) { rows { id } total } }`,
			"variables": map[string]any{"centres": append([]any{}, centres...)},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	latest := map[string]string{}
	await := func(step string, want map[string]string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			matched := true
			for id, ids := range want {
				matched = matched && latest[id] == ids
			}
			if matched {
				return
			}
			_ = conn.SetReadDeadline(deadline)
			var message wsMessage
			if err := conn.ReadJSON(&message); err != nil {
				t.Fatalf("%s: have %v, want %v: %v", step, latest, want, err)
			}
			if message.Type == "error" {
				t.Fatalf("%s: subscription %s failed: %s", step, message.ID, message.Payload)
			}
			if message.Type != "next" {
				continue
			}
			var payload struct {
				Data struct {
					LivePage struct {
						Rows []struct {
							ID string `json:"id"`
						} `json:"rows"`
						Total int `json:"total"`
					} `json:"livePage"`
				} `json:"data"`
				Errors []any `json:"errors"`
			}
			if json.Unmarshal(message.Payload, &payload) != nil || len(payload.Errors) > 0 {
				t.Fatalf("%s: payload %s", step, message.Payload)
			}
			ids := []string{}
			for _, row := range payload.Data.LivePage.Rows {
				ids = append(ids, row.ID)
			}
			if payload.Data.LivePage.Total != len(ids) {
				t.Fatalf("%s: total %d for rows %v", step, payload.Data.LivePage.Total, ids)
			}
			latest[message.ID] = strings.Join(ids, ",")
		}
	}
	subscribe("all")
	subscribe("own", "centre-a")
	subscribe("other", "centre-b")
	await("initial", map[string]string{"all": seven, "own": seven, "other": ""})

	r.insert("leads", row(9, "centre-a", "open", "zeta", "2026-09-01T00:00:00Z"))
	await("insert in scope", map[string]string{"all": "9," + seven, "own": "9," + seven, "other": ""})

	r.operator("db_update", map[string]any{"collection": "leads", "where": map[string]any{"field": "id", "op": "eq", "value": "9"}, "set": map[string]any{"centre_id": "centre-b"}, "maxAffected": 1})
	await("row leaves the scope", map[string]string{"all": seven, "own": seven, "other": ""})

	// The profile moves: empty selections follow the new scope, and a selection
	// of the old centre now matches nothing.
	r.operator("db_update", map[string]any{"collection": "profiles", "where": map[string]any{"field": "id", "op": "eq", "value": "p1"}, "set": map[string]any{"centre_id": "centre-b"}, "maxAffected": 1})
	await("authorisation moves", map[string]string{"all": "9,3", "own": "", "other": "9,3"})

	// The profile goes: nothing is visible, whatever the selection.
	r.operator("db_delete", map[string]any{"collection": "profiles", "where": map[string]any{"field": "id", "op": "eq", "value": "p1"}, "maxAffected": 1})
	await("authorisation removed", map[string]string{"all": "", "own": "", "other": ""})
}
