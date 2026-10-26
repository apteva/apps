package main

import (
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

func demandGenGroupRequested(args map[string]any) bool {
	for _, k := range []string{"placements", "locations", "languages", "excluded_locations", "audience_ids", "optimized_targeting"} {
		if _, ok := args[k]; ok {
			return true
		}
	}
	return false
}
func demandChannels(raw any) (map[string]any, error) {
	values := asSlice(raw)
	if len(values) < 1 {
		return nil, fmt.Errorf("placements must select at least one supported channel")
	}
	selected := map[string]any{}
	for _, field := range demandGenPlacements {
		selected[field] = false
	}
	for _, v := range values {
		key := toString(v)
		if key == "youtube" {
			for _, k := range []string{"youtubeInStream", "youtubeInFeed", "youtubeShorts"} {
				selected[k] = true
			}
			continue
		}
		field, ok := demandGenPlacements[key]
		if !ok {
			return nil, fmt.Errorf("unsupported Demand Gen placement %s", key)
		}
		selected[field] = true
	}
	return map[string]any{"channelControls": map[string]any{"selectedChannels": selected}}, nil
}
func demandGenCriteria(acct *adAccount, group string, args map[string]any) ([]any, error) {
	targeting, err := normalizedCampaignTargeting(args)
	if err != nil {
		return nil, err
	}
	ops := targeting.operations(acct.NativeAccountID, group)
	for _, op := range ops {
		create := asMap(asMap(op)["create"])
		delete(create, "campaign")
		create["adGroup"] = googleAdGroupResource(acct.NativeAccountID, group)
	}
	ids := asSlice(args["audience_ids"])
	if len(ids) > 1 {
		return nil, fmt.Errorf("Demand Gen supports one grouped Google Audience ID per ad group")
	}
	for _, id := range ids {
		v := toString(id)
		prefix := "customers/" + acct.NativeAccountID + "/audiences/"
		if strings.HasPrefix(v, prefix) {
			v = strings.TrimPrefix(v, prefix)
		}
		if !googleNumericID(v) {
			return nil, fmt.Errorf("audience_ids requires Google Audience IDs in this customer, not user-list IDs")
		}
		ops = append(ops, map[string]any{"create": map[string]any{"adGroup": googleAdGroupResource(acct.NativeAccountID, group), "audience": map[string]any{"audience": prefix + v}}})
	}
	return ops, nil
}
func (a *App) googleDemandGenGroupCreate(ctx *sdk.AppCtx, acct *adAccount, args map[string]any) (any, error) {
	cid, name := firstString(args, "campaign_id"), strings.TrimSpace(firstString(args, "name"))
	if !googleNumericID(cid) || name == "" {
		return mcpError("name and numeric campaign_id required"), nil
	}
	rows, out := a.googleResourceRows(ctx, acct, "SELECT campaign.id, campaign.advertising_channel_type FROM campaign WHERE campaign.id = "+cid)
	if out != nil {
		return out, nil
	}
	if len(rows) != 1 || firstString(mapAt(rows[0], "campaign"), "advertisingChannelType", "advertising_channel_type") != "DEMAND_GEN" {
		return mcpError("structured Demand Gen targeting requires a Demand Gen parent campaign"), nil
	}
	if len(asMap(args["platform_options"])) > 0 || intArg(args, "bid_amount_cents", 0) > 0 {
		return mcpError("Demand Gen ad groups inherit bidding; native overrides conflict with structured targeting"), nil
	}
	if !googleValidStatus(firstString(args, "status")) {
		return mcpError("status must be PAUSED or ACTIVE"), nil
	}
	cfg, err := a.campaignSettings(ctx, acct, cid)
	if err != nil {
		return nil, err
	}
	args = cloneMap(args)
	for _, k := range []string{"locations", "excluded_locations", "languages"} {
		if _, ok := args[k]; !ok {
			if v, exists := cfg[k]; exists {
				args[k] = v
			}
		}
	}
	group := map[string]any{"resourceName": googleAdGroupResource(acct.NativeAccountID, "-1"), "campaign": googleCampaignResource(acct.NativeAccountID, cid), "name": name, "status": googleCampaignStatus(firstString(args, "status")), "audienceSetting": map[string]any{"useAudienceGrouped": true}}
	if v, ok := args["optimized_targeting"]; ok {
		if _, ok := v.(bool); !ok {
			return mcpError("optimized_targeting must be boolean"), nil
		}
		group["optimizedTargetingEnabled"] = v
	}
	if raw, ok := args["placements"]; ok {
		settings, err := demandChannels(raw)
		if err != nil {
			return mcpError(err.Error()), nil
		}
		group["demandGenAdGroupSettings"] = settings
	}
	criteria, err := demandGenCriteria(acct, "-1", args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	if goal := firstString(cfg, "optimization_goal"); goal == "channel_subscriptions" || goal == "follow_on_views" {
		if raw, ok := args["placements"]; ok {
			for _, v := range asSlice(raw) {
				if k := toString(v); k != "youtube" && !strings.HasPrefix(k, "youtube_") {
					return mcpError("YouTube engagement goals require YouTube-only placements"), nil
				}
			}
		} else {
			settings, _ := demandChannels([]any{"youtube"})
			group["demandGenAdGroupSettings"] = settings
		}
	}
	ops := []any{map[string]any{"adGroupOperation": map[string]any{"create": group}}}
	for _, c := range criteria {
		ops = append(ops, map[string]any{"adGroupCriterionOperation": c})
	}
	parsed, out := a.execIntegrationTool(ctx, acct, "bulk_mutate", map[string]any{"customer_id": acct.NativeAccountID, "mutateOperations": ops, "partialFailure": false})
	if out != nil {
		return out, nil
	}
	id := googleMutateID(parsed, "adGroupResult")
	if id == "" {
		return mcpError("provider returned no ad group ID; reconcile before retry"), nil
	}
	return map[string]any{"id": id, "adset_id": id, "campaign_id": cid, "result": parsed}, nil
}
func (a *App) googleDemandGenGroupUpdate(ctx *sdk.AppCtx, acct *adAccount, args map[string]any) (any, error) {
	id := firstString(args, "adset_id")
	if !googleNumericID(id) {
		return mcpError("numeric adset_id required"), nil
	}
	rows, out := a.googleResourceRows(ctx, acct, "SELECT ad_group.id, campaign.id, campaign.advertising_channel_type FROM ad_group WHERE ad_group.id = "+id)
	if out != nil {
		return out, nil
	}
	if len(rows) != 1 || firstString(mapAt(rows[0], "campaign"), "advertisingChannelType", "advertising_channel_type") != "DEMAND_GEN" {
		return mcpError("structured targeting requires a Demand Gen ad group"), nil
	}
	if len(asMap(args["platform_options"])) > 0 || intArg(args, "bid_amount_cents", 0) > 0 {
		return mcpError("Demand Gen ad groups inherit bidding and use structured targeting"), nil
	}
	update := map[string]any{"resourceName": googleAdGroupResource(acct.NativeAccountID, id)}
	mask := []string{}
	if name := firstString(args, "name"); name != "" {
		update["name"] = name
		mask = append(mask, "name")
	}
	if status := firstString(args, "status"); status != "" {
		if !googleValidStatus(status) {
			return mcpError("invalid status"), nil
		}
		update["status"] = googleCampaignStatus(status)
		mask = append(mask, "status")
	}
	if raw, ok := args["placements"]; ok {
		settings, err := demandChannels(raw)
		if err != nil {
			return mcpError(err.Error()), nil
		}
		cfg, _ := a.campaignSettings(ctx, acct, firstString(mapAt(rows[0], "campaign"), "id"))
		goal := firstString(cfg, "optimization_goal")
		if goal == "channel_subscriptions" || goal == "follow_on_views" {
			for _, v := range asSlice(raw) {
				if k := toString(v); k != "youtube" && !strings.HasPrefix(k, "youtube_") {
					return mcpError("channel engagement requires YouTube-only placements"), nil
				}
			}
		}
		update["demandGenAdGroupSettings"] = settings
		for _, k := range []string{"youtube_in_stream", "youtube_in_feed", "youtube_shorts", "discover", "gmail", "display"} {
			mask = append(mask, "demand_gen_ad_group_settings.channel_controls.selected_channels."+k)
		}
	}
	if v, ok := args["optimized_targeting"]; ok {
		if _, ok := v.(bool); !ok {
			return mcpError("optimized_targeting must be boolean"), nil
		}
		update["optimizedTargetingEnabled"] = v
		mask = append(mask, "optimized_targeting_enabled")
	}
	creates, err := demandGenCriteria(acct, id, args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	ops := []any{}
	if len(mask) > 0 {
		ops = append(ops, map[string]any{"adGroupOperation": map[string]any{"update": update, "updateMask": strings.Join(mask, ",")}})
	}
	types := []string{}
	if args["locations"] != nil || args["excluded_locations"] != nil {
		types = append(types, "LOCATION")
	}
	if args["languages"] != nil {
		types = append(types, "LANGUAGE")
	}
	if args["audience_ids"] != nil {
		types = append(types, "AUDIENCE")
	}
	if len(types) > 0 {
		old, out := a.googleResourceRows(ctx, acct, "SELECT ad_group.id, ad_group_criterion.resource_name, ad_group_criterion.type, ad_group_criterion.negative FROM ad_group_criterion WHERE ad_group.id = "+id+" AND ad_group_criterion.type IN ("+strings.Join(types, ",")+")")
		if out != nil {
			return out, nil
		}
		for _, row := range old {
			v := mapAt(row, "adGroupCriterion")
			if len(v) == 0 {
				v = mapAt(row, "ad_group_criterion")
			}
			negative := googleBool(v["negative"])
			typ := firstString(v, "type")
			if typ == "LOCATION" && ((negative && args["excluded_locations"] == nil) || (!negative && args["locations"] == nil)) {
				continue
			}
			ops = append(ops, map[string]any{"adGroupCriterionOperation": map[string]any{"remove": firstString(v, "resourceName", "resource_name")}})
		}
	}
	for _, c := range creates {
		ops = append(ops, map[string]any{"adGroupCriterionOperation": c})
	}
	if len(ops) == 0 {
		return mcpError("no structured fields to update"), nil
	}
	return a.execOrErr(ctx, acct, "bulk_mutate", map[string]any{"customer_id": acct.NativeAccountID, "mutateOperations": ops, "partialFailure": false})
}
func (a *App) validateDemandGenAdParent(ctx *sdk.AppCtx, acct *adAccount, id string) map[string]any {
	if !googleNumericID(id) {
		return mcpError("numeric adset_id required")
	}
	rows, out := a.googleResourceRows(ctx, acct, "SELECT ad_group.id, campaign.id, campaign.advertising_channel_type FROM ad_group WHERE ad_group.id = "+id)
	if out != nil {
		return out
	}
	if len(rows) != 1 || firstString(mapAt(rows[0], "campaign"), "advertisingChannelType", "advertising_channel_type") != "DEMAND_GEN" {
		return mcpError("selected ad format requires a Demand Gen parent campaign")
	}
	return nil
}
func (a *App) toolAdSetConfiguration(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, def, out := a.resolveAdAccount(ctx, args)
	if out != nil {
		return out, nil
	}
	if acct.Platform != "google" {
		return mcpError("structured ad group configuration is currently supported on Google"), nil
	}
	id := firstString(args, "adset_id")
	if !googleNumericID(id) {
		return mcpError("numeric adset_id required"), nil
	}
	if out := a.requireManagedEntity(ctx, acct, def, "ad_group", id); out != nil {
		return out, nil
	}
	fields := []string{"ad_group.id", "ad_group.name", "ad_group.status", "campaign.id", "campaign.advertising_channel_type", "ad_group.optimized_targeting_enabled", "ad_group.demand_gen_ad_group_settings.channel_controls.channel_strategy"}
	for _, k := range []string{"youtube_in_stream", "youtube_in_feed", "youtube_shorts", "discover", "gmail", "display"} {
		fields = append(fields, "ad_group.demand_gen_ad_group_settings.channel_controls.selected_channels."+k)
	}
	rows, out := a.googleResourceRows(ctx, acct, "SELECT "+strings.Join(fields, ", ")+" FROM ad_group WHERE ad_group.id = "+id)
	if out != nil {
		return out, nil
	}
	if len(rows) != 1 {
		return mcpError("ad group not found"), nil
	}
	criteria, out := a.googleResourceRows(ctx, acct, "SELECT ad_group.id, ad_group_criterion.resource_name, ad_group_criterion.type, ad_group_criterion.negative, ad_group_criterion.location.geo_target_constant, ad_group_criterion.language.language_constant, ad_group_criterion.audience.audience FROM ad_group_criterion WHERE ad_group.id = "+id+" AND ad_group_criterion.type IN (LOCATION,LANGUAGE,AUDIENCE)")
	if out != nil {
		return out, nil
	}
	group := mapAt(rows[0], "adGroup")
	placements := []string{}
	settings := mapAt(group, "demandGenAdGroupSettings")
	selected := mapAt(mapAt(settings, "channelControls"), "selectedChannels")
	for k, field := range demandGenPlacements {
		if googleBool(selected[field]) {
			placements = append(placements, k)
		}
	}
	result := map[string]any{"adset_id": id, "campaign_id": firstString(mapAt(rows[0], "campaign"), "id"), "name": group["name"], "status": group["status"], "placements": placements, "placement_mode": "selected", "optimized_targeting": group["optimizedTargetingEnabled"], "locations": []string{}, "excluded_locations": []string{}, "languages": []string{}, "audience_ids": []string{}, "native": rows[0]}
	if len(selected) == 0 {
		strategy := firstString(mapAt(settings, "channelControls"), "channelStrategy")
		if strategy == "" {
			strategy = "ALL_CHANNELS"
		}
		result["placement_mode"] = strings.ToLower(strategy)
		for key := range demandGenPlacements {
			if strategy == "ALL_OWNED_AND_OPERATED_CHANNELS" && key == "display" {
				continue
			}
			placements = append(placements, key)
		}
		result["placements"] = placements
	}

	for _, row := range criteria {
		v := mapAt(row, "adGroupCriterion")
		key, id := "", ""
		switch firstString(v, "type") {
		case "LOCATION":
			key = "locations"
			if googleBool(v["negative"]) {
				key = "excluded_locations"
			}
			id = lastResourceSegment(firstString(mapAt(v, "location"), "geoTargetConstant"))
		case "LANGUAGE":
			key = "languages"
			id = lastResourceSegment(firstString(mapAt(v, "language"), "languageConstant"))
		case "AUDIENCE":
			key = "audience_ids"
			id = lastResourceSegment(firstString(mapAt(v, "audience"), "audience"))
		}
		if key != "" {
			result[key] = append(result[key].([]string), id)
		}
	}
	return result, nil
}
