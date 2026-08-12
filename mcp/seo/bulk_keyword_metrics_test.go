package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	if partial.Status != "partial" || partial.VolumeCompleted != 2 || partial.DifficultyCompleted != 1 || partial.IncompleteKeywords != 1 {
		t.Fatalf("partial job = %#v", partial)
	}
	remainingVolume, _ := pendingKeywordMetricItems(db, jobID, "volume")
	remainingDifficulty, _ := pendingKeywordMetricItems(db, jobID, "difficulty")
	if len(remainingVolume) != 0 || len(remainingDifficulty) != 1 || remainingDifficulty[0].KeywordID != secondID {
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

func rawRows(values ...string) []json.RawMessage {
	rows := make([]json.RawMessage, len(values))
	for i, value := range values {
		rows[i] = json.RawMessage(value)
	}
	return rows
}
