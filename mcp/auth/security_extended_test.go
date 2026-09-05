package main

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestStrictSelectorsAndRBACArrays(t *testing.T) {
	ctx, _ := newAuthCtx(t)
	a := &App{}
	for _, v := range []any{nil, "1", 1.5, 0, -1, json.Number("9223372036854775808"), json.Number("1.2")} {
		if _, err := a.toolUsersSearch(ctx, map[string]any{"organization_id": v}); err == nil {
			t.Errorf("organization_id accepted %#v", v)
		}
	}
	for _, q := range []string{"organization_slug=", "organization_id=", "organization_id=1&organization_slug=missing", "organization_id=1&organization_id=2", "organization_id=9223372036854775808"} {
		rec := call(a.handleAdminUsersList, "GET", "/admin/users?"+q, nil)
		if rec.Code < 400 {
			t.Errorf("selector accepted %s", q)
		}
	}
	for _, v := range []any{nil, "1", []any{"1"}, []any{0}, []any{1.1}, []any{json.Number("9223372036854775808")}, []any{true}, map[string]any{}} {
		if err := validateIDArray(map[string]any{"ids": v}, "ids"); err == nil {
			t.Errorf("IDs accepted %#v", v)
		}
	}
	for _, v := range []any{[]any{}, []any{json.Number("1"), float64(2)}, []int64{1, 2}} {
		if err := validateIDArray(map[string]any{"ids": v}, "ids"); err != nil {
			t.Errorf("valid IDs rejected: %v", err)
		}
	}
	if _, err := a.toolUsersSearch(ctx, map[string]any{"organization_id": json.Number("1")}); err != nil {
		t.Fatal("SDK json.Number compatibility", err)
	}
}

func TestConfidentialClientRequiresSecretAndM2MIsRejected(t *testing.T) {
	ctx, _ := newAuthCtx(t)
	a := &App{}
	out, err := a.toolClientsCreate(ctx, map[string]any{"name": "confidential", "type": "web", "organization_slug": "default"})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	cid := result["client_id"].(string)
	secret := result["client_secret"].(string)
	body := map[string]any{"client_id": cid, "email": "secret@example.com", "password": "GoodPassword123"}
	for _, wrong := range []string{"", "incorrect"} {
		body["client_secret"] = wrong
		if r := callJSON(a.handleSignup, "POST", "/signup", body); r.Code != 401 {
			t.Fatalf("bad secret accepted: %d", r.Code)
		}
	}
	body["client_secret"] = secret
	signup := callJSON(a.handleSignup, "POST", "/signup", body)
	if signup.Code != 201 {
		t.Fatal(signup.Body.String())
	}
	delete(body, "client_secret")
	if r := callJSON(a.handleLogin, "POST", "/login", body); r.Code != 401 {
		t.Fatalf("login did not require secret: %d", r.Code)
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/login", strings.NewReader(string(raw)))
	req.SetBasicAuth(cid, secret)
	rec := httptest.NewRecorder()
	a.handleLogin(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	refresh := decode(t, rec)["refresh_token"]
	if r := callJSON(a.handleRefresh, "POST", "/refresh", map[string]any{"client_id": cid, "refresh_token": refresh}); r.Code != 401 {
		t.Fatal("refresh secret bypass")
	}
	if r := callJSON(a.handleRefresh, "POST", "/refresh", map[string]any{"client_id": cid, "client_secret": secret, "refresh_token": refresh}); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if _, err = ctx.AppDB().Exec(`UPDATE clients SET type='m2m' WHERE client_id=?`, cid); err != nil {
		t.Fatal(err)
	}
	body["client_secret"] = secret
	if r := callJSON(a.handleLogin, "POST", "/login", body); r.Code < 400 {
		t.Fatal("M2M password login accepted")
	}
}

func TestMFAFailClosedAcrossIssuancePaths(t *testing.T) {
	for _, mode := range []string{"client", "factor"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cid := newAuthCtx(t)
			a := &App{}
			out := decode(t, callJSON(a.handleSignup, "POST", "/signup", map[string]any{"client_id": cid, "email": "mfa@example.com", "password": "GoodPassword123"}))
			uid := int64(out["user"].(map[string]any)["id"].(float64))
			var err error
			if mode == "client" {
				_, err = ctx.AppDB().Exec(`UPDATE clients SET require_mfa=1 WHERE client_id=?`, cid)
			} else {
				_, err = ctx.AppDB().Exec(`INSERT INTO mfa_factors(project_id,organization_id,user_id,kind,secret_encrypted,confirmed_at) VALUES('test-proj',1,?,'totp','fixture',?)`, uid, rfc3339(time.Now()))
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range []*httptest.ResponseRecorder{
				callJSON(a.handleLogin, "POST", "/login", map[string]any{"client_id": cid, "email": "mfa@example.com", "password": "GoodPassword123"}),
				callJSON(a.handleRefresh, "POST", "/refresh", map[string]any{"client_id": cid, "refresh_token": out["refresh_token"]}),
				call(a.handleMe, "GET", "/me", nil, "Authorization", "Bearer "+out["access_token"].(string)),
			} {
				if r.Code < 400 {
					t.Fatalf("MFA bypass %d %s", r.Code, r.Body)
				}
			}
			if mode == "client" {
				if _, err := a.toolPublicLoginIdentity(ctx, map[string]any{"client_id": cid, "provider": "device", "provider_user_id": "mfa-device"}); err == nil {
					t.Fatal("identity MFA bypass")
				}
			}
		})
	}
}

func TestRecoveryFailureRollsBackTokenPasswordAndRevocation(t *testing.T) {
	a, cid, org, _, uid := seedRecoveryUser(t, "rollback@example.com", "GoodPassword123")
	insertRecoveryToken(t, org.ID, uid, "reset_password", "rollback-token", cid)
	db := globalCtx.AppDB()
	if _, err := db.Exec(`CREATE TRIGGER fail_recovery BEFORE UPDATE OF email_verified_at ON users BEGIN SELECT RAISE(ABORT,'fault'); END`); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"client_id": cid, "token": "rollback-token", "password": "NewPassword123"}
	rec := callJSON(a.handlePasswordResetConfirm, "POST", "/password/reset/confirm", body)
	if rec.Code != 500 {
		t.Fatalf("fault not exercised %d %s", rec.Code, rec.Body)
	}
	var used sql.NullString
	if err := db.QueryRow(`SELECT used_at FROM verification_tokens WHERE token_hash=?`, hashToken("rollback-token")).Scan(&used); err != nil || used.Valid {
		t.Fatal("token partially consumed", err)
	}
	hash, err := dbGetUserPasswordHash(db, "test-proj", org.ID, uid)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := verifyPassword(hash, "GoodPassword123"); !ok {
		t.Fatal("password partially committed")
	}
	if _, err = db.Exec(`DROP TRIGGER fail_recovery`); err != nil {
		t.Fatal(err)
	}
	if rec = callJSON(a.handlePasswordResetConfirm, "POST", "/password/reset/confirm", body); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if decode(t, rec)["login_required"] != true {
		t.Fatal("recovery must require login")
	}
}

func TestSignupSessionFailureDoesNotLeaveAccount(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	a := &App{}
	if _, err := ctx.AppDB().Exec(`CREATE TRIGGER fail_session BEFORE INSERT ON sessions BEGIN SELECT RAISE(ABORT,'fault'); END`); err != nil {
		t.Fatal(err)
	}
	rec := callJSON(a.handleSignup, "POST", "/signup", map[string]any{"client_id": cid, "email": "rollback@example.com", "password": "GoodPassword123"})
	if rec.Code != 500 {
		t.Fatal(rec.Code)
	}
	var users, families int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM auth_session_families`).Scan(&families); err != nil {
		t.Fatal(err)
	}
	if users != 0 || families != 0 {
		t.Fatalf("partial signup users=%d families=%d", users, families)
	}
}

func TestCredentialEpochPreventsOldPasswordCheckMint(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	a := &App{}
	out := decode(t, callJSON(a.handleSignup, "POST", "/signup", map[string]any{"client_id": cid, "email": "epoch@example.com", "password": "GoodPassword123"}))
	uid := int64(out["user"].(map[string]any)["id"].(float64))
	org, _ := dbGetOrgByID(ctx.AppDB(), "test-proj", 1)
	user, _ := dbGetUserByID(ctx.AppDB(), "test-proj", 1, uid)
	client, _ := dbGetClientByClientID(ctx.AppDB(), "test-proj", cid)
	if _, _, err := a.setUserPassword(ctx, "test-proj", 1, uid, "NewPassword123", true, "password_change", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := mintSession(ctx, "test-proj", org, user, client, httptest.NewRequest("POST", "/login", nil)); err == nil {
		t.Fatal("stale credential proof minted a session")
	}
}

func TestRefreshLifetimeAndStableCredential(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	a := &App{}
	out := decode(t, callJSON(a.handleSignup, "POST", "/signup", map[string]any{"client_id": cid, "email": "lifetime@example.com", "password": "GoodPassword123"}))
	var family, expiry string
	if err := ctx.AppDB().QueryRow(`SELECT family_id,expires_at FROM sessions WHERE refresh_token_hash=?`, hashToken(out["refresh_token"].(string))).Scan(&family, &expiry); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		r := callJSON(a.handleRefresh, "POST", "/refresh", map[string]any{"client_id": cid, "refresh_token": out["refresh_token"]})
		if r.Code != 200 {
			t.Fatal(r.Body.String())
		}
		out = decode(t, r)
	}
	var bad int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM sessions WHERE family_id<>? OR expires_at<>?`, family, expiry).Scan(&bad); err != nil || bad != 0 {
		t.Fatal("refresh extended family", err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE clients SET refresh_rotation=0 WHERE client_id=?`, cid); err != nil {
		t.Fatal(err)
	}
	previous := out["refresh_token"]
	r := callJSON(a.handleRefresh, "POST", "/refresh", map[string]any{"client_id": cid, "refresh_token": previous})
	if r.Code != 200 || decode(t, r)["refresh_token"] != previous {
		t.Fatal("stable credential changed")
	}
}

func TestDuplicateIdentityConcurrentAndQuotaRollback(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	a := &App{}
	ctx.Config()["identity_signups_per_client_per_hour"] = "1"
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.toolPublicLoginIdentity(ctx, map[string]any{"client_id": cid, "provider": "device", "provider_user_id": "same-device"})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("duplicate identity failed", err)
		}
	}
	var n int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("orphan/duplicate users=%d err=%v", n, err)
	}
}

func TestSearchPaginationAndQueryCount(t *testing.T) {
	ctx, _ := newAuthCtx(t)
	for i := 0; i < 7; i++ {
		if _, err := dbCreateUser(ctx.AppDB(), "test-proj", 1, fmt.Sprintf("page%d@example.com", i), "", "", true, "{}"); err != nil {
			t.Fatal(err)
		}
	}
	db := &countingDB{DBTX: ctx.AppDB()}
	seen := map[int64]bool{}
	var before int64
	for {
		rows, err := dbSearchUsersPage(db, "test-proj", 1, "", "", "", 3, nil, before)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			break
		}
		for _, u := range rows {
			if seen[u.ID] {
				t.Fatal("duplicate page")
			}
			seen[u.ID] = true
		}
		before = rows[len(rows)-1].ID
	}
	if len(seen) != 7 || db.queries != 4 {
		t.Fatalf("users=%d queries=%d", len(seen), db.queries)
	}
}

type countingDB struct {
	DBTX
	queries int
}

func (d *countingDB) Query(q string, args ...any) (*sql.Rows, error) {
	d.queries++
	return d.DBTX.Query(q, args...)
}
func (d *countingDB) QueryRow(q string, args ...any) *sql.Row {
	d.queries++
	return d.DBTX.QueryRow(q, args...)
}

func TestSigningKeyRotationAndEmergencyRevocation(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	a := &App{}
	out := decode(t, callJSON(a.handleSignup, "POST", "/signup", map[string]any{"client_id": cid, "email": "keys@example.com", "password": "GoodPassword123"}))
	token := out["access_token"].(string)
	old, _ := dbAllSigningKeys(ctx.AppDB(), "test-proj", 1)
	if err := rotateSigningKey(ctx.AppDB(), "test-proj", 1, false); err != nil {
		t.Fatal(err)
	}
	keys, _ := dbAllSigningKeys(ctx.AppDB(), "test-proj", 1)
	if len(keys) != len(old)+1 {
		t.Fatal("rotation did not preserve drain key")
	}
	if r := call(a.handleMe, "GET", "/me", nil, "Authorization", "Bearer "+token); r.Code != 200 {
		t.Fatal("graceful rotation invalidated current token")
	}
	if err := rotateSigningKey(ctx.AppDB(), "test-proj", 1, true); err != nil {
		t.Fatal(err)
	}
	keys, _ = dbAllSigningKeys(ctx.AppDB(), "test-proj", 1)
	if len(keys) != 1 {
		t.Fatal("emergency retained old keys")
	}
	if r := call(a.handleMe, "GET", "/me", nil, "Authorization", "Bearer "+token); r.Code != 401 {
		t.Fatal("emergency left token valid")
	}
}

func TestJWTRequiresTypedExpiryAndIssuedAt(t *testing.T) {
	ctx, _ := newAuthCtx(t)
	kid, priv, err := dbActiveSigningKey(ctx.AppDB(), "test-proj", 1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	for _, extra := range []map[string]any{{"exp": nil}, {"exp": "soon"}, {"exp": now - 1}, {"exp": float64(now) + 0.5}, {"iat": now + 600}, {"iat": nil}} {
		token, err := jwtSign(priv, kid, jwtClaims{Iat: now, Exp: now + 900, Extra: extra})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = jwtVerify(token, func(string) (ed25519.PublicKey, bool) { return priv.Public().(ed25519.PublicKey), true }); err == nil {
			t.Fatalf("invalid claims accepted %v", extra)
		}
	}
}

func TestRequestAndHashResourceBounds(t *testing.T) {
	ctx, _ := newAuthCtx(t)
	a := &App{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/login", strings.NewReader(`{"password":"`+strings.Repeat("x", 129<<10)+`"}`))
	publicGuard(a.handleLogin)(rec, req)
	if rec.Code < 400 {
		t.Fatal("oversized request accepted")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing no-store")
	}
	if _, err := hashPassword(strings.Repeat("x", 4097)); err == nil {
		t.Fatal("long password accepted")
	}
	for i := 0; i < 4; i++ {
		if !acquirePasswordHash() {
			t.Fatal("unexpected saturated hash budget")
		}
	}
	if _, err := hashPassword("GoodPassword123"); err == nil {
		t.Fatal("hash concurrency unbounded")
	}
	for i := 0; i < 4; i++ {
		releasePasswordHash()
	}
	for i := 0; i < 3; i++ {
		err := consumeRate(ctx.AppDB(), "rate-test", 2, time.Hour)
		if (i < 2) != (err == nil) {
			t.Fatalf("quota call %d err=%v", i, err)
		}
	}
}

func TestMigrationInvalidatesExposedCredentialsAndPreservesUsers(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"001_init.sql", "002_organizations.sql", "003_user_metadata.sql", "004_rbac.sql", "005_identity_kinds.sql"} {
		raw, err := os.ReadFile(filepath.Join("migrations", file))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(raw)); err != nil {
			t.Fatal(file, err)
		}
	}
	oid, err := dbCreateOrg(db, "old", "default", "Default", "")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := dbCreateUser(db, "old", oid, "old@example.com", "", "", true, "{}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = dbCreateSession(db, "old", oid, uid, "client", "refresh-hash", "", "", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = dbInsertVerificationToken(db, "old", oid, uid, "reset_password", "reset-hash", "{}", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	dbAudit(db, "old", oid, &uid, "client", "password_reset_sent", "", "", map[string]any{"link": "https://example.com/#reset=exposed", "email": "old@example.com"})
	raw, err := os.ReadFile("migrations/006_security.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	var n int
	for _, q := range []string{`SELECT COUNT(*) FROM sessions WHERE revoked_at IS NULL`, `SELECT COUNT(*) FROM verification_tokens WHERE used_at IS NULL`, `SELECT COUNT(*) FROM audit_log WHERE metadata LIKE '%exposed%'`} {
		if err = db.QueryRow(q).Scan(&n); err != nil || n != 0 {
			t.Fatal("migration left exposed state", q, n, err)
		}
	}
	user, err := dbGetUserByID(db, "old", oid, uid)
	if err != nil || user.AuthorizationVersion != 2 {
		t.Fatalf("user state lost %+v %v", user, err)
	}
}

func TestDeliveryContainsTokenOnlyInEmail(t *testing.T) {
	platform := &recoveryMessagingStub{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(platform), tk.WithConfig(map[string]string{"from_email": "auth@example.com", "app_url": "https://auth.example.com/api/apps/auth"}))
	oid, err := dbCreateOrg(ctx.AppDB(), "test-proj", "default", "Default", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&App{}).toolClientsCreate(ctx, map[string]any{"name": "browser", "type": "spa", "organization_slug": "default"})
	if err != nil {
		t.Fatal(err)
	}
	uid, err := dbCreateUser(ctx.AppDB(), "test-proj", oid, "mail@example.com", "", "", false, "{}")
	if err != nil {
		t.Fatal(err)
	}
	org, _ := dbGetOrgByID(ctx.AppDB(), "test-proj", oid)
	if err = issueResetToken(ctx, "test-proj", org, uid, "mail@example.com"); err != nil {
		t.Fatal(err)
	}
	body := platform.input["body"].(string)
	if !strings.Contains(body, "#") || !strings.Contains(body, "reset=") {
		t.Fatal("mail missing link")
	}
	rows, err := dbAuditSearch(ctx.AppDB(), "test-proj", oid, uid, "", "", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if strings.Contains(row.Metadata, "link") || strings.Contains(row.Metadata, "reset=") {
			t.Fatal("audit retained credential", row.Metadata)
		}
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Metadata, "sent") {
		t.Fatal("delivery outcome missing")
	}
}

func TestCleanupPreservesLiveReplayEvidence(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	a := &App{}
	out := decode(t, callJSON(a.handleSignup, "POST", "/signup", map[string]any{"client_id": cid, "email": "cleanup@example.com", "password": "GoodPassword123"}))
	rec := callJSON(a.handleRefresh, "POST", "/refresh", map[string]any{"client_id": cid, "refresh_token": out["refresh_token"]})
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if err := cleanupAuth(ctx.AppDB(), 90, time.Now()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil || n != 2 {
		t.Fatal("cleanup removed live replay evidence", n, err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE auth_session_families SET expires_at=?`, rfc3339(time.Now().Add(-48*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := cleanupAuth(ctx.AppDB(), 90, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil || n != 0 {
		t.Fatal("cleanup failed to cascade expired family", n, err)
	}
}

func TestPolicyAppliesToAdminResetUpgradeAndTokenTTL(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	a := &App{}
	out := decode(t, callJSON(a.handleSignup, "POST", "/signup", map[string]any{"client_id": cid, "email": "policyall@example.com", "password": "GoodPassword123"}))
	uid := int64(out["user"].(map[string]any)["id"].(float64))
	guest := loginIdentity(t, a, ctx, cid, "device", "policy-guest", nil)["user"].(*User)
	policy := `{"password_min_length":24,"jwt_access_ttl_seconds":60,"jwt_refresh_ttl_days":1}`
	if err := dbUpdateOrg(ctx.AppDB(), "test-proj", 1, nil, nil, &policy); err != nil {
		t.Fatal(err)
	}
	if _, err := a.toolUsersCreate(ctx, map[string]any{"organization_slug": "default", "email": "admin-policy@example.com", "password": "GoodPassword123"}); err == nil {
		t.Fatal("admin ignored org password policy")
	}
	if _, _, err := a.setUserPassword(ctx, "test-proj", 1, uid, "GoodPassword123", true, "test", "", "", ""); err == nil {
		t.Fatal("setter ignored org policy")
	}
	if _, err := a.toolGuestUpgrade(ctx, map[string]any{"organization_slug": "default", "user_id": guest.ID, "email": "upgrade@example.com", "password": "GoodPassword123"}); err == nil {
		t.Fatal("guest upgrade ignored org policy")
	}
	r := callJSON(a.handleRefresh, "POST", "/refresh", map[string]any{"client_id": cid, "refresh_token": out["refresh_token"]})
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	expires := decode(t, r)["expires_in"].(float64)
	if expires > 60 || expires < 58 {
		t.Fatal("ignored org access TTL", expires)
	}
	for _, bad := range []string{`{"password_min_length":0}`, `{"email_verification_required":"true"}`, `{"unknown":3}`, `{"jwt_access_ttl_seconds":-1}`} {
		if err := validatePolicyJSON(&bad); err == nil {
			t.Fatal("accepted invalid policy", bad)
		}
	}
}

func TestRecoveryQueueDefersDeliveryAndDeduplicates(t *testing.T) {
	platform := &recoveryMessagingStub{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(platform), tk.WithConfig(map[string]string{"from_email": "auth@example.com", "app_url": "https://auth.example.com/api/apps/auth"}))
	oid, err := dbCreateOrg(ctx.AppDB(), "test-proj", "default", "Default", "")
	if err != nil {
		t.Fatal(err)
	}
	out, err := (&App{}).toolClientsCreate(ctx, map[string]any{"name": "browser", "type": "spa", "organization_slug": "default"})
	if err != nil {
		t.Fatal(err)
	}
	cid := out.(map[string]any)["client_id"].(string)
	_, err = dbCreateUser(ctx.AppDB(), "test-proj", oid, "queue@example.com", "", "", false, "{}")
	if err != nil {
		t.Fatal(err)
	}
	org, _ := dbGetOrgByID(ctx.AppDB(), "test-proj", oid)
	client, _ := dbGetClientByClientID(ctx.AppDB(), "test-proj", cid)
	for _, email := range []string{"queue@example.com", "queue@example.com", "missing@example.com"} {
		if err = enqueueRecovery(ctx, "test-proj", org, client, email, "reset_password", ""); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM auth_recovery_jobs`).Scan(&n); err != nil || n != 1 {
		t.Fatal("queue did not deduplicate", n, err)
	}
	if platform.input != nil {
		t.Fatal("request delivered synchronously")
	}
	if err = (&App{}).deliverRecoveryJobs(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if platform.input == nil {
		t.Fatal("worker did not deliver")
	}
	if err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM auth_recovery_jobs`).Scan(&n); err != nil || n != 0 {
		t.Fatal("delivered job remains", n, err)
	}
}

// Independent SQL pools exercise the on-disk WAL path rather than relying on
// the testkit's single-connection :memory: database to serialize callers.
func TestConcurrentRefreshAcrossDatabasePools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.db")
	openDB := func() *sql.DB {
		db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)&_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(4)
		t.Cleanup(func() { db.Close() })
		return db
	}
	db := openDB()
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	manifest := (&App{}).Manifest()
	cfg := sdk.Config{"email_verification_required": "false", "app_url": "https://auth.example.com"}
	first := sdk.NewAppCtxForTest(&manifest, db, cfg, nil, nil)
	second := sdk.NewAppCtxForTest(&manifest, openDB(), cfg, nil, nil)
	oid, err := dbCreateOrg(db, "wal", "default", "Default", "")
	if err != nil {
		t.Fatal(err)
	}
	uid, err := dbCreateUser(db, "wal", oid, "wal@example.com", "", "", true, "{}")
	if err != nil {
		t.Fatal(err)
	}
	c := Client{ClientID: "wal-client", Type: "spa", Name: "WAL", AllowedGrantTypes: []string{"refresh_token"}, RefreshRotation: true}
	if _, err = dbCreateClient(db, "wal", oid, c, ""); err != nil {
		t.Fatal(err)
	}
	org, _ := dbGetOrgByID(db, "wal", oid)
	u, _ := dbGetUserByID(db, "wal", oid, uid)
	client, _ := dbGetClientByClientID(db, "wal", c.ClientID)
	pair, err := mintSession(first, "wal", org, u, client, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	success := make(chan bool, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := first
			if i%2 == 1 {
				ctx = second
			}
			_, _, _, err := refreshSession(ctx, "wal", client, pair.refresh, "", nil)
			success <- err == nil
		}(i)
	}
	wg.Wait()
	close(success)
	n := 0
	for ok := range success {
		if ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("multi-pool refresh succeeded %d times", n)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM auth_session_families WHERE revoked_at IS NULL`).Scan(&n); err != nil || n != 0 {
		t.Fatal("replayed WAL family stayed active", n, err)
	}
}

func BenchmarkPasswordHash(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := hashPassword("BenchmarkPassword123"); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkSearch100Of10000Users(b *testing.B) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	files, _ := filepath.Glob("migrations/*.sql")
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			b.Fatal(err)
		}
		if _, err = db.Exec(string(raw)); err != nil {
			b.Fatal(err)
		}
	}
	oid, err := dbCreateOrg(db, "bench", "default", "Default", "")
	if err != nil {
		b.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 10000; i++ {
		if _, err := dbCreateUser(tx, "bench", oid, fmt.Sprintf("bench%05d@example.com", i), "", "", true, "{}"); err != nil {
			b.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := dbSearchUsersPage(db, "bench", oid, "", "", "", 100, nil, 0)
		if err != nil || len(rows) != 100 {
			b.Fatal(err)
		}
	}
}
