package main

// Tier 2: every handler + the store layer exercised against an
// in-memory SQLite that has the real migrations applied. Fast
// (<1s). Covers the bug classes that bit v0.2.0–v0.2.5:
//
//   v0.2.4: INSERT placeholder count drift  → TestStore_RoundTrip
//   v0.2.3: duplicate /health panic         → TestHTTPRoutes_NoHealth
//   v0.2.5: apteva binary resolution        → TestResolveAptevaBin_*
//   slug validation regressions             → TestToolCreate_Slug*
//   setup_pending guards on remote ops      → TestRunRemote / TestSupportLogin
//
// Local-process spawn is not covered here — that's the integration
// test (sidecar build tag). What we CAN test cheaply at tier 2 is
// the bookkeeping around it: state transitions, store mutations,
// decorate-view, attach-key validation.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── Test harness ───────────────────────────────────────────────────

// newTestApp returns a freshly-mounted App against an in-memory
// SQLite with all fleet migrations applied. APTEVA_DATA_DIR points
// at a per-test tmp dir so master.key generation doesn't pollute
// the working tree.
func newTestApp(t *testing.T, opts ...tk.Option) (*App, *sdk.AppCtx) {
	t.Helper()
	dataDir := t.TempDir()
	full := append([]tk.Option{tk.WithEnv("APTEVA_DATA_DIR", dataDir)}, opts...)
	ctx := tk.NewAppCtx(t, "apteva.yaml", full...)
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatalf("OnMount: %v", err)
	}
	// Force the public host to "localhost" inside tests so assertions
	// on returned URLs aren't dependent on the dev machine's outbound
	// IP. Tests that exercise the rewrite explicitly set publicHost.
	app.publicHost = "localhost"
	return app, ctx
}

// seedTenant inserts a row directly via the store, bypassing
// toolCreate's local-process spawn. Returns the tenant ID.
func seedTenant(t *testing.T, app *App, slug, status string) string {
	t.Helper()
	enc, err := app.keys.seal([]byte("sk-fake"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	tn := &Tenant{
		Slug:       slug,
		Kind:       KindLocal,
		BaseURL:    "http://localhost:65535",
		ConfigDir:  filepath.Join(t.TempDir(), slug),
		OwnerEmail: slug + "@example.com",
		Status:     status,
	}
	if err := app.store.insert(tn, enc, nil); err != nil {
		t.Fatalf("seed insert: %v", err)
	}
	return tn.ID
}

// fakeTenantServer stands up an httptest.Server that mimics the
// apteva endpoints fleet probes. authOK = whether /api/auth/status
// returns 200 (true) or 401 (false).
func fakeTenantServer(t *testing.T, authOK bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "version": "0.9.5"})
	})
	mux.HandleFunc("/api/auth/status", func(w http.ResponseWriter, r *http.Request) {
		if !authOK {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"user_id": 1, "email": "admin@local"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// ─── Tier 1 sanity (sub-millisecond) ────────────────────────────────

func TestHTTPRoutes_NoHealth(t *testing.T) {
	// The SDK auto-registers GET /health. Declaring it again panics
	// the ServeMux at mount time. This was the v0.2.2 → v0.2.3 bug.
	for _, r := range (&App{}).HTTPRoutes() {
		if r.Pattern == "/health" {
			t.Errorf("HTTPRoutes() declares /health — SDK already registers it; mount will panic")
		}
	}
}

func TestResolveAptevaBin_PrefersExplicit(t *testing.T) {
	// Write a fake binary in t.TempDir(), pass its path explicitly,
	// confirm it's preferred over anything in env / well-known dirs.
	dir := t.TempDir()
	fake := filepath.Join(dir, "apteva")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveAptevaBin(fake)
	if err != nil {
		t.Fatalf("resolveAptevaBin: %v", err)
	}
	if got != fake {
		t.Errorf("got %q want %q", got, fake)
	}
}

func TestResolveAptevaBin_FallsBackToEnv(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "apteva-env")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEET_APTEVA_BIN", fake)
	got, err := resolveAptevaBin("")
	if err != nil {
		t.Fatalf("resolveAptevaBin: %v", err)
	}
	if got != fake {
		t.Errorf("got %q want %q", got, fake)
	}
}

func TestResolveAptevaBin_ErrorMentionsCandidates(t *testing.T) {
	// On a clean machine the error message should hint at where to
	// look. Reset FLEET_APTEVA_BIN; relies on the real $HOME not
	// having apteva installed at one of the canonical paths during
	// CI. If a dev machine has apteva installed the test naturally
	// skips (resolveAptevaBin succeeds).
	t.Setenv("FLEET_APTEVA_BIN", "")
	// Point HOME at an empty tmp dir so ~/.apteva/bin/apteva doesn't exist.
	t.Setenv("HOME", t.TempDir())
	_, err := resolveAptevaBin("")
	if err == nil {
		t.Skip("apteva binary present in /usr/local/bin or /opt/homebrew/bin; can't assert error message")
	}
	want := "set FLEET_APTEVA_BIN"
	if !contains(err.Error(), want) {
		t.Errorf("error %q should hint at %q", err.Error(), want)
	}
}

func TestPortFromBaseURL(t *testing.T) {
	cases := map[string]int{
		"http://localhost:53217":     53217,
		"http://127.0.0.1:8080":      8080,
		"https://example.com":        0,
		"https://example.com:8443":   8443,
	}
	for url, want := range cases {
		got, err := portFromBaseURL(url)
		if err != nil {
			t.Errorf("portFromBaseURL(%q): %v", url, err)
		}
		if got != want {
			t.Errorf("portFromBaseURL(%q)=%d want %d", url, got, want)
		}
	}
}

func TestSlugDataDir_Validation(t *testing.T) {
	cases := []struct {
		slug    string
		wantErr bool
	}{
		{"acme", false},
		{"acme-corp", false},
		{"a1_b2", false},
		{"ACME", true},        // uppercase
		{"acme.com", true},    // dot
		{"acme/etc", true},    // slash — path-traversal risk
		{"-acme", true},       // leading dash
		{"_acme", true},       // leading underscore
		{"", true},            // empty
	}
	for _, c := range cases {
		_, err := slugDataDir(c.slug)
		if (err != nil) != c.wantErr {
			t.Errorf("slugDataDir(%q): err=%v want_err=%v", c.slug, err, c.wantErr)
		}
	}
}

func TestUnwrapMCP_Envelope(t *testing.T) {
	// {"result": {"content": [{"text": "<json>"}]}} → parsed inner
	enveloped := `{"jsonrpc":"2.0","id":1,"result":{"content":[{"text":"{\"ok\":true,\"n\":42}"}]}}`
	got, err := unwrapMCP([]byte(enveloped))
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	m := got.(map[string]any)
	if m["ok"] != true || int(m["n"].(float64)) != 42 {
		t.Errorf("unwrapped wrong: %+v", m)
	}
}

func TestScrapeSetupToken_FindsBannerToken(t *testing.T) {
	// Mimics what the apteva CLI prints to fleet-child.log on first
	// boot. The scraper has to find the token whether it's on its
	// own line or inline with prose.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "fleet-child.log")
	if err := os.WriteFile(logPath, []byte(`
  Apteva is running.

    Dashboard:  http://localhost:53217
    First run — open the dashboard and use this setup token to create your admin account:
        apt_dce09898473aa033f389855eca23a6eb
    Server log: /tmp/server.log
`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := scrapeSetupToken(logPath, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	if got != "apt_dce09898473aa033f389855eca23a6eb" {
		t.Errorf("got %q", got)
	}
}

func TestScrapeSetupToken_TimesOutOnMissing(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "fleet-child.log")
	if err := os.WriteFile(logPath, []byte("nothing useful here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := scrapeSetupToken(logPath, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected scrape to time out when token is absent")
	}
}

func TestUnwrapMCP_AlreadyFlat(t *testing.T) {
	// Some callers/handlers return the inner JSON directly, no
	// envelope. The unwrapper should pass that through.
	got, err := unwrapMCP([]byte(`{"ok":true}`))
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	m := got.(map[string]any)
	if m["ok"] != true {
		t.Errorf("got %+v", m)
	}
}

// ─── Public host rewrite ────────────────────────────────────────────

func TestPublicBaseURL_LocalhostDisablesRewrite(t *testing.T) {
	a := &App{publicHost: "localhost"}
	got := a.publicBaseURL("http://localhost:53217")
	if got != "http://localhost:53217" {
		t.Errorf("got %q, want passthrough when publicHost==localhost", got)
	}
}

func TestPublicBaseURL_EmptyDisablesRewrite(t *testing.T) {
	a := &App{publicHost: ""}
	got := a.publicBaseURL("http://localhost:53217")
	if got != "http://localhost:53217" {
		t.Errorf("got %q, want passthrough when publicHost empty", got)
	}
}

func TestPublicBaseURL_RewritesLoopback(t *testing.T) {
	a := &App{publicHost: "91.99.117.197"}
	cases := map[string]string{
		"http://localhost:53217":    "http://91.99.117.197:53217",
		"http://127.0.0.1:8080":     "http://91.99.117.197:8080",
		"https://localhost:53217/x": "https://91.99.117.197:53217/x",
	}
	for in, want := range cases {
		if got := a.publicBaseURL(in); got != want {
			t.Errorf("publicBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPublicBaseURL_PreservesRemoteHosts(t *testing.T) {
	a := &App{publicHost: "91.99.117.197"}
	// tenant_connect rows store a real public hostname already —
	// we must not rewrite those (especially not to the fleet host's
	// IP, which would break health probes silently).
	got := a.publicBaseURL("https://tenant.example.com")
	if got != "https://tenant.example.com" {
		t.Errorf("got %q, expected remote host passthrough", got)
	}
}

// ─── Crypto roundtrip ───────────────────────────────────────────────

func TestKeyring_SealOpen(t *testing.T) {
	app, _ := newTestApp(t)
	for _, plain := range []string{"sk-short", "sk-" + repeat("x", 256), "non-ascii ★ τ"} {
		enc, err := app.keys.seal([]byte(plain))
		if err != nil {
			t.Fatalf("seal: %v", err)
		}
		dec, err := app.keys.open(enc)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if string(dec) != plain {
			t.Errorf("roundtrip mismatch: got %q want %q", dec, plain)
		}
	}
}

func TestKeyring_OpenRejectsTampered(t *testing.T) {
	app, _ := newTestApp(t)
	enc, _ := app.keys.seal([]byte("secret"))
	enc[len(enc)-1] ^= 0x01 // flip a bit in the AEAD tag
	if _, err := app.keys.open(enc); err == nil {
		t.Fatal("tampered ciphertext should fail to open")
	}
}

// ─── Store roundtrip — catches the v0.2.4 placeholder-count bug ─────

func TestStore_InsertGetList(t *testing.T) {
	app, _ := newTestApp(t)

	idA := seedTenant(t, app, "acme", StatusActive)
	idB := seedTenant(t, app, "beta", StatusSetupPending)

	got, _, err := app.store.get(idA)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Slug != "acme" || got.Status != StatusActive {
		t.Errorf("got %+v", got)
	}

	gotBySlug, _, err := app.store.getBySlug("beta")
	if err != nil {
		t.Fatalf("getBySlug: %v", err)
	}
	if gotBySlug.ID != idB {
		t.Errorf("getBySlug id mismatch: %q vs %q", gotBySlug.ID, idB)
	}

	list, err := app.store.list(nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("list count=%d want 2", len(list))
	}

	// List filter: status=setup_pending.
	filtered, err := app.store.list(map[string]string{"status": StatusSetupPending})
	if err != nil {
		t.Fatalf("list filtered: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != idB {
		t.Errorf("filtered=%+v want one row id=%q", filtered, idB)
	}
}

func TestStore_SetupTokenAndAttachKey(t *testing.T) {
	app, _ := newTestApp(t)

	// Seed a setup_pending tenant with a real sealed setup_token.
	tokEnc, _ := app.keys.seal([]byte("apt_abc123"))
	apiStub, _ := app.keys.seal([]byte("pending"))
	tn := &Tenant{
		Slug:       "alpha",
		Kind:       KindLocal,
		BaseURL:    "http://localhost:65534",
		OwnerEmail: "ops@alpha.io",
		Status:     StatusSetupPending,
	}
	if err := app.store.insert(tn, apiStub, tokEnc); err != nil {
		t.Fatalf("insert with setup_token: %v", err)
	}

	gotTok, err := app.store.getSetupToken(tn.ID)
	if err != nil {
		t.Fatalf("getSetupToken: %v", err)
	}
	plain, err := app.keys.open(gotTok)
	if err != nil {
		t.Fatalf("open setup_token: %v", err)
	}
	if string(plain) != "apt_abc123" {
		t.Errorf("setup_token mismatch: %q", plain)
	}

	// attachAPIKey must clear setup_token and flip status to active.
	realKey, _ := app.keys.seal([]byte("sk-real"))
	if err := app.store.attachAPIKey(tn.ID, realKey); err != nil {
		t.Fatalf("attachAPIKey: %v", err)
	}
	gotTok, err = app.store.getSetupToken(tn.ID)
	if err != nil {
		t.Fatalf("getSetupToken after attach: %v", err)
	}
	if len(gotTok) != 0 {
		t.Errorf("setup_token should be NULL after attach, got %d bytes", len(gotTok))
	}
	got, _, _ := app.store.get(tn.ID)
	if got.Status != StatusActive {
		t.Errorf("status after attach=%q want active", got.Status)
	}
}

func TestStore_EventsAndStatusChange(t *testing.T) {
	app, _ := newTestApp(t)
	id := seedTenant(t, app, "acme", StatusActive)

	if err := app.store.recordEvent(id, "test_event", "test-actor", map[string]any{"k": "v"}); err != nil {
		t.Fatalf("recordEvent: %v", err)
	}
	evts, err := app.store.recentEvents(id, 10)
	if err != nil {
		t.Fatalf("recentEvents: %v", err)
	}
	if len(evts) != 1 || evts[0].Kind != "test_event" {
		t.Errorf("events=%+v", evts)
	}

	if err := app.store.setStatus(id, StatusStopped, "test"); err != nil {
		t.Fatalf("setStatus: %v", err)
	}
	got, _, _ := app.store.get(id)
	if got.Status != StatusStopped {
		t.Errorf("status=%q want stopped", got.Status)
	}
	// setStatus also records a status_changed event.
	evts, _ = app.store.recentEvents(id, 10)
	if len(evts) < 2 || evts[0].Kind != "status_changed" {
		t.Errorf("status_changed event not recorded: %+v", evts)
	}
}

func TestStore_HardDeleteCascadesEvents(t *testing.T) {
	app, _ := newTestApp(t)
	id := seedTenant(t, app, "acme", StatusActive)
	_ = app.store.recordEvent(id, "ev1", "test", nil)
	_ = app.store.recordEvent(id, "ev2", "test", nil)

	if err := app.store.hardDelete(id); err != nil {
		t.Fatalf("hardDelete: %v", err)
	}
	// fleet_events has ON DELETE CASCADE → both rows gone.
	evts, _ := app.store.recentEvents(id, 10)
	if len(evts) != 0 {
		t.Errorf("events should cascade-delete, got %d", len(evts))
	}
}

// ─── Tool: tenant_create slug validation ────────────────────────────

func TestToolCreate_RequiresSlugAndOwner(t *testing.T) {
	app, ctx := newTestApp(t)
	cases := []map[string]any{
		{},
		{"slug": "acme"},                          // missing owner_email
		{"owner_email": "ops@acme.com"},           // missing slug
	}
	for _, args := range cases {
		if _, err := app.toolCreate(ctx, args); err == nil {
			t.Errorf("expected error for args=%+v", args)
		}
	}
}

func TestToolCreate_RejectsBadSlug(t *testing.T) {
	app, ctx := newTestApp(t)
	// All cases here must be rejected by slugDataDir BEFORE the
	// spawn path runs — otherwise the test spawns a real apteva
	// process on this machine and leaks it. Uppercase is NOT in
	// this list because toolCreate normalises with strings.ToLower
	// first; "ACME" → "acme" is intentionally accepted.
	for _, bad := range []string{"acme.com", "-acme", "_x", "a/b", "acme!"} {
		_, err := app.toolCreate(ctx, map[string]any{
			"slug":        bad,
			"owner_email": "ops@example.com",
		})
		if err == nil {
			t.Errorf("expected slug %q to be rejected", bad)
		}
	}
}

func TestToolCreate_RejectsDuplicateSlug(t *testing.T) {
	app, ctx := newTestApp(t)
	seedTenant(t, app, "acme", StatusActive)
	_, err := app.toolCreate(ctx, map[string]any{
		"slug":        "acme",
		"owner_email": "ops@example.com",
	})
	if err == nil {
		t.Fatal("expected duplicate slug to be rejected")
	}
	if !contains(err.Error(), "already in use") {
		t.Errorf("error %q should mention 'already in use'", err.Error())
	}
}

// ─── Auto-setup orchestrator ────────────────────────────────────────

// fakeAptevaServer mimics the apteva-server endpoints autoSetupTenant
// exercises. Configurable failure points so we can prove fleet falls
// back to setup_pending cleanly when the tenant misbehaves.
type fakeAptevaServer struct {
	srv             *httptest.Server
	expectedToken   string
	failRegister    bool
	failLogin       bool
	failKeys        bool
	registeredEmail string
	registeredPw    string
}

func newFakeAptevaServer(t *testing.T, expectedToken string) *fakeAptevaServer {
	t.Helper()
	f := &fakeAptevaServer{expectedToken: expectedToken}
	mux := http.NewServeMux()

	mux.HandleFunc("/api/auth/register", func(w http.ResponseWriter, r *http.Request) {
		if f.failRegister {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		if r.Header.Get("X-Setup-Token") != f.expectedToken {
			http.Error(w, "setup token required for first registration", http.StatusForbidden)
			return
		}
		var body struct{ Email, Password string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.registeredEmail = body.Email
		f.registeredPw = body.Password
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "email": body.Email})
	})

	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if f.failLogin {
			http.Error(w, "auth down", http.StatusInternalServerError)
			return
		}
		var body struct{ Email, Password string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Email != f.registeredEmail || body.Password != f.registeredPw {
			http.Error(w, "invalid", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "test-session", Path: "/"})
		_ = json.NewEncoder(w).Encode(map[string]any{"user_id": 1})
	})

	mux.HandleFunc("/api/auth/keys", func(w http.ResponseWriter, r *http.Request) {
		if f.failKeys {
			http.Error(w, "keys broken", http.StatusInternalServerError)
			return
		}
		c, _ := r.Cookie("session")
		if c == nil || c.Value != "test-session" {
			http.Error(w, "no session", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"key": "sk-autotest-key"})
	})

	mux.HandleFunc("/api/auth/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"user_id": 1})
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func TestAutoSetup_HappyPath(t *testing.T) {
	app, _ := newTestApp(t)
	f := newFakeAptevaServer(t, "apt_testtoken")

	got, err := app.autoSetupTenant(context.Background(), f.srv.URL, "apt_testtoken", "ops@acme.com", "")
	if err != nil {
		t.Fatalf("autoSetupTenant: %v", err)
	}
	if got.APIKey != "sk-autotest-key" {
		t.Errorf("api_key=%q want sk-autotest-key", got.APIKey)
	}
	if len(got.Password) != 32 {
		t.Errorf("generated password should be 32 hex chars, got %d", len(got.Password))
	}
	if f.registeredEmail != "ops@acme.com" {
		t.Errorf("fake recorded email=%q", f.registeredEmail)
	}
}

func TestAutoSetup_RegisterFailure(t *testing.T) {
	app, _ := newTestApp(t)
	f := newFakeAptevaServer(t, "apt_testtoken")
	f.failRegister = true

	_, err := app.autoSetupTenant(context.Background(), f.srv.URL, "apt_testtoken", "ops@acme.com", "")
	if err == nil {
		t.Fatal("expected autoSetupTenant to fail when register 500s")
	}
	if !contains(err.Error(), "register") {
		t.Errorf("error %q should mention register", err.Error())
	}
}

func TestAutoSetup_BadSetupToken(t *testing.T) {
	app, _ := newTestApp(t)
	f := newFakeAptevaServer(t, "apt_realone")

	_, err := app.autoSetupTenant(context.Background(), f.srv.URL, "apt_wrongone", "ops@acme.com", "")
	if err == nil {
		t.Fatal("expected register to reject wrong setup token")
	}
}

func TestAutoSetup_KeysFailure(t *testing.T) {
	app, _ := newTestApp(t)
	f := newFakeAptevaServer(t, "apt_testtoken")
	f.failKeys = true

	_, err := app.autoSetupTenant(context.Background(), f.srv.URL, "apt_testtoken", "ops@acme.com", "")
	if err == nil {
		t.Fatal("expected autoSetupTenant to fail when /api/auth/keys 500s")
	}
	if !contains(err.Error(), "keys") {
		t.Errorf("error %q should mention keys", err.Error())
	}
}

func TestAutoSetup_OperatorPassword(t *testing.T) {
	app, _ := newTestApp(t)
	f := newFakeAptevaServer(t, "apt_testtoken")

	got, err := app.autoSetupTenant(context.Background(), f.srv.URL, "apt_testtoken", "ops@acme.com", "manual-pw-1234")
	if err != nil {
		t.Fatalf("autoSetupTenant: %v", err)
	}
	if got.Password != "manual-pw-1234" {
		t.Errorf("operator-supplied password should pass through, got %q", got.Password)
	}
	if f.registeredPw != "manual-pw-1234" {
		t.Errorf("fake recorded pw=%q", f.registeredPw)
	}
}

func TestRandomPassword_Format(t *testing.T) {
	pw := randomPassword()
	if len(pw) != 32 {
		t.Errorf("len=%d want 32", len(pw))
	}
	for _, c := range pw {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("password char %q outside hex alphabet", c)
		}
	}
	// Sanity: two calls must produce different values. crypto/rand
	// collision rate is astronomically low.
	if pw == randomPassword() {
		t.Error("randomPassword returned identical value twice")
	}
}

// ─── Tool: tenant_attach_key ────────────────────────────────────────

func TestToolAttachKey_FlipsToActive(t *testing.T) {
	app, ctx := newTestApp(t)
	srv := fakeTenantServer(t, true /* auth OK */)

	// Seed a setup_pending tenant pointing at the fake server.
	tokEnc, _ := app.keys.seal([]byte("apt_token"))
	apiStub, _ := app.keys.seal([]byte("pending"))
	tn := &Tenant{
		Slug:       "acme",
		Kind:       KindLocal,
		BaseURL:    srv.URL,
		OwnerEmail: "ops@acme.com",
		Status:     StatusSetupPending,
	}
	if err := app.store.insert(tn, apiStub, tokEnc); err != nil {
		t.Fatalf("insert: %v", err)
	}

	out, err := app.toolAttachKey(ctx, map[string]any{
		"tenant_id": tn.ID,
		"api_key":   "sk-real",
	})
	if err != nil {
		t.Fatalf("attach_key: %v", err)
	}
	res := out.(map[string]any)
	if res["status"] != StatusActive {
		t.Errorf("status=%v want active", res["status"])
	}

	got, _, _ := app.store.get(tn.ID)
	if got.Status != StatusActive {
		t.Errorf("DB status=%q want active", got.Status)
	}
}

func TestToolAttachKey_RejectsBadKey(t *testing.T) {
	app, ctx := newTestApp(t)
	srv := fakeTenantServer(t, false /* 401 on auth/status */)

	tokEnc, _ := app.keys.seal([]byte("apt_token"))
	apiStub, _ := app.keys.seal([]byte("pending"))
	tn := &Tenant{
		Slug:       "beta",
		Kind:       KindLocal,
		BaseURL:    srv.URL,
		OwnerEmail: "ops@beta.io",
		Status:     StatusSetupPending,
	}
	_ = app.store.insert(tn, apiStub, tokEnc)

	_, err := app.toolAttachKey(ctx, map[string]any{
		"tenant_id": tn.ID,
		"api_key":   "sk-bad",
	})
	if err == nil {
		t.Fatal("expected attach_key to fail on 401")
	}
	// Tenant should remain in setup_pending — no half-attached state.
	got, _, _ := app.store.get(tn.ID)
	if got.Status != StatusSetupPending {
		t.Errorf("status after failed attach=%q want setup_pending", got.Status)
	}
}

func TestToolAttachKey_RejectsAlreadyActive(t *testing.T) {
	app, ctx := newTestApp(t)
	id := seedTenant(t, app, "acme", StatusActive)
	_, err := app.toolAttachKey(ctx, map[string]any{
		"tenant_id": id,
		"api_key":   "sk-new",
	})
	if err == nil {
		t.Fatal("expected error for already-active tenant")
	}
}

// ─── Tool: tenant_get decoration ────────────────────────────────────

func TestToolGet_SurfacesSetupToken(t *testing.T) {
	app, ctx := newTestApp(t)
	tokEnc, _ := app.keys.seal([]byte("apt_visible"))
	apiStub, _ := app.keys.seal([]byte("pending"))
	tn := &Tenant{
		Slug: "gamma", Kind: KindLocal,
		BaseURL: "http://localhost:65530", OwnerEmail: "ops@gamma.io",
		Status: StatusSetupPending,
	}
	_ = app.store.insert(tn, apiStub, tokEnc)

	out, err := app.toolGet(ctx, map[string]any{"tenant_id": tn.ID})
	if err != nil {
		t.Fatalf("toolGet: %v", err)
	}
	res := out.(map[string]any)
	if res["setup_token"] != "apt_visible" {
		t.Errorf("setup_token=%v", res["setup_token"])
	}
	if res["setup_url"] != "http://localhost:65530/?setup=1" {
		t.Errorf("setup_url=%v", res["setup_url"])
	}
}

func TestToolGet_OmitsSetupTokenAfterAttach(t *testing.T) {
	app, ctx := newTestApp(t)
	id := seedTenant(t, app, "acme", StatusActive)
	out, err := app.toolGet(ctx, map[string]any{"tenant_id": id})
	if err != nil {
		t.Fatalf("toolGet: %v", err)
	}
	res := out.(map[string]any)
	if _, ok := res["setup_token"]; ok {
		t.Errorf("setup_token should not be present on active tenants")
	}
}

// ─── Tool: tenant_list filters ──────────────────────────────────────

func TestToolList_Filters(t *testing.T) {
	app, ctx := newTestApp(t)
	seedTenant(t, app, "a", StatusActive)
	seedTenant(t, app, "b", StatusSetupPending)
	seedTenant(t, app, "c", StatusActive)

	out, _ := app.toolList(ctx, map[string]any{"status": StatusActive})
	res := out.(map[string]any)
	if res["count"].(int) != 2 {
		t.Errorf("count=%v want 2", res["count"])
	}
}

// ─── Tool: tenant_delete ────────────────────────────────────────────

func TestToolDelete_LocalWithoutConfirm_PreservesData(t *testing.T) {
	app, ctx := newTestApp(t)
	id := seedTenant(t, app, "acme", StatusStopped)
	out, err := app.toolDelete(ctx, map[string]any{"tenant_id": id})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	res := out.(map[string]any)
	if res["status"] != StatusStopped {
		t.Errorf("status=%v want stopped (process stopped, data preserved)", res["status"])
	}
	// Row still present.
	if _, _, err := app.store.get(id); err != nil {
		t.Errorf("row should still exist without confirm: %v", err)
	}
}

func TestToolDelete_LocalWithConfirm_RemovesRow(t *testing.T) {
	app, ctx := newTestApp(t)
	id := seedTenant(t, app, "acme", StatusStopped)
	// Don't set ConfigDir to a real path — the seed set it under
	// t.TempDir() which os.RemoveAll handles fine.
	_, err := app.toolDelete(ctx, map[string]any{
		"tenant_id": id,
		"confirm":   true,
	})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, _, err := app.store.get(id); err == nil {
		t.Errorf("row should be hard-deleted with confirm=true")
	}
}

// ─── Setup-pending guards on remote ops ─────────────────────────────

func TestRunRemote_RefusesSetupPending(t *testing.T) {
	app, ctx := newTestApp(t)
	id := seedTenant(t, app, "acme", StatusSetupPending)
	_, err := app.toolRunRemote(ctx, map[string]any{
		"tenant_id": id,
		"app":       "tasks",
		"tool":      "task_list",
		"input":     map[string]any{},
	})
	if err == nil {
		t.Fatal("run_remote should refuse setup_pending tenants")
	}
	if !contains(err.Error(), "setup_pending") {
		t.Errorf("error %q should mention setup_pending", err.Error())
	}
}

func TestSupportLogin_RefusesSetupPending(t *testing.T) {
	app, ctx := newTestApp(t)
	id := seedTenant(t, app, "acme", StatusSetupPending)
	_, err := app.toolSupportLogin(ctx, map[string]any{
		"tenant_id": id,
		"reason":    "audit test",
	})
	if err == nil {
		t.Fatal("support_login should refuse setup_pending tenants")
	}
	if !contains(err.Error(), "setup_pending") {
		t.Errorf("error %q should mention setup_pending", err.Error())
	}
}

// ─── tiny helpers ───────────────────────────────────────────────────

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
