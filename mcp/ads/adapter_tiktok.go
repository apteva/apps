package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// tiktokAdapter drives TikTok Marketing API v1.3 through the tiktok-ads
// catalog entry. TikTok differs from the other networks in ways that shape
// this file:
//
//   - Every response is {code, message, request_id, data}; lists live under
//     data.list with data.page_info {page, page_size, total_number, total_page}.
//     The catalog maps a non-zero code to a failed result, so only successful
//     bodies reach these helpers.
//   - Status is a separate write (…/status/update/ with ENABLE, DISABLE or
//     DELETE); the update endpoints do not take it.
//   - Budgets and bids are in the account currency, not micros.
//   - The creative is part of the ad: there is no creative object to create
//     first, so ad_create takes the creative fields directly, like Google.
//   - An ad posts as an identity (identity_id + identity_type), which the
//     resource registry discovers and resolves like other identities.
type tiktokAdapter struct{}

const (
	tiktokPageSize        = 100
	tiktokMaxPages        = 50
	tiktokReportPageSize  = 1000
	tiktokReportChunkDays = 30
	tiktokIdentityType    = "tiktok_identity"
	tiktokAudienceType    = "tiktok_audience"
)

// tiktokData returns the data object of a TikTok response body.
func tiktokData(parsed any) map[string]any {
	if data := asMap(asMap(parsed)["data"]); data != nil {
		return data
	}
	return map[string]any{}
}

// tiktokRows returns data.list, falling back to the shared row reader.
func tiktokRows(parsed any) []map[string]any {
	data := tiktokData(parsed)
	raw, _ := data["list"].([]any)
	if raw == nil {
		// identity/get lists under data.identity_list.
		raw, _ = data["identity_list"].([]any)
	}
	if raw == nil {
		return resultRows(parsed)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if row := asMap(item); row != nil {
			out = append(out, row)
		}
	}
	return out
}

// tiktokNextPage returns the next page number as a string, or "" when the
// current page is the last one.
func tiktokNextPage(parsed any) string {
	info := asMap(tiktokData(parsed)["page_info"])
	page := int(numericArgAny(info["page"]))
	total := int(numericArgAny(info["total_page"]))
	if page <= 0 || total <= 0 || page >= total {
		return ""
	}
	return strconv.Itoa(page + 1)
}

func tiktokPageArgs(input, args map[string]any) {
	size := intArg(args, "limit", 0)
	if size <= 0 || size > 1000 {
		size = tiktokPageSize
	}
	input["page_size"] = size
	input["page"] = 1
	if after := stringArgAny(args, "after"); after != "" {
		if page, err := strconv.Atoi(after); err == nil && page > 0 {
			input["page"] = page
		}
	}
}

// tiktokAllRows follows page_info until the last page.
func (a *App) tiktokAllRows(ctx *sdk.AppCtx, acct *adAccount, tool string, input map[string]any) ([]map[string]any, map[string]any) {
	rows := make([]map[string]any, 0)
	if _, ok := input["page_size"]; !ok {
		input["page_size"] = tiktokPageSize
	}
	for page := 1; page <= tiktokMaxPages; page++ {
		input["page"] = page
		parsed, errOut := a.execIntegrationTool(ctx, acct, tool, input)
		if errOut != nil {
			return nil, errOut
		}
		rows = append(rows, tiktokRows(parsed)...)
		if tiktokNextPage(parsed) == "" {
			return rows, nil
		}
	}
	return nil, mcpError(tool + " pagination exceeded the safety limit")
}

// tiktokStatus maps the normalized status to TikTok's operation status.
func tiktokStatus(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "ACTIVE", "ENABLE", "ENABLED":
		return "ENABLE"
	case "DELETED", "DELETE", "ARCHIVED":
		return "DELETE"
	default:
		return "DISABLE"
	}
}

// tiktokNormalizedStatus maps TikTok's operation_status and secondary_status
// to the ACTIVE / PAUSED / DELETED values the rest of the app compares.
func tiktokNormalizedStatus(row map[string]any) string {
	if strings.Contains(strings.ToUpper(firstString(row, "secondary_status")), "DELETE") {
		return "DELETED"
	}
	switch strings.ToUpper(firstString(row, "operation_status", "status")) {
	case "ENABLE":
		return "ACTIVE"
	case "DELETE":
		return "DELETED"
	case "":
		return ""
	default:
		return "PAUSED"
	}
}

func tiktokObjective(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "traffic", "link_clicks", "landing_page_views":
		return "TRAFFIC"
	case "sales", "conversions", "value":
		return "WEB_CONVERSIONS"
	case "leads":
		return "LEAD_GENERATION"
	case "engagement", "post_engagement", "page_likes":
		return "ENGAGEMENT"
	case "awareness", "reach", "impressions":
		return "REACH"
	case "video_views", "thruplay":
		return "VIDEO_VIEWS"
	case "app_promotion", "app_installs":
		return "APP_PROMOTION"
	}
	return strings.ToUpper(strings.TrimSpace(value))
}

// tiktokOptimization maps the normalized optimization goal to TikTok's
// optimization_goal and the billing_event TikTok pairs it with.
func tiktokOptimization(value string) (goal, billing string) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "link_clicks", "":
		return "CLICK", "CPC"
	case "landing_page_views":
		return "TRAFFIC_LANDING_PAGE_VIEW", "OCPM"
	case "conversions":
		return "CONVERT", "OCPM"
	case "value":
		return "VALUE", "OCPM"
	case "leads":
		return "LEAD_GENERATION", "OCPM"
	case "reach":
		return "REACH", "CPM"
	case "impressions":
		return "SHOW", "CPM"
	case "thruplay":
		return "ENGAGED_VIEW", "CPV"
	case "app_installs":
		return "INSTALL", "OCPM"
	case "post_engagement", "page_likes":
		return "FOLLOWERS", "OCPM"
	}
	return strings.ToUpper(strings.TrimSpace(value)), ""
}

func centsToUnits(cents int) float64 { return float64(cents) / 100 }

// tiktokBudgetCents renders a TikTok budget (account currency units) as the
// cents string the normalized pages use, for the given budget mode.
func tiktokBudgetCents(row map[string]any, mode string) string {
	if !strings.EqualFold(firstString(row, "budget_mode"), mode) {
		return ""
	}
	budget := numericArgAny(row["budget"])
	if budget <= 0 {
		return ""
	}
	return strconv.FormatInt(int64(budget*100+0.5), 10)
}

// tiktokScheduleTime converts an RFC 3339 or date-time input to TikTok's
// "yyyy-MM-dd HH:mm:ss" in UTC.
func tiktokScheduleTime(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC().Format("2006-01-02 15:04:05"), nil
		}
	}
	return "", fmt.Errorf("time %q is not RFC 3339", value)
}

// tiktokMerge overlays platform_options onto a TikTok request, keeping the
// account and parent identifiers this app resolved.
func tiktokMerge(input, args map[string]any) map[string]any {
	for key, value := range asMap(args["platform_options"]) {
		switch key {
		case "advertiser_id", "campaign_id", "adgroup_id", "ad_id", "ad_ids", "adgroup_ids", "campaign_ids", "identity_id", "identity_type":
			continue
		}
		input[key] = value
	}
	return input
}

func tiktokCreated(parsed any, field string) map[string]any {
	data := tiktokData(parsed)
	id := firstString(data, field)
	if id == "" {
		if ids := anySlice(data[field+"s"]); len(ids) > 0 {
			id = toString(ids[0])
		}
	}
	return map[string]any{"id": id, field: id, "provider_response": parsed}
}

func tiktokPage(parsed any, entity string) map[string]any {
	rows := tiktokRows(parsed)
	data := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		item := map[string]any{
			"status":           tiktokNormalizedStatus(row),
			"effective_status": firstString(row, "secondary_status"),
			"campaign_id":      firstString(row, "campaign_id"),
		}
		switch entity {
		case "campaign":
			item["id"], item["name"] = firstString(row, "campaign_id"), firstString(row, "campaign_name")
			item["objective"] = firstString(row, "objective_type")
			item["daily_budget"] = tiktokBudgetCents(row, "BUDGET_MODE_DAY")
			item["lifetime_budget"] = tiktokBudgetCents(row, "BUDGET_MODE_TOTAL")
			delete(item, "campaign_id")
		case "adset":
			item["id"], item["name"] = firstString(row, "adgroup_id"), firstString(row, "adgroup_name")
			item["optimization_goal"] = firstString(row, "optimization_goal")
			item["billing_event"] = firstString(row, "billing_event")
			item["daily_budget"] = tiktokBudgetCents(row, "BUDGET_MODE_DAY")
			item["lifetime_budget"] = tiktokBudgetCents(row, "BUDGET_MODE_TOTAL")
			item["targeting"] = map[string]any{
				"location_ids": row["location_ids"], "languages": row["languages"],
				"age_groups": row["age_groups"], "gender": row["gender"],
			}
		case "ad":
			item["id"], item["name"] = firstString(row, "ad_id"), firstString(row, "ad_name")
			item["adset_id"] = firstString(row, "adgroup_id")
			creative := map[string]any{
				"id": firstString(row, "ad_id"), "name": firstString(row, "ad_name"),
				"format": firstString(row, "ad_format"), "primary_text": firstString(row, "ad_text"),
				"video_id": firstString(row, "video_id"), "image_ids": row["image_ids"],
				"destination_url": firstString(row, "landing_page_url"),
				"call_to_action":  firstString(row, "call_to_action"),
				"identity_id":     firstString(row, "identity_id"),
			}
			item["creative"] = creative
		}
		if item["id"] != "" {
			data = append(data, item)
		}
	}
	var next any
	if page := tiktokNextPage(parsed); page != "" {
		next = page
	}
	return map[string]any{"data": data, "next_cursor": next}
}

func (tiktokAdapter) ListAccounts(_ *App, ctx *sdk.AppCtx, row *pendingRow, def *platformDef) ([]map[string]any, error) {
	parsed, err := pendingIntegrationCall(ctx, row, def.ListAccountsTool, map[string]any{})
	if err != nil {
		return nil, err
	}
	advertisers := tiktokRows(parsed)
	out := make([]map[string]any, 0, len(advertisers))
	ids := make([]any, 0, len(advertisers))
	for _, advertiser := range advertisers {
		id := firstString(advertiser, "advertiser_id")
		if id == "" {
			continue
		}
		ids = append(ids, id)
		out = append(out, map[string]any{"id": id, "name": firstString(advertiser, "advertiser_name", "name")})
	}
	// Currency and timezone come from advertiser/info. A failure here leaves
	// the accounts selectable without them rather than blocking the picker.
	byID := map[string]map[string]any{}
	for start := 0; start < len(ids); start += 100 {
		end := start + 100
		if end > len(ids) {
			end = len(ids)
		}
		info, infoErr := pendingIntegrationCall(ctx, row, "advertiser_info", map[string]any{
			"advertiser_ids": ids[start:end],
			"fields":         []any{"advertiser_id", "name", "currency", "timezone", "display_timezone", "status"},
		})
		if infoErr != nil {
			break
		}
		for _, detail := range tiktokRows(info) {
			byID[firstString(detail, "advertiser_id")] = detail
		}
	}
	for _, account := range out {
		if detail := byID[toString(account["id"])]; detail != nil {
			if account["name"] == "" {
				account["name"] = firstString(detail, "name")
			}
			account["currency"] = firstString(detail, "currency")
			account["timezone"] = firstString(detail, "timezone", "display_timezone")
			account["status"] = firstString(detail, "status")
		}
	}
	return out, nil
}

func (tiktokAdapter) CampaignCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	name := stringArgAny(args, "name")
	if name == "" {
		return mcpError("name required"), nil
	}
	objective := tiktokObjective(stringArgAny(args, "objective"))
	if objective == "" {
		return mcpError("objective required"), nil
	}
	input := map[string]any{
		"advertiser_id": acct.NativeAccountID, "campaign_name": name, "objective_type": objective,
		"budget_mode": "BUDGET_MODE_INFINITE", "operation_status": tiktokStatus(stringArgAny(args, "status")),
	}
	if v := intArg(args, "daily_budget_cents", 0); v > 0 {
		input["budget_mode"], input["budget"] = "BUDGET_MODE_DAY", centsToUnits(v)
	}
	if v := intArg(args, "lifetime_budget_cents", 0); v > 0 {
		input["budget_mode"], input["budget"] = "BUDGET_MODE_TOTAL", centsToUnits(v)
	}
	if industries := anySlice(args["special_ad_categories"]); len(industries) > 0 {
		input["special_industries"] = industries
	}
	tiktokMerge(input, args)
	parsed, errOut := a.execIntegrationTool(ctx, acct, def.CampaignCreateTool, input)
	if errOut != nil {
		return errOut, nil
	}
	return tiktokCreated(parsed, "campaign_id"), nil
}

func (tiktokAdapter) CampaignList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	input := map[string]any{"advertiser_id": acct.NativeAccountID, "filtering": map[string]any{"primary_status": "STATUS_ALL"}}
	tiktokPageArgs(input, args)
	parsed, errOut := a.execIntegrationTool(ctx, acct, def.CampaignListTool, input)
	if errOut != nil {
		return errOut, nil
	}
	page := tiktokPage(parsed, "campaign")
	if status := strings.ToUpper(stringArgAny(args, "status")); status != "" {
		filtered := make([]map[string]any, 0)
		for _, item := range page["data"].([]map[string]any) {
			if item["status"] == status {
				filtered = append(filtered, item)
			}
		}
		page["data"] = filtered
	}
	return page, nil
}

func (tiktokAdapter) CampaignPerformance(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	return xRedditAdapter{}.CampaignPerformance(a, ctx, acct, def, args)
}

// tiktokStatusUpdate writes operation_status through the entity's
// status endpoint.
func (a *App) tiktokStatusUpdate(ctx *sdk.AppCtx, acct *adAccount, entity, id, status string) (any, map[string]any) {
	tool, field := "", ""
	switch entity {
	case "campaign":
		tool, field = "campaign_update_status", "campaign_ids"
	case "adset":
		tool, field = "adgroup_update_status", "adgroup_ids"
	case "ad":
		tool, field = "ad_update_status", "ad_ids"
	default:
		return nil, mcpError("unsupported TikTok entity " + entity)
	}
	return a.execIdempotentUpdate(ctx, acct, tool, map[string]any{
		"advertiser_id": acct.NativeAccountID, field: []any{id}, "operation_status": status,
	})
}

func (tiktokAdapter) CampaignUpdate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	id := stringArgAny(args, "campaign_id")
	if id == "" {
		return mcpError("campaign_id required"), nil
	}
	input := map[string]any{"advertiser_id": acct.NativeAccountID, "campaign_id": id}
	putString(input, "campaign_name", args, "name")
	if v := intArg(args, "daily_budget_cents", 0); v > 0 {
		input["budget"] = centsToUnits(v)
	}
	if v := intArg(args, "lifetime_budget_cents", 0); v > 0 {
		input["budget"] = centsToUnits(v)
	}
	tiktokMerge(input, args)
	out := map[string]any{"id": id}
	if len(input) > 2 {
		parsed, errOut := a.execIdempotentUpdate(ctx, acct, def.CampaignUpdateTool, input)
		if errOut != nil {
			return errOut, nil
		}
		out["provider_response"] = parsed
	}
	if status := stringArgAny(args, "status"); status != "" {
		parsed, errOut := a.tiktokStatusUpdate(ctx, acct, "campaign", id, tiktokStatus(status))
		if errOut != nil {
			return errOut, nil
		}
		out["status_response"] = parsed
	}
	return out, nil
}

func (tiktokAdapter) CampaignDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, _ *platformDef, args map[string]any) (any, error) {
	id := stringArgAny(args, "campaign_id")
	if id == "" {
		return mcpError("campaign_id required"), nil
	}
	parsed, errOut := a.tiktokStatusUpdate(ctx, acct, "campaign", id, "DELETE")
	if errOut != nil {
		return errOut, nil
	}
	return map[string]any{"id": id, "deleted": true, "provider_response": parsed}, nil
}

func (tiktokAdapter) AdSetCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	name, campaignID := stringArgAny(args, "name"), stringArgAny(args, "campaign_id")
	if name == "" || campaignID == "" {
		return mcpError("name and campaign_id required"), nil
	}
	goal, billing := tiktokOptimization(stringArgAny(args, "optimization_goal"))
	input := map[string]any{
		"advertiser_id": acct.NativeAccountID, "campaign_id": campaignID, "adgroup_name": name,
		"placement_type": "PLACEMENT_TYPE_AUTOMATIC", "promotion_type": "WEBSITE",
		"optimization_goal": goal, "billing_event": billing, "bid_type": "BID_TYPE_NO_BID",
		"pacing": "PACING_MODE_SMOOTH", "operation_status": tiktokStatus(stringArgAny(args, "status")),
	}
	switch {
	case intArg(args, "daily_budget_cents", 0) > 0:
		input["budget_mode"], input["budget"] = "BUDGET_MODE_DAY", centsToUnits(intArg(args, "daily_budget_cents", 0))
	case intArg(args, "lifetime_budget_cents", 0) > 0:
		input["budget_mode"], input["budget"] = "BUDGET_MODE_TOTAL", centsToUnits(intArg(args, "lifetime_budget_cents", 0))
	}
	if bid := intArg(args, "bid_amount_cents", 0); bid > 0 {
		input["bid_type"], input["bid_price"] = "BID_TYPE_CUSTOM", centsToUnits(bid)
	}
	start, err := tiktokScheduleTime(stringArgAny(args, "start_time"))
	if err != nil {
		return mcpError("start_time: " + err.Error()), nil
	}
	end, err := tiktokScheduleTime(stringArgAny(args, "end_time"))
	if err != nil {
		return mcpError("end_time: " + err.Error()), nil
	}
	if start == "" {
		start = time.Now().UTC().Add(5 * time.Minute).Format("2006-01-02 15:04:05")
	}
	input["schedule_start_time"] = start
	input["schedule_type"] = "SCHEDULE_FROM_NOW"
	if end != "" {
		input["schedule_type"], input["schedule_end_time"] = "SCHEDULE_START_END", end
	}
	// Targeting fields use TikTok's own names (location_ids, languages,
	// age_groups, gender, …), as returned by adset_list.
	for key, value := range asMap(args["targeting"]) {
		input[key] = value
	}
	tiktokMerge(input, args)
	if stringArgAny(input, "budget_mode") == "" {
		return mcpError("TikTok ad groups need daily_budget_cents or lifetime_budget_cents"), nil
	}
	if len(anySlice(input["location_ids"])) == 0 {
		return mcpError("TikTok ad groups need targeting.location_ids (TikTok location IDs)"), nil
	}
	if stringArgAny(input, "billing_event") == "" {
		return mcpError("set platform_options.billing_event for TikTok optimization goal " + goal), nil
	}
	parsed, errOut := a.execIntegrationTool(ctx, acct, def.AdSetCreateTool, input)
	if errOut != nil {
		return errOut, nil
	}
	return tiktokCreated(parsed, "adgroup_id"), nil
}

func (tiktokAdapter) AdSetList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	filtering := map[string]any{"primary_status": "STATUS_ALL"}
	if id := stringArgAny(args, "campaign_id"); id != "" {
		filtering["campaign_ids"] = []any{id}
	}
	input := map[string]any{"advertiser_id": acct.NativeAccountID, "filtering": filtering}
	tiktokPageArgs(input, args)
	parsed, errOut := a.execIntegrationTool(ctx, acct, def.AdSetListTool, input)
	if errOut != nil {
		return errOut, nil
	}
	return tiktokPage(parsed, "adset"), nil
}

func (tiktokAdapter) AdSetUpdate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	id := stringArgAny(args, "adset_id")
	if id == "" {
		return mcpError("adset_id required"), nil
	}
	input := map[string]any{"advertiser_id": acct.NativeAccountID, "adgroup_id": id}
	putString(input, "adgroup_name", args, "name")
	if v := intArg(args, "daily_budget_cents", 0); v > 0 {
		input["budget"] = centsToUnits(v)
	}
	if v := intArg(args, "lifetime_budget_cents", 0); v > 0 {
		input["budget"] = centsToUnits(v)
	}
	if bid := intArg(args, "bid_amount_cents", 0); bid > 0 {
		input["bid_price"] = centsToUnits(bid)
	}
	for field, key := range map[string]string{"start_time": "schedule_start_time", "end_time": "schedule_end_time"} {
		value, err := tiktokScheduleTime(stringArgAny(args, field))
		if err != nil {
			return mcpError(field + ": " + err.Error()), nil
		}
		if value != "" {
			input[key] = value
		}
	}
	for key, value := range asMap(args["targeting"]) {
		input[key] = value
	}
	tiktokMerge(input, args)
	out := map[string]any{"id": id}
	if len(input) > 2 {
		parsed, errOut := a.execIdempotentUpdate(ctx, acct, def.AdSetUpdateTool, input)
		if errOut != nil {
			return errOut, nil
		}
		out["provider_response"] = parsed
	}
	if status := stringArgAny(args, "status"); status != "" {
		parsed, errOut := a.tiktokStatusUpdate(ctx, acct, "adset", id, tiktokStatus(status))
		if errOut != nil {
			return errOut, nil
		}
		out["status_response"] = parsed
	}
	return out, nil
}

func (tiktokAdapter) AdSetDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, _ *platformDef, args map[string]any) (any, error) {
	id := stringArgAny(args, "adset_id")
	if id == "" {
		return mcpError("adset_id required"), nil
	}
	parsed, errOut := a.tiktokStatusUpdate(ctx, acct, "adset", id, "DELETE")
	if errOut != nil {
		return errOut, nil
	}
	return map[string]any{"id": id, "deleted": true, "provider_response": parsed}, nil
}

// tiktokCreative builds one entry of ad/create's creatives array from the
// normalized creative fields.
func (a *App) tiktokCreative(ctx *sdk.AppCtx, acct *adAccount, args map[string]any) (map[string]any, map[string]any) {
	identity, errOut := a.resolveResourceChoice(ctx, acct, "publishing_identity", resourceIdentity, tiktokIdentityType, int64(intArg(args, "identity_resource_id", 0)))
	if errOut != nil {
		return nil, errOut
	}
	identityType := firstString(identity.Metadata, "identity_type")
	if identityType == "" {
		identityType = "CUSTOMIZED_USER"
	}
	creative := map[string]any{
		"ad_name": stringArgAny(args, "name"), "identity_id": identity.NativeID, "identity_type": identityType,
	}
	if bc := firstString(identity.Metadata, "identity_authorized_bc_id"); bc != "" {
		creative["identity_authorized_bc_id"] = bc
	}
	putString(creative, "ad_text", args, "primary_text")
	putString(creative, "landing_page_url", args, "destination_url")
	if cta := strings.ToUpper(stringArgAny(args, "call_to_action")); cta != "" {
		creative["call_to_action"] = cta
	}
	format := strings.ToLower(stringArgAny(args, "format"))
	if format == "" && stringArgAny(args, "video_id") != "" {
		format = "video"
	}
	switch format {
	case "video":
		video := stringArgAny(args, "video_id")
		if video == "" {
			return nil, mcpError("TikTok video ads need video_id from creative_upload")
		}
		creative["ad_format"], creative["video_id"] = "SINGLE_VIDEO", video
		if cover := stringArgAny(args, "thumbnail_hash", "image_hash"); cover != "" {
			creative["image_ids"] = []any{cover}
		}
	case "image", "":
		image := stringArgAny(args, "image_hash")
		if image == "" {
			return nil, mcpError("TikTok image ads need image_hash (an image_id from creative_upload)")
		}
		creative["ad_format"], creative["image_ids"] = "SINGLE_IMAGE", []any{image}
	case "carousel":
		images := make([]any, 0)
		for _, value := range anySlice(args["cards"]) {
			if id := firstString(asMap(value), "image_hash"); id != "" {
				images = append(images, id)
			}
		}
		if len(images) < 2 {
			return nil, mcpError("TikTok carousel ads need at least 2 cards with image_hash")
		}
		creative["ad_format"], creative["image_ids"] = "CAROUSEL_ADS", images
	default:
		return nil, mcpError("TikTok ads support format video, image or carousel")
	}
	if text := stringArgAny(creative, "ad_text"); text == "" {
		return nil, mcpError("TikTok ads need primary_text")
	}
	if nested := asMap(asMap(args["platform_options"])["creative"]); len(nested) > 0 {
		for key, value := range nested {
			if key != "identity_id" && key != "identity_type" {
				creative[key] = value
			}
		}
	}
	return creative, nil
}

func (tiktokAdapter) AdCreate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	adSetID := stringArgAny(args, "adset_id")
	if adSetID == "" {
		return mcpError("adset_id required"), nil
	}
	creative, errOut := a.tiktokCreative(ctx, acct, args)
	if errOut != nil {
		return errOut, nil
	}
	input := map[string]any{"advertiser_id": acct.NativeAccountID, "adgroup_id": adSetID, "creatives": []any{creative}}
	parsed, errOut := a.execIntegrationTool(ctx, acct, def.AdCreateTool, input)
	if errOut != nil {
		return errOut, nil
	}
	out := tiktokCreated(parsed, "ad_id")
	// TikTok creates ads enabled. Pause them unless ACTIVE was asked for, so
	// nothing serves before activation.
	if id := toString(out["id"]); id != "" && tiktokStatus(stringArgAny(args, "status")) != "ENABLE" {
		if _, statusErr := a.tiktokStatusUpdate(ctx, acct, "ad", id, "DISABLE"); statusErr != nil {
			out["warning"] = "ad was created but could not be paused: " + providerFailureText(statusErr)
		}
	}
	return out, nil
}

func (tiktokAdapter) AdList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	filtering := map[string]any{"primary_status": "STATUS_ALL"}
	if id := stringArgAny(args, "adset_id"); id != "" {
		filtering["adgroup_ids"] = []any{id}
	}
	input := map[string]any{"advertiser_id": acct.NativeAccountID, "filtering": filtering}
	tiktokPageArgs(input, args)
	parsed, errOut := a.execIntegrationTool(ctx, acct, def.AdListTool, input)
	if errOut != nil {
		return errOut, nil
	}
	return tiktokPage(parsed, "ad"), nil
}

// tiktokEntity reads one campaign, ad group or ad by id.
func (a *App) tiktokEntity(ctx *sdk.AppCtx, acct *adAccount, entity, id string) (map[string]any, map[string]any) {
	tool, filter, idField := "", "", ""
	switch entity {
	case "campaign":
		tool, filter, idField = "campaign_list", "campaign_ids", "campaign_id"
	case "adset":
		tool, filter, idField = "adgroup_list", "adgroup_ids", "adgroup_id"
	case "ad":
		tool, filter, idField = "ad_list", "ad_ids", "ad_id"
	default:
		return nil, mcpError("unsupported TikTok entity " + entity)
	}
	parsed, errOut := a.execIntegrationTool(ctx, acct, tool, map[string]any{
		"advertiser_id": acct.NativeAccountID,
		"filtering":     map[string]any{filter: []any{id}, "primary_status": "STATUS_ALL"},
		"page":          1, "page_size": 10,
	})
	if errOut != nil {
		return nil, errOut
	}
	for _, row := range tiktokRows(parsed) {
		if firstString(row, idField) == id {
			return row, nil
		}
	}
	return nil, mcpError(fmt.Sprintf("%s %s does not belong to the selected ad account", entity, id))
}

func (tiktokAdapter) AdUpdate(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	id := stringArgAny(args, "ad_id")
	if id == "" {
		return mcpError("ad_id required"), nil
	}
	creative := map[string]any{"ad_id": id}
	putString(creative, "ad_name", args, "name")
	putString(creative, "ad_text", args, "primary_text")
	putString(creative, "landing_page_url", args, "destination_url")
	for key, value := range asMap(asMap(args["platform_options"])["creative"]) {
		if key != "ad_id" {
			creative[key] = value
		}
	}
	out := map[string]any{"id": id}
	if len(creative) > 1 {
		row, errOut := a.tiktokEntity(ctx, acct, "ad", id)
		if errOut != nil {
			return errOut, nil
		}
		parsed, errOut := a.execIdempotentUpdate(ctx, acct, def.AdUpdateTool, map[string]any{
			"advertiser_id": acct.NativeAccountID, "adgroup_id": firstString(row, "adgroup_id"),
			"creatives": []any{creative}, "patch_update": true,
		})
		if errOut != nil {
			return errOut, nil
		}
		out["provider_response"] = parsed
	}
	if status := stringArgAny(args, "status"); status != "" {
		parsed, errOut := a.tiktokStatusUpdate(ctx, acct, "ad", id, tiktokStatus(status))
		if errOut != nil {
			return errOut, nil
		}
		out["status_response"] = parsed
	}
	return out, nil
}

func (tiktokAdapter) AdDelete(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	id := stringArgAny(args, "ad_id")
	if id == "" {
		return mcpError("ad_id required"), nil
	}
	parsed, errOut := a.execIdempotentUpdate(ctx, acct, def.AdDeleteTool, map[string]any{"advertiser_id": acct.NativeAccountID, "ad_ids": []any{id}})
	if errOut != nil {
		return errOut, nil
	}
	return map[string]any{"id": id, "deleted": true, "provider_response": parsed}, nil
}

func tiktokCreativeEmbedded() map[string]any {
	return mcpError("TikTok creatives are part of the ad: pass format, primary_text, destination_url, call_to_action and video_id or image_hash to ad_create")
}

func (tiktokAdapter) CreativeCreate(*App, *sdk.AppCtx, *adAccount, *platformDef, map[string]any) (any, error) {
	return tiktokCreativeEmbedded(), nil
}

func (tiktokAdapter) CreativeGet(a *App, ctx *sdk.AppCtx, acct *adAccount, _ *platformDef, args map[string]any) (any, error) {
	id := stringArgAny(args, "creative_id", "ad_id")
	if id == "" {
		return mcpError("creative_id required (the TikTok ad_id)"), nil
	}
	row, errOut := a.tiktokEntity(ctx, acct, "ad", id)
	if errOut != nil {
		return errOut, nil
	}
	page := tiktokPage(map[string]any{"data": map[string]any{"list": []any{row}}}, "ad")
	items := page["data"].([]map[string]any)
	if len(items) == 0 {
		return mcpError("ad " + id + " not found"), nil
	}
	return items[0]["creative"], nil
}

func (tiktokAdapter) CreativeDelete(*App, *sdk.AppCtx, *adAccount, *platformDef, map[string]any) (any, error) {
	return mcpError("TikTok creatives are part of the ad; delete the ad instead"), nil
}

func (tiktokAdapter) CreativeUpload(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	sourceURL, errOut := resolveSourceURL(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	kind := "image"
	if strings.EqualFold(stringArgAny(args, "kind"), "video") {
		kind = "video"
	}
	input := map[string]any{"advertiser_id": acct.NativeAccountID, "upload_type": "UPLOAD_BY_URL"}
	tool, idField := def.CreativeUploadImageTool, "image_id"
	if kind == "video" {
		tool, idField = def.CreativeUploadVideoTool, "video_id"
		input["video_url"] = sourceURL
	} else {
		input["image_url"] = sourceURL
	}
	tiktokMerge(input, args)
	parsed, callErr := a.execIntegrationTool(ctx, acct, tool, input)
	if callErr != nil {
		return callErr, nil
	}
	// image upload returns data as an object; video upload returns a list.
	data := tiktokData(parsed)
	id := firstString(data, idField)
	if id == "" {
		if list := anySlice(asMap(parsed)["data"]); len(list) > 0 {
			id = firstString(asMap(list[0]), idField)
		}
	}
	if id == "" {
		return mcpError("TikTok " + kind + " upload returned no " + idField), nil
	}
	recordCreativeAsset(ctx, args, acct, id, kind)
	status := "ready"
	if kind == "video" {
		status = "processing"
	}
	return map[string]any{"id": id, idField: id, "kind": kind, "status": status, "provider_response": parsed}, nil
}

func (tiktokAdapter) CreativeAssetStatus(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	id := stringArgAny(args, "asset_id")
	if id == "" {
		return mcpError("asset_id required"), nil
	}
	tool, field := "video_info", "video_ids"
	if strings.EqualFold(stringArgAny(args, "kind"), "image") {
		tool, field = "image_info", "image_ids"
	}
	return a.execOrErr(ctx, acct, tool, map[string]any{"advertiser_id": acct.NativeAccountID, field: []any{id}})
}

func (tiktokAdapter) CreativeAssetDelete(*App, *sdk.AppCtx, *adAccount, *platformDef, map[string]any) (any, error) {
	return mcpError("TikTok does not expose deletion of uploaded ad assets"), nil
}

func (tiktokAdapter) CreativeList(*App, *sdk.AppCtx, *adAccount, *platformDef, map[string]any) (any, error) {
	return mcpError("TikTok creatives are part of the ad; use ad_list, where each ad carries its creative"), nil
}

func (tiktokAdapter) AudienceList(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	input := map[string]any{"advertiser_id": acct.NativeAccountID}
	tiktokPageArgs(input, args)
	if size := intArg(input, "page_size", 0); size > 100 {
		input["page_size"] = 100
	}
	parsed, errOut := a.execIntegrationTool(ctx, acct, def.AudienceListTool, input)
	if errOut != nil {
		return errOut, nil
	}
	var next any
	if page := tiktokNextPage(parsed); page != "" {
		next = page
	}
	return map[string]any{"data": tiktokRows(parsed), "next_cursor": next}, nil
}

func (tiktokAdapter) AudienceCreateCustom(*App, *sdk.AppCtx, *adAccount, *platformDef, map[string]any) (any, error) {
	return mcpError("TikTok customer-list audiences must be created with their first member file; this app does not create them yet"), nil
}

func (tiktokAdapter) AudienceCreateLookalike(a *App, ctx *sdk.AppCtx, acct *adAccount, def *platformDef, args map[string]any) (any, error) {
	name, source := stringArgAny(args, "name"), stringArgAny(args, "source_audience_id")
	if name == "" || source == "" {
		return mcpError("name and source_audience_id required"), nil
	}
	opts := asMap(args["platform_options"])
	locations := anySlice(opts["location_ids"])
	if len(locations) == 0 {
		return mcpError("TikTok lookalikes need platform_options.location_ids (TikTok location IDs)"), nil
	}
	size := strings.ToUpper(stringArgAny(opts, "audience_size"))
	if size == "" {
		ratio := numericArgAny(args["ratio"])
		switch {
		case ratio > 0 && ratio <= 0.03:
			size = "NARROW"
		case ratio > 0.06:
			size = "BROAD"
		default:
			size = "BALANCED"
		}
	}
	parsed, errOut := a.execIntegrationTool(ctx, acct, def.AudienceCreateLookalikeTool, map[string]any{
		"advertiser_id": acct.NativeAccountID, "custom_audience_name": name,
		"lookalike_spec": map[string]any{"source_audience_id": source, "location_ids": locations, "audience_size": size},
	})
	if errOut != nil {
		return errOut, nil
	}
	id := firstString(tiktokData(parsed), "custom_audience_id", "audience_id")
	return map[string]any{"id": id, "custom_audience_id": id, "provider_response": parsed}, nil
}

// loadTikTokDeliveryEntity reads the state activation compares.
func (a *App) loadTikTokDeliveryEntity(ctx *sdk.AppCtx, acct *adAccount, campaignID, adSetID, adID, entity string) (deliveryState, map[string]any) {
	id := map[string]string{"campaign": campaignID, "adset": adSetID, "ad": adID}[entity]
	row, errOut := a.tiktokEntity(ctx, acct, entity, id)
	if errOut != nil {
		return deliveryState{}, errOut
	}
	return deliveryState{
		Entity: entity, ID: id,
		ParentID:        firstString(row, "adgroup_id"),
		CampaignID:      firstString(row, "campaign_id"),
		Status:          tiktokNormalizedStatus(row),
		EffectiveStatus: firstString(row, "secondary_status"),
	}, nil
}

// discoverTikTokResources lists identities and custom audiences.
func (a *App) discoverTikTokResources(ctx *sdk.AppCtx, acct *adAccount, kind string) ([]discoveredResource, map[string]any) {
	switch kind {
	case resourceIdentity:
		out := make([]discoveredResource, 0)
		var lastErr map[string]any
		succeeded := false
		// identity/get lists one identity type per call.
		for _, identityType := range []string{"CUSTOMIZED_USER", "TT_USER", "AUTH_CODE"} {
			rows, errOut := a.tiktokAllRows(ctx, acct, "identity_list", map[string]any{
				"advertiser_id": acct.NativeAccountID, "identity_type": identityType,
			})
			if errOut != nil {
				lastErr = errOut
				continue
			}
			succeeded = true
			for _, row := range rows {
				id := firstString(row, "identity_id")
				if id == "" {
					continue
				}
				name := firstString(row, "display_name", "identity_name")
				if name == "" {
					name = id
				}
				metadata := cloneMap(row)
				if firstString(metadata, "identity_type") == "" {
					metadata["identity_type"] = identityType
				}
				out = append(out, discoveredResource{
					Kind: resourceIdentity, ProviderType: tiktokIdentityType, NativeID: id, DisplayName: name,
					Status: "active", Capabilities: []string{"advertise", "publish"}, Metadata: metadata,
				})
			}
		}
		if !succeeded && lastErr != nil {
			return nil, lastErr
		}
		return out, nil
	case resourceAudience:
		rows, errOut := a.tiktokAllRows(ctx, acct, "custom_audience_list", map[string]any{"advertiser_id": acct.NativeAccountID, "page_size": 100})
		if errOut != nil {
			return nil, errOut
		}
		out := make([]discoveredResource, 0, len(rows))
		for _, row := range rows {
			id := firstString(row, "custom_audience_id", "audience_id")
			if id == "" {
				continue
			}
			name := firstString(row, "name", "custom_audience_name")
			if name == "" {
				name = id
			}
			audienceKind := "customer_list"
			if strings.Contains(strings.ToUpper(firstString(row, "audience_type", "type")), "LOOKALIKE") {
				audienceKind = "lookalike"
			}
			metadata := cloneMap(row)
			metadata["type"] = audienceKind
			status := "active"
			if valid, ok := row["is_valid"].(bool); ok && !valid {
				status = "pending"
			}
			if expired, _ := row["is_expired"].(bool); expired {
				status = "deleted"
			}
			out = append(out, discoveredResource{
				Kind: resourceAudience, ProviderType: tiktokAudienceType, NativeID: id, DisplayName: name,
				Status: status, Capabilities: audienceResourceCapabilities("tiktok", audienceKind), Metadata: metadata,
			})
		}
		return out, nil
	}
	return nil, mcpError("unsupported TikTok resource kind: " + kind)
}

// fetchTikTokAnalytics reads report/integrated in 30-day chunks, one day
// per row.
func (a *App) fetchTikTokAnalytics(ctx *sdk.AppCtx, acct *adAccount, request *genericPerformanceRequest) ([]analyticsPoint, map[string]any) {
	levels := map[string][2]string{
		"account":  {"AUCTION_ADVERTISER", "advertiser_id"},
		"campaign": {"AUCTION_CAMPAIGN", "campaign_id"},
		"ad_group": {"AUCTION_ADGROUP", "adgroup_id"},
		"ad":       {"AUCTION_AD", "ad_id"},
	}
	dataLevel, idDimension := levels[request.Level][0], levels[request.Level][1]
	if dataLevel == "" {
		return nil, mcpError("unsupported TikTok report level " + request.Level)
	}
	metrics := []any{"spend", "impressions", "clicks", "reach", "conversion", "video_play_actions"}
	switch request.Level {
	case "campaign":
		metrics = append(metrics, "campaign_name")
	case "ad_group":
		metrics = append(metrics, "campaign_id", "campaign_name", "adgroup_name")
	case "ad":
		metrics = append(metrics, "campaign_id", "campaign_name", "adgroup_id", "adgroup_name", "ad_name")
	}
	wanted := map[string]bool{}
	for _, id := range request.EntityIDs {
		wanted[id] = true
	}
	from, _ := time.Parse("2006-01-02", request.DateFrom)
	to, _ := time.Parse("2006-01-02", request.DateTo)
	points := make([]analyticsPoint, 0)
	for chunkStart := from; !chunkStart.After(to); {
		chunkEnd := chunkStart.AddDate(0, 0, tiktokReportChunkDays-1)
		if chunkEnd.After(to) {
			chunkEnd = to
		}
		for page := 1; page <= maxPerformancePages; page++ {
			parsed, errOut := a.execIntegrationTool(ctx, acct, "report_integrated", map[string]any{
				"advertiser_id": acct.NativeAccountID, "report_type": "BASIC", "data_level": dataLevel,
				"dimensions": []any{idDimension, "stat_time_day"}, "metrics": metrics,
				"start_date": chunkStart.Format("2006-01-02"), "end_date": chunkEnd.Format("2006-01-02"),
				"page": page, "page_size": tiktokReportPageSize,
			})
			if errOut != nil {
				return nil, errOut
			}
			for _, row := range tiktokRows(parsed) {
				point := normalizeTikTokAnalyticsPoint(acct, request.Level, idDimension, row)
				if len(wanted) > 0 && request.Level != "account" && !wanted[point.EntityID] {
					continue
				}
				points = append(points, point)
			}
			if tiktokNextPage(parsed) == "" {
				break
			}
		}
		chunkStart = chunkEnd.AddDate(0, 0, 1)
	}
	return points, nil
}

func normalizeTikTokAnalyticsPoint(acct *adAccount, level, idDimension string, row map[string]any) analyticsPoint {
	dimensions := asMap(row["dimensions"])
	metrics := asMap(row["metrics"])
	entityID := firstString(dimensions, idDimension)
	date := firstString(dimensions, "stat_time_day")
	if len(date) >= 10 {
		date = date[:10]
	}
	point := analyticsPoint{
		Platform: acct.Platform, AdAccountID: acct.ID, Level: level, EntityID: entityID, EntityName: entityID,
		Date: date, Currency: acct.Currency, Timezone: acct.Timezone,
		SpendMicros:     int64(numericArgAny(metrics["spend"])*1_000_000 + 0.5),
		Impressions:     int64(numericArgAny(metrics["impressions"])),
		Reach:           int64(numericArgAny(metrics["reach"])),
		Clicks:          int64(numericArgAny(metrics["clicks"])),
		LinkClicks:      int64(numericArgAny(metrics["clicks"])),
		Conversions:     numericArgAny(metrics["conversion"]),
		VideoViews:      int64(numericArgAny(metrics["video_play_actions"])),
		ProviderMetrics: metrics, FetchedAt: time.Now().UTC().Format(time.RFC3339),
	}
	point.CampaignID, point.CampaignName = firstString(metrics, "campaign_id"), firstString(metrics, "campaign_name")
	point.AdGroupID, point.AdGroupName = firstString(metrics, "adgroup_id"), firstString(metrics, "adgroup_name")
	switch level {
	case "account":
		point.EntityID, point.EntityName = acct.NativeAccountID, "Account"
	case "campaign":
		point.CampaignID = entityID
		if point.CampaignName != "" {
			point.EntityName = point.CampaignName
		}
	case "ad_group":
		point.AdGroupID = entityID
		if point.AdGroupName != "" {
			point.EntityName = point.AdGroupName
		}
	case "ad":
		if name := firstString(metrics, "ad_name"); name != "" {
			point.EntityName = name
		}
	}
	return point
}

// tiktokRateLimitedBody reports TikTok's throttling code 40100, which arrives
// in an HTTP 200 body rather than as a 429.
func tiktokRateLimitedBody(body string) bool {
	var envelope struct {
		Code json.Number `json:"code"`
	}
	if !strings.Contains(body, "40100") || json.Unmarshal([]byte(body), &envelope) != nil {
		return false
	}
	return envelope.Code.String() == "40100"
}
