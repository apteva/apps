package main

// users_create_test — auth_users_create MCP tool + the opt-in
// reset-email contract. The bulk-email-import use case requires that
// creating a user with no password sends NO email unless explicitly
// asked, so an agent can provision N imported addresses silently.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestUsersCreate_NoPasswordSendsNoEmailByDefault(t *testing.T) {
	ctx, _ := newAuthCtx(t)
	app := &App{}

	out, err := app.toolUsersCreate(ctx, map[string]any{
		"organization_slug": "default",
		"email":             "import-1@example.com",
		// no password, no send_password_reset → silent create
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	m := out.(map[string]any)
	if _, sent := m["password_reset_sent"]; sent {
		t.Error("password_reset_sent should be absent — no email expected on a silent import")
	}
	u := m["user"].(*User)
	if u.HasPassword {
		t.Error("imported user should have no password set")
	}

	// No reset-email audit row should exist for this user.
	auditOut, _ := app.toolAuditSearch(ctx, map[string]any{
		"organization_slug": "default",
		"user_id":           u.ID,
	})
	for _, ev := range auditOut.(map[string]any)["events"].([]AuditEvent) {
		if ev.Event == "password_reset_sent" {
			t.Errorf("unexpected password_reset_sent audit event for silently-imported user %d", u.ID)
		}
	}
}

func TestUsersCreate_SendResetWhenRequested(t *testing.T) {
	ctx, _ := newAuthCtx(t)
	app := &App{}

	out, err := app.toolUsersCreate(ctx, map[string]any{
		"organization_slug":   "default",
		"email":               "invite-me@example.com",
		"send_password_reset": true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	m := out.(map[string]any)
	if sent, _ := m["password_reset_sent"].(bool); !sent {
		t.Error("password_reset_sent should be true when send_password_reset=true")
	}
}

func TestUsersCreate_WithPasswordCanLogin(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	app := &App{}

	if _, err := app.toolUsersCreate(ctx, map[string]any{
		"organization_slug": "default",
		"email":             "svc@example.com",
		"password":          "service-account-pw",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// The created user (email_verified defaults true) can log in.
	rec := callJSON(app.handleLogin, "POST", "/login", map[string]any{
		"email":     "svc@example.com",
		"password":  "service-account-pw",
		"client_id": clientID,
	})
	if rec.Code != 200 {
		t.Fatalf("login of created user expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUsersCreateAndUpdateMetadata(t *testing.T) {
	ctx, _ := newAuthCtx(t)
	app := &App{}

	out, err := app.toolUsersCreate(ctx, map[string]any{
		"organization_slug": "default",
		"email":             "metadata@example.com",
		"metadata": map[string]any{
			"onboarding_status": "started",
			"onboarding_step":   "profile",
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	user := out.(map[string]any)["user"].(*User)
	assertUserMetadata(t, user, "onboarding_status", "started")

	updatedOut, err := app.toolUsersUpdate(ctx, map[string]any{
		"organization_slug": "default",
		"user_id":           user.ID,
		"metadata": map[string]any{
			"onboarding_status": "completed",
			"onboarding_step":   "done",
		},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	updated := updatedOut.(map[string]any)["user"].(*User)
	assertUserMetadata(t, updated, "onboarding_status", "completed")

	gotOut, err := app.toolUsersGet(ctx, map[string]any{
		"organization_slug": "default",
		"id":                user.ID,
	})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got := gotOut.(map[string]any)["user"].(*User)
	assertUserMetadata(t, got, "onboarding_step", "done")
}

func TestUsersUpdateRejectsNonObjectMetadata(t *testing.T) {
	ctx, _ := newAuthCtx(t)
	app := &App{}

	out, err := app.toolUsersCreate(ctx, map[string]any{
		"organization_slug": "default",
		"email":             "bad-metadata@example.com",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	user := out.(map[string]any)["user"].(*User)

	if _, err := app.toolUsersUpdate(ctx, map[string]any{
		"organization_slug": "default",
		"user_id":           user.ID,
		"metadata":          []any{"not", "an", "object"},
	}); err == nil {
		t.Fatal("expected non-object metadata to be rejected")
	}
}

func TestUsersCreate_RequiresOrg(t *testing.T) {
	ctx, _ := newAuthCtx(t)
	app := &App{}
	_, err := app.toolUsersCreate(ctx, map[string]any{
		"email": "no-org@example.com",
	})
	if err == nil {
		t.Fatal("create without organization should error")
	}
}

func assertUserMetadata(t *testing.T, user *User, key, want string) {
	t.Helper()
	var metadata map[string]any
	if err := json.Unmarshal(user.Metadata, &metadata); err != nil {
		t.Fatalf("metadata is not valid JSON: %v raw=%s", err, string(user.Metadata))
	}
	if got, _ := metadata[key].(string); got != want {
		t.Fatalf("metadata[%s] = %q, want %q (raw=%s)", key, got, want, string(user.Metadata))
	}
}

func TestUsersCreate_DuplicateEmailErrors(t *testing.T) {
	ctx, _ := newAuthCtx(t)
	app := &App{}
	args := map[string]any{
		"organization_slug": "default",
		"email":             "dup@example.com",
		"password":          "first-password",
	}
	if _, err := app.toolUsersCreate(ctx, args); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := app.toolUsersCreate(ctx, args); err == nil {
		t.Error("duplicate email should error")
	}
}

func TestUsersSetPasswordTool_ChangesLoginPassword(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	app := &App{}

	out, err := app.toolUsersCreate(ctx, map[string]any{
		"organization_slug": "default",
		"email":             "reset-tool@example.com",
		"password":          "old-password",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	user := out.(map[string]any)["user"].(*User)

	if _, err := app.toolUsersSetPassword(ctx, map[string]any{
		"organization_slug": "default",
		"user_id":           user.ID,
		"password":          "new-password",
	}); err != nil {
		t.Fatalf("set password: %v", err)
	}

	rec := callJSON(app.handleLogin, "POST", "/login", map[string]any{
		"email":     "reset-tool@example.com",
		"password":  "old-password",
		"client_id": clientID,
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("old password expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = callJSON(app.handleLogin, "POST", "/login", map[string]any{
		"email":     "reset-tool@example.com",
		"password":  "new-password",
		"client_id": clientID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("new password expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminUsersSetPasswordRoute(t *testing.T) {
	ctx, clientID := newAuthCtx(t)
	app := &App{}

	out, err := app.toolUsersCreate(ctx, map[string]any{
		"organization_slug": "default",
		"email":             "reset-route@example.com",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	user := out.(map[string]any)["user"].(*User)

	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{
		"password":        "route-password",
		"revoke_sessions": true,
	})
	req := httptest.NewRequest("POST", "/admin/users/"+strconv.FormatInt(user.ID, 10)+"/set_password?organization_slug=default", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", strconv.FormatInt(user.ID, 10))
	rec := httptest.NewRecorder()
	app.handleAdminUsersSetPassword(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("set password route expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	login := callJSON(app.handleLogin, "POST", "/login", map[string]any{
		"email":     "reset-route@example.com",
		"password":  "route-password",
		"client_id": clientID,
	})
	if login.Code != http.StatusOK {
		t.Fatalf("login with route password expected 200, got %d body=%s", login.Code, login.Body.String())
	}
}
