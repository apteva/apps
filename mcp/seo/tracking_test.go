package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func newRankTrackingTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return newSEOTestDB(t,
		"migrations/001_init.sql",
		"migrations/002_rankings_current_unique.sql",
		"migrations/003_rankings_daily_history.sql",
		"migrations/004_search_entities.sql",
		"migrations/005_search_engine_keyword_backfill.sql",
		"migrations/006_serp_consistency_and_retention.sql",
		"migrations/007_keyword_metric_jobs.sql",
		"migrations/008_daily_rank_tracking.sql",
		"migrations/009_rank_tracking_frequency.sql",
	)
}

func TestDailyRankTracking_QueuesOnceAndStoresFoundAndNotFound(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "project-1")
	db := newRankTrackingTestDB(t)
	locID := insertProviderTestLocation(t, db, "dataforseo", "google", 2840, "United States", "US", "en")
	keywordID, err := insertKeywordRecord(db, "project-1", "google", "seo api", locID, "US", "en")
	if err != nil {
		t.Fatal(err)
	}
	firstDomainID, err := upsertDomainRecord(db, "project-1", "example.com", "Example", locID)
	if err != nil {
		t.Fatal(err)
	}
	secondDomainID, err := upsertDomainRecord(db, "project-1", "missing.example", "Missing", locID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := enableRankTracker(db, "project-1", keywordID, 0, firstDomainID, "dataforseo", "desktop", "daily", 20, 100)
	if err != nil {
		t.Fatal(err)
	}
	second, err := enableRankTracker(db, "project-1", keywordID, 0, secondDomainID, "dataforseo", "desktop", "daily", 20, 100)
	if err != nil {
		t.Fatal(err)
	}
	stub := &yepPlatformStub{
		responses: map[string]json.RawMessage{
			"serp_organic_task_post": json.RawMessage(`{
				"status_code":20000,"status_message":"Ok","tasks":[{
					"id":"task-1","status_code":20100,"status_message":"Task Created","cost":0.0012,"result":[]
				}]
			}`),
			"serp_organic_task_get": json.RawMessage(`{
				"status_code":20000,"status_message":"Ok","tasks":[{
					"id":"task-1","status_code":20000,"status_message":"Ok","cost":0.0012,
					"result":[{"items":[
						{"type":"organic","rank_absolute":3,"title":"Example","url":"https://www.example.com/guide"},
						{"type":"organic","rank_absolute":4,"title":"Other","url":"https://other.example/page"}
					]}]
				}]
			}`),
		},
		identity: &sdk.InstallIdentity{Bindings: map[string]any{
			providerRole: map[string]any{"ids": []int64{42}, "default_id": int64(42)},
		}},
		connections: map[int64]*sdk.PlatformConnection{42: {ID: 42, AppSlug: "dataforseo"}},
	}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, nil, stub, nil)
	app := &App{}
	if err := app.runRankTrackingScheduler(t.Context(), ctx); err != nil {
		t.Fatalf("scheduler: %v", err)
	}
	var jobs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM serp_refresh_jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("refresh jobs = %d, want one shared provider query", jobs)
	}
	if err := app.runRankTrackingCollector(t.Context(), ctx); err != nil {
		t.Fatalf("collector: %v", err)
	}
	firstHistory, err := listRankHistory(db, "project-1", first.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstHistory) != 1 || !firstHistory[0].Found || firstHistory[0].Rank == nil || *firstHistory[0].Rank != 3 {
		t.Fatalf("found history = %+v", firstHistory)
	}
	secondHistory, err := listRankHistory(db, "project-1", second.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondHistory) != 1 || secondHistory[0].Found || secondHistory[0].Rank != nil || secondHistory[0].CheckedDepth < 20 {
		t.Fatalf("not-found history = %+v", secondHistory)
	}
	if got := countCall(stub.calls, "serp_organic_task_post"); got != 1 {
		t.Fatalf("queue submissions = %d, want 1", got)
	}
}

func TestRankTrackingBudget_BlocksBeforeProviderSubmission(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "project-1")
	db := newRankTrackingTestDB(t)
	locID := insertProviderTestLocation(t, db, "dataforseo", "google", 2840, "United States", "US", "en")
	keywordID, _ := insertKeywordRecord(db, "project-1", "google", "expensive query", locID, "US", "en")
	domainID, _ := upsertDomainRecord(db, "project-1", "example.com", "Example", locID)
	if _, err := enableRankTracker(db, "project-1", keywordID, 0, domainID, "dataforseo", "desktop", "daily", 20, 100); err != nil {
		t.Fatal(err)
	}
	if err := saveRankTrackingSettings(db, rankTrackingSettings{
		ProjectID: "project-1", Enabled: true, MonthlyBudgetUSD: 0, DailyDepth: 20, WeeklyDepth: 100,
	}); err != nil {
		t.Fatal(err)
	}
	settings, _ := getRankTrackingSettings(db, "project-1")
	if err := createDueRefreshJobs(db, "project-1", settings, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM serp_refresh_jobs`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "budget_blocked" {
		t.Fatalf("job status = %q, want budget_blocked", status)
	}
}

func TestRankTrackingFrequencySchedulesAndCost(t *testing.T) {
	now := time.Date(2026, time.January, 31, 18, 0, 0, 0, time.UTC)
	daily := time.Unix(nextTrackingRun(now, 7, "daily"), 0).UTC()
	weekly := time.Unix(nextTrackingRun(now, 7, "weekly"), 0).UTC()
	monthly := time.Unix(nextTrackingRun(now, 7, "monthly"), 0).UTC()

	if daily.Year() != 2026 || daily.Month() != time.February || daily.Day() != 1 {
		t.Fatalf("daily next run = %s, want 2026-02-01", daily)
	}
	if weekly.Year() != 2026 || weekly.Month() != time.February || weekly.Day() != 7 {
		t.Fatalf("weekly next run = %s, want 2026-02-07", weekly)
	}
	if monthly.Year() != 2026 || monthly.Month() != time.February || monthly.Day() != 28 {
		t.Fatalf("monthly next run = %s, want clamped 2026-02-28", monthly)
	}
	if got := estimateStandardSERPCost(20); math.Abs(got-0.00105) > 0.0000001 {
		t.Fatalf("top-20 estimate = %f, want 0.00105", got)
	}
	if got := estimateStandardSERPCost(100); math.Abs(got-0.00465) > 0.0000001 {
		t.Fatalf("top-100 estimate = %f, want 0.00465", got)
	}
}

func TestEnableRankTracker_ValidatesAndStoresFrequency(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "project-1")
	db := newRankTrackingTestDB(t)
	locID := insertProviderTestLocation(t, db, "dataforseo", "google", 2840, "United States", "US", "en")
	keywordID, _ := insertKeywordRecord(db, "project-1", "google", "weekly query", locID, "US", "en")
	domainID, _ := upsertDomainRecord(db, "project-1", "example.com", "Example", locID)

	tracker, err := enableRankTracker(db, "project-1", keywordID, 0, domainID, "dataforseo", "desktop", "weekly", 20, 100)
	if err != nil {
		t.Fatal(err)
	}
	if tracker.Frequency != "weekly" {
		t.Fatalf("frequency = %q, want weekly", tracker.Frequency)
	}
	if _, err := enableRankTracker(db, "project-1", keywordID, 0, domainID, "dataforseo", "desktop", "yearly", 20, 100); err == nil {
		t.Fatal("expected invalid frequency error")
	}
}

func TestRankTrackingPatch_ChangesFrequencyWithoutDisabling(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "project-1")
	db := newRankTrackingTestDB(t)
	locID := insertProviderTestLocation(t, db, "dataforseo", "google", 2840, "United States", "US", "en")
	keywordID, _ := insertKeywordRecord(db, "project-1", "google", "monthly query", locID, "US", "en")
	domainID, _ := upsertDomainRecord(db, "project-1", "example.com", "Example", locID)
	tracker, err := enableRankTracker(db, "project-1", keywordID, 0, domainID, "dataforseo", "desktop", "daily", 20, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE serp_trackers SET next_run_at = 12345 WHERE id = ?`, tracker.ID); err != nil {
		t.Fatal(err)
	}

	previousCtx := globalCtx
	globalCtx = sdk.NewAppCtxForTest(nil, db, nil, nil, nil)
	t.Cleanup(func() { globalCtx = previousCtx })
	request := httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/rank-tracking/%d?project_id=project-1", tracker.ID), strings.NewReader(`{"frequency":"monthly"}`))
	recorder := httptest.NewRecorder()
	(&App{}).handleRankTrackingItem(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	trackers, err := listRankTrackers(db, "project-1", keywordID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trackers) != 1 || !trackers[0].Enabled || trackers[0].Frequency != "monthly" || trackers[0].NextRunAt != 0 {
		t.Fatalf("updated tracker = %+v", trackers)
	}
}

func TestFindTargetRank_MatchesDomainSubdomainAndPageExactly(t *testing.T) {
	rankThree, rankFive := int64(3), int64(5)
	rows := []SearchRanking{
		{Rank: &rankFive, URL: "https://example.com/other"},
		{Rank: &rankThree, URL: "https://blog.example.com/guide"},
	}
	rank, rankURL := findTargetRank(SearchEntity{SearchEngine: "google", EntityType: "domain", Identifier: "example.com"}, rows)
	if rank == nil || *rank != 3 || rankURL != rows[1].URL {
		t.Fatalf("domain rank = %v, url = %q", rank, rankURL)
	}
	rank, _ = findTargetRank(SearchEntity{SearchEngine: "google", EntityType: "page", Identifier: "http://example.com/other/"}, rows)
	if rank == nil || *rank != 5 {
		t.Fatalf("page rank = %v, want 5", rank)
	}
}

func countCall(calls []string, want string) int {
	count := 0
	for _, call := range calls {
		if call == want {
			count++
		}
	}
	return count
}
