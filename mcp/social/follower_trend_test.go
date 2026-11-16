package main

import (
	"testing"
	"time"
)

func TestFollowerTrendUsesDailyClosingSnapshotsAndLeavesGaps(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	id := widgetAccountFixture(t, ctx, "test-proj", 0)
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	w := performanceWindow{Start: start, End: start.AddDate(0, 0, 4), Days: 4}
	// The opening balance belongs to the preceding day and cannot fill a gap.
	followerPoint(t, ctx, id, "youtube", "account", start, 99, "opening")
	followerPoint(t, ctx, id, "youtube", "account", start.Add(12*time.Hour), 100, "midday")
	followerPoint(t, ctx, id, "youtube", "account", start.AddDate(0, 0, 1), 110, "close")
	followerPoint(t, ctx, id, "youtube", "account", start.AddDate(0, 0, 1), 111, "corrected-close")
	// No snapshot during day 2: yesterday's count is not carried forward.
	followerPoint(t, ctx, id, "youtube", "account", start.AddDate(0, 0, 3), 0, "day3-close")
	followerPoint(t, ctx, id, "youtube", "account", start.AddDate(0, 0, 4).Add(-time.Hour), 150, "day4-evening")
	followerPoint(t, ctx, id, "youtube", "account", w.End.Add(time.Hour), 999, "after-window")
	if err := insertSocialMetricPoint(ctx, "test-proj", 0, id, 0, 0, "youtube", "account", "followers", "day", start.AddDate(0, 0, 2).Format(time.RFC3339), 99999, "daily-gains", "ok", ""); err != nil {
		t.Fatal(err)
	}
	points, count, err := loadFollowerTrend(ctx, "test-proj", widgetAccount{ID: id, Platform: "youtube"}, "account", w)
	if err != nil || len(points) != 4 || count != 3 {
		t.Fatalf("%+v %d %v", points, count, err)
	}
	if points[0].Value == nil || *points[0].Value != 111 || points[0].ObservedAt != "2026-11-02T00:00:00.000Z" || points[1].Value != nil || points[1].Accounts != 0 || points[2].Value == nil || *points[2].Value != 0 || points[3].Value == nil || *points[3].Value != 150 {
		t.Fatalf("bad closing balances: %+v", points)
	}
	other := widgetAccountFixture(t, ctx, "test-proj", 0)
	for day := 1; day <= 4; day++ {
		followerPoint(t, ctx, other, "youtube", "account", start.AddDate(0, 0, day), 200, "other")
	}
	accounts := []widgetAccount{}
	for _, account := range []int64{id, other} {
		m, err := loadWidgetFollowers(ctx, "test-proj", widgetAccount{ID: account, Platform: "youtube"}, "account", w, w.End.AddDate(0, 0, 1))
		if err != nil {
			t.Fatal(err)
		}
		accounts = append(accounts, widgetAccount{Metrics: map[string]widgetMetric{"followers": m}})
	}
	m := summarizeWidgetFollowers(accounts, w)
	if len(m.Trend) != 4 || m.Trend[0].Value == nil || *m.Trend[0].Value != 311 || m.Trend[1].Value != nil || m.Trend[1].Accounts != 1 || m.Trend[2].Value == nil || *m.Trend[2].Value != 200 {
		t.Fatalf("combined trend fabricated a dip: %+v", m.Trend)
	}
}
