package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type extensionPlatformStub struct {
	tk.BasePlatformClient
	calls []struct {
		app, tool string
		input     map[string]any
	}
	fail bool
}

func (s *extensionPlatformStub) CallAppResult(app, tool string, input map[string]any, out any) error {
	s.calls = append(s.calls, struct {
		app, tool string
		input     map[string]any
	}{app: app, tool: tool, input: input})
	if s.fail {
		return errors.New("provider unavailable")
	}
	body, _ := json.Marshal(map[string]any{
		"product": map[string]any{"title": "Desk Lamp", "status": "active"},
		"cart":    map[string]any{"id": 42},
	})
	return json.Unmarshal(body, out)
}

func testExtensionManifest() ExtensionManifest {
	return ExtensionManifest{
		Name:     "Example Store",
		Version:  "1",
		Settings: map[string]any{"accent": "#111111"},
		SettingsSchema: []ExtensionSetting{{
			Key: "accent", Label: "Accent", Type: "color", Default: "#111111",
		}},
		Routes: []ExtensionRoute{{
			Name: "product", Pattern: "/products/:handle", Template: "product",
			DataSources: []string{"product"},
		}},
		DataSources: map[string]ExtensionCall{
			"product": {
				Tool: "products_get",
				Args: map[string]any{"handle": "{{ route.handle }}", "status": "active"},
			},
		},
		Actions: map[string]ExtensionAction{
			"cart": {
				AllowedInput: []string{"variant_id"},
				Steps: []ExtensionCall{{
					Tool: "cart_add",
					Args: map[string]any{
						"variant_id": "{{ input.variant_id }}",
						"session":    "{{ session.token }}",
					},
				}},
			},
		},
		Templates: map[string]string{
			"product": `<!doctype html><title>{{index (index (index .Data "product") "product") "title"}}</title>`,
		},
		Assets: map[string]string{"store.css": "body{color:#111}"},
	}
}

func TestExtensionManifestValidationRejectsReservedRoutes(t *testing.T) {
	manifest := testExtensionManifest()
	manifest.Routes[0].Pattern = "/_actions/:name"
	if err := validateExtensionManifest("store", "provider", manifest); err == nil {
		t.Fatal("reserved route accepted")
	}
}

func TestExtensionRegistrationRejectsRouteCollisions(t *testing.T) {
	db := hardeningTestDB(t)
	site, _ := dbCreateSite(db, "p1", "main", "Main", "")
	first := testExtensionManifest()
	if _, err := dbExtensionUpsert(db, "p1", site.ID, "store", "commerce", first, true); err != nil {
		t.Fatal(err)
	}
	second := testExtensionManifest()
	second.Routes[0].Pattern = "/products/:slug"
	if _, err := dbExtensionUpsert(db, "p1", site.ID, "reviews", "reviews", second, true); err == nil {
		t.Fatal("equivalent dynamic route was registered by two extensions")
	}
}

func TestPublishedExtensionRendersConfiguredProviderData(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "p1")
	db := hardeningTestDB(t)
	site, err := dbCreateSite(db, "p1", "main", "Main", "")
	if err != nil {
		t.Fatal(err)
	}
	ext, err := dbExtensionUpsert(db, "p1", site.ID, "store", "commerce", testExtensionManifest(), true)
	if err != nil {
		t.Fatal(err)
	}
	if ext.Status != "published" {
		t.Fatalf("extension status = %q", ext.Status)
	}
	platform := &extensionPlatformStub{}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, nil, platform, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/products/desk-lamp", nil)
	if handled := (&App{}).tryHandleExtensionRoute(rec, req, ctx, "p1", site.ID); !handled {
		t.Fatal("extension route not handled")
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>Desk Lamp</title>") {
		t.Fatalf("render = %d %s", rec.Code, rec.Body.String())
	}
	if len(platform.calls) != 1 || platform.calls[0].app != "commerce" ||
		platform.calls[0].tool != "products_get" ||
		platform.calls[0].input["handle"] != "desk-lamp" ||
		platform.calls[0].input["status"] != "active" {
		t.Fatalf("provider calls = %#v", platform.calls)
	}
}

func TestExtensionActionRejectsUnregisteredInput(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "p1")
	db := hardeningTestDB(t)
	site, _ := dbCreateSite(db, "p1", "main", "Main", "")
	_, _ = dbExtensionUpsert(db, "p1", site.ID, "store", "commerce", testExtensionManifest(), true)
	platform := &extensionPlatformStub{}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, nil, platform, nil)
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/_actions/store/cart", strings.NewReader(`{"variant_id":7,"store_id":999}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "storefront")
	(&App{}).handleExtensionAction(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "store_id") {
		t.Fatalf("action response = %d %s", rec.Code, rec.Body.String())
	}
	if len(platform.calls) != 0 {
		t.Fatalf("provider was called: %#v", platform.calls)
	}
}

func TestExtensionActionInjectsSessionAndProject(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "p1")
	db := hardeningTestDB(t)
	site, _ := dbCreateSite(db, "p1", "main", "Main", "")
	_, _ = dbExtensionUpsert(db, "p1", site.ID, "store", "commerce", testExtensionManifest(), true)
	platform := &extensionPlatformStub{}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, nil, platform, nil)
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/_actions/store/cart", strings.NewReader(`{"variant_id":7}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "storefront")
	(&App{}).handleExtensionAction(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("action response = %d %s", rec.Code, rec.Body.String())
	}
	call := platform.calls[0]
	if call.input["variant_id"] == nil || call.input["_project_id"] != "p1" ||
		len(call.input["session"].(string)) < 24 {
		t.Fatalf("provider input = %#v", call.input)
	}
}

func TestExtensionProviderRefreshPreservesSiteSettings(t *testing.T) {
	db := hardeningTestDB(t)
	site, _ := dbCreateSite(db, "p1", "main", "Main", "")
	_, err := dbExtensionUpsert(db, "p1", site.ID, "store", "commerce", testExtensionManifest(), true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dbExtensionUpdateSettings(db, "p1", site.ID, "store", map[string]any{"accent": "#abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	refreshed := testExtensionManifest()
	refreshed.Version = "2"
	refreshed.Settings["accent"] = "#222222"
	ext, err := dbExtensionUpsert(db, "p1", site.ID, "store", "commerce", refreshed, true)
	if err != nil {
		t.Fatal(err)
	}
	if ext.PublishedManifest.Settings["accent"] != "#abcdef" {
		t.Fatalf("provider refresh reset site settings: %#v", ext.PublishedManifest.Settings)
	}
}

func TestExtensionSessionRejectsTamperedCookie(t *testing.T) {
	token := "abcdefghijklmnopqrstuvwxyz123456"
	signed := signExtensionSession(token)
	if got, ok := verifyExtensionSession(signed); !ok || got != token {
		t.Fatalf("valid session rejected: %q %v", got, ok)
	}
	if _, ok := verifyExtensionSession(signed + "x"); ok {
		t.Fatal("tampered session accepted")
	}
}

func TestExtensionSessionRotationIsScopedAndSigned(t *testing.T) {
	name := extensionSessionCookieName("p1", 9, "store")
	original := "abcdefghijklmnopqrstuvwxyz123456"
	req := httptest.NewRequest(http.MethodPost, "/_actions/store/checkout", nil)
	req.AddCookie(&http.Cookie{Name: name, Value: signExtensionSession(original)})
	rec := httptest.NewRecorder()
	if got := extensionSession(rec, req, name, false); got != original {
		t.Fatalf("existing session changed: %q", got)
	}
	rotated := extensionSession(rec, req, name, true)
	if rotated == original {
		t.Fatal("session was not rotated")
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != name {
		t.Fatalf("rotation cookie = %#v", cookies)
	}
	if got, ok := verifyExtensionSession(cookies[0].Value); !ok || got != rotated {
		t.Fatal("rotated cookie is not signed correctly")
	}
}

func TestExtensionActionRequiresSameOriginSignal(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/_actions/store/cart", nil)
	if validActionOrigin(req) {
		t.Fatal("action without an origin or storefront request header was accepted")
	}
	req.Header.Set("Origin", "https://attacker.example")
	req.Host = "shop.example"
	if validActionOrigin(req) {
		t.Fatal("cross-origin action was accepted")
	}
}
