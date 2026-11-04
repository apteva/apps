package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// appStoreSyncPlatform wraps testFlightPlatform with forced provider failures
// and a chosen TestFlight build state.
type appStoreSyncPlatform struct {
	*testFlightPlatform
	fail       map[string]*sdk.ExecuteResult
	buildState string
}

func (p *appStoreSyncPlatform) ExecuteIntegrationTool(id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	if reply := p.fail[tool]; reply != nil {
		p.calls = append(p.calls, integrationCall{Tool: tool, Input: input})
		return reply, nil
	}
	if tool == "get_build" && p.buildState != "" {
		p.calls = append(p.calls, integrationCall{Tool: tool, Input: input})
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"data":{"id":"build-42"},"included":[{"type":"buildBetaDetails","attributes":{"internalBuildState":"` + p.buildState + `"}}]}`)}, nil
	}
	return p.testFlightPlatform.ExecuteIntegrationTool(id, tool, input)
}

func setupAppStoreSync(t *testing.T) (*appStoreSyncPlatform, *Release, *time.Time) {
	t.Helper()
	platform := &appStoreSyncPlatform{
		testFlightPlatform: &testFlightPlatform{groups: []appleBetaGroup{{ID: "group-1", Name: "Deploy Internal", IsInternal: true}}},
		fail:               map[string]*sdk.ExecuteResult{},
	}
	_, release := setupTestFlightReleaseWith(t, platform, "group-1")
	now := time.Now().UTC()
	old := appStoreNow
	appStoreNow = func() time.Time { return now }
	t.Cleanup(func() { appStoreNow = old })
	return platform, release, &now
}

func runMobileWorker(t *testing.T) {
	t.Helper()
	if err := (&App{}).syncPendingMobileReleases(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func rateLimited() *sdk.ExecuteResult {
	return &sdk.ExecuteResult{Status: 429, Data: json.RawMessage(`{"errors":[{"code":"RATE_LIMIT_EXCEEDED","status":"429"}]}`)}
}

func TestAppStoreRateLimitPausesConnectionForAllCallers(t *testing.T) {
	p, release, now := setupAppStoreSync(t)
	p.fail["get_beta_group"] = rateLimited()
	runMobileWorker(t)
	before := len(p.calls)
	if before == 0 {
		t.Fatal("first run made no provider calls")
	}
	// Later runs, and manual syncs, must not spend calls during the backoff.
	*now = now.Add(30 * time.Second)
	runMobileWorker(t)
	fresh, _ := dbGetRelease(globalCtx.AppDB(), release.ID)
	if err := (&App{}).syncMobileReleaseState(fresh); err == nil {
		t.Fatal("manual sync ran during App Store backoff")
	}
	if len(p.calls) != before {
		t.Fatalf("calls during backoff: %+v", p.calls[before:])
	}
	delete(p.fail, "get_beta_group")
	*now = now.Add(2 * time.Minute)
	runMobileWorker(t)
	if len(p.calls) == before {
		t.Fatal("calls did not resume after backoff expired")
	}
	if err := appStoreBackoffActive(globalCtx.AppDB(), 77); err != nil {
		t.Fatalf("successful call did not clear backoff: %v", err)
	}
}

func TestAppStoreBackoffHonorsRetryAfterAndGrows(t *testing.T) {
	_, _, now := setupAppStoreSync(t)
	db := globalCtx.AppDB()
	nextRetry := func() time.Time {
		var raw string
		if err := db.QueryRow(`SELECT next_retry_at FROM app_store_backoff WHERE connection_id=77`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		at, _ := time.Parse(time.RFC3339Nano, raw)
		return at
	}
	_ = recordAppStoreBackoff(db, 77, "rate limited", 0)
	if got := nextRetry().Sub(*now); got != time.Minute {
		t.Fatalf("first backoff=%s", got)
	}
	_ = recordAppStoreBackoff(db, 77, "rate limited", 0)
	if got := nextRetry().Sub(*now); got != 2*time.Minute {
		t.Fatalf("second backoff=%s", got)
	}
	_ = recordAppStoreBackoff(db, 77, "rate limited", 20*time.Minute)
	if got := nextRetry().Sub(*now); got != 20*time.Minute {
		t.Fatalf("Retry-After ignored: %s", got)
	}
	for i := 0; i < 10; i++ {
		_ = recordAppStoreBackoff(db, 77, "rate limited", 0)
	}
	if got := nextRetry().Sub(*now); got != appStoreBackoffMax {
		t.Fatalf("backoff not capped: %s", got)
	}
}

func TestAppStoreTimeoutStartsBackoff(t *testing.T) {
	err := &integrationToolError{Slug: "app-store-connect", Tool: "get_build", Status: 0, Data: json.RawMessage(`"Get \"https://api.appstoreconnect.apple.com/v1/builds/x\": context deadline exceeded"`)}
	if appStoreBackoffReason(err) == "" {
		t.Fatal("timeout did not start backoff")
	}
	if appStoreBackoffReason(&integrationToolError{Status: 500, Data: json.RawMessage(`{"errors":[{"code":"UNEXPECTED_ERROR"}]}`)}) != "" {
		t.Fatal("plain 500 should use the per-release schedule, not pause the account")
	}
}

func TestAppStoreFinalErrorFailsReleaseAndStopsSync(t *testing.T) {
	p, release, now := setupAppStoreSync(t)
	p.fail["add_builds_to_beta_group"] = &sdk.ExecuteResult{Status: 404, Data: json.RawMessage(`{"error":"not_found","retryable":false,"status":404}`)}
	runMobileWorker(t)
	fresh, _ := dbGetRelease(globalCtx.AppDB(), release.ID)
	if fresh.Status != "failed" || fresh.ExternalStatus != "provider_resource_gone" {
		t.Fatalf("release=%+v", fresh)
	}
	before := len(p.calls)
	*now = now.Add(time.Hour)
	runMobileWorker(t)
	if len(p.calls) != before {
		t.Fatalf("failed release synced again: %+v", p.calls[before:])
	}
}

func TestProcessedBuildStuckBeforeTestersTimesOut(t *testing.T) {
	p, release, _ := setupAppStoreSync(t)
	meta := mobileReleaseMeta{Platform: "ios", AppID: "app-1", BuildNumber: "42", BetaGroupID: "group-1",
		BuildProcessed: true, BuildProcessedAt: time.Now().Add(-49 * time.Hour).UTC().Format(time.RFC3339)}
	if err := dbUpdateRelease(globalCtx.AppDB(), release.ID, map[string]any{"external_id": "build-42", "error": "get_beta_group timeout", "release_meta_json": mustJSON(meta)}); err != nil {
		t.Fatal(err)
	}
	runMobileWorker(t)
	fresh, _ := dbGetRelease(globalCtx.AppDB(), release.ID)
	if fresh.Status != "failed" || fresh.ExternalStatus != "testflight_assignment_timeout" {
		t.Fatalf("release=%+v", fresh)
	}
	if len(p.calls) != 0 {
		t.Fatalf("timed-out release contacted Apple: %+v", p.calls)
	}
}

func TestReleaseWithFutureNextSyncIsSkipped(t *testing.T) {
	p, release, now := setupAppStoreSync(t)
	deferReleaseSync(globalCtx.AppDB(), release.ID, now.Add(10*time.Minute))
	runMobileWorker(t)
	if len(p.calls) != 0 {
		t.Fatalf("release synced before next_sync_at: %+v", p.calls)
	}
}

func TestSupersededTestFlightReleaseStopsSyncing(t *testing.T) {
	_, older, _ := setupAppStoreSync(t)
	db := globalCtx.AppDB()
	newer, err := dbCreateReleaseForEnv(db, older.DeploymentID, older.EnvironmentID, older.BuildID)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbUpdateRelease(db, newer.ID, map[string]any{"status": "starting", "provider": "app_store_connect", "channel": "internal", "release_meta_json": older.ReleaseMetaJSON}); err != nil {
		t.Fatal(err)
	}
	runMobileWorker(t)
	fresh, _ := dbGetRelease(db, older.ID)
	if fresh.Status != "stopped" || fresh.ExternalStatus != "superseded" {
		t.Fatalf("older release=%+v", fresh)
	}
	if latest, _ := dbGetRelease(db, newer.ID); latest.Status == "stopped" {
		t.Fatalf("newest release was stopped: %+v", latest)
	}
}

func addStuckReleases(t *testing.T, first *Release) {
	t.Helper()
	db := globalCtx.AppDB()
	for _, name := range []string{"ios-b", "ios-c", "ios-d"} {
		d, err := dbCreateDeployment(db, "p1", CreateDeploymentInput{Name: name, TargetKind: "ios", SourceKind: "local", SourceRef: "/src", Framework: "ios"})
		if err != nil {
			t.Fatal(err)
		}
		env, _ := dbEnsureProductionEnvironment(db, d)
		b, _ := dbCreateBuildForEnv(db, d.ID, env.ID, "ios", "")
		rel, _ := dbCreateReleaseForEnv(db, d.ID, env.ID, b.ID)
		if err := dbUpdateRelease(db, rel.ID, map[string]any{"status": "starting", "provider": "app_store_connect", "channel": "internal", "release_meta_json": first.ReleaseMetaJSON}); err != nil {
			t.Fatal(err)
		}
	}
}

func runWorkerMinutes(t *testing.T, now *time.Time, minutes int) {
	t.Helper()
	for i := 0; i < minutes; i++ {
		runMobileWorker(t)
		*now = now.Add(time.Minute)
	}
}

func TestStuckReleasesStayWithinCallBudget(t *testing.T) {
	p, first, now := setupAppStoreSync(t)
	addStuckReleases(t, first)
	p.buildState = "READY_FOR_BETA_TESTING"
	runWorkerMinutes(t, now, 10)
	// Unbounded, four releases would cost about 4 calls each per minute (160).
	// The first run lists, looks up and assigns. Availability checks then run
	// at minutes 0, 1 and 6.
	if got := countIntegrationCalls(p.calls, "get_build"); got != 12 {
		t.Fatalf("get_build calls=%d, want 12", got)
	}
	if len(p.calls) > 21 {
		t.Fatalf("calls=%d exceed budget 21: %+v", len(p.calls), p.calls)
	}
}

func TestFailingReleasesBackOffOnRetrySchedule(t *testing.T) {
	p, first, now := setupAppStoreSync(t)
	addStuckReleases(t, first)
	p.fail["get_build"] = &sdk.ExecuteResult{Status: 500, Data: json.RawMessage(`{"errors":[{"code":"UNEXPECTED_ERROR"}]}`)}
	runWorkerMinutes(t, now, 10)
	// Schedule 1m, 2m, 5m: attempts at minutes 0, 1, 3 and 8 for each release.
	if got := countIntegrationCalls(p.calls, "get_build"); got != 16 {
		t.Fatalf("get_build calls=%d, want 16", got)
	}
	// The assignment was saved despite the failing availability check.
	if got := countIntegrationCalls(p.calls, "add_builds_to_beta_group"); got != 4 {
		t.Fatalf("assignment calls=%d, want one per release", got)
	}
}

func TestAppStoreAwaitingAppleSlowsUntilStateChanges(t *testing.T) {
	p, release, now := setupAppStoreSync(t)
	db := globalCtx.AppDB()
	p.buildState = "READY_FOR_BETA_TESTING"
	start := *now
	var checkedAt []time.Duration
	seen := 0
	step := func(minutes int) {
		t.Helper()
		for i := 0; i < minutes; i++ {
			runMobileWorker(t)
			if got := countIntegrationCalls(p.calls, "get_build"); got != seen {
				seen = got
				checkedAt = append(checkedAt, now.Sub(start))
			}
			*now = now.Add(time.Minute)
		}
	}
	step(172)
	// The last two checks are an hour apart: the schedule stays at its cap.
	want := []time.Duration{0, time.Minute, 6 * time.Minute, 21 * time.Minute, 51 * time.Minute, 111 * time.Minute, 171 * time.Minute}
	if len(checkedAt) != len(want) {
		t.Fatalf("checks at %v, want %v", checkedAt, want)
	}
	for i, at := range want {
		if checkedAt[i] != at {
			t.Fatalf("checks at %v, want %v", checkedAt, want)
		}
	}

	// A failed check retries soon, and success returns to the slow interval.
	if err := makeReleaseSyncDue(db, release.ID); err != nil {
		t.Fatal(err)
	}
	p.fail["get_build"] = &sdk.ExecuteResult{Status: 500, Data: json.RawMessage(`{"errors":[{"code":"UNEXPECTED_ERROR"}]}`)}
	runMobileWorker(t)
	delete(p.fail, "get_build")
	*now = now.Add(time.Minute)
	runMobileWorker(t)
	var checks, failures int
	if err := db.QueryRow(`SELECT idle_checks,failures FROM release_sync_state WHERE release_id=?`, release.ID).Scan(&checks, &failures); err != nil {
		t.Fatal(err)
	}
	if checks <= len(releaseAwaitingAppleSchedule) || failures != 0 {
		t.Fatalf("failure reset the slow interval: idle_checks=%d failures=%d", checks, failures)
	}
	if releaseSyncDue(db, release.ID) {
		t.Fatal("release due immediately after a successful unchanged check")
	}

	// A changed answer from Apple goes back to the start of the schedule.
	if err := makeReleaseSyncDue(db, release.ID); err != nil {
		t.Fatal(err)
	}
	p.buildState = "MISSING_EXPORT_COMPLIANCE"
	runMobileWorker(t)
	if err := db.QueryRow(`SELECT idle_checks FROM release_sync_state WHERE release_id=?`, release.ID).Scan(&checks); err != nil || checks != 1 {
		t.Fatalf("state change kept the slow interval: idle_checks=%d err=%v", checks, err)
	}

	// Availability ends the wait and removes the schedule.
	if err := makeReleaseSyncDue(db, release.ID); err != nil {
		t.Fatal(err)
	}
	p.buildState = "IN_BETA_TESTING"
	runMobileWorker(t)
	fresh, _ := dbGetRelease(db, release.ID)
	if fresh.Status != "live" || fresh.ExternalStatus != "testflight_available" {
		t.Fatalf("release=%+v", fresh)
	}
	if err := db.QueryRow(`SELECT idle_checks FROM release_sync_state WHERE release_id=?`, release.ID).Scan(&checks); err == nil {
		t.Fatal("finished release kept a sync schedule")
	}
}

// makeReleaseSyncDue makes a waiting release due now, keeping its counters.
func makeReleaseSyncDue(db *sql.DB, releaseID int64) error {
	_, err := db.Exec(`UPDATE release_sync_state SET next_sync_at='' WHERE release_id=?`, releaseID)
	return err
}

func TestAppStoreAssignedBuildSurvivesAssignmentTimeout(t *testing.T) {
	p, release, _ := setupAppStoreSync(t)
	p.buildState = "READY_FOR_BETA_TESTING"
	meta := mobileReleaseMeta{Platform: "ios", AppID: "app-1", BuildNumber: "42", BetaGroupID: "group-1", BetaGroupAssigned: "group-1",
		BuildProcessed: true, BuildProcessedAt: time.Now().Add(-72 * time.Hour).UTC().Format(time.RFC3339)}
	if err := dbUpdateRelease(globalCtx.AppDB(), release.ID, map[string]any{"external_id": "build-42", "release_meta_json": mustJSON(meta)}); err != nil {
		t.Fatal(err)
	}
	runMobileWorker(t)
	fresh, _ := dbGetRelease(globalCtx.AppDB(), release.ID)
	if fresh.Status != "starting" || fresh.ExternalStatus != "ready_for_beta_testing" {
		t.Fatalf("assigned build was marked failed: %+v", fresh)
	}
	if countIntegrationCalls(p.calls, "get_build") != 1 || countIntegrationCalls(p.calls, "add_builds_to_beta_group") != 0 {
		t.Fatalf("calls=%+v", p.calls)
	}
}

func TestAppStoreExpiredBuildStillFailsWhileAwaitingApple(t *testing.T) {
	p, release, _ := setupAppStoreSync(t)
	p.buildState = "EXPIRED"
	meta := mobileReleaseMeta{Platform: "ios", AppID: "app-1", BuildNumber: "42", BetaGroupID: "group-1", BetaGroupAssigned: "group-1", BuildProcessed: true}
	if err := dbUpdateRelease(globalCtx.AppDB(), release.ID, map[string]any{"external_id": "build-42", "release_meta_json": mustJSON(meta)}); err != nil {
		t.Fatal(err)
	}
	runMobileWorker(t)
	fresh, _ := dbGetRelease(globalCtx.AppDB(), release.ID)
	if fresh.Status != "failed" || fresh.ExternalStatus != "expired" || fresh.Error == "" {
		t.Fatalf("release=%+v", fresh)
	}
}

func TestAppStoreAllBuildsGroupCountsAsAssigned(t *testing.T) {
	p, release, _ := setupAppStoreSync(t)
	p.groups = []appleBetaGroup{{ID: "group-1", Name: "Deploy Internal", IsInternal: true, HasAccessToAllBuilds: true}}
	p.buildState = "READY_FOR_BETA_TESTING"
	runMobileWorker(t)
	fresh, _ := dbGetRelease(globalCtx.AppDB(), release.ID)
	var meta mobileReleaseMeta
	if err := json.Unmarshal([]byte(fresh.ReleaseMetaJSON), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.BetaGroupAssigned != "group-1" || countIntegrationCalls(p.calls, "add_builds_to_beta_group") != 0 {
		t.Fatalf("meta=%+v calls=%+v", meta, p.calls)
	}
	if testFlightAssignmentExpired(fresh, mobileReleaseMeta{BuildProcessed: true, BetaGroupAssigned: meta.BetaGroupAssigned}) {
		t.Fatal("all-builds group would be marked failed after 48 hours")
	}
}

func TestAppStoreSuccessfulCheckClearsStaleError(t *testing.T) {
	p, release, now := setupAppStoreSync(t)
	db := globalCtx.AppDB()
	p.buildState = "READY_FOR_BETA_TESTING"
	if err := dbUpdateRelease(db, release.ID, map[string]any{"error": "app-store-connect.get_build returned HTTP 500"}); err != nil {
		t.Fatal(err)
	}
	runMobileWorker(t)
	fresh, _ := dbGetRelease(db, release.ID)
	if fresh.Error != "" {
		t.Fatalf("stale error kept: %q", fresh.Error)
	}
	// A failed check records its error again until the next success.
	p.fail["get_build"] = &sdk.ExecuteResult{Status: 500, Data: json.RawMessage(`{"errors":[{"code":"UNEXPECTED_ERROR"}]}`)}
	*now = now.Add(2 * time.Minute)
	runMobileWorker(t)
	fresh, _ = dbGetRelease(db, release.ID)
	if fresh.Error == "" || fresh.ExternalStatus != "sync_error" {
		t.Fatalf("failed check not recorded: %+v", fresh)
	}
	delete(p.fail, "get_build")
	*now = now.Add(2 * time.Minute)
	runMobileWorker(t)
	fresh, _ = dbGetRelease(db, release.ID)
	if fresh.Error != "" || fresh.ExternalStatus != "ready_for_beta_testing" {
		t.Fatalf("recovered check did not clear error: %+v", fresh)
	}
}
