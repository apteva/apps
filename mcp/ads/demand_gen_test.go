package main

import (
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"testing"
)

func demandFixture(t *testing.T) (*App, *sdk.AppCtx, *recordingPlatform, int64) {
	pf := newRecordingPlatform()
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "bulk_mutate" {
			return executeJSON(`{"mutateOperationResponses":[{"campaignBudgetResult":{"resourceName":"customers/123/campaignBudgets/66"}},{"campaignResult":{"resourceName":"customers/123/campaigns/77"}},{"adGroupResult":{"resourceName":"customers/123/adGroups/88"}}]}`), nil
		}
		if tool == "ad_mutate" {
			return executeJSON(`{"results":[{"resourceName":"customers/123/adGroupAds/88~99"}]}`), nil
		}
		if tool == "asset_mutate" {
			return executeJSON(`{"results":[{"resourceName":"customers/123/assets/45"}]}`), nil
		}
		q := firstString(input, "query")
		switch {
		case strings.Contains(q, "FROM conversion_action"):
			return executeJSON(`{"results":[{"conversionAction":{"id":"10","name":"YouTube channel subscriptions","type":"UNKNOWN","origin":"YOUTUBE_HOSTED","category":"ENGAGEMENT","status":"ENABLED","ownerCustomer":"customers/123","primaryForGoal":true}},{"conversionAction":{"id":"11","name":"YouTube follow-on views","type":"UNKNOWN","origin":"YOUTUBE_HOSTED","category":"YOUTUBE_FOLLOW_ON_VIEWS","status":"ENABLED","ownerCustomer":"customers/123","primaryForGoal":true}}]}`), nil
		case strings.Contains(q, "FROM customer_conversion_goal"):
			return executeJSON(`{"results":[{"customerConversionGoal":{"resourceName":"customers/123/customerConversionGoals/26~6","category":"ENGAGEMENT","origin":"YOUTUBE_HOSTED"}}]}`), nil
		case strings.Contains(q, "FROM campaign_conversion_goal"):
			return executeJSON(`{"results":[{"campaignConversionGoal":{"resourceName":"customers/123/campaignConversionGoals/77~26~6","biddable":false}}]}`), nil
		case strings.Contains(q, "FROM conversion_goal_campaign_config"):
			return executeJSON(`{"results":[{"conversionGoalCampaignConfig":{"goalConfigLevel":"CAMPAIGN","customConversionGoal":"customers/123/customConversionGoals/1234"}}]}`), nil
		case strings.Contains(q, "FROM custom_conversion_goal"):
			return executeJSON(`{"results":[{"customConversionGoal":{"resourceName":"customers/123/customConversionGoals/1234","status":"ENABLED","conversionActions":["customers/123/conversionActions/10"]}}]}`), nil
		case strings.Contains(q, "FROM asset"):
			return executeJSON(`{"results":[{"asset":{"id":"1","type":"YOUTUBE_VIDEO"}},{"asset":{"id":"2","type":"IMAGE","imageAsset":{"fullSize":{"widthPixels":"600","heightPixels":"600"}}}},{"asset":{"id":"3","type":"IMAGE","imageAsset":{"fullSize":{"widthPixels":"1200","heightPixels":"628"}}}},{"asset":{"id":"4","type":"DEMAND_GEN_CAROUSEL_CARD","demandGenCarouselCardAsset":{"squareMarketingImageAsset":"customers/123/assets/2"}}},{"asset":{"id":"5","type":"DEMAND_GEN_CAROUSEL_CARD","demandGenCarouselCardAsset":{"squareMarketingImageAsset":"customers/123/assets/3"}}}]}`), nil
		case strings.Contains(q, "FROM ad_group_criterion"):
			return executeJSON(`{"results":[{"adGroupCriterion":{"resourceName":"customers/123/adGroupCriteria/88~1","type":"LANGUAGE","language":{"languageConstant":"languageConstants/1000"}}},{"adGroupCriterion":{"resourceName":"customers/123/adGroupCriteria/88~2","type":"LOCATION","negative":true,"location":{"geoTargetConstant":"geoTargetConstants/2840"}}}]}`), nil
		case strings.Contains(q, "FROM ad_group"):
			return executeJSON(`{"results":[{"adGroup":{"id":"88","name":"Group","status":"PAUSED","demandGenAdGroupSettings":{"channelControls":{"selectedChannels":{"youtubeInFeed":true}}}},"campaign":{"id":"77","advertisingChannelType":"DEMAND_GEN"}}]}`), nil
		case strings.Contains(q, "FROM campaign"):
			return executeJSON(`{"results":[{"campaign":{"id":"77","name":"Demand Gen","advertisingChannelType":"DEMAND_GEN","status":"PAUSED","campaignBudget":"customers/123/campaignBudgets/66"},"campaignBudget":{"amountMicros":"5000000","period":"DAILY"}}]}`), nil
		default:
			return executeJSON(`{"results":[]}`), nil
		}
	}
	ctx := newAdsCtx(t, pf)
	a := &App{}
	id := seedResourceTestAccount(t, ctx, "google", "123")
	return a, ctx, pf, id
}
func demandCampaignArgs(id int64) map[string]any {
	return map[string]any{"ad_account_id": id, "name": "Channel growth", "channel_type": "demand_gen", "objective": "engagement", "optimization_goal": "channel_subscriptions", "daily_budget_cents": 500, "locations": []any{"2724"}, "languages": []any{"1000"}, "status": "PAUSED", "idempotency_key": "campaign"}
}
func demandAdArgs(id int64) map[string]any {
	return map[string]any{"ad_account_id": id, "adset_id": "88", "name": "Video", "ad_format": "responsive_video", "business_name": "Example", "headlines": []any{map[string]any{"text": "One headline is valid"}}, "long_headlines": []any{map[string]any{"text": "Explore more videos on our channel"}}, "descriptions": []any{map[string]any{"text": "Watch our latest videos"}}, "logo_asset_ids": []any{"2"}, "video_asset_ids": []any{"1"}, "final_urls": []any{"https://www.youtube.com/@example"}, "status": "PAUSED"}
}
func TestDemandGenStructuredHierarchyAndReplay(t *testing.T) {
	a, ctx, pf, id := demandFixture(t)
	args := demandCampaignArgs(id)
	result, err := a.toolCampaignCreate(ctx, args)
	requireOK(t, result, err)
	ops := asSlice(findExecuteCall(t, pf, "bulk_mutate").Input["mutateOperations"])
	if len(ops) != 4 {
		t.Fatalf("should reuse existing exact custom goal: %#v", ops)
	}
	for _, op := range ops {
		if asMap(op)["customConversionGoalOperation"] != nil || asMap(op)["campaignCriterionOperation"] != nil {
			t.Fatal("recreated custom goal or incorrectly put targeting on campaign")
		}
	}
	before := len(pf.executeCalls)
	result, err = a.toolCampaignCreate(ctx, args)
	requireOK(t, result, err)
	if len(pf.executeCalls) != before {
		t.Fatal("replay reached provider")
	}
	_, err = a.campaignSettings(ctx, &adAccount{ID: id, ProjectID: "test-proj"}, "77")
	if err != nil {
		t.Fatal(err)
	}
	group, err := a.toolAdSetCreate(ctx, map[string]any{"ad_account_id": id, "campaign_id": "77", "name": "Group", "placements": []any{"youtube_in_feed", "youtube_shorts"}})
	requireOK(t, group, err)
	var groupOps []any
	for _, c := range pf.executeCalls {
		if c.Tool == "bulk_mutate" {
			if op := asMap(asMap(asSlice(c.Input["mutateOperations"])[0])["adGroupOperation"]); len(op) > 0 {
				groupOps = asSlice(c.Input["mutateOperations"])
			}
		}
	}
	if len(groupOps) != 3 {
		t.Fatalf("campaign location/language defaults not inherited: %#v", groupOps)
	}
	for _, op := range groupOps[1:] {
		c := asMap(asMap(asMap(op)["adGroupCriterionOperation"])["create"])
		if c["campaign"] != nil || c["adGroup"] != "customers/123/adGroups/-1" {
			t.Fatalf("bad criteria parent %#v", c)
		}
	}
	ad, err := a.toolAdCreate(ctx, demandAdArgs(id))
	requireOK(t, ad, err)
	adop := asMap(asMap(asSlice(findExecuteCall(t, pf, "ad_mutate").Input["operations"])[0])["create"])
	if mapAt(mapAt(adop, "ad"), "demandGenVideoResponsiveAd")["videos"] == nil {
		t.Fatal("missing structured video payload")
	}
	ready, err := a.toolConversionReadiness(ctx, map[string]any{"ad_account_id": id, "campaign_id": "77"})
	requireOK(t, ready, err)
	if asMap(ready)["status"] != "unknown" {
		t.Fatal("unverified channel linking marked ready")
	}
	acct, _, _ := a.resolveAdAccount(ctx, map[string]any{"ad_account_id": id})
	if a.checkDemandGenActivation(ctx, acct, map[string]any{}, "77") == nil {
		t.Fatal("missing channel verification bypassed")
	}
	if a.checkDemandGenActivation(ctx, acct, map[string]any{"acknowledge_unverified_measurement": true}, "77") != nil {
		t.Fatal("explicit external verification rejected")
	}
}
func TestDemandGenValidationDoesNotCreateResources(t *testing.T) {
	for name, override := range map[string]map[string]any{
		"shared": {"shared_budget": true}, "two budgets": {"lifetime_budget_cents": 5000}, "no budget": {"daily_budget_cents": 0}, "bid mismatch": {"bid_strategy": "maximize_clicks"}, "bad date": {"start_time": "2026-02-30"}, "invalid location": {"locations": []any{"invalid"}}, "native conflict": {"platform_options": map[string]any{"campaign": map[string]any{"status": "ENABLED"}}}, "active channel": {"status": "ACTIVE"},
	} {
		t.Run(name, func(t *testing.T) {
			a, ctx, pf, id := demandFixture(t)
			args := demandCampaignArgs(id)
			for k, v := range override {
				args[k] = v
			}
			r, err := a.toolCampaignCreate(ctx, args)
			if err != nil || mcpResultError(r) == nil {
				t.Fatalf("invalid combination accepted %#v %v", r, err)
			}
			for _, call := range pf.executeCalls {
				if integrationToolMutates(call.Tool) {
					t.Fatalf("invalid combination mutated %#v", call)
				}
			}
		})
	}
}
func TestDemandGenFormatsAndSearchConstraints(t *testing.T) {
	args := demandAdArgs(1)
	for _, format := range []string{"responsive_video", "responsive_image", "carousel"} {
		copy := cloneMap(args)
		copy["ad_format"] = format
		copy["image_asset_ids"] = []any{"3"}
		copy["card_asset_ids"] = []any{"4", "5"}
		ad, err := googleDemandGenAd(copy, "123")
		if err != nil || ad[demandGenFormats[format]] == nil {
			t.Fatalf("%s: %#v %v", format, ad, err)
		}
	}
	if _, err := googleResponsiveSearchAd(args); err == nil {
		t.Fatal("Search constraints relaxed by Demand Gen text schema")
	}
	for _, field := range []string{"video_asset_ids", "logo_asset_ids"} {
		copy := cloneMap(args)
		copy[field] = []any{"customers/999/assets/1"}
		if _, err := googleDemandGenAd(copy, "123"); err == nil {
			t.Fatal("cross-account asset accepted")
		}
	}
	copy := cloneMap(args)
	copy["headlines"] = []any{map[string]any{"text": strings.Repeat("漢", 21)}}
	if _, err := googleDemandGenAd(copy, "123"); err == nil {
		t.Fatal("double-width headline accepted")
	}
}
func TestDemandGenTargetingUpdateReplacesSuppliedCategoryOnly(t *testing.T) {
	a, ctx, pf, id := demandFixture(t)
	result, err := a.toolAdSetUpdate(ctx, map[string]any{"ad_account_id": id, "adset_id": "88", "placements": []any{"youtube_in_feed"}, "languages": []any{"1003"}})
	requireOK(t, result, err)
	ops := asSlice(findExecuteCall(t, pf, "bulk_mutate").Input["mutateOperations"])
	raw, _ := json.Marshal(ops)
	if strings.Contains(string(raw), "88~2") {
		t.Fatal("language replacement removed an unrelated location exclusion")
	}
	if !strings.Contains(string(raw), "selected_channels.youtube_shorts") {
		t.Fatal("placement mask must explicitly clear prior channels")
	}
}
func TestDemandGenGoalsAreCampaignScopedAndOriginTyped(t *testing.T) {
	if googleHostedEvent(map[string]any{"origin": "APP", "category": "ENGAGEMENT"}) != "" {
		t.Fatal("app event treated as a channel subscription")
	}
	if googleHostedEvent(map[string]any{"origin": "YOUTUBE_HOSTED", "category": "ENGAGEMENT", "type": "UNKNOWN"}) != "channel_subscriptions" {
		t.Fatal("v23 unknown type lost YouTube origin")
	}
	a, ctx, _, id := demandFixture(t)
	r, err := a.toolCampaignCreate(ctx, demandCampaignArgs(id))
	requireOK(t, r, err)
	other := seedResourceTestAccount(t, ctx, "google", "999")
	rows, err := a.listResources(ctx, &adAccount{ID: id, ProjectID: "test-proj"}, resourceConversionAction)
	if err != nil || len(rows) == 0 {
		t.Fatal("missing conversion fixtures")
	}
	args := demandCampaignArgs(other)
	args["conversion_action_resource_ids"] = []any{rows[0].ID}
	args["idempotency_key"] = "cross-resource"
	r, err = a.toolCampaignCreate(ctx, args)
	if err != nil || mcpResultError(r) == nil {
		t.Fatalf("cross-account conversion resource accepted: %#v %v", r, err)
	}
}
