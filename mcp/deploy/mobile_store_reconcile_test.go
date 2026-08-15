package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type pagedApplePlatform struct {
	tk.BasePlatformClient
	calls []integrationCall
}

type availabilityApplePlatform struct {
	tk.BasePlatformClient
	calls          []integrationCall
	usaAvailable   bool
	availableInNew bool
}

type appleSettingsPlatform struct {
	tk.BasePlatformClient
	calls []integrationCall
}

type googleMediaPlatform struct {
	tk.BasePlatformClient
	calls           []integrationCall
	mu              sync.Mutex
	uploaded        map[string]bool
	details         map[string]any
	listings        map[string]map[string]any
	dataSafety      bool
	requireCombined bool
	failTool        string
}

func (p *googleMediaPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"play_store": int64(88)}}, nil
}

func (p *googleMediaPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "google-play-developer", Status: "active"}, nil
}

func (p *googleMediaPlatform) GetConnectionCredentials(id int64) (*sdk.ConnectionCredentials, error) {
	return &sdk.ConnectionCredentials{ConnectionID: id, Slug: "google-play-developer", Fields: map[string]string{"token": "play-token"}}, nil
}

func (p *googleMediaPlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.mu.Lock()
	p.calls = append(p.calls, integrationCall{Tool: tool, Input: input})
	p.mu.Unlock()
	if tool == p.failTool {
		return nil, fmt.Errorf("forced %s failure", tool)
	}
	data := json.RawMessage(`{}`)
	switch tool {
	case "create_edit":
		data = json.RawMessage(`{"id":"edit-1"}`)
	case "update_app_details":
		p.mu.Lock()
		p.details = cloneAnyMap(input)
		p.mu.Unlock()
	case "update_store_listing":
		p.mu.Lock()
		if p.listings == nil {
			p.listings = map[string]map[string]any{}
		}
		p.listings[mapStringValue(input, "language")] = cloneAnyMap(input)
		p.mu.Unlock()
	case "get_app_details":
		p.mu.Lock()
		data, _ = json.Marshal(p.details)
		p.mu.Unlock()
	case "list_store_listings":
		p.mu.Lock()
		values := make([]map[string]any, 0, len(p.listings))
		for _, listing := range p.listings {
			values = append(values, listing)
		}
		data, _ = json.Marshal(map[string]any{"listings": values})
		p.mu.Unlock()
	case "list_listing_images":
		imageType := mapStringValue(input, "imageType")
		p.mu.Lock()
		uploaded := p.uploaded[imageType]
		p.mu.Unlock()
		if uploaded {
			data = json.RawMessage(`{"images":[{"id":"image-1","sha256":"unknown"}]}`)
		} else {
			data = json.RawMessage(`{"images":[]}`)
		}
	case "delete_all_listing_images":
		p.mu.Lock()
		p.uploaded[mapStringValue(input, "imageType")] = false
		p.mu.Unlock()
	case "validate_edit":
		p.mu.Lock()
		valid := !p.requireCombined || mapStringValue(p.details, "contactEmail") != "" && len(p.listings) > 0
		for _, listing := range p.listings {
			valid = valid && mapStringValue(listing, "shortDescription") != "" && mapStringValue(listing, "fullDescription") != ""
		}
		p.mu.Unlock()
		if !valid {
			return nil, errors.New("Play edit requires listing descriptions and contact email together")
		}
	case "update_data_safety":
		p.mu.Lock()
		p.dataSafety = true
		p.mu.Unlock()
	}
	return &sdk.ExecuteResult{Success: true, Status: http.StatusOK, Data: data}, nil
}

type appleMediaPlatform struct {
	tk.BasePlatformClient
	calls       []integrationCall
	phoneName   string
	readIPadSet bool
}

func (p *appleMediaPlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calls = append(p.calls, integrationCall{Tool: tool, Input: input})
	data := json.RawMessage(`{}`)
	switch tool {
	case "list_screenshot_sets":
		data = json.RawMessage(`{"data":[{"id":"iphone-set","attributes":{"screenshotDisplayType":"APP_IPHONE_69"}},{"id":"ipad-set","attributes":{"screenshotDisplayType":"APP_IPAD_PRO_13"}}]}`)
	case "list_screenshots":
		if input["set_id"] == "ipad-set" {
			p.readIPadSet = true
		}
		data, _ = json.Marshal(map[string]any{"data": []map[string]any{{
			"id": "shot-1", "attributes": map[string]any{
				"fileName": p.phoneName, "assetDeliveryState": map[string]any{"state": "COMPLETE"},
			},
		}}})
	}
	return &sdk.ExecuteResult{Success: true, Status: http.StatusOK, Data: data}, nil
}

func (p *appleSettingsPlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calls = append(p.calls, integrationCall{Tool: tool, Input: input})
	var data json.RawMessage
	switch tool {
	case "list_app_versions":
		data = json.RawMessage(`{"data":[{"id":"version-1","attributes":{"versionString":"1.0","appStoreState":"PREPARE_FOR_SUBMISSION","copyright":"old"}}]}`)
	case "update_app_version":
		data, _ = json.Marshal(map[string]any{"data": map[string]any{
			"id": "version-1", "attributes": map[string]any{"copyright": input["copyright"]},
		}})
	case "update_app":
		data, _ = json.Marshal(map[string]any{"data": map[string]any{
			"id": "app-1", "attributes": map[string]any{"contentRightsDeclaration": input["contentRightsDeclaration"]},
		}})
	default:
		data = json.RawMessage(`{"data":[]}`)
	}
	return &sdk.ExecuteResult{Success: true, Status: http.StatusOK, Data: data}, nil
}

func (p *availabilityApplePlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calls = append(p.calls, integrationCall{Tool: tool, Input: input})
	var data json.RawMessage
	switch tool {
	case "list_territories":
		data = json.RawMessage(`{"data":[{"id":"USA"},{"id":"CHN"}],"links":{"next":null}}`)
	case "get_app_availability":
		data, _ = json.Marshal(map[string]any{"data": map[string]any{
			"type": "appAvailabilities", "id": "availability-1",
			"attributes": map[string]any{"availableInNewTerritories": p.availableInNew},
		}})
	case "list_app_availability_territories":
		data, _ = json.Marshal(map[string]any{
			"data": []any{
				map[string]any{"type": "territoryAvailabilities", "id": "usa-1", "attributes": map[string]any{"available": p.usaAvailable}, "relationships": map[string]any{"territory": map[string]any{"data": map[string]any{"id": "USA"}}}},
				map[string]any{"type": "territoryAvailabilities", "id": "chn-1", "attributes": map[string]any{"available": false}, "relationships": map[string]any{"territory": map[string]any{"data": map[string]any{"id": "CHN"}}}},
			},
			"links": map[string]any{"next": nil},
		})
	case "update_territory_availability":
		data = json.RawMessage(`{"data":{"id":"updated"}}`)
	default:
		data = json.RawMessage(`{"data":[]}`)
	}
	return &sdk.ExecuteResult{Success: true, Status: http.StatusOK, Data: data}, nil
}

func (p *pagedApplePlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calls = append(p.calls, integrationCall{Tool: tool, Input: input})
	var data json.RawMessage
	switch tool {
	case "list_app_availability_territories":
		if input["cursor"] == "page-2" {
			data = json.RawMessage(`{"data":[{"attributes":{"available":false},"relationships":{"territory":{"data":{"id":"CHN"}}}}],"links":{"next":null}}`)
		} else {
			data = json.RawMessage(`{"data":[{"attributes":{"available":true},"relationships":{"territory":{"data":{"id":"USA"}}}}],"links":{"next":"https://api.appstoreconnect.apple.com/v2/appAvailabilities/availability-1/territoryAvailabilities?cursor=page-2&limit=50"}}`)
		}
	case "list_app_schedule_manual_prices":
		if input["cursor"] == "price-2" {
			data = json.RawMessage(`{"data":[{"type":"appPrices","relationships":{"appPricePoint":{"data":{"type":"appPricePoints","id":"free-point"}}}}],"included":[{"type":"appPricePoints","id":"free-point","attributes":{"customerPrice":"0.0"}}],"links":{"next":null}}`)
		} else {
			data = json.RawMessage(`{"data":[{"type":"appPrices","relationships":{"appPricePoint":{"data":{"type":"appPricePoints","id":"paid-point"}}}}],"included":[{"type":"appPricePoints","id":"paid-point","attributes":{"customerPrice":"1.99"}}],"links":{"next":"https://api.appstoreconnect.apple.com/v1/appPriceSchedules/schedule-1/manualPrices?cursor=price-2"}}`)
		}
	default:
		data = json.RawMessage(`{"data":[],"links":{"next":null}}`)
	}
	return &sdk.ExecuteResult{Success: true, Status: http.StatusOK, Data: data}, nil
}

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

func TestProviderReadinessFindingDoesNotBlockItsApplyScope(t *testing.T) {
	finding := StoreFinding{
		Code: "provider.content_rights_unverified", Severity: "error", Scope: "compliance", Automatable: true,
	}
	if findingBlocksStoreScope(finding, "compliance") {
		t.Fatal("provider readiness prevented the apply operation that resolves it")
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

func TestAndroidPartialMediaApplyCommitsValidIconIndependently(t *testing.T) {
	platform := &googleMediaPlatform{uploaded: map[string]bool{}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/icon") {
			t.Errorf("unexpected upload path %s", r.URL.Path)
		}
		platform.mu.Lock()
		platform.uploaded["icon"] = true
		platform.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"image-1"}`))
	}))
	defer server.Close()
	oldUploadURL := googlePlayUploadBaseURL
	googlePlayUploadBaseURL = server.URL
	t.Cleanup(func() { googlePlayUploadBaseURL = oldUploadURL })

	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "android-media", TargetKind: "android", SourceKind: "local", SourceRef: "/src", Framework: "android",
		TargetConfigJSON: `{"package_name":"com.example.media"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	if err != nil {
		t.Fatal(err)
	}
	d = effectiveDeploymentForEnvironment(d, env)
	root := t.TempDir()
	doc := defaultStoreDocument("android")
	doc.VersionName = "1.0"
	doc.Localizations["en-US"] = StoreLocalization{Title: "Test", ShortDescription: "Short", Description: "Description"}
	doc.Assets = []StoreAsset{{
		ID: "icon", Kind: "icon", Locale: "en-US",
		Path: writeTestStorePNG(t, root, d, "icon", 512, 512, true),
	}}
	if _, err := dbUpsertMobileStoreConfig(ctx.AppDB(), d, doc); err != nil {
		t.Fatal(err)
	}

	result, err := (&App{dataDir: root}).applyStoreConfigScoped(d, nil, true, StoreApplyRequest{
		Scopes: []string{"media"}, AllowPartial: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.AppliedAssets, []string{"icon"}) || !containsString(result.AppliedScopes, "media") {
		t.Fatalf("result=%+v", result)
	}
	blocked := map[string]bool{}
	for _, issue := range result.Blocked {
		blocked[issue.MediaKind] = true
	}
	if !blocked["feature_graphic"] || !blocked["phone_screenshot"] {
		t.Fatalf("blocked=%+v", result.Blocked)
	}
	platform.mu.Lock()
	calls := append([]integrationCall(nil), platform.calls...)
	platform.mu.Unlock()
	for _, call := range calls {
		if call.Tool == "delete_all_listing_images" && call.Input["imageType"] != "icon" {
			t.Fatalf("unselected media was deleted: %+v", call)
		}
	}
	if countIntegrationCalls(calls, "commit_edit") != 1 {
		t.Fatalf("calls=%+v", calls)
	}

	explicit, err := (&App{dataDir: root}).applyStoreConfigScoped(d, nil, true, StoreApplyRequest{
		MediaKinds: []string{"icon"}, AllowPartial: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(explicit.AppliedAssets, []string{"icon"}) || len(explicit.Blocked) != 0 {
		t.Fatalf("explicit result=%+v", explicit)
	}
}

func TestAndroidMetadataScopesShareOneValidatedEdit(t *testing.T) {
	platform := &googleMediaPlatform{uploaded: map[string]bool{}, requireCombined: true}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })

	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "android-listing", TargetKind: "android", SourceKind: "local", SourceRef: "/src", Framework: "android",
		TargetConfigJSON: `{"package_name":"com.example.listing"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	if err != nil {
		t.Fatal(err)
	}
	d = effectiveDeploymentForEnvironment(d, env)
	doc := defaultStoreDocument("android")
	doc.VersionName = "1.0"
	doc.Localizations["en-US"] = StoreLocalization{
		Title: "Example", ShortDescription: "Short description", Description: "Full description", VideoURL: "https://youtu.be/example",
	}
	doc.Review = StoreReview{Email: "support@example.com", Phone: "+12025550123", AccessConfirmed: true}
	doc.Privacy.PolicyURL = "https://example.com/privacy"
	doc.Privacy.DataSafetyCSV = "Question,Answer\ncollects_data,false"
	if _, err := dbUpsertMobileStoreConfig(ctx.AppDB(), d, doc); err != nil {
		t.Fatal(err)
	}

	result, err := (&App{dataDir: t.TempDir()}).applyStoreConfigScoped(d, nil, true, StoreApplyRequest{
		Scopes: []string{"localizations", "review", "privacy"}, AllowPartial: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.AppliedScopes, []string{"localizations", "review", "privacy"}) || len(result.Failed) != 0 {
		t.Fatalf("result=%+v", result)
	}
	if result.ProviderValidations["google_data_safety"].Status != "accepted" {
		t.Fatalf("provider validations=%+v", result.ProviderValidations)
	}
	platform.mu.Lock()
	calls := append([]integrationCall(nil), platform.calls...)
	details := cloneAnyMap(platform.details)
	dataSafety := platform.dataSafety
	platform.mu.Unlock()
	if countIntegrationCalls(calls, "create_edit") != 3 || countIntegrationCalls(calls, "validate_edit") != 1 || countIntegrationCalls(calls, "commit_edit") != 1 {
		// Read-only observations run before and after the single transactional edit.
		t.Fatalf("calls=%+v", calls)
	}
	if mapStringValue(details, "defaultLanguage") != "en-US" || mapStringValue(details, "contactEmail") != "support@example.com" ||
		mapStringValue(details, "contactWebsite") != "https://example.com/privacy" || !dataSafety {
		t.Fatalf("details=%+v dataSafety=%v", details, dataSafety)
	}
}

func TestGoogleObservationParsesNativeProviderShapes(t *testing.T) {
	doc := defaultStoreDocument("android")
	doc.Localizations["en-US"] = StoreLocalization{
		Title: "Apteva", ShortDescription: "Agents anywhere", Description: "Full description", VideoURL: "https://youtu.be/example",
	}
	doc.Review = StoreReview{Email: "support@example.com", Phone: "+12025550123"}
	doc.Privacy.PolicyURL = "https://example.com/privacy"
	doc.Distribution.Availability = StoreAvailability{Mode: "only", IncludedTerritories: []string{"ES", "US"}}
	doc.Assets = []StoreAsset{{ID: "icon", Kind: "icon", Locale: "en-US", SHA256: "AA:BB"}}

	details := json.RawMessage(`{"defaultLanguage":"en-US","contactWebsite":"https://example.com/privacy","contactEmail":"support@example.com","contactPhone":"+12025550123"}`)
	listings := json.RawMessage(`{"kind":"androidpublisher#listingsListResponse","listings":[{"language":"en-US","title":"Apteva","shortDescription":"Agents anywhere","fullDescription":"Full description","video":"https://youtu.be/example"}]}`)
	images := map[string]any{"images": []any{map[string]any{"id": "image-1", "sha256": "aabb"}}}
	availability := json.RawMessage(`{"syncWithProduction":false,"countries":[{"countryCode":"US"},{"countryCode":"ES"}],"restOfWorld":false}`)

	if !googleAppDetailsMatch(details, doc) || !googleListingsMatch(listings, doc) ||
		!googleImagesMatch(images, doc.Assets) || !googleAvailabilityMatches(availability, doc.Distribution) {
		t.Fatal("native Google provider responses did not verify")
	}
	doc.Localizations["en-US"] = StoreLocalization{Title: "Apteva", ShortDescription: "Changed", Description: "Full description"}
	if googleListingsMatch(listings, doc) {
		t.Fatal("listing mismatch was accepted")
	}
	doc.Assets[0].SHA256 = "different"
	if googleImagesMatch(images, doc.Assets) {
		t.Fatal("media hash mismatch was accepted")
	}
}

func TestGoogleAvailabilityAllowsExplicitManualConfirmation(t *testing.T) {
	doc := defaultStoreDocument("android")
	doc.Distribution.Availability = StoreAvailability{Mode: "all_except", ExcludedTerritories: []string{"CN"}}
	doc.Distribution.Provider = map[string]any{"availability_configured": true}

	platform := &googleMediaPlatform{uploaded: map[string]bool{}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	d := &Deployment{TargetKind: "android", TargetConfigJSON: `{"package_name":"com.example.availability"}`}

	observed, err := (&App{}).observeGoogleStoreConfig(d, doc, &MobileStoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	readiness, _ := observed["readiness"].(map[string]any)
	availability, _ := readiness["availability"].(map[string]any)
	if mapStringValue(availability, "status") != "verified" || mapStringValue(availability, "source") != "manual" {
		t.Fatalf("availability readiness=%+v", availability)
	}
}

func TestAndroidRejectsIncompleteAvailabilitySelection(t *testing.T) {
	doc := defaultStoreDocument("android")
	doc.VersionName = "1.0"
	doc.Localizations["en-US"] = StoreLocalization{Title: "Example", ShortDescription: "Short", Description: "Full"}
	doc.Distribution.Availability = StoreAvailability{Mode: "only"}
	preflight := validateStoreDocument(t.TempDir(), &Deployment{TargetKind: "android"}, nil, &MobileStoreConfig{}, doc, true)
	if !hasStoreFinding(preflight, "availability.invalid") {
		t.Fatalf("missing availability validation: %+v", preflight.Findings)
	}
}

func TestAndroidDataSafetyFailurePreservesCommittedMetadataResults(t *testing.T) {
	platform := &googleMediaPlatform{uploaded: map[string]bool{}, requireCombined: true, failTool: "update_data_safety"}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })

	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "android-data-safety", TargetKind: "android", SourceKind: "local", SourceRef: "/src", Framework: "android",
		TargetConfigJSON: `{"package_name":"com.example.safety"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	d = effectiveDeploymentForEnvironment(d, env)
	doc := defaultStoreDocument("android")
	doc.VersionName = "1.0"
	doc.Localizations["en-US"] = StoreLocalization{Title: "Example", ShortDescription: "Short", Description: "Full"}
	doc.Review = StoreReview{Email: "support@example.com", AccessConfirmed: true}
	doc.Privacy.PolicyURL = "https://example.com/privacy"
	doc.Privacy.DataSafetyCSV = "Question,Answer\ncollects_data,false"
	if _, err := dbUpsertMobileStoreConfig(ctx.AppDB(), d, doc); err != nil {
		t.Fatal(err)
	}

	result, err := (&App{dataDir: t.TempDir()}).applyStoreConfigScoped(d, nil, true, StoreApplyRequest{
		Scopes: []string{"localizations", "review", "privacy"}, AllowPartial: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.AppliedScopes, []string{"localizations", "review"}) || len(result.Failed) != 1 || result.Failed[0].Scope != "privacy" {
		t.Fatalf("result=%+v", result)
	}
	if countIntegrationCalls(platform.calls, "commit_edit") != 1 {
		t.Fatalf("metadata edit was not committed exactly once: %+v", platform.calls)
	}
}

func TestAndroidProviderReadOnlyScopeIsNotReportedApplied(t *testing.T) {
	platform := &googleMediaPlatform{uploaded: map[string]bool{}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "android-version", TargetKind: "android", SourceKind: "local", SourceRef: "/src", Framework: "android",
		TargetConfigJSON: `{"package_name":"com.example.version"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	d = effectiveDeploymentForEnvironment(d, env)
	doc := defaultStoreDocument("android")
	doc.VersionName = "1.0"
	if _, err := dbUpsertMobileStoreConfig(ctx.AppDB(), d, doc); err != nil {
		t.Fatal(err)
	}
	result, err := (&App{dataDir: t.TempDir()}).applyStoreConfigScoped(d, nil, true, StoreApplyRequest{Scopes: []string{"version"}, AllowPartial: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || len(result.AppliedScopes) != 0 || result.Status != "no_op" ||
		len(result.ScopeResults) != 1 || result.ScopeResults[0].Status != "verified" {
		t.Fatalf("result=%+v", result)
	}
	if countIntegrationCalls(platform.calls, "commit_edit") != 0 {
		t.Fatalf("read-only scope committed an edit: %+v", platform.calls)
	}
}

func TestAndroidReviewAccessRequiresExplicitConfirmation(t *testing.T) {
	doc := defaultStoreDocument("android")
	doc.VersionName = "1.0"
	doc.Localizations["en-US"] = StoreLocalization{Title: "Example", ShortDescription: "Short", Description: "Full"}
	doc.Review = StoreReview{Email: "support@example.com", DemoAccountRequired: true, DemoUsername: "reviewer"}
	preflight := validateStoreDocument(t.TempDir(), &Deployment{TargetKind: "android"}, nil, &MobileStoreConfig{}, doc, true)
	for _, code := range []string{"review_demo.required", "review_access.instructions_required", "review_access.confirmation_required"} {
		if !hasStoreFinding(preflight, code) {
			t.Fatalf("missing %s: %+v", code, preflight.Findings)
		}
	}
}

func TestAndroidReadinessRequiresProviderVerification(t *testing.T) {
	out := StorePreflight{Ready: true}
	appendProviderReadinessFindings(&out, &Deployment{TargetKind: "android"}, &MobileStoreConfig{ObservedJSON: `{}`})
	for _, code := range []string{
		"provider.google_listing_unverified", "provider.google_review_unverified", "provider.google_media_unverified",
		"provider.google_privacy_unverified", "provider.google_availability_unverified", "provider.google_pricing_unverified",
	} {
		if !hasStoreFinding(out, code) {
			t.Fatalf("missing %s: %+v", code, out.Findings)
		}
	}
	if out.Ready || out.Errors != 6 {
		t.Fatalf("preflight=%+v", out)
	}
}

func TestExplicitAndroidMediaKindIgnoresOtherMissingKinds(t *testing.T) {
	doc := StoreDocument{Assets: []StoreAsset{{ID: "icon", Kind: "icon"}}}
	preflight := StorePreflight{Findings: []StoreFinding{
		{Code: "feature_graphic.google_required", Severity: "error", Scope: "media", MediaKind: "feature_graphic"},
		{Code: "screenshots.google_minimum", Severity: "error", Scope: "media", MediaKind: "phone_screenshot"},
	}}
	kinds, err := normalizeMediaKinds("android", []string{"icon"})
	if err != nil || !kinds["icon"] {
		t.Fatalf("kinds=%v err=%v", kinds, err)
	}
	if issue := mediaKindBlockingIssue(preflight, "icon"); issue != nil {
		t.Fatalf("icon blocked by unrelated finding: %+v", issue)
	}
	if got := mediaApplyCandidates("android", doc, preflight); !reflect.DeepEqual(got, []string{"feature_graphic", "icon", "phone_screenshot"}) {
		t.Fatalf("candidates=%v", got)
	}
}

func TestIOSRejectsUnsupportedStoreMediaKind(t *testing.T) {
	kinds, err := normalizeMediaKinds("ios", []string{"icon"})
	if err == nil || len(kinds) != 0 || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("kinds=%v err=%v", kinds, err)
	}
}

func TestApplePartialScreenshotApplyDoesNotReadOrDeleteIPadSet(t *testing.T) {
	root := t.TempDir()
	d := &Deployment{ID: 9, EnvironmentID: 10, TargetKind: "ios"}
	asset := StoreAsset{
		ID: "phone", Kind: "phone_screenshot", Locale: "en-US", DisplayTarget: "APP_IPHONE_69", SHA256: "abc",
		Path: writeTestStorePNG(t, root, d, "phone", 1320, 2868, true),
	}
	path, err := resolveStoreAssetPath(root, d, asset.Path)
	if err != nil {
		t.Fatal(err)
	}
	platform := &appleMediaPlatform{phoneName: storeUploadFilename(path, asset.SHA256)}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })

	err = (&App{dataDir: root}).reconcileAppleScreenshots(
		&sdk.BoundIntegration{ConnectionID: 77, AppSlug: "app-store-connect"},
		d,
		map[string]string{"en-US": "localization-1"},
		StoreDocument{DefaultLocale: "en-US", Assets: []StoreAsset{asset}},
		mediaKindSet{"phone_screenshot": true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if platform.readIPadSet || countIntegrationCalls(platform.calls, "delete_screenshot") != 0 {
		t.Fatalf("unselected iPad set was touched: calls=%+v", platform.calls)
	}
}

func TestApplePrivacyIsDeferredToProviderCommit(t *testing.T) {
	root := t.TempDir()
	d := &Deployment{ID: 1, EnvironmentID: 2, TargetKind: "ios"}
	doc := completeIOSStoreDocument()
	doc.Privacy.ManualAttestations = map[string]bool{}
	doc.Assets[0].Path = writeTestStorePNG(t, root, d, "shot", 1290, 2796, true)

	preflight := validateStoreDocument(root, d, nil, &MobileStoreConfig{}, doc, true)
	if !preflight.Ready || preflight.Errors != 0 {
		t.Fatalf("provider-commit privacy validation blocked preflight: %#v", preflight)
	}
	var privacy *StoreFinding
	for i := range preflight.Findings {
		if preflight.Findings[i].Code == "privacy.apple_provider_validation" {
			privacy = &preflight.Findings[i]
			break
		}
	}
	if privacy == nil || privacy.Severity != "warning" || privacy.Verification != "provider_commit" || !privacy.Automatable {
		t.Fatalf("privacy finding=%#v", privacy)
	}
}

func TestIOSPreflightRequiresCopyrightAndConfirmedContentRights(t *testing.T) {
	doc := completeIOSStoreDocument()
	doc.Copyright = ""
	doc.ContentRights = StoreContentRights{}
	preflight := validateStoreDocument(t.TempDir(), &Deployment{TargetKind: "ios"}, nil, &MobileStoreConfig{}, doc, true)
	if !hasStoreFinding(preflight, "copyright.required") || !hasStoreFinding(preflight, "content_rights.required") {
		t.Fatalf("findings=%#v", preflight.Findings)
	}
}

func TestAppleContentRightsUsesCanonicalDeclaration(t *testing.T) {
	rights := StoreContentRights{UsesThirdPartyContent: true, RightsConfirmed: true}
	if got := appleContentRightsDeclaration(rights); got != "USES_THIRD_PARTY_CONTENT" {
		t.Fatalf("declaration=%q", got)
	}
	raw := json.RawMessage(`{"data":{"attributes":{"contentRightsDeclaration":"USES_THIRD_PARTY_CONTENT"}}}`)
	if !appleContentRightsMatches(raw, rights) {
		t.Fatal("matching Apple content-rights declaration was rejected")
	}
	rights.UsesThirdPartyContent = false
	if appleContentRightsMatches(raw, rights) {
		t.Fatal("mismatched Apple content-rights declaration was accepted")
	}
	providerRights, ok := appleContentRightsFromProvider(raw)
	if !ok || providerRights["uses_third_party_content"] != true || providerRights["rights_confirmed"] != true {
		t.Fatalf("provider rights=%#v valid=%t", providerRights, ok)
	}
	if !appleContentRightsVerified(raw, StoreContentRights{}) {
		t.Fatal("valid provider declaration did not satisfy an unconfirmed legacy desired document")
	}
	if appleContentRightsVerified(raw, StoreContentRights{RightsConfirmed: true}) {
		t.Fatal("provider declaration overrode a conflicting confirmed desired declaration")
	}
}

func TestProviderContentRightsSatisfyLegacyDesiredDocument(t *testing.T) {
	root := t.TempDir()
	d := &Deployment{ID: 1, EnvironmentID: 2, TargetKind: "ios"}
	doc := completeIOSStoreDocument()
	doc.ContentRights = StoreContentRights{}
	doc.Assets[0].Path = writeTestStorePNG(t, root, d, "shot", 1290, 2796, true)
	cfg := &MobileStoreConfig{ObservedJSON: `{"readiness":{"content_rights":{"status":"verified"}}}`}

	preflight := validateStoreDocument(root, d, nil, cfg, doc, true)
	if hasStoreFinding(preflight, "content_rights.required") {
		t.Fatalf("provider-confirmed rights remained blocked: %#v", preflight.Findings)
	}
}

func TestInvalidProviderContentRightsRemainUnverified(t *testing.T) {
	raw := json.RawMessage(`{"data":{"attributes":{"contentRightsDeclaration":""}}}`)
	providerRights, ok := appleContentRightsFromProvider(raw)
	if ok || providerRights["rights_confirmed"] != false {
		t.Fatalf("provider rights=%#v valid=%t", providerRights, ok)
	}
}

func TestExistingAppleVersionSettingsAreUpdatedAndVerified(t *testing.T) {
	platform := &appleSettingsPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	doc := completeIOSStoreDocument()
	doc.Copyright = "2026 Apteva"
	doc.ReleaseMode = "after_approval"

	versionID, err := ensureAppleStoreVersion(&sdk.BoundIntegration{ConnectionID: 77, AppSlug: "app-store-connect"}, "app-1", doc)
	if err != nil {
		t.Fatal(err)
	}
	if versionID != "version-1" || countIntegrationCalls(platform.calls, "update_app_version") != 1 {
		t.Fatalf("version=%q calls=%#v", versionID, platform.calls)
	}
	input := platform.calls[1].Input
	if input["copyright"] != "2026 Apteva" || input["releaseType"] != "AFTER_APPROVAL" {
		t.Fatalf("update input=%#v", input)
	}
}

func TestAppleContentRightsApplyWritesAndVerifiesApp(t *testing.T) {
	platform := &appleSettingsPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	doc := completeIOSStoreDocument()
	doc.ContentRights = StoreContentRights{UsesThirdPartyContent: true, RightsConfirmed: true}

	err := (&App{}).applyAppleMetadata(
		&sdk.BoundIntegration{ConnectionID: 77, AppSlug: "app-store-connect"},
		"app-1", doc, storeScopeSet{"compliance": true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(platform.calls) != 1 || platform.calls[0].Tool != "update_app" || platform.calls[0].Input["contentRightsDeclaration"] != "USES_THIRD_PARTY_CONTENT" {
		t.Fatalf("calls=%#v", platform.calls)
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

func TestAppleAvailabilityReconcileSkipsMatchingProviderState(t *testing.T) {
	platform := &availabilityApplePlatform{usaAvailable: true, availableInNew: false}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })

	distribution := StoreDistribution{Availability: StoreAvailability{
		Mode: "only", IncludedTerritories: []string{"USA"},
	}}
	if err := reconcileAppleAvailability(&sdk.BoundIntegration{ConnectionID: 77, AppSlug: "app-store-connect"}, "app-1", distribution); err != nil {
		t.Fatal(err)
	}
	if got := countIntegrationCalls(platform.calls, "update_app_availability"); got != 0 {
		t.Fatalf("parent availability updates=%d calls=%#v", got, platform.calls)
	}
	if got := countIntegrationCalls(platform.calls, "update_territory_availability"); got != 0 {
		t.Fatalf("matching territory updates=%d calls=%#v", got, platform.calls)
	}
}

func TestAppleAvailabilityReconcileUpdatesOnlyChangedTerritories(t *testing.T) {
	platform := &availabilityApplePlatform{usaAvailable: false, availableInNew: false}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })

	distribution := StoreDistribution{Availability: StoreAvailability{
		Mode: "only", IncludedTerritories: []string{"USA"},
	}}
	if err := reconcileAppleAvailability(&sdk.BoundIntegration{ConnectionID: 77, AppSlug: "app-store-connect"}, "app-1", distribution); err != nil {
		t.Fatal(err)
	}
	if got := countIntegrationCalls(platform.calls, "update_app_availability"); got != 0 {
		t.Fatalf("parent availability updates=%d calls=%#v", got, platform.calls)
	}
	if got := countIntegrationCalls(platform.calls, "update_territory_availability"); got != 1 {
		t.Fatalf("changed territory updates=%d calls=%#v", got, platform.calls)
	}
}

func TestAppleAvailabilityReconcileRejectsImmutableParentChange(t *testing.T) {
	platform := &availabilityApplePlatform{usaAvailable: true, availableInNew: true}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })

	distribution := StoreDistribution{Availability: StoreAvailability{
		Mode: "only", IncludedTerritories: []string{"USA"},
	}}
	err := reconcileAppleAvailability(&sdk.BoundIntegration{ConnectionID: 77, AppSlug: "app-store-connect"}, "app-1", distribution)
	if err == nil || !strings.Contains(err.Error(), "cannot be updated") {
		t.Fatalf("error=%v", err)
	}
	if got := countIntegrationCalls(platform.calls, "update_app_availability"); got != 0 {
		t.Fatalf("parent availability updates=%d calls=%#v", got, platform.calls)
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

func TestAppleAvailabilityPaginationReadsEveryTerritory(t *testing.T) {
	platform := &pagedApplePlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })

	pages, err := executeAppleCollectionPages(
		&sdk.BoundIntegration{ConnectionID: 77, AppSlug: "app-store-connect"},
		"list_app_availability_territories",
		map[string]any{"availability_id": "availability-1", "include": "territory", "limit": 200},
	)
	if err != nil {
		t.Fatal(err)
	}
	state := appleTerritoryAvailabilityStateFromPages(pages)
	if len(pages) != 2 || len(state) != 2 || !state["USA"] || state["CHN"] {
		t.Fatalf("pages=%d state=%#v", len(pages), state)
	}
	if got := platform.calls[1].Input["cursor"]; got != "page-2" {
		t.Fatalf("second-page cursor=%v", got)
	}
}

func TestAppleFreePricingUsesIncludedPricePointAcrossPages(t *testing.T) {
	platform := &pagedApplePlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })

	bound := &sdk.BoundIntegration{ConnectionID: 77, AppSlug: "app-store-connect"}
	if !applePriceScheduleIsFree(bound, json.RawMessage(`{"data":{"id":"schedule-1"}}`), "app-1", "USA") {
		t.Fatal("zero manual price on the second page was not verified")
	}
	if len(platform.calls) != 2 {
		t.Fatalf("calls=%#v", platform.calls)
	}
	first := platform.calls[0].Input
	if first["include"] != "appPricePoint,territory" || first["fields_app_price_points"] != "customerPrice" {
		t.Fatalf("manual price request=%#v", first)
	}
	if platform.calls[1].Input["cursor"] != "price-2" {
		t.Fatalf("second-page request=%#v", platform.calls[1].Input)
	}
}

func TestFirstJSONAPIIDAcceptsResourceAndCollectionDocuments(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"data":{"type":"appPriceSchedules","id":"schedule-1"}}`),
		json.RawMessage(`{"data":[{"type":"appPriceSchedules","id":"schedule-1"}]}`),
	} {
		if got := firstJSONAPIID(raw); got != "schedule-1" {
			t.Fatalf("id=%q for %s", got, raw)
		}
	}
}

func TestStorePreflightReleaseErrorReturnsStructured422(t *testing.T) {
	preflight := StorePreflight{
		Ready: false, Errors: 1,
		Findings: []StoreFinding{{Code: "privacy.apple_manual", Severity: "error", Scope: "compliance", Message: "Complete App Privacy in App Store Connect."}},
	}
	recorder := httptest.NewRecorder()
	httpStoreErr(recorder, newStorePreflightError(preflight), http.StatusInternalServerError)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Error     string         `json:"error"`
		Code      string         `json:"code"`
		Preflight StorePreflight `json:"preflight"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "store_preflight_failed" || body.Preflight.Ready || len(body.Preflight.Findings) != 1 {
		t.Fatalf("response=%#v", body)
	}
}

func TestProviderCommitValidationErrorReturnsStructured422(t *testing.T) {
	providerErr := &integrationToolError{
		Slug: "app-store-connect", Tool: "submit_review_submission", Status: http.StatusUnprocessableEntity,
		Data: json.RawMessage(`{"errors":[{"code":"ENTITY_ERROR.ATTRIBUTE.REQUIRED","detail":"App Privacy is incomplete"}]}`),
	}
	err := wrapProviderCommitValidationError("app_store_connect", "store_submission", "submit_review_submission", providerErr)
	var validationErr *providerValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error type=%T", err)
	}
	recorder := httptest.NewRecorder()
	httpStoreErr(recorder, err, http.StatusInternalServerError)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "provider_validation_failed" || body["provider"] != "app_store_connect" || body["requirement"] != "store_submission" {
		t.Fatalf("response=%#v", body)
	}

	operationalErr := &integrationToolError{Slug: "app-store-connect", Tool: "submit_review_submission", Status: http.StatusInternalServerError}
	if got := wrapProviderCommitValidationError("app_store_connect", "store_submission", "submit_review_submission", operationalErr); got != operationalErr {
		t.Fatalf("operational error was misclassified: %T %v", got, got)
	}
}

func TestProviderValidationPreservesAssociatedErrors(t *testing.T) {
	raw := json.RawMessage(`{"errors":[{"code":"STATE_ERROR.VALIDATION_ERROR","title":"Submission failed","detail":"Resolve the associated errors.","meta":{"associatedErrors":{"/v1/appStoreVersions/version-1":[{"code":"ENTITY_ERROR.ATTRIBUTE.REQUIRED","title":"Missing copyright","detail":"Copyright is required.","source":{"pointer":"/data/attributes/copyright"}},{"code":"ENTITY_ERROR.ATTRIBUTE.REQUIRED","title":"Missing content rights","detail":"Content rights are required.","source":{"pointer":"/data/attributes/contentRightsDeclaration"}}]}}}]}`)
	err := wrapProviderCommitValidationError("app_store_connect", "store_submission", "submit_review_submission", &integrationToolError{
		Slug: "app-store-connect", Tool: "submit_review_submission", Status: http.StatusUnprocessableEntity, Data: raw,
	})
	var validationErr *providerValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error=%T", err)
	}
	if len(validationErr.Findings) != 3 {
		t.Fatalf("findings=%#v", validationErr.Findings)
	}
	if validationErr.Findings[1].Pointer != "/data/attributes/copyright" || validationErr.Findings[2].Pointer != "/data/attributes/contentRightsDeclaration" {
		t.Fatalf("associated findings=%#v", validationErr.Findings)
	}
	payload := providerValidationErrorPayload(validationErr)
	if findings, ok := payload["findings"].([]providerErrorFinding); !ok || len(findings) != 3 {
		t.Fatalf("payload=%#v", payload)
	}
}

func TestReusableAppleReviewSubmissionPrefersMatchingOrEmptyDraft(t *testing.T) {
	matching := json.RawMessage(`{"data":[{"type":"reviewSubmissions","id":"draft-1","relationships":{"items":{"data":[{"type":"reviewSubmissionItems","id":"item-1"}]}}}],"included":[{"type":"reviewSubmissionItems","id":"item-1","relationships":{"appStoreVersion":{"data":{"type":"appStoreVersions","id":"version-1"}}}}]}`)
	if submission, conflict := reusableAppleReviewSubmission(matching, "version-1"); submission.ID != "draft-1" || !submission.ItemAttached || !submission.NeedsSubmit || conflict != "" {
		t.Fatalf("matching draft=%+v conflict=%q", submission, conflict)
	}
	empty := json.RawMessage(`{"data":[{"type":"reviewSubmissions","id":"draft-empty","relationships":{"items":{"data":[]}}}]}`)
	if submission, conflict := reusableAppleReviewSubmission(empty, "version-1"); submission.ID != "draft-empty" || submission.ItemAttached || !submission.NeedsSubmit || conflict != "" {
		t.Fatalf("empty draft=%+v conflict=%q", submission, conflict)
	}
	conflicting := json.RawMessage(`{"data":[{"type":"reviewSubmissions","id":"draft-other","relationships":{"items":{"data":[{"type":"reviewSubmissionItems","id":"item-2"}]}}}],"included":[{"type":"reviewSubmissionItems","id":"item-2","relationships":{"appStoreVersion":{"data":{"type":"appStoreVersions","id":"version-2"}}}}]}`)
	if submission, conflict := reusableAppleReviewSubmission(conflicting, "version-1"); submission.ID != "" || submission.ItemAttached || conflict != "draft-other" {
		t.Fatalf("conflicting draft=%+v conflict=%q", submission, conflict)
	}
}

func TestReusableAppleReviewSubmissionIgnoresCompletedHistory(t *testing.T) {
	raw := json.RawMessage(`{"data":[{"type":"reviewSubmissions","id":"complete-old","attributes":{"state":"COMPLETE"},"relationships":{"items":{"data":[{"type":"reviewSubmissionItems","id":"item-old"}]}}},{"type":"reviewSubmissions","id":"empty-draft","attributes":{"state":"READY_FOR_REVIEW"},"relationships":{"items":{"data":[]}}},{"type":"reviewSubmissions","id":"rejected-current","attributes":{"state":"UNRESOLVED_ISSUES"},"relationships":{"items":{"data":[{"type":"reviewSubmissionItems","id":"item-current"}]}}}],"included":[{"type":"reviewSubmissionItems","id":"item-old","relationships":{"appStoreVersion":{"data":{"type":"appStoreVersions","id":"version-old"}}}},{"type":"reviewSubmissionItems","id":"item-current","relationships":{"appStoreVersion":{"data":{"type":"appStoreVersions","id":"version-1"}}}}]}`)
	submission, conflict := reusableAppleReviewSubmission(raw, "version-1")
	if submission.ID != "rejected-current" || !submission.ItemAttached || !submission.NeedsSubmit || submission.State != "UNRESOLVED_ISSUES" || conflict != "" {
		t.Fatalf("submission=%+v conflict=%q", submission, conflict)
	}
}

func TestAppleReviewSubmissionStoresProviderValidationEvidence(t *testing.T) {
	platform := &iosPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })

	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios-provider-validation", TargetKind: "ios", SourceKind: "local", SourceRef: "/src", Framework: "ios",
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	if err != nil {
		t.Fatal(err)
	}
	d.EnvironmentID = env.ID
	d.EnvironmentName = env.Name
	doc := completeIOSStoreDocument()
	doc.Privacy.ManualAttestations = map[string]bool{}
	if _, err := dbUpsertMobileStoreConfig(ctx.AppDB(), d, doc); err != nil {
		t.Fatal(err)
	}
	build, err := dbCreateBuildForEnv(ctx.AppDB(), d.ID, env.ID, "ios", "")
	if err != nil {
		t.Fatal(err)
	}
	release, err := dbCreateReleaseForEnv(ctx.AppDB(), d.ID, env.ID, build.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta := mobileReleaseMeta{AppID: "app-1", VersionName: "1.0", SubmitForReview: true}
	bound := &sdk.BoundIntegration{ConnectionID: 77, AppSlug: "app-store-connect"}
	if err := (&App{}).prepareIOSProductionRelease(bound, release, "build-42", &meta); err != nil {
		t.Fatal(err)
	}
	fresh, err := dbGetRelease(ctx.AppDB(), release.ID)
	if err != nil {
		t.Fatal(err)
	}
	var stored mobileReleaseMeta
	if err := json.Unmarshal([]byte(fresh.ReleaseMetaJSON), &stored); err != nil {
		t.Fatal(err)
	}
	evidence, ok := stored.ProviderValidations["app_privacy"]
	if !ok || evidence.Status != "accepted" || evidence.ExternalID != "review-1" || evidence.ValidatedAt == "" {
		t.Fatalf("provider evidence=%#v", stored.ProviderValidations)
	}
	if fresh.ExternalStatus != "waiting_for_review" {
		t.Fatalf("release status=%q", fresh.ExternalStatus)
	}
	storeCfg, err := dbGetMobileStoreConfig(ctx.AppDB(), d.ID, env.ID, "ios")
	if err != nil {
		t.Fatal(err)
	}
	if !providerCommitValidated(storeCfg, "app_privacy", "1.0") {
		t.Fatalf("store provider evidence was not persisted: %#v", storeCfg)
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
	preserveStoreObservationState(observed, `{"last_apply":{"status":"partial"},"applied_at":"then","desired_hash":"abc","provider_validations":{"app_privacy":{"status":"accepted"}},"stale":"drop"}`)
	if _, ok := observed["last_apply"]; !ok {
		t.Fatal("last_apply was not preserved")
	}
	if observed["applied_at"] != "then" || observed["desired_hash"] != "abc" {
		t.Fatalf("preserved state=%#v", observed)
	}
	if _, ok := observed["provider_validations"]; !ok {
		t.Fatalf("provider validation evidence was dropped: %#v", observed)
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
