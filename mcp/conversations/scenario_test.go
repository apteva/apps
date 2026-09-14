//go:build scenario

package main

// Tier 3 client workflows executed ONLY by apteva test. The runner provisions
// the server, peer apps/bindings, agents and provider, captures telemetry, applies
// budgets and removes its resources. These drivers exercise HTTP/SSE interactions
// and durable outcomes that cannot be expressed as a single prompt/assert pair.
// See scenarios/*.yaml and TESTING.md. Do not run this build tag directly.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type scenarioClient struct {
	t         *testing.T
	agents    []int64
	nextAgent int
	installs  map[string]int64
	base      string
	key       string
}

func newScenarioClient(t *testing.T) *scenarioClient {
	t.Helper()
	c := &scenarioClient{t: t, base: os.Getenv("APTEVA_TEST_SERVER_URL"), key: os.Getenv("APTEVA_TEST_SERVER_API_KEY")}
	if c.base == "" || c.key == "" || os.Getenv("APTEVA_TEST_PROJECT_ID") == "" {
		t.Fatal("run this workflow through apteva test; runner context is required")
	}
	if err := json.Unmarshal([]byte(os.Getenv("APTEVA_TEST_AGENT_IDS")), &c.agents); err != nil || len(c.agents) == 0 {
		t.Fatal("runner did not provision agents")
	}
	if err := json.Unmarshal([]byte(os.Getenv("APTEVA_TEST_INSTALLS")), &c.installs); err != nil || c.installs["conversations"] == 0 {
		t.Fatal("runner did not provision Conversations")
	}
	return c
}

func (c *scenarioClient) do(method, path string, body any, out any) int {
	c.t.Helper()
	if strings.HasPrefix(path, "/api/apps/") {
		slug := strings.Split(strings.TrimPrefix(path, "/api/apps/"), "/")[0]
		id := c.installs[slug]
		if id == 0 {
			c.t.Fatalf("runner has no install for %s", slug)
		}
		separator := "?"
		if strings.Contains(path, "?") {
			separator = "&"
		}
		path += separator + "project_id=" + url.QueryEscape(os.Getenv("APTEVA_TEST_PROJECT_ID")) + "&install_id=" + fmt.Sprint(id)
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
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		c.t.Logf("%s %s returned %d: %s", method, path, resp.StatusCode, strings.ReplaceAll(string(data), c.key, "[redacted]"))
		return resp.StatusCode
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			c.t.Fatalf("decode %s %s response: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

// Allocate the next runner-owned agent. Drivers never create or delete agents.
func (c *scenarioClient) ensureAgent() (int64, func()) {
	c.t.Helper()
	if c.nextAgent >= len(c.agents) {
		c.t.Fatal("scenario must declare enough setup.agents")
	}
	id := c.agents[c.nextAgent]
	c.nextAgent++
	c.t.Logf("using runner-owned agent %d", id)
	return id, func() {}
}

// TestScenario_ChatRoundTrip: create a conversation with the agent,
// send one message, and require a real model-authored reply through
// conversations_send within the deadline.
func TestScenario_ChatRoundTrip(t *testing.T) {
	c := newScenarioClient(t)
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

// TestScenario_SingleConversationRoundTrip proves the focused widget's
// server-selected conversation is the same durable chat used for a real Codex
// turn. It covers the lead-agent projection without introducing a second chat
// transport or transcript implementation.
func TestScenario_SingleConversationRoundTrip(t *testing.T) {
	c := newScenarioClient(t)
	agentID, cleanupAgent := c.ensureAgent()
	defer cleanupAgent()

	var conv Conversation
	status := c.do("POST", "/api/apps/conversations/chats", map[string]any{
		"agent_id": agentID, "title": "Live single conversation (codex)",
	}, &conv)
	if status != http.StatusOK || conv.ID == "" {
		t.Fatalf("create conversation: status=%d conv=%+v", status, conv)
	}
	defer c.do("DELETE", "/api/apps/conversations/chats?id="+conv.ID, nil, nil)

	var latest []Conversation
	status = c.do("GET", fmt.Sprintf("/api/apps/conversations/chats?lead_agent_id=%d&limit=1", agentID), nil, &latest)
	if status != http.StatusOK || len(latest) != 1 || latest[0].ID != conv.ID {
		t.Fatalf("focused lookup: status=%d latest=%+v want=%s", status, latest, conv.ID)
	}

	status = c.do("POST", "/api/apps/conversations/messages?chat_id="+conv.ID, map[string]any{
		"content":           "Reply with exactly: SINGLE_MODE_PONG",
		"client_message_id": "live-single-mode-1",
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("post focused message: status=%d", status)
	}

	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		var transcript []Message
		c.do("GET", "/api/apps/conversations/messages?chat_id="+conv.ID, nil, &transcript)
		for _, message := range transcript {
			if message.Role == "agent" && strings.Contains(message.Content, "SINGLE_MODE_PONG") {
				t.Log("focused conversation lookup and real Codex reply succeeded")
				return
			}
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatal("no Codex reply in the focused conversation within 120s")
}

// TestScenario_SoftBreak opens the real streaming channel, waits for the
// acknowledgement that makes the UI's Break control visible, then submits a
// durable advisory event against that exact active response. The model may
// finish an already-running provider call; it must nevertheless consume the
// later event and acknowledge the user's changed intent on its next turn.
func TestScenario_SoftBreak(t *testing.T) {
	c := newScenarioClient(t)
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
		"&project_id=" + os.Getenv("APTEVA_TEST_PROJECT_ID") + "&install_id=" + fmt.Sprint(c.installs["conversations"])
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

// The title and expected image colour change each run. Only actual vision can
// supply the colour; neither the filename nor the prompt reveals it.
func TestScenario_ImageStorageTicket(t *testing.T) {
	c := newScenarioClient(t)
	if c.installs["storage"] == 0 || c.installs["tickets"] == 0 {
		t.Fatal("scenario must provision Storage and Tickets")
	}
	agent, _ := c.ensureAgent()
	var conv Conversation
	if status := c.do("POST", "/api/apps/conversations/chats", map[string]any{"agent_id": agent, "title": "Image Storage ticket"}, &conv); status != 200 || conv.ID == "" {
		t.Fatalf("create: %d", status)
	}
	defer c.deleteConversation(conv.ID)
	colours := []struct {
		name  string
		value color.RGBA
	}{{"red", color.RGBA{255, 0, 0, 255}}, {"blue", color.RGBA{0, 0, 255, 255}}, {"green", color.RGBA{0, 180, 0, 255}}}
	selected := colours[time.Now().UnixNano()%int64(len(colours))]
	canvas := image.NewRGBA(image.Rect(0, 0, 128, 128))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: selected.value}, image.Point{}, draw.Src)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatal(err)
	}
	title := fmt.Sprintf("TIER3_IMAGE_STORAGE_%d", time.Now().UnixNano())
	var uploaded Attachment
	if status := c.do("POST", "/api/apps/conversations/attachments?chat_id="+conv.ID, map[string]any{"id": title, "name": "picture.png", "content_base64": base64.StdEncoding.EncodeToString(encoded.Bytes())}, &uploaded); status != 200 || uploaded.ID == "" {
		t.Fatalf("upload: %d", status)
	}
	body := map[string]any{"content": "Create exactly one Tickets ticket titled " + title + ". In its description name the dominant colour you see in the attached picture. Attach this exact original image using the Storage file ID supplied with it. Do not fetch or re-upload the image. Reply TIER3_IMAGE_STORAGE_DONE only after tickets_add_attachment succeeds.", "client_message_id": title, "attachments": []Attachment{{ID: uploaded.ID, Type: uploaded.Type}}}
	var original, retry Message
	if status := c.do("POST", "/api/apps/conversations/messages?chat_id="+conv.ID, body, &original); status != 200 {
		t.Fatalf("send image: %d", status)
	}
	if len(original.Attachments) != 1 || original.Attachments[0].FileID <= 0 {
		t.Fatalf("sent message lacks stable Storage ID: %+v", original.Attachments)
	}
	expected := original.Attachments[0].FileID
	t.Logf("fixture colour=%s; Storage file=%d; original PNG sha256=%x", selected.name, expected, sha256.Sum256(encoded.Bytes()))
	if status := c.do("POST", "/api/apps/conversations/messages?chat_id="+conv.ID, body, &retry); status != 200 || retry.ID != original.ID || len(retry.Attachments) != 1 || retry.Attachments[0].FileID != expected {
		t.Fatalf("retry duplicated message or Storage file: status=%d", status)
	}
	deadline := time.Now().Add(300 * time.Second)
	for time.Now().Before(deadline) {
		var transcript []Message
		if status := c.do("GET", "/api/apps/conversations/messages?chat_id="+conv.ID, nil, &transcript); status != 200 {
			t.Fatalf("history: %d", status)
		}
		done := false
		for _, m := range transcript {
			if m.Role == "agent" && strings.Contains(m.Content, "TIER3_IMAGE_STORAGE_DONE") {
				done = true
			}
		}
		if !done {
			time.Sleep(time.Second)
			continue
		}
		var list struct {
			Tickets []struct {
				ID    int64  `json:"id"`
				Title string `json:"title"`
			} `json:"tickets"`
		}
		if status := c.do("GET", "/api/apps/tickets/tickets?q="+url.QueryEscape(title), nil, &list); status != 200 || len(list.Tickets) != 1 || list.Tickets[0].Title != title {
			t.Fatalf("expected one uniquely titled ticket: status=%d count=%d", status, len(list.Tickets))
		}
		var detail struct {
			Ticket struct {
				Description string `json:"description"`
			} `json:"ticket"`
			Attachments []struct {
				StorageFileID string `json:"storage_file_id"`
			} `json:"attachments"`
		}
		if status := c.do("GET", fmt.Sprintf("/api/apps/tickets/tickets/%d", list.Tickets[0].ID), nil, &detail); status != 200 {
			t.Fatalf("ticket detail: %d", status)
		}
		if len(detail.Attachments) != 1 || detail.Attachments[0].StorageFileID != fmt.Sprint(expected) {
			t.Fatalf("ticket attachment differs from original Storage file %d: %+v", expected, detail.Attachments)
		}
		found := false
		for _, event := range c.ownershipToolEvents(agent) {
			if event.Data.Name == "tickets_add_attachment" || strings.HasSuffix(event.Data.Name, "_tickets_add_attachment") {
				var fileID any
				if err := json.Unmarshal(event.Data.Args["file_id"], &fileID); err != nil {
					t.Fatal(err)
				}
				if fmt.Sprint(fileID) != fmt.Sprint(expected) {
					t.Fatalf("agent used wrong file ID: %v", fileID)
				}
				if event.ThreadID != conversationThreadID(conv.ID) {
					t.Fatalf("attachment tool escaped originating chat: %s", event.ThreadID)
				}
				found = true
			}
		}
		if !found {
			time.Sleep(time.Second)
			continue // persisted telemetry can arrive after the final chat message
		}
		req, err := http.NewRequest("GET", fmt.Sprintf("%s/api/apps/storage/files/%d/download?project_id=%s&install_id=%d", c.base, expected, url.QueryEscape(os.Getenv("APTEVA_TEST_PROJECT_ID")), c.installs["storage"]), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+c.key)
		response, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		originalBytes, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || !bytes.Equal(originalBytes, encoded.Bytes()) {
			t.Fatalf("Storage does not contain the original image bytes: status=%d err=%v", response.StatusCode, err)
		}
		t.Logf("Storage and Tickets reference the same original bytes and file ID %d; tool stayed in the originating thread", expected)
		if !strings.Contains(strings.ToLower(detail.Ticket.Description), selected.name) {
			t.Fatalf("vision failed: expected %s in ticket description %q", selected.name, detail.Ticket.Description)
		}
		t.Logf("vision=%s; ticket=%d; message and ticket share Storage file %d; retry reused original message; tool ran in originating thread", selected.name, list.Tickets[0].ID, expected)
		return
	}
	t.Fatal("real agent did not complete image-to-ticket flow within 300s")
}

// TestScenario_TwoConversationIsolation proves that one agent can hold two
// simultaneous Conversations threads without replies crossing between them.
func TestScenario_TwoConversationIsolation(t *testing.T) {
	c := newScenarioClient(t)
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

func (c *scenarioClient) injectEvent(agentID int64, text string) {
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
func (c *scenarioClient) pollInbox(agentID int64, kind string, deadline time.Duration) liveInboxItem {
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

func (c *scenarioClient) deleteConversation(id string) {
	if id != "" {
		c.do("DELETE", "/api/apps/conversations/chats?id="+id, nil, nil)
	}
}

// TestScenario_AlertFlow: a monitor error at main must become an
// error/warn alert in an agent-created conversation — the agent picks
// or creates the conversation itself (list → create → alert).
func TestScenario_AlertFlow(t *testing.T) {
	c := newScenarioClient(t)
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

// TestScenario_ApprovalRoundTrip: the agent requests approval for a
// destructive action and STOPS; the operator approves with a note;
// the card mutates in place and the verdict reaches the agent, which
// acknowledges in the conversation.
func TestScenario_ApprovalRoundTrip(t *testing.T) {
	c := newScenarioClient(t)
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
		var deliveryState []map[string]any
		c.do("GET", "/api/apps/conversations/deliveries?chat_id="+item.Message.ConversationID, nil, &deliveryState)
		t.Logf("approval delivery diagnostics: %+v", deliveryState)
		t.Fatal("no agent acknowledgment after the verdict within 120s")
	}
	t.Log("approval card mutated and the agent acknowledged the verdict")
}

// TestScenario_ReportFlow: a scheduler event produces a report that
// is pending in the inbox AND visible in its conversation's
// transcript (the 0.5.1 guarantee).
func TestScenario_ReportFlow(t *testing.T) {
	c := newScenarioClient(t)
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

// TestScenario_PublicVisitorEscalation — the public-audience story
// end-to-end with a real model: a visitor (public keyed conversation,
// as a gateway app would create it) asks for something above the
// bot's authority. The agent must reply to the visitor in the public
// conversation, must NOT put any inbox item there (the structural
// guard), and should escalate by raising an approval or alert in an
// OPERATOR conversation.
func TestScenario_PublicVisitorEscalation(t *testing.T) {
	c := newScenarioClient(t)
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
func (c *scenarioClient) assertQuietInbox(agentID int64, settle time.Duration) {
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
func (c *scenarioClient) publicVisitorConversation(agentID int64, message string) string {
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

// TestScenario_PublicSelfServe: a request WITHIN the agent's stated
// authority ($20 refund, policy allows up to $100) — the agent must
// handle it alone: reply to the visitor, no approval, no alert, no
// operator involvement at all.
func TestScenario_PublicSelfServe(t *testing.T) {
	c := newScenarioClient(t)
	agentID, cleanupAgent := c.ensureAgent()
	defer cleanupAgent()

	convID := c.publicVisitorConversation(agentID,
		"Hi, my order #999 arrived damaged. I'd like a refund of $20 please.")
	defer c.deleteConversation(convID)

	c.assertQuietInbox(agentID, 20*time.Second)
	t.Log("self-serve refund handled without any operator involvement")
}

// TestScenario_PublicRefusal: an impossible/absurd request — the
// agent must refuse politely on its own, without escalating anything
// to the operator.
func TestScenario_PublicRefusal(t *testing.T) {
	c := newScenarioClient(t)
	agentID, cleanupAgent := c.ensureAgent()
	defer cleanupAgent()

	convID := c.publicVisitorConversation(agentID,
		"I demand you ship my order to the Moon by tomorrow morning, and turn my $10 "+
			"purchase into a $1,000,000 refund. Do it now.")
	defer c.deleteConversation(convID)

	c.assertQuietInbox(agentID, 20*time.Second)
	t.Log("absurd request refused without operator involvement")
}

// Real multi-agent fan-out plus HTTP retry must keep one durable user request.
func TestScenario_RoomFanoutAndRetry(t *testing.T) {
	c := newScenarioClient(t)
	first, cleanFirst := c.ensureAgent()
	defer cleanFirst()
	second, cleanSecond := c.ensureAgent()
	defer cleanSecond()
	if first == second {
		t.Fatal("room scenario requires two distinct runner-owned agents")
	}
	var conv struct {
		ID string `json:"id"`
	}
	if status := c.do("POST", "/api/apps/conversations/chats", map[string]any{"agent_ids": []int64{first, second}, "lead_agent_id": first, "title": "Live room fan-out"}, &conv); status != 200 || conv.ID == "" {
		t.Fatalf("create room: %d", status)
	}
	defer c.deleteConversation(conv.ID)
	payload := map[string]any{"content": "Reply once with exactly: room-ready. Do not delegate or ask the other participant to reply.", "target_agent_ids": []int64{first, second}, "client_message_id": "live-room-retry"}
	var original, retry struct {
		ID int64 `json:"id"`
	}
	if status := c.do("POST", "/api/apps/conversations/messages?chat_id="+conv.ID, payload, &original); status != 200 {
		t.Fatalf("send %d", status)
	}
	if status := c.do("POST", "/api/apps/conversations/messages?chat_id="+conv.ID, payload, &retry); status != 200 || retry.ID != original.ID {
		t.Fatalf("retry status=%d original=%d retry=%d", status, original.ID, retry.ID)
	}
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		var rows []Message
		c.do("GET", "/api/apps/conversations/messages?chat_id="+conv.ID, nil, &rows)
		users := 0
		replies := map[int64]bool{}
		for _, m := range rows {
			if m.Role == "user" {
				users++
			}
			if m.Role == "agent" && strings.Contains(strings.ToLower(m.Content), "room-ready") {
				replies[m.AgentID] = true
			}
		}
		if users != 1 {
			t.Fatalf("retry created %d user rows", users)
		}
		if replies[first] && replies[second] {
			t.Log("both real Codex agents replied; duplicate HTTP submission reused one message")
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("both Codex room participants did not reply within 120s")
}

func TestScenario_ConversationApprovalDestination(t *testing.T) {
	c := newScenarioClient(t)
	agent, cleanup := c.ensureAgent()
	defer cleanup()
	var conv struct {
		ID string `json:"id"`
	}
	if status := c.do("POST", "/api/apps/conversations/chats", map[string]any{"agent_id": agent, "title": "Live bound approval"}, &conv); status != 200 || conv.ID == "" {
		t.Fatalf("create %d", status)
	}
	defer c.deleteConversation(conv.ID)
	c.do("POST", "/api/apps/conversations/messages?chat_id="+conv.ID, map[string]any{"content": "This is a harmless approval-routing test. Use conversations_request_approval in this exact conversation to ask the operator to approve a simulated maintenance operation. Do not perform any real operation. Wait for the verdict, then reply in this conversation with exactly: bound-verdict-received.", "client_message_id": "live-bound-approval"}, nil)
	item := c.pollInbox(agent, "approval", 120*time.Second)
	if item.Message.ConversationID != conv.ID {
		t.Fatalf("approval escaped originating conversation: %s", item.Message.ConversationID)
	}
	if status := c.do("POST", "/api/apps/conversations/message-action", map[string]any{"message_id": item.Message.ID, "action_id": approvalActionID(item), "note": "Approved for simulation only."}, nil); status != 200 {
		t.Fatalf("resolve %d", status)
	}
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		var rows []Message
		c.do("GET", "/api/apps/conversations/messages?chat_id="+conv.ID, nil, &rows)
		for _, m := range rows {
			if m.ID > item.Message.ID && m.AgentID == agent && m.Role == "agent" && strings.Contains(m.Content, "bound-verdict-received") {
				t.Log("real Codex consumed the verdict in its bound originating conversation thread")
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("no bound-thread verdict acknowledgement within 120s")
}
