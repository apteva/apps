package main

import (
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestGrantCheckUsageAndRevoke(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	args := map[string]any{
		"subject_type": "customer",
		"subject_id":   "cus_123",
		"feature_key":  "course:123:view",
		"source_type":  "manual",
	}
	grant, err := dbGrantCreate(ctx.AppDB(), "proj-test", args)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	allowed, grants, _, _, err := dbCheck(ctx.AppDB(), "proj-test", "customer", "cus_123", "course:123:view")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !allowed || len(grants) != 1 {
		t.Fatalf("expected allowed grant, allowed=%v grants=%d", allowed, len(grants))
	}
	if _, err := dbLimitSet(ctx.AppDB(), "proj-test", map[string]any{
		"subject_type": "customer",
		"subject_id":   "cus_123",
		"feature_key":  "api:requests",
		"limit_type":   "quota",
		"limit_value":  2,
	}); err != nil {
		t.Fatalf("limit: %v", err)
	}
	if _, err := dbGrantCreate(ctx.AppDB(), "proj-test", map[string]any{
		"subject_type": "customer",
		"subject_id":   "cus_123",
		"feature_key":  "api:requests",
	}); err != nil {
		t.Fatalf("grant api: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := dbUsageRecord(ctx.AppDB(), "proj-test", map[string]any{
			"subject_type": "customer",
			"subject_id":   "cus_123",
			"feature_key":  "api:requests",
			"quantity":     1,
		}); err != nil {
			t.Fatalf("usage %d: %v", i, err)
		}
	}
	allowed, _, usage, limit, err := dbCheck(ctx.AppDB(), "proj-test", "customer", "cus_123", "api:requests")
	if err != nil {
		t.Fatalf("check quota: %v", err)
	}
	if allowed || usage != 2 || limit == nil || limit.LimitValue != 2 {
		t.Fatalf("expected quota exhausted, allowed=%v usage=%d limit=%+v", allowed, usage, limit)
	}
	revoked, err := dbGrantRevoke(ctx.AppDB(), "proj-test", grant.ID, "test")
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if revoked.Status != "revoked" {
		t.Fatalf("status = %s, want revoked", revoked.Status)
	}
}

func TestGaugeUsageUsesLatestMeasurement(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	if _, err := dbLimitSet(ctx.AppDB(), "proj-test", map[string]any{
		"subject_type": "tenant",
		"subject_id":   "tenant_123",
		"feature_key":  "containers.storage.bytes",
		"limit_type":   "gauge",
		"limit_value":  int64(1000),
	}); err != nil {
		t.Fatalf("limit: %v", err)
	}
	for _, qty := range []int64{700, 900} {
		if _, err := dbUsageRecord(ctx.AppDB(), "proj-test", map[string]any{
			"subject_type": "tenant",
			"subject_id":   "tenant_123",
			"feature_key":  "containers.storage.bytes",
			"quantity":     qty,
			"usage_kind":   "gauge",
			"unit":         "bytes",
		}); err != nil {
			t.Fatalf("record gauge %d: %v", qty, err)
		}
	}
	usage, limit, err := dbUsageGet(ctx.AppDB(), "proj-test", "tenant", "tenant_123", "containers.storage.bytes")
	if err != nil {
		t.Fatalf("usage get: %v", err)
	}
	if usage != 900 || limit == nil || limit.LimitType != "gauge" {
		t.Fatalf("gauge usage=%d limit=%+v, want latest 900 gauge limit", usage, limit)
	}
	if _, err := dbUsageRecord(ctx.AppDB(), "proj-test", map[string]any{
		"subject_type": "tenant",
		"subject_id":   "tenant_123",
		"feature_key":  "api.calls",
		"quantity":     2,
	}); err != nil {
		t.Fatalf("record counter: %v", err)
	}
	if _, err := dbUsageRecord(ctx.AppDB(), "proj-test", map[string]any{
		"subject_type": "tenant",
		"subject_id":   "tenant_123",
		"feature_key":  "api.calls",
		"quantity":     3,
	}); err != nil {
		t.Fatalf("record counter: %v", err)
	}
	counter, _, err := dbUsageGet(ctx.AppDB(), "proj-test", "tenant", "tenant_123", "api.calls")
	if err != nil {
		t.Fatalf("counter get: %v", err)
	}
	if counter != 5 {
		t.Fatalf("counter usage=%d, want 5", counter)
	}
}
