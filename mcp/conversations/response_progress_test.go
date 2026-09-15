package main

import (
	"testing"
	"time"
)

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
	ingest("llm.start", `{}`)
	phase("preparing")
	ingest("tool.call", `{"name":"pace"}`)
	phase("idle")
}
