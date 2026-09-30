package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// Only a user-triggered response owns live progress. Background model work
// after a final reply, or while waiting for approval, must stay invisible.
type ResponseProgress struct {
	Phase          string    `json:"phase"`
	RunID          string    `json:"run_id"`
	Revision       uint64    `json:"revision"`
	AfterMessageID int64     `json:"after_message_id"`
	StartedAt      time.Time `json:"started_at"`
	ToolName       string    `json:"tool_name,omitempty"`
	CallID         string    `json:"call_id,omitempty"`
	ToolStartedAt  time.Time `json:"tool_started_at,omitempty"`
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

func responseProgressKey(chat string, agent int64) string {
	return chat + ":" + strconv.FormatInt(agent, 10)
}

func (s *streamer) progressFrame(p *responseProgressState) StreamFrame {
	value := p.ResponseProgress
	return StreamFrame{Type: "stream", ConversationID: p.chatID, AgentID: p.agentID, ThreadID: p.threadID, CreatedAt: time.Now(), Progress: &value}
}
func (s *streamer) finishResponse(chat string, agent int64, afterMessageIDs ...int64) {
	s.mu.Lock()
	p := s.responses[responseProgressKey(chat, agent)]
	if p == nil || (len(afterMessageIDs) == 0 && s.telemetryConnected && p.inboundPreview != "" && !p.inboundReceived) || (len(afterMessageIDs) > 0 && p.AfterMessageID != afterMessageIDs[0]) {
		s.mu.Unlock()
		return
	}
	delete(s.responses, responseProgressKey(chat, agent))
	s.progressSeq++
	p.Revision = s.progressSeq
	p.Phase = "idle"
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
	if p := s.responses[responseProgressKey(chat, agent)]; p != nil && (!s.telemetryConnected || p.inboundPreview == "" || p.inboundReceived) {
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
		if event != "event.received" || !strings.HasPrefix(d.Message, p.inboundPreview) {
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
