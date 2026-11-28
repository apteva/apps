package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func tiktokOK(data string) *sdk.ExecuteResult {
	return executeJSON(`{"code":0,"message":"OK","request_id":"r1","data":` + data + `}`)
}

func TestTikTokCampaignAdGroupAndAdAreWiredFromResources(t *testing.T) {
	platform := newRecordingPlatform()
	calls := []string{}
	var adCreative map[string]any
	var groupInput map[string]any
	platform.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		calls = append(calls, tool)
		if input["advertiser_id"] != "7000000000000000001" {
			t.Fatalf("%s lost advertiser_id: %#v", tool, input)
		}
		switch tool {
		case "identity_list":
			if input["identity_type"] == "CUSTOMIZED_USER" {
				return tiktokOK(`{"identity_list":[{"identity_id":"id_1","identity_type":"CUSTOMIZED_USER","display_name":"Brand"}],"page_info":{"page":1,"total_page":1}}`), nil
			}
			return tiktokOK(`{"identity_list":[],"page_info":{"page":1,"total_page":1}}`), nil
		case "custom_audience_list":
			return tiktokOK(`{"list":[{"audience_id":"aud_1","name":"Buyers","is_valid":true}],"page_info":{"page":1,"total_page":1}}`), nil
		case "campaign_create":
			if input["objective_type"] != "TRAFFIC" || input["budget_mode"] != "BUDGET_MODE_DAY" || input["budget"] != 25.0 || input["operation_status"] != "DISABLE" {
				t.Fatalf("campaign payload=%#v", input)
			}
			return tiktokOK(`{"campaign_id":"1800000000000000001"}`), nil
		case "adgroup_create":
			groupInput = input
			return tiktokOK(`{"adgroup_id":"1800000000000000002"}`), nil
		case "ad_create":
			creatives := input["creatives"].([]any)
			adCreative = asMap(creatives[0])
			return tiktokOK(`{"ad_ids":["1800000000000000003"]}`), nil
		case "ad_update_status":
			if input["operation_status"] != "DISABLE" {
				t.Fatalf("new ad should be paused, got %#v", input)
			}
			return tiktokOK(`{}`), nil
		default:
			t.Fatalf("unexpected TikTok tool %s %#v", tool, input)
			return nil, nil
		}
	}
	ctx := newAdsCtx(t, platform)
	accountID := seedResourceTestAccount(t, ctx, "tiktok", "7000000000000000001")
	app := &App{}
	if _, err := app.toolAccountContextGet(ctx, map[string]any{"ad_account_id": accountID}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolCampaignCreate(ctx, map[string]any{
		"ad_account_id": accountID, "name": "Traffic", "objective": "traffic", "daily_budget_cents": 2500, "status": "PAUSED",
	})
	if err != nil || asMap(out)["isError"] == true || asMap(out)["id"] != "1800000000000000001" {
		t.Fatalf("campaign create: out=%#v err=%v", out, err)
	}
	out, err = app.toolAdSetCreate(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": "1800000000000000001", "name": "US visitors",
		"optimization_goal": "link_clicks", "daily_budget_cents": 2000,
		"start_time": "2026-12-01T09:00:00+01:00",
		"targeting":  map[string]any{"location_ids": []any{"6252001"}},
	})
	if err != nil || asMap(out)["isError"] == true {
		t.Fatalf("ad group create: out=%#v err=%v", out, err)
	}
	if groupInput["optimization_goal"] != "CLICK" || groupInput["billing_event"] != "CPC" || groupInput["schedule_start_time"] != "2026-12-01 08:00:00" ||
		groupInput["schedule_type"] != "SCHEDULE_FROM_NOW" || groupInput["budget"] != 20.0 {
		t.Fatalf("ad group payload=%#v", groupInput)
	}
	out, err = app.toolAdCreate(ctx, map[string]any{
		"ad_account_id": accountID, "adset_id": "1800000000000000002", "name": "Spot",
		"format": "video", "video_id": "v10", "thumbnail_hash": "img_cover",
		"primary_text": "Try it today", "destination_url": "https://example.com", "call_to_action": "learn_more",
	})
	if err != nil || asMap(out)["isError"] == true || asMap(out)["id"] != "1800000000000000003" {
		t.Fatalf("ad create: out=%#v err=%v", out, err)
	}
	if adCreative["identity_id"] != "id_1" || adCreative["identity_type"] != "CUSTOMIZED_USER" || adCreative["ad_format"] != "SINGLE_VIDEO" ||
		adCreative["call_to_action"] != "LEARN_MORE" || adCreative["landing_page_url"] != "https://example.com" {
		t.Fatalf("ad creative=%#v", adCreative)
	}
	if !strings.Contains(strings.Join(calls, ","), "ad_create,ad_update_status") {
		t.Fatalf("ad was not paused after create: %v", calls)
	}
}

func TestTikTokAdGroupCreateRequiresLocationsAndBudget(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		t.Fatalf("no provider call expected, got %s %#v", tool, input)
		return nil, nil
	}
	ctx := newAdsCtx(t, platform)
	accountID := seedResourceTestAccount(t, ctx, "tiktok", "7000000000000000001")
	acct, def, _ := (&App{}).resolveAdAccount(ctx, map[string]any{"ad_account_id": accountID})
	out, _ := tiktokAdapter{}.AdSetCreate(&App{}, ctx, acct, def, map[string]any{"name": "g", "campaign_id": "c", "daily_budget_cents": 100})
	if !strings.Contains(toolText(out), "location_ids") {
		t.Fatalf("missing locations should be refused: %#v", out)
	}
	out, _ = tiktokAdapter{}.AdSetCreate(&App{}, ctx, acct, def, map[string]any{"name": "g", "campaign_id": "c", "targeting": map[string]any{"location_ids": []any{"1"}}})
	if !strings.Contains(toolText(out), "budget") {
		t.Fatalf("missing budget should be refused: %#v", out)
	}
}

func TestTikTokCampaignListNormalizesStatusBudgetAndPaging(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if tool != "campaign_list" || input["page"] != 2 || input["page_size"] != 50 {
			t.Fatalf("unexpected call %s %#v", tool, input)
		}
		return tiktokOK(`{"list":[
			{"campaign_id":"c1","campaign_name":"Live","operation_status":"ENABLE","secondary_status":"CAMPAIGN_STATUS_ENABLE","budget_mode":"BUDGET_MODE_DAY","budget":12.5,"objective_type":"TRAFFIC"},
			{"campaign_id":"c2","campaign_name":"Gone","operation_status":"DISABLE","secondary_status":"CAMPAIGN_STATUS_DELETE","budget_mode":"BUDGET_MODE_INFINITE"}
		],"page_info":{"page":2,"page_size":50,"total_page":3}}`), nil
	}
	ctx := newAdsCtx(t, platform)
	accountID := seedResourceTestAccount(t, ctx, "tiktok", "7000000000000000001")
	acct, def, _ := (&App{}).resolveAdAccount(ctx, map[string]any{"ad_account_id": accountID})
	out, err := tiktokAdapter{}.CampaignList(&App{}, ctx, acct, def, map[string]any{"limit": 50, "after": "2"})
	if err != nil {
		t.Fatal(err)
	}
	page := asMap(out)
	rows := page["data"].([]map[string]any)
	if len(rows) != 2 || rows[0]["status"] != "ACTIVE" || rows[0]["daily_budget"] != "1250" || rows[1]["status"] != "DELETED" || page["next_cursor"] != "3" {
		bytes, _ := json.Marshal(page)
		t.Fatalf("page=%s", bytes)
	}
}

func TestTikTokDeliveryActivationEnablesEachLevel(t *testing.T) {
	platform := newRecordingPlatform()
	status := map[string]string{"ad": "DISABLE", "adset": "DISABLE", "campaign": "DISABLE"}
	enabled := []string{}
	platform.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "ad_list":
			return tiktokOK(`{"list":[{"ad_id":"a1","adgroup_id":"g1","campaign_id":"c1","operation_status":"` + status["ad"] + `"}],"page_info":{"page":1,"total_page":1}}`), nil
		case "adgroup_list":
			return tiktokOK(`{"list":[{"adgroup_id":"g1","campaign_id":"c1","operation_status":"` + status["adset"] + `"}],"page_info":{"page":1,"total_page":1}}`), nil
		case "campaign_list":
			return tiktokOK(`{"list":[{"campaign_id":"c1","operation_status":"` + status["campaign"] + `"}],"page_info":{"page":1,"total_page":1}}`), nil
		case "ad_update_status", "adgroup_update_status", "campaign_update_status":
			if input["operation_status"] != "ENABLE" {
				t.Fatalf("%s %#v", tool, input)
			}
			entity := map[string]string{"ad_update_status": "ad", "adgroup_update_status": "adset", "campaign_update_status": "campaign"}[tool]
			status[entity] = "ENABLE"
			enabled = append(enabled, entity)
			return tiktokOK(`{}`), nil
		default:
			t.Fatalf("unexpected TikTok activation tool %s %#v", tool, input)
			return nil, nil
		}
	}
	ctx := newAdsCtx(t, platform)
	accountID := seedResourceTestAccount(t, ctx, "tiktok", "7000000000000000001")
	out, err := (&App{}).toolDeliveryActivate(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": "c1", "adset_id": "g1", "ad_id": "a1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if asMap(out)["status"] != "completed" || strings.Join(enabled, ",") != "ad,adset,campaign" {
		bytes, _ := json.Marshal(out)
		t.Fatalf("activation=%s enabled=%v", bytes, enabled)
	}
}

func TestTikTokAnalyticsNormalizesIntegratedReport(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if tool != "report_integrated" || input["data_level"] != "AUCTION_CAMPAIGN" || input["start_date"] != "2026-11-01" || input["end_date"] != "2026-11-02" {
			t.Fatalf("unexpected report call %s %#v", tool, input)
		}
		return tiktokOK(`{"list":[
			{"dimensions":{"campaign_id":"c1","stat_time_day":"2026-11-01 00:00:00"},"metrics":{"spend":"12.34","impressions":"1000","clicks":"40","reach":"800","conversion":"3","video_play_actions":"500","campaign_name":"Live"}},
			{"dimensions":{"campaign_id":"c9","stat_time_day":"2026-11-01 00:00:00"},"metrics":{"spend":"1","impressions":"1"}}
		],"page_info":{"page":1,"total_page":1}}`), nil
	}
	ctx := newAdsCtx(t, platform)
	accountID := seedResourceTestAccount(t, ctx, "tiktok", "7000000000000000001")
	acct, _, _ := (&App{}).resolveAdAccount(ctx, map[string]any{"ad_account_id": accountID})
	points, errOut := (&App{}).fetchTikTokAnalytics(ctx, acct, &genericPerformanceRequest{
		Level: "campaign", DateFrom: "2026-11-01", DateTo: "2026-11-02", EntityIDs: []string{"c1"},
	})
	if errOut != nil {
		t.Fatalf("report error %#v", errOut)
	}
	if len(points) != 1 {
		t.Fatalf("expected the c1 row only, got %#v", points)
	}
	p := points[0]
	if p.CampaignID != "c1" || p.EntityName != "Live" || p.Date != "2026-11-01" || p.SpendMicros != 12_340_000 ||
		p.Impressions != 1000 || p.Clicks != 40 || p.Reach != 800 || p.Conversions != 3 || p.VideoViews != 500 {
		t.Fatalf("point=%#v", p)
	}
}

func TestTikTokRateLimitBodyIsRecognized(t *testing.T) {
	if !providerRateLimited(&sdk.ExecuteResult{Status: 200}, `{"code":40100,"message":"Too many requests"}`) {
		t.Fatal("TikTok code 40100 should count as rate limited")
	}
	if providerRateLimited(&sdk.ExecuteResult{Status: 200}, `{"code":40001,"message":"bad param 40100"}`) {
		t.Fatal("only the envelope code should count")
	}
}

func TestTikTokAudienceLookalikeAndDelete(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "custom_audience_list":
			return tiktokOK(`{"list":[{"audience_id":"aud_1","name":"Buyers","is_valid":true}],"page_info":{"page":1,"total_page":1}}`), nil
		case "identity_list":
			return tiktokOK(`{"identity_list":[],"page_info":{"page":1,"total_page":1}}`), nil
		case "lookalike_create":
			spec := asMap(input["lookalike_spec"])
			if spec["source_audience_id"] != "aud_1" || spec["audience_size"] != "NARROW" {
				t.Fatalf("lookalike payload=%#v", input)
			}
			return tiktokOK(`{"custom_audience_id":"aud_2"}`), nil
		case "custom_audience_delete":
			ids := anySlice(input["custom_audience_ids"])
			if len(ids) != 1 || ids[0] != "aud_2" {
				t.Fatalf("delete payload=%#v", input)
			}
			return tiktokOK(`{}`), nil
		case "adgroup_list":
			return tiktokOK(`{"list":[],"page_info":{"page":1,"total_page":1}}`), nil
		default:
			t.Fatalf("unexpected TikTok audience tool %s %#v", tool, input)
			return nil, nil
		}
	}
	ctx := newAdsCtx(t, platform)
	accountID := seedResourceTestAccount(t, ctx, "tiktok", "7000000000000000001")
	app := &App{}
	if _, err := app.toolResourceRefresh(ctx, map[string]any{"ad_account_id": accountID, "kinds": []any{"audience"}}); err != nil {
		t.Fatal(err)
	}
	source := findTestResource(t, ctx, accountID, resourceAudience, "aud_1")
	out, err := app.toolAudienceCreate(ctx, map[string]any{
		"ad_account_id": accountID, "name": "Buyers LAL", "kind": "lookalike", "source_audience_id": source,
		"ratio": 0.01, "platform_options": map[string]any{"location_ids": []any{"6252001"}},
	})
	if err != nil || asMap(out)["isError"] == true {
		t.Fatalf("lookalike create: out=%#v err=%v", out, err)
	}
	created := findTestResource(t, ctx, accountID, resourceAudience, "aud_2")
	if out, err := app.toolAudienceDelete(ctx, map[string]any{"ad_account_id": accountID, "audience_id": created}); err != nil || asMap(out)["isError"] == true {
		t.Fatalf("delete: out=%#v err=%v", out, err)
	}
	out, _ = app.toolAudienceCreate(ctx, map[string]any{"ad_account_id": accountID, "name": "List", "kind": "customer_list"})
	if !strings.Contains(toolText(out), "first member file") {
		t.Fatalf("customer list create should be refused: %#v", out)
	}
}

func findTestResource(t *testing.T, ctx *sdk.AppCtx, accountID int64, kind, nativeID string) int64 {
	t.Helper()
	var id int64
	if err := ctx.AppDB().QueryRow(`SELECT id FROM ad_resources WHERE ad_account_id=? AND kind=? AND native_asset_id=?`, accountID, kind, nativeID).Scan(&id); err != nil {
		t.Fatalf("resource %s %s: %v", kind, nativeID, err)
	}
	return id
}

func toolText(out any) string {
	bytes, _ := json.Marshal(out)
	return string(bytes)
}
