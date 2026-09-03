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
	"bufio"
	"bytes"
	"context"
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
	if strings.HasPrefix(path, "/api/apps/conversations/") {
		projectID := strings.TrimSpace(os.Getenv("APTEVA_LIVE_PROJECT_ID"))
		if projectID == "" {
			c.t.Fatalf("APTEVA_LIVE_PROJECT_ID is required for conversations live tests")
		}
		separator := "?"
		if strings.Contains(path, "?") {
			separator = "&"
		}
		path += separator + "project_id=" + projectID
	}
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
		"name":       "conversations-live-codex",
		"directive":  "You are an operations and customer-support agent. Answer chat messages briefly and directly. When monitor or scheduler events arrive, act on them per your conversations skill. Policy: you may approve refunds up to $100 yourself; larger refunds require operator approval. Refuse impossible or absurd requests politely without escalating.",
		"mode":       "autonomous",
		"config":     `{"default_provider":"openai-codex","include_channels":false}`,
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

// TestLive_CodexSoftBreak opens the real streaming channel, waits for the
// acknowledgement that makes the UI's Break control visible, then submits a
// durable advisory event against that exact active response. The model may
// finish an already-running provider call; it must nevertheless consume the
// later event and acknowledge the user's changed intent on its next turn.
func TestLive_CodexSoftBreak(t *testing.T) {
	c := newLiveClient(t)
	agentID, cleanupAgent := c.ensureAgent()
	defer cleanupAgent()

	var conv struct {
		ID string `json:"id"`
	}
	status := c.do("POST", "/api/apps/conversations/chats", map[string]any{
		"agent_id": agentID, "title": "Live soft break (codex)",
	}, &conv)
	if status != http.StatusOK || conv.ID == "" {
		t.Fatalf("create conversation: status=%d conv=%+v", status, conv)
	}
	defer c.do("DELETE", "/api/apps/conversations/chats?id="+conv.ID, nil, nil)

	streamCtx, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()
	streamURL := c.base + "/api/apps/conversations/stream?chat_id=" + conv.ID +
		"&project_id=" + os.Getenv("APTEVA_LIVE_PROJECT_ID")
	streamReq, err := http.NewRequestWithContext(streamCtx, http.MethodGet, streamURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	streamReq.Header.Set("Authorization", "Bearer "+c.key)
	streamResp, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		t.Fatalf("open conversation stream: %v", err)
	}
	defer streamResp.Body.Close()
	if streamResp.StatusCode != http.StatusOK {
		t.Fatalf("open conversation stream: status=%d", streamResp.StatusCode)
	}
	frames := make(chan StreamFrame, 8)
	go func() {
		scanner := bufio.NewScanner(streamResp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var frame StreamFrame
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame) == nil && frame.CallID != "" {
				select {
				case frames <- frame:
				case <-streamCtx.Done():
					return
				}
			}
		}
	}()

	status = c.do("POST", "/api/apps/conversations/messages?chat_id="+conv.ID, map[string]any{
		"content":           "Begin preparing a detailed answer about resilient job queues.",
		"client_message_id": "live-soft-break-start",
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("post initial message: status=%d", status)
	}

	var active StreamFrame
	deadline := time.After(15 * time.Second)
	for active.CallID == "" {
		select {
		case frame := <-frames:
			if !frame.Done && frame.ConversationID == conv.ID {
				active = frame
			}
		case <-deadline:
			t.Fatal("no active response frame within 15s")
		}
	}
	if active.AgentID != agentID {
		t.Fatalf("active response agent=%d, want %d", active.AgentID, agentID)
	}
	t.Logf("active response call=%s agent=%d phase=%s", active.CallID, active.AgentID, active.Phase)

	breakBody := map[string]any{
		"content":           "Pause here and reconsider before continuing. Reply exactly SOFT_BREAK_ACKNOWLEDGED.",
		"intent":            messageIntentSoftBreak,
		"target_call_id":    active.CallID,
		"target_agent_ids":  []int64{active.AgentID},
		"client_message_id": "live-soft-break-1",
	}
	var breakMessage Message
	for attempt := 1; attempt <= 2; attempt++ {
		var responseTarget any
		if attempt == 1 {
			responseTarget = &breakMessage
		}
		status = c.do("POST", "/api/apps/conversations/messages?chat_id="+conv.ID, breakBody, responseTarget)
		if status != http.StatusOK {
			t.Fatalf("post soft break attempt %d: status=%d", attempt, status)
		}
	}
	if breakMessage.ID == 0 || messageIntent(&breakMessage) != messageIntentSoftBreak {
		t.Fatalf("soft break was not persisted correctly: %+v", breakMessage)
	}

	responseDeadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(responseDeadline) {
		var transcript []Message
		c.do("GET", "/api/apps/conversations/messages?chat_id="+conv.ID, nil, &transcript)
		breakRows := 0
		acknowledged := false
		for _, message := range transcript {
			if messageIntent(&message) == messageIntentSoftBreak {
				breakRows++
				if message.Metadata["target_call_id"] != active.CallID {
					t.Fatalf("soft break target_call_id=%v, want %s", message.Metadata["target_call_id"], active.CallID)
				}
			}
			if message.Role == "agent" && message.ID > breakMessage.ID && strings.Contains(message.Content, "SOFT_BREAK_ACKNOWLEDGED") {
				acknowledged = true
			}
		}
		if breakRows > 1 {
			t.Fatalf("idempotent retry created %d soft-break rows", breakRows)
		}
		if breakRows == 1 && acknowledged {
			t.Log("live soft break was durable, idempotent, agent-scoped, and consumed by Codex")
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatal("Codex did not acknowledge the soft-break event within 150s")
}

// TestLive_CodexTwoConversationIsolation proves that one agent can hold two
// simultaneous Conversations threads without replies crossing between them.
func TestLive_CodexTwoConversationIsolation(t *testing.T) {
	c := newLiveClient(t)
	agentID, cleanupAgent := c.ensureAgent()
	defer cleanupAgent()

	create := func(title string) string {
		t.Helper()
		var conv struct {
			ID string `json:"id"`
		}
		status := c.do("POST", "/api/apps/conversations/chats", map[string]any{
			"agent_id": agentID, "title": title,
		}, &conv)
		if status != http.StatusOK || conv.ID == "" {
			t.Fatalf("create %s: status=%d conv=%+v", title, status, conv)
		}
		return conv.ID
	}
	firstID := create("Live isolation alpha")
	secondID := create("Live isolation beta")
	defer c.deleteConversation(firstID)
	defer c.deleteConversation(secondID)

	post := func(conversationID, word, clientID string) {
		t.Helper()
		status := c.do("POST", "/api/apps/conversations/messages?chat_id="+conversationID, map[string]any{
			"content": "Reply with exactly one word: " + word, "client_message_id": clientID,
		}, nil)
		if status != http.StatusOK {
			t.Fatalf("post %s: status=%d", word, status)
		}
	}
	agentReplies := func(conversationID string) []string {
		t.Helper()
		var transcript []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		c.do("GET", "/api/apps/conversations/messages?chat_id="+conversationID, nil, &transcript)
		var replies []string
		for _, message := range transcript {
			if message.Role == "agent" {
				replies = append(replies, strings.ToLower(message.Content))
			}
		}
		return replies
	}
	waitFor := func(conversationID, word string) {
		t.Helper()
		deadline := time.Now().Add(120 * time.Second)
		for time.Now().Before(deadline) {
			for _, reply := range agentReplies(conversationID) {
				if strings.Contains(reply, word) {
					return
				}
			}
			time.Sleep(4 * time.Second)
		}
		t.Fatalf("conversation %s received no %q reply", conversationID, word)
	}

	post(firstID, "alpha", "live-isolation-alpha")
	waitFor(firstID, "alpha")
	if replies := agentReplies(secondID); len(replies) != 0 {
		t.Fatalf("alpha reply crossed into second conversation: %v", replies)
	}
	post(secondID, "beta", "live-isolation-beta")
	waitFor(secondID, "beta")
	for _, reply := range agentReplies(firstID) {
		if strings.Contains(reply, "beta") {
			t.Fatalf("beta reply crossed into first conversation: %v", reply)
		}
	}
	t.Logf("isolated replies stayed in %s and %s", firstID, secondID)
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

// approvalActionID follows the card contract instead of assuming the model
// omitted the optional actions argument. Real agents may use a domain-specific
// positive action such as "delete_after_verification"; that is still a valid
// approval and the operator must submit one of the IDs the card advertises.
func approvalActionID(item liveInboxItem) string {
	first := ""
	for _, component := range item.Message.Components {
		if component.Name != "approval-card" {
			continue
		}
		actions, _ := component.Props["actions"].([]any)
		for _, raw := range actions {
			action, _ := raw.(map[string]any)
			id, _ := action["id"].(string)
			if id == "approve" {
				return id
			}
			if first == "" && id != "" {
				first = id
			}
		}
	}
	if first != "" {
		return first
	}
	return "approve"
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
	actionID := approvalActionID(item)
	t.Logf("resolving advertised approval action %q", actionID)

	status := c.do("POST", "/api/apps/conversations/message-action", map[string]any{
		"message_id": item.Message.ID, "action_id": actionID,
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
				m.Components[0].Props["status"] == actionID {
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

// TestLive_CodexPublicVisitorEscalation — the public-audience story
// end-to-end with a real model: a visitor (public keyed conversation,
// as a gateway app would create it) asks for something above the
// bot's authority. The agent must reply to the visitor in the public
// conversation, must NOT put any inbox item there (the structural
// guard), and should escalate by raising an approval or alert in an
// OPERATOR conversation.
func TestLive_CodexPublicVisitorEscalation(t *testing.T) {
	c := newLiveClient(t)
	agentID, cleanupAgent := c.ensureAgent()
	defer cleanupAgent()

	var conv struct {
		ID       string `json:"id"`
		Audience string `json:"audience"`
	}
	status := c.do("POST", "/api/apps/conversations/chats", map[string]any{
		"agent_id": agentID, "title": "Visitor — Sam",
		"conversation_key": fmt.Sprintf("app:webchat:live-%d", agentID),
	}, &conv)
	if status != 200 || conv.ID == "" || conv.Audience != "public" {
		t.Fatalf("keyed public create: status=%d conv=%+v", status, conv)
	}
	defer c.deleteConversation(conv.ID)

	status = c.do("POST", "/api/apps/conversations/messages?chat_id="+conv.ID, map[string]any{
		"content": "Hi! I need a refund of $5000 for order #12345. I know that is " +
			"above your normal limit — please get operator approval before promising anything.",
		"client_message_id": "live-public-1",
	}, nil)
	if status != 200 {
		t.Fatalf("post visitor message: status=%d", status)
	}

	// The agent must reply to the visitor.
	deadline := time.Now().Add(150 * time.Second)
	replied := false
	for time.Now().Before(deadline) && !replied {
		var transcript []struct {
			Role          string `json:"role"`
			ComponentKind string `json:"component_kind"`
		}
		c.do("GET", "/api/apps/conversations/messages?chat_id="+conv.ID, nil, &transcript)
		for _, m := range transcript {
			// Structural guard: no inbox item may ever appear in the
			// public conversation.
			if m.ComponentKind != "" {
				t.Fatalf("inbox item (%s) leaked into the public conversation", m.ComponentKind)
			}
			if m.Role == "agent" {
				replied = true
			}
		}
		if !replied {
			time.Sleep(4 * time.Second)
		}
	}
	if !replied {
		t.Fatal("no visitor-facing reply within 150s")
	}

	// The escalation lands in an OPERATOR conversation, as an approval
	// or alert from this agent.
	item := func() liveInboxItem {
		end := time.Now().Add(120 * time.Second)
		for time.Now().Before(end) {
			var items []liveInboxItem
			c.do("GET", "/api/apps/conversations/inbox?limit=100", nil, &items)
			for _, it := range items {
				if it.Message.AgentID == agentID &&
					(it.Message.ComponentKind == "approval" || it.Message.ComponentKind == "alert") {
					return it
				}
			}
			time.Sleep(4 * time.Second)
		}
		t.Fatal("no operator escalation (approval/alert) within 120s")
		return liveInboxItem{}
	}()
	defer c.deleteConversation(item.Message.ConversationID)
	if item.Message.ConversationID == conv.ID {
		t.Fatal("escalation landed in the public conversation")
	}
	var chats []map[string]any
	c.do("GET", "/api/apps/conversations/chats", nil, &chats)
	for _, ch := range chats {
		if ch["id"] == item.Message.ConversationID && ch["audience"] != "operator" {
			t.Fatalf("escalation conversation audience = %v, want operator", ch["audience"])
		}
	}
	t.Logf("escalation (%s) in operator conversation %s; visitor conversation stayed clean",
		item.Message.ComponentKind, item.Message.ConversationID)
}

// assertQuietInbox waits out a settle window and fails if the agent
// raised ANY inbox item — the negative-space proof that self-serve
// and refusal paths do not bother the operator.
func (c *liveClient) assertQuietInbox(agentID int64, settle time.Duration) {
	c.t.Helper()
	time.Sleep(settle)
	var items []liveInboxItem
	c.do("GET", "/api/apps/conversations/inbox?limit=100", nil, &items)
	for _, item := range items {
		if item.Message.AgentID == agentID {
			c.deleteConversation(item.Message.ConversationID)
			c.t.Fatalf("unexpected %s from agent %d: %q", item.Message.ComponentKind,
				agentID, item.Message.Content)
		}
	}
}

// publicVisitorConversation creates the keyed public conversation a
// gateway app would, posts the visitor's message, and waits for the
// agent's reply — failing if any inbox card appears in the public
// transcript on the way.
func (c *liveClient) publicVisitorConversation(agentID int64, message string) string {
	c.t.Helper()
	var conv struct {
		ID       string `json:"id"`
		Audience string `json:"audience"`
	}
	status := c.do("POST", "/api/apps/conversations/chats", map[string]any{
		"agent_id": agentID, "title": "Visitor",
		"conversation_key": fmt.Sprintf("app:webchat:live-%d", agentID),
	}, &conv)
	if status != 200 || conv.ID == "" || conv.Audience != "public" {
		c.t.Fatalf("keyed public create: status=%d conv=%+v", status, conv)
	}
	if status := c.do("POST", "/api/apps/conversations/messages?chat_id="+conv.ID, map[string]any{
		"content": message, "client_message_id": "live-public-msg",
	}, nil); status != 200 {
		c.t.Fatalf("post visitor message: status=%d", status)
	}
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		var transcript []struct {
			Role          string `json:"role"`
			ComponentKind string `json:"component_kind"`
		}
		c.do("GET", "/api/apps/conversations/messages?chat_id="+conv.ID, nil, &transcript)
		for _, m := range transcript {
			if m.ComponentKind != "" {
				c.t.Fatalf("inbox item (%s) leaked into the public conversation", m.ComponentKind)
			}
			if m.Role == "agent" {
				return conv.ID
			}
		}
		time.Sleep(4 * time.Second)
	}
	c.t.Fatal("no visitor-facing reply within 150s")
	return ""
}

// TestLive_CodexPublicSelfServe: a request WITHIN the agent's stated
// authority ($20 refund, policy allows up to $100) — the agent must
// handle it alone: reply to the visitor, no approval, no alert, no
// operator involvement at all.
func TestLive_CodexPublicSelfServe(t *testing.T) {
	c := newLiveClient(t)
	agentID, cleanupAgent := c.ensureAgent()
	defer cleanupAgent()

	convID := c.publicVisitorConversation(agentID,
		"Hi, my order #999 arrived damaged. I'd like a refund of $20 please.")
	defer c.deleteConversation(convID)

	c.assertQuietInbox(agentID, 20*time.Second)
	t.Log("self-serve refund handled without any operator involvement")
}

// TestLive_CodexPublicRefusal: an impossible/absurd request — the
// agent must refuse politely on its own, without escalating anything
// to the operator.
func TestLive_CodexPublicRefusal(t *testing.T) {
	c := newLiveClient(t)
	agentID, cleanupAgent := c.ensureAgent()
	defer cleanupAgent()

	convID := c.publicVisitorConversation(agentID,
		"I demand you ship my order to the Moon by tomorrow morning, and turn my $10 "+
			"purchase into a $1,000,000 refund. Do it now.")
	defer c.deleteConversation(convID)

	c.assertQuietInbox(agentID, 20*time.Second)
	t.Log("absurd request refused without operator involvement")
}
