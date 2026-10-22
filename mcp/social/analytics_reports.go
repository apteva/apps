package main

// Reporting semantics belong to Social, independently of the publishing adapter.
import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type analyticsWindow struct {
	Kind  string `json:"kind"` // range, lifetime, snapshot
	Start string `json:"start_date,omitempty"`
	End   string `json:"end_date,omitempty"`
}
type analyticsMetricInfo struct {
	Kind     string `json:"kind"`
	Additive bool   `json:"additive"`
	AliasOf  string `json:"alias_of,omitempty"`
}
type analyticsCoverage struct {
	ProviderStopped bool           `json:"provider_stopped,omitempty"`
	Inventory       string         `json:"inventory"`
	Discovered      int            `json:"discovered"`
	Included        int            `json:"included"`
	Failed          int            `json:"failed"`
	Complete        bool           `json:"complete"`
	Truncated       bool           `json:"truncated"`
	MetricPosts     map[string]int `json:"metric_posts,omitempty"`
	Note            string         `json:"note,omitempty"`
}
type analyticsReport struct {
	BindingKey string                         `json:"binding_key"`
	Scope      string                         `json:"scope"`
	EntityID   string                         `json:"entity_id"`
	Identity   string                         `json:"identity"`
	Source     string                         `json:"source"`
	Window     analyticsWindow                `json:"window"`
	Coverage   *analyticsCoverage             `json:"coverage,omitempty"`
	MetricInfo map[string]analyticsMetricInfo `json:"metric_info"`
	Warnings   []string                       `json:"warnings,omitempty"`
}
type analyticsPostRow struct {
	PostID      int64            `json:"post_id,omitempty"`
	ID          string           `json:"id"`
	Title       string           `json:"title"`
	URL         string           `json:"url"`
	PublishedAt string           `json:"published_at,omitempty"`
	Status      string           `json:"status"`
	Error       string           `json:"error,omitempty"`
	Metrics     map[string]int64 `json:"metrics"`
	Report      *analyticsReport `json:"report,omitempty"`
	Insights    insightSeries    `json:"insights,omitempty"`
}
type analyticsPeriodComparison struct {
	Status        string              `json:"status"`
	Reason        string              `json:"reason,omitempty"`
	Window        analyticsWindow     `json:"window"`
	Metrics       map[string]int64    `json:"metrics,omitempty"`
	ChangePercent map[string]*float64 `json:"change_percent,omitempty"`
	Coverage      *analyticsCoverage  `json:"coverage,omitempty"`
}

var reportMetricNames = []string{"followers", "following", "posts", "total_likes", "total_videos", "views", "video_views", "reach", "impressions", "engagements", "likes", "comments", "shares", "saves", "sends", "clicks", "pin_clicks", "outbound_clicks"}
var additiveReportMetrics = map[string]bool{"views": true, "video_views": true, "impressions": true, "engagements": true, "likes": true, "comments": true, "shares": true, "saves": true, "sends": true, "pin_clicks": true, "outbound_clicks": true}

func queryLimit(q analyticsQuery) int {
	if q.Limit <= 0 {
		return 20
	}
	return q.Limit
}
func queryMaxPosts(q analyticsQuery) int {
	if q.MaxPosts <= 0 {
		return 1000
	}
	return q.MaxPosts
}
func newAnalyticsReport(scope, id, source string, q analyticsQuery, kind string) *analyticsReport {
	start, end := q.dates()
	w := analyticsWindow{Kind: kind}
	if kind == "range" {
		w.Start = start
		w.End = end
	}
	return &analyticsReport{Scope: scope, EntityID: id, Source: source, Window: w, MetricInfo: map[string]analyticsMetricInfo{}}
}
func setReportMetricInfo(r *analyticsReport, available []string) {
	for _, name := range available {
		kind := r.Window.Kind
		if containsFold([]string{"followers", "following", "posts", "total_videos", "total_likes"}, name) {
			kind = "snapshot"
		}
		r.MetricInfo[name] = analyticsMetricInfo{Kind: kind, Additive: additiveReportMetrics[name]}
	}
	// Compatibility fields stay readable, but are never displayed or summed twice.
	if _, ok := r.MetricInfo["outbound_clicks"]; ok {
		r.MetricInfo["clicks"] = analyticsMetricInfo{Kind: r.Window.Kind, AliasOf: "outbound_clicks"}
	}
}
func validateAnalyticsQuery(args map[string]any, q analyticsQuery) error {
	if q.Scope != "" && q.Scope != "destination" && q.Scope != "provider_account" {
		return fmt.Errorf("scope must be destination or provider_account")
	}
	for _, key := range []string{"start_date", "end_date"} {
		if raw := strings.TrimSpace(toString(args[key])); raw != "" {
			if _, err := time.Parse("2006-01-02", raw); err != nil {
				return fmt.Errorf("%s must be YYYY-MM-DD", key)
			}
		}
	}
	if (q.StartDate == "") != (q.EndDate == "") {
		return fmt.Errorf("start_date and end_date must be supplied together")
	}
	if q.StartDate != "" {
		start, _ := time.Parse("2006-01-02", q.StartDate)
		end, _ := time.Parse("2006-01-02", q.EndDate)
		if end.Before(start) || end.Sub(start) > 729*24*time.Hour {
			return fmt.Errorf("date range must be ordered and at most 730 days")
		}
		if end.After(time.Now().UTC()) {
			return fmt.Errorf("date range cannot end in the future")
		}
	}
	if q.Limit < 0 || q.Limit > 100 || q.MaxPosts < 0 || q.MaxPosts > 1000 {
		return fmt.Errorf("limit must be 1–100; max_posts must be 1–1000")
	}
	for _, key := range []string{"limit", "max_posts"} {
		if _, ok := args[key]; ok && intArg(args, key, 0) <= 0 {
			return fmt.Errorf("%s must be positive", key)
		}
	}
	if q.SortBy != "" && !containsFold(reportMetricNames, q.SortBy) {
		return fmt.Errorf("unknown sort_by metric")
	}
	for _, key := range []string{"range", "period"} {
		if value := strings.TrimSpace(toString(args[key])); value != "" {
			if _, err := accountMetricsHistoryDays(value, 28); err != nil {
				return err
			}
		}
	}
	return nil
}
func decorateAccountReport(out *accountMetricsResult, connID int64, provider, providerID, externalID string, q analyticsQuery) {
	if out.Report == nil {
		scope := q.Scope
		if scope == "" {
			scope = "destination"
		}
		kind := "snapshot"
		// These adapters expose date-filtered series, while their profile totals can
		// remain lifetime values. Never relabel a profile total as period performance.
		if provider == zernioProviderSlug {
			kind = "range"
		}
		out.Report = newAnalyticsReport(scope, externalID, "provider", q, kind)
		id := externalID
		if providerID != "" {
			id = providerID
		}
		if id == "" {
			id = strconv.FormatInt(connID, 10)
		}
		out.Report.Identity = out.Platform + ":" + provider + ":" + id
	}
	out.Report.BindingKey = accountReportBinding(connID, provider, providerID, externalID) + ":" + q.DestinationID
	if out.Available == nil {
		for _, name := range reportMetricNames {
			if accountMetricKnown(*out, name) || accountMetricValue(*out, name) != 0 {
				out.Available = append(out.Available, name)
			}
		}
	}
	setReportMetricInfo(out.Report, out.Available)
	if out.Platform == "pinterest" && out.Report.Scope == "provider_account" && containsFold(q.GroupBy, "post") {
		out.Report.Warnings = append(out.Report.Warnings, "Whole-account post ranking is unavailable; select destination scope for board Pins.")
	}
	if out.Platform == "youtube" {
		if info, ok := out.Report.MetricInfo["views"]; ok {
			info.Kind = "lifetime"
			out.Report.MetricInfo["views"] = info
		}
	}
	if out.Report.Window.Kind == "snapshot" {
		out.Report.Warnings = append(out.Report.Warnings, "Profile totals are snapshots; the selected date range applies only to available daily series.")
	}
	if q.Compare && out.Comparison == nil {
		out.Comparison = &analyticsPeriodComparison{Status: "unsupported", Reason: "This adapter does not provide comparable period totals."}
	}
	if q.PublishedInRange && !(out.Platform == "pinterest" && q.Scope == "destination") {
		out.Status = "unsupported"
		out.Reason = "Publication-date inventory filtering is not supported by this adapter."
	}
}
func postMetricPeriod(out targetMetricsOutcome) string {
	if out.Report != nil && out.Report.Window.Kind == "range" {
		return "range:" + out.Report.Window.Start + ":" + out.Report.Window.End
	}
	return "snapshot"
}
func (a *App) getPostMetricsQuery(ctx *sdk.AppCtx, target metricsTarget, q analyticsQuery) targetMetricsOutcome {
	if !q.ForceRefresh && target.TargetID > 0 {
		var raw, stamp string
		err := ctx.AppDB().QueryRow(`SELECT result,refreshed_at FROM post_analytics_cache WHERE project_id=? AND target_id=? AND query_key=?`, projectScope(ctx), target.TargetID, postReportCacheKey(target, q)).Scan(&raw, &stamp)
		if err == nil {
			if t, e := time.Parse(time.RFC3339, stamp); e == nil && time.Since(t) < analyticsReportTTL("ok") {
				var out targetMetricsOutcome
				if json.Unmarshal([]byte(raw), &out) == nil && time.Since(t) < analyticsReportTTL(out.Status) {
					return out
				}
			}
		}
	}
	var out targetMetricsOutcome
	if target.Platform == "pinterest" && (target.ProviderSlug == "" || target.ProviderSlug == "native") && target.ExtPostID != "" {
		out = a.fetchPinterestPostReport(ctx, targetMetricsOutcome{TargetID: target.TargetID, SocialAccountID: target.SocialAccountID, Platform: target.Platform, PlatformPostID: target.ExtPostID, PlatformURL: target.ExtURL}, target.ConnID, q)
	} else {
		out = a.fetchPostMetrics(ctx, target)
	}
	if out.Report == nil {
		out.Report = newAnalyticsReport("post", out.PlatformPostID, "provider", q, "lifetime")
		out.Report.Warnings = append(out.Report.Warnings, "This adapter returns lifetime totals; date-filtered post analytics are unavailable.")
	}
	out.Report.Identity = target.Platform + ":" + strconv.FormatInt(target.ConnID, 10) + ":post:" + out.PlatformPostID
	if out.Metrics != nil {
		setReportMetricInfo(out.Report, out.Metrics.Available)
	}
	out.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if (out.Status == "ok" || out.Status == "failed") && target.TargetID > 0 {
		raw, _ := json.Marshal(out)
		_, err := ctx.AppDB().Exec(`INSERT INTO post_analytics_cache(project_id,target_id,query_key,result,refreshed_at) VALUES(?,?,?,?,?) ON CONFLICT(project_id,target_id,query_key) DO UPDATE SET result=excluded.result,refreshed_at=excluded.refreshed_at`, projectScope(ctx), target.TargetID, postReportCacheKey(target, q), string(raw), out.UpdatedAt)
		if err != nil {
			ctx.Logger().Warn("post analytics cache write failed", "err", err)
		}
		_, _ = ctx.AppDB().Exec(`DELETE FROM post_analytics_cache WHERE project_id=? AND datetime(refreshed_at)<datetime('now','-7 days')`, projectScope(ctx))
	}
	return out
}
func postReportCacheKey(t metricsTarget, q analyticsQuery) string {
	return fmt.Sprintf("%s:%d:%s:%s:%s:%s", t.Platform, t.ConnID, t.ExtPostID, t.ProviderSlug, t.ProviderPostID, q.cacheKey())
}
func normalizedMetricValues(m *normalizedMetrics) map[string]int64 {
	if m == nil {
		return map[string]int64{}
	}
	all := map[string]int64{"views": m.Views, "video_views": m.VideoViews, "impressions": m.Impressions, "reach": m.Reach, "likes": m.Likes, "comments": m.Comments, "shares": m.Shares, "saves": m.Saves, "clicks": m.Clicks, "pin_clicks": m.PinClicks, "outbound_clicks": m.OutboundClicks, "engagements": m.Engagements}
	values := map[string]int64{}
	for _, key := range m.Available {
		if value, ok := all[key]; ok {
			values[key] = value
		}
	}
	return values
}
func sortPostReport(rows []analyticsPostRow, q analyticsQuery) []analyticsPostRow {
	metric := q.SortBy
	if metric == "" {
		metric = "impressions"
	}
	sort.SliceStable(rows, func(i, j int) bool {
		iv, ip := rows[i].Metrics[metric]
		jv, jp := rows[j].Metrics[metric]
		if ip != jp {
			return ip
		}
		if iv == jv {
			return rows[i].ID < rows[j].ID
		}
		return iv > jv
	})
	if len(rows) > queryLimit(q) {
		rows = rows[:queryLimit(q)]
	}
	return rows
}

// Other adapters share post ranking without claiming the local import is a
// complete provider inventory or summing lifetime snapshots into period totals.
func (a *App) addKnownPostReport(ctx *sdk.AppCtx, pid string, out *accountMetricsResult, q analyticsQuery) {
	if len(q.Filters) > 0 {
		out.Report.Warnings = append(out.Report.Warnings, "Post ranking does not support the requested dimension filter.")
		return
	}
	rows, err := ctx.AppDB().Query(`
 SELECT t.id, t.post_id, COALESCE(t.platform_post_id,''), COALESCE(t.platform_url,''),
        a.connection_id, COALESCE(a.external_account_id,''), COALESCE(a.page_credentials,''),
        COALESCE(a.provider_slug,'native'), COALESCE(a.provider_account_id,''),
        COALESCE(t.provider_post_id,''), COALESCE(t.publish_operation_id,''),
        p.body, COALESCE(p.published_at,'')
 FROM post_targets t JOIN posts p ON p.id=t.post_id
 JOIN social_accounts a ON a.id=t.social_account_id
 WHERE p.project_id=? AND a.project_id=? AND a.id=? AND t.status='published'
 ORDER BY t.id DESC LIMIT 21`, pid, pid, out.SocialAccountID)
	if err != nil {
		out.Report.Warnings = append(out.Report.Warnings, "Post inventory unavailable.")
		return
	}
	type candidate struct {
		target          metricsTarget
		postID          int64
		body, published string
	}
	list := []candidate{}
	for rows.Next() {
		var c candidate
		c.target.SocialAccountID = out.SocialAccountID
		c.target.Platform = out.Platform
		if rows.Scan(&c.target.TargetID, &c.postID, &c.target.ExtPostID, &c.target.ExtURL, &c.target.ConnID, &c.target.ExternalAccountID, &c.target.PageCreds, &c.target.ProviderSlug, &c.target.ProviderAccountID, &c.target.ProviderPostID, &c.target.PublishOperationID, &c.body, &c.published) == nil {
			list = append(list, c)
		}
	}
	rows.Close()
	if len(list) > 20 {
		list = list[:20]
	}
	out.Report.Warnings = append(out.Report.Warnings, "Post ranking covers up to 20 locally known published posts; it is not a complete provider inventory.")
	for _, c := range list {
		m := a.getPostMetricsQuery(ctx, c.target, q)
		out.PostRows = append(out.PostRows, analyticsPostRow{PostID: c.postID, ID: m.PlatformPostID, Title: c.body, URL: m.PlatformURL, PublishedAt: c.published, Status: m.Status, Error: m.Error, Metrics: normalizedMetricValues(m.Metrics), Report: m.Report, Insights: m.Insights})
	}
	// Keep the fetched inventory in cache; presentation sorts and limits a copy.
}

func accountReportBinding(connID int64, provider, providerID, externalID string) string {
	return fmt.Sprintf("%d:%s:%s:%s", connID, provider, providerID, externalID)
}

func presentAccountReport(out accountMetricsResult, q analyticsQuery) accountMetricsResult {
	if !containsFold(q.GroupBy, "post") {
		out.PostRows = nil
	} else if out.PostRows != nil {
		out.PostRows = sortPostReport(append([]analyticsPostRow{}, out.PostRows...), q)
	}
	return out
}

// Period totals are separate from compatibility profile fields. In particular,
// YouTube's channel viewCount remains lifetime while its daily report is ranged.
func fillPeriodMetrics(out *accountMetricsResult, q analyticsQuery) {
	if len(q.Filters) > 0 {
		out.Report.Warnings = append(out.Report.Warnings, "Overview totals are unfiltered; dimension filters apply to breakdown comparisons.")
		return
	}
	values := map[string]int64{}
	if out.Report != nil && out.Report.Window.Kind == "range" {
		for _, name := range out.Available {
			if additiveReportMetrics[name] {
				values[name] = accountMetricValue(*out, name)
			}
		}
	}
	aliases := map[string]string{"page_impressions": "impressions", "page_post_engagements": "engagements"}
	if len(values) == 0 {
		for name, points := range out.Insights {
			canonical := name
			if alias, ok := aliases[name]; ok {
				canonical = alias
			}
			if !additiveReportMetrics[canonical] {
				continue
			}
			var sum int64
			valid := len(points) > 0
			for _, point := range points {
				if point.Partial {
					valid = false
					break
				}
				sum += point.Value
			}
			if valid {
				values[canonical] = sum
			}
		}
	}
	if len(values) > 0 {
		out.PeriodMetrics = values
		start, end := q.dates()
		out.PeriodWindow = &analyticsWindow{Kind: "range", Start: start, End: end}
	}
}
func (a *App) addPeriodComparison(ctx *sdk.AppCtx, pid string, out *accountMetricsResult, period string, q analyticsQuery) {
	fillPeriodMetrics(out, q)
	if !q.Compare || out.Status != "ok" || len(out.PeriodMetrics) == 0 {
		return
	}
	if out.Comparison != nil && out.Comparison.Status != "unsupported" {
		return
	}
	if out.Platform == "pinterest" && out.Report.Scope == "destination" {
		return
	}
	previousQ := previousAnalyticsQuery(q)
	previousQ.GroupBy = nil
	prev := a.getAccountMetrics(ctx, pid, out.SocialAccountID, period, previousQ)
	if prev.Status != "ok" || len(prev.PeriodMetrics) == 0 {
		out.Comparison = &analyticsPeriodComparison{Status: "unsupported", Reason: "Previous-period totals unavailable: " + prev.Reason + prev.Error}
		return
	}
	comparison := &analyticsPeriodComparison{Status: "ok", Window: *prev.PeriodWindow, Metrics: prev.PeriodMetrics, ChangePercent: map[string]*float64{}}
	for name, value := range out.PeriodMetrics {
		if old, ok := prev.PeriodMetrics[name]; ok {
			if old == 0 {
				comparison.ChangePercent[name] = nil
			} else {
				change := float64(value-old) / float64(old) * 100
				comparison.ChangePercent[name] = &change
			}
		}
	}
	out.Comparison = comparison
}

func analyticsReportTTL(status string) time.Duration {
	if status == "failed" {
		return 5 * time.Minute
	}
	return analyticsSnapshotMinInterval
}
