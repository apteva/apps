package main

import (
	"fmt"
	"testing"
	"time"
)

// TestStreamerAccumulatesPartialText is the core token-streaming
// behavior: chunks of the LLM's tool-arg JSON arrive piecewise and the
// extracted `text` grows monotonically across frames.
func TestStreamerAccumulatesPartialText(t *testing.T) {
	h := newHub()
	s := newStreamer(h)
	ch, cancel := h.subscribeFrames("conv-1")
	defer cancel()

	chunks := []string{
		`{"tool":"conversations_send","id":"call-1","chunk":"{\"conversation_id\":\"conv-1\",\"text\":\"Hel"}`,
		`{"tool":"conversations_send","id":"call-1","chunk":"lo wor"}`,
		`{"tool":"conversations_send","id":"call-1","chunk":"ld\"}"}`,
	}
	for _, c := range chunks {
		s.Ingest("llm.tool_chunk", 41, "chat-conv-1", c, time.Now())
	}

	var texts []string
	for len(texts) < 3 {
		select {
		case f := <-ch:
			texts = append(texts, f.Text)
		case <-time.After(time.Second):
			t.Fatalf("timed out with %d frames: %v", len(texts), texts)
		}
	}
	if texts[0] != "Hel" || texts[1] != "Hello wor" || texts[2] != "Hello world" {
		t.Fatalf("texts = %v, want progressive growth", texts)
	}
}

func TestStreamerIgnoresForeignThreadsAndTools(t *testing.T) {
	h := newHub()
	s := newStreamer(h)
	ch, cancel := h.subscribeFrames("conv-1")
	defer cancel()

	// A worker thread and a non-chat tool must never produce a bubble.
	s.Ingest("llm.tool_chunk", 41, "worker-7",
		`{"tool":"conversations_send","id":"c1","chunk":"{\"text\":\"leak\"}"}`, time.Now())
	s.Ingest("llm.tool_chunk", 41, "chat-conv-1",
		`{"tool":"tasks_create","id":"c2","chunk":"{\"text\":\"leak\"}"}`, time.Now())

	select {
	case f := <-ch:
		t.Fatalf("unexpected frame: %+v", f)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestStreamerToolEndEmitsDoneAndClearsState(t *testing.T) {
	h := newHub()
	s := newStreamer(h)
	ch, cancel := h.subscribeFrames("conv-1")
	defer cancel()

	s.Ingest("llm.tool_chunk", 41, "chat-conv-1",
		`{"tool":"conversations_send","id":"call-9","chunk":"{\"text\":\"partial\"}"}`, time.Now())
	s.Ingest("tool.result", 41, "chat-conv-1",
		`{"tool":"conversations_send","id":"call-9"}`, time.Now())

	var got []StreamFrame
	for len(got) < 2 {
		select {
		case f := <-ch:
			got = append(got, f)
		case <-time.After(time.Second):
			t.Fatalf("frames = %d, want text+done", len(got))
		}
	}
	if got[0].Text != "partial" || got[0].Done {
		t.Fatalf("first frame = %+v, want text frame", got[0])
	}
	if !got[1].Done || got[1].CallID != "call-9" {
		t.Fatalf("second frame = %+v, want done for call-9", got[1])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.buffers) != 0 || len(s.lastEmit) != 0 {
		t.Fatal("tool.result must clear streamer state")
	}
}

// tool.call carries the complete args — the clean final text lands even
// when chunks were missed entirely (reconnect mid-generation).
func TestStreamerFinalArgsWithoutChunks(t *testing.T) {
	h := newHub()
	s := newStreamer(h)
	ch, cancel := h.subscribeFrames("conv-1")
	defer cancel()

	s.Ingest("tool.call", 41, "chat-conv-1",
		`{"name":"conversations_send","id":"c3","args":{"conversation_id":"conv-1","text":"complete answer"}}`,
		time.Now())

	select {
	case f := <-ch:
		if f.Text != "complete answer" {
			t.Fatalf("text = %q", f.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("no frame from tool.call")
	}

	// Args addressed to a different conversation are not ours.
	s.Ingest("tool.call", 41, "chat-conv-1",
		`{"name":"conversations_send","id":"c4","args":{"conversation_id":"conv-OTHER","text":"foreign"}}`,
		time.Now())
	select {
	case f := <-ch:
		t.Fatalf("foreign-conversation frame leaked: %+v", f)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAckLifecycle(t *testing.T) {
	h := newHub()
	s := newStreamer(h)
	ch, cancel := h.subscribeFrames("conv-1")
	defer cancel()

	s.emitAck("conv-1", "chat-conv-1")
	s.settleAck("conv-1")

	var got []StreamFrame
	for len(got) < 2 {
		select {
		case f := <-ch:
			got = append(got, f)
		case <-time.After(time.Second):
			t.Fatalf("frames = %d, want ack+done", len(got))
		}
	}
	if got[0].Phase != "acknowledgement" || got[0].Done {
		t.Fatalf("ack frame = %+v", got[0])
	}
	if !got[1].Done || got[1].CallID != got[0].CallID {
		t.Fatalf("settle frame = %+v, want done on %s", got[1], got[0].CallID)
	}
}

// Two agents reusing a provider call id must not clobber each other.
func TestStreamerScopesCallIDsByAgentAndThread(t *testing.T) {
	h := newHub()
	s := newStreamer(h)
	chA, cancelA := h.subscribeFrames("conv-a")
	defer cancelA()
	chB, cancelB := h.subscribeFrames("conv-b")
	defer cancelB()

	send := func(agentID int64, conv, text string) {
		s.Ingest("llm.tool_chunk", agentID, "chat-"+conv,
			fmt.Sprintf(`{"tool":"conversations_send","id":"call-1","chunk":"{\"text\":\"%s\"}"}`, text),
			time.Now())
	}
	send(41, "conv-a", "alpha")
	send(42, "conv-b", "beta")

	for name, ch := range map[string]<-chan StreamFrame{"a": chA, "b": chB} {
		select {
		case f := <-ch:
			want := map[string]string{"a": "alpha", "b": "beta"}[name]
			if f.Text != want {
				t.Fatalf("conv-%s got %q, want %q", name, f.Text, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("conv-%s got no frame", name)
		}
	}
}
