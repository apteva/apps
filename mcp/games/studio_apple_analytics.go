package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const appleAnalyticsFamily = "store_analytics"
const appleAnalyticsTopic = "app_store_analytics"

const appleAnalyticsSchema = `
CREATE TABLE IF NOT EXISTS game_apple_report_requests (
 project_id TEXT NOT NULL, game_id TEXT NOT NULL, source_id TEXT NOT NULL,
 request_id TEXT NOT NULL, observed_at TEXT NOT NULL,
 PRIMARY KEY(project_id,game_id,source_id),
 FOREIGN KEY(project_id,game_id,source_id) REFERENCES game_metric_syncs(project_id,game_id,source_id));
CREATE TABLE IF NOT EXISTS game_apple_report_instances (
 project_id TEXT NOT NULL, game_id TEXT NOT NULL, source_id TEXT NOT NULL,
 instance_id TEXT NOT NULL, processing_date TEXT NOT NULL,
 PRIMARY KEY(project_id,game_id,source_id,instance_id),
 FOREIGN KEY(project_id,game_id,source_id) REFERENCES game_metric_syncs(project_id,game_id,source_id));
CREATE TABLE IF NOT EXISTS game_apple_report_days (
 project_id TEXT NOT NULL, game_id TEXT NOT NULL, source_id TEXT NOT NULL,
 report TEXT NOT NULL, event_date TEXT NOT NULL, processing_date TEXT NOT NULL, counts TEXT NOT NULL,
 PRIMARY KEY(project_id,game_id,source_id,report,event_date),
 FOREIGN KEY(project_id,game_id,source_id) REFERENCES game_metric_syncs(project_id,game_id,source_id));
`

type appleAnalyticsReport struct {
	key, name, dimension string
}

var appleAnalyticsReports = []appleAnalyticsReport{
	{"discovery", "App Store Discovery and Engagement", "event"},
	{"downloads", "App Store Downloads", "download type"},
	{"installations", "App Store Installations and Deletions", "event"},
}

func (r appleAnalyticsReport) metric(event, page string) string {
	switch r.key {
	case "discovery":
		if event == "impression" {
			return "impressions"
		}
		if event == "page view" && (page == "product page" || page == "store sheet") {
			return "product_page_views"
		}
	case "downloads":
		switch event {
		case "first-time download":
			return "first_time_downloads"
		case "redownload":
			return "redownloads"
		}
	case "installations":
		if event == "install" {
			return "installations"
		}
	}
	return ""
}

func appleAnalyticsList(call context.Context, ctx *sdk.AppCtx, scope GameScope, source map[string]any, tool string, input map[string]any) ([]map[string]any, error) {
	var out []map[string]any
	seen := map[string]bool{}
	input["limit"] = 200
	for range 50 {
		if err := call.Err(); err != nil {
			return nil, err
		}
		response, err := metricExecute(ctx, scope, source, tool, input)
		if err != nil {
			return nil, err
		}
		body := object(response)
		list, ok := body["data"].([]any)
		if !ok {
			return nil, fmt.Errorf("Apple returned an invalid %s list", tool)
		}
		for _, value := range list {
			item := object(value)
			id := txt(item["id"])
			if id == "" || object(item["attributes"]) == nil {
				return nil, fmt.Errorf("Apple returned an invalid %s item", tool)
			}
			if !seen[id] {
				out = append(out, item)
				seen[id] = true
			}
		}
		next := txt(object(body["links"])["next"])
		if next == "" {
			return out, nil
		}
		u, err := url.Parse(next)
		if err != nil || u.Query().Get("cursor") == "" || u.Query().Get("cursor") == input["cursor"] {
			return nil, fmt.Errorf("Apple returned an invalid %s pagination cursor", tool)
		}
		input["cursor"] = u.Query().Get("cursor")
	}
	return nil, fmt.Errorf("Apple %s pagination limit exceeded", tool)
}

func appleOngoingRequest(call context.Context, ctx *sdk.AppCtx, scope GameScope, source map[string]any) (string, error) {
	appID := txt(source["external_id"])
	active := func() (string, error) {
		requests, err := appleAnalyticsList(call, ctx, scope, source, "list_analytics_report_requests", map[string]any{"app_id": appID, "access_type": "ONGOING"})
		if err != nil {
			return "", err
		}
		for _, request := range requests {
			attrs := object(request["attributes"])
			if attrs["accessType"] == "ONGOING" && attrs["stoppedDueToInactivity"] == false {
				return txt(request["id"]), nil
			}
		}
		return "", nil
	}
	if id, err := active(); id != "" || err != nil {
		return id, err
	}
	response, err := metricExecute(ctx, scope, source, "create_analytics_report_request", map[string]any{"app_id": appID, "accessType": "ONGOING"})
	if err != nil {
		// A concurrent creator or a lost response can leave an active request.
		if id, _ := active(); id != "" {
			return id, nil
		}
		return "", fmt.Errorf("could not create Apple Analytics request (an Admin API key is required): %w", err)
	}
	request := object(object(response)["data"])
	if id := txt(request["id"]); id != "" {
		return id, nil
	}
	return "", errors.New("Apple returned an invalid analytics request")
}

const maxAppleSegment = 32 << 20
const maxAppleExpandedSegment = 128 << 20

// Apple returns temporary S3 URLs. Use an unauthenticated HTTPS request and
// refuse redirects so a URL cannot send the sidecar to an internal endpoint.
var appleReportHost = regexp.MustCompile(`^asp-[a-z0-9-]+\.s3(?:[.-][a-z0-9-]+)?\.amazonaws\.com$`)
var appleSegmentClient = &http.Client{
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func downloadAppleSegment(ctx context.Context, attrs map[string]any) ([]byte, error) {
	u, err := url.Parse(txt(attrs["url"]))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" ||
		!appleReportHost.MatchString(u.Hostname()) || !strings.HasPrefix(u.Path, "/reports/") {
		return nil, errors.New("Apple returned an unexpected report segment URL")
	}
	expected := number(attrs["sizeInBytes"])
	checksum := txt(attrs["checksum"])
	if expected <= 0 || expected > maxAppleSegment || len(checksum) != 32 {
		return nil, errors.New("Apple returned invalid report segment metadata")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("invalid Apple report segment request")
	}
	req.Header.Set("Accept-Encoding", "identity")
	res, err := appleSegmentClient.Do(req)
	if err != nil {
		return nil, errors.New("Apple report segment download failed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Apple report segment returned HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, expected+1))
	if err != nil || int64(len(data)) != expected || !strings.EqualFold(fmt.Sprintf("%x", md5.Sum(data)), checksum) {
		return nil, errors.New("Apple report segment failed integrity verification")
	}
	return data, nil
}

type appleDailyCounts map[string]map[string]int64

func parseAppleAnalyticsSegment(data []byte, report appleAnalyticsReport, appID, processingDate string, days appleDailyCounts) error {
	z, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return errors.New("invalid Apple analytics gzip")
	}
	defer z.Close()
	limited := &io.LimitedReader{R: z, N: maxAppleExpandedSegment + 1}
	reader := csv.NewReader(limited)
	reader.Comma = '\t'
	reader.ReuseRecord = true
	header, err := reader.Read()
	if err != nil {
		return errors.New("missing Apple analytics header")
	}
	columns := map[string]int{}
	for i, name := range header {
		name = strings.TrimPrefix(name, "\uFEFF")
		columns[strings.ToLower(strings.Join(strings.Fields(name), " "))] = i
	}
	required := []string{"date", "app apple identifier", "counts", report.dimension}
	if report.key == "discovery" {
		required = append(required, "page type")
	}
	for _, name := range required {
		if _, ok := columns[name]; !ok {
			return fmt.Errorf("Apple analytics report missing column %s", name)
		}
	}
	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || limited.N <= 0 {
			return errors.New("invalid or oversized Apple analytics segment")
		}
		if row[columns["app apple identifier"]] != appID {
			return errors.New("Apple analytics segment contains a different app ID")
		}
		day := row[columns["date"]]
		if !validAppleDate(day) || day > processingDate {
			return errors.New("invalid Apple analytics event date")
		}
		if days[day] == nil {
			days[day] = map[string]int64{}
		}
		page := ""
		if report.key == "discovery" {
			page = strings.ToLower(strings.TrimSpace(row[columns["page type"]]))
		}
		event := strings.ToLower(strings.Join(strings.Fields(row[columns[report.dimension]]), " "))
		key := report.metric(event, page)
		if key == "" {
			continue
		}
		count, err := strconv.ParseInt(row[columns["counts"]], 10, 64)
		if err != nil || count < 0 || count > math.MaxInt64-days[day][key] {
			return errors.New("invalid Apple analytics count")
		}
		days[day][key] += count
	}
	if limited.N <= 0 {
		return errors.New("Apple analytics segment exceeds expanded size limit")
	}
	return nil
}

func validAppleDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

func writeAppleAnalyticsInstance(ctx *sdk.AppCtx, scope GameScope, sourceID, token, instanceID, report, processingDate string, days appleDailyCounts) error {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE game_metric_syncs SET lease_until=? WHERE project_id=? AND game_id=? AND source_id=? AND lease_token=?`,
		time.Now().UTC().Add(30*time.Minute).Format(time.RFC3339), scope.ProjectID, scope.GameID, sourceID, token)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("Apple analytics sync lease lost")
	}
	for day, counts := range days {
		data, err := json.Marshal(counts)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO game_apple_report_days VALUES(?,?,?,?,?,?,?)
ON CONFLICT(project_id,game_id,source_id,report,event_date) DO UPDATE
SET processing_date=excluded.processing_date, counts=excluded.counts
WHERE excluded.processing_date > game_apple_report_days.processing_date`,
			scope.ProjectID, scope.GameID, sourceID, report, day, processingDate, string(data)); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO game_apple_report_instances VALUES(?,?,?,?,?)`,
		scope.ProjectID, scope.GameID, sourceID, instanceID, processingDate); err != nil {
		return err
	}
	return tx.Commit()
}

type appleSegmentDownload func(context.Context, map[string]any) ([]byte, error)

func syncAppleAnalytics(ctx *sdk.AppCtx, scope GameScope, source map[string]any, token string, download appleSegmentDownload) (any, error) {
	call, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	requestID, err := appleOngoingRequest(call, ctx, scope, source)
	if err != nil {
		return nil, err
	}
	sourceID := txt(source["id"])
	if _, err := ctx.AppDB().Exec(`INSERT INTO game_apple_report_requests
SELECT project_id,game_id,source_id,?,? FROM game_metric_syncs
WHERE project_id=? AND game_id=? AND source_id=? AND lease_token=?
ON CONFLICT(project_id,game_id,source_id) DO UPDATE SET request_id=excluded.request_id, observed_at=excluded.observed_at
WHERE game_apple_report_requests.request_id<>excluded.request_id`, requestID, nowRFC(), scope.ProjectID, scope.GameID, sourceID, token); err != nil {
		return nil, err
	}
	reports, err := appleAnalyticsList(call, ctx, scope, source, "list_analytics_reports", map[string]any{"request_id": requestID})
	if err != nil {
		return nil, err
	}
	imported := 0
	for _, report := range appleAnalyticsReports {
		for _, record := range reports {
			name := txt(object(record["attributes"])["name"])
			if name != report.name && name != report.name+" Standard" {
				continue
			}
			instances, err := appleAnalyticsList(call, ctx, scope, source, "list_analytics_report_instances",
				map[string]any{"report_id": record["id"], "granularity": "DAILY"})
			if err != nil {
				return nil, err
			}
			for _, instance := range instances {
				attrs := object(instance["attributes"])
				processingDate := txt(attrs["processingDate"])
				if !validAppleDate(processingDate) || attrs["granularity"] != "DAILY" {
					return nil, errors.New("Apple returned an invalid daily report instance")
				}
				id := txt(instance["id"])
				var seen int
				err := ctx.AppDB().QueryRow(`SELECT EXISTS(SELECT 1 FROM game_apple_report_instances WHERE project_id=? AND game_id=? AND source_id=? AND instance_id=?)`,
					scope.ProjectID, scope.GameID, sourceID, id).Scan(&seen)
				if err != nil {
					return nil, err
				}
				if seen != 0 {
					continue
				}
				segments, err := appleAnalyticsList(call, ctx, scope, source, "list_analytics_report_segments", map[string]any{"instance_id": id})
				if err != nil {
					return nil, err
				}
				if len(segments) == 0 {
					continue
				}
				days := appleDailyCounts{}
				for _, segment := range segments {
					data, err := download(call, object(segment["attributes"]))
					if err != nil {
						return nil, err
					}
					if err := parseAppleAnalyticsSegment(data, report, txt(source["external_id"]), processingDate, days); err != nil {
						return nil, err
					}
				}
				if err := writeAppleAnalyticsInstance(ctx, scope, sourceID, token, id, report.key, processingDate, days); err != nil {
					return nil, err
				}
				imported++
			}
			break
		}
	}
	_, err = ctx.AppDB().Exec(`DELETE FROM game_apple_report_instances WHERE project_id=? AND game_id=? AND source_id=? AND processing_date<?`,
		scope.ProjectID, scope.GameID, sourceID, time.Now().UTC().AddDate(0, 0, -45).Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	return map[string]any{"instances_imported": imported, "source_id": sourceID, "request_id": requestID}, nil
}

func appleAnalyticsStatus(ctx *sdk.AppCtx, scope GameScope, source map[string]any) (map[string]any, error) {
	out := map[string]any{}
	var observed string
	if err := ctx.AppDB().QueryRow(`SELECT COALESCE((SELECT observed_at FROM game_apple_report_requests
WHERE project_id=? AND game_id=? AND source_id=?),'')`, scope.ProjectID, scope.GameID, source["id"]).Scan(&observed); err != nil {
		return nil, err
	}
	requested, _ := time.Parse(time.RFC3339, observed)
	for _, report := range appleAnalyticsReports {
		var date string
		err := ctx.AppDB().QueryRow(`SELECT COALESCE(MAX(event_date),'') FROM game_apple_report_days
WHERE project_id=? AND game_id=? AND source_id=? AND report=? AND counts<>'{}'`,
			scope.ProjectID, scope.GameID, source["id"], report.key).Scan(&date)
		if err != nil {
			return nil, err
		}
		status := "waiting"
		switch {
		case txt(source["last_error"]) != "":
			status = "failed"
		case date != "":
			status = "reported"
		case !requested.IsZero() && time.Since(requested) >= 48*time.Hour:
			status = "unavailable"
		}
		out[report.key] = map[string]any{"latest_data_date": date, "status": status}
	}
	return out, nil
}

func queryAppleAnalytics(ctx *sdk.AppCtx, scope GameScope, args map[string]any) (any, error) {
	id := txt(args["source_id"])
	source, err := linkGet(ctx, scope, "metric", id)
	if err != nil || source["family"] != appleAnalyticsFamily {
		return nil, errors.New("choose an App Store Analytics source for this game")
	}
	since, until := "", "9999-12-31"
	if n := number(args["since"]); n > 0 {
		since = time.UnixMilli(n).UTC().Format(time.DateOnly)
	}
	if n := number(args["until"]); n > 0 {
		until = time.UnixMilli(n).UTC().Format(time.DateOnly)
	}
	rows, err := ctx.AppDB().Query(`SELECT event_date, counts FROM game_apple_report_days
WHERE project_id=? AND game_id=? AND source_id=? AND event_date IN (
 SELECT DISTINCT event_date FROM game_apple_report_days WHERE project_id=? AND game_id=? AND source_id=?
 AND event_date BETWEEN ? AND ? ORDER BY event_date DESC LIMIT ?
) ORDER BY event_date DESC`, scope.ProjectID, scope.GameID, id, scope.ProjectID, scope.GameID, id, since, until,
		boundedArg(args, "limit", 100, 1, 500))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type dayRow struct {
		Date   string           `json:"date"`
		Counts map[string]int64 `json:"counts"`
	}
	out := []dayRow{}
	for rows.Next() {
		var day, data string
		if err := rows.Scan(&day, &data); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Date != day {
			out = append(out, dayRow{day, map[string]int64{}})
		}
		if err := json.Unmarshal([]byte(data), &out[len(out)-1].Counts); err != nil {
			return nil, err
		}
	}
	return map[string]any{"days": out, "source_id": id}, rows.Err()
}
