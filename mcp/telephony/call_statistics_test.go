package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func statisticsFixture(t *testing.T, a *App, id, status, at string, metrics map[string]float64, observed bool) {
	t.Helper()
	phoneTestCall(t, a, id, status)
	durationExec(t, a, `UPDATE calls SET placed_at=?,direction='inbound',carrier_slug='twilio',from_number='+12025550101',to_number='+12025550102',answered_at='',ended_at='' WHERE id=?`, at, id)
	s := audioDashboardSummary{Telemetry: observed, Metrics: metrics, ObservedAt: at}
	raw, e := json.Marshal(s)
	if e != nil {
		t.Fatal(e)
	}
	durationExec(t, a, `INSERT INTO telephony_audio_reports VALUES(?,?,?,?,?,?,?,?)`, id, "project-a", audioDashboardTime(at), "twilio", 0, "||", "||", string(raw))
	durationExec(t, a, `DELETE FROM telephony_audio_pending WHERE call_id=?`, id)
}
func statisticsTestFilter(t *testing.T, q string) callStatisticsFilter {
	t.Helper()
	f, e := parseCallStatisticsFilter(httptest.NewRequest("GET", "/audio-health?view=statistics&from=2026-10-25T00:00:00Z&until=2026-10-26T00:00:00Z"+q, nil), time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC))
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func TestCallStatisticsCohortsRatesAndMissingTelemetry(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	now := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	statisticsFixture(t, a, "before", "completed", "2026-10-24T23:59:59Z", map[string]float64{"sip_jitter_discarded_ms": 300}, true)
	statisticsFixture(t, a, "inside", "completed", "2026-10-25T00:00:00Z", map[string]float64{"sip_jitter_discarded_ms": 300, "sip_expired_discarded_ms": 100, "sip_late_discarded_ms": 200, "sip_pacer_sent_ms": 900, "sip_pacer_dropped_ms": 100}, true)
	statisticsFixture(t, a, "healthy", "in-progress", "2026-10-25T10:00:00+02:00", map[string]float64{"sip_pacer_sent_ms": 8900, "sip_pacer_dropped_ms": 100}, true)
	statisticsFixture(t, a, "unknown", "no-answer", "2026-10-25T12:00:00Z", map[string]float64{}, false)
	statisticsFixture(t, a, "outside", "completed", "2026-10-26T00:00:00Z", map[string]float64{}, true)
	statisticsFixture(t, a, "foreign", "failed", "2026-10-25T10:00:00Z", map[string]float64{}, true)
	durationExec(t, a, `UPDATE calls SET project_id='other' WHERE id='foreign'`)
	durationExec(t, a, `UPDATE calls SET answered_at='2026-10-25T23:59:30Z',ended_at='2026-10-26T00:00:30Z' WHERE id='inside'`)
	// Latest observation outside the period must not move the call's cohort.
	durationExec(t, a, `UPDATE telephony_audio_reports SET observed_ms=? WHERE call_id='inside'`, now.UnixMilli())
	r, e := a.db().callStatistics(context.Background(), "project-a", statisticsTestFilter(t, ""), now)
	if e != nil {
		t.Fatal(e)
	}
	total := r["totals"].(statisticsTotals)
	prev := r["previous"].(statisticsTotals)
	if total.Calls != 3 || total.Observed != 2 || total.Problems != 1 || total.Missed != 1 || total.Active != 1 || total.Answered != 1 || total.TalkSeconds != 60 {
		t.Fatalf("totals %+v", total)
	}
	if *total.ProblemPercent != 50 || *total.SendDiscardPercent != 2 || total.SIPDiscardMS != 300 || prev.Calls != 1 {
		t.Fatalf("rates %+v previous %+v", total, prev)
	}
	if len(r["calls"].([]statisticsCall)) != 3 {
		t.Fatal(r["calls"])
	}
	f := statisticsTestFilter(t, "&threshold_ms=20&focus=problems")
	r, e = a.db().callStatistics(context.Background(), "project-a", f, now)
	if e != nil {
		t.Fatal(e)
	}
	// Same-pacer dropped telemetry is paired for rates; legacy aggregate is separate.
	if r["totals"].(statisticsTotals).Calls != 3 {
		t.Fatal("detail focus changed period denominator")
	}
}
func TestCallStatisticsPaginationFiltersAndTransactionFailures(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	now := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	for i := 0; i < 61; i++ {
		statisticsFixture(t, a, fmt.Sprintf("same-%03d", i), "completed", "2026-10-25T12:00:00Z", nil, false)
	}
	f := statisticsTestFilter(t, "")
	r, e := a.db().callStatistics(context.Background(), "project-a", f, now)
	if e != nil {
		t.Fatal(e)
	}
	if r["matching_calls"] != 61 || len(r["calls"].([]statisticsCall)) != 50 || r["next_cursor"] == "" {
		t.Fatal("pagination", r["matching_calls"])
	}
	if r["totals"].(statisticsTotals).ProblemPercent != nil {
		t.Fatal("missing telemetry fabricated a healthy rate")
	}
	seen := map[string]bool{}
	for _, c := range r["calls"].([]statisticsCall) {
		seen[c.ID] = true
	}
	f.Cursor = r["next_cursor"].(string)
	r, e = a.db().callStatistics(context.Background(), "project-a", f, now)
	if e != nil {
		t.Fatal(e)
	}
	if len(r["calls"].([]statisticsCall)) != 11 {
		t.Fatal(r["calls"])
	}
	for _, c := range r["calls"].([]statisticsCall) {
		if seen[c.ID] {
			t.Fatal("duplicate cursor row")
		}
	}
	r, e = a.db().callStatistics(context.Background(), "project-a", statisticsTestFilter(t, "&provider=telnyx"), now)
	if e != nil {
		t.Fatal(e)
	}
	if r["totals"].(statisticsTotals).Calls != 0 {
		t.Fatal("provider isolation")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = a.db().callStatistics(canceled, "project-a", f, now); e == nil {
		t.Fatal("read failure returned success")
	}
}
func TestCallStatisticsAuthorizationAndValidation(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	user := phoneTestIdentity("alice")
	for _, path := range []string{"/audio-health?view=statistics", "/audio-health?view=statistics&project_id=other"} {
		w := phoneTestRequest(a, &user, "GET", path, nil)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	for _, query := range []string{"&timezone=invalid/location", "&direction=bad", "&focus=bad", "&threshold_ms=0", "&cursor=bad", "&segment_from=2026-10-24T00:00:00Z", "&until=2027-01-01T00:00:00Z"} {
		r := httptest.NewRequest("GET", "/?from=2026-10-25T00:00:00Z&until=2026-10-26T00:00:00Z"+query, nil)
		// url.Values.Get takes the first value; set until explicitly for that case.
		if strings.Contains(query, "until=") {
			q := r.URL.Query()
			q.Set("until", "2027-01-01T00:00:00Z")
			r.URL.RawQuery = q.Encode()
		}
		if _, e := parseCallStatisticsFilter(r, time.Now()); e == nil {
			t.Fatal(query)
		}
	}
	w := phoneTestRequest(a, nil, "GET", "/audio-health?view=statistics", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestCallStatisticsDSTAndIndexWithLargeHistory(t *testing.T) {
	loc, e := time.LoadLocation("Europe/Madrid")
	if e != nil {
		t.Fatal(e)
	}
	f := callStatisticsFilter{From: time.Date(2026, 10, 23, 0, 0, 0, 0, loc), Until: time.Date(2026, 10, 28, 0, 0, 0, 0, loc), Location: loc}
	buckets := statisticsBuckets(f)
	if len(buckets) != 5 {
		t.Fatal(len(buckets))
	}
	if buckets[2].end-buckets[2].start != 25*3600 {
		t.Fatal("DST day was not 25 hours", buckets[2])
	}
	softphoneTestCtx(t)
	a := &App{installID: 42}
	// Use compact historical inserts; this checks the actual production query plan.
	durationExec(t, a, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<20000) INSERT INTO calls(id,thread_id,project_id,placed_at,status,to_number,from_number,directive,voice,audio_bridge_url) SELECT 'archive-'||i,'archive-thread-'||i,'project-a','2020-01-01T00:00:00Z','completed','','','','','' FROM n`)
	statisticsFixture(t, a, "recent", "completed", "2026-10-25T12:00:00Z", nil, false)
	q, args := statisticsTestFilter(t, "").where("project-a")
	rows, e := a.db().db.Query(`EXPLAIN QUERY PLAN SELECT c.id`+statisticsJoin+`WHERE `+q, args...)
	if e != nil {
		t.Fatal(e)
	}
	plan := ""
	for rows.Next() {
		var id, parent, unused int
		var detail string
		rows.Scan(&id, &parent, &unused, &detail)
		plan += detail
	}
	rows.Close()
	if !strings.Contains(plan, "idx_calls_statistics_start") {
		t.Fatal(plan)
	}
	started := time.Now()
	r, e := a.db().callStatistics(context.Background(), "project-a", statisticsTestFilter(t, ""), time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if r["totals"].(statisticsTotals).Calls != 1 {
		t.Fatal(r["totals"])
	}
	t.Logf("20,000 archived calls; scoped statistics read %s", time.Since(started))
	durationExec(t, a, "UPDATE calls SET placed_at='2026-10-25T10:00:00Z' WHERE id LIKE 'archive-%'")
	started = time.Now()
	r, e = a.db().callStatistics(context.Background(), "project-a", statisticsTestFilter(t, ""), time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if r["totals"].(statisticsTotals).Calls != 20001 || len(r["calls"].([]statisticsCall)) != 50 || r["next_cursor"] == "" {
		t.Fatal("in-window history incomplete")
	}
	t.Logf("20,001 in-window calls; complete statistics read %s", time.Since(started))
	durationExec(t, a, `INSERT INTO telephony_audio_reports SELECT id,project_id,1792886400000,'twilio',1,'||','||','{"telemetry":true,"metrics":{"sip_packets_received":10000,"sip_jitter_discarded_ms":300,"sip_concealed_ms":20,"sip_sequence_loss":1,"sip_pacer_sent_ms":99700,"sip_pacer_dropped_ms":300,"carrier_send_dropped_ms":300}}' FROM calls WHERE id LIKE 'archive-%'`)
	started = time.Now()
	r, e = a.db().callStatistics(context.Background(), "project-a", statisticsTestFilter(t, ""), time.Now())
	if e != nil {
		t.Fatal(e)
	}
	total := r["totals"].(statisticsTotals)
	if total.Observed != 20000 || total.Problems != 20000 || total.SIPDiscardMS != 6000000 {
		t.Fatalf("large summaries %+v", total)
	}
	t.Logf("20,001 in-window calls with 20,000 compact audio reports; complete read %s", time.Since(started))
}
func TestCallStatisticsPacerCoverageUsesPairedCounters(t *testing.T) {
	var total statisticsTotals
	for _, v := range [][]float64{{0, 0, 0, 0, 0, 100, 0, 0, 0, -1, -1, 0, 0}, {0, 0, 0, 0, 0, 100, 0, 0, 0, 900, 100, 0, 0}} {
		total.add(statisticsCall{Observed: true, metrics: v})
	}
	total.finish()
	if total.SendRateCalls != 1 || *total.SendDiscardPercent != 10 || total.SendDiscardMS != 200 {
		t.Fatal(total)
	}
	summary := audioDashboardSummary{Metrics: map[string]float64{}}
	addCarrierAudioSummary(&summary, &carrierTelemetryRecord{SIP: &sipLiveSnapshot{PacerAvailable: true, Pacer: livePacerSnapshot{SentMS: 900, DroppedMS: 100}}})
	if summary.Metrics["sip_pacer_sent_ms"] != 900 || summary.Metrics["sip_pacer_dropped_ms"] != 100 {
		t.Fatal(summary.Metrics)
	}
}

func TestCallStatisticsOwnershipCoveragePendingAndEmpty(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	now := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	for _, id := range []string{"alice-1", "alice-2", "bob"} {
		statisticsFixture(t, a, id, "completed", "2026-10-25T12:00:00Z", map[string]float64{"sip_packets_received": 100, "sip_jitter_discarded_ms": 20}, true)
		owner := phoneTestIdentity("145")
		if id == "bob" {
			owner.IssuerInstallID = "other-auth"
		}
		durationExec(t, a, "INSERT INTO telephony_call_owners VALUES(?,?,?,?)", id, "project-a", owner.key(), "sales")
	}
	durationExec(t, a, "INSERT INTO telephony_audio_pending VALUES(?)", "alice-1")
	durationExec(t, a, "UPDATE calls SET answered_at='2026-10-25T12:00:01Z',ended_at='' WHERE id='alice-1'")
	f := statisticsTestFilter(t, "&adviser="+url.QueryEscape(phoneTestIdentity("145").key())+"&number=%2B12025550102&direction=inbound")
	r, e := a.db().callStatistics(context.Background(), "project-a", f, now)
	if e != nil {
		t.Fatal(e)
	}
	total := r["totals"].(statisticsTotals)
	if total.Calls != 2 || total.Pending != 1 || total.MetricCalls["sip"] != 2 || total.MetricCalls["browser"] != 0 || total.SendDiscardPercent != nil {
		t.Fatalf("%+v", total)
	}
	if total.AverageSeconds != nil {
		t.Fatal("missing end time fabricated zero duration")
	}
	// A project-mismatched ownership record must not match the adviser filter.
	durationExec(t, a, "UPDATE telephony_call_owners SET project_id='other' WHERE call_id='alice-2'")
	r, e = a.db().callStatistics(context.Background(), "project-a", f, now)
	if e != nil || r["totals"].(statisticsTotals).Calls != 1 {
		t.Fatalf("%v %v", r, e)
	}
	r, e = a.db().callStatistics(context.Background(), "empty", f, now)
	if e != nil {
		t.Fatal(e)
	}
	total = r["totals"].(statisticsTotals)
	if total.Calls != 0 || total.ProblemPercent != nil || total.CoveragePercent != nil || r["complete"] != true || len(r["calls"].([]statisticsCall)) != 0 {
		t.Fatalf("empty %+v", r)
	}
}
