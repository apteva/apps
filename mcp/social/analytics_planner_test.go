package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// funcPlatform answers integration calls from a function, for calls whose
// order is not fixed (concurrent fan-out).
type funcPlatform struct {
	*recordingPlatform
	answer func(tool string, input map[string]any) *sdk.ExecuteResult
}

func (p funcPlatform) ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	_, _ = p.recordingPlatform.ExecuteIntegrationTool(connID, tool, input)
	return p.answer(tool, input), nil
}

func okJSON(body string) *sdk.ExecuteResult {
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(body)}
}

func TestPlanBreakdownFollowsVerifiedYouTubePairs(t *testing.T) {
	cases := []struct {
		dimension string
		filters   map[string][]string
		mode      string
		filter    string
	}{
		{"traffic_source", nil, breakdownDirect, ""},
		{"traffic_source", map[string][]string{"video": {"v1"}}, breakdownDirect, ""},
		{"device", map[string][]string{"os": {"android"}}, breakdownDirect, ""},
		{"video", map[string][]string{"country": {"US"}}, breakdownDirect, ""},
		{"video", map[string][]string{"traffic_source": {"related_video"}}, breakdownPerPost, "traffic_source"},
		{"video", map[string][]string{"sharing_service": {"WHATS_APP"}}, breakdownPerPost, "sharing_service"},
		{"video", map[string][]string{"device": {"tv"}}, breakdownPerPost, "device"},
		{"device", map[string][]string{"traffic_source": {"yt_search"}}, breakdownUnsupported, ""},
		{"country", map[string][]string{"device": {"tv"}}, breakdownUnsupported, ""},
		{"traffic_source", map[string][]string{"content_type": {"shorts"}}, breakdownUnsupported, ""},
	}
	for _, c := range cases {
		plan := planBreakdown("youtube", c.dimension, c.filters)
		if plan.Mode != c.mode || plan.Filter != c.filter {
			t.Errorf("%s filtered by %v: got %+v, want %s/%s", c.dimension, c.filters, plan, c.mode, c.filter)
		}
		if plan.Mode == breakdownUnsupported && !strings.Contains(plan.Reason, "Supported filters:") {
			t.Errorf("unsupported plan should list supported filters: %q", plan.Reason)
		}
	}
	if plan := planBreakdown("instagram", "country", map[string][]string{"age": {"18-24"}}); plan.Mode != breakdownUnsupported || !strings.Contains(plan.Reason, "no filters") {
		t.Fatalf("Instagram demographics must not be answered unfiltered: %+v", plan)
	}
	if plan := planBreakdown("tiktok", "country", map[string][]string{"age": {"x"}}); plan.Mode != breakdownUnsupported {
		t.Fatalf("unknown platform pairs are unsupported: %+v", plan)
	}
}

func TestYouTubeVideoBreakdownByTrafficSourceRunsPerVideo(t *testing.T) {
	pf := funcPlatform{recordingPlatform: newRecordingPlatform()}
	pf.answer = func(tool string, input map[string]any) *sdk.ExecuteResult {
		if tool != "query_analytics_report" {
			t.Errorf("unexpected tool %s", tool)
		}
		switch input["dimensions"] {
		case "video":
			if _, filtered := input["filters"]; filtered {
				t.Errorf("ranking query must not carry the traffic-source filter YouTube rejects: %v", input)
			}
			return okJSON(`{"columnHeaders":[{"name":"video","columnType":"DIMENSION"},{"name":"views","columnType":"METRIC"},{"name":"estimatedMinutesWatched","columnType":"METRIC"}],"rows":[["a",900,10],["b",500,5]]}`)
		case "insightTrafficSourceType":
			switch input["filters"] {
			case "video==a":
				return okJSON(`{"columnHeaders":[{"name":"insightTrafficSourceType","columnType":"DIMENSION"},{"name":"views","columnType":"METRIC"},{"name":"estimatedMinutesWatched","columnType":"METRIC"}],"rows":[["RELATED_VIDEO",100,3],["YT_SEARCH",800,7]]}`)
			case "video==b":
				return okJSON(`{"columnHeaders":[{"name":"insightTrafficSourceType","columnType":"DIMENSION"},{"name":"views","columnType":"METRIC"},{"name":"estimatedMinutesWatched","columnType":"METRIC"}],"rows":[["RELATED_VIDEO",400,4],["YT_SEARCH",100,1]]}`)
			}
		}
		t.Errorf("unexpected query %v", input)
		return okJSON(`{}`)
	}
	ctx := newSocialCtx(t, pf)
	query := analyticsQuery{RangeDays: 28, Limit: 2, Filters: map[string][]string{"traffic_source": {"related_video"}}}
	got := (&App{}).fetchYouTubeBreakdown(ctx, 7, "video", query)
	if got.Status != "ok" || got.Source != "youtube_analytics_per_video" || len(got.Rows) != 2 {
		t.Fatalf("%+v", got)
	}
	// Re-ranked by recommendation views: b (400) before a (100).
	minutes := canonicalMetricName("estimatedMinutesWatched")
	if got.Rows[0].Dimensions["video"] != "b" || got.Rows[0].Metrics["views"] != 400 || got.Rows[1].Metrics["views"] != 100 || got.Rows[1].Metrics[minutes] != 3 {
		t.Fatalf("per-video traffic-source rows wrong: %+v", got.Rows)
	}
	if !strings.Contains(got.Reason, "top 2 videos") {
		t.Fatalf("reason should explain the ranking: %q", got.Reason)
	}
	rank := pf.executeCalls[0].Input
	if rank["maxResults"] != 2 {
		t.Fatalf("ranking must be limited: %v", rank)
	}
}

func TestYouTubeSharingServiceIsReportedByShares(t *testing.T) {
	metrics, rank := youtubeBreakdownMetrics("sharing_service")
	input := youtubeBreakdownInput("sharing_service", analyticsQuery{RangeDays: 7}, nil)
	if metrics != "shares" || rank != "shares" || input["metrics"] != "shares" || input["sort"] != "-shares" {
		t.Fatalf("%v", input)
	}
	if input := youtubeBreakdownInput("device", analyticsQuery{RangeDays: 7}, nil); input["sort"] != "-views" {
		t.Fatalf("%v", input)
	}
}

func TestUnsupportedYouTubePairIsNotSent(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	got := (&App{}).fetchYouTubeBreakdown(ctx, 7, "device", analyticsQuery{RangeDays: 28, Filters: map[string][]string{"traffic_source": {"yt_search"}}})
	if got.Status != "unsupported" || len(pf.executeCalls) != 0 {
		t.Fatalf("got %+v with %d calls", got, len(pf.executeCalls))
	}
}

func TestInstagramFilteredBreakdownIsUnsupportedNotUnfiltered(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	out := accountMetricsResult{Platform: "instagram"}
	(&App{}).addAccountBreakdowns(ctx, &out, 7, "native", "", "ig1", "", analyticsQuery{Breakdowns: []string{"country"}, Filters: map[string][]string{"age": {"18-24"}}})
	for _, b := range out.Breakdowns {
		if b.Dimension == "country" && b.Status != "unsupported" {
			t.Fatalf("filtered Instagram breakdown should be unsupported: %+v", b)
		}
	}
	for _, call := range pf.executeCalls {
		if call.Input["breakdown"] == "country" {
			t.Fatalf("Instagram was asked for an unfiltered answer: %+v", call)
		}
	}
}

func TestYouTubeDecimalsAreKept(t *testing.T) {
	rows := parseYouTubeBreakdownRows(json.RawMessage(`{"columnHeaders":[{"name":"country","columnType":"DIMENSION","dataType":"STRING"},{"name":"views","columnType":"METRIC","dataType":"INTEGER"},{"name":"averageViewPercentage","columnType":"METRIC","dataType":"FLOAT"}],"rows":[["US",10,42.75]]}`), "country")
	if len(rows) != 1 || rows[0].DecimalMetrics[canonicalMetricName("averageViewPercentage")] != 42.75 || rows[0].DecimalMetrics["views"] != 0 {
		t.Fatalf("%+v", rows)
	}
	series := parseYoutubeAnalyticsSeries(json.RawMessage(`{"columnHeaders":[{"name":"day","dataType":"STRING"},{"name":"views","dataType":"INTEGER"},{"name":"averageViewPercentage","dataType":"FLOAT"}],"rows":[["2026-11-01",5,37.5]]}`))
	point := series["averageViewPercentage"][0]
	if point.Value != 37 || point.DecimalValue == nil || *point.DecimalValue != 37.5 || series["views"][0].DecimalValue != nil {
		t.Fatalf("%+v %+v", point, series["views"])
	}
}

func TestYouTubeThumbnailsLargestFirst(t *testing.T) {
	got := youtubeThumbnails(map[string]postThumbnail{
		"default": {URL: "d", Width: 120, Height: 90},
		"maxres":  {URL: "m", Width: 1280, Height: 720},
		"medium":  {URL: "md", Width: 320, Height: 180},
		"broken":  {},
	})
	if len(got) != 3 || got[0].Size != "maxres" || got[0].Width != 1280 || got[2].Size != "default" {
		t.Fatalf("%+v", got)
	}
}

func TestYouTubePostMetricsCarryThumbnails(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses = map[string]*sdk.ExecuteResult{"get_video": okJSON(`{"items":[{"statistics":{"viewCount":"5"},"snippet":{"thumbnails":{"high":{"url":"https://i.ytimg.com/vi/v/hqdefault.jpg","width":480,"height":360}}}}]}`)}
	ctx := newSocialCtx(t, pf)
	out := (&App{}).getYoutubePostMetrics(ctx, targetMetricsOutcome{PlatformPostID: "v"}, 7)
	if out.Status != "ok" || len(out.Thumbnails) != 1 || out.Thumbnails[0].Width != 480 || out.Thumbnails[0].Size != "high" {
		t.Fatalf("%+v", out)
	}
}

func TestYouTubeBreakdownFilteredOnItsOwnDimensionFiltersRows(t *testing.T) {
	pf := funcPlatform{recordingPlatform: newRecordingPlatform()}
	pf.answer = func(tool string, input map[string]any) *sdk.ExecuteResult {
		if _, filtered := input["filters"]; filtered {
			t.Errorf("own-dimension filter sent to YouTube: %v", input)
		}
		return okJSON(`{"columnHeaders":[{"name":"insightTrafficSourceType","columnType":"DIMENSION"},{"name":"views","columnType":"METRIC"}],"rows":[["RELATED_VIDEO",100],["YT_SEARCH",800]]}`)
	}
	ctx := newSocialCtx(t, pf)
	got := (&App{}).fetchYouTubeBreakdown(ctx, 7, "traffic_source", analyticsQuery{RangeDays: 28, Filters: map[string][]string{"traffic_source": {"related_video"}}})
	if got.Status != "ok" || len(got.Rows) != 1 || got.Rows[0].Dimensions["traffic_source"] != "related_video" || got.Rows[0].Metrics["views"] != 100 {
		t.Fatalf("%+v", got)
	}
}
