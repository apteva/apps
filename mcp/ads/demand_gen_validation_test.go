package main

import (
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"testing"
)

func TestDemandGenNativeCampaignOptionsRemainCompatible(t *testing.T) {
	pf := budgetOKPlatform()
	ctx := newAdsCtx(t, pf)
	a := &App{}
	id := seedResourceTestAccount(t, ctx, "google", "1234567890")
	args := googleCampaignCreateArgs(id, map[string]any{"channel_type": "demand_gen", "objective": "traffic", "platform_options": map[string]any{"campaign": map[string]any{"targetSpend": map[string]any{}}}})
	r, err := a.toolCampaignCreate(ctx, args)
	requireOK(t, r, err)
	if createdGoogleCampaign(t, pf)["advertisingChannelType"] != "DEMAND_GEN" {
		t.Fatal("native Demand Gen escape hatch regressed")
	}
}
func TestDemandGenSelectedActionsExcludeEveryStandardCampaignGoal(t *testing.T) {
	a, ctx, pf, id := demandFixture(t)
	r, err := a.toolCampaignCreate(ctx, demandCampaignArgs(id))
	requireOK(t, r, err)
	ops := asSlice(findExecuteCall(t, pf, "bulk_mutate").Input["mutateOperations"])
	count := 0
	for _, op := range ops {
		if goal := asMap(asMap(op)["campaignConversionGoalOperation"]); len(goal) > 0 {
			update := asMap(goal["update"])
			if update["biddable"] != false || !strings.Contains(firstString(update, "resourceName"), "/campaignConversionGoals/-2~") {
				t.Fatalf("bad goal isolation %#v", goal)
			}
			count++
		}
		if asMap(op)["customerConversionGoalOperation"] != nil {
			t.Fatal("campaign setup modified account-wide goals")
		}
	}
	if count == 0 {
		t.Fatal("standard goals were left active alongside the custom goal")
	}
}
func TestDemandGenCreativeReplacementIsExplicitAndDurable(t *testing.T) {
	a, ctx, pf, id := demandFixture(t)
	pf.executeResponses["unused"] = nil
	args := demandAdArgs(id)
	args["ad_id"] = "99"
	r, err := a.toolAdUpdate(ctx, args)
	if err != nil || mcpResultError(r) == nil {
		t.Fatal("immutable creative edited without explicit replacement")
	}
	for _, call := range pf.executeCalls {
		if integrationToolMutates(call.Tool) {
			t.Fatal("replacement validation wrote to provider")
		}
	}
	args["replace_creative"] = true
	args["idempotency_key"] = "replace"
	args["status"] = "ACTIVE"
	r, err = a.toolAdUpdate(ctx, args)
	if err != nil || mcpResultError(r) == nil {
		t.Fatal("replacement enabled delivery")
	}
}
func TestDemandGenBreakdownPreservesScopeAndAvoidsAdditiveAssetSpend(t *testing.T) {
	a, ctx, pf, id := demandFixture(t)
	r, err := a.toolPerformanceGet(ctx, map[string]any{"ad_account_id": id, "level": "campaign", "entity_ids": []any{"77"}, "date_from": "2026-10-01", "date_to": "2026-10-02", "breakdown": "creative"})
	requireOK(t, r, err)
	call := findExecuteCall(t, pf, "search")
	q := firstString(call.Input, "query")
	if !strings.Contains(q, "campaign.id IN (77)") || !strings.Contains(q, "FROM ad_group_ad_asset_view") || strings.Contains(q, "metrics.cost_micros") {
		t.Fatalf("incorrect scope or duplicative spend query %s", q)
	}
}

func TestGoogleCreateRetriesOnlyExplicitConcurrentRejection(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		pf := newRecordingPlatform()
		attempts := 0
		pf.executeResponder = func(_ int64, _ string, _ map[string]any) (*sdk.ExecuteResult, error) {
			attempts++
			if attempts == 1 {
				if ambiguous {
					return nil, fmt.Errorf("timeout after sending request")
				}
				return &sdk.ExecuteResult{Success: false, Status: 400, Data: []byte(`{"error":{"details":[{"@type":"type.googleapis.com/google.ads.googleads.v23.errors.GoogleAdsFailure","errors":[{"errorCode":{"databaseError":"CONCURRENT_MODIFICATION"}}]}]}}`)}, nil
			}
			return executeJSON(`{"results":[{"resourceName":"customers/123/assets/45"}]}`), nil
		}
		ctx := newAdsCtx(t, pf)
		app := noRetryDelayApp()
		_, out := app.execIntegrationTool(ctx, &adAccount{Platform: "google", NativeAccountID: "123"}, "asset_mutate", map[string]any{})
		if ambiguous {
			if attempts != 1 || out == nil {
				t.Fatal("ambiguous create retried")
			}
		} else if attempts != 2 || out != nil {
			t.Fatalf("explicit rejection was not retried safely: %d %#v", attempts, out)
		}
	}
}
