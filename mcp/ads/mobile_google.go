package main

import (
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func (a *App) googleAppCampaignCreate(ctx *sdk.AppCtx, acct *adAccount, args map[string]any) (any, error) {
	app, _ := args["_mobile_app"].(*adResource)
	if app == nil {
		return mcpError("channel_type=app requires mobile_app_resource_id"), nil
	}
	name := strings.TrimSpace(stringArgAny(args, "name"))
	cents := intArg(args, "daily_budget_cents", 0)
	if name == "" || cents <= 0 {
		return mcpError("app campaign requires name and positive daily_budget_cents"), nil
	}
	if intArg(args, "lifetime_budget_cents", 0) > 0 || boolArgDefault(args, "shared_budget", false) {
		return mcpError("Google App campaigns require an unshared daily budget"), nil
	}
	if opts := asMap(args["platform_options"]); len(opts) > 0 {
		return mcpError("Google App campaign configuration is derived from selected resources; native campaign overrides are not supported on this path"), nil
	}
	targeting, err := normalizedCampaignTargeting(args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	goal := firstString(args, "app_goal")
	strategy := firstString(args, "bid_strategy")
	target := intArg(args, "target_cpa_cents", 0)
	roas := floatArg(args, "target_roas")
	if value, exists := args["target_cpa_cents"]; exists && value != nil && target <= 0 {
		return mcpError("target_cpa_cents must be positive"), nil
	}
	if value, exists := args["target_roas"]; exists && value != nil && roas <= 0 {
		return mcpError("target_roas must be positive"), nil
	}
	if goal != "value" && roas > 0 {
		return mcpError("target_roas requires app_goal=value"), nil
	}
	if goal == "value" && target > 0 {
		return mcpError("value goal uses target_roas, not target_cpa_cents"), nil
	}
	if (strategy == "maximize_conversions" && target > 0) || (strategy == "maximize_conversion_value" && roas > 0) {
		return mcpError("explicit maximize strategy conflicts with a target bid"), nil
	}

	campaign := map[string]any{"resourceName": googleCampaignResource(acct.NativeAccountID, "-2"), "name": name, "status": "PAUSED", "advertisingChannelType": "MULTI_CHANNEL", "advertisingChannelSubType": "APP_CAMPAIGN", "campaignBudget": "customers/" + acct.NativeAccountID + "/campaignBudgets/-1", "containsEuPoliticalAdvertising": "DOES_NOT_CONTAIN_EU_POLITICAL_ADVERTISING"}
	store := "GOOGLE_APP_STORE"
	if firstString(app.Metadata, "os") == "ios" {
		store = "APPLE_APP_STORE"
	}
	setting := map[string]any{"appId": firstString(app.Metadata, "store_id"), "appStore": store}
	event, _ := args["_mobile_event"].(*adResource)
	switch goal {
	case "installs":
		if strategy != "" && strategy != "target_cpa" && strategy != "maximize_conversions" {
			return mcpError("install goal supports target_cpa or maximize_conversions"), nil
		}
		if target > 0 {
			setting["biddingStrategyGoalType"] = "OPTIMIZE_INSTALLS_TARGET_INSTALL_COST"
			campaign["targetCpa"] = map[string]any{"targetCpaMicros": strconv.FormatInt(int64(target)*10000, 10)}
		} else {
			if strategy == "target_cpa" {
				return mcpError("target_cpa requires target_cpa_cents"), nil
			}
			setting["biddingStrategyGoalType"] = "OPTIMIZE_INSTALLS_WITHOUT_TARGET_INSTALL_COST"
			campaign["maximizeConversions"] = map[string]any{}
		}
	case "in_app_event":
		if strategy != "" && strategy != "target_cpa" && strategy != "maximize_conversions" {
			return mcpError("event goal supports target_cpa or maximize_conversions"), nil
		}
		if target > 0 {
			setting["biddingStrategyGoalType"] = "OPTIMIZE_IN_APP_CONVERSIONS_TARGET_CONVERSION_COST"
			campaign["targetCpa"] = map[string]any{"targetCpaMicros": strconv.FormatInt(int64(target)*10000, 10)}
		} else {
			if strategy == "target_cpa" {
				return mcpError("target_cpa requires target_cpa_cents"), nil
			}
			setting["biddingStrategyGoalType"] = "OPTIMIZE_IN_APP_CONVERSIONS_WITHOUT_TARGET_CPA"
			campaign["maximizeConversions"] = map[string]any{}
		}
	case "value":
		if event == nil || !strings.Contains(firstString(event.Metadata, "type"), "FIREBASE") {
			return mcpError("Google App value optimization requires a Firebase conversion event"), nil
		}
		if strategy != "" && strategy != "target_roas" && strategy != "maximize_conversion_value" {
			return mcpError("value goal supports target_roas or maximize_conversion_value"), nil
		}
		if roas > 0 {
			setting["biddingStrategyGoalType"] = "OPTIMIZE_RETURN_ON_ADVERTISING_SPEND"
			campaign["targetRoas"] = map[string]any{"targetRoas": roas}
		} else {
			if strategy == "target_roas" {
				return mcpError("target_roas requires a positive target_roas"), nil
			}
			setting["biddingStrategyGoalType"] = "OPTIMIZE_TOTAL_VALUE_WITHOUT_TARGET_ROAS"
			campaign["maximizeConversionValue"] = map[string]any{}
		}
	default:
		return mcpError("unsupported Google App campaign goal"), nil
	}
	if event != nil {
		owner := firstString(event.Metadata, "owner_customer")
		if owner == "" {
			owner = "customers/" + acct.NativeAccountID
		}
		if !strings.HasPrefix(owner, "customers/") || !asciiDigits(strings.TrimPrefix(owner, "customers/")) || !asciiDigits(event.NativeID) {
			return mcpError("invalid mobile conversion action ownership"), nil
		}
		campaign["selectiveOptimization"] = map[string]any{"conversionActions": []any{owner + "/conversionActions/" + event.NativeID}}
	}
	campaign["appCampaignSetting"] = setting
	for _, k := range []string{"start_date", "end_date"} {
		if value := firstString(args, k); value != "" {
			if _, err := time.Parse("20060102", googleDate(value)); err != nil {
				return mcpError("invalid " + k), nil
			}
			field := "startDate"
			if k == "end_date" {
				field = "endDate"
			}
			campaign[field] = googleDate(value)
		}
	}
	budget := map[string]any{"resourceName": "customers/" + acct.NativeAccountID + "/campaignBudgets/-1", "name": name + " Budget", "amountMicros": strconv.FormatInt(int64(cents)*10000, 10), "explicitlyShared": false, "deliveryMethod": "STANDARD"}
	ops := []any{map[string]any{"campaignBudgetOperation": map[string]any{"create": budget}}, map[string]any{"campaignOperation": map[string]any{"create": campaign}}}
	// All targeting goes in the same atomic request. Invalid criteria cannot leave
	// an orphan budget or a live, globally-targeted campaign behind.
	for _, item := range targeting.operations(acct.NativeAccountID, "-2") {
		ops = append(ops, map[string]any{"campaignCriterionOperation": item})
	}
	parsed, out := a.execIntegrationTool(ctx, acct, "bulk_mutate", map[string]any{"customer_id": acct.NativeAccountID, "mutateOperations": ops, "partialFailure": false})
	if out != nil {
		return out, nil
	}
	responses, _ := asMap(parsed)["mutateOperationResponses"].([]any)
	var id, budgetName string
	for _, raw := range responses {
		v := asMap(raw)
		if name := firstString(asMap(v["campaignBudgetResult"]), "resourceName"); name != "" {
			budgetName = name
		}
		r := firstString(asMap(v["campaignResult"]), "resourceName")
		if r != "" {
			id = lastResourceSegment(r)
		}
	}
	if id == "" {
		out := mcpError("Google returned no campaign id after atomic app campaign creation; do not retry with a new idempotency key until reconciled")
		out["code"] = "creation_outcome_unknown"
		return out, nil
	}
	responseCampaign := cloneMap(campaign)
	responseCampaign["resourceName"] = googleCampaignResource(acct.NativeAccountID, id)
	responseCampaign["id"] = id
	if budgetName != "" {
		responseCampaign["campaignBudget"] = budgetName
	} else {
		delete(responseCampaign, "campaignBudget")
	}
	return map[string]any{"id": id, "campaign_id": id, "campaign": responseCampaign, "provider_result": parsed, "targets_everywhere": len(targeting.Locations) == 0}, nil
}

func googleAppAd(args map[string]any, customer string) (map[string]any, error) {
	info := map[string]any{}
	for _, field := range []string{"headlines", "descriptions"} {
		raw, ok := args[field].([]any)
		if !ok || len(raw) < 1 || len(raw) > 5 {
			return nil, fmt.Errorf("app ad %s requires 1–5 text assets", field)
		}
		limit := 30
		if field == "descriptions" {
			limit = 90
		}
		items := []any{}
		for _, v := range raw {
			text := strings.TrimSpace(toString(v))
			if m := asMap(v); len(m) > 0 {
				text = firstString(m, "text")
			}
			if text == "" || utf8.RuneCountInString(text) > limit {
				return nil, fmt.Errorf("app ad %s must contain text of at most %d characters", field, limit)
			}
			items = append(items, map[string]any{"text": text})
		}
		info[field] = items
	}
	for _, spec := range []struct{ input, output string }{{"image_asset_ids", "images"}, {"video_asset_ids", "youtubeVideos"}} {
		if args[spec.input] == nil {
			continue
		}
		raw, ok := args[spec.input].([]any)
		if !ok || len(raw) > 20 {
			return nil, fmt.Errorf("%s must be an array of at most 20 assets", spec.input)
		}
		items := []any{}
		for _, v := range raw {
			id := toString(v)
			if strings.HasPrefix(id, "customers/"+customer+"/assets/") {
				id = lastResourceSegment(id)
			}
			if !asciiDigits(id) {
				return nil, fmt.Errorf("asset must be a numeric id in the selected customer")
			}
			items = append(items, map[string]any{"asset": "customers/" + customer + "/assets/" + id})
		}
		info[spec.output] = items
	}
	ad := map[string]any{"appAd": info}
	if name := firstString(args, "name"); name != "" {
		ad["name"] = name
	}
	return ad, nil
}
func (a *App) googleCampaignIsApp(ctx *sdk.AppCtx, acct *adAccount, campaignID string, app *adResource) (bool, map[string]any) {
	if !asciiDigits(campaignID) {
		return false, mcpError("google campaign_id must be numeric")
	}
	rows, out := a.googleResourceRows(ctx, acct, "SELECT campaign.id, campaign.advertising_channel_type, campaign.advertising_channel_sub_type, campaign.app_campaign_setting.app_id FROM campaign WHERE campaign.id = "+campaignID)
	if out != nil {
		return false, out
	}
	if len(rows) != 1 {
		return false, mcpError("parent campaign not found in selected customer")
	}
	v := mapAt(rows[0], "campaign")
	setting := asMap(v["appCampaignSetting"])
	if len(setting) == 0 {
		setting = asMap(v["app_campaign_setting"])
	}
	if app == nil || firstString(setting, "appId", "app_id") != firstString(app.Metadata, "store_id") {
		return false, mcpError("parent campaign advertises a different app")
	}
	return firstString(v, "advertisingChannelType", "advertising_channel_type") == "MULTI_CHANNEL" && firstString(v, "advertisingChannelSubType", "advertising_channel_sub_type") == "APP_CAMPAIGN", nil
}

func lastResourceSegment(value string) string {
	parts := strings.Split(value, "/")
	return parts[len(parts)-1]
}
func (a *App) googleConversionMetadata(ctx *sdk.AppCtx, acct *adAccount, v map[string]any) map[string]any {
	typ := firstString(v, "type")
	appID := firstString(v, "appId", "app_id")
	category := firstString(v, "category")
	metadata := map[string]any{"type": typ, "category": category, "app_id": appID, "owner_customer": firstString(v, "ownerCustomer", "owner_customer"), "eligible": strings.EqualFold(firstString(v, "status"), "ENABLED"), "observation": "unknown"}
	if primary, ok := v["primaryForGoal"].(bool); ok && !primary {
		metadata["eligible"] = false
	}
	if primary, ok := v["primary_for_goal"].(bool); ok && !primary {
		metadata["eligible"] = false
	}
	if appID != "" && googleMobileConversionType(typ) {
		metadata["event"] = googleConversionEvent(v)
		apps, err := a.listResources(ctx, acct, resourceMobileApp)
		if err == nil {
			for _, app := range apps {
				if firstString(app.Metadata, "app_id") == appID {
					metadata["mobile_app_resource_id"] = app.ID
					break
				}
			}
		}
	}
	return metadata
}
func (a *App) validateGoogleAppAdParent(ctx *sdk.AppCtx, acct *adAccount, groupID string, args map[string]any) map[string]any {
	if !asciiDigits(groupID) {
		return mcpError("google adset_id must be numeric")
	}
	rows, out := a.googleResourceRows(ctx, acct, "SELECT ad_group.id, campaign.id, campaign.advertising_channel_type, campaign.advertising_channel_sub_type, campaign.app_campaign_setting.app_id FROM ad_group WHERE ad_group.id = "+groupID)
	if out != nil {
		return out
	}
	if len(rows) != 1 {
		return mcpError("parent ad group not found in this account")
	}
	campaign := mapAt(rows[0], "campaign")
	if firstString(campaign, "advertisingChannelType", "advertising_channel_type") != "MULTI_CHANNEL" || firstString(campaign, "advertisingChannelSubType", "advertising_channel_sub_type") != "APP_CAMPAIGN" {
		return mcpError("ad_format=app requires an App campaign parent")
	}
	if app, _ := args["_mobile_app"].(*adResource); app != nil {
		setting := asMap(campaign["appCampaignSetting"])
		if len(setting) == 0 {
			setting = asMap(campaign["app_campaign_setting"])
		}
		if firstString(setting, "appId", "app_id") != firstString(app.Metadata, "store_id") {
			return mcpError("App ad parent promotes a different mobile app")
		}
	}
	return nil
}
