package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type analyticsQuery struct {
	RangeDays  int
	StartDate  string
	EndDate    string
	Breakdowns []string
	Filters    map[string][]string
	GroupBy    []string
}

type analyticsCapabilityIssue struct {
	Dimension string `json:"dimension"`
	Reason    string `json:"reason"`
}

type analyticsCapabilities struct {
	Available   []string                   `json:"available,omitempty"`
	Unavailable []analyticsCapabilityIssue `json:"unavailable,omitempty"`
}

type analyticsBreakdownRow struct {
	Dimensions map[string]string `json:"dimensions"`
	Metrics    map[string]int64  `json:"metrics"`
}

type analyticsBreakdown struct {
	Dimension string                  `json:"dimension"`
	Status    string                  `json:"status"`
	Reason    string                  `json:"reason,omitempty"`
	Source    string                  `json:"source,omitempty"`
	Rows      []analyticsBreakdownRow `json:"rows,omitempty"`
}

var canonicalBreakdownOrder = []string{
	"device", "os", "country", "region", "city", "age", "gender",
	"traffic_source", "audience_type", "content_type", "sharing_service", "video",
}

func analyticsQueryFromArgs(args map[string]any) analyticsQuery {
	q := analyticsQuery{RangeDays: 28, Filters: map[string][]string{}}
	rangeValue := strings.ToLower(strings.TrimSpace(toString(args["range"])))
	if rangeValue == "" {
		rangeValue = strings.ToLower(strings.TrimSpace(toString(args["period"])))
	}
	if strings.HasSuffix(rangeValue, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(rangeValue, "d")); err == nil && n > 0 && n <= 730 {
			q.RangeDays = n
		}
	}
	q.StartDate = dateArg(args, "start_date")
	q.EndDate = dateArg(args, "end_date")
	q.Breakdowns = canonicalDimensions(stringSliceArg(args, "breakdowns"))
	q.GroupBy = canonicalDimensions(stringSliceArg(args, "group_by"))
	if raw, ok := args["filters"].(map[string]any); ok {
		for key, value := range raw {
			if dimension := canonicalDimension(key); dimension != "" {
				q.Filters[dimension] = stringValues(value)
			}
		}
	}
	for _, dimension := range canonicalBreakdownOrder {
		if values := stringValues(args[dimension]); len(values) > 0 {
			q.Filters[dimension] = values
		}
	}
	return q
}

func dateArg(args map[string]any, key string) string {
	value := strings.TrimSpace(toString(args[key]))
	if _, err := time.Parse("2006-01-02", value); err == nil {
		return value
	}
	return ""
}

func stringValues(value any) []string {
	var values []string
	switch v := value.(type) {
	case string:
		values = strings.Split(v, ",")
	case []string:
		values = append(values, v...)
	case []any:
		for _, item := range v {
			values = append(values, toString(item))
		}
	default:
		if v != nil {
			values = append(values, toString(v))
		}
	}
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func canonicalDimensions(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		if dimension := canonicalDimension(value); dimension != "" && !seen[dimension] {
			seen[dimension] = true
			out = append(out, dimension)
		}
	}
	return out
}

func canonicalDimension(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	switch value {
	case "device", "device_type", "devicetype":
		return "device"
	case "os", "operating_system", "operatingsystem":
		return "os"
	case "country", "region", "city", "age", "gender", "video":
		return value
	case "traffic", "source", "traffic_source", "insighttrafficsourcetype":
		return "traffic_source"
	case "audience", "audience_type", "subscribed_status", "subscribedstatus":
		return "audience_type"
	case "content", "content_type", "creator_content_type", "creatorcontenttype":
		return "content_type"
	case "sharing", "sharing_service", "sharingservice":
		return "sharing_service"
	case "social_account", "account", "channel":
		return "social_account"
	default:
		return ""
	}
}

func (q analyticsQuery) dates() (string, string) {
	if q.StartDate != "" && q.EndDate != "" {
		return q.StartDate, q.EndDate
	}
	return metricsDateWindow(q.RangeDays)
}

func (q analyticsQuery) period() string {
	if q.StartDate != "" && q.EndDate != "" {
		return "range_" + q.StartDate + "_" + q.EndDate
	}
	return fmt.Sprintf("range_%dd", q.RangeDays)
}

func requestedProviderBreakdowns(q analyticsQuery) []string {
	values := append([]string{}, q.Breakdowns...)
	for dimension := range q.Filters {
		values = append(values, dimension)
	}
	return canonicalDimensions(values)
}

func analyticsCapabilitiesFor(platform, provider string) analyticsCapabilities {
	available := []string{}
	switch platform {
	case "youtube":
		if provider == "" || provider == "native" {
			available = []string{"device", "os", "country", "traffic_source", "audience_type", "content_type", "sharing_service", "video"}
		}
	case "instagram":
		available = []string{"country", "city", "age", "gender"}
	}
	return analyticsCapabilities{Available: available}
}

func enrichAnalyticsCapabilities(capabilities analyticsCapabilities, requested []string) analyticsCapabilities {
	available := map[string]bool{}
	for _, dimension := range capabilities.Available {
		available[dimension] = true
	}
	for _, dimension := range requested {
		if dimension == "social_account" || available[dimension] {
			continue
		}
		capabilities.Unavailable = append(capabilities.Unavailable, analyticsCapabilityIssue{
			Dimension: dimension,
			Reason:    "not_exposed_by_provider",
		})
	}
	return capabilities
}

func unsupportedBreakdowns(capabilities analyticsCapabilities, requested []string) []analyticsBreakdown {
	available := map[string]bool{}
	for _, dimension := range capabilities.Available {
		available[dimension] = true
	}
	out := []analyticsBreakdown{}
	for _, dimension := range requested {
		if dimension != "social_account" && !available[dimension] {
			out = append(out, analyticsBreakdown{
				Dimension: dimension,
				Status:    "unsupported",
				Reason:    "This provider does not expose " + strings.ReplaceAll(dimension, "_", " ") + " analytics.",
			})
		}
	}
	return out
}

func (a *App) addAccountBreakdowns(ctx *sdk.AppCtx, out *accountMetricsResult, connID int64, provider, providerAccountID, externalAccountID, pageCreds string, query analyticsQuery) {
	requested := requestedProviderBreakdowns(query)
	out.Capabilities = enrichAnalyticsCapabilities(analyticsCapabilitiesFor(out.Platform, provider), requested)
	if len(requested) == 0 {
		return
	}
	available := map[string]bool{}
	for _, dimension := range out.Capabilities.Available {
		available[dimension] = true
	}
	for _, dimension := range requested {
		if !available[dimension] {
			out.Breakdowns = append(out.Breakdowns, analyticsBreakdown{
				Dimension: dimension,
				Status:    "unsupported",
				Reason:    "This provider does not expose " + strings.ReplaceAll(dimension, "_", " ") + " analytics.",
			})
			continue
		}
		var breakdown analyticsBreakdown
		switch {
		case out.Platform == "youtube" && (provider == "" || provider == "native"):
			breakdown = a.fetchYouTubeBreakdown(ctx, connID, dimension, query)
		case out.Platform == "instagram" && provider == zernioProviderSlug:
			breakdown = a.fetchInstagramBreakdown(ctx, connID, providerAccountID, "", dimension, query, true)
		case out.Platform == "instagram":
			breakdown = a.fetchInstagramBreakdown(ctx, connID, externalAccountID, extractPageToken(pageCreds), dimension, query, false)
		default:
			breakdown = analyticsBreakdown{Dimension: dimension, Status: "unsupported", Reason: "Breakdown is not wired for this provider."}
		}
		out.Breakdowns = append(out.Breakdowns, breakdown)
	}
}

var youtubeDimensions = map[string]string{
	"device":          "deviceType",
	"os":              "operatingSystem",
	"country":         "country",
	"traffic_source":  "insightTrafficSourceType",
	"audience_type":   "subscribedStatus",
	"content_type":    "creatorContentType",
	"sharing_service": "sharingService",
	"video":           "video",
}

func (a *App) fetchYouTubeBreakdown(ctx *sdk.AppCtx, connID int64, dimension string, query analyticsQuery) analyticsBreakdown {
	nativeDimension := youtubeDimensions[dimension]
	breakdown := analyticsBreakdown{Dimension: dimension, Source: "youtube_analytics"}
	if nativeDimension == "" {
		breakdown.Status, breakdown.Reason = "unsupported", "Unsupported YouTube dimension."
		return breakdown
	}
	since, until := query.dates()
	input := map[string]any{
		"ids":        "channel==MINE",
		"startDate":  since,
		"endDate":    until,
		"metrics":    "views,estimatedMinutesWatched",
		"dimensions": nativeDimension,
		"sort":       "-views",
		"maxResults": 200,
	}
	if filters := youtubeFilters(query.Filters); filters != "" {
		input["filters"] = filters
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "query_analytics_report", input)
	if err != nil {
		breakdown.Status, breakdown.Reason = "failed", err.Error()
		return breakdown
	}
	if res == nil || !res.Success {
		breakdown.Status, breakdown.Reason = "failed", upstreamError(res).Error()
		return breakdown
	}
	breakdown.Rows = parseYouTubeBreakdownRows(res.Data, dimension)
	breakdown.Status = "ok"
	return breakdown
}

func youtubeFilters(filters map[string][]string) string {
	keys := make([]string, 0, len(filters))
	for key := range filters {
		if youtubeDimensions[key] != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		values := filters[key]
		if len(values) == 0 {
			continue
		}
		normalized := make([]string, 0, len(values))
		for _, value := range values {
			normalized = append(normalized, youtubeDimensionValue(key, value))
		}
		parts = append(parts, youtubeDimensions[key]+"=="+strings.Join(normalized, ","))
	}
	return strings.Join(parts, ";")
}

func youtubeDimensionValue(dimension, value string) string {
	value = strings.TrimSpace(value)
	if dimension == "device" || dimension == "os" || dimension == "audience_type" || dimension == "content_type" || dimension == "traffic_source" {
		return strings.ToUpper(strings.ReplaceAll(value, " ", "_"))
	}
	return value
}

func parseYouTubeBreakdownRows(raw json.RawMessage, dimension string) []analyticsBreakdownRow {
	var resp struct {
		ColumnHeaders []struct {
			Name       string `json:"name"`
			ColumnType string `json:"columnType"`
		} `json:"columnHeaders"`
		Rows [][]any `json:"rows"`
	}
	if json.Unmarshal(raw, &resp) != nil || len(resp.ColumnHeaders) == 0 {
		return nil
	}
	rows := make([]analyticsBreakdownRow, 0, len(resp.Rows))
	for _, values := range resp.Rows {
		row := analyticsBreakdownRow{Dimensions: map[string]string{}, Metrics: map[string]int64{}}
		for i, header := range resp.ColumnHeaders {
			if i >= len(values) {
				continue
			}
			if header.ColumnType == "DIMENSION" || i == 0 {
				row.Dimensions[dimension] = normalizeDimensionValue(dimension, toString(values[i]))
			} else {
				row.Metrics[canonicalMetricName(header.Name)] = insightValueToInt64(values[i])
			}
		}
		if row.Dimensions[dimension] != "" {
			rows = append(rows, row)
		}
	}
	return rows
}

func normalizeDimensionValue(dimension, value string) string {
	value = strings.TrimSpace(value)
	if dimension == "device" || dimension == "os" || dimension == "audience_type" || dimension == "content_type" || dimension == "traffic_source" {
		return strings.ToLower(value)
	}
	return value
}

func canonicalMetricName(name string) string {
	switch name {
	case "estimatedMinutesWatched":
		return "watch_time_minutes"
	case "averageViewDuration":
		return "average_view_duration_seconds"
	case "averageViewPercentage":
		return "average_view_percentage"
	default:
		var b strings.Builder
		for i, r := range name {
			if i > 0 && r >= 'A' && r <= 'Z' {
				b.WriteByte('_')
			}
			b.WriteRune(r)
		}
		return strings.ToLower(b.String())
	}
}

func (a *App) fetchInstagramBreakdown(ctx *sdk.AppCtx, connID int64, accountID, token, dimension string, query analyticsQuery, zernio bool) analyticsBreakdown {
	breakdown := analyticsBreakdown{Dimension: dimension, Source: "instagram_insights"}
	metric := "follower_demographics"
	if accountID == "" {
		breakdown.Status, breakdown.Reason = "failed", "Instagram account ID is missing."
		return breakdown
	}
	since, until := query.dates()
	input := map[string]any{"metric_type": "total_value", "breakdown": dimension, "since": since, "until": until}
	tool := "get_account_insights"
	if zernio {
		tool = "get_instagram_account_insights"
		input = map[string]any{"accountId": accountID, "metrics": metric, "metricType": "total_value", "breakdown": dimension, "since": since, "until": until}
	} else {
		input["instagramAccountId"] = accountID
		input["metric"] = metric
		input["access_token"] = token
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, input)
	if err != nil {
		breakdown.Status, breakdown.Reason = "failed", err.Error()
		return breakdown
	}
	if res == nil || !res.Success {
		breakdown.Status, breakdown.Reason = "failed", upstreamError(res).Error()
		return breakdown
	}
	breakdown.Rows = parseMetaBreakdownRows(res.Data, dimension)
	breakdown.Status = "ok"
	return breakdown
}

func parseMetaBreakdownRows(raw json.RawMessage, dimension string) []analyticsBreakdownRow {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	rows := []analyticsBreakdownRow{}
	var walk func(any)
	walk = func(node any) {
		switch v := node.(type) {
		case []any:
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			values, hasValues := v["dimension_values"].([]any)
			if hasValues && len(values) > 0 {
				metricValue := insightValueToInt64(v["value"])
				rows = append(rows, analyticsBreakdownRow{
					Dimensions: map[string]string{dimension: normalizeDimensionValue(dimension, toString(values[0]))},
					Metrics:    map[string]int64{"audience": metricValue},
				})
			}
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(value)
	return rows
}

func canonicalDimensionsJSON(dimensions map[string]string) (string, string) {
	if len(dimensions) == 0 {
		return "{}", ""
	}
	keys := make([]string, 0, len(dimensions))
	for key := range dimensions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ordered := make(map[string]string, len(keys))
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		ordered[key] = dimensions[key]
		parts = append(parts, url.QueryEscape(key)+"="+url.QueryEscape(dimensions[key]))
	}
	raw, _ := json.Marshal(ordered)
	return string(raw), strings.Join(parts, "&")
}

func aggregateAccountComparison(results []accountMetricsResult, metric string, filters map[string][]string) analyticsBreakdown {
	if metric == "" {
		metric = "views"
	}
	rows := make([]analyticsBreakdownRow, 0, len(results))
	for _, result := range results {
		value := accountMetricValue(result, metric)
		if len(filters) > 0 {
			if !hasSupportedFilterBreakdown(result.Breakdowns, filters) {
				continue
			}
			value = filteredBreakdownValue(result.Breakdowns, metric, filters)
		}
		rows = append(rows, analyticsBreakdownRow{
			Dimensions: map[string]string{
				"social_account": strconv.FormatInt(result.SocialAccountID, 10),
				"account_name":   result.DisplayName,
				"platform":       result.Platform,
			},
			Metrics: map[string]int64{metric: value},
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Metrics[metric] > rows[j].Metrics[metric] })
	return analyticsBreakdown{Dimension: "social_account", Status: "ok", Source: "social", Rows: rows}
}

func hasSupportedFilterBreakdown(breakdowns []analyticsBreakdown, filters map[string][]string) bool {
	for dimension := range filters {
		found := false
		for _, breakdown := range breakdowns {
			if breakdown.Dimension == dimension && breakdown.Status == "ok" {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func accountMetricValue(result accountMetricsResult, metric string) int64 {
	switch metric {
	case "followers":
		return result.Followers
	case "reach":
		return result.Reach
	case "impressions":
		return result.Impressions
	case "engagements":
		return result.Engagements
	default:
		return result.Views
	}
}

func filteredBreakdownValue(breakdowns []analyticsBreakdown, metric string, filters map[string][]string) int64 {
	var total int64
	for _, breakdown := range breakdowns {
		for _, row := range breakdown.Rows {
			matched := true
			for key, accepted := range filters {
				if !containsFold(accepted, row.Dimensions[key]) {
					matched = false
					break
				}
			}
			if matched {
				total += row.Metrics[metric]
			}
		}
	}
	return total
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}
