package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func criterionOps(t *testing.T, pf *recordingPlatform) []any {
	t.Helper()
	ops, _ := findExecuteCall(t, pf, "campaign_criterion_mutate").Input["operations"].([]any)
	return ops
}

func TestCampaignTargetingAddBuildsLocationAndLanguageCriteria(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	result, err := app.toolCampaignTargetingAdd(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": "987",
		// targeting_catalog_search returns resource names; bare ids are also accepted.
		"locations":          []any{"geoTargetConstants/2840", "2826"},
		"excluded_locations": []any{"geoTargetConstants/2124"},
		"languages":          []any{"1000"},
	})
	if err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("campaign_targeting_add failed: %#v", result)
	}
	ops := criterionOps(t, pf)
	if len(ops) != 4 {
		t.Fatalf("expected 4 criteria, got %d: %#v", len(ops), ops)
	}

	first := asMap(asMap(ops[0])["create"])
	if first["campaign"] != "customers/1234567890/campaigns/987" {
		t.Fatalf("criterion not scoped to the campaign: %#v", first)
	}
	if asMap(first["location"])["geoTargetConstant"] != "geoTargetConstants/2840" {
		t.Fatalf("location criterion wrong: %#v", first)
	}
	if _, negative := first["negative"]; negative {
		t.Fatalf("an included location must not be negative: %#v", first)
	}
	// A bare id is normalized to the resource name Google expects.
	if asMap(asMap(asMap(ops[1])["create"])["location"])["geoTargetConstant"] != "geoTargetConstants/2826" {
		t.Fatalf("bare geo id not normalized: %#v", ops[1])
	}
	excluded := asMap(asMap(ops[2])["create"])
	if excluded["negative"] != true {
		t.Fatalf("excluded location is not negative: %#v", excluded)
	}
	language := asMap(asMap(ops[3])["create"])
	if asMap(language["language"])["languageConstant"] != "languageConstants/1000" {
		t.Fatalf("language criterion wrong: %#v", language)
	}
	// Google has no negative language criterion.
	if _, negative := language["negative"]; negative {
		t.Fatalf("language criterion must not be negative: %#v", language)
	}
}

func TestCampaignTargetingValidation(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	cases := []struct {
		name string
		args map[string]any
	}{
		{"nothing supplied", map[string]any{}},
		{"non-numeric geo id", map[string]any{"locations": []any{"united-states"}}},
		{"wrong constant namespace", map[string]any{"locations": []any{"languageConstants/1000"}}},
		{"same location included and excluded", map[string]any{
			"locations": []any{"2840"}, "excluded_locations": []any{"geoTargetConstants/2840"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"ad_account_id": accountID, "campaign_id": "987"}
			for k, v := range tc.args {
				args[k] = v
			}
			result, err := app.toolCampaignTargetingAdd(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			if result.(map[string]any)["isError"] != true {
				t.Fatalf("invalid targeting accepted: %#v", result)
			}
		})
	}
	if len(pf.executeCalls) != 0 {
		t.Fatalf("invalid targeting reached the provider: %#v", pf.executeCalls)
	}
}

func TestCampaignTargetingListFlagsUntargetedCampaign(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["search"] = executeJSON(`{"results":[]}`)
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	result, err := app.toolCampaignTargetingList(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": "987",
	})
	if err != nil {
		t.Fatal(err)
	}
	// An empty list reads like "nothing to see"; the flag says what it means.
	if result.(map[string]any)["targets_everywhere"] != true {
		t.Fatalf("a campaign with no location criteria should be flagged: %#v", result)
	}

	pf.executeResponses["search"] = executeJSON(`{"results":[
		{"campaign":{"id":"987"},"campaignCriterion":{"criterionId":"11","type":"LOCATION","negative":false,
		 "location":{"geoTargetConstant":"geoTargetConstants/2840"}}},
		{"campaign":{"id":"987"},"campaignCriterion":{"criterionId":"12","type":"LOCATION","negative":true,
		 "location":{"geoTargetConstant":"geoTargetConstants/2124"}}},
		{"campaign":{"id":"987"},"campaignCriterion":{"criterionId":"13","type":"LANGUAGE",
		 "language":{"languageConstant":"languageConstants/1000"}}}
	]}`)
	result, _ = app.toolCampaignTargetingList(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": "987",
	})
	out := result.(map[string]any)
	if out["targets_everywhere"] != false {
		t.Fatalf("targeted campaign flagged as everywhere: %#v", out)
	}
	locations := out["locations"].([]map[string]any)
	languages := out["languages"].([]map[string]any)
	if len(locations) != 2 || len(languages) != 1 {
		t.Fatalf("criteria not split by kind: %#v", out)
	}
	if locations[0]["geo_target_constant"] != "geoTargetConstants/2840" || locations[0]["negative"] != false {
		t.Fatalf("included location wrong: %#v", locations[0])
	}
	if locations[1]["negative"] != true {
		t.Fatalf("excluded location not marked negative: %#v", locations[1])
	}
}

func TestCampaignTargetingRemoveTargetsCriterionResources(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	if result, err := app.toolCampaignTargetingRemove(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": "987", "criterion_ids": []any{"11", "12"},
	}); err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("campaign_targeting_remove failed: %#v", result)
	}
	ops := criterionOps(t, pf)
	if len(ops) != 2 || asMap(ops[0])["remove"] != "customers/1234567890/campaignCriteria/987~11" {
		t.Fatalf("wrong criterion resources: %#v", ops)
	}

	// The error must name criterion_ids, not the keyword field it shares a parser with.
	result, _ := app.toolCampaignTargetingRemove(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": "987", "criterion_ids": []any{"abc"},
	})
	text := toString(result.(map[string]any)["content"].([]map[string]any)[0]["text"])
	if !strings.Contains(text, "criterion_ids") {
		t.Fatalf("error should name criterion_ids: %q", text)
	}
}

// ── campaign_create integration ────────────────────────────────────────────

func TestCampaignCreateAppliesLocationTargeting(t *testing.T) {
	pf := budgetOKPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID, map[string]any{
		"locations": []any{"geoTargetConstants/2840"}, "languages": []any{"1000"},
	}))
	if err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("campaign_create failed: %#v", result)
	}
	ops := criterionOps(t, pf)
	if len(ops) != 2 {
		t.Fatalf("expected location and language criteria: %#v", ops)
	}
	if asMap(asMap(asMap(ops[0])["create"])["location"])["geoTargetConstant"] != "geoTargetConstants/2840" {
		t.Fatalf("location not applied to the new campaign: %#v", ops[0])
	}
	targeting := asMap(result.(map[string]any)["targeting"])
	if targeting["locations"] != 1 || targeting["languages"] != 1 {
		t.Fatalf("targeting summary wrong: %#v", targeting)
	}
	if _, warned := result.(map[string]any)["targeting_warning"]; warned {
		t.Fatalf("a targeted campaign should not warn: %#v", result)
	}
}

func TestCampaignCreateWarnsWhenItWouldTargetEverywhere(t *testing.T) {
	pf := budgetOKPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID, nil))
	if err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("campaign_create failed: %#v", result)
	}
	// Nothing rejects an untargeted campaign, so the consequence is named.
	warning := toString(result.(map[string]any)["targeting_warning"])
	if !strings.Contains(warning, "everywhere") {
		t.Fatalf("untargeted campaign did not warn: %#v", result)
	}
}

func TestCampaignCreateRejectsBadGeoIDBeforeSpendingAnything(t *testing.T) {
	pf := budgetOKPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID,
		map[string]any{"locations": []any{"United States"}}))
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["isError"] != true {
		t.Fatalf("a bad geo id was accepted: %#v", result)
	}
	// Validated before the budget mutate, so nothing is stranded.
	if len(pf.executeCalls) != 0 {
		t.Fatalf("bad targeting created provider state: %#v", pf.executeCalls)
	}
}

func TestCampaignCreateReportsCampaignLeftUntargeted(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "budget_mutate":
			return executeJSON(`{"results":[{"resourceName":"customers/1234567890/campaignBudgets/55"}]}`), nil
		case "campaign_mutate":
			return executeJSON(`{"results":[{"resourceName":"customers/1234567890/campaigns/987"}]}`), nil
		}
		return &sdk.ExecuteResult{Success: false, Status: 400,
			Data: json.RawMessage(`{"error":"INVALID_GEO_TARGET"}`)}, nil
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	result, err := app.toolCampaignCreate(ctx, googleCampaignCreateArgs(accountID,
		map[string]any{"locations": []any{"2840"}}))
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	if out["isError"] != true {
		t.Fatalf("targeting failure should surface: %#v", out)
	}
	// The campaign exists and currently serves everywhere — the dangerous state.
	if out["campaign_created"] != true || out["campaign_id"] != "987" {
		t.Fatalf("the created-but-untargeted campaign was not named: %#v", out)
	}
}
