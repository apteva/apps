package main

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPresenceReaperKeepsFreshAndClosesStaleParticipants(t *testing.T) {
	app, ctx, rec := newTestApp(t)
	room := createRoom(t, app, ctx)
	fresh := joinForTest(t, app, ctx, room.ID, map[string]any{"chat": true})
	stale := joinForTest(t, app, ctx, room.ID, map[string]any{"chat": true})
	if _, err := ctx.AppDB().Exec(`UPDATE participants SET last_seen_at=datetime('now','-2 minutes') WHERE id=?`, stale.participant.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.runPresenceReaper(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	freshAfter, _ := app.dbGetParticipant(ctx, "test-proj", room.ID, fresh.participant.ID)
	staleAfter, _ := app.dbGetParticipant(ctx, "test-proj", room.ID, stale.participant.ID)
	if freshAfter.Status != "active" || staleAfter.Status != "left" {
		t.Fatalf("fresh=%s stale=%s", freshAfter.Status, staleAfter.Status)
	}
	if len(rec.EventsByTopic("calls.participant.left")) != 1 {
		t.Fatalf("left events=%d", len(rec.EventsByTopic("calls.participant.left")))
	}
}

func TestHeartbeatRefreshesPresenceAndTrackState(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	room := createRoom(t, app, ctx)
	joined := joinForTest(t, app, ctx, room.ID, map[string]any{"audio": true})
	rec := httptest.NewRecorder()
	app.handleAPIRooms(rec, roomRequest("POST", room.ID, "heartbeat", joined.token,
		`{"connection_state":"connected","muted_audio":false,"tracks":[{"id":"mic-1","kind":"audio","source":"microphone","label":"Mic","enabled":true}]}`))
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var status string
	if err := ctx.AppDB().QueryRow(`SELECT status FROM media_tracks WHERE participant_id=? AND track_id='mic-1'`, joined.participant.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "live" {
		t.Fatalf("track status=%s", status)
	}
}

func TestIdleCloserUsesLastActivityAndEndsOnlyInactiveRooms(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	room := createRoom(t, app, ctx)
	if _, err := ctx.AppDB().Exec(`UPDATE rooms SET created_at=datetime('now','-2 hours'), last_activity_at=CURRENT_TIMESTAMP WHERE id=?`, room.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.runRoomIdleCloser(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := app.dbGetRoom(ctx, "test-proj", room.ID)
	if after.Status != "open" {
		t.Fatalf("recently active room status=%s", after.Status)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE rooms SET last_activity_at=datetime('now','-2 hours') WHERE id=?`, room.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.runRoomIdleCloser(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	after, _ = app.dbGetRoom(ctx, "test-proj", room.ID)
	if after.Status != "ended" {
		t.Fatalf("idle room status=%s", after.Status)
	}
}

func TestIdleCloserKeepsScheduledRoomsUntilAppointmentEnds(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	room := createRoom(t, app, ctx)
	if _, err := ctx.AppDB().Exec(`UPDATE rooms SET last_activity_at=datetime('now','-2 hours'), metadata=? WHERE id=?`,
		`{"scheduled_start_at":"2999-01-01T10:00:00Z","scheduled_end_at":"2999-01-01T10:30:00Z"}`, room.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.runRoomIdleCloser(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := app.dbGetRoom(ctx, "test-proj", room.ID)
	if after.Status != "open" {
		t.Fatalf("scheduled room closed before appointment: status=%s", after.Status)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE rooms SET metadata=? WHERE id=?`,
		`{"scheduled_start_at":"2000-01-01T10:00:00Z","scheduled_end_at":"2000-01-01T10:30:00Z"}`, room.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.runRoomIdleCloser(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	after, _ = app.dbGetRoom(ctx, "test-proj", room.ID)
	if after.Status != "ended" {
		t.Fatalf("scheduled room stayed open after appointment: status=%s", after.Status)
	}
}

func TestSessionCleanerHonorsContextAndRemovesOldSignals(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	room := createRoom(t, app, ctx)
	a := joinForTest(t, app, ctx, room.ID, map[string]any{"audio": true})
	b := joinForTest(t, app, ctx, room.ID, map[string]any{"audio": true})
	if _, err := ctx.AppDB().Exec(`INSERT INTO signaling_messages(project_id,room_id,from_participant_id,to_participant_id,kind,payload,created_at) VALUES('test-proj',?,?,?,'ice','{}',datetime('now','-2 hours'))`, room.ID, a.participant.ID, b.participant.ID); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := app.runSessionCleaner(deadline, ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM signaling_messages`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("old signals=%d", count)
	}
}
