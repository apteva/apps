package main

import (
	"bytes"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"net/http/httptest"
	"testing"
)

func TestRelatedAggregateMCPHTTPAndReadGrants(t *testing.T) {
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		t.Run(adapter, func(t *testing.T) {
			a := testApp(t)
			operator := &sdk.Caller{ProjectID: "p", AgentID: 1, DefaultEffect: "allow"}
			call := func(op string, args map[string]any) any {
				t.Helper()
				v, e := invoke(a, operator, op, args)
				if e != nil {
					t.Fatal(e)
				}
				return v
			}
			call("database_create", map[string]any{"database": "quality", "adapter": adapter})
			for collection, defs := range map[string]map[string]string{"parents": {"id": "integer", "centre": "text", "points": "number", "maximum": "number"}, "contexts": {"id": "integer", "parent": "integer", "sequence": "integer", "allowed": "boolean", "text": "text"}} {
				fields := []any{}
				for name, typ := range defs {
					fields = append(fields, map[string]any{"name": name, "type": typ, "nullable": name != "id"})
				}
				call("collection_create", map[string]any{"database": "quality", "collection": collection, "primaryKey": []string{"id"}, "fields": fields})
			}
			call("index_create", map[string]any{"database": "quality", "collection": "contexts", "index": map[string]any{"name": "by_parent", "fields": []any{map[string]any{"field": "parent"}}}})
			call("insert", map[string]any{"database": "quality", "collection": "parents", "records": []any{map[string]any{"id": "1", "centre": "allowed", "points": 50, "maximum": 100}, map[string]any{"id": "2", "centre": "allowed", "points": 10, "maximum": 20}, map[string]any{"id": "3", "centre": "foreign", "points": 90, "maximum": 100}}})
			call("insert", map[string]any{"database": "quality", "collection": "contexts", "records": []any{map[string]any{"id": "1", "parent": "1", "sequence": "1", "allowed": true, "text": "match"}, map[string]any{"id": "2", "parent": "1", "sequence": "2", "allowed": true, "text": "match"}, map[string]any{"id": "3", "parent": "2", "sequence": "1", "allowed": true, "text": "match"}}})
			var args map[string]any
			queryTestJSON(t, "testdata/related-aggregate.json", &args)
			args["database"] = "quality"
			check := func(v any) {
				t.Helper()
				body, _ := json.Marshal(v)
				var out struct {
					Rows []struct {
						Parents         string
						Points, Average float64
					}
				}
				if e := json.Unmarshal(body, &out); e != nil || len(out.Rows) != 1 || out.Rows[0].Parents != "2" || out.Rows[0].Points != 60 || out.Rows[0].Average != 50 {
					t.Fatal(string(body), e)
				}
			}
			check(call("aggregate", args))
			raw, _ := json.Marshal(args)
			req := httptest.NewRequest("POST", "/operations/aggregate", bytes.NewReader(raw))
			req.Header.Set("X-Apteva-Project-ID", "p")
			w := httptest.NewRecorder()
			a.handleHTTP(w, req)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			var out any
			json.Unmarshal(w.Body.Bytes(), &out)
			check(out)
			caller := &sdk.Caller{ProjectID: "p", AppInstallID: 7, AppName: "graphql", DefaultEffect: "allow"}
			args["scope"] = "project"
			if _, e := invoke(a, caller, "aggregate", args); e == nil {
				t.Fatal("accepted ungranted app")
			}
			call("access_grant", map[string]any{"database": "quality", "app": "graphql", "access": "read"})
			v, e := invoke(a, caller, "aggregate", args)
			if e != nil {
				t.Fatal(e)
			}
			check(v)
			args["cursor"] = "parent-page"
			if _, e := invoke(a, caller, "aggregate", args); e == nil {
				t.Fatal("accepted parent cursor")
			}
		})
	}
}
