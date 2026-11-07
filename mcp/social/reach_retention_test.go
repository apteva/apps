package main

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func insertYouTubeAccount(t *testing.T, ctx *sdk.AppCtx, id int64) {
	t.Helper()
	if _, err := ctx.AppDB().Exec(`INSERT INTO social_accounts(id,project_id,platform,connection_id,display_name,status) VALUES(?,'test-proj','youtube',7,'HGV','active')`, id); err != nil {
		t.Fatal(err)
	}
}

func TestYouTubeRetentionCurveKeepsDecimalsAndSummarizes(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses = map[string]*sdk.ExecuteResult{"get_video": okJSON(`{"items":[{"snippet":{"publishedAt":"2025-03-15T18:00:27Z"}}]}`), "query_analytics_report": okJSON(`{"rows":[[0.0,1.12,0.61],[0.1,0.58,0.44],[0.25,0.47,0.5],[0.5,0.39,0.52],[0.75,0.31,0.55],[1.0,0.12,0.5]]}`)}
	ctx := newSocialCtx(t, pf)
	insertYouTubeAccount(t, ctx, 5)
	raw, err := (&App{}).toolPostRetention(ctx, map[string]any{"social_account_id": float64(5), "platform_post_id": "vid1"})
	if err != nil {
		t.Fatal(err)
	}
	curve := raw.(map[string]any)["targets"].([]retentionCurve)[0]
	if curve.Status != "ok" || len(curve.Points) != 6 || curve.Points[1].AudienceWatchRatio != 0.58 || *curve.Points[1].RelativeRetention != 0.44 {
		t.Fatalf("%+v", curve)
	}
	if curve.Summary["start"] != 1.12 || curve.Summary["at_10pct"] != 0.58 || curve.Summary["at_50pct"] != 0.39 {
		t.Fatalf("summary %+v", curve.Summary)
	}
	input := analyticsCall(t, pf)
	if input["dimensions"] != "elapsedVideoTimeRatio" || input["filters"] != "video==vid1;audienceType==ORGANIC" || !strings.Contains(input["metrics"].(string), "audienceWatchRatio") || input["startDate"] != "2025-03-15" || curve.StartDate != "2025-03-15" {
		t.Fatalf("query %+v", input)
	}
}

func TestRetentionEmptyUnsupportedAndAudience(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses = map[string]*sdk.ExecuteResult{"query_analytics_report": okJSON(`{"rows":[]}`)}
	ctx := newSocialCtx(t, pf)
	insertYouTubeAccount(t, ctx, 5)
	if _, err := ctx.AppDB().Exec(`INSERT INTO social_accounts(id,project_id,platform,connection_id,display_name,status) VALUES(6,'test-proj','tiktok',8,'T','active')`); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	raw, _ := app.toolPostRetention(ctx, map[string]any{"social_account_id": float64(5), "platform_post_id": "new", "audience_type": "all"})
	curve := raw.(map[string]any)["targets"].([]retentionCurve)[0]
	if input := analyticsCall(t, pf); curve.Status != "empty" || input["filters"] != "video==new" || input["startDate"] != time.Now().UTC().AddDate(-1, 0, 0).Format("2006-01-02") {
		t.Fatalf("%+v %+v", curve, input)
	}
	pf.executeCalls = nil
	_, _ = app.toolPostRetention(ctx, map[string]any{"social_account_id": float64(5), "platform_post_id": "new", "start_date": "2026-10-01", "end_date": "2026-10-31"})
	if input := analyticsCall(t, pf); input["startDate"] != "2026-10-01" || input["endDate"] != "2026-10-31" || len(pf.executeCalls) != 1 {
		t.Fatalf("explicit dates must be used without a publish-date lookup: %+v", pf.executeCalls)
	}
	raw, _ = app.toolPostRetention(ctx, map[string]any{"social_account_id": float64(6), "platform_post_id": "t1"})
	if curve := raw.(map[string]any)["targets"].([]retentionCurve)[0]; curve.Status != "unsupported" {
		t.Fatalf("%+v", curve)
	}
	if out, _ := app.toolPostRetention(ctx, map[string]any{"social_account_id": float64(5), "platform_post_id": "v", "audience_type": "paid"}); !strings.Contains(string(mustJSON(t, out)), "audience_type must be") {
		t.Fatalf("%v", out)
	}
}

func TestFacebookRetentionGraph(t *testing.T) {
	points := parseFacebookRetention(json.RawMessage(`{"data":[{"name":"total_video_retention_graph","period":"lifetime","values":[{"value":{"0":1,"1":0.6,"2":0.5,"3":0.2,"4":0.1}}]}]}`))
	if len(points) != 5 || points[0].Position != 0 || points[4].Position != 1 || points[1].Position != 0.25 || points[1].AudienceWatchRatio != 0.6 {
		t.Fatalf("%+v", points)
	}
}

func analyticsCall(t *testing.T, pf *recordingPlatform) map[string]any {
	t.Helper()
	for _, call := range pf.executeCalls {
		if call.Tool == "query_analytics_report" {
			return call.Input
		}
	}
	t.Fatal("no analytics query")
	return nil
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const reachCSV = "date,channel_id,video_id,live_or_on_demand,subscribed_status,country_code,video_thumbnail_impressions,video_thumbnail_impressions_ctr\n" +
	"20261030,UC1,a,on_demand,subscribed,US,1000,0.05\n" +
	"20261030,UC1,a,on_demand,unsubscribed,FR,3000,0.03\n" +
	"20261030,UC1,b,on_demand,subscribed,US,500,0.1\n" +
	"20261031,UC1,a,on_demand,subscribed,US,2000,0.04\n"

func TestParseReachCSVAggregatesPerDayAndVideo(t *testing.T) {
	rows, err := parseYouTubeReachCSV([]byte(reachCSV))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].day != "2026-10-30" || rows[0].videoID != "a" || rows[0].impressions != 4000 || math.Abs(rows[0].clicks-140) > 1e-9 {
		t.Fatalf("%+v", rows)
	}
	percent, _ := parseYouTubeReachCSV([]byte("date,video_id,video_thumbnail_impressions,video_thumbnail_impressions_ctr\n20261030,a,1000,5.0\n20261030,b,1000,0.5\n"))
	if math.Abs(percent[0].clicks-50) > 1e-9 || math.Abs(percent[1].clicks-5) > 1e-9 {
		t.Fatalf("percent CTR must be scaled for the whole file: %+v", percent)
	}
	if _, err := parseYouTubeReachCSV([]byte("date,video_id,views\n20261030,a,1\n")); err == nil {
		t.Fatal("a file without impressions columns must fail, not store zeros")
	}
}

func TestReportIDsOnlyFromReportingHost(t *testing.T) {
	c, j, r, err := youtubeReportIDs("https://youtubereporting.googleapis.com/v1/media/CHANNEL/UC1/jobs/job-1/reports/rep-9?alt=media")
	if err != nil || c != "UC1" || j != "job-1" || r != "rep-9" {
		t.Fatal(c, j, r, err)
	}
	for _, bad := range []string{"https://evil.example/v1/media/CHANNEL/UC1/jobs/j/reports/r", "https://youtubereporting.googleapis.com/v1/media/CONTENT_OWNER/x/jobs/j/reports/r", "https://youtubereporting.googleapis.com/v1/media/CHANNEL/UC1/jobs/j/reports/r/extra"} {
		if _, _, _, err := youtubeReportIDs(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestYouTubeReachSyncCreatesJobImportsReportsAndFeedsMetrics(t *testing.T) {
	pf := newRecordingPlatform()
	file := okJSON(`{"_binary":true,"base64":"` + base64.StdEncoding.EncodeToString([]byte(reachCSV)) + `","mimeType":"application/octet-stream"}`)
	pf.executeResponses = map[string]*sdk.ExecuteResult{
		"list_reporting_jobs":         okJSON(`{"jobs":[{"id":"other","reportTypeId":"channel_basic_a2"}]}`),
		"list_reporting_report_types": okJSON(`{"reportTypes":[{"id":"channel_basic_a2"},{"id":"channel_reach_basic_a1"}]}`),
		"create_reporting_job":        okJSON(`{"id":"job-1","createTime":"2026-10-29T00:00:00Z"}`),
		"list_reporting_reports":      okJSON(`{"reports":[{"id":"r2","createTime":"2026-11-01T10:00:00Z","downloadUrl":"https://youtubereporting.googleapis.com/v1/media/CHANNEL/UC1/jobs/job-1/reports/r2?alt=media"},{"id":"r1","createTime":"2026-10-31T10:00:00Z","downloadUrl":"https://youtubereporting.googleapis.com/v1/media/CHANNEL/UC1/jobs/job-1/reports/r1?alt=media"}]}`),
		"download_reporting_report":   file,
	}
	ctx := newSocialCtx(t, pf)
	insertYouTubeAccount(t, ctx, 5)
	app := &App{}
	if err := app.syncYouTubeReach(ctx, "test-proj", 5, 7); err != nil {
		t.Fatal(err)
	}
	var created, downloads int
	for _, call := range pf.executeCalls {
		switch call.Tool {
		case "create_reporting_job":
			created++
			if call.Input["reportTypeId"] != "channel_reach_basic_a1" {
				t.Fatalf("wrong report type %+v", call.Input)
			}
		case "download_reporting_report":
			downloads++
			if call.Input["channelId"] != "UC1" || call.Input["alt"] != "media" {
				t.Fatalf("download input %+v", call.Input)
			}
		}
	}
	if created != 1 || downloads != 2 {
		t.Fatalf("created=%d downloads=%d", created, downloads)
	}
	var cursor, state string
	_ = ctx.AppDB().QueryRow(`SELECT reports_cursor,state FROM reach_jobs WHERE social_account_id=5`).Scan(&cursor, &state)
	if cursor != "2026-11-01T10:00:00Z" || state != reachOK {
		t.Fatalf("cursor=%s state=%s", cursor, state)
	}
	// Re-importing the same days replaces them instead of doubling.
	var total int64
	_ = ctx.AppDB().QueryRow(`SELECT SUM(impressions) FROM reach_daily WHERE social_account_id=5`).Scan(&total)
	if total != 6500 {
		t.Fatalf("impressions %d", total)
	}

	post := targetMetricsOutcome{SocialAccountID: 5, PlatformPostID: "a", Metrics: &normalizedMetrics{Available: []string{"views"}}}
	addYouTubeVideoReach(ctx, &post)
	if post.Metrics.Impressions != 6000 || math.Abs(*post.Metrics.ImpressionsCTR-220.0/6000) > 1e-6 || !containsFold(post.Metrics.Available, "impressions_ctr") || post.ImpressionsCoverage.FirstDay != "2026-10-30" {
		t.Fatalf("%+v %+v", post.Metrics, post.ImpressionsCoverage)
	}
	account := accountMetricsResult{SocialAccountID: 5}
	addYouTubeReach(ctx, &account, analyticsQuery{StartDate: "2026-10-31", EndDate: "2026-10-31"})
	if account.Impressions != 2000 || *account.ImpressionsCTR != 0.04 || len(account.Insights["impressions"]) != 1 || *account.Insights["impressions_ctr"][0].DecimalValue != 0.04 {
		t.Fatalf("%+v", account)
	}

	// The next sync reuses the job and asks only for newer reports.
	_, _ = ctx.AppDB().Exec(`UPDATE reach_jobs SET last_checked_at=? WHERE social_account_id=5`, time.Now().Add(-7*time.Hour).UTC().Format(time.RFC3339))
	pf.executeCalls = nil
	if err := app.syncYouTubeReach(ctx, "test-proj", 5, 7); err != nil {
		t.Fatal(err)
	}
	for _, call := range pf.executeCalls {
		if call.Tool == "create_reporting_job" || call.Tool == "list_reporting_jobs" {
			t.Fatalf("job should be reused: %+v", call)
		}
		if call.Tool == "list_reporting_reports" && call.Input["createdAfter"] != "2026-11-01T10:00:00Z" {
			t.Fatalf("cursor not used: %+v", call.Input)
		}
	}
}

func TestReachNeedsPlatformUpdateWhenToolsAreMissing(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses = map[string]*sdk.ExecuteResult{"list_reporting_jobs": {Success: false, Status: 404, Data: json.RawMessage(`{"error":"tool not found: list_reporting_jobs"}`)}}
	ctx := newSocialCtx(t, pf)
	insertYouTubeAccount(t, ctx, 5)
	if err := (&App{}).syncYouTubeReach(ctx, "test-proj", 5, 7); err == nil {
		t.Fatal("expected error")
	}
	coverage := loadReachCoverage(ctx.AppDB(), 5)
	if coverage.State != reachNeedsUpdate || !strings.Contains(coverage.Note, "update the platform") {
		t.Fatalf("%+v", coverage)
	}
	if reachDue(ctx.AppDB(), 5, time.Now().UTC()) {
		t.Fatal("a failed check still waits for the next interval")
	}
}
