package main

import (
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) fetchXAnalytics(ctx *sdk.AppCtx, acct *adAccount, request *genericPerformanceRequest) ([]analyticsPoint, map[string]any) {
	entity := map[string]string{"account": "ACCOUNT", "campaign": "CAMPAIGN", "ad_group": "LINE_ITEM", "ad": "PROMOTED_TWEET"}[request.Level]
	from, _ := time.Parse("2006-01-02", request.DateFrom)
	to, _ := time.Parse("2006-01-02", request.DateTo)
	location := time.UTC
	if acct.Timezone != "" {
		if loaded, err := time.LoadLocation(acct.Timezone); err == nil {
			location = loaded
		}
	}
	points := make([]analyticsPoint, 0)
	for chunkStart := from; !chunkStart.After(to); {
		chunkEnd := chunkStart.AddDate(0, 0, 6)
		if chunkEnd.After(to) {
			chunkEnd = to
		}
		startTime := time.Date(chunkStart.Year(), chunkStart.Month(), chunkStart.Day(), 0, 0, 0, 0, location).Format(time.RFC3339)
		endExclusive := chunkEnd.AddDate(0, 0, 1)
		endTime := time.Date(endExclusive.Year(), endExclusive.Month(), endExclusive.Day(), 0, 0, 0, 0, location).Format(time.RFC3339)
		ids := append([]string(nil), request.EntityIDs...)
		if request.Level == "account" {
			ids = []string{acct.NativeAccountID}
		} else if len(ids) == 0 {
			parsed, errOut := a.execIntegrationTool(ctx, acct, "list_active_entities", map[string]any{
				"account_id": acct.NativeAccountID, "entity": entity, "start_time": startTime, "end_time": endTime,
			})
			if errOut != nil {
				return nil, errOut
			}
			ids = append(ids, xActiveEntityIDs(parsed)...)
		}
		for offset := 0; offset < len(ids); offset += 20 {
			end := offset + 20
			if end > len(ids) {
				end = len(ids)
			}
			parsed, errOut := a.execIntegrationTool(ctx, acct, "get_stats", map[string]any{
				"account_id": acct.NativeAccountID,
				"entity":     entity, "entity_ids": strings.Join(ids[offset:end], ","),
				"start_time": startTime, "end_time": endTime, "granularity": "DAY",
				"placement": "ALL_ON_TWITTER", "metric_groups": "ENGAGEMENT,BILLING,VIDEO,WEB_CONVERSION,MOBILE_CONVERSION",
			})
			if errOut != nil {
				return nil, errOut
			}
			points = append(points, normalizeXAnalytics(acct, request.Level, chunkStart, chunkEnd, parsed)...)
		}
		chunkStart = chunkEnd.AddDate(0, 0, 1)
	}
	return points, nil
}

func xActiveEntityIDs(parsed any) []string {
	out := make([]string, 0)
	for _, row := range resultRows(parsed) {
		if id := firstString(row, "id", "entity_id"); id != "" {
			out = append(out, id)
		}
	}
	if len(out) > 0 {
		return out
	}
	raw, _ := asMap(parsed)["data"].([]any)
	for _, value := range raw {
		if id := strings.TrimSpace(toString(value)); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func normalizeXAnalytics(acct *adAccount, level string, start, end time.Time, parsed any) []analyticsPoint {
	out := make([]analyticsPoint, 0)
	for _, entity := range resultRows(parsed) {
		entityID := firstString(entity, "id")
		idData, _ := entity["id_data"].([]any)
		for dateIndex, date := 0, start; !date.After(end); dateIndex, date = dateIndex+1, date.AddDate(0, 0, 1) {
			combined := map[string]any{}
			for _, rawSegment := range idData {
				metrics := asMap(asMap(rawSegment)["metrics"])
				for key, raw := range metrics {
					combined[key] = numericArgAny(combined[key]) + metricAt(raw, dateIndex)
				}
			}
			point := analyticsPoint{
				Platform: acct.Platform, AdAccountID: acct.ID, Level: level,
				EntityID: entityID, EntityName: entityID, Date: date.Format("2006-01-02"),
				Currency: acct.Currency, Timezone: acct.Timezone,
				SpendMicros:     int64(metricAny(combined, "billed_charge_local_micro", "billed_charge_local_micro_amount", "spend_micro")),
				Impressions:     int64(metricAny(combined, "impressions")),
				Clicks:          int64(metricAny(combined, "clicks", "url_clicks")),
				LinkClicks:      int64(metricAny(combined, "url_clicks")),
				Conversions:     xConversionTotal(combined),
				VideoViews:      int64(metricAny(combined, "video_total_views", "video_views")),
				ProviderMetrics: combined, FetchedAt: time.Now().UTC().Format(time.RFC3339),
			}
			switch level {
			case "campaign":
				point.CampaignID, point.CampaignName = entityID, entityID
			case "ad_group":
				point.AdGroupID, point.AdGroupName = entityID, entityID
			}
			out = append(out, point)
		}
	}
	return out
}

func metricAt(raw any, index int) float64 {
	values, ok := raw.([]any)
	if !ok || index < 0 || index >= len(values) {
		return numericArgAny(raw)
	}
	return numericArgAny(values[index])
}

func metricAny(metrics map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if value, ok := metrics[key]; ok {
			return numericArgAny(value)
		}
	}
	return 0
}

func xConversionTotal(metrics map[string]any) float64 {
	total := 0.0
	for key, value := range metrics {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "conversion_") &&
			(strings.HasSuffix(lower, "_purchases") || strings.HasSuffix(lower, "_sign_ups") || strings.HasSuffix(lower, "_site_visits") || strings.HasSuffix(lower, "_downloads")) {
			total += numericArgAny(value)
		}
	}
	return total
}

func (a *App) fetchRedditAnalytics(ctx *sdk.AppCtx, acct *adAccount, request *genericPerformanceRequest) ([]analyticsPoint, map[string]any) {
	breakdown := map[string]string{"account": "DATE", "campaign": "CAMPAIGN_ID", "ad_group": "AD_GROUP_ID", "ad": "AD_ID"}[request.Level]
	breakdowns := []any{"DATE"}
	if request.Level != "account" {
		breakdowns = append(breakdowns, breakdown)
	}
	from, _ := time.Parse("2006-01-02", request.DateFrom)
	to, _ := time.Parse("2006-01-02", request.DateTo)
	data := map[string]any{
		"breakdowns": breakdowns,
		"fields":     []any{"IMPRESSIONS", "REACH", "CLICKS", "SPEND", "VIDEO_STARTED", "VIDEO_COMPLETED", "CONVERSION_PURCHASE_CLICKS", "CONVERSION_PURCHASE_VIEWS", "CONVERSION_PURCHASE_TOTAL_VALUE"},
		"starts_at":  from.Format("2006-01-02") + "T00:00:00Z",
		"ends_at":    to.AddDate(0, 0, 1).Format("2006-01-02") + "T00:00:00Z",
	}
	if acct.Timezone != "" {
		data["time_zone_id"] = acct.Timezone
	}
	if len(request.EntityIDs) > 0 && request.Level != "account" {
		data["filters"] = []any{map[string]any{"field": breakdown, "values": request.EntityIDs}}
	}
	points := make([]analyticsPoint, 0)
	nextURL := ""
	for page := 0; page < maxPerformancePages; page++ {
		input := map[string]any{"ad_account_id": acct.NativeAccountID}
		if nextURL == "" {
			input["data"] = data
		} else {
			input["next_url"] = nextURL
		}
		parsed, errOut := a.execIntegrationTool(ctx, acct, "get_report", input)
		if errOut != nil {
			return nil, errOut
		}
		for _, row := range redditReportRows(parsed) {
			points = append(points, normalizeRedditAnalyticsPoint(acct, request.Level, row))
		}
		next := redditNextURL(parsed)
		if next == "" {
			return points, nil
		}
		if next == nextURL {
			return nil, mcpError("Reddit report pagination returned a repeated next URL")
		}
		nextURL = next
	}
	return nil, mcpError("Reddit report pagination exceeded the safety limit")
}

func redditReportRows(parsed any) []map[string]any {
	data := asMap(asMap(parsed)["data"])
	raw, _ := data["metrics"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		if row := asMap(value); row != nil {
			out = append(out, row)
		}
	}
	if len(out) == 0 {
		return resultRows(parsed)
	}
	return out
}

func normalizeRedditAnalyticsPoint(acct *adAccount, level string, row map[string]any) analyticsPoint {
	metrics := asMap(row["metrics"])
	if len(metrics) == 0 {
		metrics = row
	}
	entityID := acct.NativeAccountID
	entityName := "Account"
	if level == "campaign" {
		entityID, entityName = firstString(row, "campaign_id"), firstString(row, "campaign_name")
	}
	if level == "ad_group" {
		entityID, entityName = firstString(row, "ad_group_id"), firstString(row, "ad_group_name")
	}
	if level == "ad" {
		entityID, entityName = firstString(row, "ad_id"), firstString(row, "ad_name")
	}
	if entityName == "" {
		entityName = entityID
	}
	conversionCount := metricAnyFold(metrics, "conversion_purchase_clicks") + metricAnyFold(metrics, "conversion_purchase_views")
	date := firstString(row, "date", "starts_at")
	if len(date) >= 10 {
		date = date[:10]
	}
	return analyticsPoint{
		Platform: acct.Platform, AdAccountID: acct.ID, Level: level, EntityID: entityID, EntityName: entityName,
		CampaignID: firstString(row, "campaign_id"), CampaignName: firstString(row, "campaign_name"),
		AdGroupID: firstString(row, "ad_group_id"), AdGroupName: firstString(row, "ad_group_name"),
		Date: date, Currency: acct.Currency, Timezone: acct.Timezone,
		SpendMicros: int64(metricAnyFold(metrics, "spend")), Impressions: int64(metricAnyFold(metrics, "impressions")),
		Reach: int64(metricAnyFold(metrics, "reach")), Clicks: int64(metricAnyFold(metrics, "clicks")), LinkClicks: int64(metricAnyFold(metrics, "clicks")),
		Conversions: conversionCount, ConversionValueMicros: int64(metricAnyFold(metrics, "conversion_purchase_total_value") * 10_000),
		VideoViews: int64(metricAnyFold(metrics, "video_started")), ProviderMetrics: metrics, FetchedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

func metricAnyFold(metrics map[string]any, wanted string) float64 {
	for key, value := range metrics {
		if strings.EqualFold(key, wanted) {
			return numericArgAny(value)
		}
	}
	return 0
}

func validateProviderAnalyticsIDs(platform string, ids []string) error {
	if platform != "google" {
		return nil
	}
	for _, id := range ids {
		if !googleNumericID(id) {
			return fmt.Errorf("Google entity IDs must be numeric")
		}
	}
	return nil
}
