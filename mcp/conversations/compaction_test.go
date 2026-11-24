package main

import (
	"encoding/json"
	"testing"
	"time"
)

// Prod 2026-11-24 17:52: a reply sat on "Thinking…" for 79 s while Core
// recovered from an over-budget request. The recovery must show as one
// running row that completes when the retried call starts, with the sizes.
func TestCompactionRecoveryShowsOneRowUntilRetry(t *testing.T) {
	a, _, _ := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	thread := conversationThreadID(conv.ID)
	at := time.Now().UTC()
	step := func(event, body string) {
		t.Helper()
		at = at.Add(10 * time.Second)
		if err := a.ingestCompaction(event, 41, thread, body, at); err != nil {
			t.Fatal(err)
		}
	}
	feed, cancel := a.hub.subscribeFrames(conv.ID)
	defer cancel()

	// Other provider errors are not compaction.
	step("llm.provider_error", `{"class":"rate_limited"}`)
	step("llm.provider_error", `{"class":"context_length_exceeded","error":"estimated input 102390 exceeds safe input budget 101581"}`)
	rows, _ := a.store.toolActivities(conv.ID)
	if len(rows) != 1 || rows[0].Name != compactionActivityName || rows[0].Status != "running" {
		t.Fatalf("rows=%+v, want one running compaction", rows)
	}
	select {
	case f := <-feed:
		if f.Activity == nil || f.Activity.Name != compactionActivityName {
			t.Fatalf("frame=%+v", f)
		}
	default:
		t.Fatal("no live compaction frame")
	}
	// Two failing passes stay one row; the sizes span the first and last pass.
	step("llm.context_recovery", `{"pass":0,"before_tokens_est":76950,"after_tokens_est":40000}`)
	step("llm.provider_error", `{"class":"context_length_exceeded"}`)
	step("llm.context_recovery", `{"pass":1,"before_tokens_est":40000,"after_tokens_est":24095}`)
	step("llm.start", `{"provider":"claude-subscription","model":"opus"}`)
	rows, _ = a.store.toolActivities(conv.ID)
	if len(rows) != 1 {
		t.Fatalf("rows=%+v, want one compaction row", rows)
	}
	row := rows[0]
	if row.Status != "completed" || row.Reason != "77k → 24k" || row.EndedAt == "" || row.DurationMs == nil || *row.DurationMs != 40000 {
		t.Fatalf("row=%+v (duration %v)", row, row.DurationMs)
	}
	// The next model call is ordinary work.
	step("llm.start", `{}`)
	if rows, _ = a.store.toolActivities(conv.ID); len(rows) != 1 || rows[0].Revision != 2 {
		t.Fatalf("rows=%+v", rows)
	}
}

func TestPlannedCompactionCompletesOnDone(t *testing.T) {
	a, _, _ := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	thread := conversationThreadID(conv.ID)
	at := time.Now().UTC()
	for _, ev := range []struct{ typ, body string }{
		{"llm.compaction_started", `{"before_tokens_est":90000}`},
		// A model call inside planned compaction does not end it.
		{"llm.start", `{}`},
		{"llm.compaction_done", `{"mode":"semantic","before_tokens_est":90000,"after_tokens_est":12000}`},
	} {
		at = at.Add(time.Second)
		if err := a.ingestCompaction(ev.typ, 41, thread, ev.body, at); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := a.store.toolActivities(conv.ID)
	if len(rows) != 1 || rows[0].Status != "completed" || rows[0].Reason != "90k → 12k" {
		t.Fatalf("rows=%+v", rows)
	}
}

func TestCompactionFailsWithTheTurn(t *testing.T) {
	a, _, _ := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	thread := conversationThreadID(conv.ID)
	at := time.Now().UTC()
	for _, typ := range []string{"llm.provider_error", "llm.error"} {
		at = at.Add(time.Second)
		if err := a.ingestCompaction(typ, 41, thread, `{"class":"context_length_exceeded"}`, at); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := a.store.toolActivities(conv.ID)
	if len(rows) != 1 || rows[0].Status != "failed" {
		t.Fatalf("rows=%+v, want a failed compaction", rows)
	}
}

// Main compacts during background work too. Only a compaction inside the
// response to the conversation main answers belongs in it.
func TestMainCompactionOnlyInsideOwnedResponse(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	mountedCtx = ctx
	app.streamer.telemetryConnected = true
	app.streamer.mainTelemetryConnected = true
	routed := mkAgentConversation(t, app, CreateConversationInput{Title: "Chat"})
	at := time.Now()
	step := func(typ, data string) {
		t.Helper()
		at = at.Add(time.Millisecond)
		if err := app.ingestCompaction(typ, routedAgent, mainThreadID, data, at); err != nil {
			t.Fatal(err)
		}
		app.streamer.Ingest(typ, routedAgent, mainThreadID, data, at)
	}
	overBudget := `{"class":"context_length_exceeded"}`
	// Background compaction before anyone writes.
	step("llm.provider_error", overBudget)
	step("llm.start", `{}`)
	postUserMessage(t, app, routed, "Check the run")
	at = time.Now()
	text, _ := platform.trackedEvents[0].Message.(string)
	received, _ := json.Marshal(map[string]string{"source": "console", "message": coreEventPreview(text)})
	step("event.received", string(received))
	step("llm.provider_error", overBudget)
	step("llm.start", `{}`)

	rows, err := app.store.toolActivities(routed.ID)
	if err != nil || len(rows) != 1 || rows[0].Name != compactionActivityName || rows[0].Status != "completed" {
		t.Fatalf("rows=%+v err=%v, want only the owned compaction", rows, err)
	}
}
