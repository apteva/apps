package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestVerifySession(t *testing.T) {
	app := &App{}
	dbCtx, clientID := newAuthCtx(t)
	const project = "test-proj"
	caller := sdk.WithCaller(t.Context(), &sdk.Caller{AppInstallID: 42, AppName: "api", ProjectID: project})
	rec := callJSON(app.handleSignup, "POST", "/signup", map[string]any{
		"email": "session@example.com", "password": "VerySafe!Pw#12345", "client_id": clientID,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup status = %d", rec.Code)
	}
	signup := decode(t, rec)
	userID := int64(signup["user"].(map[string]any)["id"].(float64))
	access := signup["access_token"].(string)
	refresh := signup["refresh_token"].(string)
	args := func() map[string]any {
		return map[string]any{"credential": map[string]any{"type": "bearer", "token": access}}
	}
	checkRejected := func(t *testing.T) {
		t.Helper()
		result, err := app.toolVerifySession(caller, dbCtx, args())
		if err != nil || !reflect.DeepEqual(result, map[string]any{"authenticated": false}) {
			t.Fatalf("expected credential rejection, got %v (error %v)", result, err)
		}
	}

	for _, tc := range []struct {
		name   string
		caller *sdk.Caller
		scope  map[string]any
	}{
		{name: "no caller"},
		{name: "agent", caller: &sdk.Caller{AgentID: 1, ProjectID: project}},
		{name: "no project", caller: &sdk.Caller{AppInstallID: 42, AppName: "api"}},
		{name: "install mismatch", caller: &sdk.Caller{AppInstallID: 42, AppName: "api", ProjectID: "other"}},
		{name: "project argument mismatch", caller: sdk.CallerFrom(caller), scope: map[string]any{"project_id": "other"}},
		{name: "SDK project argument mismatch", caller: sdk.CallerFrom(caller), scope: map[string]any{"_project_id": "other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := args()
			for k, v := range tc.scope {
				input[k] = v
			}
			ctx := sdk.WithCaller(t.Context(), tc.caller)
			result, err := app.toolVerifySession(ctx, dbCtx, input)
			if result != nil || err == nil || strings.Contains(err.Error(), access) {
				t.Fatal("accepted untrusted caller scope or exposed the credential")
			}
		})
	}

	orgID := dbDefaultOrgID(dbCtx.AppDB(), project)
	permission, err := dbCreatePermission(dbCtx.AppDB(), project, orgID, "centres:read", "Read centres", "")
	if err != nil {
		t.Fatal(err)
	}
	role, err := dbCreateRole(dbCtx.AppDB(), project, orgID, "lab-user", "Lab user", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbSetRolePermissions(dbCtx.AppDB(), project, orgID, role.ID, []int64{permission.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := dbSetUserRoles(dbCtx.AppDB(), project, orgID, userID, []int64{role.ID}); err != nil {
		t.Fatal(err)
	}
	t.Run("stale authorization", checkRejected)
	rec = callJSON(app.handleRefresh, "POST", "/refresh", map[string]any{"client_id": clientID, "refresh_token": refresh})
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d", rec.Code)
	}
	refreshed := decode(t, rec)
	access = refreshed["access_token"].(string)
	refresh = refreshed["refresh_token"].(string)

	t.Run("verified principal and unchanged me response", func(t *testing.T) {
		result, err := app.toolVerifySession(caller, dbCtx, args())
		if err != nil {
			t.Fatal(err)
		}
		out := result.(map[string]any)
		principal := out["principal"].(map[string]any)
		authorization := principal["claims"].(AuthorizationContext)
		keys, err := dbAllSigningKeys(dbCtx.AppDB(), project, orgID)
		if err != nil {
			t.Fatal(err)
		}
		jwt, err := jwtVerify(access, func(kid string) (ed25519.PublicKey, bool) {
			key, ok := keys[kid]
			return key, ok
		})
		if err != nil {
			t.Fatal(err)
		}
		exp, _ := jwtInt64Claim(jwt, "exp")
		if out["authenticated"] != true || out["expires_at"] != rfc3339(time.Unix(exp, 0)) ||
			principal["project_id"] != project || principal["issuer"] != "apteva:auth:default" ||
			principal["subject"] != uintToStr(userID) || principal["tenant_id"] != "default" ||
			!reflect.DeepEqual(authorization.Permissions, []string{"centres:read"}) {
			t.Fatalf("unexpected verified principal: %+v", out)
		}
		raw, _ := json.Marshal(out)
		if strings.Contains(string(raw), access) {
			t.Fatal("credential leaked in tool response")
		}

		rec := call(app.handleMe, "GET", "/me", nil, "Authorization", "Bearer "+access)
		me := decode(t, rec)
		if rec.Code != http.StatusOK || len(me) != 3 || me["org"] != "default" || me["user"] == nil {
			t.Fatalf("unexpected /me response: status %d, body %v", rec.Code, me)
		}
		meAuthorization, _ := json.Marshal(me["authorization"])
		var decoded AuthorizationContext
		if err := json.Unmarshal(meAuthorization, &decoded); err != nil || !reflect.DeepEqual(decoded, authorization) {
			t.Fatalf("/me authorization = %+v (error %v)", decoded, err)
		}
	})

	t.Run("revoked session", func(t *testing.T) {
		rec := callJSON(app.handleLogout, "POST", "/logout", map[string]any{"client_id": clientID, "refresh_token": refresh})
		if rec.Code != http.StatusNoContent {
			t.Fatalf("logout status = %d", rec.Code)
		}
		checkRejected(t)
		rec = call(app.handleMe, "GET", "/me", nil, "Authorization", "Bearer "+access)
		if rec.Code != http.StatusUnauthorized || decode(t, rec)["error"] != "session_revoked" {
			t.Fatalf("revoked /me: %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("session database failure", func(t *testing.T) {
		if _, err := dbCtx.AppDB().Exec(`ALTER TABLE auth_session_families RENAME TO unavailable_families`); err != nil {
			t.Fatal(err)
		}
		checkSessionServiceFailure(t, app, caller, dbCtx, args())
		rec := call(app.handleMe, "GET", "/me", nil, "Authorization", "Bearer "+access)
		if rec.Code != http.StatusInternalServerError || decode(t, rec)["error"] != "auth_service_unavailable" {
			t.Fatalf("/me service failure: %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("issuer unavailable", func(t *testing.T) {
		unconfigured := sdk.NewAppCtxForTest(nil, dbCtx.AppDB(), nil, nil, dbCtx.Logger())
		checkSessionServiceFailure(t, app, caller, unconfigured, args())
	})
}

func checkSessionServiceFailure(t *testing.T, app *App, caller context.Context, appCtx *sdk.AppCtx, input map[string]any) {
	t.Helper()
	result, err := app.toolVerifySession(caller, appCtx, input)
	if result != nil || !errors.Is(err, errSessionUnavailable) {
		t.Fatalf("service failure was treated as credential rejection: %v, %v", result, err)
	}
}
