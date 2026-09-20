package main

import (
	"context"
	"testing"
	"time"
)

func TestRuntimeCachesRemainImmutableUntilInvalidated(t *testing.T) {
	a := secureTestApp(t, &trustedPlatform{})
	db := a.ctx.AppDB()

	api, err := a.cachedAPI("p1", "default")
	if err != nil || api.Name != "Default GraphQL API" {
		t.Fatalf("initial api: %#v %v", api, err)
	}
	if _, err := db.Exec(`UPDATE graphql_apis SET name='Changed' WHERE project_id='p1' AND slug='default'`); err != nil {
		t.Fatal(err)
	}
	if cached, _ := a.cachedAPI("p1", "default"); cached.Name != api.Name {
		t.Fatalf("api cache changed without invalidation: %#v", cached)
	}
	a.invalidateRuntime("p1", "default")
	if cached, _ := a.cachedAPI("p1", "default"); cached.Name != "Changed" {
		t.Fatalf("api cache was not invalidated: %#v", cached)
	}

	policy, err := a.cachedSecurity("p1", "default")
	if err != nil || policy.Mode != "platform" {
		t.Fatalf("initial security: %#v %v", policy, err)
	}
	if _, err := setSecurity(db, "p1", "default", testSecurityPolicy()); err != nil {
		t.Fatal(err)
	}
	if cached, _ := a.cachedSecurity("p1", "default"); cached.Mode != "platform" {
		t.Fatalf("security cache changed without invalidation: %#v", cached)
	}
	a.invalidateRuntime("p1", "default")
	if cached, _ := a.cachedSecurity("p1", "default"); cached.Mode != "auth" {
		t.Fatalf("security cache was not invalidated: %#v", cached)
	}
	if _, err := setSecurity(db, "p1", "default", map[string]any{"mode": "platform"}); err != nil {
		t.Fatal(err)
	}
	a.invalidateRuntime("p1", "default")

	first, validationErrors, err := createSchemaForAPI(db, "p1", "default", "production", "type Query { value: Int }", 0)
	if err != nil || len(validationErrors) != 0 {
		t.Fatalf("create first schema: %v %v", validationErrors, err)
	}
	if _, err := publishSchemaForAPI(db, "p1", "default", "production", first.Version); err != nil {
		t.Fatal(err)
	}
	loaded, err := a.cachedPublishedSchema("p1", "default", "production")
	if err != nil || loaded.Version != first.Version {
		t.Fatalf("initial schema: %#v %v", loaded, err)
	}
	second, validationErrors, err := createSchemaForAPI(db, "p1", "default", "production", "type Query { value: Int other: Int }", 0)
	if err != nil || len(validationErrors) != 0 {
		t.Fatalf("create second schema: %v %v", validationErrors, err)
	}
	if _, err := publishSchemaForAPI(db, "p1", "default", "production", second.Version); err != nil {
		t.Fatal(err)
	}
	if cached, _ := a.cachedPublishedSchema("p1", "default", "production"); cached.Version != first.Version {
		t.Fatalf("schema cache changed without invalidation: %#v", cached)
	}
	a.invalidateRuntime("p1", "default")
	if cached, _ := a.cachedPublishedSchema("p1", "default", "production"); cached.Version != second.Version {
		t.Fatalf("schema cache was not invalidated: %#v", cached)
	}

	source, err := createSourceForAPI(db, "p1", "default", "rows", "tables", map[string]any{"table": "rows_v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upsertResolverForAPI(db, "p1", "default", "Query", "value", "find", source.ID, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	plan, err := a.executionPlan("p1", "default")
	if err != nil || plan.sources[source.ID].Config["table"] != "rows_v1" {
		t.Fatalf("initial plan: %#v %v", plan, err)
	}
	if _, err := createSourceForAPI(db, "p1", "default", "rows", "tables", map[string]any{"table": "rows_v2"}); err != nil {
		t.Fatal(err)
	}
	if cached, _ := a.executionPlan("p1", "default"); cached.sources[source.ID].Config["table"] != "rows_v1" {
		t.Fatalf("plan cache changed without invalidation: %#v", cached.sources[source.ID])
	}
	a.invalidateRuntime("p1", "default")
	if cached, _ := a.executionPlan("p1", "default"); cached.sources[source.ID].Config["table"] != "rows_v2" {
		t.Fatalf("plan cache was not invalidated: %#v", cached.sources[source.ID])
	}
}

func TestPreparedOperationCachesCostAndPermissionFields(t *testing.T) {
	a := &App{}
	schema, errs := a.compiledSchema("p\x00default\x00production\x001\x00hash", `type Query { rows: [Row!]! } type Row { id: Int! name: String! }`)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	key := "p\x00default\x00production\x001\x00hash\x00\x00query { rows { id name } }"
	first, errs, err := a.prepareOperation(key, `query { rows { id name } }`, "", schema)
	if err != nil || len(errs) != 0 {
		t.Fatalf("prepare: %v %v", errs, err)
	}
	second, _, _ := a.prepareOperation(key, "unused", "", schema)
	if second != first {
		t.Fatal("prepared operation was not reused")
	}
	if first.fields != 3 || first.depth != 2 {
		t.Fatalf("cost = %d/%d", first.fields, first.depth)
	}
	want := []string{"Query.rows", "Row.id", "Row.name"}
	if len(first.permissionFields) != len(want) {
		t.Fatalf("permission fields: %v", first.permissionFields)
	}
	for index := range want {
		if first.permissionFields[index] != want[index] {
			t.Fatalf("permission fields: %v", first.permissionFields)
		}
	}
	policy := securityPolicy{Mode: "auth", Fields: map[string][]string{"Row.name": {"read:name"}}}
	identity := &requestIdentity{Permissions: []string{"read:name"}, Expires: time.Now().Add(time.Minute)}
	ctx := context.WithValue(context.Background(), identityKey{}, identity)
	if err := authorizePermissionFields(ctx, policy, first.permissionFields); err != nil {
		t.Fatal(err)
	}
}

func TestRequestLoggerFlushesOnUnmount(t *testing.T) {
	a := secureTestApp(t, &trustedPlatform{})
	a.logRequest("p1", "default", "Workspace", "query", 200, 12*time.Millisecond, nil)
	a.stopRequestLogger()
	var count int
	if err := a.ctx.AppReadDB().QueryRow(`SELECT COUNT(*) FROM graphql_request_logs WHERE project_id='p1' AND operation_name='Workspace'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("request log count = %d", count)
	}
}
