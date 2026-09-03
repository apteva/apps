package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func seedResourceTestAccount(t *testing.T, ctx *sdk.AppCtx, platform, nativeID string) int64 {
	t.Helper()
	result, err := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts
		 (project_id, platform, connection_id, native_account_id, display_name, currency, timezone_name)
		 VALUES ('test-proj', ?, 7, ?, 'Test account', 'EUR', 'Europe/Madrid')`,
		platform, nativeID,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func executeJSON(value string) *sdk.ExecuteResult {
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(value)}
}

func findExecuteCall(t *testing.T, platform *recordingPlatform, tool string) executeCall {
	t.Helper()
	platform.mu.Lock()
	defer platform.mu.Unlock()
	for i := len(platform.executeCalls) - 1; i >= 0; i-- {
		if platform.executeCalls[i].Tool == tool {
			return platform.executeCalls[i]
		}
	}
	t.Fatalf("integration tool %s was not called", tool)
	return executeCall{}
}

func resourceByProviderType(t *testing.T, ctx *sdk.AppCtx, accountID int64, providerType string) adResource {
	t.Helper()
	resource, err := scanResource(ctx.AppDB().QueryRow(
		`SELECT id, ad_account_id, kind, provider_type, native_asset_id,
		        COALESCE(parent_resource_id,0), display_name, status,
		        capabilities_json, metadata_json, managed_by_app,
		        COALESCE(refreshed_at,'')
		 FROM ad_resources
		 WHERE project_id='test-proj' AND ad_account_id=? AND provider_type=?`,
		accountID, providerType,
	))
	if err != nil {
		t.Fatal(err)
	}
	return resource
}

func TestMetaResourcesDriveCreativeAndConversionSetup(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "campaign_list":
			return executeJSON(`{"data":[{"id":"campaign_1","objective":"OUTCOME_SALES"}]}`), nil
		case "page_list":
			if strings.Contains(toString(input["fields"]), "instagram_business_account") {
				return executeJSON(`{"data":[{"id":"page_1","name":"Main Page","tasks":["ADVERTISE"],"instagram_business_account":{"id":"ig_1","username":"main"}}]}`), nil
			}
			return executeJSON(`{"data":[{"id":"page_1","name":"Main Page"}]}`), nil
		case "pixel_list":
			return executeJSON(`{"data":[{"id":"pixel_1","name":"Main Pixel","is_unavailable":false}]}`), nil
		case "leadform_list":
			return executeJSON(`{"data":[{"id":"form_1","name":"Lead Form","status":"ACTIVE"}]}`), nil
		case "audience_list":
			return executeJSON(`{"data":[{"id":"audience_1","name":"Customers","subtype":"CUSTOM"}]}`), nil
		default:
			return executeJSON(`{"id":"created_1"}`), nil
		}
	}
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")

	out, err := app.toolAccountContextGet(ctx, map[string]any{"ad_account_id": accountID})
	if err != nil {
		t.Fatal(err)
	}
	context := out.(map[string]any)
	resources := context["resources"].([]map[string]any)
	if len(resources) != 5 {
		t.Fatalf("expected five discovered resources, got %#v", resources)
	}
	for _, resource := range resources {
		if _, leaked := resource["native_id"]; leaked {
			t.Fatalf("native provider ID leaked from normalized response: %#v", resource)
		}
	}

	page := resourceByProviderType(t, ctx, accountID, "facebook_page")
	instagram := resourceByProviderType(t, ctx, accountID, "instagram_business")
	pixel := resourceByProviderType(t, ctx, accountID, "meta_pixel")
	form := resourceByProviderType(t, ctx, accountID, "meta_lead_form")
	if instagram.ParentResourceID != page.ID || form.ParentResourceID != page.ID {
		t.Fatalf("provider parent links were not normalized: page=%d instagram=%d form=%d", page.ID, instagram.ParentResourceID, form.ParentResourceID)
	}
	for purpose, resourceID := range map[string]int64{
		"publishing_identity": page.ID,
		"conversion_source":   pixel.ID,
	} {
		result, err := app.toolResourceSetDefault(ctx, map[string]any{
			"ad_account_id": accountID, "purpose": purpose, "resource_id": resourceID,
		})
		if err != nil || result.(map[string]any)["isError"] == true {
			t.Fatalf("set %s default: result=%#v err=%v", purpose, result, err)
		}
	}

	creative, err := app.toolCreativeCreate(ctx, map[string]any{
		"ad_account_id":   accountID,
		"format":          "link",
		"name":            "Generic creative",
		"destination_url": "https://example.com",
	})
	if err != nil || creative.(map[string]any)["isError"] == true {
		t.Fatalf("creative create failed: result=%#v err=%v", creative, err)
	}
	creativeCall := findExecuteCall(t, platform, "creative_create")
	story := creativeCall.Input["object_story_spec"].(map[string]any)
	if story["page_id"] != "page_1" || story["instagram_user_id"] != "ig_1" {
		t.Fatalf("creative did not resolve normalized identities: %#v", story)
	}

	adSet, err := app.toolAdSetCreate(ctx, map[string]any{
		"ad_account_id":     accountID,
		"campaign_id":       "campaign_1",
		"name":              "Conversions",
		"optimization_goal": "conversions",
		"conversion_event":  "lead",
		"targeting": map[string]any{
			"geo_locations": map[string]any{"countries": []any{"ES"}},
		},
	})
	if err != nil || adSet.(map[string]any)["isError"] == true {
		t.Fatalf("ad set create failed: result=%#v err=%v", adSet, err)
	}
	promoted := findExecuteCall(t, platform, "adset_create").Input["promoted_object"].(map[string]any)
	if promoted["pixel_id"] != "pixel_1" || promoted["custom_event_type"] != "LEAD" {
		t.Fatalf("ad set did not resolve the normalized pixel: %#v", promoted)
	}

	var legacyTables, resourceTables int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='creative_assets'`).Scan(&legacyTables)
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='ad_resources'`).Scan(&resourceTables)
	if legacyTables != 0 || resourceTables != 1 {
		t.Fatalf("expected only the generic resource table; legacy=%d generic=%d", legacyTables, resourceTables)
	}
}

func TestGoogleResourceDiscoveryPaginatesAndKeepsQueriesScoped(t *testing.T) {
	platform := newRecordingPlatform()
	conversionPages := 0
	platform.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if tool != "search" {
			t.Fatalf("unexpected integration tool: %s", tool)
		}
		if input["customer_id"] != "1234567890" {
			t.Fatalf("search escaped the selected account: %#v", input)
		}
		if _, exists := input["page_size"]; exists {
			t.Fatalf("unsupported Google page_size sent: %#v", input)
		}
		query := toString(input["query"])
		if strings.Contains(query, "conversion_action") {
			conversionPages++
			if input["page_token"] == nil {
				return executeJSON(`{"results":[{"conversionAction":{"id":"11","name":"Purchase","status":"ENABLED","type":"WEBPAGE","category":"PURCHASE"}}],"nextPageToken":"next"}`), nil
			}
			return executeJSON(`{"results":[{"conversionAction":{"id":"12","name":"Lead","status":"ENABLED","type":"WEBPAGE","category":"SUBMIT_LEAD_FORM"}}]}`), nil
		}
		if strings.Contains(query, "FROM asset") {
			return executeJSON(`{"results":[{"asset":{"id":"31","resourceName":"customers/1234567890/assets/31","name":"Quote form","type":"LEAD_FORM","leadFormAsset":{"businessName":"Example","headline":"Get a quote","privacyPolicyUrl":"https://example.com/privacy"}}}]}`), nil
		}
		if !strings.Contains(query, "user_list") {
			t.Fatalf("unexpected GAQL query: %s", query)
		}
		return executeJSON(`{"results":[{"userList":{"id":"21","name":"Customers","type":"CRM_BASED","membershipStatus":"OPEN"}}]}`), nil
	}
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	out, err := app.toolAccountContextGet(ctx, map[string]any{"ad_account_id": accountID})
	if err != nil {
		t.Fatal(err)
	}
	context := out.(map[string]any)
	resources := context["resources"].([]map[string]any)
	if len(resources) != 4 || conversionPages != 2 {
		t.Fatalf("unexpected Google resources or pagination: resources=%#v pages=%d", resources, conversionPages)
	}
	conversion := resourceByProviderType(t, ctx, accountID, "google_conversion_action")
	leadForm := resourceByProviderType(t, ctx, accountID, "google_lead_form")
	if leadForm.Kind != resourceLeadForm || leadForm.NativeID != "customers/1234567890/assets/31" {
		t.Fatalf("Google lead form was not normalized: %#v", leadForm)
	}
	if conversion.Status != "active" {
		t.Fatalf("Google status was not normalized: %#v", conversion)
	}
	result, err := app.toolResourceSetDefault(ctx, map[string]any{
		"ad_account_id": accountID, "purpose": "conversion_source", "resource_id": conversion.ID,
	})
	if err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("set Google conversion default: result=%#v err=%v", result, err)
	}
}

func TestResourceOwnershipAndDisconnectCleanup(t *testing.T) {
	ctx := newAdsCtx(t, newRecordingPlatform())
	app := &App{}
	firstAccountID := seedResourceTestAccount(t, ctx, "meta", "act_1")
	secondAccountID := seedResourceTestAccount(t, ctx, "meta", "act_2")
	result, err := ctx.AppDB().Exec(
		`INSERT INTO ad_resources
		 (project_id, ad_account_id, platform, native_asset_id, provider_type, kind, display_name, managed_by_app)
		 VALUES ('test-proj', ?, 'meta', 'page_1', 'facebook_page', 'identity', 'Page', 0)`,
		firstAccountID,
	)
	if err != nil {
		t.Fatal(err)
	}
	resourceID, _ := result.LastInsertId()

	out, err := app.toolResourceGet(ctx, map[string]any{"ad_account_id": secondAccountID, "resource_id": resourceID})
	if err != nil || out.(map[string]any)["isError"] != true {
		t.Fatalf("cross-account resource read was allowed: result=%#v err=%v", out, err)
	}
	_, _ = app.toolResourceSetDefault(ctx, map[string]any{
		"ad_account_id": firstAccountID, "purpose": "publishing_identity", "resource_id": resourceID,
	})
	if _, err := app.toolAccountDisconnect(ctx, map[string]any{"id": firstAccountID}); err != nil {
		t.Fatal(err)
	}
	var resources, defaults int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ad_resources WHERE ad_account_id=?`, firstAccountID).Scan(&resources)
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ad_resource_defaults WHERE ad_account_id=?`, firstAccountID).Scan(&defaults)
	if resources != 0 || defaults != 0 {
		t.Fatalf("disconnect left account resources behind: resources=%d defaults=%d", resources, defaults)
	}
}

func TestPlatformResourceKindSupportMatrix(t *testing.T) {
	allKinds := []string{
		resourceIdentity,
		resourceTrackingSource,
		resourceConversionAction,
		resourceLeadForm,
		resourceAudience,
		resourceCreativeAsset,
		resourceFundingSource,
	}
	expectedRefreshable := map[string]map[string]bool{
		"meta": {
			resourceIdentity: true, resourceTrackingSource: true,
			resourceLeadForm: true, resourceAudience: true,
		},
		"google": {
			resourceConversionAction: true, resourceLeadForm: true, resourceAudience: true,
		},
		"x": {
			resourceIdentity: true, resourceFundingSource: true, resourceAudience: true,
		},
		"reddit": {
			resourceIdentity: true, resourceTrackingSource: true, resourceFundingSource: true,
			resourceLeadForm: true, resourceAudience: true,
		},
	}
	for platform, supported := range expectedRefreshable {
		for _, kind := range allKinds {
			t.Run(platform+"/"+kind, func(t *testing.T) {
				if got := platformCanRefreshResourceKind(platform, kind); got != supported[kind] {
					t.Fatalf("refresh support=%v, want %v", got, supported[kind])
				}
				wantList := supported[kind] || kind == resourceCreativeAsset
				if got := platformCanListResourceKind(platform, kind); got != wantList {
					t.Fatalf("list support=%v, want %v", got, wantList)
				}
			})
		}
	}
	for _, kind := range allKinds {
		if platformCanListResourceKind("unknown", kind) || platformCanRefreshResourceKind("unknown", kind) {
			t.Fatalf("unknown platform accepted %s", kind)
		}
	}
}

func TestResourceListRejectsUnsupportedPlatformKindsBeforeRefresh(t *testing.T) {
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	allKinds := []string{
		resourceIdentity,
		resourceTrackingSource,
		resourceConversionAction,
		resourceLeadForm,
		resourceAudience,
		resourceCreativeAsset,
		resourceFundingSource,
	}
	for platformName := range platformResourceKinds {
		accountID := seedResourceTestAccount(t, ctx, platformName, platformName+"_account")
		for _, kind := range allKinds {
			if platformCanListResourceKind(platformName, kind) {
				continue
			}
			for _, refresh := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/refresh=%t", platformName, kind, refresh), func(t *testing.T) {
					result, err := app.toolResourceList(ctx, map[string]any{
						"ad_account_id": accountID,
						"kind":          kind,
						"refresh":       refresh,
					})
					if err != nil {
						t.Fatal(err)
					}
					out := result.(map[string]any)
					want := "unsupported resource kind for " + platformName + ": " + kind
					if out["isError"] != true || mcpErrorMessage(out) != want {
						t.Fatalf("unexpected validation response: %#v", out)
					}
				})
			}
		}
	}
	if len(platform.executeCalls) != 0 {
		t.Fatalf("unsupported kinds reached provider discovery: %#v", platform.executeCalls)
	}
}

func TestResourceListKeepsAppManagedCreativeAssetsListable(t *testing.T) {
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO ad_resources
		 (project_id, ad_account_id, platform, native_asset_id, provider_type, kind, display_name, managed_by_app)
		 VALUES ('test-proj', ?, 'meta', 'image_1', 'image', 'creative_asset', 'Hero image', 1)`,
		accountID,
	); err != nil {
		t.Fatal(err)
	}

	result, err := app.toolResourceList(ctx, map[string]any{
		"ad_account_id": accountID,
		"kind":          resourceCreativeAsset,
		"refresh":       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	data := result.(map[string]any)["data"].([]map[string]any)
	if len(data) != 1 || data[0]["name"] != "Hero image" {
		t.Fatalf("unexpected creative assets: %#v", data)
	}
	if len(platform.executeCalls) != 0 {
		t.Fatalf("app-managed creative refresh reached provider: %#v", platform.executeCalls)
	}
}

func TestMetaCampaignCreateIgnoresGenericFundingSourceResource(t *testing.T) {
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")

	result, err := app.toolCampaignCreate(ctx, map[string]any{
		"ad_account_id":              accountID,
		"name":                       "Meta traffic",
		"objective":                  "traffic",
		"funding_source_resource_id": 999,
	})
	if err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("Meta campaign create failed: result=%#v err=%v", result, err)
	}
	input := findExecuteCall(t, platform, "campaign_create").Input
	if _, exists := input["funding_source_resource_id"]; exists {
		t.Fatalf("generic local resource id leaked upstream: %#v", input)
	}
	if _, exists := input["funding_instrument_id"]; exists {
		t.Fatalf("Meta campaign unexpectedly required a funding source: %#v", input)
	}
}
