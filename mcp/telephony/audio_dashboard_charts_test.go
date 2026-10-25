package main

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAudioDashboardChartsFollowWindowFiltersAndWholeCohort(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	from := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	until := from.Add(time.Hour)
	for _, id := range []string{"a", "b", "unknown", "outside", "foreign"} {
		statisticsFixture(t, a, id, "completed", from.Format(time.RFC3339), map[string]float64{"sip_jitter_discarded_ms": 300}, id != "unknown")
	}
	durationExec(t, a, `UPDATE telephony_audio_reports SET has_issues=1,issue_codes='|dropped_audio|',stage_codes='|carrier_to_telephony|' WHERE call_id='a'`)
	durationExec(t, a, `UPDATE telephony_audio_reports SET observed_ms=? WHERE call_id='b'`, until.UnixMilli())
	durationExec(t, a, `UPDATE telephony_audio_reports SET observed_ms=? WHERE call_id='outside'`, until.Add(time.Millisecond).UnixMilli())
	durationExec(t, a, `UPDATE calls SET project_id='other' WHERE id='foreign'`)
	durationExec(t, a, `UPDATE telephony_audio_reports SET project_id='other' WHERE call_id='foreign'`)
	f := audioDashboardFilter{From: from.UnixMilli(), Until: until.UnixMilli(), Limit: 1, Cursor: "not-used-by-chart"}
	got, e := a.db().audioDashboardCharts(context.Background(), "project-a", f, until)
	if e != nil {
		t.Fatal(e)
	}
	var n, observed, affected int64
	for _, b := range got {
		n += b.Calls
		observed += b.Observed
		affected += b.Affected
	}
	if n != 3 || observed != 2 || affected != 1 || len(got) != 60 || got[59].Calls != 1 {
		t.Fatalf("whole cohort %d %d %d %+v", n, observed, affected, got)
	}
	if got[1].ProblemPercent != nil || got[0].ProblemPercent == nil || *got[0].ProblemPercent != 100 {
		t.Fatal("unknown/affected rates")
	}
	f.Issue = "dropped_audio"
	f.Stage = "carrier_to_telephony"
	got, e = a.db().audioDashboardCharts(context.Background(), "project-a", f, until)
	if e != nil || got[0].Calls != 1 {
		t.Fatalf("filtered %v %v", got, e)
	}
	f.Provider = "telnyx"
	got, e = a.db().audioDashboardCharts(context.Background(), "project-a", f, until)
	if e != nil || got[0].Calls != 0 || got[0].ProblemPercent != nil {
		t.Fatalf("empty %v %v", got, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = a.db().audioDashboardCharts(ctx, "project-a", f, until); e == nil {
		t.Fatal("read failure returned success")
	}
}
func TestAudioDashboardChartsOptInAndBounds(t *testing.T) {
	for _, span := range []time.Duration{time.Hour, 24 * time.Hour, 7 * 24 * time.Hour, 31 * 24 * time.Hour} {
		softphoneTestCtx(t)
		a := &App{installID: 42}
		now := time.Now()
		f := audioDashboardFilter{From: now.Add(-span).UnixMilli(), Until: now.UnixMilli()}
		buckets, e := a.db().audioDashboardCharts(context.Background(), "project-a", f, now)
		if e != nil || len(buckets) > 60 || len(buckets) == 0 {
			t.Fatalf("%s %d %v", span, len(buckets), e)
		}
	}
	softphoneTestCtx(t)
	a := &App{installID: 42}
	if v := dashboardTestResult(t, a, "?include_charts=true"); v["charts"] == nil {
		t.Fatal("missing charts")
	}
	if v := dashboardTestResult(t, a, ""); v["charts"] != nil {
		t.Fatal("default response added chart cost")
	}
	if _, e := parseAudioDashboardFilter(httptest.NewRequest("GET", "/?include_charts=bad", nil), time.Now()); e == nil {
		t.Fatal("invalid flag accepted")
	}
}
