package main

import (
	"encoding/json"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func budgetOKPlatform() *recordingPlatform {
	pf := newRecordingPlatform()
	pf.executeResponses["budget_mutate"] = executeJSON(
		`{"results":[{"resourceName":"customers/1234567890/campaignBudgets/55"}]}`)
	pf.executeResponses["campaign_mutate"] = executeJSON(
		`{"results":[{"resourceName":"customers/1234567890/campaigns/987"}]}`)
	return pf
}

func createdBudget(t *testing.T, pf *recordingPlatform) map[string]any {
	t.Helper()
	for _, call := range pf.executeCalls {
		if call.Tool != "budget_mutate" {
			continue
		}
		ops, _ := call.Input["operations"].([]any)
		if len(ops) > 0 {
			if create := asMap(asMap(ops[0])["create"]); len(create) > 0 {
				return create
			}
		}
	}
	t.Fatalf("no budget create was issued: %#v", pf.executeCalls)
	return nil
}

// ── EU political advertising (required since Google Ads API v23) ───────────

func TestCampaignCreateAlwaysDeclaresEUPoliticalAdvertising(t *testing.T) {
	pf := budgetOKPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	// Omitting the field fails the mutate with fieldError REQUIRED, so it is
	// populated on every create rather than only when asked for.
	if result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID, nil)); err != nil ||
		result.(map[string]any)["isError"] == true {
		t.Fatalf("campaign_create failed: %#v", result)
	}
	campaign := createdGoogleCampaign(t, pf)
	if campaign["containsEuPoliticalAdvertising"] != "DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING" {
		t.Fatalf("EU political declaration missing or wrong: %#v", campaign)
	}

	if result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID,
		map[string]any{"contains_eu_political_advertising": true})); err != nil ||
		result.(map[string]any)["isError"] == true {
		t.Fatalf("campaign_create failed: %#v", result)
	}
	if got := createdGoogleCampaign(t, pf)["containsEuPoliticalAdvertising"]; got != "CONTAINS_EU_POLITICAL_ADVERTISING" {
		t.Fatalf("opt-in declaration not honoured: %v", got)
	}
}

// ── Shared budget vs inline bidding strategy ───────────────────────────────

func TestCampaignBudgetIsNotSharedByDefault(t *testing.T) {
	pf := budgetOKPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	if result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID, nil)); err != nil ||
		result.(map[string]any)["isError"] == true {
		t.Fatalf("campaign_create failed: %#v", result)
	}
	// An explicitly shared budget is incompatible with the inline campaign-level
	// strategy this path sets (BIDDING_STRATEGY_TYPE_INCOMPATIBLE_WITH_SHARED_BUDGET).
	budget := createdBudget(t, pf)
	if budget["explicitlyShared"] != false {
		t.Fatalf("budget should not be explicitly shared by default: %#v", budget)
	}
}

func TestSharedBudgetRequiresPortfolioStrategy(t *testing.T) {
	pf := budgetOKPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID,
		map[string]any{"shared_budget": true}))
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["isError"] != true {
		t.Fatalf("shared budget with an inline strategy was accepted: %#v", result)
	}
	// Refused before anything is created, so no budget is stranded.
	if len(pf.executeCalls) != 0 {
		t.Fatalf("an invalid combination reached the provider: %#v", pf.executeCalls)
	}

	// With a portfolio strategy the combination is valid.
	result, err = app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID, map[string]any{
		"shared_budget": true,
		"platform_options": map[string]any{
			"campaign": map[string]any{"biddingStrategy": "customers/1234567890/biddingStrategies/77"},
		},
	}))
	if err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("portfolio strategy with a shared budget was rejected: %#v", result)
	}
	if createdBudget(t, pf)["explicitlyShared"] != true {
		t.Fatalf("shared_budget opt-in was not applied: %#v", createdBudget(t, pf))
	}
	campaign := createdGoogleCampaign(t, pf)
	for _, inline := range []string{"maximizeConversions", "maximizeConversionValue", "targetSpend"} {
		if _, set := campaign[inline]; set {
			t.Fatalf("inline strategy %s sent alongside a shared budget: %#v", inline, campaign)
		}
	}
}

// ── Bidding strategy mapping ───────────────────────────────────────────────

func TestBiddingStrategyFieldsExistInTheOneof(t *testing.T) {
	// Every value the app can emit must be a real Campaign.campaign_bidding_strategy
	// field. v0.1.47 emitted "maximizeClicks", which is not one.
	valid := map[string]bool{
		"manualCpc": true, "manualCpm": true, "manualCpv": true,
		"maximizeConversions": true, "maximizeConversionValue": true,
		"targetCpa": true, "targetCpm": true, "targetImpressionShare": true,
		"targetRoas": true, "targetSpend": true, "percentCpc": true, "commission": true,
	}
	for objective, field := range googleObjectiveBidding {
		if !valid[field] {
			t.Errorf("objective %q maps to %q, which is not a Campaign bidding field", objective, field)
		}
	}
	for name, field := range googleBidStrategyNames {
		if !valid[field] {
			t.Errorf("bid_strategy %q maps to %q, which is not a Campaign bidding field", name, field)
		}
	}
	if googleBidStrategyNames["maximize_clicks"] != "targetSpend" {
		t.Error("Maximize Clicks is TargetSpend in the Google Ads API")
	}
}

func TestObjectiveBiddingAcrossAllSixObjectives(t *testing.T) {
	pf := budgetOKPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	supported := map[string]string{
		"sales":     "maximizeConversionValue",
		"leads":     "maximizeConversions",
		"traffic":   "targetSpend",
		"awareness": "targetImpressionShare",
	}
	for objective, want := range supported {
		result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID,
			map[string]any{"objective": objective}))
		if err != nil || result.(map[string]any)["isError"] == true {
			t.Fatalf("objective %s rejected: %#v", objective, result)
		}
		if _, ok := createdGoogleCampaign(t, pf)[want]; !ok {
			t.Fatalf("objective %s should bid with %s: %#v", objective, want, createdGoogleCampaign(t, pf))
		}
	}

	// The remaining two have no Search equivalent and must fail loudly rather
	// than emitting a field Google does not know.
	for _, objective := range []string{"engagement", "app_promotion"} {
		before := len(pf.executeCalls)
		result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID,
			map[string]any{"objective": objective}))
		if err != nil {
			t.Fatal(err)
		}
		if result.(map[string]any)["isError"] != true {
			t.Fatalf("objective %s should be rejected on Search: %#v", objective, result)
		}
		if len(pf.executeCalls) != before {
			t.Fatalf("rejected objective %s still called the provider", objective)
		}
	}
}

func TestTargetImpressionShareCarriesRequiredSubfields(t *testing.T) {
	pf := budgetOKPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	if result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID, map[string]any{
		"objective": "awareness", "impression_share_percent": 80.0,
		"impression_share_location": "TOP_OF_PAGE", "cpc_bid_ceiling_cents": 90,
	})); err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("awareness campaign failed: %#v", result)
	}
	share := asMap(createdGoogleCampaign(t, pf)["targetImpressionShare"])
	// location and locationFractionMicros are required by the API; an empty
	// strategy body would be rejected.
	if share["location"] != "TOP_OF_PAGE" {
		t.Fatalf("impression share location not applied: %#v", share)
	}
	if share["locationFractionMicros"] != "800000" {
		t.Fatalf("impression share percent not converted to micros: %#v", share)
	}
	if share["cpcBidCeilingMicros"] != "900000" {
		t.Fatalf("cpc ceiling not converted to micros: %#v", share)
	}
}

func TestExplicitGoogleBidStrategyOverridesObjective(t *testing.T) {
	pf := budgetOKPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	// Smart Bidding needs conversion history, so a new account must be able to
	// start on Maximize Clicks even with a sales objective.
	if result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID,
		map[string]any{"objective": "sales", "bid_strategy": "maximize_clicks", "cpc_bid_ceiling_cents": 65})); err != nil ||
		result.(map[string]any)["isError"] == true {
		t.Fatalf("explicit bid strategy rejected: %#v", result)
	}
	campaign := createdGoogleCampaign(t, pf)
	spend := asMap(campaign["targetSpend"])
	if spend == nil || spend["cpcBidCeilingMicros"] != "650000" {
		t.Fatalf("maximize_clicks should map to targetSpend with a ceiling: %#v", campaign)
	}
	if _, set := campaign["maximizeConversionValue"]; set {
		t.Fatalf("objective mapping should not also apply: %#v", campaign)
	}

	// Meta vocabulary must not silently do nothing on Google.
	result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID,
		map[string]any{"bid_strategy": "cost_cap"}))
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["isError"] != true {
		t.Fatalf("Meta bid_strategy was accepted on Google: %#v", result)
	}
}

func TestBiddingStrategyChannelCompatibility(t *testing.T) {
	pf := budgetOKPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	// Target impression share is a Search strategy; Performance Max takes only
	// the two conversion strategies.
	result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID,
		map[string]any{"channel_type": "performance_max", "objective": "awareness"}))
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["isError"] != true {
		t.Fatalf("awareness bidding was accepted for Performance Max: %#v", result)
	}
	if result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID,
		map[string]any{"channel_type": "performance_max", "objective": "sales"})); err != nil ||
		result.(map[string]any)["isError"] == true {
		t.Fatalf("Performance Max with a value objective should work: %#v", result)
	}
}

// ── Orphaned budgets ───────────────────────────────────────────────────────

func TestValidationFailuresNeverCreateABudget(t *testing.T) {
	pf := budgetOKPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	// Each of these used to be caught only after the budget mutate had already
	// run, stranding a budget per failed attempt.
	for _, extra := range []map[string]any{
		{"objective": "engagement"},
		{"bid_strategy": "cost_cap"},
		{"channel_type": "carrier_pigeon"},
		{"shared_budget": true},
	} {
		if result, _ := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID, extra)); result.(map[string]any)["isError"] != true {
			t.Fatalf("expected rejection for %#v", extra)
		}
	}
	if len(pf.executeCalls) != 0 {
		t.Fatalf("validation failures created provider state: %#v", pf.executeCalls)
	}
}

func TestOrphanedBudgetIsNamedWhenRollbackFails(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "budget_mutate" {
			ops, _ := input["operations"].([]any)
			if len(ops) > 0 && asMap(ops[0])["remove"] != nil {
				// Rollback itself fails: the budget survives.
				return &sdk.ExecuteResult{Success: false, Status: 400,
					Data: json.RawMessage(`{"error":"CANNOT_REMOVE"}`)}, nil
			}
			return executeJSON(`{"results":[{"resourceName":"customers/1234567890/campaignBudgets/55"}]}`), nil
		}
		return &sdk.ExecuteResult{Success: false, Status: 400,
			Data: json.RawMessage(`{"error":"INVALID_ARGUMENT"}`)}, nil
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID, nil))
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	if out["isError"] != true {
		t.Fatalf("campaign failure should surface: %#v", out)
	}
	if out["orphaned_budget"] != "customers/1234567890/campaignBudgets/55" {
		t.Fatalf("stranded budget was not named: %#v", out)
	}
}

func TestBudgetListFlagsOrphansAndDeleteRemovesThem(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["search"] = executeJSON(`{"results":[
		{"campaignBudget":{"id":"55","name":"Live Budget","amountMicros":"20000000","explicitlyShared":false,"status":"ENABLED","referenceCount":1}},
		{"campaignBudget":{"id":"56","name":"Stranded Budget","amountMicros":"20000000","explicitlyShared":false,"status":"ENABLED","referenceCount":0}}
	]}`)
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	result, err := app.toolBudgetList(ctx, map[string]any{"ad_account_id": accountID})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	if out["count"] != 2 || out["orphaned_count"] != 1 {
		t.Fatalf("orphan detection wrong: %#v", out)
	}
	budgets := out["budgets"].([]map[string]any)
	if budgets[0]["orphaned"] != false || budgets[1]["orphaned"] != true {
		t.Fatalf("reference_count not used for orphan detection: %#v", budgets)
	}

	result, _ = app.toolBudgetList(ctx, map[string]any{"ad_account_id": accountID, "only_orphaned": true})
	if result.(map[string]any)["count"] != 1 {
		t.Fatalf("only_orphaned filter failed: %#v", result)
	}

	if result, err := app.toolBudgetDelete(ctx, map[string]any{
		"ad_account_id": accountID, "budget_ids": []any{"56"},
	}); err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("budget_delete failed: %#v", result)
	}
	op := asMap(findExecuteCall(t, pf, "budget_mutate").Input["operations"].([]any)[0])
	if op["remove"] != "customers/1234567890/campaignBudgets/56" {
		t.Fatalf("wrong budget removed: %#v", op)
	}
}

// ── Chaining: create calls must return an id ───────────────────────────────

func TestCreateResultsCarryProviderID(t *testing.T) {
	pf := budgetOKPlatform()
	pf.executeResponses["ad_group_mutate"] = executeJSON(
		`{"results":[{"resourceName":"customers/1234567890/adGroups/456"}]}`)
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	// A caller chains campaign -> ad group -> ad, so each create has to hand
	// back an id rather than only a provider resource name.
	campResult, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID, nil))
	if err != nil {
		t.Fatal(err)
	}
	camp := campResult.(map[string]any)
	if camp["id"] != "987" {
		t.Fatalf("campaign_create did not surface an id: %#v", camp)
	}
	if asMap(camp["campaign"])["id"] != "987" {
		t.Fatalf("campaign payload did not surface an id: %#v", camp["campaign"])
	}

	agResult, err := app.toolAdSetCreate(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": "987", "name": "Ad group", "status": "PAUSED",
	})
	if err != nil {
		t.Fatal(err)
	}
	if asMap(agResult)["id"] != "456" {
		t.Fatalf("adset_create did not surface an id: %#v", agResult)
	}
}
