package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func testPlayCoordinator(t *testing.T) (*googlePlayCoordinator, *sdk.BoundIntegration, *time.Time) {
	t.Helper()
	db := openSchemaDB(t)
	t.Cleanup(func() { db.Close() })
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	c := &googlePlayCoordinator{db: db, dir: t.TempDir(), now: func() time.Time { return now }, edits: map[string]func(){}}
	return c, &sdk.BoundIntegration{ConnectionID: 88, AppSlug: "google-play-developer"}, &now
}
func playResult(input map[string]any) (json.RawMessage, error) {
	return json.RawMessage(`{"id":"edit-1"}`), nil
}

func TestGoogleObservationsCoalescePollingAndScope(t *testing.T) {
	c, bound, now := testPlayCoordinator(t)
	var requests atomic.Int32
	load := func() (json.RawMessage, error) {
		requests.Add(1)
		time.Sleep(15 * time.Millisecond)
		return json.RawMessage(`{"status":"completed"}`), nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, obs, err := c.observe(bound, "com.example.app", "track/internal", load)
			if err != nil || string(data) != `{"status":"completed"}` || obs.Stale {
				t.Errorf("observation=%s %+v err=%v", data, obs, err)
			}
		}()
	}
	wg.Wait()
	if requests.Load() != 1 {
		t.Fatalf("polling issued %d refreshes", requests.Load())
	}
	_, first, err := c.observe(bound, "com.example.app", "track/internal", load)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	_, again, err := c.observe(bound, "com.example.app", "track/internal", load)
	if err != nil || !again.Cached || first.CheckedAt != again.CheckedAt {
		t.Fatal("cache falsely refreshed observation age", err)
	}
	for _, scope := range []struct {
		connection int64
		pkg, key   string
	}{{88, "com.example.other", "track/internal"}, {89, "com.example.app", "track/internal"}, {88, "com.example.app", "track/production"}} {
		copy := *bound
		copy.ConnectionID = scope.connection
		if _, _, err := c.observe(&copy, scope.pkg, scope.key, load); err != nil {
			t.Fatal(err)
		}
	}
	if requests.Load() != 4 {
		t.Fatal("cache leaked across account, package or track")
	}
	*now = now.Add(googlePlayObservationTTL)
	if _, _, err := c.observe(bound, "com.example.app", "track/internal", load); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 5 {
		t.Fatal("expired observation did not refresh")
	}
}

const dailyEditQuotaResponse = `{"error":{"code":403,"message":"Daily edit creation quota exceeded.","status":"PERMISSION_DENIED"}}`

func TestGoogleQuotaBackoffPersistsAcrossDatabaseReopen(t *testing.T) {
	t.Run("429-retry-after", func(t *testing.T) {
		testGoogleQuotaDatabaseReopen(t, 429, map[string]string{"retry-after": "3600"}, nil)
	})
	t.Run("403-daily-edit", func(t *testing.T) {
		testGoogleQuotaDatabaseReopen(t, 403, nil, json.RawMessage(dailyEditQuotaResponse))
	})
}

func testGoogleQuotaDatabaseReopen(t *testing.T, status int, headers map[string]string, body json.RawMessage) {
	path := filepath.Join(t.TempDir(), "deploy.db")
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	db := open()
	for _, file := range testMigrationFiles {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	c := &googlePlayCoordinator{db: db, dir: dir, now: func() time.Time { return now }, edits: map[string]func(){}}
	bound := &sdk.BoundIntegration{ConnectionID: 88, AppSlug: "google-play-developer"}
	if _, _, err := c.observe(bound, "com.example.app", "track", func() (json.RawMessage, error) { return json.RawMessage(`{"old":true}`), nil }); err != nil {
		t.Fatal(err)
	}
	if err := c.recordBackoff(88, status, headers, body); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open()
	defer db.Close()
	restarted := &googlePlayCoordinator{db: db, dir: dir, now: func() time.Time { return now }, edits: map[string]func(){}}
	now = now.Add(10 * time.Minute)
	requests := 0
	load := func() (json.RawMessage, error) { requests++; return json.RawMessage(`{"new":true}`), nil }
	data, obs, err := restarted.observe(bound, "com.example.app", "track", load)
	if err != nil || string(data) != `{"old":true}` || !obs.Stale || requests != 0 || obs.NextRetryAt == "" {
		t.Fatalf("restart bypassed quota: %s %+v %v", data, obs, err)
	}
	if obs.CheckedAt != "2026-10-28T12:00:00Z" {
		t.Fatal("stale timestamp rewritten", obs)
	}
	if _, err := restarted.execute(bound, "create_edit", map[string]any{"packageName": "another.app"}, playResult); err == nil {
		t.Fatal("another app bypassed account quota")
	}
	other := *bound
	other.ConnectionID = 89
	if _, _, err := restarted.observe(&other, "com.example.app", "track", load); err != nil {
		t.Fatal("quota leaked to unrelated account", err)
	}
	now = now.Add(time.Hour)
	_, obs, err = restarted.observe(bound, "com.example.app", "track", load)
	if err != nil || obs.Stale || obs.Cached || requests != 2 {
		t.Fatalf("did not recover: %+v %d %v", obs, requests, err)
	}
}

func TestGoogleRetryAfterAndQuotaClassification(t *testing.T) {
	c, _, now := testPlayCoordinator(t)
	for _, tc := range []struct {
		name    string
		status  int
		headers map[string]string
		body    string
		minimum time.Duration
		quota   bool
	}{
		{"seconds", 429, map[string]string{"Retry-After": "600"}, `{}`, 10 * time.Minute, true},
		{"http-date", 429, map[string]string{"Retry-After": now.Add(2 * time.Hour).Format(http.TimeFormat)}, `{}`, 2 * time.Hour, true},
		{"retry-info", 429, nil, `{"error":{"details":[{"retryDelay":"7200s"}]}}`, 2 * time.Hour, true},
		{"daily", 403, nil, `{"error":{"errors":[{"reason":"dailyLimitExceeded"}]}}`, time.Hour, true},
		{"daily-edit-permission-denied", 403, nil, dailyEditQuotaResponse, time.Hour, true},
		{"daily-edit-retry-after", 403, map[string]string{"Retry-After": "7200"}, dailyEditQuotaResponse, 2 * time.Hour, true},
		{"daily-edit-normalized-wrapper", 403, nil, `{"error":"permission_denied","provider_error":{"message":"Daily edit creation quota exceeded.","status":"PERMISSION_DENIED"}}`, time.Hour, true},
		{"daily-edit-legacy-forbidden", 403, nil, `{"error":{"errors":[{"reason":"forbidden","message":"Daily edit creation quota exceeded."}]}}`, time.Hour, true},
		{"daily-edit-whitespace", 403, nil, `{"error":{"message":"  DAILY  EDIT CREATION QUOTA EXCEEDED.  ","status":"PERMISSION_DENIED"}}`, time.Hour, true},
		{"daily-reason-snake-case", 403, nil, `{"error":{"details":[{"reason":"DAILY_LIMIT_EXCEEDED"}]}}`, time.Hour, true},
		{"permission-mentions-quota", 403, nil, `{"error":{"status":"PERMISSION_DENIED","message":"User does not have permission to access the quota project."}}`, 0, false},
		{"unrelated-diagnostic", 403, nil, `{"error":{"status":"PERMISSION_DENIED","message":"Missing permission"},"description":"Daily edit creation quota exceeded."}`, 0, false},
		{"rate-limit", 403, nil, `{"error":{"errors":[{"reason":"userRateLimitExceeded"}]}}`, time.Minute, true},
		{"unavailable", 503, map[string]string{"Retry-After": "600"}, `{}`, 10 * time.Minute, true},
		{"permission", 403, nil, `{"error":{"status":"PERMISSION_DENIED"}}`, 0, false},
		{"auth", 401, nil, `{}`, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if googlePlayBackoffRequired(tc.status, json.RawMessage(tc.body)) != tc.quota {
				t.Fatal("wrong quota classification")
			}
			if err := c.recordBackoff(100, tc.status, tc.headers, json.RawMessage(tc.body)); err != nil {
				t.Fatal(err)
			}
			err := c.before(100)
			if tc.quota {
				var paused *googlePlayPausedError
				if !errors.As(err, &paused) || paused.Until.Before(now.Add(tc.minimum)) {
					t.Fatal("ignored requested delay", err)
				}
			} else if err != nil {
				t.Fatal("backed off authentication error", err)
			}
			c.db.Exec(`DELETE FROM google_play_backoff`)
		})
	}
	if err := c.recordBackoff(100, 429, nil, nil); err != nil {
		t.Fatal(err)
	}
	first := c.before(100).(*googlePlayPausedError).Until
	*now = first.Add(time.Second)
	if err := c.recordBackoff(100, 429, nil, nil); err != nil {
		t.Fatal(err)
	}
	second := c.before(100).(*googlePlayPausedError).Until
	if second.Sub(*now) < 2*time.Minute {
		t.Fatal("backoff did not grow")
	}
}

func TestGoogleEditTransactionsProtectCachedObservationsAndInvalidateOnCommit(t *testing.T) {
	c, bound, _ := testPlayCoordinator(t)
	requests := 0
	load := func() (json.RawMessage, error) { requests++; return json.RawMessage(`{"value":1}`), nil }
	if _, _, err := c.observe(bound, "app", "track", load); err != nil {
		t.Fatal(err)
	}
	if _, err := c.execute(bound, "create_edit", map[string]any{"packageName": "app"}, playResult); err != nil {
		t.Fatal(err)
	}
	// A second coordinator represents another process using the same install.
	other := &googlePlayCoordinator{db: c.db, dir: c.dir, now: c.now, edits: map[string]func(){}}
	_, obs, err := other.observe(bound, "app", "track", load)
	if err != nil || !obs.Stale || requests != 1 {
		t.Fatal("observation entered an active write transaction", obs, err)
	}
	commits := 0
	if _, err := c.execute(bound, "commit_edit", map[string]any{"packageName": "app", "editId": "edit-1"}, func(map[string]any) (json.RawMessage, error) { commits++; return json.RawMessage(`{}`), nil }); err != nil {
		t.Fatal(err)
	}
	_, obs, err = other.observe(bound, "app", "track", load)
	if err != nil || obs.Cached || requests != 2 || commits != 1 {
		t.Fatal("committed publication did not invalidate cache", err)
	}
	// A failed/ambiguous commit must not be automatically replayed.
	if _, err := c.execute(bound, "create_edit", map[string]any{"packageName": "app"}, playResult); err != nil {
		t.Fatal(err)
	}
	_, err = c.execute(bound, "commit_edit", map[string]any{"packageName": "app", "editId": "edit-1"}, func(map[string]any) (json.RawMessage, error) {
		commits++
		return nil, &integrationToolError{Status: 429, Headers: map[string]string{"Retry-After": "120"}}
	})
	if err == nil {
		t.Fatal("lost quota failure")
	}
	_, _ = c.execute(bound, "delete_edit", map[string]any{"packageName": "app", "editId": "edit-1"}, playResult)
	if unlock, err := c.lock("observation/88/app", 0); err != nil {
		t.Fatal("failed cleanup leaked transaction lock", err)
	} else {
		unlock()
	}
	if commits != 2 {
		t.Fatal("mutation replayed")
	}
}

type quotaPlayPlatform struct {
	tk.BasePlatformClient
	mu           sync.Mutex
	calls        []integrationCall
	status       int
	failureReply *sdk.ExecuteResult
}

func (p *quotaPlayPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"play_store": int64(88)}}, nil
}
func (p *quotaPlayPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "google-play-developer", Status: "active"}, nil
}
func (p *quotaPlayPlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, integrationCall{Tool: tool, Input: input})
	if _, leaked := input["_deploy_observation"]; leaked {
		return nil, errors.New("internal marker leaked to provider")
	}
	if p.failureReply != nil {
		return p.failureReply, nil
	}
	if p.status != 0 {
		return &sdk.ExecuteResult{Status: p.status, Headers: map[string]string{"Retry-After": "600"}, Data: json.RawMessage(`{"error":{"status":"RESOURCE_EXHAUSTED"}}`)}, nil
	}
	data := json.RawMessage(`{}`)
	switch tool {
	case "create_edit":
		data = json.RawMessage(`{"id":"edit-1"}`)
	case "get_track":
		data = json.RawMessage(`{"releases":[{"versionCodes":["42"],"status":"completed"}]}`)
	case "get_track_testers":
		data = json.RawMessage(`{"googleGroups":["testers@example.com"]}`)
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: data}, nil
}
func setupQuotaPlatform(t *testing.T) (*quotaPlayPlatform, *googlePlayCoordinator, *sdk.BoundIntegration) {
	t.Helper()
	p := &quotaPlayPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("quota-test"), tk.WithPlatform(p))
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { googlePlayCoordinators.Delete(ctx.AppDB()); globalCtx = old })
	c := googlePlayCoordinatorForContext()
	return p, c, &sdk.BoundIntegration{ConnectionID: 88, AppSlug: "google-play-developer"}
}

func TestGoogleRealStatusPathsShareCacheAndPreserveStaleEvidence(t *testing.T) {
	p, c, bound := setupQuotaPlatform(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	d, err := dbCreateDeployment(c.db, "quota-test", CreateDeploymentInput{Name: "android", TargetKind: "android", SourceKind: "local", SourceRef: "/src", Framework: "android", TargetConfigJSON: `{"package_name":"com.example.app"}`})
	if err != nil {
		t.Fatal(err)
	}
	build, err := dbCreateBuild(c.db, d.ID, "android", "")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := dbCreateRelease(c.db, d.ID, build.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta := mobileReleaseMeta{Platform: "android", PackageName: "com.example.app", VersionCode: "42", Connections: map[string]int64{"play_store": 88}}
	if err = dbUpdateRelease(c.db, rel.ID, map[string]any{"provider": "google_play", "channel": "internal", "release_meta_json": mustJSON(meta)}); err != nil {
		t.Fatal(err)
	}
	rel, _ = dbGetRelease(c.db, rel.ID)
	for i := 0; i < 4; i++ {
		if err := (&App{}).syncMobileReleaseState(rel); err != nil {
			t.Fatal(err)
		}
	}
	target := distributionTarget{Platform: "android", PackageName: "com.example.app", Channel: "internal"}
	for i := 0; i < 4; i++ {
		if _, err := readAndroidDistribution(bound, target); err != nil {
			t.Fatal(err)
		}
	}
	doc := StoreDocument{}
	cfg := &MobileStoreConfig{}
	for i := 0; i < 4; i++ {
		if _, err := (&App{}).observeGoogleStoreConfig(d, doc, cfg); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.db.Exec(`UPDATE google_play_observations SET payload_json=json_set(payload_json,'$.readiness.listing.status','verified') WHERE observation_key LIKE 'listing/%'`); err != nil {
		t.Fatal(err)
	}
	if got := countIntegrationCalls(p.calls, "create_edit"); got != 3 {
		t.Fatalf("status polling opened %d edits", got)
	}
	now = now.Add(googlePlayObservationTTL + time.Second)
	p.status = 429
	if err := (&App{}).syncMobileReleaseState(rel); err != nil {
		t.Fatal("lost last known state", err)
	}
	before := len(p.calls)
	for i := 0; i < 4; i++ {
		if _, err := readAndroidDistribution(bound, target); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.calls) != before {
		t.Fatal("tester polling ignored account backoff")
	}
	listing, err := (&App{}).observeGoogleStoreConfig(d, doc, cfg)
	if err != nil {
		t.Fatal(err)
	}
	check := listing["readiness"].(map[string]any)["listing"].(map[string]any)
	if check["status"] == "verified" || !listing["observation"].(*providerObservation).Stale || len(p.calls) != before {
		t.Fatal("stale listing was presented as verified", check)
	}

	fresh, _ := dbGetRelease(c.db, rel.ID)
	var saved mobileReleaseMeta
	json.Unmarshal([]byte(fresh.ReleaseMetaJSON), &saved)
	if fresh.Status != "live" || saved.Observation == nil || !saved.Observation.Stale || saved.Availability.CheckedAt != "2026-10-28T12:00:00Z" {
		t.Fatalf("stale evidence became fresh: %+v", saved)
	}
	state, err := readAndroidDistribution(bound, target)
	if err != nil || !state.Observation.Stale || state.Synced || state.Observation.NextRetryAt == "" {
		t.Fatal("cached testers claimed fresh verification", state, err)
	}
}

func TestGoogleCloudUploadDefersReadsWithoutCreatingEdits(t *testing.T) {
	c, bound, _ := testPlayCoordinator(t)
	d, err := dbCreateDeployment(c.db, "p1", CreateDeploymentInput{Name: "cloud", TargetKind: "android", Framework: "android", SourceKind: "local", SourceRef: "/src"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := dbCreateBuildForEnvBackend(c.db, d.ID, 0, "android", "", "bitrise", `{"artifact_mode":"store_upload"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err = dbUpdateBuild(c.db, b.ID, map[string]any{"external_job_id": "job-1", "status": "running", "target_config_json": `{"package_name":"com.example.app","connections":{"play_store":88}}`}); err != nil {
		t.Fatal(err)
	}
	called := false
	_, _, err = c.observe(bound, "com.example.app", "track", func() (json.RawMessage, error) { called = true; return nil, nil })
	var paused *googlePlayPausedError
	if !errors.As(err, &paused) || called {
		t.Fatal("polling entered a cloud upload", err)
	}
}

func TestGoogleObservationCannotRepopulateCacheAfterPublication(t *testing.T) {
	c, bound, _ := testPlayCoordinator(t)
	observing, finishObservation := make(chan struct{}), make(chan struct{})
	observed := make(chan error, 1)
	go func() {
		_, _, err := c.observe(bound, "app", "track", func() (json.RawMessage, error) {
			close(observing)
			<-finishObservation
			return json.RawMessage(`{"old":true}`), nil
		})
		observed <- err
	}()
	<-observing
	writerStarted, writerCreated := make(chan struct{}), make(chan error, 1)
	go func() {
		close(writerStarted)
		_, err := c.execute(bound, "create_edit", map[string]any{"packageName": "app"}, playResult)
		writerCreated <- err
	}()
	<-writerStarted
	select {
	case err := <-writerCreated:
		close(finishObservation)
		t.Fatalf("writer entered before observation persistence: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(finishObservation)
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
	if err := <-writerCreated; err != nil {
		t.Fatal(err)
	}
	if _, err := c.execute(bound, "commit_edit", map[string]any{"packageName": "app", "editId": "edit-1"}, playResult); err != nil {
		t.Fatal(err)
	}
	data, obs, err := c.observe(bound, "app", "track", func() (json.RawMessage, error) { return json.RawMessage(`{"new":true}`), nil })
	if err != nil || obs.Cached || string(data) != `{"new":true}` {
		t.Fatalf("pre-commit observation survived: %s %+v %v", data, obs, err)
	}
}

func TestGoogleStreamingUploadPreservesRetryAfter(t *testing.T) {
	_, c, bound := setupQuotaPlatform(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":{"status":"RESOURCE_EXHAUSTED"}}`)
	}))
	defer server.Close()
	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	before := c.now()
	err = googlePlayHTTPError(bound, "upload_bundle", response.StatusCode, response.Header, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("lost stream error")
	}
	paused, ok := c.before(88).(*googlePlayPausedError)
	if !ok || paused.Until.Before(before.Add(time.Hour)) {
		t.Fatal("stream upload ignored Retry-After")
	}
}

func TestGoogleDailyEditQuotaStopsIntegrationCallsAcrossCoordinatorRestart(t *testing.T) {
	p, c, bound := setupQuotaPlatform(t)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	p.failureReply = &sdk.ExecuteResult{Status: 403, Data: json.RawMessage(dailyEditQuotaResponse)}
	_, err := executeIntegration(bound, "create_edit", map[string]any{"packageName": "com.example.app"})
	if err == nil {
		t.Fatal("lost the provider failure")
	}
	var paused *googlePlayPausedError
	if !errors.As(c.before(bound.ConnectionID), &paused) || paused.Until.Before(now.Add(time.Hour)) {
		t.Fatal("daily-edit response did not save a daily cooldown")
	}
	googlePlayCoordinators.Delete(c.db)
	restarted := googlePlayCoordinatorForContext()
	restarted.now = c.now
	for i := 0; i < 8; i++ {
		_, err = executeIntegration(bound, "create_edit", map[string]any{"packageName": "com.example.another"})
		if !errors.As(err, &paused) {
			t.Fatal("restart did not retain account backoff", err)
		}
	}
	if len(p.calls) != 1 {
		t.Fatalf("cooldown allowed %d upstream calls", len(p.calls))
	}
}
