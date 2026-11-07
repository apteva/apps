package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestTrackingSourceCreateUsesBusinessAndSharesWithAccount(t *testing.T) {
	platform := newRecordingPlatform()
	var shared map[string]any
	platform.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "pixel_list", "business_pixel_list":
			return executeJSON(`{"data":[]}`), nil
		case "ad_account_get":
			return executeJSON(`{"id":"act_42","business":{"id":"biz_9"}}`), nil
		case "business_pixel_create":
			if input["businessId"] != "biz_9" || input["name"] != "Signup Pixel" {
				t.Fatalf("business create input: %#v", input)
			}
			return executeJSON(`{"id":"pixel_77"}`), nil
		case "pixel_share_with_ad_account":
			shared = input
			return executeJSON(`{"success":true}`), nil
		default:
			t.Fatalf("unexpected tool %s (ad-account pixel_create must not be used)", tool)
			return nil, nil
		}
	}
	ctx := newAdsCtx(t, platform)
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")

	resultAny, err := (&App{}).toolTrackingSourceCreate(ctx, map[string]any{"ad_account_id": accountID, "name": "Signup Pixel"})
	if err != nil {
		t.Fatal(err)
	}
	result := resultAny.(map[string]any)
	if result["created"] != true || result["owner"] != "business" || result["default_selected"] != true {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result["ad_account_association"].(map[string]any)["status"] != "shared" {
		t.Fatalf("association not reported: %#v", result)
	}
	if shared["pixelId"] != "pixel_77" || shared["account_id"] != "42" || shared["business"] != "biz_9" {
		t.Fatalf("share input: %#v", shared)
	}
	if resource := resourceByProviderType(t, ctx, accountID, "meta_pixel"); resource.NativeID != "pixel_77" || !resource.ManagedByApp {
		t.Fatalf("resource: %#v", resource)
	}
}

func TestTrackingSourceCreateReportsFailedShareAndRetrySharesExisting(t *testing.T) {
	platform := newRecordingPlatform()
	creates, shareAttempts := 0, 0
	businessPixels := `{"data":[]}`
	platform.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "pixel_list":
			return executeJSON(`{"data":[]}`), nil
		case "business_pixel_list":
			return executeJSON(businessPixels), nil
		case "ad_account_get":
			return executeJSON(`{"business":{"id":"biz_9"}}`), nil
		case "business_pixel_create":
			creates++
			businessPixels = `{"data":[{"id":"pixel_77","name":"Signup Pixel"}]}`
			return executeJSON(`{"id":"pixel_77"}`), nil
		case "pixel_share_with_ad_account":
			shareAttempts++
			if shareAttempts == 1 {
				return &sdk.ExecuteResult{Success: false, Status: 400, Data: json.RawMessage(`{"error":{"code":200,"type":"OAuthException","message":"Permissions error"}}`)}, nil
			}
			return executeJSON(`{"success":true}`), nil
		}
		t.Fatalf("unexpected tool %s", tool)
		return nil, nil
	}
	ctx := newAdsCtx(t, platform)
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")
	app := &App{}

	first, _ := app.toolTrackingSourceCreate(ctx, map[string]any{"ad_account_id": accountID, "name": "Signup Pixel"})
	firstResult := first.(map[string]any)
	if firstResult["ad_account_association"].(map[string]any)["status"] != "failed" || firstResult["default_selected"] != false {
		t.Fatalf("failed share not reported or default set anyway: %#v", firstResult)
	}
	second, _ := app.toolTrackingSourceCreate(ctx, map[string]any{"ad_account_id": accountID, "name": "Signup Pixel"})
	secondResult := second.(map[string]any)
	if creates != 1 || secondResult["reused"] != true || secondResult["ad_account_association"].(map[string]any)["status"] != "shared" {
		t.Fatalf("retry created a duplicate or did not share: creates=%d result=%#v", creates, secondResult)
	}
}

func TestTrackingSourceCreateExplainsAdAccountOwnerError(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "pixel_list":
			return executeJSON(`{"data":[]}`), nil
		case "pixel_create":
			return &sdk.ExecuteResult{Success: false, Status: 400, Data: json.RawMessage(`{"error":{"code":6200,"type":"OAuthException","message":"A pixel already exists for this account"}}`)}, nil
		}
		t.Fatalf("unexpected tool %s", tool)
		return nil, nil
	}
	ctx := newAdsCtx(t, platform)
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")

	resultAny, _ := (&App{}).toolTrackingSourceCreate(ctx, map[string]any{"ad_account_id": accountID, "name": "Main", "owner": "ad_account"})
	result := resultAny.(map[string]any)
	if result["code"] != "pixel_owner_unsupported" || !strings.Contains(toString(result["hint"]), "owner=business") {
		t.Fatalf("6200 not explained: %#v", result)
	}
}

func readinessPlatform(t *testing.T, hosts string, diagnostics string) *recordingPlatform {
	platform := newRecordingPlatform()
	platform.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "pixel_list":
			return executeJSON(`{"data":[{"id":"pixel_77","name":"Signup Pixel"}]}`), nil
		case "pixel_get":
			return executeJSON(`{"id":"pixel_77","owner_business":{"id":"biz_9"},"is_unavailable":false,"last_fired_time":"2026-10-28T10:00:00+0000"}`), nil
		case "pixel_shared_accounts_list":
			return executeJSON(`{"data":[{"id":"act_42","account_id":"42"}]}`), nil
		case "pixel_stats":
			if input["aggregation"] == "host" {
				return executeJSON(hosts), nil
			}
			return executeJSON(`{"data":[{"aggregation":"event","data":[{"value":"PageView","count":30},{"value":"Lead","count":2}]},{"aggregation":"event","data":[{"value":"PageView","count":10}]}]}`), nil
		case "pixel_diagnostics":
			return executeJSON(diagnostics), nil
		}
		t.Fatalf("unexpected tool %s", tool)
		return nil, nil
	}
	return platform
}

func TestTrackingSourceReadinessReadyWhenDomainAndEventsReceived(t *testing.T) {
	original := fetchSiteHTML
	fetchSiteHTML = func(context.Context, string) (string, int, error) {
		return `<script src="https://connect.facebook.net/en_US/fbevents.js"></script><script>fbq('init','pixel_77')</script>`, 200, nil
	}
	defer func() { fetchSiteHTML = original }()
	platform := readinessPlatform(t,
		`{"data":[{"aggregation":"host","data":[{"value":"www.example.com","count":40}]}]}`,
		`{"data":[{"key":"k1","title":"Event match quality","result":"passed"}]}`)
	ctx := newAdsCtx(t, platform)
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")

	resultAny, err := (&App{}).toolTrackingSourceReadinessGet(ctx, map[string]any{
		"ad_account_id": accountID, "domains": []any{"example.com"}, "required_events": []any{"PageView", "Lead"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := resultAny.(map[string]any)
	if result["readiness"] != "ready" || result["advertising_eligibility"] != "not_established" {
		t.Fatalf("expected ready: %#v", result)
	}
	checks := result["checks"].(map[string]any)
	if checks["events_received"].(map[string]any)["total"] != int64(42) {
		t.Fatalf("hourly buckets not summed: %#v", checks["events_received"])
	}
	domain := checks["domains"].([]map[string]any)[0]
	if domain["installation"].(map[string]any)["status"] != "pixel_found" {
		t.Fatalf("installation: %#v", domain)
	}
	if _, leaked := result["pixel_id"]; leaked || strings.Contains(toString(mustJSON(t, result["checks"])), "biz_9") {
		t.Fatalf("native ids leaked: %#v", result)
	}
}

func TestTrackingSourceReadinessBlocksSilentDomainAndFailedDiagnostics(t *testing.T) {
	original := fetchSiteHTML
	fetchSiteHTML = func(context.Context, string) (string, int, error) { return `<html></html>`, 200, nil }
	defer func() { fetchSiteHTML = original }()
	// An active, available Pixel that receives events from another site only.
	platform := readinessPlatform(t,
		`{"data":[{"aggregation":"host","data":[{"value":"other.example.org","count":40}]}]}`,
		`{"data":[{"key":"domain_restricted","title":"Events from this domain are restricted","result":"failed"}]}`)
	ctx := newAdsCtx(t, platform)
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")

	resultAny, _ := (&App{}).toolTrackingSourceReadinessGet(ctx, map[string]any{
		"ad_account_id": accountID, "domains": []any{"https://shop.example.com/"}, "required_events": []any{"CompleteRegistration"},
	})
	result := resultAny.(map[string]any)
	reasons := strings.Join(result["blocking_reasons"].([]string), "|")
	if result["readiness"] != "not_ready" ||
		!strings.Contains(reasons, "shop.example.com") ||
		!strings.Contains(reasons, "CompleteRegistration") ||
		!strings.Contains(reasons, "restricted") {
		t.Fatalf("blocking reasons missing: %#v", result)
	}
	if len(result["limitations"].([]string)) == 0 {
		t.Fatal("API limitations not stated")
	}
}

func TestReadinessDomainsRejectsNonPublicHosts(t *testing.T) {
	for _, bad := range []string{"localhost", "127.0.0.1", "10.0.0.1", "intranet"} {
		if _, err := readinessDomains([]any{bad}); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestTrackingSourceTestEventSeparatesAcceptanceFromVisibility(t *testing.T) {
	platform := newRecordingPlatform()
	var sent map[string]any
	platform.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "pixel_list":
			return executeJSON(`{"data":[{"id":"pixel_77","name":"Signup Pixel"}]}`), nil
		case "pixel_send_event":
			sent = input
			return executeJSON(`{"events_received":1,"messages":["The test event code does not match an active Test Events session."],"fbtrace_id":"x"}`), nil
		}
		t.Fatalf("unexpected tool %s", tool)
		return nil, nil
	}
	ctx := newAdsCtx(t, platform)
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")

	resultAny, err := (&App{}).toolTrackingSourceTestEventSend(ctx, map[string]any{
		"ad_account_id": accountID, "test_event_code": "TEST123", "event_name": "Lead",
		"event_source_url": "https://shop.example.com/signup",
	})
	if err != nil {
		t.Fatal(err)
	}
	result := resultAny.(map[string]any)
	if result["api_accepted"] != true || result["test_session_match"] != "not_matched" {
		t.Fatalf("acceptance and match not separated: %#v", result)
	}
	if result["verification"].(map[string]any)["advertising_eligibility"] != "not_established" {
		t.Fatalf("eligibility claimed: %#v", result)
	}
	if sent["test_event_code"] != "TEST123" || sent["pixelId"] != "pixel_77" {
		t.Fatalf("sent: %#v", sent)
	}
	if calls := countCalls(platform, "pixel_send_event"); calls != 1 {
		t.Fatalf("test event retried: %d", calls)
	}
}

func countCalls(platform *recordingPlatform, tool string) int {
	n := 0
	for _, call := range platform.executeCalls {
		if call.Tool == tool {
			n++
		}
	}
	return n
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
