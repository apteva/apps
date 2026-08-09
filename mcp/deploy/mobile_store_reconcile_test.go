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
	if first["id"] != "deploy-territory-chn" || second["id"] != "deploy-territory-usa" {
		t.Fatalf("territories are not stable/sorted: %#v", included)
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
