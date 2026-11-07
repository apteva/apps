package main

import (
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func analyticsOK(raw string) *sdk.ExecuteResult {
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(raw)}
}
func analyticsAccount(t *testing.T, ctx *sdk.AppCtx, project, platform, board string) int64 {
	t.Helper()
	r, e := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,external_account_id,display_name,status) VALUES(?,?,42,?,?,'active')`, project, platform, board, board)
	if e != nil {
		t.Fatal(e)
	}
	id, _ := r.LastInsertId()
	return id
}
func analyticsQueryArgs(id int64) map[string]any {
	return map[string]any{"social_account_id": id, "scope": "destination", "range": "7d", "group_by": []string{"post"}, "compare": true, "sort_by": "outbound_clicks"}
}

const pinA = `{"all":{"summary_metrics":{"IMPRESSION":100,"SAVE":0,"PIN_CLICK":7,"OUTBOUND_CLICK":3},"daily_metrics":[{"date":"2026-10-20","metrics":{"IMPRESSION":100,"SAVE":0,"PIN_CLICK":7,"OUTBOUND_CLICK":3}}]}}`

func TestBoardAnalyticsScopePaginationComparisonAndCache(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeQueues["list_board_pins"] = []*sdk.ExecuteResult{analyticsOK(`{"items":[{"id":"a","title":"First"}],"bookmark":"next"}`), analyticsOK(`{"items":[{"id":"a"},{"id":"b","title":"Second"}]}`)}
	pf.executeQueues["get_multiple_pin_analytics"] = []*sdk.ExecuteResult{analyticsOK(`{"a":` + pinA + `,"b":{"all":{"summary_metrics":{"IMPRESSION":40,"SAVE":0,"PIN_CLICK":1,"OUTBOUND_CLICK":1}}}}`), analyticsOK(`{"a":{"all":{"summary_metrics":{"IMPRESSION":50,"SAVE":0,"PIN_CLICK":3,"OUTBOUND_CLICK":1}}},"b":{"all":{"summary_metrics":{"IMPRESSION":20,"SAVE":0,"PIN_CLICK":1,"OUTBOUND_CLICK":1}}}}`)}
	ctx := newSocialCtx(t, pf)
	id := analyticsAccount(t, ctx, "test-proj", "pinterest", "board")
	app := &App{}
	args := analyticsQueryArgs(id)
	raw, e := app.toolAccountMetrics(ctx, args)
	if e != nil {
		t.Fatal(e)
	}
	out := raw.(accountMetricsResult)
	if out.Status != "ok" || out.Report.Scope != "destination" || out.Report.EntityID != "board" || !out.Report.Coverage.Complete || out.Report.Coverage.Discovered != 2 || out.Impressions != 140 || out.Followers != 0 || out.Views != 0 || out.OutboundClicks != 4 || out.PinClicks != 8 || !containsFold(out.Available, "saves") {
		t.Fatalf("bad report: %+v / %+v", out, out.Report)
	}
	if len(out.PostRows) != 2 || out.PostRows[0].ID != "a" || out.Comparison == nil || *out.Comparison.ChangePercent["impressions"] != 100 {
		t.Fatalf("ranking/comparison: %+v", out)
	}
	if len(pf.executeCalls) != 4 || pf.executeCalls[1].Input["bookmark"] != "next" || pf.executeCalls[0].Input["board_id"] != "board" {
		t.Fatalf("calls: %+v", pf.executeCalls)
	}
	if _, e = app.toolAccountMetrics(ctx, args); e != nil {
		t.Fatal(e)
	}
	if len(pf.executeCalls) != 4 {
		t.Fatal("exact query not cached")
	}
	args["scope"] = "provider_account"
	cached := app.storedAccountMetrics(ctx, "test-proj", id, analyticsQueryFromArgs(args))
	if cached.Status == "ok" {
		t.Fatal("board cache reused for account")
	}
	args["scope"] = "destination"
	ctx.AppDB().Exec(`UPDATE social_accounts SET external_account_id='other-board' WHERE id=?`, id)
	cached = app.storedAccountMetrics(ctx, "test-proj", id, analyticsQueryFromArgs(args))
	if cached.Status == "ok" {
		t.Fatal("cache survived reconnection to another destination")
	}
}
func TestBoardAnalyticsMissingMetricIsNotZero(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["list_board_pins"] = analyticsOK(`{"items":[{"id":"a"},{"id":"b"}]}`)
	pf.executeResponses["get_multiple_pin_analytics"] = analyticsOK(`{"a":` + pinA + `,"b":{"all":{"summary_metrics":{"IMPRESSION":40}}}}`)
	ctx := newSocialCtx(t, pf)
	id := analyticsAccount(t, ctx, "test-proj", "pinterest", "board")
	args := analyticsQueryArgs(id)
	args["compare"] = false
	raw, _ := (&App{}).toolAccountMetrics(ctx, args)
	out := raw.(accountMetricsResult)
	if out.Report.Coverage.Complete || out.Report.Coverage.MetricPosts["saves"] != 1 || out.Report.Coverage.MetricPosts["impressions"] != 2 {
		t.Fatalf("coverage: %+v", out.Report.Coverage)
	}
	if _, ok := out.PostRows[1].Metrics["saves"]; ok {
		t.Fatal("missing saves became zero")
	}
	var points int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM social_metric_points WHERE period='day'`).Scan(&points)
	if points != 0 {
		t.Fatal("partial series entered widget history")
	}
}
func TestBoardAnalyticsStandardAccessFallbackIsBounded(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_multiple_pin_analytics"] = &sdk.ExecuteResult{Status: 403, Success: false, Data: json.RawMessage(`{"message":"beta unavailable"}`)}
	pf.executeResponses["get_pin_analytics"] = analyticsOK(pinA)
	ctx := newSocialCtx(t, pf)
	pins := []pinterestReportPin{}
	for i := 0; i < 60; i++ {
		pins = append(pins, pinterestReportPin{ID: fmt.Sprint(i)})
	}
	rows, c, _ := (&App{}).pinterestBatchReport(ctx, 42, pins, analyticsQuery{RangeDays: 7})
	if len(rows) != 60 || c.Included != 50 || c.Failed != 10 || c.Complete || len(pf.executeCalls) != 51 {
		t.Fatalf("fallback unbounded or wrong: %+v, calls %d", c, len(pf.executeCalls))
	}
}
func TestBoardAnalyticsRateLimitStopsRequestsAndComparison(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_multiple_pin_analytics"] = &sdk.ExecuteResult{Status: 429, Success: false}
	ctx := newSocialCtx(t, pf)
	pins := make([]pinterestReportPin, 201)
	for i := range pins {
		pins[i].ID = fmt.Sprint(i)
	}
	_, c, _ := (&App{}).pinterestBatchReport(ctx, 42, pins, analyticsQuery{RangeDays: 7})
	if len(pf.executeCalls) != 1 || c.Failed != 201 || !c.ProviderStopped {
		t.Fatalf("rate limit: %+v calls %d", c, len(pf.executeCalls))
	}
}
func TestBoardAnalyticsInventoryLimitsAndMalformedListings(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		max       int
		count     int
		complete  bool
	}{{"empty", `{"items":[]}`, 10, 0, true}, {"missing items", `{}`, 10, 0, false}, {"truncated", `{"items":[{"id":"a"},{"id":"b"}]}`, 1, 1, false}, {"repeated bookmark", `{"items":[{"id":"a"}],"bookmark":"same"}`, 10, 1, false}} {
		t.Run(test.name, func(t *testing.T) {
			pf := newRecordingPlatform()
			pf.executeResponses["list_board_pins"] = analyticsOK(test.raw)
			ctx := newSocialCtx(t, pf)
			pins, c, _ := (&App{}).pinterestBoardInventory(ctx, 42, "board", analyticsQuery{MaxPosts: test.max})
			if len(pins) != test.count || c.Complete != test.complete || len(pf.executeCalls) > 2 {
				t.Fatalf("inventory: %+v, calls %d", c, len(pf.executeCalls))
			}
		})
	}
}
func TestAnalyticsValidationAndProviderDateLimit(t *testing.T) {
	for _, args := range []map[string]any{{"start_date": "nope"}, {"start_date": "2026-10-01"}, {"start_date": "2026-10-20", "end_date": "2026-10-01"}, {"scope": "arbitrary"}, {"max_posts": 1001}, {"limit": 0}, {"sort_by": "nonsense"}, {"range": "garbage"}} {
		if validateAnalyticsQuery(args, analyticsQueryFromArgs(args)) == nil {
			t.Fatalf("accepted %+v", args)
		}
	}
	if pinterestReportDates(analyticsQuery{RangeDays: 180}) == nil {
		t.Fatal("accepted unsupported range")
	}
	q := analyticsQuery{RangeDays: 7}
	prev := previousAnalyticsQuery(q)
	s, e := q.dates()
	ps, pe := prev.dates()
	if !(ps < pe && pe < s && s < e) {
		t.Fatalf("previous window %s/%s vs %s/%s", ps, pe, s, e)
	}
}
func TestAnalyticsAggregationNeverSumsReachOrAliases(t *testing.T) {
	rows := []analyticsPostRow{{Status: "ok", Metrics: map[string]int64{"reach": 100, "impressions": 120, "outbound_clicks": 2, "clicks": 2}}, {Status: "ok", Metrics: map[string]int64{"reach": 100, "impressions": 30, "outbound_clicks": 1}}}
	v, _ := aggregatePostReport(rows)
	if v["impressions"] != 150 || v["outbound_clicks"] != 3 {
		t.Fatal(v)
	}
	if _, ok := v["reach"]; ok {
		t.Fatal("summed unique reach")
	}
	if _, ok := v["clicks"]; ok {
		t.Fatal("summed alias")
	}
}
func TestAnalyticsComparisonRequiresMatchedCoverage(t *testing.T) {
	current := []analyticsPostRow{{ID: "a", Status: "ok", Metrics: map[string]int64{"impressions": 100}}, {ID: "b", Status: "failed"}}
	previous := []analyticsPostRow{{ID: "a", Status: "ok", Metrics: map[string]int64{"impressions": 50}}, {ID: "b", Status: "ok", Metrics: map[string]int64{"impressions": 50}}}
	out := comparePostReports(current, previous, analyticsQuery{RangeDays: 7}, &analyticsCoverage{Complete: true})
	if out.Status != "partial" || len(out.ChangePercent) != 0 {
		t.Fatalf("misleading percentage: %+v", out)
	}
}
func TestAnalyticsProjectIsolationAndPostCacheWindows(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_pin_analytics"] = analyticsOK(pinA)
	ctx := newSocialCtx(t, pf)
	other := analyticsAccount(t, ctx, "other", "pinterest", "secret")
	raw, _ := (&App{}).toolAccountMetrics(ctx, analyticsQueryArgs(other))
	if raw.(accountMetricsResult).Status != "failed" || len(pf.executeCalls) != 0 {
		t.Fatal("cross-project provider request")
	}
	target := metricsTarget{TargetID: 2, SocialAccountID: 1, ConnID: 42, Platform: "pinterest", ExtPostID: "a"}
	app := &App{}
	for _, days := range []int{7, 7, 28} {
		out := app.getPostMetricsQuery(ctx, target, analyticsQuery{RangeDays: days})
		if out.Report.Window.Kind != "range" {
			t.Fatal(out)
		}
	}
	if len(pf.executeCalls) != 2 {
		t.Fatal("cache ignored date window")
	}
	app.getPostMetricsQuery(ctx.WithProject("other"), target, analyticsQuery{RangeDays: 7})
	if len(pf.executeCalls) != 3 {
		t.Fatal("cache leaked across projects")
	}
}
func TestAccountAnalyticsWholeAccountDeduplicated(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_user_account"] = analyticsOK(`{"username":"same","follower_count":0,"pin_count":2}`)
	pf.executeResponses["get_user_analytics"] = analyticsOK(pinA)
	ctx := newSocialCtx(t, pf)
	a := analyticsAccount(t, ctx, "test-proj", "pinterest", "a")
	b := analyticsAccount(t, ctx, "test-proj", "pinterest", "b")
	raw, _ := (&App{}).toolAccountMetrics(ctx, map[string]any{"social_account_ids": []int64{a, b}, "scope": "provider_account", "range": "7d", "metric": "impressions"})
	out := raw.(map[string]any)
	if len(out["accounts"].([]accountMetricsResult)) != 1 || len(out["comparison"].(analyticsBreakdown).Rows) != 1 {
		t.Fatal("whole account double counted")
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM social_metric_points WHERE scope='account'`).Scan(&count)
	if count != 0 {
		t.Fatal("provider totals leaked into destination history")
	}
}
func TestPostAnalyticsBatchAndRESTPassDateRange(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_pin_analytics"] = analyticsOK(pinA)
	ctx := newSocialCtx(t, pf)
	id := analyticsAccount(t, ctx, "test-proj", "pinterest", "board")
	postIDs := []int64{}
	for i := 0; i < 2; i++ {
		r, _ := ctx.AppDB().Exec(`INSERT INTO posts(project_id,body,status) VALUES('test-proj','test','published')`)
		post, _ := r.LastInsertId()
		ctx.AppDB().Exec(`INSERT INTO post_targets(post_id,social_account_id,status,platform_post_id) VALUES(?,?,'published',?)`, post, id, fmt.Sprint(i))
		postIDs = append(postIDs, post)
	}
	app := &App{}
	raw, err := app.toolPostMetrics(ctx, map[string]any{"post_ids": postIDs, "range": "7d"})
	if err != nil || len(raw.(map[string]any)["posts"].([]any)) != 2 {
		t.Fatalf("batch: %v %v", raw, err)
	}
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/posts/%d/metrics?project_id=test-proj&range=28d", postIDs[0]), nil)
	w := httptest.NewRecorder()
	app.handlePostsItem(w, req)
	if w.Code != 200 || len(pf.executeCalls) != 3 || !strings.Contains(w.Body.String(), `"impressions":100`) {
		t.Fatalf("route: %d %s", w.Code, w.Body.String())
	}
}
func TestPublicationCohortAndNonWindowedAdapters(t *testing.T) {
	q := analyticsQuery{StartDate: "2026-10-01", EndDate: "2026-10-10", PublishedInRange: true}
	pins := []pinterestReportPin{{ID: "a", CreatedAt: "2026-10-05T12:00:00Z"}, {ID: "old", CreatedAt: "2026-09-01T00:00:00Z"}, {ID: "unknown"}}
	got, unknown := filterPinterestInventory(pins, q)
	if len(got) != 1 || got[0].ID != "a" || !unknown {
		t.Fatal("cohort filtering")
	}
	pf := newRecordingPlatform()
	pf.executeResponses["get_video"] = analyticsOK(`{"items":[{"statistics":{"viewCount":"0","likeCount":"0","commentCount":"0"}}]}`)
	ctx := newSocialCtx(t, pf)
	out := (&App{}).getPostMetricsQuery(ctx, metricsTarget{Platform: "youtube", ConnID: 42, ExtPostID: "video"}, analyticsQuery{RangeDays: 7})
	if out.Report.Window.Kind != "lifetime" || !metricAvailable(out.Metrics, "views") || out.Metrics.Views != 0 {
		t.Fatalf("lifetime/zero lost: %+v", out)
	}
}
func TestAnalyticsCacheKeySeparatesScopeCohortAndComparison(t *testing.T) {
	q := analyticsQuery{RangeDays: 7}
	seen := map[string]bool{}
	for _, change := range []func(*analyticsQuery){func(q *analyticsQuery) {}, func(q *analyticsQuery) { q.Scope = "destination" }, func(q *analyticsQuery) { q.Scope = "provider_account" }, func(q *analyticsQuery) { q.Compare = true }, func(q *analyticsQuery) { q.PublishedInRange = true }, func(q *analyticsQuery) { q.MaxPosts = 10 }} {
		c := q
		change(&c)
		key := c.cacheKey()
		if seen[key] {
			t.Fatal("cache collision")
		}
		seen[key] = true
	}
	q.ForceRefresh = true
	if q.cacheKey() != (analyticsQuery{RangeDays: 7}).cacheKey() {
		t.Fatal("refresh changed cache identity")
	}
}
func TestPostMetricsCacheExpires(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_pin_analytics"] = analyticsOK(pinA)
	ctx := newSocialCtx(t, pf)
	target := metricsTarget{TargetID: 1, SocialAccountID: 1, Platform: "pinterest", ConnID: 42, ExtPostID: "a"}
	app := &App{}
	q := analyticsQuery{RangeDays: 7}
	app.getPostMetricsQuery(ctx, target, q)
	ctx.AppDB().Exec(`UPDATE post_analytics_cache SET refreshed_at=?`, time.Now().UTC().Add(-5*time.Hour).Format(time.RFC3339))
	app.getPostMetricsQuery(ctx, target, q)
	if len(pf.executeCalls) != 2 {
		t.Fatal("stale cache reused")
	}
}

func TestAnalyticsSortingReusesFetchedReport(t *testing.T) {
	q := analyticsQuery{RangeDays: 7, GroupBy: []string{"post"}}
	alternate := q
	alternate.SortBy = "saves"
	alternate.Limit = 1
	if q.cacheKey() != alternate.cacheKey() {
		t.Fatal("display sort/limit caused another provider fetch")
	}
	source := accountMetricsResult{PostRows: []analyticsPostRow{{ID: "a", Metrics: map[string]int64{"saves": 1}}, {ID: "b", Metrics: map[string]int64{"saves": 5}}}}
	shown := presentAccountReport(source, alternate)
	if len(shown.PostRows) != 1 || shown.PostRows[0].ID != "b" || len(source.PostRows) != 2 || source.PostRows[0].ID != "a" {
		t.Fatal("presentation mutated cached inventory")
	}
}
func TestPinterestDailyStatusAndCoverage(t *testing.T) {
	series := pinterestDailyInsights(json.RawMessage(`{"all":{"daily_metrics":[{"date":"2026-10-01","data_status":"PROCESSING","metrics":{"IMPRESSION":0}},{"date":"2026-10-02","data_status":"ESTIMATE","metrics":{"IMPRESSION":4}},{"date":"2026-10-03","data_status":"READY","metrics":{"IMPRESSION":0}}]}}`))
	if len(series["impressions"]) != 3 || series["impressions"][0].DataStatus != "pending" || series["impressions"][1].DataStatus != "estimated" || series["impressions"][2].Value != 0 {
		t.Fatalf("invalid daily statuses: %+v", series)
	}
	_, daily := aggregatePostReport([]analyticsPostRow{{Status: "ok", Insights: series}, {Status: "ok"}})
	if !daily["impressions"][1].Partial || daily["impressions"][1].Included != 1 || daily["impressions"][1].Expected != 2 {
		t.Fatal("missing daily coverage")
	}
}

func TestAnalyticsRelativeWindowsHaveExactInclusiveLength(t *testing.T) {
	for _, days := range []int{1, 7, 28, 90} {
		q := analyticsQuery{RangeDays: days}
		start, end := q.dates()
		s, _ := time.Parse("2006-01-02", start)
		e, _ := time.Parse("2006-01-02", end)
		if int(e.Sub(s).Hours()/24)+1 != days {
			t.Fatalf("%d-day window: %s – %s", days, start, end)
		}
	}
}

func TestYouTubePeriodTotalsRemainSeparateFromLifetimeAndCompare(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_my_channel"] = analyticsOK(`{"items":[{"statistics":{"viewCount":"10000","subscriberCount":"20","videoCount":"5"}}]}`)
	pf.executeQueues["query_analytics_report"] = []*sdk.ExecuteResult{
		analyticsOK(`{"columnHeaders":[{"name":"day"},{"name":"views"},{"name":"likes"}],"rows":[["2026-10-20",100,0],["2026-10-21",50,0]]}`),
		analyticsOK(`{"columnHeaders":[{"name":"day"},{"name":"views"},{"name":"likes"}],"rows":[["2026-10-13",75,0]]}`),
	}
	ctx := newSocialCtx(t, pf)
	id := analyticsAccount(t, ctx, "test-proj", "youtube", "channel")
	raw, err := (&App{}).toolAccountMetrics(ctx, map[string]any{"social_account_id": id, "range": "7d", "compare": true})
	if err != nil {
		t.Fatal(err)
	}
	out := raw.(accountMetricsResult)
	if out.Views != 10000 || out.PeriodMetrics["views"] != 150 || out.Report.MetricInfo["views"].Kind != "lifetime" || *out.Comparison.ChangePercent["views"] != 100 {
		t.Fatalf("mixed lifetime and period: %+v", out)
	}
	if _, ok := out.PeriodMetrics["likes"]; !ok {
		t.Fatal("period zero omitted")
	}
}
func TestPinterestAccountOnlyProfileStillReportsWholeAccountScope(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_user_account"] = analyticsOK(`{"username":"creator","follower_count":0,"pin_count":5}`)
	pf.executeResponses["get_user_analytics"] = &sdk.ExecuteResult{Status: 403}
	ctx := newSocialCtx(t, pf)
	id := analyticsAccount(t, ctx, "test-proj", "pinterest", "board")
	raw, _ := (&App{}).toolAccountMetrics(ctx, map[string]any{"social_account_id": id})
	out := raw.(accountMetricsResult)
	if out.Report.Scope != "provider_account" || out.Report.Window.Kind != "snapshot" || !containsFold(out.Available, "followers") {
		t.Fatalf("profile scope: %+v", out)
	}
}

func TestAccountComparisonUsesCompatiblePeriodMetrics(t *testing.T) {
	results := []accountMetricsResult{
		{SocialAccountID: 1, Status: "ok", Views: 10000, Available: []string{"views"}, Report: &analyticsReport{Identity: "youtube", MetricInfo: map[string]analyticsMetricInfo{"views": {Kind: "lifetime"}}}, PeriodMetrics: map[string]int64{"views": 100}},
		{SocialAccountID: 2, Status: "ok", Views: 900, Available: []string{"views"}, Report: &analyticsReport{Identity: "other", MetricInfo: map[string]analyticsMetricInfo{"views": {Kind: "lifetime"}}}},
	}
	out := aggregateAccountComparison(results, "views", nil)
	if len(out.Rows) != 1 || out.Rows[0].Metrics["views"] != 100 || out.Rows[0].Dimensions["metric_kind"] != "range" {
		t.Fatalf("mixed lifetime and period: %+v", out)
	}
}

func TestBoardProviderFailureBacksOffAndExplicitRefreshBypasses(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["list_board_pins"] = &sdk.ExecuteResult{Status: 429}
	ctx := newSocialCtx(t, pf)
	id := analyticsAccount(t, ctx, "test-proj", "pinterest", "board")
	app := &App{}
	args := analyticsQueryArgs(id)
	for i := 0; i < 2; i++ {
		raw, _ := app.toolAccountMetrics(ctx, args)
		if raw.(accountMetricsResult).Status != "failed" {
			t.Fatal(raw)
		}
	}
	if len(pf.executeCalls) != 1 {
		t.Fatal("failed board request repeated without backoff")
	}
	args["refresh"] = true
	app.toolAccountMetrics(ctx, args)
	if len(pf.executeCalls) != 2 {
		t.Fatal("explicit refresh ignored")
	}
}

func TestEmptyBoardIsACompleteZeroReport(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["list_board_pins"] = analyticsOK(`{"items":[]}`)
	ctx := newSocialCtx(t, pf)
	id := analyticsAccount(t, ctx, "test-proj", "pinterest", "empty")
	raw, _ := (&App{}).toolAccountMetrics(ctx, map[string]any{"social_account_id": id, "scope": "destination", "range": "7d"})
	out := raw.(accountMetricsResult)
	if out.Status != "ok" || !out.Report.Coverage.Complete || !containsFold(out.Available, "impressions") || out.Impressions != 0 || len(pf.executeCalls) != 1 {
		t.Fatalf("empty board: %+v", out)
	}
}
