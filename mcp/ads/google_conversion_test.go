package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func seedGooglePending(t *testing.T, ctx *sdk.AppCtx) int64 {
	t.Helper()
	res, err := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, connection_id, status, expires_at)
		 VALUES ('test-proj','google','google-ads',8,'ready',datetime('now','+1 hour'))`,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// ── Account picker ─────────────────────────────────────────────────────────

func TestGoogleAccountPickerRecoversFromFailedDetailLookup(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		query, _ := input["query"].(string)
		switch {
		case tool == "list_accounts":
			return executeJSON(`{"resourceNames":["customers/1111111111"]}`), nil
		case strings.Contains(query, "FROM customer LIMIT 1"):
			// The manager account denies the customer read. This used to fall
			// through as a bare {id, name} row whose absent manager flag read
			// false, so the MCC was offered as a place to run ads.
			return &sdk.ExecuteResult{Success: false, Status: 403, Data: json.RawMessage(`{"error":"PERMISSION_DENIED"}`)}, nil
		case strings.Contains(query, "FROM customer_client"):
			return executeJSON(`{"results":[
				{"customerClient":{"id":"1111111111","descriptiveName":"Agency Manager","manager":true,"status":"ENABLED","level":"0"}},
				{"customerClient":{"id":"2222222222","descriptiveName":"Client One","currencyCode":"EUR","timeZone":"Europe/Madrid","manager":false,"status":"ENABLED","level":"1"}}
			]}`), nil
		}
		t.Fatalf("unexpected call: tool=%s input=%#v", tool, input)
		return nil, nil
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}

	out, err := app.toolAccountListPendingPages(ctx, map[string]any{
		"pending_account_id": seedGooglePending(t, ctx),
	})
	if err != nil {
		t.Fatal(err)
	}
	pages := out.(map[string]any)["pages"].([]map[string]any)
	if len(pages) != 1 {
		t.Fatalf("expected only the operating account, got %#v", pages)
	}
	if pages[0]["id"] != "2222222222" || pages[0]["name"] != "Client One" {
		t.Fatalf("hierarchy fallback did not recover client metadata: %#v", pages[0])
	}
	if pages[0]["manager"] != false {
		t.Fatalf("every row must carry a manager flag: %#v", pages[0])
	}
	if pages[0]["currency"] != "EUR" {
		t.Fatalf("fallback lost the account currency: %#v", pages[0])
	}
}

func TestGoogleAccountPickerFlagsUnverifiableAccounts(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "list_accounts" {
			return executeJSON(`{"resourceNames":["customers/1111111111"]}`), nil
		}
		// Neither the detail read nor the hierarchy read succeeds.
		return &sdk.ExecuteResult{Success: false, Status: 403, Data: json.RawMessage(`{"error":"PERMISSION_DENIED"}`)}, nil
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}

	out, err := app.toolAccountListPendingPages(ctx, map[string]any{
		"pending_account_id": seedGooglePending(t, ctx),
	})
	if err != nil {
		t.Fatal(err)
	}
	pages := out.(map[string]any)["pages"].([]map[string]any)
	if len(pages) != 1 {
		t.Fatalf("the account should still be offered, got %#v", pages)
	}
	// Visible, but explicitly marked so its defaults are not read as facts.
	if pages[0]["detail_unavailable"] != true {
		t.Fatalf("unverified account was not flagged: %#v", pages[0])
	}
	if _, ok := pages[0]["manager"]; !ok {
		t.Fatalf("manager flag must always be present: %#v", pages[0])
	}
}

// ── Conversion actions ─────────────────────────────────────────────────────

func TestGoogleConversionActionCreate(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["conversion_action_mutate"] = executeJSON(
		`{"results":[{"resourceName":"customers/1234567890/conversionActions/777"}]}`)
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	result, err := app.toolTrackingSourceCreate(ctx, map[string]any{
		"ad_account_id": accountID, "name": "Purchase", "category": "purchase",
		"reuse_existing": false, "default_value_cents": 2500,
	})
	if err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("conversion action create failed: result=%#v err=%v", result, err)
	}
	create := asMap(asMap(findExecuteCall(t, pf, "conversion_action_mutate").
		Input["operations"].([]any)[0])["create"])
	if create["name"] != "Purchase" || create["category"] != "PURCHASE" || create["type"] != "WEBPAGE" {
		t.Fatalf("unexpected conversion action: %#v", create)
	}
	// A purchase can happen more than once per click; a lead should not.
	if create["countingType"] != "MANY_PER_CLICK" {
		t.Fatalf("purchase should count many per click: %#v", create)
	}
	if create["primaryForGoal"] != true {
		t.Fatalf("conversion action should be primary for its goal: %#v", create)
	}
	if values := asMap(create["valueSettings"]); values["defaultValue"] != 25.0 {
		t.Fatalf("default value was not converted from cents: %#v", create)
	}
	if result.(map[string]any)["created"] != true {
		t.Fatalf("result should report creation: %#v", result)
	}

	resource := resourceByProviderType(t, ctx, accountID, "google_conversion_action")
	if resource.NativeID != "777" || !resource.ManagedByApp {
		t.Fatalf("conversion action was not recorded as managed: %#v", resource)
	}
}

func TestGoogleConversionActionCountingAndCategoryValidation(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["conversion_action_mutate"] = executeJSON(
		`{"results":[{"resourceName":"customers/1234567890/conversionActions/778"}]}`)
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	if result, err := app.toolTrackingSourceCreate(ctx, map[string]any{
		"ad_account_id": accountID, "name": "Lead", "category": "lead", "reuse_existing": false,
	}); err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("lead conversion action failed: %#v", result)
	}
	create := asMap(asMap(findExecuteCall(t, pf, "conversion_action_mutate").
		Input["operations"].([]any)[0])["create"])
	if create["category"] != "LEAD" || create["countingType"] != "ONE_PER_CLICK" {
		t.Fatalf("a lead should be counted once per click: %#v", create)
	}

	if result, _ := app.toolTrackingSourceCreate(ctx, map[string]any{
		"ad_account_id": accountID, "name": "Bad", "category": "teleportation", "reuse_existing": false,
	}); result.(map[string]any)["isError"] != true {
		t.Fatalf("an unknown category was accepted: %#v", result)
	}
}

func TestGoogleConversionInstallationReturnsGtagSnippet(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["search"] = executeJSON(`{"results":[{"conversionAction":{
		"id":"777","name":"Purchase","status":"ENABLED","category":"PURCHASE",
		"tagSnippets":[{
			"type":"WEBPAGE","pageFormat":"HTML",
			"globalSiteTag":"<script async src=\"https://www.googletagmanager.com/gtag/js?id=AW-123456789\"></script>",
			"eventSnippet":"<script>gtag('event','conversion',{'send_to':'AW-123456789/AbC-D_efGhIjKl'});</script>"
		}]
	}}]}`)
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")
	resource, err := app.upsertResource(ctx, &adAccount{ID: accountID, Platform: "google"}, discoveredResource{
		Kind: resourceConversionAction, ProviderType: "google_conversion_action",
		NativeID: "777", DisplayName: "Purchase", Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}

	resultAny, err := app.toolTrackingSourceInstallationGet(ctx, map[string]any{
		"ad_account_id": accountID, "tracking_source_resource_id": resource.ID,
	})
	if err != nil || resultAny.(map[string]any)["isError"] == true {
		t.Fatalf("installation get failed: %#v", resultAny)
	}
	installation := resultAny.(map[string]any)["installation"].(map[string]any)
	if installation["provider"] != "google" || installation["snippet_available"] != true {
		t.Fatalf("unexpected installation payload: %#v", installation)
	}
	// The conversion id and label are what a site actually needs; scraping them
	// out of the snippet should not be every caller's job.
	if installation["conversion_id"] != "AW-123456789" {
		t.Fatalf("conversion id not extracted: %#v", installation)
	}
	if installation["conversion_label"] != "AbC-D_efGhIjKl" {
		t.Fatalf("conversion label not extracted: %#v", installation)
	}
	if installation["send_to"] != "AW-123456789/AbC-D_efGhIjKl" {
		t.Fatalf("send_to not assembled: %#v", installation)
	}
	if !strings.Contains(toString(installation["global_site_tag"]), "googletagmanager.com") {
		t.Fatalf("global site tag missing: %#v", installation)
	}
}

// ── Ad extensions ──────────────────────────────────────────────────────────

func TestAdExtensionCreateLinksAssetsToCampaign(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["asset_mutate"] = executeJSON(`{"results":[
		{"resourceName":"customers/1234567890/assets/11"},
		{"resourceName":"customers/1234567890/assets/12"}
	]}`)
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	result, err := app.toolAdExtensionCreate(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": "987", "type": "sitelink",
		"extensions": []any{
			map[string]any{"link_text": "Shop Sale", "final_url": "https://example.com/sale",
				"description1": "Up to 50% off", "description2": "Ends Sunday"},
			map[string]any{"link_text": "Contact Us", "final_url": "https://example.com/contact"},
		},
	})
	if err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("ad_extension_create failed: result=%#v err=%v", result, err)
	}
	assetOps := findExecuteCall(t, pf, "asset_mutate").Input["operations"].([]any)
	asset := asMap(asMap(assetOps[0])["create"])
	sitelink := asMap(asset["sitelinkAsset"])
	if sitelink["linkText"] != "Shop Sale" || sitelink["description1"] != "Up to 50% off" {
		t.Fatalf("unexpected sitelink asset: %#v", asset)
	}

	linkOps := findExecuteCall(t, pf, "campaign_asset_mutate").Input["operations"].([]any)
	if len(linkOps) != 2 {
		t.Fatalf("expected one link per asset, got %#v", linkOps)
	}
	link := asMap(asMap(linkOps[0])["create"])
	if link["campaign"] != "customers/1234567890/campaigns/987" {
		t.Fatalf("extension not linked to the campaign: %#v", link)
	}
	// The plumbing previously only ever sent LEAD_FORM.
	if link["fieldType"] != "SITELINK" {
		t.Fatalf("wrong field type: %#v", link)
	}
}

func TestAdExtensionCalloutAndStructuredSnippet(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["asset_mutate"] = executeJSON(
		`{"results":[{"resourceName":"customers/1234567890/assets/11"}]}`)
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	if result, err := app.toolAdExtensionCreate(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": "987", "type": "callout",
		"extensions": []any{map[string]any{"text": "Free Returns"}},
	}); err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("callout create failed: %#v", result)
	}
	asset := asMap(asMap(findExecuteCall(t, pf, "asset_mutate").Input["operations"].([]any)[0])["create"])
	if asMap(asset["calloutAsset"])["calloutText"] != "Free Returns" {
		t.Fatalf("unexpected callout asset: %#v", asset)
	}
	if findExecuteCall(t, pf, "campaign_asset_mutate").Input["operations"].([]any)[0].(map[string]any)["create"].(map[string]any)["fieldType"] != "CALLOUT" {
		t.Fatalf("callout field type not set")
	}

	if result, err := app.toolAdExtensionCreate(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": "987", "type": "structured_snippet",
		"extensions": []any{map[string]any{"header": "Brands", "values": []any{"Nike", "Adidas", "Asics"}}},
	}); err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("structured snippet create failed: %#v", result)
	}
	snippet := asMap(asMap(asMap(findExecuteCall(t, pf, "asset_mutate").
		Input["operations"].([]any)[0])["create"])["structuredSnippetAsset"])
	if snippet["header"] != "Brands" || len(snippet["values"].([]any)) != 3 {
		t.Fatalf("unexpected structured snippet: %#v", snippet)
	}
}

func TestAdExtensionValidation(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	cases := []struct {
		name string
		args map[string]any
	}{
		{"callout over 25 characters", map[string]any{"type": "callout",
			"extensions": []any{map[string]any{"text": "This callout text is much too long"}}}},
		{"sitelink with one description", map[string]any{"type": "sitelink",
			"extensions": []any{map[string]any{"link_text": "Sale", "final_url": "https://example.com", "description1": "Only one"}}}},
		{"sitelink with a relative url", map[string]any{"type": "sitelink",
			"extensions": []any{map[string]any{"link_text": "Sale", "final_url": "/sale"}}}},
		{"unknown snippet header", map[string]any{"type": "structured_snippet",
			"extensions": []any{map[string]any{"header": "Vibes", "values": []any{"A", "B", "C"}}}}},
		{"too few snippet values", map[string]any{"type": "structured_snippet",
			"extensions": []any{map[string]any{"header": "Brands", "values": []any{"Nike"}}}}},
		{"unknown extension type", map[string]any{"type": "banner",
			"extensions": []any{map[string]any{"text": "Hi"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"ad_account_id": accountID, "campaign_id": "987"}
			for k, v := range tc.args {
				args[k] = v
			}
			result, err := app.toolAdExtensionCreate(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			if result.(map[string]any)["isError"] != true {
				t.Fatalf("invalid extension was accepted: %#v", result)
			}
		})
	}
	if len(pf.executeCalls) != 0 {
		t.Fatalf("an invalid extension reached the provider: %#v", pf.executeCalls)
	}
}

func TestAdExtensionCreateReportsOrphanedAssetsWhenLinkFails(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "asset_mutate" {
			return executeJSON(`{"results":[{"resourceName":"customers/1234567890/assets/11"}]}`), nil
		}
		return &sdk.ExecuteResult{Success: false, Status: 400, Data: json.RawMessage(`{"error":"INVALID_CAMPAIGN"}`)}, nil
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	result, err := app.toolAdExtensionCreate(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": "987", "type": "callout",
		"extensions": []any{map[string]any{"text": "Free Returns"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	if out["isError"] != true {
		t.Fatalf("link failure should surface as an error: %#v", out)
	}
	// The assets exist but point at nothing; a silent failure here would make
	// the caller create duplicates on retry.
	orphaned, ok := out["orphaned_assets"].([]string)
	if !ok || len(orphaned) != 1 || orphaned[0] != "customers/1234567890/assets/11" {
		t.Fatalf("orphaned assets were not reported: %#v", out)
	}
}

func TestAdExtensionListNormalizesRows(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["search"] = executeJSON(`{"results":[
		{"campaign":{"id":"987"},"campaignAsset":{"asset":"customers/1234567890/assets/11","fieldType":"SITELINK","status":"ENABLED"},
		 "asset":{"id":"11","name":"Shop Sale","finalUrls":["https://example.com/sale"],
		  "sitelinkAsset":{"linkText":"Shop Sale","description1":"Up to 50% off","description2":"Ends Sunday"}}},
		{"campaignAsset":{"asset":"customers/1234567890/assets/12","fieldType":"CALLOUT","status":"ENABLED"},
		 "asset":{"id":"12","name":"Free Returns","calloutAsset":{"calloutText":"Free Returns"}}}
	]}`)
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	result, err := app.toolAdExtensionList(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": "987",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	if out["count"] != 2 {
		t.Fatalf("expected two extensions: %#v", out)
	}
	items := out["extensions"].([]map[string]any)
	if items[0]["link_text"] != "Shop Sale" || items[0]["field_type"] != "SITELINK" {
		t.Fatalf("sitelink not normalized: %#v", items[0])
	}
	// ad_extension_delete sources asset_resource_names from here, so the row has
	// to carry enough to act on and to read.
	if items[0]["asset_resource_name"] != "customers/1234567890/assets/11" {
		t.Fatalf("asset resource name missing: %#v", items[0])
	}
	if items[0]["description1"] != "Up to 50% off" || items[0]["description2"] != "Ends Sunday" {
		t.Fatalf("sitelink descriptions not surfaced: %#v", items[0])
	}
	if urls, ok := items[0]["final_urls"].([]any); !ok || len(urls) != 1 {
		t.Fatalf("sitelink final urls not surfaced: %#v", items[0])
	}
	if items[1]["text"] != "Free Returns" || items[1]["field_type"] != "CALLOUT" {
		t.Fatalf("callout not normalized: %#v", items[1])
	}
}

func TestAdExtensionSurfaceIsGoogleOnly(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	metaID := seedResourceTestAccount(t, ctx, "meta", "act_42")

	result, err := app.toolAdExtensionCreate(ctx, map[string]any{
		"ad_account_id": metaID, "campaign_id": "987", "type": "callout",
		"extensions": []any{map[string]any{"text": "Free Returns"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	if out["code"] != "unsupported_operation" || out["platform"] != "meta" {
		t.Fatalf("expected a structured unsupported error: %#v", out)
	}
	// The message must name the operation, not keywords.
	text := toString(out["content"].([]map[string]any)[0]["text"])
	if !strings.Contains(text, "ad_extension_create") {
		t.Fatalf("error should name the operation: %q", text)
	}
}
