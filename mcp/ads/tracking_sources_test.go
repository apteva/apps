package main

import (
	"encoding/json"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestTrackingSourceCreateNormalizesAndSelectsMetaPixel(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "pixel_list":
			return executeJSON(`{"data":[]}`), nil
		case "pixel_create":
			if input["adAccountId"] != "act_42" || input["name"] != "Website conversions" {
				t.Fatalf("unexpected scoped pixel create input: %#v", input)
			}
			return executeJSON(`{"id":"pixel_42"}`), nil
		default:
			t.Fatalf("unexpected tool %s", tool)
			return nil, nil
		}
	}
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")

	resultAny, err := app.toolTrackingSourceCreate(ctx, map[string]any{
		"ad_account_id": accountID,
		"name":          "  Website conversions  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	result := resultAny.(map[string]any)
	if result["created"] != true || result["default_selected"] != true || result["site_tracking_installed"] != false {
		t.Fatalf("unexpected create result: %#v", result)
	}
	response := result["resource"].(map[string]any)
	if _, leaked := response["native_id"]; leaked {
		t.Fatalf("native provider id leaked: %#v", response)
	}
	resource := resourceByProviderType(t, ctx, accountID, "meta_pixel")
	if resource.NativeID != "pixel_42" || !resource.ManagedByApp || resource.DisplayName != "Website conversions" {
		t.Fatalf("tracking source was not normalized: %#v", resource)
	}
	defaults, err := app.resourceDefaults(ctx, &adAccount{ID: accountID})
	if err != nil || defaults[trackingSourceDefaultPurpose].(map[string]any)["id"] != resource.ID {
		t.Fatalf("tracking source was not selected by default: defaults=%#v err=%v", defaults, err)
	}
	if len(platform.executeCalls) != 2 {
		t.Fatalf("expected one discovery and one create call, got %#v", platform.executeCalls)
	}
}

func TestTrackingSourceCreateReusesOneExactMatchWithoutProviderCreate(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool != "pixel_list" {
			t.Fatalf("unexpected create call while reusing: %s", tool)
		}
		return executeJSON(`{"data":[{"id":"pixel_existing","name":"Main Pixel"}]}`), nil
	}
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")

	resultAny, err := app.toolTrackingSourceCreate(ctx, map[string]any{
		"ad_account_id": accountID, "name": "main pixel", "set_default": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := resultAny.(map[string]any)
	if result["created"] != false || result["reused"] != true || result["default_selected"] != false {
		t.Fatalf("unexpected reuse result: %#v", result)
	}
	var defaults int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ad_resource_defaults`).Scan(&defaults)
	if defaults != 0 || len(platform.executeCalls) != 1 {
		t.Fatalf("reuse changed defaults or called create: defaults=%d calls=%#v", defaults, platform.executeCalls)
	}
}

func TestTrackingSourceCreateRequiresSelectionForDuplicateNames(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool != "pixel_list" {
			t.Fatalf("unexpected tool %s", tool)
		}
		return executeJSON(`{"data":[{"id":"pixel_1","name":"Main"},{"id":"pixel_2","name":"main"}]}`), nil
	}
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")

	resultAny, err := app.toolTrackingSourceCreate(ctx, map[string]any{"ad_account_id": accountID, "name": "Main"})
	if err != nil {
		t.Fatal(err)
	}
	result := resultAny.(map[string]any)
	if result["code"] != "selection_required" || len(result["choices"].([]map[string]any)) != 2 {
		t.Fatalf("expected provider-neutral selection error: %#v", result)
	}
	if len(platform.executeCalls) != 1 {
		t.Fatalf("duplicate reconciliation must not create: %#v", platform.executeCalls)
	}
}

func TestTrackingSourceCreateReconcilesTransientFailureWithoutRetry(t *testing.T) {
	platform := newRecordingPlatform()
	listCalls := 0
	createCalls := 0
	platform.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		switch tool {
		case "pixel_list":
			listCalls++
			if listCalls == 1 {
				return executeJSON(`{"data":[]}`), nil
			}
			return executeJSON(`{"data":[{"id":"pixel_reconciled","name":"Main"}]}`), nil
		case "pixel_create":
			createCalls++
			return &sdk.ExecuteResult{Success: false, Status: 500, Data: json.RawMessage(`{"error":{"code":2,"type":"OAuthException","is_transient":true,"message":"Try later","fbtrace_id":"trace_1"}}`)}, nil
		default:
			t.Fatalf("unexpected tool %s", tool)
			return nil, nil
		}
	}
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")

	resultAny, err := app.toolTrackingSourceCreate(ctx, map[string]any{"ad_account_id": accountID, "name": "Main"})
	if err != nil {
		t.Fatal(err)
	}
	result := resultAny.(map[string]any)
	if result["reconciled"] != true || createCalls != 1 || listCalls != 2 {
		t.Fatalf("create was not safely reconciled: result=%#v create=%d list=%d", result, createCalls, listCalls)
	}
	if resource := resourceByProviderType(t, ctx, accountID, "meta_pixel"); !resource.ManagedByApp {
		t.Fatalf("reconciled tracking source was not marked app-managed: %#v", resource)
	}
}

func TestTrackingSourceCreateReturnsStructuredUnsupportedError(t *testing.T) {
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")

	resultAny, err := app.toolTrackingSourceCreate(ctx, map[string]any{"ad_account_id": accountID, "name": "Main"})
	if err != nil {
		t.Fatal(err)
	}
	result := resultAny.(map[string]any)
	if result["code"] != "unsupported_operation" || result["platform"] != "google" || len(platform.executeCalls) != 0 {
		t.Fatalf("unexpected unsupported response: %#v calls=%#v", result, platform.executeCalls)
	}
}

func TestTrackingSourceCreateDoesNotRetryUnreconciledCreate(t *testing.T) {
	platform := newRecordingPlatform()
	createCalls := 0
	platform.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "pixel_list" {
			return executeJSON(`{"data":[]}`), nil
		}
		createCalls++
		return &sdk.ExecuteResult{Success: false, Status: 403, Data: json.RawMessage(`{"error":{"code":190,"type":"OAuthException","message":"Invalid token"}}`)}, nil
	}
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")

	resultAny, err := app.toolTrackingSourceCreate(ctx, map[string]any{"ad_account_id": accountID, "name": "Main"})
	if err != nil {
		t.Fatal(err)
	}
	result := resultAny.(map[string]any)
	if result["code"] != "provider_auth_error" || result["provider_code"] != 190 || createCalls != 1 {
		t.Fatalf("auth failure was not preserved: result=%#v creates=%d", result, createCalls)
	}
}

func TestTrackingSourceCreatePreservesExistingDefaultWhenOptedOut(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "pixel_list" {
			return executeJSON(`{"data":[{"id":"pixel_old","name":"Existing"}]}`), nil
		}
		return executeJSON(`{"id":"pixel_new"}`), nil
	}
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")
	oldResource, err := app.upsertResource(ctx, &adAccount{ID: accountID, Platform: "meta"}, discoveredResource{
		Kind: resourceTrackingSource, ProviderType: "meta_pixel", NativeID: "pixel_old",
		DisplayName: "Existing", Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.setResourceDefault(ctx, &adAccount{ID: accountID}, trackingSourceDefaultPurpose, oldResource.ID); err != nil {
		t.Fatal(err)
	}

	resultAny, err := app.toolTrackingSourceCreate(ctx, map[string]any{
		"ad_account_id": accountID, "name": "New Pixel", "set_default": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resultAny.(map[string]any)["default"] != false {
		t.Fatalf("unexpected default result: %#v", resultAny)
	}
	var defaultID int64
	if err := ctx.AppDB().QueryRow(`SELECT resource_id FROM ad_resource_defaults WHERE ad_account_id=? AND purpose=?`, accountID, trackingSourceDefaultPurpose).Scan(&defaultID); err != nil {
		t.Fatal(err)
	}
	if defaultID != oldResource.ID {
		t.Fatalf("existing default changed from %d to %d", oldResource.ID, defaultID)
	}
}

func TestTrackingSourceCreateRejectsProviderSuccessWithoutID(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "pixel_list" {
			return executeJSON(`{"data":[]}`), nil
		}
		return executeJSON(`{"success":true}`), nil
	}
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")

	resultAny, err := app.toolTrackingSourceCreate(ctx, map[string]any{"ad_account_id": accountID, "name": "Main"})
	if err != nil {
		t.Fatal(err)
	}
	result := resultAny.(map[string]any)
	if result["code"] != "provider_response_invalid" {
		t.Fatalf("missing provider id was accepted: %#v", result)
	}
	var resources int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ad_resources`).Scan(&resources)
	if resources != 0 {
		t.Fatalf("invalid provider response created %d local resources", resources)
	}
}

func TestTrackingSourceCreatePreservesMetaPermissionDiagnostics(t *testing.T) {
	platform := newRecordingPlatform()
	platform.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "pixel_list" {
			return executeJSON(`{"data":[]}`), nil
		}
		return &sdk.ExecuteResult{Success: false, Status: 403, Data: json.RawMessage(`{"error":{"code":10,"error_subcode":200,"type":"OAuthException","message":"Permission denied","fbtrace_id":"trace_permission"}}`)}, nil
	}
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")

	resultAny, err := app.toolTrackingSourceCreate(ctx, map[string]any{"ad_account_id": accountID, "name": "Main"})
	if err != nil {
		t.Fatal(err)
	}
	result := resultAny.(map[string]any)
	if result["provider_code"] != 10 || result["provider_subcode"] != 200 || result["fbtrace_id"] != "trace_permission" || result["retryable"] != false {
		t.Fatalf("Meta diagnostics were not preserved: %#v", result)
	}
}

func TestTrackingSourceCreateRejectsCrossProjectAccount(t *testing.T) {
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	result, err := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name, status)
		 VALUES ('another-project', 'meta', 7, 'act_other', 'Other', 'active')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := result.LastInsertId()

	resultAny, err := app.toolTrackingSourceCreate(ctx, map[string]any{"ad_account_id": accountID, "name": "Main"})
	if err != nil {
		t.Fatal(err)
	}
	if resultAny.(map[string]any)["isError"] != true || len(platform.executeCalls) != 0 {
		t.Fatalf("cross-project account reached provider: result=%#v calls=%#v", resultAny, platform.executeCalls)
	}
}

func TestTrackingSourceCreateRemainsManagedAfterRefresh(t *testing.T) {
	platform := newRecordingPlatform()
	listCalls := 0
	platform.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "pixel_create" {
			return executeJSON(`{"id":"pixel_42"}`), nil
		}
		listCalls++
		if listCalls == 1 {
			return executeJSON(`{"data":[]}`), nil
		}
		return executeJSON(`{"data":[{"id":"pixel_42","name":"Main"}]}`), nil
	}
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")
	if result, err := app.toolTrackingSourceCreate(ctx, map[string]any{"ad_account_id": accountID, "name": "Main"}); err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("create failed: result=%#v err=%v", result, err)
	}
	if result, err := app.toolResourceRefresh(ctx, map[string]any{"ad_account_id": accountID, "kinds": []any{resourceTrackingSource}}); err != nil || result.(map[string]any)["isError"] == true {
		t.Fatalf("refresh failed: result=%#v err=%v", result, err)
	}
	if resource := resourceByProviderType(t, ctx, accountID, "meta_pixel"); !resource.ManagedByApp {
		t.Fatalf("refresh cleared managed_by_app: %#v", resource)
	}
}

func TestTrackingSourceCapabilitiesAreProviderNeutral(t *testing.T) {
	meta := accountResourceCapabilities("meta")["tracking_source"].(map[string]any)
	google := accountResourceCapabilities("google")["tracking_source"].(map[string]any)
	x := accountResourceCapabilities("x")["tracking_source"].(map[string]any)
	if meta["create"] != true || meta["resource_kind"] != resourceTrackingSource {
		t.Fatalf("unexpected Meta capabilities: %#v", meta)
	}
	if google["create"] != false || google["resource_kind"] != resourceConversionAction {
		t.Fatalf("unexpected Google capabilities: %#v", google)
	}
	if x["supported"] != false || x["create"] != false {
		t.Fatalf("unexpected X capabilities: %#v", x)
	}
}
