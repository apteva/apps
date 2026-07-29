package main

import (
	"crypto/ed25519"
	"net/http"
	"reflect"
	"testing"
)

func TestRBAC_TrustedClaimsVersionAndRefresh(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	app := &App{}

	rec := callJSON(app.handleSignup, "POST", "/signup", map[string]any{
		"email":     "rbac@example.com",
		"password":  "VerySafe!Pw#12345",
		"client_id": clientID,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup: %d %s", rec.Code, rec.Body.String())
	}
	userID := int64(decode(t, rec)["user"].(map[string]any)["id"].(float64))

	view, err := dbCreatePermission(ctx.AppDB(), "test-proj", dbDefaultOrgID(ctx.AppDB(), "test-proj"),
		"documents:view", "View documents", "")
	if err != nil {
		t.Fatal(err)
	}
	edit, err := dbCreatePermission(ctx.AppDB(), "test-proj", dbDefaultOrgID(ctx.AppDB(), "test-proj"),
		"documents:edit", "Edit documents", "")
	if err != nil {
		t.Fatal(err)
	}
	role, err := dbCreateRole(ctx.AppDB(), "test-proj", dbDefaultOrgID(ctx.AppDB(), "test-proj"),
		"editor", "Editor", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbSetRolePermissions(ctx.AppDB(), "test-proj", role.OrganizationID, role.ID, []int64{view.ID, edit.ID}); err != nil {
		t.Fatal(err)
	}
	authorization, err := dbSetUserRoles(ctx.AppDB(), "test-proj", role.OrganizationID, userID, []int64{role.ID})
	if err != nil {
		t.Fatal(err)
	}
	if authorization.AuthorizationVersion != 2 {
		t.Fatalf("version after assignment=%d, want 2", authorization.AuthorizationVersion)
	}

	rec = callJSON(app.handleLogin, "POST", "/login", map[string]any{
		"email":     "rbac@example.com",
		"password":  "VerySafe!Pw#12345",
		"client_id": clientID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	login := decode(t, rec)
	access := login["access_token"].(string)
	refresh := login["refresh_token"].(string)
	authJSON := login["authorization"].(map[string]any)
	assertStringSet(t, authJSON["roles"], []string{"editor"})
	assertStringSet(t, authJSON["permissions"], []string{"documents:edit", "documents:view"})
	if got := int64(authJSON["authorization_version"].(float64)); got != 2 {
		t.Fatalf("login authorization_version=%d, want 2", got)
	}

	org, _ := dbGetOrgBySlug(ctx.AppDB(), "test-proj", "default")
	keys, _ := dbAllSigningKeys(ctx.AppDB(), "test-proj", org.ID)
	claims, err := jwtVerify(access, func(kid string) (ed25519.PublicKey, bool) {
		key, ok := keys[kid]
		return key, ok
	})
	if err != nil {
		t.Fatalf("verify access token: %v", err)
	}
	assertStringSet(t, claims["roles"], []string{"editor"})
	assertStringSet(t, claims["permissions"], []string{"documents:edit", "documents:view"})
	if claims["organization_id"] != uintToStr(org.ID) || claims["organization_slug"] != "default" {
		t.Fatalf("organization claims=%v/%v", claims["organization_id"], claims["organization_slug"])
	}

	// Removing a permission affects every assigned user of the role and
	// makes the previously issued access token stale.
	if _, err := dbSetRolePermissions(ctx.AppDB(), "test-proj", org.ID, role.ID, []int64{view.ID}); err != nil {
		t.Fatal(err)
	}
	current, err := dbAuthorizationContext(ctx.AppDB(), "test-proj", org, userID)
	if err != nil {
		t.Fatal(err)
	}
	if current.AuthorizationVersion != 3 {
		t.Fatalf("version after permission change=%d, want 3", current.AuthorizationVersion)
	}
	rec = call(app.handleMe, "GET", "/me", nil, "Authorization", "Bearer "+access)
	if rec.Code != http.StatusUnauthorized || decode(t, rec)["error"] != "stale_authorization" {
		t.Fatalf("stale /me: %d %s", rec.Code, rec.Body.String())
	}

	// Refresh sessions survive RBAC changes and mint the current context.
	rec = callJSON(app.handleRefresh, "POST", "/refresh", map[string]any{
		"refresh_token": refresh,
		"client_id":     clientID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body.String())
	}
	refreshed := decode(t, rec)
	refreshedAuth := refreshed["authorization"].(map[string]any)
	assertStringSet(t, refreshedAuth["permissions"], []string{"documents:view"})
	newAccess := refreshed["access_token"].(string)

	// Self-service metadata can use role-like keys without affecting the
	// server-managed authorization context.
	rec = call(app.handleMeMetadata, "PATCH", "/me/metadata", map[string]any{
		"metadata": map[string]any{
			"roles":       []string{"owner"},
			"permissions": []string{"*"},
		},
	}, "Authorization", "Bearer "+newAccess)
	if rec.Code != http.StatusOK {
		t.Fatalf("metadata update: %d %s", rec.Code, rec.Body.String())
	}
	rec = call(app.handleMe, "GET", "/me", nil, "Authorization", "Bearer "+newAccess)
	if rec.Code != http.StatusOK {
		t.Fatalf("me after metadata update: %d %s", rec.Code, rec.Body.String())
	}
	meAuth := decode(t, rec)["authorization"].(map[string]any)
	assertStringSet(t, meAuth["roles"], []string{"editor"})
	assertStringSet(t, meAuth["permissions"], []string{"documents:view"})

	// Reapplying the exact same assignment is idempotent.
	same, err := dbSetUserRoles(ctx.AppDB(), "test-proj", org.ID, userID, []int64{role.ID, role.ID})
	if err != nil {
		t.Fatal(err)
	}
	if same.AuthorizationVersion != 3 {
		t.Fatalf("idempotent assignment advanced version to %d", same.AuthorizationVersion)
	}
}

func TestRBAC_OrganizationIsolation(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	app := &App{}
	rec := callJSON(app.handleSignup, "POST", "/signup", map[string]any{
		"email":     "isolated@example.com",
		"password":  "VerySafe!Pw#12345",
		"client_id": clientID,
	})
	userID := int64(decode(t, rec)["user"].(map[string]any)["id"].(float64))
	defaultID := dbDefaultOrgID(ctx.AppDB(), "test-proj")
	acmeID, err := dbCreateOrg(ctx.AppDB(), "test-proj", "acme", "Acme", "")
	if err != nil {
		t.Fatal(err)
	}
	acmeRole, err := dbCreateRole(ctx.AppDB(), "test-proj", acmeID, "owner", "Owner", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbSetUserRoles(ctx.AppDB(), "test-proj", defaultID, userID, []int64{acmeRole.ID}); err == nil {
		t.Fatal("cross-organization role assignment unexpectedly succeeded")
	}
	defaultRole, err := dbCreateRole(ctx.AppDB(), "test-proj", defaultID, "reader", "Reader", "")
	if err != nil {
		t.Fatal(err)
	}
	acmePermission, err := dbCreatePermission(ctx.AppDB(), "test-proj", acmeID, "records:read", "Read records", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbSetRolePermissions(ctx.AppDB(), "test-proj", defaultID, defaultRole.ID, []int64{acmePermission.ID}); err == nil {
		t.Fatal("cross-organization permission assignment unexpectedly succeeded")
	}
}

func TestRBAC_DeletePermissionAdvancesAffectedUsersOnce(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	app := &App{}
	rec := callJSON(app.handleSignup, "POST", "/signup", map[string]any{
		"email":     "delete-permission@example.com",
		"password":  "VerySafe!Pw#12345",
		"client_id": clientID,
	})
	userID := int64(decode(t, rec)["user"].(map[string]any)["id"].(float64))
	org, _ := dbGetOrgBySlug(ctx.AppDB(), "test-proj", "default")
	permission, _ := dbCreatePermission(ctx.AppDB(), "test-proj", org.ID, "reports:view", "View reports", "")
	roleA, _ := dbCreateRole(ctx.AppDB(), "test-proj", org.ID, "auditor", "Auditor", "")
	roleB, _ := dbCreateRole(ctx.AppDB(), "test-proj", org.ID, "reviewer", "Reviewer", "")
	_, _ = dbSetRolePermissions(ctx.AppDB(), "test-proj", org.ID, roleA.ID, []int64{permission.ID})
	_, _ = dbSetRolePermissions(ctx.AppDB(), "test-proj", org.ID, roleB.ID, []int64{permission.ID})
	before, err := dbSetUserRoles(ctx.AppDB(), "test-proj", org.ID, userID, []int64{roleA.ID, roleB.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbDeletePermission(ctx.AppDB(), "test-proj", org.ID, permission.ID); err != nil {
		t.Fatal(err)
	}
	after, err := dbAuthorizationContext(ctx.AppDB(), "test-proj", org, userID)
	if err != nil {
		t.Fatal(err)
	}
	if after.AuthorizationVersion != before.AuthorizationVersion+1 {
		t.Fatalf("delete advanced version %d -> %d, want exactly once", before.AuthorizationVersion, after.AuthorizationVersion)
	}
	if len(after.Permissions) != 0 {
		t.Fatalf("deleted permission remains effective: %v", after.Permissions)
	}
}

func TestRBAC_MCPManagementSurface(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	app := &App{}
	rec := callJSON(app.handleSignup, "POST", "/signup", map[string]any{
		"email":     "mcp-rbac@example.com",
		"password":  "VerySafe!Pw#12345",
		"client_id": clientID,
	})
	userID := int64(decode(t, rec)["user"].(map[string]any)["id"].(float64))

	permissionOut, err := app.toolPermissionsCreate(ctx, map[string]any{
		"organization_slug": "default",
		"key":               "projects:read",
		"name":              "Read projects",
	})
	if err != nil {
		t.Fatalf("create permission tool: %v", err)
	}
	permission := permissionOut.(map[string]any)["permission"].(*Permission)
	roleOut, err := app.toolRolesCreate(ctx, map[string]any{
		"organization_slug": "default",
		"key":               "viewer",
		"name":              "Viewer",
	})
	if err != nil {
		t.Fatalf("create role tool: %v", err)
	}
	role := roleOut.(map[string]any)["role"].(*Role)
	if _, err := app.toolRolePermissionsSet(ctx, map[string]any{
		"organization_slug": "default",
		"role_id":           role.ID,
		"permission_ids":    []any{float64(permission.ID)},
	}); err != nil {
		t.Fatalf("set role permissions tool: %v", err)
	}
	userOut, err := app.toolUserRolesSet(ctx, map[string]any{
		"organization_slug": "default",
		"user_id":           userID,
		"role_ids":          []any{float64(role.ID)},
	})
	if err != nil {
		t.Fatalf("set user roles tool: %v", err)
	}
	authorization := userOut.(map[string]any)["authorization"].(AuthorizationContext)
	if !reflect.DeepEqual(authorization.Roles, []string{"viewer"}) ||
		!reflect.DeepEqual(authorization.Permissions, []string{"projects:read"}) {
		t.Fatalf("unexpected authorization context: %+v", authorization)
	}
	listOut, err := app.toolRolesList(ctx, map[string]any{"organization_slug": "default"})
	if err != nil {
		t.Fatal(err)
	}
	listed := listOut.(map[string]any)["roles"].([]Role)
	if len(listed) != 1 || !reflect.DeepEqual(listed[0].Permissions, []string{"projects:read"}) {
		t.Fatalf("roles list=%+v", listed)
	}
}

func assertStringSet(t *testing.T, raw any, want []string) {
	t.Helper()
	var got []string
	switch values := raw.(type) {
	case []string:
		got = append(got, values...)
	case []any:
		for _, value := range values {
			if s, ok := value.(string); ok {
				got = append(got, s)
			}
		}
	default:
		t.Fatalf("value %T is not a string array: %#v", raw, raw)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
