package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestMainApprovalAcknowledgementIsScopedAndIdempotent(t *testing.T) {
	for _, verdict := range []string{"approve", "deny"} {
		t.Run(verdict, func(t *testing.T) {
			app, ctx, platform := newTestEnv(t)
			conv := mkConversation(t, app, 41)
			out, err := app.toolRequestApproval(callerCtx(41, "main"), ctx, map[string]any{"conversation_id": conv.ID, "title": "Proceed?"})
			if err != nil {
				t.Fatal(err)
			}
			id := out.(map[string]any)["message_id"].(int64)
			args := map[string]any{"conversation_id": conv.ID, "text": "Decision received", "phase": "acknowledgement", "approval_message_id": float64(id)}
			if _, err := app.toolSend(callerCtx(41, "main"), ctx, args); err == nil {
				t.Fatal("pending approval receipt accepted")
			}
			msg, _ := app.store.GetMessage(id)
			if _, err := app.resolveApproval(ctx, msg, verdict, "", 1); err != nil {
				t.Fatal(err)
			}
			if len(platform.trackedEvents) != 1 || !strings.Contains(fmt.Sprint(platform.trackedEvents[0].Message), fmt.Sprintf("approval_message_id=%d", id)) {
				t.Fatal("main verdict lacks explicit acknowledgement instructions")
			}
			for _, phase := range []string{"progress", "final"} {
				args["phase"] = phase
				if _, err := app.toolSend(callerCtx(41, "main"), ctx, args); err == nil {
					t.Fatalf("ordinary %s allowed", phase)
				}
			}
			args["phase"] = "acknowledgement"
			other := mkConversation(t, app, 41)
			args["conversation_id"] = other.ID
			if _, err := app.toolSend(callerCtx(41, "main"), ctx, args); err == nil {
				t.Fatal("cross-conversation receipt accepted")
			}
			args["conversation_id"] = conv.ID
			if err := app.store.AddAgentParticipant(conv.ID, 42); err != nil {
				t.Fatal(err)
			}
			if _, err := app.toolSend(callerCtx(42, "main"), ctx, args); err == nil {
				t.Fatal("another participant acknowledged approval")
			}
			for i := 0; i < 2; i++ {
				result, err := app.toolSend(callerCtxCall(41, "main", fmt.Sprintf("call-%d", i)), ctx, args)
				if err != nil {
					t.Fatal(err)
				}
				if result.(map[string]any)["inserted"] != (i == 0) {
					t.Fatalf("receipt idempotency: %v", result)
				}
			}
			rows, _ := app.store.Transcript(conv.ID, 0, 20)
			if len(rows) != 2 || rows[1].Phase != "acknowledgement" {
				t.Fatalf("transcript=%+v", rows)
			}
		})
	}
}

func TestMainCannotAcknowledgeAnotherThreadApproval(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	out, err := app.toolRequestApproval(boundConversationCaller(t, app, conv, 41), ctx, map[string]any{"conversation_id": conv.ID, "title": "Proceed?"})
	if err != nil {
		t.Fatal(err)
	}
	id := out.(map[string]any)["message_id"].(int64)
	msg, _ := app.store.GetMessage(id)
	if _, err := app.resolveApproval(ctx, msg, "approve", "", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolSend(callerCtx(41, "main"), ctx, map[string]any{"conversation_id": conv.ID, "text": "Received", "phase": "acknowledgement", "approval_message_id": float64(id)}); err == nil {
		t.Fatal("main acknowledged another thread's approval")
	}
}
