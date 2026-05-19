package main

// multi_org_client_test — v0.4.1 multi-organization client flow.
//
// A multi-org client (`organization_id IS NULL`) is one SaaS deployment
// serving many orgs. The SaaS sends client_id + organization_slug on
// every public call. These tests pin the contract:
//
//   1. Creating a client without an org → multi-org.
//   2. /signup via a multi-org client requires organization_slug;
//      omission is a 400.
//   3. Same multi-org client signs up two users in two orgs; users are
//      isolated (different ids, different password pools).
//   4. A refresh token issued under org-A's session cannot be exchanged
//      with organization_slug=B against the same multi-org client.

import (
	"net/http"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// newMultiOrgCtx — two orgs + one multi-org client serving both.
// Returns (ctx, multiOrgClientID).
func newMultiOrgCtx(t *testing.T) (*sdk.AppCtx, string) {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("multi"),
		tk.WithConfig(map[string]string{
			"email_verification_required": "false",
			"app_url":                     "http://localhost:8080",
		}))
	for _, o := range []struct{ slug, name string }{
		{"default", "Default"}, {"acme", "Acme"}, {"beta", "Beta"},
	} {
		id, err := dbCreateOrg(ctx.AppDB(), "multi", o.slug, o.name, "")
		if err != nil {
			t.Fatalf("seed org %s: %v", o.slug, err)
		}
		if err := ensureSigningKey(ctx.AppDB(), "multi", id); err != nil {
			t.Fatalf("seed signing key for %s: %v", o.slug, err)
		}
	}
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = nil })

	app := &App{}
	// Create the multi-org client by NOT passing organization_slug.
	out, err := app.toolClientsCreate(ctx, map[string]any{
		"name": "shared-saas",
		"type": "web",
	})
	if err != nil {
		t.Fatalf("create multi-org client: %v", err)
	}
	clientID := out.(map[string]any)["client_id"].(string)
	if multiFlag, _ := out.(map[string]any)["multi_organization"].(bool); !multiFlag {
		t.Fatal("client did not report multi_organization=true after omitted org")
	}
	return ctx, clientID
}

func TestMultiOrg_ClientCreatedWithoutOrgIsMultiOrg(t *testing.T) {
	ctx, clientID := newMultiOrgCtx(t)
	app := &App{}

	// auth_clients_list with organization_slug=acme should still
	// surface the multi-org client (it serves every org).
	out, err := app.toolClientsList(ctx, map[string]any{
		"organization_slug": "acme",
	})
	if err != nil {
		t.Fatalf("list clients in acme: %v", err)
	}
	clients := out.(map[string]any)["clients"].([]Client)
	found := false
	for _, c := range clients {
		if c.ClientID == clientID {
			if c.OrganizationID != 0 {
				t.Errorf("multi-org client has organization_id=%d, want 0", c.OrganizationID)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("multi-org client %s missing from acme org's list", clientID)
	}
}

func TestMultiOrg_SignupWithoutOrgSlugErrors(t *testing.T) {
	_, clientID := newMultiOrgCtx(t)
	app := &App{}
	rec := callJSON(app.handleSignup, "POST", "/signup", map[string]any{
		"email":     "alice@example.com",
		"password":  "VerySafe!Pw#12345",
		"client_id": clientID,
		// no organization_slug — must fail with 400
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("signup w/o org_slug expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMultiOrg_SameClientServesTwoOrgsIndependently(t *testing.T) {
	ctx, clientID := newMultiOrgCtx(t)
	app := &App{}

	// Signup Alice in acme via the multi-org client.
	rec := callJSON(app.handleSignup, "POST", "/signup", map[string]any{
		"email":             "alice@example.com",
		"password":          "VerySafe!Pw#12345",
		"client_id":         clientID,
		"organization_slug": "acme",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("acme signup: %d %s", rec.Code, rec.Body.String())
	}

	// Same email, different org — must succeed and produce a different
	// user. This is the whole point of the partition.
	rec = callJSON(app.handleSignup, "POST", "/signup", map[string]any{
		"email":             "alice@example.com",
		"password":          "Different!Pw#54321",
		"client_id":         clientID,
		"organization_slug": "beta",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("beta signup: %d %s", rec.Code, rec.Body.String())
	}

	// Read each back via the org-scoped lookup; they must be distinct.
	acmeOrg, _ := dbGetOrgBySlug(ctx.AppDB(), "multi", "acme")
	betaOrg, _ := dbGetOrgBySlug(ctx.AppDB(), "multi", "beta")
	uA, err := dbGetUserByEmail(ctx.AppDB(), "multi", acmeOrg.ID, "alice@example.com")
	if err != nil {
		t.Fatalf("read alice in acme: %v", err)
	}
	uB, err := dbGetUserByEmail(ctx.AppDB(), "multi", betaOrg.ID, "alice@example.com")
	if err != nil {
		t.Fatalf("read alice in beta: %v", err)
	}
	if uA.ID == uB.ID {
		t.Fatalf("multi-org client produced same user_id %d in both orgs — partition broken", uA.ID)
	}

	// And login with the wrong-org password must 401: the acme
	// password shouldn't authenticate the beta user.
	rec = callJSON(app.handleLogin, "POST", "/login", map[string]any{
		"email":             "alice@example.com",
		"password":          "VerySafe!Pw#12345", // acme's password
		"client_id":         clientID,
		"organization_slug": "beta", // … sent to beta
	})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("acme password against beta org expected 401, got %d body=%s",
			rec.Code, rec.Body.String())
	}
}

func TestMultiOrg_RefreshHintMismatchRejected(t *testing.T) {
	_, clientID := newMultiOrgCtx(t)
	app := &App{}

	// Sign up + auto-login in acme; capture the refresh token.
	rec := callJSON(app.handleSignup, "POST", "/signup", map[string]any{
		"email":             "alice@example.com",
		"password":          "VerySafe!Pw#12345",
		"client_id":         clientID,
		"organization_slug": "acme",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup: %d %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	refresh, _ := body["refresh_token"].(string)
	if refresh == "" {
		t.Fatal("no refresh token from signup")
	}

	// Refresh with a /correct/ hint — should work.
	rec = callJSON(app.handleRefresh, "POST", "/refresh", map[string]any{
		"refresh_token":     refresh,
		"client_id":         clientID,
		"organization_slug": "acme",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("acme refresh expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body = decode(t, rec)
	rotatedRefresh, _ := body["refresh_token"].(string)

	// Refresh with a /wrong/ hint — must 401. The session was minted
	// under acme; even though the client is multi-org, the SaaS
	// claiming "this token is for beta" is a strong signal of cross-
	// org replay.
	rec = callJSON(app.handleRefresh, "POST", "/refresh", map[string]any{
		"refresh_token":     rotatedRefresh,
		"client_id":         clientID,
		"organization_slug": "beta",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong-org hint expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}
