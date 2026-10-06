package main

import (
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestConfigureMarketingChannelUsesAdsAndExposesOnlyPublicConfig(t *testing.T) {
	platform := newCommercePlatformStub()
	platform.responses["ads:account_list"] = map[string]any{"accounts": []any{
		map[string]any{"id": int64(12), "platform": "meta", "display_name": "Feliqo Meta", "status": "active"},
	}}
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
		Provider: metaMarketingProvider,
		Status:   "active",
		PublicConfig: map[string]any{
			"script_origins":  []any{"https://connect.facebook.net"},
			"connect_origins": []any{"https://connect.facebook.net", "https://www.facebook.com"},
			"image_origins":   []any{"https://www.facebook.com"},
		},
	}
	manifest := commerceStorefrontManifest(&Store{ID: 7, Name: "Feliqo"}, []*MarketingChannel{channel})
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

// newMixedAdsStub serves one Meta and one Google ad account, each with one
// active tracking source, mirroring what Ads 0.1.47 returns.
func newMixedAdsStub() *commercePlatformStub {
	platform := newCommercePlatformStub()
	platform.responses["ads:account_list"] = map[string]any{"accounts": []any{
		map[string]any{"id": int64(3), "platform": "meta", "display_name": "Meta Account", "status": "active"},
		map[string]any{"id": int64(6), "platform": "google", "display_name": "Google Account", "status": "active"},
		map[string]any{"id": int64(9), "platform": "tiktok", "display_name": "TikTok Account", "status": "active"},
	}}
	platform.responses["ads:resource_list"] = platformResponseFunc(func(input map[string]any) (any, error) {
		switch intArg(input, "ad_account_id") {
		case 3:
			return map[string]any{"data": []any{
				map[string]any{"id": int64(31), "name": "Store Pixel", "provider_type": "meta_pixel", "status": "active"},
			}}, nil
		case 6:
			return map[string]any{"data": []any{
				map[string]any{"id": int64(62), "name": "Purchase", "provider_type": "google_conversion_action", "status": "active"},
			}}, nil
		}
		return map[string]any{"data": []any{}}, nil
	})
	return platform
}

// The reported blocker: _options saw the Ads app but offered Meta accounts only,
// so a Google conversion action could never be selected for a storefront.
func TestMarketingChannelOptionsOffersGoogleAccounts(t *testing.T) {
	platform := newMixedAdsStub()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("marketing-project"), tk.WithPlatform(platform))
	store, err := dbStoreCreate(ctx.AppDB(), "marketing-project", map[string]any{"slug": "feliqo", "name": "Feliqo"})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{}
	resultAny, err := app.toolMarketingChannelOptions(ctx, map[string]any{"store_id": store.ID})
	if err != nil {
		t.Fatal(err)
	}
	result := resultAny.(map[string]any)
	if result["ads_available"] != true {
		t.Fatalf("expected ads_available: %#v", result)
	}
	byProvider := map[string]map[string]any{}
	for _, value := range result["accounts"].([]any) {
		account := value.(map[string]any)
		byProvider[strArg(account, "provider")] = account
	}
	if len(byProvider) != 2 {
		t.Fatalf("expected meta and google accounts, got %d: %#v", len(byProvider), result["accounts"])
	}
	google, ok := byProvider["google"]
	if !ok {
		t.Fatalf("google account not offered: %#v", result["accounts"])
	}
	if intArg(google, "id") != 6 {
		t.Fatalf("wrong google account: %#v", google)
	}
	resources := google["resources"].([]any)
	if len(resources) != 1 || strArg(resources[0].(map[string]any), "provider_type") != "google_conversion_action" {
		t.Fatalf("google conversion action not offered: %#v", resources)
	}
	if _, offered := byProvider["tiktok"]; offered {
		t.Fatalf("unsupported platform should not be offered: %#v", result["accounts"])
	}
}

// Configuring a Google conversion action must persist the gtag identifiers that
// the storefront needs, not just the Meta-shaped public_id/script_url pair.
func TestConfigureMarketingChannelAcceptsGoogleConversionAction(t *testing.T) {
	platform := newMixedAdsStub()
	platform.responses["ads:resource_set_default"] = map[string]any{"ok": true}
	platform.responses["ads:tracking_source_installation_get"] = map[string]any{
		"resource": map[string]any{"id": 62, "name": "Purchase", "provider_type": "google_conversion_action"},
		"installation": map[string]any{
			"provider": "google", "public_id": "876543210",
			"conversion_id": "AW-123456789", "conversion_label": "AbC-D_efGhIjKl",
			"send_to":           "AW-123456789/AbC-D_efGhIjKl",
			"script_url":        "https://www.googletagmanager.com/gtag/js?id=AW-123456789",
			"event_snippet":     "<script>gtag('event','conversion',{'send_to':'AW-123456789/AbC-D_efGhIjKl'});</script>",
			"script_origins":    []any{"https://www.googletagmanager.com"},
			"connect_origins":   []any{"https://www.googletagmanager.com"},
			"image_origins":     []any{"https://www.google.com"},
			"snippet_available": true,
		},
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("marketing-project"), tk.WithPlatform(platform))
	store, err := dbStoreCreate(ctx.AppDB(), "marketing-project", map[string]any{"slug": "feliqo", "name": "Feliqo"})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{}
	resultAny, err := app.toolMarketingChannelConfigure(ctx, map[string]any{
		"store_id": store.ID, "ad_account_id": int64(6), "tracking_source_resource_id": int64(62),
	})
	if err != nil {
		t.Fatal(err)
	}
	channel := resultAny.(map[string]any)["channel"].(*MarketingChannel)
	if channel.Provider != "google" {
		t.Fatalf("expected google provider, got %q", channel.Provider)
	}
	if strArg(channel.PublicConfig, "conversion_id") != "AW-123456789" ||
		strArg(channel.PublicConfig, "conversion_label") != "AbC-D_efGhIjKl" {
		t.Fatalf("gtag identifiers not persisted: %#v", channel.PublicConfig)
	}

	// The store can hold Meta and Google side by side.
	getAny, err := app.toolMarketingChannelGet(ctx, map[string]any{"store_id": store.ID, "provider": "google"})
	if err != nil {
		t.Fatal(err)
	}
	if got := getAny.(map[string]any)["channel"].(*MarketingChannel); got.Provider != "google" {
		t.Fatalf("provider-addressed get returned %q", got.Provider)
	}
	if channels := getAny.(map[string]any)["channels"].([]*MarketingChannel); len(channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(channels))
	}
}

// Google can return a conversion action before its tag snippet exists. Storing
// that as active would publish a channel that silently emits nothing.
func TestConfigureMarketingChannelRejectsGoogleActionWithoutSnippet(t *testing.T) {
	platform := newMixedAdsStub()
	platform.responses["ads:resource_set_default"] = map[string]any{"ok": true}
	platform.responses["ads:tracking_source_installation_get"] = map[string]any{
		"resource": map[string]any{"id": 62, "name": "Purchase", "provider_type": "google_conversion_action"},
		"installation": map[string]any{
			"provider": "google", "public_id": "876543210", "snippet_available": false,
		},
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("marketing-project"), tk.WithPlatform(platform))
	store, err := dbStoreCreate(ctx.AppDB(), "marketing-project", map[string]any{"slug": "feliqo", "name": "Feliqo"})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{}
	_, err = app.toolMarketingChannelConfigure(ctx, map[string]any{
		"store_id": store.ID, "ad_account_id": int64(6), "tracking_source_resource_id": int64(62),
	})
	if err == nil || !strings.Contains(err.Error(), "no tag snippet yet") {
		t.Fatalf("expected pending-snippet rejection, got %v", err)
	}
}

// A provider hint that contradicts the account must not be trusted.
func TestConfigureMarketingChannelRejectsMismatchedProviderHint(t *testing.T) {
	platform := newMixedAdsStub()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("marketing-project"), tk.WithPlatform(platform))
	store, err := dbStoreCreate(ctx.AppDB(), "marketing-project", map[string]any{"slug": "feliqo", "name": "Feliqo"})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{}
	_, err = app.toolMarketingChannelConfigure(ctx, map[string]any{
		"store_id": store.ID, "ad_account_id": int64(6), "provider": "meta",
		"tracking_source_resource_id": int64(62),
	})
	if err == nil || !strings.Contains(err.Error(), "is a google account, not meta") {
		t.Fatalf("expected provider mismatch rejection, got %v", err)
	}
}

// CSP scoped to one channel would block the other's tag, so the manifest must
// union origins across every active channel.
func TestStorefrontManifestUnionsOriginsAcrossChannels(t *testing.T) {
	channels := []*MarketingChannel{
		{Provider: metaMarketingProvider, Status: "active", PublicConfig: map[string]any{
			"script_origins":  []any{"https://connect.facebook.net"},
			"connect_origins": []any{"https://www.facebook.com"},
			"image_origins":   []any{"https://www.facebook.com"},
		}},
		{Provider: googleMarketingProvider, Status: "active", PublicConfig: map[string]any{
			"script_origins":  []any{"https://www.googletagmanager.com"},
			"connect_origins": []any{"https://googleads.g.doubleclick.net"},
			"image_origins":   []any{"https://www.google.com"},
		}},
		{Provider: "disabled-one", Status: "disabled", PublicConfig: map[string]any{
			"script_origins": []any{"https://should-not-appear.example"},
		}},
	}
	policy := commerceStorefrontManifest(&Store{ID: 7, Name: "Feliqo"}, channels)["browser_policy"].(map[string]any)
	for _, want := range []string{"https://connect.facebook.net", "https://www.googletagmanager.com"} {
		if !containsAnyString(policy["script_origins"].([]any), want) {
			t.Fatalf("script origin %q missing: %#v", want, policy["script_origins"])
		}
	}
	if !containsAnyString(policy["connect_origins"].([]any), "https://googleads.g.doubleclick.net") {
		t.Fatalf("google connect origin missing: %#v", policy["connect_origins"])
	}
	if !containsAnyString(policy["image_origins"].([]any), "https://www.google.com") {
		t.Fatalf("google image origin missing: %#v", policy["image_origins"])
	}
	if containsAnyString(policy["script_origins"].([]any), "https://should-not-appear.example") {
		t.Fatalf("disabled channel leaked into CSP: %#v", policy["script_origins"])
	}
}

// Storefronts published before 0.9.0 embed a store.js that reads public_id and
// script_url off the top level of the response.
func TestMarketingChannelPublicGetKeepsPre090Shape(t *testing.T) {
	platform := newMixedAdsStub()
	platform.responses["ads:resource_set_default"] = map[string]any{"ok": true}
	platform.responses["ads:tracking_source_installation_get"] = map[string]any{
		"resource": map[string]any{"id": 31, "name": "Store Pixel", "provider_type": "meta_pixel"},
		"installation": map[string]any{
			"provider": "meta", "public_id": "987654321",
			"script_url": "https://connect.facebook.net/en_US/fbevents.js",
		},
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("marketing-project"), tk.WithPlatform(platform))
	store, err := dbStoreCreate(ctx.AppDB(), "marketing-project", map[string]any{"slug": "feliqo", "name": "Feliqo"})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{}
	if _, err := app.toolMarketingChannelConfigure(ctx, map[string]any{
		"store_id": store.ID, "ad_account_id": int64(3), "tracking_source_resource_id": int64(31),
	}); err != nil {
		t.Fatal(err)
	}
	publicAny, err := app.toolMarketingChannelPublicGet(ctx, map[string]any{"store_id": store.ID})
	if err != nil {
		t.Fatal(err)
	}
	public := publicAny.(map[string]any)
	if public["enabled"] != true || public["public_id"] != "987654321" || public["provider"] != "meta" {
		t.Fatalf("pre-0.9.0 flat shape not preserved: %#v", public)
	}
	if len(public["channels"].([]any)) != 1 {
		t.Fatalf("expected one uniform channel view: %#v", public["channels"])
	}
	// public_config now holds the whole Ads installation payload, so the view
	// must allow-list rather than pass through.
	for _, leaked := range []string{"ad_account_id", "tracking_source_resource_id", "requires_site_installation"} {
		if _, bad := public[leaked]; bad {
			t.Fatalf("public config leaked %q: %#v", leaked, public)
		}
	}
}

// The shipped storefront asset must drive every provider through one canonical
// event vocabulary rather than branching on provider at each call site.
func TestStorefrontAssetUsesUniformTrackingLayer(t *testing.T) {
	manifest := commerceStorefrontManifest(&Store{ID: 7, Name: "Feliqo"}, nil)
	js := manifest["assets"].(map[string]any)["store.js"].(string)

	// canonical events, emitted by the storefront regardless of provider
	for _, event := range []string{"page_view", "view_item", "search", "add_to_cart", "begin_checkout", "purchase"} {
		if !strings.Contains(js, "'"+event+"'") {
			t.Fatalf("canonical event %q missing from storefront asset", event)
		}
	}
	// both adapters, and the Google-specific conversion hop
	for _, fragment := range []string{"marketingAdapters", "marketingTrack(", "meta:{", "google:{", "window.gtag", "window.fbq", "send_to:channel.send_to"} {
		if !strings.Contains(js, fragment) {
			t.Fatalf("uniform tracking layer missing %q", fragment)
		}
	}
	// the old Meta-only surface must be gone
	for _, gone := range []string{"metaTrack(", "metaConfig", "data-meta-product", "data-meta-add-to-cart"} {
		if strings.Contains(js, gone) {
			t.Fatalf("Meta-only symbol %q still present in storefront asset", gone)
		}
	}
	// consent still gates loading, and the banner is no longer Meta-conditioned
	if !strings.Contains(js, "marketingConsent()!=='granted'") {
		t.Fatal("consent gate missing from storefront asset")
	}
	if !strings.Contains(js, "panel&&marketingChannels.length") {
		t.Fatal("consent banner is not gated on configured channels")
	}
	// pre-0.9.0 responses (flat public_id, no channels[]) must still work
	if !strings.Contains(js, "config.public_id?[{provider:config.provider||'meta'") {
		t.Fatal("storefront asset dropped the pre-0.9.0 flat-config fallback")
	}
}
