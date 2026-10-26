package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in test: high-level app handlers -> the user's existing integration -> Google.
// Delivery fixtures are PAUSED and campaigns/budgets are removed on completion.
// Reusable assets/audiences are retained where Google API v23 provides no removal.
// No activation is attempted.
func TestDemandGenLive(t *testing.T) {
	if os.Getenv("ADS_DEMAND_GEN_LIVE") != "1" {
		t.Skip("set ADS_DEMAND_GEN_LIVE=1 to run paused real-provider validation")
	}
	cf := os.Getenv("ADS_LIVE_CONFIG")
	if cf == "" {
		home, _ := os.UserHomeDir()
		cf = filepath.Join(home, ".apteva-prod", "apteva.json")
	}
	raw, err := os.ReadFile(cf)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	key := firstString(config, "api_key", "apiKey")
	if key == "" {
		t.Fatal("missing API key")
	}
	base := os.Getenv("ADS_LIVE_SERVER")
	if base == "" {
		base = "https://agents.schwartzindustries.com"
	}
	connection := os.Getenv("ADS_LIVE_CONNECTION")
	if connection == "" {
		connection = "103"
	}
	customer := os.Getenv("ADS_LIVE_CUSTOMER")
	if customer == "" {
		customer = "1875399872"
	}
	client := &http.Client{Timeout: 60 * time.Second}
	pf := newRecordingPlatform()
	evidence := []any{}
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		payload, _ := json.Marshal(map[string]any{"tool": tool, "input": input})
		req, err := http.NewRequest("POST", base+"/api/connections/"+connection+"/execute", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		var envelope struct {
			Success bool              `json:"success"`
			Status  int               `json:"status"`
			Data    json.RawMessage   `json:"data"`
			Headers map[string]string `json:"headers"`
		}
		if err = json.Unmarshal(body, &envelope); err != nil {
			return nil, fmt.Errorf("integration HTTP %d: %s", resp.StatusCode, body)
		}
		evidence = append(evidence, map[string]any{"tool": tool, "input": input, "success": envelope.Success, "status": envelope.Status, "data": json.RawMessage(envelope.Data), "request_id": envelope.Headers["Request-Id"]})
		return &sdk.ExecuteResult{Success: envelope.Success, Status: envelope.Status, Data: envelope.Data}, nil
	}
	t.Cleanup(func() {
		path := os.Getenv("ADS_LIVE_EVIDENCE")
		if path == "" {
			path = "/private/tmp/ads-demand-gen-live/evidence.json"
		}
		data, _ := json.MarshalIndent(evidence, "", "  ")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Error(err)
		}
		t.Log("Real-provider evidence:", path)
	})
	ctx := newAdsCtx(t, pf)
	a := &App{}
	account := seedResourceTestAccount(t, ctx, "google", customer)
	// Assets are supplied explicitly to make this test repeatable for any linked account.
	video := os.Getenv("ADS_LIVE_VIDEO_ASSET")
	logo := os.Getenv("ADS_LIVE_LOGO_ASSET")
	if video == "" || logo == "" {
		t.Fatal("set ADS_LIVE_VIDEO_ASSET and ADS_LIVE_LOGO_ASSET to existing account assets")
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	var createdAudience string
	t.Cleanup(func() {
		if createdAudience != "" {
			t.Log("Reusable audience retained: Google API v23 has no Audience remove operation", createdAudience)
		}
	})

	result, err := a.toolCampaignCreate(ctx, map[string]any{"ad_account_id": account, "name": "Apteva Demand Gen validation " + stamp, "channel_type": "demand_gen", "objective": "engagement", "optimization_goal": "channel_subscriptions", "daily_budget_cents": 500, "locations": []any{"2724"}, "languages": []any{"1000"}, "status": "PAUSED", "idempotency_key": "live-campaign-" + stamp})
	requireOK(t, result, err)
	cid := firstString(asMap(result), "campaign_id", "id")
	if cid == "" {
		t.Fatal("missing campaign ID")
	}
	budgetID := firstString(asMap(result), "budget_id")
	t.Log("Created paused real Demand Gen campaign", cid)
	t.Cleanup(func() {
		result, err := a.toolCampaignDelete(ctx, map[string]any{"ad_account_id": account, "campaign_id": cid})
		if err != nil || mcpResultError(result) != nil {
			t.Errorf("cleanup campaign %s: %v %#v", cid, err, result)
		} else {
			t.Log("Removed test campaign", cid)
			budget, err := a.toolBudgetDelete(ctx, map[string]any{"ad_account_id": account, "budget_ids": []any{budgetID}})
			if err != nil || mcpResultError(budget) != nil {
				t.Log("Budget cleanup:", err, budget)
			}
		}
	})
	group, err := a.toolAdSetCreate(ctx, map[string]any{"ad_account_id": account, "campaign_id": cid, "name": "YouTube validation group", "placements": []any{"youtube_in_feed", "youtube_shorts"}, "status": "PAUSED", "idempotency_key": "live-group-" + stamp})
	requireOK(t, group, err)
	gid := firstString(asMap(group), "adset_id", "id")
	adArgs := map[string]any{"ad_account_id": account, "adset_id": gid, "name": "Paused structured video validation", "ad_format": "responsive_video", "business_name": "Marcos Schwartz", "headlines": []any{map[string]any{"text": "Explore the channel"}}, "long_headlines": []any{map[string]any{"text": "Discover interviews and insights on the channel"}}, "descriptions": []any{map[string]any{"text": "Watch more videos and explore the channel."}}, "video_asset_ids": []any{video}, "logo_asset_ids": []any{logo}, "final_urls": []any{os.Getenv("ADS_LIVE_FINAL_URL")}, "status": "PAUSED", "idempotency_key": "live-ad-" + stamp}
	ad, err := a.toolAdCreate(ctx, adArgs)
	requireOK(t, ad, err)
	t.Log("Created paused real video ad", createdProviderID(ad, "ad"))
	cfg, err := a.toolCampaignConfiguration(ctx, map[string]any{"ad_account_id": account, "campaign_id": cid})
	requireOK(t, cfg, err)
	if firstString(asMap(cfg), "status") != "PAUSED" {
		t.Fatalf("test campaign unexpectedly active %#v", cfg)
	}
	gc, err := a.toolAdSetConfiguration(ctx, map[string]any{"ad_account_id": account, "adset_id": gid})
	requireOK(t, gc, err)
	t.Log("Read back group configuration", gc)
	update, err := a.toolCampaignUpdate(ctx, map[string]any{"ad_account_id": account, "campaign_id": cid, "name": "Apteva verified Demand Gen " + stamp, "daily_budget_cents": 600})
	requireOK(t, update, err)
	gu, err := a.toolAdSetUpdate(ctx, map[string]any{"ad_account_id": account, "adset_id": gid, "placements": []any{"youtube_in_feed"}, "languages": []any{"1000"}})
	requireOK(t, gu, err)
	today := time.Now().UTC().Format("2006-01-02")
	report, err := a.toolConversionPerformance(ctx, map[string]any{"ad_account_id": account, "level": "campaign", "date_from": today, "date_to": today, "entity_ids": []any{cid}, "refresh": true})
	requireOK(t, report, err)
	ready, err := a.toolConversionReadiness(ctx, map[string]any{"ad_account_id": account, "campaign_id": cid})
	requireOK(t, ready, err)
	t.Log("Measurement readiness", ready)

	// A second campaign verifies non-YouTube Demand Gen formats and a total budget.
	start := time.Now().Format("2006-01-02")
	end := time.Now().AddDate(0, 0, 7).Format("2006-01-02")
	traffic, err := a.toolCampaignCreate(ctx, map[string]any{"ad_account_id": account, "name": "Apteva format validation " + stamp, "channel_type": "demand_gen", "objective": "traffic", "optimization_goal": "link_clicks", "lifetime_budget_cents": 10000, "start_time": start, "end_time": end, "status": "PAUSED", "idempotency_key": "live-formats-" + stamp})
	requireOK(t, traffic, err)
	trafficID := firstString(asMap(traffic), "campaign_id", "id")
	t.Cleanup(func() {
		r, e := a.toolCampaignDelete(ctx, map[string]any{"ad_account_id": account, "campaign_id": trafficID})
		if e != nil || mcpResultError(r) != nil {
			t.Errorf("cleanup format campaign %s: %v %#v", trafficID, e, r)
		} else {
			t.Log("Removed format campaign", trafficID)
			b, e := a.toolBudgetDelete(ctx, map[string]any{"ad_account_id": account, "budget_ids": []any{firstString(asMap(traffic), "budget_id")}})
			if e != nil || mcpResultError(b) != nil {
				t.Log("Budget cleanup:", e, b)
			}
		}
	})
	createdAudience = os.Getenv("ADS_LIVE_AUDIENCE_ID")
	if createdAudience == "" {
		grouped, err := a.toolAudienceCreateCustom(ctx, map[string]any{"ad_account_id": account, "name": "Apteva validation audience " + stamp, "audience_type": "grouped", "user_interest_ids": []any{"80001"}, "idempotency_key": "audience-" + stamp})
		requireOK(t, grouped, err)
		createdAudience = firstString(asMap(grouped), "id")
		t.Log("Created grouped Audience", createdAudience)
	}

	audiences, err := a.toolAudienceList(ctx, map[string]any{"ad_account_id": account, "audience_type": "grouped"})
	requireOK(t, audiences, err)
	ga := map[string]any{"ad_account_id": account, "campaign_id": trafficID, "name": "Format validation group", "placements": []any{"youtube_in_feed", "discover"}, "locations": []any{"2724"}, "status": "PAUSED"}
	ga["audience_ids"] = []any{createdAudience}
	if list := asSlice(asMap(audiences)["data"]); len(list) > 0 && createdAudience == "" {
		ga["audience_ids"] = []any{firstString(asMap(list[0]), "id")}
	}
	tg, err := a.toolAdSetCreate(ctx, ga)
	requireOK(t, tg, err)
	tgID := firstString(asMap(tg), "adset_id", "id")
	imageArgs := cloneMap(adArgs)
	imageArgs["adset_id"] = tgID
	imageArgs["ad_format"] = "responsive_image"
	imageArgs["name"] = "Paused image validation"
	imageArgs["image_asset_ids"] = []any{os.Getenv("ADS_LIVE_IMAGE_ASSET")}
	imageArgs["idempotency_key"] = "image-" + stamp
	delete(imageArgs, "video_asset_ids")
	delete(imageArgs, "long_headlines")
	imageAd, err := a.toolAdCreate(ctx, imageArgs)
	requireOK(t, imageAd, err)
	t.Log("Created paused image ad", createdProviderID(imageAd, "ad"))
	cards := []any{}
	for index, headline := range []string{"Explore the channel", "Discover more interviews"} {
		card, err := a.toolCreativeCreate(ctx, map[string]any{"ad_account_id": account, "name": fmt.Sprintf("Apteva card %s %d", stamp, index), "format": "carousel", "headline": headline, "destination_url": os.Getenv("ADS_LIVE_FINAL_URL"), "square_image_asset_id": func() string {
			if index == 1 {
				return os.Getenv("ADS_LIVE_SECOND_CARD_IMAGE")
			}
			return logo
		}(), "idempotency_key": fmt.Sprintf("card-%s-%d", stamp, index)})
		requireOK(t, card, err)
		cards = append(cards, lastResourceSegment(firstResourceName(card)))
	}
	carouselArgs := cloneMap(imageArgs)
	carouselArgs["ad_format"] = "carousel"
	carouselArgs["card_asset_ids"] = cards
	carouselArgs["name"] = "Paused carousel validation"
	carouselArgs["idempotency_key"] = "carousel-" + stamp
	delete(carouselArgs, "image_asset_ids")
	carouselAd, err := a.toolAdCreate(ctx, carouselArgs)
	requireOK(t, carouselAd, err)
	t.Log("Created paused carousel ad", createdProviderID(carouselAd, "ad"))
	// Exercise format-specific mutable fields and masks against Google.
	updatedAd := cloneMap(adArgs)
	updatedAd["ad_id"] = createdProviderID(ad, "ad")
	updatedAd["headlines"] = []any{map[string]any{"text": "Explore more on our channel"}}
	updatedAd["idempotency_key"] = "replace-" + stamp
	updatedAd["replace_creative"] = true
	updated, err := a.toolAdUpdate(ctx, updatedAd)
	requireOK(t, updated, err)
	// Switching the selected YouTube event must remain campaign-specific.
	changed, err := a.toolCampaignUpdate(ctx, map[string]any{"ad_account_id": account, "campaign_id": cid, "optimization_goal": "follow_on_views"})
	requireOK(t, changed, err)
	switchedReady, err := a.toolConversionReadiness(ctx, map[string]any{"ad_account_id": account, "campaign_id": cid})
	requireOK(t, switchedReady, err)
	if firstString(asMap(switchedReady), "optimization_goal") != "follow_on_views" {
		t.Fatal("goal switch did not persist")
	}
	defaults, err := a.toolCampaignUpdate(ctx, map[string]any{"ad_account_id": account, "campaign_id": cid, "optimization_goal": "link_clicks", "conversion_action_resource_ids": []any{}})
	requireOK(t, defaults, err)
	for _, kind := range []string{"placement", "creative"} {
		r, e := a.toolPerformanceGet(ctx, map[string]any{"ad_account_id": account, "level": "campaign", "entity_ids": []any{cid}, "date_from": today, "date_to": today, "breakdown": kind, "refresh": true})
		requireOK(t, r, e)
	}
	t.Log("Real create/read/update/report and video/image/carousel formats passed; no paid delivery was enabled")

}
