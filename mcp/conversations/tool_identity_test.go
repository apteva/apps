package main

import (
	"context"
	"fmt"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestReusedToolCallIDAcrossMessagesAndInbox(t *testing.T) {
	a, app, _ := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	call := callerCtxCall(41, conversationThreadID(conv.ID), "call_0")
	form := interactionArgs("form", false)
	form["name"] = "form-card"
	// Existing installations already have rows with the old identity scheme.
	if _, err := a.store.AppendMessage(&Message{ConversationID: conv.ID, Role: "agent", AgentID: 41,
		ThreadID: conversationThreadID(conv.ID), Content: "Earlier answer", ClientID: "tool-call:41:call_0"}); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		tool func(context.Context, *sdk.AppCtx, map[string]any) (any, error)
		args map[string]any
	}{
		{a.toolSend, map[string]any{"text": "Searching now", "phase": "acknowledgement"}},
		{a.toolSend, map[string]any{"text": "Found the results", "phase": "progress"}},
		{a.toolSend, map[string]any{"text": "Here are five titles", "phase": "final"}},
		{a.toolReport, map[string]any{"title": "Search results", "summary": "Five titles verified"}},
		{a.toolAlert, map[string]any{"text": "One source is unavailable", "severity": "warn"}},
		{a.toolRequestApproval, map[string]any{"title": "Publish results", "body": "Approve publication?"}},
		{a.toolSend, map[string]any{"components": []any{interactionArgs("question", false)}}},
		{a.toolSend, map[string]any{"components": []any{form}}},
	}
	seen := map[any]bool{}
	for i, step := range steps {
		step.args["conversation_id"] = conv.ID
		first, err := step.tool(call, app, step.args)
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		id := first.(map[string]any)["message_id"]
		if seen[id] || first.(map[string]any)["inserted"] != true {
			t.Fatalf("step %d lost a distinct message: %v", i, first)
		}
		seen[id] = true
		// Reconstruct the store wrapper, so suppression cannot rely on RAM.
		a.store = newStore(a.store.db)
		retry, err := step.tool(call, app, step.args)
		if err != nil || retry.(map[string]any)["message_id"] != id || retry.(map[string]any)["duplicate_suppressed"] != true {
			t.Fatalf("step %d retry=%v err=%v", i, retry, err)
		}
	}
	rows, err := a.store.Transcript(conv.ID, 0, 100)
	if err != nil || len(rows) != len(steps)+1 {
		t.Fatalf("history rows=%d err=%v", len(rows), err)
	}
}

func TestReusedToolCallIDIdenticalReplyToLaterInput(t *testing.T) {
	a, app, _ := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	call := callerCtxCall(41, conversationThreadID(conv.ID), "call_0")
	args := map[string]any{"conversation_id": conv.ID, "text": "Done", "phase": "final"}
	var previous any
	for turn := 0; turn < 2; turn++ {
		if _, err := a.store.AppendMessage(&Message{ConversationID: conv.ID, Role: "user", UserID: 1, Content: "Check again"}); err != nil {
			t.Fatal(err)
		}
		out, err := a.toolSend(call, app, args)
		if err != nil || out.(map[string]any)["inserted"] != true || out.(map[string]any)["message_id"] == previous {
			t.Fatalf("turn %d: %v %v", turn, out, err)
		}
		previous = out.(map[string]any)["message_id"]
		retry, err := a.toolSend(call, app, args)
		if err != nil || retry.(map[string]any)["message_id"] != previous || retry.(map[string]any)["duplicate_suppressed"] != true {
			t.Fatalf("turn %d retry=%v err=%v", turn, retry, err)
		}
		// Accept queued input before another final. Final delivery can release
		// that input; changing queue state must not duplicate its retry.
		queued, err := a.store.AppendMessage(&Message{ConversationID: conv.ID, Role: "user", UserID: 1,
			Content: "Later", QueueBehavior: "queue", QueueState: "queued", ClientID: fmt.Sprint(turn)})
		if err != nil {
			t.Fatal(err)
		}
		finalArgs := map[string]any{"conversation_id": conv.ID, "text": "Previous task completed", "phase": "final"}
		final, err := a.toolSend(call, app, finalArgs)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.store.db.Exec(`UPDATE messages SET queue_state='released' WHERE id=?`, queued.ID); err != nil {
			t.Fatal(err)
		}
		retry, err = a.toolSend(call, app, finalArgs)
		if err != nil || retry.(map[string]any)["message_id"] != final.(map[string]any)["message_id"] || retry.(map[string]any)["duplicate_suppressed"] != true {
			t.Fatalf("turn %d release retry=%v err=%v", turn, retry, err)
		}
	}
}

func TestExplicitToolIdempotencyKeyStillRejectsConflicts(t *testing.T) {
	a, app, _ := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	call := callerCtxCall(41, conversationThreadID(conv.ID), "call_0")
	args := map[string]any{"conversation_id": conv.ID, "text": "Original", "idempotency_key": "intentional-key"}
	if _, err := a.toolSend(call, app, args); err != nil {
		t.Fatal(err)
	}
	args["text"] = "Different"
	if _, err := a.toolSend(call, app, args); err == nil {
		t.Fatal("explicit key accepted a different request")
	}
}
