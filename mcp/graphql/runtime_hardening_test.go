package main

import (
	"context"
	"testing"
	"time"
)

func TestAtomicReleaseSnapshotAndRollback(t *testing.T) {
	db := testDB(t)
	if _, err := createGraphQLAPI(db, "release-project", "commerce", "Commerce", ""); err != nil {
		t.Fatal(err)
	}
	schema1, problems, err := createSchemaForAPI(db, "release-project", "commerce", "production", `type Query { items: [Item!]! } type Item { id: ID! }`, 1)
	if err != nil || len(problems) > 0 {
		t.Fatalf("schema: %v %#v", err, problems)
	}
	source, err := createSourceForAPI(db, "release-project", "commerce", "items", "tables", map[string]any{"table": "items_v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upsertResolverForAPI(db, "release-project", "commerce", "Query", "items", "find", source.ID, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	first, err := publishAPIRelease(db, "release-project", "commerce", "production", schema1.Version, map[string]any{"max_rows": 123})
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != 1 || first.Limits.MaxRows != 123 || first.Sources[0].Config["table"] != "items_v1" {
		t.Fatalf("first=%#v", first)
	}
	if _, err := createSourceForAPI(db, "release-project", "commerce", "items", "tables", map[string]any{"table": "items_v2"}); err != nil {
		t.Fatal(err)
	}
	active, err := getAPIRelease(db, "release-project", "commerce", "production", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if active.Sources[0].Config["table"] != "items_v1" {
		t.Fatal("draft source leaked into active release")
	}
	second, err := publishAPIRelease(db, "release-project", "commerce", "production", schema1.Version, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Version != 2 || second.Sources[0].Config["table"] != "items_v2" {
		t.Fatalf("second=%#v", second)
	}
	rolled, err := rollbackAPIRelease(db, "release-project", "commerce", "production", 1)
	if err != nil {
		t.Fatal(err)
	}
	if rolled.Version != 1 || rolled.Status != "published" || rolled.Sources[0].Config["table"] != "items_v1" {
		t.Fatalf("rolled=%#v", rolled)
	}
}

func TestCardinalityCostAndDeadlineLimits(t *testing.T) {
	schema, problems := validateSDL(`type Query { customers(first: Int): [Customer!]! } type Customer { orders(limit: Int): [Order!]! } type Order { id: ID! }`)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	doc, problems := parseAndValidateQuery(schema, `{ customers(first: 5) { orders(limit: 4) { id } } }`)
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	cost, rows, resolvers := cardinalityCost(doc.Operations[0].SelectionSet, nil, 100)
	if rows != 25 || cost != 26 || resolvers != 26 {
		t.Fatalf("cost=%d rows=%d resolvers=%d", cost, rows, resolvers)
	}
	parent := time.Now().Add(time.Second)
	if deadline := releaseDeadline(parent, releaseLimits{MaxExecutionMS: 100}); deadline.After(parent) || time.Until(deadline) > 150*time.Millisecond {
		t.Fatalf("deadline=%v", deadline)
	}
	ctx, cancel := context.WithDeadline(context.Background(), parent)
	defer cancel()
	if deadlineFromContext(ctx).IsZero() {
		t.Fatal("context deadline not read")
	}
}

func TestExecutionDeadlineExceededAtClockBoundary(t *testing.T) {
	active, cancel := context.WithCancel(context.Background())
	defer cancel()
	if executionDeadlineExceeded(active, time.Now().Add(time.Second)) {
		t.Fatal("future execution deadline reported as exceeded")
	}
	if !executionDeadlineExceeded(active, time.Now().Add(-time.Nanosecond)) {
		t.Fatal("elapsed execution deadline was not reported as exceeded")
	}

	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	if !executionDeadlineExceeded(expired, time.Now().Add(time.Second)) {
		t.Fatal("context deadline was not reported as exceeded")
	}
}

func TestExecutionMetadataIsAvailableToEarlyFailures(t *testing.T) {
	result := executionMetadata(context.Background(), "telemetry-project", graphqlRequest{Query: "query Workspace { value }", OperationName: "Workspace"}, &apiRelease{Version: 7})
	if result.OperationName != "Workspace" || len(result.OperationHash) != 64 || result.Release != 7 || result.AuthScope != "platform:telemetry-project" {
		t.Fatalf("metadata=%#v", result)
	}
}

func TestHardenedModuleDecimalNullAndDate(t *testing.T) {
	decimal := resolverModule{Name: "price", Version: 1, Status: "published", OutputType: "Decimal", NullBehavior: "strict", DecimalPrecision: 8, DecimalScale: 2, RoundingMode: "half_even", Timezone: "UTC", Inputs: map[string]any{"value": "Decimal!"}, Definition: map[string]any{"op": "multiply", "args": []any{map[string]any{"input": "value"}, map[string]any{"const": "1.005"}}}}
	value, err := (moduleRuntime{}).evaluate(decimal, map[string]any{"value": "10"})
	if err != nil || value != "10.05" {
		t.Fatalf("value=%#v err=%v", value, err)
	}
	if _, err := (moduleRuntime{}).evaluate(decimal, map[string]any{"value": nil}); errorCode(err) == "" || err == nil {
		t.Fatal("strict null accepted")
	}
	date := resolverModule{Name: "date", Version: 1, Status: "published", OutputType: "Date", Timezone: "Europe/Madrid", Definition: map[string]any{"op": "date_add", "args": []any{map[string]any{"const": "2026-09-20"}, map[string]any{"const": "24h"}}}}
	value, err = (moduleRuntime{}).evaluate(date, map[string]any{})
	if err != nil || value != "2026-09-21" {
		t.Fatalf("date=%#v err=%v", value, err)
	}
}

func TestTelemetryRoundTrip(t *testing.T) {
	db := testDB(t)
	entry := requestLogEntry{projectID: "telemetry", operationName: "Workspace", operationType: "query", status: 200, durationMS: 12, createdAt: nowUTC(), operationHash: "abc", apiRelease: 3, responseBytes: 42, rowCount: 7, resolverCount: 5, sourceTimings: `{"tables":4.2}`, errorCodes: `[]`, authorizationScope: "platform:p", requestID: "request-1"}
	a := &App{logQueue: make(chan requestLogEntry, 1)}
	// Exercise the exact batch insert shape without starting a background writer.
	a.ctx = nil
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`INSERT INTO graphql_request_logs(project_id,operation_name,operation_type,status_code,duration_ms,error,created_at,operation_hash,api_release,response_bytes,row_count,resolver_count,source_timings_json,error_codes_json,authorization_scope,request_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, entry.projectID, entry.operationName, entry.operationType, entry.status, entry.durationMS, entry.errorMessage, entry.createdAt, entry.operationHash, entry.apiRelease, entry.responseBytes, entry.rowCount, entry.resolverCount, entry.sourceTimings, entry.errorCodes, entry.authorizationScope, entry.requestID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err := publicLogs(db, "telemetry", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["api_release"] != 3 || rows[0]["response_bytes"] != 42 || rows[0]["request_id"] != "request-1" {
		t.Fatalf("rows=%#v", rows)
	}
}
