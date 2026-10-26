package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var demandGenFormats = map[string]string{"responsive_video": "demandGenVideoResponsiveAd", "responsive_image": "demandGenMultiAssetAd", "carousel": "demandGenCarouselAd"}
var demandGenPlacements = map[string]string{"youtube_in_stream": "youtubeInStream", "youtube_in_feed": "youtubeInFeed", "youtube_shorts": "youtubeShorts", "discover": "discover", "gmail": "gmail", "display": "display"}

func demandGenCapabilities() map[string]any {
	return map[string]any{"supported": true, "channel_type": "demand_gen", "ad_formats": []string{"responsive_video", "responsive_image", "carousel"}, "optimization_goals": []string{"link_clicks", "conversions", "conversion_value", "channel_subscriptions", "follow_on_views"}, "placements": []string{"youtube_in_stream", "youtube_in_feed", "youtube_shorts", "discover", "gmail", "display"}, "targeting_level": "ad_group", "budget_types": []string{"daily", "lifetime"}, "product_feeds": false, "creative_updates": "paused_replacement", "channel_link_verification": "unknown_api_v23", "operations": []string{"create", "read", "update", "activate", "report"}}
}
func (a *App) extendDemandGenTools(tools []sdk.Tool) []sdk.Tool {
	str := func() map[string]any { return map[string]any{"type": "string"} }
	ids := func() map[string]any { return map[string]any{"type": "array", "items": str()} }
	for i := range tools {
		t := &tools[i]
		p := asMap(t.InputSchema["properties"])
		switch t.Name {
		case "campaign_create", "campaign_update":
			p["optimization_goal"] = map[string]any{"type": "string", "enum": []string{"link_clicks", "conversions", "conversion_value", "channel_subscriptions", "follow_on_views"}, "description": "Structured Demand Gen optimization. Channel goals select existing YouTube conversion actions; no account-wide goal changes."}
			p["conversion_action_resource_ids"] = map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Existing conversion_action resource IDs owned by this bound account. Campaign-specific optimization goals."}
			p["video_channel_id"] = str()
			t.Description += " Google Demand Gen supports structured optimization_goal and conversion_action_resource_ids, daily or lifetime budgets, and campaign location/language defaults inherited by its ad groups."
		case "adset_create", "adset_update":
			for _, k := range []string{"locations", "excluded_locations", "languages", "audience_ids", "placements"} {
				p[k] = ids()
			}
			p["optimized_targeting"] = map[string]any{"type": "boolean"}
			t.Description += " Demand Gen: placements selects YouTube in-stream/in-feed/Shorts, Discover, Gmail or display. Locations/languages and Google Audience IDs are applied to this ad group. Updates replace only supplied targeting categories."
		case "ad_create", "ad_update":
			p["ad_format"] = map[string]any{"type": "string", "enum": []string{"responsive_search", "app", "responsive_video", "responsive_image", "carousel"}}
			for _, k := range []string{"video_asset_ids", "logo_asset_ids", "image_asset_ids", "square_image_asset_ids", "portrait_image_asset_ids", "card_asset_ids"} {
				p[k] = ids()
			}
			p["business_name"] = str()
			p["call_to_action"] = str()
			p["long_headlines"] = map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"text": str()}, "required": []string{"text"}}}
			// Counts and lengths differ by format. Search validation remains in its builder.
			for _, k := range []string{"headlines", "descriptions"} {
				if len(asMap(p[k])) == 0 {
					p[k] = map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"text": str()}, "required": []string{"text"}}}
				}
				q := asMap(p[k])
				delete(q, "minItems")
				delete(q, "maxItems")
				if text := asMap(asMap(asMap(q["items"])["properties"])["text"]); len(text) > 0 {
					delete(text, "maxLength")
				}
				q["description"] = "Text assets. Requirements are validated for the selected ad_format; existing responsive_search requirements remain unchanged."
			}
			if t.Name == "ad_update" {
				p["replace_creative"] = map[string]any{"type": "boolean", "description": "Create a paused replacement and pause the old ad because API v23 creative fields are immutable. Returns the new ad ID."}
				p["idempotency_key"] = str()
				p["final_urls"] = ids()
			}
			t.Description += " Structured Demand Gen ad_format: responsive_video, responsive_image or carousel. Uses reusable asset IDs, business_name and format-specific text."
		case "audience_list", "audience_create_custom":
			p["audience_type"] = map[string]any{"type": "string", "enum": []string{"user_list", "grouped"}}
			if t.Name == "audience_create_custom" {
				p["idempotency_key"] = str()
				for _, k := range []string{"user_list_ids", "custom_audience_ids", "user_interest_ids"} {
					p[k] = ids()
				}
			}
			t.Description += " Google audience_type=grouped reads/creates the Audience resources required by Demand Gen; segment IDs remain typed."
		case "performance_get":
			p["breakdown"] = map[string]any{"type": "string", "enum": []string{"placement", "creative"}, "description": "Google live placement or creative-asset rows. Asset counts may overlap and must not be summed into campaign totals."}
		case "creative_create":
			p["image_asset_id"] = str()
			p["square_image_asset_id"] = str()
			p["portrait_image_asset_id"] = str()
			t.Description += " Google carousel creates a reusable card asset from headline, destination_url and existing image_asset_id or square_image_asset_id. Google image accepts image_url through the upload pipeline."
		case "conversion_event_list":
			t.InputSchema["required"] = []string{"ad_account_id"}
			t.Description += " Without mobile_app_resource_id, lists existing Google conversion actions including YouTube engagement goals."
		case "conversion_readiness_get":
			p["campaign_id"] = str()
			t.InputSchema["required"] = []string{"ad_account_id"}
			t.Description += " For a structured Demand Gen campaign, pass campaign_id to check its selected goals and account-level YouTube goal availability. Channel-specific linking can be unknown."
		}
	}
	common := func(required string) map[string]any {
		return schemaObject(map[string]any{"ad_account_id": map[string]any{"type": "integer"}, required: str()}, []string{"ad_account_id", required})
	}
	return append(tools,
		sdk.Tool{Name: "campaign_configuration_get", Description: "Read normalized campaign channel, bidding, budget, schedule, conversion-goal settings and targeting defaults.", InputSchema: common("campaign_id"), Handler: a.toolCampaignConfiguration},
		sdk.Tool{Name: "adset_configuration_get", Description: "Read normalized Demand Gen placements, audiences, geography and languages for an existing managed ad group.", InputSchema: common("adset_id"), Handler: a.toolAdSetConfiguration})
}

func (a *App) campaignSettings(ctx *sdk.AppCtx, acct *adAccount, id string) (map[string]any, error) {
	var raw string
	err := ctx.AppDB().QueryRow(`SELECT settings_json FROM ad_campaign_settings WHERE project_id=? AND ad_account_id=? AND campaign_id=?`, acct.ProjectID, acct.ID, id).Scan(&raw)
	if err == sql.ErrNoRows {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	err = json.Unmarshal([]byte(raw), &m)
	return m, err
}
func (a *App) saveCampaignSettings(ctx *sdk.AppCtx, acct *adAccount, id string, args map[string]any) error {
	m, err := a.campaignSettings(ctx, acct, id)
	if err != nil {
		return err
	}
	for _, k := range []string{"channel_type", "optimization_goal", "conversion_action_resource_ids", "video_channel_id", "locations", "excluded_locations", "languages"} {
		if v, ok := args[k]; ok {
			m[k] = v
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = ctx.AppDB().Exec(`INSERT INTO ad_campaign_settings(project_id,ad_account_id,campaign_id,settings_json) VALUES(?,?,?,?) ON CONFLICT(project_id,ad_account_id,campaign_id) DO UPDATE SET settings_json=excluded.settings_json`, acct.ProjectID, acct.ID, id, string(raw))
	return err
}
func (a *App) isDemandGen(ctx *sdk.AppCtx, acct *adAccount, id string) bool {
	m, _ := a.campaignSettings(ctx, acct, id)
	if firstString(m, "channel_type") == "demand_gen" {
		return true
	}
	var raw string
	if err := ctx.AppDB().QueryRow(`SELECT provider_data_json FROM ad_entities WHERE project_id=? AND ad_account_id=? AND level='campaign' AND native_entity_id=?`, acct.ProjectID, acct.ID, id).Scan(&raw); err != nil {
		return false
	}
	var row map[string]any
	if json.Unmarshal([]byte(raw), &row) != nil {
		return false
	}
	return strings.EqualFold(firstString(row, "channel_type", "objective"), "demand_gen")
}

func googleHostedEvent(v map[string]any) string {
	if firstString(v, "origin") != "YOUTUBE_HOSTED" {
		return ""
	}
	switch firstString(v, "category") {
	case "ENGAGEMENT":
		return "channel_subscriptions"
	case "YOUTUBE_FOLLOW_ON_VIEWS":
		return "follow_on_views"
	}
	return "youtube_engagement"
}
func (a *App) googleGoalActions(ctx *sdk.AppCtx, acct *adAccount, args map[string]any) ([]string, map[string]any) {
	goal := firstString(args, "optimization_goal")
	raw, present := args["conversion_action_resource_ids"]
	if !present && goal != "channel_subscriptions" && goal != "follow_on_views" {
		return nil, nil
	}
	if err := a.refreshResourceKinds(ctx, acct, []string{resourceConversionAction})[resourceConversionAction]; err != "" {
		return nil, mcpError(err)
	}
	actions, err := a.listResources(ctx, acct, resourceConversionAction)
	if err != nil {
		return nil, mcpError(err.Error())
	}
	selected := []adResource{}
	if present {
		list, ok := raw.([]any)
		if ok && len(list) == 0 && goal != "channel_subscriptions" && goal != "follow_on_views" {
			return nil, nil
		}
		if !ok || len(list) < 1 || len(list) > 20 {
			return nil, mcpError("select 1–20 conversion_action_resource_ids")
		}
		seen := map[int64]bool{}
		for _, v := range list {
			id := int64(intArg(map[string]any{"id": v}, "id", 0))
			if seen[id] {
				continue
			}
			seen[id] = true
			r, out := a.getResource(ctx, acct, id)
			if out != nil {
				return nil, mcpError(out.Error())
			}
			if r == nil || r.Kind != resourceConversionAction {
				return nil, mcpError("selected resource must be a conversion_action in this account")
			}
			selected = append(selected, *r)
		}
	} else {
		for _, r := range actions {
			if firstString(r.Metadata, "event") == goal && r.Status == "active" {
				selected = append(selected, r)
			}
		}
		if len(selected) != 1 {
			return nil, mcpError("select an existing eligible conversion_action_resource_ids for " + goal)
		}
	}
	refs := []string{}
	for _, r := range selected {
		owner := firstString(r.Metadata, "owner_customer")
		if r.Status != "active" || (owner != "" && owner != "customers/"+acct.NativeAccountID) {
			return nil, mcpError("conversion action is disabled or owned by another conversion customer")
		}
		if googleMobileConversionType(firstString(r.Metadata, "type")) || firstString(r.Metadata, "app_id") != "" {
			return nil, mcpError("mobile conversion actions require the existing app campaign workflow")
		}
		if (goal == "channel_subscriptions" || goal == "follow_on_views") && firstString(r.Metadata, "event") != goal {
			return nil, mcpError("selected conversion action does not match optimization_goal")
		}
		refs = append(refs, "customers/"+acct.NativeAccountID+"/conversionActions/"+r.NativeID)
	}
	return refs, nil
}
func (a *App) googleGoalOperations(ctx *sdk.AppCtx, acct *adAccount, campaign, name string, actions []string) ([]any, map[string]any) {
	if len(actions) == 0 {
		return nil, nil
	}
	rows, out := a.googleResourceRows(ctx, acct, "SELECT custom_conversion_goal.resource_name, custom_conversion_goal.conversion_actions, custom_conversion_goal.status FROM custom_conversion_goal")
	if out != nil {
		return nil, out
	}
	expected := append([]string{}, actions...)
	sort.Strings(expected)
	custom := ""
	ops := []any{}
	for _, row := range rows {
		v := mapAt(row, "customConversionGoal")
		if len(v) == 0 {
			v = mapAt(row, "custom_conversion_goal")
		}
		if firstString(v, "status") == "REMOVED" {
			continue
		}
		refs := []string{}
		raw := v["conversionActions"]
		if raw == nil {
			raw = v["conversion_actions"]
		}
		for _, r := range asSlice(raw) {
			refs = append(refs, toString(r))
		}
		sort.Strings(refs)
		if strings.Join(refs, "\n") == strings.Join(expected, "\n") {
			custom = firstString(v, "resourceName", "resource_name")
			break
		}
	}
	if custom == "" {
		custom = "customers/" + acct.NativeAccountID + "/customConversionGoals/-3"
		if len(name) > 200 {
			name = name[:200]
		}
		ops = append(ops, map[string]any{"customConversionGoalOperation": map[string]any{"create": map[string]any{"resourceName": custom, "name": name + " goals", "conversionActions": actions}}})
	}
	ops = append(ops, map[string]any{"conversionGoalCampaignConfigOperation": map[string]any{"update": map[string]any{"resourceName": "customers/" + acct.NativeAccountID + "/conversionGoalCampaignConfigs/" + campaign, "goalConfigLevel": "CAMPAIGN", "customConversionGoal": custom}, "updateMask": "goal_config_level,custom_conversion_goal"}})
	// A custom goal supplements standard goals unless each campaign-level goal is disabled.
	goalQuery := "SELECT customer_conversion_goal.resource_name, customer_conversion_goal.category, customer_conversion_goal.origin FROM customer_conversion_goal"
	if campaign != "-2" {
		goalQuery = "SELECT campaign_conversion_goal.resource_name, campaign_conversion_goal.category, campaign_conversion_goal.origin, campaign_conversion_goal.biddable FROM campaign_conversion_goal WHERE campaign_conversion_goal.campaign = 'customers/" + acct.NativeAccountID + "/campaigns/" + campaign + "'"
	}
	standard, out := a.googleResourceRows(ctx, acct, goalQuery)
	if out != nil {
		return nil, out
	}
	if len(standard) == 0 {
		return nil, mcpError("cannot inspect standard conversion goals to isolate campaign optimization")
	}
	for _, row := range standard {
		v := mapAt(row, "customerConversionGoal")
		if len(v) == 0 {
			v = mapAt(row, "campaignConversionGoal")
		}
		ref := firstString(v, "resourceName", "resource_name")
		if campaign == "-2" {
			suffix := lastResourceSegment(ref)
			parts := strings.Split(suffix, "~")
			if len(parts) != 2 || !safeProviderID(parts[0]) || !safeProviderID(parts[1]) {
				return nil, mcpError("invalid provider goal resource name")
			}
			ref = "customers/" + acct.NativeAccountID + "/campaignConversionGoals/" + campaign + "~" + suffix
		}
		if !strings.HasPrefix(ref, "customers/"+acct.NativeAccountID+"/campaignConversionGoals/") {
			return nil, mcpError("conversion goal belongs to another customer")
		}
		ops = append(ops, map[string]any{"campaignConversionGoalOperation": map[string]any{"update": map[string]any{"resourceName": ref, "biddable": false}, "updateMask": "biddable"}})
	}
	return ops, nil
}

func (a *App) googleDemandGenCampaignCreate(ctx *sdk.AppCtx, acct *adAccount, args map[string]any) (any, error) {
	name := strings.TrimSpace(firstString(args, "name"))
	if name == "" {
		return mcpError("name required"), nil
	}
	if !googleValidStatus(firstString(args, "status")) {
		return mcpError("status must be PAUSED or ACTIVE"), nil
	}
	if len(asMap(args["platform_options"])) > 0 {
		return mcpError("structured Demand Gen uses normalized fields; conflicting native options are not accepted"), nil
	}
	if boolArgDefault(args, "shared_budget", false) {
		return mcpError("Demand Gen requires an unshared budget"), nil
	}
	_, err := normalizedCampaignTargeting(args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	daily, total := intArg(args, "daily_budget_cents", 0), intArg(args, "lifetime_budget_cents", 0)
	if (daily <= 0 && total <= 0) || (daily > 0 && total > 0) {
		return mcpError("supply exactly one positive daily_budget_cents or lifetime_budget_cents"), nil
	}
	budget := map[string]any{"resourceName": "customers/" + acct.NativeAccountID + "/campaignBudgets/-1", "name": name + " Budget", "explicitlyShared": false, "deliveryMethod": "STANDARD"}
	if daily > 0 {
		budget["amountMicros"] = googleMicros(daily)
	} else {
		if firstString(args, "start_time") == "" || firstString(args, "end_time") == "" {
			return mcpError("a lifetime budget requires start_time and end_time"), nil
		}
		budget["totalAmountMicros"] = googleMicros(total)
		budget["period"] = "CUSTOM_PERIOD"
	}
	goal := firstString(args, "optimization_goal")
	if goal == "" {
		switch firstString(args, "objective") {
		case "engagement":
			return mcpError("engagement requires channel_subscriptions or follow_on_views optimization_goal"), nil
		case "sales":
			goal = "conversion_value"
		case "leads":
			goal = "conversions"
		case "traffic":
			goal = "link_clicks"
		default:
			return mcpError("select traffic, leads, sales or engagement for Demand Gen"), nil
		}
	}
	defaults := map[string]string{"link_clicks": "maximize_clicks", "conversions": "maximize_conversions", "conversion_value": "maximize_conversion_value", "channel_subscriptions": "maximize_conversions", "follow_on_views": "maximize_conversions"}
	strategy, ok := defaults[goal]
	if !ok {
		return mcpError("unsupported Demand Gen optimization_goal"), nil
	}
	args = cloneMap(args)
	args["optimization_goal"] = goal
	if firstString(args, "bid_strategy") == "" {
		args["bid_strategy"] = strategy
	}
	if err := validateDemandGenBids(args, goal); err != nil {
		return mcpError(err.Error()), nil
	}
	field, err := googleBiddingField(args, "DEMAND_GEN")
	if err != nil {
		return mcpError(err.Error()), nil
	}
	if goal == "link_clicks" && field != bidTargetSpend || goal == "conversion_value" && field != bidMaximizeConvValue || (goal == "channel_subscriptions" || goal == "follow_on_views" || goal == "conversions") && field != bidMaximizeConversions {
		return mcpError("bidding strategy conflicts with optimization_goal"), nil
	}
	refs, out := a.googleGoalActions(ctx, acct, args)
	if out != nil {
		return out, nil
	}
	if (goal == "channel_subscriptions" || goal == "follow_on_views") && googleCampaignStatus(firstString(args, "status")) == "ENABLED" {
		return mcpError("create channel engagement campaigns paused; verify channel linking before activation"), nil
	}
	campaign := map[string]any{"resourceName": googleCampaignResource(acct.NativeAccountID, "-2"), "name": name, "advertisingChannelType": "DEMAND_GEN", "status": googleCampaignStatus(firstString(args, "status")), "campaignBudget": budget["resourceName"], "containsEuPoliticalAdvertising": googleEUPoliticalDeclaration(args), field: googleBiddingPayload(field, args)}
	for _, k := range []string{"start_time", "end_time"} {
		if v := firstString(args, k); v != "" {
			date, err := demandGenDate(v, k == "end_time")
			if err != nil {
				return mcpError(err.Error()), nil
			}
			key := "startDateTime"
			if k == "end_time" {
				key = "endDateTime"
			}
			campaign[key] = date
		}
	}
	if campaign["startDateTime"] != nil && campaign["endDateTime"] != nil && toString(campaign["startDateTime"]) > toString(campaign["endDateTime"]) {
		return mcpError("end_time must follow start_time"), nil
	}

	ops := []any{map[string]any{"campaignBudgetOperation": map[string]any{"create": budget}}, map[string]any{"campaignOperation": map[string]any{"create": campaign}}}
	goalOps, goalOut := a.googleGoalOperations(ctx, acct, "-2", name, refs)
	if goalOut != nil {
		return goalOut, nil
	}
	ops = append(ops, goalOps...)
	parsed, out := a.execIntegrationTool(ctx, acct, "bulk_mutate", map[string]any{"customer_id": acct.NativeAccountID, "mutateOperations": ops, "partialFailure": false})
	if out != nil {
		return out, nil
	}
	id := googleMutateID(parsed, "campaignResult")
	bid := googleMutateID(parsed, "campaignBudgetResult")
	if id == "" {
		return mcpError("provider returned no campaign ID; reconcile before another create"), nil
	}
	args["channel_type"] = "demand_gen"
	if len(refs) > 0 {
		args["conversion_action_resource_ids"] = a.localGoalIDs(ctx, acct, refs)
	}
	if err := a.saveCampaignSettings(ctx, acct, id, args); err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "campaign_id": id, "budget_id": bid, "channel_type": "demand_gen", "optimization_goal": goal, "result": parsed}, nil
}
func googleMutateID(parsed any, field string) string {
	for _, r := range asSlice(asMap(parsed)["mutateOperationResponses"]) {
		if ref := firstString(asMap(asMap(r)[field]), "resourceName", "resource_name"); ref != "" {
			id := lastResourceSegment(ref)
			if field == "adGroupAdResult" {
				parts := strings.Split(id, "~")
				id = parts[len(parts)-1]
			}
			return id
		}
	}
	return ""
}
func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	if s, ok := v.([]string); ok {
		out := []any{}
		for _, x := range s {
			out = append(out, x)
		}
		return out
	}
	return nil
}
func demandText(raw any, field string, min, max, width int) ([]any, error) {
	list := asSlice(raw)
	if len(list) < min || len(list) > max {
		return nil, fmt.Errorf("%s requires %d–%d entries", field, min, max)
	}
	out := []any{}
	seen := map[string]bool{}
	for _, v := range list {
		m := asMap(v)
		text := strings.TrimSpace(firstString(m, "text"))
		if text == "" {
			if s, ok := v.(string); ok {
				text = strings.TrimSpace(s)
			}
		}
		w := 0
		for _, r := range text {
			if r >= 0x2e80 {
				w += 2
			} else {
				w++
			}
		}
		if text == "" || w > width || m["pinned_field"] != nil {
			return nil, fmt.Errorf("%s text must be nonempty, unpinned and at most %d display-width units", field, width)
		}
		if seen[strings.ToLower(text)] {
			return nil, fmt.Errorf("duplicate %s text", field)
		}
		seen[strings.ToLower(text)] = true
		out = append(out, map[string]any{"text": text})
	}
	return out, nil
}
func googleAssetRefs(customer string, raw any, field string, min, max int) ([]any, error) {
	list := asSlice(raw)
	if len(list) < min || len(list) > max {
		return nil, fmt.Errorf("%s requires %d–%d asset IDs", field, min, max)
	}
	out := []any{}
	for _, v := range list {
		id := toString(v)
		if strings.HasPrefix(id, "customers/") {
			prefix := "customers/" + customer + "/assets/"
			if !strings.HasPrefix(id, prefix) {
				return nil, fmt.Errorf("%s contains an asset from another customer", field)
			}
			id = strings.TrimPrefix(id, prefix)
		}
		if !googleNumericID(id) {
			return nil, fmt.Errorf("%s requires numeric existing asset IDs", field)
		}
		out = append(out, map[string]any{"asset": "customers/" + customer + "/assets/" + id})
	}
	return out, nil
}
func googleDemandGenAd(args map[string]any, customer string) (map[string]any, error) {
	format := firstString(args, "ad_format")
	native, ok := demandGenFormats[format]
	if !ok {
		return nil, fmt.Errorf("unsupported Demand Gen ad_format")
	}
	urls, err := rsaFinalURLs(args["final_urls"])
	if err != nil {
		return nil, err
	}
	business := strings.TrimSpace(firstString(args, "business_name"))
	if business == "" || utf8.RuneCountInString(business) > 25 {
		return nil, fmt.Errorf("business_name requires 1–25 characters")
	}
	headlines, err := demandText(args["headlines"], "headlines", 1, 5, 40)
	if format != "responsive_video" {
		headlines, err = demandText(args["headlines"], "headlines", 1, 5, 40)
	}
	if err != nil {
		return nil, err
	}
	desc, err := demandText(args["descriptions"], "descriptions", 1, 5, 90)
	if err != nil {
		return nil, err
	}
	logos, err := googleAssetRefs(customer, args["logo_asset_ids"], "logo_asset_ids", 1, 5)
	if err != nil {
		return nil, err
	}
	info := map[string]any{"headlines": headlines, "descriptions": desc, "logoImages": logos, "businessName": business}
	switch format {
	case "responsive_video":
		videos, e := googleAssetRefs(customer, args["video_asset_ids"], "video_asset_ids", 1, 5)
		if e != nil {
			return nil, e
		}
		long, e := demandText(args["long_headlines"], "long_headlines", 1, 5, 90)
		if e != nil {
			return nil, e
		}
		info["videos"] = videos
		info["longHeadlines"] = long
		info["businessName"] = map[string]any{"text": business}
	case "responsive_image":
		h, e := demandText(args["headlines"], "headlines", 1, 5, 40)
		if e != nil {
			return nil, e
		}
		info["headlines"] = h
		images := 0
		for _, spec := range []struct{ input, output string }{{"image_asset_ids", "marketingImages"}, {"square_image_asset_ids", "squareMarketingImages"}, {"portrait_image_asset_ids", "portraitMarketingImages"}} {
			refs, e := googleAssetRefs(customer, args[spec.input], spec.input, 0, 20)
			if e != nil {
				return nil, e
			}
			images += len(refs)
			if len(refs) > 0 {
				info[spec.output] = refs
			}
		}
		if images < 1 || images > 20 || len(asSlice(args["image_asset_ids"]))+len(asSlice(args["square_image_asset_ids"])) < 1 {
			return nil, fmt.Errorf("responsive_image requires landscape or square images, at most 20 total")
		}
		if c := firstString(args, "call_to_action"); c != "" {
			info["callToActionText"] = c
		}
	case "carousel":
		if len(headlines) != 1 || len(desc) != 1 || len(logos) != 1 {
			return nil, fmt.Errorf("carousel requires one headline, description and logo")
		}
		cards, e := googleAssetRefs(customer, args["card_asset_ids"], "card_asset_ids", 2, 10)
		if e != nil {
			return nil, e
		}
		info = map[string]any{"businessName": business, "headline": headlines[0], "description": desc[0], "logoImage": logos[0], "carouselCards": cards}
		if c := firstString(args, "call_to_action"); c != "" {
			info["callToActionText"] = c
		}
	}
	ad := map[string]any{"finalUrls": urls, native: info}
	if n := firstString(args, "name"); n != "" {
		ad["name"] = n
	}
	return ad, nil
}

func demandGenDate(raw string, end bool) (string, error) {
	date := raw
	if len(date) >= 10 {
		date = date[:10]
	}
	var parsed time.Time
	var err error
	if len(raw) == 8 {
		parsed, err = time.Parse("20060102", raw)
	} else {
		parsed, err = time.Parse("2006-01-02", date)
	}
	if err != nil {
		return "", fmt.Errorf("invalid campaign date %s", raw)
	}
	clock := " 00:00:00"
	if end {
		clock = " 23:59:59"
	}
	return parsed.Format("2006-01-02") + clock, nil
}

func (a *App) localGoalIDs(ctx *sdk.AppCtx, acct *adAccount, refs []string) []any {
	rows, _ := a.listResources(ctx, acct, resourceConversionAction)
	wanted := map[string]bool{}
	for _, r := range refs {
		wanted[lastResourceSegment(r)] = true
	}
	ids := []any{}
	for _, r := range rows {
		if wanted[r.NativeID] {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

func validateDemandGenBids(args map[string]any, goal string) error {
	if args["target_cpa_cents"] != nil && intArg(args, "target_cpa_cents", 0) <= 0 {
		return fmt.Errorf("target_cpa_cents must be positive")
	}
	if args["target_roas"] != nil && floatArg(args, "target_roas") <= 0 {
		return fmt.Errorf("target_roas must be positive")
	}
	strategy := firstString(args, "bid_strategy")
	if strategy == "target_cpa" && intArg(args, "target_cpa_cents", 0) <= 0 {
		return fmt.Errorf("target_cpa requires target_cpa_cents")
	}
	if strategy == "target_roas" && floatArg(args, "target_roas") <= 0 {
		return fmt.Errorf("target_roas bidding requires a positive target_roas")
	}
	if args["target_roas"] != nil && goal != "conversion_value" {
		return fmt.Errorf("target_roas requires conversion_value optimization")
	}
	if args["target_cpa_cents"] != nil && (goal == "conversion_value" || goal == "link_clicks") {
		return fmt.Errorf("target_cpa_cents requires conversion-count optimization")
	}
	return nil
}
