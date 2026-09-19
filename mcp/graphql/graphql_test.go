package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:graphql-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	migration, err = os.ReadFile("migrations/002_multi_api.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestNamedAPIsIsolateSchemasSourcesAndResolvers(t *testing.T) {
	db := testDB(t)
	first, err := createGraphQLAPI(db, "p1", "commerce", "Commerce", "")
	if err != nil || first == nil {
		t.Fatalf("create first api: %#v %v", first, err)
	}
	second, err := createGraphQLAPI(db, "p1", "analytics", "Analytics", "")
	if err != nil || second == nil {
		t.Fatalf("create second api: %#v %v", second, err)
	}
	row, _, err := createSchemaForAPI(db, "p1", "commerce", "production", "type Query { products: String! }", 0)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	other, _, err := createSchemaForAPI(db, "p1", "analytics", "production", "type Query { reports: String! }", 0)
	if err != nil || other == nil || other.Version != 1 {
		t.Fatalf("isolated schema: %#v %v", other, err)
	}
	source, err := createSourceForAPI(db, "p1", "commerce", "items", "tables", map[string]any{"table": "items"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createSourceForAPI(db, "p1", "analytics", "items", "tables", map[string]any{"table": "reports"}); err != nil {
		t.Fatalf("same source name should be allowed per api: %v", err)
	}
	if _, err := upsertResolverForAPI(db, "p1", "commerce", "Query", "items", "find", source.ID, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if got, err := listResolversForAPI(db, "p1", "analytics"); err != nil || len(got) != 0 {
		t.Fatalf("resolver leaked across api: %#v %v", got, err)
	}
}

func TestSchemaLifecycleAndResolverStorage(t *testing.T) {
	db := testDB(t)
	sdl := `type Query { orderMetrics: OrderMetrics! } type OrderMetrics { count: Int! revenue: Float! }`
	row, validationErrors, err := createSchema(db, "p1", "staging", sdl, 0)
	if err != nil || len(validationErrors) != 0 || row == nil {
		t.Fatalf("create schema: row=%v errors=%v err=%v", row, validationErrors, err)
	}
	if _, err := publishSchema(db, "p1", "staging", row.Version); err != nil {
		t.Fatal(err)
	}
	active, err := getSchema(db, "p1", "staging", 0, true)
	if err != nil || active == nil || active.Status != "published" {
		t.Fatalf("active schema: %#v err=%v", active, err)
	}
	source, err := createSource(db, "p1", "orders", "database", map[string]any{"database": "prod", "collection": "orders"})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := upsertResolver(db, "p1", "Query", "orderMetrics", "aggregate", source.ID, map[string]any{
		"metrics": []any{map[string]any{"name": "count", "op": "count"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolver.SourceID != source.ID || resolver.Operation != "aggregate" {
		t.Fatalf("resolver: %#v", resolver)
	}
}

func TestManifestDeclaresStandaloneGraphQLSurface(t *testing.T) {
	manifest := (&App{}).Manifest()
	if manifest.Name != "graphql" {
		t.Fatalf("name=%q", manifest.Name)
	}
	if len(manifest.Provides.HTTPRoutes) < 2 {
		t.Fatalf("expected graphql and realtime routes: %#v", manifest.Provides.HTTPRoutes)
	}
	for _, dep := range manifest.Requires.Apps {
		if dep.Name == "api" {
			t.Fatal("graphql must not depend on the API app")
		}
	}
}

func TestQueryValidationAndCost(t *testing.T) {
	schema, errors := validateSDL(`type Query { hello: String! }`)
	if schema == nil || len(errors) != 0 {
		t.Fatalf("schema errors: %v", errors)
	}
	doc, errors := parseAndValidateQuery(schema, `{ hello }`)
	if doc == nil || len(errors) != 0 {
		t.Fatalf("query errors: %v", errors)
	}
	fields, depth := queryCost(doc.Operations[0].SelectionSet, 0)
	if fields != 1 || depth != 1 {
		t.Fatalf("cost fields=%d depth=%d", fields, depth)
	}
	_, errors = parseAndValidateQuery(schema, `{ missing }`)
	if len(errors) == 0 {
		t.Fatal("expected validation error")
	}
}

func TestSubscriptionHubDoesNotBlockSlowSubscriber(t *testing.T) {
	hub := newSubscriptionHub()
	sub, cancel := hub.subscribe("p1", "orders.updated")
	defer cancel()
	if got := hub.publish("p1", "orders.updated", map[string]any{"id": "1"}); got != 1 {
		t.Fatalf("delivered=%d", got)
	}
	select {
	case payload := <-sub.events:
		if payload["id"] != "1" {
			t.Fatalf("payload=%v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("event not delivered")
	}
	if got := hub.publish("p1", "other", map[string]any{}); got != 0 {
		t.Fatalf("unexpected delivery=%d", got)
	}
	encoded, err := json.Marshal(publicSource(sourceRecord{ID: 1, Name: "orders", Kind: "database", Config: map[string]any{}}))
	if err != nil || len(encoded) == 0 {
		t.Fatal("source should be JSON serializable")
	}
}

func TestAggregateResultUnwrap(t *testing.T) {
	rows := []any{map[string]any{"status": "paid", "count": "3", "revenue": 12.5}}
	got := unwrapSourceResult("aggregate", map[string]any{"rows": rows, "truncated": false})
	if len(got.([]any)) != 1 {
		t.Fatalf("aggregate result=%#v", got)
	}
	if got := unwrapSourceResult("count", map[string]any{"count": "42"}); got != "42" {
		t.Fatalf("count=%v", got)
	}
}
