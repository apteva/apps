package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type dashboardFilter struct {
	Hours       int
	SuiteID     int64
	Environment string
	Limit       int
	Group       bool
}
type dashboardOptions struct {
	Suites       []dashboardSuite `json:"suites"`
	Environments []string         `json:"environments"`
}
type dashboardSuite struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Archived bool   `json:"archived"`
}
type dashboardStats struct {
	Runs      int      `json:"runs"`
	Passed    int      `json:"passed"`
	Failed    int      `json:"failed"`
	Cancelled int      `json:"cancelled"`
	Running   int      `json:"running"`
	Queued    int      `json:"queued"`
	PassRate  *float64 `json:"pass_rate"`
}
type dashboardRun struct {
	ID          int64  `json:"id"`
	SuiteID     int64  `json:"suite_id"`
	SuiteName   string `json:"suite_name"`
	Environment string `json:"environment"`
	Status      string `json:"status"`
	Passed      int    `json:"passed"`
	Failed      int    `json:"failed"`
	CreatedAt   string `json:"created_at"`
	DurationMS  *int64 `json:"duration_ms"`
}
type dashboardDay struct {
	Date      string `json:"date"`
	Runs      int    `json:"runs"`
	Passed    int    `json:"passed"`
	Failed    int    `json:"failed"`
	Cancelled int    `json:"cancelled"`
}
type dashboardFailure struct {
	RunID         int64  `json:"run_id"`
	SuiteID       int64  `json:"suite_id"`
	SuiteName     string `json:"suite_name"`
	Environment   string `json:"environment"`
	CheckID       int64  `json:"check_id"`
	CheckName     string `json:"check_name"`
	StepID        string `json:"step_id,omitempty"`
	Message       string `json:"message"`
	CreatedAt     string `json:"created_at"`
	Occurrences   int    `json:"occurrences"`
	HasScreenshot bool   `json:"has_screenshot"`
}
type dashboardResponse struct {
	AsOf     string             `json:"as_of"`
	From     string             `json:"from"`
	Hours    int                `json:"hours"`
	Options  dashboardOptions   `json:"options"`
	Stats    *dashboardStats    `json:"stats,omitempty"`
	Recent   []dashboardRun     `json:"recent,omitempty"`
	Trend    []dashboardDay     `json:"trend,omitempty"`
	Failures []dashboardFailure `json:"failures"`
	Total    int                `json:"total"`
}

func parseDashboardFilter(r *http.Request) (dashboardFilter, error) {
	f := dashboardFilter{Hours: 24, Limit: 3, Group: true}
	q := r.URL.Query()
	if v := q.Get("hours"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || (n != 24 && n != 168) {
			return f, errors.New("hours must be 24 or 168")
		}
		f.Hours = n
	}
	if v := q.Get("suite_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return f, errors.New("invalid suite_id")
		}
		f.SuiteID = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 10 {
			return f, errors.New("limit must be 1–10")
		}
		f.Limit = n
	}
	f.Environment = q.Get("environment")
	if len(f.Environment) > 200 {
		return f, errors.New("invalid environment")
	}
	if v := q.Get("group"); v != "" {
		if v != "true" && v != "false" {
			return f, errors.New("group must be true or false")
		}
		f.Group = v == "true"
	}
	return f, nil
}
func (a *App) httpDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET required", 405)
		return
	}
	if r.URL.Path != "/dashboard/overview" && r.URL.Path != "/dashboard/failures" {
		http.NotFound(w, r)
		return
	}
	if a.ctx == nil {
		http.Error(w, "app is not mounted", 503)
		return
	}
	call := r.Context()
	if project := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID")); project != "" {
		call = sdk.WithCaller(call, &sdk.Caller{ProjectID: project})
	}
	app, err := scoped(call, a.ctx, r.URL.Query().Get("project_id"))
	if err != nil {
		http.Error(w, "project access denied", 403)
		return
	}
	f, err := parseDashboardFilter(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	out, err := (store{app.AppReadDB(), app.CurrentProject()}).dashboard(call, f, r.URL.Path == "/dashboard/failures", time.Now().UTC())
	if err != nil {
		app.Logger().Warn("dashboard summary failed", "error", err)
		http.Error(w, "could not load test summary", 500)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}
func dashboardWhere(project string, f dashboardFilter, at time.Time, window bool) (string, []any) {
	where := "r.project_id=?"
	args := []any{project}
	if f.SuiteID > 0 {
		where += " AND r.suite_id=?"
		args = append(args, f.SuiteID)
	}
	if f.Environment != "" {
		where += " AND r.environment=?"
		args = append(args, f.Environment)
	}
	if window {
		from := at.Add(-time.Duration(f.Hours) * time.Hour)
		// The coarse bound uses the history index; julianday handles exact-second
		// timestamps and subsecond cutoffs consistently across old and new rows.
		where += " AND r.created_at>=? AND julianday(r.created_at)>=julianday(?) AND julianday(r.created_at)<=julianday(?)"
		args = append(args, from.Format("2006-01-02T15:04:05"), from.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano))
	}
	return where, args
}
func (s store) dashboard(ctx context.Context, f dashboardFilter, failures bool, at time.Time) (out dashboardResponse, err error) {
	out = dashboardResponse{AsOf: at.Format(time.RFC3339Nano), From: at.Add(-time.Duration(f.Hours) * time.Hour).Format(time.RFC3339Nano), Hours: f.Hours, Failures: []dashboardFailure{}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if out.Options, err = dashboardChoices(ctx, tx, s.project, f.SuiteID); err != nil {
		return out, err
	}
	where, args := dashboardWhere(s.project, f, at, true)
	if failures {
		out.Failures, out.Total, err = dashboardFailures(ctx, tx, where, args, f)
		if err != nil {
			return out, err
		}
		return out, tx.Commit()
	}
	stats := &dashboardStats{}
	err = tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(status='passed'),0),coalesce(sum(status='failed' AND cancel_requested=0),0),coalesce(sum(status='failed' AND cancel_requested=1),0) FROM test_runs r WHERE `+where, args...).Scan(&stats.Runs, &stats.Passed, &stats.Failed, &stats.Cancelled)
	if err != nil {
		return out, err
	}
	active, activeArgs := dashboardWhere(s.project, f, at, false)
	err = tx.QueryRowContext(ctx, `SELECT coalesce(sum(status='running'),0),coalesce(sum(status='queued'),0) FROM test_runs r WHERE `+active, activeArgs...).Scan(&stats.Running, &stats.Queued)
	if err != nil {
		return out, err
	}
	if n := stats.Passed + stats.Failed; n > 0 {
		p := 100 * float64(stats.Passed) / float64(n)
		stats.PassRate = &p
	}
	out.Stats = stats
	rows, err := tx.QueryContext(ctx, `SELECT id,suite_id,suite_name,environment,status,passed,failed,created_at,started_at,finished_at,cancel_requested FROM test_runs r WHERE `+where+` ORDER BY r.created_at DESC,r.id DESC LIMIT ?`, append(args, f.Limit)...)
	if err != nil {
		return out, err
	}
	out.Recent = []dashboardRun{}
	for rows.Next() {
		var v dashboardRun
		var started, finished string
		var cancelled bool
		if err = rows.Scan(&v.ID, &v.SuiteID, &v.SuiteName, &v.Environment, &v.Status, &v.Passed, &v.Failed, &v.CreatedAt, &started, &finished, &cancelled); err != nil {
			rows.Close()
			return out, err
		}
		if cancelled {
			if v.Status == "failed" {
				v.Status = "cancelled"
			} else {
				v.Status = "cancelling"
			}
		}
		if start, e := time.Parse(time.RFC3339Nano, started); e == nil {
			end := at
			if finished != "" {
				end, e = time.Parse(time.RFC3339Nano, finished)
			}
			if e == nil && !end.Before(start) {
				ms := end.Sub(start).Milliseconds()
				v.DurationMS = &ms
			}
		}
		out.Recent = append(out.Recent, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT substr(created_at,1,10),count(*),sum(status='passed'),sum(status='failed' AND cancel_requested=0),sum(status='failed' AND cancel_requested=1) FROM test_runs r WHERE `+where+` GROUP BY substr(created_at,1,10)`, args...)
	if err != nil {
		return out, err
	}
	days := map[string]dashboardDay{}
	for rows.Next() {
		var d dashboardDay
		if err = rows.Scan(&d.Date, &d.Runs, &d.Passed, &d.Failed, &d.Cancelled); err != nil {
			rows.Close()
			return out, err
		}
		days[d.Date] = d
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	start := at.Add(-time.Duration(f.Hours) * time.Hour)
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	for d := start; !d.After(at); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		v := days[key]
		v.Date = key
		out.Trend = append(out.Trend, v)
	}
	return out, tx.Commit()
}
func dashboardChoices(ctx context.Context, tx *sql.Tx, project string, selected int64) (out dashboardOptions, err error) {
	out = dashboardOptions{Suites: []dashboardSuite{}, Environments: []string{}}
	rows, err := tx.QueryContext(ctx, `SELECT id,name,archived FROM test_suites WHERE project_id=? ORDER BY id DESC LIMIT 500`, project)
	if err != nil {
		return out, err
	}
	found := false
	for rows.Next() {
		var v dashboardSuite
		if err = rows.Scan(&v.ID, &v.Name, &v.Archived); err != nil {
			rows.Close()
			return out, err
		}
		found = found || v.ID == selected
		out.Suites = append(out.Suites, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if selected > 0 && !found {
		var v dashboardSuite
		if e := tx.QueryRowContext(ctx, `SELECT id,name,archived FROM test_suites WHERE project_id=? AND id=?`, project, selected).Scan(&v.ID, &v.Name, &v.Archived); e == nil {
			out.Suites = append(out.Suites, v)
		} else if !errors.Is(e, sql.ErrNoRows) {
			return out, e
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT DISTINCT environment FROM test_runs WHERE project_id=? ORDER BY environment`, project)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v string
		if err = rows.Scan(&v); err != nil {
			rows.Close()
			return out, err
		}
		out.Environments = append(out.Environments, v)
	}
	err = rows.Err()
	rows.Close()
	return out, err
}
func dashboardFailures(ctx context.Context, tx *sql.Tx, where string, args []any, f dashboardFilter) (out []dashboardFailure, total int, err error) {
	// Grouping and totals cover the entire period. Only the selected rows' safe
	// failure fields are decoded; definitions, credentials and output bodies stay private.
	query := `WITH failures AS (
 SELECT r.id run_id,r.suite_id,r.suite_name,r.environment,r.created_at,t.check_id,t.name check_name,t.error,
 CASE WHEN t.kind IN ('browser','scenario') THEN coalesce(json_extract(t.output,'$.steps'),'[]') ELSE '[]' END steps
 FROM test_runs r JOIN test_results t ON t.project_id=r.project_id AND t.run_id=r.id
 WHERE ` + where + ` AND r.status='failed' AND r.cancel_requested=0 AND t.status IN ('failed','error')
 UNION ALL
 SELECT r.id,r.suite_id,r.suite_name,r.environment,r.created_at,0,'Suite execution',r.error,'[]'
 FROM test_runs r WHERE ` + where + ` AND r.status='failed' AND r.cancel_requested=0
 AND NOT EXISTS(SELECT 1 FROM test_results t WHERE t.project_id=r.project_id AND t.run_id=r.id AND t.status IN ('failed','error'))
 ), ranked AS (
 SELECT *,row_number() OVER (PARTITION BY suite_id,environment,check_id,CASE WHEN ? THEN 0 ELSE run_id END ORDER BY created_at DESC,run_id DESC) rank,
 count(*) OVER (PARTITION BY suite_id,environment,check_id,CASE WHEN ? THEN 0 ELSE run_id END) occurrences FROM failures
 ) SELECT run_id,suite_id,suite_name,environment,created_at,check_id,check_name,error,steps,occurrences,count(*) OVER() FROM ranked WHERE rank=1 ORDER BY created_at DESC,run_id DESC,check_id LIMIT ?`
	values := append(append(append([]any{}, args...), args...), f.Group, f.Group, f.Limit)
	rows, err := tx.QueryContext(ctx, query, values...)
	if err != nil {
		return nil, 0, err
	}
	out = []dashboardFailure{}
	for rows.Next() {
		var v dashboardFailure
		var steps string
		if err = rows.Scan(&v.RunID, &v.SuiteID, &v.SuiteName, &v.Environment, &v.CreatedAt, &v.CheckID, &v.CheckName, &v.Message, &steps, &v.Occurrences, &total); err != nil {
			rows.Close()
			return nil, 0, err
		}
		var list []dashboardFailureStep
		if err = json.Unmarshal([]byte(steps), &list); err != nil {
			rows.Close()
			return nil, 0, err
		}
		if step := firstDashboardFailure(list); step != nil {
			v.StepID = step.ID
			if step.Error != "" {
				v.Message = step.Error
			} else {
				for _, a := range step.Assertions {
					if !a.Passed {
						v.Message = a.Path + " " + a.Op + " assertion failed"
						break
					}
				}
			}
		}
		v.Message = strings.Join(strings.Fields(v.Message), " ")
		if v.Message == "" {
			v.Message = "Run failed; open its evidence for details"
		}
		runes := []rune(v.Message)
		if len(runes) > 300 {
			v.Message = string(runes[:300]) + "…"
		}
		out = append(out, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	for i := range out {
		v := &out[i]
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM test_artifacts WHERE project_id=? AND run_id=? AND check_id=?)`, args[0], v.RunID, v.CheckID).Scan(&v.HasScreenshot); err != nil {
			return nil, 0, err
		}
	}
	return out, total, nil
}

type dashboardFailureStep struct {
	ID         string                 `json:"id"`
	Status     string                 `json:"status"`
	Error      string                 `json:"error"`
	Parallel   []dashboardFailureStep `json:"parallel"`
	Assertions []struct {
		Path   string `json:"path"`
		Op     string `json:"op"`
		Passed bool   `json:"passed"`
	} `json:"assertions"`
}

func firstDashboardFailure(steps []dashboardFailureStep) *dashboardFailureStep {
	for i := range steps {
		s := &steps[i]
		if s.Status != "passed" {
			if child := firstDashboardFailure(s.Parallel); child != nil {
				return child
			}
			return s
		}
	}
	return nil
}
