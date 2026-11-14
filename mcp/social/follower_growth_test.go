package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func followerPoint(t *testing.T, ctx *sdk.AppCtx, account int64, platform, scope string, at time.Time, value int64, source string) {
	t.Helper()
	if err := insertSocialMetricPoint(ctx, "test-proj", 0, account, 0, 0, platform, scope, "followers", "snapshot", at.Format(time.RFC3339Nano), value, source, "ok", ""); err != nil {
		t.Fatal(err)
	}
}

func followerWindow() performanceWindow {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	return performanceWindow{Start: start, End: start.AddDate(0, 0, 28), Previous: start.AddDate(0, 0, -28), Days: 28}
}

func TestFollowerGrowthBoundaryEvidence(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		baseline, end                 int64
		startOffset, endOffset        time.Duration
		omitBaseline, omitEnd, oneDay bool
		want                          *float64
		reason                        string
	}{
		{name: "gain", baseline: 1000, end: 1100, want: floatPtr(10)},
		{name: "loss", baseline: 1000, end: 800, want: floatPtr(-20)},
		{name: "unchanged", baseline: 1000, end: 1000, want: floatPtr(0)},
		{name: "fell_to_zero", baseline: 1000, end: 0, want: floatPtr(-100)},
		{name: "zero_baseline", baseline: 0, end: 100, reason: "zero_baseline"},
		{name: "zero_to_zero", baseline: 0, end: 0, reason: "zero_baseline"},
		{name: "missing_baseline", omitBaseline: true, end: 1100, reason: "missing_baseline"},
		{name: "baseline_after_start", baseline: 1000, startOffset: time.Hour, end: 1100, reason: "missing_baseline"},
		{name: "stale_baseline", baseline: 1000, startOffset: -25 * time.Hour, end: 1100, reason: "stale_baseline"},
		{name: "stale_end", baseline: 1000, end: 1100, endOffset: -25 * time.Hour, reason: "stale_end_count"},
		{name: "fresh_daily_snapshots", baseline: 1000, startOffset: -12 * time.Hour, end: 1100, endOffset: -12 * time.Hour, want: floatPtr(10)},
		{name: "24_hour_boundary", baseline: 1000, startOffset: -24 * time.Hour, end: 1100, endOffset: -24 * time.Hour, want: floatPtr(10)},
		{name: "end_after_selected_dates", baseline: 1000, end: 1100, endOffset: time.Hour, reason: "stale_end_count"},
		{name: "same_observation", baseline: 1000, oneDay: true, omitEnd: true, reason: "no_distinct_observations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newSocialCtx(t, newRecordingPlatform())
			id := widgetAccountFixture(t, ctx, "test-proj", 0)
			window := followerWindow()
			if tc.oneDay {
				window.End, window.Days = window.Start.AddDate(0, 0, 1), 1
			}
			now := window.End.AddDate(0, 0, 5)
			if !tc.omitBaseline {
				followerPoint(t, ctx, id, "youtube", "account", window.Start.Add(tc.startOffset), tc.baseline, "start")
			}
			if !tc.omitEnd {
				followerPoint(t, ctx, id, "youtube", "account", window.End.Add(tc.endOffset), tc.end, "end")
			}
			followerPoint(t, ctx, id, "youtube", "account", now, 9999, "current")
			followerPoint(t, ctx, id, "youtube", "account", now.Add(time.Hour), 999999, "future")
			m, err := loadWidgetFollowers(ctx, "test-proj", widgetAccount{ID: id, Platform: "youtube"}, "account", window, now)
			if err != nil {
				t.Fatal(err)
			}
			if m.Value == nil || *m.Value != 9999 || m.Growth == nil || len(m.Trend) != 0 {
				t.Fatalf("bad current count: %+v", m)
			}
			if tc.want == nil {
				if m.Change != nil || m.Growth.Status != "unavailable" || m.Growth.Reason != tc.reason {
					t.Fatalf("unproven growth: %+v, %+v", m, m.Growth)
				}
			} else if m.Change == nil || math.Abs(*m.Change-*tc.want) > 1e-9 || m.Growth.Status != "available" || m.Growth.NetChange == nil || *m.Growth.NetChange != tc.end-tc.baseline {
				t.Fatalf("growth: %+v, %+v", m, m.Growth)
			}
		})
	}
}

func floatPtr(n float64) *float64 { return &n }

func TestFollowerGrowthAcrossPlatformsScopesAndUnrelatedEvidence(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	w := followerWindow()
	now := w.End.AddDate(0, 0, 2)
	for platform := range platforms {
		id := widgetAccountFixture(t, ctx, "test-proj", 0)
		provider := "native"
		if platform == "linkedin" {
			provider = "zernio"
		}
		if _, err := ctx.AppDB().Exec(`UPDATE social_accounts SET platform=?,provider_slug=? WHERE id=?`, platform, provider, id); err != nil {
			t.Fatal(err)
		}
		scope := "account"
		if platform == "pinterest" {
			scope = "provider_account"
		}
		for _, p := range []struct {
			at     time.Time
			value  int64
			source string
		}{{w.Start, 1000, "start"}, {w.End, 1100, "end"}, {now, 1200, "current"}} {
			followerPoint(t, ctx, id, platform, scope, p.at, p.value, p.source)
		}
		// A corrected observation at the same instant wins deterministically.
		followerPoint(t, ctx, id, platform, scope, w.End, 1200, "corrected-end")
		for i, bad := range []struct {
			project, platform, scope, period, status, dimensions, key string
			post, target                                              int64
			at                                                        time.Time
			value                                                     int64
		}{
			{"foreign", platform, scope, "snapshot", "ok", "{}", "", 0, 0, now, 9999},
			{"test-proj", "wrong-platform", scope, "snapshot", "ok", "{}", "", 0, 0, now, 9999},
			{"test-proj", platform, "post", "snapshot", "ok", "{}", "", 0, 0, now, 9999},
			{"test-proj", platform, scope, "day", "ok", "{}", "", 0, 0, now, 9999},
			{"test-proj", platform, scope, "snapshot", "unavailable", "{}", "", 0, 0, now, 9999},
			{"test-proj", platform, scope, "snapshot", "ok", `{"country":"US"}`, "country", 0, 0, now, 9999},
			{"test-proj", platform, scope, "snapshot", "ok", `{"country":"US"}`, "", 0, 0, now, 9999},
			{"test-proj", platform, scope, "snapshot", "ok", "{}", "", 4, 0, now, 9999},
			{"test-proj", platform, scope, "snapshot", "ok", "{}", "", 0, 8, now, 9999},
			{"test-proj", platform, scope, "snapshot", "ok", "{}", "", 0, 0, now, -1},
		} {
			_, err := ctx.AppDB().Exec(`INSERT INTO social_metric_points(project_id,social_account_id,platform,scope,metric,period,status,dimensions_json,dimensions_key,post_id,post_target_id,point_time,value,source) VALUES(?,?,?,?,'followers',?,?,?,?,?,?,?,?,?)`, bad.project, id, bad.platform, bad.scope, bad.period, bad.status, bad.dimensions, bad.key, bad.post, bad.target, bad.at.Format(time.RFC3339), bad.value, fmt.Sprintf("bad-%d", i))
			if err != nil {
				t.Fatal(err)
			}
		}
		m, err := loadWidgetFollowers(ctx, "test-proj", widgetAccount{ID: id, Platform: platform}, scope, w, now)
		if err != nil || m.Value == nil || *m.Value != 1200 || m.Change == nil || *m.Change != 20 {
			t.Fatalf("%s: %+v %v", platform, m, err)
		}
	}
	if len(pf.executeCalls) != 0 || len(pf.callAppCalls) != 0 {
		t.Fatal("follower read called a provider")
	}
}

func TestFollowerGrowthEmptyHistoryAndZeroBaselineCohorts(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	w := followerWindow()
	id := widgetAccountFixture(t, ctx, "test-proj", 0)
	m, err := loadWidgetFollowers(ctx, "test-proj", widgetAccount{ID: id, Platform: "youtube"}, "account", w, w.End)
	if err != nil || m.Value != nil || m.Change != nil || m.Growth.Reason != "missing_current_count" {
		t.Fatalf("missing stock became zero: %+v %v", m, err)
	}
	followerPoint(t, ctx, id, "youtube", "account", w.Start, 0, "start")
	followerPoint(t, ctx, id, "youtube", "account", w.End, 50, "end")
	m, err = loadWidgetFollowers(ctx, "test-proj", widgetAccount{ID: id, Platform: "youtube"}, "account", w, w.End)
	if err != nil || m.Change != nil || m.Growth.NetChange == nil || *m.Growth.NetChange != 50 || m.Growth.Reason != "zero_baseline" {
		t.Fatalf("zero denominator: %+v %v", m, err)
	}
	accounts := []widgetAccount{{Metrics: map[string]widgetMetric{"followers": m}}}
	group := summarizeWidgetFollowers(accounts, w)
	if group.Change != nil || group.Growth.Reason != "zero_baseline" {
		t.Fatalf("group fabricated percent: %+v", group)
	}
	other := widgetAccountFixture(t, ctx, "test-proj", 0)
	followerPoint(t, ctx, other, "youtube", "account", w.Start, 100, "start")
	followerPoint(t, ctx, other, "youtube", "account", w.End, 110, "end")
	m, err = loadWidgetFollowers(ctx, "test-proj", widgetAccount{ID: other, Platform: "youtube"}, "account", w, w.End)
	if err != nil {
		t.Fatal(err)
	}
	accounts = append(accounts, widgetAccount{Metrics: map[string]widgetMetric{"followers": m}})
	group = summarizeWidgetFollowers(accounts, w)
	if group.Change == nil || *group.Change != 60 || group.Growth.Accounts != 2 {
		t.Fatalf("zero-baseline member was dropped from comparable cohort: %+v", group)
	}
}

func TestFollowerGrowthWidgetAggregatesMatchedBalances(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	w := followerWindow()
	ids := []int64{widgetAccountFixture(t, ctx, "test-proj", 0), widgetAccountFixture(t, ctx, "test-proj", 0)}
	for i, id := range ids {
		base, end, current := int64(1000), int64(1200), int64(1500)
		if i == 1 {
			base, end, current = 9000, 9000, 9500
		}
		followerPoint(t, ctx, id, "youtube", "account", w.Start, base, "start")
		followerPoint(t, ctx, id, "youtube", "account", w.End, end, "end")
		followerPoint(t, ctx, id, "youtube", "account", w.End.AddDate(0, 0, 1), current, "current")
	}
	read := func() widgetMetric {
		t.Helper()
		r := httptest.NewRecorder()
		(&App{}).handlePerformanceWidget(r, httptest.NewRequest("GET", "/widgets/performance?start_date=2026-10-01&end_date=2026-10-28", nil))
		var data struct{ Metrics map[string]widgetMetric }
		if r.Code != 200 {
			t.Fatal(r.Body.String())
		}
		if err := json.Unmarshal(r.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		return data.Metrics["followers"]
	}
	m := read()
	if m.Value == nil || *m.Value != 11000 || m.Change == nil || *m.Change != 2 || m.Growth.EndValue == nil || *m.Growth.EndValue != 10200 {
		t.Fatalf("aggregate averaged percentages or used current counts: %+v %+v", m, m.Growth)
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM social_metric_points WHERE social_account_id=? AND source='start'`, ids[1]); err != nil {
		t.Fatal(err)
	}
	m = read()
	if m.Change != nil || *m.Value != 11000 || m.Growth.Reason != "incomplete_account_coverage" || m.Growth.Accounts != 1 {
		t.Fatalf("partial cohort fabricated growth: %+v %+v", m, m.Growth)
	}
}
