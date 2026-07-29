package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func addPerformanceAccount(t *testing.T, ctx *sdk.AppCtx, projectID, platform, nativeID, currency, timezone string) int64 {
	t.Helper()
	result, err := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts
		   (project_id, platform, connection_id, native_account_id, display_name, currency, timezone_name, status)
		 VALUES (?, ?, 8, ?, 'Performance account', ?, ?, 'active')`,
		projectID, platform, nativeID, currency, timezone,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func performanceArgs(accountID int64) map[string]any {
	return map[string]any{
		"ad_account_id": accountID,
		"date_from":     "2026-07-01",
		"date_to":       "2026-07-31",
		"granularity":   "day",
		"campaign_ids":  []any{"123456789"},
	}
}

func TestCampaignPerformanceGoogle_PaginatesAndNormalizes(t *testing.T) {
	pf := newRecordingPlatform()
	call := 0
	pf.executeResponder = func(connID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		call++
		if connID != 8 || tool != "search" {
			t.Fatalf("unexpected integration call: conn=%d tool=%s", connID, tool)
		}
		query := input["query"].(string)
		for _, required := range []string{
			"segments.date",
			"campaign.id",
			"campaign.name",
			"metrics.cost_micros",
			"metrics.impressions",
			"metrics.clicks",
			"metrics.conversions",
			"segments.date BETWEEN '2026-07-01' AND '2026-07-31'",
			"campaign.id IN (123456789,987654321)",
		} {
			if !strings.Contains(query, required) {
				t.Fatalf("GAQL query missing %q: %s", required, query)
			}
		}
		if input["customer_id"] != "9876543210" {
			t.Fatalf("customer_id=%#v", input["customer_id"])
		}
		switch call {
		case 1:
			if input["page_token"] != nil {
				t.Fatalf("first page unexpectedly has page token: %#v", input)
			}
			return &sdk.ExecuteResult{
				Success: true,
				Status:  200,
				Data: json.RawMessage(`{
					"results":[{
						"segments":{"date":"2026-07-15"},
						"campaign":{"id":"123456789","name":"Energy comparison"},
						"customer":{"currencyCode":"EUR"},
						"metrics":{"costMicros":"124500001","impressions":"18420","clicks":"391","conversions":"28.5"}
					}],
					"nextPageToken":"page-2"
				}`),
			}, nil
		case 2:
			if input["page_token"] != "page-2" {
				t.Fatalf("second page token=%#v", input["page_token"])
			}
			return &sdk.ExecuteResult{
				Success: true,
				Status:  200,
				Data: json.RawMessage(`{
					"results":[{
						"segments":{"date":"2026-07-16"},
						"campaign":{"id":"987654321","name":"Solar comparison"},
						"metrics":{}
					}]
				}`),
			}, nil
		default:
			t.Fatalf("unexpected page %d", call)
			return nil, nil
		}
	}
	ctx := newAdsCtx(t, pf)
	accountID := addPerformanceAccount(t, ctx, "test-proj", "google", "9876543210", "EUR", "Europe/Madrid")

	args := performanceArgs(accountID)
	args["campaign_ids"] = []any{"123456789", "987654321"}
	out, err := (&App{}).toolCampaignPerformanceGet(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	data := result["data"].([]map[string]any)
	if len(data) != 2 || call != 2 {
		t.Fatalf("pagination lost data: calls=%d data=%#v", call, data)
	}
	first := data[0]
	if first["campaign_id"] != "123456789" || first["campaign_name"] != "Energy comparison" {
		t.Fatalf("campaign identity not retained: %#v", first)
	}
	if first["currency"] != "EUR" || first["timezone"] != "Europe/Madrid" {
		t.Fatalf("account locale not retained: %#v", first)
	}
	if first["spend_micros"] != int64(124500001) || first["impressions"] != int64(18420) || first["clicks"] != int64(391) {
		t.Fatalf("metrics not normalized: %#v", first)
	}
	if first["conversions"] != 28.5 {
		t.Fatalf("conversions=%#v", first["conversions"])
	}
	second := data[1]
	if second["spend_micros"] != int64(0) || second["impressions"] != int64(0) ||
		second["clicks"] != int64(0) || second["conversions"] != float64(0) {
		t.Fatalf("missing metrics must normalize to zero: %#v", second)
	}
	if second["currency"] != "EUR" {
		t.Fatalf("account currency fallback missing: %#v", second)
	}
	if second["campaign_id"] != "987654321" || second["campaign_name"] != "Solar comparison" {
		t.Fatalf("second-page campaign missing: %#v", second)
	}
	if result["next_cursor"] != nil {
		t.Fatalf("next_cursor=%#v", result["next_cursor"])
	}
}

func TestCampaignPerformanceMeta_UsesDailyInsightsAndExactSpend(t *testing.T) {
	pf := newRecordingPlatform()
	call := 0
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		call++
		if tool != "insights_get" {
			t.Fatalf("tool=%s", tool)
		}
		if input["objectId"] != "act_1401961790918065" || input["level"] != "campaign" {
			t.Fatalf("unscoped insights request: %#v", input)
		}
		if input["time_increment"] != "1" {
			t.Fatalf("time_increment=%#v", input["time_increment"])
		}
		for _, field := range []string{"spend", "impressions", "clicks", "actions"} {
			if !strings.Contains(input["fields"].(string), field) {
				t.Fatalf("Meta fields missing %q: %s", field, input["fields"])
			}
		}
		var timeRange map[string]string
		if err := json.Unmarshal([]byte(input["time_range"].(string)), &timeRange); err != nil {
			t.Fatal(err)
		}
		if timeRange["since"] != "2026-07-01" || timeRange["until"] != "2026-07-31" {
			t.Fatalf("time_range=%#v", timeRange)
		}
		var filtering []map[string]any
		if err := json.Unmarshal([]byte(input["filtering"].(string)), &filtering); err != nil {
			t.Fatal(err)
		}
		if len(filtering) != 1 || filtering[0]["field"] != "campaign.id" {
			t.Fatalf("filtering=%#v", filtering)
		}
		switch call {
		case 1:
			return &sdk.ExecuteResult{
				Success: true,
				Status:  200,
				Data: json.RawMessage(`{
					"data":[{
						"campaign_id":"123456789",
						"campaign_name":"Energy comparison",
						"date_start":"2026-07-15",
						"date_stop":"2026-07-15",
						"spend":"124.500001",
						"impressions":"18420",
						"clicks":"391",
						"actions":[{"action_type":"offsite_conversion","value":"28"}]
					}],
					"paging":{"cursors":{"after":"cursor-2"}}
				}`),
			}, nil
		case 2:
			if input["after"] != "cursor-2" {
				t.Fatalf("after=%#v", input["after"])
			}
			return &sdk.ExecuteResult{
				Success: true,
				Status:  200,
				Data: json.RawMessage(`{
					"data":[{
						"campaign_id":"987654321",
						"campaign_name":"Solar comparison",
						"date_start":"2026-07-16"
					}]
				}`),
			}, nil
		default:
			t.Fatalf("unexpected page %d", call)
			return nil, nil
		}
	}
	ctx := newAdsCtx(t, pf)
	accountID := addPerformanceAccount(t, ctx, "test-proj", "meta", "act_1401961790918065", "EUR", "Europe/Madrid")

	args := performanceArgs(accountID)
	args["campaign_ids"] = []any{"123456789", "987654321"}
	out, err := (&App{}).toolCampaignPerformanceGet(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	data := out.(map[string]any)["data"].([]map[string]any)
	if len(data) != 2 || call != 2 {
		t.Fatalf("pagination lost data: calls=%d data=%#v", call, data)
	}
	first := data[0]
	if first["spend_micros"] != int64(124500001) || first["conversions"] != float64(28) {
		t.Fatalf("Meta metrics not normalized precisely: %#v", first)
	}
	if first["currency"] != "EUR" || first["campaign_id"] != "123456789" {
		t.Fatalf("identity or currency missing: %#v", first)
	}
	second := data[1]
	if second["spend_micros"] != int64(0) || second["conversions"] != float64(0) {
		t.Fatalf("zero-spend day not handled: %#v", second)
	}
	if second["campaign_id"] != "987654321" || second["campaign_name"] != "Solar comparison" {
		t.Fatalf("second-page campaign missing: %#v", second)
	}

	rounded, err := decimalToMicros("12.3456789")
	if err != nil || rounded != 12345679 {
		t.Fatalf("decimal rounding: micros=%d err=%v", rounded, err)
	}
}

func TestCampaignPerformance_RejectsInvalidScopeAndRanges(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	foreignID := addPerformanceAccount(t, ctx, "other-project", "google", "9876543210", "EUR", "Europe/Madrid")
	localID := addPerformanceAccount(t, ctx, "test-proj", "google", "1234567890", "EUR", "Europe/Madrid")
	app := &App{}

	cases := []map[string]any{
		performanceArgs(foreignID),
		{
			"ad_account_id": localID,
			"date_from":     "2026-07-32",
			"date_to":       "2026-07-31",
		},
		{
			"ad_account_id": localID,
			"date_from":     "2026-01-01",
			"date_to":       "2026-04-01",
		},
		{
			"ad_account_id": localID,
			"date_from":     "2026-07-01",
			"date_to":       "2026-07-31",
			"campaign_ids":  []any{"123) OR campaign.id > 0"},
		},
	}
	for _, args := range cases {
		out, err := app.toolCampaignPerformanceGet(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		if out.(map[string]any)["isError"] != true {
			t.Fatalf("invalid request accepted: args=%#v out=%#v", args, out)
		}
	}
	if len(pf.executeCalls) != 0 {
		t.Fatalf("invalid requests reached provider: %#v", pf.executeCalls)
	}
}

func TestCampaignPerformance_ClassifiesProviderRateLimit(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponder = func(_ int64, _ string, _ map[string]any) (*sdk.ExecuteResult, error) {
		return &sdk.ExecuteResult{
			Success: false,
			Status:  429,
			Data:    json.RawMessage(`{"error":{"message":"Too many calls"}}`),
		}, nil
	}
	ctx := newAdsCtx(t, pf)
	accountID := addPerformanceAccount(t, ctx, "test-proj", "google", "1234567890", "EUR", "Europe/Madrid")

	out, err := (&App{}).toolCampaignPerformanceGet(ctx, performanceArgs(accountID))
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["isError"] != true || result["code"] != "provider_rate_limited" ||
		result["retryable"] != true || result["provider_status"] != 429 {
		t.Fatalf("rate limit not classified: %#v", result)
	}
}

func TestCampaignPerformanceToolIsDeclared(t *testing.T) {
	tools := (&App{}).MCPTools()
	for _, tool := range tools {
		if tool.Name == "campaign_performance_get" {
			return
		}
	}
	t.Fatal("campaign_performance_get is not declared")
}
