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
		"directive": "You answer questions in chat conversations, briefly and directly.",
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
