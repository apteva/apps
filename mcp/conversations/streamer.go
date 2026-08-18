package main

// streamer.go — ephemeral streaming bubbles, ported from channel-chat's
// stream.go (the donor is unmodified; see DESIGN.md).
//
// Two feeds produce frames:
//
//   - Telemetry (token-level, "same UI experience" parity): the
//     platform's llm.tool_chunk / tool.call / tool.result events for
//     chat-* threads, subscribed through the SDK's optional
//     TelemetryClient behind platform.telemetry.read. The LLM streams
//     its conversations_send arguments token by token; we extract the
//     partial `text` field and republish whenever it grows.
//
//   - Stage-1 phase frames (automatic fallback): when the telemetry
//     bridge is unavailable (permission not granted, pre-bridge
//     server), the app still emits an acknowledgement frame when a
//     user message is forwarded and a Done frame when the agent's
//     durable reply lands — same bubble lifecycle, no token text.
//
// Frames are ephemeral: the hub fan-outs them on a separate SSE event
// channel ("stream"), nothing persists, and the durable message row is
// always what finally replaces the bubble.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// StreamFrame mirrors channel-chat's wire shape so the dashboard's
// existing streaming-bubble machinery ports to the panel unchanged.
type StreamFrame struct {
	Type           string    `json:"type"` // always "stream"
	ConversationID string    `json:"chat_id"`
	ThreadID       string    `json:"thread_id"`
	CallID         string    `json:"call_id"`
	Text           string    `json:"text"`
	Phase          string    `json:"phase,omitempty"`
	Done           bool      `json:"done"`
	CreatedAt      time.Time `json:"created_at"`
}

type streamState struct {
	buf            strings.Builder
	conversationID string
	threadID       string
	callID         string
	tool           string
	createdAt      time.Time
}

type streamer struct {
	hub      *hub
	mu       sync.Mutex
	buffers  map[string]*streamState
	lastEmit map[string]string
}

func newStreamer(h *hub) *streamer {
	return &streamer{
		hub:      h,
		buffers:  map[string]*streamState{},
		lastEmit: map[string]string{},
	}
}

// visibleConversationTool: which tool calls stream into a bubble.
// conversations_send is this app's own surface; the channels_* names
// keep the streamer correct once the server relay (migration phase 1)
// forwards legacy-shaped calls.
func visibleConversationTool(name string) bool {
	switch name {
	case "conversations_send", "channels_send", "channels_respond":
		return true
	default:
		return false
	}
}

// conversationForThread maps "chat-conv-<hex>" → "conv-<hex>". Empty
// means the thread is not a conversation thread and the event is
// ignored — main and worker threads must never leak partial text into
// a chat.
func conversationForThread(threadID string) string {
	if rest, ok := strings.CutPrefix(threadID, "chat-"); ok && rest != "" {
		return rest
	}
	return ""
}

func streamCallKey(agentID int64, threadID, callID string) string {
	// Provider call IDs are only unique within one model response —
	// scope by agent and thread so two cores reusing an id cannot
	// clobber each other's bubbles.
	return strconv.FormatInt(agentID, 10) + "\x00" + threadID + "\x00" + callID
}

// Ingest consumes one telemetry event. Irrelevant events are no-ops so
// the feed loop needs no per-event gating.
func (s *streamer) Ingest(eventType string, agentID int64, threadID, dataJSON string, ts time.Time) {
	if s == nil {
		return
	}
	conversationID := conversationForThread(threadID)
	if conversationID == "" {
		return
	}
	switch eventType {
	case "llm.tool_chunk":
		s.onChunk(agentID, threadID, conversationID, dataJSON, ts)
	case "tool.call":
		s.onFinalArgs(agentID, threadID, conversationID, dataJSON, ts)
	case "tool.result":
		s.onToolEnd(agentID, threadID, conversationID, dataJSON, ts)
	}
}

func (s *streamer) onChunk(agentID int64, threadID, conversationID, dataJSON string, ts time.Time) {
	var d struct {
		Tool       string `json:"tool"`
		Name       string `json:"name"`
		ID         string `json:"id"`
		CallID     string `json:"call_id"`
		ToolCallID string `json:"tool_call_id"`
		Chunk      string `json:"chunk"`
		Delta      string `json:"delta"`
		Text       string `json:"text"`
	}
	if err := json.Unmarshal([]byte(dataJSON), &d); err != nil {
		return
	}
	tool := firstNonEmptyString(d.Tool, d.Name)
	callID := firstNonEmptyString(d.ID, d.CallID, d.ToolCallID)
	chunk := firstNonEmptyString(d.Chunk, d.Delta, d.Text)
	if !visibleConversationTool(tool) || callID == "" {
		return
	}
	key := streamCallKey(agentID, threadID, callID)
	s.mu.Lock()
	st, ok := s.buffers[key]
	if !ok {
		st = &streamState{
			conversationID: conversationID, threadID: threadID,
			callID: callID, tool: tool, createdAt: ts,
		}
		s.buffers[key] = st
	}
	st.buf.WriteString(chunk)
	text, _ := extractTextField(st.buf.String())
	changed := text != "" && text != s.lastEmit[key]
	if changed {
		s.lastEmit[key] = text
	}
	s.mu.Unlock()
	if !changed {
		return
	}
	s.hub.publishStream(StreamFrame{
		Type: "stream", ConversationID: conversationID, ThreadID: threadID,
		CallID: callID, Text: text, CreatedAt: ts,
	})
}

// onFinalArgs fires on tool.call — the args are complete, so this is
// the guaranteed-clean extraction moment before the DB row lands.
func (s *streamer) onFinalArgs(agentID int64, threadID, conversationID, dataJSON string, ts time.Time) {
	var d struct {
		Name       string          `json:"name"`
		Tool       string          `json:"tool"`
		ID         string          `json:"id"`
		CallID     string          `json:"call_id"`
		ToolCallID string          `json:"tool_call_id"`
		Args       json.RawMessage `json:"args"`
		Arguments  json.RawMessage `json:"arguments"`
		Input      json.RawMessage `json:"input"`
		Params     json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal([]byte(dataJSON), &d); err != nil {
		return
	}
	name := firstNonEmptyString(d.Name, d.Tool)
	callID := firstNonEmptyString(d.ID, d.CallID, d.ToolCallID)
	if !visibleConversationTool(name) || callID == "" {
		return
	}
	text, ok := decodeSendArgs(firstRawJSON(d.Args, d.Arguments, d.Input, d.Params), conversationID)
	if !ok || text == "" {
		return
	}
	key := streamCallKey(agentID, threadID, callID)
	s.mu.Lock()
	changed := text != s.lastEmit[key]
	if changed {
		s.lastEmit[key] = text
	}
	s.mu.Unlock()
	if !changed {
		return
	}
	s.hub.publishStream(StreamFrame{
		Type: "stream", ConversationID: conversationID, ThreadID: threadID,
		CallID: callID, Text: text, CreatedAt: ts,
	})
}

// onToolEnd clears state and tells the UI to drop the bubble — the
// durable row is the authoritative replacement.
func (s *streamer) onToolEnd(agentID int64, threadID, conversationID, dataJSON string, ts time.Time) {
	var d struct {
		Name       string `json:"name"`
		Tool       string `json:"tool"`
		ID         string `json:"id"`
		CallID     string `json:"call_id"`
		ToolCallID string `json:"tool_call_id"`
	}
	if err := json.Unmarshal([]byte(dataJSON), &d); err != nil {
		return
	}
	name := firstNonEmptyString(d.Name, d.Tool)
	callID := firstNonEmptyString(d.ID, d.CallID, d.ToolCallID)
	if !visibleConversationTool(name) || callID == "" {
		return
	}
	key := streamCallKey(agentID, threadID, callID)
	s.mu.Lock()
	delete(s.buffers, key)
	delete(s.lastEmit, key)
	s.mu.Unlock()
	s.hub.publishStream(StreamFrame{
		Type: "stream", ConversationID: conversationID, ThreadID: threadID,
		CallID: callID, Done: true, CreatedAt: ts,
	})
}

// ─── Stage-1 phase frames (fallback + instant feedback) ──────────────

// ackCallID is the synthetic call id for the acknowledgement bubble
// shown between "user message forwarded" and "agent reply landed".
// One per conversation: a newer forward replaces the bubble, and the
// durable reply drops it.
func ackCallID(conversationID string) string { return "ack-" + conversationID }

func (s *streamer) emitAck(conversationID, threadID string) {
	s.hub.publishStream(StreamFrame{
		Type: "stream", ConversationID: conversationID, ThreadID: threadID,
		CallID: ackCallID(conversationID), Phase: "acknowledgement",
		CreatedAt: time.Now(),
	})
}

func (s *streamer) settleAck(conversationID string) {
	s.hub.publishStream(StreamFrame{
		Type: "stream", ConversationID: conversationID,
		CallID: ackCallID(conversationID), Done: true, CreatedAt: time.Now(),
	})
}

// ─── telemetry feed ──────────────────────────────────────────────────

// runTelemetryFeed subscribes to the platform's telemetry bridge and
// pipes events into the streamer. Returns false when the bridge is
// unavailable — the app then runs on Stage-1 phase frames alone, and
// the panel cannot tell the difference structurally (only textually).
func (a *App) runTelemetryFeed(ctx *sdk.AppCtx) bool {
	tc, ok := ctx.PlatformAPI().(sdk.TelemetryClient)
	if !ok {
		return false
	}
	feedCtx, cancel := context.WithCancel(context.Background())
	a.telemetryStop = cancel
	ch, err := tc.SubscribeTelemetry(feedCtx, sdk.TelemetrySubscription{
		Events:       []string{"llm.tool_chunk", "tool.call", "tool.result"},
		ThreadPrefix: "chat-",
	})
	if err != nil {
		cancel()
		a.telemetryStop = nil
		ctx.Logger().Info("telemetry bridge unavailable — streaming falls back to phase frames", "err", err)
		return false
	}
	go func() {
		for ev := range ch {
			a.streamer.Ingest(ev.Type, ev.AgentID, ev.ThreadID, string(ev.Data), ev.Time)
		}
		ctx.Logger().Info("telemetry feed ended")
	}()
	return true
}

// ─── helpers ─────────────────────────────────────────────────────────

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstRawJSON(values ...json.RawMessage) json.RawMessage {
	for _, v := range values {
		trimmed := strings.TrimSpace(string(v))
		if trimmed != "" && trimmed != "null" {
			return v
		}
	}
	return nil
}

// decodeSendArgs handles conversations_send's {conversation_id, text}
// and the legacy channels shapes. A call addressed to a DIFFERENT
// conversation is not ours to render.
func decodeSendArgs(raw json.RawMessage, conversationID string) (string, bool) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return "", false
	}
	var args struct {
		ConversationID string `json:"conversation_id"`
		Channel        string `json:"channel"`
		Text           string `json:"text"`
	}
	decode := func(data []byte) bool { return json.Unmarshal(data, &args) == nil }
	if !decode(raw) {
		// Double-encoded args (a string containing JSON) appear on some
		// provider paths.
		var encoded string
		if json.Unmarshal(raw, &encoded) != nil || encoded == "" || !decode([]byte(encoded)) {
			return "", false
		}
	}
	if args.ConversationID != "" && args.ConversationID != conversationID {
		return "", false
	}
	if args.Channel != "" && args.Channel != "apteva" && args.Channel != "chat" && args.Channel != "current" {
		return "", false
	}
	return args.Text, args.Text != ""
}

// extractTextField pulls `text` out of possibly-truncated streaming
// JSON — ported verbatim from channel-chat (flat scanner; the send
// schema has no nested objects).
func extractTextField(buf string) (string, bool) {
	i := 0
	for i < len(buf) {
		switch buf[i] {
		case ' ', '\t', '\n', '\r', '{', ',':
			i++
			continue
		case '"':
			i++
			keyStart := i
			for i < len(buf) && buf[i] != '"' {
				if buf[i] == '\\' && i+1 < len(buf) {
					i += 2
					continue
				}
				i++
			}
			if i >= len(buf) {
				return "", false
			}
			key := buf[keyStart:i]
			i++
			for i < len(buf) && (buf[i] == ' ' || buf[i] == '\t' || buf[i] == '\n' || buf[i] == ':') {
				i++
			}
			if i >= len(buf) {
				return "", false
			}
			if key == "text" {
				if buf[i] != '"' {
					return "", false
				}
				return readStringContent(buf, i+1)
			}
			if buf[i] == '"' {
				i++
				for i < len(buf) && buf[i] != '"' {
					if buf[i] == '\\' && i+1 < len(buf) {
						i += 2
						continue
					}
					i++
				}
				if i >= len(buf) {
					return "", false
				}
				i++
			} else {
				for i < len(buf) && buf[i] != ',' && buf[i] != '}' {
					i++
				}
			}
		default:
			i++
		}
	}
	return "", false
}

func readStringContent(buf string, i int) (string, bool) {
	var sb strings.Builder
	for i < len(buf) {
		c := buf[i]
		if c == '\\' {
			if i+1 >= len(buf) {
				return sb.String(), true
			}
			next := buf[i+1]
			switch next {
			case '"', '\\', '/':
				sb.WriteByte(next)
			case 'n':
				sb.WriteByte('\n')
			case 't':
				sb.WriteByte('\t')
			case 'r':
				sb.WriteByte('\r')
			case 'b':
				sb.WriteByte('\b')
			case 'f':
				sb.WriteByte('\f')
			case 'u':
				if i+6 > len(buf) {
					return sb.String(), true
				}
				if r, err := strconv.ParseUint(buf[i+2:i+6], 16, 32); err == nil {
					sb.WriteRune(rune(r))
				}
				i += 6
				continue
			default:
				sb.WriteByte(next)
			}
			i += 2
		} else if c == '"' {
			return sb.String(), true
		} else {
			sb.WriteByte(c)
			i++
		}
	}
	return sb.String(), true
}
