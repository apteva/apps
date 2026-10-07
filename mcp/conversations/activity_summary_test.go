package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestActivitySummaryFollowsResponseAndVisibility(t *testing.T) {
	app, _, _ := newTestEnv(t)
	first := mkConversation(t, app, 41)
	second := mkConversation(t, app, 42)
	otherProject, err := app.store.CreateConversation(CreateConversationInput{
		ProjectID: "proj-other", LeadAgentID: 41, Title: "Other project", OwnerUserID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	app.streamer.emitAck(first.ID, conversationThreadID(first.ID), 41)
	app.streamer.emitAck(second.ID, conversationThreadID(second.ID), 42)
	app.streamer.emitAck(otherProject.ID, conversationThreadID(otherProject.ID), 41)
	wantProject := []string{first.ID, second.ID}
	slices.Sort(wantProject)

	get := func(path string, user string) (int, []string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if user != "" {
			req.Header.Set("X-User-ID", user)
		}
		req.Header.Set("X-Apteva-Project-ID", testProject)
		rec := httptest.NewRecorder()
		app.handleActivitySummary(rec, req)
		var body struct {
			IDs []string `json:"active_conversation_ids"`
		}
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, body.IDs
	}
	if status, ids := get("/activity-summary", "1"); status != http.StatusOK || !reflect.DeepEqual(ids, wantProject) {
		t.Fatalf("project activity: status=%d ids=%v", status, ids)
	}
	if status, ids := get("/activity-summary?agent_id=41", "1"); status != http.StatusOK || !reflect.DeepEqual(ids, []string{first.ID}) {
		t.Fatalf("agent scope: status=%d ids=%v", status, ids)
	}
	if status, ids := get("/activity-summary", "2"); status != http.StatusOK || len(ids) != 0 {
		t.Fatalf("other user: status=%d ids=%v", status, ids)
	}
	if status, _ := get("/activity-summary", ""); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", status)
	}
	if status, _ := get("/activity-summary?agent_id=nope", "1"); status != http.StatusBadRequest {
		t.Fatalf("invalid agent scope status=%d", status)
	}
	app.streamer.Ingest("tool.call", 41, conversationThreadID(first.ID), `{"name":"processes_list"}`, time.Now())
	if status, ids := get("/activity-summary?agent_id=41", "1"); status != http.StatusOK || !reflect.DeepEqual(ids, []string{first.ID}) {
		t.Fatalf("running tool activity: status=%d ids=%v", status, ids)
	}
	app.streamer.finishResponse(first.ID, 41)
	if status, ids := get("/activity-summary", "1"); status != http.StatusOK || !reflect.DeepEqual(ids, []string{second.ID}) {
		t.Fatalf("settled activity: status=%d ids=%v", status, ids)
	}
}

func TestFinalSendKeepsTranscriptAndListIdleThroughPacingDecision(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	caller := boundConversationCaller(t, app, conv, 41)
	thread := conversationThreadID(conv.ID)
	req := httptest.NewRequest(http.MethodGet, "/stream?scope=user", nil)
	authorizeTestRequest(req)
	app.streamer.emitAck(conv.ID, thread, 41, 10)
	start := time.Now()
	app.streamer.Ingest("llm.start", 41, thread, `{}`, start)
	if _, err := app.toolSend(caller, ctx, map[string]any{
		"conversation_id": conv.ID, "text": "You're welcome!", "phase": "final",
	}); err != nil {
		t.Fatal(err)
	}
	assertIdle := func() {
		t.Helper()
		if got := app.streamer.snapshot(conv.ID); len(got.Frames) != 0 {
			t.Fatalf("transcript is active after final: %+v", got)
		}
		if got := app.userActivitySnapshot(req, 0); len(got.Frames) != 0 {
			t.Fatalf("list is active after final: %+v", got)
		}
		if ids := app.visibleActivityIDs(req, 0); len(ids) != 0 {
			t.Fatalf("activity-summary is active after final: %v", ids)
		}
	}
	assertIdle()
	app.streamer.Ingest("llm.start", 41, thread, `{"iteration":2}`, start.Add(time.Second))
	assertIdle()
	app.streamer.Ingest("tool.call", 41, thread, `{"name":"pace"}`, start.Add(8*time.Second))
	assertIdle()
	// A later external event still lights both surfaces, despite old history.
	app.streamer.Ingest("event.received", 41, thread, `{"message":"[subscription:todos] changed"}`, start.Add(9*time.Second))
	app.streamer.Ingest("llm.start", 41, thread, `{}`, start.Add(10*time.Second))
	if got := app.streamer.snapshot(conv.ID); len(got.Frames) != 1 || got.Frames[0].Progress.Phase != "thinking" {
		t.Fatalf("new event missing transcript progress: %+v", got)
	}
	if got := app.userActivitySnapshot(req, 0); len(got.Frames) != 1 || got.Frames[0].Progress.Phase != "thinking" {
		t.Fatalf("new event missing list progress: %+v", got)
	}
}

func TestUserSSEReusesStreamProgressForScopedThreadActivity(t *testing.T) {
	app, _, _ := newTestEnv(t)
	first := mkConversation(t, app, 41)
	second := mkConversation(t, app, 42)
	otherProject, err := app.store.CreateConversation(CreateConversationInput{
		ProjectID: "proj-other", LeadAgentID: 41, Title: "Other project", OwnerUserID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/stream?scope=user&agent_id=41", nil)
	authorizeTestRequest(request)
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	writer := newRecordingSSEWriter()
	done := make(chan struct{})
	go func() {
		app.handleStream(writer, request.WithContext(ctx))
		close(done)
	}()
	select {
	case <-writer.flushed:
	case <-time.After(2 * time.Second):
		t.Fatal("user stream did not send its initial snapshot")
	}
	if body := writer.String(); !strings.Contains(body, `"snapshot":true`) {
		t.Fatalf("missing initial activity snapshot: %s", body)
	}

	app.streamer.emitAck(second.ID, conversationThreadID(second.ID), 42)
	app.streamer.emitAck(otherProject.ID, conversationThreadID(otherProject.ID), 41)
	app.streamer.emitAck(first.ID, conversationThreadID(first.ID), 41)
	waitFor := func(match func(string) bool, label string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if match(writer.String()) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s: %s", label, writer.String())
	}
	waitFor(func(body string) bool { return strings.Contains(body, `"chat_id":"`+first.ID+`"`) }, "receipt activity")
	if body := writer.String(); strings.Contains(body, second.ID) || strings.Contains(body, otherProject.ID) {
		t.Fatalf("user stream leaked another agent or project: %s", body)
	}

	app.streamer.finishResponse(first.ID, 41)
	waitFor(func(body string) bool {
		return strings.Count(body, "event: stream\ndata: ") >= 3
	}, "settled activity")
	frames := []StreamFrame{}
	for _, block := range strings.Split(writer.String(), "\n\n") {
		if !strings.HasPrefix(block, "event: stream\ndata: ") {
			continue
		}
		var frame StreamFrame
		if err := json.Unmarshal([]byte(strings.TrimPrefix(block, "event: stream\ndata: ")), &frame); err != nil {
			t.Fatal(err)
		}
		if frame.ConversationID == first.ID {
			frames = append(frames, frame)
		}
	}
	if len(frames) < 2 || len(frames[0].Frames) == 0 || len(frames[len(frames)-1].Frames) != 0 {
		t.Fatalf("activity did not run from receipt through settlement: %+v", frames)
	}
	if strings.Contains(writer.String(), "\nid:") || strings.Contains(writer.String(), `"text":"secret"`) {
		t.Fatal("ephemeral list activity acquired a durable cursor or leaked transcript text")
	}

	otherUser := httptest.NewRequest(http.MethodGet, "/stream?scope=user", nil)
	otherUser.Header.Set("X-User-ID", "2")
	otherUser.Header.Set("X-Apteva-Project-ID", testProject)
	app.streamer.emitAck(first.ID, conversationThreadID(first.ID), 41)
	if snapshot := app.userActivitySnapshot(otherUser, 0); len(snapshot.Frames) != 0 {
		t.Fatalf("other user's snapshot leaked activity: %+v", snapshot)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("user stream did not close")
	}
}

func TestListProgressWaitsForEveryAgentToSettle(t *testing.T) {
	app, _, _ := newTestEnv(t)
	conversation := mkConversation(t, app, 41)
	thread := conversationThreadID(conversation.ID)
	app.streamer.emitAck(conversation.ID, thread, 41)
	app.streamer.emitAck(conversation.ID, thread, 42)
	if got := len(app.listProgressSnapshot(conversation.ID).Frames); got != 2 {
		t.Fatalf("expected both active agents, got %d", got)
	}
	app.streamer.finishResponse(conversation.ID, 41)
	if got := len(app.listProgressSnapshot(conversation.ID).Frames); got != 1 {
		t.Fatalf("settling one agent hid the other: %d active frames", got)
	}
	app.streamer.finishResponse(conversation.ID, 42)
	if got := len(app.listProgressSnapshot(conversation.ID).Frames); got != 0 {
		t.Fatalf("settled conversation still active: %d frames", got)
	}
}
