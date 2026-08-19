//go:build live

package main

// Tier 3 — live smoke against a real Apteva platform with a real
// LLM-backed agent. One simple scenario: a Codex-backed agent answers
// a chat message through the conversations app end-to-end (platform
// proxy → sidecar → thread spawn → provider → conversations_send).
//
// Credentials come from the operator's shell, never a checked-in
// fixture:
//
//	APTEVA_API_KEY=sk-...            (required)
//	APTEVA_BASE_URL=http://...       (default http://localhost:5280)
//	APTEVA_LIVE_AGENT_ID=123         (optional: reuse an existing agent)
//	APTEVA_LIVE_PROJECT_ID=...       (optional: project for a temp agent)
//
// Without APTEVA_LIVE_AGENT_ID the test creates a temporary agent
// whose default provider is openai-codex and deletes it afterward —
// this requires the conversations install to have
// default_for_new_agents enabled so the new agent gets the MCP.
//
// Everything the test creates (agent, conversation) it deletes. It
// never touches pre-existing conversations.
//
// Run:
//	APTEVA_API_KEY=sk-... go test -tags live -v -run TestLive_ ./...

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type liveClient struct {
	t    *testing.T
	base string
	key  string
}

func newLiveClient(t *testing.T) *liveClient {
	key := os.Getenv("APTEVA_API_KEY")
	if key == "" {
		t.Skip("APTEVA_API_KEY not set — skipping live smoke")
	}
	base := os.Getenv("APTEVA_BASE_URL")
	if base == "" {
		base = "http://localhost:5280"
	}
	return &liveClient{t: t, base: strings.TrimRight(base, "/"), key: key}
}

func (c *liveClient) do(method, path string, body any, out any) int {
	c.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatalf("build %s %s: %v", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

// ensureAgent returns (agentID, cleanup). With APTEVA_LIVE_AGENT_ID it
// reuses the operator's agent; otherwise it creates a temporary
// Codex-backed one and the cleanup deletes it.
func (c *liveClient) ensureAgent() (int64, func()) {
	c.t.Helper()
	if raw := os.Getenv("APTEVA_LIVE_AGENT_ID"); raw != "" {
		var id int64
		fmt.Sscanf(raw, "%d", &id)
		if id == 0 {
			c.t.Fatalf("invalid APTEVA_LIVE_AGENT_ID %q", raw)
		}
		return id, func() {}
	}
	var created struct {
		ID int64 `json:"id"`
	}
	status := c.do("POST", "/api/agents", map[string]any{
		"name":      "conversations-live-codex",
		"directive": "You are an operations agent. Answer chat messages briefly and directly. When monitor or scheduler events arrive, act on them per your conversations skill.",
		"mode":      "autonomous",
		"config":    `{"default_provider":"openai-codex","include_channels":false}`,
		"project_id": os.Getenv("APTEVA_LIVE_PROJECT_ID"),
	}, &created)
	if status != 200 || created.ID == 0 {
		c.t.Fatalf("create temp agent: status=%d id=%d", status, created.ID)
	}
	c.t.Logf("created temp Codex agent %d", created.ID)
	return created.ID, func() {
		c.do("POST", fmt.Sprintf("/api/agents/%d/stop", created.ID), nil, nil)
		c.do("DELETE", fmt.Sprintf("/api/agents/%d", created.ID), nil, nil)
	}
}

// TestLive_CodexChatRoundTrip: create a conversation with the agent,
// send one message, and require a real model-authored reply through
// conversations_send within the deadline.
func TestLive_CodexChatRoundTrip(t *testing.T) {
	c := newLiveClient(t)
	agentID, cleanupAgent := c.ensureAgent()
	defer cleanupAgent()

	var conv struct {
		ID string `json:"id"`
	}
	status := c.do("POST", "/api/apps/conversations/chats", map[string]any{
		"agent_id": agentID, "title": "Live smoke (codex)",
	}, &conv)
	if status != 200 || conv.ID == "" {
		t.Fatalf("create conversation: status=%d conv=%+v", status, conv)
	}
	defer c.do("DELETE", "/api/apps/conversations/chats?id="+conv.ID, nil, nil)

	status = c.do("POST", "/api/apps/conversations/messages?chat_id="+conv.ID, map[string]any{
		"content":           "Reply with exactly one word: pong",
		"client_message_id": "live-smoke-1",
	}, nil)
	if status != 200 {
		t.Fatalf("post message: status=%d", status)
	}

	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		var transcript []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		c.do("GET", "/api/apps/conversations/messages?chat_id="+conv.ID, nil, &transcript)
		for _, m := range transcript {
			if m.Role == "agent" {
				if !strings.Contains(strings.ToLower(m.Content), "pong") {
					t.Fatalf("agent replied %q, want pong", m.Content)
				}
				t.Logf("live round-trip OK: %q", m.Content)
				return
			}
		}
		time.Sleep(4 * time.Second)
	}
	t.Fatal("no agent reply within 120s")
}

// ─── background scenarios (the manually-proven suite, codified) ─────

func (c *liveClient) injectEvent(agentID int64, text string) {
	c.t.Helper()
	status := c.do("POST", fmt.Sprintf("/api/agents/%d/event", agentID),
		map[string]any{"message": text}, nil)
	if status != 200 {
		c.t.Fatalf("inject event: status=%d", status)
	}
}

type liveInboxItem struct {
	Priority int `json:"priority"`
	Message  struct {
		ID             int64  `json:"id"`
		ConversationID string `json:"conversation_id"`
		AgentID        int64  `json:"agent_id"`
		ComponentKind  string `json:"component_kind"`
		Severity       string `json:"severity"`
		Content        string `json:"content"`
		Components     []struct {
			Name  string         `json:"name"`
			Props map[string]any `json:"props"`
		} `json:"components"`
	} `json:"message"`
}

// pollInbox waits for a pending inbox item from the given agent with
// the given kind. The inbox is instance-global, so filtering by agent
// keeps the test independent of pre-existing items.
func (c *liveClient) pollInbox(agentID int64, kind string, deadline time.Duration) liveInboxItem {
	c.t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		var items []liveInboxItem
		c.do("GET", "/api/apps/conversations/inbox?limit=100", nil, &items)
		for _, item := range items {
			if item.Message.AgentID == agentID && item.Message.ComponentKind == kind {
				return item
			}
		}
		time.Sleep(4 * time.Second)
	}
	c.t.Fatalf("no pending %s from agent %d within %s", kind, agentID, deadline)
	return liveInboxItem{}
}

func (c *liveClient) deleteConversation(id string) {
	if id != "" {
		c.do("DELETE", "/api/apps/conversations/chats?id="+id, nil, nil)
	}
}

// TestLive_CodexAlertFlow: a monitor error at main must become an
// error/warn alert in an agent-created conversation — the agent picks
// or creates the conversation itself (list → create → alert).
func TestLive_CodexAlertFlow(t *testing.T) {
	c := newLiveClient(t)
	agentID, cleanupAgent := c.ensureAgent()
	defer cleanupAgent()

	c.injectEvent(agentID, "[monitor] ERROR: nightly backup job failed on "+
		"build-server — rsync exit 23, retries exhausted. No operator is watching "+
		"any chat. Raise an operator alert per your conversations skill.")

	item := c.pollInbox(agentID, "alert", 180*time.Second)
	defer c.deleteConversation(item.Message.ConversationID)
	if item.Message.Severity != "error" && item.Message.Severity != "warn" {
		t.Fatalf("severity = %q, want error or warn", item.Message.Severity)
	}
	var conv struct {
		Origin string `json:"origin"`
		Title  string `json:"title"`
	}
	// The chats list carries origin; fetch and find ours.
	var chats []map[string]any
	c.do("GET", "/api/apps/conversations/chats", nil, &chats)
	for _, ch := range chats {
		if ch["id"] == item.Message.ConversationID {
			conv.Origin, _ = ch["origin"].(string)
			conv.Title, _ = ch["title"].(string)
		}
	}
	if conv.Origin != "agent" || conv.Title == "" {
		t.Fatalf("alert conversation = %+v, want an agent-created titled conversation", conv)
	}
	t.Logf("alert landed in agent-created %q (%s)", conv.Title, item.Message.ConversationID)
}

// TestLive_CodexApprovalRoundTrip: the agent requests approval for a
// destructive action and STOPS; the operator approves with a note;
// the card mutates in place and the verdict reaches the agent, which
// acknowledges in the conversation.
func TestLive_CodexApprovalRoundTrip(t *testing.T) {
	c := newLiveClient(t)
	agentID, cleanupAgent := c.ensureAgent()
	defer cleanupAgent()

	c.injectEvent(agentID, "[monitor] The backup target disk is full. Deleting the "+
		"oldest backup archive would free 120GB but is destructive — request operator "+
		"approval per your conversations skill BEFORE doing anything, then wait for the verdict.")

	item := c.pollInbox(agentID, "approval", 180*time.Second)
	defer c.deleteConversation(item.Message.ConversationID)

	status := c.do("POST", "/api/apps/conversations/message-action", map[string]any{
		"message_id": item.Message.ID, "action_id": "approve",
		"note": "Approved — verify the two newest backups are intact first.",
	}, nil)
	if status != 200 {
		t.Fatalf("message-action: status=%d", status)
	}

	// The card must mutate in place, and the agent must acknowledge the
	// verdict in the same conversation (proof the approval.result event
	// reached its thread).
	deadline := time.Now().Add(120 * time.Second)
	acked := false
	for time.Now().Before(deadline) && !acked {
		var transcript []struct {
			ID         int64  `json:"id"`
			Role       string `json:"role"`
			Content    string `json:"content"`
			Components []struct {
				Props map[string]any `json:"props"`
			} `json:"components"`
		}
		c.do("GET", "/api/apps/conversations/messages?chat_id="+item.Message.ConversationID, nil, &transcript)
		cardApproved := false
		for _, m := range transcript {
			if m.ID == item.Message.ID && len(m.Components) > 0 &&
				m.Components[0].Props["status"] == "approve" {
				cardApproved = true
			}
			if m.Role == "agent" && m.ID > item.Message.ID {
				acked = true
			}
		}
		if !cardApproved && acked {
			t.Fatal("agent acknowledged but the card did not mutate to approved")
		}
		if !acked {
			time.Sleep(4 * time.Second)
		}
	}
	if !acked {
		t.Fatal("no agent acknowledgment after the verdict within 120s")
	}
	t.Log("approval card mutated and the agent acknowledged the verdict")
}

// TestLive_CodexReportFlow: a scheduler event produces a report that
// is pending in the inbox AND visible in its conversation's
// transcript (the 0.5.1 guarantee).
func TestLive_CodexReportFlow(t *testing.T) {
	c := newLiveClient(t)
	agentID, cleanupAgent := c.ensureAgent()
	defer cleanupAgent()

	c.injectEvent(agentID, "[scheduler] Weekly reporting cycle is due. File your "+
		"weekly report per your conversations skill. This week: 2 monitor errors on "+
		"build-server handled, no other activity.")

	item := c.pollInbox(agentID, "report", 180*time.Second)
	defer c.deleteConversation(item.Message.ConversationID)

	var transcript []struct {
		ID            int64  `json:"id"`
		ComponentKind string `json:"component_kind"`
	}
	c.do("GET", "/api/apps/conversations/messages?chat_id="+item.Message.ConversationID, nil, &transcript)
	found := false
	for _, m := range transcript {
		if m.ID == item.Message.ID && m.ComponentKind == "report" {
			found = true
		}
	}
	if !found {
		t.Fatal("report pending in the inbox but missing from its conversation's transcript")
	}
	t.Logf("report visible in inbox and transcript (%s)", item.Message.ConversationID)
}
