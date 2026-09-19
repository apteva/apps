package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	gql "github.com/graphql-go/graphql"
	"github.com/vektah/gqlparser/v2/ast"
)

type standardTestSchema struct {
	runtime *gql.Schema
	parsed  *ast.Schema
}

func standardFixture(t *testing.T, sdl string, resolve gql.FieldResolveFn) *standardTestSchema {
	t.Helper()
	schema, issues := validateSDL(sdl)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	runtime, err := buildStandardSchema(schema, resolve)
	if err != nil {
		t.Fatal(err)
	}
	return &standardTestSchema{runtime, schema}
}

func runStandard(schema *standardTestSchema, p gql.Params) *gql.Result {
	doc, issues := parseAndValidateQuery(schema.parsed, p.RequestString)
	if len(issues) > 0 {
		return inputError(fmt.Errorf("%v", issues))
	}
	op, err := operationFor(doc, p.OperationName)
	if err != nil {
		return inputError(err)
	}
	ctx := p.Context
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := executeRuntime(ctx, schema.runtime, schema.parsed, doc, op, p.VariableValues)
	if err != nil {
		return inputError(err)
	}
	return result
}

func TestStandardFragmentsDirectivesVariablesAndIntrospection(t *testing.T) {
	calls := 0
	schema := standardFixture(t, `schema { query: Root }
enum Order { ASC DESC }
input Options { limit: Int = 7 order: Order = DESC }
type Root { rows(options: Options): [Row!]! ignored: String }
type Row { id: ID! name: String! }`, func(p gql.ResolveParams) (any, error) {
		if p.Info.FieldName == "rows" {
			calls++
			input := p.Args["options"].(map[string]any)
			if input["limit"] != 7 || input["order"] != "DESC" {
				t.Errorf("coercion/defaults: %v", input)
			}
			return []any{map[string]any{"id": 42, "name": "Ada"}}, nil
		}
		if p.Info.FieldName == "ignored" {
			t.Error("skipped field executed")
		}
		return gql.DefaultResolveFn(p)
	})
	result := runStandard(schema, gql.Params{RequestString: `query Read($show: Boolean! = true, $o: Options = {}) {
  first: rows(options: $o) { ...Basic ... on Row { name @include(if:$show) } }
  first: rows(options: $o) { id }
  ignored @skip(if:true)
} fragment Basic on Row { id __typename }`, OperationName: "Read"})
	if len(result.Errors) > 0 {
		t.Fatal(result.Errors)
	}
	encoded, _ := json.Marshal(result.Data)
	if string(encoded) != `{"first":[{"__typename":"Row","id":"42","name":"Ada"}]}` || calls != 1 {
		t.Fatalf("data=%s calls=%d", encoded, calls)
	}
	result = runStandard(schema, gql.Params{RequestString: `{ __schema { queryType { name } } __type(name:"Options") { kind inputFields { name defaultValue } } }`})
	if len(result.Errors) > 0 || !strings.Contains(fmt.Sprint(result.Data), "Root") {
		t.Fatalf("introspection: %+v", result)
	}
	for _, q := range []string{`query($v: Int!) { rows(options:{limit:$v}) { id } }`, `{ rows(options:{limit:"bad"}) { id } }`, `{ rows(options:{unknown:1}) { id } }`, `{ rows(options:{order:WRONG}) { id } }`} {
		result = runStandard(schema, gql.Params{RequestString: q})
		if len(result.Errors) == 0 {
			t.Fatalf("accepted invalid query %s", q)
		}
	}
	if calls != 1 {
		t.Fatal("invalid input reached resolver")
	}
}

func TestStandardAbstractTypesAndNullPropagation(t *testing.T) {
	schema := standardFixture(t, `interface Node { id: ID! } type User implements Node { id: ID! name: String! }
union Result = User
type Query { node: Node result: Result good: String broken: User }`, func(p gql.ResolveParams) (any, error) {
		switch p.Info.FieldName {
		case "node", "result":
			return map[string]any{"__typename": "User", "id": 1, "name": "Ada"}, nil
		case "good":
			return "ok", nil
		case "broken":
			return map[string]any{"id": 2, "name": nil}, nil
		}
		return gql.DefaultResolveFn(p)
	})
	result := runStandard(schema, gql.Params{RequestString: `{ node { id ... on User { name } } result { __typename ... on User { name } } good broken { id name } }`})
	data := result.Data.(map[string]any)
	if data["good"] != "ok" || data["broken"] != nil || len(result.Errors) != 1 {
		t.Fatalf("completion: %+v", result)
	}
	if fmt.Sprint(result.Errors[0].Path) != "[broken name]" || len(result.Errors[0].Locations) == 0 {
		t.Fatalf("error: %+v", result.Errors)
	}
	if data["node"].(map[string]any)["id"] != "1" {
		t.Fatal("ID not serialized")
	}
}

type standardTables struct {
	sdk.PlatformClient
	sdk.AppContextClient
	mu     sync.Mutex
	calls  []string
	inputs []map[string]any
}

func (p *standardTables) CallAppResult(app, tool string, input map[string]any, out any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if app != "tables" {
		return fmt.Errorf("unexpected app %s", app)
	}
	p.calls = append(p.calls, tool)
	p.inputs = append(p.inputs, input)
	read := func(args map[string]any) (any, error) {
		switch args["table"] {
		case "customers":
			return map[string]any{"rows": []any{map[string]any{"id": 1}, map[string]any{"id": 2}}, "total": 2}, nil
		case "orders":
			where := args["where"].([]any)
			constraint := where[len(where)-1].(map[string]any)
			return map[string]any{"rows": []any{map[string]any{"id": fmt.Sprint(constraint["value"]), "customer_id": constraint["value"]}}, "total": 1}, nil
		case "failure":
			return nil, fmt.Errorf("read failed")
		default:
			return map[string]any{"rows": []any{map[string]any{"id": 3}}, "total": 99}, nil
		}
	}
	var value any
	if tool == "tables_batch" {
		results := map[string]any{}
		for _, op := range input["operations"].([]map[string]any) {
			v, err := read(op["args"].(map[string]any))
			entry := map[string]any{"status": "ok", "result": v}
			if err != nil {
				entry = map[string]any{"status": "error", "error": err.Error()}
			}
			results[op["id"].(string)] = entry
		}
		value = map[string]any{"results": results}
	} else {
		var err error
		value, err = read(input)
		if err != nil {
			return err
		}
	}
	encoded, _ := json.Marshal(value)
	return json.Unmarshal(encoded, out)
}

func (p *standardTables) CallAppContext(ctx context.Context, app, method string, params map[string]any) (json.RawMessage, error) {
	return nil, fmt.Errorf("unexpected raw call")
}
func (p *standardTables) CallAppResultContext(ctx context.Context, app, tool string, input map[string]any, out any) error {
	return p.CallAppResult(app, tool, input, out)
}

func standardApp(t *testing.T, sdl string, platform sdk.PlatformClient, bindings *executionBindings) (*App, *standardTestSchema, context.Context) {
	t.Helper()
	a := &App{ctx: tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))}
	schema := standardFixture(t, sdl, a.standardResolve)
	ctx := context.Background()
	state := &standardRequest{project: "p1", api: "default", bindings: bindings, loader: newResolverLoader(a, ctx, "p1")}
	return a, schema, context.WithValue(ctx, standardRequestKey{}, state)
}

func TestStandardTablesRelationshipsBatchingAndEnvelope(t *testing.T) {
	p := &standardTables{}
	bindings := &executionBindings{sources: map[int64]sourceRecord{
		1: {Kind: "tables", Config: map[string]any{"table": "customers"}},
		2: {Kind: "tables", Config: map[string]any{"table": "orders"}},
		3: {Kind: "tables", Config: map[string]any{"table": "other"}},
	}, resolvers: map[string]resolverRecord{
		"Query.customers": {SourceID: 1, Operation: "find"},
		"Customer.orders": {SourceID: 2, Operation: "find", Config: map[string]any{"relation": map[string]any{"parent_key": "id", "foreign_key": "customer_id"}}},
		"Query.page":      {SourceID: 3, Operation: "search"},
	}}
	_, schema, ctx := standardApp(t, `type Query { customers: [Customer!]! page: Page! }
type Customer { id: ID! orders: [Order!]! } type Order { id: ID! customer_id: ID! }
type Page { rows: [Order!]! total: Int! }`, p, bindings)
	result := runStandard(schema, gql.Params{Context: ctx, RequestString: `{ customers { id orders { id customer_id } } page { total rows { id } } }`})
	if len(result.Errors) > 0 {
		t.Fatal(result.Errors)
	}
	if fmt.Sprint(p.calls) != "[tables_batch tables_batch]" {
		t.Fatalf("N+1: %v", p.calls)
	}
	data := result.Data.(map[string]any)
	if data["page"].(map[string]any)["total"] != 99 {
		t.Fatalf("lost result envelope: %v", data)
	}
	for _, raw := range data["customers"].([]any) {
		row := raw.(map[string]any)
		order := row["orders"].([]any)[0].(map[string]any)
		if row["id"] != order["customer_id"] {
			t.Fatalf("relationship leak: %v", data)
		}
	}
}

func TestStandardLoaderDedupAndPartialErrors(t *testing.T) {
	p := &standardTables{}
	bindings := &executionBindings{sources: map[int64]sourceRecord{1: {Kind: "tables", Config: map[string]any{"table": "other"}}, 2: {Kind: "tables", Config: map[string]any{"table": "failure"}}}, resolvers: map[string]resolverRecord{
		"Query.good": {SourceID: 1, Operation: "find"}, "Query.bad": {SourceID: 2, Operation: "find"},
	}}
	_, schema, ctx := standardApp(t, `type Query { good: [Row!] bad: [Row!] } type Row { id: ID! }`, p, bindings)
	result := runStandard(schema, gql.Params{Context: ctx, RequestString: `{ good copy:good bad { id } }`})
	if len(result.Errors) == 0 {
		t.Fatal("invalid selection accepted")
	}
	result = runStandard(schema, gql.Params{Context: ctx, RequestString: `{ good { id } copy:good { id } bad { id } }`})
	if len(result.Errors) != 1 || fmt.Sprint(result.Errors[0].Path) != "[bad]" {
		t.Fatalf("errors: %+v", result.Errors)
	}
	data := result.Data.(map[string]any)
	if data["good"] == nil || data["copy"] == nil || data["bad"] != nil {
		t.Fatalf("partial data: %v", data)
	}
	if len(p.calls) != 1 || len(p.inputs[0]["operations"].([]map[string]any)) != 2 {
		t.Fatalf("dedup: %v", p.inputs)
	}
}

func TestRelationshipConstraintsCannotBeOverridden(t *testing.T) {
	config := map[string]any{"table": "orders", "parent": map[string]any{"id": 5}, "where": []any{map[string]any{"col": "active", "op": "eq", "value": true}}, "relation": map[string]any{"parent_key": "id", "foreign_key": "customer_id"}}
	input, err := mappedTablesInput(config, map[string]any{"table": "secret", "where": []any{map[string]any{"col": "customer_id", "op": "eq", "value": 8}}})
	if err != nil {
		t.Fatal(err)
	}
	where := input["where"].([]any)
	if input["table"] != "orders" || len(where) != 3 || where[2].(map[string]any)["value"] != 5 {
		t.Fatalf("unsafe mapping: %v", input)
	}
	delete(config, "parent")
	if _, err = mappedTablesInput(config, nil); err == nil {
		t.Fatal("missing parent must fail closed")
	}
	if validateTableRelation("get", config) == nil {
		t.Fatal("unfiltered get must reject relation")
	}
}

func TestStandardNullOmissionDefaultsAndStrictVariables(t *testing.T) {
	var seen map[string]any
	schema := standardFixture(t, `scalar JSON
enum Direction { ASC DESC }
input Nested { n: Int = 9 text: String = "default" }
type Query { echo(n: Int = 5, input: Nested, ids: [ID!], direction: Direction): JSON }`, func(p gql.ResolveParams) (any, error) { seen = p.Args; return p.Args, nil })
	for _, test := range []struct {
		query string
		vars  map[string]any
		want  string
	}{
		{`{ echo(n:null,input:{text:null}) }`, nil, `{"input":{"n":9,"text":null},"n":null}`},
		{`query($n:Int) { echo(n:$n,input:{n:$n}) }`, nil, `{"input":{"n":9,"text":"default"},"n":5}`},
		{`query($n:Int=8) { echo(n:$n) }`, map[string]any{"n": nil}, `{"n":null}`},
		{`{ echo(ids:12) }`, nil, `{"ids":["12"],"n":5}`},
		{`query($n:Int! = 8) { echo(n:$n) }`, nil, `{"n":8}`},
	} {
		r := runStandard(schema, gql.Params{RequestString: test.query, VariableValues: test.vars})
		if len(r.Errors) > 0 {
			t.Fatal(r.Errors)
		}
		data, _ := json.Marshal(seen)
		if string(data) != test.want {
			t.Fatalf("%s: %s != %s", test.query, data, test.want)
		}
	}
	for _, value := range []any{1.5, "1", true, float64(2147483648)} {
		r := runStandard(schema, gql.Params{RequestString: `query($n:Int){echo(n:$n)}`, VariableValues: map[string]any{"n": value}})
		if len(r.Errors) == 0 {
			t.Fatalf("Int accepted %v", value)
		}
	}
	r := runStandard(schema, gql.Params{RequestString: `query($d:Direction){echo(direction:$d)}`, VariableValues: map[string]any{"d": "asc"}})
	if len(r.Errors) == 0 {
		t.Fatal("enum must be case sensitive")
	}
}

func TestStandardMutationOrderAndRootNull(t *testing.T) {
	order := []string{}
	schema := standardFixture(t, `type Query { ok: String broken: String! } type Mutation { first: String second: String }`, func(p gql.ResolveParams) (any, error) {
		if p.Info.FieldName == "broken" {
			return nil, fmt.Errorf("broken")
		}
		order = append(order, p.Info.FieldName)
		return "ok", nil
	})
	r := runStandard(schema, gql.Params{RequestString: `mutation { first second }`})
	if len(r.Errors) > 0 || fmt.Sprint(order) != "[first second]" {
		t.Fatalf("mutation ordering: %v %+v", order, r)
	}
	r = runStandard(schema, gql.Params{RequestString: `{ ok broken }`})
	if r.Data != nil || len(r.Errors) != 1 || fmt.Sprint(r.Errors[0].Path) != "[broken]" {
		t.Fatalf("root non-null: %+v", r)
	}
}

func TestStandardFragmentCost(t *testing.T) {
	schema, _ := validateSDL(`type Query { a: Row } type Row { b: Row id: ID }`)
	doc, issues := parseAndValidateQuery(schema, `{ ...Q } fragment Q on Query { a { ...R } } fragment R on Row { b { id } }`)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	n, d := queryCost(doc.Operations[0].SelectionSet, 0)
	if n != 3 || d != 3 {
		t.Fatalf("fragments evade limits: %d %d", n, d)
	}
}

func TestStandardConcurrentAbstractExecution(t *testing.T) {
	schema := standardFixture(t, `interface Node { id: ID! } type User implements Node { id: ID! name: String } union Result = User type Query { node: Node result: Result }`, func(p gql.ResolveParams) (any, error) {
		if p.Info.FieldName == "node" || p.Info.FieldName == "result" {
			return map[string]any{"__typename": "User", "id": 1, "name": "Ada"}, nil
		}
		return gql.DefaultResolveFn(p)
	})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				r := runStandard(schema, gql.Params{RequestString: `{ node { ... on User { id name } } result { ... on User { name } } }`})
				if len(r.Errors) > 0 {
					t.Error(r.Errors)
				}
			}
		}()
	}
	wg.Wait()
}

func TestStandardSelectedOperationDoesNotCoerceUnrelatedFragments(t *testing.T) {
	schema := standardFixture(t, `type Query { value(n: Int!): Int }`, func(p gql.ResolveParams) (any, error) { return p.Args["n"], nil })
	r := runStandard(schema, gql.Params{RequestString: `query One { value(n:1) } query Two($n:Int!) { ...Other } fragment Other on Query { value(n:$n) }`, OperationName: "One"})
	if len(r.Errors) > 0 || r.Data.(map[string]any)["value"] != 1 {
		t.Fatalf("unselected operation affected execution: %+v", r)
	}
}

func TestStandardDeferredErrorBubblesOnlyToNullableParent(t *testing.T) {
	p := &standardTables{}
	bindings := &executionBindings{sources: map[int64]sourceRecord{1: {Kind: "tables", Config: map[string]any{"table": "other"}}, 2: {Kind: "tables", Config: map[string]any{"table": "failure"}}}, resolvers: map[string]resolverRecord{
		"Query.parent": {SourceID: 1, Operation: "search"}, "Query.good": {SourceID: 1, Operation: "find"}, "Parent.bad": {SourceID: 2, Operation: "find"},
	}}
	_, schema, ctx := standardApp(t, `type Query { parent: Parent good: [Row!] } type Parent { bad: [Row!]! } type Row { id: ID! }`, p, bindings)
	r := runStandard(schema, gql.Params{Context: ctx, RequestString: `{ good { id } parent { bad { id } } }`})
	data, ok := r.Data.(map[string]any)
	if !ok || data["good"] == nil || data["parent"] != nil || len(r.Errors) != 1 {
		t.Fatalf("deferred null propagation: %+v", r)
	}
	if len(p.calls) != 2 {
		t.Fatalf("completion repeated source calls: %v", p.calls)
	}
}

func TestStandardErrorCompletionDoesNotRepeatHTTPResolver(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"Ada"}`))
	}))
	defer server.Close()
	bindings := &executionBindings{sources: map[int64]sourceRecord{1: {Kind: "http", Config: map[string]any{"url": server.URL}}}, resolvers: map[string]resolverRecord{"Query.parent": {SourceID: 1, Operation: "request"}}}
	a, schema, ctx := standardApp(t, `type Query { parent: Parent } type Parent { required: String! }`, &standardTables{}, bindings)
	a.httpClient = server.Client()
	r := runStandard(schema, gql.Params{Context: ctx, RequestString: `{ parent { required } }`})
	if len(r.Errors) != 1 || r.Data.(map[string]any)["parent"] != nil || calls.Load() != 1 {
		t.Fatalf("error completion repeated HTTP call: %d %+v", calls.Load(), r)
	}
}
