package main

import (
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"testing"
)

func TestPinterestRestricted401FallbackAndAuthenticationFailure(t *testing.T) {
	for _, tc := range []struct {
		message  string
		code     int
		fallback bool
	}{
		{"Your application does not have access to this restricted feature.", 3, true},
		{"Authentication failed.", 3, false},
		{"Your application does not have access to this restricted feature.", 1, false},
	} {
		t.Run(tc.message+string(rune(tc.code)), func(t *testing.T) {
			pf := newRecordingPlatform()
			body, _ := json.Marshal(map[string]any{"code": tc.code, "message": tc.message})
			pf.executeResponses["get_multiple_pin_analytics"] = &sdk.ExecuteResult{Status: 401, Data: body}
			pf.executeResponses["get_pin_analytics"] = analyticsOK(pinA)
			ctx := newSocialCtx(t, pf)
			rows, c, _ := (&App{}).pinterestBatchReport(ctx, 42, []pinterestReportPin{{ID: "a"}}, analyticsQuery{RangeDays: 7})
			if (c.Included == 1) != tc.fallback {
				t.Fatalf("fallback=%t: %+v", tc.fallback, c)
			}
			if !tc.fallback && (len(pf.executeCalls) != 1 || !strings.Contains(rows[0].Error, tc.message)) {
				t.Fatal("authentication failure was hidden", rows, pf.executeCalls)
			}
		})
	}
}
func TestMetricAvailabilityJSONAndCacheRoundTrip(t *testing.T) {
	for _, platform := range []string{"pinterest", "other-channel"} {
		m := accountMetricsResult{Platform: platform, Available: []string{"impressions", "saves"}, Impressions: 10, Report: newAnalyticsReport("post", "a", "provider", analyticsQuery{RangeDays: 7}, "range")}
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var values map[string]any
		_ = json.Unmarshal(raw, &values)
		if values["outbound_clicks"] != nil || values["clicks"] != nil || values["saves"] != float64(0) || values["impressions"] != float64(10) {
			t.Fatal(string(raw))
		}
		var cached accountMetricsResult
		_ = json.Unmarshal(raw, &cached)
		again, _ := json.Marshal(cached)
		if string(raw) != string(again) {
			t.Fatal("cache lost availability")
		}
	}
	p := pinterestNormalized(json.RawMessage(`{"all":{"summary_metrics":{"IMPRESSION":2,"SAVE":0,"PIN_CLICK":null}}}`))
	raw, _ := json.Marshal(p)
	var values map[string]any
	_ = json.Unmarshal(raw, &values)
	if values["pin_clicks"] != nil || values["outbound_clicks"] != nil || values["saves"] != float64(0) {
		t.Fatal(string(raw))
	}
	p = pinterestNormalized(json.RawMessage(`{"all":{"summary_metrics":{"OUTBOUND_CLICK":0}}}`))
	raw, _ = json.Marshal(p)
	_ = json.Unmarshal(raw, &values)
	if values["clicks"] != float64(0) || values["outbound_clicks"] != float64(0) {
		t.Fatal("zero alias lost", string(raw))
	}
}
func TestPinterestFreshnessPreservesPendingDatesAndEstimates(t *testing.T) {
	q := analyticsQuery{StartDate: "2026-10-24", EndDate: "2026-10-26"}
	series := pinterestDailyInsights(json.RawMessage(`{"all":{"daily_metrics":[{"date":"2026-10-24","data_status":"READY","metrics":{"IMPRESSION":10,"PIN_CLICK":0,"OUTBOUND_CLICK":0,"SAVE":0}},{"date":"2026-10-25","data_status":"PROCESSING","metrics":{"IMPRESSION":0}},{"date":"2026-10-26","data_status":"PROCESSING","metrics":{}}]}}`))
	r := newAnalyticsReport("post", "a", "provider", q, "range")
	setReportMetricInfo(r, []string{"impressions", "pin_clicks", "outbound_clicks", "saves"})
	finalizePinterestReport(r, series, q, []string{"impressions", "pin_clicks", "outbound_clicks", "saves"})
	if r.Window.Timezone != "UTC" || r.Freshness.ProcessedThrough != "2026-10-24" || len(r.Freshness.PendingDates) != 2 {
		t.Fatalf("%+v", r.Freshness)
	}
	raw, _ := json.Marshal(series)
	var data map[string][]map[string]any
	_ = json.Unmarshal(raw, &data)
	if data["impressions"][1]["value"] != nil || data["outbound_clicks"][0]["value"] != float64(0) {
		t.Fatal(string(raw))
	}
	series["impressions"][1] = insightPoint{Time: "2026-10-25", Value: 2, DataStatus: "estimated", Partial: true}
	series["impressions"][2] = insightPoint{Time: "2026-10-26", Value: 0, DataStatus: "ready"}
	f := seriesFreshness(series, []string{"impressions"}, q)
	if len(f.EstimatedDates) != 1 || f.ProcessedThrough != "2026-10-24" {
		t.Fatalf("claimed readiness across a gap: %+v", f)
	}
}
func TestPinterestAggregationCannotTurnPendingOrMissingPinsIntoZero(t *testing.T) {
	_, series := aggregatePostReport([]analyticsPostRow{
		{Status: "ok", Insights: insightSeries{"impressions": {{Time: "2026-10-24", Value: 3, DataStatus: "ready"}, {Time: "2026-10-25", Value: 0, DataStatus: "pending", Partial: true}}}},
		{Status: "ok", Insights: insightSeries{"impressions": {{Time: "2026-10-24", Value: 4, DataStatus: "ready"}, {Time: "2026-10-25", Value: 0, DataStatus: "ready"}}}},
	})
	if series["impressions"][0].Value != 7 || series["impressions"][1].DataStatus != "pending" {
		t.Fatal(series)
	}
	raw, _ := json.Marshal(series["impressions"][1])
	if !strings.Contains(string(raw), `"value":null`) {
		t.Fatal(string(raw))
	}
}
func TestAnalyticsCacheSchemaRefreshesOldReports(t *testing.T) {
	q := analyticsQuery{RangeDays: 30}
	key := q.cacheKey()
	if !strings.Contains(key, `"Version":4`) {
		t.Fatal(key)
	}
	start, end := q.dates()
	if start == end {
		t.Fatal("30 day window collapsed")
	}
}

func TestPinterestUnknownProcessingStatusDoesNotClaimReady(t *testing.T) {
	q := analyticsQuery{StartDate: "2026-10-24", EndDate: "2026-10-24"}
	series := pinterestDailyInsights(json.RawMessage(`{"all":{"daily_metrics":[{"date":"2026-10-24","metrics":{"IMPRESSION":3}}]}}`))
	r := newAnalyticsReport("post", "a", "provider", q, "range")
	setReportMetricInfo(r, []string{"impressions"})
	finalizePinterestReport(r, series, q, []string{"impressions"})
	setReportMetricInfo(r, []string{"impressions"})
	if r.Freshness.ProcessedThrough != "" || series["impressions"][0].Value != 3 || series["impressions"][0].DataStatus != "unknown" || r.MetricInfo["impressions"].Freshness == nil {
		t.Fatalf("missing readiness became ready or metadata lost: %+v", r)
	}
}
func TestBoardAuthenticationErrorsVisibleWithoutPostRows(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["list_board_pins"] = analyticsOK(`{"items":[{"id":"a"}]}`)
	pf.executeResponses["get_multiple_pin_analytics"] = &sdk.ExecuteResult{Status: 401, Data: json.RawMessage(`{"code":2,"message":"Authentication failed."}`)}
	ctx := newSocialCtx(t, pf)
	out := (&App{}).getPinterestBoardMetrics(ctx, accountMetricsResult{}, 42, "board", analyticsQuery{RangeDays: 7})
	if out.Status != "failed" || !strings.Contains(strings.Join(out.Report.Warnings, " "), "Authentication failed") {
		t.Fatalf("authentication error hidden: %+v", out)
	}
}
func TestPinterestOldReportCacheIsNotReused(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["list_board_pins"] = analyticsOK(`{"items":[{"id":"a"}]}`)
	pf.executeResponses["get_multiple_pin_analytics"] = analyticsOK(`{"a":` + pinA + `}`)
	ctx := newSocialCtx(t, pf)
	id := analyticsAccount(t, ctx, "test-proj", "pinterest", "board")
	args := analyticsQueryArgs(id)
	args["compare"] = false
	q := analyticsQueryFromArgs(args)
	oldKey := strings.Replace(q.cacheKey(), `"Version":4`, `"Version":3`, 1)
	_, err := ctx.AppDB().Exec(`INSERT INTO account_metric_cache(project_id,social_account_id,query_key,result,refreshed_at) VALUES(?,?,?,?,datetime('now'))`, "test-proj", id, oldKey, `{"status":"ok","impressions":999,"available":["impressions"]}`)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := (&App{}).toolAccountMetrics(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	out := raw.(accountMetricsResult)
	if out.Impressions != 100 || len(pf.executeCalls) != 2 || out.Report.MetricInfo["impressions"].Freshness == nil {
		t.Fatalf("stale report or freshness lost: %+v", out)
	}
}

func TestPinterestComparisonRetainsPreviousPeriodEstimates(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["list_board_pins"] = analyticsOK(`{"items":[{"id":"a"}]}`)
	pf.executeQueues["get_multiple_pin_analytics"] = []*sdk.ExecuteResult{
		analyticsOK(`{"a":{"all":{"summary_metrics":{"IMPRESSION":4,"PIN_CLICK":0,"OUTBOUND_CLICK":0,"SAVE":0},"daily_metrics":[{"date":"2026-10-24","data_status":"READY","metrics":{"IMPRESSION":4,"PIN_CLICK":0,"OUTBOUND_CLICK":0,"SAVE":0}}]}}}`),
		analyticsOK(`{"a":{"all":{"summary_metrics":{"IMPRESSION":2,"PIN_CLICK":0,"OUTBOUND_CLICK":0,"SAVE":0},"daily_metrics":[{"date":"2026-10-23","data_status":"ESTIMATE","metrics":{"IMPRESSION":2,"PIN_CLICK":0,"OUTBOUND_CLICK":0,"SAVE":0}}]}}}`),
	}
	ctx := newSocialCtx(t, pf)
	out := (&App{}).getPinterestBoardMetrics(ctx, accountMetricsResult{}, 42, "board", analyticsQuery{StartDate: "2026-10-24", EndDate: "2026-10-24", Compare: true})
	if out.Comparison == nil || out.Comparison.Status != "partial" || out.Comparison.Window.Timezone != "UTC" || out.Comparison.Freshness == nil || len(out.Comparison.Freshness.EstimatedDates) != 1 {
		t.Fatalf("previous estimates hidden: %+v", out.Comparison)
	}
}
