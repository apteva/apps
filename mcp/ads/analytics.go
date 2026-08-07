package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const performanceCollectorReconcileInterval = 6 * time.Hour

const (
	performanceCollectorConcurrency = 2
	performanceBackoffMin           = time.Minute
	performanceBackoffMax           = 30 * time.Minute
)

var performanceCollectorIntervals = map[string]time.Duration{
	"campaign": 5 * time.Minute,
	"ad_group": 15 * time.Minute,
	"ad":       15 * time.Minute,
}

type analyticsSyncCall struct {
	signature   string
	done        chan struct{}
	points      []analyticsPoint
	providerErr map[string]any
	err         error
}

type analyticsSyncState struct {
	FailureCount int
	NextAttempt  string
}

type genericPerformanceRequest struct {
	Level     string
	DateFrom  string
	DateTo    string
	EntityIDs []string
	Refresh   bool
}

type analyticsPoint struct {
	Platform              string
	AdAccountID           int64
	Level                 string
	EntityID              string
	EntityName            string
	CampaignID            string
	CampaignName          string
	AdGroupID             string
	AdGroupName           string
	Date                  string
	Currency              string
	Timezone              string
	SpendMicros           int64
	Impressions           int64
	Reach                 int64
	Clicks                int64
	LinkClicks            int64
	Conversions           float64
	ConversionValueMicros int64
	VideoViews            int64
	ProviderMetrics       map[string]any
	FetchedAt             string
}

func validateGenericPerformanceRequest(args map[string]any) (*genericPerformanceRequest, error) {
	level := strings.ToLower(stringArgAny(args, "level"))
	if level == "" {
		level = "campaign"
	}
	switch level {
	case "account", "campaign", "ad_group", "ad":
	default:
		return nil, fmt.Errorf("level must be account, campaign, ad_group, or ad")
	}

	legacyArgs := map[string]any{
		"date_from":    args["date_from"],
		"date_to":      args["date_to"],
		"granularity":  args["granularity"],
		"campaign_ids": args["entity_ids"],
	}
	validated, err := validatePerformanceRequest(legacyArgs)
	if err != nil {
		return nil, err
	}
	refresh := true
	if value, ok := args["refresh"].(bool); ok {
		refresh = value
	}
	return &genericPerformanceRequest{
		Level:     level,
		DateFrom:  validated.DateFrom,
		DateTo:    validated.DateTo,
		EntityIDs: validated.CampaignIDs,
		Refresh:   refresh,
	}, nil
}

func (a *App) toolPerformanceGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	acct, _, errOut := a.resolveAdAccount(ctx, args)
	if errOut != nil {
		return errOut, nil
	}
	request, err := validateGenericPerformanceRequest(args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	pid, err := requireProject(ctx, args)
	if err != nil {
		return mcpError(err.Error()), nil
	}

	if !request.Refresh {
		points, err := loadAnalyticsPoints(ctx, pid, acct.ID, request)
		if err != nil {
			return nil, err
		}
		return analyticsResponse(points, "cache"), nil
	}

	points, providerErr, syncErr, live := a.syncAnalytics(ctx, pid, acct, request, "manual")
	if providerErr != nil {
		return providerErr, nil
	}
	if syncErr != nil {
		return nil, syncErr
	}
	source := "cache"
	if live {
		source = "live"
	}
	return analyticsResponse(points, source), nil
}

func (a *App) syncAnalytics(ctx *sdk.AppCtx, pid string, acct *adAccount, request *genericPerformanceRequest, source string) ([]analyticsPoint, map[string]any, error, bool) {
	key := fmt.Sprintf("%s:%d:%s", pid, acct.ID, request.Level)
	signature := request.DateFrom + ":" + request.DateTo + ":" + strings.Join(request.EntityIDs, ",")

	a.analyticsMu.Lock()
	if a.analyticsInFlight == nil {
		a.analyticsInFlight = make(map[string]*analyticsSyncCall)
	}
	if active := a.analyticsInFlight[key]; active != nil {
		a.analyticsMu.Unlock()
		<-active.done
		if active.signature == signature {
			return active.points, active.providerErr, active.err, false
		}
		return a.syncAnalytics(ctx, pid, acct, request, source)
	}
	call := &analyticsSyncCall{signature: signature, done: make(chan struct{})}
	a.analyticsInFlight[key] = call
	a.analyticsMu.Unlock()

	defer func() {
		a.analyticsMu.Lock()
		delete(a.analyticsInFlight, key)
		close(call.done)
		a.analyticsMu.Unlock()
	}()

	call.points, call.providerErr = a.fetchAnalyticsPoints(ctx, acct, request)
	if call.providerErr != nil {
		message := mcpErrorTextValue(call.providerErr)
		state, _ := recordAnalyticsSync(ctx, pid, acct.ID, request, "failed", message)
		ctx.EmitWithProject("performance.sync_failed", pid, map[string]any{
			"ad_account_id":   acct.ID,
			"level":           request.Level,
			"date_from":       request.DateFrom,
			"date_to":         request.DateTo,
			"source":          source,
			"failure_count":   state.FailureCount,
			"next_attempt_at": state.NextAttempt,
			"message":         message,
		})
		return nil, call.providerErr, nil, true
	}
	if call.err = persistAnalyticsPoints(ctx, pid, acct, request, call.points); call.err != nil {
		state, _ := recordAnalyticsSync(ctx, pid, acct.ID, request, "failed", call.err.Error())
		ctx.EmitWithProject("performance.sync_failed", pid, map[string]any{
			"ad_account_id":   acct.ID,
			"level":           request.Level,
			"date_from":       request.DateFrom,
			"date_to":         request.DateTo,
			"source":          source,
			"failure_count":   state.FailureCount,
			"next_attempt_at": state.NextAttempt,
			"message":         "could not store provider performance",
		})
		return nil, nil, call.err, true
	}
	_, _ = recordAnalyticsSync(ctx, pid, acct.ID, request, "ok", "")
	fetchedAt := time.Now().UTC().Format(time.RFC3339)
	if freshness := analyticsFreshness(call.points); freshness["fetched_at"] != "" {
		fetchedAt = freshness["fetched_at"].(string)
	}
	ctx.EmitWithProject("performance.updated", pid, map[string]any{
		"ad_account_id": acct.ID,
		"levels":        []string{request.Level},
		"date_from":     request.DateFrom,
		"date_to":       request.DateTo,
		"fetched_at":    fetchedAt,
		"source":        source,
		"row_counts":    map[string]int{request.Level: len(call.points)},
	})
	return call.points, nil, nil, true
}

func (a *App) fetchAnalyticsPoints(ctx *sdk.AppCtx, acct *adAccount, request *genericPerformanceRequest) ([]analyticsPoint, map[string]any) {
	switch acct.Platform {
	case "meta":
		return a.fetchMetaAnalytics(ctx, acct, request)
	case "google":
		return a.fetchGoogleAnalytics(ctx, acct, request)
	default:
		return nil, mcpError("performance reporting is not supported for " + acct.Platform)
	}
}

func (a *App) fetchMetaAnalytics(ctx *sdk.AppCtx, acct *adAccount, request *genericPerformanceRequest) ([]analyticsPoint, map[string]any) {
	providerLevel := map[string]string{
		"account": "account", "campaign": "campaign", "ad_group": "adset", "ad": "ad",
	}[request.Level]
	identityFields := map[string]string{
		"account":  "account_id,account_name",
		"campaign": "campaign_id,campaign_name",
		"ad_group": "campaign_id,campaign_name,adset_id,adset_name",
		"ad":       "campaign_id,campaign_name,adset_id,adset_name,ad_id,ad_name",
	}[request.Level]
	timeRange, _ := json.Marshal(map[string]string{"since": request.DateFrom, "until": request.DateTo})
	input := map[string]any{
		"objectId":       acct.NativeAccountID,
		"level":          providerLevel,
		"fields":         identityFields + ",date_start,date_stop,spend,impressions,reach,frequency,clicks,inline_link_clicks,actions,action_values,video_play_actions",
		"time_range":     string(timeRange),
		"time_increment": "1",
		"limit":          500,
	}
	if len(request.EntityIDs) > 0 {
		filterField := map[string]string{
			"account": "account.id", "campaign": "campaign.id", "ad_group": "adset.id", "ad": "ad.id",
		}[request.Level]
		filtering, _ := json.Marshal([]map[string]any{{
			"field": filterField, "operator": "IN", "value": request.EntityIDs,
		}})
		input["filtering"] = string(filtering)
	}

	points := make([]analyticsPoint, 0)
	after := ""
	for page := 0; page < maxPerformancePages; page++ {
		if after == "" {
			delete(input, "after")
		} else {
			input["after"] = after
		}
		parsed, errOut := a.execIntegrationTool(ctx, acct, "insights_get", input)
		if errOut != nil {
			return nil, errOut
		}
		for _, row := range resultRows(parsed) {
			point, err := normalizeMetaAnalyticsPoint(acct, request.Level, row)
			if err != nil {
				return nil, mcpError("normalize Meta performance: " + err.Error())
			}
			points = append(points, point)
		}
		next := metaNextCursor(parsed)
		if next == "" {
			return points, nil
		}
		if next == after {
			return nil, mcpError("Meta insights pagination returned a repeated cursor")
		}
		after = next
	}
	return nil, mcpError("Meta insights pagination exceeded the safety limit")
}

func (a *App) fetchGoogleAnalytics(ctx *sdk.AppCtx, acct *adAccount, request *genericPerformanceRequest) ([]analyticsPoint, map[string]any) {
	resource := map[string]string{
		"account": "customer", "campaign": "campaign", "ad_group": "ad_group", "ad": "ad_group_ad",
	}[request.Level]
	identityFields := map[string]string{
		"account":  "customer.id, customer.descriptive_name",
		"campaign": "customer.id, customer.descriptive_name, campaign.id, campaign.name",
		"ad_group": "customer.id, campaign.id, campaign.name, ad_group.id, ad_group.name",
		"ad":       "customer.id, campaign.id, campaign.name, ad_group.id, ad_group.name, ad_group_ad.ad.id, ad_group_ad.ad.name",
	}[request.Level]
	query := "SELECT segments.date, " + identityFields + ", customer.currency_code, " +
		"metrics.cost_micros, metrics.impressions, metrics.clicks, metrics.conversions, " +
		"metrics.conversions_value, metrics.video_views FROM " + resource +
		" WHERE segments.date BETWEEN '" + request.DateFrom + "' AND '" + request.DateTo + "'"
	if len(request.EntityIDs) > 0 {
		filterField := map[string]string{
			"account": "customer.id", "campaign": "campaign.id", "ad_group": "ad_group.id", "ad": "ad_group_ad.ad.id",
		}[request.Level]
		query += " AND " + filterField + " IN (" + strings.Join(request.EntityIDs, ",") + ")"
	}
	input := map[string]any{"customer_id": acct.NativeAccountID, "query": query}
	points := make([]analyticsPoint, 0)
	pageToken := ""
	for page := 0; page < maxPerformancePages; page++ {
		if pageToken == "" {
			delete(input, "page_token")
		} else {
			input["page_token"] = pageToken
		}
		parsed, errOut := a.execIntegrationTool(ctx, acct, "search", input)
		if errOut != nil {
			return nil, errOut
		}
		for _, row := range resultRows(parsed) {
			point, err := normalizeGoogleAnalyticsPoint(acct, request.Level, row)
			if err != nil {
				return nil, mcpError("normalize Google performance: " + err.Error())
			}
			points = append(points, point)
		}
		next := googleNextPageToken(parsed)
		if next == "" {
			return points, nil
		}
		if next == pageToken {
			return nil, mcpError("Google Ads pagination returned a repeated page token")
		}
		pageToken = next
	}
	return nil, mcpError("Google Ads pagination exceeded the safety limit")
}

func normalizeMetaAnalyticsPoint(acct *adAccount, level string, row map[string]any) (analyticsPoint, error) {
	spendMicros, err := decimalToMicros(row["spend"])
	if err != nil {
		return analyticsPoint{}, err
	}
	conversionValueMicros, err := decimalToMicros(metaConversionValueRaw(row))
	if err != nil {
		return analyticsPoint{}, err
	}
	entityID, entityName := metaAnalyticsIdentity(acct, level, row)
	providerMetrics := map[string]any{
		"frequency": numericArgAny(row["frequency"]),
		"actions":   actionValues(row["actions"]),
	}
	return analyticsPoint{
		Platform:              acct.Platform,
		AdAccountID:           acct.ID,
		Level:                 level,
		EntityID:              entityID,
		EntityName:            entityName,
		CampaignID:            firstString(row, "campaign_id", "campaignId"),
		CampaignName:          firstString(row, "campaign_name", "campaignName"),
		AdGroupID:             firstString(row, "adset_id", "adsetId"),
		AdGroupName:           firstString(row, "adset_name", "adsetName"),
		Date:                  firstString(row, "date_start", "dateStart"),
		Currency:              acct.Currency,
		Timezone:              acct.Timezone,
		SpendMicros:           spendMicros,
		Impressions:           int64ArgAny(row["impressions"]),
		Reach:                 int64ArgAny(row["reach"]),
		Clicks:                int64ArgAny(row["clicks"]),
		LinkClicks:            int64ArgAny(row["inline_link_clicks"], row["inlineLinkClicks"]),
		Conversions:           metaConversionValue(row),
		ConversionValueMicros: conversionValueMicros,
		VideoViews:            metaVideoViews(row),
		ProviderMetrics:       providerMetrics,
		FetchedAt:             time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func metaAnalyticsIdentity(acct *adAccount, level string, row map[string]any) (string, string) {
	switch level {
	case "account":
		id := firstString(row, "account_id", "accountId")
		if id == "" {
			id = acct.NativeAccountID
		}
		return id, firstString(row, "account_name", "accountName")
	case "ad_group":
		return firstString(row, "adset_id", "adsetId"), firstString(row, "adset_name", "adsetName")
	case "ad":
		return firstString(row, "ad_id", "adId"), firstString(row, "ad_name", "adName")
	default:
		return firstString(row, "campaign_id", "campaignId"), firstString(row, "campaign_name", "campaignName")
	}
}

func normalizeGoogleAnalyticsPoint(acct *adAccount, level string, row map[string]any) (analyticsPoint, error) {
	metrics := mapAt(row, "metrics")
	customer := mapAt(row, "customer")
	campaign := mapAt(row, "campaign")
	adGroup := mapAtEither(row, "adGroup", "ad_group")
	adGroupAd := mapAtEither(row, "adGroupAd", "ad_group_ad")
	ad := mapAt(adGroupAd, "ad")
	entityID, entityName := googleAnalyticsIdentity(acct, level, customer, campaign, adGroup, ad)
	currency := firstString(customer, "currencyCode", "currency_code")
	if currency == "" {
		currency = acct.Currency
	}
	conversionValueMicros, err := decimalToMicros(firstString(metrics, "conversionsValue", "conversions_value"))
	if err != nil {
		return analyticsPoint{}, err
	}
	return analyticsPoint{
		Platform:              acct.Platform,
		AdAccountID:           acct.ID,
		Level:                 level,
		EntityID:              entityID,
		EntityName:            entityName,
		CampaignID:            firstString(campaign, "id"),
		CampaignName:          firstString(campaign, "name"),
		AdGroupID:             firstString(adGroup, "id"),
		AdGroupName:           firstString(adGroup, "name"),
		Date:                  firstString(mapAt(row, "segments"), "date"),
		Currency:              currency,
		Timezone:              acct.Timezone,
		SpendMicros:           int64ArgAny(metrics["costMicros"], metrics["cost_micros"]),
		Impressions:           int64ArgAny(metrics["impressions"]),
		Clicks:                int64ArgAny(metrics["clicks"]),
		LinkClicks:            int64ArgAny(metrics["clicks"]),
		Conversions:           numericArgAny(metrics["conversions"]),
		ConversionValueMicros: conversionValueMicros,
		VideoViews:            int64ArgAny(metrics["videoViews"], metrics["video_views"]),
		ProviderMetrics:       map[string]any{},
		FetchedAt:             time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func googleAnalyticsIdentity(acct *adAccount, level string, customer, campaign, adGroup, ad map[string]any) (string, string) {
	switch level {
	case "account":
		id := firstString(customer, "id")
		if id == "" {
			id = acct.NativeAccountID
		}
		return id, firstString(customer, "descriptiveName", "descriptive_name")
	case "ad_group":
		return firstString(adGroup, "id"), firstString(adGroup, "name")
	case "ad":
		return firstString(ad, "id"), firstString(ad, "name")
	default:
		return firstString(campaign, "id"), firstString(campaign, "name")
	}
}

func mapAtEither(row map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if value := mapAt(row, key); len(value) > 0 {
			return value
		}
	}
	return map[string]any{}
}

func metaConversionValueRaw(row map[string]any) string {
	values := actionValues(row["action_values"])
	for _, candidate := range []string{"omni_purchase", "offsite_conversion.fb_pixel_purchase", "purchase", "offsite_conversion"} {
		if value, ok := values[candidate]; ok {
			return strconv.FormatFloat(value, 'f', -1, 64)
		}
	}
	return "0"
}

func metaVideoViews(row map[string]any) int64 {
	values := actionValues(row["video_play_actions"])
	if value, ok := values["video_view"]; ok {
		return int64(math.Round(value))
	}
	for _, value := range values {
		return int64(math.Round(value))
	}
	return 0
}

func persistAnalyticsPoints(ctx *sdk.AppCtx, pid string, acct *adAccount, request *genericPerformanceRequest, points []analyticsPoint) error {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, point := range points {
		if point.EntityID == "" || point.Date == "" {
			continue
		}
		providerJSON, _ := json.Marshal(point.ProviderMetrics)
		if _, err := tx.Exec(
			`INSERT INTO ad_entities (
			    project_id, ad_account_id, platform, level, native_entity_id, name,
			    campaign_id, ad_group_id, provider_data_json, last_seen_at
			 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(project_id, ad_account_id, level, native_entity_id) DO UPDATE SET
			    name=excluded.name, campaign_id=excluded.campaign_id,
			    ad_group_id=excluded.ad_group_id, provider_data_json=excluded.provider_data_json,
			    last_seen_at=excluded.last_seen_at, updated_at=CURRENT_TIMESTAMP`,
			pid, acct.ID, acct.Platform, point.Level, point.EntityID, point.EntityName,
			point.CampaignID, point.AdGroupID, string(providerJSON), point.FetchedAt,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO ad_metric_points (
			    project_id, ad_account_id, platform, level, native_entity_id, entity_name,
			    campaign_id, campaign_name, ad_group_id, ad_group_name, point_date,
			    currency, timezone_name, spend_micros, impressions, reach, clicks,
			    link_clicks, conversions, conversion_value_micros, video_views,
			    provider_metrics_json, fetched_at
			 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(project_id, ad_account_id, level, native_entity_id, point_date) DO UPDATE SET
			    entity_name=excluded.entity_name, campaign_id=excluded.campaign_id,
			    campaign_name=excluded.campaign_name, ad_group_id=excluded.ad_group_id,
			    ad_group_name=excluded.ad_group_name, currency=excluded.currency,
			    timezone_name=excluded.timezone_name, spend_micros=excluded.spend_micros,
			    impressions=excluded.impressions, reach=excluded.reach, clicks=excluded.clicks,
			    link_clicks=excluded.link_clicks, conversions=excluded.conversions,
			    conversion_value_micros=excluded.conversion_value_micros,
			    video_views=excluded.video_views,
			    provider_metrics_json=excluded.provider_metrics_json,
			    fetched_at=excluded.fetched_at, updated_at=CURRENT_TIMESTAMP`,
			pid, acct.ID, acct.Platform, point.Level, point.EntityID, point.EntityName,
			point.CampaignID, point.CampaignName, point.AdGroupID, point.AdGroupName,
			point.Date, point.Currency, point.Timezone, point.SpendMicros,
			point.Impressions, point.Reach, point.Clicks, point.LinkClicks,
			point.Conversions, point.ConversionValueMicros, point.VideoViews,
			string(providerJSON), point.FetchedAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func loadAnalyticsPoints(ctx *sdk.AppCtx, pid string, accountID int64, request *genericPerformanceRequest) ([]analyticsPoint, error) {
	query := `SELECT platform, ad_account_id, level, native_entity_id, entity_name,
	                 campaign_id, campaign_name, ad_group_id, ad_group_name, point_date,
	                 currency, timezone_name, spend_micros, impressions, reach, clicks,
	                 link_clicks, conversions, conversion_value_micros, video_views,
	                 provider_metrics_json, fetched_at
	            FROM ad_metric_points
	           WHERE project_id=? AND ad_account_id=? AND level=?
	             AND point_date BETWEEN ? AND ?`
	queryArgs := []any{pid, accountID, request.Level, request.DateFrom, request.DateTo}
	if len(request.EntityIDs) > 0 {
		query += " AND native_entity_id IN (" + analyticsSQLPlaceholders(len(request.EntityIDs)) + ")"
		for _, id := range request.EntityIDs {
			queryArgs = append(queryArgs, id)
		}
	}
	query += " ORDER BY point_date ASC, entity_name ASC, native_entity_id ASC"
	rows, err := ctx.AppDB().Query(query, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := make([]analyticsPoint, 0)
	for rows.Next() {
		var point analyticsPoint
		var providerJSON string
		if err := rows.Scan(
			&point.Platform, &point.AdAccountID, &point.Level, &point.EntityID, &point.EntityName,
			&point.CampaignID, &point.CampaignName, &point.AdGroupID, &point.AdGroupName,
			&point.Date, &point.Currency, &point.Timezone, &point.SpendMicros,
			&point.Impressions, &point.Reach, &point.Clicks, &point.LinkClicks,
			&point.Conversions, &point.ConversionValueMicros, &point.VideoViews,
			&providerJSON, &point.FetchedAt,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(providerJSON), &point.ProviderMetrics)
		points = append(points, point)
	}
	return points, rows.Err()
}

func analyticsSQLPlaceholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func analyticsResponse(points []analyticsPoint, source string) map[string]any {
	data := make([]map[string]any, 0, len(points))
	for _, point := range points {
		data = append(data, analyticsPointMap(point))
	}
	return map[string]any{
		"data":      data,
		"summary":   analyticsSummary(points),
		"source":    source,
		"freshness": analyticsFreshness(points),
		"metric_notes": map[string]any{
			"reach": "Reach is non-additive; use the daily rows rather than summing it across dates or entities.",
		},
		"next_cursor": nil,
	}
}

func analyticsPointMap(point analyticsPoint) map[string]any {
	result := map[string]any{
		"platform":                point.Platform,
		"ad_account_id":           point.AdAccountID,
		"level":                   point.Level,
		"entity_id":               point.EntityID,
		"entity_name":             point.EntityName,
		"campaign_id":             point.CampaignID,
		"campaign_name":           point.CampaignName,
		"ad_group_id":             point.AdGroupID,
		"ad_group_name":           point.AdGroupName,
		"date":                    point.Date,
		"currency":                point.Currency,
		"timezone":                point.Timezone,
		"spend_micros":            point.SpendMicros,
		"impressions":             point.Impressions,
		"reach":                   point.Reach,
		"clicks":                  point.Clicks,
		"link_clicks":             point.LinkClicks,
		"conversions":             point.Conversions,
		"conversion_value_micros": point.ConversionValueMicros,
		"video_views":             point.VideoViews,
		"provider_metrics":        point.ProviderMetrics,
		"fetched_at":              point.FetchedAt,
	}
	addDerivedAnalytics(result, point.SpendMicros, point.Impressions, point.Clicks, point.Conversions, point.ConversionValueMicros)
	return result
}

func analyticsSummary(points []analyticsPoint) map[string]any {
	var spend, impressions, clicks, linkClicks, conversionValue, videoViews int64
	var conversions float64
	currency, timezone := "", ""
	for _, point := range points {
		spend += point.SpendMicros
		impressions += point.Impressions
		clicks += point.Clicks
		linkClicks += point.LinkClicks
		conversions += point.Conversions
		conversionValue += point.ConversionValueMicros
		videoViews += point.VideoViews
		if currency == "" {
			currency = point.Currency
		}
		if timezone == "" {
			timezone = point.Timezone
		}
	}
	result := map[string]any{
		"spend_micros": spend, "impressions": impressions, "reach": int64(0),
		"clicks": clicks, "link_clicks": linkClicks, "conversions": conversions,
		"conversion_value_micros": conversionValue, "video_views": videoViews,
		"currency": currency, "timezone": timezone,
	}
	addDerivedAnalytics(result, spend, impressions, clicks, conversions, conversionValue)
	return result
}

func addDerivedAnalytics(result map[string]any, spend, impressions, clicks int64, conversions float64, conversionValue int64) {
	result["ctr"] = safeRatio(float64(clicks)*100, float64(impressions))
	result["cpc_micros"] = safeRoundedRatio(spend, float64(clicks))
	result["cpm_micros"] = safeRoundedRatio(spend*1000, float64(impressions))
	result["cpa_micros"] = safeRoundedRatio(spend, conversions)
	result["roas"] = safeRatio(float64(conversionValue), float64(spend))
}

func safeRatio(numerator, denominator float64) float64 {
	if denominator <= 0 {
		return 0
	}
	return math.Round((numerator/denominator)*10000) / 10000
}

func safeRoundedRatio(numerator int64, denominator float64) int64 {
	if numerator <= 0 || denominator <= 0 {
		return 0
	}
	return int64(math.Round(float64(numerator) / denominator))
}

func analyticsFreshness(points []analyticsPoint) map[string]any {
	latest := ""
	for _, point := range points {
		if point.FetchedAt > latest {
			latest = point.FetchedAt
		}
	}
	return map[string]any{"fetched_at": latest, "row_count": len(points)}
}

func recordAnalyticsSync(ctx *sdk.AppCtx, pid string, accountID int64, request *genericPerformanceRequest, status, message string) (analyticsSyncState, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	state := analyticsSyncState{}
	if status == "failed" {
		_ = ctx.AppDB().QueryRow(
			`SELECT failure_count FROM ad_sync_state WHERE project_id=? AND ad_account_id=? AND level=?`,
			pid, accountID, request.Level,
		).Scan(&state.FailureCount)
		state.FailureCount++
		delay := performanceBackoffMin * time.Duration(1<<min(state.FailureCount-1, 5))
		if delay > performanceBackoffMax {
			delay = performanceBackoffMax
		}
		state.NextAttempt = time.Now().UTC().Add(delay).Format(time.RFC3339)
	}
	_, err := ctx.AppDB().Exec(
		`INSERT INTO ad_sync_state (
		    project_id, ad_account_id, level, last_incremental_at, last_attempt_at,
		    last_success_at, last_date_from, last_date_to, last_status, last_error,
		    failure_count, next_attempt_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, ad_account_id, level) DO UPDATE SET
		    last_incremental_at=CASE WHEN excluded.last_status='ok' THEN excluded.last_incremental_at ELSE ad_sync_state.last_incremental_at END,
		    last_attempt_at=excluded.last_attempt_at,
		    last_success_at=CASE WHEN excluded.last_status='ok' THEN excluded.last_success_at ELSE ad_sync_state.last_success_at END,
		    last_date_from=excluded.last_date_from, last_date_to=excluded.last_date_to,
		    last_status=excluded.last_status, last_error=excluded.last_error,
		    failure_count=excluded.failure_count, next_attempt_at=excluded.next_attempt_at,
		    updated_at=CURRENT_TIMESTAMP`,
		pid, accountID, request.Level,
		map[bool]string{true: now, false: ""}[status == "ok"], now,
		map[bool]string{true: now, false: ""}[status == "ok"],
		request.DateFrom, request.DateTo, status, message, state.FailureCount, state.NextAttempt,
	)
	return state, err
}

func mcpErrorTextValue(value map[string]any) string {
	content, _ := value["content"].([]map[string]any)
	if len(content) > 0 {
		return firstString(content[0], "text")
	}
	if raw, ok := value["content"].([]any); ok && len(raw) > 0 {
		return firstString(asMap(raw[0]), "text")
	}
	return "provider performance request failed"
}

func (a *App) runPerformanceCollector(runCtx context.Context, app *sdk.AppCtx) error {
	query := `SELECT project_id, id, COALESCE(timezone_name,'')
	            FROM ad_accounts WHERE status='active'`
	args := []any{}
	if pid := projectScope(app); pid != "" {
		query += " AND project_id=?"
		args = append(args, pid)
	}
	query += " ORDER BY project_id, id"
	rows, err := app.AppDB().Query(query, args...)
	if err != nil {
		return err
	}
	type accountJob struct {
		projectID string
		accountID int64
		timezone  string
	}
	jobs := make([]accountJob, 0)
	for rows.Next() {
		var job accountJob
		if err := rows.Scan(&job.projectID, &job.accountID, &job.timezone); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, job)
	}
	rows.Close()
	type syncJob struct {
		accountJob
		level     string
		reconcile bool
	}
	due := make([]syncJob, 0, len(jobs)*len(performanceCollectorIntervals))
	now := time.Now().UTC()
	for _, job := range jobs {
		for level, interval := range performanceCollectorIntervals {
			isDue, reconcile := analyticsSyncDue(app, job.projectID, job.accountID, level, interval, now)
			if isDue {
				due = append(due, syncJob{accountJob: job, level: level, reconcile: reconcile})
			}
		}
	}
	work := make(chan syncJob)
	var wg sync.WaitGroup
	for i := 0; i < performanceCollectorConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range work {
				if runCtx.Err() != nil {
					return
				}
				a.runPerformanceSyncJob(app, job.projectID, job.accountID, job.timezone, job.level, job.reconcile)
			}
		}()
	}
	for _, job := range due {
		select {
		case work <- job:
		case <-runCtx.Done():
			close(work)
			wg.Wait()
			return runCtx.Err()
		}
	}
	close(work)
	wg.Wait()
	return nil
}

func (a *App) runPerformanceSyncJob(app *sdk.AppCtx, pid string, accountID int64, timezone, level string, reconcile bool) {
	location := time.UTC
	if loaded, err := time.LoadLocation(timezone); err == nil {
		location = loaded
	}
	today := time.Now().In(location)
	dateFrom := today.Format("2006-01-02")
	if reconcile {
		dateFrom = today.AddDate(0, 0, -6).Format("2006-01-02")
	}
	acct, _, errOut := a.resolveAdAccount(app, map[string]any{"_project_id": pid, "ad_account_id": accountID})
	if errOut != nil {
		app.Logger().Warn("performance_collector: account resolution failed", "project", pid, "account", accountID)
		return
	}
	request := &genericPerformanceRequest{Level: level, DateFrom: dateFrom, DateTo: today.Format("2006-01-02"), Refresh: true}
	_, providerErr, err, _ := a.syncAnalytics(app, pid, acct, request, "worker")
	if err != nil {
		app.Logger().Warn("performance_collector: sync failed", "project", pid, "account", accountID, "level", level, "err", err)
		return
	}
	if providerErr != nil {
		app.Logger().Warn("performance_collector: provider failed", "project", pid, "account", accountID, "level", level, "err", mcpErrorTextValue(providerErr))
		return
	}
	if reconcile {
		_, _ = app.AppDB().Exec(
			`UPDATE ad_sync_state SET last_reconciled_at=?, updated_at=CURRENT_TIMESTAMP
			  WHERE project_id=? AND ad_account_id=? AND level=?`,
			time.Now().UTC().Format(time.RFC3339), pid, accountID, level,
		)
	}
}

func analyticsSyncDue(ctx *sdk.AppCtx, pid string, accountID int64, level string, interval time.Duration, now time.Time) (bool, bool) {
	var lastSuccess, lastReconciled, nextAttempt string
	err := ctx.AppDB().QueryRow(
		`SELECT COALESCE(NULLIF(last_success_at,''), last_incremental_at), last_reconciled_at, next_attempt_at
		   FROM ad_sync_state WHERE project_id=? AND ad_account_id=? AND level=?`,
		pid, accountID, level,
	).Scan(&lastSuccess, &lastReconciled, &nextAttempt)
	if err == sql.ErrNoRows {
		return true, true
	}
	if err != nil {
		return true, true
	}
	if retryAt, parseErr := time.Parse(time.RFC3339, nextAttempt); parseErr == nil && now.Before(retryAt) {
		return false, false
	}
	last, parseErr := time.Parse(time.RFC3339, lastSuccess)
	due := parseErr != nil || now.Sub(last) >= interval
	reconciled, reconcileErr := time.Parse(time.RFC3339, lastReconciled)
	reconcile := reconcileErr != nil || now.Sub(reconciled) >= performanceCollectorReconcileInterval
	return due, reconcile
}
