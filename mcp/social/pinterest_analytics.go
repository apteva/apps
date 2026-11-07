package main

import (
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"sort"
	"strings"
	"time"
)

var pinterestMetricMap = map[string]string{"IMPRESSION": "impressions", "PIN_CLICK": "pin_clicks", "OUTBOUND_CLICK": "outbound_clicks", "SAVE": "saves", "ENGAGEMENT": "engagements", "VIDEO_MRC_VIEW": "video_views"}
var pinterestPinMetrics = []string{"IMPRESSION", "PIN_CLICK", "OUTBOUND_CLICK", "SAVE"}

func pinterestAvailable(values map[string]int64) []string {
	out := []string{}
	for k, n := range pinterestMetricMap {
		if _, ok := values[k]; ok {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}
func pinterestNormalized(raw json.RawMessage) *normalizedMetrics {
	v := pinterestSummaryMetrics(raw)
	return &normalizedMetrics{Impressions: v["IMPRESSION"], PinClicks: v["PIN_CLICK"], OutboundClicks: v["OUTBOUND_CLICK"], Clicks: v["OUTBOUND_CLICK"], Saves: v["SAVE"], Engagements: v["ENGAGEMENT"], VideoViews: v["VIDEO_MRC_VIEW"], Available: pinterestAvailable(v), Raw: sanitizeRawJSON(raw)}
}
func (a *App) fetchPinterestPostReport(ctx *sdk.AppCtx, out targetMetricsOutcome, connID int64, q analyticsQuery) targetMetricsOutcome {
	out.Report = newAnalyticsReport("post", out.PlatformPostID, "provider", q, "range")
	if err := pinterestReportDates(q); err != nil {
		out.Status = "unsupported"
		out.Reason = err.Error()
		return out
	}
	start, end := q.dates()
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_pin_analytics", map[string]any{"pin_id": out.PlatformPostID, "start_date": start, "end_date": end, "metric_types": pinterestPinMetrics})
	if err != nil {
		out.Status = "failed"
		out.Error = err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status = "failed"
		out.Error = upstreamError(res).Error()
		return out
	}
	out.Metrics = pinterestNormalized(res.Data)
	if len(out.Metrics.Available) == 0 {
		out.Status = "failed"
		out.Error = "Pinterest returned no supported metrics"
		return out
	}
	out.Status = "ok"
	out.Insights = pinterestDailyInsights(res.Data)
	setReportMetricInfo(out.Report, out.Metrics.Available)
	finalizePinterestReport(out.Report, out.Insights, q, []string{"impressions", "pin_clicks", "outbound_clicks", "saves"})
	return out
}

type pinterestReportPin struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	Media     struct {
		Type string `json:"media_type"`
	} `json:"media"`
}

func (a *App) pinterestBoardInventory(ctx *sdk.AppCtx, connID int64, boardID string, q analyticsQuery) ([]pinterestReportPin, *analyticsCoverage, []string) {
	c := &analyticsCoverage{Inventory: "current_destination_posts", MetricPosts: map[string]int{}, Note: "Current board membership; deleted or moved Pins and historical board membership cannot be reconstructed."}
	list := []pinterestReportPin{}
	seen := map[string]bool{}
	bookmarks := map[string]bool{}
	bookmark := ""
	warnings := []string{}
	for page := 0; page < 20; page++ {
		args := map[string]any{"board_id": boardID, "page_size": 250}
		if bookmark != "" {
			args["bookmark"] = bookmark
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_board_pins", args)
		if err != nil || res == nil || !res.Success {
			if err == nil {
				err = upstreamError(res)
			}
			return list, c, append(warnings, "Board listing incomplete: "+err.Error())
		}
		var p struct {
			Items    []pinterestReportPin `json:"items"`
			Bookmark string               `json:"bookmark"`
		}
		if json.Unmarshal(res.Data, &p) != nil || p.Items == nil {
			return list, c, append(warnings, "Invalid board listing response.")
		}
		for _, pin := range p.Items {
			if pin.ID == "" || seen[pin.ID] {
				continue
			}
			seen[pin.ID] = true
			c.Discovered++
			if len(list) < queryMaxPosts(q) {
				list = append(list, pin)
			} else {
				c.Truncated = true
			}
		}
		if c.Truncated {
			return list, c, append(warnings, "Board inventory exceeds max_posts; totals cover only included Pins.")
		}
		if p.Bookmark == "" {
			c.Complete = true
			return list, c, warnings
		}
		if len(list) >= queryMaxPosts(q) || bookmarks[p.Bookmark] {
			c.Truncated = true
			return list, c, append(warnings, "Board listing stopped at the configured limit or a repeated bookmark.")
		}
		bookmark = p.Bookmark
		bookmarks[bookmark] = true
	}
	c.Truncated = true
	return list, c, append(warnings, "Board listing reached the page budget.")
}
func filterPinterestInventory(pins []pinterestReportPin, q analyticsQuery) ([]pinterestReportPin, bool) {
	if !q.PublishedInRange {
		return pins, false
	}
	start, end := q.dates()
	out := []pinterestReportPin{}
	unknown := false
	for _, pin := range pins {
		if len(pin.CreatedAt) < 10 {
			unknown = true
			continue
		}
		day := pin.CreatedAt[:10]
		if _, err := time.Parse("2006-01-02", day); err != nil {
			unknown = true
			continue
		}
		if day >= start && day <= end {
			out = append(out, pin)
		}
	}
	return out, unknown
}
func (a *App) pinterestBatchReport(ctx *sdk.AppCtx, connID int64, pins []pinterestReportPin, q analyticsQuery) ([]analyticsPostRow, *analyticsCoverage, []string) {
	c := &analyticsCoverage{Inventory: "current_destination_posts", Discovered: len(pins), Complete: true, MetricPosts: map[string]int{}}
	rows := make([]analyticsPostRow, 0, len(pins))
	warnings := []string{}
	start, end := q.dates()
	stopped := false
	stopReason := ""
	fallback := false
	fallbackCalls := 0
	pinErrors := map[string]string{}
	for offset := 0; offset < len(pins); offset += 100 {
		last := offset + 100
		if last > len(pins) {
			last = len(pins)
		}
		batch := pins[offset:last]
		ids := []string{}
		for _, pin := range batch {
			ids = append(ids, pin.ID)
		}
		payload := map[string]json.RawMessage{}
		batchError := ""
		if stopped {
			batchError = "Not fetched after provider failure: " + stopReason
		} else if !fallback {
			res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_multiple_pin_analytics", map[string]any{"pin_ids": ids, "start_date": start, "end_date": end, "metric_types": pinterestPinMetrics})
			if err != nil || res == nil || !res.Success {
				if err == nil {
					err = upstreamError(res)
				}
				batchError = err.Error()
				if pinterestBulkRestricted(res) {
					fallback = true
					batchError = ""
					warnings = append(warnings, "Batch analytics unavailable; using individual Pin requests (maximum 50 per report).")
				}
				if !fallback {
					warnings = append(warnings, "Bulk analytics request failed: "+batchError)
				}
				if res != nil && (res.Status == 401 && !fallback || res.Status == 429 || res.Status >= 500) {
					stopped = true
					stopReason = batchError
				}
			} else if json.Unmarshal(res.Data, &payload) != nil {
				batchError = "Invalid batch analytics response"
			}
		}
		if fallback && !stopped {
			for _, pin := range batch {
				if fallbackCalls >= 50 {
					break
				}
				fallbackCalls++
				res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_pin_analytics", map[string]any{"pin_id": pin.ID, "start_date": start, "end_date": end, "metric_types": pinterestPinMetrics})
				if err == nil && res != nil && res.Success {
					payload[pin.ID] = res.Data
				} else {
					if err == nil {
						err = upstreamError(res)
					}
					pinErrors[pin.ID] = err.Error()
					warnings = append(warnings, "Pin analytics request failed: "+err.Error())
					if res != nil && (res.Status == 401 || res.Status == 429 || res.Status >= 500) {
						stopped = true
						stopReason = err.Error()
						break
					}
				}
			}
		}
		for _, pin := range batch {
			row := analyticsPostRow{ID: pin.ID, Title: pin.Title, URL: "", PublishedAt: valueOrEmpty(nativeTimestamp(pin.CreatedAt)), Status: "ok", Metrics: map[string]int64{}, Report: newAnalyticsReport("post", pin.ID, "provider", q, "range")}
			row.Report.Identity = "pinterest:post:" + pin.ID
			raw, ok := payload[pin.ID]
			m := pinterestNormalized(raw)
			if batchError != "" || !ok || len(m.Available) == 0 {
				row.Status = "failed"
				row.Error = batchError
				if row.Error == "" {
					row.Error = pinErrors[pin.ID]
					if row.Error == "" {
						row.Error = "Pinterest omitted this Pin's analytics (or the fallback request budget was reached)"
					}
				}
				c.Failed++
				c.Complete = false
			} else {
				row.Metrics = normalizedMetricValues(m)
				row.Insights = pinterestDailyInsights(raw)
				setReportMetricInfo(row.Report, m.Available)
				finalizePinterestReport(row.Report, row.Insights, q, []string{"impressions", "pin_clicks", "outbound_clicks", "saves"})
				c.Included++
				for name := range row.Metrics {
					c.MetricPosts[name]++
				}
			}
			rows = append(rows, row)
		}
	}
	videoIDs := []string{}
	for _, pin := range pins {
		if strings.Contains(strings.ToLower(pin.Media.Type), "video") {
			videoIDs = append(videoIDs, pin.ID)
		}
	}
	for offset := 0; !stopped && !fallback && offset < len(videoIDs); offset += 100 {
		last := offset + 100
		if last > len(videoIDs) {
			last = len(videoIDs)
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_multiple_pin_analytics", map[string]any{"pin_ids": videoIDs[offset:last], "start_date": start, "end_date": end, "metric_types": []string{"VIDEO_MRC_VIEW"}})
		if err != nil || res == nil || !res.Success {
			warnings = append(warnings, "Video views unavailable for some Pins.")
			if res != nil && (res.Status == 429 || res.Status >= 500) {
				stopped = true
			}
			continue
		}
		data := map[string]json.RawMessage{}
		_ = json.Unmarshal(res.Data, &data)
		for i := range rows {
			if raw, ok := data[rows[i].ID]; ok {
				m := pinterestNormalized(raw)
				if metricAvailable(m, "video_views") {
					rows[i].Metrics["video_views"] = m.VideoViews
					if rows[i].Insights == nil {
						rows[i].Insights = insightSeries{}
					}
					rows[i].Insights["video_views"] = pinterestDailyInsights(raw)["video_views"]
					rows[i].Report.MetricInfo["video_views"] = analyticsMetricInfo{Kind: "range", Additive: true}
					c.MetricPosts["video_views"]++
				}
			}
		}
	}
	if fallback && len(videoIDs) > 0 {
		warnings = append(warnings, "Video view totals are unavailable when batch analytics is restricted.")
	}
	for _, name := range []string{"impressions", "pin_clicks", "outbound_clicks", "saves"} {
		if c.MetricPosts[name] < len(pins) {
			c.Complete = false
		}
	}
	c.ProviderStopped = stopped
	if c.Failed > 0 {
		warnings = append(warnings, fmt.Sprintf("Analytics missing for %d Pins; totals are partial.", c.Failed))
	}
	if stopped {
		warnings = append(warnings, "Provider failure stopped further requests; resolve the reported error before refreshing.")
	}
	return rows, c, warnings
}
func aggregatePostReport(rows []analyticsPostRow) (map[string]int64, insightSeries) {
	totals := map[string]int64{}
	daily := map[string]map[string]int64{}
	counts := map[string]map[string]int{}
	partial := map[string]map[string]bool{}
	states := map[string]map[string]string{}
	estimates := map[string]map[string]bool{}
	for _, row := range rows {
		if row.Status != "ok" {
			continue
		}
		for name, v := range row.Metrics {
			if additiveReportMetrics[name] {
				totals[name] += v
			}
		}
		for name, points := range row.Insights {
			if !additiveReportMetrics[name] {
				continue
			}
			if daily[name] == nil {
				daily[name] = map[string]int64{}
				counts[name] = map[string]int{}
				partial[name] = map[string]bool{}
				states[name] = map[string]string{}
				estimates[name] = map[string]bool{}
			}
			for _, p := range points {
				if p.DataStatus != "pending" && p.DataStatus != "unavailable" {
					daily[name][p.Time] += p.Value
					counts[name][p.Time]++
				} else if _, ok := daily[name][p.Time]; !ok {
					daily[name][p.Time] = 0
				}
				states[name][p.Time] = mergeMetricStatus(states[name][p.Time], p.DataStatus)
				estimates[name][p.Time] = estimates[name][p.Time] || p.Estimated || p.DataStatus == "estimated"
				partial[name][p.Time] = partial[name][p.Time] || p.Partial
			}
		}
	}
	series := insightSeries{}
	for name, days := range daily {
		for day, v := range days {
			status := states[name][day]
			if status != "pending" && counts[name][day] < len(rows) {
				status = "unavailable"
			}
			series[name] = append(series[name], insightPoint{Estimated: estimates[name][day], DataStatus: status, Time: day, Value: v, Included: counts[name][day], Expected: len(rows), Partial: counts[name][day] < len(rows) || partial[name][day]})
		}
		sort.Slice(series[name], func(i, j int) bool { return series[name][i].Time < series[name][j].Time })
	}
	return totals, series
}
func assignAccountReportMetrics(out *accountMetricsResult, t map[string]int64) {
	out.Impressions = t["impressions"]
	out.Views = t["views"]
	out.VideoViews = t["video_views"]
	out.PinClicks = t["pin_clicks"]
	out.OutboundClicks = t["outbound_clicks"]
	out.Clicks = out.OutboundClicks
	out.Saves = t["saves"]
	out.Engagements = t["engagements"]
	for name := range t {
		out.Available = append(out.Available, name)
	}
	sort.Strings(out.Available)
}
func previousAnalyticsQuery(q analyticsQuery) analyticsQuery {
	start, end := q.dates()
	s, _ := time.Parse("2006-01-02", start)
	e, _ := time.Parse("2006-01-02", end)
	days := int(e.Sub(s).Hours()/24) + 1
	q.StartDate = s.AddDate(0, 0, -days).Format("2006-01-02")
	q.EndDate = s.AddDate(0, 0, -1).Format("2006-01-02")
	q.Compare = false
	return q
}
func comparePostReports(current, previous []analyticsPostRow, q analyticsQuery, c *analyticsCoverage) *analyticsPeriodComparison {
	prev, _ := aggregatePostReport(previous)
	curr, _ := aggregatePostReport(current)
	start, end := q.dates()
	out := &analyticsPeriodComparison{Status: "ok", Window: analyticsWindow{Kind: "range", Start: start, End: end}, Metrics: prev, ChangePercent: map[string]*float64{}, Coverage: c}
	for name, v := range curr {
		matched := true
		for i, row := range current {
			if i >= len(previous) || row.Status != "ok" || previous[i].Status != "ok" {
				matched = false
				break
			}
			_, cp := row.Metrics[name]
			_, pp := previous[i].Metrics[name]
			if cp != pp {
				matched = false
				break
			}
		}
		old, ok := prev[name]
		if !matched || !ok {
			out.Status = "partial"
			continue
		}
		if old != 0 {
			change := float64(v-old) / float64(old) * 100
			out.ChangePercent[name] = &change
		} else {
			out.ChangePercent[name] = nil
		}
	}
	if !c.Complete {
		out.Status = "partial"
	}
	return out
}
func (a *App) getPinterestBoardMetrics(ctx *sdk.AppCtx, out accountMetricsResult, connID int64, boardID string, q analyticsQuery) accountMetricsResult {
	out.Report = newAnalyticsReport("destination", boardID, "post_aggregation", q, "range")
	out.Report.Identity = "pinterest:board:" + boardID
	if err := pinterestReportDates(q); err != nil {
		out.Status = "unsupported"
		out.Reason = err.Error()
		return out
	}
	if boardID == "" {
		out.Status = "failed"
		out.Error = "Pinterest destination has no board ID"
		return out
	}
	pins, inventory, warnings := a.pinterestBoardInventory(ctx, connID, boardID, q)
	selected, unknown := filterPinterestInventory(pins, q)
	rows, c, bw := a.pinterestBatchReport(ctx, connID, selected, q)
	c.Discovered = inventory.Discovered
	c.Truncated = inventory.Truncated
	c.Complete = c.Complete && inventory.Complete && !unknown
	c.Note = inventory.Note
	if q.PublishedInRange {
		c.Note += " Only Pins published during the selected window are included."
	}
	if unknown {
		warnings = append(warnings, "Pins with unknown publication dates were excluded.")
	}
	out.Report.Coverage = c
	out.Report.Warnings = append(warnings, bw...)
	out.Status = "ok"
	if len(rows) == 0 && !c.Complete || len(rows) > 0 && c.Included == 0 {
		out.Status = "failed"
		out.Error = "No usable board analytics; see coverage and warnings"
	}
	totals, series := aggregatePostReport(rows)
	if len(rows) == 0 && c.Complete {
		for _, name := range pinterestPinMetrics {
			totals[pinterestMetricMap[name]] = 0
		}
	}
	assignAccountReportMetrics(&out, totals)
	out.Insights = series
	for _, points := range series {
		for _, point := range points {
			if point.Partial {
				if !containsFold(out.Report.Warnings, "Some daily trend points have incomplete post coverage or estimated data.") {
					out.Report.Warnings = append(out.Report.Warnings, "Some daily trend points have incomplete post coverage or estimated data.")
				}
				break
			}
		}
	}
	out.Posts = int64(len(selected))
	out.Available = append(out.Available, "posts")
	setReportMetricInfo(out.Report, out.Available)
	finalizePinterestReport(out.Report, out.Insights, q, []string{"impressions", "pin_clicks", "outbound_clicks", "saves"})
	if containsFold(q.GroupBy, "post") {
		out.PostRows = append([]analyticsPostRow{}, rows...)
	}
	if q.Compare && out.Status == "ok" {
		prevQ := previousAnalyticsQuery(q)
		if err := pinterestReportDates(prevQ); err != nil {
			out.Comparison = &analyticsPeriodComparison{Status: "unsupported", Reason: err.Error()}
			return out
		}
		if c.ProviderStopped {
			out.Comparison = &analyticsPeriodComparison{Status: "unsupported", Reason: "Comparison deferred after provider rate limit or service failure."}
			return out
		}
		prevPins := selected
		if q.PublishedInRange {
			prevPins, _ = filterPinterestInventory(pins, prevQ)
		}
		prevRows, pc, pw := a.pinterestBatchReport(ctx, connID, prevPins, prevQ)
		pc.Complete = pc.Complete && inventory.Complete
		pc.Truncated = inventory.Truncated
		if q.PublishedInRange {
			pt, _ := aggregatePostReport(prevRows)
			start, end := prevQ.dates()
			out.Comparison = &analyticsPeriodComparison{Status: "ok", Window: analyticsWindow{Kind: "range", Start: start, End: end}, Metrics: pt, Coverage: pc, Reason: "Each period uses its own publication-date cohort; change percentages are omitted."}
		} else {
			out.Comparison = comparePostReports(rows, prevRows, prevQ, pc)
		}
		out.Comparison.Window.Timezone = "UTC"
		_, previousSeries := aggregatePostReport(prevRows)
		out.Comparison.Freshness = seriesFreshness(previousSeries, []string{"impressions", "pin_clicks", "outbound_clicks", "saves"}, prevQ)
		if f := out.Comparison.Freshness; len(f.PendingDates) > 0 || len(f.EstimatedDates) > 0 || len(f.UnavailableDates) > 0 {
			out.Comparison.Status = "partial"
		}
		out.Report.Warnings = append(out.Report.Warnings, pw...)
	}
	return out
}

// Pinterest's organic analytics retain only the last 90 days. Reject rather
// than silently trimming dates or claiming an unavailable comparison period.
func pinterestReportDates(q analyticsQuery) error {
	start, end := q.dates()
	s, err := time.Parse("2006-01-02", start)
	if err != nil {
		return err
	}
	e, err := time.Parse("2006-01-02", end)
	if err != nil {
		return err
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if s.Before(today.AddDate(0, 0, -90)) || e.Sub(s) > 90*24*time.Hour {
		return fmt.Errorf("Pinterest analytics require a start date within the last 90 days and a range of at most 90 days.")
	}
	return nil
}

func pinterestBulkRestricted(res *sdk.ExecuteResult) bool {
	if res == nil {
		return false
	}
	if res.Status == 403 || res.Status == 404 {
		return true
	}
	if res.Status != 401 {
		return false
	}
	var body struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	return json.Unmarshal(res.Data, &body) == nil && body.Code == 3 && strings.EqualFold(strings.TrimSpace(body.Message), "Your application does not have access to this restricted feature.")
}
func pinterestDailyInsights(raw json.RawMessage, requested ...string) insightSeries {
	if len(requested) == 0 {
		requested = pinterestPinMetrics
	}
	type day struct {
		Date    string              `json:"date"`
		Status  string              `json:"data_status"`
		Metrics map[string]*float64 `json:"metrics"`
	}
	type group struct {
		Daily []day `json:"daily_metrics"`
	}
	var payload map[string]group
	_ = json.Unmarshal(raw, &payload)
	if all, ok := payload["all"]; ok {
		payload = map[string]group{"all": all}
	}
	days := map[string]map[string]insightPoint{}
	for _, g := range payload {
		for _, d := range g.Daily {
			if _, err := time.Parse("2006-01-02", d.Date); err != nil {
				continue
			}
			expected := map[string]bool{}
			for _, key := range requested {
				expected[key] = true
			}
			for key := range d.Metrics {
				expected[key] = true
			}
			for key := range expected {
				name, ok := pinterestMetricMap[key]
				if !ok {
					continue
				}
				status := "unavailable"
				switch d.Status {
				case "PROCESSING":
					status = "pending"
				case "ESTIMATE":
					status = "estimated"
				case "READY":
					status = "ready"
				case "":
					status = "unknown"
				}
				value, present := d.Metrics[key]
				if (status == "ready" || status == "estimated" || status == "unknown") && (!present || value == nil) {
					status = "unavailable"
				}
				if days[name] == nil {
					days[name] = map[string]insightPoint{}
				}
				p, exists := days[name][d.Date]
				if !exists {
					p = insightPoint{Time: d.Date, DataStatus: status}
				} else {
					p.DataStatus = mergeMetricStatus(p.DataStatus, status)
				}
				if status == "ready" || status == "estimated" || status == "unknown" {
					p.Value += int64(*value)
				}
				p.Estimated = p.Estimated || status == "estimated"
				p.Partial = p.DataStatus != "ready"
				days[name][d.Date] = p
			}
		}
	}
	out := insightSeries{}
	for name, values := range days {
		for _, p := range values {
			out[name] = append(out[name], p)
		}
		sort.Slice(out[name], func(i, j int) bool { return out[name][i].Time < out[name][j].Time })
	}
	return out
}

func finalizePinterestReport(report *analyticsReport, series insightSeries, q analyticsQuery, names []string) {
	if report == nil {
		return
	}
	report.Window.Timezone = "UTC"
	report.RequestedMetrics = append([]string{}, names...)
	// Preserve missing calendar dates as gaps; do not synthesize zero activity.
	start, end := q.dates()
	s, _ := time.Parse("2006-01-02", start)
	e, _ := time.Parse("2006-01-02", end)
	for _, name := range names {
		seen := map[string]bool{}
		for _, p := range series[name] {
			seen[p.Time] = true
		}
		for day := s; !day.After(e); day = day.AddDate(0, 0, 1) {
			date := day.Format("2006-01-02")
			if !seen[date] {
				series[name] = append(series[name], insightPoint{Time: date, DataStatus: "unavailable", Partial: true})
			}
		}
		sort.Slice(series[name], func(i, j int) bool { return series[name][i].Time < series[name][j].Time })
		if info, ok := report.MetricInfo[name]; ok {
			info.Freshness = seriesFreshness(series, []string{name}, q)
			report.MetricInfo[name] = info
		}
	}
	report.Freshness = seriesFreshness(series, names, q)
}
