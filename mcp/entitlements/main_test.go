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
