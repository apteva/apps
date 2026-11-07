package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/database/engine"
)

func queryTestJSON(t *testing.T, path string, dst any) {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if e := d.Decode(dst); e != nil {
		t.Fatal(e)
	}
}
func TestQueryMCPHTTPAndReadGrants(t *testing.T) {
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		t.Run(adapter, func(t *testing.T) {
			a := testApp(t)
			operator := &sdk.Caller{ProjectID: "p", AgentID: 1, DefaultEffect: "allow"}
			app := &sdk.Caller{ProjectID: "p", AppInstallID: 7, AppName: "graphql", DefaultEffect: "allow"}
			call := func(op string, args map[string]any) any {
				t.Helper()
				v, e := invoke(a, operator, op, args)
				if e != nil {
					t.Fatal(op, e)
				}
				return v
			}
			// A read-only query cannot create an absent default database.
			var missing map[string]any
			queryTestJSON(t, "testdata/dependent-aggregation.json", &missing)
			if _, e := invoke(a, operator, "query", missing); e == nil {
				t.Fatal("query on missing default succeeded")
			}
			if v := call("databases_list", nil); len(v.([]engine.DatabaseInfo)) != 0 {
				t.Fatal("read-only query created a database")
			}
			call("database_create", map[string]any{"database": "reporting", "adapter": adapter})
			fields := []any{}
			for _, name := range []string{"id", "centre", "period", "caller", "kind", "contracts", "campaign", "adviser", "display_date"} {
				typ := "text"
				if name == "contracts" {
					typ = "integer"
				}
				fields = append(fields, map[string]any{"name": name, "type": typ, "nullable": name != "id"})
			}
			call("collection_create", map[string]any{"database": "reporting", "collection": "facts", "fields": fields})
			var records []any
			queryTestJSON(t, "testdata/dependent-records.json", &records)
			call("insert", map[string]any{"database": "reporting", "collection": "facts", "records": records})
			var plan map[string]any
			queryTestJSON(t, "testdata/dependent-aggregation.json", &plan)
			plan["database"] = "reporting"
			assertAnswer := func(v any) {
				t.Helper()
				body, _ := json.Marshal(v)
				var out struct {
					Rows []struct {
						Revenue   string `json:"revenue_cents"`
						Contracts string `json:"contracts"`
					}
				}
				if e := json.Unmarshal(body, &out); e != nil || len(out.Rows) != 1 || out.Rows[0].Revenue != "6000" || out.Rows[0].Contracts != "2" {
					t.Fatalf("bad result %s: %v", body, e)
				}
			}
			assertAnswer(call("query", plan))
			var consolidated map[string]any
			queryTestJSON(t, "testdata/dependent-aggregate.json", &consolidated)
			consolidated["database"] = "reporting"
			assertAnswer(call("aggregate", consolidated))
			findPlan := map[string]any{"database": "reporting", "pipeline": map[string]any{"version": 1, "nodes": plan["nodes"], "from": "rates"}, "count": true, "orderBy": []any{map[string]any{"field": "period", "direction": "desc"}}, "limit": 1}
			first := call("find", findPlan).(map[string]any)
			if first["total"] != "2" || first["hasMore"] != true {
				t.Fatal(first)
			}
			findPlan["cursor"] = first["nextCursor"]
			second := call("find", findPlan).(map[string]any)
			if second["hasMore"] != false {
				t.Fatal(second)
			}
			for _, tool := range a.MCPTools() {
				if tool.Name == "db_query" || tool.Name == "db_joined_aggregate" || tool.Name == "db_joined_page" || tool.Name == "db_related_read" {
					if tool.Meta["deprecated"] != true {
						t.Fatal("compatibility alias unmarked", tool.Name)
					}
				}
			}

			plan["scope"] = "project"
			consolidated["scope"] = "project"
			findPlan["scope"] = "project"
			for op, args := range map[string]map[string]any{"aggregate": consolidated, "find": findPlan} {
				if _, e := invoke(a, app, op, args); e == nil {
					t.Fatal("ungranted canonical read", op)
				}
			}

			if _, e := invoke(a, app, "query", plan); e == nil {
				t.Fatal("ungranted app read project database")
			}
			call("access_grant", map[string]any{"database": "reporting", "app": "graphql", "access": "read"})
			v, e := invoke(a, app, "query", plan)
			if e != nil {
				t.Fatal(e)
			}
			assertAnswer(v)
			v, e = invoke(a, app, "aggregate", consolidated)
			if e != nil {
				t.Fatal(e)
			}
			assertAnswer(v)
			if _, e := invoke(a, app, "find", findPlan); e != nil {
				t.Fatal(e)
			}

			denied := &sdk.Caller{ProjectID: "p", AgentID: 2, DefaultEffect: "deny"}
			if _, e := invoke(a, denied, "query", plan); e == nil {
				t.Fatal("missing read permission accepted")
			}
			foreign := &sdk.Caller{ProjectID: "q", AppInstallID: 7, AppName: "graphql", DefaultEffect: "allow"}
			if _, e := invoke(a, foreign, "query", plan); e == nil {
				t.Fatal("foreign project read accepted")
			}
			for _, caller := range []*sdk.Caller{denied, foreign} {
				for op, args := range map[string]map[string]any{"aggregate": consolidated, "find": findPlan} {
					if _, e := invoke(a, caller, op, args); e == nil {
						t.Fatal("unauthorized canonical read", op)
					}
				}
			}
			delete(consolidated, "scope")
			delete(findPlan, "scope")
			for op, args := range map[string]map[string]any{"aggregate": consolidated, "find": findPlan} {
				raw, _ := json.Marshal(args)
				req := httptest.NewRequest("POST", "/operations/"+op, bytes.NewReader(raw))
				req.Header.Set("X-Apteva-Project-ID", "p")
				w := httptest.NewRecorder()
				a.handleHTTP(w, req)
				if w.Code != 200 {
					t.Fatal(op, w.Code, w.Body.String())
				}
			}
			delete(plan, "scope")
			body, _ := json.Marshal(plan)
			req := httptest.NewRequest("POST", "/operations/query", bytes.NewReader(body))
			req.Header.Set("X-Apteva-Project-ID", "p")
			w := httptest.NewRecorder()
			a.handleHTTP(w, req)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			var answer any
			json.Unmarshal(w.Body.Bytes(), &answer)
			assertAnswer(answer)
			for _, mutation := range []func(map[string]any){
				func(p map[string]any) { p["collection"] = "facts" },
				func(p map[string]any) { p["nodes"].([]any)[0].(map[string]any)["unknown"] = true },
				func(p map[string]any) { p["nodes"].([]any)[0].(map[string]any)["op"] = "delete" },
				func(p map[string]any) { p["timeoutMs"] = 60001 },
			} {
				var bad map[string]any
				json.Unmarshal(body, &bad)
				mutation(bad)
				if _, e := invoke(a, operator, "query", bad); e == nil {
					t.Fatalf("invalid query accepted: %v", bad)
				}
			}
			// Query is also a read in a batch, including through an app's read-only grant.
			plan["scope"] = "project"
			batchPlan := map[string]any{}
			json.Unmarshal(body, &batchPlan)
			delete(batchPlan, "database")
			v, e = invoke(a, app, "batch", map[string]any{"database": "reporting", "scope": "project", "operations": []any{map[string]any{"op": "query", "args": batchPlan}}})
			if e != nil {
				t.Fatal(e)
			}
			result := v.(map[string]any)["results"].([]any)
			assertAnswer(result[0])
			aggregateBatch := map[string]any{}
			raw, _ := json.Marshal(consolidated)
			json.Unmarshal(raw, &aggregateBatch)
			delete(aggregateBatch, "database")
			v, e = invoke(a, app, "batch", map[string]any{"database": "reporting", "scope": "project", "operations": []any{map[string]any{"op": "aggregate", "args": aggregateBatch}, map[string]any{"op": "find", "args": map[string]any{"collection": "facts", "limit": 1, "count": true}}}})
			if e != nil {
				t.Fatal(e)
			}
			assertAnswer(v.(map[string]any)["results"].([]any)[0])

		})
	}
}
