package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/vektah/gqlparser/v2/ast"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type performancePlatform struct {
	sdk.PlatformClient
	calls  int
	input  map[string]any
	result map[string]any
}

func (p *performancePlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	if app != "tables" || tool != "tables_batch" {
		return fmt.Errorf("unexpected dispatch %s.%s", app, tool)
	}
	p.calls++
	p.input = input
	raw, err := json.Marshal(p.result)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func TestAdaptiveTablesBatchAndProjection(t *testing.T) {
	platform := &performancePlatform{result: map[string]any{"results": map[string]any{
		"op0": map[string]any{"status": "ok", "result": map[string]any{"rows": []any{map[string]any{"id": 1, "hidden": "secret"}}}},
		"op1": map[string]any{"status": "ok", "result": map[string]any{"rows": []any{map[string]any{"id": 2}}}},
	}}}
	app := &App{ctx: tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))}
	schema, _ := validateSDL("type Query { a(limit: Int): [Row!]! b(limit: Int): [Row!]! } type Row { id: Int! }")
	doc, errs := parseAndValidateQuery(schema, "query($n: Int) { first: a(limit: $n) { key: id } b(limit: $n) { id } }")
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	bindings := &executionBindings{
		resolvers: map[string]resolverRecord{"Query.a": {SourceID: 1, Operation: "find"}, "Query.b": {SourceID: 2, Operation: "find"}},
		sources:   map[int64]sourceRecord{1: {Kind: "tables", Config: map[string]any{"table": "a", "orderBy": "id asc"}}, 2: {Kind: "tables", Config: map[string]any{"table": "b"}}},
	}
	ctx := context.WithValue(context.Background(), executionBindingsKey{}, bindings)
	data, remaining, used, err := app.executeBatchedTableRoots(ctx, "p1", "default", doc.Operations[0].SelectionSet, map[string]any{"n": 10})
	if err != nil || !used || len(remaining) != 0 || platform.calls != 1 {
		t.Fatalf("batch: %v %v %v", data, used, err)
	}
	if platform.input["_project_id"] != "p1" {
		t.Fatal("missing project")
	}
	row := data["first"].([]any)[0].(map[string]any)
	if len(row) != 1 || row["key"] != float64(1) {
		t.Fatalf("projection: %v", row)
	}
	op := platform.input["operations"].([]map[string]any)[0]["args"].(map[string]any)
	if op["order_by"] != "id asc" || op["include_total"] != false {
		t.Fatalf("input: %v", op)
	}
	_, _, used, err = app.executeBatchedTableRoots(ctx, "p1", "default", doc.Operations[0].SelectionSet, map[string]any{"n": 1000})
	if err != nil || used || platform.calls != 1 {
		t.Fatal("large reads must fall back to parallel calls")
	}
	platform.result = map[string]any{"results": map[string]any{"op0": map[string]any{"status": "error", "error": map[string]any{"message": "table missing"}}}}
	_, _, _, err = app.executeBatchedTableRoots(ctx, "p1", "default", doc.Operations[0].SelectionSet, map[string]any{"n": 10})
	if err == nil || err.Error() != "tables batch operation first: table missing" {
		t.Fatalf("error detail: %v", err)
	}
}

func TestCompiledCachesReuseValidatedDocuments(t *testing.T) {
	a := &App{}
	schema, errs := a.compiledSchema("schema1", "type Query { n: Int }")
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	same, _ := a.compiledSchema("schema1", "unused")
	if same != schema {
		t.Fatal("schema not reused")
	}
	doc, errs := a.parsedQuery("query1", "{ n }", schema)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	again, _ := a.parsedQuery("query1", "{ n }", schema)
	if again != doc {
		t.Fatal("document not reused")
	}
	if _, errs = a.parsedQuery("bad", "{ missing }", schema); len(errs) == 0 {
		t.Fatal("invalid query accepted")
	}
	if a.queryCache["bad"] != nil {
		t.Fatal("invalid query cached")
	}
}

func TestProjectionNeedsNoDatabase(t *testing.T) {
	schema, _ := validateSDL("type Query { rows: [Row!]! } type Row { id: Int! name: String! }")
	doc, errs := parseAndValidateQuery(schema, "{ rows { key: id name } }")
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	ctx := context.WithValue(context.Background(), executionBindingsKey{}, &executionBindings{resolvers: map[string]resolverRecord{}, sources: map[int64]sourceRecord{}})
	rows := make([]any, 3000)
	for i := range rows {
		rows[i] = map[string]any{"id": i, "name": "example"}
	}
	// A nil AppCtx would panic if scalar projection accessed the database.
	result, err := (&App{}).executeSelection(ctx, "p", "default", "Row", rows, doc.Operations[0].SelectionSet[0].(*ast.Field).SelectionSet, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range result.([]any) {
		value := row.(map[string]any)
		if value["key"] != i || value["name"] != "example" {
			t.Fatalf("row %d: %v", i, value)
		}
	}
}

func TestQueryParallelAndMutationSerial(t *testing.T) {
	for _, root := range []string{"Query", "Mutation"} {
		t.Run(root, func(t *testing.T) {
			var active, peak atomic.Int32
			arrived := make(chan struct{}, 3)
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := active.Add(1)
				defer active.Add(-1)
				for old := peak.Load(); n > old; old = peak.Load() {
					if peak.CompareAndSwap(old, n) {
						break
					}
				}
				if root == "Query" {
					arrived <- struct{}{}
					<-release
				}
				fmt.Fprint(w, "7")
			}))
			defer server.Close()
			schema, _ := validateSDL("type Query { a: Int b: Int c: Int } type Mutation { a: Int b: Int c: Int }")
			query := "{ a b c }"
			if root == "Mutation" {
				query = "mutation { a b c }"
			}
			doc, errs := parseAndValidateQuery(schema, query)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			bindings := &executionBindings{resolvers: map[string]resolverRecord{}, sources: map[int64]sourceRecord{1: {ID: 1, Kind: "http", Config: map[string]any{"url": server.URL}}}}
			for _, f := range []string{"a", "b", "c"} {
				bindings.resolvers[root+"."+f] = resolverRecord{SourceID: 1}
			}
			ctx := context.WithValue(context.Background(), executionBindingsKey{}, bindings)
			done := make(chan error, 1)
			go func() {
				_, err := (&App{httpClient: server.Client()}).executeSelection(ctx, "p", "default", root, nil, doc.Operations[0].SelectionSet, nil)
				done <- err
			}()
			if root == "Query" {
				for i := 0; i < 3; i++ {
					select {
					case <-arrived:
					case <-time.After(3 * time.Second):
						close(release)
						t.Fatal("query fields did not overlap")
					}
				}
				close(release)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			want := int32(1)
			if root == "Query" {
				want = 3
			}
			if peak.Load() != want {
				t.Fatalf("concurrency=%d want %d", peak.Load(), want)
			}
		})
	}
}
