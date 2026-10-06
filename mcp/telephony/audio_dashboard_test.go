package main

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func audioDashboardTestSample(t *testing.T, a *App, id string, b browserAudioDiagnostics) {
	t.Helper()
	phoneTestCall(t, a, id, "in-progress")
	if err := a.db().updateBrowserAudioDiagnostics(id, b); err != nil {
		t.Fatal(err)
	}
}
func dashboardTestResult(t *testing.T, a *App, query string) map[string]any {
	t.Helper()
	w := phoneTestRequest(a, nil, "GET", "/audio-health"+query, nil)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var v map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func TestAudioDashboardFiltersTotalsAndCursor(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	for i := range 125 {
		b := browserAudioDiagnostics{PlaybackDroppedMS: 20}
		if i >= 110 {
			b.PlaybackDroppedMS = 0
		}
		audioDashboardTestSample(t, a, fmt.Sprintf("audio-%03d", i), b)
	}
	if _, err := a.db().db.Exec(`UPDATE calls SET carrier_slug='telnyx' WHERE id='audio-001'`); err != nil {
		t.Fatal(err)
	}
	if err := a.db().refreshAudioDashboard(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := dashboardTestResult(t, a, "?limit=100")
	if first["pending_reports"] != float64(25) {
		t.Fatal(first["pending_reports"])
	}
	if err := a.db().refreshAudioDashboard(context.Background()); err != nil {
		t.Fatal(err)
	}
	result := dashboardTestResult(t, a, "?state=issues&limit=100")
	if result["totals"].(map[string]any)["calls"] != float64(110) {
		t.Fatal(result["totals"])
	}
	rows := result["calls"].([]any)
	if len(rows) != 100 || result["next_cursor"] == "" {
		t.Fatalf("pagination %+v", result["next_cursor"])
	}
	next := dashboardTestResult(t, a, "?state=issues&limit=100&cursor="+result["next_cursor"].(string))
	if len(next["calls"].([]any)) != 10 {
		t.Fatal(next["calls"])
	}
	ids := map[string]bool{}
	for _, v := range append(rows, next["calls"].([]any)...) {
		id := v.(map[string]any)["call_id"].(string)
		if ids[id] {
			t.Fatal("duplicate page row", id)
		}
		ids[id] = true
	}
	for query, want := range map[string]int{"?provider=telnyx": 1, "?issue=dropped_audio": 110, "?stage=telephony_to_browser": 110, "?stage=browser_to_telephony": 0, "?search=audio-001": 1, "?adviser=__unassigned__": 125} {
		v := dashboardTestResult(t, a, query)
		if v["totals"].(map[string]any)["calls"] != float64(want) {
			t.Fatalf("%s %+v", query, v["totals"])
		}
	}
	var plan []string
	rr, e := a.db().db.Query(`EXPLAIN QUERY PLAN SELECT call_id FROM telephony_audio_reports WHERE project_id=? AND observed_ms BETWEEN ? AND ? ORDER BY observed_ms DESC,call_id DESC LIMIT 50`, "project-a", 0, time.Now().UnixMilli())
	if e != nil {
		t.Fatal(e)
	}
	defer rr.Close()
	for rr.Next() {
		var id, parent, unused int
		var detail string
		rr.Scan(&id, &parent, &unused, &detail)
		plan = append(plan, detail)
	}
	if !strings.Contains(strings.Join(plan, " "), "telephony_audio_reports_window") {
		t.Fatalf("unindexed dashboard %v", plan)
	}
}
func TestAudioDashboardFreshnessPermissionsAndAdvisers(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	now := time.Now().UTC()
	server := &serverAudioDiagnostics{UpdatedAt: now.Format(time.RFC3339Nano), Health: audioHealthSnapshot{State: "audio_degraded", Stages: map[string]audioHealthStage{"telephony_to_browser": {State: "audio_degraded", LastBadAt: now.Format(time.RFC3339Nano)}}}}
	for _, id := range []string{"current", "stale", "ended", "other"} {
		audioDashboardTestSample(t, a, id, browserAudioDiagnostics{Server: server})
	}
	phoneTestCall(t, a, "no-report", "in-progress")
	if _, e := a.db().db.Exec(`UPDATE calls SET project_id='other-project' WHERE id='other'`); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db().db.Exec(`UPDATE calls SET ended_at=?,status='completed' WHERE id='ended'`, now.Format(time.RFC3339Nano)); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"current", "stale"} {
		p := phoneTestIdentity(id)
		if _, e := a.db().db.Exec(`INSERT INTO telephony_call_owners VALUES(?,?,?,?)`, id, "project-a", p.key(), "sales"); e != nil {
			t.Fatal(e)
		}
	}
	if e := a.db().refreshAudioDashboard(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db().db.Exec(`UPDATE telephony_audio_reports SET observed_ms=? WHERE call_id='stale'`, now.Add(-30*time.Second).UnixMilli()); e != nil {
		t.Fatal(e)
	}
	v := dashboardTestResult(t, a, "?state=degraded")
	if v["totals"].(map[string]any)["calls"] != float64(1) {
		t.Fatal(v)
	}
	all := dashboardTestResult(t, a, "")
	if all["totals"].(map[string]any)["calls"] != float64(4) {
		t.Fatal(all)
	}
	states := map[string]string{}
	for _, v := range all["calls"].([]any) {
		r := v.(map[string]any)
		states[r["call_id"].(string)] = r["state"].(string)
	}
	if states["stale"] != "stale" || states["ended"] != "ended" || states["no-report"] != "unobserved" {
		t.Fatal(states)
	}
	user := phoneTestIdentity("alice")
	w := phoneTestRequest(a, &user, "GET", "/audio-health", nil)
	if w.Code != 403 || strings.Contains(w.Body.String(), "current") {
		t.Fatalf("delegated enumeration %d %s", w.Code, w.Body)
	}
	w = phoneTestRequest(a, nil, "GET", "/audio-health?project_id=other-project", nil)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	w = phoneTestRequest(a, nil, "GET", "/audio-health?call_id=other", nil)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	p := phoneTestIdentity("current")
	r := httptest.NewRequest("GET", "/audio-health", nil)
	q := r.URL.Query()
	q.Set("adviser", p.key())
	r.URL.RawQuery = q.Encode()
	f, e := parseAudioDashboardFilter(r, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	result, e := a.db().audioDashboard(context.Background(), "project-a", f, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if len(result["calls"].([]audioDashboardCall)) != 1 {
		t.Fatal(result)
	}
	for _, query := range []string{"?cursor=invalid", "?from=2025-01-01T00:00:00Z", "?stage=wrong", "?issue=wrong", "?limit=101", "?state=wrong"} {
		w = phoneTestRequest(a, nil, "GET", "/audio-health"+query, nil)
		if w.Code != 400 {
			t.Fatalf("%s %d %s", query, w.Code, w.Body)
		}
	}
}
func TestAudioDashboardSummaryDoesNotDoubleCountOrChangeCall(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	b := browserAudioDiagnostics{PlaybackDroppedMS: 10, SessionEvents: []mediaSessionEvent{{Outcome: "transport_error"}, {Outcome: "temporary_failure", Status: 503}}, Timing: &browserAudioTiming{}}
	b.Timing.Transport.CaptureDroppedMS = 20
	b.Timing.Transport.ReconnectAttempts = 1
	b.Timing.Runtime.AudioContextSuspendCount = 2
	b.Server = &serverAudioDiagnostics{UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Socket: audioSocketSnapshot{BrowserTotals: map[string]float64{"audio_context_suspend_count": 3}, Reconnects: 1}, Reception: carrierReceptionSnapshot{Stalls: 1, StaleDroppedMS: 30}, CaptureStaleBytes: 960, ToBrowser: liveAudioQueueSnapshot{StaleBytes: 480, MaxWriteMS: 300}}
	audioDashboardTestSample(t, a, "observation", b)
	row, _ := a.db().findCall("observation")
	s := summarizeAudioDashboard(row)
	if s.Metrics["context_suspensions"] != 3 || s.Metrics["server_capture_dropped_ms"] != 20 || s.Metrics["playback_dropped_ms"] != 10 || s.Metrics["server_browser_dropped_ms"] != 10 {
		t.Fatal(s.Metrics)
	}
	for _, issue := range []string{"browser_error", "carrier_stall", "reconnect", "context_suspended", "write_delay"} {
		if !containsString(s.Issues, issue) {
			t.Fatal(issue, s.Issues)
		}
	}
	// A broken monitoring projection cannot remove the authoritative diagnostic
	// report, modify the call or lose its pending work.
	if _, e := a.db().db.Exec(`DROP TABLE telephony_audio_reports`); e != nil {
		t.Fatal(e)
	}
	if e := a.db().refreshAudioDashboard(context.Background()); e == nil {
		t.Fatal("expected summary failure")
	}
	var pending int
	a.db().db.QueryRow(`SELECT count(*) FROM telephony_audio_pending WHERE call_id='observation'`).Scan(&pending)
	if pending != 1 {
		t.Fatal("lost queued work")
	}
	after, _ := a.db().findCall(row.ID)
	if after.Status != row.Status || after.PeerToken != row.PeerToken || after.BrowserAudioDiagnostics != row.BrowserAudioDiagnostics {
		t.Fatal("monitor changed call")
	}
}
func TestAudioDashboardAlertPersistenceAndWidgetManifest(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	for _, project := range []string{"project-a", "other-project"} {
		for _, state := range []string{"audio_degraded", "recovered"} {
			if e := a.db().saveAudioDashboardAlert(audioAlert{ProjectID: project, Provider: "twilio", Stage: "carrier_to_telephony", State: state, CallCount: 3, CallIDs: []string{"a", "b", "c"}, At: time.Now().UTC().Format(time.RFC3339Nano)}); e != nil {
				t.Fatal(e)
			}
		}
	}
	v := dashboardTestResult(t, a, "?provider=twilio&stage=carrier_to_telephony")
	if len(v["alerts"].([]any)) != 2 {
		t.Fatal(v["alerts"])
	}
	v = dashboardTestResult(t, a, "?provider=telnyx")
	if len(v["alerts"].([]any)) != 0 {
		t.Fatal(v["alerts"])
	}
	raw, e := os.ReadFile("apteva.yaml")
	if e != nil {
		t.Fatal(e)
	}
	disk, e := sdk.ParseManifest(raw)
	if e != nil {
		t.Fatal(e)
	}
	embedded := a.Manifest()
	if !reflect.DeepEqual(disk.Provides.UIComponents, embedded.Provides.UIComponents) || !reflect.DeepEqual(disk.Provides.HTTPRoutes, embedded.Provides.HTTPRoutes) {
		t.Fatalf("widget/routes manifest drift: widgets disk=%+v embed=%+v routes disk=%+v embed=%+v", disk.Provides.UIComponents, embedded.Provides.UIComponents, disk.Provides.HTTPRoutes, embedded.Provides.HTTPRoutes)
	}
	if len(disk.Provides.UIComponents) != 1 || disk.Provides.UIComponents[0].Entry != "/ui/AudioHealthWidget.mjs" {
		t.Fatal("missing widget")
	}
}

func TestAudioDashboardConcurrentIndexingAndCounterRestoration(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	audioDashboardTestSample(t, a, "concurrent", browserAudioDiagnostics{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= 25; i++ {
			if e := a.db().updateBrowserAudioDiagnostics("concurrent", browserAudioDiagnostics{PlaybackDroppedMS: i}); e != nil {
				t.Error(e)
				return
			}
		}
	}()
	for range 5 {
		_ = a.db().refreshAudioDashboard(context.Background())
	}
	wg.Wait()
	if e := a.db().refreshAudioDashboard(context.Background()); e != nil {
		t.Fatal(e)
	}
	v := dashboardTestResult(t, a, "")
	row := v["calls"].([]any)[0].(map[string]any)
	if row["metrics"].(map[string]any)["playback_dropped_ms"] != float64(25) {
		t.Fatal("lost concurrent diagnostic update", row)
	}
	// A new browser runtime may report zeros; per-call cumulative server totals
	// remain authoritative without adding overlapping observations twice.
	b := browserAudioDiagnostics{Server: &serverAudioDiagnostics{Socket: audioSocketSnapshot{BrowserTotals: map[string]float64{"playback_worklet_dropped_ms": 125, "playback_worklet_sequence_gaps": 8, "capture_worker_dropped_ms": 20}}}}
	raw, _ := json.Marshal(b)
	summary := summarizeAudioDashboard(&callRow{PlacedAt: time.Now().UTC().Format(time.RFC3339Nano), BrowserAudioDiagnostics: string(raw)})
	if summary.Metrics["playback_dropped_ms"] != 125 || summary.Metrics["playback_sequence_gaps"] != 8 || !containsString(summary.Stages, "browser_to_telephony") {
		t.Fatal(summary)
	}
}
func TestAudioDashboardUpgradeBackfillsExistingReports(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	for _, q := range []string{"DROP TRIGGER telephony_audio_insert", "DROP TRIGGER telephony_audio_update", "DROP TRIGGER telephony_audio_delete", "DROP TABLE telephony_audio_pending", "DROP TABLE telephony_audio_reports", "DROP TABLE telephony_audio_alert_history"} {
		if _, e := a.db().db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	audioDashboardTestSample(t, a, "existing", browserAudioDiagnostics{PlaybackDroppedMS: 125})
	before, _ := a.db().findCall("existing")
	applyMigrationFile(t, a.db().db, "migrations/040_audio_health_dashboard.sql")
	v := dashboardTestResult(t, a, "")
	if v["pending_reports"] != float64(1) {
		t.Fatal(v)
	}
	if e := a.db().refreshAudioDashboard(context.Background()); e != nil {
		t.Fatal(e)
	}
	v = dashboardTestResult(t, a, "?state=issues")
	if v["totals"].(map[string]any)["calls"] != float64(1) {
		t.Fatal(v)
	}
	after, _ := a.db().findCall("existing")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("upgrade changed existing call")
	}
	if _, e := a.db().db.Exec(`DELETE FROM calls WHERE id='existing'`); e != nil {
		t.Fatal(e)
	}
	v = dashboardTestResult(t, a, "")
	if v["totals"].(map[string]any)["calls"] != float64(0) {
		t.Fatal("orphan summary")
	}
}
func TestAudioDashboardWindowUnderHistoricalLoad(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	now := time.Now().UTC()
	summary := summarizeAudioDashboard(&callRow{PlacedAt: now.Format(time.RFC3339Nano)})
	raw, _ := json.Marshal(summary)
	if _, e := a.db().db.Exec(`WITH RECURSIVE ids(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM ids WHERE n<3641) INSERT INTO calls(id,thread_id,to_number,from_number,directive,voice,audio_bridge_url,status,placed_at,project_id,peer_kind) SELECT 'load-'||n,'load-thread-'||n,'+33123456789','+33187654321','','','','completed',?,'project-a','human' FROM ids`, now.Format(time.RFC3339Nano)); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db().db.Exec(`INSERT INTO telephony_audio_reports SELECT id,project_id,?,'twilio',0,'||','||',? FROM calls`, now.UnixMilli(), string(raw)); e != nil {
		t.Fatal(e)
	}
	f := audioDashboardFilter{From: now.Add(-time.Hour).UnixMilli(), Until: now.Add(time.Second).UnixMilli(), Limit: 50}
	start := time.Now()
	for range 5 {
		v, e := a.db().audioDashboard(context.Background(), "project-a", f, now)
		if e != nil {
			t.Fatal(e)
		}
		if v["totals"].(map[string]int64)["calls"] != 3641 || len(v["calls"].([]audioDashboardCall)) != 50 {
			t.Fatal("incomplete aggregate")
		}
	}
	t.Logf("3641 indexed reports: average complete aggregate + page + facets: %s", time.Since(start)/5)
}

func TestAudioDashboardNotificationsFollowCommitAndStayBounded(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	for i := range 125 {
		id := fmt.Sprintf("notify-%03d", i)
		audioDashboardTestSample(t, a, id, browserAudioDiagnostics{PlaybackDroppedMS: 40})
		if i < 10 {
			if _, err := a.db().db.Exec(`UPDATE calls SET project_id='project-b' WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	notifications := 0
	notify := func(changed map[string][]string) {
		notifications++
		size := 0
		for project, ids := range changed {
			for _, id := range ids {
				var actual string
				// The notification must run after commit and outside the transaction.
				if err := a.db().db.QueryRow(`SELECT project_id FROM telephony_audio_reports WHERE call_id=?`, id).Scan(&actual); err != nil {
					t.Fatal(err)
				}
				if actual != project {
					t.Fatalf("wrong notification project %s != %s", project, actual)
				}
			}
			size += len(ids)
		}
		if size == 0 || size > 100 {
			t.Fatalf("unbounded notification size %d", size)
		}
	}
	for range 2 {
		if err := a.db().refreshAudioDashboardAndNotify(context.Background(), notify); err != nil {
			t.Fatal(err)
		}
	}
	if notifications != 2 {
		t.Fatalf("notifications=%d", notifications)
	}
	if err := a.db().refreshAudioDashboardAndNotify(context.Background(), notify); err != nil {
		t.Fatal(err)
	}
	if notifications != 2 {
		t.Fatal("idle worker emitted a change")
	}
	audioDashboardTestSample(t, a, "rollback-notify", browserAudioDiagnostics{PlaybackDroppedMS: 80})
	if _, err := a.db().db.Exec(`DROP TABLE telephony_audio_reports`); err != nil {
		t.Fatal(err)
	}
	if err := a.db().refreshAudioDashboardAndNotify(context.Background(), notify); err == nil {
		t.Fatal("expected indexing failure")
	}
	if notifications != 2 {
		t.Fatal("failed transaction emitted a change")
	}
	var pending int
	if err := a.db().db.QueryRow(`SELECT count(*) FROM telephony_audio_pending WHERE call_id='rollback-notify'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatal("failed transaction lost work")
	}
}
