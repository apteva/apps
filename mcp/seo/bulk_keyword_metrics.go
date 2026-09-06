package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const keywordMetricBatchSize = 1000

type keywordMetricJob struct {
	ID                  int64  `json:"id"`
	ProjectID           string `json:"project_id"`
	Provider            string `json:"provider"`
	SearchEngine        string `json:"search_engine"`
	LocationID          int64  `json:"location_id"`
	Status              string `json:"status"`
	Phase               string `json:"phase"`
	TotalKeywords       int64  `json:"total_keywords"`
	CompletedKeywords   int64  `json:"completed_keywords"`
	IncompleteKeywords  int64  `json:"incomplete_keywords"`
	VolumeCompleted     int64  `json:"volume_completed"`
	DifficultyCompleted int64  `json:"difficulty_completed"`
	LastError           string `json:"last_error,omitempty"`
	CreatedAt           int64  `json:"created_at"`
	StartedAt           *int64 `json:"started_at,omitempty"`
	CompletedAt         *int64 `json:"completed_at,omitempty"`
	UpdatedAt           int64  `json:"updated_at"`
}

type keywordMetricJobItem struct {
	ItemID     int64
	KeywordID  int64
	Text       string
	SnapshotID sql.NullInt64
}

type keywordMetricJobCreateRequest struct {
	KeywordIDs []int64 `json:"keyword_ids"`
	Provider   string  `json:"provider,omitempty"`
}

type dfsAccountInfo struct {
	Money struct {
		Balance *float64 `json:"balance"`
	} `json:"money"`
}

type dfsKeywordDifficultyItem struct {
	Keyword    string          `json:"keyword"`
	Difficulty *int64          `json:"keyword_difficulty"`
	Raw        json.RawMessage `json:"-"`
}

type metricRawBundle struct {
	VolumeResult     json.RawMessage `json:"volume_result,omitempty"`
	DifficultyResult json.RawMessage `json:"difficulty_result,omitempty"`
	Legacy           json.RawMessage `json:"legacy,omitempty"`
}

func (a *App) handleKeywordMetricJobs(w http.ResponseWriter, r *http.Request) {
	pid := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if pid == "" {
		pid = projectScopeFromArgs(mustCtx(r), nil)
	}
	switch r.Method {
	case http.MethodGet:
		jobs, err := listKeywordMetricJobs(mustCtx(r).AppDB(), pid, 20)
		writeJSONOrErr(w, map[string]any{"jobs": jobs}, err)
	case http.MethodPost:
		var body keywordMetricJobCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		balance := float64(0)
		checked := map[int64]bool{}
		jobs, err := createKeywordMetricJobsWithPreflight(mustCtx(r).AppDB(), pid, body.KeywordIDs, func(slug string) error {
			if body.Provider != "" && !strings.EqualFold(body.Provider, slug) {
				return &requestValidationError{Message: fmt.Sprintf("keyword metric job uses provider %q, not requested provider %q", slug, body.Provider)}
			}
			if slug != "dataforseo" {
				return &requestValidationError{Message: fmt.Sprintf("bulk keyword metrics are not implemented for provider %q", slug)}
			}
			provider, err := selectProvider(mustCtx(r), slug)
			if err != nil {
				return err
			}
			if !checked[provider.ConnectionID()] {
				balance, err = preflightDataForSEO(mustCtx(r), provider.ConnectionID())
				if err != nil {
					return err
				}
				checked[provider.ConnectionID()] = true
			}
			return nil
		})
		if err != nil {
			writeJSONOrErr(w, nil, err)
			return
		}
		for _, job := range jobs {
			go func(id int64) { _ = runKeywordMetricJob(globalCtx, id) }(job.ID)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"jobs": jobs, "balance": balance})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleKeywordMetricJobItem(w http.ResponseWriter, r *http.Request) {
	pid := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if pid == "" {
		pid = projectScopeFromArgs(mustCtx(r), nil)
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/keyword-metric-jobs/"), "/")
	parts := strings.Split(rest, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid keyword metric job id", http.StatusBadRequest)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		job, err := getKeywordMetricJob(mustCtx(r).AppDB(), pid, id)
		writeJSONOrErr(w, job, err)
		return
	}
	if len(parts) == 2 && parts[1] == "resume" && r.Method == http.MethodPost {
		job, err := getKeywordMetricJob(mustCtx(r).AppDB(), pid, id)
		if err != nil {
			writeJSONOrErr(w, nil, err)
			return
		}
		provider, err := selectProvider(mustCtx(r), job.Provider)
		if err != nil {
			writeJSONOrErr(w, nil, err)
			return
		}
		balance, err := preflightDataForSEO(mustCtx(r), provider.ConnectionID())
		if err != nil {
			writeJSONOrErr(w, nil, err)
			return
		}
		res, err := mustCtx(r).AppDB().Exec(
			`UPDATE keyword_metric_jobs SET status = 'pending', phase = 'queued', last_error = '',
			        completed_at = NULL, updated_at = ?
			  WHERE id = ? AND project_id = ? AND status IN ('pending', 'partial', 'failed')`,
			time.Now().Unix(), id, pid)
		if err != nil {
			writeJSONOrErr(w, nil, err)
			return
		}
		updated, _ := res.RowsAffected()
		if updated == 0 {
			http.Error(w, "only pending, partial, or failed jobs can be resumed", http.StatusConflict)
			return
		}
		go func() { _ = runKeywordMetricJob(globalCtx, id) }()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"job_id": id, "balance": balance})
		return
	}
	http.Error(w, "not found", http.StatusNotFound)
}

func preflightDataForSEO(ctx *sdk.AppCtx, connID int64) (float64, error) {
	rows, _, err := callDfsRowsWithIntegrationInput(ctx, connID, "account_info", map[string]any{})
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, errors.New("dataforseo: account_info returned no result")
	}
	var account dfsAccountInfo
	if err := json.Unmarshal(rows[0], &account); err != nil {
		return 0, fmt.Errorf("dataforseo: parse account_info: %w", err)
	}
	if account.Money.Balance == nil {
		return 0, errors.New("dataforseo: account_info did not include a balance")
	}
	if *account.Money.Balance <= 0 {
		return *account.Money.Balance, &providerRequestError{
			HTTPStatus: http.StatusPaymentRequired,
			Message:    fmt.Sprintf("payment required; account balance is %.4f USD", *account.Money.Balance),
		}
	}
	return *account.Money.Balance, nil
}

func createKeywordMetricJobs(db *sql.DB, pid string, keywordIDs []int64) ([]keywordMetricJob, error) {
	return createKeywordMetricJobsWithPreflight(db, pid, keywordIDs, nil)
}

// Validate every group and its provider before creating any jobs. A rejected
// request must not leave permanently queued rows with no worker to run them.
func createKeywordMetricJobsWithPreflight(db *sql.DB, pid string, keywordIDs []int64, preflight func(string) error) ([]keywordMetricJob, error) {
	keywordIDs = uniquePositiveInt64s(keywordIDs)
	if len(keywordIDs) == 0 {
		return nil, errors.New("keyword_ids required")
	}
	if len(keywordIDs) > 10000 {
		return nil, errors.New("keyword_ids cannot exceed 10000")
	}
	type group struct {
		LocationID int64
		Provider   string
		IDs        []int64
	}
	groups := map[string]*group{}
	for _, id := range keywordIDs {
		var projectID, engine, provider string
		var locationID int64
		err := db.QueryRow(
			`SELECT k.project_id, k.search_engine, k.location_id, l.provider
			   FROM keywords k JOIN seo_locations l ON l.id = k.location_id
			  WHERE k.id = ? AND k.project_id = ?`, id, pid,
		).Scan(&projectID, &engine, &locationID, &provider)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("keyword %d not found", id)
		}
		if err != nil {
			return nil, err
		}
		if engine != "google" {
			return nil, fmt.Errorf("keyword %d uses %s; bulk keyword metrics support google only", id, engine)
		}
		key := provider + ":" + strconv.FormatInt(locationID, 10)
		if groups[key] == nil {
			groups[key] = &group{LocationID: locationID, Provider: provider}
		}
		groups[key].IDs = append(groups[key].IDs, id)
	}

	if preflight != nil {
		for _, group := range groups {
			if err := preflight(group.Provider); err != nil {
				return nil, err
			}
		}
	}

	now := time.Now().Unix()
	_, _ = db.Exec(`DELETE FROM keyword_metric_jobs WHERE completed_at IS NOT NULL AND completed_at < ?`, now-30*86400)
	groupKeys := make([]string, 0, len(groups))
	for key := range groups {
		groupKeys = append(groupKeys, key)
	}
	sort.Strings(groupKeys)
	out := make([]keywordMetricJob, 0, len(groups))
	for _, key := range groupKeys {
		g := groups[key]
		tx, err := db.Begin()
		if err != nil {
			return nil, err
		}
		res, err := tx.Exec(
			`INSERT INTO keyword_metric_jobs
			   (project_id, provider, search_engine, location_id, status, phase,
			    total_keywords, incomplete_keywords, created_at, updated_at)
			 VALUES (?, ?, 'google', ?, 'pending', 'queued', ?, ?, ?, ?)`,
			pid, g.Provider, g.LocationID, len(g.IDs), len(g.IDs), now, now)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		jobID, _ := res.LastInsertId()
		for _, keywordID := range g.IDs {
			if _, err := tx.Exec(
				`INSERT INTO keyword_metric_job_items (job_id, keyword_id, updated_at) VALUES (?, ?, ?)`,
				jobID, keywordID, now); err != nil {
				tx.Rollback()
				return nil, err
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		job, err := getKeywordMetricJob(db, pid, jobID)
		if err != nil {
			return nil, err
		}
		out = append(out, *job)
	}
	return out, nil
}

func runKeywordMetricJob(ctx *sdk.AppCtx, jobID int64) error {
	if ctx == nil {
		return errors.New("seo app is not mounted")
	}
	db := ctx.AppDB()
	now := time.Now().Unix()
	res, err := db.Exec(
		`UPDATE keyword_metric_jobs
		    SET status = 'running', phase = 'account_check', started_at = COALESCE(started_at, ?),
		        completed_at = NULL, last_error = '', updated_at = ?
		  WHERE id = ? AND status IN ('pending', 'partial', 'failed')`, now, now, jobID)
	if err != nil {
		return err
	}
	claimed, _ := res.RowsAffected()
	if claimed == 0 {
		return nil
	}
	job, loc, connID, err := loadKeywordMetricJobRuntime(ctx, jobID)
	if err != nil {
		setKeywordMetricJobError(db, jobID, err)
		return err
	}
	if _, err := preflightDataForSEO(ctx, connID); err != nil {
		setKeywordMetricJobError(db, jobID, err)
		return err
	}
	if err := runKeywordMetricVolumePhase(ctx, job, loc, connID); err != nil {
		setKeywordMetricJobError(db, jobID, err)
		return err
	}
	if err := runKeywordMetricDifficultyPhase(ctx, job, loc, connID); err != nil {
		setKeywordMetricJobError(db, jobID, err)
		return err
	}
	return finalizeKeywordMetricJob(db, jobID)
}

func loadKeywordMetricJobRuntime(ctx *sdk.AppCtx, jobID int64) (*keywordMetricJob, *SEOLocation, int64, error) {
	var pid string
	if err := ctx.AppDB().QueryRow(`SELECT project_id FROM keyword_metric_jobs WHERE id = ?`, jobID).Scan(&pid); err != nil {
		return nil, nil, 0, err
	}
	job, err := getKeywordMetricJob(ctx.AppDB(), pid, jobID)
	if err != nil {
		return nil, nil, 0, err
	}
	loc, err := getLocation(ctx.AppDB(), job.LocationID)
	if err != nil {
		return nil, nil, 0, err
	}
	if loc.LocationCode == nil {
		return nil, nil, 0, fmt.Errorf("location %d has no DataForSEO location code", loc.ID)
	}
	provider, err := selectProvider(ctx, job.Provider)
	if err != nil {
		return nil, nil, 0, err
	}
	if provider.Slug() != "dataforseo" {
		return nil, nil, 0, fmt.Errorf("bulk keyword metrics are not implemented for provider %q", provider.Slug())
	}
	return job, loc, provider.ConnectionID(), nil
}

func runKeywordMetricVolumePhase(ctx *sdk.AppCtx, job *keywordMetricJob, loc *SEOLocation, connID int64) error {
	items, err := pendingKeywordMetricItems(ctx.AppDB(), job.ID, "volume")
	if err != nil {
		return err
	}
	for start := 0; start < len(items); start += keywordMetricBatchSize {
		end := min(start+keywordMetricBatchSize, len(items))
		batch := items[start:end]
		keywords := make([]string, len(batch))
		for i := range batch {
			keywords[i] = batch[i].Text
		}
		_, _ = ctx.AppDB().Exec(`UPDATE keyword_metric_jobs SET phase = 'volume', updated_at = ? WHERE id = ?`, time.Now().Unix(), job.ID)
		rows, _, err := retryProviderCall(func() ([]json.RawMessage, []byte, error) {
			return callDfsRows(ctx, connID, "keyword_search_volume", map[string]any{
				"keywords": keywords, "location_code": *loc.LocationCode,
				"language_code": strings.ToLower(loc.LanguageCode),
			})
		})
		if err != nil {
			markKeywordMetricItemsError(ctx.AppDB(), job.ID, "volume", batch, err)
			return err
		}
		values, err := decodeKeywordVolumeItems(rows)
		if err != nil {
			markKeywordMetricItemsError(ctx.AppDB(), job.ID, "volume", batch, err)
			return err
		}
		if err := persistKeywordVolumeBatch(ctx.AppDB(), loc.ID, batch, values); err != nil {
			return err
		}
		refreshKeywordMetricJobCounts(ctx.AppDB(), job.ID)
	}
	return nil
}

func runKeywordMetricDifficultyPhase(ctx *sdk.AppCtx, job *keywordMetricJob, loc *SEOLocation, connID int64) error {
	items, err := pendingKeywordMetricItems(ctx.AppDB(), job.ID, "difficulty")
	if err != nil {
		return err
	}
	for start := 0; start < len(items); start += keywordMetricBatchSize {
		end := min(start+keywordMetricBatchSize, len(items))
		batch := items[start:end]
		keywords := make([]string, len(batch))
		for i := range batch {
			keywords[i] = batch[i].Text
		}
		_, _ = ctx.AppDB().Exec(`UPDATE keyword_metric_jobs SET phase = 'difficulty', updated_at = ? WHERE id = ?`, time.Now().Unix(), job.ID)
		rows, _, err := retryProviderCall(func() ([]json.RawMessage, []byte, error) {
			return callDfsRows(ctx, connID, "keyword_difficulty", map[string]any{
				"keywords": keywords, "location_code": *loc.LocationCode,
				"language_code": strings.ToLower(loc.LanguageCode),
			})
		})
		if err != nil {
			markKeywordMetricItemsError(ctx.AppDB(), job.ID, "difficulty", batch, err)
			return err
		}
		values, err := decodeKeywordDifficultyItems(rows)
		if err != nil {
			markKeywordMetricItemsError(ctx.AppDB(), job.ID, "difficulty", batch, err)
			return err
		}
		if err := persistKeywordDifficultyBatch(ctx.AppDB(), loc.ID, batch, values); err != nil {
			return err
		}
		refreshKeywordMetricJobCounts(ctx.AppDB(), job.ID)
	}
	return nil
}

func decodeKeywordVolumeItems(rows []json.RawMessage) (map[string]dfsKeywordVolumeItem, error) {
	out := map[string]dfsKeywordVolumeItem{}
	for _, row := range rows {
		var wrapped struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(row, &wrapped); err != nil {
			return nil, fmt.Errorf("parse keyword_search_volume row: %w", err)
		}
		itemRows := wrapped.Items
		if len(itemRows) == 0 {
			itemRows = []json.RawMessage{row}
		}
		for _, itemRaw := range itemRows {
			var item dfsKeywordVolumeItem
			if err := json.Unmarshal(itemRaw, &item); err != nil {
				return nil, fmt.Errorf("parse keyword_search_volume item: %w", err)
			}
			item.Raw = append(json.RawMessage(nil), itemRaw...)
			if key := normaliseKeyword(item.Keyword); key != "" {
				out[key] = item
			}
		}
	}
	return out, nil
}

func decodeKeywordDifficultyItems(rows []json.RawMessage) (map[string]dfsKeywordDifficultyItem, error) {
	out := map[string]dfsKeywordDifficultyItem{}
	for _, row := range rows {
		var wrapped struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(row, &wrapped); err != nil {
			return nil, fmt.Errorf("parse keyword_difficulty row: %w", err)
		}
		itemRows := wrapped.Items
		if len(itemRows) == 0 {
			itemRows = []json.RawMessage{row}
		}
		for _, itemRaw := range itemRows {
			var item dfsKeywordDifficultyItem
			if err := json.Unmarshal(itemRaw, &item); err != nil {
				return nil, fmt.Errorf("parse keyword_difficulty item: %w", err)
			}
			item.Raw = append(json.RawMessage(nil), itemRaw...)
			if key := normaliseKeyword(item.Keyword); key != "" {
				out[key] = item
			}
		}
	}
	return out, nil
}

func persistKeywordVolumeBatch(db *sql.DB, locationID int64, batch []keywordMetricJobItem, values map[string]dfsKeywordVolumeItem) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	for _, pending := range batch {
		item, ok := values[normaliseKeyword(pending.Text)]
		if !ok {
			_, err = tx.Exec(`UPDATE keyword_metric_job_items SET attempts = attempts + 1, last_error = ?, updated_at = ? WHERE id = ?`, "volume response omitted keyword", now, pending.ItemID)
			if err != nil {
				return err
			}
			continue
		}
		snapshotID, raw, err := ensureJobMetricSnapshot(tx, pending, locationID, "volume", item.Raw)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE keyword_metrics SET volume = ?, cpc_usd = ?, raw_json = ? WHERE id = ?`, item.SearchVolume, item.CPC, raw, snapshotID); err != nil {
			return err
		}
		for _, month := range item.MonthlySearches {
			if month.Year == 0 || month.Month == 0 {
				continue
			}
			if _, err := tx.Exec(
				`INSERT INTO keyword_volume_history (keyword_id, location_id, provider, year, month, volume)
				 VALUES (?, ?, 'dataforseo', ?, ?, ?)
				 ON CONFLICT(keyword_id, location_id, provider, year, month)
				 DO UPDATE SET volume = excluded.volume`,
				pending.KeywordID, locationID, month.Year, month.Month, month.Volume); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(
			`UPDATE keyword_metric_job_items
			    SET metric_snapshot_id = ?, volume_done = 1, attempts = attempts + 1,
			        last_error = '', updated_at = ? WHERE id = ?`, snapshotID, now, pending.ItemID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func persistKeywordDifficultyBatch(db *sql.DB, locationID int64, batch []keywordMetricJobItem, values map[string]dfsKeywordDifficultyItem) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	for _, pending := range batch {
		item, ok := values[normaliseKeyword(pending.Text)]
		if !ok || item.Difficulty == nil {
			_, err = tx.Exec(`UPDATE keyword_metric_job_items SET attempts = attempts + 1, last_error = ?, updated_at = ? WHERE id = ?`, "difficulty response omitted keyword", now, pending.ItemID)
			if err != nil {
				return err
			}
			continue
		}
		snapshotID, raw, err := ensureJobMetricSnapshot(tx, pending, locationID, "difficulty", item.Raw)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE keyword_metrics SET difficulty = ?, raw_json = ? WHERE id = ?`, item.Difficulty, raw, snapshotID); err != nil {
			return err
		}
		if _, err := tx.Exec(
			`UPDATE keyword_metric_job_items
			    SET metric_snapshot_id = ?, difficulty_done = 1, attempts = attempts + 1,
			        last_error = '', updated_at = ? WHERE id = ?`, snapshotID, now, pending.ItemID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func ensureJobMetricSnapshot(tx *sql.Tx, pending keywordMetricJobItem, locationID int64, field string, providerRaw []byte) (int64, string, error) {
	snapshotID := pending.SnapshotID.Int64
	existingRaw := ""
	if snapshotID != 0 {
		if err := tx.QueryRow(`SELECT raw_json FROM keyword_metrics WHERE id = ?`, snapshotID).Scan(&existingRaw); err != nil {
			return 0, "", err
		}
	} else {
		res, err := tx.Exec(
			`INSERT INTO keyword_metrics (keyword_id, location_id, provider, ts, raw_json)
			 VALUES (?, ?, 'dataforseo', ?, '{}')`, pending.KeywordID, locationID, time.Now().Unix())
		if err != nil {
			return 0, "", err
		}
		snapshotID, _ = res.LastInsertId()
	}
	raw := mergeMetricResultRaw(existingRaw, field, providerRaw)
	return snapshotID, raw, nil
}

func mergeMetricResultRaw(existing, field string, providerRaw []byte) string {
	var bundle metricRawBundle
	if existing != "" && existing != "{}" {
		if err := json.Unmarshal([]byte(existing), &bundle); err != nil || (len(bundle.VolumeResult) == 0 && len(bundle.DifficultyResult) == 0 && len(bundle.Legacy) == 0) {
			if json.Valid([]byte(existing)) {
				bundle.Legacy = json.RawMessage(existing)
			}
		}
	}
	if field == "volume" {
		bundle.VolumeResult = append(json.RawMessage(nil), providerRaw...)
	} else {
		bundle.DifficultyResult = append(json.RawMessage(nil), providerRaw...)
	}
	raw, _ := json.Marshal(bundle)
	return string(raw)
}

func pendingKeywordMetricItems(db *sql.DB, jobID int64, field string) ([]keywordMetricJobItem, error) {
	column := "volume_done"
	if field == "difficulty" {
		column = "difficulty_done"
	}
	rows, err := db.Query(
		`SELECT ji.id, ji.keyword_id, k.text, ji.metric_snapshot_id
		   FROM keyword_metric_job_items ji JOIN keywords k ON k.id = ji.keyword_id
		  WHERE ji.job_id = ? AND ji.`+column+` = 0 ORDER BY ji.keyword_id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []keywordMetricJobItem{}
	for rows.Next() {
		var item keywordMetricJobItem
		if err := rows.Scan(&item.ItemID, &item.KeywordID, &item.Text, &item.SnapshotID); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func markKeywordMetricItemsError(db *sql.DB, jobID int64, field string, items []keywordMetricJobItem, cause error) {
	now := time.Now().Unix()
	for _, item := range items {
		_, _ = db.Exec(`UPDATE keyword_metric_job_items SET attempts = attempts + 1, last_error = ?, updated_at = ? WHERE id = ?`, cause.Error(), now, item.ItemID)
	}
	_, _ = db.Exec(`UPDATE keyword_metric_jobs SET phase = ?, last_error = ?, updated_at = ? WHERE id = ?`, field, cause.Error(), now, jobID)
}

func setKeywordMetricJobError(db *sql.DB, jobID int64, cause error) {
	refreshKeywordMetricJobCounts(db, jobID)
	var fieldsCompleted int64
	_ = db.QueryRow(
		`SELECT COUNT(*) FROM keyword_metric_job_items
		  WHERE job_id = ? AND (volume_done = 1 OR difficulty_done = 1)`, jobID,
	).Scan(&fieldsCompleted)
	status := "failed"
	if fieldsCompleted > 0 {
		status = "partial"
	}
	_, _ = db.Exec(`UPDATE keyword_metric_jobs SET status = ?, last_error = ?, updated_at = ? WHERE id = ?`, status, cause.Error(), time.Now().Unix(), jobID)
}

func refreshKeywordMetricJobCounts(db *sql.DB, jobID int64) {
	_, _ = db.Exec(
		`UPDATE keyword_metric_jobs
		    SET completed_keywords = (SELECT COUNT(*) FROM keyword_metric_job_items WHERE job_id = ? AND volume_done = 1 AND difficulty_done = 1),
		        incomplete_keywords = total_keywords - (SELECT COUNT(*) FROM keyword_metric_job_items WHERE job_id = ? AND volume_done = 1 AND difficulty_done = 1),
		        updated_at = ?
		  WHERE id = ?`, jobID, jobID, time.Now().Unix(), jobID)
}

func finalizeKeywordMetricJob(db *sql.DB, jobID int64) error {
	refreshKeywordMetricJobCounts(db, jobID)
	var incomplete int64
	if err := db.QueryRow(`SELECT incomplete_keywords FROM keyword_metric_jobs WHERE id = ?`, jobID).Scan(&incomplete); err != nil {
		return err
	}
	status, phase, lastError := "completed", "completed", ""
	var completedAt any = time.Now().Unix()
	if incomplete > 0 {
		status, phase = "partial", "incomplete"
		lastError = fmt.Sprintf("%d keyword(s) still have missing metric fields; resume retries only those fields", incomplete)
		completedAt = nil
	}
	_, err := db.Exec(`UPDATE keyword_metric_jobs SET status = ?, phase = ?, last_error = ?, completed_at = ?, updated_at = ? WHERE id = ?`, status, phase, lastError, completedAt, time.Now().Unix(), jobID)
	return err
}

func getKeywordMetricJob(db *sql.DB, pid string, id int64) (*keywordMetricJob, error) {
	var job keywordMetricJob
	var startedAt, completedAt sql.NullInt64
	err := db.QueryRow(
		`SELECT j.id, j.project_id, j.provider, j.search_engine, j.location_id,
		        j.status, j.phase, j.total_keywords, j.completed_keywords,
		        j.incomplete_keywords,
		        (SELECT COUNT(*) FROM keyword_metric_job_items WHERE job_id = j.id AND volume_done = 1),
		        (SELECT COUNT(*) FROM keyword_metric_job_items WHERE job_id = j.id AND difficulty_done = 1),
		        j.last_error, j.created_at, j.started_at, j.completed_at, j.updated_at
		   FROM keyword_metric_jobs j WHERE j.id = ? AND j.project_id = ?`, id, pid,
	).Scan(&job.ID, &job.ProjectID, &job.Provider, &job.SearchEngine, &job.LocationID,
		&job.Status, &job.Phase, &job.TotalKeywords, &job.CompletedKeywords,
		&job.IncompleteKeywords, &job.VolumeCompleted, &job.DifficultyCompleted,
		&job.LastError, &job.CreatedAt, &startedAt, &completedAt, &job.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("keyword metric job %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	if startedAt.Valid {
		job.StartedAt = &startedAt.Int64
	}
	if completedAt.Valid {
		job.CompletedAt = &completedAt.Int64
	}
	return &job, nil
}

func listKeywordMetricJobs(db *sql.DB, pid string, limit int) ([]keywordMetricJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := db.Query(`SELECT id FROM keyword_metric_jobs WHERE project_id = ? ORDER BY id DESC LIMIT ?`, pid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	out := make([]keywordMetricJob, 0, len(ids))
	for _, id := range ids {
		job, err := getKeywordMetricJob(db, pid, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *job)
	}
	return out, nil
}

func uniquePositiveInt64s(values []int64) []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
