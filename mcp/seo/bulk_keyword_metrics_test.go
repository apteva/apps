package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestClassifyProviderError(t *testing.T) {
	tests := []struct {
		code    int
		message string
		want    int
	}{
		{40200, "Payment Required.", http.StatusPaymentRequired},
		{40202, "The rates limit per minute has been exceeded", http.StatusTooManyRequests},
		{50301, "Too many requests.", http.StatusTooManyRequests},
	}
	for _, test := range tests {
		err := classifyProviderError(test.code, test.message)
		var providerErr *providerRequestError
		if !errors.As(err, &providerErr) {
			t.Fatalf("code %d returned %T, want providerRequestError", test.code, err)
		}
		if providerErr.HTTPStatus != test.want {
			t.Fatalf("code %d HTTP status = %d, want %d", test.code, providerErr.HTTPStatus, test.want)
		}
		if test.want == http.StatusTooManyRequests && providerErr.RetryAfter != 60 {
			t.Fatalf("code %d RetryAfter = %d, want 60", test.code, providerErr.RetryAfter)
		}
	}
}

func TestWriteJSONOrErrMapsProviderStatus(t *testing.T) {
	for _, status := range []int{http.StatusPaymentRequired, http.StatusTooManyRequests} {
		recorder := httptest.NewRecorder()
		writeJSONOrErr(recorder, nil, &providerRequestError{HTTPStatus: status, Message: "provider failure"})
		if recorder.Code != status {
			t.Fatalf("provider status %d mapped to %d", status, recorder.Code)
		}
	}
	recorder := httptest.NewRecorder()
	writeJSONOrErr(recorder, nil, &providerRequestError{HTTPStatus: http.StatusTooManyRequests, RetryAfter: 60, Message: "slow down"})
	if got := recorder.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q, want 60", got)
	}
}

func TestDecodeBulkKeywordMetricRows(t *testing.T) {
	volumes, err := decodeKeywordVolumeItems(rawRows(
		`{"keyword":"mcp gateway","search_volume":1900,"cpc":47.03}`,
		`{"items":[{"keyword":"hosted mcp server","search_volume":210,"cpc":32.87}]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes) != 2 || volumes["mcp gateway"].SearchVolume == nil || *volumes["mcp gateway"].SearchVolume != 1900 {
		t.Fatalf("decoded volumes = %#v", volumes)
	}

	difficulties, err := decodeKeywordDifficultyItems(rawRows(
		`{"items":[{"keyword":"mcp gateway","keyword_difficulty":67},{"keyword":"hosted mcp server","keyword_difficulty":31}]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(difficulties) != 2 || difficulties["hosted mcp server"].Difficulty == nil || *difficulties["hosted mcp server"].Difficulty != 31 {
		t.Fatalf("decoded difficulties = %#v", difficulties)
	}
}

func TestKeywordMetricJobResumesOnlyMissingFields(t *testing.T) {
	db := newSEOTestDB(t,
		"migrations/001_init.sql",
		"migrations/002_rankings_current_unique.sql",
		"migrations/003_rankings_daily_history.sql",
		"migrations/004_search_entities.sql",
		"migrations/005_search_engine_keyword_backfill.sql",
		"migrations/006_serp_consistency_and_retention.sql",
		"migrations/007_keyword_metric_jobs.sql",
		"migrations/011_keyword_metric_availability.sql",
	)
	locID := insertTestLocation(t, db, "google", 2840)
	firstID, err := insertKeywordRecord(db, "project-a", "google", "mcp gateway", locID, "US", "en")
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := insertKeywordRecord(db, "project-a", "google", "hosted mcp server", locID, "US", "en")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := createKeywordMetricJobs(db, "project-a", []int64{firstID, secondID, firstID})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].TotalKeywords != 2 {
		t.Fatalf("jobs = %#v", jobs)
	}
	jobID := jobs[0].ID

	volumeItems, err := pendingKeywordMetricItems(db, jobID, "volume")
	if err != nil {
		t.Fatal(err)
	}
	volumeValues, _ := decodeKeywordVolumeItems(rawRows(
		`{"keyword":"mcp gateway","search_volume":1900,"cpc":47.03,"monthly_searches":[{"year":2026,"month":7,"search_volume":1900}]}`,
		`{"keyword":"hosted mcp server","search_volume":210,"cpc":32.87}`,
	))
	if err := persistKeywordVolumeBatch(db, locID, volumeItems, volumeValues); err != nil {
		t.Fatal(err)
	}
	setKeywordMetricJobError(db, jobID, errors.New("difficulty phase interrupted"))
	afterVolume, err := getKeywordMetricJob(db, "project-a", jobID)
	if err != nil {
		t.Fatal(err)
	}
	if afterVolume.Status != "partial" {
		t.Fatalf("job with completed volume phase status = %q, want partial", afterVolume.Status)
	}

	difficultyItems, err := pendingKeywordMetricItems(db, jobID, "difficulty")
	if err != nil {
		t.Fatal(err)
	}
	firstDifficulty, _ := decodeKeywordDifficultyItems(rawRows(
		`{"items":[{"keyword":"mcp gateway","keyword_difficulty":67}]}`,
	))
	if err := persistKeywordDifficultyBatch(db, locID, difficultyItems, firstDifficulty); err != nil {
		t.Fatal(err)
	}
	if err := finalizeKeywordMetricJob(db, jobID); err != nil {
		t.Fatal(err)
	}
	partial, err := getKeywordMetricJob(db, "project-a", jobID)
	if err != nil {
		t.Fatal(err)
	}
	if partial.Status != "partial" || partial.VolumeCompleted != 2 || partial.DifficultyCompleted != 1 ||
		partial.DifficultyUnavailable != 1 || partial.IncompleteKeywords != 1 || partial.CompletedAt == nil {
		t.Fatalf("partial job = %#v", partial)
	}
	remainingVolume, _ := pendingKeywordMetricItems(db, jobID, "volume")
	remainingDifficulty, _ := pendingKeywordMetricItems(db, jobID, "difficulty")
	if len(remainingVolume) != 0 || len(remainingDifficulty) != 0 {
		t.Fatalf("provider-confirmed missing fields should not retry automatically: volume=%#v difficulty=%#v", remainingVolume, remainingDifficulty)
	}
	items, err := listKeywordMetricJobItems(db, jobID, partial.Status)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[1].DifficultyStatus != "unavailable" {
		t.Fatalf("partial item statuses = %#v", items)
	}
	if err := resetUnavailableKeywordMetricFields(db, jobID); err != nil {
		t.Fatal(err)
	}
	remainingDifficulty, _ = pendingKeywordMetricItems(db, jobID, "difficulty")
	if len(remainingDifficulty) != 1 || remainingDifficulty[0].KeywordID != secondID {
		t.Fatalf("remaining volume=%#v difficulty=%#v", remainingVolume, remainingDifficulty)
	}

	secondDifficulty, _ := decodeKeywordDifficultyItems(rawRows(
		`{"keyword":"hosted mcp server","keyword_difficulty":31}`,
	))
	if err := persistKeywordDifficultyBatch(db, locID, remainingDifficulty, secondDifficulty); err != nil {
		t.Fatal(err)
	}
	if err := finalizeKeywordMetricJob(db, jobID); err != nil {
		t.Fatal(err)
	}
	completed, err := getKeywordMetricJob(db, "project-a", jobID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "completed" || completed.CompletedKeywords != 2 || completed.IncompleteKeywords != 0 {
		t.Fatalf("completed job = %#v", completed)
	}
	for keywordID, wantDifficulty := range map[int64]int64{firstID: 67, secondID: 31} {
		metrics, err := latestKeywordMetrics(db, keywordID, "dataforseo")
		if err != nil {
			t.Fatal(err)
		}
		if metrics == nil || metrics.Volume == nil || metrics.Difficulty == nil || *metrics.Difficulty != wantDifficulty {
			t.Fatalf("keyword %d metrics = %#v", keywordID, metrics)
		}
		var raw string
		if err := db.QueryRow(`SELECT raw_json FROM keyword_metrics WHERE id = ?`, metrics.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if keywordID == firstID && strings.Contains(raw, "hosted mcp server") {
			t.Fatalf("keyword snapshot duplicated another batch row: %s", raw)
		}
	}
}

func TestKeywordMetricJobCreationReusesActiveWork(t *testing.T) {
	db := newSEOTestDB(t,
		"migrations/001_init.sql",
		"migrations/004_search_entities.sql",
		"migrations/005_search_engine_keyword_backfill.sql",
		"migrations/007_keyword_metric_jobs.sql",
		"migrations/011_keyword_metric_availability.sql",
	)
	locID := insertTestLocation(t, db, "google", 2840)
	keywordID, err := insertKeywordRecord(db, "project-a", "google", "mcp gateway", locID, "US", "en")
	if err != nil {
		t.Fatal(err)
	}
	first, err := createKeywordMetricJobs(db, "project-a", []int64{keywordID})
	if err != nil {
		t.Fatal(err)
	}
	second, err := createKeywordMetricJobs(db, "project-a", []int64{keywordID})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 || first[0].ID != second[0].ID {
		t.Fatalf("active job was not reused: first=%#v second=%#v", first, second)
	}
}

func TestBulkKeywordMetricJobQueuesMoreThanOneHundredKeywords(t *testing.T) {
	db := newSEOTestDB(t,
		"migrations/001_init.sql",
		"migrations/004_search_entities.sql",
		"migrations/005_search_engine_keyword_backfill.sql",
		"migrations/007_keyword_metric_jobs.sql",
		"migrations/011_keyword_metric_availability.sql",
	)
	locID := insertTestLocation(t, db, "google", 2840)
	ids := make([]int64, 0, 150)
	for i := 0; i < 150; i++ {
		id, err := insertKeywordRecord(db, "project-a", "google", fmt.Sprintf("keyword %03d", i), locID, "US", "en")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	jobs, err := createKeywordMetricJobs(db, "project-a", ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].TotalKeywords != 150 {
		t.Fatalf("jobs = %#v, want one 150-keyword job", jobs)
	}
	items, err := listKeywordMetricJobItems(db, jobs[0].ID, jobs[0].Status)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 150 || items[0].VolumeStatus != "pending" || items[149].DifficultyStatus != "pending" {
		t.Fatalf("item status summary: count=%d first=%#v last=%#v", len(items), items[0], items[len(items)-1])
	}
}

func TestSingleKeywordRefreshReturnsSuccessfulPartialResult(t *testing.T) {
	db := newSEOTestDB(t,
		"migrations/001_init.sql",
		"migrations/004_search_entities.sql",
		"migrations/005_search_engine_keyword_backfill.sql",
		"migrations/007_keyword_metric_jobs.sql",
		"migrations/011_keyword_metric_availability.sql",
	)
	locID := insertTestLocation(t, db, "google", 2840)
	keywordID, err := insertKeywordRecord(db, "project-a", "google", "mcp gateway", locID, "US", "en")
	if err != nil {
		t.Fatal(err)
	}
	keyword, err := getKeyword(db, "project-a", keywordID)
	if err != nil {
		t.Fatal(err)
	}
	location, err := getLocation(db, locID)
	if err != nil {
		t.Fatal(err)
	}
	stub := &yepPlatformStub{
		responses: map[string]json.RawMessage{
			"account_info":          json.RawMessage(`{"status_code":20000,"tasks":[{"status_code":20000,"result":[{"money":{"balance":10}}]}]}`),
			"keyword_search_volume": json.RawMessage(`{"status_code":20000,"tasks":[{"status_code":20000,"result":[{"keyword":"mcp gateway","search_volume":1900,"cpc":47.03}]}]}`),
			"keyword_difficulty":    json.RawMessage(`{"status_code":20000,"tasks":[{"status_code":20000,"result":[{"items":[]}]}]}`),
		},
		identity:    &sdk.InstallIdentity{Bindings: map[string]any{providerRole: map[string]any{"ids": []int64{42}, "default_id": int64(42)}}},
		connections: map[int64]*sdk.PlatformConnection{42: {ID: 42, AppSlug: "dataforseo"}},
	}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, nil, stub, nil)
	result, err := refreshKeywordViaDataForSEO(ctx, 42, keyword, location)
	if err != nil {
		t.Fatalf("partial refresh returned an error: %v", err)
	}
	payload := result.(map[string]any)
	if payload["status"] != "partial" || payload["volume"] != int64(1900) || payload["difficulty"] != nil {
		t.Fatalf("partial payload = %#v", payload)
	}
	unavailable := payload["unavailable"].([]string)
	if len(unavailable) != 1 || unavailable[0] != "difficulty" {
		t.Fatalf("unavailable = %#v", unavailable)
	}
}

func rawRows(values ...string) []json.RawMessage {
	rows := make([]json.RawMessage, len(values))
	for i, value := range values {
		rows[i] = json.RawMessage(value)
	}
	return rows
}
