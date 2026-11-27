package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// Timings, event ordering, tool identities and chunk counts from the reported
// Processes conversation. User text/IDs/arguments are replaced with fixtures.
func TestProcessesTelemetryReplay(t *testing.T) {
	data, err := os.ReadFile("testdata/process-lifecycle.json")
	if err != nil {
		t.Fatal(err)
	}
	var events []struct {
		MS   int             `json:"ms"`
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &events); err != nil {
		t.Fatal(err)
	}
	s := newStreamer(newHub())
	s.resolve = func(int64, string) string { return "conv-replay" }
	s.emitAck("conv-replay", "chat-conv-replay", 41, 863)
	var phase string
	var texts []string
	s.onFrame = func(f StreamFrame) {
		if f.Progress != nil {
			phase = f.Progress.Phase
		}
		if f.Text != "" {
			texts = append(texts, f.Text)
		}
	}
	start := time.Now()
	completed := 0
	for _, event := range events {
		var d struct {
			Name string `json:"name"`
			ID   string `json:"id"`
			Args struct {
				Phase string `json:"phase"`
				Text  string `json:"text"`
			} `json:"args"`
		}
		if err := json.Unmarshal(event.Data, &d); err != nil {
			t.Fatal(err)
		}
		s.Ingest(event.Type, 41, "chat-conv-replay", string(event.Data), start.Add(time.Duration(event.MS)*time.Millisecond))
		if event.Type == "tool.call" && d.Name == "conversations_send" {
			if len(texts) == 0 || texts[len(texts)-1] != d.Args.Text {
				t.Fatalf("incomplete streamed %s: %v", d.Args.Phase, texts)
			}
			s.settleAck("conv-replay", 41)
			if d.Args.Phase == "final" {
				s.finishResponse("conv-replay", 41)
			} else {
				s.intermediateReply("conv-replay", 41)
				if phase != "thinking" {
					t.Fatal("acknowledgement did not immediately restore Thinking")
				}
			}
		}
		if event.Type == "tool.result" && visibleActivityTool(d.Name) {
			completed++
			if phase != "continuing" {
				t.Fatalf("completed call still owns response: %s", phase)
			}
			// Reordered delivery of an older preparation event cannot regress.
			s.Ingest("llm.tool_chunk", 41, "chat-conv-replay", `{"tool":"`+d.Name+`","id":"`+d.ID+`"}`, start.Add(time.Duration(event.MS-100)*time.Millisecond))
			if phase != "continuing" {
				t.Fatal("late preparation regressed completed call")
			}
		}
	}
	if completed != 3 || phase != "idle" {
		t.Fatalf("completed=%d phase=%s", completed, phase)
	}
	if len(texts) < 10 {
		t.Fatalf("lost incremental streaming: %d frames", len(texts))
	}
	if got := s.snapshot("conv-replay"); !got.Snapshot || len(got.Frames) != 0 {
		t.Fatalf("finished response survived reconnect: %+v", got)
	}
}

func TestResponseSnapshotScopeAndText(t *testing.T) {
	s := newStreamer(newHub())
	s.emitAck("conv-one", "chat-conv-one", 41, 10)
	s.emitAck("conv-two", "chat-conv-two", 42, 20)
	ts := time.Now()
	s.Ingest("llm.tool_chunk", 41, "chat-conv-one", `{"tool":"conversations_send","id":"c1","chunk":"{\"conversation_id\":\"conv-one\",\"text\":\"Working"}`, ts)
	got := s.snapshot("conv-one")
	if !got.Snapshot || len(got.Frames) != 2 {
		t.Fatalf("missing progress or text: %+v", got)
	}
	for _, f := range got.Frames {
		if f.ConversationID != "conv-one" || f.AgentID != 41 {
			t.Fatalf("cross-scope snapshot: %+v", f)
		}
	}
	text := got.Frames[1]
	if text.Text != "Working" || !text.CreatedAt.Equal(ts) || text.AfterMessageID != 10 {
		t.Fatalf("lost stream anchor: %+v", text)
	}
}

func TestResponseProgressLifecycle(t *testing.T) {
	s := newStreamer(newHub())
	var frames []StreamFrame
	s.onFrame = func(f StreamFrame) { frames = append(frames, f) }
	ingest := func(kind, raw string) { s.Ingest(kind, 41, "chat-conv-progress", raw, time.Now()) }
	ingest("llm.start", `{}`)
	if len(frames) != 0 {
		t.Fatal("background work was displayed")
	}
	// Production resolves mapped threads, including proactive llm.start.
	s.resolve = func(int64, string) string { return "conv-progress" }
	s.emitAck("conv-progress", "chat-conv-progress", 41, 70)
	phase := func(want string) {
		t.Helper()
		f := frames[len(frames)-1]
		if f.Progress == nil || f.Progress.Phase != want {
			t.Fatalf("want %s, got %+v", want, f)
		}
	}
	ingest("llm.start", `{}`)
	phase("thinking")
	beforeDiscovery := len(frames)
	for _, event := range []string{"llm.tool_chunk", "tool.call", "tool.result"} {
		ingest(event, `{"name":"search_tools","id":"discovery","chunk":"query"}`)
	}
	if len(frames) != beforeDiscovery {
		t.Fatal("internal lookup changed visible response progress")
	}
	ingest("llm.tool_chunk", `{"tool":"code_repos_list","id":"c1","chunk":"private arguments"}`)
	phase("preparing_tool")
	n := len(frames)
	ingest("llm.tool_chunk", `{"tool":"code_repos_list","id":"c1","chunk":"more"}`)
	if len(frames) != n {
		t.Fatal("preparation flooded by argument deltas")
	}
	ingest("tool.call", `{"name":"code_repos_list","id":"c1"}`)
	phase("running")
	if got := frames[len(frames)-1].Progress; got == nil || got.ToolName != "code_repos_list" || got.CallID != "c1" {
		t.Fatalf("running progress lost tool identity: %+v", got)
	}
	ingest("tool.result", `{"name":"code_repos_list","id":"c1"}`)
	phase("continuing")
	ingest("llm.start", `{}`)
	phase("continuing")
	s.finishResponse("conv-progress", 41)
	phase("idle")
	n = len(frames)
	ingest("llm.start", `{}`)
	if len(frames) != n {
		t.Fatal("post-response housekeeping resumed animation")
	}
	s.emitAck("conv-progress", "chat-conv-progress", 41, 71)
	s.intermediateReply("conv-progress", 41)
	phase("thinking")
	ingest("llm.start", `{}`)
	phase("thinking")
	ingest("tool.call", `{"name":"pace"}`)
	phase("idle")

	// A progress message can be followed directly by a long pace without a
	// second llm.start. The pace settles this response instead of leaving a
	// stale Thinking row visible for the whole sleep.
	s.emitAck("conv-progress", "chat-conv-progress", 41, 72)
	s.intermediateReply("conv-progress", 41)
	phase("thinking")
	ingest("tool.call", `{"name":"pace"}`)
	phase("idle")
}

func TestFinalReplyDoesNotRestartActivityForHousekeeping(t *testing.T) {
	s := newStreamer(newHub())
	chat, thread := "conv-final", "chat-conv-final"
	s.resolve = func(int64, string) string { return chat }
	var listSnapshots []StreamFrame
	s.onActivityChange = func(id string) { listSnapshots = append(listSnapshots, s.snapshot(id)) }
	s.emitAck(chat, thread, 41, 1311)
	start := time.Now()
	s.Ingest("llm.start", 41, thread, `{"iteration":1}`, start)
	s.finishResponse(chat, 41)
	settledCount := len(listSnapshots)
	// Reported trace: final saved, 24 ms later an extra model pass, then
	// pacing after 7.5 seconds. List and transcript must both remain idle.
	s.Ingest("llm.start", 41, thread, `{"iteration":2}`, start.Add(24*time.Millisecond))
	if ids := s.activeConversationIDs(); len(ids) != 0 {
		t.Fatalf("final reply restarted list activity: %v", ids)
	}
	if snapshot := s.snapshot(chat); len(snapshot.Frames) != 0 {
		t.Fatalf("final reply restarted transcript progress: %+v", snapshot)
	}
	s.Ingest("tool.call", 41, thread, `{"name":"pace","id":"waiting"}`, start.Add(7500*time.Millisecond))
	if len(listSnapshots) != settledCount {
		t.Fatalf("housekeeping republished list activity: %+v", listSnapshots[settledCount:])
	}
	// A delayed input from the completed response cannot claim a new turn.
	s.Ingest("event.received", 41, thread, `{"message":"[chat] old"}`, start.Add(-time.Second))
	s.Ingest("llm.start", 41, thread, `{}`, start.Add(7600*time.Millisecond))
	if s.responseActive(chat, 41) {
		t.Fatal("stale received event restarted response")
	}
	// A real subscription event on the same thread still starts feedback.
	s.Ingest("event.received", 41, thread, `{"message":"[subscription:todos] updated"}`, start.Add(8*time.Second))
	s.Ingest("llm.start", 41, thread, `{}`, start.Add(9*time.Second))
	if !s.responseActive(chat, 41) || len(s.activeConversationIDs()) != 1 {
		t.Fatal("fresh proactive event did not restart shared progress")
	}
}

func TestCompletedPaceAllowsNextTimerWake(t *testing.T) {
	s := newStreamer(newHub())
	chat, thread := "conv-timer", "chat-conv-timer"
	s.resolve = func(int64, string) string { return chat }
	s.emitAck(chat, thread, 41, 10)
	start := time.Now()
	s.Ingest("llm.start", 41, thread, `{}`, start)
	s.finishResponse(chat, 41)
	s.Ingest("llm.start", 41, thread, `{}`, start.Add(time.Second))
	s.Ingest("tool.call", 41, thread, `{"name":"pace","id":"sleep"}`, start.Add(2*time.Second))
	s.Ingest("tool.result", 41, thread, `{"name":"pace","id":"sleep","success":true}`, start.Add(3*time.Second))
	// Core's timer-only wakes have no event.received; the previous cycle
	// really ended when pace completed, so the next model start is new work.
	s.Ingest("llm.start", 41, thread, `{}`, start.Add(time.Minute))
	if !s.responseActive(chat, 41) {
		t.Fatal("timer wake lost visible progress")
	}
	s.finishResponse(chat, 41)
	// Completion is scoped to this agent/thread, never to the whole chat.
	s.Ingest("llm.start", 42, "subscription-worker", `{}`, start.Add(2*time.Minute))
	if !s.responseActive(chat, 42) || s.responseActive(chat, 41) {
		t.Fatal("completed agent suppressed another participant's response")
	}
}

func TestProactiveStartCreatesVisibleProgress(t *testing.T) {
	s := newStreamer(newHub())
	s.resolve = func(int64, string) string { return "conv-proactive" }
	var frames []StreamFrame
	s.onFrame = func(f StreamFrame) { frames = append(frames, f) }
	start := time.Now()
	s.Ingest("llm.start", 41, "chat-proactive-42", `{}`, start)
	if len(frames) != 1 || frames[0].Progress == nil || frames[0].Progress.Phase != "thinking" {
		t.Fatalf("proactive model start did not create Thinking progress: %+v", frames)
	}
	// A terminal pacing event must settle the synthetic response as well, so
	// the indicator cannot remain active after the event-driven turn ends.
	s.Ingest("tool.call", 41, "chat-proactive-42", `{"name":"pace"}`, start.Add(time.Second))
	if got := s.snapshot("conv-proactive"); len(got.Frames) != 0 {
		t.Fatalf("proactive response remained active after pace: %+v", got.Frames)
	}
}

func TestQueuedResponseSurvivesPreviousPaceAndReconnect(t *testing.T) {
	s := newStreamer(newHub())
	s.telemetryConnected = true
	chat, thread := "conv-queued", "chat-conv-queued"
	s.emitAck(chat, thread, 41, 892)
	s.Ingest("llm.start", 41, thread, `{}`, time.Now())
	s.finishResponse(chat, 41)
	s.emitInboundAck(chat, thread, 41, &Message{ID: 894, Content: "List them again now"})
	start := time.Now()
	phase := func(want string) {
		t.Helper()
		snapshot := s.snapshot(chat)
		if len(snapshot.Frames) != 1 || snapshot.Frames[0].Progress == nil || snapshot.Frames[0].Progress.Phase != want || snapshot.Frames[0].Progress.AfterMessageID != 894 {
			t.Fatalf("reconnect snapshot=%+v, want current response in %s", snapshot.Frames, want)
		}
	}
	// Exact ordering from the reported second repository-list request:
	// the old model was started before this request, but pace arrives later.
	s.Ingest("llm.start", 41, thread, `{}`, start.Add(-time.Second))
	s.Ingest("tool.call", 41, thread, `{"name":"pace","id":"previous-response"}`, start.Add(time.Second))
	phase("thinking")
	// Even housekeeping that starts after the new acknowledgement cannot
	// own it. A different user event also must not transfer ownership.
	s.Ingest("llm.start", 41, thread, `{}`, start.Add(1100*time.Millisecond))
	s.Ingest("tool.call", 41, thread, `{"name":"pace"}`, start.Add(1200*time.Millisecond))
	s.Ingest("event.received", 41, thread, `{"message":"[chat] unrelated request"}`, start.Add(1300*time.Millisecond))
	s.Ingest("tool.call", 41, thread, `{"name":"done"}`, start.Add(1400*time.Millisecond))
	phase("thinking")
	s.intermediateReply(chat, 41)
	s.finishResponse(chat, 41) // A delayed final send from the previous reply.
	s.Ingest("llm.tool_chunk", 41, thread, `{"tool":"conversations_send","id":"old-final","chunk":"{\"text\":\"Old reply"}`, start.Add(1450*time.Millisecond))
	phase("thinking")
	s.Ingest("event.received", 41, thread, `{"message":"[chat] List them again now"}`, start.Add(1500*time.Millisecond))
	s.Ingest("llm.start", 41, thread, `{}`, start.Add(2*time.Second))
	phase("thinking")
	s.Ingest("llm.tool_chunk", 41, thread, `{"tool":"code_repos_list","id":"second-list","chunk":"{}"}`, start.Add(3*time.Second))
	phase("preparing_tool")
	s.Ingest("tool.call", 41, thread, `{"name":"code_repos_list","id":"second-list"}`, start.Add(4*time.Second))
	phase("running")
	s.Ingest("tool.result", 41, thread, `{"name":"code_repos_list","id":"second-list"}`, start.Add(5*time.Second))
	phase("continuing")
	s.finishResponse(chat, 41)
	if snapshot := s.snapshot(chat); len(snapshot.Frames) != 0 {
		t.Fatalf("finished response stayed active: %+v", snapshot)
	}
}

func TestInboundProgressBeforeDeliveryAndScopedFailure(t *testing.T) {
	s := newStreamer(newHub())
	s.telemetryConnected = true
	chat, thread := "conv-fast", "chat-conv-fast"
	s.emitInboundAck(chat, thread, 41, &Message{ID: 10, Content: "Hello"})
	s.Ingest("event.received", 41, thread, `{"message":"[chat] Hello"}`, time.Now())
	s.Ingest("llm.start", 41, thread, `{}`, time.Now())
	s.Ingest("tool.call", 41, thread, `{"name":"done"}`, time.Now())
	if got := s.snapshot(chat); len(got.Frames) != 0 {
		t.Fatal("fast completion did not settle")
	}
	s.emitInboundAck(chat, thread, 41, &Message{ID: 11, Content: "Next"})
	s.finishResponse(chat, 41, 10)
	if got := s.snapshot(chat); len(got.Frames) != 1 {
		t.Fatal("old delivery failure cleared next response")
	}
	s.finishResponse(chat, 41, 11)
	if got := s.snapshot(chat); len(got.Frames) != 0 {
		t.Fatal("failed delivery left Thinking active")
	}
}

func TestInboundPreviewHandlesUnicodeBoundary(t *testing.T) {
	s := newStreamer(newHub())
	s.telemetryConnected = true
	content := strings.Repeat("a", 92) + "你好"
	s.emitInboundAck("conv-unicode", "chat-conv-unicode", 41, &Message{ID: 10, Content: content})
	// Core JSON replaces an incomplete UTF-8 rune at its 100-byte boundary.
	raw, _ := json.Marshal(map[string]string{"message": ("[chat] " + content)[:100] + "..."})
	s.Ingest("event.received", 41, "chat-conv-unicode", string(raw), time.Now())
	s.Ingest("llm.start", 41, "chat-conv-unicode", `{}`, time.Now())
	s.Ingest("tool.call", 41, "chat-conv-unicode", `{"name":"done"}`, time.Now())
	if got := s.snapshot("conv-unicode"); len(got.Frames) != 0 {
		t.Fatal("unicode preview never transferred response ownership")
	}
}

func TestInboundWithoutTelemetrySettlesOnDurableReply(t *testing.T) {
	s := newStreamer(newHub())
	s.emitInboundAck("conv-fallback", "chat-conv-fallback", 41, &Message{ID: 10, Content: "Hello"})
	s.finishResponse("conv-fallback", 41)
	if got := s.snapshot("conv-fallback"); len(got.Frames) != 0 {
		t.Fatal("fallback reply left Thinking active")
	}
}

// Replay the scheduled check reported at 19:49 on 2026-10-08. A final
// confirmation closes visible work before the housekeeping model chooses
// pace; the five-minute wake must still start fresh visible work.
func TestScheduledCheckFinalConfirmationSettlesBeforePaceAndResumesOnWake(t *testing.T) {
	app, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	boundConversationCaller(t, app, conv, 41)
	thread := conversationThreadID(conv.ID)
	s := app.streamer
	s.resolve = func(agent int64, candidate string) string {
		if agent == 41 && candidate == thread {
			return conv.ID
		}
		return ""
	}
	s.emitAck(conv.ID, thread, 41, 1449)
	start := time.Now()
	s.Ingest("llm.start", 41, thread, `{}`, start)
	sent, err := app.toolSend(callerCtxCall(41, thread, "timed-confirmation"), ctx, map[string]any{
		"conversation_id": conv.ID, "text": "I'll check in five minutes.", "phase": "final",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sent.(map[string]any)["phase"] != "final" {
		t.Fatal("confirmation lost final phase")
	}
	idle := func() {
		t.Helper()
		if got := s.snapshot(conv.ID); len(got.Frames) != 0 {
			t.Fatalf("idle confirmation still has progress: %+v", got.Frames)
		}
	}
	idle()
	s.Ingest("llm.start", 41, thread, `{}`, start.Add(time.Second))
	idle()
	s.Ingest("llm.tool_chunk", 41, thread, `{"tool":"pace","id":"wait","chunk":"{\"sleep\":\"5m\"}"}`, start.Add(3*time.Second))
	idle()
	s.Ingest("tool.call", 41, thread, `{"name":"pace","id":"wait","args":{"sleep":"5m"}}`, start.Add(4*time.Second))
	idle()
	s.Ingest("tool.result", 41, thread, `{"name":"pace","id":"wait","success":true}`, start.Add(4*time.Second))
	idle()
	s.Ingest("llm.start", 41, thread, `{}`, start.Add(5*time.Minute))
	snapshot := s.snapshot(conv.ID)
	if len(snapshot.Frames) != 1 || snapshot.Frames[0].Progress == nil || snapshot.Frames[0].Progress.Phase != "thinking" || snapshot.Frames[0].Progress.AfterMessageID != 0 {
		t.Fatalf("timer wake did not start a fresh response: %+v", snapshot.Frames)
	}
}

// Prod (Helper, 2026-11-25): after the final reply the model reported to main
// and called pace clear_wake. Core started one more model call 4ms after the
// pace result, in the same run, with no tool and no terminal event. That call
// must not reopen Thinking: nothing would ever close it, and the next user
// message would queue behind it.
func TestClearWakePaceAfterReplyLeavesNoThinking(t *testing.T) {
	s := newStreamer(newHub())
	chat, thread := "conv-helper", "chat-conv-helper"
	s.resolve = func(int64, string) string { return chat }
	s.emitAck(chat, thread, 3, 10)
	start := time.Now()
	s.Ingest("llm.start", 3, thread, `{}`, start)
	s.finishResponse(chat, 3)
	s.Ingest("llm.start", 3, thread, `{}`, start.Add(time.Second))
	s.Ingest("tool.call", 3, thread, `{"name":"send","id":"report","args":{"id":"main"}}`, start.Add(2*time.Second))
	s.Ingest("tool.call", 3, thread, `{"name":"pace","id":"rest","args":{"clear_wake":"true"}}`, start.Add(2*time.Second))
	s.Ingest("tool.result", 3, thread, `{"name":"send","id":"report","success":true}`, start.Add(3*time.Second))
	s.Ingest("tool.result", 3, thread, `{"name":"pace","id":"rest","success":true}`, start.Add(3*time.Second))
	s.Ingest("llm.start", 3, thread, `{}`, start.Add(3*time.Second+4*time.Millisecond))
	if s.responseActive(chat, 3) || len(s.snapshot(chat).Frames) != 0 {
		t.Fatalf("the run's last model call reopened Thinking: %+v", s.snapshot(chat).Frames)
	}
	// Much later, still no input: a cleared wake never counts as one.
	s.Ingest("llm.start", 3, thread, `{}`, start.Add(time.Hour))
	if s.responseActive(chat, 3) {
		t.Fatal("a cleared wake reopened Thinking")
	}
	// New input always starts visible work.
	s.Ingest("event.received", 3, thread, `{}`, start.Add(2*time.Hour))
	s.Ingest("llm.start", 3, thread, `{}`, start.Add(2*time.Hour+time.Second))
	if !s.responseActive(chat, 3) {
		t.Fatal("new input after a cleared wake showed no Thinking")
	}
}

func TestTimedPaceWakesOnlyAfterItsDelay(t *testing.T) {
	s := newStreamer(newHub())
	chat, thread := "conv-timed", "chat-conv-timed"
	s.resolve = func(int64, string) string { return chat }
	s.emitAck(chat, thread, 41, 10)
	start := time.Now()
	s.Ingest("llm.start", 41, thread, `{}`, start)
	s.finishResponse(chat, 41)
	s.Ingest("llm.start", 41, thread, `{}`, start.Add(time.Second))
	s.Ingest("tool.call", 41, thread, `{"name":"pace","id":"nap","args":{"sleep":"2m"}}`, start.Add(2*time.Second))
	s.Ingest("tool.result", 41, thread, `{"name":"pace","id":"nap","success":true}`, start.Add(2*time.Second))
	s.Ingest("llm.start", 41, thread, `{}`, start.Add(2*time.Second+5*time.Millisecond))
	if s.responseActive(chat, 41) {
		t.Fatal("a model call before the pace timer reopened Thinking")
	}
	s.Ingest("llm.start", 41, thread, `{}`, start.Add(2*time.Minute+2*time.Second))
	if !s.responseActive(chat, 41) {
		t.Fatal("the pace timer wake showed no Thinking")
	}
}

func TestInputDuringTimedPaceShowsThinking(t *testing.T) {
	s := newStreamer(newHub())
	chat, thread := "conv-input", "chat-conv-input"
	s.resolve = func(int64, string) string { return chat }
	s.emitAck(chat, thread, 41, 10)
	start := time.Now()
	s.Ingest("llm.start", 41, thread, `{}`, start)
	s.finishResponse(chat, 41)
	s.Ingest("tool.call", 41, thread, `{"name":"pace","id":"nap","args":{"rate":"slow"}}`, start.Add(time.Second))
	s.Ingest("tool.result", 41, thread, `{"name":"pace","id":"nap","success":true}`, start.Add(time.Second))
	s.Ingest("event.received", 41, thread, `{}`, start.Add(3*time.Second))
	s.Ingest("llm.start", 41, thread, `{}`, start.Add(3*time.Second+10*time.Millisecond))
	if !s.responseActive(chat, 41) {
		t.Fatal("input during a timed pace showed no Thinking")
	}
}

// While the model writes a tool call's arguments, the row shows the call's
// own label as soon as Core's "_reason" argument is complete, and how much
// has been written; size updates are throttled, never one frame per chunk.
// Prod: 16% of Codex tool calls spent over 3s in "Preparing a tool action…"
// while the model wrote ~1.5 KB of arguments.
func TestPreparingToolShowsReasonAndArgumentSize(t *testing.T) {
	s := newStreamer(newHub())
	var frames []StreamFrame
	s.onFrame = func(f StreamFrame) { frames = append(frames, f) }
	s.resolve = func(int64, string) string { return "conv-args" }
	ingest := func(kind, raw string) { s.Ingest(kind, 41, "chat-conv-args", raw, time.Now()) }
	chunk := func(id, text string) {
		raw, _ := json.Marshal(map[string]string{"tool": "todo_todos_create", "id": id, "chunk": text})
		ingest("llm.tool_chunk", string(raw))
	}
	last := func() *ResponseProgress { t.Helper(); return frames[len(frames)-1].Progress }
	s.emitAck("conv-args", "chat-conv-args", 41, 80)
	ingest("llm.start", `{}`)

	chunk("c1", `{"_reason":"Creating mark`)
	if p := last(); p.Phase != "preparing_tool" || p.ToolReason != "" || p.ToolArgBytes != 25 {
		t.Fatalf("first chunk: %+v", p)
	}
	n := len(frames)
	chunk("c1", `eting todo","notes":"`)
	if len(frames) != n+1 || last().ToolReason != "Creating marketing todo" {
		t.Fatalf("reason not published once complete: frames=%d %+v", len(frames)-n, last())
	}
	n = len(frames)
	for i := 0; i < 40; i++ {
		chunk("c1", strings.Repeat("x", 50))
	}
	if len(frames) != n {
		t.Fatalf("argument deltas flooded progress: %d frames", len(frames)-n)
	}
	s.mu.Lock()
	s.responses[responseProgressKey("conv-args", 41)].toolArgsEmit = time.Now().Add(-time.Second)
	s.mu.Unlock()
	chunk("c1", "y")
	if p := last(); len(frames) != n+1 || p.ToolArgBytes != 25+21+40*50+1 || p.ToolReason != "Creating marketing todo" {
		t.Fatalf("size update: frames=%d %+v", len(frames)-n, p)
	}
	// A new call starts from zero, without the previous call's label.
	chunk("c2", `{"title":"x"`)
	if p := last(); p.CallID != "c2" || p.ToolReason != "" || p.ToolArgBytes != 12 {
		t.Fatalf("second call inherited state: %+v", p)
	}
	ingest("tool.call", `{"name":"todo_todos_create","id":"c2"}`)
	ingest("tool.result", `{"name":"todo_todos_create","id":"c2"}`)
	if p := last(); p.ToolReason != "" || p.ToolArgBytes != 0 {
		t.Fatalf("result kept argument state: %+v", p)
	}
}

func TestFlatStringParserReadsReasonAcrossChunks(t *testing.T) {
	var p flatStringParser
	buf := ""
	for _, c := range []string{`{"_rea`, `son":"Sending é`, `t\"e\" note","text":"hi"}`} {
		buf += c
		p.consume(buf)
	}
	if !p.reasonSeen || p.reason != `Sending ét"e" note` {
		t.Fatalf("reason=%q seen=%v", p.reason, p.reasonSeen)
	}
	var nested flatStringParser
	nested.consume(`{"input":{"_reason":"inner"},"x":1`)
	if nested.reasonSeen {
		t.Fatal("nested _reason taken as the call's reason")
	}
}
