package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func seedAudience(t *testing.T, app *App, ctx *sdk.AppCtx, accountID int64, platform, nativeID string) adResource {
	t.Helper()
	acct := &adAccount{ID: accountID, ProjectID: "test-proj", Platform: platform}
	resource, err := app.upsertResource(ctx, acct, discoveredResource{
		Kind: resourceAudience, ProviderType: platform + "_audience", NativeID: nativeID,
		DisplayName: "Customers", Status: "active", Capabilities: audienceResourceCapabilities(platform, "customer_list"),
		Metadata: map[string]any{"type": "customer_list"}, ManagedByApp: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return *resource
}

func TestAudienceCapabilitiesAreProviderSpecific(t *testing.T) {
	ctx := newAdsCtx(t, newRecordingPlatform())
	app := &App{}
	googleID := seedResourceTestAccount(t, ctx, "google", "1234567890")
	out, err := app.toolAudienceCapabilitiesGet(ctx, map[string]any{"ad_account_id": googleID})
	if err != nil {
		t.Fatal(err)
	}
	capabilities := out.(map[string]any)
	if containsString(capabilities["kinds"].([]string), "lookalike") {
		t.Fatalf("Google must not advertise removed similar-audience creation: %#v", capabilities)
	}
	privacy := capabilities["privacy"].(map[string]any)
	if privacy["hashes_before_provider"] != true || privacy["persists_identifiers"] != false {
		t.Fatalf("privacy contract=%#v", privacy)
	}
}

func TestAudienceSyncHashesInMemoryAndIsIdempotent(t *testing.T) {
	platform := newRecordingPlatform()
	csvBody := "email,phone\nAlice@Example.com,+1 (415) 555-0100\n"
	platform.callAppResponses["storage:files_get_content"] = json.RawMessage(`{"id":8,"name":"audience.csv","content_type":"text/csv","content_base64":"` + base64.StdEncoding.EncodeToString([]byte(csvBody)) + `"}`)
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "meta", "act_42")
	audience := seedAudience(t, app, ctx, accountID, "meta", "aud_1")
	args := map[string]any{
		"ad_account_id": accountID, "audience_id": audience.ID, "operation": "add",
		"source": map[string]any{"kind": "storage", "ref": "8"}, "idempotency_key": "sync-1",
	}
	first, err := app.toolAudienceMembersSync(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.toolAudienceMembersSync(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if first.(map[string]any)["id"] != second.(map[string]any)["id"] {
		t.Fatalf("idempotency failed: first=%#v second=%#v", first, second)
	}
	if err := app.runAudienceSyncProcessor(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	call := findExecuteCall(t, platform, "audience_users_add")
	payload := call.Input["payload"].(map[string]any)
	data := payload["data"].([][]string)
	emailHash := sha256.Sum256([]byte("alice@example.com"))
	phoneHash := sha256.Sum256([]byte("+14155550100"))
	if data[0][0] != hex.EncodeToString(emailHash[:]) || data[0][1] != hex.EncodeToString(phoneHash[:]) {
		t.Fatalf("provider received incorrect normalized hashes: %#v", data)
	}
	var persisted string
	if err := ctx.AppDB().QueryRow(`SELECT mapping_json || consent_json || source_checksum || last_error FROM ad_audience_jobs WHERE id=?`, first.(map[string]any)["id"]).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(persisted), "alice") || strings.Contains(persisted, "415") {
		t.Fatalf("raw PII persisted in job: %q", persisted)
	}
}

func TestAudienceSyncRejectsCrossProjectAndMissingGoogleConsent(t *testing.T) {
	ctx := newAdsCtx(t, newRecordingPlatform())
	app := &App{}
	result, err := ctx.AppDB().Exec(`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name) VALUES ('other-project','meta',7,'act_other','Other')`)
	if err != nil {
		t.Fatal(err)
	}
	otherID, _ := result.LastInsertId()
	out, err := app.toolAudienceCapabilitiesGet(ctx, map[string]any{"ad_account_id": otherID})
	if err != nil {
		t.Fatal(err)
	}
	if asMap(out)["isError"] != true {
		t.Fatalf("cross-project account was exposed: %#v", out)
	}

	googleID := seedResourceTestAccount(t, ctx, "google", "1234567890")
	audience := seedAudience(t, app, ctx, googleID, "google", "77")
	out, err = app.toolAudienceMembersSync(ctx, map[string]any{
		"ad_account_id": googleID, "audience_id": audience.ID, "operation": "add",
		"source": map[string]any{"kind": "storage", "ref": "8"}, "idempotency_key": "google-no-consent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if asMap(out)["isError"] != true || !strings.Contains(strings.ToLower(mcpErrorMessage(asMap(out))), "consent") {
		t.Fatalf("missing consent was accepted: %#v", out)
	}
}

func TestGoogleAudienceSyncTracksAsyncDiagnostics(t *testing.T) {
	platform := newRecordingPlatform()
	platform.callAppResponses["storage:files_get_content"] = json.RawMessage(`{"id":8,"name":"audience.csv","content_type":"text/csv","content_base64":"` + base64.StdEncoding.EncodeToString([]byte("email\nbob@example.com\n")) + `"}`)
	platform.executeResponses["data_manager_audience_members_ingest"] = executeJSON(`{"requestId":"request-1"}`)
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "google", "1234567890")
	if _, err := ctx.AppDB().Exec(`UPDATE ad_accounts SET login_account_id='9999999999' WHERE id=?`, accountID); err != nil {
		t.Fatal(err)
	}
	audience := seedAudience(t, app, ctx, accountID, "google", "77")
	queued, err := app.toolAudienceMembersSync(ctx, map[string]any{
		"ad_account_id": accountID, "audience_id": audience.ID, "operation": "add",
		"source": map[string]any{"kind": "storage", "ref": "8"}, "idempotency_key": "google-async",
		"consent": map[string]any{"ad_user_data": "granted", "ad_personalization": "granted"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.runAudienceSyncProcessor(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	status, err := app.toolAudienceSyncStatus(ctx, map[string]any{"job_id": queued.(map[string]any)["id"]})
	if err != nil {
		t.Fatal(err)
	}
	if status.(map[string]any)["status"] != "provider_processing" || status.(map[string]any)["provider_request_id"] != "request-1" {
		t.Fatalf("async status=%#v", status)
	}
	call := findExecuteCall(t, platform, "data_manager_audience_members_ingest")
	if call.Input["termsOfService"] == nil || call.Input["encoding"] != "HEX" {
		t.Fatalf("Google Data Manager payload=%#v", call.Input)
	}
	destination := call.Input["destinations"].([]map[string]any)[0]
	login := destination["loginAccount"].(map[string]any)
	if login["accountType"] != "GOOGLE_ADS" || login["accountId"] != "9999999999" {
		t.Fatalf("Google manager destination=%#v", destination)
	}
}

func TestXAudienceBatchUsesRootOperationShape(t *testing.T) {
	members := []map[string]string{{"email": strings.Repeat("a", 64)}}
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "x", "abc")
	acct := &adAccount{ID: accountID, ProjectID: "test-proj", Platform: "x", NativeAccountID: "abc", ConnectionID: 7}
	job := &audienceJob{NativeAudienceID: "aud", Operation: "remove"}
	if _, errOut := app.sendAudienceBatch(ctx, acct, job, members); errOut != nil {
		t.Fatalf("send failed: %#v", errOut)
	}
	call := findExecuteCall(t, platform, "remove_custom_audience_users")
	operations := call.Input["users"].([]map[string]any)
	if operations[0]["operation_type"] != "Delete" {
		t.Fatalf("X operation=%#v", operations)
	}
}

func TestRedditAudienceBatchUsesColumnOrderShape(t *testing.T) {
	members := []map[string]string{{"email": strings.Repeat("a", 64), "mobile_id": strings.Repeat("b", 64)}}
	platform := newRecordingPlatform()
	ctx := newAdsCtx(t, platform)
	app := &App{}
	accountID := seedResourceTestAccount(t, ctx, "reddit", "abc")
	acct := &adAccount{ID: accountID, ProjectID: "test-proj", Platform: "reddit", NativeAccountID: "abc", ConnectionID: 7}
	job := &audienceJob{NativeAudienceID: "aud", Operation: "add"}
	if _, errOut := app.sendAudienceBatch(ctx, acct, job, members); errOut != nil {
		t.Fatalf("send failed: %#v", errOut)
	}
	call := findExecuteCall(t, platform, "update_custom_audience_users")
	data := call.Input["data"].(map[string]any)
	columns := data["column_order"].([]string)
	rows := data["user_data"].([][]string)
	if data["action_type"] != "ADD" || len(columns) != 2 || columns[0] != "EMAIL_SHA256" || columns[1] != "MAID_SHA256" {
		t.Fatalf("Reddit member contract=%#v", data)
	}
	if len(rows) != 1 || len(rows[0]) != 2 || rows[0][0] != members[0]["email"] || rows[0][1] != members[0]["mobile_id"] {
		t.Fatalf("Reddit rows=%#v", rows)
	}
}

func TestAudienceMembersAreFilteredPerProvider(t *testing.T) {
	members := []map[string]string{{"phone": "phone"}, {"email": "email", "phone": "phone"}}
	filtered, rejected := filterAudienceMembers("reddit", members)
	if rejected != 1 || len(filtered) != 1 || filtered[0]["email"] != "email" || filtered[0]["phone"] != "" {
		t.Fatalf("filtered=%#v rejected=%d", filtered, rejected)
	}
}
