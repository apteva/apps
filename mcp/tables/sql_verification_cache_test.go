package main

import (
	"context"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"testing"
)

func authorizationEntries(a *App) int {
	a.authorizationMu.RLock()
	defer a.authorizationMu.RUnlock()
	return len(a.authorizationCache)
}
func TestOrdinarySQLVerificationCachedPermissionsStillChecked(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	projectionSourceTable(t, a, ctx)
	createProjection(t, a, ctx)
	runProjectionWorker(t, a, ctx)
	for _, q := range []string{"SELECT value FROM {events}", "SELECT total FROM {event_totals}", "SELECT value FROM json_each('[1,2]')", "SELECT value FROM json_tree('[1,2]')"} {
		a.invalidateSQLCaches()
		for i := 0; i < 2; i++ {
			mustCall(t, a, ctx, "tables_query", map[string]any{"sql": q})
			if authorizationEntries(a) != 1 {
				t.Fatalf("successful verification not reused for %q", q)
			}
		}
		for _, tool := range a.MCPTools() {
			if tool.Name == "tables_query" && q == "SELECT total FROM {event_totals}" {
				if _, err := tool.HandlerCtx(sdk.WithCaller(context.Background(), &sdk.Caller{DefaultEffect: "deny"}), ctx, map[string]any{"sql": q}); err == nil {
					t.Fatal("warm verification bypassed revoked caller permissions", q)
				}
			}
		}
	}
	// Exercise projection permission checks specifically, independently of the
	// normal placeholder resolution performed by the API.
	resolved, err := a.substitutePlaceholders(ctx, "test-proj", "SELECT total FROM {event_totals}")
	if err != nil {
		t.Fatal(err)
	}
	read, err := acquireReadConn(ctx, "event_totals")
	if err != nil {
		t.Fatal(err)
	}
	defer read.close()
	if err := authorizeQuery(context.Background(), read.conn, ctx, a, "test-proj", "SELECT total FROM {event_totals}", resolved, nil); err != nil {
		t.Fatal(err)
	}
	denied := ctx.WithProject("test-proj")
	activeContexts.Store(denied, sdk.WithCaller(context.Background(), &sdk.Caller{DefaultEffect: "deny"}))
	defer activeContexts.Delete(denied)
	if err := authorizeQuery(context.Background(), read.conn, denied, a, "test-proj", "SELECT total FROM {event_totals}", resolved, nil); err == nil {
		t.Fatal("cached program bypassed projection permission revocation")
	}
}
func TestSQLVerificationCacheLifecycleAndRejections(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	projectionSourceTable(t, a, ctx)
	warm := func() {
		t.Helper()
		mustCall(t, a, ctx, "tables_query", map[string]any{"sql": "SELECT value FROM {events}"})
		if authorizationEntries(a) == 0 {
			t.Fatal("no successful verification")
		}
	}
	check := func(tool string, args map[string]any) {
		t.Helper()
		warm()
		mustCall(t, a, ctx, tool, args)
		if authorizationEntries(a) != 0 {
			t.Fatalf("%s left stale verifications", tool)
		}
	}
	check("tables_alter", map[string]any{"name": "events", "add": map[string]any{"name": "metadata", "type": "text"}})
	check("indexes_create", map[string]any{"table": "events", "name": "by_value", "columns": []any{"value"}})
	check("indexes_drop", map[string]any{"table": "events", "name": "by_value", "confirm": true})
	warm()
	createProjection(t, a, ctx)
	if authorizationEntries(a) != 0 {
		t.Fatal("projection create failed to invalidate")
	}
	runProjectionWorker(t, a, ctx)
	check("indexes_create", map[string]any{"table": "event_totals", "name": "by_centre", "columns": []any{"centre_id"}})
	check("indexes_drop", map[string]any{"table": "event_totals", "name": "by_centre", "confirm": true})
	replacement := watchedProjectionArgs("event_totals")
	replacement["version"] = 2
	replacement["activate"] = false
	mustCall(t, a, ctx, "projections_create", replacement)
	runProjectionWorker(t, a, ctx)
	check("projections_activate", map[string]any{"name": "event_totals", "version": 2})
	check("projections_delete", map[string]any{"name": "event_totals", "version": 1, "confirm": true})
	a.invalidateSQLCaches()
	for _, q := range []string{"SELECT * FROM tables_meta", "SELECT * FROM t_1", "DELETE FROM {events}", "WITH c AS (SELECT 1) UPDATE {events} SET value=4", "SELECT missing FROM {events}", "SELECT * FROM dbstat"} {
		for i := 0; i < 2; i++ {
			if _, err := callTool(a, ctx, "tables_query", map[string]any{"sql": q}); err == nil {
				t.Fatal("invalid SQL allowed", q)
			}
		}
		if authorizationEntries(a) != 0 {
			t.Fatal("failed verification cached", q)
		}
	}
	// Even after a good query is cached, inaccessible tables/write operations fail.
	warm()
	for _, q := range []string{"SELECT * FROM tables_meta", "DELETE FROM {events}"} {
		if _, err := callTool(a, ctx, "tables_query", map[string]any{"sql": q}); err == nil {
			t.Fatal("warm cache allowed forbidden SQL", q)
		}
	}
}
func TestSQLVerificationCacheBoundedAndEpochSafe(t *testing.T) {
	ctx, _, bump := newFileBackedTestCtx(t, "test-proj")
	a := &App{}
	old := a.authorizationKey(ctx, "SELECT 1")
	a.invalidateSQLCaches()
	a.rememberAuthorization(old)
	if a.authorizationCached(a.authorizationKey(ctx, "SELECT 1")) {
		t.Fatal("verification begun in old epoch populated current epoch")
	}
	current := a.authorizationKey(ctx, "SELECT 1")
	a.rememberAuthorization(current)
	bump()
	if a.authorizationCached(a.authorizationKey(ctx, "SELECT 1")) {
		t.Fatal("database generation reused old verification")
	}
	for i := 0; i < maxQueryPlanEntries+10; i++ {
		a.rememberAuthorization(a.authorizationKey(ctx, fmt.Sprintf("SELECT %d", i)))
	}
	if authorizationEntries(a) > maxQueryPlanEntries {
		t.Fatal("unbounded verification cache")
	}
}
func BenchmarkOrdinarySQLVerification(b *testing.B) {
	ctx := benchmarkCtx(b)
	a := &App{}
	seedBenchmarkTable(b, a, ctx, "events", 0)
	raw := "SELECT external_key,SUM(value) AS total FROM {events} WHERE value>=? GROUP BY external_key ORDER BY total DESC"
	resolved, err := a.substitutePlaceholders(ctx, "bench", raw)
	if err != nil {
		b.Fatal(err)
	}
	read, err := acquireReadConn(ctx, "events")
	if err != nil {
		b.Fatal(err)
	}
	defer read.close()
	for _, mode := range []string{"cold", "warm"} {
		b.Run(mode, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if mode == "cold" {
					a.authorizationMu.Lock()
					a.authorizationCache = nil
					a.authorizationMu.Unlock()
				}
				if err := authorizeQuery(context.Background(), read.conn, ctx, a, "bench", raw, resolved, []any{1}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
