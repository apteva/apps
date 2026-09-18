package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Regressions fixed in v0.3.0. Each test fails against v0.2.0.

// ─── 1. The /events cursor must never outrun what it delivered ────

type eventsResponse struct {
	Cursor int `json:"cursor"`
	Events []struct {
		Kind     string `json:"kind"`
		Sequence int    `json:"sequence"`
		Body     string `json:"body"`
	} `json:"events"`
}

// v0.2 set cursor to the highest sequence across three separately
// capped queries. 250 pending chat rows plus one offer at a higher
// sequence delivered chat 1..200 and a cursor of 251, so chat 201..250
// could never be requested again.
func TestEventsCursorNeverSkipsChat(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{
		"title": "Busy room", "scheduled_at": futureRFC3339(time.Hour),
	})
	reg := mustRegister(t, app, ctx, map[string]any{
		"webinar_id": w.ID, "email": "viewer@example.com",
	})

	const chatCount = 250
	for i := 1; i <= chatCount; i++ {
		if _, err := ctx.AppDB().Exec(
			`INSERT INTO webinar_chat (project_id, webinar_id, display_name, body, kind, sequence, created_at)
			 VALUES (?,?,?,?,?,?,?)`,
			w.ProjectID, w.ID, "A", fmt.Sprintf("msg-%d", i), "message", i, nowRFC3339()); err != nil {
			t.Fatal(err)
		}
	}
	// An offer whose sequence sits above every pending chat row.
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO webinar_offers (project_id, webinar_id, offset_seconds, headline,
			cta_label, cta_url, duration_seconds, shown_at, sequence)
		 VALUES (?,?,NULL,?,?,?,?,?,?)`,
		w.ProjectID, w.ID, "Buy", "Go", "https://example.com", 30, nowRFC3339(), chatCount+1); err != nil {
		t.Fatal(err)
	}

	// Drain the way a real client does: poll until nothing new arrives.
	seen := map[string]bool{}
	cursor := 0
	for i := 0; i < 20; i++ {
		rw := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet,
			fmt.Sprintf("/live/%s/events?since=%d", reg.JoinToken, cursor), nil)
		app.handleLiveEvents(rw, r, ctx, w, reg)
		if rw.Code != http.StatusOK {
			t.Fatalf("events status=%d", rw.Code)
		}
		var resp eventsResponse
		if err := json.Unmarshal(rw.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if len(resp.Events) == 0 {
			break
		}
		for _, e := range resp.Events {
			if e.Kind != "chat" {
				continue
			}
			if seen[e.Body] {
				t.Errorf("chat %q delivered twice", e.Body)
			}
			seen[e.Body] = true
		}
		if resp.Cursor <= cursor {
			t.Fatalf("cursor did not advance: %d -> %d", cursor, resp.Cursor)
		}
		cursor = resp.Cursor
	}

	for i := 1; i <= chatCount; i++ {
		if !seen[fmt.Sprintf("msg-%d", i)] {
			t.Fatalf("chat message %d was never delivered (%d of %d arrived)", i, len(seen), chatCount)
		}
	}
}

// ─── 2. webinars_push_poll must verify ownership ──────────────────

// v0.2 wrote the poll row straight from the caller-supplied id. Under
// scope=global that put an attacker's poll onto another tenant's
// webinar, and /events delivered it into their live room.
func TestPushPollRejectsForeignWebinar(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO webinars (id, project_id, slug, title, kind, status, duration_minutes, created_at)
		 VALUES (4242, 'other-proj', 'victim', 'Victim', 'scheduled', 'live', 60, ?)`,
		nowRFC3339()); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPushPoll(ctx, map[string]any{
		"id": int64(4242), "question": "ATTACKER", "choices": []any{"a", "b"},
	}); err == nil {
		t.Fatal("push_poll accepted another project's webinar id")
	}
	if n := countRow(t, ctx, `SELECT COUNT(*) FROM webinar_polls WHERE webinar_id = 4242`); n != 0 {
		t.Errorf("poll rows written onto a foreign webinar: %d", n)
	}
}

// Even if a row lands on a foreign webinar some other way, the live
// room must not serve it.
func TestEventsIgnoreForeignProjectRows(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Mine"})
	reg := mustRegister(t, app, ctx, map[string]any{
		"webinar_id": w.ID, "email": "v@example.com",
	})
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO webinar_polls (project_id, webinar_id, question, choices, duration_seconds, sequence)
		 VALUES ('other-proj', ?, 'INJECTED', '["a","b"]', 60, 9)`, w.ID); err != nil {
		t.Fatal(err)
	}
	rw := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/live/"+reg.JoinToken+"/events?since=0", nil)
	app.handleLiveEvents(rw, r, ctx, w, reg)
	if strings.Contains(rw.Body.String(), "INJECTED") {
		t.Errorf("a row owned by another project reached the live room: %s", rw.Body.String())
	}
}

// ─── 3. Reminders must carry a join link ──────────────────────────

// The join token was selected on both dispatch paths and never used,
// so "We're live! Join now." arrived with nothing to click.
func TestScheduledReminderCarriesJoinLink(t *testing.T) {
	app, ctx, _, _, messaging := newTestAppCfg(t, false, true, nil)
	w := mustCreate(t, app, ctx, map[string]any{
		"title": "Launch", "scheduled_at": futureRFC3339(30 * time.Minute),
	})
	reg := mustRegister(t, app, ctx, map[string]any{
		"webinar_id": w.ID, "email": "someone@example.com",
	})
	// Make every pending reminder due.
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinar_reminders SET scheduled_for = ? WHERE registrant_id = ?`,
		formatRFC3339(nowUTC().Add(-time.Minute)), reg.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.runReminderScheduler(context.Background(), ctx); err != nil {
		t.Fatalf("scheduler: %v", err)
	}
	if len(messaging.sent) == 0 {
		t.Fatal("no reminder was dispatched")
	}
	for _, m := range messaging.sent {
		if !strings.Contains(m.Body, reg.JoinToken) {
			t.Errorf("reminder body has no join link:\n%s", m.Body)
		}
	}
}

func TestManualReminderCarriesJoinLink(t *testing.T) {
	app, ctx, _, _, messaging := newTestAppCfg(t, false, true, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Manual"})
	reg := mustRegister(t, app, ctx, map[string]any{
		"webinar_id": w.ID, "email": "someone@example.com",
	})
	if _, err := app.toolSendReminder(ctx, map[string]any{"id": w.ID}); err != nil {
		t.Fatal(err)
	}
	if len(messaging.sent) != 1 {
		t.Fatalf("sent=%d, want 1", len(messaging.sent))
	}
	if !strings.Contains(messaging.sent[0].Body, reg.JoinToken) {
		t.Errorf("manual reminder has no join link:\n%s", messaging.sent[0].Body)
	}
}

// A caller-supplied body still gets the recipient's own link appended.
func TestManualReminderAppendsLinkToCustomBody(t *testing.T) {
	app, ctx, _, _, messaging := newTestAppCfg(t, false, true, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Custom"})
	reg := mustRegister(t, app, ctx, map[string]any{
		"webinar_id": w.ID, "email": "someone@example.com",
	})
	if _, err := app.toolSendReminder(ctx, map[string]any{
		"id": w.ID, "body": "Doors open in five.",
	}); err != nil {
		t.Fatal(err)
	}
	body := messaging.sent[0].Body
	if !strings.HasPrefix(body, "Doors open in five.") {
		t.Errorf("custom body was not preserved: %q", body)
	}
	if !strings.Contains(body, reg.JoinToken) {
		t.Errorf("custom body did not get a join link: %q", body)
	}
}

// Bodies must not print raw storage timestamps at people.
func TestReminderBodyRendersTimeForHumans(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := &Webinar{Title: "Launch", ScheduledAt: "2026-09-20T15:00:00Z", Timezone: "Europe/Paris"}
	body := app.reminderBody(ctx, w, "T-1h", "https://example.test/live/tok")
	if strings.Contains(body, "2026-09-20T15:00:00Z") {
		t.Errorf("raw RFC3339 leaked into the reminder: %q", body)
	}
	if !strings.Contains(body, "17:00") {
		t.Errorf("time not rendered in the webinar's timezone: %q", body)
	}
}

// ─── 4. Slot capacity must hold ───────────────────────────────────

func TestSlotCapacityIsEnforced(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{
		"title": "Capped", "scheduling_mode": "multi",
	})
	out, err := app.toolCreateSlot(ctx, map[string]any{
		"webinar_id": w.ID, "starts_at": futureRFC3339(2 * time.Hour), "capacity": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	slot := out.(map[string]any)["slot"].(*WebinarSlot)

	// Both resolve while the slot still looks free — the v0.2 race.
	s1, err := app.resolveRegistrationSlot(ctx, testProject, w, slot.ID)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := app.resolveRegistrationSlot(ctx, testProject, w, slot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.upsertRegistrant(ctx, testProject, w.ID, "one@example.com", "", "One", "form", s1); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	_, _, err = app.upsertRegistrant(ctx, testProject, w.ID, "two@example.com", "", "Two", "form", s2)
	if err == nil {
		t.Error("second registration into a capacity=1 slot was accepted")
	}
	if n := countRow(t, ctx, `SELECT COUNT(*) FROM webinar_registrants WHERE slot_id = ?`, slot.ID); n != 1 {
		t.Errorf("slot holds %d registrants, want 1", n)
	}
}

// ─── 5. Registration page lifecycle ───────────────────────────────

func TestRegistrationClosedForEndedWebinar(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{"title": "Finished"})
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinars SET status='ended', ended_at=? WHERE id=?`, nowRFC3339(), w.ID); err != nil {
		t.Fatal(err)
	}
	rw := doGET(t, app.handleRegistrationPage, withProject("/r/"+w.Slug))
	body := rw.Body.String()
	if strings.Contains(body, "Save my seat") {
		t.Error("a finished webinar still rendered the registration form")
	}
	if !strings.Contains(body, "Registration has closed") {
		t.Errorf("expected the closed state, got: %s", body)
	}
}

func TestRegistrationClosedForCancelledWebinar(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	w := mustCreate(t, app, ctx, map[string]any{
		"title": "Called off", "scheduled_at": futureRFC3339(time.Hour),
	})
	if _, err := app.toolUpdate(ctx, map[string]any{
		"id": w.ID, "patch": map[string]any{"status": "cancelled"},
	}); err != nil {
		t.Fatal(err)
	}
	body := doGET(t, app.handleRegistrationPage, withProject("/r/"+w.Slug)).Body.String()
	if !strings.Contains(body, "cancelled") || strings.Contains(body, "Save my seat") {
		t.Errorf("cancelled webinar should not offer a form: %s", body)
	}
}

// ─── 6. Worker queries are scoped to the dispatch project ─────────

// The SDK runs each worker once per project. v0.2's workers queried
// every project on every one of those passes.
func TestOfferBroadcasterOnlyTouchesItsOwnProject(t *testing.T) {
	app, ctx, _, _, _ := newTestAppCfg(t, false, false, nil)
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO webinars (id, project_id, slug, title, kind, status, duration_minutes, started_at, created_at)
		 VALUES (99, 'other-proj', 'theirs', 'Theirs', 'scheduled', 'live', 60, ?, ?)`,
		formatRFC3339(nowUTC().Add(-time.Hour)), nowRFC3339()); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO webinar_offers (project_id, webinar_id, offset_seconds, headline, cta_label, cta_url, duration_seconds)
		 VALUES ('other-proj', 99, 0, 'Theirs', 'Go', 'https://example.com', 30)`); err != nil {
		t.Fatal(err)
	}
	if err := app.runOfferBroadcaster(context.Background(), ctx.WithProject(testProject)); err != nil {
		t.Fatal(err)
	}
	if n := countRow(t, ctx,
		`SELECT COUNT(*) FROM webinar_offers WHERE webinar_id = 99 AND shown_at IS NOT NULL`); n != 0 {
		t.Error("the broadcaster stamped an offer belonging to a different project")
	}
}

// ─── 7. Lead labels must stay distinct ────────────────────────────

// lead_label is part of a reminder's natural key, so two lead times
// must never render the same label. v0.2 truncated with int(), making
// "1.5,1" produce "T-1h" twice.
func TestReminderLeadLabelsAreDistinct(t *testing.T) {
	seen := map[string]float64{}
	for _, hours := range []float64{24, 2, 1.5, 1, 0.5, 0.25} {
		label := reminderLeadLabel(hours)
		if prev, dup := seen[label]; dup {
			t.Errorf("lead %.2fh and %.2fh both label %q", prev, hours, label)
		}
		seen[label] = hours
	}
}
