package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
	_ "modernc.org/sqlite"
)

func TestSourceGrantMigrationDeduplicatesActiveHistory(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "entitlements.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	initial, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(initial)); err != nil {
		t.Fatal(err)
	}
	for _, plan := range []string{"basic", "pro"} {
		if _, err := db.Exec(`INSERT INTO entitlement_grants
			(project_id,subject_type,subject_id,feature_key,status,source_type,source_id,metadata)
			VALUES ('p','saas_account','a','crm:contacts','active','saas','a',?)`, `{"plan_key":"`+plan+`"}`); err != nil {
			t.Fatal(err)
		}
	}
	migration, err := os.ReadFile("migrations/002_source_grant_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var active, revoked int
	if err := db.QueryRow(`SELECT COUNT(*) FROM entitlement_grants WHERE status='active'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM entitlement_grants WHERE status='revoked'`).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if active != 1 || revoked != 1 {
		t.Fatalf("active=%d revoked=%d, want one of each", active, revoked)
	}
}

func TestGrantUpsertRefreshesSourceProjectionAndPreservesHistory(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-upsert"))
	base := map[string]any{
		"subject_type": "saas_account",
		"subject_id":   "acct_123",
		"feature_key":  "crm:contacts",
		"source_type":  "saas",
		"source_id":    "acct_123",
		"metadata":     map[string]any{"plan_key": "basic"},
	}
	first, created, changed, err := dbGrantUpsert(ctx.AppDB(), "proj-upsert", base)
	if err != nil || !created || !changed {
		t.Fatalf("first upsert: created=%v changed=%v err=%v", created, changed, err)
	}
	base["metadata"] = map[string]any{"plan_key": "pro"}
	second, created, changed, err := dbGrantUpsert(ctx.AppDB(), "proj-upsert", base)
	if err != nil || created || !changed {
		t.Fatalf("second upsert: created=%v changed=%v err=%v", created, changed, err)
	}
	if second.ID != first.ID {
		t.Fatalf("grant id changed: first=%d second=%d", first.ID, second.ID)
	}
	var metadata map[string]any
	if err := json.Unmarshal(second.Metadata, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if metadata["plan_key"] != "pro" {
		t.Fatalf("plan_key=%v, want pro", metadata["plan_key"])
	}
	_, created, changed, err = dbGrantUpsert(ctx.AppDB(), "proj-upsert", base)
	if err != nil || created || changed {
		t.Fatalf("unchanged upsert: created=%v changed=%v err=%v", created, changed, err)
	}
	grants, err := dbGrantsList(ctx.AppDB(), "proj-upsert", map[string]any{
		"subject_type": "saas_account", "subject_id": "acct_123", "status": "active",
	})
	if err != nil || len(grants) != 1 {
		t.Fatalf("active grants=%d err=%v, want one", len(grants), err)
	}
	if _, err := dbGrantRevoke(ctx.AppDB(), "proj-upsert", second.ID, "plan ended"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	third, created, changed, err := dbGrantUpsert(ctx.AppDB(), "proj-upsert", base)
	if err != nil || !created || !changed || third.ID == second.ID {
		t.Fatalf("replacement upsert: id=%d created=%v changed=%v err=%v", third.ID, created, changed, err)
	}
	all, err := dbGrantsList(ctx.AppDB(), "proj-upsert", map[string]any{
		"subject_type": "saas_account", "subject_id": "acct_123", "limit": 20,
	})
	if err != nil || len(all) != 2 {
		t.Fatalf("grant history=%d err=%v, want two", len(all), err)
	}
}

func TestGrantUpsertRequiresSourceIdentity(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-upsert"))
	_, _, _, err := dbGrantUpsert(ctx.AppDB(), "proj-upsert", map[string]any{
		"subject_id": "acct_123", "feature_key": "crm:contacts",
	})
	if err == nil {
		t.Fatal("expected source identity validation error")
	}
}

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
