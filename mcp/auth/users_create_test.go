package main

// users_create_test — auth_users_create MCP tool + the opt-in
// reset-email contract. The bulk-email-import use case requires that
// creating a user with no password sends NO email unless explicitly
// asked, so an agent can provision N imported addresses silently.

import (
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
