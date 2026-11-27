package main

import (
	"encoding/json"
	"fmt"
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
	// While the model writes a tool call's arguments: its activity label as
	// soon as Core's "_reason" argument is complete, and how many argument
	// bytes have arrived so far.
	ToolReason   string `json:"tool_reason,omitempty"`
	ToolArgBytes int    `json:"tool_arg_bytes,omitempty"`
	// Calls whose arguments are complete (Core's llm.tool_ready) but that
	// have not started running yet, in the order they became ready.
	ReadyTools []ReadyTool `json:"ready_tools,omitempty"`
}

// ReadyTool is a tool call the model has finished writing while it is still
// writing others. It waits for the end of the model stream before it runs.
type ReadyTool struct {
	CallID    string    `json:"call_id"`
	ToolName  string    `json:"tool_name"`
	Reason    string    `json:"reason,omitempty"`
	ArgBytes  int       `json:"arg_bytes,omitempty"`
	StartedAt time.Time `json:"started_at"`
	ReadyAt   time.Time `json:"ready_at"`
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
	// Argument stream of the tool call being written (see ToolReason).
	toolArgs       strings.Builder
	toolArgsParser flatStringParser
	toolArgsEmit   time.Time
	toolArgsSent   int
	// Calls written in the current model pass, by call id, so a call keeps
	// its label and start time when it is reported ready after the model
	// has moved on to the next call.
	written map[string]ReadyTool
}

// rememberCurrentCall records the call being written under its id.
func (p *responseProgressState) rememberCurrentCall() {
	if p.CallID == "" || p.ToolName == "" {
		return
	}
	if p.written == nil {
		p.written = map[string]ReadyTool{}
	}
	p.written[p.CallID] = ReadyTool{CallID: p.CallID, ToolName: p.ToolName, Reason: p.ToolReason, ArgBytes: p.ToolArgBytes, StartedAt: p.ToolStartedAt}
}

// clearModelPass forgets the calls of a model pass that ended or failed.
// Calls reported ready by a failed stream never run.
func (p *responseProgressState) clearModelPass() {
	p.ToolName = ""
	p.CallID = ""
	p.resetToolArgs()
	p.ReadyTools = nil
	p.written = nil
}

func (p *responseProgressState) readyIndex(callID string) int {
	for i, tool := range p.ReadyTools {
		if tool.CallID == callID {
			return i
		}
	}
	return -1
}

// The reason is read from the start of the arguments; past this many
// buffered bytes only the count is kept.
const maxReasonScanBytes = 16 << 10

// Argument-size updates are throttled: a frame when the size grew by this
// much and this long has passed since the previous one.
const (
	toolArgsStep     = 256
	toolArgsInterval = 500 * time.Millisecond
)

// resetToolArgs starts tracking a new tool call's arguments.
func (p *responseProgressState) resetToolArgs() {
	p.toolArgs.Reset()
	p.toolArgsParser = flatStringParser{}
	p.toolArgsEmit = time.Time{}
	p.toolArgsSent = 0
	p.ToolReason = ""
	p.ToolArgBytes = 0
}

// trackToolArgs adds one argument chunk and reports whether the visible
// state (a newly read reason, or enough new bytes) is worth a frame.
func (p *responseProgressState) trackToolArgs(chunk string, now time.Time) bool {
	p.ToolArgBytes += len(chunk)
	reasonFound := false
	if !p.toolArgsParser.reasonSeen && p.toolArgs.Len() < maxReasonScanBytes {
		p.toolArgs.WriteString(chunk)
		p.toolArgsParser.consume(p.toolArgs.String())
		if p.toolArgsParser.reasonSeen {
			p.ToolReason = truncateRunes(strings.TrimSpace(p.toolArgsParser.reason), 120)
			reasonFound = p.ToolReason != ""
			p.toolArgs.Reset() // nothing else is read from the arguments
		}
	}
	grown := p.ToolArgBytes-p.toolArgsSent >= toolArgsStep && now.Sub(p.toolArgsEmit) >= toolArgsInterval
	if reasonFound || grown {
		p.toolArgsEmit = now
		p.toolArgsSent = p.ToolArgBytes
		return true
	}
	return false
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

// Keep the completed turn boundary through the model's post-reply work.
// New input, or a pace's timer coming due, permits a subsequent event/timer
// turn. Scope by agent/thread so one participant cannot hide another
// participant's work.
type completedResponseTurn struct {
	settledAt time.Time
	wakeAt    time.Time
	// pendingPaces holds each pace call's earliest timer wake (zero: none)
	// until its result arrives.
	pendingPaces map[string]paceWake
}

type paceWake struct {
	delay time.Duration
	never bool
}

// minimumTimerWake is below Core's shortest pace rate (reactive, 500ms). A
// model call that starts sooner after a pace result is the same run.
const minimumTimerWake = 400 * time.Millisecond

// paceWakeFromArgs reads when a pace can next wake its thread without new
// input. Core sets the timer when pace runs: clear_wake waits for events only,
// sleep and rate wake after their delay. A pace that changes neither keeps
// Core's earlier timer, whose remaining time is unknown here.
func paceWakeFromArgs(args map[string]any) paceWake {
	text := func(key string) string { return strings.TrimSpace(fmt.Sprint(args[key])) }
	if v := strings.ToLower(text("clear_wake")); v == "true" || v == "1" || v == "yes" {
		return paceWake{never: true}
	}
	if raw := text("sleep"); raw != "" && raw != "<nil>" {
		if d, err := time.ParseDuration(raw); err == nil && d > minimumTimerWake {
			return paceWake{delay: d}
		}
	}
	if d, ok := paceRateDelays[text("rate")]; ok && d > minimumTimerWake {
		return paceWake{delay: d}
	}
	return paceWake{delay: minimumTimerWake}
}

// Core's named pace rates.
var paceRateDelays = map[string]time.Duration{
	"reactive": 500 * time.Millisecond, "fast": 2 * time.Second, "normal": 10 * time.Second,
	"slow": 30 * time.Second, "sleep": 2 * time.Minute,
}

func (s *streamer) observeResponseWake(event string, agent int64, thread, raw string, ts time.Time) {
	if event != "event.received" && event != "tool.call" && event != "tool.result" {
		return
	}
	var data struct {
		ID      string         `json:"id"`
		Name    string         `json:"name"`
		Tool    string         `json:"tool"`
		Success *bool          `json:"success"`
		Args    map[string]any `json:"args"`
	}
	if event != "event.received" {
		if json.Unmarshal([]byte(raw), &data) != nil || firstNonEmptyString(data.Name, data.Tool) != "pace" {
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := streamCallKey(agent, thread, "")
	turn, ok := s.completedTurns[key]
	if !ok || !ts.After(turn.settledAt) {
		return
	}
	switch event {
	case "event.received":
		// New input wakes the thread now, ahead of any pending timer.
		turn.wakeAt = ts
	case "tool.call":
		if turn.pendingPaces == nil {
			turn.pendingPaces = map[string]paceWake{}
		}
		if len(turn.pendingPaces) < 64 {
			turn.pendingPaces[data.ID] = paceWakeFromArgs(data.Args)
		}
	case "tool.result":
		wake, known := turn.pendingPaces[data.ID]
		delete(turn.pendingPaces, data.ID)
		if data.Success != nil && !*data.Success {
			break
		}
		if !known {
			wake = paceWake{delay: minimumTimerWake}
		}
		if wake.never {
			// Only new input can start the next turn. A model call right
			// after this result is the run finishing, not new work.
			break
		}
		// Core's timer starts when pace runs; allow for event clock skew.
		delay := wake.delay - wake.delay/10
		if delay < minimumTimerWake {
			delay = minimumTimerWake
		}
		if at := ts.Add(delay); at.After(turn.wakeAt) {
			turn.wakeAt = at
		}
	}
	s.completedTurns[key] = turn
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

// awaitingReceiptLocked reports whether p must wait for Core's receipt of its
// message before a durable reply may settle it. Only while the feed covering
// its thread is connected and has shown this agent's telemetry: an agent the
// install cannot see (no project, for a project-scoped install) never sends
// a receipt, so its response settles on the durable reply.
func (s *streamer) awaitingReceiptLocked(p *responseProgressState) bool {
	return p.inboundPreview != "" && !p.inboundReceived && s.feedConnectedLocked(p.threadID) && s.telemetrySeen[p.agentID]
}

// settleTrackedEvent ends the response to a message when Core reports that
// the turn which consumed it settled (the thread reached its wait boundary).
// A turn can end without a reply, say after a refused conversations_send;
// Thinking would otherwise stay on. A reply has already settled the
// response, and a newer message's response has another id, so this is then
// a no-op. Core tracks the events of main; chat threads end with pace/done.
func (s *streamer) settleTrackedEvent(agent int64, thread, raw string) {
	var d struct {
		SourceEventID string `json:"source_event_id"`
	}
	if json.Unmarshal([]byte(raw), &d) != nil {
		return
	}
	chat, messageID, eventAgent, ok := parseConversationThreadEventID(d.SourceEventID)
	if !ok || eventAgent != agent {
		return
	}
	s.mu.Lock()
	p := s.responses[responseProgressKey(chat, agent)]
	match := p != nil && p.threadID == thread && p.AfterMessageID == messageID
	s.mu.Unlock()
	if !match {
		return
	}
	s.settleAck(chat, agent)
	s.finishResponse(chat, agent, messageID)
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
	value.ReadyTools = append([]ReadyTool(nil), p.ReadyTools...)
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
	if p == nil || (len(afterMessageIDs) == 0 && s.awaitingReceiptLocked(p)) || (len(afterMessageIDs) > 0 && p.AfterMessageID != afterMessageIDs[0]) {
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
	p.clearModelPass()
	frame := s.progressFrame(p)
	s.mu.Unlock()
	s.publish(frame)
	if s.onActivityChange != nil {
		s.onActivityChange(chat)
	}
}
func (s *streamer) intermediateReply(chat string, agent int64) {
	s.mu.Lock()
	if p := s.responses[responseProgressKey(chat, agent)]; p != nil && !s.awaitingReceiptLocked(p) {
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
		Chunk      string `json:"chunk"`
		Delta      string `json:"delta"`
		ArgBytes   int    `json:"arg_bytes"`
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
	argsChanged := false
	switch event {
	case "llm.start":
		phase = "thinking"
		if p.hadTools {
			phase = "continuing"
		}
		p.clearModelPass()
	case "llm.provider_error":
		// Core retries the request on another provider without a new
		// llm.start. Nothing the failed stream wrote will run.
		if p.CallID == "" && len(p.ReadyTools) == 0 {
			s.mu.Unlock()
			return
		}
		phase = "thinking"
		if p.hadTools {
			phase = "continuing"
		}
		p.clearModelPass()
	case "llm.tool_chunk":
		if !visibleActivityTool(name) {
			s.mu.Unlock()
			return
		}
		callID := firstNonEmptyString(d.ID, d.CallID, d.ToolCallID)
		if callID != "" && p.readyIndex(callID) >= 0 {
			s.mu.Unlock()
			return
		}
		if callID != p.CallID {
			p.rememberCurrentCall()
		}
		phase = "preparing_tool"
		p.ToolName = name
		p.CallID = callID
		if p.CallID != p.lastCallID || p.Phase != "preparing_tool" {
			p.ToolStartedAt = ts
			p.resetToolArgs()
		}
		argsChanged = p.trackToolArgs(firstNonEmptyString(d.Chunk, d.Delta), time.Now())
	case "llm.tool_ready":
		callID := firstNonEmptyString(d.ID, d.CallID, d.ToolCallID)
		if !visibleActivityTool(name) || callID == "" || p.readyIndex(callID) >= 0 {
			s.mu.Unlock()
			return
		}
		p.rememberCurrentCall()
		ready, ok := p.written[callID]
		if !ok {
			ready = ReadyTool{CallID: callID, ToolName: name, StartedAt: ts}
		}
		if d.ArgBytes > 0 {
			ready.ArgBytes = d.ArgBytes
		}
		ready.ReadyAt = ts
		p.ReadyTools = append(p.ReadyTools, ready)
		if callID == p.CallID {
			// The model is between calls: the next one has not started yet.
			p.ToolName = ""
			p.CallID = ""
			p.resetToolArgs()
		}
		phase = "preparing_tool"
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
		p.resetToolArgs()
	case "tool.call":
		if !visibleActivityTool(name) {
			s.mu.Unlock()
			return
		}
		phase = "running"
		p.hadTools = true
		if i := p.readyIndex(firstNonEmptyString(d.ID, d.CallID, d.ToolCallID)); i >= 0 {
			p.ReadyTools = append(p.ReadyTools[:i], p.ReadyTools[i+1:]...)
		}
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
	// Argument deltas are sent once per tool identity, then only when the
	// reason is read or the size grew (throttled), never per chunk.
	if phase == p.Phase && event == "llm.tool_chunk" && p.CallID == p.lastCallID && !argsChanged {
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
