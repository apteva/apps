package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type xRedditAdapter struct{}

func pendingIntegrationCall(ctx *sdk.AppCtx, row *pendingRow, tool string, input map[string]any) (any, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(row.connectionID, tool, input)
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return nil, fmt.Errorf("upstream non-2xx: %s", body)
	}
	var parsed any
	if err := json.Unmarshal(res.Data, &parsed); err != nil {
		return nil, fmt.Errorf("parse provider response: %w", err)
	}
	return parsed, nil
}

func pendingRedditRows(ctx *sdk.AppCtx, row *pendingRow, tool string, input map[string]any) ([]map[string]any, error) {
	rows := make([]map[string]any, 0)
	nextURL := ""
	for page := 0; page < 20; page++ {
		if nextURL != "" {
			input["next_url"] = nextURL
		}
		parsed, err := pendingIntegrationCall(ctx, row, tool, input)
		if err != nil {
			return nil, err
		}
		rows = append(rows, resultRows(parsed)...)
		next := redditNextURL(parsed)
		if next == "" {
			return rows, nil
		}
		if next == nextURL {
			return nil, fmt.Errorf("%s returned a repeated pagination URL", tool)
		}
		nextURL = next
	}
	return nil, fmt.Errorf("%s pagination exceeded the safety limit", tool)
}

func (xRedditAdapter) ListAccounts(_ *App, ctx *sdk.AppCtx, row *pendingRow, def *platformDef) ([]map[string]any, error) {
	if row.platform == "x" {
		parsed, err := pendingIntegrationCall(ctx, row, def.ListAccountsTool, map[string]any{"count": 1000})
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, 0)
		for _, account := range resultRows(parsed) {
			out = append(out, map[string]any{
				"id":       firstString(account, "id"),
				"name":     firstString(account, "name"),
				"currency": firstString(account, "currency"),
				"timezone": firstString(account, "timezone", "timezone_name"),
			})
		}
		return out, nil
	}

	businesses, err := pendingRedditRows(ctx, row, "list_my_businesses", map[string]any{"page.size": 200})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	accounts := make([]map[string]any, 0)
	for _, business := range businesses {
		businessID := firstString(business, "id", "business_id")
		if businessID == "" {
			continue
		}
		listed, listErr := pendingRedditRows(ctx, row, def.ListAccountsTool, map[string]any{
			"business_id": businessID,
			"page.size":   200,
		})
		if listErr != nil {
			return nil, listErr
		}
		for _, account := range listed {
			id := firstString(account, "id", "ad_account_id")
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			accounts = append(accounts, map[string]any{
				"id":       id,
				"name":     firstString(account, "name"),
				"currency": firstString(account, "currency"),
				"timezone": firstString(account, "time_zone_id", "timezone"),
			})
		}
		// Partner/shared accounts are absent from the list endpoint. Query is
		// additive and failure is non-fatal because access varies by business.
		if queried, queryErr := pendingIntegrationCall(ctx, row, "query_ad_accounts", map[string]any{
			"business_id": businessID,
			"data":        map[string]any{"page": map[string]any{"size": 200}},
		}); queryErr == nil {
			for _, account := range resultRows(queried) {
				id := firstString(account, "id", "ad_account_id")
				if id == "" || seen[id] {
					continue
				}
				seen[id] = true
				accounts = append(accounts, map[string]any{
					"id": id, "name": firstString(account, "name"),
					"currency": firstString(account, "currency"),
					"timezone": firstString(account, "time_zone_id", "timezone"),
				})
			}
		}
	}
	return accounts, nil
}

func xStatus(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "ACTIVE" {
		return "ACTIVE"
	}
	return "PAUSED"
}

func redditStatus(value string) string { return xStatus(value) }

func xObjective(value string) string {
	return map[string]string{
		"traffic": "WEBSITE_CLICKS", "sales": "WEBSITE_CLICKS", "leads": "WEBSITE_CLICKS",
		"engagement": "ENGAGEMENTS", "awareness": "REACH", "app_promotion": "APP_INSTALLS",
		"link_clicks": "WEBSITE_CLICKS", "conversions": "WEBSITE_CLICKS", "landing_page_views": "WEBSITE_CLICKS",
		"reach": "REACH", "impressions": "REACH", "page_likes": "FOLLOWERS",
		"post_engagement": "ENGAGEMENTS", "thruplay": "VIDEO_VIEWS", "app_installs": "APP_INSTALLS", "value": "WEBSITE_CLICKS",
	}[strings.ToLower(value)]
}

func redditObjective(value string) string {
	return map[string]string{
		"traffic": "CLICKS", "sales": "CONVERSIONS", "leads": "LEAD_GENERATION",
		"engagement": "CLICKS", "awareness": "IMPRESSIONS", "app_promotion": "APP_INSTALLS",
	}[strings.ToLower(value)]
}

func redditOptimizationGoal(value string) string {
	return map[string]string{
		"link_clicks": "CLICKS", "landing_page_views": "PAGE_VISIT", "conversions": "PURCHASE",
		"leads": "LEAD", "reach": "REACH", "impressions": "IMPRESSIONS",
		"page_likes": "CLICKS", "post_engagement": "CLICKS", "thruplay": "VIDEO_VIEW_6S",
		"app_installs": "MOBILE_CONVERSION_INSTALL", "value": "PURCHASE",
	}[strings.ToLower(value)]
}

func centsToMicros(value int) int64 { return int64(value) * 10_000 }

func redditData(args map[string]any, base map[string]any) map[string]any {
	if opts := asMap(args["platform_options"]); len(opts) > 0 {
		if nested := asMap(opts["data"]); len(nested) > 0 {
			for key, value := range nested {
				base[key] = value
			}
		} else {
			for key, value := range opts {
				base[key] = value
			}
		}
	}
	return base
}

func normalizedProviderPage(parsed any, platform, entity string) map[string]any {
	rows := resultRows(parsed)
	data := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		item := map[string]any{
			"id": firstString(row, "id"), "name": firstString(row, "name"),
			"status":           firstString(row, "status", "entity_status", "configured_status"),
			"effective_status": firstString(row, "effective_status", "delivery_status"),
		}
		switch entity {
		case "campaign":
			item["objective"] = firstString(row, "objective")
			item["daily_budget"] = providerBudgetCents(row, platform, "daily_budget_amount_local_micro", "goal_value")
		case "adset":
			item["campaign_id"] = firstString(row, "campaign_id")
			item["optimization_goal"] = firstString(row, "optimization_goal", "objective")
			item["billing_event"] = firstString(row, "bid_type", "charge_by")
			item["targeting"] = row["targeting"]
			item["daily_budget"] = providerBudgetCents(row, platform, "daily_budget_amount_local_micro", "goal_value")
		case "ad":
			item["adset_id"] = firstString(row, "adset_id", "ad_group_id", "line_item_id")
			item["campaign_id"] = firstString(row, "campaign_id")
			creativeID := firstString(row, "creative_id", "post_id", "tweet_id")
			item["creative"] = map[string]any{"id": creativeID, "name": creativeID}
			if item["name"] == "" {
				item["name"] = creativeID
			}
		}
		if item["id"] != "" {
			data = append(data, item)
		}
	}
	var next any
	if platform == "x" {
		if cursor := xNextCursor(parsed); cursor != "" {
			next = cursor
		}
	} else if nextURL := redditNextURL(parsed); nextURL != "" {
		next = nextURL
	}
	return map[string]any{"data": data, "next_cursor": next}
}

func providerBudgetCents(row map[string]any, platform string, keys ...string) string {
	for _, key := range keys {
		value := numericArgAny(row[key])
		if value <= 0 {
			continue
		}
		if platform == "reddit" && key == "goal_value" && !strings.EqualFold(firstString(row, "goal_type"), "DAILY_SPEND") {
			continue
		}
		return strconv.FormatInt(int64(value)/10_000, 10)
	}
	return ""
}

func (xRedditAdapter) CampaignCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	name := stringArgAny(args, "name")
	if name == "" {
		return mcpError("name required"), nil
	}
	if acct.Platform == "x" {
		input := map[string]any{"account_id": acct.NativeAccountID, "name": name, "entity_status": xStatus(stringArgAny(args, "status"))}
		if v := intArg(args, "daily_budget_cents", 0); v > 0 {
			input["daily_budget_amount_local_micro"] = centsToMicros(v)
		}
		if v := intArg(args, "lifetime_budget_cents", 0); v > 0 {
			input["total_budget_amount_local_micro"] = centsToMicros(v)
		}
		putString(input, "start_time", args, "start_time")
		putString(input, "end_time", args, "end_time")
		mergeOptions(input, args)
		if stringArgAny(input, "funding_instrument_id") == "" {
			funding, errOut := a.resolveResourceChoice(ctx, acct, "funding_source", resourceFundingSource, "x_funding_instrument", int64(intArg(args, "funding_source_resource_id", 0)))
			if errOut != nil {
				return errOut, nil
			}
			input["funding_instrument_id"] = funding.NativeID
		}
		return a.execOrErr(ctx, acct, def.CampaignCreateTool, input)
	}
	data := redditData(args, map[string]any{
		"name": name, "objective": redditObjective(stringArgAny(args, "objective")), "configured_status": redditStatus(stringArgAny(args, "status")),
	})
	if v := intArg(args, "daily_budget_cents", 0); v > 0 {
		data["goal_type"], data["goal_value"] = "DAILY_SPEND", centsToMicros(v)
	}
	if v := intArg(args, "lifetime_budget_cents", 0); v > 0 {
		data["goal_type"], data["spend_cap"] = "LIFETIME_SPEND", centsToMicros(v)
	}
	if stringArgAny(data, "funding_instrument_id") == "" {
		funding, errOut := a.resolveResourceChoice(ctx, acct, "funding_source", resourceFundingSource, "reddit_funding_instrument", int64(intArg(args, "funding_source_resource_id", 0)))
		if errOut != nil {
			return errOut, nil
		}
		data["funding_instrument_id"] = funding.NativeID
	}
	return a.execOrErr(ctx, acct, def.CampaignCreateTool, map[string]any{"ad_account_id": acct.NativeAccountID, "data": data})
}

func (xRedditAdapter) CampaignList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	input := map[string]any{def.AccountIDInputField: acct.NativeAccountID}
	if limit := intArg(args, "limit", 0); limit > 0 {
		if acct.Platform == "x" {
			input["count"] = limit
		} else {
			input["page.size"] = limit
		}
	}
	if after := stringArgAny(args, "after"); after != "" {
		if acct.Platform == "x" {
			input["cursor"] = after
		} else {
			input["next_url"] = after
		}
	}
	if status := stringArgAny(args, "status"); status != "" && acct.Platform == "reddit" {
		input["effective_status"] = strings.ToUpper(status)
	}
	parsed, errOut := a.execIntegrationTool(ctx, acct, def.CampaignListTool, input)
	if errOut != nil {
		return errOut, nil
	}
	return normalizedProviderPage(parsed, acct.Platform, "campaign"), nil
}

func (xRedditAdapter) CampaignPerformance(a *App, ctx *sdk.AppCtx, acct *adAccount, _ *platformDef, args map[string]any) (any, error) {
	req, err := validateGenericPerformanceRequest(map[string]any{
		"level": "campaign", "date_from": args["date_from"], "date_to": args["date_to"], "granularity": args["granularity"], "entity_ids": args["campaign_ids"],
	})
	if err != nil {
		return mcpError(err.Error()), nil
	}
	points, errOut := a.fetchAnalyticsPoints(ctx, acct, req)
	if errOut != nil {
		return errOut, nil
	}
	data := make([]map[string]any, 0, len(points))
	for _, point := range points {
		data = append(data, map[string]any{
			"platform": point.Platform, "ad_account_id": point.AdAccountID, "campaign_id": point.CampaignID,
			"campaign_name": point.CampaignName, "date": point.Date, "currency": point.Currency,
			"spend_micros": point.SpendMicros, "impressions": point.Impressions, "clicks": point.Clicks, "conversions": point.Conversions,
		})
	}
	return performanceResponse(data), nil
}

func (xRedditAdapter) CampaignUpdate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	id := stringArgAny(args, "campaign_id")
	if id == "" {
		return mcpError("campaign_id required"), nil
	}
	if acct.Platform == "x" {
		input := map[string]any{"account_id": acct.NativeAccountID, "campaign_id": id}
		putString(input, "name", args, "name")
		if status := stringArgAny(args, "status"); status != "" {
			input["entity_status"] = xStatus(status)
		}
		if v := intArg(args, "daily_budget_cents", 0); v > 0 {
			input["daily_budget_amount_local_micro"] = centsToMicros(v)
		}
		if v := intArg(args, "lifetime_budget_cents", 0); v > 0 {
			input["total_budget_amount_local_micro"] = centsToMicros(v)
		}
		mergeOptions(input, args)
		return a.execUpdateOrErr(ctx, acct, def.CampaignUpdateTool, input)
	}
	data := redditData(args, map[string]any{})
	putString(data, "name", args, "name")
	if status := stringArgAny(args, "status"); status != "" {
		data["configured_status"] = redditStatus(status)
	}
	return a.execUpdateOrErr(ctx, acct, def.CampaignUpdateTool, map[string]any{"campaign_id": id, "data": data})
}

func (xRedditAdapter) CampaignDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	id := stringArgAny(args, "campaign_id")
	if id == "" {
		return mcpError("campaign_id required"), nil
	}
	if acct.Platform == "x" {
		return a.execOrErr(ctx, acct, def.CampaignDeleteTool, map[string]any{"account_id": acct.NativeAccountID, "campaign_id": id})
	}
	return a.execUpdateOrErr(ctx, acct, def.CampaignDeleteTool, map[string]any{"campaign_id": id, "data": map[string]any{"configured_status": "ARCHIVED"}})
}

func (xRedditAdapter) AdSetCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	name, campaignID := stringArgAny(args, "name"), stringArgAny(args, "campaign_id")
	if name == "" || campaignID == "" {
		return mcpError("name and campaign_id required"), nil
	}
	if acct.Platform == "x" {
		objective := xObjective(stringArgAny(args, "optimization_goal"))
		if objective == "" {
			objective = xObjective(stringArgAny(asMap(args["platform_options"]), "objective"))
		}
		input := map[string]any{
			"account_id": acct.NativeAccountID, "campaign_id": campaignID, "name": name,
			"product_type": "PROMOTED_TWEETS", "objective": objective, "placements": []any{"ALL_ON_TWITTER"}, "entity_status": xStatus(stringArgAny(args, "status")),
		}
		if bid := intArg(args, "bid_amount_cents", 0); bid > 0 {
			input["bid_amount_local_micro"] = centsToMicros(bid)
		}
		if budget := intArg(args, "lifetime_budget_cents", 0); budget > 0 {
			input["total_budget_amount_local_micro"] = centsToMicros(budget)
		}
		putString(input, "start_time", args, "start_time")
		putString(input, "end_time", args, "end_time")
		mergeOptions(input, args)
		return a.execOrErr(ctx, acct, def.AdSetCreateTool, input)
	}
	goal := redditOptimizationGoal(stringArgAny(args, "optimization_goal"))
	if goal == "" {
		goal = strings.ToUpper(stringArgAny(args, "optimization_goal"))
	}
	data := redditData(args, map[string]any{
		"campaign_id": campaignID, "name": name, "configured_status": redditStatus(stringArgAny(args, "status")),
		"optimization_goal": goal, "targeting": args["targeting"],
	})
	if bid := intArg(args, "bid_amount_cents", 0); bid > 0 {
		data["bid_value"] = centsToMicros(bid)
	}
	if budget := intArg(args, "daily_budget_cents", 0); budget > 0 {
		data["goal_type"], data["goal_value"] = "DAILY_SPEND", centsToMicros(budget)
	}
	if stringArgAny(data, "conversion_pixel_id") == "" {
		pixel, errOut := a.resolveResourceChoice(ctx, acct, "conversion_source", resourceTrackingSource, "reddit_pixel", int64(intArg(args, "tracking_source_resource_id", 0)))
		if errOut != nil {
			return errOut, nil
		}
		data["conversion_pixel_id"] = pixel.NativeID
	}
	return a.execOrErr(ctx, acct, def.AdSetCreateTool, map[string]any{"ad_account_id": acct.NativeAccountID, "data": data})
}

func (xRedditAdapter) AdSetList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	input := map[string]any{def.AccountIDInputField: acct.NativeAccountID}
	if id := stringArgAny(args, "campaign_id"); id != "" {
		if acct.Platform == "x" {
			input["campaign_ids"] = id
		} else {
			input["campaign_id"] = id
		}
	}
	if limit := intArg(args, "limit", 0); limit > 0 {
		if acct.Platform == "x" {
			input["count"] = limit
		} else {
			input["page.size"] = limit
		}
	}
	if after := stringArgAny(args, "after"); after != "" {
		if acct.Platform == "x" {
			input["cursor"] = after
		} else {
			input["next_url"] = after
		}
	}
	parsed, errOut := a.execIntegrationTool(ctx, acct, def.AdSetListTool, input)
	if errOut != nil {
		return errOut, nil
	}
	return normalizedProviderPage(parsed, acct.Platform, "adset"), nil
}

func (xRedditAdapter) AdSetUpdate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	id := stringArgAny(args, "adset_id")
	if id == "" {
		return mcpError("adset_id required"), nil
	}
	if acct.Platform == "x" {
		input := map[string]any{"account_id": acct.NativeAccountID, "line_item_id": id}
		putString(input, "name", args, "name")
		if status := stringArgAny(args, "status"); status != "" {
			input["entity_status"] = xStatus(status)
		}
		if bid := intArg(args, "bid_amount_cents", 0); bid > 0 {
			input["bid_amount_local_micro"] = centsToMicros(bid)
		}
		mergeOptions(input, args)
		return a.execUpdateOrErr(ctx, acct, def.AdSetUpdateTool, input)
	}
	data := redditData(args, map[string]any{})
	putString(data, "name", args, "name")
	if status := stringArgAny(args, "status"); status != "" {
		data["configured_status"] = redditStatus(status)
	}
	if targeting := asMap(args["targeting"]); len(targeting) > 0 {
		data["targeting"] = targeting
	}
	return a.execUpdateOrErr(ctx, acct, def.AdSetUpdateTool, map[string]any{"ad_group_id": id, "data": data})
}

func (xRedditAdapter) AdSetDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	id := stringArgAny(args, "adset_id")
	if id == "" {
		return mcpError("adset_id required"), nil
	}
	if acct.Platform == "x" {
		return a.execOrErr(ctx, acct, def.AdSetDeleteTool, map[string]any{"account_id": acct.NativeAccountID, "line_item_id": id})
	}
	return a.execUpdateOrErr(ctx, acct, def.AdSetDeleteTool, map[string]any{"ad_group_id": id, "data": map[string]any{"configured_status": "ARCHIVED"}})
}

func (xRedditAdapter) AdCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	adSetID, creativeID := stringArgAny(args, "adset_id"), stringArgAny(args, "creative_id")
	if adSetID == "" || creativeID == "" {
		return mcpError("adset_id and creative_id required"), nil
	}
	if acct.Platform == "x" {
		return a.execOrErr(ctx, acct, def.AdCreateTool, map[string]any{"account_id": acct.NativeAccountID, "line_item_id": adSetID, "tweet_ids": creativeID})
	}
	data := redditData(args, map[string]any{
		"ad_group_id": adSetID, "post_id": creativeID, "name": stringArgAny(args, "name"), "configured_status": redditStatus(stringArgAny(args, "status")),
	})
	return a.execOrErr(ctx, acct, def.AdCreateTool, map[string]any{"ad_account_id": acct.NativeAccountID, "data": data})
}

func (xRedditAdapter) AdList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	input := map[string]any{def.AccountIDInputField: acct.NativeAccountID}
	if id := stringArgAny(args, "adset_id"); id != "" {
		if acct.Platform == "x" {
			input["line_item_ids"] = id
		} else {
			input["ad_group_id"] = id
		}
	}
	if limit := intArg(args, "limit", 0); limit > 0 {
		if acct.Platform == "x" {
			input["count"] = limit
		} else {
			input["page.size"] = limit
		}
	}
	if after := stringArgAny(args, "after"); after != "" {
		if acct.Platform == "x" {
			input["cursor"] = after
		} else {
			input["next_url"] = after
		}
	}
	parsed, errOut := a.execIntegrationTool(ctx, acct, def.AdListTool, input)
	if errOut != nil {
		return errOut, nil
	}
	return normalizedProviderPage(parsed, acct.Platform, "ad"), nil
}

func (xRedditAdapter) AdUpdate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	id := stringArgAny(args, "ad_id")
	if id == "" {
		return mcpError("ad_id required"), nil
	}
	if acct.Platform == "x" {
		if strings.EqualFold(stringArgAny(args, "status"), "ACTIVE") && stringArgAny(args, "name", "creative_id") == "" {
			return map[string]any{"id": id, "status": "ACTIVE", "unchanged": true}, nil
		}
		return mcpError("X promoted-post associations are immutable; delete and recreate the association to change it"), nil
	}
	data := redditData(args, map[string]any{})
	putString(data, "name", args, "name")
	putString(data, "post_id", args, "creative_id")
	if status := stringArgAny(args, "status"); status != "" {
		data["configured_status"] = redditStatus(status)
	}
	return a.execUpdateOrErr(ctx, acct, def.AdUpdateTool, map[string]any{"ad_id": id, "data": data})
}

func (xRedditAdapter) AdDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	id := stringArgAny(args, "ad_id")
	if id == "" {
		return mcpError("ad_id required"), nil
	}
	if acct.Platform == "x" {
		return a.execOrErr(ctx, acct, def.AdDeleteTool, map[string]any{"account_id": acct.NativeAccountID, "promoted_tweet_id": id})
	}
	return a.execUpdateOrErr(ctx, acct, def.AdDeleteTool, map[string]any{"ad_id": id, "data": map[string]any{"configured_status": "ARCHIVED"}})
}

func resolveSourceURL(ctx *sdk.AppCtx, args map[string]any) (string, map[string]any) {
	if sourceURL := stringArgAny(args, "source_url"); sourceURL != "" {
		return sourceURL, nil
	}
	storageID := int64(intArg(args, "storage_id", 0))
	if storageID <= 0 {
		return "", mcpError("source_url or storage_id required")
	}
	var file struct {
		URL string `json:"url"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_url", map[string]any{"id": storageID, "ttl_seconds": 3600}, &file); err != nil {
		return "", mcpError("storage.files_get_url: " + err.Error())
	}
	if file.URL == "" {
		return "", mcpError("storage returned no file URL")
	}
	return file.URL, nil
}

func fetchAsDataURL(rawURL string, maxBytes int64) (string, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("asset download returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return "", fmt.Errorf("asset exceeds %d byte upload limit", maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxBytes {
		return "", fmt.Errorf("asset exceeds %d byte upload limit", maxBytes)
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func downloadAssetFile(rawURL string, maxBytes int64) (*os.File, int64, string, error) {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(rawURL)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, 0, "", fmt.Errorf("asset download returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return nil, 0, "", fmt.Errorf("asset exceeds %d byte upload limit", maxBytes)
	}
	file, err := os.CreateTemp("", "apteva-x-media-*")
	if err != nil {
		return nil, 0, "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()
	written, err := io.Copy(file, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, 0, "", err
	}
	if written > maxBytes {
		return nil, 0, "", fmt.Errorf("asset exceeds %d byte upload limit", maxBytes)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, 0, "", err
	}
	mimeType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if mimeType == "" {
		mimeType = "video/mp4"
	}
	ok = true
	return file, written, mimeType, nil
}

func (xRedditAdapter) CreativeUpload(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	sourceURL, errOut := resolveSourceURL(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	if acct.Platform == "reddit" {
		kind := stringArgAny(args, "kind")
		recordCreativeAsset(ctx, args, acct, sourceURL, kind)
		return map[string]any{"id": sourceURL, "source_url": sourceURL, "kind": kind, "status": "ready", "upload_mode": "remote_url"}, nil
	}
	if stringArgAny(args, "kind") != "video" {
		dataURL, err := fetchAsDataURL(sourceURL, 5*1024*1024)
		if err != nil {
			return mcpError("download X creative asset: " + err.Error()), nil
		}
		input := map[string]any{"media": dataURL, "media_category": "tweet_image"}
		mergeOptions(input, args)
		parsed, callErr := a.execIntegrationTool(ctx, acct, def.CreativeUploadImageTool, input)
		if callErr != nil {
			return callErr, nil
		}
		mediaID := firstString(asMap(parsed), "media_id_string", "media_id")
		if mediaID == "" {
			return mcpError("X media upload returned no media_id"), nil
		}
		recordCreativeAsset(ctx, args, acct, mediaID, "image")
		return map[string]any{"id": mediaID, "media_id": mediaID, "kind": "image", "status": "ready", "provider_response": parsed}, nil
	}
	file, totalBytes, mimeType, err := downloadAssetFile(sourceURL, 500*1024*1024)
	if err != nil {
		return mcpError("download X video: " + err.Error()), nil
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	initialized, errOut := a.execIntegrationTool(ctx, acct, "media_upload_init", map[string]any{
		"command": "INIT", "total_bytes": totalBytes, "media_type": mimeType, "media_category": "amplify_video",
	})
	if errOut != nil {
		return errOut, nil
	}
	mediaID := firstString(asMap(initialized), "media_id_string", "media_id")
	if mediaID == "" {
		return mcpError("X media INIT returned no media_id"), nil
	}
	buffer := make([]byte, 4*1024*1024)
	for segment := 0; ; segment++ {
		read, readErr := file.Read(buffer)
		if read > 0 {
			dataURL := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(buffer[:read])
			if _, appendErr := a.execIntegrationTool(ctx, acct, "media_upload_append", map[string]any{
				"command": "APPEND", "media_id": mediaID, "segment_index": segment, "media": dataURL,
			}); appendErr != nil {
				return appendErr, nil
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return mcpError("read X video segment: " + readErr.Error()), nil
		}
	}
	finalized, finalizeErr := a.execIntegrationTool(ctx, acct, "media_upload_finalize", map[string]any{"command": "FINALIZE", "media_id": mediaID})
	if finalizeErr != nil {
		return finalizeErr, nil
	}
	recordCreativeAsset(ctx, args, acct, mediaID, "video")
	return map[string]any{"id": mediaID, "media_id": mediaID, "kind": "video", "status": "processing", "provider_response": finalized}, nil
}

func (xRedditAdapter) CreativeCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	if acct.Platform == "x" {
		text := stringArgAny(args, "primary_text")
		if text == "" {
			text = stringArgAny(args, "headline")
		}
		input := map[string]any{"account_id": acct.NativeAccountID, "text": text, "nullcast": true}
		format := strings.ToLower(stringArgAny(args, "format"))
		if format == "video" {
			input["media_ids"] = stringArgAny(args, "video_id")
		} else if format == "carousel" {
			cards, _ := args["cards"].([]any)
			mediaIDs := make([]string, 0, len(cards))
			for _, value := range cards {
				card := asMap(value)
				if mediaID := firstString(card, "image_hash"); mediaID != "" {
					mediaIDs = append(mediaIDs, mediaID)
				}
			}
			if len(mediaIDs) < 2 || len(mediaIDs) > 4 {
				return mcpError("X multi-image creatives require 2 to 4 cards with provider media IDs in image_hash"), nil
			}
			input["media_ids"] = strings.Join(mediaIDs, ",")
		} else if media := stringArgAny(args, "image_hash"); media != "" {
			input["media_ids"] = media
		}
		if cardURI := stringArgAny(asMap(args["platform_options"]), "card_uri"); cardURI != "" {
			input["card_uri"] = cardURI
		} else if destination := stringArgAny(args, "destination_url"); destination != "" && !strings.Contains(text, destination) {
			input["text"] = strings.TrimSpace(text + " " + destination)
		}
		mergeOptions(input, args)
		parsed, callErr := a.execIntegrationTool(ctx, acct, def.CreativeCreateTool, input)
		if callErr != nil {
			return callErr, nil
		}
		id := firstString(asMap(parsed), "id_str", "id", "tweet_id")
		if data := asMap(asMap(parsed)["data"]); id == "" {
			id = firstString(data, "id_str", "id", "tweet_id")
		}
		return map[string]any{"id": id, "tweet_id": id, "status": "ready", "provider_response": parsed}, nil
	}
	profile, resourceErr := a.resolveResourceChoice(ctx, acct, "publishing_identity", resourceIdentity, "reddit_profile", int64(intArg(args, "identity_resource_id", 0)))
	if resourceErr != nil {
		return resourceErr, nil
	}
	format := strings.ToUpper(stringArgAny(args, "format"))
	creative := map[string]any{"type": format, "headline": stringArgAny(args, "headline")}
	if format == "LINK" {
		creative["type"] = "TEXT"
	}
	if body := stringArgAny(args, "primary_text"); body != "" {
		creative["body"] = body
		creative["text_format"] = "PLAIN_TEXT"
	}
	if destination := stringArgAny(args, "destination_url"); destination != "" {
		creative["destination"] = map[string]any{"type": "URL", "url": destination}
	}
	if format == "IMAGE" {
		creative["image"] = map[string]any{"media": map[string]any{"type": "URL", "url": stringArgAny(args, "image_url")}}
	}
	if format == "VIDEO" {
		creative["video"] = map[string]any{"media": map[string]any{"type": "URL", "url": stringArgAny(args, "video_id")}}
		if thumbnail := stringArgAny(args, "thumbnail_url", "thumbnail_hash"); thumbnail != "" {
			creative["thumbnail"] = map[string]any{"media": map[string]any{"type": "URL", "url": thumbnail}}
		}
	}
	if format == "CAROUSEL" {
		cards, _ := args["cards"].([]any)
		if len(cards) < 2 || len(cards) > 7 {
			return mcpError("Reddit carousel creatives require 2 to 7 cards"), nil
		}
		carousel := make([]any, 0, len(cards))
		for _, value := range cards {
			card := asMap(value)
			imageURL := firstString(card, "image_url", "image_hash")
			destination := firstString(card, "destination_url")
			if imageURL == "" || destination == "" {
				return mcpError("each Reddit carousel card requires image_url and destination_url"), nil
			}
			carousel = append(carousel, map[string]any{
				"destination": map[string]any{"type": "URL", "url": destination},
				"image":       map[string]any{"media": map[string]any{"type": "URL", "url": imageURL}},
				"caption":     firstString(card, "headline", "description"),
			})
		}
		creative["carousel"] = carousel
		if thumbnail := stringArgAny(args, "thumbnail_url", "thumbnail_hash"); thumbnail != "" {
			creative["thumbnail"] = map[string]any{"media": map[string]any{"type": "URL", "url": thumbnail}}
		}
	}
	data := redditData(args, map[string]any{"allow_comments": true, "creative": creative})
	parsed, callErr := a.execIntegrationTool(ctx, acct, def.CreativeCreateTool, map[string]any{"profile_id": profile.NativeID, "data": data})
	if callErr != nil {
		return callErr, nil
	}
	job := asMap(asMap(parsed)["data"])
	if len(job) == 0 {
		job = asMap(parsed)
	}
	jobID := firstString(job, "id", "post_creation_job_id")
	return map[string]any{
		"id": jobID, "job_id": jobID, "post_id": firstString(job, "post_id"),
		"status": strings.ToLower(firstString(job, "status")), "provider_response": parsed,
	}, nil
}

func (xRedditAdapter) CreativeGet(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	id := stringArgAny(args, "creative_id")
	if id == "" {
		return mcpError("creative_id required"), nil
	}
	if acct.Platform == "x" {
		return a.execOrErr(ctx, acct, def.CreativeGetTool, map[string]any{"account_id": acct.NativeAccountID, "tweet_ids": id})
	}
	return a.execOrErr(ctx, acct, def.CreativeGetTool, map[string]any{"post_id": id})
}

func (xRedditAdapter) CreativeDelete(_ *App, _ *sdk.AppCtx, _ *adAccount, _ *platformDef, _ map[string]any) (any, error) {
	return mcpError("provider creative content cannot be deleted safely; archive ads that reference it"), nil
}

func (xRedditAdapter) CreativeAssetStatus(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	id := stringArgAny(args, "asset_id")
	if id == "" {
		return mcpError("asset_id required"), nil
	}
	if acct.Platform == "reddit" {
		return a.execOrErr(ctx, acct, def.CreativeAssetStatusTool, map[string]any{"post_creation_job_id": id})
	}
	return a.execOrErr(ctx, acct, "media_upload_status", map[string]any{"command": "STATUS", "media_id": id})
}

func (xRedditAdapter) CreativeAssetDelete(_ *App, _ *sdk.AppCtx, _ *adAccount, _ *platformDef, _ map[string]any) (any, error) {
	return mcpError("uploaded asset deletion is not exposed by this provider workflow"), nil
}

func (xRedditAdapter) CreativeList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	if acct.Platform == "x" {
		input := map[string]any{"account_id": acct.NativeAccountID}
		if limit := intArg(args, "limit", 0); limit > 0 {
			input["count"] = limit
		}
		if after := stringArgAny(args, "after"); after != "" {
			input["cursor"] = after
		}
		return a.execOrErr(ctx, acct, def.CreativeListTool, input)
	}
	profile, errOut := a.resolveResourceChoice(ctx, acct, "publishing_identity", resourceIdentity, "reddit_profile", 0)
	if errOut != nil {
		return errOut, nil
	}
	input := map[string]any{"profile_id": profile.NativeID}
	if limit := intArg(args, "limit", 0); limit > 0 {
		input["page.size"] = limit
	}
	if after := stringArgAny(args, "after"); after != "" {
		input["next_url"] = after
	}
	return a.execOrErr(ctx, acct, def.CreativeListTool, input)
}

func (xRedditAdapter) AudienceList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	input := map[string]any{}
	if acct.Platform == "x" {
		input["account_id"] = acct.NativeAccountID
	} else {
		input["ad_account_id"] = acct.NativeAccountID
	}
	if limit := intArg(args, "limit", 0); limit > 0 {
		if acct.Platform == "x" {
			input["count"] = limit
		} else {
			input["page.size"] = limit
		}
	}
	if after := stringArgAny(args, "after"); after != "" {
		if acct.Platform == "x" {
			input["cursor"] = after
		} else {
			input["next_url"] = after
		}
	}
	return a.execOrErr(ctx, acct, def.AudienceListTool, input)
}

func (xRedditAdapter) AudienceCreateCustom(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	name := stringArgAny(args, "name")
	if name == "" {
		return mcpError("name required"), nil
	}
	if acct.Platform == "x" {
		input := map[string]any{"account_id": acct.NativeAccountID, "name": name, "list_type": "EMAIL"}
		mergeOptions(input, args)
		return a.execOrErr(ctx, acct, def.AudienceCreateCustomTool, input)
	}
	data := redditData(args, map[string]any{"name": name})
	return a.execOrErr(ctx, acct, def.AudienceCreateCustomTool, map[string]any{"ad_account_id": acct.NativeAccountID, "data": data})
}

func (xRedditAdapter) AudienceCreateLookalike(_ *App, _ *sdk.AppCtx, acct *adAccount, _ *platformDef, _ map[string]any) (any, error) {
	return mcpError(acct.Platform + " lookalike creation is not available through the current public generic endpoint; use native targeting expansion where supported"), nil
}

func numericString(value any) string {
	switch v := value.(type) {
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return toString(value)
	}
}
