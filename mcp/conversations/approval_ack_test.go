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

func TestApprovalCardSettlesOnlyRequestingAgentAck(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	caller := boundConversationCaller(t, app, conv, 41)
	frames, cancel := app.hub.subscribeFrames(conv.ID)
	defer cancel()
	app.streamer.emitAck(conv.ID, conversationThreadID(conv.ID), 41)
	app.streamer.emitAck(conv.ID, conversationThreadID(conv.ID), 42)
	first := <-frames
	second := <-frames
	if _, err := app.toolRequestApproval(caller, ctx, map[string]any{"conversation_id": conv.ID, "title": "Confirm deleting patch repository"}); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-frames:
		if !frame.Done || frame.CallID != first.CallID || frame.AgentID != 41 {
			t.Fatalf("unexpected settlement: %+v", frame)
		}
	default:
		t.Fatal("approval left thinking acknowledgement active")
	}
	// The response lifecycle also terminates; it is separate from ack frames.
	select {
	case frame := <-frames:
		if frame.Progress == nil || frame.Progress.Phase != "idle" || frame.AgentID != 41 {
			t.Fatalf("missing idle progress: %+v", frame)
		}
	default:
		t.Fatal("response progress stayed active")
	}
	// Another room participant remains active.
	app.streamer.settleAck(conv.ID, 42)
	select {
	case frame := <-frames:
		if !frame.Done || frame.CallID != second.CallID {
			t.Fatalf("other agent acknowledgement lost: %+v", frame)
		}
	default:
		t.Fatal("other agent acknowledgement was cleared")
	}
}

func TestApprovalVerdictStartsNewResponseIndicator(t *testing.T) {
	for _, verdict := range []string{"approve", "deny"} {
		t.Run(verdict, func(t *testing.T) {
			app, ctx, platform := newTestEnv(t)
			conv := mkConversation(t, app, 41)
			caller := boundConversationCaller(t, app, conv, 41)
			out, err := app.toolRequestApproval(caller, ctx, map[string]any{"conversation_id": conv.ID, "title": "Proceed?"})
			if err != nil {
				t.Fatal(err)
			}
			approval, _ := app.store.GetMessage(out.(map[string]any)["message_id"].(int64))
			later, err := app.store.AppendMessage(&Message{ConversationID: conv.ID, Role: "agent", AgentID: 41, Content: "Additional details"})
			if err != nil {
				t.Fatal(err)
			}
			frames, cancel := app.hub.subscribeFrames(conv.ID)
			defer cancel()
			if _, err := app.resolveApproval(ctx, approval, verdict, "", 1); err != nil {
				t.Fatal(err)
			}
			if len(platform.trackedEvents) != 1 {
				t.Fatal("verdict not delivered")
			}
			select {
			case frame := <-frames:
				if frame.Done || frame.Phase != "acknowledgement" || frame.AgentID != 41 || frame.ThreadID != conversationThreadID(conv.ID) || frame.AfterMessageID != later.ID {
					t.Fatalf("invalid resumed frame: %+v", frame)
				}
			default:
				t.Fatal("no thinking indicator after verdict")
			}
			if _, err := app.toolSend(caller, ctx, map[string]any{"conversation_id": conv.ID, "text": "Decision received"}); err != nil {
				t.Fatal(err)
			}
			select {
			case frame := <-frames:
				if !frame.Done {
					t.Fatalf("reply did not finish indicator: %+v", frame)
				}
			default:
				t.Fatal("missing completion frame")
			}
		})
	}
}

func TestFailedApprovalDeliveryDoesNotStartThinking(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	caller := boundConversationCaller(t, app, conv, 41)
	out, err := app.toolRequestApproval(caller, ctx, map[string]any{"conversation_id": conv.ID, "title": "Proceed?"})
	if err != nil {
		t.Fatal(err)
	}
	approval, _ := app.store.GetMessage(out.(map[string]any)["message_id"].(int64))
	platform.failSend = true
	frames, cancel := app.hub.subscribeFrames(conv.ID)
	defer cancel()
	if _, err := app.resolveApproval(ctx, approval, "approve", "", 1); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-frames:
		t.Fatalf("failed delivery claimed agent activity: %+v", frame)
	default:
	}
}
