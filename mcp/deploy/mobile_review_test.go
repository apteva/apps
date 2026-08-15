package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestAppleRejectedReleaseRecordsReviewDiagnostics(t *testing.T) {
	platform := &iosPlatform{responses: map[string]json.RawMessage{
		"get_app_version": json.RawMessage(`{
			"data":{"type":"appStoreVersions","id":"version-1","attributes":{"appStoreState":"REJECTED","versionString":"1.0"},"relationships":{"build":{"data":{"type":"builds","id":"build-10"}}}},
			"included":[{"type":"builds","id":"build-10","attributes":{"version":"10"}}]
		}`),
		"list_builds":           json.RawMessage(`{"data":[{"type":"builds","id":"build-22","attributes":{"version":"22","processingState":"VALID"}}]}`),
		"get_review_submission": json.RawMessage(`{"data":{"type":"reviewSubmissions","id":"review-1","attributes":{"state":"UNRESOLVED_ISSUES","submittedDate":"2026-08-14T09:09:00Z"}}}`),
		"list_review_submission_items": json.RawMessage(`{
			"data":[{"type":"reviewSubmissionItems","id":"item-1","attributes":{"state":"REJECTED"},"relationships":{"appStoreVersion":{"data":{"type":"appStoreVersions","id":"version-1"}}}}]
		}`),
	}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })

	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios-review", TargetKind: "ios", SourceKind: "local", SourceRef: "/src", Framework: "ios",
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
	meta := mobileReleaseMeta{
		Platform: "ios", AppID: "app-123", VersionName: "1.0", BuildNumber: "10",
		AppStoreVersionID: "version-1", ReviewSubmissionID: "review-1", Prepared: true,
	}
	if err := dbUpdateRelease(ctx.AppDB(), release.ID, map[string]any{
		"provider": "app_store_connect", "channel": "production", "status": "failed",
		"external_id": "build-10", "external_status": "rejected", "error": "obsolete provider error",
		"release_meta_json": mustJSON(meta),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := (&App{}).toolReleaseSync(ctx, map[string]any{"release_id": float64(release.ID)}); err != nil {
		t.Fatal(err)
	}
	fresh, err := dbGetRelease(ctx.AppDB(), release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != "failed" || fresh.ExternalStatus != "rejected" || strings.Contains(fresh.Error, "obsolete") {
		t.Fatalf("release=%+v", fresh)
	}
	if !strings.Contains(fresh.Error, "does not expose App Review message text") {
		t.Fatalf("error=%q", fresh.Error)
	}
	var stored mobileReleaseMeta
	if err := json.Unmarshal([]byte(fresh.ReleaseMetaJSON), &stored); err != nil {
		t.Fatal(err)
	}
	outcome := stored.ReviewOutcome
	if outcome == nil {
		t.Fatal("review outcome was not persisted")
	}
	if outcome.SubmissionState != "UNRESOLVED_ISSUES" || outcome.ItemState != "REJECTED" {
		t.Fatalf("outcome=%+v", outcome)
	}
	if outcome.SubmittedArtifactID != "build-10" || outcome.SubmittedArtifactVersion != "10" {
		t.Fatalf("submitted artifact=%+v", outcome)
	}
	if outcome.LatestArtifactID != "build-22" || outcome.LatestArtifactVersion != "22" {
		t.Fatalf("latest artifact=%+v", outcome)
	}
	if outcome.DetailsAvailable || outcome.DetailsSource != "provider_console" || !strings.Contains(outcome.ProviderConsoleURL, "app-123") {
		t.Fatalf("provider details=%+v", outcome)
	}
	if outcome.ActionRequired == "" || outcome.SyncError != "" {
		t.Fatalf("diagnostic state=%+v", outcome)
	}

	wantCalls := map[string]bool{
		"get_app_version": false, "list_builds": false, "get_review_submission": false, "list_review_submission_items": false,
	}
	for _, call := range platform.calls {
		if _, ok := wantCalls[call.Tool]; ok {
			wantCalls[call.Tool] = true
		}
		if call.Tool == "get_app_version" && call.Input["include"] != "build" {
			t.Fatalf("get_app_version input=%+v", call.Input)
		}
	}
	for tool, called := range wantCalls {
		if !called {
			t.Fatalf("%s was not called; calls=%+v", tool, platform.calls)
		}
	}
}

func TestGoogleTrackVersionStatus(t *testing.T) {
	raw := json.RawMessage(`{"releases":[{"status":"completed","versionCodes":["41","42"]}]}`)
	if got := googleTrackVersionStatus(raw, "42"); got != "completed" {
		t.Fatalf("status=%q", got)
	}
	if got := googleTrackVersionStatus(raw, "99"); got != "" {
		t.Fatalf("unexpected status=%q", got)
	}
}

func TestReviewOutcomeChangeIgnoresSyncTimestamp(t *testing.T) {
	a := &mobileReviewOutcome{Provider: "app_store_connect", ItemState: "REJECTED", SyncedAt: "one"}
	b := &mobileReviewOutcome{Provider: "app_store_connect", ItemState: "REJECTED", SyncedAt: "two"}
	if mobileReviewOutcomeChanged(a, b) {
		t.Fatal("timestamp-only update was treated as a material review change")
	}
	b.ItemState = "APPROVED"
	if !mobileReviewOutcomeChanged(a, b) {
		t.Fatal("review state update was not detected")
	}
}

func TestAppStoreVersionSyncRejectsMissingState(t *testing.T) {
	platform := &iosPlatform{responses: map[string]json.RawMessage{"get_app_version": json.RawMessage(`{"data":{}}`)}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	err := (&App{}).syncAppStoreVersionState(
		&sdk.BoundIntegration{ConnectionID: 77, AppSlug: "app-store-connect"},
		&Release{ID: 1},
		&mobileReleaseMeta{AppStoreVersionID: "version-1"},
	)
	if err == nil || !strings.Contains(err.Error(), "missing state") {
		t.Fatalf("error=%v", err)
	}
}

var _ sdk.PlatformClient = (*iosPlatform)(nil)
