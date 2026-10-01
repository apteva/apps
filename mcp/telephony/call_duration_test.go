package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func durationRow(t *testing.T, a *App, id string) *callRow {
	t.Helper()
	r, err := a.db().findCall(id)
	if err != nil || r == nil {
		t.Fatalf("load: %v", err)
	}
	return r
}
func durationExec(t *testing.T, a *App, q string, args ...any) {
	t.Helper()
	if _, err := a.db().db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}
func TestCallDurationPolicyDefaultsAndConfiguration(t *testing.T) {
	base := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	var row callRow
	row.PlacedAt = base.Format(time.RFC3339)
	configureCallDuration(nil, &row, 0)
	if row.MaxDurationSec != 14400 || row.MediaRecoveryTimeoutSec != 120 || row.DeadlineAt != base.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("default policy: %+v", row)
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithConfig(map[string]string{"connected_call_max_duration_seconds": "10800", "call_setup_timeout_seconds": "600", "call_media_recovery_timeout_seconds": "240"}))
	configureCallDuration(ctx, &row, 0)
	if row.MaxDurationSec != 10800 || row.MediaRecoveryTimeoutSec != 240 || row.DeadlineAt != base.Add(10*time.Minute).Format(time.RFC3339) {
		t.Fatalf("configured policy: %+v", row)
	}
	configureCallDuration(ctx, &row, 7200)
	if row.MaxDurationSec != 7200 {
		t.Fatal("explicit AI override lost")
	}
	for _, v := range []string{"0", "-1", "garbage", "999999999999999999999999"} {
		if durationSetting(map[string]string{"x": v}, "x", 14400, 60, 86400) != 14400 {
			t.Fatal("unsafe config", v)
		}
	}
}

func TestCallDurationStartsOnEvidenceNotLocalAnswer(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	row := insertSoftphoneCall(t, a, "ringing")
	start := time.Now().UTC().Truncate(time.Second)
	if err := a.db().updateStatus(row.ID, "answered", ""); err != nil {
		t.Fatal(err)
	}
	if got := durationRow(t, a, row.ID); got.DurationStartedAt != "" || got.ConnectedDeadlineAt != "" {
		t.Fatal("local answer started duration clock")
	}
	if _, err := a.db().updateStatusWithFacts(row.ID, "answered", "", lifecycleFacts{Source: "provider", OccurredAt: start.Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	first := durationRow(t, a, row.ID)
	deadline, err := time.Parse(time.RFC3339Nano, first.ConnectedDeadlineAt)
	if err != nil || !deadline.Equal(start.Add(4*time.Hour)) {
		t.Fatalf("deadline=%s %v", first.ConnectedDeadlineAt, err)
	}
	// Reconnect, transfer/local claim and late duplicate provider events must not
	// create another four-hour window. A new App simulates loss of process state.
	a = &App{installID: 42}
	for i := 0; i < 3; i++ {
		if err := a.db().updateMediaStatus(row.ID, "connected", "", 0, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := a.db().updateStatusWithFacts(row.ID, "in-progress", "", lifecycleFacts{Source: "provider", OccurredAt: start.Add(time.Hour).Format(time.RFC3339)}); err != nil {
			t.Fatal(err)
		}
	}
	next := durationRow(t, a, row.ID)
	if next.ConnectedDeadlineAt != first.ConnectedDeadlineAt || next.DurationStartedAt != first.DurationStartedAt {
		t.Fatal("reconnect/restart extended duration")
	}
}

func TestCallDurationLongCallExpiresAsCompletedExactlyOnce(t *testing.T) {
	a, ctx, platform, _, row, _ := reliabilityFixture(t)
	start := time.Now().UTC().Truncate(time.Second)
	durationExec(t, a, `UPDATE calls SET status='answering',placed_at=?,deadline_at=?,state_expires_at='' WHERE id=?`, start.Add(-10*time.Minute).Format(time.RFC3339), start.Add(50*time.Minute).Format(time.RFC3339), row.ID)
	if _, err := a.db().updateStatusWithFacts(row.ID, "answered", "", lifecycleFacts{Source: "provider", OccurredAt: start.Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if err := a.db().updateMediaStatus(row.ID, "connected", "", 0, ""); err != nil {
		t.Fatal(err)
	}
	row = durationRow(t, a, row.ID)
	for _, elapsed := range []time.Duration{61 * time.Minute, 3 * time.Hour, 4*time.Hour - time.Millisecond} {
		if err := a.expireCallAt(ctx, row, start.Add(elapsed)); err != nil {
			t.Fatal(err)
		}
		if len(platform.integrationCalls) != 0 || isTerminalStatus(durationRow(t, a, row.ID).Status) {
			t.Fatalf("healthy long call ended at %s", elapsed)
		}
	}
	if err := a.expireCallAt(ctx, row, start.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	ended := durationRow(t, a, row.ID)
	if ended.Status != "completed" || ended.TerminationReason != "time_limit" || ended.ErrorMessage != "" || callbackEligible(*ended) {
		t.Fatalf("wrong expiry classification: %+v", ended)
	}
	if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != "hangup_call" {
		t.Fatalf("termination commands: %+v", platform.integrationCalls)
	}
	// A normal carrier hangup or failure arriving later cannot turn this back
	// into a technical failure, change its reason, or duplicate completion.
	for _, status := range []string{"completed", "failed", "answered"} {
		_, err := a.db().updateStatusWithFacts(row.ID, status, "carrier reports failure", lifecycleFacts{Source: "provider", TerminationCause: "normal_clearing", OccurredAt: start.Add(4*time.Hour + time.Second).Format(time.RFC3339)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := a.expireCallAt(ctx, row, start.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	ended = durationRow(t, a, row.ID)
	if ended.Status != "completed" || ended.TerminationReason != "time_limit" || ended.ErrorMessage != "" || len(platform.integrationCalls) != 1 {
		t.Fatal("late callback changed expiry")
	}
	var events int
	if err := a.db().db.QueryRow(`SELECT COUNT(*) FROM call_events WHERE call_id=? AND topic='call.completed'`, row.ID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("completion count %d: %v", events, err)
	}
	public := callsPanelPublic([]callRow{*ended}, true)[0]
	if public["termination"].(map[string]any)["reason"] != "time_limit" || public["max_duration_sec"] != 14400 {
		t.Fatalf("public policy/reason: %+v", public)
	}
}

func TestCallDurationMediaRecoveryIsSeparateAndDoesNotReset(t *testing.T) {
	a, ctx, platform, _, row, _ := reliabilityFixture(t)
	durationExec(t, a, `UPDATE calls SET status='answered',state_expires_at='',media_recovery_timeout_sec=240 WHERE id=?`, row.ID)
	if err := a.db().updateMediaStatus(row.ID, "connected", "", 0, ""); err != nil {
		t.Fatal(err)
	}
	connected := durationRow(t, a, row.ID)
	// Hold/mute/silence have no transport failure, so a healthy held call has
	// no media deadline even after an hour.
	durationExec(t, a, `UPDATE calls SET hold_state='held' WHERE id=?`, row.ID)
	if err := a.expireCallAt(ctx, connected, time.Now().Add(61*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 0 {
		t.Fatal("held call treated as stuck")
	}
	if err := a.db().updateMediaStatus(row.ID, "disconnected", "", 1000, "transport closed"); err != nil {
		t.Fatal(err)
	}
	broken := durationRow(t, a, row.ID)
	expiry, err := time.Parse(time.RFC3339Nano, broken.MediaDeadlineAt)
	if err != nil || time.Until(expiry) < 235*time.Second {
		t.Fatalf("configured recovery budget: %s %v", broken.MediaDeadlineAt, err)
	}
	if err := a.db().updateMediaStatus(row.ID, "error", "transport failed", 1011, "failed"); err != nil {
		t.Fatal(err)
	}
	if durationRow(t, a, row.ID).MediaDeadlineAt != broken.MediaDeadlineAt {
		t.Fatal("repeat errors extended recovery")
	}
	if err := a.db().updateMediaStatus(row.ID, "connected", "", 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := a.expireCallAt(ctx, broken, expiry.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	recovered := durationRow(t, a, row.ID)
	if len(platform.integrationCalls) != 0 || recovered.MediaDeadlineAt != "" || recovered.ConnectedDeadlineAt != connected.ConnectedDeadlineAt {
		t.Fatal("stale expiry killed recovered call or reset duration")
	}
	if err := a.db().updateMediaStatus(row.ID, "error", "stuck transport", 1011, "failed"); err != nil {
		t.Fatal(err)
	}
	broken = durationRow(t, a, row.ID)
	expiry, _ = time.Parse(time.RFC3339Nano, broken.MediaDeadlineAt)
	if err := a.expireCallAt(ctx, broken, expiry.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := durationRow(t, a, row.ID); got.Status != "failed" || got.TerminationCause != "media_timeout" || got.TerminationReason == "time_limit" {
		t.Fatalf("watchdog misclassified: %+v", got)
	}
}

func TestCallDurationCancelledCallCannotBeRevived(t *testing.T) {
	a, ctx, platform, _, row, _ := reliabilityFixture(t)
	if err := a.db().updateStatus(row.ID, "canceled", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.db().updateMediaStatus(row.ID, "connected", "", 0, ""); err != nil {
		t.Fatal(err)
	}
	_, err := a.db().updateStatusWithFacts(row.ID, "answered", "", lifecycleFacts{Source: "provider"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.expireCallAt(ctx, row, time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := durationRow(t, a, row.ID)
	if got.Status != "canceled" || got.DurationStartedAt != "" || len(platform.integrationCalls) > 0 {
		t.Fatal("late completion revived canceled call")
	}
}

type durationCarrier struct{ request carrierPlaceRequest }

func (c *durationCarrier) Slug() string { return "twilio" }
func (c *durationCarrier) Place(_ *sdk.AppCtx, r carrierPlaceRequest) (*carrierPlaceResult, error) {
	c.request = r
	return &carrierPlaceResult{CarrierSID: "duration-carrier"}, nil
}
func (c *durationCarrier) Hangup(_ *sdk.AppCtx, _ *callRow) error { return nil }
func TestCallDurationOutboundUsesSamePersistedAndCarrierPolicy(t *testing.T) {
	ctx := softphoneTestCtx(t)
	a := &App{installID: 42}
	for _, peer := range []string{peerKindHuman, peerKindRealtime, peerKindExternal} {
		row := callRow{ID: peer, ThreadID: peer, Direction: "outbound", CarrierSlug: "twilio", Status: "initiated", PlacedAt: time.Now().UTC().Format(time.RFC3339), ProjectID: "project-a", PeerKind: peer}
		carrier := &durationCarrier{}
		if err := a.placeOutboundLeg(ctx, carrier, &row, 30, 10800, nil); err != nil {
			t.Fatal(err)
		}
		stored := durationRow(t, a, row.ID)
		if stored.MaxDurationSec != 10800 || carrier.request.MaxDurationSec != 10800 || stored.DurationStartedAt != "" {
			t.Fatalf("%s policy mismatch: %+v %+v", peer, stored, carrier.request)
		}
	}
}

func TestCallDurationUpgradePreservesEvidenceAndExplicitLimits(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	files, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Name() < "036" {
			applyMigrationFile(t, db, filepath.Join("migrations", f.Name()))
		}
	}
	for _, id := range []string{"local", "carrier", "media", "override", "outbound_early", "outbound_answered"} {
		_, err = db.Exec(`INSERT INTO calls(id,thread_id,to_number,from_number,directive,voice,audio_bridge_url,status,placed_at,project_id,answered_at,deadline_at) VALUES(?,?,'+33111111111','+33611111111','','','bridge','answered','2026-09-29T05:00:00Z','project-a','2026-09-29T05:01:00Z','2026-09-29T06:00:00Z')`, id, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{`UPDATE calls SET carrier_answered_at='2026-09-29T05:01:00Z' WHERE id IN ('carrier','override','outbound_answered')`, `UPDATE calls SET direction='inbound', media_connected_at='2026-09-29T05:02:00Z' WHERE id='media'`, `UPDATE calls SET direction='outbound', media_connected_at='2026-09-29T05:00:30Z' WHERE id IN ('outbound_early','outbound_answered')`, `UPDATE calls SET deadline_at='2026-09-29T07:00:00Z' WHERE id='override'`} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	applyMigrationFile(t, db, "migrations/036_call_duration.sql")
	for id, want := range map[string]string{"local": "", "carrier": "2026-09-29T06:01:00.000Z", "media": "2026-09-29T06:02:00.000Z", "override": "2026-09-29T07:01:00.000Z", "outbound_early": "", "outbound_answered": "2026-09-29T06:01:00.000Z"} {
		var deadline string
		if err = db.QueryRow(`SELECT connected_deadline_at FROM calls WHERE id=?`, id).Scan(&deadline); err != nil || deadline != want {
			t.Fatalf("%s: %s != %s (%v)", id, deadline, want, err)
		}
	}
}

func TestCallDurationTerminationPayloadRemainsMachineReadable(t *testing.T) {
	raw, err := json.Marshal(terminationPublic(callRow{TerminationReason: terminationTimeLimit, TerminationCause: "max_duration", TerminationInitiator: "telephony"}))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]string
	if err = json.Unmarshal(raw, &payload); err != nil || payload["reason"] != "time_limit" {
		t.Fatal(string(raw), err)
	}
}

func TestCallDurationOutboundEarlyMediaIsNotCarrierAnswer(t *testing.T) {
	softphoneTestCtx(t)
	a := &App{installID: 42}
	r := insertSoftphoneCall(t, a, "ringing")
	// Telnyx may open its media stream before originating the PSTN leg.
	if err := a.db().updateMediaStatus(r.ID, "connected", "", 0, ""); err != nil {
		t.Fatal(err)
	}
	if got := durationRow(t, a, r.ID); got.DurationStartedAt != "" {
		t.Fatal("pre-answer stream started call-duration clock")
	}
	answer := time.Now().Add(30 * time.Second).UTC().Truncate(time.Second)
	if _, err := a.db().updateStatusWithFacts(r.ID, "answered", "", lifecycleFacts{Source: "provider", OccurredAt: answer.Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	got := durationRow(t, a, r.ID)
	if got.DurationStartedAt != answer.Format(time.RFC3339) {
		t.Fatalf("clock starts at %s, expected carrier answer %s", got.DurationStartedAt, answer)
	}
}

func TestCallDurationCarrierAdaptersReceiveConfiguredLimit(t *testing.T) {
	for _, tc := range []struct {
		slug, tool, key, response string
		fields                    map[string]string
	}{
		{"telnyx", "dial_call", "time_limit_secs", `{"data":{"call_control_id":"c1"}}`, map[string]string{"connection_id": "app"}},
		{"twilio", "make_call", "TimeLimit", `{"sid":"c1"}`, nil},
		{"plivo", "make_call", "time_limit", `{"request_uuid":"c1"}`, nil},
		{"sinch", "create_call", "maxCallDurationSeconds", `{"sessionId":"c1","serviceId":"svc"}`, map[string]string{"service_id": "svc", "service_secret": "MDEyMzQ1Njc4OWFiY2RlZg=="}},
	} {
		t.Run(tc.slug, func(t *testing.T) {
			platform := &answerPlatform{integrationResponse: map[string]json.RawMessage{tc.tool: json.RawMessage(tc.response)}}
			a, ctx := withTelephonyTestContext(t, platform)
			c, err := a.carrierForSlug(tc.slug, 9, tc.fields)
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Place(ctx, carrierPlaceRequest{CallID: "duration", To: "+33111111111", From: "+33611111111", TimeoutSec: 30, MaxDurationSec: 10800})
			if err != nil {
				t.Fatal(err)
			}
			if len(platform.integrationCalls) != 1 {
				t.Fatal(platform.integrationCalls)
			}
			input := platform.integrationCalls[0].Input
			if tc.slug == "sinch" {
				input = input["commands"].([]any)[0].(map[string]any)
			}
			if input[tc.key] != 10800 {
				t.Fatalf("duration not forwarded: %+v", platform.integrationCalls)
			}
		})
	}
}

func TestCallDurationWorkerUsesConnectedDeadlineInsteadOfOldSetup(t *testing.T) {
	a, ctx, platform, _, row, _ := reliabilityFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	durationExec(t, a, `UPDATE calls SET status='answering',placed_at=?,deadline_at=?,state_expires_at='' WHERE id=?`, now.Add(-90*time.Minute).Format(time.RFC3339), now.Add(-30*time.Minute).Format(time.RFC3339), row.ID)
	if _, err := a.db().updateStatusWithFacts(row.ID, "answered", "", lifecycleFacts{Source: "provider", OccurredAt: now.Add(-70 * time.Minute).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if err := a.db().updateMediaStatus(row.ID, "connected", "", 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := a.runLifecycleTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 0 {
		t.Fatal("old setup deadline killed healthy connected call")
	}
	durationExec(t, a, `UPDATE calls SET connected_deadline_at=? WHERE id=?`, now.Add(-time.Second).Format(time.RFC3339), row.ID)
	if err := a.runLifecycleTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if got := durationRow(t, a, row.ID); got.Status != "completed" || got.TerminationReason != "time_limit" {
		t.Fatalf("worker did not enforce connected deadline: %+v", got)
	}
}

func TestCallDurationSetupDeadlineStillExpiresUnconnectedCall(t *testing.T) {
	a, ctx, platform, _, row, _ := reliabilityFixture(t)
	when := time.Now().UTC().Add(-time.Second)
	durationExec(t, a, `UPDATE calls SET deadline_at=?,state_expires_at='' WHERE id=?`, when.Format(time.RFC3339), row.ID)
	if err := a.runLifecycleTick(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	got := durationRow(t, a, row.ID)
	if got.Status != "no-answer" || got.TerminationCause != "setup_timeout" || len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != "reject_call" {
		t.Fatalf("setup watchdog disabled: %+v", got)
	}
}

func TestCallDurationTransferredExternalLegKeepsParentClock(t *testing.T) {
	ctx := softphoneTestCtx(t)
	a := &App{installID: 42}
	carrier := &durationCarrier{}
	started := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	row := callRow{ID: "child", ThreadID: "child", Direction: "outbound", CarrierSlug: "twilio", Status: "initiated", PlacedAt: time.Now().UTC().Format(time.RFC3339), ProjectID: "project-a", PeerKind: peerKindExternal, MaxDurationSec: 14400, MediaRecoveryTimeoutSec: 120, DurationStartedAt: started.Format(time.RFC3339), ConnectedDeadlineAt: started.Add(4 * time.Hour).Format(time.RFC3339)}
	if err := a.placeOutboundLeg(ctx, carrier, &row, 30, 14400, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db().updateStatusWithFacts(row.ID, "answered", "", lifecycleFacts{Source: "provider"}); err != nil {
		t.Fatal(err)
	}
	if got := durationRow(t, a, row.ID); got.ConnectedDeadlineAt != row.ConnectedDeadlineAt {
		t.Fatal("new external leg extended parent deadline")
	}
}

func TestCallDurationInboundSnapshotsSettingsAndDuplicateIngress(t *testing.T) {
	a, ctx, _, route, _, _ := reliabilityFixture(t)
	ctx.Config()["connected_call_max_duration_seconds"] = "7200"
	ctx.Config()["call_media_recovery_timeout_seconds"] = "240"
	row, _, err := a.recordInboundCall(route, "duration-snapshot", "+33611111112", route.PhoneNumber)
	if err != nil {
		t.Fatal(err)
	}
	if row.MaxDurationSec != 7200 || row.MediaRecoveryTimeoutSec != 240 || row.ConnectedDeadlineAt != "" {
		t.Fatalf("inbound policy not snapshotted: %+v", row)
	}
	ctx.Config()["connected_call_max_duration_seconds"] = "14400"
	again, _, err := a.recordInboundCall(route, "duration-snapshot", "+33611111112", route.PhoneNumber)
	if err != nil || again.ID != row.ID || again.MaxDurationSec != 7200 {
		t.Fatalf("duplicate ingress changed policy: %+v %v", again, err)
	}
}

func TestCallDurationConcurrentExpiryDoesNotDuplicateHangup(t *testing.T) {
	a, ctx, platform, _, row, _ := reliabilityFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	durationExec(t, a, `UPDATE calls SET status='answering',state_expires_at='' WHERE id=?`, row.ID)
	if _, err := a.db().updateStatusWithFacts(row.ID, "answered", "", lifecycleFacts{Source: "provider", OccurredAt: now.Add(-4 * time.Hour).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- a.expireCallAt(ctx, row, now) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(platform.integrationCalls) != 1 || durationRow(t, a, row.ID).TerminationReason != "time_limit" {
		t.Fatalf("duplicate expiry: %+v", platform.integrationCalls)
	}
}

func TestCallDurationFailedHangupKeepsDurableIntentForRetry(t *testing.T) {
	a, ctx, platform, _, row, _ := reliabilityFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	durationExec(t, a, `UPDATE calls SET status='answering',state_expires_at='' WHERE id=?`, row.ID)
	if _, err := a.db().updateStatusWithFacts(row.ID, "answered", "", lifecycleFacts{Source: "provider", OccurredAt: now.Add(-4 * time.Hour).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	platform.failCarrier = true
	if err := a.expireCallAt(ctx, row, now); err == nil {
		t.Fatal("carrier failure hidden")
	}
	got := durationRow(t, a, row.ID)
	if isTerminalStatus(got.Status) || got.TerminationReason != "time_limit" {
		t.Fatal("failed remote hangup marked completed or lost intent")
	}
	platform.failCarrier = false
	// Simulate a restart: no in-memory expiry state is needed for the retry.
	a = &App{installID: a.installID}
	if err := a.expireCallAt(ctx, row, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got = durationRow(t, a, row.ID)
	if got.Status != "completed" || got.TerminationReason != "time_limit" || len(platform.integrationCalls) != 2 {
		t.Fatalf("failed duration hangup did not recover: %+v", got)
	}
}
