package main

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func newTestApp(t *testing.T) (*App, *sdk.AppCtx, *tk.EmitRecorder) {
	t.Helper()
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID("test-proj"),
		tk.WithConfig(map[string]string{
			"max_rooms":                 "10",
			"max_participants_per_room": "4",
		}),
		tk.WithEmitter(rec),
	)
	app := &App{}
	globalCtx = ctx
	globalApp = app
	return app, ctx, rec
}

func createRoom(t *testing.T, app *App, ctx *sdk.AppCtx) *Room {
	t.Helper()
	out, err := app.toolCreateRoom(ctx, map[string]any{"title": "Design Review"})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	room := out.(map[string]any)["room"].(*Room)
	if room.ID == 0 {
		t.Fatal("room id not set")
	}
	return room
}

func TestCreateRoom_ReturnsHostTokenAndEmits(t *testing.T) {
	app, ctx, rec := newTestApp(t)
	out, err := app.toolCreateRoom(ctx, map[string]any{"title": "Design Review"})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	room := out.(map[string]any)["room"].(*Room)
	if room.Slug != "design-review" {
		t.Fatalf("slug=%q, want design-review", room.Slug)
	}
	if out.(map[string]any)["host_join_url"] == "" {
		t.Fatal("host_join_url should be returned")
	}
	events := rec.EventsByTopic("calls.room.created")
	if len(events) != 1 {
		t.Fatalf("room.created events=%d, want 1", len(events))
	}
	payload := events[0].Data.(map[string]any)
	if payload["project_id"] != "test-proj" {
		t.Fatalf("event project_id=%v", payload["project_id"])
	}
}

func TestAgentUsesSameJoinAndMessageFlow(t *testing.T) {
	app, ctx, rec := newTestApp(t)
	room := createRoom(t, app, ctx)
	tokenOut, err := app.toolCreateJoinToken(ctx, map[string]any{
		"room_id":          room.ID,
		"participant_kind": "agent",
		"role":             "observer",
		"display_name":     "Research Agent",
		"capabilities": map[string]any{
			"chat":            true,
			"transcript_read": true,
		},
	})
	if err != nil {
		t.Fatalf("create agent token: %v", err)
	}
	token := tokenOut.(map[string]any)["token"].(string)
	joinOut, err := app.toolJoinRoom(ctx, map[string]any{"token": token})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}
	p := joinOut.(map[string]any)["participant"].(*Participant)
	if p.Kind != "agent" {
		t.Fatalf("participant kind=%q, want agent", p.Kind)
	}
	if p.Role != "observer" {
		t.Fatalf("role=%q, want observer", p.Role)
	}
	msgOut, err := app.toolSendMessage(ctx, map[string]any{
		"room_id":        room.ID,
		"participant_id": p.ID,
		"body":           "I am listening.",
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}
	msg := msgOut.(map[string]any)["message"].(*Message)
	if msg.Body != "I am listening." {
		t.Fatalf("message body=%q", msg.Body)
	}
	joinEvents := rec.EventsByTopic("calls.participant.joined")
	if len(joinEvents) != 1 {
		t.Fatalf("participant.joined events=%d, want 1", len(joinEvents))
	}
	payload := joinEvents[0].Data.(map[string]any)
	if payload["participant_kind"] != "agent" {
		t.Fatalf("event participant_kind=%v", payload["participant_kind"])
	}
	if len(rec.EventsByTopic("calls.message.created")) != 1 {
		t.Fatalf("message.created not emitted")
	}
}

func TestTranscriptUsesUnifiedParticipant(t *testing.T) {
	app, ctx, rec := newTestApp(t)
	room := createRoom(t, app, ctx)
	tokenOut, _ := app.toolCreateJoinToken(ctx, map[string]any{"room_id": room.ID, "participant_kind": "human", "display_name": "Alice"})
	joinOut, _ := app.toolJoinRoom(ctx, map[string]any{"token": tokenOut.(map[string]any)["token"].(string)})
	p := joinOut.(map[string]any)["participant"].(*Participant)
	out, err := app.toolAppendTranscript(ctx, map[string]any{
		"room_id":        room.ID,
		"participant_id": p.ID,
		"text":           "We should ship the first slice.",
		"source":         "manual",
	})
	if err != nil {
		t.Fatalf("append transcript: %v", err)
	}
	item := out.(map[string]any)["transcript_item"].(*TranscriptItem)
	if item.SpeakerName != "Alice" {
		t.Fatalf("speaker=%q, want Alice", item.SpeakerName)
	}
	if len(rec.EventsByTopic("calls.transcript.created")) != 1 {
		t.Fatalf("transcript.created not emitted")
	}
}

func TestEndRoomClosesParticipants(t *testing.T) {
	app, ctx, rec := newTestApp(t)
	room := createRoom(t, app, ctx)
	tokenOut, _ := app.toolCreateJoinToken(ctx, map[string]any{"room_id": room.ID, "display_name": "Bob"})
	joinOut, _ := app.toolJoinRoom(ctx, map[string]any{"token": tokenOut.(map[string]any)["token"].(string)})
	p := joinOut.(map[string]any)["participant"].(*Participant)

	out, err := app.toolEndRoom(ctx, map[string]any{"id": room.ID})
	if err != nil {
		t.Fatalf("end room: %v", err)
	}
	ended := out.(map[string]any)["room"].(*Room)
	if ended.Status != "ended" {
		t.Fatalf("status=%q, want ended", ended.Status)
	}
	after, err := app.dbGetParticipant(ctx, "test-proj", room.ID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "left" {
		t.Fatalf("participant status=%q, want left", after.Status)
	}
	if len(rec.EventsByTopic("calls.room.ended")) != 1 {
		t.Fatalf("room.ended not emitted")
	}
}

func TestJoinTokenExhaustion(t *testing.T) {
	app, ctx, _ := newTestApp(t)
	room := createRoom(t, app, ctx)
	tokenOut, _ := app.toolCreateJoinToken(ctx, map[string]any{"room_id": room.ID, "max_uses": 1})
	token := tokenOut.(map[string]any)["token"].(string)
	if _, err := app.toolJoinRoom(ctx, map[string]any{"token": token}); err != nil {
		t.Fatalf("first join: %v", err)
	}
	if _, err := app.toolJoinRoom(ctx, map[string]any{"token": token}); err == nil {
		t.Fatal("second join should fail after max_uses=1")
	}
}
