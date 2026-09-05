package main

// Regression tests for the independently reproduced v0.10.0 audit findings.
import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRegressionInvalidOrgWidensClientAndSearch(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	a := &App{}
	callJSON(a.handleSignup, "POST", "/signup", map[string]any{"email": "scope@example.com", "password": "GoodPassword123", "client_id": cid})
	_, err := a.toolClientsCreate(ctx, map[string]any{"organization_slug": "typo-missing", "name": "scoped", "type": "spa"})
	if err == nil {
		t.Fatal("invalid org broadened client scope")
	}
	if _, err = a.toolUsersSearch(ctx, map[string]any{"organization_slug": "typo-missing"}); err == nil {
		t.Fatal("invalid org broadened search scope")
	}

}

func TestRegressionMFAAndClientPolicyIgnored(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	a := &App{}
	rec := callJSON(a.handleSignup, "POST", "/signup", map[string]any{"email": "mfa@example.com", "password": "GoodPassword123", "client_id": cid})
	uid := int64(decode(t, rec)["user"].(map[string]any)["id"].(float64))
	_, err := ctx.AppDB().Exec(`UPDATE clients SET require_mfa=1, type='m2m', allowed_grant_types='["client_credentials"]', token_endpoint_auth_method='client_secret_post', client_secret_hash='unused' WHERE client_id=?`, cid)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ctx.AppDB().Exec(`INSERT INTO mfa_factors(project_id,organization_id,user_id,kind,secret_encrypted,confirmed_at) VALUES('test-proj',1,?,'totp','fixture',?)`, uid, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	rec = callJSON(a.handleLogin, "POST", "/login", map[string]any{"email": "mfa@example.com", "password": "GoodPassword123", "client_id": cid})
	if rec.Code < 400 || decode(t, rec)["access_token"] != nil {
		t.Fatalf("not reproduced: %d", rec.Code)
	}

}

func TestRegressionAccessSurvivesRevocationAndClientDisable(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	a := &App{}
	rec := callJSON(a.handleSignup, "POST", "/signup", map[string]any{"email": "revoke@example.com", "password": "GoodPassword123", "client_id": cid})
	out := decode(t, rec)
	access := out["access_token"].(string)
	callJSON(a.handleLogout, "POST", "/logout", map[string]any{"refresh_token": out["refresh_token"]})
	if rec := call(a.handleMe, "GET", "/me", nil, "Authorization", "Bearer "+access); rec.Code != 401 {
		t.Fatal("logout failed to revoke access")
	}
	if err := dbDisableClient(ctx.AppDB(), "test-proj", cid); err != nil {
		t.Fatal(err)
	}
	if rec := call(a.handleMe, "GET", "/me", nil, "Authorization", "Bearer "+access); rec.Code != 401 {
		t.Fatal("disabled client retained access")
	}

}

func TestRegressionArchivedOrgAccess(t *testing.T) {
	ctx, _ := newAuthCtx(t)
	a := &App{}
	oid, err := dbCreateOrg(ctx.AppDB(), "test-proj", "archived", "Archived", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureSigningKey(ctx.AppDB(), "test-proj", oid); err != nil {
		t.Fatal(err)
	}
	c, err := a.toolClientsCreate(ctx, map[string]any{"organization_slug": "archived", "name": "client", "type": "spa"})
	if err != nil {
		t.Fatal(err)
	}
	cid := c.(map[string]any)["client_id"].(string)
	out := decode(t, callJSON(a.handleSignup, "POST", "/signup", map[string]any{"email": "archive@example.com", "password": "GoodPassword123", "client_id": cid}))
	if err := dbArchiveOrg(ctx.AppDB(), "test-proj", oid); err != nil {
		t.Fatal(err)
	}
	rec := call(a.handleMe, "GET", "/me", nil, "Authorization", "Bearer "+out["access_token"].(string))
	if rec.Code != 401 {
		t.Fatal("archived organization retained access")
	}

}

func TestRegressionDisabledUserRedeemsRecovery(t *testing.T) {
	for _, flow := range []string{"verify_email", "reset_password"} {
		t.Run(flow, func(t *testing.T) {
			a, cid, org, _, uid := seedRecoveryUser(t, "disabled@example.com", "GoodPassword123")
			insertRecoveryToken(t, org.ID, uid, flow, "recovery-fixture", cid)
			if err := dbSetUserStatus(globalCtx.AppDB(), "test-proj", org.ID, uid, "disabled"); err != nil {
				t.Fatal(err)
			}
			handler := a.handleEmailVerify
			if flow == "reset_password" {
				handler = a.handlePasswordResetConfirm
			}
			rec := callJSON(handler, "POST", "/recovery", map[string]any{"client_id": cid, "token": "recovery-fixture", "password": "ChangedPassword123"})
			if rec.Code < 400 || decode(t, rec)["access_token"] != nil {
				t.Fatalf("not reproduced: %d %s", rec.Code, rec.Body)
			}

		})
	}
}

func TestRegressionResetBurnsTokenBeforePasswordValidation(t *testing.T) {
	a, cid, org, _, uid := seedRecoveryUser(t, "trim@example.com", "GoodPassword123")
	insertRecoveryToken(t, org.ID, uid, "reset_password", "trim-token", cid)
	body := map[string]any{"client_id": cid, "token": "trim-token", "password": "short"}
	rec := callJSON(a.handlePasswordResetConfirm, "POST", "/password/reset/confirm", body)
	if rec.Code != 400 {
		t.Fatalf("first status %d", rec.Code)
	}
	body["password"] = "ChangedPassword123"
	rec = callJSON(a.handlePasswordResetConfirm, "POST", "/password/reset/confirm", body)
	if rec.Code != 200 {
		t.Fatal("not reproduced")
	}

}

func TestRegressionOldResetTokenSurvivesPasswordChange(t *testing.T) {
	a, cid, org, _, uid := seedRecoveryUser(t, "old-reset@example.com", "GoodPassword123")
	insertRecoveryToken(t, org.ID, uid, "reset_password", "older-reset", cid)
	if _, _, err := a.setUserPassword(globalCtx, "test-proj", org.ID, uid, "OwnerNewPassword123", true, "audit", "", "", ""); err != nil {
		t.Fatal(err)
	}
	rec := callJSON(a.handlePasswordResetConfirm, "POST", "/password/reset/confirm", map[string]any{"client_id": cid, "token": "older-reset", "password": "OlderTokenWins123"})
	if rec.Code != 400 {
		t.Fatalf("old token accepted: %d", rec.Code)
	}

}

func TestRecoveryLandingAndAuditSecrecy(t *testing.T) {
	a, cid, org, _, uid := seedRecoveryUser(t, "audit@example.com", "GoodPassword123")
	if err := issueResetToken(globalCtx, "test-proj", org, uid, "audit@example.com"); err == nil {
		t.Fatal("unconfigured delivery reported success")
	}
	u, err := url.Parse(recoveryLink(globalCtx, org, "", "reset", "secret-fixture", "/password/reset"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("token") != "" || !strings.Contains(u.Fragment, "secret-fixture") {
		t.Fatal("token must be in fragment")
	}
	mux := http.NewServeMux()
	for _, route := range a.HTTPRoutes() {
		pattern := route.Pattern
		if route.Method != "" {
			pattern = route.Method + " " + pattern
		}
		mux.HandleFunc(pattern, route.Handler)
	}
	u.Fragment = "" // Browsers never send fragments to the HTTP server.
	landing := call(mux.ServeHTTP, "GET", u.String(), nil)
	if landing.Code != 200 {
		t.Fatalf("landing status %d: %s", landing.Code, landing.Body)
	}
	if !strings.Contains(landing.Body.String(), "history.replaceState") || !strings.Contains(landing.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("missing recovery page safeguards")
	}
	_ = cid
}

func TestRegressionRefreshConcurrentReplay(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	a := &App{}
	out := decode(t, callJSON(a.handleSignup, "POST", "/signup", map[string]any{"email": "race@example.com", "password": "GoodPassword123", "client_id": cid}))
	raw := out["refresh_token"].(string)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := callJSON(a.handleRefresh, "POST", "/refresh", map[string]any{"client_id": cid, "refresh_token": raw})
			if rec.Code == 200 {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if success != 1 {
		t.Fatalf("refresh successes=%d, want 1", success)
	}
	var live int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM sessions WHERE revoked_at IS NULL`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("replayed family kept %d active credentials", live)
	}
}

func TestRegressionNonRotatingRefreshFansOut(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	a := &App{}
	if _, err := ctx.AppDB().Exec(`UPDATE clients SET refresh_rotation=0 WHERE client_id=?`, cid); err != nil {
		t.Fatal(err)
	}
	out := decode(t, callJSON(a.handleSignup, "POST", "/signup", map[string]any{"email": "no-rotate@example.com", "password": "GoodPassword123", "client_id": cid}))
	for i := 0; i < 3; i++ {
		rec := callJSON(a.handleRefresh, "POST", "/refresh", map[string]any{"client_id": cid, "refresh_token": out["refresh_token"]})
		if rec.Code != 200 {
			t.Fatal(rec.Code)
		}
	}
	var live int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM sessions WHERE revoked_at IS NULL`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Fatalf("not reproduced: %d", live)
	}
}

func TestRegressionIdentityInsertFailureLeavesOrphan(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	a := &App{}
	if _, err := ctx.AppDB().Exec(`CREATE TRIGGER audit_fail_identity BEFORE INSERT ON oauth_identities BEGIN SELECT RAISE(ABORT,'simulated identity insert failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := a.toolPublicLoginIdentity(ctx, map[string]any{"client_id": cid, "provider": "device", "provider_user_id": "orphan"})
	if err == nil {
		t.Fatal("expected error")
	}
	var users, ids int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users)
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM oauth_identities`).Scan(&ids)
	if users != 0 || ids != 0 {
		t.Fatalf("not reproduced users=%d ids=%d", users, ids)
	}
}

func TestRegressionOrgPolicyIgnored(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	a := &App{}
	policy := `{"password_min_length":32,"email_verification_required":true}`
	if err := dbUpdateOrg(ctx.AppDB(), "test-proj", 1, nil, nil, &policy); err != nil {
		t.Fatal(err)
	}
	rec := callJSON(a.handleSignup, "POST", "/signup", map[string]any{"email": "policy@example.com", "password": "short123", "client_id": cid})
	if rec.Code != 400 || decode(t, rec)["access_token"] != nil {
		t.Fatal("not reproduced")
	}
}

func TestRegressionMFASearchFiltersAfterLimit(t *testing.T) {
	ctx, _ := newAuthCtx(t)
	a := &App{}
	for i := 0; i < 2; i++ {
		uid, err := dbCreateUser(ctx.AppDB(), "test-proj", 1, fmt.Sprintf("search%d@example.com", i), "", "", true, "{}")
		if err != nil {
			t.Fatal(err)
		}
		ctx.AppDB().Exec(`UPDATE users SET created_at=? WHERE id=?`, fmt.Sprintf("2026-09-0%dT00:00:00Z", i+1), uid)
		if i == 0 {
			ctx.AppDB().Exec(`INSERT INTO mfa_factors(project_id,organization_id,user_id,kind,secret_encrypted,confirmed_at) VALUES('test-proj',1,?,'totp','fixture','2026-09-01T00:00:00Z')`, uid)
		}
	}
	out, err := a.toolUsersSearch(ctx, map[string]any{"organization_slug": "default", "limit": 1, "mfa": true})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["count"].(int) != 1 {
		t.Fatal("not reproduced")
	}
}

func TestRegressionUpdatingDisabledClientRestoresCORS(t *testing.T) {
	stub := &browserOriginStub{}
	ctx := newBrowserOriginCtx(t, "test-proj", stub)
	a := &App{}
	out, err := a.toolClientsCreate(ctx, map[string]any{"name": "cors", "type": "spa", "allowed_origins": []string{"https://one.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	cid := out.(map[string]any)["client_id"].(string)
	if _, err := a.toolClientsDisable(ctx, map[string]any{"client_id": cid}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.toolClientsUpdate(ctx, map[string]any{"client_id": cid, "add_allowed_origins": []string{"https://two.example.com"}}); err == nil {
		t.Fatal("disabled client update succeeded")
	}
	if len(stub.registrations[browserOriginRegistrationKey(cid)]) != 0 {
		t.Fatal("not reproduced")
	}
}

func TestRegressionIdentityQuotaConcurrentOvershoot(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	a := &App{}
	ctx.Config()["identity_signups_per_client_per_hour"] = "1"
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := a.toolPublicLoginIdentity(ctx, map[string]any{"client_id": cid, "provider": "device", "provider_user_id": fmt.Sprintf("quota-%d", i)})
			if err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if success != 1 {
		t.Fatalf("quota permitted %d, want 1", success)
	}
	t.Logf("client configured for one identity per hour created %d concurrently", success)
}

func TestRegressionMalformedRoleIDsClearAssignments(t *testing.T) {
	ctx, _ := newAuthCtx(t)
	a := &App{}
	uid, err := dbCreateUser(ctx.AppDB(), "test-proj", 1, "roles@example.com", "", "", true, "{}")
	if err != nil {
		t.Fatal(err)
	}
	role, err := dbCreateRole(ctx.AppDB(), "test-proj", 1, "admin", "Admin", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbSetUserRoles(ctx.AppDB(), "test-proj", 1, uid, []int64{role.ID}); err != nil {
		t.Fatal(err)
	}
	_, err = a.toolUserRolesSet(ctx, map[string]any{"organization_slug": "default", "user_id": uid, "role_ids": []any{"not-an-integer"}})
	if err == nil {
		t.Fatal("malformed IDs accepted")
	}
	org, _ := dbGetOrgByID(ctx.AppDB(), "test-proj", 1)
	auth, err := dbAuthorizationContext(ctx.AppDB(), "test-proj", org, uid)
	if err != nil || len(auth.Roles) != 1 {
		t.Fatal("malformed input changed roles")
	}

}

func TestRegressionPasswordWhitespaceChanged(t *testing.T) {
	a, cid, org, _, uid := seedRecoveryUser(t, "spaces@example.com", "GoodPassword123")
	password := "  SpacedPassword123  "
	if _, _, err := a.setUserPassword(globalCtx, "test-proj", org.ID, uid, password, true, "audit", "", "", ""); err != nil {
		t.Fatal(err)
	}
	rec := callJSON(a.handleLogin, "POST", "/login", map[string]any{"client_id": cid, "email": "spaces@example.com", "password": password})
	if rec.Code != 200 {
		t.Fatalf("exact password rejected: %d", rec.Code)
	}
	rec = callJSON(a.handleLogin, "POST", "/login", map[string]any{"client_id": cid, "email": "spaces@example.com", "password": strings.TrimSpace(password)})
	if rec.Code != 401 {
		t.Fatal("trimmed password accepted")
	}
}
