package main

import (
	"database/sql"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Follower counts are balances, not daily activity. Compare observations near
// the two selected boundaries; never sum daily follower values or interpolate
// an absent observation. Daily series can mean new followers on some providers.
const followerBoundaryMaxAge = 24 * time.Hour

type widgetFollowerGrowth struct {
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	StartDate          string `json:"start_date"`
	EndDate            string `json:"end_date"`
	Baseline           *int64 `json:"baseline_value"`
	EndValue           *int64 `json:"end_value"`
	BaselineObservedAt string `json:"baseline_observed_at,omitempty"`
	EndObservedAt      string `json:"end_observed_at,omitempty"`
	NetChange          *int64 `json:"net_change"`
	Accounts           int    `json:"accounts"`
}

type followerObservation struct {
	Value int64
	At    time.Time
}

func newFollowerGrowth(window performanceWindow) *widgetFollowerGrowth {
	return &widgetFollowerGrowth{Status: "unavailable", Reason: "missing_history", StartDate: window.Start.Format("2006-01-02"), EndDate: window.End.AddDate(0, 0, -1).Format("2006-01-02")}
}

func loadWidgetFollowers(ctx *sdk.AppCtx, project string, account widgetAccount, scope string, window performanceWindow, now time.Time) (widgetMetric, error) {
	m := widgetMetric{Trend: []widgetMetricPoint{}, Growth: newFollowerGrowth(window)}
	// Each lookup returns at most one row. Scope, platform and dimension guards
	// exclude post, audience-segment, board-parent and foreign-project evidence.
	read := func(boundary time.Time) (*followerObservation, error) {
		var value int64
		var stamp string
		err := ctx.AppDB().QueryRow(`SELECT value,strftime('%Y-%m-%dT%H:%M:%fZ',point_time) FROM social_metric_points
			WHERE project_id=? AND social_account_id=? AND platform=? AND scope=?
			AND COALESCE(post_id,0)=0 AND COALESCE(post_target_id,0)=0 AND dimensions_key='' AND dimensions_json='{}' AND status='ok'
			AND metric='followers' AND period='snapshot' AND value>=0
			AND julianday(point_time)<=julianday(?)
			ORDER BY julianday(point_time) DESC,id DESC LIMIT 1`, project, account.ID, account.Platform, scope, boundary.UTC().Format(time.RFC3339Nano)).Scan(&value, &stamp)
		if err == sql.ErrNoRows {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		at, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return nil, err
		}
		return &followerObservation{value, at}, nil
	}
	current, err := read(now)
	if err != nil {
		return m, err
	}
	if current == nil {
		m.Growth.Reason = "missing_current_count"
		return m, nil
	}
	m.Value, m.Accounts, m.ObservedAt = &current.Value, 1, current.At.Format(time.RFC3339Nano)
	m.Trend, m.Days, err = loadFollowerTrend(ctx, project, account, scope, window)
	if err != nil {
		return m, err
	}
	baseline, err := read(window.Start)
	if err != nil {
		return m, err
	}
	end, err := read(window.End)
	if err != nil {
		return m, err
	}
	g := m.Growth
	if baseline != nil {
		g.Baseline, g.BaselineObservedAt = &baseline.Value, baseline.At.Format(time.RFC3339Nano)
	}
	if end != nil {
		g.EndValue, g.EndObservedAt = &end.Value, end.At.Format(time.RFC3339Nano)
	}
	switch {
	case baseline == nil:
		g.Reason = "missing_baseline"
	case end == nil:
		g.Reason = "missing_end_count"
	case window.Start.Sub(baseline.At) > followerBoundaryMaxAge:
		g.Reason = "stale_baseline"
	case window.End.Sub(end.At) > followerBoundaryMaxAge:
		g.Reason = "stale_end_count"
	case !end.At.After(baseline.At):
		g.Reason = "no_distinct_observations"
	default:
		delta := end.Value - baseline.Value
		g.NetChange, g.Accounts = &delta, 1
		if baseline.Value == 0 {
			g.Reason = "zero_baseline"
		} else {
			change := float64(delta) / float64(baseline.Value) * 100
			m.Change, g.Status, g.Reason = &change, "available", ""
		}
	}
	return m, nil
}

func summarizeWidgetFollowers(accounts []widgetAccount, window performanceWindow) widgetMetric {
	m := widgetMetric{Trend: []widgetMetricPoint{}, Growth: newFollowerGrowth(window)}
	var current, baseline, end int64
	for _, account := range accounts {
		f := account.Metrics["followers"]
		if f.Value == nil {
			continue
		}
		current += *f.Value
		m.Accounts++
		if g := f.Growth; g != nil && g.NetChange != nil && g.Baseline != nil && g.EndValue != nil && g.StartDate == m.Growth.StartDate && g.EndDate == m.Growth.EndDate {
			baseline += *g.Baseline
			end += *g.EndValue
			m.Growth.Accounts++
		}
	}
	if m.Accounts == 0 {
		m.Growth.Reason = "missing_current_count"
		return m
	}
	m.Value = &current
	// A fixed cohort and explicit gaps prevent a missing account from looking
	// like a fall in the combined follower balance.
	totals := map[string]int64{}
	counts := map[string]int{}
	for _, account := range accounts {
		f := account.Metrics["followers"]
		if f.Value == nil {
			continue
		}
		for _, point := range f.Trend {
			if point.Value != nil {
				totals[point.Date] += *point.Value
				counts[point.Date]++
			}
		}
	}
	for day := window.Start; day.Before(window.End); day = day.AddDate(0, 0, 1) {
		stamp := day.Format("2006-01-02")
		p := widgetMetricPoint{Date: stamp, Accounts: counts[stamp]}
		if p.Accounts == m.Accounts {
			value := totals[stamp]
			p.Value = &value
			m.Days++
		}
		m.Trend = append(m.Trend, p)
	}
	if m.Growth.Accounts != m.Accounts {
		m.Growth.Reason = "incomplete_account_coverage"
		return m
	}
	delta := end - baseline
	m.Growth.Baseline, m.Growth.EndValue, m.Growth.NetChange = &baseline, &end, &delta
	if baseline == 0 {
		m.Growth.Reason = "zero_baseline"
		return m
	}
	change := float64(delta) / float64(baseline) * 100
	m.Change, m.Growth.Status, m.Growth.Reason = &change, "available", ""
	return m
}

func loadFollowerTrend(ctx *sdk.AppCtx, project string, account widgetAccount, scope string, window performanceWindow) ([]widgetMetricPoint, int, error) {
	// Midnight is the closing observation of the preceding UTC day. Select the
	// newest snapshot in (day start, next midnight]; never carry it into a
	// later day without an observation, and never treat daily gains as balances.
	rows, err := ctx.AppDB().Query(`WITH observations AS (
	 SELECT id,value,point_time,
	 CASE WHEN strftime('%H:%M:%f',point_time)='00:00:00.000' THEN date(point_time,'-1 day') ELSE date(point_time) END AS day
	 FROM social_metric_points WHERE project_id=? AND social_account_id=? AND platform=? AND scope=?
	 AND COALESCE(post_id,0)=0 AND COALESCE(post_target_id,0)=0 AND dimensions_key='' AND dimensions_json='{}'
	 AND status='ok' AND metric='followers' AND period='snapshot' AND value>=0
	 AND julianday(point_time)>julianday(?) AND julianday(point_time)<=julianday(?)
	), ranked AS (SELECT *,row_number() OVER (PARTITION BY day ORDER BY julianday(point_time) DESC,id DESC) AS rank FROM observations)
	 SELECT day,value,strftime('%Y-%m-%dT%H:%M:%fZ',point_time) FROM ranked WHERE rank=1 ORDER BY day`, project, account.ID, account.Platform, scope, window.Start.Format(time.RFC3339), window.End.Format(time.RFC3339))
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	known := map[string]widgetMetricPoint{}
	for rows.Next() {
		var p widgetMetricPoint
		var value int64
		if err := rows.Scan(&p.Date, &value, &p.ObservedAt); err != nil {
			return nil, 0, err
		}
		p.Value = &value
		p.Accounts = 1
		known[p.Date] = p
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	points := []widgetMetricPoint{}
	count := 0
	for day := window.Start; day.Before(window.End); day = day.AddDate(0, 0, 1) {
		stamp := day.Format("2006-01-02")
		p, ok := known[stamp]
		if ok {
			count++
		} else {
			p.Date = stamp
		}
		points = append(points, p)
	}
	return points, count, nil
}
