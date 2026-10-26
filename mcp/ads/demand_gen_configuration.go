package main

import (
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

func (a *App) toolCampaignConfiguration(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, out := a.resolveAdAccount(ctx, args)
	if out != nil {
		return out, nil
	}
	if acct.Platform != "google" {
		return mcpError("structured campaign configuration currently supports Google"), nil
	}
	id := firstString(args, "campaign_id")
	if !googleNumericID(id) {
		return mcpError("numeric campaign_id required"), nil
	}
	if out := a.requireManagedCampaign(ctx, acct, id); out != nil {
		return out, nil
	}
	rows, out := a.googleResourceRows(ctx, acct, "SELECT campaign.id, campaign.name, campaign.status, campaign.advertising_channel_type, campaign.start_date_time, campaign.end_date_time, campaign.bidding_strategy_type, campaign.maximize_conversions.target_cpa_micros, campaign.maximize_conversion_value.target_roas, campaign.campaign_budget, campaign_budget.amount_micros, campaign_budget.total_amount_micros, campaign_budget.period FROM campaign WHERE campaign.id = "+id)
	if out != nil {
		return out, nil
	}
	if len(rows) != 1 {
		return mcpError("campaign not found"), nil
	}
	cfg, err := a.campaignSettings(ctx, acct, id)
	if err != nil {
		return nil, err
	}
	campaign := mapAt(rows[0], "campaign")
	budget := mapAt(rows[0], "campaignBudget")
	if len(budget) == 0 {
		budget = mapAt(rows[0], "campaign_budget")
	}
	result := map[string]any{"campaign_id": id, "name": campaign["name"], "status": campaign["status"], "channel_type": strings.ToLower(firstString(campaign, "advertisingChannelType", "advertising_channel_type")), "bidding_strategy": campaign["biddingStrategyType"], "start_time": campaign["startDateTime"], "end_time": campaign["endDateTime"], "budget": budget, "settings": cfg, "native": rows[0]}
	if firstString(campaign, "advertisingChannelType", "advertising_channel_type") == "DEMAND_GEN" {
		goals, out := a.googleResourceRows(ctx, acct, "SELECT conversion_goal_campaign_config.campaign, conversion_goal_campaign_config.goal_config_level, conversion_goal_campaign_config.custom_conversion_goal FROM conversion_goal_campaign_config WHERE conversion_goal_campaign_config.campaign = 'customers/"+acct.NativeAccountID+"/campaigns/"+id+"'")
		if out != nil {
			return out, nil
		}
		result["conversion_goals"] = goals
	}
	return result, nil
}
func (a *App) googleDemandGenCampaignUpdate(ctx *sdk.AppCtx, acct *adAccount, args map[string]any) (any, error) {
	id := firstString(args, "campaign_id")
	if !googleNumericID(id) {
		return mcpError("numeric campaign_id required"), nil
	}
	cfg, err := a.campaignSettings(ctx, acct, id)
	if err != nil {
		return nil, err
	}
	merged := cloneMap(cfg)
	merged["channel_type"] = "demand_gen"
	for k, v := range args {
		merged[k] = v
	}
	if args["optimization_goal"] != nil && firstString(args, "optimization_goal") != firstString(cfg, "optimization_goal") && args["conversion_action_resource_ids"] == nil {
		delete(merged, "conversion_action_resource_ids")
	}
	goal := firstString(merged, "optimization_goal")
	if googleCampaignStatus(firstString(args, "status")) == "ENABLED" && firstString(args, "status") != "" && (goal == "channel_subscriptions" || goal == "follow_on_views") && !boolArg(args, "acknowledge_unverified_measurement") {
		return mcpError("verify the promoted channel link before enabling a YouTube engagement goal"), nil
	}

	for _, k := range []string{"locations", "excluded_locations", "languages"} {
		if args[k] != nil {
			return mcpError("Demand Gen targeting lives on ad groups; update it through adset_update"), nil
		}
	}
	if len(asMap(args["platform_options"])) > 0 {
		return mcpError("structured Demand Gen updates use normalized fields"), nil
	}
	rows, out := a.googleResourceRows(ctx, acct, "SELECT campaign.id, campaign.name, campaign.advertising_channel_type, campaign.bidding_strategy_type, campaign.campaign_budget FROM campaign WHERE campaign.id = "+id)
	if out != nil {
		return out, nil
	}
	if len(rows) != 1 || firstString(mapAt(rows[0], "campaign"), "advertisingChannelType", "advertising_channel_type") != "DEMAND_GEN" {
		return mcpError("campaign is not Demand Gen"), nil
	}
	campaign := mapAt(rows[0], "campaign")
	update := map[string]any{"resourceName": googleCampaignResource(acct.NativeAccountID, id)}
	fields := []string{}
	for _, k := range []string{"name", "status", "start_time", "end_time"} {
		if v := firstString(args, k); v != "" {
			key := k
			switch k {
			case "status":
				if !googleValidStatus(v) {
					return mcpError("invalid status"), nil
				}
				v = googleCampaignStatus(v)
			case "start_time":
				key = "startDateTime"
				var err error
				v, err = demandGenDate(v, false)
				if err != nil {
					return mcpError(err.Error()), nil
				}
			case "end_time":
				key = "endDateTime"
				var err error
				v, err = demandGenDate(v, true)
				if err != nil {
					return mcpError(err.Error()), nil
				}
			}
			update[key] = v
			fields = append(fields, googleMaskField(key))
		}
	}
	if args["bid_strategy"] != nil || args["optimization_goal"] != nil || args["target_cpa_cents"] != nil || args["target_roas"] != nil {
		if firstString(args, "bid_strategy") == "" {
			switch goal {
			case "link_clicks":
				merged["bid_strategy"] = "maximize_clicks"
			case "conversion_value":
				merged["bid_strategy"] = "maximize_conversion_value"
			case "conversions", "channel_subscriptions", "follow_on_views":
				merged["bid_strategy"] = "maximize_conversions"
			default:
				return mcpError("select a supported optimization_goal"), nil
			}
		}
		if err := validateDemandGenBids(merged, goal); err != nil {
			return mcpError(err.Error()), nil
		}
		field, err := googleBiddingField(merged, "DEMAND_GEN")
		if err != nil {
			return mcpError(err.Error()), nil
		}
		if goal == "link_clicks" && field != bidTargetSpend || goal == "conversion_value" && field != bidMaximizeConvValue || (goal == "conversions" || goal == "channel_subscriptions" || goal == "follow_on_views") && field != bidMaximizeConversions {
			return mcpError("bidding conflicts with optimization_goal"), nil
		}
		expected := map[string]string{bidMaximizeConversions: "MAXIMIZE_CONVERSIONS", bidMaximizeConvValue: "MAXIMIZE_CONVERSION_VALUE", bidTargetSpend: "TARGET_SPEND"}[field]
		if firstString(campaign, "biddingStrategyType", "bidding_strategy_type") != expected || args["bid_strategy"] != nil || args["target_cpa_cents"] != nil || args["target_roas"] != nil {
			update[field] = googleBiddingPayload(field, merged)
			switch field {
			case bidMaximizeConversions:
				fields = append(fields, "maximize_conversions.target_cpa_micros")
			case bidMaximizeConvValue:
				fields = append(fields, "maximize_conversion_value.target_roas")
			case bidTargetSpend:
				fields = append(fields, "target_spend.cpc_bid_ceiling_micros")
			}
		}
	}
	ops := []any{}
	if len(fields) > 0 {
		ops = append(ops, map[string]any{"campaignOperation": map[string]any{"update": update, "updateMask": strings.Join(fields, ",")}})
	}
	if args["daily_budget_cents"] != nil || args["lifetime_budget_cents"] != nil {
		if args["daily_budget_cents"] != nil && args["lifetime_budget_cents"] != nil {
			return mcpError("select one budget type"), nil
		}
		budget := map[string]any{"resourceName": firstString(campaign, "campaignBudget", "campaign_budget")}
		field := "amountMicros"
		key := "daily_budget_cents"
		if args["lifetime_budget_cents"] != nil {
			field = "totalAmountMicros"
			key = "lifetime_budget_cents"
		}
		cents := intArg(args, key, 0)
		if cents <= 0 {
			return mcpError("budget must be positive"), nil
		}
		budget[field] = googleMicros(cents)
		ops = append(ops, map[string]any{"campaignBudgetOperation": map[string]any{"update": budget, "updateMask": googleMaskField(field)}})
	}
	if args["optimization_goal"] != nil || args["conversion_action_resource_ids"] != nil {
		refs, out := a.googleGoalActions(ctx, acct, merged)
		if out != nil {
			return out, nil
		}
		if len(refs) == 0 {
			ops = append(ops, map[string]any{"conversionGoalCampaignConfigOperation": map[string]any{"update": map[string]any{"resourceName": "customers/" + acct.NativeAccountID + "/conversionGoalCampaignConfigs/" + id, "goalConfigLevel": "CUSTOMER"}, "updateMask": "goal_config_level"}})
			merged["conversion_action_resource_ids"] = []any{}
		} else {
			goalOps, out := a.googleGoalOperations(ctx, acct, id, firstString(campaign, "name"), refs)
			if out != nil {
				return out, nil
			}
			ops = append(ops, goalOps...)
			merged["conversion_action_resource_ids"] = a.localGoalIDs(ctx, acct, refs)
		}
	}
	if len(ops) == 0 {
		return mcpError("no Demand Gen fields to update"), nil
	}
	result, out := a.execIntegrationTool(ctx, acct, "bulk_mutate", map[string]any{"customer_id": acct.NativeAccountID, "mutateOperations": ops, "partialFailure": false})
	if out != nil {
		return out, nil
	}
	if err := a.saveCampaignSettings(ctx, acct, id, merged); err != nil {
		return nil, err
	}
	return map[string]any{"campaign_id": id, "result": result}, nil
}
func (a *App) demandGenReadiness(ctx *sdk.AppCtx, acct *adAccount, id string) (map[string]any, map[string]any) {
	cfg, err := a.campaignSettings(ctx, acct, id)
	if err != nil {
		return nil, mcpError(err.Error())
	}
	if firstString(cfg, "channel_type") != "demand_gen" {
		return nil, mcpError("no structured Demand Gen settings for this campaign")
	}
	goal := firstString(cfg, "optimization_goal")
	refs, out := a.googleGoalActions(ctx, acct, cfg)
	if out != nil {
		return map[string]any{"ready": false, "status": "ineligible", "reason": mcpErrorMessage(out)}, out
	}
	if len(refs) > 0 {
		rows, out := a.googleResourceRows(ctx, acct, "SELECT conversion_goal_campaign_config.campaign, conversion_goal_campaign_config.goal_config_level, conversion_goal_campaign_config.custom_conversion_goal FROM conversion_goal_campaign_config WHERE conversion_goal_campaign_config.campaign = 'customers/"+acct.NativeAccountID+"/campaigns/"+id+"'")
		if out != nil {
			return nil, out
		}
		if len(rows) != 1 {
			return nil, mcpError("campaign conversion-goal configuration is unavailable")
		}
		config := mapAt(rows[0], "conversionGoalCampaignConfig")
		if len(config) == 0 {
			config = mapAt(rows[0], "conversion_goal_campaign_config")
		}
		resource := firstString(config, "customConversionGoal", "custom_conversion_goal")
		if !strings.HasPrefix(resource, "customers/"+acct.NativeAccountID+"/customConversionGoals/") || firstString(config, "goalConfigLevel", "goal_config_level") != "CAMPAIGN" {
			return nil, mcpError("campaign no longer uses its selected conversion goals")
		}
		goals, out := a.googleResourceRows(ctx, acct, "SELECT custom_conversion_goal.resource_name, custom_conversion_goal.conversion_actions, custom_conversion_goal.status FROM custom_conversion_goal WHERE custom_conversion_goal.resource_name = '"+resource+"'")
		if out != nil {
			return nil, out
		}
		if len(goals) != 1 {
			return nil, mcpError("selected custom goal is unavailable")
		}
		actual := mapAt(goals[0], "customConversionGoal")
		if len(actual) == 0 {
			actual = mapAt(goals[0], "custom_conversion_goal")
		}
		raw := actual["conversionActions"]
		if raw == nil {
			raw = actual["conversion_actions"]
		}
		same := len(asSlice(raw)) == len(refs)
		for _, ref := range refs {
			found := false
			for _, v := range asSlice(raw) {
				if toString(v) == ref {
					found = true
				}
			}
			same = same && found
		}
		if !same || firstString(actual, "status") == "REMOVED" {
			return nil, mcpError("provider conversion goal differs from the selected campaign actions")
		}
	}
	if len(refs) > 0 {
		standard, out := a.googleResourceRows(ctx, acct, "SELECT campaign_conversion_goal.campaign, campaign_conversion_goal.biddable FROM campaign_conversion_goal WHERE campaign_conversion_goal.campaign = 'customers/"+acct.NativeAccountID+"/campaigns/"+id+"'")
		if out != nil {
			return nil, out
		}
		if len(standard) == 0 {
			return nil, mcpError("standard campaign goals could not be verified")
		}
		for _, row := range standard {
			if googleBool(mapAt(row, "campaignConversionGoal")["biddable"]) {
				return nil, mcpError("campaign still bids on standard conversion goals in addition to its selected actions; reapply campaign goals before activation")
			}
		}
	}

	result := map[string]any{"campaign_id": id, "optimization_goal": goal, "conversion_actions": refs, "status": "ready", "ready": true}
	if goal == "channel_subscriptions" || goal == "follow_on_views" {
		result["status"] = "unknown"
		result["ready"] = false
		result["account_has_youtube_goals"] = len(refs) > 0
		result["channel_link_status"] = "unknown"
		result["video_channel_id"] = cfg["video_channel_id"]
		result["reason"] = "API v23 exposes YouTube conversion actions but cannot verify linking for the specific promoted channel. Verify the channel in Google Ads and acknowledge_unverified_measurement before activation."
	}
	return result, nil
}
func (a *App) checkDemandGenActivation(ctx *sdk.AppCtx, acct *adAccount, args map[string]any, id string) map[string]any {
	cfg, err := a.campaignSettings(ctx, acct, id)
	if err != nil {
		return mcpError("could not load campaign readiness: " + err.Error())
	}
	if acct.Platform != "google" || firstString(cfg, "channel_type") != "demand_gen" {
		return nil
	}
	result, out := a.demandGenReadiness(ctx, acct, id)
	if out != nil {
		return out
	}
	if result["ready"] == true {
		return nil
	}
	if result["status"] == "unknown" && boolArg(args, "acknowledge_unverified_measurement") {
		return nil
	}
	return mcpError(fmt.Sprint(result["reason"]))
}
