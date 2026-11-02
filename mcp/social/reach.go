// reach — thumbnail impressions and click-through rate. YouTube only
// publishes them as bulk daily reach reports (Reporting API): Social
// creates one reporting job per channel, then the analytics collector
// downloads each new daily file into reach_daily. account_metrics and
// post_metrics read impressions and impressions_ctr from there, with
// impressions_coverage saying which days are in.
package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	reachCheckInterval = 6 * time.Hour
	reachJobName       = "Apteva Social reach"

	reachNotStarted   = "not_started"
	reachCollecting   = "collecting"
	reachOK           = "ok"
	reachNeedsUpdate  = "needs_platform_update"
	reachNeedsConnect = "needs_reconnect"
	reachUnavailable  = "unavailable"
	reachError        = "error"
)

// youtubeReachReportTypes are tried in order; any other channel report
// type with "reach" in its ID is the fallback.
var youtubeReachReportTypes = []string{"channel_reach_basic_a1", "channel_reach_combined_a1"}

type reachCoverage struct {
	State     string `json:"state"`
	FirstDay  string `json:"first_day,omitempty"`
	LastDay   string `json:"last_day,omitempty"`
	Note      string `json:"note,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

type reachTotals struct {
	Impressions int64
	Clicks      float64
}

func (t reachTotals) ctr() float64 {
	if t.Impressions <= 0 {
		return 0
	}
	return math.Round(t.Clicks/float64(t.Impressions)*1e6) / 1e6
}

// collectProjectReach refreshes reach for the project's direct YouTube
// accounts, at most every reachCheckInterval per account.
func (a *App) collectProjectReach(runCtx context.Context, app *sdk.AppCtx, pid string) {
	rows, err := app.AppDB().Query(`SELECT id, connection_id FROM social_accounts
		WHERE project_id=? AND status='active' AND platform='youtube' AND COALESCE(provider_slug,'native')='native' ORDER BY id`, pid)
	if err != nil {
		app.Logger().Warn("reach: list accounts", "project", pid, "err", err)
		return
	}
	type account struct{ id, conn int64 }
	var accounts []account
	for rows.Next() {
		var acc account
		if rows.Scan(&acc.id, &acc.conn) == nil {
			accounts = append(accounts, acc)
		}
	}
	rows.Close()
	ctx := app.WithProject(pid)
	for _, acc := range accounts {
		if runCtx.Err() != nil {
			return
		}
		if !reachDue(ctx.AppDB(), acc.id, time.Now().UTC()) {
			continue
		}
		if err := a.syncYouTubeReach(ctx, pid, acc.id, acc.conn); err != nil {
			app.Logger().Warn("reach: sync failed", "project", pid, "account", acc.id, "err", err)
		}
	}
}

func reachDue(db *sql.DB, accountID int64, now time.Time) bool {
	var last string
	if db.QueryRow(`SELECT COALESCE(last_checked_at,'') FROM reach_jobs WHERE social_account_id=?`, accountID).Scan(&last) != nil || last == "" {
		return true
	}
	checked, err := time.Parse(time.RFC3339, last)
	return err != nil || now.Sub(checked) >= reachCheckInterval
}

// syncYouTubeReach makes sure the channel has a reach job, then reads
// every report created since the last one it read.
func (a *App) syncYouTubeReach(ctx *sdk.AppCtx, pid string, accountID, connID int64) error {
	db := ctx.AppDB()
	_, _ = db.Exec(`INSERT INTO reach_jobs(social_account_id,project_id,platform) VALUES(?,?,'youtube') ON CONFLICT(social_account_id) DO NOTHING`, accountID, pid)
	var jobID, cursor string
	if err := db.QueryRow(`SELECT job_id, reports_cursor FROM reach_jobs WHERE social_account_id=?`, accountID).Scan(&jobID, &cursor); err != nil {
		return err
	}
	if jobID == "" {
		var err error
		if jobID, err = ensureYouTubeReachJob(ctx, accountID, connID); err != nil {
			recordReachState(db, accountID, reachErrorState(err), err.Error())
			return err
		}
	}
	reports, err := listYouTubeReachReports(ctx, connID, jobID, cursor)
	if err != nil {
		recordReachState(db, accountID, reachErrorState(err), err.Error())
		return err
	}
	for _, report := range reports {
		if err := importYouTubeReachReport(ctx, accountID, connID, report); err != nil {
			recordReachState(db, accountID, reachErrorState(err), err.Error())
			return err
		}
		_, _ = db.Exec(`UPDATE reach_jobs SET reports_cursor=? WHERE social_account_id=?`, report.CreateTime, accountID)
	}
	state := reachCollecting
	var days int
	_ = db.QueryRow(`SELECT COUNT(DISTINCT day) FROM reach_daily WHERE social_account_id=?`, accountID).Scan(&days)
	if days > 0 {
		state = reachOK
	}
	recordReachState(db, accountID, state, "")
	return nil
}

func recordReachState(db *sql.DB, accountID int64, state, detail string) {
	_, _ = db.Exec(`UPDATE reach_jobs SET state=?, last_error=?, last_checked_at=? WHERE social_account_id=?`,
		state, nullable(detail), time.Now().UTC().Format(time.RFC3339), accountID)
}

func reachErrorState(err error) string {
	text := err.Error()
	switch {
	case strings.HasPrefix(text, youtubeErrPrefixCat):
		return reachNeedsUpdate
	case strings.HasPrefix(text, youtubeErrPrefixPerm):
		return reachNeedsConnect
	case strings.HasPrefix(text, "reach_unavailable:"):
		return reachUnavailable
	}
	return reachError
}

func ensureYouTubeReachJob(ctx *sdk.AppCtx, accountID, connID int64) (string, error) {
	db := ctx.AppDB()
	var jobs struct {
		Jobs []struct {
			ID           string `json:"id"`
			ReportTypeID string `json:"reportTypeId"`
			CreateTime   string `json:"createTime"`
		} `json:"jobs"`
	}
	if err := youtubeReachCall(ctx, connID, "list_reporting_jobs", map[string]any{}, &jobs); err != nil {
		return "", err
	}
	var types struct {
		ReportTypes []struct {
			ID string `json:"id"`
		} `json:"reportTypes"`
	}
	if err := youtubeReachCall(ctx, connID, "list_reporting_report_types", map[string]any{}, &types); err != nil {
		return "", err
	}
	available := make([]string, 0, len(types.ReportTypes))
	for _, t := range types.ReportTypes {
		available = append(available, t.ID)
	}
	reportType := pickReachReportType(available)
	if reportType == "" {
		return "", errors.New("reach_unavailable: YouTube offers this channel no reach report type")
	}
	jobID, created := "", ""
	for _, job := range jobs.Jobs {
		if job.ReportTypeID == reportType {
			jobID, created = job.ID, job.CreateTime
			break
		}
	}
	if jobID == "" {
		var job struct {
			ID         string `json:"id"`
			CreateTime string `json:"createTime"`
		}
		if err := youtubeReachCall(ctx, connID, "create_reporting_job", map[string]any{"reportTypeId": reportType, "name": reachJobName}, &job); err != nil {
			return "", err
		}
		if job.ID == "" {
			return "", errors.New("create_reporting_job returned no job ID")
		}
		jobID, created = job.ID, job.CreateTime
	}
	_, err := db.Exec(`UPDATE reach_jobs SET report_type_id=?, job_id=?, job_created_at=?, state=? WHERE social_account_id=?`, reportType, jobID, nullable(created), reachCollecting, accountID)
	return jobID, err
}

func pickReachReportType(available []string) string {
	for _, preferred := range youtubeReachReportTypes {
		if containsFold(available, preferred) {
			return preferred
		}
	}
	sort.Strings(available)
	for _, id := range available {
		if strings.HasPrefix(id, "channel_") && strings.Contains(id, "reach") {
			return id
		}
	}
	return ""
}

type youtubeReachReport struct {
	ID          string `json:"id"`
	StartTime   string `json:"startTime"`
	CreateTime  string `json:"createTime"`
	DownloadURL string `json:"downloadUrl"`
}

func listYouTubeReachReports(ctx *sdk.AppCtx, connID int64, jobID, createdAfter string) ([]youtubeReachReport, error) {
	var all []youtubeReachReport
	token := ""
	for page := 0; page < 20; page++ {
		input := map[string]any{"jobId": jobID}
		if createdAfter != "" {
			input["createdAfter"] = createdAfter
		}
		if token != "" {
			input["pageToken"] = token
		}
		var resp struct {
			Reports       []youtubeReachReport `json:"reports"`
			NextPageToken string               `json:"nextPageToken"`
		}
		if err := youtubeReachCall(ctx, connID, "list_reporting_reports", input, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Reports...)
		if token = resp.NextPageToken; token == "" {
			break
		}
	}
	// Oldest first, so the cursor only moves past reports already stored.
	sort.SliceStable(all, func(i, j int) bool { return all[i].CreateTime < all[j].CreateTime })
	return all, nil
}

// youtubeReportIDs reads the channel, job and report IDs from a report's
// downloadUrl; the download tool only accepts those, never the URL.
func youtubeReportIDs(downloadURL string) (channelID, jobID, reportID string, err error) {
	u, err := url.Parse(downloadURL)
	if err != nil || u.Host != "youtubereporting.googleapis.com" {
		return "", "", "", fmt.Errorf("unexpected reach report URL %q", downloadURL)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	// v1/media/CHANNEL/{channel}/jobs/{job}/reports/{report}
	if len(parts) != 8 || parts[0] != "v1" || parts[1] != "media" || parts[2] != "CHANNEL" || parts[4] != "jobs" || parts[6] != "reports" {
		return "", "", "", fmt.Errorf("unexpected reach report URL %q", downloadURL)
	}
	return parts[3], parts[5], parts[7], nil
}

func importYouTubeReachReport(ctx *sdk.AppCtx, accountID, connID int64, report youtubeReachReport) error {
	channelID, jobID, reportID, err := youtubeReportIDs(report.DownloadURL)
	if err != nil {
		return err
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "download_reporting_report", map[string]any{"channelId": channelID, "jobId": jobID, "reportId": reportID, "alt": "media"})
	if err != nil || res == nil || !res.Success {
		return youtubeAPIError("download_reporting_report", res, err)
	}
	body, err := reportFileBytes(res.Data)
	if err != nil {
		return err
	}
	rows, err := parseYouTubeReachCSV(body)
	if err != nil {
		return fmt.Errorf("reach report %s: %w", reportID, err)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A regenerated report replaces the days it covers.
	days := map[string]bool{}
	for _, row := range rows {
		days[row.day] = true
	}
	for day := range days {
		if _, err := tx.Exec(`DELETE FROM reach_daily WHERE social_account_id=? AND day=?`, accountID, day); err != nil {
			return err
		}
	}
	for _, row := range rows {
		if _, err := tx.Exec(`INSERT INTO reach_daily(social_account_id,day,video_id,impressions,clicks,report_id) VALUES(?,?,?,?,?,?)`,
			accountID, row.day, row.videoID, row.impressions, row.clicks, reportID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// reportFileBytes accepts the download as a binary envelope or as text,
// depending on the content type Google sent.
func reportFileBytes(raw json.RawMessage) ([]byte, error) {
	var envelope struct {
		Binary bool   `json:"_binary"`
		Base64 string `json:"base64"`
	}
	if json.Unmarshal(raw, &envelope) == nil && envelope.Binary {
		return base64.StdEncoding.DecodeString(envelope.Base64)
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []byte(text), nil
	}
	return raw, nil
}

type reachRow struct {
	day         string
	videoID     string
	impressions int64
	clicks      float64
}

// parseYouTubeReachCSV sums a reach report to one row per day and video.
// Reports split each video across other dimensions (country, subscribed
// status...), so clicks are summed as impressions x CTR and the rate is
// recomputed from the totals.
func parseYouTubeReachCSV(body []byte) ([]reachRow, error) {
	reader := csv.NewReader(strings.NewReader(string(body)))
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	col := map[string]int{"date": -1, "video_id": -1, "impressions": -1, "ctr": -1}
	for i, name := range header {
		name = strings.ToLower(strings.TrimSpace(name))
		switch {
		case name == "date":
			col["date"] = i
		case name == "video_id":
			col["video_id"] = i
		case strings.Contains(name, "impressions_ctr"):
			col["ctr"] = i
		case strings.Contains(name, "thumbnail_impressions"):
			col["impressions"] = i
		}
	}
	for key, index := range col {
		if index < 0 {
			return nil, fmt.Errorf("missing %s column in %v", key, header)
		}
	}
	type key struct{ day, video string }
	type record struct {
		impressions int64
		ctr         float64
	}
	parsed := map[key][]record{}
	percent := false
	for {
		values, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		get := func(name string) string {
			if col[name] < len(values) {
				return strings.TrimSpace(values[col[name]])
			}
			return ""
		}
		day := get("date")
		if len(day) == 8 {
			day = day[:4] + "-" + day[4:6] + "-" + day[6:]
		}
		impressions, _ := strconv.ParseInt(get("impressions"), 10, 64)
		ctr, _ := strconv.ParseFloat(get("ctr"), 64)
		if ctr > 1 {
			percent = true
		}
		k := key{day, get("video_id")}
		parsed[k] = append(parsed[k], record{impressions, ctr})
	}
	rows := make([]reachRow, 0, len(parsed))
	for k, records := range parsed {
		row := reachRow{day: k.day, videoID: k.video}
		for _, r := range records {
			ctr := r.ctr
			// Rates above 1 can only be percentages; one such value means
			// the whole file is in percent.
			if percent {
				ctr /= 100
			}
			row.impressions += r.impressions
			row.clicks += float64(r.impressions) * ctr
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].day != rows[j].day {
			return rows[i].day < rows[j].day
		}
		return rows[i].videoID < rows[j].videoID
	})
	return rows, nil
}

func youtubeReachCall(ctx *sdk.AppCtx, connID int64, tool string, input map[string]any, out any) error {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, input)
	if err != nil || res == nil || !res.Success {
		return youtubeAPIError(tool, res, err)
	}
	if out != nil {
		if err := json.Unmarshal(res.Data, out); err != nil {
			return fmt.Errorf("%s: decode response: %w", tool, err)
		}
	}
	return nil
}

// ─── reading ───────────────────────────────────────────────────────

func loadReachCoverage(db *sql.DB, accountID int64) reachCoverage {
	coverage := reachCoverage{State: reachNotStarted}
	var lastError string
	if db.QueryRow(`SELECT state, COALESCE(last_error,'') FROM reach_jobs WHERE social_account_id=?`, accountID).Scan(&coverage.State, &lastError) == nil {
		coverage.LastError = lastError
	}
	_ = db.QueryRow(`SELECT COALESCE(MIN(day),''), COALESCE(MAX(day),'') FROM reach_daily WHERE social_account_id=?`, accountID).Scan(&coverage.FirstDay, &coverage.LastDay)
	switch coverage.State {
	case reachNotStarted:
		coverage.Note = "Reach collection starts with the next analytics run."
	case reachCollecting:
		coverage.Note = "The YouTube reach job exists; Google delivers its first daily reports within about 48 hours."
	case reachNeedsUpdate:
		coverage.Note = "The platform's YouTube integration has no Reporting API tools yet; update the platform."
	case reachNeedsConnect:
		coverage.Note = "Reconnect this YouTube channel in Integrations to grant analytics access."
	case reachUnavailable:
		coverage.Note = "YouTube offers no reach report for this channel."
	case reachOK:
		coverage.Note = "Thumbnail impressions and CTR from YouTube's daily reach reports; Google publishes each day about two days late."
	}
	return coverage
}

func reachTotalsFor(db *sql.DB, accountID int64, videoID, since, until string) (reachTotals, bool) {
	query := `SELECT COALESCE(SUM(impressions),0), COALESCE(SUM(clicks),0), COUNT(*) FROM reach_daily WHERE social_account_id=?`
	args := []any{accountID}
	if videoID != "" {
		query += ` AND video_id=?`
		args = append(args, videoID)
	}
	if since != "" {
		query += ` AND day>=?`
		args = append(args, since)
	}
	if until != "" {
		query += ` AND day<=?`
		args = append(args, until)
	}
	var totals reachTotals
	var rows int
	if db.QueryRow(query, args...).Scan(&totals.Impressions, &totals.Clicks, &rows) != nil || rows == 0 {
		return totals, false
	}
	return totals, true
}

func reachDailySeries(db *sql.DB, accountID int64, since, until string) insightSeries {
	rows, err := db.Query(`SELECT day, SUM(impressions), SUM(clicks) FROM reach_daily WHERE social_account_id=? AND day>=? AND day<=? GROUP BY day ORDER BY day`, accountID, since, until)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := insightSeries{}
	for rows.Next() {
		var day string
		var totals reachTotals
		if rows.Scan(&day, &totals.Impressions, &totals.Clicks) != nil {
			continue
		}
		ctr := totals.ctr()
		out["impressions"] = append(out["impressions"], insightPoint{Time: day, Value: totals.Impressions})
		out["impressions_ctr"] = append(out["impressions_ctr"], insightPoint{Time: day, Value: int64(math.Round(ctr * 100)), DecimalValue: &ctr})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// addYouTubeReach puts the channel's impressions and CTR for the query
// window on an account_metrics result.
func addYouTubeReach(ctx *sdk.AppCtx, out *accountMetricsResult, query analyticsQuery) {
	db := ctx.AppDB()
	coverage := loadReachCoverage(db, out.SocialAccountID)
	out.ImpressionsCoverage = &coverage
	since, until := query.dates()
	totals, ok := reachTotalsFor(db, out.SocialAccountID, "", since, until)
	if !ok {
		return
	}
	out.Impressions = totals.Impressions
	ctr := totals.ctr()
	out.ImpressionsCTR = &ctr
	out.Available = appendUnique(out.Available, "impressions", "impressions_ctr")
	if series := reachDailySeries(db, out.SocialAccountID, since, until); series != nil {
		if out.Insights == nil {
			out.Insights = insightSeries{}
		}
		mergeInsightSeries(out.Insights, series)
	}
}

// addYouTubeVideoReach puts a video's lifetime impressions and CTR on a
// post_metrics outcome.
func addYouTubeVideoReach(ctx *sdk.AppCtx, out *targetMetricsOutcome) {
	if out.Metrics == nil {
		return
	}
	db := ctx.AppDB()
	coverage := loadReachCoverage(db, out.SocialAccountID)
	out.ImpressionsCoverage = &coverage
	totals, ok := reachTotalsFor(db, out.SocialAccountID, out.PlatformPostID, "", "")
	if !ok {
		return
	}
	ctr := totals.ctr()
	out.Metrics.Impressions = totals.Impressions
	out.Metrics.ImpressionsCTR = &ctr
	out.Metrics.Available = appendUnique(out.Metrics.Available, "impressions", "impressions_ctr")
}

func appendUnique(values []string, more ...string) []string {
	for _, value := range more {
		if !containsFold(values, value) {
			values = append(values, value)
		}
	}
	return values
}
