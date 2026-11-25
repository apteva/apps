package main

import (
	"encoding/json"
	"testing"
	"time"
)

// Prod 2026-11-25 (Management, agent 56 on main): answering a blocking form
// sent component.result to main without opening a response. The thread showed
// no Thinking for 26s and dropped 15 of 16 archive calls, because main's
// telemetry only belongs to a conversation through an open response.
func TestAnsweredFormShowsMainWorkInTheConversation(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	app.streamer.telemetryConnected = true
	app.streamer.mainTelemetryConnected = true
	conv := mkAgentConversation(t, app, CreateConversationInput{Title: "Management"})
	postUserMessage(t, app, conv, "Can you archive all assignments?")
	at := time.Now()
	step := func(typ, data string) {
		t.Helper()
		at = at.Add(time.Millisecond)
		if err := app.ingestToolActivity(typ, routedAgent, mainThreadID, data, at); err != nil {
			t.Fatal(err)
		}
		app.streamer.Ingest(typ, routedAgent, mainThreadID, data, at)
	}
	receive := func(event int) {
		t.Helper()
		text, _ := platform.trackedEvents[event].Message.(string)
		received, _ := json.Marshal(map[string]string{"source": "console", "message": coreEventPreview(text)})
		step("event.received", string(received))
		step("llm.start", `{}`)
	}
	receive(0)
	out, err := app.toolSend(callerCtxCall(routedAgent, mainThreadID, "form"), ctx, map[string]any{
		"conversation_id": conv.ID, "text": "Which option do you want?", "phase": "final",
		"components": []map[string]any{interactionArgs("tone", true)}})
	if err != nil {
		t.Fatal(err)
	}
	form, _ := app.store.GetMessage(out.(map[string]any)["message_id"].(int64))
	if app.streamer.hasActiveResponse(conv.ID, routedAgent) {
		t.Fatal("blocking form left the response open")
	}

	frames, cancel := app.hub.subscribeFrames(conv.ID)
	defer cancel()
	if _, err := app.resolveComponent(ctx, form, "tone", "submit", map[string]any{"tone": "detailed"}, 1, false); err != nil {
		t.Fatal(err)
	}
	// Core's events follow the answer: the response opened at the answer.
	at = time.Now()
	if len(platform.trackedEvents) != 2 {
		t.Fatalf("answer not delivered: %d events", len(platform.trackedEvents))
	}
	select {
	case frame := <-frames:
		if frame.Phase != "acknowledgement" || frame.Progress == nil || frame.Progress.Phase != "thinking" || frame.AfterMessageID < form.ID {
			t.Fatalf("answer did not start Thinking: %+v", frame)
		}
	default:
		t.Fatal("no response opened for the answer")
	}

	// Background work before Core takes the answer is not this conversation's.
	step("tool.call", `{"id":"background-1","name":"processes_assignments"}`)
	receive(1)
	if _, err := app.toolSend(callerCtxCall(routedAgent, mainThreadID, "ack"), ctx, map[string]any{
		"conversation_id": conv.ID, "text": "Confirmed. Archiving now.", "phase": "acknowledgement"}); err != nil {
		t.Fatal(err)
	}
	step("llm.start", `{}`)
	if !app.streamer.hasActiveResponse(conv.ID, routedAgent) {
		t.Fatal("Thinking ended while main was still working")
	}
	// One model turn writes a parallel batch.
	for _, id := range []string{"archive-1", "archive-2", "archive-3"} {
		step("tool.call", `{"id":"`+id+`","name":"processes_assignment_status"}`)
	}
	for _, id := range []string{"archive-1", "archive-2", "archive-3"} {
		step("tool.result", `{"id":"`+id+`","name":"processes_assignment_status","success":true}`)
	}
	if _, err := app.toolSend(callerCtxCall(routedAgent, mainThreadID, "final"), ctx, map[string]any{
		"conversation_id": conv.ID, "text": "All three archived.", "phase": "final"}); err != nil {
		t.Fatal(err)
	}
	rows, err := app.store.toolActivities(conv.ID)
	if err != nil || len(rows) != 3 {
		t.Fatalf("activity rows=%+v err=%v, want the three archive calls", rows, err)
	}
	for _, row := range rows {
		if row.Status != "completed" || row.CallID == "background-1" {
			t.Fatalf("unexpected row %+v", row)
		}
	}
	if app.streamer.hasActiveResponse(conv.ID, routedAgent) {
		t.Fatal("final reply did not settle the answer's response")
	}
}

func TestAnswerDeliveryFailureOrDuplicateLeavesNoThinking(t *testing.T) {
	for _, mode := range []string{"failed", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			app, ctx, platform := newTestEnv(t)
			conv := mkAgentConversation(t, app, CreateConversationInput{Title: "Management"})
			postUserMessage(t, app, conv, "Can you archive all assignments?")
			out, err := app.toolSend(callerCtxCall(routedAgent, mainThreadID, "form"), ctx, map[string]any{
				"conversation_id": conv.ID, "text": "Pick one.", "phase": "final",
				"components": []map[string]any{interactionArgs("tone", true)}})
			if err != nil {
				t.Fatal(err)
			}
			form, _ := app.store.GetMessage(out.(map[string]any)["message_id"].(int64))
			platform.failSend = mode == "failed"
			platform.duplicateSend = mode == "duplicate"
			if _, err := app.resolveComponent(ctx, form, "tone", "submit", map[string]any{"tone": "concise"}, 1, false); err != nil {
				t.Fatal(err)
			}
			if app.streamer.hasActiveResponse(conv.ID, routedAgent) {
				t.Fatal("undelivered answer left Thinking on")
			}
		})
	}
}

// A non-blocking card answered while the agent is still on its response joins
// that turn; replacing the response would hide its work until Core's receipt.
func TestAnswerDuringRunningResponseKeepsIt(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	conv := mkAgentConversation(t, app, CreateConversationInput{Title: "Management"})
	postUserMessage(t, app, conv, "Draft the weekly summary")
	out, err := app.toolSend(callerCtxCall(routedAgent, mainThreadID, "question"), ctx, map[string]any{
		"conversation_id": conv.ID, "text": "Using concise unless you say otherwise.", "phase": "progress",
		"components": []map[string]any{interactionArgs("tone", false)}})
	if err != nil {
		t.Fatal(err)
	}
	card, _ := app.store.GetMessage(out.(map[string]any)["message_id"].(int64))
	running := app.streamer.responses[responseProgressKey(conv.ID, routedAgent)]
	if running == nil || running.Phase == "idle" {
		t.Fatal("progress message should keep the operator's response running")
	}
	before := running.RunID
	if _, err := app.resolveComponent(ctx, card, "tone", "submit", map[string]any{"tone": "detailed"}, 1, false); err != nil {
		t.Fatal(err)
	}
	if len(platform.trackedEvents) != 2 {
		t.Fatal("answer not delivered")
	}
	if p := app.streamer.responses[responseProgressKey(conv.ID, routedAgent)]; p == nil || p.RunID != before {
		t.Fatalf("running response replaced: %+v", p)
	}
}
