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
		{`SELECT c.id FROM ai_handoffs h INDEXED BY idx_ai_handoffs_ready CROSS JOIN calls c WHERE h.project_id=? AND h.status='ready' AND c.id=h.call_id`, "idx_ai_handoffs_ready", []any{"project-a"}},
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
