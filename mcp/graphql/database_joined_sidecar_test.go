package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// These run the published resolvers against released Database, on the same plans the
// fake's tests use, so the fake can't drift from what Database really answers.

func seedJoinedRows(r *realDatabase) {
	// Integer fields travel as decimal text.
	insert := func(collection string, records ...map[string]any) {
		for _, record := range records {
			for field, value := range record {
				if number, isInt := value.(int); isInt {
					record[field] = strconv.Itoa(number)
				}
			}
		}
		r.insert(collection, records...)
	}
	r.collection("centres", "id:text", "local_id:text")
	r.collection("profiles", "id:text", "user_id:text", "centre_id:text")
	r.collection("grants", "id:text", "user_id:text", "offer_ids:text")
	r.collection("reports", "id:integer", "local_id:text", "centre_id:text", "modified_time:text", "score_total:integer")
	r.collection("sale_links", "id:text", "report_id:text", "sale_ref:text")
	r.collection("sales", "id:integer", "local_id:text", "offer_id:text", "client:text", "phone:text")
	r.collection("offers", "id:text", "local_id:text", "name:text")
	r.collection("call_links", "id:text", "report_id:text", "call_ref:text")
	r.collection("calls", "id:integer", "local_id:text", "interaction_id:text", "centre_id:text", "prospect_id:integer", "sale_id:integer", "started_at:text", "attempt_number:integer", "offer_mix:text", "state:text", "direction:text", "status:text", "audio_files:text")
	r.collection("prospects", "id:integer", "name:text", "phone:text")
	r.collection("costs", "id:integer", "day:text", "campaign:text", "spend:integer", "conversions:integer", "visible:text")
	r.collection("campaigns", "id:text", "name:text")
	for _, index := range [][2]string{
		{"profiles", "user_id"}, {"grants", "user_id"}, {"reports", "centre_id"}, {"sale_links", "report_id"},
		{"sales", "local_id"}, {"offers", "local_id"}, {"call_links", "report_id"}, {"calls", "local_id"},
		{"calls", "centre_id"}, {"calls", "direction"}, {"costs", "day"},
	} {
		r.operator("db_index_create", map[string]any{"collection": index[0], "index": map[string]any{"name": index[0] + "_" + index[1], "fields": []any{map[string]any{"field": index[1], "direction": "asc"}}}})
	}
	insert("centres", map[string]any{"id": "a", "local_id": "a"}, map[string]any{"id": "b", "local_id": "b"})
	insert("profiles", map[string]any{"id": "p21", "user_id": "21", "centre_id": "a"}, map[string]any{"id": "p22", "user_id": "22", "centre_id": "a"}, map[string]any{"id": "p27", "user_id": "27", "centre_id": "a"})
	insert("grants", map[string]any{"id": "g22", "user_id": "22", "offer_ids": "energy"}, map[string]any{"id": "g27", "user_id": "27", "offer_ids": ""})
	insert("offers", map[string]any{"id": "energy", "local_id": "energy", "name": "Energy"}, map[string]any{"id": "telecom", "local_id": "telecom", "name": "Telecom"})
	insert("reports",
		map[string]any{"id": 1, "local_id": "r1", "centre_id": "a", "modified_time": "2026-10-01", "score_total": 90},
		map[string]any{"id": 2, "local_id": "r2", "centre_id": "a", "modified_time": "2026-10-02", "score_total": nil},
		map[string]any{"id": 3, "local_id": "r3", "centre_id": "b", "modified_time": "2026-10-03", "score_total": 70})
	// Report 1 has 41 sales, one past the cap on context.
	for i := 1; i <= 41; i++ {
		insert("sales", map[string]any{"id": i, "local_id": fmt.Sprintf("s%d", i), "offer_id": "energy", "client": fmt.Sprintf(" Client %02d ", i), "phone": fmt.Sprintf("336%02d", i)})
		insert("sale_links", map[string]any{"id": fmt.Sprintf("l%d", i), "report_id": "1", "sale_ref": fmt.Sprintf("s%d", i)})
	}
	insert("sales",
		map[string]any{"id": 50, "local_id": "s50", "offer_id": "telecom", "client": "Tel <b>", "phone": "33650"},
		map[string]any{"id": 51, "local_id": "s51", "offer_id": "telecom", "client": "Zoe", "phone": "33651"})
	insert("sale_links", map[string]any{"id": "l50", "report_id": "2", "sale_ref": "s50"}, map[string]any{"id": "l51", "report_id": "3", "sale_ref": "s51"})
	insert("prospects", map[string]any{"id": 1, "name": "Ana", "phone": "33601"}, map[string]any{"id": 2, "name": nil, "phone": "33602"}, map[string]any{"id": 3, "name": "Mixte Nom", "phone": "33690"})
	call := func(id int, local, interaction, centre string, prospect int, started string, attempt int, mix string) map[string]any {
		return map[string]any{"id": id, "local_id": local, "interaction_id": interaction, "centre_id": centre, "prospect_id": prospect, "sale_id": 50, "started_at": started,
			"attempt_number": attempt, "offer_mix": mix, "state": "open", "direction": "inbound", "status": "answered", "audio_files": "[\"" + local + ".mp3\"]"}
	}
	insert("calls",
		call(9, "c1", "", "a", 3, "2026-10-03T09:00", 1, "mixed"),
		call(10, "solo", "i2", "a", 1, "2026-10-04T09:00", 1, "single"),
		call(11, "leg1", "i1", "a", 2, "2026-10-05T10:00", 1, "single"),
		call(12, "leg2", "i1", "a", 1, "2026-10-05T10:05", 2, "single"),
		call(13, "other", "i3", "b", 1, "2026-10-06T09:00", 1, "single"))
	insert("call_links", map[string]any{"id": "cl1", "report_id": "2", "call_ref": "c1"})
	insert("campaigns", map[string]any{"id": "x", "name": "Brand"}, map[string]any{"id": "y", "name": "Search"})
	insert("costs",
		map[string]any{"id": 1, "day": "2026-09-30", "campaign": "x", "spend": 1000, "conversions": 1, "visible": "yes"},
		map[string]any{"id": 2, "day": "2026-10-01", "campaign": "x", "spend": 9007199254740993, "conversions": 2, "visible": "yes"},
		map[string]any{"id": 3, "day": "2026-10-01", "campaign": "y", "spend": 500, "conversions": 3, "visible": "yes"},
		map[string]any{"id": 4, "day": "2026-10-02", "campaign": "y", "spend": 700, "conversions": 4, "visible": "no"})
	r.operator("db_access_grant", map[string]any{"app": "graphql", "access": "read"})
}

func joinedResolverMap() map[string]map[string]any {
	out := map[string]map[string]any{}
	for field, plan := range joinedResolvers() {
		var config map[string]any
		if err := json.Unmarshal([]byte(plan), &config); err != nil {
			panic(field + ": " + err.Error())
		}
		out["Query."+field] = map[string]any{"operation": databasePipelineOperation, "config": config}
	}
	return out
}

func realJSON(t *testing.T, what string, got graphResponse, want string) {
	t.Helper()
	if len(got.Errors) > 0 {
		t.Errorf("%s: errors %v", what, got.Errors)
		return
	}
	sameJSON(t, what, got.Data, want)
}

func TestRealDatabaseJoinedReads(t *testing.T) {
	r := newRealDatabase(t, "graphql-joined-test")
	seedJoinedRows(r)
	r.requireAuth()
	for _, user := range []string{"21", "23"} {
		r.permissions[user] = []string{}
	}
	r.permissions["20"] = []string{"all_centres"}
	r.permissions["22"] = []string{"quality"}
	r.permissions["24"] = []string{"economics"}
	r.permissions["25"] = []string{"economics", "all_centres"}
	r.permissions["26"] = []string{"admin", "recording:read"}
	r.permissions["27"] = []string{"quality"}

	failure := r.publish(joinedSDL, joinedResolverMap())
	if os.Getenv("GRAPHQL_TEST_DATABASE_JOINED") == "refused" {
		if !strings.Contains(failure, "require Database 0.12.0") {
			t.Fatalf("older Database: %s", failure)
		}
		return
	}
	if failure != "" {
		t.Fatalf("publish: %s", failure)
	}

	t.Run("Quality keeps its saved shape", func(t *testing.T) {
		got := r.queryAs("20", `{qualityReportsPage(limit: 2, offset: 1){`+qualityFields+`}}`, nil)
		if len(got.Errors) > 0 {
			t.Fatal(got.Errors)
		}
		rows := got.Data["qualityReportsPage"].([]any)
		if len(rows) != 2 {
			t.Fatalf("rows: %v", rows)
		}
		second := rows[0].(map[string]any)
		sameJSON(t, "second report", second, `{"id":"2","local_id":"r2","score_total":null,"total_count":3,
		  "display_context":"[{\"client_name\":\"Tel <b>\",\"phone\":\"33650\"},{\"client_name\":\"Mixte Nom\",\"phone\":\"33690\"}]"}`)
		first := rows[1].(map[string]any)
		var context []map[string]any
		if err := json.Unmarshal([]byte(first["display_context"].(string)), &context); err != nil {
			t.Fatal(err)
		}
		if len(context) != 40 || context[0]["client_name"] != "Client 01" || context[39]["client_name"] != "Client 40" {
			t.Errorf("bounded context: %d rows, first %v, last %v", len(context), context[0], context[len(context)-1])
		}
		if first["score_total"] != float64(90) || first["total_count"] != float64(3) {
			t.Errorf("numbers: %v", first)
		}
		// A sale past the cap can never match a search.
		realJSON(t, "search past the cap", r.queryAs("20", `{qualityReportsPage(q: "Client 41"){id}}`, nil), `{"qualityReportsPage":[]}`)
		realJSON(t, "search inside the cap", r.queryAs("20", `{qualityReportsPage(q: "Client 40"){id total_count}}`, nil), `{"qualityReportsPage":[{"id":"1","total_count":1}]}`)
		// %, _ and other wildcards are ordinary characters.
		realJSON(t, "wildcards are literal", r.queryAs("20", `{qualityReportsPage(q: "%"){id}}`, nil), `{"qualityReportsPage":[]}`)
		realJSON(t, "past the end", r.queryAs("20", `{qualityReportsPage(offset: 50){id}}`, nil), `{"qualityReportsPage":[]}`)
	})

	t.Run("each viewer gets their own scope", func(t *testing.T) {
		const q = `{qualityReportsPage{id}}`
		realJSON(t, "all centres", r.queryAs("20", q, nil), `{"qualityReportsPage":[{"id":"3"},{"id":"2"},{"id":"1"}]}`)
		realJSON(t, "centre scoped", r.queryAs("21", q, nil), `{"qualityReportsPage":[{"id":"2"},{"id":"1"}]}`)
		realJSON(t, "no profile", r.queryAs("23", q, nil), `{"qualityReportsPage":[]}`)
		realJSON(t, "a centre outside scope narrows to nothing", r.queryAs("21", `{qualityReportsPage(centreId: "b"){id}}`, nil), `{"qualityReportsPage":[]}`)
		realJSON(t, "a forged subject changes nothing", r.queryAs("23", `{qualityReportsPage(subject: "20"){id}}`, nil), `{"qualityReportsPage":[]}`)
	})

	t.Run("the summary counts what the list excludes and nothing more", func(t *testing.T) {
		realJSON(t, "list", r.queryAs("22", `{qualityReportsPage(q: "Mixte"){id display_context}}`, nil), `{"qualityReportsPage":[]}`)
		summary := r.queryAs("22", `{qualitySummary(q: "Mixte"){report_total}}`, nil)
		realJSON(t, "summary", summary, `{"qualitySummary":[{"report_total":1}]}`)
		raw, _ := json.Marshal(summary)
		if strings.Contains(string(raw), "Mixte") || strings.Contains(string(raw), "33690") {
			t.Errorf("the summary exposed the match: %s", raw)
		}
		// The offer restriction also trims what the viewer sees on an allowed report.
		realJSON(t, "offer restriction", r.queryAs("22", `{qualityReportsPage{id display_context}}`, nil).pickContext(t), `{"1":40,"2":0}`)
		// A grant holding only a blank is a grant, and it allows nothing.
		realJSON(t, "blank grant", r.queryAs("27", `{qualityReportsPage{id display_context}}`, nil).pickContext(t), `{"1":0,"2":0}`)
		if got := r.queryAs("22", `{playback(id: "c1"){audio_files}}`, nil); got.code() != "permission_denied" {
			t.Errorf("playback after a summary match: %v", got)
		}
		realJSON(t, "playback with its own permission", r.queryAs("26", `{playback(id: "c1"){audio_files}}`, nil), `{"playback":[{"audio_files":"[\"c1.mp3\"]"}]}`)
	})

	t.Run("Activity counts interactions and returns every leg", func(t *testing.T) {
		const q = `{operationsCallsPage(limit: 1){id local_id display_prospect_name attempt_number total_count}}`
		realJSON(t, "centre scoped", r.queryAs("21", q, nil), `{"operationsCallsPage":[
		  {"id":"12","local_id":"leg2","display_prospect_name":"Ana","attempt_number":2,"total_count":3},
		  {"id":"11","local_id":"leg1","display_prospect_name":null,"attempt_number":1,"total_count":3}]}`)
		realJSON(t, "next interaction", r.queryAs("21", `{operationsCallsPage(limit: 1, offset: 1){id total_count}}`, nil), `{"operationsCallsPage":[{"id":"10","total_count":3}]}`)
		realJSON(t, "a blank interaction uses the call's own", r.queryAs("21", `{operationsCallsPage(limit: 1, offset: 2){local_id}}`, nil), `{"operationsCallsPage":[{"local_id":"c1"}]}`)
		realJSON(t, "search", r.queryAs("21", `{operationsCallsPage(q: "mixte"){id total_count}}`, nil), `{"operationsCallsPage":[{"id":"9","total_count":1}]}`)
		realJSON(t, "all centres", r.queryAs("20", `{operationsCallsPage(limit: 1){id total_count}}`, nil), `{"operationsCallsPage":[{"id":"13","total_count":4}]}`)
		realJSON(t, "requested centres", r.queryAs("20", `{operationsCallsPage(limit: 1, centreIds: "[\"b\"]"){id total_count}}`, nil), `{"operationsCallsPage":[{"id":"13","total_count":1}]}`)
		realJSON(t, "offer-only stays denied", r.queryAs("22", q, nil), `{"operationsCallsPage":[]}`)
		realJSON(t, "no access", r.queryAs("23", q, nil), `{"operationsCallsPage":[]}`)
		if got := r.queryAs("21", `{operationsCallsPage(centreIds: "[\"b\"]"){id}}`, nil); len(got.Errors) == 0 {
			t.Errorf("a centre outside scope: %v", got.Data)
		}
	})

	t.Run("costs keep their masks and empty envelope", func(t *testing.T) {
		const q = `{costs(dateFrom: "2026-10-01"){payload}}`
		realJSON(t, "no financial permission", r.queryAs("21", q, nil), `{"costs":[{"payload":"[]"}]}`)
		realJSON(t, "financial but no totals", r.queryAs("24", `{costs(dateFrom: "2026-10-01"){payload}}`, nil), `{"costs":[{"payload":"[{\"campaign\":\"Brand\",\"conversions\":null,\"day\":\"2026-10-01\",\"spend_micros\":\"9007199254740993\"},{\"campaign\":\"Search\",\"conversions\":null,\"day\":\"2026-10-01\",\"spend_micros\":500}]"}]}`)
	})

	t.Run("the export is JSON text behind its own gate", func(t *testing.T) {
		realJSON(t, "admin", r.queryAs("26", `{crmCallExport(limit: 1, offset: -3){payload}}`, nil), `{"crmCallExport":[{"payload":"{\"items\":[{\"id\":13,\"status\":\"answered\"}],\"total\":5}"}]}`)
		realJSON(t, "denied", r.queryAs("21", `{crmCallExport(limit: 1){payload}}`, nil), `{"crmCallExport":[]}`)
	})

	t.Run("cursors stay with their viewer and request", func(t *testing.T) {
		const q = `query($c: String, $centre: String) {qualityCursor(limit: 1, cursor: $c, centreId: $centre){rows{id} nextCursor total}}`
		page := r.queryAs("21", q, nil)
		if len(page.Errors) > 0 {
			t.Fatal(page.Errors)
		}
		first := page.Data["qualityCursor"].(map[string]any)
		sameJSON(t, "first page", first["rows"], `[{"id":"2"}]`)
		cursor, _ := first["nextCursor"].(string)
		if !strings.HasPrefix(cursor, cursorPrefix) {
			t.Fatalf("cursor %q", cursor)
		}
		second := r.queryAs("21", q, map[string]any{"c": cursor})
		if len(second.Errors) > 0 {
			t.Fatal(second.Errors)
		}
		sameJSON(t, "second page", second.Data["qualityCursor"].(map[string]any)["rows"], `[{"id":"1"}]`)
		for what, attempt := range map[string]graphResponse{
			"same scope, another viewer": r.queryAs("22", q, map[string]any{"c": cursor}),
			"wider scope":                r.queryAs("20", q, map[string]any{"c": cursor}),
			"another filter":             r.queryAs("21", q, map[string]any{"c": cursor, "centre": "a"}),
		} {
			if attempt.code() != "invalid_cursor" {
				t.Errorf("%s: %v %v", what, attempt.Data, attempt.Errors)
			}
		}
		// A grant change moves the viewer to another scope, so the cursor ends.
		r.insert("profiles", map[string]any{"id": "p21b", "user_id": "21", "centre_id": "b"})
		if attempt := r.queryAs("21", q, map[string]any{"c": cursor}); attempt.code() != "invalid_cursor" {
			t.Errorf("after a grant change: %v", attempt.Data)
		}
		r.operator("db_delete", map[string]any{"collection": "profiles", "where": map[string]any{"field": "id", "op": "eq", "value": "p21b"}, "maxAffected": 1})
	})

	t.Run("a revision Database refuses leaves the release active", func(t *testing.T) {
		const q = `{qualityReportsPage(limit: 1){id local_id}}`
		const previous = `{"qualityReportsPage":[{"id":"2","local_id":"r2"}]}`
		realJSON(t, "before", r.queryAs("21", q, nil), previous)
		plan := qualityListPlan()
		for name, c := range map[string]struct{ old, replacement, want string }{
			"incompatible join keys":        {`, "comparison": "text"}]}]},`, `}]}]},`, "comparison"},
			"unknown expression":            {`{"trim": {"field": "s.client"}}`, `{"reverse": {"field": "s.client"}}`, "reverse"},
			"missing collection":            {`"collection": "offers"`, `"collection": "nothing_here"`, "nothing_here"},
			"missing field":                 {`"select": ["id", "local_id", "score_total"]`, `"select": ["id", "local_id", "not_a_field"]`, "not_a_field"},
			"unindexed join":                {`"on": [{"local": "s.offer_id", "foreign": "local_id"}]`, `"on": [{"local": "s.offer_id", "foreign": "name"}]`, "needs an index"},
			"fixed value of the wrong type": {`{"field": "centre_id", "op": "in", "value": "$scope.centres"},`, `{"field": "id", "op": "eq", "value": "not-a-number"}, {"field": "centre_id", "op": "in", "value": "$scope.centres"},`, "integer value for id"},
		} {
			if !strings.Contains(plan, c.old) {
				t.Fatalf("%s: plan lacks %s", name, c.old)
			}
			var config map[string]any
			if err := json.Unmarshal([]byte(strings.Replace(plan, c.old, c.replacement, 1)), &config); err != nil {
				t.Fatal(err)
			}
			revision := joinedResolverMap()
			revision["Query.qualityReportsPage"] = map[string]any{"operation": databasePipelineOperation, "config": config}
			failure := r.publish(joinedSDL, revision)
			if !strings.Contains(failure, c.want) {
				t.Errorf("%s: %s", name, failure)
			}
			realJSON(t, name, r.queryAs("21", q, nil), previous)
		}
		// The working plan publishes again.
		if failure := r.publish(joinedSDL, joinedResolverMap()); failure != "" {
			t.Fatal(failure)
		}
	})
}

func (g graphResponse) pickContext(t *testing.T) graphResponse {
	t.Helper()
	if len(g.Errors) > 0 {
		t.Fatal(g.Errors)
	}
	counts := map[string]any{}
	for _, raw := range g.Data["qualityReportsPage"].([]any) {
		row := raw.(map[string]any)
		var context []any
		if err := json.Unmarshal([]byte(row["display_context"].(string)), &context); err != nil {
			t.Fatal(err)
		}
		counts[row["id"].(string)] = len(context)
	}
	return graphResponse{Data: counts}
}

func TestRealDatabaseJoinedLiveRevocation(t *testing.T) {
	if adapter := os.Getenv("GRAPHQL_TEST_DATABASE_ADAPTER"); adapter != "" && adapter != "aptevadb" {
		t.Skip("change capture exists on AptevaDB only")
	}
	if os.Getenv("GRAPHQL_TEST_DATABASE_JOINED") == "refused" {
		t.Skip("this Database has no joined reads")
	}
	r := newRealDatabase(t, "graphql-joined-live-test")
	seedJoinedRows(r)
	r.operator("db_realtime_configure", map[string]any{"enabled": true})
	r.requireAuth()
	r.permissions["21"] = []string{}
	var config map[string]any
	if err := json.Unmarshal([]byte(livePlan), &config); err != nil {
		t.Fatal(err)
	}
	if failure := r.publish(liveSDL, map[string]map[string]any{"Subscription.live": {"operation": databasePipelineOperation, "config": config}}); failure != "" {
		t.Fatal(failure)
	}
	dial := func() (*websocket.Conn, chan wsMessage) {
		header := http.Header{}
		header.Set("Sec-WebSocket-Protocol", "graphql-transport-ws")
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(r.graph.URL(), "http")+"/public/realtime/default", header)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		var ack wsMessage
		if conn.WriteJSON(map[string]any{"type": "connection_init", "payload": map[string]any{"Authorization": "Bearer " + pipelineToken("21")}}) != nil || conn.ReadJSON(&ack) != nil || ack.Type != "connection_ack" {
			t.Fatalf("realtime init: %+v", ack)
		}
		if err := conn.WriteJSON(map[string]any{"id": "live", "type": "subscribe", "payload": map[string]any{"query": `subscription {live {id prospect}}`}}); err != nil {
			t.Fatal(err)
		}
		messages := make(chan wsMessage, 64)
		go func() {
			defer close(messages)
			for {
				var message wsMessage
				if conn.ReadJSON(&message) != nil {
					return
				}
				messages <- message
			}
		}()
		return conn, messages
	}
	const liveStep = 20 * time.Second
	await := func(messages chan wsMessage, step string, ok func(ids string, raw string) bool) {
		t.Helper()
		deadline := time.After(liveStep)
		last := "(nothing)"
		for {
			select {
			case message, open := <-messages:
				if !open {
					t.Fatalf("%s: the connection closed after %s", step, last)
				}
				if message.Type != "next" {
					t.Fatalf("%s: %s %s", step, message.Type, message.Payload)
				}
				var payload struct {
					Data struct {
						Live []struct {
							ID string `json:"id"`
						} `json:"live"`
					} `json:"data"`
				}
				_ = json.Unmarshal(message.Payload, &payload)
				ids := []string{}
				for _, row := range payload.Data.Live {
					ids = append(ids, row.ID)
				}
				last = strings.Join(ids, ",")
				if ok(last, string(message.Payload)) {
					return
				}
			case <-deadline:
				t.Fatalf("%s: last result %q", step, last)
			}
		}
	}
	all := func(ids, _ string) bool { return ids == "9,10,11,12" || ids == "12,11,10,9" }

	first, messages := dial()
	await(messages, "first read", all)
	// A linked prospect's new name reaches the row without any change to the call.
	r.operator("db_update", map[string]any{"collection": "prospects", "where": map[string]any{"field": "id", "op": "eq", "value": "3"}, "set": map[string]any{"name": "Renamed"}, "maxAffected": 1})
	await(messages, "linked prospect", func(_ string, raw string) bool { return strings.Contains(raw, "Renamed") })
	// Revoking the saved profile removes the rows at once.
	r.operator("db_delete", map[string]any{"collection": "profiles", "where": map[string]any{"field": "id", "op": "eq", "value": "p21"}, "maxAffected": 1})
	await(messages, "revocation", func(ids, _ string) bool { return ids == "" })
	// A later data change stays empty, and so does a reconnect.
	r.operator("db_update", map[string]any{"collection": "prospects", "where": map[string]any{"field": "id", "op": "eq", "value": "3"}, "set": map[string]any{"name": "Again"}, "maxAffected": 1})
	await(messages, "after revocation", func(ids, raw string) bool {
		if ids != "" {
			t.Errorf("a revoked row reappeared: %s", raw)
		}
		return true
	})
	first.Close()
	_, messages = dial()
	await(messages, "reconnect", func(ids, raw string) bool {
		if ids != "" {
			t.Errorf("a reconnect saw a revoked row: %s", raw)
		}
		return true
	})
	// Restoring the grant brings them back.
	r.insert("profiles", map[string]any{"id": "p21", "user_id": "21", "centre_id": "a"})
	await(messages, "restored", all)
}
