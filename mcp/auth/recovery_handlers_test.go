package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

type recoveryMessagingStub struct {
	tk.BasePlatformClient
	app   string
	tool  string
	input map[string]any
}

func (s *recoveryMessagingStub) CallAppResult(app, tool string, input map[string]any, out any) error {
	s.app, s.tool, s.input = app, tool, input
	raw, _ := json.Marshal(map[string]any{"status": "sent"})
	return json.Unmarshal(raw, out)
}

func seedRecoveryUser(t *testing.T, email, password string) (*App, string, *Organization, *Client, int64) {
	t.Helper()
	ctx, clientID := newAuthCtx(t)
	org, err := dbGetOrgBySlug(ctx.AppDB(), "test-proj", "default")
	if err != nil {
		t.Fatalf("get org: %v", err)
	}
	client, err := dbGetClientByClientID(ctx.AppDB(), "test-proj", clientID)
	if err != nil {
		t.Fatalf("get client: %v", err)
	}
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	uid, err := dbCreateUser(ctx.AppDB(), "test-proj", org.ID, email, hash, "Recovery User", false, "{}")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return &App{}, clientID, org, client, uid
}

func insertRecoveryToken(t *testing.T, orgID, userID int64, kind, raw, clientID string) {
	t.Helper()
	meta, _ := json.Marshal(recoveryLinkOptions{ClientID: clientID})
	if err := dbInsertVerificationToken(globalCtx.AppDB(), "test-proj", orgID, userID, kind, hashToken(raw), string(meta), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("insert token: %v", err)
	}
}

func TestEmailVerifyRequiresLoginAndIsSingleUse(t *testing.T) {
	app, clientID, org, _, uid := seedRecoveryUser(t, "verify@example.com", "Before!Password123")
	insertRecoveryToken(t, org.ID, uid, "verify_email", "verify-token", clientID)

	rec := callJSON(app.handleEmailVerify, http.MethodPost, "/email/verify", map[string]any{
		"client_id": clientID,
		"token":     "verify-token",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("verify status=%d body=%s", rec.Code, rec.Body.String())
	}
	if token, _ := decode(t, rec)["access_token"].(string); token != "" {
		t.Fatal("verification must require a fresh login")
	}
	user, err := dbGetUserByID(globalCtx.AppDB(), "test-proj", org.ID, uid)
	if err != nil || user.EmailVerifiedAt == "" {
		t.Fatalf("email not marked verified: user=%+v err=%v", user, err)
	}

	rec = callJSON(app.handleEmailVerify, http.MethodPost, "/email/verify", map[string]any{
		"client_id": clientID,
		"token":     "verify-token",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reused token status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRecoveryTokenCannotCrossClientsOrBeBurnedByWrongClient(t *testing.T) {
	app, clientID, org, _, uid := seedRecoveryUser(t, "scoped@example.com", "Before!Password123")
	out, err := app.toolClientsCreate(globalCtx, map[string]any{
		"organization_slug": "default",
		"name":              "other-client",
		"type":              "spa",
		"redirect_uris":     []any{"http://localhost:3000/callback"},
	})
	if err != nil {
		t.Fatalf("create other client: %v", err)
	}
	otherClientID := out.(map[string]any)["client_id"].(string)
	insertRecoveryToken(t, org.ID, uid, "verify_email", "client-bound-token", clientID)

	rec := callJSON(app.handleEmailVerify, http.MethodPost, "/email/verify", map[string]any{
		"client_id": otherClientID,
		"token":     "client-bound-token",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cross-client verify status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = callJSON(app.handleEmailVerify, http.MethodPost, "/email/verify", map[string]any{
		"client_id": clientID,
		"token":     "client-bound-token",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("correct client could not redeem token: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPasswordResetChangesPasswordAndRequiresLogin(t *testing.T) {
	app, clientID, org, _, uid := seedRecoveryUser(t, "reset@example.com", "Before!Password123")
	insertRecoveryToken(t, org.ID, uid, "reset_password", "reset-token", clientID)

	rec := callJSON(app.handlePasswordResetConfirm, http.MethodPost, "/password/reset/confirm", map[string]any{
		"client_id": clientID,
		"token":     "reset-token",
		"password":  "After!Password456",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", rec.Code, rec.Body.String())
	}
	if token, _ := decode(t, rec)["access_token"].(string); token != "" {
		t.Fatal("password reset must require a fresh login")
	}

	rec = callJSON(app.handleLogin, http.MethodPost, "/login", map[string]any{
		"client_id": clientID,
		"email":     "reset@example.com",
		"password":  "After!Password456",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login with reset password status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPasswordResetRequestIsNonEnumeratingAndValidatesContinueURL(t *testing.T) {
	app, clientID, _, _, _ := seedRecoveryUser(t, "known@example.com", "Before!Password123")
	rec := callJSON(app.handlePasswordResetRequest, http.MethodPost, "/password/reset/request", map[string]any{
		"client_id": clientID,
		"email":     "missing@example.com",
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("unknown email status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = callJSON(app.handlePasswordResetRequest, http.MethodPost, "/password/reset/request", map[string]any{
		"client_id":    clientID,
		"email":        "known@example.com",
		"continue_url": "https://evil.example/steal",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("untrusted continue URL status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRecoveryEmailUsesMessagingApp(t *testing.T) {
	platform := &recoveryMessagingStub{}
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithPlatform(platform),
		tk.WithConfig(map[string]string{"from_email": "courses@example.test"}),
	)
	err := sendRecoveryEmail(ctx, "test-proj", "learner@example.test", "Makecademy", "Verify your email",
		"Finish setting up your account.", "Verify email", "https://courses.example.test/#verify=secret", "verify-once")
	if err != nil {
		t.Fatal(err)
	}
	if platform.app != "messaging" || platform.tool != "send_message" {
		t.Fatalf("unexpected app call %s:%s", platform.app, platform.tool)
	}
	for field, want := range map[string]any{
		"channel": "email", "from": "courses@example.test", "from_name": "Makecademy",
		"to": "learner@example.test", "idempotency_key": "verify-once",
	} {
		if platform.input[field] != want {
			t.Errorf("messaging %s=%v, want %v", field, platform.input[field], want)
		}
	}
}
