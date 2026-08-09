package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestStorePreflightReportsObservedAppleVersionMismatch(t *testing.T) {
	doc := completeIOSStoreDocument()
	doc.VersionName = "0.1.0"
	cfg := &MobileStoreConfig{
		DesiredHash:  "desired",
		ObservedJSON: `{"version_mismatch":{"deploy_version":"0.1.0","apple_version":"1.0","apple_version_id":"version-1"}}`,
	}
	preflight := validateStoreDocument(t.TempDir(), &Deployment{TargetKind: "ios"}, nil, cfg, doc, true)
	if !hasStoreFinding(preflight, "version.mismatch") {
		t.Fatalf("missing version.mismatch: %#v", preflight.Findings)
	}
}

func TestVersionMismatchBlocksOnlyDependentScopes(t *testing.T) {
	finding := StoreFinding{Code: "version.mismatch", Severity: "error", Scope: "version"}
	for _, scope := range []string{"version", "localizations", "media", "review"} {
		if !findingBlocksStoreScope(finding, scope) {
			t.Fatalf("version mismatch should block %s", scope)
		}
	}
	for _, scope := range []string{"classification", "privacy", "distribution"} {
		if findingBlocksStoreScope(finding, scope) {
			t.Fatalf("version mismatch should not block %s", scope)
		}
	}
}

func TestManualPrivacyDoesNotBlockIndependentStoreScopes(t *testing.T) {
	finding := StoreFinding{Code: "privacy.apple_manual", Severity: "error", Scope: "compliance"}
	for _, scope := range []string{"version", "localizations", "media", "review", "classification", "privacy", "distribution"} {
		if findingBlocksStoreScope(finding, scope) {
			t.Fatalf("privacy finding should not block %s", scope)
		}
	}
	if !findingBlocksStoreScope(finding, "compliance") {
		t.Fatal("privacy attestation did not block compliance")
	}
}

func TestFirstZeroApplePricePoint(t *testing.T) {
	raw := json.RawMessage(`{"data":[
		{"id":"paid","attributes":{"customerPrice":"0.99"}},
		{"id":"free","attributes":{"customerPrice":"0.0"}}
	]}`)
	if got := firstZeroApplePricePoint(raw); got != "free" {
		t.Fatalf("zero price point=%q", got)
	}
}

func TestAppleFreePriceUsesJSONAPIInlineEntityID(t *testing.T) {
	body := appleFreePriceScheduleBody("app-1", "USA", "point-1")
	data := body["data"].(map[string]any)
	relationships := data["relationships"].(map[string]any)
	manualPrices := relationships["manualPrices"].(map[string]any)["data"].([]any)
	linkageID := manualPrices[0].(map[string]any)["id"]
	includedID := body["included"].([]any)[0].(map[string]any)["id"]
	if linkageID != "${deploy-free-usa}" || includedID != linkageID {
		t.Fatalf("inline IDs linkage=%q included=%q", linkageID, includedID)
	}
}

func TestIndependentStoreOperationsContinueAfterFailure(t *testing.T) {
	pricingErr := errors.New("pricing failed")
	availabilityCalled := false
	err := applyIndependentStoreOperations(
		func() error { return pricingErr },
		func() error { availabilityCalled = true; return nil },
	)
	if !errors.Is(err, pricingErr) {
		t.Fatalf("combined error=%v", err)
	}
	if !availabilityCalled {
		t.Fatal("availability did not run after pricing failed")
	}
}

func TestAppleAvailabilityCreateBodyUsesGenericTerritorySelection(t *testing.T) {
	body := appleAvailabilityCreateBody("app-1", map[string]bool{"USA": true, "CHN": false}, true)
	data := body["data"].(map[string]any)
	if data["type"] != "appAvailabilities" {
		t.Fatalf("data=%#v", data)
	}
	included := body["included"].([]any)
	if len(included) != 2 {
		t.Fatalf("included=%#v", included)
	}
	first := included[0].(map[string]any)
	second := included[1].(map[string]any)
	if first["id"] != "${deploy-territory-chn}" || second["id"] != "${deploy-territory-usa}" {
		t.Fatalf("territories are not stable/sorted: %#v", included)
	}
	linkages := data["relationships"].(map[string]any)["territoryAvailabilities"].(map[string]any)["data"].([]any)
	if linkages[0].(map[string]any)["id"] != first["id"] || linkages[1].(map[string]any)["id"] != second["id"] {
		t.Fatalf("territory linkage IDs do not match included IDs: %#v", linkages)
	}
}

func TestApplePricingVerificationFollowsManualPricePointRelationship(t *testing.T) {
	manualPrices := json.RawMessage(`{"data":[
		{"type":"appPrices","id":"manual-1","relationships":{"appPricePoint":{"data":{"type":"appPricePoints","id":"selected"}}}}
	],"included":[
		{"type":"appPricePoints","id":"unrelated","attributes":{"customerPrice":"0.0"}},
		{"type":"appPricePoints","id":"selected","attributes":{"customerPrice":"1.99"}}
	]}`)
	ids := appleManualPricePointIDs(manualPrices)
	if !ids["selected"] || appleReferencedPricePointIsFree(manualPrices, ids) {
		t.Fatalf("manual price relationship was not followed: ids=%#v", ids)
	}
	points := json.RawMessage(`{"data":[{"type":"appPricePoints","id":"selected","attributes":{"customerPrice":0.0}}]}`)
	if !appleReferencedPricePointIsFree(points, ids) {
		t.Fatal("referenced numeric zero price was not verified")
	}
	for _, value := range []json.RawMessage{json.RawMessage(`"0.0"`), json.RawMessage(`0.0`)} {
		if !applePriceValueIsZero(value) {
			t.Fatalf("zero price %s was not accepted", value)
		}
	}
}

func TestScreenshotOrderDiffDetectsReorderAndRemoval(t *testing.T) {
	existing := []appleScreenshotResource{{ID: "1", Filename: "a.png"}, {ID: "2", Filename: "b.png"}}
	if !sameAppleScreenshotOrder(existing, []string{"a.png", "b.png"}) {
		t.Fatal("matching screenshot order was rejected")
	}
	if sameAppleScreenshotOrder(existing, []string{"b.png", "a.png"}) {
		t.Fatal("reordered screenshots were accepted")
	}
	if sameAppleScreenshotOrder(existing, []string{"a.png"}) {
		t.Fatal("removed screenshot was accepted")
	}
}

func TestIncompleteAppleScreenshotReservationIsReplaced(t *testing.T) {
	desired := []string{"a.png"}
	if appleScreenshotsCompleteInOrder([]appleScreenshotResource{{Filename: "a.png", State: "AWAITING_UPLOAD"}}, desired) {
		t.Fatal("incomplete reservation was treated as uploaded")
	}
	if !appleScreenshotsCompleteInOrder([]appleScreenshotResource{{Filename: "a.png", State: "COMPLETE"}}, desired) {
		t.Fatal("complete screenshot was rejected")
	}
}

func TestAppleScreenshotSetSelectionRequiresExactDisplayType(t *testing.T) {
	raw := json.RawMessage(`{"data":[
		{"id":"ipad","attributes":{"screenshotDisplayType":"APP_IPAD_PRO_13"}},
		{"id":"iphone","attributes":{"screenshotDisplayType":"APP_IPHONE_69"}}
	]}`)
	if got := appleScreenshotSetID(raw, "APP_IPHONE_69"); got != "iphone" {
		t.Fatalf("selected set=%q", got)
	}
	if got := appleScreenshotSetID(raw, "APP_IPHONE_67"); got != "" {
		t.Fatalf("unexpected fallback set=%q", got)
	}
}

func TestSyncPreservesLastApplyObservation(t *testing.T) {
	observed := map[string]any{"readiness": map[string]any{"media": readinessCheck(true, "provider", "ok")}}
	preserveStoreObservationState(observed, `{"last_apply":{"status":"partial"},"applied_at":"then","desired_hash":"abc","stale":"drop"}`)
	if _, ok := observed["last_apply"]; !ok {
		t.Fatal("last_apply was not preserved")
	}
	if observed["applied_at"] != "then" || observed["desired_hash"] != "abc" {
		t.Fatalf("preserved state=%#v", observed)
	}
	if _, ok := observed["stale"]; ok {
		t.Fatalf("stale provider state was retained: %#v", observed)
	}
}

func TestEditableAppleAppInfoPreferredForMetadata(t *testing.T) {
	raw := json.RawMessage(`{"data":[
		{"id":"live","attributes":{"state":"READY_FOR_SALE"}},
		{"id":"editable","attributes":{"appStoreState":"PREPARE_FOR_SUBMISSION"}}
	]}`)
	if got := editableAppleAppInfoID(raw); got != "editable" {
		t.Fatalf("app info=%q", got)
	}
}

func TestAppleAppInfoCategoriesMustMatchEditableResource(t *testing.T) {
	raw := json.RawMessage(`{"data":[
		{"id":"live","relationships":{"primaryCategory":{"data":{"id":"GAMES"}}}},
		{"id":"editable","relationships":{"primaryCategory":{"data":{"id":"PRODUCTIVITY"}},"secondaryCategory":{"data":{"id":"UTILITIES"}}}}
	]}`)
	desired := StoreClassification{PrimaryCategory: "PRODUCTIVITY", SecondaryCategory: "UTILITIES"}
	if !appleAppInfoCategoriesMatch(raw, "editable", desired) {
		t.Fatal("matching editable categories were rejected")
	}
	desired.PrimaryCategory = "BUSINESS"
	if appleAppInfoCategoriesMatch(raw, "editable", desired) {
		t.Fatal("category mismatch was accepted")
	}
}

func TestAppleAppInfoLocalizationIncludesSubtitleAndPrivacy(t *testing.T) {
	raw := json.RawMessage(`{"data":[{"attributes":{"name":"Apteva","subtitle":"Autonomous agents, anywhere","privacyPolicyUrl":"https://example.com/privacy","privacyChoicesUrl":"https://example.com/choices"}}]}`)
	loc := StoreLocalization{Title: "Apteva", Subtitle: "Autonomous agents, anywhere"}
	privacy := StorePrivacy{PolicyURL: "https://example.com/privacy", ChoicesURL: "https://example.com/choices"}
	if !appleAppInfoLocalizationMatches(raw, loc, privacy) {
		t.Fatal("matching app info localization was rejected")
	}
	loc.Subtitle = "wrong"
	if appleAppInfoLocalizationMatches(raw, loc, privacy) {
		t.Fatal("subtitle mismatch was accepted")
	}
}

func TestProviderReadinessAddsReleaseCriticalFindings(t *testing.T) {
	preflight := StorePreflight{Ready: true}
	cfg := &MobileStoreConfig{ObservedJSON: `{"readiness":{"listing":{"status":"verified"},"review":{"status":"verified"}}}`}
	appendProviderReadinessFindings(&preflight, &Deployment{TargetKind: "ios"}, cfg)
	for _, code := range []string{"provider.media_unverified", "provider.classification_unverified", "provider.pricing_unverified", "provider.availability_unverified"} {
		if !hasStoreFinding(preflight, code) {
			t.Fatalf("missing %s: %#v", code, preflight.Findings)
		}
	}
	if preflight.Ready {
		t.Fatal("unverified provider state was considered ready")
	}
}

func TestAppleVersionLocalizationOmitsEmptyWhatsNew(t *testing.T) {
	input := appleVersionLocalizationInput(StoreLocalization{Description: "Description"}, true)
	if _, ok := input["whatsNew"]; ok {
		t.Fatalf("empty first-release whatsNew was sent: %#v", input)
	}
	input = appleVersionLocalizationInput(StoreLocalization{WhatsNew: "Changes"}, true)
	if input["whatsNew"] != "Changes" {
		t.Fatalf("subsequent whatsNew missing: %#v", input)
	}
}

func TestStoreObservationErrorPreservesProviderStatus(t *testing.T) {
	err := &integrationToolError{Slug: "app-store-connect", Tool: "get", Status: 404, Data: json.RawMessage(`{"errors":[{"code":"RESOURCE_NOT_FOUND"}]}`)}
	got := storeObservationError(err, true, "create")
	want := map[string]any{"message": err.Error(), "recoverable": true, "action": "create", "status": 404, "code": "RESOURCE_NOT_FOUND"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("error=%#v, want %#v", got, want)
	}
	if integrationErrorStatus(errors.New("plain")) != 0 {
		t.Fatal("plain error unexpectedly had provider status")
	}
}
