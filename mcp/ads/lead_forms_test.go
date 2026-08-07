package main

import (
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func seedPublishingIdentityForTest(t *testing.T, ctx *sdk.AppCtx, accountID int64) int64 {
	t.Helper()
	result, err := ctx.AppDB().Exec(
		`INSERT INTO ad_resources
		 (project_id, ad_account_id, platform, native_asset_id, provider_type, kind, display_name, status, capabilities_json, metadata_json, managed_by_app)
		 VALUES ('test-proj', ?, 'meta', 'page_1', 'facebook_page', 'identity', 'Test Page', 'active', '[]', '{}', 0)`,
		accountID,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func TestMetaLeadFormLifecycleAndInstantFormWiring(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "leadform_create":
			return executeJSON(`{"id":"form_99"}`), nil
		case "leadform_delete":
			return executeJSON(`{"success":true}`), nil
		default:
			return executeJSON(`{"id":"created_1"}`), nil
		}
	}
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")
	pageID := seedPublishingIdentityForTest(t, ctx, accountID)
	if _, err := app.toolResourceSetDefault(ctx, map[string]any{"ad_account_id": accountID, "purpose": "publishing_identity", "resource_id": pageID}); err != nil {
		t.Fatal(err)
	}

	created, err := app.toolLeadFormCreate(ctx, map[string]any{
		"ad_account_id": accountID, "name": "Website leads", "privacy_policy_url": "https://example.com/privacy",
		"questions":     []any{map[string]any{"type": "full_name"}, map[string]any{"type": "email"}, map[string]any{"type": "phone"}},
		"follow_up_url": "https://example.com/thanks", "higher_intent": true,
	})
	if err != nil || created.(map[string]any)["resource"] == nil {
		t.Fatalf("create failed: result=%#v err=%v", created, err)
	}
	form := resourceByProviderType(t, ctx, accountID, "meta_lead_form")
	if form.ParentResourceID != pageID || !form.ManagedByApp {
		t.Fatalf("form ownership was not retained: %#v", form)
	}
	createCall := findExecuteCall(t, platform, "leadform_create")
	if createCall.Input["pageId"] != "page_1" {
		t.Fatalf("native Page was not resolved internally: %#v", createCall.Input)
	}
	questions := createCall.Input["questions"].([]any)
	if questions[2].(map[string]any)["type"] != "PHONE" {
		t.Fatalf("Meta questions were not normalized: %#v", questions)
	}

	creative, err := app.toolCreativeCreate(ctx, map[string]any{
		"ad_account_id": accountID, "format": "image", "name": "Lead image",
		"conversion_location": "instant_form", "lead_form_resource_id": form.ID,
		"destination_url": "https://example.com", "image_url": "https://example.com/ad.jpg",
	})
	if err != nil || creative.(map[string]any)["isError"] == true {
		t.Fatalf("lead creative failed: result=%#v err=%v", creative, err)
	}
	story := findExecuteCall(t, platform, "creative_create").Input["object_story_spec"].(map[string]any)
	cta := story["link_data"].(map[string]any)["call_to_action"].(map[string]any)
	if cta["type"] != "SIGN_UP" || cta["value"].(map[string]any)["lead_gen_form_id"] != "form_99" {
		t.Fatalf("lead form was not wired into creative CTA: %#v", cta)
	}

	adSet, err := app.toolAdSetCreate(ctx, map[string]any{
		"ad_account_id": accountID, "campaign_id": "campaign_1", "name": "Instant leads",
		"optimization_goal": "leads", "conversion_location": "instant_form",
		"targeting": map[string]any{"geo_locations": map[string]any{"countries": []any{"ES"}}},
	})
	if err != nil || adSet.(map[string]any)["isError"] == true {
		t.Fatalf("instant-form ad set failed: result=%#v err=%v", adSet, err)
	}
	adSetInput := findExecuteCall(t, platform, "adset_create").Input
	if adSetInput["destination_type"] != "ON_AD" || adSetInput["promoted_object"].(map[string]any)["page_id"] != "page_1" {
		t.Fatalf("instant-form ad set was not normalized: %#v", adSetInput)
	}

	archived, err := app.toolLeadFormArchive(ctx, map[string]any{"ad_account_id": accountID, "lead_form_resource_id": form.ID})
	if err != nil || archived.(map[string]any)["status"] != "archived" {
		t.Fatalf("archive failed: result=%#v err=%v", archived, err)
	}
	var defaults int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ad_resource_defaults WHERE resource_id=?`, form.ID).Scan(&defaults)
	if defaults != 0 {
		t.Fatalf("archived form remained the default")
	}
}

func TestGoogleLeadFormCreatesAssetAndAttachesCampaign(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "asset_mutate" {
			operations := input["operations"].([]any)
			if _, creating := operations[0].(map[string]any)["create"]; creating {
				return executeJSON(`{"results":[{"resourceName":"customers/1234567890/assets/88"}]}`), nil
			}
		}
		return executeJSON(`{"results":[{"resourceName":"ok"}]}`), nil
	}
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	created, err := app.toolLeadFormCreate(ctx, map[string]any{
		"ad_account_id": accountID, "name": "Quote requests", "privacy_policy_url": "https://example.com/privacy",
		"business_name": "Example", "headline": "Get a quote", "campaign_id": "77", "follow_up_url": "https://example.com/thanks",
		"questions": []any{map[string]any{"type": "full_name"}, map[string]any{"type": "email"}},
	})
	if err != nil || created.(map[string]any)["resource"] == nil {
		t.Fatalf("Google create failed: result=%#v err=%v", created, err)
	}
	assetCall := findExecuteCall(t, platform, "asset_mutate")
	create := assetCall.Input["operations"].([]any)[0].(map[string]any)["create"].(map[string]any)
	leadForm := create["leadFormAsset"].(map[string]any)
	if leadForm["privacyPolicyUrl"] != "https://example.com/privacy" || len(leadForm["fields"].([]any)) != 2 {
		t.Fatalf("Google lead-form payload is incomplete: %#v", leadForm)
	}
	if create["finalUrls"].([]any)[0] != "https://example.com/thanks" {
		t.Fatalf("Google destination was not set on the asset: %#v", create)
	}
	attachCall := findExecuteCall(t, platform, "campaign_asset_mutate")
	link := attachCall.Input["operations"].([]any)[0].(map[string]any)["create"].(map[string]any)
	if link["campaign"] != "customers/1234567890/campaigns/77" || link["asset"] != "customers/1234567890/assets/88" || link["fieldType"] != "LEAD_FORM" {
		t.Fatalf("Google campaign attachment is wrong: %#v", link)
	}
	resource := resourceByProviderType(t, ctx, accountID, "google_lead_form")
	if resource.NativeID != "customers/1234567890/assets/88" || !resource.ManagedByApp {
		t.Fatalf("Google resource was not tracked: %#v", resource)
	}
	if !strings.Contains(toString(resource.Metadata["privacy_policy_url"]), "/privacy") {
		t.Fatalf("normalized metadata missing: %#v", resource.Metadata)
	}
}
