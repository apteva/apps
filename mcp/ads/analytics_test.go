package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestPerformanceGetGoogle_DerivesAndCachesMetrics(t *testing.T) {
	pf := newRecordingPlatform()
	callCount := 0
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		callCount++
		if tool != "search" {
			t.Fatalf("tool=%s", tool)
		}
		query := input["query"].(string)
		for _, field := range []string{
			"segments.date", "campaign.id", "metrics.cost_micros",
			"metrics.conversions_value", "metrics.video_views",
			"segments.date BETWEEN '2026-07-01' AND '2026-07-31'",
		} {
			if !strings.Contains(query, field) {
				t.Fatalf("query missing %q: %s", field, query)
			}
		}
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{
			"results":[{
				"segments":{"date":"2026-07-15"},
				"customer":{"currencyCode":"EUR"},
				"campaign":{"id":"123","name":"Generic reporting"},
				"metrics":{
					"costMicros":"100000000","impressions":"10000","clicks":"200",
					"conversions":"10","conversionsValue":"250.5","videoViews":"80"
				}
			}]
		}`)}, nil
	}
	ctx := newAdsCtx(t, pf)
	accountID := addPerformanceAccount(t, ctx, "test-proj", "google", "9876543210", "EUR", "Europe/Madrid")
	args := map[string]any{
		"ad_account_id": accountID, "level": "campaign",
		"date_from": "2026-07-01", "date_to": "2026-07-31",
	}

	out, err := (&App{}).toolPerformanceGet(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	data := result["data"].([]map[string]any)
	if result["source"] != "live" || len(data) != 1 {
		t.Fatalf("result=%#v", result)
	}
	row := data[0]
	if row["entity_id"] != "123" || row["spend_micros"] != int64(100000000) || row["conversion_value_micros"] != int64(250500000) {
		t.Fatalf("normalized row=%#v", row)
	}
	if row["ctr"] != 2.0 || row["cpc_micros"] != int64(500000) || row["cpm_micros"] != int64(10000000) || row["cpa_micros"] != int64(10000000) || row["roas"] != 2.505 {
		t.Fatalf("derived metrics=%#v", row)
	}

	args["refresh"] = false
	cached, err := (&App{}).toolPerformanceGet(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if callCount != 1 || cached.(map[string]any)["source"] != "cache" {
		t.Fatalf("cache missed: calls=%d result=%#v", callCount, cached)
	}
	cachedRows := cached.(map[string]any)["data"].([]map[string]any)
	if len(cachedRows) != 1 || cachedRows[0]["roas"] != 2.505 {
		t.Fatalf("cached metrics changed: %#v", cachedRows)
	}
}

func TestPerformanceGetMeta_UsesGenericLevelAndRichMetrics(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if tool != "insights_get" || input["level"] != "adset" {
			t.Fatalf("unexpected call: tool=%s input=%#v", tool, input)
		}
		fields := input["fields"].(string)
		for _, field := range []string{"adset_id", "reach", "inline_link_clicks", "action_values", "video_play_actions"} {
			if !strings.Contains(fields, field) {
				t.Fatalf("fields missing %q: %s", field, fields)
			}
		}
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{
			"data":[{
				"campaign_id":"10","campaign_name":"Leads","adset_id":"20","adset_name":"Madrid",
				"date_start":"2026-07-03","spend":"12.345678","impressions":"800","reach":"700",
				"clicks":"40","inline_link_clicks":"32","actions":[{"action_type":"lead","value":"8"}],
				"action_values":[{"action_type":"purchase","value":"55.25"}],
				"video_play_actions":[{"action_type":"video_view","value":"120"}]
			}]
		}`)}, nil
	}
	ctx := newAdsCtx(t, pf)
	accountID := addPerformanceAccount(t, ctx, "test-proj", "meta", "act_123", "EUR", "Europe/Madrid")
	out, err := (&App{}).toolPerformanceGet(ctx, map[string]any{
		"ad_account_id": accountID, "level": "ad_group",
		"date_from": "2026-07-01", "date_to": "2026-07-07",
	})
	if err != nil {
		t.Fatal(err)
	}
	row := out.(map[string]any)["data"].([]map[string]any)[0]
	if row["entity_id"] != "20" || row["campaign_id"] != "10" || row["reach"] != int64(700) || row["link_clicks"] != int64(32) {
		t.Fatalf("identity/delivery metrics=%#v", row)
	}
	if row["conversions"] != float64(8) || row["conversion_value_micros"] != int64(55250000) || row["video_views"] != int64(120) {
		t.Fatalf("outcome metrics=%#v", row)
	}
}

func TestPerformanceGetX_ChunksNativeStatsAndNormalizesMicros(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "list_active_entities":
			return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"data":["abc123"]}`)}, nil
		case "get_stats":
			if input["entity"] != "CAMPAIGN" || input["granularity"] != "DAY" || input["entity_ids"] != "abc123" {
				t.Fatalf("X stats input=%#v", input)
			}
			return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{
				"data":[{"id":"abc123","id_data":[{"metrics":{
					"billed_charge_local_micro":[1250000],"impressions":[1000],"clicks":[25],
					"url_clicks":[20],"video_total_views":[80],"conversion_purchases":[3]
				}}]}]
			}`)}, nil
		default:
			t.Fatalf("unexpected X tool %s", tool)
			return nil, nil
		}
	}
	ctx := newAdsCtx(t, pf)
	accountID := addPerformanceAccount(t, ctx, "test-proj", "x", "18ce54d4x5t", "EUR", "UTC")
	out, err := (&App{}).toolPerformanceGet(ctx, map[string]any{
		"ad_account_id": accountID, "level": "campaign", "date_from": "2026-08-07", "date_to": "2026-08-07",
	})
	if err != nil {
		t.Fatal(err)
	}
	row := out.(map[string]any)["data"].([]map[string]any)[0]
	if row["entity_id"] != "abc123" || row["spend_micros"] != int64(1250000) || row["conversions"] != float64(3) || row["video_views"] != int64(80) {
		t.Fatalf("X normalized row=%#v", row)
	}
}

func TestPerformanceGetReddit_FollowsOpaqueReportPagination(t *testing.T) {
	pf := newRecordingPlatform()
	calls := 0
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if tool != "get_report" {
			t.Fatalf("tool=%s", tool)
		}
		calls++
		if calls == 1 {
			data := asMap(input["data"])
			if data == nil || data["breakdowns"] == nil || data["starts_at"] != "2026-08-01T00:00:00Z" {
				t.Fatalf("Reddit report input=%#v", input)
			}
			return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{
				"data":{"metrics":[{"date":"2026-08-01","campaign_id":"c1","spend":1000000,"impressions":100,"clicks":5}]},
				"pagination":{"next_url":"https://ads-api.reddit.com/api/v3/reports?page=2"}
			}`)}, nil
		}
		if input["next_url"] != "https://ads-api.reddit.com/api/v3/reports?page=2" || input["data"] != nil {
			t.Fatalf("Reddit continuation input=%#v", input)
		}
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{
			"data":{"metrics":[{"date":"2026-08-02","campaign_id":"c1","spend":2000000,"impressions":200,"clicks":8}]},
			"pagination":{}
		}`)}, nil
	}
	ctx := newAdsCtx(t, pf)
	accountID := addPerformanceAccount(t, ctx, "test-proj", "reddit", "a2_client", "EUR", "UTC")
	out, err := (&App{}).toolPerformanceGet(ctx, map[string]any{
		"ad_account_id": accountID, "level": "campaign", "date_from": "2026-08-01", "date_to": "2026-08-02",
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := out.(map[string]any)["data"].([]map[string]any)
	if calls != 2 || len(rows) != 2 || rows[1]["spend_micros"] != int64(2000000) {
		t.Fatalf("calls=%d rows=%#v", calls, rows)
	}
}

func TestPerformanceGetRejectsUnscopedAndUnsafeQueries(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	localID := addPerformanceAccount(t, ctx, "test-proj", "google", "123", "EUR", "UTC")
	foreignID := addPerformanceAccount(t, ctx, "another-project", "google", "456", "EUR", "UTC")
	app := &App{}
	cases := []map[string]any{
		{"ad_account_id": foreignID, "date_from": "2026-07-01", "date_to": "2026-07-02"},
		{"ad_account_id": localID, "level": "creative", "date_from": "2026-07-01", "date_to": "2026-07-02"},
		{"ad_account_id": localID, "date_from": "2026-01-01", "date_to": "2026-07-02"},
		{"ad_account_id": localID, "date_from": "2026-07-01", "date_to": "2026-07-02", "entity_ids": []any{"1) OR 1=1"}},
	}
	for _, args := range cases {
		out, err := app.toolPerformanceGet(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		if out.(map[string]any)["isError"] != true {
			t.Fatalf("unsafe request accepted: args=%#v out=%#v", args, out)
		}
	}
	if len(pf.executeCalls) != 0 {
		t.Fatalf("invalid requests reached provider: %#v", pf.executeCalls)
	}
}

func TestPerformanceToolAndCollectorAreDeclared(t *testing.T) {
	found := false
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "performance_get" {
			found = true
		}
	}
	if !found {
		t.Fatal("performance_get is not declared")
	}
	workers := (&App{}).Workers()
	if len(workers) != 2 || workers[0].Name != "performance_collector" || workers[0].Schedule != "@every 1m" || workers[1].Name != "audience_sync_processor" || workers[1].Schedule != "@every 30s" {
		t.Fatalf("workers=%#v", workers)
	}
	if performanceCollectorIntervals["campaign"] != 5*time.Minute || performanceCollectorIntervals["ad_group"] != 15*time.Minute || performanceCollectorIntervals["ad"] != 15*time.Minute {
		t.Fatalf("collector intervals=%#v", performanceCollectorIntervals)
	}
}

func TestPerformanceGetEmitsPostCommitInvalidation(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponder = func(_ int64, _ string, _ map[string]any) (*sdk.ExecuteResult, error) {
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{
			"data":[{"campaign_id":"123","campaign_name":"Live","date_start":"2026-08-07","spend":"1.25","impressions":"20","clicks":"2"}]
		}`)}, nil
	}
	ctx := newAdsCtx(t, pf)
	recorder := tk.NewEmitRecorder()
	ctx.SetEmitter(recorder)
	accountID := addPerformanceAccount(t, ctx, "test-proj", "meta", "act_123", "EUR", "Europe/Madrid")

	out, err := (&App{}).toolPerformanceGet(ctx, map[string]any{
		"ad_account_id": accountID, "level": "campaign",
		"date_from": "2026-08-07", "date_to": "2026-08-07",
	})
	if err != nil || out.(map[string]any)["source"] != "live" {
		t.Fatalf("out=%#v err=%v", out, err)
	}
	events := recorder.EventsByTopic("performance.updated")
	if len(events) != 1 || events[0].ProjectID != "test-proj" {
		t.Fatalf("events=%#v", events)
	}
	payload := events[0].Data.(map[string]any)
	if payload["ad_account_id"] != accountID || payload["source"] != "manual" {
		t.Fatalf("payload=%#v", payload)
	}
	levels := payload["levels"].([]string)
	if len(levels) != 1 || levels[0] != "campaign" {
		t.Fatalf("levels=%#v", levels)
	}
	var stored int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM ad_metric_points WHERE ad_account_id=? AND level='campaign'`, accountID,
	).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("stored=%d err=%v", stored, err)
	}
}

func TestPerformanceGetSingleFlightsIdenticalRefreshes(t *testing.T) {
	pf := newRecordingPlatform()
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	pf.executeResponder = func(_ int64, _ string, _ map[string]any) (*sdk.ExecuteResult, error) {
		calls.Add(1)
		startOnce.Do(func() { close(started) })
		<-release
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"results":[]}`)}, nil
	}
	ctx := newAdsCtx(t, pf)
	accountID := addPerformanceAccount(t, ctx, "test-proj", "google", "123", "EUR", "UTC")
	app := &App{}
	args := map[string]any{
		"ad_account_id": accountID, "level": "campaign",
		"date_from": "2026-08-07", "date_to": "2026-08-07",
	}
	results := make(chan any, 2)
	errors := make(chan error, 2)
	go func() { out, err := app.toolPerformanceGet(ctx, args); results <- out; errors <- err }()
	<-started
	go func() { out, err := app.toolPerformanceGet(ctx, args); results <- out; errors <- err }()
	time.Sleep(25 * time.Millisecond)
	close(release)
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
		if out := <-results; out.(map[string]any)["isError"] == true {
			t.Fatalf("out=%#v", out)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls=%d", calls.Load())
	}
}

func TestPerformanceFailurePersistsBackoffAndEmitsRetryState(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponder = func(_ int64, _ string, _ map[string]any) (*sdk.ExecuteResult, error) {
		return &sdk.ExecuteResult{Success: false, Status: 500, Data: json.RawMessage(`{
			"error":{"code":2,"is_transient":true,"message":"Try again","type":"OAuthException"}
		}`)}, nil
	}
	ctx := newAdsCtx(t, pf)
	recorder := tk.NewEmitRecorder()
	ctx.SetEmitter(recorder)
	accountID := addPerformanceAccount(t, ctx, "test-proj", "meta", "act_123", "EUR", "UTC")
	out, err := (&App{}).toolPerformanceGet(ctx, map[string]any{
		"ad_account_id": accountID, "level": "campaign",
		"date_from": "2026-08-07", "date_to": "2026-08-07",
	})
	if err != nil || out.(map[string]any)["isError"] != true {
		t.Fatalf("out=%#v err=%v", out, err)
	}
	var failureCount int
	var nextAttempt, lastSuccess string
	if err := ctx.AppDB().QueryRow(
		`SELECT failure_count, next_attempt_at, last_success_at FROM ad_sync_state WHERE project_id='test-proj' AND ad_account_id=? AND level='campaign'`,
		accountID,
	).Scan(&failureCount, &nextAttempt, &lastSuccess); err != nil {
		t.Fatal(err)
	}
	if failureCount != 1 || nextAttempt == "" || lastSuccess != "" {
		t.Fatalf("failure_count=%d next=%q success=%q", failureCount, nextAttempt, lastSuccess)
	}
	if due, _ := analyticsSyncDue(ctx, "test-proj", accountID, "campaign", 5*time.Minute, time.Now().UTC()); due {
		t.Fatal("collector ignored persisted provider backoff")
	}
	events := recorder.EventsByTopic("performance.sync_failed")
	if len(events) != 1 || events[0].Data.(map[string]any)["failure_count"] != 1 {
		t.Fatalf("events=%#v", events)
	}
}

func TestPerformanceCollectorRefreshesEveryGenericHierarchyLevel(t *testing.T) {
	pf := newRecordingPlatform()
	var calls atomic.Int32
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if tool != "search" {
			t.Fatalf("tool=%s input=%#v", tool, input)
		}
		calls.Add(1)
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"results":[]}`)}, nil
	}
	ctx := newAdsCtx(t, pf)
	recorder := tk.NewEmitRecorder()
	ctx.SetEmitter(recorder)
	accountID := addPerformanceAccount(t, ctx, "test-proj", "google", "123", "EUR", "UTC")

	if err := (&App{}).runPerformanceCollector(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("provider calls=%d", calls.Load())
	}
	events := recorder.EventsByTopic("performance.updated")
	if len(events) != 3 {
		t.Fatalf("events=%#v", events)
	}
	var states int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM ad_sync_state WHERE ad_account_id=? AND last_status='ok' AND last_reconciled_at<>''`, accountID,
	).Scan(&states); err != nil || states != 3 {
		t.Fatalf("states=%d err=%v", states, err)
	}
	if err := (&App{}).runPerformanceCollector(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("collector ignored due intervals; provider calls=%d", calls.Load())
	}
}
