package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Only a user-triggered response owns live progress. Background model work
// after a final reply, or while waiting for approval, must stay invisible.
type ResponseProgress struct {
	CompletionMessageID int64     `json:"completion_message_id,omitempty"`
	Phase               string    `json:"phase"`
	RunID               string    `json:"run_id"`
	Revision            uint64    `json:"revision"`
	AfterMessageID      int64     `json:"after_message_id"`
	StartedAt           time.Time `json:"started_at"`
	ToolName            string    `json:"tool_name,omitempty"`
	CallID              string    `json:"call_id,omitempty"`
	ToolStartedAt       time.Time `json:"tool_started_at,omitempty"`
}
type responseProgressState struct {
	ResponseProgress
	agentID         int64
	threadID        string
	chatID          string
	lastCallID      string
	hadTools        bool
	modelStarted    bool
	inboundPreview  string
	inboundReceived bool
	touched         time.Time
	lastEvent       time.Time
}

// Keep the completed turn boundary through the model's post-reply work.
// New input or a completed pace permits a subsequent event/timer turn. Scope
// by agent/thread so one participant cannot hide another participant's work.
type completedResponseTurn struct {
	settledAt time.Time
	wakeAt    time.Time
}

func (s *streamer) observeResponseWake(event string, agent int64, thread, raw string, ts time.Time) {
	if event != "event.received" && event != "tool.result" {
		return
	}
	if event == "tool.result" {
		var data struct {
			Name    string `json:"name"`
			Tool    string `json:"tool"`
			Success *bool  `json:"success"`
		}
		if json.Unmarshal([]byte(raw), &data) != nil || firstNonEmptyString(data.Name, data.Tool) != "pace" || (data.Success != nil && !*data.Success) {
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := streamCallKey(agent, thread, "")
	if turn, ok := s.completedTurns[key]; ok && ts.After(turn.settledAt) && ts.After(turn.wakeAt) {
		turn.wakeAt = ts
		s.completedTurns[key] = turn
	}
}

// inboundPreviewMatches reports whether Core's event.received preview is the
// registered inbound message. Core keeps the first 100 bytes of the event text
// and appends "..." when it cuts. Messages main receives through tracked agent
// events carry Core's source tag in the text ("[console] [chat] ..."), with
// the same name in the event's source field, so after that tag only the bytes
// Core kept can be compared.
func inboundPreviewMatches(message, source, preview string) bool {
	if strings.HasPrefix(message, preview) {
		return true
	}
	if source == "" {
		return false
	}
	rest, tagged := strings.CutPrefix(message, "["+source+"] ")
	if !tagged {
		return false
	}
	if strings.HasPrefix(rest, preview) {
		return true
	}
	kept, cut := strings.CutSuffix(rest, "...")
	if !cut {
		return false
	}
	// Core cuts bytes; JSON turns a split rune into U+FFFD.
	kept = strings.TrimRight(kept, "�")
	for kept != "" && !utf8.ValidString(kept) {
		kept = kept[:len(kept)-1]
	}
	return len(kept) > len("[chat] ") && strings.HasPrefix(preview, kept)
}

// feedConnectedLocked reports whether the telemetry feed covering thread is
// live. Main threads have their own feed. Called with mu held.
func (s *streamer) feedConnectedLocked(thread string) bool {
	if thread == mainThreadID {
		return s.mainTelemetryConnected
	}
	return s.telemetryConnected
}

// mainResponseChat returns the conversation whose open response main owns for
// agent, and whether Core has received that message. Only receipt-gated
// responses count: the conversation main answers has at most one per agent.
// Approval receipts on main open ungated responses that settle on durable
// messages, so main's telemetry is never attributed to them.
func (s *streamer) mainResponseChat(agent int64) (chat string, received bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.responses {
		if p.threadID == mainThreadID && p.agentID == agent && p.inboundPreview != "" && p.Phase != "idle" {
			return p.chatID, p.inboundReceived
		}
	}
	return "", false
}

func responseProgressKey(chat string, agent int64) string {
	return chat + ":" + strconv.FormatInt(agent, 10)
}

func (s *streamer) progressFrame(p *responseProgressState) StreamFrame {
	value := p.ResponseProgress
	return StreamFrame{Type: "stream", ConversationID: p.chatID, AgentID: p.agentID, ThreadID: p.threadID, CreatedAt: time.Now(), Progress: &value}
}
func (s *streamer) finishResponse(chat string, agent int64, afterMessageIDs ...int64) {
	s.finishResponseWithMessage(chat, agent, 0, afterMessageIDs...)
}

// Message and progress frames use independent queues. Include the durable
// replacement so clients need not clear their indicator before it arrives.
func (s *streamer) finishResponseWithMessage(chat string, agent, messageID int64, afterMessageIDs ...int64) {
	s.mu.Lock()
	p := s.responses[responseProgressKey(chat, agent)]
	if p == nil || (len(afterMessageIDs) == 0 && s.feedConnectedLocked(p.threadID) && p.inboundPreview != "" && !p.inboundReceived) || (len(afterMessageIDs) > 0 && p.AfterMessageID != afterMessageIDs[0]) {
		s.mu.Unlock()
		return
	}
	delete(s.responses, responseProgressKey(chat, agent))
	settledAt := time.Now()
	if p.lastEvent.After(settledAt) {
		settledAt = p.lastEvent
	}
	s.completedTurns[streamCallKey(agent, p.threadID, "")] = completedResponseTurn{settledAt: settledAt}
	s.progressSeq++
	p.Revision = s.progressSeq
	p.Phase = "idle"
	p.CompletionMessageID = messageID
	p.ToolName = ""
	p.CallID = ""
	frame := s.progressFrame(p)
	s.mu.Unlock()
	s.publish(frame)
	if s.onActivityChange != nil {
		s.onActivityChange(chat)
	}
}
func (s *streamer) intermediateReply(chat string, agent int64) {
	s.mu.Lock()
	if p := s.responses[responseProgressKey(chat, agent)]; p != nil && (!s.feedConnectedLocked(p.threadID) || p.inboundPreview == "" || p.inboundReceived) {
		// A progress message is part of the current response, so subsequent
		// pacing belongs to this response even when Core does not emit another
		// llm.start before sleeping. Acknowledgements do not call this method,
		// so stale housekeeping pacing cannot settle a new response.
		p.modelStarted = true
		p.Phase = "thinking"
		p.ToolName, p.CallID = "", ""
		s.progressSeq++
		p.Revision = s.progressSeq
		p.touched = time.Now()
		frame := s.progressFrame(p)
		s.mu.Unlock()
		s.publish(frame)
		return
	}
	s.mu.Unlock()
}
func (s *streamer) ingestProgress(event string, agent int64, thread, chat, raw string, ts time.Time) {
	var d struct {
		Name       string `json:"name"`
		Tool       string `json:"tool"`
		ID         string `json:"id"`
		CallID     string `json:"call_id"`
		ToolCallID string `json:"tool_call_id"`
		Message    string `json:"message"`
		Source     string `json:"source"`
	}
	_ = json.Unmarshal([]byte(raw), &d)
	name := firstNonEmptyString(d.Name, d.Tool)
	s.mu.Lock()
	s.pruneLocked()
	p := s.responses[responseProgressKey(chat, agent)]
	if p == nil || p.threadID != thread || ts.Before(p.lastEvent) {
		s.mu.Unlock()
		return
	}
	// Register progress before delivery; transfer ownership only when Core
	// consumes this message, never on the previous reply's housekeeping.
	if p.inboundPreview != "" && !p.inboundReceived {
		if event != "event.received" || !inboundPreviewMatches(d.Message, d.Source, p.inboundPreview) {
			s.mu.Unlock()
			return
		}
		p.inboundReceived = true
		p.lastEvent = ts
		s.mu.Unlock()
		return
	}
	// A new user event can queue while the previous response is still
	// finishing its housekeeping model turn. Its pace/done/error must not
	// settle the newly acknowledged response before that response starts.
	if event == "llm.start" {
		p.modelStarted = true
	}
	terminal := event == "llm.error" || event == "llm.err" || event == "thread.done" || (event == "tool.call" && (name == "pace" || name == "done"))
	if terminal && !p.modelStarted && !p.inboundReceived {
		s.mu.Unlock()
		return
	}
	p.lastEvent = ts
	if terminal {
		s.mu.Unlock()
		s.settleAck(chat, agent)
		s.finishResponse(chat, agent, p.AfterMessageID)
		return
	}
	phase := p.Phase
	switch event {
	case "llm.start":
		phase = "thinking"
		if p.hadTools {
			phase = "continuing"
		}
		p.ToolName = ""
		p.CallID = ""
	case "llm.tool_chunk":
		if !visibleActivityTool(name) {
			s.mu.Unlock()
			return
		}
		phase = "preparing_tool"
		p.ToolName = name
		p.CallID = firstNonEmptyString(d.ID, d.CallID, d.ToolCallID)
		if p.CallID != p.lastCallID || p.Phase != "preparing_tool" {
			p.ToolStartedAt = ts
		}
	case "tool.result":
		if !visibleActivityTool(name) || !p.hadTools {
			s.mu.Unlock()
			return
		}
		// The response remains active across the result-to-model gap. The UI
		// shows Thinking after the completed tool stops animating.
		phase = "continuing"
		p.ToolName = ""
		p.CallID = ""
	case "tool.call":
		if !visibleActivityTool(name) {
			s.mu.Unlock()
			return
		}
		phase = "running"
		p.hadTools = true
		// Keep the identity announced by llm.tool_chunk on the running
		// frame.  The durable tool_activity frame normally follows almost
		// immediately, but it can arrive on a separate queue (or after a
		// reconnect).  Without the identity, clients briefly fall back to a
		// generic Thinking row and can miss the pulsing tool row entirely.
		p.ToolName = name
		p.CallID = firstNonEmptyString(d.ID, d.CallID, d.ToolCallID, p.CallID)
		if p.ToolStartedAt.IsZero() {
			p.ToolStartedAt = ts
		}
	default:
		s.mu.Unlock()
		return
	}
	// Argument deltas contain no safe final label; emit once per tool identity.
	if phase == p.Phase && event == "llm.tool_chunk" && p.CallID == p.lastCallID {
		s.mu.Unlock()
		return
	}
	p.Phase = phase
	p.lastCallID = p.CallID
	p.touched = ts
	s.progressSeq++
	p.Revision = s.progressSeq
	frame := s.progressFrame(p)
	s.mu.Unlock()
	s.publish(frame)
}
