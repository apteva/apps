package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func dbChanges(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEfficiencyIdleWorkersDoNotRewriteHistory(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	seedEfficiencyArchive(t, ctx.AppDB(), 12941, time.Now().Add(-48*time.Hour))
	before := dbChanges(t, ctx.AppDB())
	for _, worker := range a.Workers() {
		start := dbChanges(t, ctx.AppDB())
		if err := worker.Run(context.Background(), ctx); err != nil {
			t.Fatalf("%s: %v", worker.Name, err)
		}
		t.Logf("%s changes=%d", worker.Name, dbChanges(t, ctx.AppDB())-start)
	}
	if changed := dbChanges(t, ctx.AppDB()) - before; changed != 0 {
		t.Fatalf("idle archive produced %d DB changes", changed)
	}
	// No ANALYZE prerequisite: actual shipped indexes must work on a freshly
	// upgraded installation, not only a hand-tuned performance fixture.
	checks := []struct {
		query, index string
		args         []any
	}{
		{`SELECT id FROM calls INDEXED BY idx_calls_pending_answer WHERE project_id=? AND direction='inbound' AND status='pending' AND handling_reason='' ORDER BY placed_at LIMIT 20`, "idx_calls_pending_answer", []any{"project-a"}},
		{`SELECT id FROM recordings INDEXED BY idx_recordings_import_candidates WHERE project_id=? AND deleted_at='' AND provider_status='completed' AND storage_status IN ('pending','failed','provider_only') ORDER BY created_at LIMIT 1`, "idx_recordings_import_candidates", []any{"project-a"}},
		{`SELECT id FROM calls INDEXED BY idx_calls_credentials_due WHERE project_id=? AND ended_at<>'' AND ended_at<? AND (callback_secret<>'' OR audio_bridge_url<>'') LIMIT 100`, "idx_calls_credentials_due", []any{"project-a", time.Now().Format(time.RFC3339)}},
		{`SELECT c.id FROM calls c INDEXED BY idx_calls_ai_activation_recovery CROSS JOIN ai_handoffs h WHERE c.project_id=? AND c.direction='inbound' AND c.carrier_slug='telnyx' AND c.peer_kind='realtime' AND c.status IN ('answering','answered','pending') AND h.call_id=c.id AND h.status='ready'`, "idx_calls_ai_activation_recovery", []any{"project-a"}},
	}
	for _, check := range checks {
		rows, err := ctx.AppDB().Query("EXPLAIN QUERY PLAN "+check.query, check.args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, aux int
			var detail string
			if err = rows.Scan(&id, &parent, &aux, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(plan, " "), check.index) {
			t.Fatalf("missing candidate plan %s: %v", check.index, plan)
		}
		t.Logf("%s: %v", check.index, plan)
	}
}

func TestEfficiencyHousekeepingBoundsAndPreservesRecordings(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	now := time.Now().UTC()
	seedEfficiencyArchive(t, ctx.AppDB(), 220, now.Add(-40*24*time.Hour))
	// All initial calls are recording-protected. Add unprotected calls behind
	// them to prove a protected head does not permanently starve later work.
	_, err := ctx.AppDB().Exec(`DELETE FROM recordings WHERE call_id IN (SELECT id FROM calls ORDER BY id DESC LIMIT 10)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ctx.AppDB().Exec(`UPDATE calls SET callback_secret='expired-secret',audio_bridge_url='expired-url'`)
	if err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 3; pass++ {
		if err = a.runHousekeepingAt(context.Background(), ctx, now); err != nil {
			t.Fatal(err)
		}
		var remaining int
		if err = ctx.AppDB().QueryRow(`SELECT count(*) FROM calls WHERE callback_secret<>'' OR audio_bridge_url<>''`).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		want := max(0, 220-(pass+1)*housekeepingBatch)
		if remaining != want {
			t.Fatalf("batch %d: credentials remaining %d want %d", pass, remaining, want)
		}
	}
	var count int
	if err = ctx.AppDB().QueryRow(`SELECT count(*) FROM calls`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 210 {
		t.Fatalf("retention removed protected call or starved eligible calls: %d", count)
	}
	before := dbChanges(t, ctx.AppDB())
	if err = a.runHousekeepingAt(context.Background(), ctx, now); err != nil {
		t.Fatal(err)
	}
	if dbChanges(t, ctx.AppDB()) != before {
		t.Fatal("protected history reconsidered before its next deadline")
	}
	// Removing a retained original permits deletion on the next bounded check.
	_, err = ctx.AppDB().Exec(`DELETE FROM recordings WHERE call_id='efficiency-1'`)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err = a.runHousekeepingAt(context.Background(), ctx, now.Add(25*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	row, err := a.db().findCall("efficiency-1")
	if err != nil || row != nil {
		t.Fatalf("released recording protection not reconsidered: %v %v", row, err)
	}
}
func TestEfficiencyHousekeepingCancellationAndProjects(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	seedEfficiencyArchive(t, ctx.AppDB(), 5, time.Now().Add(-40*24*time.Hour))
	_, err := ctx.AppDB().Exec(`UPDATE calls SET callback_secret='secret',project_id='other'`)
	if err != nil {
		t.Fatal(err)
	}
	before := dbChanges(t, ctx.AppDB())
	if err = a.runHousekeeping(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = a.runHousekeeping(canceled, ctx.WithProject("other")); err == nil {
		t.Fatal("ignored cancellation")
	}
	if dbChanges(t, ctx.AppDB()) != before {
		t.Fatal("changed another project or canceled work")
	}
}
func TestEfficiencyPolicyCacheFreshRevisionAndIsolation(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	policy := phoneTestPolicy(t, a)
	identity := phoneTestIdentity("alice")
	first, err := a.phonePrincipal("project-a", identity)
	if err != nil {
		t.Fatal(err)
	}
	first.Destinations["injected"] = true
	second, err := a.phonePrincipal("project-a", identity)
	if err != nil || second.Destinations["injected"] {
		t.Fatalf("mutable grants shared: %v %v", second, err)
	}
	for i := range policy.Users {
		policy.Users[i].Enabled = false
	}
	raw, _ := json.Marshal(policy)
	if _, err = a.db().db.Exec(`UPDATE telephony_access_policies SET revision=revision+1,policy_json=? WHERE project_id='project-a'`, string(raw)); err != nil {
		t.Fatal(err)
	}
	updated, err := a.phonePrincipal("project-a", identity)
	if err != errPhoneAccessDenied || updated != nil {
		t.Fatal("cached policy bypassed revocation")
	}
	other, err := a.phonePrincipal("other", identity)
	if err != errPhoneAccessDenied || other != nil {
		t.Fatalf("cross-project policy: %v %v", other, err)
	}
	if _, err = a.db().db.Exec(`UPDATE telephony_access_policies SET revision=revision+1,policy_json='invalid' WHERE project_id='project-a'`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.phonePrincipal("project-a", identity); err == nil {
		t.Fatal("malformed policy served from old cache")
	}
}

// Opt-in measured results include process CPU and allocations, not just wall
// time. No carrier calls, real credentials, or production data are involved.
func TestEfficiencyPerformance(t *testing.T) {
	if os.Getenv("TELEPHONY_EFFICIENCY_BENCHMARK") != "1" {
		t.Skip("opt-in archive and idle CPU comparison")
	}
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	seedEfficiencyArchive(t, ctx.AppDB(), 12941, time.Now().Add(-48*time.Hour))
	var results []queryPerformanceResult
	measure := func(name string, n int, run func() error) {
		t.Helper()
		var memBefore, memAfter runtime.MemStats
		runtime.ReadMemStats(&memBefore)
		cpu := queryProcessCPU()
		started := time.Now()
		for range n {
			if err := run(); err != nil {
				t.Fatal(err)
			}
		}
		elapsed := time.Since(started)
		cost := queryProcessCPU() - cpu
		runtime.ReadMemStats(&memAfter)
		results = append(results, queryPerformanceResult{Name: name, Iterations: n, WallMS: float64(elapsed) / float64(n) / 1e6, CPUMS: cost / float64(n), Bytes: (memAfter.TotalAlloc - memBefore.TotalAlloc) / uint64(n), Allocations: (memAfter.Mallocs - memBefore.Mallocs) / uint64(n)})
	}
	cutoff := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	measure("credentials_before", 30, func() error {
		_, err := ctx.AppDB().Exec(`UPDATE calls SET callback_secret='',audio_bridge_url='' WHERE project_id=? AND ended_at<>'' AND ended_at<?`, "project-a", cutoff)
		return err
	})
	measure("housekeeping_after", 100, func() error { return a.runHousekeeping(context.Background(), ctx) })
	for _, worker := range a.Workers() {
		measure("idle_"+worker.Name, 30, func() error { return worker.Run(context.Background(), ctx) })
	}
	policy := phoneTestPolicy(t, a)
	for i := 0; i < 100; i++ {
		u := policy.Users[0]
		u.Identity.SubjectID = fmt.Sprint("benchmark-", i)
		policy.Users = append(policy.Users, u)
	}
	policyJSON, _ := json.Marshal(policy)
	if _, err := ctx.AppDB().Exec(`UPDATE telephony_access_policies SET policy_json=? WHERE project_id='project-a'`, string(policyJSON)); err != nil {
		t.Fatal(err)
	}
	identity := phoneTestIdentity("alice")
	measure("principal_before", 300, func() error {
		policy, err := a.phonePolicy("project-a")
		if err != nil {
			return err
		}
		_, err = phonePrincipalFromPolicy("project-a", identity, policy)
		return err
	})
	measure("principal_after", 300, func() error { _, err := a.phonePrincipal("project-a", identity); return err })
	// A separate within-window WebSocket archive proves already-discovered
	// carrier recordings cannot dominate the reconciliation queue either.
	stamp := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	if _, err := ctx.AppDB().Exec(`UPDATE calls SET ingress_path='websocket',carrier_slug='twilio',carrier_sid=id,recording_mode='always',ended_at=?`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO recordings(id,call_id,project_id,provider,carrier_connection_id,provider_recording_id,provider_status,storage_status,provider_deleted_at,created_at) SELECT 'rec-'||id,id,project_id,'twilio',10,'original-'||id,'completed','stored',ended_at,ended_at FROM calls WHERE recording_known=0`); err != nil {
		t.Fatal(err)
	}
	measure("discovered_websocket_reconcile_before", 30, func() error {
		now := time.Now()
		_, err := a.db().listWhere(`project_id=? AND carrier_slug IN ('twilio','telnyx','plivo') AND ingress_path<>'sip_direct' AND recording_mode='always' AND status IN ('completed','failed') AND carrier_sid<>'' AND ended_at<>'' AND ended_at<=? AND ended_at>=? AND (recording_checked_at='' OR recording_checked_at<=?) AND NOT EXISTS(SELECT 1 FROM recordings r WHERE r.call_id=calls.id) ORDER BY ended_at LIMIT 1`, ctx.CurrentProject(), now.Add(-10*time.Second).UTC().Format(time.RFC3339), now.Add(-24*time.Hour).UTC().Format(time.RFC3339), now.Add(-5*time.Minute).UTC().Format(time.RFC3339))
		return err
	})
	measure("discovered_websocket_reconcile_after", 100, func() error { return a.reconcileProviderRecording(ctx) })

	if _, err := ctx.AppDB().Exec(`UPDATE recordings SET provider_deleted_at=''`); err != nil {
		t.Fatal(err)
	}
	measure("retained_provider_cleanup_before", 30, func() error {
		rows, err := ctx.AppDB().Query(`SELECT id FROM recordings WHERE project_id=? AND deleted_at='' AND storage_status='stored' AND provider_deleted_at='' AND cleanup_next_at<=? AND call_id IN (SELECT id FROM calls WHERE recording_storage_mode='copy_then_delete_provider') ORDER BY cleanup_next_at LIMIT 1`, ctx.CurrentProject(), time.Now().UTC().Format(time.RFC3339))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
		}
		return rows.Err()
	})
	measure("retained_provider_cleanup_after", 100, func() error { return a.cleanupRecordingJobs(ctx) })
	if _, err := ctx.AppDB().Exec(`UPDATE calls SET peer_kind='realtime',carrier_slug='telnyx',audio_bridge_url='wss://fixture.test/audio'`); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO ai_handoffs(call_id,project_id,status,max_attempts,deadline_at,next_attempt_at,directive,voice,greeting) SELECT id,project_id,'ready',3,placed_at,placed_at,'','','' FROM calls`); err != nil {
		t.Fatal(err)
	}
	measure("historical_ai_activation_before", 30, func() error {
		_, err := a.db().listWhere(`project_id=? AND direction='inbound' AND carrier_slug='telnyx' AND peer_kind='realtime' AND status IN ('answering','answered','pending') AND thread_id NOT LIKE 'pending-%' AND audio_bridge_url<>'pending' AND audio_bridge_url<>'' AND EXISTS(SELECT 1 FROM ai_handoffs h WHERE h.call_id=calls.id AND h.status='ready') AND NOT EXISTS(SELECT 1 FROM inbound_routes r WHERE r.id=calls.route_id AND r.inbound_transport='sip_direct') AND NOT EXISTS(SELECT 1 FROM carrier_activations x WHERE x.call_id=calls.id) LIMIT 100`, ctx.CurrentProject())
		return err
	})
	measure("historical_ai_activation_after", 100, func() error { return a.runCarrierActivations(context.Background(), ctx) })
	if _, err := ctx.AppDB().Exec(`INSERT INTO call_events(event_id,call_id,project_id,topic,revision,occurred_at,payload_json,created_at,published_at) SELECT 'event-'||id,id,project_id,'call.completed',1,ended_at,'{}',ended_at,ended_at FROM calls`); err != nil {
		t.Fatal(err)
	}
	measure("published_events_before", 100, func() error {
		rows, err := ctx.AppDB().Query(`SELECT event_id,call_id,project_id,topic,revision,occurred_at,payload_json FROM call_events WHERE published_at='' AND (?='' OR call_id=?) ORDER BY created_at,event_id LIMIT 100`, "", "")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
		}
		return rows.Err()
	})
	measure("published_events_after", 100, func() error { return a.publishLifecycleEvents(ctx, "") })
	raw, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(raw))
	if path := os.Getenv("TELEPHONY_EFFICIENCY_RESULTS"); path != "" {
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEfficiencyAIPolicyComparisonMatchesPersistedChanges(t *testing.T) {
	s := aiPolicyState{Stage: "active", Events: make([]aiPolicyEvent, 32)}
	before := aiPolicyScalars(s)
	s.event(time.Now(), "test", "changed")
	if before == aiPolicyScalars(s) {
		t.Fatal("bounded event roll lost dirty state")
	}
	r := &aiPolicyRuntime{thread: "thread", state: s}
	row := &callRow{ThreadID: "thread", PeerKind: peerKindRealtime, Status: "in-progress"}
	before = aiPolicyScalars(r.state)
	r.advance(row, time.Now())
	if before != aiPolicyScalars(r.state) {
		t.Fatal("idle prepared policy changed unexpectedly")
	}
}

func TestEfficiencySIPTimerExtensionAndExpiry(t *testing.T) {
	f := newSIPDialogFixture(t, false, 2*time.Second)
	f.answer(t)
	s := f.session
	s.answerMu.Lock()
	s.timer = sipSessionTimer{interval: 70 * time.Millisecond}
	s.timerUpdated = time.Now()
	signalSIPTimer(s.timerWake)
	s.answerMu.Unlock()
	time.Sleep(25 * time.Millisecond)
	s.answerMu.Lock()
	s.timer.interval = 180 * time.Millisecond
	s.timerUpdated = time.Now()
	signalSIPTimer(s.timerWake)
	s.answerMu.Unlock()
	time.Sleep(80 * time.Millisecond)
	if s.ended.Load() {
		t.Fatal("old timer expired a refreshed dialog")
	}
	// Expiry follows its deadline rather than the former one-second poll.
	started := time.Now()
	bye := readSIPResponseContaining(t, f.signaling, "BYE sip:")
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("deadline was delayed %s", elapsed)
	}
	f.respond(t, bye, "")
	f.waitEnded(t)
}

func TestEfficiencyAIPolicyFailedPersistenceIsRetried(t *testing.T) {
	a, ctx, _, _, r, base := aiPolicyFixture(t)
	if _, err := ctx.AppDB().Exec(`CREATE TRIGGER fail_policy_save BEFORE UPDATE ON ai_call_policies BEGIN SELECT RAISE(ABORT,'temporary test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := a.runAICallPoliciesAt(ctx, base); err == nil {
		t.Fatal("expected failed persistence")
	}
	r.mu.Lock()
	dirty := r.dirty
	r.mu.Unlock()
	if !dirty {
		t.Fatal("failed transition lost its retry")
	}
	if _, err := ctx.AppDB().Exec(`DROP TRIGGER fail_policy_save`); err != nil {
		t.Fatal(err)
	}
	if err := a.runAICallPoliciesAt(ctx, base); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := ctx.AppDB().QueryRow(`SELECT state_json FROM ai_call_policies WHERE call_id=?`, r.id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved aiPolicyState
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Stage != "active" || saved.StartedAt.IsZero() {
		t.Fatalf("transition wasn't persisted: %+v", saved)
	}
}

func TestEfficiencySIPPromptWaitsForCompletedPacerWork(t *testing.T) {
	receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	media := &sipRTPMedia{conn: sender, remote: receiver.LocalAddr().(*net.UDPAddr), offer: sipMediaOffer{PayloadType: 0, Codec: "PCMU"}}
	entered, release := make(chan struct{}), make(chan struct{})
	pacer := newSIPRTPPacerWithPolicy(ctx, media, &sipPlaybackState{}, bufferedCarrierPacerPolicy(), nil, func([]byte) { close(entered); <-release })
	defer func() { cancel(); <-pacer.done }()
	done := make(chan error, 1)
	go func() { done <- playSIPPCM(ctx, media, pacer, make([]int16, 480)) }()
	receiveSoon(t, entered)
	select {
	case err := <-done:
		close(release)
		t.Fatalf("prompt completed while pacer work was unfinished: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := receiveSoon(t, done); err != nil {
		t.Fatal(err)
	}
	if pacer.playback.pending.Load() != 0 {
		t.Fatal("completed prompt left pending work")
	}
}

func TestEfficiencyRoutingBackoffAllowsImmediateNewWork(t *testing.T) {
	now := time.Now()
	for _, raw := range []string{ringTime(now.Add(-time.Second)), "malformed"} {
		if due := boundedRoutingDeadline(raw, now); due != now.Add(time.Second) {
			t.Fatalf("unbounded retry: %s", due)
		}
	}
	future := now.Add(time.Minute)
	if due := boundedRoutingDeadline(ringTime(future), now); !due.Equal(future) {
		t.Fatal("future deadline changed")
	}
	var passes atomic.Int32
	ran := make(chan struct{}, 4)
	d := newRoutingDispatcher(func(string) time.Time {
		ran <- struct{}{}
		if passes.Add(1) == 1 {
			return boundedRoutingDeadline(ringTime(now), time.Now())
		}
		return time.Time{}
	})
	defer d.close()
	d.wake("project")
	receiveSoon(t, ran)
	select {
	case <-ran:
		t.Fatal("expired record spun during backoff")
	case <-time.After(150 * time.Millisecond):
	}
	d.wake("project")
	receiveSoon(t, ran)
}
func TestEfficiencyCallRetentionForeignKeysAreIndexed(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	_ = a
	for _, table := range []string{"call_offers", "routing_effects", "carrier_command_events", "telephony_listener_sessions", "telephony_listener_audit", "telephony_coaching_audit", "telephony_browser_network_events", "telephony_browser_transport_samples"} {
		rows, err := ctx.AppDB().Query(`EXPLAIN QUERY PLAN SELECT rowid FROM `+table+` WHERE call_id=?`, "deleted-call")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(detail, "SEARCH") {
				t.Fatalf("retention FK scans child history: %s %s", table, detail)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestEfficiencyDiscoveredWebSocketRecordingsLeaveReconciliationQueue(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	seedEfficiencyArchive(t, ctx.AppDB(), 12941, time.Now().Add(-2*time.Hour))
	if _, err := ctx.AppDB().Exec(`UPDATE calls SET ingress_path='websocket',carrier_slug='twilio',carrier_sid=id,recording_mode='always'`); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO recordings(id,call_id,project_id,provider,carrier_connection_id,provider_recording_id,provider_status,storage_status,provider_deleted_at,created_at)
 SELECT 'rec-'||id,id,project_id,'twilio',10,'original-'||id,'completed','stored',ended_at,ended_at FROM calls WHERE recording_known=0`); err != nil {
		t.Fatal(err)
	}
	var candidates int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM calls INDEXED BY idx_calls_recording_reconcile WHERE project_id='project-a' AND recording_known=0 AND carrier_slug IN ('twilio','telnyx','plivo') AND ingress_path<>'sip_direct' AND recording_mode='always' AND status IN ('completed','failed') AND carrier_sid<>'' AND ended_at<>''`).Scan(&candidates); err != nil {
		t.Fatal(err)
	}
	if candidates != 0 {
		t.Fatalf("discovered recordings revisited: %d", candidates)
	}
	before := dbChanges(t, ctx.AppDB())
	if err := a.reconcileProviderRecording(ctx); err != nil {
		t.Fatal(err)
	}
	if dbChanges(t, ctx.AppDB()) != before {
		t.Fatal("reconciled an already-discovered recording")
	}
	// Retained provider copies must not become a scan of stored history.
	if _, err := ctx.AppDB().Exec(`UPDATE recordings SET provider_deleted_at=''`); err != nil {
		t.Fatal(err)
	}
	before = dbChanges(t, ctx.AppDB())
	if err := a.cleanupRecordingJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if dbChanges(t, ctx.AppDB()) != before {
		t.Fatal("retained provider copy entered cleanup")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE calls SET recording_storage_mode='copy_then_delete_provider' WHERE id='efficiency-2'`); err != nil {
		t.Fatal(err)
	}
	var cleanup int
	if err := ctx.AppDB().QueryRow(`SELECT provider_cleanup_required FROM recordings WHERE call_id='efficiency-2'`).Scan(&cleanup); err != nil || cleanup != 1 {
		t.Fatalf("cleanup policy not tracked: %d %v", cleanup, err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE calls SET recording_storage_mode='copy' WHERE id='efficiency-2'`); err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT provider_cleanup_required FROM recordings WHERE call_id='efficiency-2'`).Scan(&cleanup); err != nil || cleanup != 0 {
		t.Fatalf("retained copy still eligible for deletion: %d %v", cleanup, err)
	}
	// Tombstones intentionally prevent redownloading deleted recordings, exactly
	// like the original NOT EXISTS check. Only removal of the last DB row makes
	// provider discovery eligible again.
	if _, err := ctx.AppDB().Exec(`UPDATE recordings SET deleted_at=? WHERE call_id='efficiency-1'`, ringTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	var known int
	if err := ctx.AppDB().QueryRow(`SELECT recording_known FROM calls WHERE id='efficiency-1'`).Scan(&known); err != nil || known != 1 {
		t.Fatalf("tombstone lost discovery state: %d %v", known, err)
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM recordings WHERE call_id='efficiency-1'`); err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT recording_known FROM calls WHERE id='efficiency-1'`).Scan(&known); err != nil || known != 0 {
		t.Fatalf("removed discovery not reconsidered: %d %v", known, err)
	}
}

func TestEfficiencyHistoricalAIAndPublishedEventsAreNotCandidates(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	t.Setenv("APTEVA_GATEWAY_URL", "")
	t.Setenv("APTEVA_APP_TOKEN", "")
	seedEfficiencyArchive(t, ctx.AppDB(), 12941, time.Now().Add(-48*time.Hour))
	if _, err := ctx.AppDB().Exec(`UPDATE calls SET peer_kind='realtime',audio_bridge_url='wss://fixture.test/audio'`); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO ai_handoffs(call_id,project_id,status,max_attempts,deadline_at,next_attempt_at,directive,voice,greeting) SELECT id,project_id,'ready',3,placed_at,placed_at,'','','' FROM calls`); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO call_events(event_id,call_id,project_id,topic,revision,occurred_at,payload_json,created_at,published_at) SELECT 'event-'||id,id,project_id,'call.completed',1,ended_at,'{}',ended_at,ended_at FROM calls`); err != nil {
		t.Fatal(err)
	}
	before := dbChanges(t, ctx.AppDB())
	if err := a.runCarrierActivations(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.publishLifecycleEvents(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if dbChanges(t, ctx.AppDB()) != before {
		t.Fatal("historical AI/event work was revisited")
	}
	other := testCall("other-event", "completed")
	other.ProjectID = "project-b"
	if err := a.db().insertCall(other); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO call_events(event_id,call_id,project_id,topic,revision,occurred_at,payload_json,created_at) VALUES('pending-b',?,'project-b','call.completed',1,?,'invalid',?)`, other.ID, other.PlacedAt, other.PlacedAt); err != nil {
		t.Fatal(err)
	}
	if err := a.publishLifecycleEvents(ctx, ""); err != nil {
		t.Fatalf("project A recovery read project B outbox: %v", err)
	}
	if err := a.publishLifecycleEvents(ctx, other.ID); err == nil {
		t.Fatal("explicit call outbox lookup changed its cross-project callback behavior")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.publishLifecycleEventsContext(canceled, ctx, ""); err == nil {
		t.Fatal("outbox ignored cancellation")
	}
}

func TestEfficiencyFirstExpiredRoutingDeadlineRunsImmediately(t *testing.T) {
	now := time.Now()
	raw := ringTime(now.Add(-time.Second))
	tracker := routingDeadlineTracker{}
	if due := tracker.next("p", raw, 1, now); due != now.Add(time.Millisecond) {
		t.Fatalf("newly expired work postponed: %s", due)
	}
	if due := tracker.next("p", raw, 1, now); due != now.Add(time.Second) {
		t.Fatalf("unchanged expired record did not back off: %s", due)
	}
	if due := tracker.next("p", raw, 2, now); due != now.Add(time.Millisecond) {
		t.Fatalf("new revision inherited backoff: %s", due)
	}
	tracker.next("p", "", 2, now)
	if due := tracker.next("p", raw, 2, now); due != now.Add(time.Millisecond) {
		t.Fatal("completed work retained stale backoff")
	}
}
