package main

import "testing"

func TestProjectionSQLValidationCacheIsBoundedAndInvalidated(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	projectionSourceTable(t, a, ctx)
	query := "SELECT centre_id FROM {events}"
	if _, err := a.cachedProjectionSQL(ctx, query); err != nil {
		t.Fatal(err)
	}
	a.projectionSQLMu.RLock()
	first := len(a.projectionSQLCache)
	a.projectionSQLMu.RUnlock()
	if first != 1 {
		t.Fatalf("validation cache entries=%d, want 1", first)
	}
	if _, err := a.cachedProjectionSQL(ctx, query); err != nil {
		t.Fatal(err)
	}
	a.projectionSQLMu.RLock()
	second := len(a.projectionSQLCache)
	a.projectionSQLMu.RUnlock()
	if second != 1 {
		t.Fatalf("repeated validation grew cache to %d", second)
	}
	a.invalidateSQLCaches()
	a.projectionSQLMu.RLock()
	remaining := len(a.projectionSQLCache)
	a.projectionSQLMu.RUnlock()
	if remaining != 0 {
		t.Fatalf("schema/projection invalidation left %d validation entries", remaining)
	}
}
