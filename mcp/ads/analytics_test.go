package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
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
	if len(workers) != 1 || workers[0].Name != "performance_collector" || workers[0].Schedule != "@every 30m" {
		t.Fatalf("workers=%#v", workers)
	}
}
