//go:build integration

package main

// Tier 2 — the real binary, real HTTP. Boot the sidecar, talk MCP +
// REST. Validates the SDK wiring tier 1 cannot see: manifest parsing
// at boot, migrations applied to a fresh on-disk DB, JSON-RPC
// dispatch, route mounting (including the reserved-prefix guard
// against the real mux), /health, and caller-header auth.
//
// Run with:  go test -tags integration ./...

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// mcpErr calls a tool expecting a tool-level failure and returns the
// JSON-RPC error message (tool errors surface as code -32000 through
// the real dispatch, which the testkit helpers treat as fatal).
func mcpErr(t *testing.T, sc *tk.Sidecar, tool string, args map[string]any, agentID int64) string {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	req, _ := http.NewRequest("POST", sc.URL()+"/mcp", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+sc.Token())
	if agentID != 0 {
		req.Header.Set("X-Apteva-Caller-Agent", strconv.FormatInt(agentID, 10))
		req.Header.Set("X-Apteva-Caller-Thread", "main")
		req.Header.Set("X-Apteva-Project-ID", itProject)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("mcp request: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Error == nil {
		t.Fatalf("%s: expected a tool error, got success", tool)
	}
	return out.Error.Message
}

const itProject = "test-proj"

func TestSidecar_BootsAndHealthOK(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(itProject))
	var got map[string]any
	resp := sc.GET("/health", &got)
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	if got["ok"] != true {
		t.Errorf("/health body=%v", got)
	}
}

// The 0.5.x agent contract end-to-end over real JSON-RPC: create
// (title-idempotent) → alert into it → list with query → history.
func TestSidecar_AgentToolFlow(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(itProject))

	created := sc.MCPAs("conversations_create",
		map[string]any{"title": "Infra monitoring"}, 41, "main", itProject)
	convID, _ := created["conversation_id"].(string)
	if convID == "" || created["created"] != true {
		t.Fatalf("create = %v", created)
	}
	again := sc.MCPAs("conversations_create",
		map[string]any{"title": "infra MONITORING"}, 41, "worker-1", itProject)
	if again["conversation_id"] != convID || again["created"] != false {
		t.Fatalf("second create = %v, want reuse of %s", again, convID)
	}

	alerted := sc.MCPAs("conversations_alert", map[string]any{
		"conversation_id": convID, "text": "disk almost full", "severity": "warn",
	}, 41, "main", itProject)
	if _, ok := alerted["message_id"]; !ok {
		t.Fatalf("alert = %v", alerted)
	}

	listed := sc.MCPAs("conversations_list",
		map[string]any{"query": "infra"}, 41, "main", itProject)
	conversations, _ := listed["conversations"].([]any)
	if len(conversations) != 1 {
		t.Fatalf("list query = %v, want exactly the monitoring conversation", listed)
	}

	history := sc.MCPAs("conversations_history",
		map[string]any{"conversation_id": convID}, 41, "main", itProject)
	if !strings.Contains(fmt.Sprint(history), "disk almost full") {
		t.Fatalf("history missing the alert: %v", history)
	}
}

// Omitting conversation_id must return the teaching error — through
// the real JSON-RPC error surface, not just the in-process handler.
func TestSidecar_AlertWithoutConversationTeachesFlow(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(itProject))
	msg := mcpErr(t, sc, "conversations_alert", map[string]any{"text": "orphan"}, 41)
	if !strings.Contains(msg, "conversations_create") {
		t.Fatalf("error = %q, want the list/create teaching error", msg)
	}
}

// Fail-closed without caller headers: the platform gateway always
// stamps them; a bare call must not write.
func TestSidecar_AnonymousCallerRefused(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(itProject))
	msg := mcpErr(t, sc, "conversations_create", map[string]any{"title": "Anon"}, 0)
	if !strings.Contains(strings.ToLower(msg), "caller identity") {
		t.Fatalf("error = %q, want caller-identity refusal", msg)
	}
}

// The dashboard-facing REST surface against the real mux: user chat
// creation, message append + read-back, inbox visibility, dismissal.
func TestSidecar_HTTPSurface(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(itProject))

	var conv map[string]any
	if resp := sc.POST("/chats", map[string]any{
		"agent_id": 41, "title": "Ops chat", "project_id": itProject,
	}, &conv); resp.Status != 200 {
		t.Fatalf("create chat: %d %s", resp.Status, resp.Body)
	}
	convID, _ := conv["id"].(string)
	if convID == "" {
		t.Fatalf("chat = %v", conv)
	}

	var msg map[string]any
	if resp := sc.POST("/messages?chat_id="+convID, map[string]any{
		"content": "hello over real HTTP", "client_message_id": "it-1",
	}, &msg); resp.Status != 200 {
		t.Fatalf("post message: %d %s", resp.Status, resp.Body)
	}

	var transcript []map[string]any
	if resp := sc.GET("/messages?chat_id="+convID, &transcript); resp.Status != 200 {
		t.Fatalf("get messages: %d", resp.Status)
	}
	// Two rows: the user message, then the LOUD system notice — with
	// no platform behind the sidecar, agent forwarding fails and the
	// failure must be visible, never silent. Tier 2 is the only place
	// that path runs through the real HTTP stack.
	if len(transcript) != 2 || transcript[0]["content"] != "hello over real HTTP" {
		t.Fatalf("transcript = %v", transcript)
	}
	if transcript[1]["role"] != "system" ||
		!strings.Contains(fmt.Sprint(transcript[1]["content"]), "could not reach") {
		t.Fatalf("expected loud forward-failure notice, got %v", transcript[1])
	}

	// An MCP-raised alert shows up in the REST inbox — the two
	// surfaces share one store in the real binary too.
	alerted := sc.MCPAs("conversations_alert", map[string]any{
		"conversation_id": convID, "text": "http-tier alert", "severity": "error",
	}, 41, "main", itProject)
	messageID, ok := alerted["message_id"].(float64)
	if !ok {
		t.Fatalf("alert = %v", alerted)
	}

	var inbox []map[string]any
	if resp := sc.GET("/inbox?limit=10", &inbox); resp.Status != 200 {
		t.Fatalf("inbox: %d", resp.Status)
	}
	if len(inbox) != 1 {
		t.Fatalf("inbox = %v, want the alert", inbox)
	}

	var dismissed map[string]any
	if resp := sc.POST("/message-dismiss", map[string]any{
		"message_id": int64(messageID),
	}, &dismissed); resp.Status != 200 {
		t.Fatalf("dismiss: %d %s", resp.Status, resp.Body)
	}
	if resp := sc.GET("/inbox?limit=10", &inbox); resp.Status != 200 || len(inbox) != 0 {
		t.Fatalf("inbox after dismiss = %v (status %d), want empty", inbox, resp.Status)
	}
}

// Public audience (0.6.0) through the real binary: keyed creation is
// public find-or-create, inbox kinds are refused via real JSON-RPC,
// and replying still works.
func TestSidecar_PublicConversationFlow(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(itProject))

	create := func() map[string]any {
		var conv map[string]any
		if resp := sc.POST("/chats", map[string]any{
			"agent_id": 41, "title": "Visitor", "conversation_key": "app:webchat:v-1",
		}, &conv); resp.Status != 200 {
			t.Fatalf("keyed create: %d %s", resp.Status, resp.Body)
		}
		return conv
	}
	conv := create()
	convID, _ := conv["id"].(string)
	if conv["audience"] != "public" || convID == "" {
		t.Fatalf("keyed create = %v, want public", conv)
	}
	if again := create(); again["id"] != convID {
		t.Fatalf("keyed create minted a duplicate: %v vs %v", again["id"], convID)
	}

	msg := mcpErr(t, sc, "conversations_alert", map[string]any{
		"conversation_id": convID, "text": "internal", "severity": "error",
	}, 41)
	if !strings.Contains(msg, "operator conversation") {
		t.Fatalf("alert into public = %q, want operator-conversation refusal", msg)
	}

	sent := sc.MCPAs("conversations_send", map[string]any{
		"conversation_id": convID, "text": "Happy to help!",
	}, 41, "chat-"+convID, itProject)
	if _, ok := sent["message_id"]; !ok {
		t.Fatalf("send into public conversation = %v", sent)
	}
}
