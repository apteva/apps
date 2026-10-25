package main

import (
	"context"
	"time"
)

type audioDashboardBucket struct {
	SIPCalls         int64    `json:"sip_calls"`
	BrowserCalls     int64    `json:"browser_calls"`
	From             string   `json:"from"`
	Until            string   `json:"until"`
	Calls            int64    `json:"calls"`
	Observed         int64    `json:"observed"`
	Affected         int64    `json:"affected"`
	SIPDiscardMS     float64  `json:"sip_discard_ms"`
	BrowserDiscardMS float64  `json:"browser_discard_ms"`
	ProblemPercent   *float64 `json:"problem_percent"`
}

// Use the same latest-observation cohort and filters as the cards/list, across
// all matching rows (not just the current page). No raw diagnostics are loaded.
func (c *callsDB) audioDashboardCharts(ctx context.Context, project string, f audioDashboardFilter, now time.Time) ([]audioDashboardBucket, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	span := f.Until - f.From
	step := int64(86400000)
	for _, s := range []int64{60000, 300000, 900000, 3600000, 21600000, 86400000} {
		if (span+s-1)/s <= 60 {
			step = s
			break
		}
	}
	count := int((span + step - 1) / step)
	buckets := make([]audioDashboardBucket, count)
	for i := range buckets {
		start := f.From + int64(i)*step
		buckets[i].From = time.UnixMilli(start).UTC().Format(time.RFC3339Nano)
		buckets[i].Until = time.UnixMilli(min(f.Until, start+step)).UTC().Format(time.RFC3339Nano)
	}
	where, args := f.where(project, now)
	params := append([]any{count - 1, f.From, step}, args...)
	rows, err := c.db.QueryContext(ctx, `SELECT min(?,CAST((a.observed_ms-?)/? AS INTEGER)),count(*),
 COALESCE(sum(CASE WHEN json_extract(a.summary_json,'$.telemetry')=1 THEN 1 ELSE 0 END),0),
 COALESCE(sum(CASE WHEN json_extract(a.summary_json,'$.telemetry')=1 AND a.has_issues=1 THEN 1 ELSE 0 END),0),
 COALESCE(sum(max(0,COALESCE(json_extract(a.summary_json,'$.metrics.sip_jitter_discarded_ms'),0))),0),
 COALESCE(sum(max(0,COALESCE(json_extract(a.summary_json,'$.metrics.playback_dropped_ms'),0))),0),
 COALESCE(sum(CASE WHEN json_type(a.summary_json,'$.metrics.sip_packets_received') IS NOT NULL THEN 1 ELSE 0 END),0),
 COALESCE(sum(CASE WHEN `+statisticsBrowserObserved+` THEN 1 ELSE 0 END),0)`+audioDashboardJoin+`WHERE `+where+` GROUP BY 1`, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var i int
		var b audioDashboardBucket
		if err := rows.Scan(&i, &b.Calls, &b.Observed, &b.Affected, &b.SIPDiscardMS, &b.BrowserDiscardMS, &b.SIPCalls, &b.BrowserCalls); err != nil {
			return nil, err
		}
		if i < 0 || i >= len(buckets) {
			continue
		}
		b.From, b.Until = buckets[i].From, buckets[i].Until
		b.ProblemPercent = statisticsRatio(float64(b.Affected), float64(b.Observed))
		buckets[i] = b
	}
	return buckets, rows.Err()
}
