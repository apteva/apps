package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestXVideoUploadUsesChunkedMediaProtocol(t *testing.T) {
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("test-video"))
	}))
	defer media.Close()

	platform := newRecordingPlatform()
	calls := []string{}
	platform.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		calls = append(calls, tool)
		switch tool {
		case "media_upload_init":
			if input["total_bytes"] != int64(10) || input["media_type"] != "video/mp4" {
				t.Fatalf("unexpected INIT payload: %#v", input)
			}
			return executeJSON(`{"media_id_string":"media_1"}`), nil
		case "media_upload_append":
			if input["media_id"] != "media_1" || input["segment_index"] != 0 || !strings.HasPrefix(input["media"].(string), "data:video/mp4;base64,") {
				t.Fatalf("unexpected APPEND payload: %#v", input)
			}
			return executeJSON(`{}`), nil
		case "media_upload_finalize":
			if input["media_id"] != "media_1" {
				t.Fatalf("unexpected FINALIZE payload: %#v", input)
			}
			return executeJSON(`{"media_id_string":"media_1","processing_info":{"state":"pending"}}`), nil
		default:
			t.Fatalf("unexpected integration tool %s", tool)
			return nil, nil
		}
	}
	ctx := newAdsCtx(t, platform)
	accountID := seedResourceTestAccount(t, ctx, "x", "18ce54d4x5t")
	out, err := (&App{}).toolCreativeUpload(ctx, map[string]any{
		"ad_account_id": accountID, "kind": "video", "source_url": media.URL,
	})
	if err != nil || asMap(out)["id"] != "media_1" || strings.Join(calls, ",") != "media_upload_init,media_upload_append,media_upload_finalize" {
		t.Fatalf("upload=%#v calls=%v err=%v", out, calls, err)
	}
}

func TestXCampaignListNormalizesNativeResponse(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if tool != "list_campaigns" || input["account_id"] != "18ce54d4x5t" {
			t.Fatalf("unexpected integration call: %s %#v", tool, input)
		}
		return executeJSON(`{"data":[{"id":"c1","name":"Website","entity_status":"ACTIVE","daily_budget_amount_local_micro":12500000}],"next_cursor":"cursor-2"}`), nil
	}
	ctx := newAdsCtx(t, platform)
	accountID := seedResourceTestAccount(t, ctx, "x", "18ce54d4x5t")

	out, err := (&App{}).toolCampaignList(ctx, map[string]any{"ad_account_id": accountID, "limit": 50})
	if err != nil {
		t.Fatal(err)
	}
	page := asMap(out)
	rows := page["data"].([]map[string]any)
	if len(rows) != 1 || rows[0]["status"] != "ACTIVE" || rows[0]["daily_budget"] != "1250" || page["next_cursor"] != "cursor-2" {
		t.Fatalf("normalized X page=%#v", page)
	}
}

func TestRedditResourcesAutomaticallyWireCampaignAdGroupAndCarousel(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "list_profiles":
			return executeJSON(`{"data":[{"id":"profile_1","name":"Brand"}]}`), nil
		case "list_pixels":
			return executeJSON(`{"data":[{"id":"pixel_1","name":"Main Pixel","status":"ACTIVE"}]}`), nil
		case "list_funding_instruments":
			return executeJSON(`{"data":[{"id":"funding_1","name":"Invoice","status":"ACTIVE"}]}`), nil
		case "list_lead_forms":
			return executeJSON(`{"data":[]}`), nil
		case "list_custom_audiences":
			return executeJSON(`{"data":[{"id":"audience_1","name":"Customers","status":"ACTIVE"}]}`), nil
		case "create_campaign":
			data := asMap(input["data"])
			if data["funding_instrument_id"] != "funding_1" || data["objective"] != "CLICKS" {
				t.Fatalf("campaign was not generically wired: %#v", input)
			}
			return executeJSON(`{"data":{"id":"campaign_1"}}`), nil
		case "create_ad_group":
			data := asMap(input["data"])
			if data["conversion_pixel_id"] != "pixel_1" || data["optimization_goal"] != "CLICKS" {
				t.Fatalf("ad group was not generically wired: %#v", input)
			}
			return executeJSON(`{"data":{"id":"group_1"}}`), nil
		case "create_structured_post_job":
			data := asMap(input["data"])
			creative := asMap(data["creative"])
			cards, _ := creative["carousel"].([]any)
			if input["profile_id"] != "profile_1" || creative["type"] != "CAROUSEL" || len(cards) != 2 {
				t.Fatalf("Reddit carousel payload=%#v", input)
			}
			return executeJSON(`{"data":{"id":"job_1","status":"PENDING"}}`), nil
		default:
			t.Fatalf("unexpected integration tool %s with %#v", tool, input)
			return nil, nil
		}
	}
	ctx := newAdsCtx(t, platform)
	accountID := seedResourceTestAccount(t, ctx, "reddit", "a2_client")
	app := &App{}
	if _, err := app.toolAccountContextGet(ctx, map[string]any{"ad_account_id": accountID}); err != nil {
		t.Fatal(err)
	}

	if out, err := app.toolCampaignCreate(ctx, map[string]any{
		"ad_account_id": accountID, "name": "Traffic", "objective": "traffic", "status": "PAUSED",
	}); err != nil || asMap(out)["isError"] == true {
		t.Fatalf("campaign create: out=%#v err=%v", out, err)
	}
	if out, err := app.toolAdSetCreate(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": "campaign_1", "name": "Visitors",
		"optimization_goal": "link_clicks", "targeting": map[string]any{"locations": []any{"US"}},
	}); err != nil || asMap(out)["isError"] == true {
		t.Fatalf("ad group create: out=%#v err=%v", out, err)
	}
	if out, err := app.toolCreativeCreate(ctx, map[string]any{
		"ad_account_id": accountID, "format": "carousel", "name": "Products", "headline": "Choose one",
		"cards": []any{
			map[string]any{"headline": "One", "image_url": "https://cdn.example.com/one.jpg", "destination_url": "https://example.com/one"},
			map[string]any{"headline": "Two", "image_url": "https://cdn.example.com/two.jpg", "destination_url": "https://example.com/two"},
		},
	}); err != nil || asMap(out)["isError"] == true {
		t.Fatalf("creative create: out=%#v err=%v", out, err)
	}
}

func TestXDeliveryActivationSkipsImmutablePromotedTweet(t *testing.T) {
	platform := newRecordingPlatform()
	statuses := map[string]string{"ad": "ACTIVE", "adset": "PAUSED", "campaign": "PAUSED"}
	updates := map[string]int{}
	platform.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "get_promoted_tweet":
			return executeJSON(`{"data":{"id":"ad_1","line_item_id":"group_1","entity_status":"` + statuses["ad"] + `"}}`), nil
		case "get_line_item":
			return executeJSON(`{"data":{"id":"group_1","campaign_id":"campaign_1","entity_status":"` + statuses["adset"] + `"}}`), nil
		case "get_campaign":
			return executeJSON(`{"data":{"id":"campaign_1","entity_status":"` + statuses["campaign"] + `"}}`), nil
		case "update_line_item":
			updates[tool]++
			statuses["adset"] = "ACTIVE"
			return executeJSON(`{"data":{"id":"group_1"}}`), nil
		case "update_campaign":
			updates[tool]++
			statuses["campaign"] = "ACTIVE"
			return executeJSON(`{"data":{"id":"campaign_1"}}`), nil
		default:
			t.Fatalf("unexpected X activation tool %s input=%#v", tool, input)
			return nil, nil
		}
	}
	ctx := newAdsCtx(t, platform)
	accountID := seedResourceTestAccount(t, ctx, "x", "18ce54d4x5t")
	out, err := (&App{}).toolDeliveryActivate(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": "campaign_1", "adset_id": "group_1", "ad_id": "ad_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if asMap(out)["status"] != "completed" || updates["update_line_item"] != 1 || updates["update_campaign"] != 1 {
		bytes, _ := json.Marshal(out)
		t.Fatalf("activation=%s updates=%#v", bytes, updates)
	}
}
