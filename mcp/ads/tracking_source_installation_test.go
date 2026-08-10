package main

import (
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestTrackingSourceInstallationUsesNormalizedResource(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "pixel_list" {
			return executeJSON(`{"data":[{"id":"pixel_public_42","name":"Store Pixel","is_unavailable":false}]}`), nil
		}
		return executeJSON(`{"data":[]}`), nil
	}
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")
	if result, err := app.toolAccountContextGet(ctx, map[string]any{"ad_account_id": accountID}); err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("discover resources: result=%#v err=%v", result, err)
	}
	resource := resourceByProviderType(t, ctx, accountID, "meta_pixel")
	if _, err := app.toolResourceSetDefault(ctx, map[string]any{
		"ad_account_id": accountID, "purpose": trackingSourceDefaultPurpose, "resource_id": resource.ID,
	}); err != nil {
		t.Fatal(err)
	}

	resultAny, err := app.toolTrackingSourceInstallationGet(ctx, map[string]any{"ad_account_id": accountID})
	if err != nil {
		t.Fatal(err)
	}
	result := resultAny.(map[string]any)
	installation := result["installation"].(map[string]any)
	if installation["public_id"] != "pixel_public_42" || installation["provider"] != "meta" {
		t.Fatalf("unexpected installation config: %#v", installation)
	}
	response := result["resource"].(map[string]any)
	if _, leaked := response["native_id"]; leaked {
		t.Fatalf("normal resource response leaked native id: %#v", response)
	}
	if strings.Contains(toString(installation["script_url"]), "pixel_public_42") {
		t.Fatalf("script URL should be provider-static: %#v", installation)
	}
	if len(installation["image_origins"].([]string)) == 0 {
		t.Fatalf("installation config must allow Meta's tracking image endpoint: %#v", installation)
	}
}

func TestTrackingSourceInstallationEnforcesAccountOwnershipAndProvider(t *testing.T) {
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	metaAccountID := seedResourceTestAccount(t, ctx, "meta", "act_1")
	otherAccountID := seedResourceTestAccount(t, ctx, "meta", "act_2")
	googleAccountID := seedResourceTestAccount(t, ctx, "google", "1234567890")
	resource, err := app.upsertResource(ctx, &adAccount{ID: metaAccountID, Platform: "meta"}, discoveredResource{
		Kind: resourceTrackingSource, ProviderType: "meta_pixel", NativeID: "pixel_1", DisplayName: "Main", Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}

	resultAny, err := app.toolTrackingSourceInstallationGet(ctx, map[string]any{
		"ad_account_id": otherAccountID, "tracking_source_resource_id": resource.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resultAny.(map[string]any)["isError"] != true {
		t.Fatalf("cross-account resource was accepted: %#v", resultAny)
	}

	resultAny, err = app.toolTrackingSourceInstallationGet(ctx, map[string]any{"ad_account_id": googleAccountID})
	if err != nil {
		t.Fatal(err)
	}
	result := resultAny.(map[string]any)
	if result["code"] != "unsupported_operation" || result["platform"] != "google" {
		t.Fatalf("unexpected unsupported response: %#v", result)
	}
}
