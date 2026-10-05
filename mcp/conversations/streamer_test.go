package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
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
			if f.AgentID != 41 {
				t.Fatalf("stream frame agent=%d, want 41", f.AgentID)
			}
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

	s.emitAck("conv-1", "chat-conv-1", 41)
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
	if got[0].AgentID != 41 {
		t.Fatalf("ack agent=%d, want 41", got[0].AgentID)
	}
	if !got[1].Done || got[1].CallID != got[0].CallID {
		t.Fatalf("settle frame = %+v, want done on %s", got[1], got[0].CallID)
	}
}

// Hidden terminal tools such as pace are not rendered as tool cards. Their
// result must still complete the visible Thinking placeholder; otherwise the
// panel shows Thinking while the agent is asleep waiting for an event.
func TestHiddenToolResultSettlesThinking(t *testing.T) {
	h := newHub()
	s := newStreamer(h)
	ch, cancel := h.subscribeFrames("conv-1")
	defer cancel()

	s.emitAck("conv-1", "chat-conv-1", 41)
	select {
	case frame := <-ch:
		if frame.Done || frame.Phase != "acknowledgement" {
			t.Fatalf("ack frame = %+v, want active acknowledgement", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("ack frame not published")
	}

	s.Ingest("tool.result", 41, "chat-conv-1",
		`{"name":"pace","id":"pace-1","result":"set sleep=10m"}`, time.Now())
	select {
	case frame := <-ch:
		if !frame.Done || frame.AgentID != 41 {
			t.Fatalf("pace settlement = %+v, want done for the pending ack", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("pace result did not settle Thinking")
	}

	select {
	case frame := <-ch:
		t.Fatalf("unexpected frame after pace settlement: %+v", frame)
	case <-time.After(100 * time.Millisecond):
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

// The MCP gateway prefixes tool names with the app name — telemetry
// reports "conversations_conversations_send". The live-probe of
// 2026-08-19 showed exactly that name; bare-name matching dropped
// every frame.
func TestStreamerAcceptsGatewayPrefixedToolNames(t *testing.T) {
	h := newHub()
	s := newStreamer(h)
	ch, cancel := h.subscribeFrames("conv-1")
	defer cancel()

	s.Ingest("llm.tool_chunk", 41, "chat-conv-1",
		`{"tool":"conversations_conversations_send","id":"gemini_1","chunk":"{\"conversation_id\":\"conv-1\",\"text\":\"streamed\"}"}`,
		time.Now())

	select {
	case f := <-ch:
		if f.Text != "streamed" {
			t.Fatalf("text = %q", f.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("prefixed tool name produced no frame")
	}
}

// Ack ids must be unique per emission: the panel tombstones settled
// ids, so a constant per-conversation id would suppress the thinking
// bubble for every message after the first.
func TestAckIDsUniquePerEmission(t *testing.T) {
	h := newHub()
	s := newStreamer(h)
	ch, cancel := h.subscribeFrames("conv-1")
	defer cancel()

	s.emitAck("conv-1", "chat-conv-1", 41)
	s.settleAck("conv-1")
	s.emitAck("conv-1", "chat-conv-1", 41)
	s.settleAck("conv-1")

	var frames []StreamFrame
	for len(frames) < 4 {
		select {
		case f := <-ch:
			frames = append(frames, f)
		case <-time.After(time.Second):
			t.Fatalf("frames = %d, want 4", len(frames))
		}
	}
	if frames[0].CallID == frames[2].CallID {
		t.Fatalf("ack ids reused: %q", frames[0].CallID)
	}
	if frames[1].CallID != frames[0].CallID || frames[3].CallID != frames[2].CallID {
		t.Fatal("settle frames must target their own ack ids")
	}
	// Settling with nothing pending publishes nothing.
	s.settleAck("conv-1")
	select {
	case f := <-ch:
		t.Fatalf("unexpected frame after empty settle: %+v", f)
	case <-time.After(100 * time.Millisecond):
	}
}

type telemetrySubscriberFunc func(context.Context, sdk.TelemetrySubscription) (<-chan sdk.TelemetryStreamEvent, error)

func (f telemetrySubscriberFunc) SubscribeTelemetry(ctx context.Context, sub sdk.TelemetrySubscription) (<-chan sdk.TelemetryStreamEvent, error) {
	return f(ctx, sub)
}

func TestTelemetryStartupAuthRaceRecoversActivityAndLiveFrames(t *testing.T) {
	a, ctx, _ := newTestEnv(t)
	conv := mkConversation(t, a, 41)
	boundConversationCaller(t, a, conv, 41)
	thread := conversationThreadID(conv.ID)
	frames, cancel := a.hub.subscribeFrames(conv.ID)
	defer cancel()
	now := time.Now().UTC()
	ch := make(chan sdk.TelemetryStreamEvent, 2)
	ch <- sdk.TelemetryStreamEvent{AgentID: 41, ThreadID: thread, Type: "tool.call", Time: now, Data: json.RawMessage(`{"id":"recovered","name":"code_repos_list","reason":"Listing available repositories"}`)}
	ch <- sdk.TelemetryStreamEvent{AgentID: 41, ThreadID: thread, Type: "tool.result", Time: now.Add(time.Millisecond), Data: json.RawMessage(`{"id":"recovered","name":"code_repos_list","success":true,"duration_ms":1}`)}
	close(ch)
	var attempts atomic.Int32
	tc := telemetrySubscriberFunc(func(_ context.Context, sub sdk.TelemetrySubscription) (<-chan sdk.TelemetryStreamEvent, error) {
		if sub.ThreadPrefix != "chat-" {
			return nil, fmt.Errorf("unexpected subscription: %+v", sub)
		}
		if attempts.Add(1) == 1 {
			return nil, fmt.Errorf("telemetry subscribe: HTTP 401")
		}
		return ch, nil
	})
	feedCtx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan struct{})
	go func() {
		a.connectTelemetryFeed(ctx, tc, feedCtx, sdk.TelemetrySubscription{ThreadPrefix: "chat-"}, false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("startup auth failure did not recover")
	}
	rows, err := a.store.toolActivities(conv.ID)
	if err != nil || attempts.Load() != 2 || len(rows) != 1 || rows[0].Status != "completed" || rows[0].Name != "code_repos_list" {
		t.Fatalf("recovery: attempts=%d rows=%+v err=%v", attempts.Load(), rows, err)
	}
	for _, status := range []string{"running", "completed"} {
		select {
		case frame := <-frames:
			if frame.Activity == nil || frame.Activity.Status != status {
				t.Fatalf("live frame: %+v, want %s activity", frame, status)
			}
		default:
			t.Fatalf("missing %s live frame", status)
		}
	}
}

func TestTelemetryStartupRetryStopsOnUnmount(t *testing.T) {
	a, ctx, _ := newTestEnv(t)
	feedCtx, stop := context.WithCancel(context.Background())
	defer stop()
	attempted := make(chan struct{}, 1)
	var attempts atomic.Int32
	tc := telemetrySubscriberFunc(func(context.Context, sdk.TelemetrySubscription) (<-chan sdk.TelemetryStreamEvent, error) {
		attempts.Add(1)
		attempted <- struct{}{}
		return nil, fmt.Errorf("telemetry subscribe: HTTP 401")
	})
	done := make(chan struct{})
	go func() {
		a.connectTelemetryFeed(ctx, tc, feedCtx, sdk.TelemetrySubscription{}, false)
		close(done)
	}()
	<-attempted
	stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled subscription kept retrying")
	}
	if attempts.Load() != 1 {
		t.Fatalf("subscription retried after cancellation: %d", attempts.Load())
	}
}

func TestRetryTelemetrySubscriptionOnlyStopsForPermanentRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "startup unauthorized", err: fmt.Errorf("telemetry subscribe: HTTP 401"), want: true},
		{name: "temporary unavailable", err: fmt.Errorf("telemetry subscribe: HTTP 503"), want: true},
		{name: "permission denied", err: fmt.Errorf("telemetry subscription unsupported: HTTP 403"), want: false},
		{name: "bridge missing", err: fmt.Errorf("telemetry subscription unsupported: HTTP 404"), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryTelemetrySubscription(tc.err); got != tc.want {
				t.Fatalf("retryTelemetrySubscription(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}
