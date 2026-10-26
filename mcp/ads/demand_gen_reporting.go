package main

import (
	sdk "github.com/apteva/app-sdk"
	"strings"
)

// Breakdown reports intentionally do not enter the additive delivery cache.
func (a *App) googleStructuredBreakdown(ctx *sdk.AppCtx, acct *adAccount, r *genericPerformanceRequest, args map[string]any) (any, error) {
	if acct.Platform != "google" {
		return mcpError("placement/creative breakdowns currently support Google Ads"), nil
	}
	if err := validateProviderAnalyticsIDs("google", r.EntityIDs); err != nil {
		return mcpError(err.Error()), nil
	}
	breakdown := firstString(args, "breakdown")
	resource := map[string]string{"account": "customer", "campaign": "campaign", "ad_group": "ad_group", "ad": "ad_group_ad"}[r.Level]
	identity := map[string]string{"account": "customer.id", "campaign": "campaign.id", "ad_group": "ad_group.id", "ad": "ad_group_ad.ad.id"}[r.Level]
	fields := "segments.date, " + identity + ", segments.ad_network_type, segments.ad_sub_network_type, metrics.cost_micros, metrics.impressions, metrics.clicks, metrics.video_trueview_views"
	if breakdown == "creative" {
		resource = "ad_group_ad_asset_view"
		fields = "segments.date, campaign.id, ad_group.id, ad_group_ad.ad.id, asset.id, asset.type, ad_group_ad_asset_view.field_type, metrics.impressions, metrics.clicks"
	} else if breakdown != "placement" {
		return mcpError("breakdown must be placement or creative"), nil
	}
	query := "SELECT " + fields + " FROM " + resource + " WHERE segments.date BETWEEN '" + r.DateFrom + "' AND '" + r.DateTo + "'"
	if len(r.EntityIDs) > 0 {
		query += " AND " + identity + " IN (" + strings.Join(r.EntityIDs, ",") + ")"
	}
	rows, out := a.googleResourceRows(ctx, acct, query)
	if out != nil {
		return out, nil
	}
	data := []any{}
	for _, row := range rows {
		metrics := mapAt(row, "metrics")
		item := map[string]any{"date": mapAt(row, "segments")["date"], "campaign_id": firstString(mapAt(row, "campaign"), "id"), "adset_id": firstString(mapAt(row, "adGroup"), "id"), "ad_id": firstString(mapAt(mapAt(row, "adGroupAd"), "ad"), "id"), "impressions": int64ArgAny(metrics["impressions"]), "clicks": int64ArgAny(metrics["clicks"])}
		if breakdown == "placement" {
			segments := mapAt(row, "segments")
			item["channel"] = segments["adNetworkType"]
			item["placement"] = segments["adSubNetworkType"]
			item["spend_micros"] = int64ArgAny(metrics["costMicros"])
			item["video_views"] = int64ArgAny(metrics["videoTrueviewViews"])
		} else {
			item["asset_id"] = firstString(mapAt(row, "asset"), "id")
			item["asset_type"] = firstString(mapAt(row, "asset"), "type")
			item["field_type"] = firstString(mapAt(row, "adGroupAdAssetView"), "fieldType")
			item["spend_micros"] = nil
		}
		data = append(data, item)
	}
	return map[string]any{"data": data, "breakdown": breakdown, "source": "live", "currency": acct.Currency, "metric_notes": "Creative-asset rows can overlap; do not sum them into campaign totals. Asset spend is unavailable here and stays null. Use the regular performance report for total spend and event reports for subscriptions/follow-on views."}, nil
}
