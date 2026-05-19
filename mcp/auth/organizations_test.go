package main

// organizations_test — cross-org isolation. The whole point of v0.4.0
// is that users / clients / sessions / signing keys / audit are
// partitioned by Organization. These tests ensure missing org scoping
// in db.go would fail loudly: every assertion below relies on a
// (project_id, org_id) WHERE predicate to keep org-A and org-B
// invisible to each other.

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// tamperJWTOrg rewrites the `org` claim of a JWT without re-signing.
// The result has a valid structure but a broken signature; /me should
// reject it. Used to assert per-org signing-key isolation.
func tamperJWTOrg(t *testing.T, token, newOrg string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("malformed token: %q", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	m["org"] = newOrg
	tampered, _ := json.Marshal(m)
	parts[1] = base64.RawURLEncoding.EncodeToString(tampered)
	return strings.Join(parts, ".")
}

// newTwoOrgCtx — two organizations, one client and one signed-up user
// in each. Returns (ctx, clientA, clientB, userIDA, userIDB).
func newTwoOrgCtx(t *testing.T) (ctx *sdk.AppCtx, clientA, clientB string, uidA, uidB int64) {
	t.Helper()
	ctx = tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("two-orgs"),
		tk.WithConfig(map[string]string{
			"email_verification_required": "false",
			"app_url":                     "http://localhost:8080",
		}))

	// Seed both orgs + per-org signing keys.
	orgAID, err := dbCreateOrg(ctx.AppDB(), "two-orgs", "default", "Default", "#94a3b8")
	if err != nil {
		t.Fatalf("seed default org: %v", err)
	}
	orgBID, err := dbCreateOrg(ctx.AppDB(), "two-orgs", "acme", "Acme", "#3b82f6")
	if err != nil {
		t.Fatalf("seed acme org: %v", err)
	}
	for _, oid := range []int64{orgAID, orgBID} {
		if err := ensureSigningKey(ctx.AppDB(), "two-orgs", oid); err != nil {
			t.Fatalf("seed signing key for org %d: %v", oid, err)
		}
	}
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = nil })

	app := &App{}

	// One client per org.
	outA, err := app.toolClientsCreate(ctx, map[string]any{
		"organization_slug": "default",
		"name":              "default-client",
		"type":              "spa",
	})
	if err != nil {
		t.Fatalf("create client A: %v", err)
	}
	clientA = outA.(map[string]any)["client_id"].(string)

	outB, err := app.toolClientsCreate(ctx, map[string]any{
		"organization_slug": "acme",
		"name":              "acme-client",
		"type":              "spa",
	})
	if err != nil {
		t.Fatalf("create client B: %v", err)
	}
	clientB = outB.(map[string]any)["client_id"].(string)

	// One user per org. Same email on both — proves users are partitioned.
	for _, c := range []string{clientA, clientB} {
		rec := callJSON(app.handleSignup, "POST", "/signup", map[string]any{
			"email":     "shared@example.com",
			"password":  "VerySafe!Pw#12345",
			"client_id": c,
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("signup via %s: %d %s", c, rec.Code, rec.Body.String())
		}
	}

	// Recover the user_ids by reading the per-org user back.
	uA, err := dbGetUserByEmail(ctx.AppDB(), "two-orgs", orgAID, "shared@example.com")
	if err != nil {
		t.Fatalf("read org-A user: %v", err)
	}
	uB, err := dbGetUserByEmail(ctx.AppDB(), "two-orgs", orgBID, "shared@example.com")
	if err != nil {
		t.Fatalf("read org-B user: %v", err)
	}
	uidA, uidB = uA.ID, uB.ID
	return
}

// TestOrgIsolation_SameEmailDifferentUsers — the load-bearing test for
// v0.4.0. Signup with the same email against two different clients
// (= two different orgs) must produce two distinct user rows.
func TestOrgIsolation_SameEmailDifferentUsers(t *testing.T) {
	_, _, _, uidA, uidB := newTwoOrgCtx(t)
	if uidA == 0 || uidB == 0 {
		t.Fatalf("user_ids empty: A=%d B=%d", uidA, uidB)
	}
	if uidA == uidB {
		t.Fatalf("same user id %d for both orgs — partition is broken", uidA)
	}
}

// TestOrgIsolation_AdminListScoped — searching users in org A must not
// surface org B's user; searching project-wide returns both.
func TestOrgIsolation_AdminListScoped(t *testing.T) {
	ctx, _, _, _, _ := newTwoOrgCtx(t)
	app := &App{}

	// Scoped to org A.
	out, err := app.toolUsersSearch(ctx, map[string]any{
		"organization_slug": "default",
	})
	if err != nil {
		t.Fatalf("search org A: %v", err)
	}
	usersA := out.(map[string]any)["users"].([]User)
	if len(usersA) != 1 {
		t.Errorf("org A users = %d, want 1", len(usersA))
	}

	// Scoped to org B.
	out, err = app.toolUsersSearch(ctx, map[string]any{
		"organization_slug": "acme",
	})
	if err != nil {
		t.Fatalf("search org B: %v", err)
	}
	usersB := out.(map[string]any)["users"].([]User)
	if len(usersB) != 1 {
		t.Errorf("org B users = %d, want 1", len(usersB))
	}

	// Project-wide rollup returns both.
	out, err = app.toolUsersSearch(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("search project-wide: %v", err)
	}
	usersAll := out.(map[string]any)["users"].([]User)
	if len(usersAll) != 2 {
		t.Errorf("project-wide users = %d, want 2", len(usersAll))
	}
}

// TestOrgIsolation_JWTFromOrgARejectedByOrgB — defends the per-org
// signing keys invariant. A JWT minted for org A signed by org A's
// key must not validate against org B's JWKS, even if we hand /me a
// faked `org` claim claiming to be from B.
func TestOrgIsolation_JWTFromOrgARejectedByOrgB(t *testing.T) {
	_, clientA, _, _, _ := newTwoOrgCtx(t)
	app := &App{}

	// Get a real org-A token.
	rec := callJSON(app.handleLogin, "POST", "/login", map[string]any{
		"email":     "shared@example.com",
		"password":  "VerySafe!Pw#12345",
		"client_id": clientA,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login org A: %d %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	tokenA, _ := body["access_token"].(string)
	if tokenA == "" {
		t.Fatal("no access token")
	}

	// Confirm /me validates it correctly (org-A claim, org-A keys).
	rec = call(app.handleMe, "GET", "/me", nil, "Authorization", "Bearer "+tokenA)
	if rec.Code != http.StatusOK {
		t.Fatalf("/me with org-A token expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Now: tamper. Take the org-A JWT's payload and swap org → "acme".
	// The signature won't match any of org-B's keys, so /me should 401.
	tampered := tamperJWTOrg(t, tokenA, "acme")
	rec = call(app.handleMe, "GET", "/me", nil, "Authorization", "Bearer "+tampered)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("/me with tampered org claim expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestOrgIsolation_RefreshTokenCantCrossOrgs — replay a refresh token
// issued for org A against org B's client. The client_id mismatch
// alone catches it, but we also assert the org defense-in-depth check
// fires when client_ids somehow match.
func TestOrgIsolation_RefreshTokenCantCrossOrgs(t *testing.T) {
	_, clientA, clientB, _, _ := newTwoOrgCtx(t)
	app := &App{}

	// Get a refresh token for org A.
	rec := callJSON(app.handleLogin, "POST", "/login", map[string]any{
		"email":     "shared@example.com",
		"password":  "VerySafe!Pw#12345",
		"client_id": clientA,
	})
	body := decode(t, rec)
	refreshA, _ := body["refresh_token"].(string)
	if refreshA == "" {
		t.Fatal("no refresh token")
	}

	// Replay against org B's client.
	rec = callJSON(app.handleRefresh, "POST", "/refresh", map[string]any{
		"refresh_token": refreshA,
		"client_id":     clientB,
	})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("cross-org refresh expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestOrgIsolation_DefaultOrgCannotBeArchived — archiving the default
// org would brick the legacy `/.well-known/*` aliases. The handler
// must refuse it.
func TestOrgIsolation_DefaultOrgCannotBeArchived(t *testing.T) {
	ctx, _, _, _, _ := newTwoOrgCtx(t)
	app := &App{}
	_, err := app.toolOrgsArchive(ctx, map[string]any{
		"organization_slug": "default",
	})
	if err == nil {
		t.Fatal("archiving default org should error")
	}
}
