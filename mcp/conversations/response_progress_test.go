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

func TestProactiveStartCreatesVisibleProgress(t *testing.T) {
	s := newStreamer(newHub())
	s.resolve = func(int64, string) string { return "conv-proactive" }
	var frames []StreamFrame
	s.onFrame = func(f StreamFrame) { frames = append(frames, f) }
	start := time.Now()
	s.Ingest("llm.start", 41, "subscription-42", `{}`, start)
	if len(frames) != 1 || frames[0].Progress == nil || frames[0].Progress.Phase != "thinking" {
		t.Fatalf("proactive model start did not create Thinking progress: %+v", frames)
	}
	// A terminal pacing event must settle the synthetic response as well, so
	// the indicator cannot remain active after the event-driven turn ends.
	s.Ingest("tool.call", 41, "subscription-42", `{"name":"pace"}`, start.Add(time.Second))
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
