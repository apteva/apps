package main

import (
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"testing"
	"time"
)

func mobileFixture(t *testing.T, ctx *sdk.AppCtx, a *App, accountID int64, platform, os string) (int64, int64) {
	t.Helper()
	native := "com.example.app"
	store := "https://play.google.com/store/apps/details?id=com.example.app"
	if os == "ios" {
		native = "123456789"
		store = "https://apps.apple.com/us/app/example/id123456789"
	}
	if platform == "meta" {
		native = "98765"
	}
	result, err := a.toolMobileAppBind(ctx, map[string]any{"ad_account_id": accountID, "name": "Example", "os": os, "store_url": store, "app_id": native})
	if err != nil || mcpResultError(result) != nil {
		t.Fatalf("app bind: %v %#v", err, result)
	}
	appID := int64(intArg(asMap(asMap(result)["resource"]), "id", 0))
	source := "mmp"
	if platform == "google" {
		source = "firebase"
	}
	if platform == "meta" {
		source = "meta_sdk"
	}
	result, err = a.toolMeasurementSourceBind(ctx, map[string]any{"ad_account_id": accountID, "mobile_app_resource_id": appID, "source": source})
	if err != nil || mcpResultError(result) != nil {
		t.Fatalf("source bind: %v %#v", err, result)
	}
	sourceID := int64(intArg(asMap(asMap(result)["resource"]), "id", 0))
	return appID, sourceID
}
func mobileArgs(accountID, appID, sourceID int64) map[string]any {
	return map[string]any{"ad_account_id": accountID, "mobile_app_resource_id": appID, "measurement_source_resource_id": sourceID, "name": "App campaign", "objective": "app_promotion", "app_goal": "installs", "daily_budget_cents": 1000}
}
func requireOK(t *testing.T, result any, err error) {
	t.Helper()
	if err != nil || mcpResultError(result) != nil {
		t.Fatalf("unexpected error %v %#v", err, result)
	}
}

func TestMobileStoreOwnershipAndCapabilities(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	a := &App{}
	account := seedResourceTestAccount(t, ctx, "google", "123")
	for _, args := range []map[string]any{
		{"os": "android", "store_url": "https://play.google.com.evil.test/store/apps/details?id=com.example.app", "app_id": "com.example.app"},
		{"os": "ios", "store_url": "https://play.google.com/store/apps/details?id=com.example.app", "app_id": "123"},
		{"os": "android", "store_url": "https://play.google.com/store/apps/details?id=com.example.app", "app_id": "com.other.app"},
	} {
		args["ad_account_id"] = account
		args["name"] = "Test"
		result, err := a.toolMobileAppBind(ctx, args)
		if err != nil || mcpResultError(result) == nil {
			t.Fatalf("unsafe store binding accepted %#v %v", result, err)
		}
	}
	appID, sourceID := mobileFixture(t, ctx, a, account, "google", "android")
	other := seedResourceTestAccount(t, ctx, "google", "456")
	args := mobileArgs(other, appID, sourceID)
	result, err := a.toolCampaignCreate(ctx, args)
	if err != nil || mcpResultError(result) == nil {
		t.Fatalf("cross-account app accepted %#v", result)
	}
	if len(pf.executeCalls) > 0 {
		t.Fatal("invalid binding reached provider")
	}
	if mobileCapabilities("reddit")["requires_pixel"] != true {
		t.Fatal("Reddit still requires a pixel on mobile ad groups")
	}
	for _, platform := range []string{"meta", "google", "x", "reddit"} {
		if mobileCapabilities(platform)["creates_sdk"] != false {
			t.Fatal("SDK installation cannot be asserted by binding")
		}
	}
}
func TestGoogleAppCampaignAtomicAndroidAndIOS(t *testing.T) {
	for _, os := range []string{"android", "ios"} {
		t.Run(os, func(t *testing.T) {
			pf := newRecordingPlatform()
			pf.executeResponses["bulk_mutate"] = executeJSON(`{"mutateOperationResponses":[{"campaignBudgetResult":{"resourceName":"customers/123/campaignBudgets/42"}},{"campaignResult":{"resourceName":"customers/123/campaigns/43"}}]}`)
			ctx := newAdsCtx(t, pf)
			a := &App{}
			account := seedResourceTestAccount(t, ctx, "google", "123")
			appID, sourceID := mobileFixture(t, ctx, a, account, "google", os)
			args := mobileArgs(account, appID, sourceID)
			args["locations"] = []any{"2840"}
			args["idempotency_key"] = "atomic"
			result, err := a.toolCampaignCreate(ctx, args)
			requireOK(t, result, err)
			call := findExecuteCall(t, pf, "bulk_mutate")
			if call.Input["partialFailure"] != false {
				t.Fatal("mobile create must be atomic")
			}
			ops := call.Input["mutateOperations"].([]any)
			if len(ops) != 3 {
				t.Fatalf("budget campaign and targeting must be atomic %#v", ops)
			}
			budget := asMap(asMap(asMap(ops[0])["campaignBudgetOperation"])["create"])
			if budget["explicitlyShared"] != false {
				t.Fatal("App budget must be unshared")
			}
			campaign := asMap(asMap(asMap(ops[1])["campaignOperation"])["create"])
			if campaign["advertisingChannelType"] != "MULTI_CHANNEL" || campaign["advertisingChannelSubType"] != "APP_CAMPAIGN" || campaign["status"] != "PAUSED" {
				t.Fatalf("invalid app campaign %#v", campaign)
			}
			setting := asMap(campaign["appCampaignSetting"])
			want := "GOOGLE_APP_STORE"
			if os == "ios" {
				want = "APPLE_APP_STORE"
			}
			if setting["appStore"] != want {
				t.Fatal("wrong app store")
			}
			result, err = a.toolCampaignCreate(ctx, args)
			requireOK(t, result, err)
			if len(pf.executeCalls) != 1 {
				t.Fatal("retry created another campaign")
			}
			args["daily_budget_cents"] = 2000
			result, err = a.toolCampaignCreate(ctx, args)
			if err != nil || mcpResultError(result) == nil {
				t.Fatal("changed keyed request accepted")
			}
			var bound int64
			if err := ctx.AppDB().QueryRow(`SELECT mobile_app_resource_id FROM ad_mobile_campaigns WHERE campaign_id='43'`).Scan(&bound); err != nil || bound != appID {
				t.Fatal("campaign binding was not persisted")
			}
		})
	}
}
func TestGoogleAppCampaignPreflightAndUnknownOutcome(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	a := &App{}
	account := seedResourceTestAccount(t, ctx, "google", "123")
	appID, sourceID := mobileFixture(t, ctx, a, account, "google", "android")
	for _, extra := range []map[string]any{{"shared_budget": true}, {"app_goal": "value"}, {"bid_strategy": "manual_cpc"}, {"locations": []any{"bad"}}, {"platform_options": map[string]any{"campaign": map[string]any{"advertisingChannelType": "SEARCH"}}}, {"status": "ACTIVE"}} {
		args := mobileArgs(account, appID, sourceID)
		for k, v := range extra {
			args[k] = v
		}
		result, err := a.toolCampaignCreate(ctx, args)
		if err != nil || mcpResultError(result) == nil {
			t.Fatalf("invalid campaign accepted %#v", result)
		}
	}
	if len(pf.executeCalls) != 0 {
		t.Fatal("invalid campaign changed provider")
	}
	args := mobileArgs(account, appID, sourceID)
	args["idempotency_key"] = "lost-response"
	result, err := a.toolCampaignCreate(ctx, args)
	if err != nil || mcpResultError(result) == nil {
		t.Fatal("missing provider id accepted")
	}
	result, err = a.toolCampaignCreate(ctx, args)
	if err != nil || asMap(result)["code"] != "creation_outcome_unknown" {
		t.Fatalf("unknown creation must remain reserved %#v", result)
	}
	if len(pf.executeCalls) != 1 {
		t.Fatal("ambiguous create retried")
	}
}
func TestGoogleMobileActionsAreDiscoveredEnabledAndNotWebsiteTags(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	a := &App{}
	account := seedResourceTestAccount(t, ctx, "google", "123")
	appID, _ := mobileFixture(t, ctx, a, account, "google", "android")
	status := "HIDDEN"
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "conversion_action_mutate" {
			status = "ENABLED"
			return executeJSON(`{"results":[{"resourceName":"customers/123/conversionActions/7"}]}`), nil
		}
		return executeJSON(`{"results":[{"conversionAction":{"id":"7","appId":"com.example.app","type":"FIREBASE_ANDROID_CUSTOM","category":"PURCHASE","ownerCustomer":"customers/123","status":"` + status + `","name":"App purchase"}}]}`), nil
	}
	result, err := a.toolConversionEventList(ctx, map[string]any{"ad_account_id": account, "mobile_app_resource_id": appID})
	requireOK(t, result, err)
	items := asMap(result)["data"].([]map[string]any)
	eventID := int64(intArg(items[0], "id", 0))
	if items[0]["status"] != "hidden" {
		t.Fatalf("hidden event not preserved %#v", items)
	}
	result, err = a.toolConversionEventEnable(ctx, map[string]any{"ad_account_id": account, "mobile_app_resource_id": appID, "conversion_event_resource_id": eventID})
	requireOK(t, result, err)
	if asMap(result)["status"] != "active" {
		t.Fatal("event not refreshed after enable")
	}
	call := findExecuteCall(t, pf, "conversion_action_mutate")
	operation := asMap(call.Input["operations"].([]any)[0])
	if operation["create"] != nil || operation["updateMask"] != "status" {
		t.Fatal("enable must not create a WEBPAGE action")
	}
	before := len(pf.executeCalls)
	result, err = a.googleConversionInstallation(ctx, &adAccount{ID: account, Platform: "google", NativeAccountID: "123", ProjectID: "test-proj"}, map[string]any{"tracking_source_resource_id": eventID})
	if err != nil || mcpResultError(result) == nil || len(pf.executeCalls) != before {
		t.Fatal("app event offered a website gtag")
	}
}
func TestMetaAppWiringUsesAppInsteadOfPixelAndInheritsBinding(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["campaign_list"] = executeJSON(`{"data":[{"id":"123","objective":"OUTCOME_APP_PROMOTION"}]}`)
	ctx := newAdsCtx(t, pf)
	a := &App{}
	account := seedResourceTestAccount(t, ctx, "meta", "act_123")
	appID, sourceID := mobileFixture(t, ctx, a, account, "meta", "ios")
	result, err := a.toolCampaignCreate(ctx, mobileArgs(account, appID, sourceID))
	requireOK(t, result, err)
	result, err = a.toolAdSetCreate(ctx, map[string]any{"ad_account_id": account, "campaign_id": "123", "name": "Installs", "targeting": map[string]any{"geo_locations": map[string]any{"countries": []any{"US"}}}})
	requireOK(t, result, err)
	input := findExecuteCall(t, pf, "adset_create").Input
	promoted := asMap(input["promoted_object"])
	if promoted["application_id"] != "98765" || promoted["object_store_url"] != "https://apps.apple.com/us/app/example/id123456789" || promoted["pixel_id"] != nil || input["destination_type"] != "APP" || input["optimization_goal"] != "APP_INSTALLS" {
		t.Fatalf("invalid app promoted object %#v", input)
	}
}
func TestRedditMobileRequiresPixelAndRecentEvent(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	a := &App{}
	account := seedResourceTestAccount(t, ctx, "reddit", "acct")
	appID, sourceID := mobileFixture(t, ctx, a, account, "reddit", "android")
	pf.executeResponses["list_apps"] = executeJSON(`{"data":[{"id":"com.example.app"}]}`)
	pf.executeResponses["app_last_fired_at"] = executeJSON(`{"data":{"mobile_conversion_install":"` + time.Now().UTC().Format(time.RFC3339) + `"}}`)
	result, err := a.toolConversionEventList(ctx, map[string]any{"ad_account_id": account, "mobile_app_resource_id": appID})
	requireOK(t, result, err)
	var eventID int64
	for _, v := range asMap(result)["data"].([]map[string]any) {
		if firstString(asMap(v["metadata"]), "event") == "install" {
			eventID = int64(intArg(v, "id", 0))
		}
	}
	result, err = a.toolConversionReadiness(ctx, map[string]any{"ad_account_id": account, "mobile_app_resource_id": appID, "measurement_source_resource_id": sourceID, "conversion_event_resource_id": eventID})
	requireOK(t, result, err)
	if asMap(result)["ready"] != true || asMap(result)["event_observed"] != "observed" {
		t.Fatalf("provider observation not recognized %#v", result)
	}
	acct := &adAccount{ID: account, ProjectID: "test-proj", Platform: "reddit", NativeAccountID: "acct"}
	_, err = a.upsertResource(ctx, acct, discoveredResource{Kind: resourceTrackingSource, ProviderType: "reddit_pixel", NativeID: "pixel", Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	args := mobileArgs(account, appID, sourceID)
	args["campaign_id"] = "campaign"
	args["conversion_event_resource_id"] = eventID
	args["targeting"] = map[string]any{}
	result, err = a.toolAdSetCreate(ctx, args)
	requireOK(t, result, err)
	data := asMap(findExecuteCall(t, pf, "create_ad_group").Input["data"])
	if data["app_id"] != "com.example.app" || data["conversion_pixel_id"] != "pixel" || data["optimization_goal"] != "MOBILE_CONVERSION_INSTALL" || data["bid_strategy"] != "BIDLESS" {
		t.Fatalf("invalid Reddit mobile payload %#v", data)
	}
	pf.executeResponses["app_last_fired_at"] = executeJSON(`{"data":{"mobile_conversion_install":"2020-01-01T00:00:00Z"}}`)
	result, err = a.toolConversionReadiness(ctx, map[string]any{"ad_account_id": account, "mobile_app_resource_id": appID, "conversion_event_resource_id": eventID})
	requireOK(t, result, err)
	if asMap(result)["ready"] != false || asMap(result)["eligible_for_optimization"] != false {
		t.Fatal("stale measurement cannot be ready")
	}
}
func TestXMobileCardAndUnverifiedMeasurement(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["create_card"] = executeJSON(`{"data":{"card_uri":"card://123"}}`)
	ctx := newAdsCtx(t, pf)
	a := &App{}
	account := seedResourceTestAccount(t, ctx, "x", "acct")
	appID, sourceID := mobileFixture(t, ctx, a, account, "x", "android")
	args := mobileArgs(account, appID, sourceID)
	args["headline"] = "Install Example"
	args["app_country_code"] = "US"
	args["format"] = "carousel"
	args["media_keys"] = []any{"3_12345", "3_12346"}
	result, err := a.toolCreativeCreate(ctx, args)
	requireOK(t, result, err)
	card := findExecuteCall(t, pf, "create_card").Input
	components := card["components"].([]any)
	button := asMap(components[1])
	dest := asMap(button["destination"])
	if dest["type"] != "APP" || dest["googleplay_app_id"] != "com.example.app" {
		t.Fatalf("not an app card %#v", components)
	}
	tweet := findExecuteCall(t, pf, "create_tweet").Input
	if tweet["card_uri"] != "card://123" || strings.Contains(firstString(tweet, "text"), "play.google.com") {
		t.Fatal("app tweet must attach an app card")
	}
	result, err = a.toolConversionReadiness(ctx, map[string]any{"ad_account_id": account, "mobile_app_resource_id": appID, "measurement_source_resource_id": sourceID})
	requireOK(t, result, err)
	if asMap(result)["configured"] != true || asMap(result)["ready"] != false || asMap(result)["eligible_for_optimization"] != "unknown" {
		t.Fatal("recording partner must not attest measurement")
	}
}
func TestGoogleAppAdsAndParentCompatibility(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	a := &App{}
	account := seedResourceTestAccount(t, ctx, "google", "123")
	appID, sourceID := mobileFixture(t, ctx, a, account, "google", "ios")
	pf.executeResponses["search"] = executeJSON(`{"results":[{"campaign":{"id":"8","advertisingChannelType":"MULTI_CHANNEL","advertisingChannelSubType":"APP_CAMPAIGN","appCampaignSetting":{"appId":"123456789"}},"adGroup":{"id":"9"}}]}`)
	args := mobileArgs(account, appID, sourceID)
	args["adset_id"] = "9"
	args["headlines"] = []any{"Try Example"}
	args["descriptions"] = []any{"Install Example and get started."}
	args["image_asset_ids"] = []any{"20"}
	result, err := a.toolAdCreate(ctx, args)
	requireOK(t, result, err)
	create := asMap(asMap(findExecuteCall(t, pf, "ad_mutate").Input["operations"].([]any)[0])["create"])
	ad := asMap(create["ad"])
	if ad["responsiveSearchAd"] != nil || ad["finalUrls"] != nil || ad["appAd"] == nil {
		t.Fatalf("App ad confused with Search ad %#v", ad)
	}
	pf.executeResponses["search"] = executeJSON(`{"results":[{"campaign":{"advertisingChannelType":"SEARCH"}}]}`)
	before := len(pf.executeCalls)
	result, err = a.toolAdCreate(ctx, args)
	if err != nil || mcpResultError(result) == nil {
		t.Fatal("App ad accepted Search parent")
	}
	for _, call := range pf.executeCalls[before:] {
		if call.Tool == "ad_mutate" {
			t.Fatal("incompatible parent reached mutate")
		}
	}
}
func TestConversionReportsDoNotDuplicateSpendOrMeasurements(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	account := seedResourceTestAccount(t, ctx, "meta", "act_1")
	acct := &adAccount{ID: account, ProjectID: "test-proj", Platform: "meta"}
	base := []analyticsPoint{{EntityID: "8", Date: "2026-10-01", SpendMicros: 10000000, Currency: "EUR"}}
	value := int64(20000000)
	points := []conversionPoint{{EntityID: "8", Date: "2026-10-01", EventID: "mobile_app_install", Event: "install", Source: "app", Window: "provider_default", Count: 5}, {EntityID: "8", Date: "2026-10-01", EventID: "mmp_install", Event: "install", Source: "mmp", Window: "provider_default", Count: 4}, {EntityID: "8", Date: "2026-10-01", EventID: "purchase", Event: "purchase", Source: "app", Window: "provider_default", Count: 1, ValueMicros: &value}}
	request := &genericPerformanceRequest{Level: "campaign", DateFrom: "2026-10-01", DateTo: "2026-10-02", EntityIDs: []string{"8"}}
	if err := storeConversionPoints(ctx, acct, request, points); err != nil {
		t.Fatal(err)
	}
	if err := storeConversionPoints(ctx, acct, request, points); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConversionPoints(ctx, acct, request, nil)
	if err != nil || len(loaded) != 3 {
		t.Fatal("retry duplicated event facts")
	}
	report := conversionReport(loaded, base, "cache")
	if report["spend_micros"] != int64(10000000) {
		t.Fatal("event segmentation multiplied spend")
	}
	events := report["events"].([]map[string]any)
	for _, event := range events {
		if event["event_id"] == "mobile_app_install" && event["cpi_micros"] != int64(2000000) {
			t.Fatalf("wrong CPI %#v", event)
		}
		if event["event_id"] == "mmp_install" && event["value_micros"] != nil {
			t.Fatal("missing revenue became zero")
		}
	}
	filtered, err := loadConversionPoints(ctx, acct, request, map[string]any{"measurement_source": "mmp"})
	if err != nil || len(filtered) != 1 || filtered[0].Count != 4 {
		t.Fatal("measurement sources were mixed")
	}
	request.EntityIDs = []string{"other"}
	if err := storeConversionPoints(ctx, acct, request, points); err == nil {
		t.Fatal("out-of-scope event stored")
	}
}
func TestMetaAndXConversionNormalizationPreservesMobileEvents(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	_ = ctx
	point := analyticsPoint{Platform: "meta", EntityID: "1", Date: "2026-10-01", ProviderMetrics: map[string]any{"actions": map[string]float64{"mobile_app_install": 5, "app_custom_event.fb_mobile_purchase": 2, "omni_purchase": 3, "link_click": 100}, "action_values": map[string]float64{"app_custom_event.fb_mobile_purchase": 40}}}
	raw, _ := json.Marshal(point.ProviderMetrics)
	_ = json.Unmarshal(raw, &point.ProviderMetrics)
	events := providerConversionPoints(point)
	if len(events) != 3 {
		t.Fatalf("mobile event loss or click miscount %#v", events)
	}
	point.Platform = "x"
	point.ProviderMetrics = map[string]any{"mobile_conversion_installs": 5, "mobile_conversion_purchases": 2, "conversion_site_visits": 8, "clicks": 20}
	events = providerConversionPoints(point)
	if len(events) != 3 {
		t.Fatalf("wrong X mobile mapping %#v", events)
	}
}

func TestMobilePreflightRetryAndActivationCannotBypassReadiness(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	a := &App{}
	account := seedResourceTestAccount(t, ctx, "google", "123")
	appID, sourceID := mobileFixture(t, ctx, a, account, "google", "android")
	args := mobileArgs(account, appID, sourceID)
	args["idempotency_key"] = "fix-local"
	args["locations"] = []any{"bad"}
	result, err := a.toolCampaignCreate(ctx, args)
	if err != nil || mcpResultError(result) == nil {
		t.Fatal("expected invalid target")
	}
	pf.executeResponses["bulk_mutate"] = executeJSON(`{"mutateOperationResponses":[{"campaignResult":{"resourceName":"customers/123/campaigns/44"}}]}`)
	args["locations"] = []any{"2840"}
	result, err = a.toolCampaignCreate(ctx, args)
	requireOK(t, result, err)
	before := len(pf.executeCalls)
	result, err = a.toolCampaignUpdate(ctx, map[string]any{"ad_account_id": account, "campaign_id": "44", "platform_options": map[string]any{"campaign": map[string]any{"status": "ENABLED"}}})
	if err != nil || asMap(result)["code"] != "mobile_measurement_not_ready" {
		t.Fatalf("native activation bypass %#v %v", result, err)
	}
	for _, call := range pf.executeCalls[before:] {
		if integrationToolMutates(call.Tool) {
			t.Fatal("unverified activation dispatched")
		}
	}
	// Replay survives a new App instance (the reservation/result live in SQLite).
	result, err = (&App{}).toolCampaignCreate(ctx, args)
	requireOK(t, result, err)
	if len(pf.executeCalls) != before+1 {
		t.Fatalf("replay created duplicate; calls %#v", pf.executeCalls)
	}
}

func TestGoogleMobileGroupCannotChooseDifferentAppOrNativeActiveStatus(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	a := &App{}
	account := seedResourceTestAccount(t, ctx, "google", "123")
	appID, sourceID := mobileFixture(t, ctx, a, account, "google", "android")
	pf.executeResponses["search"] = executeJSON(`{"results":[{"campaign":{"id":"8","advertisingChannelType":"MULTI_CHANNEL","advertisingChannelSubType":"APP_CAMPAIGN","appCampaignSetting":{"appId":"com.other.app"}}}]}`)
	args := mobileArgs(account, appID, sourceID)
	args["campaign_id"] = "8"
	result, err := a.toolAdSetCreate(ctx, args)
	if err != nil || mcpResultError(result) == nil {
		t.Fatal("different provider app accepted")
	}
	args["platform_options"] = map[string]any{"ad_group": map[string]any{"status": "ENABLED"}}
	result, err = a.toolAdSetCreate(ctx, args)
	if err != nil || mcpResultError(result) == nil {
		t.Fatal("active native group accepted")
	}
	for _, call := range pf.executeCalls {
		if integrationToolMutates(call.Tool) {
			t.Fatal("invalid group dispatched")
		}
	}
}

func TestXMobileTargetingUsesCatalogOSAndLocationCriteria(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	a := &App{}
	account := seedResourceTestAccount(t, ctx, "x", "acct")
	appID, sourceID := mobileFixture(t, ctx, a, account, "x", "ios")
	pf.executeResponses["list_targeting_platforms"] = executeJSON(`{"data":[{"id":"0","name":"iOS"},{"id":"1","name":"Android"}]}`)
	pf.executeResponses["create_line_item"] = executeJSON(`{"data":{"id":"group1"}}`)
	args := mobileArgs(account, appID, sourceID)
	args["campaign_id"] = "campaign"
	args["targeting"] = map[string]any{"location_ids": []any{"abc123"}}
	result, err := a.toolAdSetCreate(ctx, args)
	requireOK(t, result, err)
	group := findExecuteCall(t, pf, "create_line_item").Input
	if group["ios_app_store_identifier"] != "123456789" || group["goal"] != "APP_INSTALLS" || group["entity_status"] != "PAUSED" {
		t.Fatalf("invalid group %#v", group)
	}
	criteria := []map[string]any{}
	for _, call := range pf.executeCalls {
		if call.Tool == "create_targeting_criteria" {
			criteria = append(criteria, call.Input)
		}
	}
	if len(criteria) != 2 || criteria[0]["targeting_type"] != "PLATFORM" || criteria[0]["targeting_value"] != "0" || criteria[1]["targeting_value"] != "abc123" {
		t.Fatalf("targeting not applied %#v", criteria)
	}
}

func TestXAttributedMobileMetricsKeepIndependentWindows(t *testing.T) {
	start, _ := time.Parse("2006-01-02", "2026-10-01")
	raw := map[string]any{}
	_ = json.Unmarshal([]byte(`{"data":[{"id":"1","id_data":[{"metrics":{"billed_charge_local_micro":[10000000],"mobile_conversion_installs":{"post_view":[2],"post_engagement":[3]},"mobile_conversion_purchases":{"post_view":[1],"post_engagement":[2]}}}]}]}`), &raw)
	points := normalizeXAnalytics(&adAccount{Platform: "x"}, "campaign", start, start, raw)
	if len(points) != 1 {
		t.Fatalf("points %#v", points)
	}
	events := providerConversionPoints(points[0])
	if len(events) != 4 {
		t.Fatalf("attributed maps lost %#v", events)
	}
	report := conversionReport(events, points, "live")
	if report["spend_micros"] != int64(10000000) {
		t.Fatal("spend duplicated")
	}
	for _, event := range events {
		if event.Count <= 0 || event.Source != "mmp" || event.Window == "provider_default" {
			t.Fatalf("missing attribution %#v", event)
		}
	}
}

func TestMobileReportScopeAndWorkerEventCache(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	a := &App{}
	account := seedResourceTestAccount(t, ctx, "meta", "act_1")
	appID, sourceID := mobileFixture(t, ctx, a, account, "meta", "android")
	acct, _, out := a.resolveAdAccount(ctx, map[string]any{"ad_account_id": account})
	if out != nil {
		t.Fatal(out)
	}
	args, _ := a.prepareMobileArgs(ctx, acct, mobileArgs(account, appID, sourceID), "campaign")
	if err := a.persistMobileCampaign(ctx, acct, "c1", args); err != nil {
		t.Fatal(err)
	}
	if err := a.upsertDeliveryEntities(ctx, acct, "ad_group", []map[string]any{{"id": "g1", "name": "group"}}, "c1"); err != nil {
		t.Fatal(err)
	}
	ids, err := mobileReportEntityIDs(ctx, acct, appID, "ad_group")
	if err != nil || len(ids) != 1 || ids[0] != "g1" {
		t.Fatalf("app hierarchy scope %v %v", ids, err)
	}
	request := &genericPerformanceRequest{Level: "campaign", DateFrom: "2026-10-01", DateTo: "2026-10-01", EntityIDs: []string{"c1"}}
	points := []analyticsPoint{{Platform: "meta", EntityID: "c1", Date: "2026-10-01", SpendMicros: 1000000, ProviderMetrics: map[string]any{"actions": map[string]any{"mobile_app_install": 2}}, FetchedAt: "2026-10-02T00:00:00Z"}}
	a.collectMobileConversions(ctx, acct, request, points)
	events, err := loadConversionPoints(ctx, acct, request, map[string]any{})
	if err != nil || len(events) != 1 {
		t.Fatalf("worker cache %v %v", events, err)
	}
	state, err := conversionSyncStatus(ctx, acct, "campaign")
	if err != nil || state["status"] != "ok" {
		t.Fatalf("worker sync %v %v", state, err)
	}
	recordConversionSync(ctx, acct, "campaign", "throttled")
	a.collectMobileConversions(ctx, acct, request, nil)
	events, err = loadConversionPoints(ctx, acct, request, map[string]any{})
	if err != nil || len(events) != 1 {
		t.Fatal("backoff destroyed successful cache")
	}
	result, err := a.toolConversionPerformance(ctx, map[string]any{"ad_account_id": account, "mobile_app_resource_id": appID, "level": "ad_group", "date_from": "2026-10-01", "date_to": "2026-10-01", "refresh": false})
	requireOK(t, result, err)
}

func TestRedditRequestsMobileReportFieldsOnlyOnConversionPath(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	a := &App{}
	acct := &adAccount{Platform: "reddit", NativeAccountID: "acct", ConnectionID: 1}
	r := &genericPerformanceRequest{Level: "campaign", DateFrom: "2026-10-01", DateTo: "2026-10-01", EntityIDs: []string{"c1"}, IncludeEvents: true}
	_, out := a.fetchRedditAnalytics(ctx, acct, r)
	if out != nil {
		t.Fatal(out)
	}
	fields := asMap(findExecuteCall(t, pf, "get_report").Input["data"])["fields"].([]any)
	set := map[string]bool{}
	for _, field := range fields {
		set[toString(field)] = true
	}
	for _, field := range []string{"APP_INSTALL_INSTALL_COUNT", "APP_INSTALL_MMP_PURCHASE_COUNT", "APP_INSTALL_SKAN_INSTALL_COUNT", "APP_INSTALL_MMP_REVENUE"} {
		if !set[field] {
			t.Fatalf("missing mobile field %s", field)
		}
	}
}

func TestGoogleFirstOpenTypeAndSecondaryOptimization(t *testing.T) {
	if googleConversionEvent(map[string]any{"type": "FIREBASE_IOS_FIRST_OPEN", "category": "DEFAULT", "name": "User launches"}) != "first_open" {
		t.Fatal("first open counted as install")
	}
	if googleConversionEvent(map[string]any{"type": "THIRD_PARTY_APP_ANALYTICS_ANDROID_IN_APP_PURCHASE", "category": "DEFAULT"}) != "purchase" {
		t.Fatal("purchase type lost")
	}
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	a := &App{}
	account := seedResourceTestAccount(t, ctx, "google", "123")
	appID, sourceID := mobileFixture(t, ctx, a, account, "google", "android")
	pf.executeResponses["search"] = executeJSON(`{"results":[{"conversionAction":{"id":"7","appId":"com.example.app","type":"FIREBASE_ANDROID_CUSTOM","category":"PURCHASE","status":"ENABLED","primaryForGoal":false,"name":"Secondary purchase"}}]}`)
	result, err := a.toolConversionEventList(ctx, map[string]any{"ad_account_id": account, "mobile_app_resource_id": appID})
	requireOK(t, result, err)
	events := asMap(result)["data"].([]map[string]any)
	eventID := intArg(events[0], "id", 0)
	args := mobileArgs(account, appID, sourceID)
	args["conversion_event_resource_id"] = eventID
	args["app_goal"] = "in_app_event"
	result, err = a.toolCampaignCreate(ctx, args)
	if err != nil || mcpResultError(result) == nil {
		t.Fatal("non-biddable event accepted")
	}
}

func TestRedditAppMoneyAndSKANCountsUseProviderUnits(t *testing.T) {
	point := analyticsPoint{Platform: "reddit", EntityID: "1", Date: "2026-10-01", SpendMicros: 10000000, ProviderMetrics: map[string]any{"APP_INSTALL_MMP_INSTALL_COUNT": 5, "APP_INSTALL_SKAN_INSTALL_COUNT": 2000000, "APP_INSTALL_MMP_REVENUE": 30000000, "APP_INSTALL_SKAN_REVENUE": 20000000}}
	events := providerConversionPoints(point)
	for _, p := range events {
		switch p.EventID {
		case "APP_INSTALL_SKAN_INSTALL_COUNT":
			if p.Count != 2 {
				t.Fatal("SKAN count micros not normalized")
			}
		case "APP_INSTALL_MMP_REVENUE":
			if p.ValueMicros == nil || *p.ValueMicros != 30000000 {
				t.Fatal("revenue micros multiplied twice")
			}
		}
	}
	report := conversionReport(events, []analyticsPoint{point}, "live")
	for _, row := range report["events"].([]map[string]any) {
		if row["event_id"] == "APP_INSTALL_MMP_REVENUE" && row["roas"] != float64(3) {
			t.Fatalf("wrong ROAS %#v", row)
		}
	}
}

func TestCreateKeysPreventConcurrentProviderDispatch(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	a := &App{}
	account := seedResourceTestAccount(t, ctx, "google", "123")
	appID, sourceID := mobileFixture(t, ctx, a, account, "google", "android")
	started, release := make(chan struct{}), make(chan struct{})
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		close(started)
		<-release
		return executeJSON(`{"mutateOperationResponses":[{"campaignResult":{"resourceName":"customers/123/campaigns/88"}}]}`), nil
	}
	args := mobileArgs(account, appID, sourceID)
	args["idempotency_key"] = "concurrent"
	done := make(chan error, 1)
	go func() {
		r, err := a.toolCampaignCreate(ctx, args)
		if err == nil && mcpResultError(r) != nil {
			err = fmt.Errorf("create failed %v", r)
		}
		done <- err
	}()
	<-started
	result, err := a.toolCampaignCreate(ctx, args)
	close(release)
	if err != nil || asMap(result)["code"] != "creation_outcome_unknown" {
		t.Fatalf("concurrent request wasn't reserved %#v %v", result, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(pf.executeCalls) != 1 {
		t.Fatal("duplicate dispatch")
	}
}

func TestGoogleAppEventAndValueGoalsSelectTypedBidding(t *testing.T) {
	for _, test := range []struct {
		goal, strategy, setting string
		target                  int
		roas                    float64
	}{{"in_app_event", "target_cpa", "OPTIMIZE_IN_APP_CONVERSIONS_TARGET_CONVERSION_COST", 500, 0}, {"in_app_event", "maximize_conversions", "OPTIMIZE_IN_APP_CONVERSIONS_WITHOUT_TARGET_CPA", 0, 0}, {"value", "target_roas", "OPTIMIZE_RETURN_ON_ADVERTISING_SPEND", 0, 2}, {"value", "maximize_conversion_value", "OPTIMIZE_TOTAL_VALUE_WITHOUT_TARGET_ROAS", 0, 0}} {
		t.Run(test.strategy, func(t *testing.T) {
			pf := newRecordingPlatform()
			ctx := newAdsCtx(t, pf)
			a := &App{}
			account := seedResourceTestAccount(t, ctx, "google", "123")
			appID, sourceID := mobileFixture(t, ctx, a, account, "google", "android")
			acct, _, _ := a.resolveAdAccount(ctx, map[string]any{"ad_account_id": account})
			event, err := a.upsertResource(ctx, acct, discoveredResource{Kind: resourceConversionAction, ProviderType: "google_conversion_action", NativeID: "7", DisplayName: "Purchase", Status: "active", Metadata: map[string]any{"mobile_app_resource_id": appID, "type": "FIREBASE_ANDROID_IN_APP_PURCHASE", "event": "purchase", "eligible": true, "owner_customer": "customers/123"}})
			if err != nil {
				t.Fatal(err)
			}
			pf.executeResponses["bulk_mutate"] = executeJSON(`{"mutateOperationResponses":[{"campaignResult":{"resourceName":"customers/123/campaigns/33"}}]}`)
			args := mobileArgs(account, appID, sourceID)
			args["app_goal"] = test.goal
			args["bid_strategy"] = test.strategy
			args["conversion_event_resource_id"] = event.ID
			if test.target > 0 {
				args["target_cpa_cents"] = test.target
			}
			if test.roas > 0 {
				args["target_roas"] = test.roas
			}
			result, err := a.toolCampaignCreate(ctx, args)
			requireOK(t, result, err)
			ops := findExecuteCall(t, pf, "bulk_mutate").Input["mutateOperations"].([]any)
			campaign := asMap(asMap(asMap(ops[1])["campaignOperation"])["create"])
			if asMap(campaign["appCampaignSetting"])["biddingStrategyGoalType"] != test.setting {
				t.Fatalf("wrong app goal %#v", campaign)
			}
			actions := asMap(campaign["selectiveOptimization"])["conversionActions"].([]any)
			if len(actions) != 1 || actions[0] != "customers/123/conversionActions/7" {
				t.Fatal("event missing")
			}
		})
	}
}
