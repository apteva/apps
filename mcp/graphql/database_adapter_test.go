package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	gql "github.com/graphql-go/graphql"
)

type databasePlatform struct {
	sdk.PlatformClient
	sdk.AppContextClient
	mu    sync.Mutex
	calls map[string]map[string]any
}

func (p *databasePlatform) CallAppResultContext(_ context.Context, app, tool string, input map[string]any, out any) error {
	return p.CallAppResult(app, tool, input, out)
}

func (p *databasePlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if app != "database" {
		return fmt.Errorf("unexpected adapter %s.%s", app, tool)
	}
	p.calls[tool] = input
	var result any
	switch tool {
	case "db_find":
		result = map[string]any{"records": []any{map[string]any{"id": "1", "amount": 5}}, "hasMore": true, "nextCursor": "c2"}
	case "db_insert", "db_update", "db_delete", "db_upsert":
		result = map[string]any{"affected": 1}
	default:
		return fmt.Errorf("unexpected database tool %s", tool)
	}
	raw, _ := json.Marshal(result)
	return json.Unmarshal(raw, out)
}

const databaseSDL = `input Filter { field: String! op: String! value: String }
input OrderInput { id: ID! amount: Int! }
input OrderKey { id: ID! }
input OrderPatch { amount: Int }
type Order { id: ID! amount: Int! }
type OrderPage { rows: [Order!]! has_more: Boolean! next_cursor: String }
type WriteResult { affected: Int! }
type Query {
 orders(where: Filter, limit: Int, all: Boolean, requireIndex: Boolean): [Order!]!
 orderPage(cursor: String): OrderPage!
}
type Mutation {
 createOrders(records: [OrderInput!]!): WriteResult!
 patchOrder(key: OrderKey!, set: OrderPatch!, ifVersion: Int, maxAffected: Int): WriteResult!
 upsertOrders(records: [OrderInput!]!, conflictIndex: String): WriteResult!
}`

func databaseBindings() *executionBindings {
	return &executionBindings{sources: map[int64]sourceRecord{
		1: {Kind: "database", Config: map[string]any{"database": "prod", "collection": "orders"}},
	}, resolvers: map[string]resolverRecord{
		"Query.orders": {SourceID: 1, Operation: "find", Config: map[string]any{
			"where": map[string]any{"field": "tenant", "op": "eq", "value": "t1"}, "limit": 50, "requireIndex": true,
		}},
		"Query.orderPage":       {SourceID: 1, Operation: "search"},
		"Mutation.createOrders": {SourceID: 1, Operation: "insert"},
		"Mutation.patchOrder":   {SourceID: 1, Operation: "update", Config: map[string]any{"maxAffected": 5}},
		"Mutation.upsertOrders": {SourceID: 1, Operation: "upsert", Config: map[string]any{"conflictIndex": "orders_id"}},
	}}
}

func runDatabaseQuery(t *testing.T, query string) (*databasePlatform, map[string]any) {
	t.Helper()
	p := &databasePlatform{calls: map[string]map[string]any{}}
	_, schema, ctx := standardApp(t, databaseSDL, p, databaseBindings())
	result := runStandard(schema, gql.Params{Context: ctx, RequestString: query})
	if len(result.Errors) > 0 {
		t.Fatal(result.Errors)
	}
	return p, result.Data.(map[string]any)
}

func TestDatabaseReadsKeepServerConstraints(t *testing.T) {
	p, _ := runDatabaseQuery(t, `{ orders(where:{field:"status",op:"eq",value:"open"}, limit:500, all:true, requireIndex:false) { id } }`)
	input := p.calls["db_find"]
	want := map[string]any{"and": []any{
		map[string]any{"field": "tenant", "op": "eq", "value": "t1"},
		map[string]any{"field": "status", "op": "eq", "value": "open"},
	}}
	if !reflect.DeepEqual(input["where"], want) {
		t.Fatalf("server where not combined with client where: %#v", input["where"])
	}
	if intValue(input["limit"]) != 50 || input["requireIndex"] != true {
		t.Fatalf("client raised server bounds: %v", input)
	}
	if _, ok := input["all"]; ok {
		t.Fatalf("client set all: %v", input)
	}
	if input["database"] != "prod" || input["collection"] != "orders" {
		t.Fatalf("source scope overridden: %v", input)
	}

	p, _ = runDatabaseQuery(t, `{ orders(limit:10) { id } }`)
	if intValue(p.calls["db_find"]["limit"]) != 10 {
		t.Fatalf("client could not lower limit: %v", p.calls["db_find"])
	}
}

func TestDatabaseSearchReturnsCursor(t *testing.T) {
	p, data := runDatabaseQuery(t, `{ orderPage(cursor:"c1") { has_more next_cursor rows { id } } }`)
	if p.calls["db_find"]["cursor"] != "c1" {
		t.Fatalf("cursor not forwarded: %v", p.calls["db_find"])
	}
	body, _ := json.Marshal(data)
	if string(body) != `{"orderPage":{"has_more":true,"next_cursor":"c2","rows":[{"id":"1"}]}}` {
		t.Fatalf("page: %s", body)
	}
}

func TestDatabaseWritesForwardRecordsAndPatches(t *testing.T) {
	p, _ := runDatabaseQuery(t, `mutation { createOrders(records:[{id:"1",amount:5}]) { affected } }`)
	if fmt.Sprint(p.calls["db_insert"]["records"]) != "[map[amount:5 id:1]]" {
		t.Fatalf("records lost: %v", p.calls["db_insert"])
	}

	p, _ = runDatabaseQuery(t, `mutation { patchOrder(key:{id:"1"}, set:{amount:7}, ifVersion:3, maxAffected:1000) { affected } }`)
	input := p.calls["db_update"]
	if fmt.Sprint(input["key"]) != "map[id:1]" || fmt.Sprint(input["set"]) != "map[amount:7]" || intValue(input["ifVersion"]) != 3 {
		t.Fatalf("update inputs lost: %v", input)
	}
	if intValue(input["maxAffected"]) != 5 {
		t.Fatalf("client raised maxAffected: %v", input)
	}

	p, _ = runDatabaseQuery(t, `mutation { upsertOrders(records:[{id:"1",amount:5}], conflictIndex:"other") { affected } }`)
	if p.calls["db_upsert"]["conflictIndex"] != "orders_id" || p.calls["db_upsert"]["records"] == nil {
		t.Fatalf("upsert inputs: %v", p.calls["db_upsert"])
	}
}

func TestDatabaseResolverRejectsSchemaAndDropTools(t *testing.T) {
	db := testDB(t)
	source, err := createSource(db, "p1", "orders", "database", map[string]any{"database": "prod", "collection": "orders"})
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"database_drop", "collection_drop", "index_create", "batch", "explain"} {
		if _, err := upsertResolver(db, "p1", "Mutation", "danger", operation, source.ID, nil); err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("%s accepted: %v", operation, err)
		}
	}
	for _, operation := range []string{"find", "Search", "get", "count", "aggregate", "insert", "update", "delete", "upsert"} {
		if _, err := upsertResolver(db, "p1", "Query", "ok_"+strings.ToLower(operation), operation, source.ID, nil); err != nil {
			t.Fatalf("%s rejected: %v", operation, err)
		}
	}
	// Resolvers stored before this check still fail closed at execution.
	if _, err := (&App{}).callDatabase(context.Background(), "collection_drop", map[string]any{"_project_id": "p1"}); err == nil {
		t.Fatal("stored drop resolver executed")
	}
}
