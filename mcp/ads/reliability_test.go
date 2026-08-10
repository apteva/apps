package main

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func noRetryDelayApp() *App {
	return &App{
		retryDelay: func(int) time.Duration { return 0 },
		sleep:      func(*sdk.AppCtx, time.Duration) bool { return true },
	}
}

func addReliabilityAccount(t *testing.T, ctx *sdk.AppCtx) int64 {
	t.Helper()
	res, err := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func metaTransientResult() *sdk.ExecuteResult {
	return &sdk.ExecuteResult{
		Success: false,
		Status:  500,
		Data: json.RawMessage(`{
			"error": {
				"code": 2,
				"type": "OAuthException",
				"is_transient": true,
				"message": "An unexpected error has occurred. Please retry your request later.",
				"fbtrace_id": "AJErU6lvtebZdt9CMG7uQ2N"
			}
		}`),
	}
}

func TestMetaAdSetUpdateRetriesTransientFailure(t *testing.T) {
	pf := newRecordingPlatform()
	var updateAttempts int
	pf.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "adset_list":
			return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"data":[{"id":"adset_1"}]}`)}, nil
		case "adset_update":
			updateAttempts++
			if updateAttempts == 1 {
				return metaTransientResult(), nil
			}
			return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"success":true}`)}, nil
		default:
			t.Fatalf("unexpected tool %s", tool)
			return nil, nil
		}
	}
	ctx := newAdsCtx(t, pf)
	accountID := addReliabilityAccount(t, ctx)

	out, err := noRetryDelayApp().toolAdSetUpdate(ctx, map[string]any{
		"ad_account_id": accountID,
		"adset_id":      "adset_1",
		"status":        "ACTIVE",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updateAttempts != 2 {
		t.Fatalf("expected transient update to retry once, got %d attempts", updateAttempts)
	}
	if asMap(out)["success"] != true {
		t.Fatalf("unexpected successful response: %#v", out)
	}
}

func TestMetaCode190IsStructuredAndNotRetried(t *testing.T) {
	pf := newRecordingPlatform()
	var updateAttempts int
	pf.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "campaign_list" {
			return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"data":[{"id":"campaign_1"}]}`)}, nil
		}
		updateAttempts++
		return &sdk.ExecuteResult{
			Success: false,
			Status:  401,
			Data:    json.RawMessage(`{"error":{"code":190,"type":"OAuthException","message":"Invalid OAuth access token.","fbtrace_id":"trace-190"}}`),
		}, nil
	}
	ctx := newAdsCtx(t, pf)
	accountID := addReliabilityAccount(t, ctx)

	out, err := noRetryDelayApp().toolCampaignResume(ctx, map[string]any{
		"ad_account_id": accountID,
		"campaign_id":   "campaign_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	result := asMap(out)
	if updateAttempts != 1 {
		t.Fatalf("credential failure was retried %d times", updateAttempts)
	}
	if result["code"] != "provider_auth_error" || result["provider_code"] != 190 || result["retryable"] != false || result["fbtrace_id"] != "trace-190" {
		t.Fatalf("credential error was not structured: %#v", result)
	}
}

func TestMetaCreateOperationDoesNotRetryTransientFailure(t *testing.T) {
	pf := newRecordingPlatform()
	var createAttempts int
	pf.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool != "campaign_create" {
			t.Fatalf("unexpected tool %s", tool)
		}
		createAttempts++
		return metaTransientResult(), nil
	}
	ctx := newAdsCtx(t, pf)
	accountID := addReliabilityAccount(t, ctx)

	out, err := noRetryDelayApp().toolCampaignCreate(ctx, map[string]any{
		"ad_account_id": accountID,
		"name":          "No duplicate",
		"objective":     "traffic",
	})
	if err != nil {
		t.Fatal(err)
	}
	result := asMap(out)
	if createAttempts != 1 {
		t.Fatalf("create operation retried %d times", createAttempts)
	}
	if result["code"] != "provider_transient" || result["retryable"] != true || result["attempts"] != 1 {
		t.Fatalf("transient create error was not preserved: %#v", result)
	}
}

func TestDeliveryActivateResumesAfterPartialFailure(t *testing.T) {
	pf := newRecordingPlatform()
	var mu sync.Mutex
	status := map[string]string{"ad": "PAUSED", "adset": "PAUSED", "campaign": "PAUSED"}
	failAdSet := true
	updateCounts := map[string]int{}
	pf.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		mu.Lock()
		defer mu.Unlock()
		switch tool {
		case "ad_list":
			return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"data":[{"id":"ad_1","adset_id":"adset_1","campaign_id":"campaign_1","status":"` + status["ad"] + `","effective_status":"` + status["ad"] + `"}]}`)}, nil
		case "adset_list":
			return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"data":[{"id":"adset_1","campaign_id":"campaign_1","status":"` + status["adset"] + `","effective_status":"` + status["adset"] + `"}]}`)}, nil
		case "campaign_list":
			return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"data":[{"id":"campaign_1","status":"` + status["campaign"] + `","effective_status":"` + status["campaign"] + `"}]}`)}, nil
		case "ad_update":
			updateCounts[tool]++
			status["ad"] = "ACTIVE"
			return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"success":true}`)}, nil
		case "adset_update":
			updateCounts[tool]++
			if failAdSet {
				return metaTransientResult(), nil
			}
			status["adset"] = "ACTIVE"
			return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"success":true}`)}, nil
		case "campaign_update":
			updateCounts[tool]++
			status["campaign"] = "ACTIVE"
			return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"success":true}`)}, nil
		default:
			t.Fatalf("unexpected tool %s", tool)
			return nil, nil
		}
	}
	ctx := newAdsCtx(t, pf)
	accountID := addReliabilityAccount(t, ctx)
	args := map[string]any{
		"ad_account_id": accountID,
		"campaign_id":   "campaign_1",
		"adset_id":      "adset_1",
		"ad_id":         "ad_1",
	}
	app := noRetryDelayApp()

	first, err := app.toolDeliveryActivate(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	partial := asMap(first)
	if partial["status"] != "partial" || partial["failed_step"] != "adset" || updateCounts["ad_update"] != 1 || updateCounts["adset_update"] != 3 {
		t.Fatalf("unexpected partial activation: result=%#v updates=%#v", partial, updateCounts)
	}

	failAdSet = false
	second, err := app.toolDeliveryActivate(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	completed := asMap(second)
	if completed["status"] != "completed" {
		t.Fatalf("activation did not resume: %#v", completed)
	}
	if updateCounts["ad_update"] != 1 {
		t.Fatalf("resume repeated completed ad update: %#v", updateCounts)
	}
	if updateCounts["adset_update"] != 4 || updateCounts["campaign_update"] != 1 {
		t.Fatalf("resume did not finish remaining updates: %#v", updateCounts)
	}
}

func TestDeliveryActivateToolIsDeclared(t *testing.T) {
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "delivery_activate" {
			return
		}
	}
	t.Fatal("delivery_activate is not declared")
}
