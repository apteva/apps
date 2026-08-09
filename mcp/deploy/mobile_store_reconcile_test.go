package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type pagedApplePlatform struct {
	tk.BasePlatformClient
	calls []integrationCall
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
