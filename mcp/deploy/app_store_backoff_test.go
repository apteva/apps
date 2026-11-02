package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// appStoreSyncPlatform wraps testFlightPlatform with forced provider failures
// and a build that never reaches TestFlight testers.
type appStoreSyncPlatform struct {
	*testFlightPlatform
	fail  map[string]*sdk.ExecuteResult
	stuck bool
}

func (p *appStoreSyncPlatform) ExecuteIntegrationTool(id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	if reply := p.fail[tool]; reply != nil {
		p.calls = append(p.calls, integrationCall{Tool: tool, Input: input})
		return reply, nil
	}
	if tool == "get_build" && p.stuck {
		p.calls = append(p.calls, integrationCall{Tool: tool, Input: input})
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"data":{"id":"build-42"},"included":[{"type":"buildBetaDetails","attributes":{"internalBuildState":"READY_FOR_BETA_TESTING"}}]}`)}, nil
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

func TestStuckReleasesStayWithinCallBudget(t *testing.T) {
	p, first, now := setupAppStoreSync(t)
	db := globalCtx.AppDB()
	// Four releases on separate deployments: two failing with Apple 500s and
	// two whose processed build never reaches testers.
	releases := []*Release{first}
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
		releases = append(releases, rel)
	}
	p.stuck = true
	runs := 10
	for i := 0; i < runs; i++ {
		runMobileWorker(t)
		*now = now.Add(time.Minute)
	}
	// Unbounded, four releases would cost about 4 calls each per minute (160).
	// Stuck builds reuse the processed build and cached group: one get_build
	// per run after the first. Budget: first run 4×4 plus 4 per later run.
	if budget := 16 + 4*(runs-1); len(p.calls) > budget {
		t.Fatalf("calls=%d exceed budget %d", len(p.calls), budget)
	}

	// Apple errors on every release back off on the per-release schedule.
	p.calls = nil
	p.fail["get_build"] = &sdk.ExecuteResult{Status: 500, Data: json.RawMessage(`{"errors":[{"code":"UNEXPECTED_ERROR"}]}`)}
	for i := 0; i < runs; i++ {
		runMobileWorker(t)
		*now = now.Add(time.Minute)
	}
	// Schedule 1m, 2m, 5m: at most 4 attempts per release in ten minutes.
	if len(p.calls) > 4*len(releases) {
		t.Fatalf("failing releases made %d calls in %d runs", len(p.calls), runs)
	}
}
