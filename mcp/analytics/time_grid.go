package main

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const maxSeriesPoints = 1000

func seriesGrid(db sqlRunner, f Filter, interval string) (int64, int64, int64, error) {
	step := int64(60000)
	switch interval {
	case "minute", "":
	case "hour":
		step = 3600000
	case "day":
		step = 86400000
	default:
		return 0, 0, 0, fmt.Errorf("invalid interval %q", interval)
	}
	start, end := f.Since, f.Until
	if start == 0 || end == 0 {
		where, args, err := f.buildWhere()
		if err != nil {
			return 0, 0, 0, err
		}
		q := `SELECT MIN(ts),MAX(ts) FROM events`
		if where != "" {
			q += " WHERE " + where
		}
		var lo, hi sql.NullInt64
		if err := db.QueryRow(q, args...).Scan(&lo, &hi); err != nil {
			return 0, 0, 0, err
		}
		if !lo.Valid {
			return 0, 0, step, nil
		}
		if start == 0 {
			start = lo.Int64
		}
		if end == 0 {
			end = hi.Int64 + 1
		}
	}
	if end <= start {
		return 0, 0, 0, fmt.Errorf("until must be after since")
	}
	if span := end - start; span/step >= maxSeriesPoints {
		step *= ((span / step) + maxSeriesPoints - 2) / (maxSeriesPoints - 1)
	}
	start = (start / step) * step
	return start, end, step, nil
}
func gridBucketExpr(step int64) string {
	return fmt.Sprintf("strftime('%%Y-%%m-%%dT%%H:%%M:00Z', ((ts / %d) * %d) / 1000, 'unixepoch')", step, step)
}
func fillSeries(rows []map[string]any, start, end, step int64, aggregation string) []map[string]any {
	if end == 0 {
		return []map[string]any{}
	}
	byTime := map[int64]map[string]any{}
	for _, row := range rows {
		raw, _ := row["bucket"].(string)
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			t, err = time.Parse("2006-01-02", raw)
		}
		if err == nil {
			byTime[t.UnixMilli()] = row
		}
	}
	out := make([]map[string]any, 0, maxSeriesPoints)
	for ts := start; ts < end && len(out) < maxSeriesPoints; ts += step {
		row := byTime[ts]
		if row == nil {
			row = map[string]any{"bucket": time.UnixMilli(ts).UTC().Format(time.RFC3339), "count": int64(0), "value": nil, "aggregation": aggregation}
			if aggregation == "count" || aggregation == "sum" || aggregation == "sum_money" || aggregation == "distinct" {
				row["value"] = float64(0)
			}
		}
		row["ts"] = ts
		row["interval_ms"] = step
		out = append(out, row)
	}
	return out
}
func parseGridInterval(interval string) (int64, bool) {
	if !strings.HasPrefix(interval, "grid:") {
		return 0, false
	}
	n, e := strconv.ParseInt(strings.TrimPrefix(interval, "grid:"), 10, 64)
	return n, e == nil && n >= 60000
}
