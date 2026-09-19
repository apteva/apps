package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	gql "github.com/graphql-go/graphql"
)

func fastTestBindings() *executionBindings {
	return &executionBindings{sources: map[int64]sourceRecord{1: {Kind: "tables", Config: map[string]any{"table": "customers"}}, 2: {Kind: "tables", Config: map[string]any{"table": "orders"}}}, resolvers: map[string]resolverRecord{
		"Query.customers": {SourceID: 1, Operation: "find"},
		"Customer.orders": {SourceID: 2, Operation: "search", Config: map[string]any{"relation": map[string]any{"parent_key": "id", "foreign_key": "customer_id"}}},
	}}
}

const fastTestSDL = `interface Node { id: ID! }
type Query { customers(limit: Int = 2): [Customer!]! }
type Customer implements Node { id: ID! orders(limit: Int = 3): OrderPage! }
type OrderPage { rows: [Order!]! total: Int! }
type Order { id: ID! customer_id: ID! optional: String }
`

func TestFastProjectionDifferential(t *testing.T) {
	queries := []string{
		`{ customers { id __typename } }`,
		`{ first:customers { key:id } copy:customers { id } }`,
		`{ customers { ...C orders { total } } customers { orders { rows { id customer_id optional } } } } fragment C on Customer { id orders { rows { id } } }`,
		`query($show:Boolean! = true,$n:Int=2) { customers(limit:$n) { ... on Node { id } orders @include(if:$show) { total rows { id optional } } } }`,
		`query($show:Boolean! = false) { customers { id orders @include(if:$show) { rows { id } total } } }`,
		`{ __typename customers @skip(if:true) { id } }`,
		`{ customers { ...C ...C } } fragment C on Customer { id orders { rows { __typename id } total } }`,
	}
	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			var results [2][]byte
			for i := 0; i < 2; i++ {
				platform := &standardTables{}
				_, schema, ctx := standardApp(t, fastTestSDL, platform, fastTestBindings())
				if i == 1 {
					ctx = context.WithValue(ctx, disableFastProjectionKey{}, true)
				}
				result := runStandard(schema, gql.Params{Context: ctx, RequestString: query})
				if len(result.Errors) > 0 {
					t.Fatal(result.Errors)
				}
				results[i], _ = json.Marshal(result)
				used := ctx.Value(standardRequestKey{}).(*standardRequest).fastProjectionUsed
				if used != (i == 0) {
					t.Fatalf("unexpected fast path: %v", used)
				}
			}
			if string(results[0]) != string(results[1]) {
				t.Fatalf("fast=%s normal=%s", results[0], results[1])
			}
		})
	}
}

func TestFastProjectionFallbackPreservesErrorsAndReads(t *testing.T) {
	bindings := fastTestBindings()
	bindings.sources[2] = sourceRecord{Kind: "tables", Config: map[string]any{"table": "failure"}}
	for _, sdl := range []string{fastTestSDL, `type Query { customers: [Customer] } type Customer { id: ID! missing: String! }`} {
		query := `{ customers { id orders { rows { id } total } } }`
		if sdl != fastTestSDL {
			query = `{ customers { id missing } }`
		}
		var bodies [2][]byte
		var calls [2]int
		for i := 0; i < 2; i++ {
			platform := &standardTables{}
			_, schema, ctx := standardApp(t, sdl, platform, bindings)
			if i == 1 {
				ctx = context.WithValue(ctx, disableFastProjectionKey{}, true)
			}
			result := runStandard(schema, gql.Params{Context: ctx, RequestString: query})
			if len(result.Errors) == 0 {
				t.Fatal("expected completion error")
			}
			bodies[i], _ = json.Marshal(result)
			calls[i] = len(platform.calls)
			if ctx.Value(standardRequestKey{}).(*standardRequest).fastProjectionUsed {
				t.Fatal("error path must use normal completion")
			}
		}
		if string(bodies[0]) != string(bodies[1]) || calls[0] != calls[1] {
			t.Fatalf("fallback mismatch:\n%s\n%s\ncalls=%v", bodies[0], bodies[1], calls)
		}
	}
}

func TestFastProjectionPermissionChecks(t *testing.T) {
	platform := &standardTables{}
	_, schema, ctx := standardApp(t, fastTestSDL, platform, fastTestBindings())
	state := ctx.Value(standardRequestKey{}).(*standardRequest)
	state.policy.Fields = map[string][]string{"Customer.id": {"private:read"}}
	result := runStandard(schema, gql.Params{Context: ctx, RequestString: `{ customers { id } }`})
	if len(result.Errors) == 0 || result.Data != nil || state.fastProjectionUsed {
		t.Fatalf("permission bypass: %+v", result)
	}
	// The HTTP execution preflight denies before reads. This lower-level test
	// checks that the projection itself never bypasses scalar-field permission.
}

func TestFastProjectionSkipsIntrospectionAndCustomScalars(t *testing.T) {
	for _, query := range []string{`{ __schema { queryType { name } } }`, `{ customers }`} {
		platform := &standardTables{}
		_, schema, ctx := standardApp(t, `scalar JSON type Query { customers: JSON }`, platform, fastTestBindings())
		result := runStandard(schema, gql.Params{Context: ctx, RequestString: query})
		if len(result.Errors) > 0 || ctx.Value(standardRequestKey{}).(*standardRequest).fastProjectionUsed {
			t.Fatalf("unsupported projection did not fall back: %+v", result)
		}
	}
}

// Preloaded JSON-shaped source rows isolate executor/projection overhead.
// Real-process latency measurements live in fast_projection_sidecar_test.go.
type projectionPlatform struct {
	sdk.PlatformClient
	sdk.AppContextClient
	rows []any
}

func (p *projectionPlatform) CallAppResultContext(ctx context.Context, app, tool string, input map[string]any, out any) error {
	if app != "tables" || tool != "rows_search" {
		return fmt.Errorf("unexpected call %s.%s", app, tool)
	}
	*out.(*any) = map[string]any{"rows": p.rows}
	return nil
}

func BenchmarkThreeTableProjection(b *testing.B) {
	rows := make([]any, 1000)
	for i := range rows {
		rows[i] = map[string]any{"id": float64(i + 1), "name": "sample", "amount": 12.5, "active": true, "category": "demo"}
	}
	platform := &projectionPlatform{rows: rows}
	manifest := (&App{}).Manifest()
	a := &App{ctx: sdk.NewAppCtxForTest(&manifest, nil, nil, platform, nil)}
	schema, issues := validateSDL(`type Query { a(limit:Int): [Row!]! b(limit:Int): [Row!]! c(limit:Int): [Row!]! } type Row { id: ID! name:String! amount:Float! active:Boolean! category:String! }`)
	if len(issues) > 0 {
		b.Fatal(issues)
	}
	runtime, err := buildStandardSchema(schema, a.standardResolve)
	if err != nil {
		b.Fatal(err)
	}
	doc, issues := parseAndValidateQuery(schema, `{ a(limit:1000) { ...R } b(limit:1000) { ...R } c(limit:1000) { ...R } } fragment R on Row { id name amount active category }`)
	if len(issues) > 0 {
		b.Fatal(issues)
	}
	bindings := &executionBindings{sources: map[int64]sourceRecord{}, resolvers: map[string]resolverRecord{}}
	for i, name := range []string{"a", "b", "c"} {
		id := int64(i + 1)
		bindings.sources[id] = sourceRecord{Kind: "tables", Config: map[string]any{"table": name}}
		bindings.resolvers["Query."+name] = resolverRecord{SourceID: id, Operation: "find"}
	}
	for _, fast := range []bool{false, true} {
		b.Run(fmt.Sprintf("fast=%v", fast), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ctx := context.WithValue(context.Background(), disableFastProjectionKey{}, !fast)
				state := &standardRequest{project: "p", api: "default", bindings: bindings, loader: newResolverLoader(a, ctx, "p")}
				ctx = context.WithValue(ctx, standardRequestKey{}, state)
				result, err := executeRuntime(ctx, runtime, schema, doc, doc.Operations[0], nil)
				if err != nil || len(result.Errors) > 0 || state.fastProjectionUsed != fast {
					b.Fatalf("execution failed: %+v %v", result, err)
				}
			}
		})
	}
}

func TestFastProjectionConcurrentRequests(t *testing.T) {
	a, schema, _ := standardApp(t, fastTestSDL, &standardTables{}, fastTestBindings())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			state := &standardRequest{project: "p", bindings: fastTestBindings(), loader: newResolverLoader(a, ctx, "p")}
			ctx = context.WithValue(ctx, standardRequestKey{}, state)
			r := runStandard(schema, gql.Params{Context: ctx, RequestString: `{ customers { id orders { total rows { id customer_id } } } }`})
			if len(r.Errors) > 0 || !state.fastProjectionUsed {
				t.Errorf("concurrent projection: %+v", r)
			}
		}()
	}
	wg.Wait()
}

type parallelProjectionPlatform struct {
	projectionPlatform
	started atomic.Int32
	ready   chan struct{}
}

func (p *parallelProjectionPlatform) CallAppResultContext(ctx context.Context, app, tool string, input map[string]any, out any) error {
	if p.started.Add(1) == 3 {
		close(p.ready)
	}
	select {
	case <-p.ready:
		return p.projectionPlatform.CallAppResultContext(ctx, app, tool, input, out)
	case <-ctx.Done():
		return fmt.Errorf("large source reads failed to start in parallel: %w", ctx.Err())
	}
}

func TestLiveExecutorsKeepLargeTablesReadsParallel(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		p := &parallelProjectionPlatform{projectionPlatform: projectionPlatform{rows: []any{map[string]any{"id": 1}}}, ready: make(chan struct{})}
		bindings := &executionBindings{sources: map[int64]sourceRecord{}, resolvers: map[string]resolverRecord{}}
		for i, name := range []string{"a", "b", "c"} {
			id := int64(i + 1)
			bindings.sources[id] = sourceRecord{Kind: "tables", Config: map[string]any{"table": name}}
			bindings.resolvers["Query."+name] = resolverRecord{SourceID: id, Operation: "find"}
		}
		a, schema, _ := standardApp(t, `type Query { a(limit:Int):[Row!]! b(limit:Int):[Row!]! c(limit:Int):[Row!]! } type Row { id:ID! }`, p, bindings)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		state := &standardRequest{project: "p1", bindings: bindings, loader: newResolverLoader(a, ctx, "p1")}
		ctx = context.WithValue(ctx, standardRequestKey{}, state)
		ctx = context.WithValue(ctx, disableFastProjectionKey{}, disabled)
		result := runStandard(schema, gql.Params{Context: ctx, RequestString: `{ a(limit:1000){id} b(limit:1000){id} c(limit:1000){id} }`})
		cancel()
		if len(result.Errors) > 0 || p.started.Load() != 3 || state.fastProjectionUsed == disabled {
			t.Fatalf("parallel execution (fast=%v): %+v", !disabled, result)
		}
	}
}
