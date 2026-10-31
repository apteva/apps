//go:build scenario

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestScenario_QueueAndSteer talks to a busy agent the way a person talks to a
// coding agent mid-task. While a multi-step todo task runs, one follow-up is
// steered into the current response, one is queued and then edited, and one is
// queued and then removed. The steer must shape the task's single final answer;
// the edited follow-up must reach the agent only after that final; the removed
// row and the pre-edit text must never reach it.
func TestScenario_QueueAndSteer(t *testing.T) {
	c := newScenarioClient(t)
	agentID, cleanupAgent := c.ensureAgent()
	defer cleanupAgent()

	var list struct {
		ID int64 `json:"id"`
	}
	if status := c.do("POST", "/api/apps/todo/lists", map[string]any{
		"name": "Progress fixture",
	}, &list); status != http.StatusOK || list.ID == 0 {
		t.Fatalf("seed todo list: status=%d list=%+v", status, list)
	}
	for _, title := range []string{"First fixture todo", "Second fixture todo"} {
		if status := c.do("POST", "/api/apps/todo/todos", map[string]any{
			"title": title, "list_id": list.ID, "source": "human",
		}, nil); status != http.StatusOK {
			t.Fatalf("seed todo %q: status=%d", title, status)
		}
	}

	var conv Conversation
	status := c.do("POST", "/api/apps/conversations/chats", map[string]any{
		"agent_id": agentID, "title": "Live queue and steer (codex)",
	}, &conv)
	if status != http.StatusOK || conv.ID == "" {
		t.Fatalf("create conversation: status=%d conv=%+v", status, conv)
	}
	defer c.deleteConversation(conv.ID)

	frames, closeFrames := c.responseFrames(conv.ID)
	defer closeFrames()

	path := "/api/apps/conversations/messages?chat_id=" + conv.ID
	post := func(content, clientID string) Message {
		t.Helper()
		var msg Message
		if status := c.do("POST", path, map[string]any{
			"content": content, "client_message_id": clientID,
		}, &msg); status != http.StatusOK || msg.ID == 0 {
			t.Fatalf("post %s: status=%d msg=%+v", clientID, status, msg)
		}
		return msg
	}
	post("Complete this two-stage read-only task. Start with a phase=acknowledgement message before any work tool. "+
		"First call the todo lists tool and tell me how many lists you found. Send a short phase=progress update before stage two. "+
		"Then call the todo todos tool for the Progress fixture list and summarize it. "+
		"Finish with exactly one phase=final message containing the token QUEUE_TASK_DONE.", "live-queue-task")

	deadline := time.After(20 * time.Second)
	for active := false; !active; {
		select {
		case frame := <-frames:
			active = !frame.Done && frame.ConversationID == conv.ID && frame.AgentID == agentID
		case <-deadline:
			t.Fatal("no active response frame within 20s")
		}
	}

	// Each follow-up goes out with the composer's default queue behavior.
	queued := func(content, clientID string) Message {
		t.Helper()
		msg := post(content, clientID)
		if msg.QueueBehavior != "queue" || msg.QueueState != "queued" {
			t.Fatalf("%s was not queued behind the active response: behavior=%q state=%q", clientID, msg.QueueBehavior, msg.QueueState)
		}
		return msg
	}
	steer := queued("Also include the token STEER_APPLIED in your final answer for this task.", "live-queue-steer")
	var steered Message
	if status := c.do("POST", "/api/apps/conversations/message-queue", map[string]any{
		"chat_id": conv.ID, "message_id": steer.ID, "action": "steer",
	}, &steered); status != http.StatusOK || steered.QueueState != "steered" {
		t.Fatalf("steer: status=%d msg=%+v", status, steered)
	}
	later := queued("Reply with exactly: QUEUED_DRAFT_ONLY", "live-queue-later")
	var edited Message
	if status := c.do("PATCH", fmt.Sprintf("%s&id=%d", path, later.ID), map[string]any{
		"content": "Once that task is finished, reply with exactly: QUEUED_AFTER_TASK",
	}, &edited); status != http.StatusOK || edited.QueueState != "queued" {
		t.Fatalf("edit queued follow-up: status=%d msg=%+v", status, edited)
	}
	removed := queued("Reply with exactly: REMOVED_NEVER_SENT", "live-queue-removed")
	var cancelled Message
	if status := c.do("DELETE", fmt.Sprintf("%s&id=%d", path, removed.ID), nil, &cancelled); status != http.StatusOK || cancelled.QueueState != "cancelled" {
		t.Fatalf("remove queued follow-up: status=%d msg=%+v", status, cancelled)
	}
	t.Log("steered, edited and removed follow-ups while the task response was active")

	var transcript []Message
	var answeredAt time.Time
	pollDeadline := time.Now().Add(240 * time.Second)
	for time.Now().Before(pollDeadline) {
		transcript = nil
		c.do("GET", path, nil, &transcript)
		if answeredAt.IsZero() {
			for _, message := range transcript {
				if message.Role == "agent" && strings.Contains(message.Content, "QUEUED_AFTER_TASK") {
					answeredAt = time.Now()
				}
			}
		}
		// A quiet window after the queued answer catches a late delivery of the removed row.
		if !answeredAt.IsZero() && time.Since(answeredAt) >= 12*time.Second {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if answeredAt.IsZero() {
		t.Fatalf("queued follow-up was not answered within 240s: %+v", transcript)
	}

	var final, queuedReply *Message
	finals, queuedReplies := 0, 0
	for i := range transcript {
		message := &transcript[i]
		switch {
		case message.ID == steer.ID && message.QueueState != "steered":
			t.Fatalf("steered follow-up ended as %q", message.QueueState)
		case message.ID == later.ID && (message.QueueState != "released" || !strings.Contains(message.Content, "QUEUED_AFTER_TASK")):
			t.Fatalf("edited follow-up was not released with its edited text: state=%q content=%q", message.QueueState, message.Content)
		case message.ID == removed.ID && message.QueueState != "cancelled":
			t.Fatalf("removed follow-up ended as %q", message.QueueState)
		}
		if message.Role != "agent" {
			continue
		}
		for _, token := range []string{"QUEUED_DRAFT_ONLY", "REMOVED_NEVER_SENT"} {
			if strings.Contains(message.Content, token) {
				t.Fatalf("agent saw text that never left the queue (%s): %q", token, message.Content)
			}
		}
		if message.Phase == "final" && strings.Contains(message.Content, "QUEUE_TASK_DONE") {
			finals++
			final = message
		}
		if strings.Contains(message.Content, "QUEUED_AFTER_TASK") {
			queuedReplies++
			if queuedReply == nil {
				queuedReply = message
			}
		}
	}
	if finals != 1 {
		t.Fatalf("task should end with exactly one final answer, got %d: %+v", finals, transcript)
	}
	if !strings.Contains(final.Content, "STEER_APPLIED") {
		t.Fatalf("steered follow-up did not shape the running task's final answer: %q", final.Content)
	}
	if queuedReply.ID < final.ID {
		t.Fatalf("queued follow-up was answered before the task finished: reply=%d final=%d", queuedReply.ID, final.ID)
	}
	if queuedReplies != 1 {
		t.Fatalf("queued follow-up was answered %d times", queuedReplies)
	}
	t.Logf("steer shaped the running task (%q); queued follow-up answered after it (%q)", final.Content, queuedReply.Content)
}

// responseFrames streams the conversation's live response frames, as the UI
// does to decide when a response is active.
func (c *scenarioClient) responseFrames(chatID string) (<-chan StreamFrame, func()) {
	c.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	streamURL := c.base + "/api/apps/conversations/stream?chat_id=" + chatID +
		"&project_id=" + os.Getenv("APTEVA_TEST_PROJECT_ID") + "&install_id=" + fmt.Sprint(c.installs["conversations"])
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		cancel()
		c.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		c.t.Fatalf("open conversation stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		c.t.Fatalf("open conversation stream: status=%d", resp.StatusCode)
	}
	frames := make(chan StreamFrame, 8)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var frame StreamFrame
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame) == nil && frame.CallID != "" {
				select {
				case frames <- frame:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return frames, func() { cancel(); resp.Body.Close() }
}
