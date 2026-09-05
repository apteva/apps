package main

import (
	"database/sql"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"sort"
	"strings"
)

type metricDB interface {
	Exec(string, ...any) (sql.Result, error)
}

func insertSocialMetricPointDB(db metricDB, pid string, profileID, accountID, postID, targetID int64, platform, scope, metric, period, pointTime string, value int64, source, status, note string) error {
	return insertSocialMetricPointDimensionsDB(db, pid, profileID, accountID, postID, targetID, platform, scope, metric, period, pointTime, value, source, status, note, nil)
}
func (q analyticsQuery) cacheKey() string {
	if q.RangeDays <= 0 {
		q.RangeDays = 28
	}
	start, end := q.dates()
	filters := map[string][]string{}
	for k, v := range q.Filters {
		cp := append([]string{}, v...)
		sort.Strings(cp)
		filters[k] = cp
	}
	dims := canonicalDimensions(q.Breakdowns)
	sort.Strings(dims)
	groups := canonicalDimensions(q.GroupBy)
	sort.Strings(groups)
	raw, _ := json.Marshal(struct {
		Start, End         string
		Filters            map[string][]string
		Dimensions, Groups []string
	}{start, end, filters, dims, groups})
	return string(raw)
}
func (q analyticsQuery) snapshotPeriod(metric string) string {
	if len(q.Filters) == 0 {
		switch metric {
		case "followers", "following", "total_likes", "total_videos", "posts":
			return "snapshot"
		}
	}
	return "query:" + q.cacheKey()
}
func loadAccountMetricHistoryForQuery(ctx *sdk.AppCtx, pid string, accountID int64, q analyticsQuery) insightSeries {
	history := loadAccountMetricHistory(ctx, pid, accountID, 730)
	start, end := q.dates()
	out := insightSeries{}
	for metric, points := range history {
		for _, p := range points {
			date := strings.Split(p.Time, "T")[0]
			if len(date) > 10 {
				date = date[:10]
			}
			if date >= start && date <= end {
				out[metric] = append(out[metric], p)
			}
		}
	}
	return out
}

func accountMetricKnown(res accountMetricsResult, name string) bool {
	for _, available := range res.Available {
		if available == name {
			return true
		}
	}
	values := extractNamedNumbers(res.Raw)
	aliases := map[string][]string{
		"followers": {"followers", "followers_count", "follower_count", "subscriberCount"},
		"following": {"following", "following_count"}, "views": {"views", "viewCount", "view_count"},
		"posts": {"posts", "media_count", "videoCount"}, "total_likes": {"likes_count", "likesCount"},
		"total_videos": {"video_count", "videoCount"},
	}
	keys := append([]string{name}, aliases[name]...)
	return namedMetricPresent(values, keys...)
}
