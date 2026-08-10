package main

import (
	"strings"
	"testing"
)

func TestMobileVersionReservationAdvancesPastProviderAndLocalValues(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()
	d, err := dbCreateDeployment(db, "p1", CreateDeploymentInput{
		Name: "android-versioning", TargetKind: "android", SourceKind: "local", SourceRef: "/src", Framework: "android",
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbEnsureProductionEnvironment(db, d)
	if err != nil {
		t.Fatal(err)
	}
	first, err := dbCreateBuildForEnv(db, d.ID, env.ID, "android", "")
	if err != nil {
		t.Fatal(err)
	}
	allocation := mobileVersionAllocation{Platform: "android", Provider: "google_play", AppKey: "com.example.app"}
	next, err := dbReserveMobileVersion(db, d, first.ID, allocation, 41)
	if err != nil {
		t.Fatal(err)
	}
	if next != 42 {
		t.Fatalf("first reservation=%d, want 42", next)
	}
	second, err := dbCreateBuildForEnv(db, d.ID, env.ID, "android", "")
	if err != nil {
		t.Fatal(err)
	}
	next, err = dbReserveMobileVersion(db, d, second.ID, allocation, 40)
	if err != nil {
		t.Fatal(err)
	}
	if next != 43 {
		t.Fatalf("second reservation=%d, want 43", next)
	}
}

func TestProviderVersionParsersIgnoreMalformedValues(t *testing.T) {
	if got := maxAppleBuildNumber([]byte(`{"data":[{"attributes":{"version":"8"}},{"attributes":{"version":"bad"}},{"attributes":{"version":"12"}}]}`)); got != 12 {
		t.Fatalf("Apple max=%d, want 12", got)
	}
	if got := maxGoogleVersionCode([]byte(`{"bundles":[{"versionCode":7},{"versionCode":19}]}`)); got != 19 {
		t.Fatalf("Google bundle max=%d, want 19", got)
	}
	if got := maxGoogleTrackVersionCode([]byte(`{"tracks":[{"releases":[{"versionCodes":["4","21","bad"]}]}]}`)); got != 21 {
		t.Fatalf("Google track max=%d, want 21", got)
	}
}

func TestVersionContractIsBackendNeutral(t *testing.T) {
	target := mobileTargetConfig{PackageName: "com.example.app", VersionName: "1.2.3", VersionCode: "42"}
	script := androidVersionInitScript(target)
	if !strings.Contains(script, "versionCode = 42") || !strings.Contains(script, `versionName = "1.2.3"`) {
		t.Fatalf("Gradle version script=%s", script)
	}
	d := &Deployment{
		Name: "android", ProjectID: "p1", EnvironmentName: "production", TargetKind: "android", Framework: "android",
		TargetConfigJSON: `{"package_name":"com.example.app","version_name":"1.2.3","version_code":"42","device_families":["phone"]}`,
	}
	values, err := cloudBuildContractVariables(cloudBuildConfig{SourceMode: "repository"}, d, &Build{ID: 9}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if values["APTEVA_VERSION_NAME"] != "1.2.3" || values["APTEVA_VERSION_CODE"] != "42" || values["APTEVA_DEVICE_FAMILIES_JSON"] != `["phone"]` {
		t.Fatalf("cloud contract=%#v", values)
	}
}

func TestStoreReleaseModesAreProviderSpecific(t *testing.T) {
	ios, err := parseStoreDocument(`{"release_mode":"after_approval"}`, "ios")
	if err != nil || ios.ReleaseMode != "after_approval" {
		t.Fatalf("iOS parse=%+v err=%v", ios, err)
	}
	legacyIOS, err := parseStoreDocument(`{"release_mode":"automatic"}`, "ios")
	if err != nil || legacyIOS.ReleaseMode != "after_approval" {
		t.Fatalf("legacy iOS automatic mode=%+v err=%v", legacyIOS, err)
	}
	android, err := parseStoreDocument(`{"release_mode":"automatic"}`, "android")
	if err != nil || android.ReleaseMode != "immediate" {
		t.Fatalf("Android compatibility parse=%+v err=%v", android, err)
	}
	if _, err := parseStoreDocument(`{"release_mode":"scheduled"}`, "ios"); err == nil {
		t.Fatal("scheduled iOS release without RFC3339 time should be rejected")
	}
	legacyScheduled, err := parseStoreDocument(`{"release_mode":"scheduled","earliest_release_at":"2026-08-07T14:30"}`, "ios")
	if err != nil || legacyScheduled.EarliestReleaseAt != "2026-08-07T14:30:00Z" {
		t.Fatalf("legacy scheduled=%+v err=%v", legacyScheduled, err)
	}
}

func TestRepositoryStoreUploadRequiresExplicitIOSDeviceFamilies(t *testing.T) {
	d := &Deployment{TargetKind: "ios", TargetConfigJSON: `{"bundle_id":"com.example.app","version_name":"1.0","build_number":"2"}`}
	cfg := cloudBuildConfig{SourceMode: "repository", ArtifactMode: "store_upload"}
	if err := validateMobileCloudContract(d, cfg); err == nil {
		t.Fatal("repository store upload without device families should fail")
	}
	d.TargetConfigJSON = `{"bundle_id":"com.example.app","version_name":"1.0","build_number":"2","device_families":["iphone","ipad"]}`
	if err := validateMobileCloudContract(d, cfg); err != nil {
		t.Fatalf("explicit device families rejected: %v", err)
	}
}
