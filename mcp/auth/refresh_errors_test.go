package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRefreshFailureBeforeCommitIsRetryable(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	app := &App{}
	signup := decode(t, callJSON(app.handleSignup, "POST", "/signup", map[string]any{"email": "persist@example.com", "password": "GoodPassword123", "client_id": cid}))
	body := map[string]any{"client_id": cid, "refresh_token": signup["refresh_token"]}
	_, err := ctx.AppDB().Exec(`CREATE TRIGGER reject_fixture_rotation BEFORE UPDATE OF revoked_at ON sessions BEGIN SELECT RAISE(ABORT, 'private fixture database unavailable'); END`)
	if err != nil {
		t.Fatal(err)
	}
	w := callJSON(app.handleRefresh, "POST", "/refresh", body)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "refresh_unavailable") || strings.Contains(w.Body.String(), "private fixture") {
		t.Fatalf("temporary error: %d %s", w.Code, w.Body)
	}
	if _, err := ctx.AppDB().Exec(`DROP TRIGGER reject_fixture_rotation`); err != nil {
		t.Fatal(err)
	}
	w = callJSON(app.handleRefresh, "POST", "/refresh", body)
	if w.Code != 200 {
		t.Fatalf("original refresh was consumed despite rollback: %d %s", w.Code, w.Body)
	}
	rotated := decode(t, w)
	w = callJSON(app.handleRefresh, "POST", "/refresh", body)
	if w.Code != 401 {
		t.Fatalf("reuse: %d %s", w.Code, w.Body)
	}
	w = callJSON(app.handleRefresh, "POST", "/refresh", map[string]any{"client_id": cid, "refresh_token": rotated["refresh_token"]})
	if w.Code != 401 {
		t.Fatalf("reused family remained active: %d %s", w.Code, w.Body)
	}
}

func TestRefreshLookupOutageIsNotInvalidGrant(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	app := &App{}
	signup := decode(t, callJSON(app.handleSignup, "POST", "/signup", map[string]any{"email": "lookup@example.com", "password": "GoodPassword123", "client_id": cid}))
	body := map[string]any{"client_id": cid, "refresh_token": signup["refresh_token"]}
	if _, err := ctx.AppDB().Exec(`ALTER TABLE sessions RENAME TO fixture_unavailable_sessions`); err != nil {
		t.Fatal(err)
	}
	w := callJSON(app.handleRefresh, "POST", "/refresh", body)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "refresh_unavailable") {
		t.Fatalf("lookup failure: %d %s", w.Code, w.Body)
	}
	w = callJSON(app.handleLogout, "POST", "/logout", body)
	if w.Code != 503 {
		t.Fatalf("logout falsely confirmed revocation: %d %s", w.Code, w.Body)
	}
	if _, err := ctx.AppDB().Exec(`ALTER TABLE fixture_unavailable_sessions RENAME TO sessions`); err != nil {
		t.Fatal(err)
	}
	if w := callJSON(app.handleRefresh, "POST", "/refresh", body); w.Code != 200 {
		t.Fatalf("recovery: %d %s", w.Code, w.Body)
	}
}

func TestUncertainRefreshCommitDoesNotAdvertiseSafeRetry(t *testing.T) {
	w := httptest.NewRecorder()
	writeRefreshError(w, errRefreshUncertain)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "refresh_uncertain") || strings.Contains(w.Body.String(), "refresh_unavailable") {
		t.Fatalf("uncertain response: %d %s", w.Code, w.Body)
	}
}

func TestMissingSigningKeyPreservesRefreshForRecovery(t *testing.T) {
	ctx, cid := newAuthCtx(t)
	app := &App{}
	signup := decode(t, callJSON(app.handleSignup, "POST", "/signup", map[string]any{"email": "keys@example.com", "password": "GoodPassword123", "client_id": cid}))
	body := map[string]any{"client_id": cid, "refresh_token": signup["refresh_token"]}
	if _, err := ctx.AppDB().Exec(`UPDATE signing_keys SET retired_at='2026-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	w := callJSON(app.handleRefresh, "POST", "/refresh", body)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "refresh_unavailable") {
		t.Fatalf("missing key rejected valid session: %d %s", w.Code, w.Body)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE signing_keys SET retired_at=NULL`); err != nil {
		t.Fatal(err)
	}
	if w := callJSON(app.handleRefresh, "POST", "/refresh", body); w.Code != 200 {
		t.Fatalf("refresh not preserved: %d %s", w.Code, w.Body)
	}
}
