package main

import (
	"database/sql"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestGenericCredentialMigrationPreservesLegacyRows(t *testing.T) {
	db, err := sql.Open("sqlite", t.TempDir()+"/legacy.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, path := range []string{"migrations/001_init.sql", "migrations/002_app_events.sql", "migrations/003_reliability.sql", "migrations/004_configurations_stages.sql"} {
		script, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(script)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO apis(id,project_id,slug,name) VALUES(1,?,?,?)`, testProject, "legacy", "Legacy"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO api_keys(project_id,api_id,name,key_prefix,key_hash,status) VALUES(?,?,?,?,?,'active')`, testProject, 1, "old", "aptv_api_old", "hash"); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("migrations/005_generic_credentials.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(script)); err != nil {
		t.Fatal(err)
	}
	key, err := dbGetAPIKey(db, testProject, 1)
	if err != nil || key == nil {
		t.Fatalf("legacy key after migration = %+v err=%v", key, err)
	}
	if key.SubjectType != "" || len(key.Claims) != 0 || len(key.Scopes) != 0 || key.ExpiresAt != "" {
		t.Fatalf("legacy defaults changed behavior: %+v", key)
	}
}

func createCredentialTestAPI(t *testing.T, app *App, slug string) (*API, *App) {
	t.Helper()
	api, err := dbCreateAPI(app.ctx.AppDB(), apiInput{ProjectID: testProject, Slug: slug, Name: slug, AuthJSON: `{"kind":"api_key"}`})
	if err != nil {
		t.Fatal(err)
	}
	return api, app
}

func TestGenericCredentialIdentityScopesAndIdempotency(t *testing.T) {
	app, ctx := mountTestApp(t)
	api, _ := createCredentialTestAPI(t, app, "credentials")
	args := map[string]any{
		"api_id": api.ID, "name": "automation", "subject_type": "service", "subject_id": "worker-42",
		"claims": map[string]any{"tenant_id": "tenant-a", "region": "eu"},
		"scopes": []any{"orders:write", "orders:read"}, "metadata": map[string]any{"owner": "operations"},
		"external_id": "credential-42", "idempotency_key": "issue-42",
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	result, err := app.toolKeyCreate(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	created := result.(map[string]any)
	if created["created"] != true || created["secret"] == "" {
		t.Fatalf("unexpected create result: %#v", created)
	}
	key := created["key"].(*APIKey)
	if key.SubjectType != "service" || key.SubjectID != "worker-42" || key.Metadata["owner"] != "operations" {
		t.Fatalf("generic key metadata lost: %+v", key)
	}

	retry, err := app.toolKeyCreate(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	retried := retry.(map[string]any)
	if retried["created"] != false {
		t.Fatalf("retry created another credential: %#v", retried)
	}
	if _, exists := retried["secret"]; exists {
		t.Fatal("idempotent retry leaked or synthesized a plaintext secret")
	}
	if retried["key"].(*APIKey).ID != key.ID {
		t.Fatal("idempotent retry returned a different credential")
	}
	conflict := map[string]any{}
	for name, value := range args {
		conflict[name] = value
	}
	conflict["name"] = "different"
	if _, err := app.toolKeyCreate(ctx, conflict); err == nil || !strings.Contains(err.Error(), "different arguments") {
		t.Fatalf("conflicting retry error = %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", created["secret"].(string))
	auth, err := app.authorizeRequest(req, api, &APIRoute{AuthJSON: `{"kind":"api_key","required_scopes":["orders:write"]}`})
	if err != nil {
		t.Fatal(err)
	}
	if auth.Principal.Subject != "api_key:"+formatID(key.ID) || auth.Principal.Claims["subject_type"] != "service" || auth.Principal.Claims["subject_id"] != "worker-42" || auth.Principal.Claims["tenant_id"] != "tenant-a" {
		t.Fatalf("unexpected principal: %+v", auth.Principal)
	}
	if _, forwarded := auth.Principal.Claims["owner"]; forwarded {
		t.Fatal("management metadata was forwarded as a verified claim")
	}
	if _, err := app.authorizeRequest(req, api, &APIRoute{AuthJSON: `{"kind":"api_key","required_scopes":["admin"]}`}); err == nil {
		t.Fatal("missing required scope was accepted")
	}
}

func TestLegacyAndExpiredAPIKeys(t *testing.T) {
	app, ctx := mountTestApp(t)
	api, _ := createCredentialTestAPI(t, app, "compatibility")
	legacy, err := app.toolKeyCreate(ctx, map[string]any{"api_id": api.ID, "name": "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-API-Key", legacy.(map[string]any)["secret"].(string))
	if _, err := app.authorizeRequest(req, api, &APIRoute{}); err != nil {
		t.Fatalf("legacy key stopped authenticating: %v", err)
	}
	if _, err := app.authorizeRequest(req, api, &APIRoute{AuthJSON: `{"kind":"api_key","required_scopes":["read"]}`}); err == nil {
		t.Fatal("legacy unscoped key bypassed an explicit scope requirement")
	}

	expired, err := app.toolKeyCreate(ctx, map[string]any{
		"api_id": api.ID, "name": "expired", "expires_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	expiredReq := httptest.NewRequest("GET", "/", nil)
	expiredReq.Header.Set("X-API-Key", expired.(map[string]any)["secret"].(string))
	if _, err := app.authorizeRequest(expiredReq, api, &APIRoute{}); err == nil {
		t.Fatal("expired credential authenticated")
	}
}

func TestSubjectRevocationAndUsagePlan(t *testing.T) {
	app, ctx := mountTestApp(t)
	api, _ := createCredentialTestAPI(t, app, "traffic")
	created, err := app.toolKeyCreate(ctx, map[string]any{
		"api_id": api.ID, "name": "device", "subject_type": "device", "subject_id": "device-7",
	})
	if err != nil {
		t.Fatal(err)
	}
	key := created.(map[string]any)["key"].(*APIKey)
	planResult, err := app.toolUsagePlanCreate(ctx, map[string]any{
		"api_id": api.ID, "name": "one-at-a-time", "rate_limit": 1, "interval_seconds": 60, "burst": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := planResult.(map[string]any)["usage_plan"].(*APIUsagePlan)
	if _, err := app.toolUsagePlanAttachKey(ctx, map[string]any{"key_id": key.ID, "usage_plan_id": plan.ID}); err != nil {
		t.Fatal(err)
	}
	if allowed, _, err := app.allowAPIKeyRequest(testProject, api.ID, key.ID); err != nil || !allowed {
		t.Fatalf("first request allowed=%v err=%v", allowed, err)
	}
	if allowed, retry, err := app.allowAPIKeyRequest(testProject, api.ID, key.ID); err != nil || allowed || retry < time.Second {
		t.Fatalf("second request allowed=%v retry=%v err=%v", allowed, retry, err)
	}

	listed, err := app.toolKeyListBySubject(ctx, map[string]any{"subject_type": "device", "subject_id": "device-7"})
	if err != nil || listed.(map[string]any)["count"] != 1 {
		t.Fatalf("subject list = %#v err=%v", listed, err)
	}
	revoked, err := app.toolKeyRevokeBySubject(ctx, map[string]any{"subject_type": "device", "subject_id": "device-7"})
	if err != nil || revoked.(map[string]any)["revoked"] != 1 {
		t.Fatalf("subject revoke = %#v err=%v", revoked, err)
	}
}

func formatID(id int64) string {
	const digits = "0123456789"
	if id == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for id > 0 {
		i--
		buf[i] = digits[id%10]
		id /= 10
	}
	return string(buf[i:])
}
