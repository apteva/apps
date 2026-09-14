package main

import (
	"encoding/json"
	"strconv"
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
}
type responseProgressState struct {
	ResponseProgress
	agentID    int64
	threadID   string
	chatID     string
	lastCallID string
	hadTools   bool
	hadReply   bool
	touched    time.Time
}

func responseProgressKey(chat string, agent int64) string {
	return chat + ":" + strconv.FormatInt(agent, 10)
}

func (s *streamer) progressFrame(p *responseProgressState) StreamFrame {
	value := p.ResponseProgress
	return StreamFrame{Type: "stream", ConversationID: p.chatID, AgentID: p.agentID, ThreadID: p.threadID, CreatedAt: time.Now(), Progress: &value}
}
func (s *streamer) finishResponse(chat string, agent int64) {
	s.mu.Lock()
	p := s.responses[responseProgressKey(chat, agent)]
	if p == nil {
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
}
func (s *streamer) intermediateReply(chat string, agent int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.responses[responseProgressKey(chat, agent)]; p != nil {
		p.hadReply = true
	}
}
func (s *streamer) ingestProgress(event string, agent int64, thread, chat, raw string, ts time.Time) {
	var d struct {
		Name       string `json:"name"`
		Tool       string `json:"tool"`
		ID         string `json:"id"`
		CallID     string `json:"call_id"`
		ToolCallID string `json:"tool_call_id"`
	}
	_ = json.Unmarshal([]byte(raw), &d)
	name := firstNonEmptyString(d.Name, d.Tool)
	if event == "llm.error" || event == "llm.err" || event == "thread.done" || (event == "tool.call" && (name == "pace" || name == "done")) {
		s.settleAck(chat, agent)
		s.finishResponse(chat, agent)
		return
	}
	s.mu.Lock()
	s.pruneLocked()
	p := s.responses[responseProgressKey(chat, agent)]
	if p == nil || p.threadID != thread {
		s.mu.Unlock()
		return
	}
	phase := p.Phase
	switch event {
	case "llm.start":
		phase = "thinking"
		if p.hadTools {
			phase = "continuing"
		} else if p.hadReply {
			phase = "preparing"
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
	case "tool.result":
		if !visibleActivityTool(name) || !p.hadTools {
			s.mu.Unlock()
			return
		}
		// The response remains active across the result-to-model gap. The UI
		// keeps the tool group pulsing without extending execution durations.
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
		p.ToolName = ""
		p.CallID = ""
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
