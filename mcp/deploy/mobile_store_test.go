package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestMobileStoreConfigRoundTripUsesEnvironmentIdentity(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()
	d, err := dbCreateDeployment(db, "p1", CreateDeploymentInput{
		Name: "ios-listing", TargetKind: "ios", SourceKind: "local", SourceRef: "/src", Framework: "ios",
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbEnsureProductionEnvironment(db, d)
	if err != nil {
		t.Fatal(err)
	}
	d = effectiveDeploymentForEnvironment(d, env)
	doc := completeIOSStoreDocument()
	cfg, err := dbUpsertMobileStoreConfig(db, d, doc)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Platform != "ios" || cfg.Provider != "app_store_connect" || cfg.EnvironmentID != env.ID || cfg.DesiredHash == "" {
		t.Fatalf("config=%+v", cfg)
	}
	var saved StoreDocument
	if err := json.Unmarshal([]byte(cfg.DesiredJSON), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.VersionName != "1.0" || saved.Localizations["en-US"].Description == "" {
		t.Fatalf("saved=%+v", saved)
	}
}

func TestMobileStorePartialStatusAndUnchangedSavePreserveFailureHistory(t *testing.T) {
	db := openSchemaDBThrough(t, len(testMigrationFiles)-1)
	defer db.Close()
	d, err := dbCreateDeployment(db, "p1", CreateDeploymentInput{
		Name: "ios-state", TargetKind: "ios", SourceKind: "local", SourceRef: "/src", Framework: "ios",
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbEnsureProductionEnvironment(db, d)
	if err != nil {
		t.Fatal(err)
	}
	d = effectiveDeploymentForEnvironment(d, env)
	doc := completeIOSStoreDocument()
	cfg, err := dbUpsertMobileStoreConfig(db, d, doc)
	if err != nil {
		t.Fatal(err)
	}
	observed := `{"last_apply":{"status":"partial"}}`
	if err := dbUpdateMobileStoreState(db, cfg.ID, "failed", observed, "{}", "", "pricing failed"); err != nil {
		t.Fatal(err)
	}
	applyTestMigration(t, db, testMigrationFiles[len(testMigrationFiles)-1])
	if err := dbUpdateMobileStoreState(db, cfg.ID, "partial", observed, "{}", "", "pricing failed"); err != nil {
		t.Fatalf("partial status rejected after migration: %v", err)
	}
	cfg, err = dbUpsertMobileStoreConfig(db, d, doc)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Status != "partial" || cfg.LastError != "pricing failed" || cfg.ObservedJSON != observed {
		t.Fatalf("unchanged save lost reconciliation state: %+v", cfg)
	}
	doc.Copyright = "Changed"
	cfg, err = dbUpsertMobileStoreConfig(db, d, doc)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Status != "draft" || cfg.LastError != "" {
		t.Fatalf("changed desired document did not reset active state: %+v", cfg)
	}
}

func TestReadyStorePreflightClearsStaleBlockedState(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()
	d, err := dbCreateDeployment(db, "p1", CreateDeploymentInput{
		Name: "ios-ready", TargetKind: "ios", SourceKind: "local", SourceRef: "/src", Framework: "ios",
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbEnsureProductionEnvironment(db, d)
	if err != nil {
		t.Fatal(err)
	}
	d = effectiveDeploymentForEnvironment(d, env)
	cfg, err := dbUpsertMobileStoreConfig(db, d, completeIOSStoreDocument())
	if err != nil {
		t.Fatal(err)
	}
	if err := dbUpdateMobileStoreState(db, cfg.ID, "blocked", "", "{}", "", "old preflight failure"); err != nil {
		t.Fatal(err)
	}
	cfg, err = dbGetMobileStoreConfig(db, d.ID, d.EnvironmentID, d.TargetKind)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistStorePreflightState(db, cfg, StorePreflight{Ready: true}); err != nil {
		t.Fatal(err)
	}
	cfg, err = dbGetMobileStoreConfig(db, d.ID, d.EnvironmentID, d.TargetKind)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Status != "ready" || cfg.LastError != "" {
		t.Fatalf("stale blocked state was not cleared: %+v", cfg)
	}
}

func TestReadyStorePreflightRestoresAppliedStateAndPreservesApplyFailures(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()
	d, err := dbCreateDeployment(db, "p1", CreateDeploymentInput{
		Name: "ios-applied", TargetKind: "ios", SourceKind: "local", SourceRef: "/src", Framework: "ios",
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbEnsureProductionEnvironment(db, d)
	if err != nil {
		t.Fatal(err)
	}
	d = effectiveDeploymentForEnvironment(d, env)
	cfg, err := dbUpsertMobileStoreConfig(db, d, completeIOSStoreDocument())
	if err != nil {
		t.Fatal(err)
	}
	if err := dbUpdateMobileStoreState(db, cfg.ID, "blocked", "", "{}", cfg.DesiredHash, "old preflight failure"); err != nil {
		t.Fatal(err)
	}
	cfg, err = dbGetMobileStoreConfig(db, d.ID, d.EnvironmentID, d.TargetKind)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistStorePreflightState(db, cfg, StorePreflight{Ready: true}); err != nil {
		t.Fatal(err)
	}
	if cfg.Status != "applied" || cfg.LastError != "" {
		t.Fatalf("matching applied configuration was not restored: %+v", cfg)
	}

	if err := dbUpdateMobileStoreState(db, cfg.ID, "failed", "", "{}", "", "provider apply failed"); err != nil {
		t.Fatal(err)
	}
	cfg, err = dbGetMobileStoreConfig(db, d.ID, d.EnvironmentID, d.TargetKind)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistStorePreflightState(db, cfg, StorePreflight{Ready: true}); err != nil {
		t.Fatal(err)
	}
	if cfg.Status != "failed" || cfg.LastError != "provider apply failed" {
		t.Fatalf("apply failure history was overwritten: %+v", cfg)
	}
}

func TestStoreDocumentNeverPersistsReviewPassword(t *testing.T) {
	doc := completeIOSStoreDocument()
	doc.Review.DemoAccountRequired = true
	doc.Review.DemoUsername = "reviewer"
	doc.Review.DemoPassword = "do-not-store"
	raw, _, err := canonicalStoreDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "do-not-store") || strings.Contains(raw, "demo_password\"") {
		t.Fatalf("review password leaked into desired document: %s", raw)
	}
}

func TestGenericContentRatingTranslatesToAppleAttributes(t *testing.T) {
	rating := StoreContentRating{
		Violence: "NONE", SexualContent: "INFREQUENT_OR_MILD", Profanity: "NONE", Drugs: "NONE",
		GamblingSimulation: "NONE", Contests: "NONE", Weapons: "NONE", HorrorFear: "NONE",
		MedicalInformation: "NONE", HealthWellness: "NONE", MatureThemes: "NONE",
		UnrestrictedWebAccess: true, LootBoxes: true,
	}
	if !storeContentRatingComplete(rating) {
		t.Fatal("complete generic content rating was rejected")
	}
	attributes := appleAgeDeclaration(StoreClassification{ContentRating: rating})
	if attributes["sexualContentOrNudity"] != "INFREQUENT" || attributes["unrestrictedWebAccess"] != true || attributes["lootBox"] != true {
		t.Fatalf("translated attributes=%#v", attributes)
	}
}

func TestProviderReadinessCanReplaceManualDistributionAttestation(t *testing.T) {
	cfg := &MobileStoreConfig{ObservedJSON: `{"readiness":{"pricing":{"status":"verified"},"availability":{"status":"verified"}}}`}
	if !providerReadinessVerified(cfg, "pricing") || !providerReadinessVerified(cfg, "availability") {
		t.Fatalf("provider readiness not recognized: %s", cfg.ObservedJSON)
	}
}

func TestStorePreflightRejectsBinaryListingVersionMismatch(t *testing.T) {
	root := t.TempDir()
	d := &Deployment{ID: 1, EnvironmentID: 2, TargetKind: "ios"}
	doc := completeIOSStoreDocument()
	cfg := &MobileStoreConfig{DesiredHash: "configured"}
	build := &Build{ArtifactManifestJSON: mustJSON(artifactManifest{
		Platform: "ios", VersionName: "0.1.0", BuildNumber: "1", Primary: "app.ipa",
	})}
	preflight := validateStoreDocument(root, d, build, cfg, doc, true)
	if preflight.Ready {
		t.Fatalf("preflight unexpectedly ready: %+v", preflight)
	}
	found := false
	for _, finding := range preflight.Findings {
		if finding.Code == "version.binary_mismatch" && strings.Contains(finding.Message, "0.1.0") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing version mismatch: %+v", preflight.Findings)
	}
}

func TestStorePreflightRequiresProviderMedia(t *testing.T) {
	root := t.TempDir()
	ios := &Deployment{ID: 1, EnvironmentID: 2, TargetKind: "ios"}
	iosDoc := completeIOSStoreDocument()
	iosDoc.Localizations["en-US"] = StoreLocalization{
		Description: "Description", Keywords: []string{"test"}, SupportURL: "https://example.com/support",
	}
	preflight := validateStoreDocument(root, ios, nil, &MobileStoreConfig{}, iosDoc, true)
	if !hasStoreFinding(preflight, "title.required") {
		t.Fatalf("expected iOS title finding: %#v", preflight.Findings)
	}

	android := &Deployment{ID: 3, EnvironmentID: 4, TargetKind: "android"}
	androidDoc := defaultStoreDocument("android")
	androidDoc.VersionName = "1.0"
	androidDoc.Localizations["en-US"] = StoreLocalization{
		Title: "Test", ShortDescription: "Short", Description: "Description",
	}
	androidDoc.Privacy.PolicyURL = "https://example.com/privacy"
	androidDoc.Privacy.ManualAttestations["google_data_safety_published"] = true
	androidDoc.Privacy.ManualAttestations["google_app_content_complete"] = true
	androidDoc.Distribution.Provider = map[string]any{
		"availability_configured": true,
		"pricing_configured":      true,
	}
	preflight = validateStoreDocument(root, android, nil, &MobileStoreConfig{}, androidDoc, true)
	for _, code := range []string{"screenshots.google_minimum", "icon.google_required", "feature_graphic.google_required"} {
		if !hasStoreFinding(preflight, code) {
			t.Fatalf("expected %s finding: %#v", code, preflight.Findings)
		}
	}
}

func hasStoreFinding(preflight StorePreflight, code string) bool {
	for _, finding := range preflight.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func TestStoreAssetPathsCannotCrossDeploymentBoundary(t *testing.T) {
	root := t.TempDir()
	d := &Deployment{ID: 12, EnvironmentID: 34}
	good := filepath.Join(root, "store-assets", "12", "34", "asset", "shot.png")
	if err := os.MkdirAll(filepath.Dir(good), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveStoreAssetPath(root, d, "store-assets/12/34/asset/shot.png"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../secret", "store-assets/12/99/asset/shot.png", good} {
		if _, err := resolveStoreAssetPath(root, d, path); err == nil {
			t.Fatalf("path %q should be rejected", path)
		}
	}
}

func TestAppleAssetUploadExecutesReservedChunks(t *testing.T) {
	var received bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.Header.Get("X-Upload") != "yes" {
			t.Fatalf("request=%s headers=%v", r.Method, r.Header)
		}
		_, _ = received.ReadFrom(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(path, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"data":{"attributes":{"uploadOperations":[
		{"method":"PUT","url":"` + server.URL + `","length":3,"offset":0,"requestHeaders":[{"name":"X-Upload","value":"yes"}]},
		{"method":"PUT","url":"` + server.URL + `","length":3,"offset":3,"requestHeaders":[{"name":"X-Upload","value":"yes"}]}
	]}}}`)
	if err := uploadAppleAssetOperations(path, raw); err != nil {
		t.Fatal(err)
	}
	if received.String() != "abcdef" {
		t.Fatalf("uploaded=%q", received.String())
	}
}

func TestApplePendingReleaseIsNotMarkedLive(t *testing.T) {
	platform := &iosPlatform{state: "PENDING_APPLE_RELEASE"}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios-pending", TargetKind: "ios", SourceKind: "local", SourceRef: "/src", Framework: "ios",
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	if err != nil {
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
	if err := dbUpdateRelease(ctx.AppDB(), release.ID, map[string]any{
		"provider": "app_store_connect", "status": "starting",
	}); err != nil {
		t.Fatal(err)
	}
	bound := &sdk.BoundIntegration{ConnectionID: 77, AppSlug: "app-store-connect"}
	if err := (&App{}).syncAppStoreVersionState(bound, release, &mobileReleaseMeta{AppStoreVersionID: "version-1"}); err != nil {
		t.Fatal(err)
	}
	fresh, err := dbGetRelease(ctx.AppDB(), release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status == "live" || fresh.ExternalStatus != "approved_pending_release" {
		t.Fatalf("release=%+v", fresh)
	}
}

func completeIOSStoreDocument() StoreDocument {
	return StoreDocument{
		SchemaVersion: 1, VersionName: "1.0", DefaultLocale: "en-US", ReleaseMode: "manual",
		Localizations: map[string]StoreLocalization{
			"en-US": {
				Title: "Example", Description: "A complete description.", Keywords: []string{"example"},
				SupportURL: "https://example.com/support",
			},
		},
		Assets: []StoreAsset{{
			ID: "shot", Locale: "en-US", Kind: "phone_screenshot",
			Path: "store-assets/1/2/shot/shot.png", SHA256: "abc",
		}},
		Review: StoreReview{
			FirstName: "Ada", LastName: "Lovelace", Email: "review@example.com", Phone: "+1 555 0100",
		},
		Classification: StoreClassification{
			PrimaryCategory: "PRODUCTIVITY", AgeDeclaration: map[string]any{"gamblingAndContests": "NONE"},
		},
		Distribution: StoreDistribution{
			Territories: []string{"US"}, PriceTier: "FREE",
			Provider: map[string]any{"availability_configured": true, "pricing_configured": true},
		},
		Privacy: StorePrivacy{
			PolicyURL:          "https://example.com/privacy",
			ManualAttestations: map[string]bool{"apple_privacy_published": true},
		},
	}
}
