package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	_ "modernc.org/sqlite"
)

const testAppToken = "hardening-app-token"

func newHardenedCtx(t *testing.T, extra map[string]string) (*sdk.AppCtx, string) {
	t.Helper()
	t.Setenv("APTEVA_APP_TOKEN", testAppToken)
	cfg := map[string]string{"email_verification_required": "false", "app_url": "http://localhost:8080"}
	for k, v := range extra {
		cfg[k] = v
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithConfig(cfg))
	orgID, err := dbCreateOrg(ctx.AppDB(), "test-proj", "default", "Default", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureSigningKey(ctx.AppDB(), "test-proj", orgID); err != nil {
		t.Fatal(err)
	}
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = nil })
	out, err := (&App{}).toolClientsCreate(ctx, map[string]any{"organization_slug": "default", "name": "spa", "type": "spa"})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, out.(map[string]any)["client_id"].(string)
}

// signedRequest builds a request as the platform proxy delivers it: loopback
// RemoteAddr plus, optionally, a client IP signed with the install token.
func signedRequest(method, path string, body any, signedIP, token string, headers ...string) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	r := httptest.NewRequest(method, path, &buf)
	r.RemoteAddr = "127.0.0.1:41234"
	r.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	if signedIP != "" {
		sdk.SetClientIPHeaders(r, signedIP, token)
	}
	return r
}

func serve(h http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func signupBody(clientID, email string) map[string]any {
	return map[string]any{"email": email, "password": "VerySafe!Pw#12345", "client_id": clientID}
}

func lastAudit(t *testing.T, ctx *sdk.AppCtx, event string) AuditEvent {
	t.Helper()
	// Rows written in the same millisecond tie on occurred_at; the highest id
	// is the most recent.
	events, err := dbAuditSearch(ctx.AppDB(), "test-proj", 0, 0, event, "", "", 500)
	if err != nil || len(events) == 0 {
		t.Fatalf("no %s audit row: %v", event, err)
	}
	latest := events[0]
	for _, e := range events {
		if e.ID > latest.ID {
			latest = e
		}
	}
	return latest
}

func TestSignedClientIPIsRecordedAndForgedHeadersAreIgnored(t *testing.T) {
	ctx, clientID := newHardenedCtx(t, nil)
	app := &App{}
	rec := serve(app.handleSignup, signedRequest("POST", "/signup", signupBody(clientID, "ip@example.com"), "203.0.113.7", testAppToken, "X-Forwarded-For", "198.51.100.9"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup %d %s", rec.Code, rec.Body.String())
	}
	ev := lastAudit(t, ctx, "signup")
	if ev.IP != "203.0.113.7" || !strings.Contains(ev.Metadata, `"ip_trusted":true`) {
		t.Fatalf("signup audit ip=%q meta=%s", ev.IP, ev.Metadata)
	}
	var sessionIP string
	if err := ctx.AppDB().QueryRow(`SELECT ip FROM sessions ORDER BY id DESC LIMIT 1`).Scan(&sessionIP); err != nil || sessionIP != "203.0.113.7" {
		t.Fatalf("session ip=%q err=%v", sessionIP, err)
	}

	// A client cannot assert its own IP: a signature made with another key
	// and a plain forwarding header both fall back to the proxy address.
	rec = serve(app.handleSignup, signedRequest("POST", "/signup", signupBody(clientID, "forged@example.com"), "198.51.100.9", "attacker-token", "X-Forwarded-For", "198.51.100.9"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("forged signup %d %s", rec.Code, rec.Body.String())
	}
	ev = lastAudit(t, ctx, "signup")
	if ev.IP != "127.0.0.1" || !strings.Contains(ev.Metadata, `"ip_trusted":false`) {
		t.Fatalf("forged header trusted: ip=%q meta=%s", ev.IP, ev.Metadata)
	}
}

func TestSignupPerIPLimitUsesOnlySignedAddress(t *testing.T) {
	_, clientID := newHardenedCtx(t, map[string]string{"signups_per_ip_per_hour": "2"})
	app := &App{}
	signup := func(email, ip, token string, headers ...string) int {
		return serve(app.handleSignup, signedRequest("POST", "/signup", signupBody(clientID, email), ip, token, headers...)).Code
	}
	for i, email := range []string{"a1@example.com", "a2@example.com"} {
		if code := signup(email, "203.0.113.1", testAppToken); code != http.StatusCreated {
			t.Fatalf("signup %d from A = %d", i, code)
		}
	}
	if code := signup("a3@example.com", "203.0.113.1", testAppToken); code != http.StatusTooManyRequests {
		t.Fatalf("third signup from A = %d, want 429", code)
	}
	// A forged forwarding header does not move A into another bucket.
	if code := signup("a4@example.com", "203.0.113.1", testAppToken, "X-Forwarded-For", "203.0.113.2"); code != http.StatusTooManyRequests {
		t.Fatalf("A with forged XFF = %d, want 429", code)
	}
	if code := signup("b1@example.com", "203.0.113.2", testAppToken); code != http.StatusCreated {
		t.Fatalf("signup from B = %d; one client exhausted another's limit", code)
	}
	// Unsigned traffic is never charged to a per-IP bucket (it would all share
	// the proxy address); forged signatures count as unsigned.
	for _, email := range []string{"u1@example.com", "u2@example.com", "u3@example.com"} {
		if code := signup(email, "203.0.113.1", "attacker-token"); code != http.StatusCreated {
			t.Fatalf("unsigned signup = %d", code)
		}
	}
}

func TestPublicGuardBucketsPerSignedClientIP(t *testing.T) {
	newHardenedCtx(t, nil)
	ok := publicGuard(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	post := func(ip string) int {
		return serve(ok, signedRequest("POST", "/login", map[string]any{}, ip, testAppToken)).Code
	}
	for i := 0; i < 300; i++ {
		if code := post("203.0.113.50"); code != http.StatusNoContent {
			t.Fatalf("request %d = %d", i, code)
		}
	}
	if code := post("203.0.113.50"); code != http.StatusTooManyRequests {
		t.Fatalf("request 301 = %d, want 429", code)
	}
	if code := post("203.0.113.51"); code != http.StatusNoContent {
		t.Fatalf("other client = %d; one client throttled everyone", code)
	}
	if code := serve(ok, signedRequest("POST", "/login", map[string]any{}, "203.0.113.51", testAppToken, "X-Forwarded-For", "203.0.113.50")).Code; code != http.StatusNoContent {
		t.Fatalf("forged XFF moved the request into the exhausted bucket: %d", code)
	}
}

func TestSignupWithVerificationOffDoesNotClaimVerifiedMailbox(t *testing.T) {
	ctx, clientID := newHardenedCtx(t, nil)
	app := &App{}
	rec := callJSON(app.handleSignup, "POST", "/signup", signupBody(clientID, "skip@example.com"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup %d %s", rec.Code, rec.Body.String())
	}
	u, err := dbGetUserByEmail(ctx.AppDB(), "test-proj", 1, "skip@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if u.EmailVerifiedAt != "" || u.EmailVerification != emailNotRequired {
		t.Fatalf("verified_at=%q state=%q", u.EmailVerifiedAt, u.EmailVerification)
	}
	if rec := callJSON(app.handleLogin, "POST", "/login", signupBody(clientID, "skip@example.com")); rec.Code != http.StatusOK {
		t.Fatalf("login with verification off = %d %s", rec.Code, rec.Body.String())
	}
	// Turning verification on now makes the unproven account verify first.
	if _, err := app.toolOrgsUpdate(ctx, map[string]any{"organization_slug": "default", "policy_overrides": `{"email_verification_required":true}`}); err != nil {
		t.Fatal(err)
	}
	if rec := callJSON(app.handleLogin, "POST", "/login", signupBody(clientID, "skip@example.com")); rec.Code == http.StatusOK {
		t.Fatal("unproven mailbox logged in after verification was required")
	}
	ev := lastAudit(t, ctx, "org_updated")
	if !strings.Contains(ev.Metadata, `"email_verification_required":{"from":false,"to":true}`) {
		t.Fatalf("org_updated did not record the verification change: %s", ev.Metadata)
	}
	if _, _, _, err := app.createUser(ctx, "test-proj", mustOrg(t, ctx), createUserInput{email: "asserted@example.com", emailVerified: true}, auditSource{}, "test"); err != nil {
		t.Fatal(err)
	}
	report, err := verificationReport(ctx, "test-proj")
	if err != nil {
		t.Fatal(err)
	}
	orgs := report["organizations"].([]verificationOrgReport)
	if len(orgs) != 1 || orgs[0].Source != "policy_overrides" || !*orgs[0].EmailVerificationRequired ||
		orgs[0].Users[emailNotRequired] != 1 || orgs[0].Users[emailAdminAsserted] != 1 {
		t.Fatalf("report %+v", orgs)
	}
	if changes := report["changes"].([]map[string]any); len(changes) != 1 || changes[0]["email_verification_required"] != true {
		t.Fatalf("changes %+v", changes)
	}
}

func mustOrg(t *testing.T, ctx *sdk.AppCtx) *Organization {
	t.Helper()
	org, err := dbGetOrgByID(ctx.AppDB(), "test-proj", 1)
	if err != nil {
		t.Fatal(err)
	}
	return org
}

func findTool(t *testing.T, name string) sdk.Tool {
	t.Helper()
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %s missing", name)
	return sdk.Tool{}
}

func signPrincipal(t *testing.T, r *http.Request, p sdk.TrustedPrincipal, token string) {
	t.Helper()
	raw, _ := json.Marshal(p)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(payload))
	r.Header.Set(sdk.HeaderTrustedPrincipal, payload)
	r.Header.Set(sdk.HeaderTrustedPrincipalSignature, hex.EncodeToString(mac.Sum(nil)))
}

func TestAuditRecordsActorSourceAndFailures(t *testing.T) {
	ctx, _ := newHardenedCtx(t, nil)
	app := &App{}
	uid, err := dbCreateUser(ctx.AppDB(), "test-proj", 1, "target@example.com", "", "", emailPending, "{}")
	if err != nil {
		t.Fatal(err)
	}
	// An agent's tool call: the actor and correlation come from the SDK caller,
	// never from arguments (a model cannot inject the private key).
	disable := findTool(t, "auth_users_disable")
	call := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 42, ToolCallID: "call-1"})
	args := map[string]any{"organization_slug": "default", "user_id": uid, "reason": "suspicious", callerArgKey: "forged"}
	if _, err := disable.HandlerCtx(call, ctx, args); err != nil {
		t.Fatal(err)
	}
	ev := lastAudit(t, ctx, "user_disabled")
	if ev.ActorType != "agent" || ev.ActorID != "42" || ev.CorrelationID != "call-1" || ev.Result != "success" {
		t.Fatalf("agent attribution %+v", ev)
	}
	// A bound sibling app.
	app2 := sdk.WithCaller(context.Background(), &sdk.Caller{AppName: "crm", AppInstallID: 9, ToolCallID: "call-2"})
	if _, err := findTool(t, "auth_users_enable").HandlerCtx(app2, ctx, map[string]any{"organization_slug": "default", "user_id": uid}); err != nil {
		t.Fatal(err)
	}
	ev = lastAudit(t, ctx, "user_enabled")
	if ev.ActorType != "app" || ev.SourceApp != "crm" || ev.SourceInstallID != 9 {
		t.Fatalf("app attribution %+v", ev)
	}
	// A dashboard admin: the platform-signed principal names the platform user.
	r := signedRequest("POST", "/admin/users/1/disable?organization_slug=default", map[string]any{"reason": "fraud"}, "203.0.113.9", testAppToken)
	r.SetPathValue("id", jsonInt(uid))
	signPrincipal(t, r, sdk.TrustedPrincipal{Version: sdk.TrustedPrincipalVersion, UserID: 7, ExpiresAt: time.Now().Add(time.Minute).Unix()}, testAppToken)
	if rec := serve(app.handleAdminUsersDisable, r); rec.Code != http.StatusOK {
		t.Fatalf("admin disable %d %s", rec.Code, rec.Body.String())
	}
	ev = lastAudit(t, ctx, "user_disabled")
	if ev.ActorType != "platform_user" || ev.ActorID != "7" || ev.IP != "203.0.113.9" || ev.CorrelationID == "" {
		t.Fatalf("admin attribution %+v", ev)
	}
	// A forged principal is not trusted and is not mistaken for the user.
	r = signedRequest("POST", "/admin/users/1/enable?organization_slug=default", nil, "", "")
	r.SetPathValue("id", jsonInt(uid))
	signPrincipal(t, r, sdk.TrustedPrincipal{Version: sdk.TrustedPrincipalVersion, UserID: 7, ExpiresAt: time.Now().Add(time.Minute).Unix()}, "attacker-token")
	serve(app.handleAdminUsersEnable, r)
	if ev = lastAudit(t, ctx, "user_enabled"); ev.ActorType != "unattributed" {
		t.Fatalf("forged principal attribution %+v", ev)
	}
	// Failures are recorded with the actor.
	if _, err := app.toolOrgsUpdate(ctx, map[string]any{"organization_slug": "default", "policy_overrides": `{"nope":1}`, auditSourceArgKey: auditSource{ActorType: "platform_user", ActorID: "7"}}); err == nil {
		t.Fatal("invalid policy accepted")
	}
	if ev = lastAudit(t, ctx, "org_update_failed"); ev.Result != "failure" || ev.ActorID != "7" {
		t.Fatalf("failure attribution %+v", ev)
	}
}

func jsonInt(v int64) string { b, _ := json.Marshal(v); return string(b) }

func TestAccessTokenLifetimeIsCapped(t *testing.T) {
	_, clientID := newHardenedCtx(t, map[string]string{"jwt_access_ttl_seconds": "86400"})
	rec := callJSON((&App{}).handleSignup, "POST", "/signup", signupBody(clientID, "ttl@example.com"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup %d %s", rec.Code, rec.Body.String())
	}
	if got := decode(t, rec)["expires_in"].(float64); got > maxAccessTokenTTL || got < maxAccessTokenTTL-5 {
		t.Fatalf("expires_in=%v, want capped at %d", got, maxAccessTokenTTL)
	}
}

func testKEK(t *testing.T, seed byte) cipher.AEAD {
	t.Helper()
	block, _ := aes.NewCipher(bytes.Repeat([]byte{seed}, 32))
	aead, _ := cipher.NewGCM(block)
	return aead
}

func TestSigningKeysAreEncryptedAtRest(t *testing.T) {
	setSigningKEK(testKEK(t, 1))
	t.Cleanup(func() { setSigningKEK(nil) })
	ctx, clientID := newHardenedCtx(t, nil)
	var kid, stored string
	if err := ctx.AppDB().QueryRow(`SELECT kid, private_pem FROM signing_keys WHERE organization_id=1 AND retired_at IS NULL`).Scan(&kid, &stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, sealedKeyPrefix) || strings.Contains(stored, "PRIVATE KEY") {
		t.Fatalf("private key stored in clear: %.40s", stored)
	}
	if rec := callJSON((&App{}).handleSignup, "POST", "/signup", signupBody(clientID, "seal@example.com")); rec.Code != http.StatusCreated {
		t.Fatalf("signup with sealed key %d %s", rec.Code, rec.Body.String())
	}
	// The sealed key is bound to its row: copied to another organization it
	// does not open.
	if _, err := openSigningKey("test-proj", 2, kid, stored); err == nil {
		t.Fatal("sealed key opened under another organization")
	}
	// Legacy plaintext rows are encrypted on mount.
	oid2, _ := dbCreateOrg(ctx.AppDB(), "test-proj", "legacy", "Legacy", "")
	setSigningKEK(nil)
	if err := ensureSigningKey(ctx.AppDB(), "test-proj", oid2); err != nil {
		t.Fatal(err)
	}
	setSigningKEK(testKEK(t, 1))
	if err := sealStoredSigningKeys(ctx); err != nil {
		t.Fatal(err)
	}
	var plain int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM signing_keys WHERE private_pem LIKE '%PRIVATE KEY%'`).Scan(&plain)
	if plain != 0 {
		t.Fatalf("%d plaintext keys after sealing", plain)
	}
	// A lost or replaced KEK cannot strand the service: unreadable active keys
	// are replaced by an emergency rotation.
	setSigningKEK(testKEK(t, 2))
	if err := sealStoredSigningKeys(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := dbActiveSigningKey(ctx.AppDB(), "test-proj", 1); err != nil {
		t.Fatalf("no usable key after KEK loss: %v", err)
	}
	if ev := lastAudit(t, ctx, "signing_key_rotated"); ev.ActorType != "system" || !strings.Contains(ev.Metadata, `"emergency":true`) {
		t.Fatalf("rotation audit %+v", ev)
	}
}

func TestSigningKEKFileIsCreatedOncePrivately(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AUTH_SIGNING_KEK", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithEnv("APTEVA_DATA_DIR", dir))
	a, err := resolveSigningKEK(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := resolveSigningKEK(ctx)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("KEK changed between loads")
	}
	info, err := os.Stat(filepath.Join(dir, "auth-signing.kek"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("kek file %v %v", info, err)
	}
}

func TestContainUserRevokesEverythingAndReportsRemainder(t *testing.T) {
	ctx, clientID := newHardenedCtx(t, nil)
	app := &App{}
	rec := callJSON(app.handleSignup, "POST", "/signup", signupBody(clientID, "victim@example.com"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup %d", rec.Code)
	}
	tokens := decode(t, rec)
	u, _ := dbGetUserByEmail(ctx.AppDB(), "test-proj", 1, "victim@example.com")
	if _, err := app.toolUsersContain(ctx, map[string]any{"organization_slug": "default", "user_id": u.ID}); err == nil {
		t.Fatal("containment without a reason was accepted")
	}
	contain := findTool(t, "auth_users_contain")
	call := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 5, ToolCallID: "contain-1"})
	out, err := contain.HandlerCtx(call, ctx, map[string]any{"organization_slug": "default", "user_id": u.ID, "reason": "credential stuffing"})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	revoked := res["revoked"].(map[string]any)
	if res["status"] != "disabled" || revoked["session_families"] != 1 || revoked["sessions"].(int64) < 1 || len(res["session_families"].([]containedFamily)) != 1 {
		t.Fatalf("containment result %+v", res)
	}
	if rec := callJSON(app.handleRefresh, "POST", "/refresh", map[string]any{"refresh_token": tokens["refresh_token"], "client_id": clientID}); rec.Code == http.StatusOK {
		t.Fatal("refresh token survived containment")
	}
	if _, err := verifySession(context.Background(), ctx, "test-proj", tokens["access_token"].(string)); err == nil {
		t.Fatal("access token still verifies through the session check")
	}
	ev := lastAudit(t, ctx, "user_contained")
	if ev.ActorID != "5" || !strings.Contains(ev.Metadata, "credential stuffing") {
		t.Fatalf("containment audit %+v", ev)
	}
	if _, err := app.toolUsersContain(ctx, map[string]any{"organization_slug": "default", "user_id": 9999, "reason": "x"}); err == nil {
		t.Fatal("unknown user contained")
	}
}

func TestSignupEmailAndDomainControls(t *testing.T) {
	ctx, clientID := newHardenedCtx(t, map[string]string{"signups_per_email_per_hour": "2", "blocked_email_domains": "mailinator.com, @tempmail.dev"})
	app := &App{}
	if rec := callJSON(app.handleSignup, "POST", "/signup", signupBody(clientID, "x@sub.mailinator.com")); rec.Code != http.StatusBadRequest {
		t.Fatalf("blocked subdomain = %d", rec.Code)
	}
	if rec := callJSON(app.handleSignup, "POST", "/signup", signupBody(clientID, "x@tempmail.dev")); rec.Code != http.StatusBadRequest {
		t.Fatalf("blocked domain = %d", rec.Code)
	}
	if ev := lastAudit(t, ctx, "signup_rejected"); ev.Result != "failure" || !strings.Contains(ev.Metadata, "email_domain_blocked") {
		t.Fatalf("rejection audit %+v", ev)
	}
	if rec := callJSON(app.handleSignup, "POST", "/signup", signupBody(clientID, "x@notmailinator.com")); rec.Code != http.StatusCreated {
		t.Fatalf("lookalike domain blocked = %d", rec.Code)
	}
	codes := []int{}
	for i := 0; i < 3; i++ {
		codes = append(codes, callJSON(app.handleSignup, "POST", "/signup", signupBody(clientID, "repeat@example.com")).Code)
	}
	if codes[0] != http.StatusCreated || codes[1] != http.StatusConflict || codes[2] != http.StatusTooManyRequests {
		t.Fatalf("per-email attempts = %v", codes)
	}
}

func TestMigration007ClassifiesExistingVerification(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, file := range []string{"001_init.sql", "002_organizations.sql", "003_user_metadata.sql", "004_rbac.sql", "005_identity_kinds.sql", "006_security.sql"} {
		raw, err := os.ReadFile(filepath.Join("migrations", file))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(raw)); err != nil {
			t.Fatal(file, err)
		}
	}
	if _, err = db.Exec(`INSERT INTO organizations(project_id, slug, name, status, created_at, updated_at) VALUES('p','default','Default','active','t','t')`); err != nil {
		t.Fatal(err)
	}
	insert := func(email, verifiedAt, event string) {
		res, err := db.Exec(`INSERT INTO users(project_id, organization_id, email, email_verified_at, metadata_json, status, created_at, updated_at) VALUES('p', 1, ?, NULLIF(?, ''), '{}', 'active', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, email, verifiedAt)
		if err != nil {
			t.Fatal(err)
		}
		if event != "" {
			id, _ := res.LastInsertId()
			if _, err = db.Exec(`INSERT INTO audit_log(project_id, organization_id, user_id, event) VALUES('p', 1, ?, ?)`, id, event); err != nil {
				t.Fatal(err)
			}
		}
	}
	const created = "2026-01-01T00:00:00Z"
	insert("skipped@example.com", created, "signup")
	insert("admin@example.com", created, "user_created_admin")
	insert("legacy@example.com", created, "")
	insert("proven@example.com", "2026-01-02T00:00:00Z", "signup")
	insert("pending@example.com", "", "signup")
	raw, _ := os.ReadFile("migrations/007_hardening.sql")
	if _, err = db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		"skipped@example.com": {"not_required", ""},
		"admin@example.com":   {"admin_asserted", created},
		"legacy@example.com":  {"legacy_assumed", created},
		"proven@example.com":  {"verified", "2026-01-02T00:00:00Z"},
		"pending@example.com": {"pending", ""},
	}
	for email, w := range want {
		var state, at string
		if err := db.QueryRow(`SELECT email_verification, IFNULL(email_verified_at,'') FROM users WHERE email=?`, email).Scan(&state, &at); err != nil {
			t.Fatal(err)
		}
		if state != w[0] || at != w[1] {
			t.Errorf("%s = (%s,%q), want %v", email, state, at, w)
		}
	}
}
