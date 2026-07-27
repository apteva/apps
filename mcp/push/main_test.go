package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type providerCall struct {
	token    string
	delivery delivery
}

type providerStub struct {
	calls []providerCall
}

func (p *providerStub) send(_ *sdk.AppCtx, token string, _ *device, d *delivery) (*providerResult, error) {
	p.calls = append(p.calls, providerCall{token: token, delivery: *d})
	return &providerResult{ID: "apns-test-id"}, nil
}

type credentialPlatform struct {
	tk.BasePlatformClient
	executeInput map[string]any
}

func (*credentialPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{
		AppName:  "push",
		Bindings: map[string]any{"ios_provider": float64(7)},
	}, nil
}

func (*credentialPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "apple-push-notifications", Status: "active"}, nil
}

func (*credentialPlatform) GetConnectionCredentials(id int64) (*sdk.ConnectionCredentials, error) {
	return &sdk.ConnectionCredentials{
		ConnectionID: id,
		Slug:         "apple-push-notifications",
		Fields:       map[string]string{"relay_encryption_key": "connection-generated-secret-with-32-bytes"},
	}, nil
}

func (p *credentialPlatform) ExecuteIntegrationTool(_ int64, _ string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.executeInput = input
	return &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Headers: map[string]string{"Apns-Id": "apns-routing-test"},
	}, nil
}

type testHarness struct {
	app    *App
	mux    *http.ServeMux
	caller *providerStub
}

func newHarness(t *testing.T) *testHarness {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	provider := &providerStub{}
	app := &App{
		provider:      provider,
		encryptionKey: "test-only-key-with-at-least-24-characters",
	}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	for _, route := range app.HTTPRoutes() {
		mux.HandleFunc(route.Method+" "+route.Pattern, route.Handler)
	}
	return &testHarness{app: app, mux: mux, caller: provider}
}

func (h *testHarness) request(t *testing.T, method, path, grant string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &payload)
	req.Header.Set("Content-Type", "application/json")
	if grant != "" {
		req.Header.Set("Authorization", "Bearer "+grant)
	}
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, req)
	return recorder
}

func decodeResponse[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
	return value
}

func TestManifestExposesDashboardPanel(t *testing.T) {
	manifest := (&App{}).Manifest()
	if manifest.Version != "0.2.1" {
		t.Fatalf("manifest version=%q, want 0.2.1", manifest.Version)
	}
	if len(manifest.Provides.UIPanels) != 1 {
		t.Fatalf("ui panels=%d, want 1", len(manifest.Provides.UIPanels))
	}
	panel := manifest.Provides.UIPanels[0]
	if panel.Slot != "project.page" || panel.Entry != "/ui/PushPanel.mjs" {
		t.Fatalf("unexpected dashboard panel: %+v", panel)
	}
}

func TestRegisterAndDeliver(t *testing.T) {
	h := newHarness(t)
	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	registered := h.request(t, http.MethodPost, "/v1/devices/register", "", map[string]any{
		"provider_token": token,
		"platform":       "ios",
		"bundle_id":      "ai.apteva.mobile",
		"environment":    "sandbox",
		"instance_ref":   "local-instance",
		"app_version":    "0.1",
	})
	if registered.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", registered.Code, registered.Body)
	}
	body := decodeResponse[struct {
		Device device `json:"device"`
		Grant  string `json:"grant"`
	}](t, registered)
	if body.Device.ID == "" || body.Grant == "" {
		t.Fatalf("incomplete registration: %+v", body)
	}
	if body.Device.BundleID != "ai.apteva.mobile" || body.Device.Environment != "sandbox" {
		t.Fatalf("device routing not stored: %+v", body.Device)
	}

	delivered := h.request(t, http.MethodPost, "/v1/deliveries", body.Grant, map[string]any{
		"device_id":       body.Device.ID,
		"type":            "approval",
		"item_id":         "approval-42",
		"idempotency_key": "event-42",
		"badge":           3,
	})
	if delivered.Code != http.StatusCreated {
		t.Fatalf("delivery status=%d body=%s", delivered.Code, delivered.Body)
	}
	push := decodeResponse[delivery](t, delivered)
	if push.Status != "sent" || push.ProviderID != "apns-test-id" {
		t.Fatalf("unexpected delivery: %+v", push)
	}
	if len(h.caller.calls) != 1 || h.caller.calls[0].token != token {
		t.Fatalf("provider calls: %+v", h.caller.calls)
	}
	title, detail := notificationCopy(h.caller.calls[0].delivery.Type)
	if title != "Approval required" || detail != "Open Apteva to review." {
		t.Fatalf("unsafe or incorrect notification copy: %q / %q", title, detail)
	}

	duplicate := h.request(t, http.MethodPost, "/v1/deliveries", body.Grant, map[string]any{
		"device_id":       body.Device.ID,
		"type":            "approval",
		"item_id":         "approval-42",
		"idempotency_key": "event-42",
	})
	if duplicate.Code != http.StatusOK || len(h.caller.calls) != 1 {
		t.Fatalf("idempotent request dispatched again: status=%d calls=%d", duplicate.Code, len(h.caller.calls))
	}
}

func TestGrantCannotControlAnotherDevice(t *testing.T) {
	h := newHarness(t)
	register := func(token string) struct {
		Device device `json:"device"`
		Grant  string `json:"grant"`
	} {
		response := h.request(t, http.MethodPost, "/v1/devices/register", "", map[string]any{
			"provider_token": token,
			"platform":       "ios",
			"bundle_id":      "ai.apteva.mobile",
			"environment":    "sandbox",
			"instance_ref":   "instance",
		})
		return decodeResponse[struct {
			Device device `json:"device"`
			Grant  string `json:"grant"`
		}](t, response)
	}
	first := register("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	second := register("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	response := h.request(t, http.MethodDelete, "/v1/devices/"+second.Device.ID, first.Grant, nil)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-device revoke status=%d body=%s", response.Code, response.Body)
	}
}

func TestDeviceTokensAreEncryptedAtRest(t *testing.T) {
	h := newHarness(t)
	token := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	response := h.request(t, http.MethodPost, "/v1/devices/register", "", map[string]any{
		"provider_token": token,
		"platform":       "ios",
		"bundle_id":      "ai.apteva.mobile",
		"environment":    "sandbox",
		"instance_ref":   "instance",
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", response.Code, response.Body)
	}
	var ciphertext string
	if err := h.app.store.db.QueryRow(`SELECT token_ciphertext FROM devices LIMIT 1`).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if ciphertext == token || ciphertext == "" {
		t.Fatalf("device token was not encrypted: %q", ciphertext)
	}
	plain, err := h.app.cipher.decrypt(ciphertext)
	if err != nil || plain != token {
		t.Fatalf("encrypted token did not round trip: %q, %v", plain, err)
	}
}

func TestRegistrationRejectsInvalidRouting(t *testing.T) {
	h := newHarness(t)
	response := h.request(t, http.MethodPost, "/v1/devices/register", "", map[string]any{
		"provider_token": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		"platform":       "ios",
		"bundle_id":      "not a bundle id",
		"environment":    "staging",
		"instance_ref":   "instance",
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid routing status=%d body=%s", response.Code, response.Body)
	}
}

func TestBackfillLegacyDeviceRouting(t *testing.T) {
	h := newHarness(t)
	now := "2026-07-27T00:00:00Z"
	_, err := h.app.store.db.Exec(`
		INSERT INTO devices
			(id, token_ciphertext, token_hash, platform, user_ref, app_version, status, last_seen_at, created_at)
		VALUES ('legacy', 'ciphertext', 'legacy-token', 'ios', '', '', 'active', ?, ?)`,
		now, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.app.store.backfillDeviceRouting("ai.apteva.mobile", "sandbox"); err != nil {
		t.Fatal(err)
	}
	legacy, err := h.app.store.deviceByID("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.BundleID != "ai.apteva.mobile" || legacy.Environment != "sandbox" {
		t.Fatalf("legacy routing not backfilled: %+v", legacy)
	}
}

func TestManifestHasNoMCPTools(t *testing.T) {
	manifest := (&App{}).Manifest()
	if manifest.Name != "push" {
		t.Fatalf("manifest name=%q", manifest.Name)
	}
	if err := sdk.ValidateManifest(&manifest); err != nil {
		t.Fatalf("invalid manifest: %v", err)
	}
	if len(manifest.Provides.MCPTools) != 0 {
		t.Fatalf("Push MVP should not expose MCP tools: %+v", manifest.Provides.MCPTools)
	}
}

func TestConnectionEncryptionKeyComesFromBoundIntegration(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(&credentialPlatform{}))
	key, err := connectionEncryptionKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if key != "connection-generated-secret-with-32-bytes" {
		t.Fatalf("key=%q", key)
	}
}

func TestAPNsProviderUsesDeviceRouting(t *testing.T) {
	platform := &credentialPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(platform))
	_, err := (apnsProvider{}).send(ctx, "device-token", &device{
		BundleID:    "com.example.second-app",
		Environment: "production",
	}, &delivery{Type: "alert", ItemID: "alert-1"})
	if err != nil {
		t.Fatal(err)
	}
	if platform.executeInput["topic"] != "com.example.second-app" {
		t.Fatalf("topic=%v", platform.executeInput["topic"])
	}
	if platform.executeInput["environment"] != "production" {
		t.Fatalf("environment=%v", platform.executeInput["environment"])
	}
}
