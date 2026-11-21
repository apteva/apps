package main

import (
	"net/http"
	"path/filepath"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// A data directory written by the real handlers under project rt_old (sealed
// signing keys, two organizations, a signed-up user with a live session, a
// client whose redirect URI points at the runtime) is restored under rt_new:
// the same organization, user and client ids resolve under the new project,
// the signing key still opens (so mount does not rotate it and revoke the
// sessions), the pre-snapshot refresh token still works, and URLs and audit
// install ids follow the new runtime. Another project's rows are untouched.
func TestRestoreMovesAuthDataToNewProject(t *testing.T) {
	const oldBase, newBase = "https://runtime.apteva.invalid/rt_old", "https://runtime.apteva.invalid/rt_new"
	src := t.TempDir()
	t.Setenv("AUTH_SIGNING_KEK", "")
	t.Setenv("APTEVA_APP_TOKEN", testAppToken)
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("rt_old"), tk.WithEnv("APTEVA_DATA_DIR", src),
		tk.WithConfig(map[string]string{"email_verification_required": "false", "app_url": "http://localhost:8080"}))
	kek, err := loadSigningKEK(ctx)
	if err != nil {
		t.Fatal(err)
	}
	setSigningKEK(kek)
	t.Cleanup(func() { setSigningKEK(nil) })
	db := ctx.AppDB()
	defaultOrg, err := dbCreateOrg(db, "rt_old", "default", "Default", "")
	if err != nil {
		t.Fatal(err)
	}
	acme, err := dbCreateOrg(db, "rt_old", "acme", "Acme", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, oid := range []int64{defaultOrg, acme} {
		if err := ensureSigningKey(db, "rt_old", oid); err != nil {
			t.Fatal(err)
		}
	}
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = nil })
	app := &App{}
	out, err := app.toolClientsCreate(ctx, map[string]any{"organization_slug": "default", "name": "site", "type": "spa",
		"redirect_uris":   []any{oldBase + "/api/apps/content/callback", "https://shop.example/callback"},
		"allowed_origins": []any{"https://shop.example"}})
	if err != nil {
		t.Fatal(err)
	}
	clientID := out.(map[string]any)["client_id"].(string)
	rec := callJSON(app.handleSignup, "POST", "/signup", signupBody(clientID, "alice@example.com"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("signup %d %s", rec.Code, rec.Body.String())
	}
	tokens := decode(t, rec)
	user, err := dbGetUserByEmail(db, "rt_old", defaultOrg, "alice@example.com")
	if err != nil || user == nil {
		t.Fatalf("user before snapshot: %v", err)
	}
	var kid string
	if err := db.QueryRow(`SELECT kid FROM signing_keys WHERE project_id='rt_old' AND organization_id=? AND retired_at IS NULL`, defaultOrg).Scan(&kid); err != nil {
		t.Fatal(err)
	}
	dbAudit(db, "rt_old", defaultOrg, nil, clientID, "login", auditSource{ActorType: "app", SourceApp: "content", SourceInstallID: 5}, nil)
	dbAudit(db, "rt_old", defaultOrg, nil, clientID, "login", auditSource{ActorType: "app", SourceApp: "other", SourceInstallID: 3}, nil)
	// A second project in the same install keeps its rows.
	other, err := dbCreateOrg(db, "other", "default", "Default", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO clients (project_id, organization_id, client_id, name, type, redirect_uris) VALUES ('other', ?, 'akc_other', 'other', 'spa', ?)`,
		other, `["https://elsewhere.example/cb"]`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`VACUUM INTO ?`, filepath.Join(src, "app.db")); err != nil {
		t.Fatal(err)
	}
	setSigningKEK(nil)

	rc := sdk.RestoreContext{ID: "auth-restore", OldProjectID: "rt_old", NewProjectID: "rt_new",
		InstallIDs: map[int64]int64{5: 50}, PublicBaseURLs: map[string]string{oldBase: newBase}}
	restored, report, err := tk.RunRestore(t, app, src, rc, tk.WithProjectID("rt_new"))
	if err != nil {
		t.Fatal(err)
	}
	if report.ProjectRows["organizations"] != 2 || report.ProjectRows["users"] != 1 || report.ProjectRows["signing_keys"] != 2 {
		t.Fatalf("report %+v", report.ProjectRows)
	}

	// Organizations, user and client keep their ids under the new project.
	for slug, id := range map[string]int64{"default": defaultOrg, "acme": acme} {
		org, err := dbGetOrgBySlug(restored, "rt_new", slug)
		if err != nil || org == nil || org.ID != id {
			t.Fatalf("org %s after restore: %+v %v", slug, org, err)
		}
	}
	if orgs, _ := dbListOrgs(restored, "rt_old", true); len(orgs) != 0 {
		t.Fatalf("orgs left under the old project: %+v", orgs)
	}
	if u, err := dbGetUserByEmail(restored, "rt_new", defaultOrg, "alice@example.com"); err != nil || u == nil || u.ID != user.ID {
		t.Fatalf("user after restore: %+v %v", u, err)
	}
	client, err := dbGetClientByClientID(restored, "rt_new", clientID)
	if err != nil || client == nil {
		t.Fatalf("client after restore: %v", err)
	}
	var redirects, origins string
	restored.QueryRow(`SELECT redirect_uris, allowed_origins FROM clients WHERE client_id=?`, clientID).Scan(&redirects, &origins)
	if redirects != `["`+newBase+`/api/apps/content/callback","https://shop.example/callback"]` || origins != `["https://shop.example"]` {
		t.Fatalf("client URLs after restore: %s %s", redirects, origins)
	}
	if err := restored.QueryRow(`SELECT redirect_uris FROM clients WHERE client_id='akc_other' AND project_id='other'`).Scan(&redirects); err != nil {
		t.Fatal(err)
	}
	if redirects != `["https://elsewhere.example/cb"]` {
		t.Fatalf("other project's client: %s", redirects)
	}
	if org, _ := dbGetOrgBySlug(restored, "other", "default"); org == nil || org.ID != other {
		t.Fatal("other project's organization moved")
	}
	rows, err := restored.Query(`SELECT source_install_id FROM audit_log WHERE source_install_id IS NOT NULL ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var installs []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		installs = append(installs, id)
	}
	rows.Close()
	if len(installs) != 2 || installs[0] != 50 || installs[1] != 3 {
		t.Fatalf("audit install ids %v", installs)
	}

	// Mount's key check: the active key opens under rt_new, so nothing is
	// rotated and no session is revoked.
	newCtx := sdk.NewAppCtxForTest(nil, restored, sdk.Config{"email_verification_required": "false", "app_url": "http://localhost:8080"}, nil, nil)
	copied, err := loadSigningKEK(newCtx)
	if err != nil {
		t.Fatal(err)
	}
	setSigningKEK(copied)
	if err := sealStoredSigningKeys(newCtx); err != nil {
		t.Fatal(err)
	}
	gotKID, _, err := dbActiveSigningKey(restored, "rt_new", defaultOrg)
	if err != nil || gotKID != kid {
		t.Fatalf("active key after restore: %q (want %q) %v", gotKID, kid, err)
	}
	var revoked int
	restored.QueryRow(`SELECT COUNT(*) FROM sessions WHERE revoked_at IS NOT NULL`).Scan(&revoked)
	if revoked != 0 {
		t.Fatalf("%d sessions revoked after restore", revoked)
	}
	globalCtx = newCtx
	if rec := callJSON(app.handleRefresh, "POST", "/refresh", map[string]any{"refresh_token": tokens["refresh_token"], "client_id": clientID}); rec.Code != http.StatusOK {
		t.Fatalf("refresh after restore %d %s", rec.Code, rec.Body.String())
	}
	if rec := callJSON(app.handleLogin, "POST", "/login", signupBody(clientID, "alice@example.com")); rec.Code != http.StatusOK {
		t.Fatalf("login after restore %d %s", rec.Code, rec.Body.String())
	}
}
