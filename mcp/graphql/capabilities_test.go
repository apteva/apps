package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	gql "github.com/graphql-go/graphql"
)

type capabilityPlatform struct {
	sdk.PlatformClient
	sdk.AppContextClient
	mu    sync.Mutex
	calls map[string]map[string]any
}

func (p *capabilityPlatform) CallAppResultContext(_ context.Context, app, tool string, input map[string]any, out any) error {
	return p.CallAppResult(app, tool, input, out)
}
func (p *capabilityPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	if app == "tables" && tool == "tables_batch" {
		results := map[string]any{}
		for _, op := range input["operations"].([]map[string]any) {
			var result any
			if err := p.CallAppResult(app, op["operation"].(string), op["args"].(map[string]any), &result); err != nil {
				return err
			}
			results[op["id"].(string)] = map[string]any{"status": "ok", "result": result}
		}
		raw, _ := json.Marshal(map[string]any{"results": results})
		return json.Unmarshal(raw, out)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls[app+"."+tool] = input
	var result any
	switch app + "." + tool {
	case "database.db_aggregate", "tables.rows_aggregate":
		result = map[string]any{"rows": []any{map[string]any{"category": "demo", "total": 2, "sum": 30, "avg": 15, "min": 10, "max": 20}}}
	case "database.db_count", "tables.rows_count":
		result = map[string]any{"count": 2}
	case "tables.rows_search":
		result = map[string]any{"rows": []any{map[string]any{"id": 1}}}
	case "functions.functions_invoke":
		result = map[string]any{"status": "ok", "response": `{"ok":true}`}
	default:
		return fmt.Errorf("unexpected adapter %s.%s", app, tool)
	}
	raw, _ := json.Marshal(result)
	return json.Unmarshal(raw, out)
}

const capabilitySDL = `enum MetricOp { count sum avg min max }
input Metric { name: String! op: MetricOp! col: String }
input Filter { col: String! op: String! value: Int! }
type Stats { category: String! total: Int! sum: Float! avg: Float! min: Float! max: Float! }
type Row { id: ID! }
type Status { ok: Boolean! }
type Query {
 stats(metrics:[Metric!]!,group_by:[String!],order_by:String,where:[Filter!],limit:Int, database:String,collection:String): [Stats!]!
 count: Int!
 rows: [Row!]!
 remote: Status!
 function(message:String!): Status!
}`

func TestAggregationAdaptersAndMixedSourcesRemainAvailable(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":true}`) }))
	defer remote.Close()
	metrics := []any{map[string]any{"name": "total", "op": "count"}}
	for _, op := range []string{"sum", "avg", "min", "max"} {
		metrics = append(metrics, map[string]any{"name": op, "op": op, "col": "amount"})
	}
	for _, kind := range []string{"tables", "database"} {
		t.Run(kind, func(t *testing.T) {
			query := `query($m:[Metric!]!) { stats(metrics:$m,group_by:["category"],order_by:"total desc",where:[{col:"amount",op:"gt",value:0}],limit:1000,database:"attacker",collection:"attacker") { category total sum avg min max } }`
			bindings := &executionBindings{sources: map[int64]sourceRecord{
				1: {Kind: kind, Config: map[string]any{"table": "orders", "database": "prod", "collection": "orders"}},
				2: {Kind: "tables", Config: map[string]any{"table": "orders"}},
				3: {Kind: "http", Config: map[string]any{"url": remote.URL}},
				4: {Kind: "function", Config: map[string]any{"name": "demo"}},
			}, resolvers: map[string]resolverRecord{
				"Query.stats": {SourceID: 1, Operation: "aggregate"}, "Query.count": {SourceID: 1, Operation: "count"},
				"Query.rows": {SourceID: 2, Operation: "find"}, "Query.remote": {SourceID: 3}, "Query.function": {SourceID: 4},
			}}
			var expected []byte
			for _, disabled := range []bool{false, true} {
				p := &capabilityPlatform{calls: map[string]map[string]any{}}
				a, schema, ctx := standardApp(t, capabilitySDL, p, bindings)
				a.httpClient = remote.Client()
				ctx = context.WithValue(ctx, disableFastProjectionKey{}, disabled)
				result := runStandard(schema, gql.Params{Context: ctx, RequestString: query, VariableValues: map[string]any{"m": metrics}})
				if len(result.Errors) > 0 {
					t.Fatal(result.Errors)
				}
				body, _ := json.Marshal(result.Data)
				if expected == nil {
					expected = body
				} else if string(body) != string(expected) {
					t.Fatalf("aggregation changed: %s != %s", body, expected)
				}
				if ctx.Value(standardRequestKey{}).(*standardRequest).fastProjectionUsed != (kind == "tables" && !disabled) {
					t.Fatal("unexpected fast-path eligibility")
				}
				tool, group, order := kind+".rows_aggregate", "group_by", "order_by"
				if kind == "database" {
					tool, group, order = "database.db_aggregate", "groupBy", "orderBy"
				}
				input := p.calls[tool]
				if input["_project_id"] != "p1" || !reflect.DeepEqual(input["metrics"], metrics) || fmt.Sprint(input[group]) != "[category]" || input[order] != "total desc" || input["where"] == nil {
					t.Fatalf("aggregation inputs lost: %v", input)
				}
				if kind == "database" && (input["database"] != "prod" || input["collection"] != "orders") {
					t.Fatal("source scope overridden")
				}
			}
			// A query mixing source kinds must use the standard executor, not
			// remove capabilities just because they are ineligible for fast projection.
			p := &capabilityPlatform{calls: map[string]map[string]any{}}
			a, schema, ctx := standardApp(t, capabilitySDL, p, bindings)
			a.httpClient = remote.Client()
			result := runStandard(schema, gql.Params{Context: ctx, RequestString: `{ count rows { id } remote { ok } function(message:"hello") { ok } }`})
			if len(result.Errors) > 0 {
				t.Fatal(result.Errors)
			}
			body, _ := json.Marshal(result.Data)
			if string(body) != `{"count":2,"function":{"ok":true},"remote":{"ok":true},"rows":[{"id":"1"}]}` || ctx.Value(standardRequestKey{}).(*standardRequest).fastProjectionUsed {
				t.Fatalf("mixed adapters: %s", body)
			}
			if p.calls["functions.functions_invoke"]["event"].(map[string]any)["arguments"].(map[string]any)["message"] != "hello" {
				t.Fatal("Function arguments lost")
			}
		})
	}
}

func TestRealtimeRetainsAPIAndProjectIsolation(t *testing.T) {
	hub := newSubscriptionHub()
	defer hub.close()
	for _, scope := range [][2]string{{"p1", "one"}, {"p1", "two"}, {"p2", "one"}} {
		sub, cancel := hub.subscribeForAPI(scope[0], scope[1], "changed")
		defer cancel()
		if scope == [2]string{"p1", "one"} {
			defer func() {
				if len(sub.events) != 1 {
					t.Fatal("target event not delivered")
				}
			}()
		} else {
			defer func() {
				if len(sub.events) != 0 {
					t.Fatal("realtime event leaked")
				}
			}()
		}
	}
	if hub.publishForAPI("p1", "one", "changed", map[string]any{"id": 1}) != 1 {
		t.Fatal("wrong delivery count")
	}
}

func TestPublishedSchemasRetainEnvironmentAndAPIIsolation(t *testing.T) {
	db := testDB(t)
	for _, scope := range [][3]string{{"p1", "one", "development"}, {"p1", "one", "production"}, {"p1", "two", "production"}, {"p2", "one", "production"}} {
		field := scope[0] + scope[1] + scope[2]
		row, issues, err := createSchemaForAPI(db, scope[0], scope[1], scope[2], "type Query { "+field+": String }", 0)
		if err != nil || len(issues) > 0 {
			t.Fatalf("schema: %v %v", err, issues)
		}
		if _, err := publishSchemaForAPI(db, scope[0], scope[1], scope[2], row.Version); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range [][3]string{{"p1", "one", "development"}, {"p1", "one", "production"}, {"p1", "two", "production"}, {"p2", "one", "production"}} {
		row, err := getSchemaForAPI(db, scope[0], scope[1], scope[2], 0, true)
		if err != nil || row == nil || row.SDL != "type Query { "+scope[0]+scope[1]+scope[2]+": String }" {
			t.Fatalf("schema isolation: %+v %v", row, err)
		}
	}
}
