package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postQueueMessage(t *testing.T, app *App, conv *Conversation, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/messages?chat_id="+conv.ID, strings.NewReader(body))
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	app.handleMessages(rec, req)
	return rec
}

func TestQueueDefaultAndFIFORelease(t *testing.T) {
	app, _, platform := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	thread := conversationThreadID(conv.ID)
	app.streamer.emitAck(conv.ID, thread, 41)
	first := postQueueMessage(t, app, conv, `{"content":"first","client_message_id":"queue-1"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	var queued Message
	if err := json.NewDecoder(first.Body).Decode(&queued); err != nil {
		t.Fatal(err)
	}
	if queued.QueueBehavior != "queue" || queued.QueueState != "queued" {
		t.Fatalf("queued row=%+v", queued)
	}
	if got := len(platform.ensures); got != 0 {
		t.Fatalf("queued message dispatched while active: %d", got)
	}
	second := postQueueMessage(t, app, conv, `{"content":"second","client_message_id":"queue-2"}`)
	if second.Code != http.StatusOK {
		t.Fatalf("second status=%d", second.Code)
	}
	app.streamer.finishResponse(conv.ID, 41)
	if got := len(platform.ensures); got != 1 {
		t.Fatalf("release count=%d want 1", got)
	}
	var released Message
	if err := app.store.db.QueryRow(`SELECT queue_state FROM messages WHERE id=?`, queued.ID).Scan(&released.QueueState); err != nil {
		t.Fatal(err)
	}
	if released.QueueState != "released" {
		t.Fatalf("state=%s", released.QueueState)
	}
	if rows, err := app.store.queuedMessages(conv.ID, 41); err != nil || len(rows) != 1 {
		t.Fatalf("remaining queue=%d err=%v", len(rows), err)
	}
}

func TestLegacyBehaviorStillDispatchesWhileActive(t *testing.T) {
	app, _, platform := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	app.streamer.emitAck(conv.ID, conversationThreadID(conv.ID), 41)
	rec := postQueueMessage(t, app, conv, `{"content":"legacy","client_message_id":"legacy-1","next_message_behavior":"legacy"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if len(platform.ensures) != 1 {
		t.Fatalf("legacy dispatches=%d", len(platform.ensures))
	}
}

func TestQueueEditRemoveAndSteer(t *testing.T) {
	app, _, platform := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	thread := conversationThreadID(conv.ID)
	app.streamer.emitAck(conv.ID, thread, 41)
	rec := postQueueMessage(t, app, conv, `{"content":"draft","client_message_id":"queue-actions"}`)
	var msg Message
	if err := json.NewDecoder(rec.Body).Decode(&msg); err != nil {
		t.Fatal(err)
	}
	edit := httptest.NewRequest(http.MethodPatch, "/messages?chat_id="+conv.ID+"&id="+itoa(msg.ID), strings.NewReader(`{"content":"edited"}`))
	authorizeTestRequest(edit)
	editRec := httptest.NewRecorder()
	app.handleMessages(editRec, edit)
	if editRec.Code != http.StatusOK {
		t.Fatalf("edit=%d", editRec.Code)
	}
	steer := httptest.NewRequest(http.MethodPost, "/message-queue", strings.NewReader(`{"chat_id":"`+conv.ID+`","message_id":`+itoa(msg.ID)+`,"action":"steer"}`))
	authorizeTestRequest(steer)
	steerRec := httptest.NewRecorder()
	app.handleMessageQueue(steerRec, steer)
	if steerRec.Code != http.StatusOK {
		t.Fatalf("steer=%d body=%s", steerRec.Code, steerRec.Body.String())
	}
	if len(platform.ensures) != 1 {
		t.Fatalf("steer dispatches=%d", len(platform.ensures))
	}
	// A second queued row can be removed while the response is still active.
	removedRec := postQueueMessage(t, app, conv, `{"content":"remove me","client_message_id":"queue-remove"}`)
	var removed Message
	if err := json.NewDecoder(removedRec.Body).Decode(&removed); err != nil {
		t.Fatal(err)
	}
	remove := httptest.NewRequest(http.MethodDelete, "/messages?chat_id="+conv.ID+"&id="+itoa(removed.ID), nil)
	authorizeTestRequest(remove)
	removeRec := httptest.NewRecorder()
	app.handleMessages(removeRec, remove)
	if removeRec.Code != http.StatusOK {
		t.Fatalf("remove=%d", removeRec.Code)
	}
	var state string
	if err := app.store.db.QueryRow(`SELECT queue_state FROM messages WHERE id=?`, removed.ID).Scan(&state); err != nil || state != "cancelled" {
		t.Fatalf("removed state=%q err=%v", state, err)
	}
	var deliveryState string
	if err := app.store.db.QueryRow(`SELECT status FROM deliveries WHERE message_id=? AND target='agent-inbound:41'`, removed.ID).Scan(&deliveryState); err != nil || deliveryState != "cancelled" {
		t.Fatalf("removed delivery=%q err=%v", deliveryState, err)
	}
}

func itoa(value int64) string { return fmt.Sprint(value) }
