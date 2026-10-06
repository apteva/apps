package main

import (
	"testing"
)

// ── Keywords ───────────────────────────────────────────────────────────────

func TestKeywordCreateBuildsMatchTypedCriteria(t *testing.T) {
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	result, err := app.toolKeywordCreate(ctx, map[string]any{
		"ad_account_id": accountID,
		"adset_id":      "555",
		"keywords": []any{
			map[string]any{"text": "running shoes", "match_type": "exact", "cpc_bid_cents": 150},
			map[string]any{"text": "trail runners", "match_type": "phrase"},
			map[string]any{"text": "sneakers"},
		},
	})
	if err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("keyword_create failed: result=%#v err=%v", result, err)
	}
	call := findExecuteCall(t, platform, "ad_group_criterion_mutate")
	if call.Input["customer_id"] != "1234567890" {
		t.Fatalf("wrong customer: %#v", call.Input)
	}
	ops, _ := call.Input["operations"].([]any)
	if len(ops) != 3 {
		t.Fatalf("expected 3 operations, got %d: %#v", len(ops), ops)
	}

	first := asMap(asMap(ops[0])["create"])
	if first["adGroup"] != "customers/1234567890/adGroups/555" {
		t.Fatalf("keyword not attached to the ad group: %#v", first)
	}
	if kw := asMap(first["keyword"]); kw["text"] != "running shoes" || kw["matchType"] != "EXACT" {
		t.Fatalf("unexpected keyword payload: %#v", first)
	}
	if first["cpcBidMicros"] != "1500000" {
		t.Fatalf("cents were not converted to micros: %#v", first)
	}
	if first["status"] != "ENABLED" {
		t.Fatalf("keyword should default to enabled: %#v", first)
	}
	// An omitted match_type must not silently become EXACT.
	third := asMap(asMap(ops[2])["create"])
	if kw := asMap(third["keyword"]); kw["matchType"] != "BROAD" {
		t.Fatalf("default match type should be BROAD: %#v", third)
	}
	if _, bid := third["cpcBidMicros"]; bid {
		t.Fatalf("keyword without a bid should not send cpcBidMicros: %#v", third)
	}
}

func TestKeywordCreateRejectsMatchTypeSyntaxInText(t *testing.T) {
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	for _, text := range []string{`[running shoes]`, `"running shoes"`, `+running shoes`} {
		result, err := app.toolKeywordCreate(ctx, map[string]any{
			"ad_account_id": accountID, "adset_id": "555",
			"keywords": []any{map[string]any{"text": text}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.(map[string]any)["isError"] != true {
			t.Fatalf("match-type punctuation %q was accepted: %#v", text, result)
		}
	}
	if len(platform.executeCalls) != 0 {
		t.Fatalf("invalid keywords reached the provider: %#v", platform.executeCalls)
	}
}

func TestKeywordCreateValidatesLengthAndDeduplicates(t *testing.T) {
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	long := "a one two three four five six seven eight nine ten eleven"
	if result, _ := app.toolKeywordCreate(ctx, map[string]any{
		"ad_account_id": accountID, "adset_id": "555",
		"keywords": []any{map[string]any{"text": long}},
	}); result.(map[string]any)["isError"] != true {
		t.Fatalf("an over-long keyword was accepted: %#v", result)
	}

	// Same text and match type twice is one keyword, not two.
	result, err := app.toolKeywordCreate(ctx, map[string]any{
		"ad_account_id": accountID, "adset_id": "555",
		"keywords": []any{
			map[string]any{"text": "running shoes", "match_type": "exact"},
			map[string]any{"text": "Running Shoes", "match_type": "exact"},
			map[string]any{"text": "running shoes", "match_type": "broad"},
		},
	})
	if err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("keyword_create failed: %#v", result)
	}
	if got := result.(map[string]any)["requested"]; got != 2 {
		t.Fatalf("expected 2 deduplicated keywords, got %v", got)
	}
}

func TestKeywordSurfaceIsGoogleOnly(t *testing.T) {
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	metaID := seedResourceTestAccount(t, ctx, "meta", "act_42")

	result, err := app.toolKeywordCreate(ctx, map[string]any{
		"ad_account_id": metaID, "adset_id": "555",
		"keywords": []any{map[string]any{"text": "shoes"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	if out["code"] != "unsupported_operation" || out["platform"] != "meta" {
		t.Fatalf("expected a structured unsupported error: %#v", out)
	}
	if len(platform.executeCalls) != 0 {
		t.Fatalf("Meta keyword call reached the provider: %#v", platform.executeCalls)
	}
}

func TestNegativeKeywordsOmitStatusAndBid(t *testing.T) {
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	result, err := app.toolNegativeKeywordCreate(ctx, map[string]any{
		"ad_account_id": accountID, "adset_id": "555",
		"keywords": []any{map[string]any{"text": "free", "match_type": "broad"}},
	})
	if err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("negative_keyword_create failed: %#v", result)
	}
	call := findExecuteCall(t, platform, "ad_group_criterion_mutate")
	create := asMap(asMap(asMap(call.Input["operations"].([]any)[0]))["create"])
	if create["negative"] != true {
		t.Fatalf("criterion is not negative: %#v", create)
	}
	// Google rejects a negative criterion that carries either field.
	if _, ok := create["status"]; ok {
		t.Fatalf("negative criterion must not carry status: %#v", create)
	}
	if _, ok := create["cpcBidMicros"]; ok {
		t.Fatalf("negative criterion must not carry a bid: %#v", create)
	}

	if result, _ := app.toolNegativeKeywordCreate(ctx, map[string]any{
		"ad_account_id": accountID, "adset_id": "555",
		"keywords": []any{map[string]any{"text": "cheap", "cpc_bid_cents": 100}},
	}); result.(map[string]any)["isError"] != true {
		t.Fatalf("a bid on a negative keyword was accepted: %#v", result)
	}
}

func TestCampaignNegativeKeywordsUseCampaignCriterionService(t *testing.T) {
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	result, err := app.toolNegativeKeywordCreate(ctx, map[string]any{
		"ad_account_id": accountID, "level": "campaign", "campaign_id": "987",
		"keywords": []any{map[string]any{"text": "jobs", "match_type": "phrase"}},
	})
	if err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("campaign negative create failed: %#v", result)
	}
	call := findExecuteCall(t, platform, "campaign_criterion_mutate")
	create := asMap(asMap(asMap(call.Input["operations"].([]any)[0]))["create"])
	if create["campaign"] != "customers/1234567890/campaigns/987" {
		t.Fatalf("campaign negative not scoped to the campaign: %#v", create)
	}
	if create["negative"] != true || asMap(create["keyword"])["matchType"] != "PHRASE" {
		t.Fatalf("unexpected campaign criterion: %#v", create)
	}
	if _, ok := create["adGroup"]; ok {
		t.Fatalf("campaign criterion must not name an ad group: %#v", create)
	}
}

func TestKeywordListNormalizesAndFiltersNegatives(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponses["search"] = executeJSON(`{"results":[
		{"adGroupCriterion":{"criterionId":"11","status":"ENABLED","negative":false,"cpcBidMicros":"1500000","keyword":{"text":"running shoes","matchType":"EXACT"}}},
		{"adGroupCriterion":{"criterionId":"22","status":"ENABLED","negative":true,"keyword":{"text":"free","matchType":"BROAD"}}}
	]}`)
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	result, err := app.toolKeywordList(ctx, map[string]any{"ad_account_id": accountID, "adset_id": "555"})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	if out["count"] != 1 {
		t.Fatalf("negatives should be excluded by default: %#v", out)
	}
	first := out["keywords"].([]map[string]any)[0]
	if first["id"] != "11" || first["text"] != "running shoes" || first["match_type"] != "EXACT" {
		t.Fatalf("unexpected normalized keyword: %#v", first)
	}

	result, _ = app.toolKeywordList(ctx, map[string]any{
		"ad_account_id": accountID, "adset_id": "555", "include_negatives": true,
	})
	if result.(map[string]any)["count"] != 2 {
		t.Fatalf("include_negatives did not return the negative: %#v", result)
	}
}

func TestKeywordUpdateAndDeleteTargetCriterionResources(t *testing.T) {
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	if result, err := app.toolKeywordUpdate(ctx, map[string]any{
		"ad_account_id": accountID, "adset_id": "555", "keyword_id": "11",
		"status": "PAUSED", "cpc_bid_cents": 200,
	}); err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("keyword_update failed: %#v", result)
	}
	op := asMap(findExecuteCall(t, platform, "ad_group_criterion_mutate").Input["operations"].([]any)[0])
	update := asMap(op["update"])
	if update["resourceName"] != "customers/1234567890/adGroupCriteria/555~11" {
		t.Fatalf("wrong criterion resource: %#v", update)
	}
	if op["updateMask"] != "status,cpc_bid_micros" {
		t.Fatalf("unexpected update mask: %#v", op)
	}

	// An update with no mutable field is a caller error, not an empty mutate.
	if result, _ := app.toolKeywordUpdate(ctx, map[string]any{
		"ad_account_id": accountID, "adset_id": "555", "keyword_id": "11",
	}); result.(map[string]any)["isError"] != true {
		t.Fatalf("empty update was accepted: %#v", result)
	}

	if result, err := app.toolKeywordDelete(ctx, map[string]any{
		"ad_account_id": accountID, "adset_id": "555", "keyword_ids": []any{"11", "22"},
	}); err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("keyword_delete failed: %#v", result)
	}
	ops := findExecuteCall(t, platform, "ad_group_criterion_mutate").Input["operations"].([]any)
	if len(ops) != 2 || asMap(ops[1])["remove"] != "customers/1234567890/adGroupCriteria/555~22" {
		t.Fatalf("unexpected remove operations: %#v", ops)
	}
}

// ── Responsive Search Ads ──────────────────────────────────────────────────

func rsaArgs(accountID int64, extra map[string]any) map[string]any {
	args := map[string]any{
		"ad_account_id": accountID,
		"adset_id":      "555",
		"name":          "Brand RSA",
		"headlines": []any{
			map[string]any{"text": "Running Shoes", "pinned_field": "headline_1"},
			map[string]any{"text": "Free Delivery"},
			map[string]any{"text": "Shop The Sale"},
		},
		"descriptions": []any{
			map[string]any{"text": "Next day delivery on every order."},
			map[string]any{"text": "Free returns within 30 days."},
		},
		"final_urls": []any{"https://example.com/shoes"},
	}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

func TestAdCreateBuildsResponsiveSearchAd(t *testing.T) {
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	result, err := app.toolAdCreate(ctx, rsaArgs(accountID, map[string]any{"path1": "shoes", "path2": "sale"}))
	if err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("ad_create failed: result=%#v err=%v", result, err)
	}
	call := findExecuteCall(t, platform, "ad_mutate")
	create := asMap(asMap(call.Input["operations"].([]any)[0])["create"])
	if create["adGroup"] != "customers/1234567890/adGroups/555" {
		t.Fatalf("ad not attached to the ad group: %#v", create)
	}
	ad := asMap(create["ad"])
	rsa := asMap(ad["responsiveSearchAd"])
	if len(rsa["headlines"].([]any)) != 3 || len(rsa["descriptions"].([]any)) != 2 {
		t.Fatalf("unexpected RSA asset counts: %#v", rsa)
	}
	if asMap(rsa["headlines"].([]any)[0])["pinnedField"] != "HEADLINE_1" {
		t.Fatalf("headline pin was not mapped: %#v", rsa)
	}
	if rsa["path1"] != "shoes" || rsa["path2"] != "sale" {
		t.Fatalf("display paths missing: %#v", rsa)
	}
	if urls, _ := ad["finalUrls"].([]any); len(urls) != 1 || urls[0] != "https://example.com/shoes" {
		t.Fatalf("final URLs missing: %#v", ad)
	}
}

func TestResponsiveSearchAdValidation(t *testing.T) {
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	cases := []struct {
		name  string
		extra map[string]any
	}{
		{"too few headlines", map[string]any{"headlines": []any{
			map[string]any{"text": "One"}, map[string]any{"text": "Two"},
		}}},
		{"too few descriptions", map[string]any{"descriptions": []any{
			map[string]any{"text": "Only one description."},
		}}},
		{"description pinned to a headline slot", map[string]any{"descriptions": []any{
			map[string]any{"text": "First description.", "pinned_field": "headline_1"},
			map[string]any{"text": "Second description."},
		}}},
		{"duplicate headline text", map[string]any{"headlines": []any{
			map[string]any{"text": "Same"}, map[string]any{"text": "Same"}, map[string]any{"text": "Other"},
		}}},
		{"path2 without path1", map[string]any{"path2": "sale"}},
		{"relative final url", map[string]any{"final_urls": []any{"/shoes"}}},
		{"headline over 30 characters", map[string]any{"headlines": []any{
			map[string]any{"text": "This headline is far too long to be accepted"},
			map[string]any{"text": "Two"}, map[string]any{"text": "Three"},
		}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := app.toolAdCreate(ctx, rsaArgs(accountID, tc.extra))
			if err != nil {
				t.Fatal(err)
			}
			if result.(map[string]any)["isError"] != true {
				t.Fatalf("invalid RSA was accepted: %#v", result)
			}
		})
	}
	if len(platform.executeCalls) != 0 {
		t.Fatalf("an invalid RSA reached the provider: %#v", platform.executeCalls)
	}
}

// ── Campaign channel type ──────────────────────────────────────────────────

func googleCampaignCreateArgs(accountID int64, extra map[string]any) map[string]any {
	args := map[string]any{
		"ad_account_id":      accountID,
		"name":               "Brand Search",
		"objective":          "sales",
		"daily_budget_cents": 5000,
	}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

func createdGoogleCampaign(t *testing.T, platform *recordingPlatform) map[string]any {
	t.Helper()
	call := findExecuteCall(t, platform, "campaign_mutate")
	return asMap(asMap(call.Input["operations"].([]any)[0])["create"])
}

func TestCampaignCreateChannelType(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponses["budget_mutate"] = executeJSON(
		`{"results":[{"resourceName":"customers/1234567890/campaignBudgets/55"}]}`)
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	// Default stays SEARCH so existing callers are unaffected.
	if result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID, nil)); err != nil ||
		result.(map[string]any)["isError"] == true {
		t.Fatalf("campaign_create failed: %#v", result)
	}
	if got := createdGoogleCampaign(t, platform)["advertisingChannelType"]; got != "SEARCH" {
		t.Fatalf("default channel should be SEARCH, got %v", got)
	}

	// And a different shape is now reachable without a native payload.
	if result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID,
		map[string]any{"channel_type": "performance_max"})); err != nil ||
		result.(map[string]any)["isError"] == true {
		t.Fatalf("campaign_create failed: %#v", result)
	}
	if got := createdGoogleCampaign(t, platform)["advertisingChannelType"]; got != "PERFORMANCE_MAX" {
		t.Fatalf("channel_type was ignored, got %v", got)
	}

	if result, _ := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID,
		map[string]any{"channel_type": "carrier_pigeon"})); result.(map[string]any)["isError"] != true {
		t.Fatalf("an unknown channel_type was accepted: %#v", result)
	}
}

func TestCampaignCreateRejectsConflictingNativeChannelType(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponses["budget_mutate"] = executeJSON(
		`{"results":[{"resourceName":"customers/1234567890/campaignBudgets/55"}]}`)
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	// One of the two values would otherwise be silently discarded.
	result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID, map[string]any{
		"channel_type": "search",
		"platform_options": map[string]any{
			"campaign": map[string]any{"advertisingChannelType": "DISPLAY"},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["isError"] != true {
		t.Fatalf("conflicting channel types were accepted: %#v", result)
	}

	// The escape hatch still works on its own.
	if result, _ := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID, map[string]any{
		"platform_options": map[string]any{
			"campaign": map[string]any{"advertisingChannelType": "DISPLAY"},
		},
	})); result.(map[string]any)["isError"] == true {
		t.Fatalf("native override alone should be allowed: %#v", result)
	}
	if got := createdGoogleCampaign(t, platform)["advertisingChannelType"]; got != "DISPLAY" {
		t.Fatalf("native override was lost, got %v", got)
	}
}

func TestGoogleCampaignCreateIgnoresFundingSourceResource(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponses["budget_mutate"] = executeJSON(
		`{"results":[{"resourceName":"customers/1234567890/campaignBudgets/55"}]}`)
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	// funding_source_resource_id is an X/Reddit concept. Google bills the
	// account, exposes no funding_source resource kind, and must not leak the
	// local id upstream.
	result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID,
		map[string]any{"funding_source_resource_id": 999}))
	if err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("campaign_create failed: %#v", result)
	}
	campaign := createdGoogleCampaign(t, platform)
	if _, leaked := campaign["funding_source_resource_id"]; leaked {
		t.Fatalf("local resource id leaked upstream: %#v", campaign)
	}
}

// ── Ad groups ──────────────────────────────────────────────────────────────

func TestGoogleAdSetCreateNeedsNoMetaFields(t *testing.T) {
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	// targeting and optimization_goal are Meta concepts; a Google ad group
	// should not require the caller to invent values the adapter discards.
	result, err := app.toolAdSetCreate(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": "987",
		"name": "Brand terms", "bid_amount_cents": 120,
	})
	if err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("google adset_create failed: result=%#v err=%v", result, err)
	}
	create := asMap(asMap(findExecuteCall(t, platform, "ad_group_mutate").
		Input["operations"].([]any)[0])["create"])
	if create["campaign"] != "customers/1234567890/campaigns/987" || create["name"] != "Brand terms" {
		t.Fatalf("unexpected ad group payload: %#v", create)
	}
	if create["cpcBidMicros"] != "1200000" {
		t.Fatalf("bid was not converted to micros: %#v", create)
	}
	for _, metaField := range []string{"targeting", "optimization_goal", "billing_event"} {
		if _, leaked := create[metaField]; leaked {
			t.Fatalf("Meta field %s leaked into the Google ad group: %#v", metaField, create)
		}
	}
}

func TestMetaAdSetCreateStillRequiresTargetingAndGoal(t *testing.T) {
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")

	// Relaxing the shared schema must not relax Meta's own contract.
	result, err := app.toolAdSetCreate(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": "987", "name": "Prospecting",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["isError"] != true {
		t.Fatalf("Meta ad set was created without targeting or optimization_goal: %#v", result)
	}
}

func TestGoogleCampaignObjectiveSelectsBiddingStrategy(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponses["budget_mutate"] = executeJSON(
		`{"results":[{"resourceName":"customers/1234567890/campaignBudgets/55"}]}`)
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	// Every name here must be a real field in Campaign's campaign_bidding_strategy
	// oneof. v0.1.47 sent "maximizeClicks", which does not exist — Maximize
	// Clicks is TargetSpend — and Google rejected the whole mutate.
	for objective, want := range map[string]string{
		"sales":     "maximizeConversionValue",
		"leads":     "maximizeConversions",
		"traffic":   "targetSpend",
		"awareness": "targetImpressionShare",
	} {
		result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID,
			map[string]any{"objective": objective}))
		if err != nil || result.(map[string]any)["isError"] == true {
			t.Fatalf("objective %s was rejected: %#v", objective, result)
		}
		campaign := createdGoogleCampaign(t, platform)
		if _, ok := campaign[want]; !ok {
			t.Fatalf("objective %s should bid with %s: %#v", objective, want, campaign)
		}
	}

	// A native strategy must win rather than being fought over.
	result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID, map[string]any{
		"objective": "sales",
		"platform_options": map[string]any{
			"campaign": map[string]any{"targetSpend": map[string]any{}},
		},
	}))
	if err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("native bidding strategy was rejected: %#v", result)
	}
	campaign := createdGoogleCampaign(t, platform)
	if _, ok := campaign["maximizeConversions"]; ok {
		t.Fatalf("mapped strategy overrode the native one: %#v", campaign)
	}
	if _, ok := campaign["targetSpend"]; !ok {
		t.Fatalf("native bidding strategy was dropped: %#v", campaign)
	}
}
