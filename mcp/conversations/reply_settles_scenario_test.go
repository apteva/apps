//go:build scenario

package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestScenario_ThinkingEndsAfterReplyAndPace guards the prod Helper chat of
// 2026-11-25 end to end: on its own chat thread the agent replies and paces
// with clear_wake. Thinking must end with the reply and stay ended, and the
// operator's next message must be released at once, not queued. In prod the
// Helper also reported to main with Core's send in the pace step, so Core ran
// one more model call that reopened Thinking; models rarely repeat that exact
// batch, so response_progress_test.go replays the prod events deterministically.
func TestScenario_ThinkingEndsAfterReplyAndPace(t *testing.T) {
	c := newScenarioClient(t)
	agentID, cleanupAgent := c.ensureAgent()
	defer cleanupAgent()
	// Main's model events must reach Conversations for the bug to show.
	t.Log("waiting for the new agent to join app telemetry ownership")
	time.Sleep(65 * time.Second)

	newChat := func(title string) string {
		t.Helper()
		var conv struct {
			ID string `json:"id"`
		}
		if status := c.do("POST", "/api/apps/conversations/chats", map[string]any{"agent_id": agentID, "title": title}, &conv); status != http.StatusOK || conv.ID == "" {
			t.Fatalf("create conversation: status=%d", status)
		}
		return conv.ID
	}
	// The agent's first operator chat binds to main, whose model calls never
	// open a response on their own. The prod Helper answered on its own chat
	// thread: take the main binding first, then test a second chat.
	defer c.claimMainRouting(agentID)()
	conv := struct{ ID string }{newChat("Helper")}
	defer c.do("DELETE", "/api/apps/conversations/chats?id="+conv.ID, nil, nil)
	path := "/api/apps/conversations/messages?chat_id=" + conv.ID
	rows := func() []Message {
		var out []Message
		c.do("GET", path, nil, &out)
		return out
	}
	waitFinal := func(marker string, after int64) Message {
		t.Helper()
		for deadline := time.Now().Add(180 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
			for _, m := range rows() {
				if m.Role == "agent" && m.Phase == "final" && m.ID > after && strings.Contains(m.Content, marker) {
					return m
				}
			}
		}
		t.Fatalf("no final reply containing %s within 180s", marker)
		return Message{}
	}

	first := `Reply with conversations_send phase=final and text "REPLY_ONE: done". After that, if you can, report to main with send (id="main", message="REPORT ONLY: REPLY_ONE sent") in the same response as pace with clear_wake="true", so you only wake for new messages.`
	var sent Message
	if status := c.do("POST", path, map[string]any{"content": first, "client_message_id": "reply-settles-1"}, &sent); status != http.StatusOK {
		t.Fatalf("post message: status=%d", status)
	}
	var participants struct {
		Agents []AgentModelInfo `json:"agents"`
	}
	c.do("GET", "/api/apps/conversations/participants?id="+conv.ID, nil, &participants)
	if len(participants.Agents) != 1 || participants.Agents[0].ThreadID != conversationThreadID(conv.ID) {
		t.Fatalf("the second chat should run on its own thread: %+v", participants.Agents)
	}
	reply := waitFinal("REPLY_ONE", sent.ID)
	defer func() { t.Logf("chat thread events: %s", c.threadEvents(agentID, conversationThreadID(conv.ID))) }()

	// Thinking ends with the reply and the pace after it, and stays ended.
	c.waitIdle(conv.ID, 45*time.Second)
	frames := c.liveFrames(conv.ID)
	defer frames.stop()
	quietFrom := time.Now()
	time.Sleep(15 * time.Second)
	if n := frames.thinkingBetween(quietFrom, time.Now()); n != 0 {
		t.Fatalf("Thinking reopened after the reply with no new input: %d frames; %s", n, frames.summary())
	}
	c.waitIdle(conv.ID, time.Second)

	// The next message is delivered at once, not queued behind a ghost turn.
	var next Message
	if status := c.do("POST", path, map[string]any{"content": `Reply with conversations_send phase=final and text "REPLY_TWO: done".`, "client_message_id": "reply-settles-2"}, &next); status != http.StatusOK {
		t.Fatalf("post follow-up: status=%d", status)
	}
	if next.QueueState == "queued" {
		t.Fatalf("follow-up was queued behind a finished turn: %+v", next)
	}
	waitFinal("REPLY_TWO", next.ID)
	c.waitIdle(conv.ID, 45*time.Second)
	t.Logf("first reply %d settled; follow-up %d released and answered", reply.ID, next.ID)
}

// threadEvents lists one thread's model and tool events, oldest first.
func (c *scenarioClient) threadEvents(agentID int64, thread string) string {
	var events []struct {
		Type     string    `json:"type"`
		ThreadID string    `json:"thread_id"`
		Time     time.Time `json:"time"`
		Data     struct {
			Name string         `json:"name"`
			Args map[string]any `json:"args"`
		} `json:"data"`
	}
	c.do("GET", fmt.Sprintf("/api/telemetry?agent_id=%d&limit=1000", agentID), nil, &events)
	var parts []string
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		switch e.Type {
		case "llm.start", "llm.done", "tool.call", "tool.result", "event.received", "thread.done", "llm.error":
		default:
			continue
		}
		if e.ThreadID != thread {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %s %s %v", e.Time.Format("15:04:05.000"), e.Type, e.Data.Name, e.Data.Args))
	}
	return strings.Join(parts, "\n")
}
