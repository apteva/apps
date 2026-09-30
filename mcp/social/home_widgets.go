package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Home widgets only read local Social data. Opening Home never refreshes a
// provider or mutates a post. The gateway and SDK authenticate both routes.
type homeWidgetScope struct {
	projectID  string
	profileID  int64
	accountIDs []int64
}

func widgetScope(ctx *sdk.AppCtx, r *http.Request) (homeWidgetScope, error) {
	s := homeWidgetScope{projectID: projectScope(ctx, projectArgsFromRequest(r))}
	if s.projectID == "" {
		return s, fmt.Errorf("project_id required")
	}
	if raw := r.URL.Query().Get("profile_id"); raw != "" && raw != "0" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return s, fmt.Errorf("invalid profile_id")
		}
		if resolveProfileArg(ctx, s.projectID, map[string]any{"profile_id": id}) != id {
			return s, fmt.Errorf("profile not found in this project")
		}
		s.profileID = id
	}
	rawIDs := strings.TrimSpace(r.URL.Query().Get("account_ids"))
	if rawIDs != "" {
		parts := strings.Split(rawIDs, ",")
		if len(parts) > 100 {
			return s, fmt.Errorf("at most 100 account IDs allowed")
		}
		for _, raw := range parts {
			id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			if err != nil || id <= 0 {
				return s, fmt.Errorf("invalid account_ids")
			}
			var n int
			q := `SELECT COUNT(*) FROM social_accounts WHERE project_id=? AND id=?`
			args := []any{s.projectID, id}
			if s.profileID > 0 {
				q += ` AND profile_id=?`
				args = append(args, s.profileID)
			}
			if err := ctx.AppDB().QueryRow(q, args...).Scan(&n); err != nil {
				return s, err
			}
			if n != 1 {
				return s, fmt.Errorf("account not found in this scope")
			}
			s.accountIDs = append(s.accountIDs, id)
		}
		s.accountIDs = uniquePositiveInt64s(s.accountIDs)
	}
	return s, nil
}

func (s homeWidgetScope) postWhere() (string, []any) {
	q, args := `p.project_id=?`, []any{s.projectID}
	if s.profileID > 0 {
		q += ` AND p.profile_id=?`
		args = append(args, s.profileID)
	}
	if len(s.accountIDs) > 0 {
		q += ` AND EXISTS (SELECT 1 FROM post_targets t WHERE t.post_id=p.id AND t.social_account_id IN (` + placeholders(len(s.accountIDs)) + `))`
		for _, id := range s.accountIDs {
			args = append(args, id)
		}
	}
	return q, args
}

func (a *App) handlePublishingWidget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", 405)
		return
	}
	scope, err := widgetScope(globalCtx, r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	from, err1 := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
	to, err2 := time.Parse(time.RFC3339, r.URL.Query().Get("to"))
	if err1 != nil || err2 != nil || !from.Before(to) || to.Sub(from) > 43*24*time.Hour {
		http.Error(w, "from and to must define a window of at most 43 days", 400)
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode != "upcoming" && mode != "calendar" {
		http.Error(w, "mode must be upcoming or calendar", 400)
		return
	}
	limit := 1000
	if mode == "upcoming" {
		limit = 5
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 || limit > 20 {
				http.Error(w, "limit must be between 1 and 20", 400)
				return
			}
		}
	}
	where, args := scope.postWhere()
	var failed, approval, overdue int
	err = globalCtx.AppDB().QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN p.status IN ('failed','partial') THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN p.approval_status='pending' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN p.status='scheduled' AND datetime(p.schedule_at)<datetime(?) THEN 1 ELSE 0 END),0)
		FROM posts p WHERE `+where, append([]any{time.Now().UTC().Format(time.RFC3339)}, args...)...).Scan(&failed, &approval, &overdue)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	effective := `CASE WHEN p.status IN ('published','partial') AND COALESCE(p.published_at,'')!='' THEN p.published_at WHEN COALESCE(p.schedule_at,'')!='' THEN p.schedule_at ELSE p.created_at END`
	if mode == "upcoming" {
		where += ` AND p.status='scheduled'`
		effective = `p.schedule_at`
	}
	where += ` AND datetime(` + effective + `)>=datetime(?) AND datetime(` + effective + `)<datetime(?)`
	args = append(args, from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
	var total int
	if err := globalCtx.AppDB().QueryRow(`SELECT COUNT(*) FROM posts p WHERE `+where, args...).Scan(&total); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	rows, err := globalCtx.AppDB().Query(`SELECT p.id FROM posts p WHERE `+where+` ORDER BY datetime(`+effective+`) ASC,p.id ASC LIMIT ?`, append(args, limit)...)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			http.Error(w, err.Error(), 500)
			return
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	posts := []map[string]any{}
	for _, id := range ids {
		post, err := a.loadPostByID(globalCtx, scope.projectID, id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		posts = append(posts, post)
	}
	writeJSON(w, map[string]any{"posts": posts, "total": total, "attention": map[string]int{"failed": failed, "approval": approval, "overdue": overdue}})
}

type widgetMetric struct {
	Value    *int64              `json:"value"`
	Accounts int                 `json:"accounts"`
	Days     int                 `json:"days,omitempty"`
	Change   *float64            `json:"change_percent,omitempty"`
	Trend    []widgetMetricPoint `json:"trend"`
}
type widgetMetricPoint struct {
	Date     string `json:"date"`
	Value    *int64 `json:"value"`
	Accounts int    `json:"accounts"`
}
type widgetAccount struct {
	ID        int64                   `json:"id"`
	Name      string                  `json:"name"`
	Platform  string                  `json:"platform"`
	UpdatedAt string                  `json:"updated_at"`
	Metrics   map[string]widgetMetric `json:"metrics"`
	daily     map[string]map[string]int64
}

var widgetMetricNames = []string{"followers", "views", "impressions", "interactions"}

func (a *App) handlePerformanceWidget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", 405)
		return
	}
	scope, err := widgetScope(globalCtx, r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	days := 28
	if raw := r.URL.Query().Get("days"); raw != "" {
		days, err = strconv.Atoi(raw)
	}
	if err != nil || (days != 7 && days != 28 && days != 90) {
		http.Error(w, "days must be 7, 28, or 90", 400)
		return
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	start, previous := today.AddDate(0, 0, -days), today.AddDate(0, 0, -2*days)
	q, args := `SELECT id,display_name,platform FROM social_accounts WHERE project_id=? AND status='active'`, []any{scope.projectID}
	if scope.profileID > 0 {
		q += ` AND profile_id=?`
		args = append(args, scope.profileID)
	}
	if len(scope.accountIDs) > 0 {
		q += ` AND id IN (` + placeholders(len(scope.accountIDs)) + `)`
		for _, id := range scope.accountIDs {
			args = append(args, id)
		}
	}
	rows, err := globalCtx.AppDB().Query(q+` ORDER BY id`, args...)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	accounts := []widgetAccount{}
	for rows.Next() {
		var acct widgetAccount
		if err := rows.Scan(&acct.ID, &acct.Name, &acct.Platform); err != nil {
			rows.Close()
			http.Error(w, err.Error(), 500)
			return
		}
		accounts = append(accounts, acct)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	for i := range accounts {
		acct := &accounts[i]
		acct.daily = map[string]map[string]int64{}
		acct.Metrics = map[string]widgetMetric{}
		// Read daily observations separately from range snapshots. A YouTube
		// lifetime view count must never become views for the selected period.
		points, err := globalCtx.AppDB().Query(`SELECT metric,period,point_time,value FROM social_metric_points
			WHERE project_id=? AND social_account_id=? AND scope='account' AND status='ok' AND dimensions_key=''
			AND ((period='day' AND metric IN ('views','impressions','page_impressions','likes','comments','shares') AND date(point_time)>=date(?) AND date(point_time)<date(?))
			OR id=(SELECT id FROM social_metric_points WHERE project_id=? AND social_account_id=? AND scope='account' AND status='ok' AND dimensions_key='' AND period='snapshot' AND metric='followers' ORDER BY datetime(point_time) DESC,id DESC LIMIT 1)
			OR id=(SELECT id FROM social_metric_points WHERE project_id=? AND social_account_id=? AND scope='account' AND status='ok' AND dimensions_key='' AND metric='_refresh' ORDER BY datetime(point_time) DESC,id DESC LIMIT 1))
			ORDER BY datetime(created_at) DESC,datetime(point_time) DESC,id DESC`, scope.projectID, acct.ID, previous.Format("2006-01-02"), today.Format("2006-01-02"), scope.projectID, acct.ID, scope.projectID, acct.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		for points.Next() {
			var metric, period, stamp string
			var value int64
			if err := points.Scan(&metric, &period, &stamp, &value); err != nil {
				points.Close()
				http.Error(w, err.Error(), 500)
				return
			}
			if metric == "_refresh" {
				if acct.UpdatedAt == "" {
					acct.UpdatedAt = stamp
				}
				continue
			}
			if metric == "followers" && period == "snapshot" {
				if _, ok := acct.Metrics["followers"]; !ok {
					v := value
					acct.Metrics["followers"] = widgetMetric{Value: &v, Accounts: 1, Trend: []widgetMetricPoint{}}
				}
				continue
			}
			name := widgetDailyMetric(metric)
			if name == "" || value < 0 || len(stamp) < 10 {
				continue
			}
			date := stamp[:10]
			if acct.daily[name] == nil {
				acct.daily[name] = map[string]int64{}
			}
			// Providers may overlap windows or change source identifiers. Take
			// the newest observation for a metric/day, never sum duplicates.
			if _, ok := acct.daily[name][date]; !ok {
				acct.daily[name][date] = value
			}
		}
		err = points.Err()
		points.Close()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		acct.daily["interactions"] = map[string]int64{}
		for day, likes := range acct.daily["likes"] {
			comments, c := acct.daily["comments"][day]
			shares, s := acct.daily["shares"][day]
			if c && s {
				acct.daily["interactions"][day] = likes + comments + shares
			}
		}
		for _, name := range widgetMetricNames[1:] {
			acct.Metrics[name] = summarizeWidgetMetric([]widgetAccount{*acct}, name, start, today, previous)
		}
	}
	metrics := map[string]widgetMetric{}
	var updated string
	for _, name := range widgetMetricNames {
		if name == "followers" {
			m := widgetMetric{Trend: []widgetMetricPoint{}}
			var total int64
			for _, acct := range accounts {
				if v := acct.Metrics[name].Value; v != nil {
					total += *v
					m.Accounts++
				}
			}
			if m.Accounts > 0 {
				m.Value = &total
			}
			metrics[name] = m
		} else {
			metrics[name] = summarizeWidgetMetric(accounts, name, start, today, previous)
		}
	}
	for _, acct := range accounts {
		if acct.UpdatedAt != "" && (updated == "" || acct.UpdatedAt < updated) {
			updated = acct.UpdatedAt
		}
	}
	writeJSON(w, map[string]any{"accounts": accounts, "metrics": metrics, "days": days, "start_date": start.Format("2006-01-02"), "end_date": today.AddDate(0, 0, -1).Format("2006-01-02"), "updated_at": updated})
}

func widgetDailyMetric(name string) string {
	switch name {
	case "views", "likes", "comments", "shares":
		return name
	case "impressions", "page_impressions":
		return "impressions"
	default:
		return ""
	}
}

func summarizeWidgetMetric(accounts []widgetAccount, name string, start, end, previous time.Time) widgetMetric {
	m := widgetMetric{Trend: []widgetMetricPoint{}}
	contributors := []widgetAccount{}
	var total int64
	for _, acct := range accounts {
		present := false
		for day, value := range acct.daily[name] {
			if day >= start.Format("2006-01-02") && day < end.Format("2006-01-02") {
				total += value
				present = true
			}
		}
		if present {
			contributors = append(contributors, acct)
		}
	}
	m.Accounts = len(contributors)
	if m.Accounts == 0 {
		return m
	}
	m.Value = &total
	for date := start; date.Before(end); date = date.AddDate(0, 0, 1) {
		day := date.Format("2006-01-02")
		var value int64
		count := 0
		for _, acct := range contributors {
			if n, ok := acct.daily[name][day]; ok {
				value += n
				count++
			}
		}
		point := widgetMetricPoint{Date: day, Accounts: count}
		// A chart gap is safer than a false dip from missing observations.
		if count == len(contributors) {
			point.Value = &value
			m.Days++
		}
		m.Trend = append(m.Trend, point)
	}
	complete := m.Days == len(m.Trend)
	var before int64
	for date := previous; date.Before(start); date = date.AddDate(0, 0, 1) {
		for _, acct := range contributors {
			value, ok := acct.daily[name][date.Format("2006-01-02")]
			if !ok {
				complete = false
			}
			before += value
		}
	}
	if complete && before > 0 {
		change := float64(total-before) / float64(before) * 100
		m.Change = &change
	}
	return m
}
