package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// newTestAppCfg is newTestApp with extra config keys — several hardening
// behaviours (rate limit budget, retention window) are config-driven.
func newTestAppCfg(t *testing.T, crmBound, msgBound bool, extra map[string]string) (*App, *sdk.AppCtx, *fakeStreaming, *fakeCRM, *fakeMessaging) {
	t.Helper()
	cfg := map[string]string{
		"reminder_lead_hours":  "24,1,0.25",
		"viewer_idle_seconds":  "30",
		"default_sender_email": "host@example.com",
	}
	for k, v := range extra {
		cfg[k] = v
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(cfg),
	)
	streaming := newFakeStreaming()
	crm := newFakeCRM(crmBound)
	messaging := newFakeMessaging(msgBound)
	app := &App{
		streamingCaller: streaming,
		crmCaller:       crm,
		messagingCaller: messaging,
	}
	globalCtx = ctx
	globalApp = app
	return app, ctx, streaming, crm, messaging
}

func mustCreate(t *testing.T, app *App, ctx *sdk.AppCtx, args map[string]any) *Webinar {
	t.Helper()
	out, err := app.toolCreate(ctx, args)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return out.(map[string]any)["webinar"].(*Webinar)
}

func mustRegister(t *testing.T, app *App, ctx *sdk.AppCtx, args map[string]any) *Registrant {
	t.Helper()
	out, err := app.toolRegister(ctx, args)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return out.(map[string]any)["registrant"].(*Registrant)
}

func countRow(t *testing.T, ctx *sdk.AppCtx, query string, args ...any) int {
	t.Helper()
	var n int
	if err := ctx.AppDB().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count query %q: %v", query, err)
	}
	return n
}

func futureRFC3339(d time.Duration) string { return formatRFC3339(nowUTC().Add(d)) }

// ─── 1. Scripted offers actually fire ─────────────────────────────

// The regression: handleStreamStarted wrote started_at with SQL
// CURRENT_TIMESTAMP, and the offer broadcaster parsed it with
// time.Parse(time.RFC3339, …). That parse always failed, so every live
// webinar was skipped on every tick and no scripted offer ever showed.
// The old test suite hand-wrote an RFC3339 started_at, which hid it.
func TestOfferBroadcaster_FiresAfterStreamStarted(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{
		"title":        "Scripted Offers",
		"scheduled_at": futureRFC3339(time.Hour),
	})
	if _, err := app.toolDefineOffer(ctx, map[string]any{
		"id":             w.ID,
		"offset_seconds": 0,
		"headline":       "Founding member pricing",
		"cta_label":      "Claim it",
		"cta_url":        "https://example.com/offer",
	}); err != nil {
		t.Fatalf("define offer: %v", err)
	}

	// Exactly the production path: the stream lifecycle event writes
	// started_at, then the broadcaster reads it back.
	if err := app.handleStreamStarted(ctx, sdk.Event{
		Topic:     "stream.started",
		ProjectID: "test-proj",
		Data:      map[string]any{"id": float64(w.StreamID)},
	}); err != nil {
		t.Fatalf("stream.started: %v", err)
	}
	var startedAt string
	_ = ctx.AppDB().QueryRow(`SELECT started_at FROM webinars WHERE id = ?`, w.ID).Scan(&startedAt)
	if _, err := time.Parse(time.RFC3339, startedAt); err != nil {
		t.Fatalf("started_at=%q is not RFC3339: %v", startedAt, err)
	}

	if err := app.runOfferBroadcaster(context.Background(), ctx); err != nil {
		t.Fatalf("offer broadcaster: %v", err)
	}
	var shownAt string
	var seq int
	if err := ctx.AppDB().QueryRow(
		`SELECT COALESCE(shown_at,''), sequence FROM webinar_offers WHERE webinar_id = ?`,
		w.ID).Scan(&shownAt, &seq); err != nil {
		t.Fatalf("read offer: %v", err)
	}
	if shownAt == "" {
		t.Fatal("scripted offer never fired — shown_at is still NULL")
	}
	if seq < 1 {
		t.Errorf("offer sequence=%d, want >= 1", seq)
	}
}

// Legacy rows written before migration 003 must keep working.
func TestOfferBroadcaster_ToleratesLegacySQLiteTimestamp(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Legacy"})
	if _, err := app.toolDefineOffer(ctx, map[string]any{
		"id": w.ID, "offset_seconds": 0, "headline": "H",
		"cta_label": "Go", "cta_url": "https://example.com",
	}); err != nil {
		t.Fatal(err)
	}
	legacy := nowUTC().Add(-time.Minute).Format("2006-01-02 15:04:05")
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinars SET status='live', started_at = ? WHERE id = ?`, legacy, w.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.runOfferBroadcaster(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if n := countRow(t, ctx,
		`SELECT COUNT(*) FROM webinar_offers WHERE webinar_id = ? AND shown_at IS NOT NULL`, w.ID); n != 1 {
		t.Errorf("legacy started_at layout should still broadcast; shown offers=%d", n)
	}
}

// ─── 2. Duplicate registration must not corrupt another row ───────

func TestRegister_DuplicateEmailOnWarmConnection(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, true, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{
		"title":        "Dupes",
		"scheduled_at": futureRFC3339(48 * time.Hour),
	})

	first := mustRegister(t, app, ctx, map[string]any{
		"webinar_id": w.ID, "email": "first@example.com", "display_name": "First",
	})
	second := mustRegister(t, app, ctx, map[string]any{
		"webinar_id": w.ID, "email": "second@example.com", "display_name": "Second",
	})

	// Warm the connection with inserts into OTHER tables — this is what
	// made LastInsertId() hand back a foreign rowid after ON CONFLICT DO
	// NOTHING.
	if _, _, err := app.InsertChatMessage(ctx, w, first, "First", "hello", "message"); err != nil {
		t.Fatal(err)
	}

	again := mustRegister(t, app, ctx, map[string]any{
		"webinar_id": w.ID, "email": "second@example.com", "display_name": "Second",
	})
	if again.ID != second.ID {
		t.Fatalf("duplicate submit returned registrant %d, want the existing %d", again.ID, second.ID)
	}
	if n := countRow(t, ctx, `SELECT COUNT(*) FROM webinar_registrants WHERE webinar_id = ?`, w.ID); n != 2 {
		t.Errorf("registrants=%d, want 2", n)
	}

	// The unrelated registrant must be untouched — the old code wrote
	// contact_id onto whatever row LastInsertId happened to name.
	reload, err := app.dbGetRegistrant(ctx, "test-proj", first.ID)
	if err != nil || reload == nil {
		t.Fatalf("reload first registrant: %v", err)
	}
	if reload.Email != "first@example.com" || reload.JoinToken != first.JoinToken {
		t.Errorf("first registrant was mutated: %+v", reload)
	}
	if first.ContactID == nil || reload.ContactID == nil || *reload.ContactID != *first.ContactID {
		t.Errorf("first registrant's contact_id changed: %v -> %v", first.ContactID, reload.ContactID)
	}
}

// ─── 3. Phone-only idempotency + no double reminders ──────────────

func TestRegister_PhoneOnlyIsIdempotent(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{
		"title":        "SMS funnel",
		"scheduled_at": futureRFC3339(48 * time.Hour),
	})

	a := mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "phone": "+15551234567"})
	b := mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "phone": "+1 (555) 123-4567"})
	if a.ID != b.ID {
		t.Fatalf("phone-only double submit created a second registrant (%d then %d)", a.ID, b.ID)
	}
	if n := countRow(t, ctx, `SELECT COUNT(*) FROM webinar_registrants WHERE webinar_id = ?`, w.ID); n != 1 {
		t.Errorf("registrants=%d, want 1", n)
	}
	// 3 leads × sms only, and NOT six.
	if n := countRow(t, ctx,
		`SELECT COUNT(*) FROM webinar_reminders WHERE webinar_id = ? AND status='pending'`, w.ID); n != 3 {
		t.Errorf("pending reminders=%d, want 3 (a repeat submit must not double-schedule)", n)
	}
}

func TestRegister_RepeatEmailDoesNotDoubleScheduleReminders(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{
		"title":        "Repeat",
		"scheduled_at": futureRFC3339(48 * time.Hour),
	})
	for i := 0; i < 3; i++ {
		mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "email": "dupe@example.com"})
	}
	if n := countRow(t, ctx,
		`SELECT COUNT(*) FROM webinar_reminders WHERE webinar_id = ? AND status='pending'`, w.ID); n != 3 {
		t.Errorf("pending reminders=%d, want 3", n)
	}
}

// ─── 4. Idempotency keys distinguish distinct sends ───────────────

func TestReminderIdempotencyKey_IncludesScheduledTime(t *testing.T) {
	app, ctx, _, _, msg := newTestAppCfg(t, false, true, nil)
	w := mustCreate(t, app, ctx, map[string]any{
		"title":        "Reschedule me",
		"scheduled_at": futureRFC3339(26 * time.Hour),
	})
	mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "email": "a@example.com"})

	// Make the T-24h row due and dispatch it.
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinar_reminders SET scheduled_for = ? WHERE lead_label = 'T-24h'`,
		futureRFC3339(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := app.runReminderScheduler(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(msg.sent) != 1 {
		t.Fatalf("expected 1 dispatched reminder, got %d", len(msg.sent))
	}
	firstKey := msg.sent[0].IdempotencyKey

	// Reschedule a week out and dispatch the regenerated T-24h row.
	if _, err := app.toolUpdate(ctx, map[string]any{
		"id":    w.ID,
		"patch": map[string]any{"scheduled_at": futureRFC3339(7 * 24 * time.Hour)},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	// A different scheduled_for than the already-sent row: that column is
	// part of the reminder's natural key now.
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinar_reminders SET scheduled_for = ?
		 WHERE lead_label = 'T-24h' AND status = 'pending'`,
		futureRFC3339(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := app.runReminderScheduler(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(msg.sent) < 2 {
		t.Fatalf("rescheduled reminder was never dispatched (sends=%d)", len(msg.sent))
	}
	secondKey := msg.sent[len(msg.sent)-1].IdempotencyKey
	if firstKey == secondKey {
		t.Errorf("rescheduled reminder reused idempotency key %q — messaging would swallow it", firstKey)
	}
}

func TestManualSend_DistinctKeysPerInvocation(t *testing.T) {
	app, ctx, _, _, msg := newTestAppCfg(t, false, true, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Blast"})
	mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "email": "a@example.com"})

	for i := 0; i < 2; i++ {
		if _, err := app.toolSendReminder(ctx, map[string]any{"id": w.ID}); err != nil {
			t.Fatalf("send reminder %d: %v", i, err)
		}
	}
	if len(msg.sent) != 2 {
		t.Fatalf("expected 2 sends, got %d", len(msg.sent))
	}
	if msg.sent[0].IdempotencyKey == msg.sent[1].IdempotencyKey {
		t.Errorf("repeat manual sends shared idempotency key %q", msg.sent[0].IdempotencyKey)
	}
}

// ─── 5. Reschedule moves the slot, and per-slot reminders ─────────

func TestUpdate_ReschedulesMaterializedSlot(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	original := futureRFC3339(48 * time.Hour)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Move me", "scheduled_at": original})

	moved := futureRFC3339(10 * 24 * time.Hour)
	if _, err := app.toolUpdate(ctx, map[string]any{
		"id": w.ID, "patch": map[string]any{"scheduled_at": moved},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	slots, err := app.dbListSlots(ctx, "test-proj", w.ID, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 1 {
		t.Fatalf("want exactly one materialized slot, got %d", len(slots))
	}
	if slots[0].StartsAt != moved {
		t.Errorf("slot starts_at=%q, want %q — the registration page still advertises the old time",
			slots[0].StartsAt, moved)
	}
	if slots[0].EndsAt == "" {
		t.Error("slot ends_at should be re-derived from the new start")
	}
}

func TestRegenerateReminders_UsesPerRegistrantSlotTime(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{
		"title": "Two tracks", "scheduling_mode": "multi",
	})
	slot1Start := futureRFC3339(72 * time.Hour)
	slot2Start := futureRFC3339(120 * time.Hour)
	s1Out, err := app.toolCreateSlot(ctx, map[string]any{"webinar_id": w.ID, "starts_at": slot1Start})
	if err != nil {
		t.Fatal(err)
	}
	s2Out, err := app.toolCreateSlot(ctx, map[string]any{"webinar_id": w.ID, "starts_at": slot2Start})
	if err != nil {
		t.Fatal(err)
	}
	s1 := s1Out.(map[string]any)["slot"].(*WebinarSlot)
	s2 := s2Out.(map[string]any)["slot"].(*WebinarSlot)

	r1 := mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "slot_id": s1.ID, "email": "one@example.com"})
	r2 := mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "slot_id": s2.ID, "email": "two@example.com"})

	if err := app.regenerateReminders(ctx, "test-proj", w.ID); err != nil {
		t.Fatalf("regenerate: %v", err)
	}

	want1, _ := parseDBTime(slot1Start)
	want2, _ := parseDBTime(slot2Start)
	for _, tc := range []struct {
		reg   int64
		start time.Time
	}{{r1.ID, want1}, {r2.ID, want2}} {
		var got string
		if err := ctx.AppDB().QueryRow(
			`SELECT scheduled_for FROM webinar_reminders
			 WHERE registrant_id = ? AND lead_label = 'T-24h'`, tc.reg).Scan(&got); err != nil {
			t.Fatalf("registrant %d has no T-24h reminder: %v", tc.reg, err)
		}
		want := formatRFC3339(tc.start.Add(-24 * time.Hour))
		if got != want {
			t.Errorf("registrant %d T-24h scheduled_for=%q, want %q (per-slot time, not the webinar's)",
				tc.reg, got, want)
		}
	}
}

// ─── 6. Elapsed slots stop being registrable ──────────────────────

func TestSlots_ElapsedSlotIsNotAvailable(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Yesterday", "scheduling_mode": "multi"})
	if _, err := app.toolCreateSlot(ctx, map[string]any{
		"webinar_id": w.ID,
		"starts_at":  futureRFC3339(-25 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	avail, err := app.dbListSlots(ctx, "test-proj", w.ID, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(avail) != 0 {
		t.Errorf("a slot that already happened is still offered for registration: %+v", avail[0])
	}

	// And registration says so rather than silently producing a
	// reminder-less registrant.
	if _, err := app.toolRegister(ctx, map[string]any{
		"webinar_id": w.ID, "email": "late@example.com",
	}); err == nil {
		t.Error("expected registration to fail when every slot has elapsed")
	}
}

func TestSweepSlotStatuses_TransitionsScheduledToLiveToEnded(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Sweep", "scheduling_mode": "multi"})

	past, err := app.toolCreateSlot(ctx, map[string]any{
		"webinar_id": w.ID, "starts_at": futureRFC3339(-3 * time.Hour), "duration_minutes": 60})
	if err != nil {
		t.Fatal(err)
	}
	running, err := app.toolCreateSlot(ctx, map[string]any{
		"webinar_id": w.ID, "starts_at": futureRFC3339(-10 * time.Minute), "duration_minutes": 60})
	if err != nil {
		t.Fatal(err)
	}
	upcoming, err := app.toolCreateSlot(ctx, map[string]any{
		"webinar_id": w.ID, "starts_at": futureRFC3339(24 * time.Hour), "duration_minutes": 60})
	if err != nil {
		t.Fatal(err)
	}

	if err := app.sweepSlotStatuses(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for _, tc := range []struct {
		slot *WebinarSlot
		want string
	}{
		{past.(map[string]any)["slot"].(*WebinarSlot), "ended"},
		{running.(map[string]any)["slot"].(*WebinarSlot), "live"},
		{upcoming.(map[string]any)["slot"].(*WebinarSlot), "scheduled"},
	} {
		got, err := app.dbGetSlot(ctx, "test-proj", tc.slot.ID)
		if err != nil || got == nil {
			t.Fatalf("reload slot %d: %v", tc.slot.ID, err)
		}
		if got.Status != tc.want {
			t.Errorf("slot starting %s: status=%q, want %q", tc.slot.StartsAt, got.Status, tc.want)
		}
	}
}

// ─── 7. Reminders stop for ended / cancelled webinars ─────────────

func TestReminderScheduler_SkipsEndedWebinars(t *testing.T) {
	app, ctx, _, _, msg := newTestAppCfg(t, false, true, nil)
	w := mustCreate(t, app, ctx, map[string]any{
		"title": "Started early", "scheduled_at": futureRFC3339(30 * time.Minute),
	})
	mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "email": "a@example.com"})
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinar_reminders SET scheduled_for = ?`, futureRFC3339(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	// The webinar ran early and is already over.
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinars SET status='ended', ended_at = ? WHERE id = ?`, nowRFC3339(), w.ID); err != nil {
		t.Fatal(err)
	}

	if err := app.runReminderScheduler(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(msg.sent) != 0 {
		t.Errorf("sent %d reminders for a webinar that already ended", len(msg.sent))
	}
}

func TestUpdate_CancelIsARealState(t *testing.T) {
	app, ctx, streaming, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{
		"title": "Called off", "scheduled_at": futureRFC3339(48 * time.Hour),
	})
	mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "email": "a@example.com"})

	out, err := app.toolUpdate(ctx, map[string]any{
		"id": w.ID, "patch": map[string]any{"status": "cancelled"},
	})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if got := out.(map[string]any)["webinar"].(*Webinar).Status; got != "cancelled" {
		t.Fatalf("status=%q, want cancelled", got)
	}
	if !streaming.stopped[w.StreamID] {
		t.Error("cancelling should stop the stream")
	}
	if n := countRow(t, ctx,
		`SELECT COUNT(*) FROM webinar_reminders WHERE webinar_id = ? AND status='pending'`, w.ID); n != 0 {
		t.Errorf("%d pending reminders survived cancellation", n)
	}
	if n := countRow(t, ctx,
		`SELECT COUNT(*) FROM webinar_slots WHERE webinar_id = ? AND status <> 'cancelled'`, w.ID); n != 0 {
		t.Errorf("%d slots still registrable after cancellation", n)
	}
	// The guard in toolRegister is now reachable.
	if _, err := app.toolRegister(ctx, map[string]any{
		"webinar_id": w.ID, "email": "late@example.com"}); err == nil {
		t.Error("expected registration to be refused for a cancelled webinar")
	}
	// Any other status value is refused.
	if _, err := app.toolUpdate(ctx, map[string]any{
		"id": w.ID, "patch": map[string]any{"status": "live"}}); err == nil {
		t.Error("expected status=live to be rejected by webinars_update")
	}
}

// ─── 8. messaging_id is recorded ──────────────────────────────────

func TestReminderScheduler_RecordsMessagingID(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, true, nil)
	w := mustCreate(t, app, ctx, map[string]any{
		"title": "Audit", "scheduled_at": futureRFC3339(26 * time.Hour),
	})
	mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "email": "a@example.com"})
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinar_reminders SET scheduled_for = ? WHERE lead_label='T-24h'`,
		futureRFC3339(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := app.runReminderScheduler(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}

	var status string
	var msgID int64
	if err := ctx.AppDB().QueryRow(
		`SELECT status, COALESCE(messaging_id,0) FROM webinar_reminders WHERE lead_label='T-24h'`).
		Scan(&status, &msgID); err != nil {
		t.Fatal(err)
	}
	if status != "sent" {
		t.Fatalf("status=%q, want sent", status)
	}
	if msgID == 0 {
		t.Error("messaging_id is 0 — delivery is untraceable")
	}
}

// ─── 9. Update validates before it writes ─────────────────────────

func TestUpdate_RejectsGarbageWithoutDestroyingReminders(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{
		"title": "Validate", "scheduled_at": futureRFC3339(48 * time.Hour),
	})
	mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "email": "a@example.com"})
	before := countRow(t, ctx,
		`SELECT COUNT(*) FROM webinar_reminders WHERE webinar_id = ? AND status='pending'`, w.ID)
	if before == 0 {
		t.Fatal("fixture should have pending reminders")
	}

	if _, err := app.toolUpdate(ctx, map[string]any{
		"id": w.ID, "patch": map[string]any{"scheduled_at": "next Tuesday"},
	}); err == nil {
		t.Fatal("expected an error for an unparseable scheduled_at")
	}
	after := countRow(t, ctx,
		`SELECT COUNT(*) FROM webinar_reminders WHERE webinar_id = ? AND status='pending'`, w.ID)
	if after != before {
		t.Errorf("pending reminders went %d -> %d on a rejected patch", before, after)
	}
	reload, _ := app.dbGet(ctx, "test-proj", w.ID)
	if reload.ScheduledAt != w.ScheduledAt {
		t.Errorf("scheduled_at was written despite validation failing: %q", reload.ScheduledAt)
	}

	for _, bad := range []map[string]any{
		{"kind": "bogus"},
		{"duration_minutes": -5},
		{"title": "   "},
	} {
		if _, err := app.toolUpdate(ctx, map[string]any{"id": w.ID, "patch": bad}); err == nil {
			t.Errorf("expected patch %v to be rejected", bad)
		}
	}
}

func TestUpdate_DraftBecomesScheduled(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Draft"})
	if w.Status != "draft" {
		t.Fatalf("fixture status=%q", w.Status)
	}
	out, err := app.toolUpdate(ctx, map[string]any{
		"id": w.ID, "patch": map[string]any{"scheduled_at": futureRFC3339(48 * time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(map[string]any)["webinar"].(*Webinar).Status; got != "scheduled" {
		t.Errorf("status=%q, want scheduled — webinars_list status=scheduled would miss it", got)
	}
}

// ─── 10. Sequence allocation is atomic ────────────────────────────

func TestNextWebinarSequence_NoDuplicatesUnderConcurrency(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Busy room"})

	const n = 50
	var mu sync.Mutex
	seen := map[int]bool{}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seq := app.nextWebinarSequence(ctx, w.ID)
			mu.Lock()
			defer mu.Unlock()
			seen[seq] = true
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Errorf("allocated %d distinct sequences out of %d — duplicates silently drop live-room messages",
			len(seen), n)
	}
}

// ─── 11. Registration validation + abuse budget ───────────────────

func TestRegister_RejectsMalformedContacts(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Guard"})
	for _, bad := range []map[string]any{
		{"email": "not-an-email"},
		{"email": "a@b"},
		{"email": "victim@example.com, second@example.com"},
		{"email": "a@example.com\nBcc: x@y.com"},
		{"phone": "1234"},
		{"phone": "5551234567"},
		{"phone": "+1-555-CALL-NOW"},
	} {
		args := map[string]any{"webinar_id": w.ID}
		for k, v := range bad {
			args[k] = v
		}
		if _, err := app.toolRegister(ctx, args); err == nil {
			t.Errorf("expected %v to be rejected", bad)
		}
	}
	// …and the good ones are normalized.
	r := mustRegister(t, app, ctx, map[string]any{
		"webinar_id": w.ID, "email": "  Alice@Example.COM ", "phone": "+1 (555) 010-2030",
	})
	if r.Email != "alice@example.com" {
		t.Errorf("email=%q, want normalized lowercase", r.Email)
	}
	if r.Phone != "+15550102030" {
		t.Errorf("phone=%q, want E.164", r.Phone)
	}
}

func TestRegister_PerWebinarRateLimit(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, map[string]string{
		"registration_rate_limit_per_minute": "3",
	})
	w := mustCreate(t, app, ctx, map[string]any{"title": "Amplifier"})

	for i := 0; i < 3; i++ {
		if _, err := app.toolRegister(ctx, map[string]any{
			"webinar_id": w.ID, "email": fmt.Sprintf("ok%d@example.com", i),
		}); err != nil {
			t.Fatalf("registration %d should be inside the budget: %v", i, err)
		}
	}
	_, err := app.toolRegister(ctx, map[string]any{
		"webinar_id": w.ID, "email": "over@example.com"})
	if !errors.Is(err, errRegistrationRateLimited) {
		t.Fatalf("4th registration err=%v, want errRegistrationRateLimited", err)
	}
	// Bulk import stays exempt.
	if _, err := app.toolRegister(ctx, map[string]any{
		"webinar_id": w.ID, "email": "import@example.com", "source": "import"}); err != nil {
		t.Errorf("source=import should bypass the budget: %v", err)
	}
}

// ─── 12. Replay expiry is enforced through signed URLs ────────────

func TestReplayPlayback_TTLBoundedByExpiry(t *testing.T) {
	app, ctx, streaming, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Replay"})
	streaming.streams[w.StreamID].Status = "ended"
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinars SET status='ended', ended_at = ? WHERE id = ?`, nowRFC3339(), w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPublishReplay(ctx, map[string]any{
		"id": w.ID, "expires_at": futureRFC3339(30 * time.Minute),
	}); err != nil {
		t.Fatalf("publish replay: %v", err)
	}
	if !streaming.policy[w.StreamID] {
		t.Error("publishing a replay must lock the stream to signed URLs")
	}

	reload, _ := app.dbGet(ctx, "test-proj", w.ID)
	pb, err := app.ReplayPlayback(ctx, reload)
	if err != nil {
		t.Fatalf("replay playback: %v", err)
	}
	if !pb.Signed {
		t.Fatal("replay URL is not signed — expiry would be unenforceable")
	}
	reqs := streaming.signedRequests()
	last := reqs[len(reqs)-1]
	if last.ExpiresInSeconds > 30*60 || last.ExpiresInSeconds <= 0 {
		t.Errorf("signed TTL=%ds, want (0, 1800] — bounded by replay_expires_at", last.ExpiresInSeconds)
	}

	// Past the window, nothing is minted at all.
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinars SET replay_expires_at = ? WHERE id = ?`, futureRFC3339(-time.Hour), w.ID); err != nil {
		t.Fatal(err)
	}
	expired, _ := app.dbGet(ctx, "test-proj", w.ID)
	if _, err := app.ReplayPlayback(ctx, expired); !errors.Is(err, errReplayExpired) {
		t.Errorf("expired replay err=%v, want errReplayExpired", err)
	}
}

func TestLiveRoomPlayback_SignedAndFallsBack(t *testing.T) {
	app, ctx, streaming, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Live", "duration_minutes": 90})
	if !streaming.policy[w.StreamID] {
		t.Error("creating a webinar should lock its stream to signed URLs")
	}
	snap, _ := streaming.GetStream(w.StreamID)

	pb := app.LiveRoomPlayback(ctx, w, &snap)
	if !pb.Signed || pb.URL == "" {
		t.Fatalf("live playback not signed: %+v", pb)
	}
	reqs := streaming.signedRequests()
	ttl := reqs[len(reqs)-1].ExpiresInSeconds
	if ttl < 90*60 {
		t.Errorf("live TTL=%ds, shorter than the webinar itself — viewers would need a mid-session refresh", ttl)
	}

	// An older streaming install without the tool must not break the room.
	streaming.signedErr = errors.New("unknown tool: streams_signed_url")
	fallback := app.LiveRoomPlayback(ctx, w, &snap)
	if fallback.Signed {
		t.Error("fallback should report Signed=false")
	}
	if fallback.URL != snap.PlaybackURL {
		t.Errorf("fallback URL=%q, want the snapshot's playback URL", fallback.URL)
	}
}

// ─── 13. CTA URL allowlist ────────────────────────────────────────

func TestOffers_RejectNonHTTPCTA(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "XSS"})
	for _, bad := range []string{
		"javascript:alert(document.cookie)",
		"JavaScript:fetch('//evil')",
		"data:text/html;base64,PHNjcmlwdD4=",
		"vbscript:msgbox(1)",
		"file:///etc/passwd",
		"https://",
	} {
		if _, err := app.toolPostOffer(ctx, map[string]any{
			"id": w.ID, "headline": "H", "cta_label": "Go", "cta_url": bad,
		}); err == nil {
			t.Errorf("post_offer accepted cta_url=%q", bad)
		}
		if _, err := app.toolDefineOffer(ctx, map[string]any{
			"id": w.ID, "offset_seconds": 10, "headline": "H", "cta_label": "Go", "cta_url": bad,
		}); err == nil {
			t.Errorf("define_offer accepted cta_url=%q", bad)
		}
	}
	if _, err := app.toolPostOffer(ctx, map[string]any{
		"id": w.ID, "headline": "H", "cta_label": "Go", "cta_url": "https://example.com/buy?x=1",
	}); err != nil {
		t.Errorf("a plain https CTA should be accepted: %v", err)
	}
}

// ─── 14. Poll / offer writes are ownership-checked ────────────────

func TestRecordPollResponse_RejectsCrossWebinalIDs(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	victim := mustCreate(t, app, ctx, map[string]any{"title": "Victim"})
	attacker := mustCreate(t, app, ctx, map[string]any{"title": "Attacker"})
	reg := mustRegister(t, app, ctx, map[string]any{"webinar_id": attacker.ID, "email": "a@example.com"})

	pollOut, err := app.toolPushPoll(ctx, map[string]any{
		"id": victim.ID, "question": "Which plan?", "choices": []any{"Pro", "Team"},
	})
	if err != nil {
		t.Fatal(err)
	}
	pollID := pollOut.(map[string]any)["poll_id"].(int64)

	if err := app.RecordPollResponse(ctx, attacker, reg, pollID, 0); !errors.Is(err, errEngagementNotFound) {
		t.Errorf("cross-webinar poll stuffing err=%v, want errEngagementNotFound", err)
	}
	if n := countRow(t, ctx, `SELECT COUNT(*) FROM webinar_poll_responses WHERE poll_id = ?`, pollID); n != 0 {
		t.Errorf("%d foreign poll responses were written", n)
	}

	// The legitimate owner can vote, and out-of-range choices are refused.
	ownReg := mustRegister(t, app, ctx, map[string]any{"webinar_id": victim.ID, "email": "b@example.com"})
	if err := app.RecordPollResponse(ctx, victim, ownReg, pollID, 1); err != nil {
		t.Fatalf("own poll response: %v", err)
	}
	if err := app.RecordPollResponse(ctx, victim, ownReg, pollID, 7); !errors.Is(err, errInvalidChoice) {
		t.Errorf("out-of-range choice err=%v, want errInvalidChoice", err)
	}

	// …and votes stop once the poll closes.
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinar_polls SET closes_at = ? WHERE id = ?`, futureRFC3339(-time.Minute), pollID); err != nil {
		t.Fatal(err)
	}
	if err := app.RecordPollResponse(ctx, victim, ownReg, pollID, 0); !errors.Is(err, errPollClosed) {
		t.Errorf("late vote err=%v, want errPollClosed", err)
	}
}

func TestRecordOfferClick_RejectsForeignAndUnshownOffers(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	victim := mustCreate(t, app, ctx, map[string]any{"title": "Victim"})
	attacker := mustCreate(t, app, ctx, map[string]any{"title": "Attacker"})
	reg := mustRegister(t, app, ctx, map[string]any{"webinar_id": attacker.ID, "email": "a@example.com"})

	shown, err := app.toolPostOffer(ctx, map[string]any{
		"id": victim.ID, "headline": "H", "cta_label": "Go", "cta_url": "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	offerID := shown.(map[string]any)["offer_id"].(int64)
	if err := app.RecordOfferClick(ctx, attacker, reg, offerID); !errors.Is(err, errEngagementNotFound) {
		t.Errorf("cross-webinar CTR stuffing err=%v, want errEngagementNotFound", err)
	}

	scripted, err := app.toolDefineOffer(ctx, map[string]any{
		"id": victim.ID, "offset_seconds": 600, "headline": "H",
		"cta_label": "Go", "cta_url": "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	hiddenID := scripted.(map[string]any)["offer_id"].(int64)
	ownReg := mustRegister(t, app, ctx, map[string]any{"webinar_id": victim.ID, "email": "b@example.com"})
	if err := app.RecordOfferClick(ctx, victim, ownReg, hiddenID); !errors.Is(err, errEngagementNotFound) {
		t.Errorf("click on an offer nobody has seen err=%v, want errEngagementNotFound", err)
	}
	if err := app.RecordOfferClick(ctx, victim, ownReg, offerID); err != nil {
		t.Errorf("legitimate click rejected: %v", err)
	}
}

// ─── 15. CTR cannot exceed 100% ───────────────────────────────────

func TestEngagement_CTRCountsDistinctClickers(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "CTR"})
	reg := mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "email": "a@example.com"})
	out, err := app.toolPostOffer(ctx, map[string]any{
		"id": w.ID, "headline": "H", "cta_label": "Go", "cta_url": "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	offerID := out.(map[string]any)["offer_id"].(int64)

	for i := 0; i < 5; i++ {
		if err := app.RecordOfferClick(ctx, w, reg, offerID); err != nil {
			t.Fatal(err)
		}
	}
	eOut, err := app.toolGetEngagement(ctx, map[string]any{"id": w.ID})
	if err != nil {
		t.Fatal(err)
	}
	e := eOut.(map[string]any)
	if got := e["offer_click_through_pct"].(int); got != 100 {
		t.Errorf("offer_click_through_pct=%d, want 100 (one of one registrant clicked)", got)
	}
	if got := e["unique_offer_clickers"].(int); got != 1 {
		t.Errorf("unique_offer_clickers=%d, want 1", got)
	}
	if got := e["offer_clicks"].(int); got != 5 {
		t.Errorf("offer_clicks=%d, want the raw 5", got)
	}
}

// ─── 21. Heartbeat accumulator ────────────────────────────────────

func TestAttendance_HeartbeatClampsWatchTimeAndClearsLeftAt(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Room"})
	reg := mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "email": "a@example.com"})

	// A registrant spamming the endpoint accrues real elapsed time, not
	// +10s per request.
	credited := 0
	for i := 0; i < 100; i++ {
		credited += app.RecordHeartbeat(ctx, w, reg)
	}
	if credited > 30 {
		t.Errorf("100 rapid heartbeats credited %ds of watch time — the counter is spoofable", credited)
	}
	if credited < 1 {
		t.Error("the first heartbeat should credit at least the heartbeat interval")
	}

	if err := app.runAttendanceFlush(context.Background(), ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	var watch int
	var leftAt any
	if err := ctx.AppDB().QueryRow(
		`SELECT watch_seconds, left_at FROM webinar_attendance WHERE registrant_id = ?`,
		reg.ID).Scan(&watch, &leftAt); err != nil {
		t.Fatalf("read attendance: %v", err)
	}
	if watch != credited {
		t.Errorf("watch_seconds=%d, want the %d credited in memory", watch, credited)
	}

	// One idle blip used to mark a viewer permanently "left".
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinar_attendance SET left_at = ? WHERE registrant_id = ?`,
		nowRFC3339(), reg.ID); err != nil {
		t.Fatal(err)
	}
	app.RecordHeartbeat(ctx, w, reg)
	if err := app.runAttendanceFlush(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(
		`SELECT left_at FROM webinar_attendance WHERE registrant_id = ?`, reg.ID).Scan(&leftAt); err != nil {
		t.Fatal(err)
	}
	if leftAt != nil {
		t.Errorf("left_at=%v after a fresh heartbeat — the viewer is back", leftAt)
	}

	// The flush promotes attended_live for exactly the rows it wrote.
	reload, _ := app.dbGetRegistrant(ctx, "test-proj", reg.ID)
	if !reload.AttendedLive {
		t.Error("attended_live should be promoted by the flush")
	}
}

func TestAttendanceTracker_CreditsRealElapsedTime(t *testing.T) {
	tr := newAttendanceTracker()
	key := attendanceKey{RegistrantID: 1, Source: "live"}
	base := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)

	if got := tr.record(key, "p", 1, base, 10*time.Second); got != 10 {
		t.Errorf("first beat credited %d, want the 10s interval", got)
	}
	if got := tr.record(key, "p", 1, base.Add(7*time.Second), 10*time.Second); got != 7 {
		t.Errorf("7s later credited %d, want 7", got)
	}
	// A suspended browser must not credit the whole gap.
	if got := tr.record(key, "p", 1, base.Add(time.Hour), 10*time.Second); got != 20 {
		t.Errorf("an hour-long gap credited %d, want the 20s ceiling", got)
	}
	batch := tr.drain()
	if len(batch) != 1 || batch[0].Seconds != 37 {
		t.Errorf("drain=%+v, want one entry totalling 37s", batch)
	}
	if got := tr.drain(); len(got) != 0 {
		t.Errorf("second drain returned %d entries, want 0", len(got))
	}
	// The entry stays resident after a flush — that's what makes the
	// elapsed-time clamp meaningful across flush boundaries.
	if tr.size() != 1 {
		t.Errorf("tracker size=%d after drain, want the viewer still tracked", tr.size())
	}
	tr.evictIdle(base.Add(2*time.Hour), 30*time.Minute)
	if tr.size() != 0 {
		t.Errorf("tracker size=%d after eviction, want 0", tr.size())
	}
}

// ─── 22. Retention pruning ────────────────────────────────────────

func TestRetentionPrune_DropsAgedRowsOnly(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, map[string]string{"retention_days": "30"})
	w := mustCreate(t, app, ctx, map[string]any{"title": "Old"})
	reg := mustRegister(t, app, ctx, map[string]any{"webinar_id": w.ID, "email": "a@example.com"})

	if _, _, err := app.InsertChatMessage(ctx, w, reg, "A", "recent", "message"); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO webinar_chat (project_id, webinar_id, registrant_id, display_name, body, kind, sequence, created_at)
		 VALUES (?, ?, ?, 'A', 'ancient', 'message', 999, ?)`,
		"test-proj", w.ID, reg.ID, futureRFC3339(-90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := app.runRetentionPrune(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if n := countRow(t, ctx, `SELECT COUNT(*) FROM webinar_chat WHERE body = 'ancient'`); n != 0 {
		t.Errorf("aged chat row survived pruning")
	}
	if n := countRow(t, ctx, `SELECT COUNT(*) FROM webinar_chat WHERE body = 'recent'`); n != 1 {
		t.Errorf("recent chat row was pruned")
	}
}

// ─── 23. uniqueSlug off-by-one ────────────────────────────────────

func TestUniqueSlug_UsesTheFullNumberedRange(t *testing.T) {
	_, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	// Occupy base and base-2 … base-99, leaving base-100 free.
	insert := func(slug string) {
		if _, err := ctx.AppDB().Exec(
			`INSERT INTO webinars (project_id, slug, title, kind, status, created_at)
			 VALUES ('test-proj', ?, 'x', 'scheduled', 'draft', ?)`, slug, nowRFC3339()); err != nil {
			t.Fatal(err)
		}
	}
	insert("demo")
	for n := 2; n <= 99; n++ {
		insert(fmt.Sprintf("demo-%d", n))
	}
	if got := uniqueSlug(ctx, "test-proj", "demo"); got != "demo-100" {
		t.Errorf("uniqueSlug=%q, want demo-100 (the old loop skipped the last candidate)", got)
	}
}

// ─── 24. Event ids survive every numeric encoding ─────────────────

func TestStreamLifecycle_AcceptsAnyNumericEventID(t *testing.T) {
	for name, raw := range map[string]any{
		"float64":     float64(0),
		"int":         int(0),
		"int64":       int64(0),
		"json.Number": json.Number("0"),
		"string":      "0",
	} {
		t.Run(name, func(t *testing.T) {
			app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
			w := mustCreate(t, app, ctx, map[string]any{
				"title": "Numeric", "scheduled_at": futureRFC3339(time.Hour),
			})
			var id any
			switch raw.(type) {
			case float64:
				id = float64(w.StreamID)
			case int:
				id = int(w.StreamID)
			case int64:
				id = w.StreamID
			case json.Number:
				id = json.Number(fmt.Sprintf("%d", w.StreamID))
			case string:
				id = fmt.Sprintf("%d", w.StreamID)
			}
			if err := app.handleStreamStarted(ctx, sdk.Event{
				Topic: "stream.started", ProjectID: "test-proj",
				Data: map[string]any{"id": id},
			}); err != nil {
				t.Fatal(err)
			}
			reload, _ := app.dbGet(ctx, "test-proj", w.ID)
			if reload.Status != "live" {
				t.Errorf("status=%q with a %s event id — lifecycle mirroring is dead", reload.Status, name)
			}
		})
	}
}

// ─── 16. The we're-live blast is queued, not sent inline ──────────

func TestStreamStarted_QueuesLiveBlastInsteadOfBlocking(t *testing.T) {
	app, ctx, _, _, msg := newTestAppCfg(t, false, true, nil)
	w := mustCreate(t, app, ctx, map[string]any{
		"title": "Going live", "scheduled_at": futureRFC3339(time.Hour),
	})
	for i := 0; i < 3; i++ {
		mustRegister(t, app, ctx, map[string]any{
			"webinar_id": w.ID, "email": fmt.Sprintf("r%d@example.com", i)})
	}

	if err := app.handleStreamStarted(ctx, sdk.Event{
		Topic: "stream.started", ProjectID: "test-proj",
		Data: map[string]any{"id": float64(w.StreamID)},
	}); err != nil {
		t.Fatal(err)
	}
	if len(msg.sent) != 0 {
		t.Errorf("the event handler sent %d messages inline; it should only enqueue", len(msg.sent))
	}
	if n := countRow(t, ctx,
		`SELECT COUNT(*) FROM webinar_reminders WHERE webinar_id = ? AND lead_label='live' AND status='pending'`,
		w.ID); n != 3 {
		t.Fatalf("queued live-blast rows=%d, want 3", n)
	}

	if err := app.runReminderScheduler(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(msg.sent) != 3 {
		t.Errorf("scheduler dispatched %d live-blast messages, want 3", len(msg.sent))
	}
	for _, m := range msg.sent {
		if !strings.Contains(m.Body, "We're live") {
			t.Errorf("unexpected blast body %q", m.Body)
		}
	}
	// Every row carries an audit trail now, which the inline path never wrote.
	if n := countRow(t, ctx,
		`SELECT COUNT(*) FROM webinar_reminders
		 WHERE webinar_id = ? AND lead_label='live' AND status='sent' AND messaging_id IS NOT NULL`,
		w.ID); n != 3 {
		t.Errorf("audited live-blast rows=%d, want 3", n)
	}
}

// ─── Migration 003 against a database that already has data ───────
//
// The testkit only ever replays migrations into an EMPTY database, so
// nothing else exercises the normalization + de-duplication steps that
// have to survive a real upgrade.

func TestMigration003_UpgradesADatabaseWithLegacyRows(t *testing.T) {
	db, err := sql.Open("sqlite",
		"file:"+t.TempDir()+"/webinars.db?_pragma=foreign_keys(on)&_pragma=busy_timeout(2000)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	apply := func(name string) {
		t.Helper()
		body, err := os.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	apply("001_init.sql")
	apply("002_slots.sql")

	// Legacy rows in the SQLite CURRENT_TIMESTAMP layout.
	exec(`INSERT INTO webinars (id, project_id, slug, title, kind, status, started_at, created_at)
		  VALUES (1, 'p', 'legacy', 'Legacy', 'scheduled', 'live', '2026-08-18 09:06:00', '2026-08-01 08:00:00')`)
	// Two phone-only registrants for the same number: the double-submit
	// artifact ux_reg_phone has to collapse before it can be created.
	exec(`INSERT INTO webinar_registrants (id, project_id, webinar_id, phone, join_token, registered_at)
		  VALUES (1, 'p', 1, '+15550000001', 'tok-1', '2026-08-01 08:01:00')`)
	exec(`INSERT INTO webinar_registrants (id, project_id, webinar_id, phone, join_token, registered_at)
		  VALUES (2, 'p', 1, '+15550000001', 'tok-2', '2026-08-01 08:01:30')`)
	exec(`INSERT INTO webinar_registrants (id, project_id, webinar_id, email, phone, join_token)
		  VALUES (3, 'p', 1, 'keep@example.com', '+15550000001', 'tok-3')`)
	// Duplicate reminder rows that differ only by id.
	for i := 0; i < 2; i++ {
		exec(`INSERT INTO webinar_reminders (project_id, webinar_id, registrant_id, channel, lead_label, scheduled_for)
			  VALUES ('p', 1, 1, 'sms', 'T-24h', '2026-08-17 09:06:00')`)
	}
	exec(`INSERT INTO webinar_chat (project_id, webinar_id, display_name, body, sequence, created_at)
		  VALUES ('p', 1, 'A', 'hi', 7, '2026-08-18 09:07:00')`)
	exec(`INSERT INTO webinar_polls (project_id, webinar_id, question, choices, sequence, opened_at)
		  VALUES ('p', 1, 'Q', '["a","b"]', 11, '2026-08-18 09:08:00')`)

	apply("003_hardening.sql")

	var startedAt string
	if err := db.QueryRow(`SELECT started_at FROM webinars WHERE id = 1`).Scan(&startedAt); err != nil {
		t.Fatal(err)
	}
	if startedAt != "2026-08-18T09:06:00Z" {
		t.Errorf("started_at=%q, want normalized RFC3339", startedAt)
	}
	if _, err := time.Parse(time.RFC3339, startedAt); err != nil {
		t.Errorf("started_at still fails RFC3339 parse: %v", err)
	}

	var phoneOnly int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM webinar_registrants
		  WHERE phone = '+15550000001' AND (email IS NULL OR email = '')`).Scan(&phoneOnly); err != nil {
		t.Fatal(err)
	}
	if phoneOnly != 1 {
		t.Errorf("phone-only duplicates=%d, want 1 (collapsed onto the earliest row)", phoneOnly)
	}
	var kept int64
	if err := db.QueryRow(
		`SELECT id FROM webinar_registrants WHERE join_token = 'tok-1'`).Scan(&kept); err != nil {
		t.Errorf("the earliest phone-only registrant should survive: %v", err)
	}
	var withEmail int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM webinar_registrants WHERE email = 'keep@example.com'`).Scan(&withEmail); err != nil {
		t.Fatal(err)
	}
	if withEmail != 1 {
		t.Error("a registrant that shares the phone but has an email must not be collapsed")
	}

	var reminders int
	if err := db.QueryRow(`SELECT COUNT(*) FROM webinar_reminders`).Scan(&reminders); err != nil {
		t.Fatal(err)
	}
	if reminders != 1 {
		t.Errorf("duplicate reminders=%d, want 1", reminders)
	}

	var seq int
	if err := db.QueryRow(
		`SELECT value FROM webinar_sequences WHERE webinar_id = 1 AND kind = 'event'`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if seq != 11 {
		t.Errorf("seeded sequence=%d, want 11 (the max already handed out)", seq)
	}

	// The new unique indexes must actually be enforceable.
	if _, err := db.Exec(
		`INSERT INTO webinar_registrants (project_id, webinar_id, phone, join_token)
		 VALUES ('p', 1, '+15550000001', 'tok-4')`); err == nil {
		t.Error("ux_reg_phone did not reject a duplicate phone-only registration")
	}
}

// ─── Timestamp helpers ────────────────────────────────────────────

func TestParseDBTime_AcceptsLegacyAndRFC3339(t *testing.T) {
	want := time.Date(2026, 8, 18, 9, 6, 0, 0, time.UTC)
	for _, in := range []string{
		"2026-08-18T09:06:00Z",
		"2026-08-18 09:06:00",
		"2026-08-18T09:06:00",
		"2026-08-18T11:06:00+02:00",
	} {
		got, err := parseDBTime(in)
		if err != nil {
			t.Fatalf("parseDBTime(%q): %v", in, err)
		}
		if !got.Equal(want) {
			t.Errorf("parseDBTime(%q)=%v, want %v", in, got, want)
		}
	}
	if _, err := parseDBTime("next Tuesday"); err == nil {
		t.Error("expected an error for an unparseable timestamp")
	}
}
