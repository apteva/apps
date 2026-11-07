package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// appStoreSyncPlatform wraps testFlightPlatform with forced provider failures,
// canned replies, a chosen TestFlight build state and a hook that runs while a
// get_build call is in flight.
type appStoreSyncPlatform struct {
	*testFlightPlatform
	fail       map[string]*sdk.ExecuteResult
	replies    map[string]*sdk.ExecuteResult
	buildState string
	onGetBuild func()
}

func (p *appStoreSyncPlatform) ExecuteIntegrationTool(id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	if tool == "get_build" && p.onGetBuild != nil {
		p.onGetBuild()
	}
	reply := p.fail[tool]
	if reply == nil {
		reply = p.replies[tool]
	}
	if reply != nil {
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
		replies:            map[string]*sdk.ExecuteResult{},
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
	// The first run lists, looks up and assigns. Availability and tester checks
	// then run together at minutes 0, 1 and 6.
	if got := countIntegrationCalls(p.calls, "get_build"); got != 12 {
		t.Fatalf("get_build calls=%d, want 12", got)
	}
	if got := countIntegrationCalls(p.calls, "list_beta_testers"); got != 12 {
		t.Fatalf("list_beta_testers calls=%d, want 12", got)
	}
	if len(p.calls) > 33 {
		t.Fatalf("calls=%d exceed budget 33: %+v", len(p.calls), p.calls)
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

func cannedReply(data string) *sdk.ExecuteResult {
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(data)}
}

func releaseWithMeta(t *testing.T, id int64) (*Release, mobileReleaseMeta) {
	t.Helper()
	rel, err := dbGetRelease(globalCtx.AppDB(), id)
	if err != nil {
		t.Fatal(err)
	}
	var meta mobileReleaseMeta
	if err := json.Unmarshal([]byte(rel.ReleaseMetaJSON), &meta); err != nil {
		t.Fatal(err)
	}
	return rel, meta
}

// checkTimes runs the worker once a minute and returns when tool was called.
func checkTimes(t *testing.T, p *appStoreSyncPlatform, now *time.Time, tool string, minutes int) []time.Duration {
	t.Helper()
	start, seen := *now, countIntegrationCalls(p.calls, tool)
	var at []time.Duration
	for i := 0; i < minutes; i++ {
		runMobileWorker(t)
		if got := countIntegrationCalls(p.calls, tool); got != seen {
			seen = got
			at = append(at, now.Sub(start))
		}
		*now = now.Add(time.Minute)
	}
	return at
}

func minutesOf(values ...int) []time.Duration {
	out := make([]time.Duration, len(values))
	for i, value := range values {
		out[i] = time.Duration(value) * time.Minute
	}
	return out
}

func TestAppStoreEmptyInternalGroupWarnsUntilTesterAppears(t *testing.T) {
	p, release, _ := setupAppStoreSync(t)
	p.buildState = "READY_FOR_BETA_TESTING"
	runMobileWorker(t)
	fresh, meta := releaseWithMeta(t, release.ID)
	if fresh.Status != "starting" || fresh.Error != "" || meta.TesterAccess != testerAccessNoTesters || meta.TesterCount != 0 {
		t.Fatalf("release=%+v meta=%+v", fresh, meta)
	}
	if !strings.Contains(meta.TesterWarning, "likely") {
		t.Fatalf("warning must not state a certain cause: %q", meta.TesterWarning)
	}

	sync := func() map[string]any {
		t.Helper()
		result, err := (&App{}).toolReleaseSync(globalCtx, map[string]any{"release_id": float64(release.ID)})
		if err != nil {
			t.Fatal(err)
		}
		return result.(map[string]any)
	}
	if got := sync()["tester_warning"]; got != emptyTesterGroupWarning {
		t.Fatalf("deploy_release_sync tester_warning=%v", got)
	}

	// A tester in the group clears the warning. Only the count is kept.
	p.replies["list_beta_testers"] = cannedReply(`{"data":[{"id":"tester-1","attributes":{"email":"tester@example.com"}}]}`)
	if got, ok := sync()["tester_warning"]; ok {
		t.Fatalf("warning kept with a tester in the group: %v", got)
	}
	fresh, meta = releaseWithMeta(t, release.ID)
	if meta.TesterAccess != "configured" || meta.TesterCount != 1 || meta.TesterWarning != "" {
		t.Fatalf("meta=%+v", meta)
	}
	if strings.Contains(fresh.ReleaseMetaJSON, "tester@example.com") {
		t.Fatal("tester email stored on the release")
	}

	// A later state ends the warning without another lookup.
	delete(p.replies, "list_beta_testers")
	sync()
	if _, meta = releaseWithMeta(t, release.ID); meta.TesterWarning == "" {
		t.Fatal("group emptied again but no warning")
	}
	lookups := countIntegrationCalls(p.calls, "list_beta_testers")
	p.buildState = "IN_BETA_TESTING"
	sync()
	fresh, meta = releaseWithMeta(t, release.ID)
	if fresh.Status != "live" || meta.TesterWarning != "" || meta.TesterAccess == testerAccessNoTesters {
		t.Fatalf("release=%+v meta=%+v", fresh, meta)
	}
	if countIntegrationCalls(p.calls, "list_beta_testers") != lookups {
		t.Fatal("available build still looked up testers")
	}
}

func TestAppStoreTesterLookupOnlyForWaitingInternalBuilds(t *testing.T) {
	t.Run("other internal state", func(t *testing.T) {
		p, _, _ := setupAppStoreSync(t)
		p.buildState = "MISSING_EXPORT_COMPLIANCE"
		runMobileWorker(t)
		if got := countIntegrationCalls(p.calls, "list_beta_testers"); got != 0 {
			t.Fatalf("list_beta_testers calls=%d, want 0", got)
		}
	})
	t.Run("external channel", func(t *testing.T) {
		p, release, _ := setupAppStoreSync(t)
		p.groups = []appleBetaGroup{{ID: "group-1", Name: "Deploy External"}}
		p.replies["get_build"] = cannedReply(`{"data":{"id":"build-42"},"included":[{"type":"buildBetaDetails","attributes":{"externalBuildState":"READY_FOR_BETA_TESTING"}}]}`)
		if err := dbUpdateRelease(globalCtx.AppDB(), release.ID, map[string]any{"channel": "external"}); err != nil {
			t.Fatal(err)
		}
		runMobileWorker(t)
		fresh, meta := releaseWithMeta(t, release.ID)
		if fresh.ExternalStatus != "ready_for_beta_testing" || meta.TesterWarning != "" {
			t.Fatalf("release=%+v meta=%+v", fresh, meta)
		}
		if got := countIntegrationCalls(p.calls, "list_beta_testers"); got != 0 {
			t.Fatalf("list_beta_testers calls=%d, want 0", got)
		}
	})
}

func TestAppStoreTesterLookupFailureKeepsCheckWorking(t *testing.T) {
	p, release, _ := setupAppStoreSync(t)
	p.buildState = "READY_FOR_BETA_TESTING"
	p.fail["list_beta_testers"] = &sdk.ExecuteResult{Status: 500, Data: json.RawMessage(`{"errors":[{"code":"UNEXPECTED_ERROR"}]}`)}
	runMobileWorker(t)
	fresh, meta := releaseWithMeta(t, release.ID)
	if fresh.Status != "starting" || fresh.ExternalStatus != "ready_for_beta_testing" || fresh.Error != "" {
		t.Fatalf("failed lookup changed the release: %+v", fresh)
	}
	if meta.TesterAccess != "" || meta.TesterWarning != "" {
		t.Fatalf("failed lookup reported a tester state: %+v", meta)
	}
	var failures int
	if err := globalCtx.AppDB().QueryRow(`SELECT failures FROM release_sync_state WHERE release_id=?`, release.ID).Scan(&failures); err != nil || failures != 0 {
		t.Fatalf("failed lookup counted as a sync failure: failures=%d err=%v", failures, err)
	}
}

func TestAppStoreAvailabilityCheckKeepsConcurrentStatusChange(t *testing.T) {
	for _, state := range []string{"READY_FOR_BETA_TESTING", "IN_BETA_TESTING", "EXPIRED"} {
		t.Run(state, func(t *testing.T) {
			p, release, _ := setupAppStoreSync(t)
			p.buildState = state
			// The stop lands while Apple's answer is on its way back.
			p.onGetBuild = func() {
				if err := dbUpdateRelease(globalCtx.AppDB(), release.ID, map[string]any{"status": "stopped", "external_status": "stopped_by_user"}); err != nil {
					t.Error(err)
				}
			}
			runMobileWorker(t)
			fresh, _ := releaseWithMeta(t, release.ID)
			if fresh.Status != "stopped" || fresh.ExternalStatus != "stopped_by_user" || fresh.Error != "" {
				t.Fatalf("availability check overwrote the stop: %+v", fresh)
			}
			var checks int
			if err := globalCtx.AppDB().QueryRow(`SELECT idle_checks FROM release_sync_state WHERE release_id=?`, release.ID).Scan(&checks); err == nil {
				t.Fatal("stopped release kept a sync schedule")
			}
		})
	}
}

func TestAppStoreProcessingChecksSpaceOutUntilProcessed(t *testing.T) {
	p, release, now := setupAppStoreSync(t)
	db := globalCtx.AppDB()
	p.replies["list_builds"] = cannedReply(`{"data":[{"id":"build-42","attributes":{"version":"42","processingState":"PROCESSING"}}]}`)
	got := checkTimes(t, p, now, "list_builds", 70)
	if want := minutesOf(0, 1, 3, 8, 13, 23, 38, 53, 68); !reflect.DeepEqual(got, want) {
		t.Fatalf("checks at %v, want %v", got, want)
	}
	fresh, _ := releaseWithMeta(t, release.ID)
	if fresh.Status != "starting" || fresh.ExternalStatus != "processing" || fresh.Error != "" {
		t.Fatalf("release=%+v", fresh)
	}

	// Finished processing moves straight on and starts the waiting schedule.
	delete(p.replies, "list_builds")
	p.buildState = "READY_FOR_BETA_TESTING"
	if err := makeReleaseSyncDue(db, release.ID); err != nil {
		t.Fatal(err)
	}
	runMobileWorker(t)
	var checks int
	var observed string
	if err := db.QueryRow(`SELECT idle_checks,observed_status FROM release_sync_state WHERE release_id=?`, release.ID).Scan(&checks, &observed); err != nil {
		t.Fatal(err)
	}
	if checks != 1 || observed != "ready_for_beta_testing" {
		t.Fatalf("idle_checks=%d observed=%q", checks, observed)
	}
}

func TestAppStoreProcessingTimeoutIgnoresCheckSchedule(t *testing.T) {
	p, release, now := setupAppStoreSync(t)
	db := globalCtx.AppDB()
	p.replies["list_builds"] = cannedReply(`{"data":[]}`)
	runMobileWorker(t)
	if releaseSyncDue(db, release.ID) {
		t.Fatal("processing build due again straight away")
	}
	if _, err := db.Exec(`UPDATE releases SET created_at=? WHERE id=?`, now.Add(-49*time.Hour).Format(time.RFC3339), release.ID); err != nil {
		t.Fatal(err)
	}
	runMobileWorker(t)
	fresh, _ := releaseWithMeta(t, release.ID)
	if fresh.Status != "failed" || fresh.ExternalStatus != "processing_timeout" {
		t.Fatalf("release=%+v", fresh)
	}
}

func setupAppStoreReview(t *testing.T) (*appStoreSyncPlatform, *Release, *time.Time, func(string) *sdk.ExecuteResult) {
	t.Helper()
	p, release, now := setupAppStoreSync(t)
	meta := mobileReleaseMeta{Platform: "ios", AppID: "app-1", BuildNumber: "42", VersionName: "1.0", AppStoreVersionID: "version-1",
		BuildProcessed: true, Prepared: true, SubmitForReview: true, ReviewSubmitted: true}
	if err := dbUpdateRelease(globalCtx.AppDB(), release.ID, map[string]any{
		"channel": "production", "external_id": "build-42", "external_status": "waiting_for_review", "release_meta_json": mustJSON(meta),
	}); err != nil {
		t.Fatal(err)
	}
	version := func(state string) *sdk.ExecuteResult {
		return cannedReply(fmt.Sprintf(`{"data":{"id":"version-1","attributes":{"appVersionState":%q}}}`, state))
	}
	return p, release, now, version
}

func TestAppStoreReviewChecksSpaceOutUntilStateChanges(t *testing.T) {
	p, release, now, version := setupAppStoreReview(t)
	db := globalCtx.AppDB()
	p.replies["get_app_version"] = version("IN_REVIEW")
	got := checkTimes(t, p, now, "get_app_version", 112)
	if want := minutesOf(0, 1, 6, 21, 51, 111); !reflect.DeepEqual(got, want) {
		t.Fatalf("checks at %v, want %v", got, want)
	}

	// A new answer from Apple goes back to the start of the schedule.
	if err := makeReleaseSyncDue(db, release.ID); err != nil {
		t.Fatal(err)
	}
	p.replies["get_app_version"] = version("PENDING_DEVELOPER_RELEASE")
	runMobileWorker(t)
	var checks int
	if err := db.QueryRow(`SELECT idle_checks FROM release_sync_state WHERE release_id=?`, release.ID).Scan(&checks); err != nil || checks != 1 {
		t.Fatalf("state change kept the slow interval: idle_checks=%d err=%v", checks, err)
	}

	// Release ends the wait and removes the schedule.
	if err := makeReleaseSyncDue(db, release.ID); err != nil {
		t.Fatal(err)
	}
	p.replies["get_app_version"] = version("READY_FOR_SALE")
	runMobileWorker(t)
	fresh, _ := releaseWithMeta(t, release.ID)
	if fresh.Status != "live" || fresh.ExternalStatus != "ready_for_sale" {
		t.Fatalf("release=%+v", fresh)
	}
	if err := db.QueryRow(`SELECT idle_checks FROM release_sync_state WHERE release_id=?`, release.ID).Scan(&checks); err == nil {
		t.Fatal("finished release kept a sync schedule")
	}
}

func TestAppStoreReleaseRequestChecksOnNextRun(t *testing.T) {
	p, release, now, version := setupAppStoreReview(t)
	db := globalCtx.AppDB()
	p.replies["get_app_version"] = version("PENDING_DEVELOPER_RELEASE")
	p.replies["request_version_release"] = &sdk.ExecuteResult{Success: true, Status: 201, Data: json.RawMessage(`{"data":{"id":"request-1"}}`)}
	runMobileWorker(t)
	fresh, _ := releaseWithMeta(t, release.ID)
	if fresh.Status != "starting" || fresh.ExternalStatus != "pending_developer_release" {
		t.Fatalf("release=%+v", fresh)
	}
	for _, call := range p.calls {
		if call.Tool == "request_version_release" {
			t.Fatal("sync must not request a manual release")
		}
	}
	deferReleaseSync(db, release.ID, now.Add(time.Hour))
	updated, err := (&App{}).releaseApprovedMobileVersion(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status == "live" || updated.ExternalStatus != "release_requested" {
		t.Fatalf("accepted request must await availability: %+v", updated)
	}
	var requests int
	for _, call := range p.calls {
		if call.Tool == "request_version_release" {
			requests++
			if call.Input["version_id"] != "version-1" {
				t.Fatalf("release request=%+v", call)
			}
		}
	}
	if requests != 1 {
		t.Fatalf("release requests=%d, want 1", requests)
	}
	if !releaseSyncDue(db, release.ID) {
		t.Fatal("requested release still waits out the long interval")
	}
}

func TestAppStoreWaitScheduleOnlyWhileAppleMovesNext(t *testing.T) {
	assigned := mobileReleaseMeta{BuildProcessed: true, BetaGroupAssigned: "group-1"}
	submitted := mobileReleaseMeta{BuildProcessed: true, AppStoreVersionID: "v", Prepared: true, SubmitForReview: true, ReviewSubmitted: true}
	for _, tt := range []struct {
		name                  string
		provider, status, ext string
		channel               string
		meta                  mobileReleaseMeta
		want                  []time.Duration
	}{
		{"processing", "app_store_connect", "starting", "processing", "internal", mobileReleaseMeta{}, releaseProcessingSchedule},
		{"processing for production", "app_store_connect", "starting", "processing", "production", mobileReleaseMeta{}, releaseProcessingSchedule},
		{"assigned internal", "app_store_connect", "starting", "ready_for_beta_testing", "internal", assigned, releaseAwaitingAppleSchedule},
		{"assigned external", "app_store_connect", "starting", "waiting_for_beta_review", "external", assigned, releaseAwaitingAppleSchedule},
		{"submitted version", "app_store_connect", "starting", "in_review", "production", submitted, releaseAwaitingAppleSchedule},
		{"prepared without submission", "app_store_connect", "starting", "ready_for_review", "production", mobileReleaseMeta{BuildProcessed: true, AppStoreVersionID: "v", Prepared: true}, releaseAwaitingAppleSchedule},
		{"live but still in review", "app_store_connect", "live", "in_review", "production", submitted, releaseAwaitingAppleSchedule},
		{"submission unfinished", "app_store_connect", "starting", "ready_for_review", "production", mobileReleaseMeta{BuildProcessed: true, AppStoreVersionID: "v", Prepared: true, SubmitForReview: true}, nil},
		{"processed, group not accepted", "app_store_connect", "starting", "sync_error", "internal", mobileReleaseMeta{BuildProcessed: true}, nil},
		{"processed, version not prepared", "app_store_connect", "starting", "processing", "production", mobileReleaseMeta{BuildProcessed: true}, nil},
		{"live and finished", "app_store_connect", "live", "ready_for_sale", "production", submitted, nil},
		{"failed", "app_store_connect", "failed", "rejected", "production", submitted, nil},
		{"other provider", "google_play", "starting", "draft", "internal", assigned, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rel := &Release{Provider: tt.provider, Status: tt.status, ExternalStatus: tt.ext, Channel: tt.channel, ReleaseMetaJSON: mustJSON(tt.meta)}
			if got := releaseWaitSchedule(rel); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("schedule=%v, want %v", got, tt.want)
			}
		})
	}
}
