package main

import (
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuditEmptySERPDoesNotReviveLegacyRankings(t *testing.T) {
	db := newRankTrackingTestDB(t)
	loc := insertTestLocation(t, db, "google", 2840)
	keyword, _ := insertKeywordRecord(db, "p", "google", "seo", loc, "US", "en")
	domain, _ := upsertDomainRecord(db, "p", "example.com", "", loc)
	for _, q := range []string{
		fmt.Sprintf(`INSERT INTO rankings (domain_id, keyword_id, location_id, provider, ts, observed_date, rank, rank_url, device) VALUES (%d,%d,%d,'dataforseo',100,'2026-07-01',3,'https://example.com','desktop')`, domain, keyword, loc),
		fmt.Sprintf(`INSERT INTO ranking_observations (domain_id, location_id, provider, device, ts, observed_date, result_count) VALUES (%d,%d,'dataforseo','desktop',100,'2026-07-01',1)`, domain, loc),
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := persistSERPSnapshot(db, "p", "google", &keyword, "seo", loc, "dataforseo", 20, &providerSERPResponse{ResultRaw: json.RawMessage(`{"items":[]}`), Raw: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := searchRankingsForKeywordsProvider(db, "p", []int64{keyword}, 0, 20, false, "dataforseo")
	if err != nil || len(rows) != 0 {
		t.Fatalf("empty latest SERP returned stale rows: %+v, %v", rows, err)
	}
}

func TestAuditAllProvidersKeepsLatestOfEach(t *testing.T) {
	db := newRankTrackingTestDB(t)
	loc := insertTestLocation(t, db, "google", 2840)
	keyword, _ := insertKeywordRecord(db, "p", "google", "seo", loc, "US", "en")
	for _, provider := range []string{"dataforseo", "yepapi", "yepapi"} {
		_, _, err := persistSERPSnapshot(db, "p", "google", &keyword, "seo", loc, provider, 20, &providerSERPResponse{ResultRaw: json.RawMessage(`{"items":[{"type":"organic","rank_absolute":1,"url":"https://example.com"}]}`), Raw: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := searchRankingsForKeywordsProvider(db, "p", []int64{keyword}, 0, 20, false, "")
	if err != nil || len(rows) != 2 {
		t.Fatalf("want latest of both providers, got %+v, %v", rows, err)
	}
}

func TestAuditFailedPaidJobStillConsumesBudget(t *testing.T) {
	db := newRankTrackingTestDB(t)
	loc := insertTestLocation(t, db, "google", 2840)
	keyword, _ := insertKeywordRecord(db, "p", "google", "seo", loc, "US", "en")
	domain, _ := upsertDomainRecord(db, "p", "example.com", "", loc)
	if _, err := enableRankTracker(db, "p", keyword, 0, domain, "dataforseo", "desktop", "daily", 20, 100); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	_, err := db.Exec(`INSERT INTO serp_refresh_jobs (project_id,keyword_id,location_id,provider,depth,observed_date,status,provider_task_id,actual_cost_usd,submitted_at) VALUES ('p',?,?,'dataforseo',20,'2026-08-11','failed','paid-task',5,?)`, keyword, loc, now.Add(-24*time.Hour).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if err := createDueRefreshJobs(db, "p", rankTrackingSettings{MonthlyBudgetUSD: 5}, now); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM serp_refresh_jobs WHERE observed_date='2026-08-12'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "budget_blocked" {
		t.Fatalf("paid failure freed budget: %s", status)
	}
}

func TestAuditSubmissionRetainsChargedCost(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "p")
	db := newRankTrackingTestDB(t)
	loc := insertTestLocation(t, db, "google", 2840)
	keyword, _ := insertKeywordRecord(db, "p", "google", "seo", loc, "US", "en")
	domain, _ := upsertDomainRecord(db, "p", "example.com", "", loc)
	if _, err := enableRankTracker(db, "p", keyword, 0, domain, "dataforseo", "desktop", "daily", 20, 100); err != nil {
		t.Fatal(err)
	}
	stub := &yepPlatformStub{responses: map[string]json.RawMessage{
		"serp_organic_task_post": json.RawMessage(`{"status_code":20000,"tasks":[{"id":"paid","status_code":20100,"cost":0.02}]}`),
	}, identity: &sdk.InstallIdentity{Bindings: map[string]any{providerRole: map[string]any{"ids": []int64{42}, "default_id": int64(42)}}}, connections: map[int64]*sdk.PlatformConnection{42: {ID: 42, AppSlug: "dataforseo"}}}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, nil, stub, nil)
	if err := (&App{}).runRankTrackingScheduler(t.Context(), ctx); err != nil {
		t.Fatal(err)
	}
	var cost float64
	if err := db.QueryRow(`SELECT actual_cost_usd FROM serp_refresh_jobs`).Scan(&cost); err != nil {
		t.Fatal(err)
	}
	if cost != 0.02 {
		t.Fatalf("charged cost lost: %v", cost)
	}
	stub.responses["serp_organic_task_get"] = json.RawMessage(`{"status_code":20000,"tasks":[{"id":"paid","status_code":20000,"cost":0,"result":[{"items":[]}]}]}`)
	if err := (&App{}).runRankTrackingCollector(t.Context(), ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT actual_cost_usd FROM serp_refresh_jobs`).Scan(&cost); err != nil {
		t.Fatal(err)
	}
	if cost != 0.02 {
		t.Fatalf("collection overwrote charged cost: %v", cost)
	}
}

func BenchmarkAuditBacklinkMovement(b *testing.B) {
	// Use the same migrated SQLite fixture as the behavior tests.
	db := newSEOTestDB(b, "migrations/001_init.sql", "migrations/010_backlink_summary_index.sql")
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO domains (project_id,host) VALUES ('p','example.com');
 WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<100000)
 INSERT INTO backlinks (domain_id,provider,source_url,dest_url,first_seen,last_seen,is_lost)
 SELECT 1,'dataforseo','https://source/'||x,'https://example.com',1780000000+x*60,1780000000+x*60,x%2 FROM n`); err != nil {
		b.Fatal(err)
	}
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := cachedBacklinkMovement(db, 1, "dataforseo", 90, now); err != nil {
			b.Fatal(err)
		}
	}
}

func TestAuditMetricPreflightLeavesNoJobs(t *testing.T) {
	db := newRankTrackingTestDB(t)
	loc := insertTestLocation(t, db, "google", 2840)
	keyword, _ := insertKeywordRecord(db, "p", "google", "seo", loc, "US", "en")
	stub := &yepPlatformStub{responses: map[string]json.RawMessage{
		"account_info": json.RawMessage(`{"status_code":20000,"tasks":[{"status_code":20000,"result":[{"money":{"balance":0}}]}]}`),
	}, identity: &sdk.InstallIdentity{Bindings: map[string]any{providerRole: map[string]any{"ids": []int64{42}, "default_id": int64(42)}}}, connections: map[int64]*sdk.PlatformConnection{42: {ID: 42, AppSlug: "dataforseo"}}}
	manifest := (&App{}).Manifest()
	previous := globalCtx
	globalCtx = sdk.NewAppCtxForTest(&manifest, db, nil, stub, nil)
	t.Cleanup(func() { globalCtx = previous })
	for _, provider := range []string{"dataforseo", "yepapi"} {
		request := httptest.NewRequest(http.MethodPost, "/keyword-metric-jobs?project_id=p", strings.NewReader(fmt.Sprintf(`{"keyword_ids":[%d],"provider":%q}`, keyword, provider)))
		recorder := httptest.NewRecorder()
		(&App{}).handleKeywordMetricJobs(recorder, request)
		expected := http.StatusPaymentRequired
		if provider == "yepapi" {
			expected = http.StatusBadRequest
		}
		if recorder.Code != expected {
			t.Fatalf("status %d, want %d: %s", recorder.Code, expected, recorder.Body.String())
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM keyword_metric_jobs`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("rejected request left %d queued jobs", count)
		}
	}
}

func TestAuditBacklinkBucketsMatchIndividualLinks(t *testing.T) {
	db := newRankTrackingTestDB(t)
	applySEOMigration(t, db, "migrations/010_backlink_summary_index.sql")
	domain, err := upsertDomainRecord(db, "p", "example.com", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 12, 23, 0, 0, 0, time.UTC)
	from := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC).Unix()
	end := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC).Unix()
	expected := make([]BacklinkMovementPoint, 10)
	var active, lost, knownFirst, knownLost int64
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 1500; i++ {
		first := from + int64((i*7919)%1500000) - 200000
		last := from + int64((i*3571)%1500000) - 200000
		var firstArg, lastArg any = first, last
		if i%7 == 0 {
			firstArg = nil
			first = 0
		}
		if i%11 == 0 {
			lastArg = nil
			last = 0
		}
		if i%17 == 0 {
			firstArg = int64(0)
			first = 0
		}
		isLost := int64(i % 2)
		if _, err := tx.Exec(`INSERT INTO backlinks (domain_id,provider,source_url,dest_url,first_seen,last_seen,is_lost) VALUES (?,'dataforseo',?,'https://example.com',?,?,?)`, domain, fmt.Sprintf("https://source/%d", i), firstArg, lastArg, isLost); err != nil {
			t.Fatal(err)
		}
		if isLost == 0 {
			active++
		} else {
			lost++
		}
		if first > 0 {
			knownFirst++
		}
		if isLost != 0 && last > 0 {
			knownLost++
		}
		if first >= from && first < end {
			expected[(first-from)/86400].Gained++
		}
		if isLost != 0 && last >= from && last < end {
			expected[(last-from)/86400].Lost++
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err := cachedBacklinkMovement(db, domain, "dataforseo", 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveLinks != active || got.LostLinks != lost || got.KnownFirstSeen != knownFirst || got.KnownLostDate != knownLost {
		t.Fatalf("incorrect totals: %+v", got)
	}
	for i, want := range expected {
		if got.Points[i].Gained != want.Gained || got.Points[i].Lost != want.Lost {
			t.Fatalf("day %d: got %+v want %+v", i, got.Points[i], want)
		}
	}
}
