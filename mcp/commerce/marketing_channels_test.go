package main

import (
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestConfigureMarketingChannelUsesAdsAndExposesOnlyPublicConfig(t *testing.T) {
	platform := newCommercePlatformStub()
	platform.responses["ads:resource_set_default"] = map[string]any{"ok": true}
	platform.responses["ads:tracking_source_installation_get"] = map[string]any{
		"resource": map[string]any{"id": 31, "name": "Feliqo Pixel", "provider_type": "meta_pixel"},
		"installation": map[string]any{
			"provider": "meta", "public_id": "987654321", "script_url": "https://connect.facebook.net/en_US/fbevents.js",
			"script_origins":  []any{"https://connect.facebook.net"},
			"connect_origins": []any{"https://connect.facebook.net", "https://www.facebook.com"},
			"image_origins":   []any{"https://www.facebook.com"},
		},
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("marketing-project"), tk.WithPlatform(platform))
	store, err := dbStoreCreate(ctx.AppDB(), "marketing-project", map[string]any{"slug": "feliqo", "name": "Feliqo"})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{}
	resultAny, err := app.toolMarketingChannelConfigure(ctx, map[string]any{
		"store_id": store.ID, "ad_account_id": int64(12), "tracking_source_resource_id": int64(31),
	})
	if err != nil {
		t.Fatal(err)
	}
	channel := resultAny.(map[string]any)["channel"].(*MarketingChannel)
	if channel.Status != "active" || channel.TrackingSourceName != "Feliqo Pixel" || strArg(channel.PublicConfig, "public_id") != "987654321" {
		t.Fatalf("unexpected channel: %#v", channel)
	}
	publicAny, err := app.toolMarketingChannelPublicGet(ctx, map[string]any{"store_id": store.ID})
	if err != nil {
		t.Fatal(err)
	}
	public := publicAny.(map[string]any)
	if public["enabled"] != true || public["public_id"] != "987654321" {
		t.Fatalf("unexpected public config: %#v", public)
	}
	if _, leaked := public["ad_account_id"]; leaked {
		t.Fatalf("public config leaked internal account details: %#v", public)
	}
	if findPlatformCall(platform.calls, "ads", "tracking_source_installation_get").Tool == "" {
		t.Fatalf("Commerce did not resolve installation config through Ads: %#v", platform.calls)
	}

	if _, err := app.toolMarketingChannelDisconnect(ctx, map[string]any{"store_id": store.ID}); err != nil {
		t.Fatal(err)
	}
	publicAny, _ = app.toolMarketingChannelPublicGet(ctx, map[string]any{"store_id": store.ID})
	if publicAny.(map[string]any)["enabled"] != false {
		t.Fatalf("disabled channel remains public: %#v", publicAny)
	}
}

func TestStorefrontManifestAddsMetaPolicyAndConsentGatedEvents(t *testing.T) {
	channel := &MarketingChannel{
		Status: "active",
		PublicConfig: map[string]any{
			"script_origins":  []any{"https://connect.facebook.net"},
			"connect_origins": []any{"https://connect.facebook.net", "https://www.facebook.com"},
			"image_origins":   []any{"https://www.facebook.com"},
		},
	}
	manifest := commerceStorefrontManifest(&Store{ID: 7, Name: "Feliqo"}, channel)
	policy := manifest["browser_policy"].(map[string]any)
	if !containsAnyString(policy["script_origins"].([]any), "https://connect.facebook.net") || !containsAnyString(policy["connect_origins"].([]any), "https://www.facebook.com") {
		t.Fatalf("Meta CSP origins missing: %#v", policy)
	}
	if !containsAnyString(policy["image_origins"].([]any), "https://www.facebook.com") {
		t.Fatalf("Meta image origin missing: %#v", policy)
	}
	actions := manifest["actions"].(map[string]any)
	if _, ok := actions["marketing_config"]; !ok {
		t.Fatal("browser-safe marketing config action missing")
	}
	js := manifest["assets"].(map[string]any)["store.js"].(string)
	for _, expected := range []string{"marketingConsent()!=='granted'", "PageView", "ViewContent", "AddToCart", "InitiateCheckout"} {
		if !strings.Contains(js, expected) {
			t.Fatalf("storefront tracking asset missing %q", expected)
		}
	}
}

func containsAnyString(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
