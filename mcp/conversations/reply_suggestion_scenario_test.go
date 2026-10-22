//go:build scenario

package main

import (
	"strings"
	"testing"
	"time"
)

// Real generation and durable handoff are checked here; the paired browser
// suite checks actual Tab/touch acceptance and proves those gestures never send.
func TestScenario_SuggestedReply(t *testing.T) {
	c := newScenarioClient(t)
	agent, _ := c.ensureAgent()
	t.Log("waiting for newly provisioned agent to enter app telemetry ownership")
	time.Sleep(65 * time.Second)
	var conv struct {
		ID string `json:"id"`
	}
	if code := c.do("POST", "/api/apps/conversations/chats", map[string]any{"agent_id": agent, "title": "Suggested reply lifecycle"}, &conv); code != 200 || conv.ID == "" {
		t.Fatalf("create %d", code)
	}
	defer c.deleteConversation(conv.ID)
	path := "/api/apps/conversations/messages?chat_id=" + conv.ID
	rows := func() []Message {
		var out []Message
		if code := c.do("GET", path, nil, &out); code != 200 {
			t.Fatalf("history %d", code)
		}
		return out
	}
	send := func(text, key string) {
		if code := c.do("POST", path, map[string]any{"content": text, "client_message_id": key}, nil); code != 200 {
			t.Fatalf("send %d", code)
		}
	}
	wait := func(token string) Message {
		deadline := time.Now().Add(150 * time.Second)
		for time.Now().Before(deadline) {
			for _, m := range rows() {
				if m.Role == "agent" && m.Phase == "final" && strings.Contains(m.Content, token) {
					return m
				}
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("missing %s", token)
		return Message{}
	}
	idle := func() {
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			var out struct {
				Active []string `json:"active_conversation_ids"`
			}
			c.do("GET", "/api/apps/conversations/activity-summary", nil, &out)
			active := false
			for _, id := range out.Active {
				active = active || id == conv.ID
			}
			if !active {
				return
			}
			time.Sleep(time.Second)
		}
		t.Fatal("suggestion left response active")
	}
	const suggested = "Run the dry-run validation next."
	send(`For this isolated test, send one final reply containing SUGGESTION_READY to confirm the plan is ready. Attach exactly one suggested next user message using the suggested-reply feature: "Run the dry-run validation next." Do not perform or claim to perform that action; wait for me to actually send it. When I later send that exact message, reply with final text SUGGESTION_ACCEPTED and no further suggestion. This is conversation-only testing; do not modify any external system.`, "suggestion-request")
	first := wait("SUGGESTION_READY")
	if first.ComponentKind != "" || len(first.Components) != 1 || first.Components[0].Name != replySuggestionComponent || first.Components[0].Interaction != nil {
		t.Fatalf("bad suggestion %+v", first)
	}
	items, err := genericObjectsArg(first.Components[0].Props, "items", 1)
	if err != nil || len(items) != 1 || items[0]["text"] != suggested {
		t.Fatalf("wrong suggestion %+v %v", items, err)
	}
	idle()
	var inbox struct {
		Items []struct {
			Message Message `json:"message"`
		} `json:"items"`
	}
	c.do("GET", "/api/apps/conversations/inbox?page=1", nil, &inbox)
	for _, item := range inbox.Items {
		if item.Message.ID == first.ID {
			t.Fatal("suggestion became inbox attention")
		}
	}
	time.Sleep(8 * time.Second)
	history := rows()
	agentRows := 0
	userRows := 0
	for _, m := range history {
		if m.Role == "agent" {
			agentRows++
		}
		if m.Role == "user" {
			userRows++
		}
	}
	if agentRows != 1 || userRows != 1 {
		t.Fatalf("suggestion auto-continued: %d agent, %d user", agentRows, userRows)
	}
	var restored *Message
	for i := range history {
		if history[i].ID == first.ID {
			restored = &history[i]
		}
	}
	if restored == nil || len(restored.Components) != 1 {
		t.Fatal("suggestion lost on history reload")
	}
	send(suggested, "accepted-suggestion")
	final := wait("SUGGESTION_ACCEPTED")
	for _, component := range final.Components {
		if component.Name == replySuggestionComponent {
			t.Fatal("unexpected next suggestion")
		}
	}
	idle()
	send(suggested, "accepted-suggestion") // Stable send retry is one user turn.
	time.Sleep(5 * time.Second)
	accepted := 0
	replies := 0
	for _, m := range rows() {
		if m.Role == "user" && m.Content == suggested {
			accepted++
		}
		if m.Role == "agent" && strings.Contains(m.Content, "SUGGESTION_ACCEPTED") {
			replies++
		}
	}
	if accepted != 1 || replies != 1 {
		t.Fatalf("duplicate handoff: user=%d reply=%d", accepted, replies)
	}
	send("Thanks, that's all for now. Reply with final text SUGGESTION_END and no follow-up suggestion.", "suggestion-end")
	end := wait("SUGGESTION_END")
	for _, component := range end.Components {
		if component.Name == replySuggestionComponent {
			t.Fatal("routine closure got a suggestion")
		}
	}
	idle()
	t.Log("one suggested reply persisted with the final; no extra send or execution before actual user input; accepted text resumed once; later final cleared suggestions")
}
