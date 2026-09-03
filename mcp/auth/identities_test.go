package main

// identities_test — guest login by external identity, linking,
// upgrade to a full account, the per-client creation rate limit, and
// the JWKS tool sibling apps use to verify tokens locally.

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func loginIdentity(t *testing.T, app *App, ctx *sdk.AppCtx, clientID, provider, subject string, extra map[string]any) map[string]any {
	t.Helper()
	args := map[string]any{"client_id": clientID, "provider": provider, "provider_user_id": subject}
	for k, v := range extra {
		args[k] = v
	}
	out, err := app.toolPublicLoginIdentity(ctx, args)
	if err != nil {
		t.Fatalf("auth_public_login_identity(%s/%s): %v", provider, subject, err)
	}
	return out.(map[string]any)
}

func jwtPayload(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("malformed jwt %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("payload json: %v", err)
	}
	return m
}

func TestLoginIdentity_GuestCreateThenReturningLogin(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	app := &App{}

	first := loginIdentity(t, app, ctx, clientID, "device", "ios-device-abc", map[string]any{"display_name": "Player One"})
	if created, _ := first["created"].(bool); !created {
		t.Fatal("first login must create the guest")
	}
	user := first["user"].(*User)
	if user.Kind != "guest" {
		t.Errorf("kind = %q, want guest", user.Kind)
	}
	if user.Email != "" {
		t.Errorf("guest email must be blank in output, got %q", user.Email)
	}
	if user.HasPassword {
		t.Error("guest must not have a password")
	}
	if user.DisplayName != "Player One" {
		t.Errorf("display_name = %q", user.DisplayName)
	}
	access, _ := first["access_token"].(string)
	refresh, _ := first["refresh_token"].(string)
	if access == "" || refresh == "" {
		t.Fatalf("tokens missing: %v", first)
	}
	claims := jwtPayload(t, access)
	if claims["kind"] != "guest" {
		t.Errorf("jwt kind claim = %v, want guest", claims["kind"])
	}
	if _, has := claims["email"]; has {
		t.Errorf("guest jwt must not carry an email claim: %v", claims["email"])
	}

	rec := call(app.handleMe, "GET", "/me", nil, "Authorization", "Bearer "+access)
	if rec.Code != http.StatusOK {
		t.Fatalf("/me status=%d body=%s", rec.Code, rec.Body.String())
	}
	meUser := decode(t, rec)["user"].(map[string]any)
	if meUser["kind"] != "guest" {
		t.Errorf("/me kind = %v", meUser["kind"])
	}
	if meUser["email"] != "" {
		t.Errorf("/me email should be blank for guests, got %v", meUser["email"])
	}

	second := loginIdentity(t, app, ctx, clientID, "device", "ios-device-abc", nil)
	if created, _ := second["created"].(bool); created {
		t.Fatal("second login must reuse the guest")
	}
	if second["user"].(*User).ID != user.ID {
		t.Fatal("second login resolved a different user")
	}

	rec = callJSON(app.handleRefresh, "POST", "/refresh", map[string]any{"refresh_token": refresh, "client_id": clientID})
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLoginIdentity_UnknownWithoutCreateFails(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	app := &App{}
	_, err := app.toolPublicLoginIdentity(ctx, map[string]any{
		"client_id": clientID, "provider": "steam", "provider_user_id": "7656", "create_if_missing": false,
	})
	if err == nil || !strings.Contains(err.Error(), "identity_not_found") {
		t.Fatalf("expected identity_not_found, got %v", err)
	}
}

func TestLoginIdentity_RejectsMalformedProvider(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	app := &App{}
	for _, bad := range []string{"Steam!", "", "a", "has space"} {
		if _, err := app.toolPublicLoginIdentity(ctx, map[string]any{
			"client_id": clientID, "provider": bad, "provider_user_id": "x",
		}); err == nil {
			t.Errorf("provider %q should be rejected", bad)
		}
	}
	if _, err := app.toolPublicLoginIdentity(ctx, map[string]any{
		"client_id": clientID, "provider": "device", "provider_user_id": "",
	}); err == nil {
		t.Error("empty provider_user_id should be rejected")
	}
}

func TestLoginIdentity_RateLimitPerClient(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	ctx.Config()["identity_signups_per_client_per_hour"] = "2"
	app := &App{}

	loginIdentity(t, app, ctx, clientID, "device", "d1", nil)
	loginIdentity(t, app, ctx, clientID, "device", "d2", nil)
	_, err := app.toolPublicLoginIdentity(ctx, map[string]any{
		"client_id": clientID, "provider": "device", "provider_user_id": "d3",
	})
	if err == nil || !strings.Contains(err.Error(), "rate_limited") {
		t.Fatalf("third creation should be rate limited, got %v", err)
	}
	// Returning logins are not creations and stay allowed.
	again := loginIdentity(t, app, ctx, clientID, "device", "d1", nil)
	if created, _ := again["created"].(bool); created {
		t.Fatal("returning login must not create")
	}
}

func TestLoginIdentity_DisabledUserRejected(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	app := &App{}
	first := loginIdentity(t, app, ctx, clientID, "custom", "player-42", nil)
	uid := first["user"].(*User).ID
	if _, err := app.toolUsersDisable(ctx, map[string]any{"organization_slug": "default", "user_id": uid, "reason": "cheating"}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	_, err := app.toolPublicLoginIdentity(ctx, map[string]any{
		"client_id": clientID, "provider": "custom", "provider_user_id": "player-42",
	})
	if err == nil || !strings.Contains(err.Error(), "user_inactive") {
		t.Fatalf("disabled user must not log in, got %v", err)
	}
}

func TestIdentities_LinkListResolveUnlink(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	app := &App{}

	out, err := app.toolUsersCreate(ctx, map[string]any{
		"organization_slug": "default", "email": "alice@example.com", "password": "VerySafe!Pw#12345",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	alice := out.(map[string]any)["user"].(*User)

	linkOut, err := app.toolIdentitiesLink(ctx, map[string]any{
		"organization_slug": "default", "user_id": alice.ID, "provider": "steam", "provider_user_id": "7656119",
		"raw_profile": map[string]any{"persona": "alice_plays"},
	})
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	if linked, _ := linkOut.(map[string]any)["linked"].(bool); !linked {
		t.Fatal("first link should report linked=true")
	}
	// Idempotent re-link of the same pair to the same user.
	linkOut, err = app.toolIdentitiesLink(ctx, map[string]any{
		"organization_slug": "default", "user_id": alice.ID, "provider": "steam", "provider_user_id": "7656119",
	})
	if err != nil {
		t.Fatalf("re-link: %v", err)
	}
	if linked, _ := linkOut.(map[string]any)["linked"].(bool); linked {
		t.Fatal("re-link should report linked=false")
	}

	listOut, err := app.toolIdentitiesList(ctx, map[string]any{"organization_slug": "default", "user_id": alice.ID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if n, _ := listOut.(map[string]any)["count"].(int); n != 1 {
		t.Fatalf("count = %d, want 1", n)
	}
	resolveOut, err := app.toolIdentitiesList(ctx, map[string]any{
		"organization_slug": "default", "provider": "steam", "provider_user_id": "7656119",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if ids := resolveOut.(map[string]any)["identities"].([]Identity); len(ids) != 1 || ids[0].UserID != alice.ID {
		t.Fatalf("resolve returned %+v", ids)
	}

	// Logging in through the linked identity lands on Alice, not a new guest.
	login := loginIdentity(t, app, ctx, clientID, "steam", "7656119", nil)
	if created, _ := login["created"].(bool); created {
		t.Fatal("linked identity login must not create a user")
	}
	if u := login["user"].(*User); u.ID != alice.ID || u.Kind != "account" || u.Email != "alice@example.com" {
		t.Fatalf("login resolved %+v", u)
	}

	// The same subject cannot be bound to a second user.
	guest := loginIdentity(t, app, ctx, clientID, "device", "dev-x", nil)
	if _, err := app.toolIdentitiesLink(ctx, map[string]any{
		"organization_slug": "default", "user_id": guest["user"].(*User).ID, "provider": "steam", "provider_user_id": "7656119",
	}); err == nil || !strings.Contains(err.Error(), "identity_already_linked") {
		t.Fatalf("expected identity_already_linked, got %v", err)
	}

	unlinkOut, err := app.toolIdentitiesUnlink(ctx, map[string]any{
		"organization_slug": "default", "user_id": alice.ID, "provider": "steam",
	})
	if err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if n, _ := unlinkOut.(map[string]any)["removed"].(int64); n != 1 {
		t.Fatalf("removed = %d, want 1", n)
	}
	if _, err := app.toolPublicLoginIdentity(ctx, map[string]any{
		"client_id": clientID, "provider": "steam", "provider_user_id": "7656119", "create_if_missing": false,
	}); err == nil {
		t.Fatal("unlinked identity must no longer resolve")
	}
}

func TestIdentities_UnlinkRefusesToStrandGuest(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	app := &App{}
	guest := loginIdentity(t, app, ctx, clientID, "device", "only-device", nil)
	uid := guest["user"].(*User).ID
	if _, err := app.toolIdentitiesUnlink(ctx, map[string]any{
		"organization_slug": "default", "user_id": uid, "provider": "device",
	}); err == nil {
		t.Fatal("unlinking a guest's only identity must be refused")
	}
	// A second identity makes the first removable.
	if _, err := app.toolIdentitiesLink(ctx, map[string]any{
		"organization_slug": "default", "user_id": uid, "provider": "custom", "provider_user_id": "c-1",
	}); err != nil {
		t.Fatalf("link second: %v", err)
	}
	if _, err := app.toolIdentitiesUnlink(ctx, map[string]any{
		"organization_slug": "default", "user_id": uid, "provider": "device",
	}); err != nil {
		t.Fatalf("unlink with another identity left: %v", err)
	}
	// force removes the last one regardless.
	if _, err := app.toolIdentitiesUnlink(ctx, map[string]any{
		"organization_slug": "default", "user_id": uid, "provider": "custom", "force": true,
	}); err != nil {
		t.Fatalf("forced unlink: %v", err)
	}
}

func TestGuestUpgrade_BecomesAccountAndKeepsIdentity(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	app := &App{}
	guest := loginIdentity(t, app, ctx, clientID, "device", "dev-upgrade", nil)
	uid := guest["user"].(*User).ID

	if _, err := app.toolGuestUpgrade(ctx, map[string]any{
		"organization_slug": "default", "user_id": uid, "email": "bob@example.com", "password": "short",
	}); err == nil {
		t.Fatal("password policy must apply on upgrade")
	}
	out, err := app.toolGuestUpgrade(ctx, map[string]any{
		"organization_slug": "default", "user_id": uid, "email": "Bob@Example.com", "password": "VerySafe!Pw#12345",
		"display_name": "Bob",
	})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	u := out.(map[string]any)["user"].(*User)
	if u.Kind != "account" || u.Email != "bob@example.com" || !u.HasPassword || u.DisplayName != "Bob" {
		t.Fatalf("upgraded user = %+v", u)
	}
	if req, _ := out.(map[string]any)["verification_required"].(bool); req {
		t.Fatal("fixture disables email verification; upgrade should not require it")
	}
	// Email + password login now works …
	rec := callJSON(app.handleLogin, "POST", "/login", map[string]any{
		"email": "bob@example.com", "password": "VerySafe!Pw#12345", "client_id": clientID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login after upgrade status=%d body=%s", rec.Code, rec.Body.String())
	}
	// … and the device identity still resolves to the same, now-account user.
	again := loginIdentity(t, app, ctx, clientID, "device", "dev-upgrade", nil)
	if au := again["user"].(*User); au.ID != uid || au.Kind != "account" {
		t.Fatalf("device login after upgrade = %+v", au)
	}
	// A second upgrade is refused.
	if _, err := app.toolGuestUpgrade(ctx, map[string]any{
		"organization_slug": "default", "user_id": uid, "email": "bob2@example.com", "password": "VerySafe!Pw#12345",
	}); err == nil {
		t.Fatal("upgrading an account must fail")
	}
	// The email is now taken for signups.
	if _, err := app.toolPublicSignup(ctx, map[string]any{
		"email": "bob@example.com", "password": "VerySafe!Pw#12345", "client_id": clientID,
	}); err == nil {
		t.Fatal("signup with the upgraded email must conflict")
	}
}

func TestJWKSGet_MatchesDiscoveryRoute(t *testing.T) {
	ctx, _ := newAuthCtx(t)
	app := &App{}
	out, err := app.toolJWKSGet(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("jwks tool: %v", err)
	}
	m := out.(map[string]any)
	toolKeys := m["keys"].([]jwk)
	if len(toolKeys) == 0 {
		t.Fatal("tool returned no keys")
	}
	if m["organization_slug"] != "default" || m["issuer"] == "" {
		t.Fatalf("tool meta = %v", m)
	}
	rec := call(app.handleJWKS, "GET", "/.well-known/jwks.json", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("jwks route status=%d", rec.Code)
	}
	routeKeys := decode(t, rec)["keys"].([]any)
	if len(routeKeys) != len(toolKeys) {
		t.Fatalf("route has %d keys, tool has %d", len(routeKeys), len(toolKeys))
	}
	routeKid := routeKeys[0].(map[string]any)["kid"]
	if routeKid != toolKeys[0].Kid {
		t.Fatalf("kid mismatch route=%v tool=%v", routeKid, toolKeys[0].Kid)
	}
}
