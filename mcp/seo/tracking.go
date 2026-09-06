package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	dataForSEOStandardFirstPageCost      = 0.0006
	dataForSEOStandardAdditionalPageCost = 0.00045
)

const defaultTrackingFrequency = "daily"

type rankTrackingSettings struct {
	ProjectID        string  `json:"project_id"`
	Enabled          bool    `json:"enabled"`
	MonthlyBudgetUSD float64 `json:"monthly_budget_usd"`
	DailyDepth       int     `json:"daily_depth"`
	WeeklyDepth      int     `json:"weekly_depth"`
}

type rankTracker struct {
	ID               int64  `json:"id"`
	ProjectID        string `json:"project_id"`
	KeywordID        int64  `json:"keyword_id"`
	KeywordText      string `json:"keyword_text"`
	SearchEngine     string `json:"search_engine"`
	LocationID       int64  `json:"location_id"`
	EntityID         int64  `json:"entity_id"`
	EntityType       string `json:"entity_type"`
	EntityIdentifier string `json:"entity_identifier"`
	EntityLabel      string `json:"entity_label,omitempty"`
	Provider         string `json:"provider"`
	Device           string `json:"device"`
	Frequency        string `json:"frequency"`
	Enabled          bool   `json:"enabled"`
	DailyDepth       int    `json:"daily_depth"`
	WeeklyDepth      int    `json:"weekly_depth"`
	NextRunAt        int64  `json:"next_run_at"`
	LastAttemptAt    *int64 `json:"last_attempt_at,omitempty"`
	LastSuccessAt    *int64 `json:"last_success_at,omitempty"`
	LastError        string `json:"last_error,omitempty"`
}

type rankObservation struct {
	ID           int64  `json:"id"`
	TrackerID    int64  `json:"tracker_id"`
	ObservedDate string `json:"observed_date"`
	TS           int64  `json:"ts"`
	Found        bool   `json:"found"`
	Rank         *int64 `json:"rank,omitempty"`
	RankURL      string `json:"rank_url,omitempty"`
	CheckedDepth int    `json:"checked_depth"`
	Provider     string `json:"provider"`
}

type refreshJob struct {
	ID           int64
	ProjectID    string
	KeywordID    int64
	KeywordText  string
	LocationID   int64
	SearchEngine string
	Provider     string
	Device       string
	Depth        int
	ObservedDate string
	TaskID       string
	Attempts     int
}

type dueTracker struct {
	id, keywordID, locationID int64
	engine, provider, device  string
	frequency                 string
	dailyDepth, weeklyDepth   int
}

func (a *App) runRankTrackingScheduler(_ context.Context, ctx *sdk.AppCtx) error {
	db := ctx.AppDB()
	pid := projectScope(ctx)
	settings, err := getRankTrackingSettings(db, pid)
	if err != nil || !settings.Enabled {
		return err
	}
	now := time.Now().UTC()
	if err := createDueRefreshJobs(db, pid, settings, now); err != nil {
		return err
	}
	return submitPendingRefreshJobs(ctx, pid, now)
}

func (a *App) runRankTrackingCollector(_ context.Context, ctx *sdk.AppCtx) error {
	pid := projectScope(ctx)
	now := time.Now().UTC()
	_, _ = ctx.AppDB().Exec(`UPDATE serp_refresh_jobs
		SET status = 'failed', completed_at = ?, last_error = 'DataForSEO queue task timed out after 24 hours', updated_at = CURRENT_TIMESTAMP
		WHERE project_id = ? AND status = 'submitted' AND submitted_at < ?`, now.Unix(), pid, now.Add(-24*time.Hour).Unix())
	var submitted int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM serp_refresh_jobs WHERE project_id = ? AND status = 'submitted'`, pid).Scan(&submitted); err != nil {
		return err
	}
	if submitted == 0 {
		return nil
	}
	provider, err := selectProvider(ctx, "dataforseo")
	if err != nil {
		// An unbound provider is a configuration state. Jobs stay submitted and
		// can be collected after the connection is restored.
		if errors.Is(err, errProviderUnbound) {
			return nil
		}
		return err
	}
	dfs, ok := provider.(*dataForSEOProvider)
	if !ok {
		return errors.New("automatic rank tracking requires DataForSEO")
	}
	rows, err := ctx.AppDB().Query(
		`SELECT j.id, j.project_id, j.keyword_id, k.text, j.location_id,
		        j.search_engine, j.provider, j.device, j.depth, j.observed_date,
		        j.provider_task_id, j.attempts
		   FROM serp_refresh_jobs j
		   JOIN keywords k ON k.id = j.keyword_id
		  WHERE j.project_id = ? AND j.status = 'submitted'
		  ORDER BY j.submitted_at, j.id
		  LIMIT 50`, pid)
	if err != nil {
		return err
	}
	defer rows.Close()
	jobs := []refreshJob{}
	for rows.Next() {
		var job refreshJob
		if err := rows.Scan(&job.ID, &job.ProjectID, &job.KeywordID, &job.KeywordText,
			&job.LocationID, &job.SearchEngine, &job.Provider, &job.Device, &job.Depth,
			&job.ObservedDate, &job.TaskID, &job.Attempts); err != nil {
			return err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, job := range jobs {
		response, cost, ready, err := collectDataForSEOSERPTask(ctx, dfs.connID, job.TaskID)
		if err != nil {
			_, _ = ctx.AppDB().Exec(`UPDATE serp_refresh_jobs SET last_error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, err.Error(), job.ID)
			continue
		}
		if !ready {
			continue
		}
		if err := completeRefreshJob(ctx.AppDB(), job, response, cost); err != nil {
			_, _ = ctx.AppDB().Exec(`UPDATE serp_refresh_jobs SET last_error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, err.Error(), job.ID)
			continue
		}
	}
	return nil
}

func getRankTrackingSettings(db *sql.DB, pid string) (rankTrackingSettings, error) {
	settings := rankTrackingSettings{ProjectID: pid, Enabled: true, MonthlyBudgetUSD: 5, DailyDepth: 20, WeeklyDepth: 100}
	var enabled int
	err := db.QueryRow(`SELECT enabled, monthly_budget_usd, daily_depth, weekly_depth
		FROM serp_tracking_settings WHERE project_id = ?`, pid).
		Scan(&enabled, &settings.MonthlyBudgetUSD, &settings.DailyDepth, &settings.WeeklyDepth)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	settings.Enabled = enabled != 0
	return settings, err
}

func saveRankTrackingSettings(db *sql.DB, settings rankTrackingSettings) error {
	if settings.MonthlyBudgetUSD < 0 {
		return errors.New("monthly_budget_usd must be at least 0")
	}
	if settings.DailyDepth < 10 || settings.DailyDepth > 100 || settings.WeeklyDepth < 10 || settings.WeeklyDepth > 100 {
		return errors.New("daily_depth and weekly_depth must be between 10 and 100")
	}
	if settings.WeeklyDepth < settings.DailyDepth {
		return errors.New("weekly_depth must be greater than or equal to daily_depth")
	}
	_, err := db.Exec(`INSERT INTO serp_tracking_settings
		(project_id, enabled, monthly_budget_usd, daily_depth, weekly_depth, updated_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(project_id) DO UPDATE SET
		 enabled = excluded.enabled,
		 monthly_budget_usd = excluded.monthly_budget_usd,
		 daily_depth = excluded.daily_depth,
		 weekly_depth = excluded.weekly_depth,
		 updated_at = CURRENT_TIMESTAMP`, settings.ProjectID, boolInt(settings.Enabled),
		settings.MonthlyBudgetUSD, settings.DailyDepth, settings.WeeklyDepth)
	return err
}

func createDueRefreshJobs(db *sql.DB, pid string, settings rankTrackingSettings, now time.Time) error {
	rows, err := db.Query(`SELECT t.id, t.keyword_id, k.location_id, k.search_engine,
		 t.provider, t.device, t.frequency, t.daily_depth, t.weekly_depth
		 FROM serp_trackers t JOIN keywords k ON k.id = t.keyword_id
		 WHERE t.project_id = ? AND t.enabled = 1 AND t.next_run_at <= ?
		 ORDER BY t.id LIMIT 500`, pid, now.Unix())
	if err != nil {
		return err
	}
	due := []dueTracker{}
	for rows.Next() {
		var item dueTracker
		if err := rows.Scan(&item.id, &item.keywordID, &item.locationID, &item.engine,
			&item.provider, &item.device, &item.frequency, &item.dailyDepth, &item.weeklyDepth); err != nil {
			rows.Close()
			return err
		}
		due = append(due, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	type groupKey struct {
		keywordID, locationID    int64
		engine, provider, device string
	}
	type groupValue struct {
		trackers []dueTracker
		depth    int
	}
	groups := map[groupKey]groupValue{}
	weekly := now.Weekday() == time.Sunday
	for _, tracker := range due {
		depth := tracker.dailyDepth
		if tracker.frequency == defaultTrackingFrequency && weekly && tracker.weeklyDepth > depth {
			depth = tracker.weeklyDepth
		}
		key := groupKey{tracker.keywordID, tracker.locationID, tracker.engine, tracker.provider, tracker.device}
		value := groups[key]
		value.trackers = append(value.trackers, tracker)
		if depth > value.depth {
			value.depth = depth
		}
		groups[key] = value
	}
	month := now.Format("2006-01")
	var committed float64
	if err := db.QueryRow(`SELECT COALESCE(SUM(CASE WHEN actual_cost_usd > 0 THEN actual_cost_usd ELSE estimated_cost_usd END), 0)
		FROM serp_refresh_jobs WHERE project_id = ? AND substr(observed_date, 1, 7) = ?
		AND (status IN ('pending', 'submitted', 'complete') OR provider_task_id != '' OR actual_cost_usd > 0)`, pid, month).Scan(&committed); err != nil {
		return err
	}
	for key, value := range groups {
		if key.provider != "dataforseo" || key.engine != "google" {
			setTrackerSchedule(db, value.trackers, now, "automatic tracking currently requires DataForSEO Google SERPs")
			continue
		}
		estimated := estimateStandardSERPCost(value.depth)
		status := "pending"
		errorText := ""
		if committed+estimated > settings.MonthlyBudgetUSD {
			status = "budget_blocked"
			errorText = fmt.Sprintf("monthly SERP budget of $%.2f reached", settings.MonthlyBudgetUSD)
		}
		result, err := db.Exec(`INSERT OR IGNORE INTO serp_refresh_jobs
			(project_id, keyword_id, location_id, search_engine, provider, device,
			 depth, observed_date, status, estimated_cost_usd, next_attempt_at, last_error)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, pid, key.keywordID, key.locationID,
			key.engine, key.provider, key.device, value.depth, now.Format("2006-01-02"), status,
			estimated, now.Unix(), errorText)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected > 0 && status == "pending" {
			committed += estimated
		}
		setTrackerSchedule(db, value.trackers, now, errorText)
	}
	return nil
}

func setTrackerSchedule(db *sql.DB, trackers []dueTracker, now time.Time, lastError string) {
	for _, tracker := range trackers {
		_, _ = db.Exec(`UPDATE serp_trackers SET next_run_at = ?, last_error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, nextTrackingRun(now, tracker.id, tracker.frequency), lastError, tracker.id)
	}
}

func nextTrackingRun(now time.Time, trackerID int64, frequency string) int64 {
	now = now.UTC()
	var next time.Time
	switch frequency {
	case "weekly":
		next = now.Truncate(24*time.Hour).AddDate(0, 0, 7)
	case "monthly":
		year, month, day := now.Date()
		month++
		lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
		if day > lastDay {
			day = lastDay
		}
		next = time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	default:
		next = now.Truncate(24*time.Hour).AddDate(0, 0, 1)
	}
	// Spread calls deterministically across the first six UTC hours.
	jitter := time.Duration((trackerID*7919)%21600) * time.Second
	return next.Add(jitter).Unix()
}

func estimateStandardSERPCost(depth int) float64 {
	pages := int(math.Ceil(float64(depth) / 10))
	if pages <= 1 {
		return dataForSEOStandardFirstPageCost
	}
	return dataForSEOStandardFirstPageCost + float64(pages-1)*dataForSEOStandardAdditionalPageCost
}

func submitPendingRefreshJobs(ctx *sdk.AppCtx, pid string, now time.Time) error {
	var pending int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM serp_refresh_jobs
		WHERE project_id = ? AND status = 'pending' AND next_attempt_at <= ?`, pid, now.Unix()).Scan(&pending); err != nil {
		return err
	}
	if pending == 0 {
		return nil
	}
	provider, err := selectProvider(ctx, "dataforseo")
	if err != nil {
		if errors.Is(err, errProviderUnbound) {
			message := "DataForSEO is not bound; scheduled check skipped"
			_, _ = ctx.AppDB().Exec(`UPDATE serp_trackers SET last_error = ?, updated_at = CURRENT_TIMESTAMP
				WHERE project_id = ? AND enabled = 1`, message, pid)
			_, _ = ctx.AppDB().Exec(`UPDATE serp_refresh_jobs SET status = 'failed', completed_at = ?,
				last_error = ?, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND status = 'pending'`,
				now.Unix(), message, pid)
			return nil
		}
		return err
	}
	dfs, ok := provider.(*dataForSEOProvider)
	if !ok {
		return errors.New("automatic rank tracking requires DataForSEO")
	}
	rows, err := ctx.AppDB().Query(`SELECT j.id, j.project_id, j.keyword_id, k.text,
		j.location_id, j.search_engine, j.provider, j.device, j.depth, j.observed_date,
		j.provider_task_id, j.attempts
		FROM serp_refresh_jobs j JOIN keywords k ON k.id = j.keyword_id
		WHERE j.project_id = ? AND j.status = 'pending' AND j.next_attempt_at <= ?
		ORDER BY j.id LIMIT 100`, pid, now.Unix())
	if err != nil {
		return err
	}
	jobs := []refreshJob{}
	for rows.Next() {
		var job refreshJob
		if err := rows.Scan(&job.ID, &job.ProjectID, &job.KeywordID, &job.KeywordText,
			&job.LocationID, &job.SearchEngine, &job.Provider, &job.Device, &job.Depth,
			&job.ObservedDate, &job.TaskID, &job.Attempts); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, job := range jobs {
		var chargedCost float64
		loc, err := getLocation(ctx.AppDB(), job.LocationID)
		if err == nil {
			if loc.LocationCode == nil {
				err = errors.New("DataForSEO location has no location_code")
			} else {
				job.TaskID, chargedCost, err = submitDataForSEOSERPTask(ctx, dfs.connID, job.KeywordText, loc, job.Depth, job.Device)
			}
		}
		if err != nil {
			attempts := job.Attempts + 1
			status := "pending"
			if attempts >= 3 {
				status = "failed"
			}
			_, _ = ctx.AppDB().Exec(`UPDATE serp_refresh_jobs SET status = ?, attempts = ?,
				next_attempt_at = ?, last_error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
				status, attempts, now.Add(time.Duration(attempts*15)*time.Minute).Unix(), err.Error(), job.ID)
			continue
		}
		_, err = ctx.AppDB().Exec(`UPDATE serp_refresh_jobs SET status = 'submitted',
			provider_task_id = ?, actual_cost_usd = ?, attempts = attempts + 1, submitted_at = ?, last_error = '',
			updated_at = CURRENT_TIMESTAMP WHERE id = ? AND status = 'pending'`, job.TaskID, chargedCost, now.Unix(), job.ID)
		if err != nil {
			return err
		}
	}
	return nil
}

func submitDataForSEOSERPTask(ctx *sdk.AppCtx, connID int64, keyword string, loc *SEOLocation, depth int, device string) (string, float64, error) {
	input := map[string]any{
		"keyword": keyword, "location_code": *loc.LocationCode,
		"language_code": strings.ToLower(loc.LanguageCode), "device": device, "depth": depth,
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "serp_organic_task_post", map[string]any{"tasks": []map[string]any{input}})
	if err != nil {
		return "", 0, fmt.Errorf("dataforseo queue submit: %w", err)
	}
	if !res.Success || res.Status >= 400 {
		return "", 0, &providerRequestError{Provider: "dataforseo", Status: res.Status, Message: "queue submit failed"}
	}
	var env dfsEnvelope
	if err := json.Unmarshal(res.Data, &env); err != nil {
		return "", 0, fmt.Errorf("dataforseo queue submit: parse envelope: %w", err)
	}
	if env.StatusCode != 20000 || len(env.Tasks) == 0 {
		return "", 0, fmt.Errorf("dataforseo queue submit: status %d: %s", env.StatusCode, env.StatusMsg)
	}
	task := env.Tasks[0]
	if task.ID == "" || (task.StatusCode != 20000 && task.StatusCode != 20100) {
		return "", 0, fmt.Errorf("dataforseo queue submit: task status %d: %s", task.StatusCode, task.StatusMsg)
	}
	return task.ID, task.Cost, nil
}

func collectDataForSEOSERPTask(ctx *sdk.AppCtx, connID int64, taskID string) (*providerSERPResponse, float64, bool, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "serp_organic_task_get", map[string]any{"id": taskID})
	if err != nil {
		return nil, 0, false, fmt.Errorf("dataforseo queue collect: %w", err)
	}
	if !res.Success || res.Status >= 400 {
		return nil, 0, false, &providerRequestError{Provider: "dataforseo", Status: res.Status, Message: "queue collect failed"}
	}
	var env dfsEnvelope
	if err := json.Unmarshal(res.Data, &env); err != nil {
		return nil, 0, false, fmt.Errorf("dataforseo queue collect: parse envelope: %w", err)
	}
	if env.StatusCode != 20000 {
		return nil, 0, false, fmt.Errorf("dataforseo queue collect: status %d: %s", env.StatusCode, env.StatusMsg)
	}
	if len(env.Tasks) == 0 || len(env.Tasks[0].Result) == 0 {
		return nil, 0, false, nil
	}
	task := env.Tasks[0]
	if task.StatusCode != 20000 {
		if task.StatusCode == 20100 {
			return nil, task.Cost, false, nil
		}
		return nil, task.Cost, false, fmt.Errorf("dataforseo queue collect: task status %d: %s", task.StatusCode, task.StatusMsg)
	}
	taskRaw, _ := json.Marshal(task)
	return &providerSERPResponse{Tool: "serp_organic_task_get", ResultRaw: task.Result[0], Raw: taskRaw}, task.Cost, true, nil
}

func completeRefreshJob(db *sql.DB, job refreshJob, response *providerSERPResponse, cost float64) error {
	keywordID := job.KeywordID
	snapshotID, rankings, err := persistSERPSnapshot(db, job.ProjectID, job.SearchEngine, &keywordID,
		job.KeywordText, job.LocationID, job.Provider, job.Depth, response)
	if err != nil {
		return err
	}
	rows, err := db.Query(`SELECT t.id, e.id, e.search_engine, e.entity_type, e.identifier,
		e.label, e.url, e.created_at, e.updated_at
		FROM serp_trackers t JOIN search_entities e ON e.id = t.entity_id
		WHERE t.project_id = ? AND t.keyword_id = ? AND t.provider = ? AND t.device = ? AND t.enabled = 1`,
		job.ProjectID, job.KeywordID, job.Provider, job.Device)
	if err != nil {
		return err
	}
	type trackedTarget struct {
		trackerID int64
		entity    SearchEntity
	}
	targets := []trackedTarget{}
	for rows.Next() {
		var target trackedTarget
		if err := rows.Scan(&target.trackerID, &target.entity.ID, &target.entity.SearchEngine,
			&target.entity.EntityType, &target.entity.Identifier, &target.entity.Label,
			&target.entity.URL, &target.entity.CreatedAt, &target.entity.UpdatedAt); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, target)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := time.Now().UTC().Unix()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, target := range targets {
		rank, rankURL := findTargetRank(target.entity, rankings)
		found := rank != nil
		_, err := tx.Exec(`INSERT INTO serp_rank_observations
			(project_id, tracker_id, job_id, snapshot_id, observed_date, ts, found,
			 rank, rank_url, checked_depth, provider)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(tracker_id, observed_date) DO UPDATE SET
			 job_id = excluded.job_id, snapshot_id = excluded.snapshot_id, ts = excluded.ts,
			 found = excluded.found, rank = excluded.rank, rank_url = excluded.rank_url,
			 checked_depth = excluded.checked_depth, provider = excluded.provider`,
			job.ProjectID, target.trackerID, job.ID, snapshotID, job.ObservedDate, now,
			boolInt(found), rank, rankURL, job.Depth, job.Provider)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE serp_trackers SET last_attempt_at = ?, last_success_at = ?,
			last_error = '', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, now, now, target.trackerID)
		if err != nil {
			return err
		}
	}
	if cost <= 0 {
		cost = estimateStandardSERPCost(job.Depth)
	}
	_, err = tx.Exec(`UPDATE serp_refresh_jobs SET status = 'complete', snapshot_id = ?,
		actual_cost_usd = MAX(actual_cost_usd, ?), completed_at = ?, last_error = '', updated_at = CURRENT_TIMESTAMP
		WHERE id = ?`, snapshotID, cost, now, job.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func findTargetRank(target SearchEntity, rankings []SearchRanking) (*int64, string) {
	var best *int64
	bestURL := ""
	targetIdentifier := strings.TrimSpace(target.Identifier)
	for _, row := range rankings {
		matched := false
		switch target.SearchEngine + "/" + target.EntityType {
		case "google/domain":
			targetHost := normaliseHost(targetIdentifier)
			if targetHost == "" {
				targetHost = normaliseHost(target.URL)
			}
			rowHost := normaliseHost(row.URL)
			matched = rowHost == targetHost || (targetHost != "" && strings.HasSuffix(rowHost, "."+targetHost))
		case "google/page":
			matched = normalizeTrackingURL(row.URL) == normalizeTrackingURL(targetIdentifier)
			if !matched && target.URL != "" {
				matched = normalizeTrackingURL(row.URL) == normalizeTrackingURL(target.URL)
			}
		case "youtube/video":
			matched = row.Identifier == youtubeVideoID(targetIdentifier) || row.Identifier == targetIdentifier
		case "youtube/channel":
			want := youtubeChannelID(targetIdentifier)
			got := youtubeChannelID(row.ChannelIdentifier)
			matched = want != "" && got == want
			if !matched && target.Label != "" {
				matched = strings.EqualFold(strings.TrimSpace(row.ChannelTitle), strings.TrimSpace(target.Label))
			}
		}
		if !matched || row.Rank == nil || (best != nil && *row.Rank >= *best) {
			continue
		}
		value := *row.Rank
		best = &value
		bestURL = row.URL
	}
	return best, bestURL
}

func normalizeTrackingURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), "/")
	}
	u.Fragment = ""
	u.Host = strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	u.Scheme = "https"
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u.String()
}

func listRankTrackers(db *sql.DB, pid string, keywordID int64) ([]rankTracker, error) {
	query := `SELECT t.id, t.project_id, t.keyword_id, k.text, k.search_engine, k.location_id,
		t.entity_id, e.entity_type, e.identifier, e.label, t.provider, t.device, t.enabled,
		t.frequency, t.daily_depth, t.weekly_depth, t.next_run_at, t.last_attempt_at, t.last_success_at, t.last_error
		FROM serp_trackers t
		JOIN keywords k ON k.id = t.keyword_id
		JOIN search_entities e ON e.id = t.entity_id
		WHERE t.project_id = ?`
	args := []any{pid}
	if keywordID > 0 {
		query += " AND t.keyword_id = ?"
		args = append(args, keywordID)
	}
	query += " ORDER BY k.text, e.label, e.identifier"
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []rankTracker{}
	for rows.Next() {
		var item rankTracker
		var enabled int
		var attempt, success sql.NullInt64
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.KeywordID, &item.KeywordText,
			&item.SearchEngine, &item.LocationID, &item.EntityID, &item.EntityType,
			&item.EntityIdentifier, &item.EntityLabel, &item.Provider, &item.Device,
			&enabled, &item.Frequency, &item.DailyDepth, &item.WeeklyDepth, &item.NextRunAt,
			&attempt, &success, &item.LastError); err != nil {
			return nil, err
		}
		item.Enabled = enabled != 0
		if attempt.Valid {
			item.LastAttemptAt = &attempt.Int64
		}
		if success.Valid {
			item.LastSuccessAt = &success.Int64
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func listRankHistory(db *sql.DB, pid string, trackerID int64, limit int) ([]rankObservation, error) {
	if limit <= 0 || limit > 2000 {
		limit = 400
	}
	rows, err := db.Query(`SELECT o.id, o.tracker_id, o.observed_date, o.ts, o.found,
		o.rank, o.rank_url, o.checked_depth, o.provider
		FROM serp_rank_observations o JOIN serp_trackers t ON t.id = o.tracker_id
		WHERE o.project_id = ? AND o.tracker_id = ? AND t.project_id = ?
		ORDER BY o.observed_date DESC LIMIT ?`, pid, trackerID, pid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []rankObservation{}
	for rows.Next() {
		var item rankObservation
		var found int
		var rank sql.NullInt64
		if err := rows.Scan(&item.ID, &item.TrackerID, &item.ObservedDate, &item.TS,
			&found, &rank, &item.RankURL, &item.CheckedDepth, &item.Provider); err != nil {
			return nil, err
		}
		item.Found = found != 0
		if rank.Valid {
			item.Rank = &rank.Int64
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (a *App) toolRankTrackersList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return listRankTrackers(ctx.AppDB(), projectScopeFromArgs(ctx, args), toInt64(args["keyword_id"]))
}

func (a *App) toolRankHistory(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	trackerID := toInt64(args["tracker_id"])
	if trackerID <= 0 {
		return nil, errors.New("tracker_id required")
	}
	return listRankHistory(ctx.AppDB(), projectScopeFromArgs(ctx, args), trackerID, int(toInt64(args["limit"])))
}

func (a *App) handleRankTracking(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	pid := projectScopeFromRequest(ctx, r)
	switch r.Method {
	case http.MethodGet:
		keywordID, _ := strconv.ParseInt(r.URL.Query().Get("keyword_id"), 10, 64)
		trackers, err := listRankTrackers(ctx.AppDB(), pid, keywordID)
		writeJSONOrErr(w, map[string]any{"trackers": trackers}, err)
	case http.MethodPost:
		if _, err := selectProvider(ctx, "dataforseo"); err != nil {
			writeJSONOrErr(w, nil, err)
			return
		}
		var body struct {
			KeywordID   int64  `json:"keyword_id"`
			EntityID    int64  `json:"entity_id"`
			DomainID    int64  `json:"domain_id"`
			Provider    string `json:"provider"`
			Device      string `json:"device"`
			Frequency   string `json:"frequency"`
			DailyDepth  int    `json:"daily_depth"`
			WeeklyDepth int    `json:"weekly_depth"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		tracker, err := enableRankTracker(ctx.AppDB(), pid, body.KeywordID, body.EntityID,
			body.DomainID, body.Provider, body.Device, body.Frequency, body.DailyDepth, body.WeeklyDepth)
		writeJSONOrErr(w, tracker, err)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleRankTrackingItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPatch {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, err := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, "/rank-tracking/"), "/"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid tracker id", http.StatusBadRequest)
		return
	}
	ctx := mustCtx(r)
	pid := projectScopeFromRequest(ctx, r)
	if r.Method == http.MethodDelete {
		result, err := ctx.AppDB().Exec(`UPDATE serp_trackers SET enabled = 0,
			updated_at = CURRENT_TIMESTAMP WHERE id = ? AND project_id = ?`, id, pid)
		if err == nil {
			if affected, _ := result.RowsAffected(); affected == 0 {
				err = sql.ErrNoRows
			}
		}
		writeJSONOrErr(w, map[string]any{"id": id, "enabled": false}, err)
		return
	}
	var body struct {
		Enabled   *bool   `json:"enabled"`
		Frequency *string `json:"frequency"`
	}
	if r.Method == http.MethodPatch {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if body.Enabled == nil && body.Frequency == nil {
		http.Error(w, "enabled or frequency required", http.StatusBadRequest)
		return
	}
	var enabled int
	var frequency string
	if err := ctx.AppDB().QueryRow(`SELECT enabled, frequency FROM serp_trackers
		WHERE id = ? AND project_id = ?`, id, pid).Scan(&enabled, &frequency); err != nil {
		writeJSONOrErr(w, nil, err)
		return
	}
	resetSchedule := false
	if body.Enabled != nil {
		enabled = boolInt(*body.Enabled)
		resetSchedule = *body.Enabled
	}
	if body.Frequency != nil {
		updatedFrequency := normalizeTrackingFrequency(*body.Frequency)
		if updatedFrequency == "" {
			http.Error(w, "frequency must be daily, weekly, or monthly", http.StatusBadRequest)
			return
		}
		frequency = updatedFrequency
		resetSchedule = true
	}
	result, err := ctx.AppDB().Exec(`UPDATE serp_trackers SET
		enabled = ?, frequency = ?,
		next_run_at = CASE WHEN ? = 1 THEN 0 ELSE next_run_at END,
		last_error = CASE WHEN ? = 1 THEN '' ELSE last_error END,
		updated_at = CURRENT_TIMESTAMP WHERE id = ? AND project_id = ?`,
		enabled, frequency, boolInt(resetSchedule), boolInt(resetSchedule), id, pid)
	if err == nil {
		if affected, _ := result.RowsAffected(); affected == 0 {
			err = sql.ErrNoRows
		}
	}
	writeJSONOrErr(w, map[string]any{"id": id, "enabled": enabled != 0, "frequency": frequency}, err)
}

func (a *App) handleRankHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	trackerID, _ := strconv.ParseInt(r.URL.Query().Get("tracker_id"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if trackerID <= 0 {
		http.Error(w, "tracker_id required", http.StatusBadRequest)
		return
	}
	ctx := mustCtx(r)
	history, err := listRankHistory(ctx.AppDB(), projectScopeFromRequest(ctx, r), trackerID, limit)
	writeJSONOrErr(w, map[string]any{"history": history}, err)
}

func (a *App) handleRankTrackingSettings(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	pid := projectScopeFromRequest(ctx, r)
	settings, err := getRankTrackingSettings(ctx.AppDB(), pid)
	if err != nil {
		writeJSONOrErr(w, nil, err)
		return
	}
	if r.Method == http.MethodPatch || r.Method == http.MethodPost {
		var body struct {
			Enabled          *bool    `json:"enabled"`
			MonthlyBudgetUSD *float64 `json:"monthly_budget_usd"`
			DailyDepth       *int     `json:"daily_depth"`
			WeeklyDepth      *int     `json:"weekly_depth"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Enabled != nil {
			settings.Enabled = *body.Enabled
		}
		if body.MonthlyBudgetUSD != nil {
			settings.MonthlyBudgetUSD = *body.MonthlyBudgetUSD
		}
		if body.DailyDepth != nil {
			settings.DailyDepth = *body.DailyDepth
		}
		if body.WeeklyDepth != nil {
			settings.WeeklyDepth = *body.WeeklyDepth
		}
		err = saveRankTrackingSettings(ctx.AppDB(), settings)
		if err == nil {
			// A raised cap or re-enabled scheduler should get another chance on the
			// next worker tick. No provider request was made for blocked rows.
			_, _ = ctx.AppDB().Exec(`DELETE FROM serp_refresh_jobs
				WHERE project_id = ? AND status = 'budget_blocked' AND observed_date >= ?`, pid, time.Now().UTC().Format("2006-01-02"))
			_, _ = ctx.AppDB().Exec(`UPDATE serp_trackers SET next_run_at = 0, updated_at = CURRENT_TIMESTAMP
				WHERE project_id = ? AND enabled = 1`, pid)
		}
	} else if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSONOrErr(w, settings, err)
}

func enableRankTracker(db *sql.DB, pid string, keywordID, entityID, domainID int64, provider, device, frequency string, dailyDepth, weeklyDepth int) (*rankTracker, error) {
	if keywordID <= 0 {
		return nil, errors.New("keyword_id required")
	}
	settings, err := getRankTrackingSettings(db, pid)
	if err != nil {
		return nil, err
	}
	if provider == "" {
		provider = "dataforseo"
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider != "dataforseo" {
		return nil, errors.New("automatic rank tracking currently uses DataForSEO Standard Queue")
	}
	if device == "" {
		device = "desktop"
	}
	if device != "desktop" && device != "mobile" {
		return nil, errors.New("device must be desktop or mobile")
	}
	if frequency == "" {
		frequency = defaultTrackingFrequency
	}
	frequency = normalizeTrackingFrequency(frequency)
	if frequency == "" {
		return nil, errors.New("frequency must be daily, weekly, or monthly")
	}
	if dailyDepth == 0 {
		dailyDepth = settings.DailyDepth
	}
	if weeklyDepth == 0 {
		weeklyDepth = settings.WeeklyDepth
	}
	if dailyDepth < 10 || dailyDepth > 100 || weeklyDepth < dailyDepth || weeklyDepth > 100 {
		return nil, errors.New("depths must be 10-100 and weekly_depth must be at least daily_depth")
	}
	keyword, err := getKeyword(db, pid, keywordID)
	if err != nil {
		return nil, err
	}
	location, err := getLocation(db, keyword.LocationID)
	if err != nil {
		return nil, err
	}
	if location.Provider != "dataforseo" || location.LocationCode == nil {
		return nil, errors.New("automatic DataForSEO tracking requires the keyword to use a DataForSEO locale")
	}
	if entityID <= 0 && domainID > 0 {
		domain, err := getDomain(db, pid, domainID)
		if err != nil {
			return nil, err
		}
		entityID, err = upsertSearchEntity(db, pid, "google", "domain", domain.Host, domain.Label, "https://"+domain.Host, domain.DefaultLocationID, "{}")
		if err != nil {
			return nil, err
		}
	}
	if entityID <= 0 {
		return nil, errors.New("entity_id or domain_id required")
	}
	entity, err := getSearchEntity(db, pid, entityID)
	if err != nil {
		return nil, err
	}
	if keyword.SearchEngine != entity.SearchEngine {
		return nil, fmt.Errorf("keyword uses %s but target entity uses %s", keyword.SearchEngine, entity.SearchEngine)
	}
	_, err = db.Exec(`INSERT INTO serp_trackers
		(project_id, keyword_id, entity_id, provider, device, frequency, enabled, daily_depth, weekly_depth, next_run_at, last_error)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, 0, '')
		ON CONFLICT(project_id, keyword_id, entity_id, provider, device) DO UPDATE SET
		 enabled = 1, frequency = excluded.frequency, daily_depth = excluded.daily_depth, weekly_depth = excluded.weekly_depth,
		 next_run_at = 0, last_error = '', updated_at = CURRENT_TIMESTAMP`,
		pid, keywordID, entityID, provider, device, frequency, dailyDepth, weeklyDepth)
	if err != nil {
		return nil, err
	}
	trackers, err := listRankTrackers(db, pid, keywordID)
	if err != nil {
		return nil, err
	}
	for i := range trackers {
		if trackers[i].EntityID == entityID && trackers[i].Provider == provider && trackers[i].Device == device {
			return &trackers[i], nil
		}
	}
	return nil, sql.ErrNoRows
}

func normalizeTrackingFrequency(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "daily":
		return "daily"
	case "weekly":
		return "weekly"
	case "monthly":
		return "monthly"
	default:
		return ""
	}
}

func projectScopeFromRequest(ctx *sdk.AppCtx, r *http.Request) string {
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		return pid
	}
	return projectScope(ctx)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
