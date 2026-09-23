package main

import (
	"encoding/json"
	"os"
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
}
