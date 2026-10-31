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
// coding agent mid-task. The agent works through a five-item todo list while the
// user steers it three times at different points (add work, leave one item for
// me, shape the summary) and lines up two follow-ups behind it, editing one and
// removing a destructive third. Every steer must land in the running task; the
// follow-ups must run afterwards, in order; the removed one must never run. The
// run passes only when the Todo data and the transcript both show all work done.
func TestScenario_QueueAndSteer(t *testing.T) {
	c := newScenarioClient(t)
	agentID, cleanupAgent := c.ensureAgent()
	defer cleanupAgent()

	seeds := []string{"Book the venue", "Confirm the caterer", "Print badges", "Order banners", "Send invites"}
	launchID := c.seedTodoList("Launch prep", seeds...)

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
	history := func() []Message {
		t.Helper()
		var rows []Message
		if status := c.do("GET", path, nil, &rows); status != http.StatusOK {
			t.Fatalf("history: status=%d", status)
		}
		return rows
	}
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
	post("Work through my Launch prep todo list. Start with a phase=acknowledgement message before any work tool. "+
		"Complete the open todos one at a time, and after each one send a short phase=progress update naming it. "+
		"When everything is handled, send exactly one phase=final summary containing the token LAUNCH_DONE.", "live-queue-task")

	deadline := time.After(20 * time.Second)
	for active := false; !active; {
		select {
		case frame := <-frames:
			active = !frame.Done && frame.ConversationID == conv.ID && frame.AgentID == agentID
		case <-deadline:
			t.Fatal("no active response frame within 20s")
		}
	}

	// Each message goes out with the composer's default queue behavior.
	queued := func(content, clientID string) Message {
		t.Helper()
		msg := post(content, clientID)
		if msg.QueueBehavior != "queue" || msg.QueueState != "queued" {
			t.Fatalf("%s was not queued behind the running task (it may already have finished): behavior=%q state=%q", clientID, msg.QueueBehavior, msg.QueueState)
		}
		return msg
	}
	steerNow := func(content, clientID string) Message {
		t.Helper()
		msg := queued(content, clientID)
		var steered Message
		if status := c.do("POST", "/api/apps/conversations/message-queue", map[string]any{
			"chat_id": conv.ID, "message_id": msg.ID, "action": "steer",
		}, &steered); status != http.StatusOK || steered.QueueState != "steered" {
			t.Fatalf("steer %s: status=%d msg=%+v", clientID, status, steered)
		}
		return steered
	}
	// Later steers wait for real progress in the Todo data rather than for chat
	// messages, so they arrive mid-task even when the agent skips an update.
	waitForDone := func(n int) {
		t.Helper()
		deadline := time.Now().Add(150 * time.Second)
		for len(c.todoTitles(launchID, "done")) < n {
			if time.Now().After(deadline) {
				t.Fatalf("fewer than %d Launch prep todos were completed within 150s", n)
			}
			time.Sleep(time.Second)
		}
	}

	steers := []Message{steerNow(`Also add a todo called "Notify the team" to Launch prep and complete it as part of this task.`, "live-steer-1")}
	retro := queued("Next, create a list called Draft.", "live-queue-retro")
	removed := queued("Delete the Launch prep list.", "live-queue-removed")
	var cancelled Message
	if status := c.do("DELETE", fmt.Sprintf("%s&id=%d", path, removed.ID), nil, &cancelled); status != http.StatusOK || cancelled.QueueState != "cancelled" {
		t.Fatalf("remove queued follow-up: status=%d msg=%+v", status, cancelled)
	}
	count := queued("Then reply with OPEN_COUNT=<number>, where the number is how many open todos remain across all my lists.", "live-queue-count")

	waitForDone(1)
	// Hand back the last seeded todo that is still open, so the steer changes work not yet done.
	skip := ""
	for _, title := range seeds {
		if containsFold(c.todoTitles(launchID, "all"), title) {
			skip = title
		}
	}
	if skip == "" {
		t.Fatal("every seeded todo was completed before the second steer could be sent")
	}
	steers = append(steers, steerNow(fmt.Sprintf("Leave %q open, I'll do that one myself. If you already completed it, reopen it.", skip), "live-steer-2"))
	retroText := `Next, create a list called "Retro" with one todo "Collect feedback", then reply with RETRO_READY.`
	var edited Message
	if status := c.do("PATCH", fmt.Sprintf("%s&id=%d", path, retro.ID), map[string]any{
		"content": retroText,
	}, &edited); status != http.StatusOK || edited.QueueState != "queued" {
		t.Fatalf("edit queued follow-up: status=%d msg=%+v", status, edited)
	}

	waitForDone(2)
	steers = append(steers, steerNow("In your final summary, list every todo you completed and include the token STEERS_APPLIED.", "live-steer-3"))
	t.Logf("three steers sent mid-task (leave %q open); two follow-ups queued, one edited, one removed", skip)

	var rows []Message
	var answeredAt time.Time
	answerDeadline := time.Now().Add(300 * time.Second)
	for time.Now().Before(answerDeadline) {
		rows = history()
		if _, n := agentMessage(rows, "OPEN_COUNT="); answeredAt.IsZero() && n > 0 {
			answeredAt = time.Now()
		}
		// A quiet window after the last follow-up catches late or duplicate work.
		if !answeredAt.IsZero() && time.Since(answeredAt) >= 15*time.Second {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if answeredAt.IsZero() {
		t.Fatalf("the last queued follow-up was not answered within 300s: %+v", rows)
	}

	for _, message := range rows {
		switch message.ID {
		case steers[0].ID, steers[1].ID, steers[2].ID:
			if message.QueueState != "steered" {
				t.Fatalf("steer %d ended as %q", message.ID, message.QueueState)
			}
		case retro.ID, count.ID:
			// Released rows read back like ordinary messages, with no queue state.
			if message.QueueState != "" {
				t.Fatalf("follow-up %d ended as %q", message.ID, message.QueueState)
			}
			if message.ID == retro.ID && message.Content != retroText {
				t.Fatalf("follow-up was released without its edit: %q", message.Content)
			}
		case removed.ID:
			if message.QueueState != "cancelled" {
				t.Fatalf("removed follow-up ended as %q", message.QueueState)
			}
		}
	}
	task, tasks := agentMessage(rows, "LAUNCH_DONE")
	if tasks != 1 || task.Phase != "final" {
		t.Fatalf("the task should end with exactly one final summary, got %d: %+v", tasks, rows)
	}
	if !strings.Contains(task.Content, "STEERS_APPLIED") {
		t.Fatalf("the last steer did not reach the running task: %q", task.Content)
	}
	retroReply, retroReplies := agentMessage(rows, "RETRO_READY")
	countReply, countReplies := agentMessage(rows, "OPEN_COUNT=")
	if retroReplies != 1 || countReplies != 1 {
		t.Fatalf("each follow-up should be answered once: retro=%d count=%d", retroReplies, countReplies)
	}
	if retroReply.ID < task.ID {
		t.Fatalf("the first follow-up was answered before the task finished: task=%d retro=%d", task.ID, retroReply.ID)
	}
	// Queued messages are released one per finished response, so each follow-up gets its own turn.
	if countReply.ID <= retroReply.ID {
		t.Fatalf("the second follow-up was not answered after the first, in its own turn: retro=%d count=%d %q", retroReply.ID, countReply.ID, countReply.Content)
	}
	for _, message := range rows {
		if message.Role == "agent" && message.ID > countReply.ID {
			t.Fatalf("the agent kept going after the last follow-up: %q", message.Content)
		}
	}

	// The Todo data must show every requested change, and nothing the user removed or rewrote.
	lists := c.todoListIDs()
	if lists["launch prep"] == 0 {
		t.Fatal("the removed follow-up ran: Launch prep was deleted")
	}
	if lists["draft"] != 0 {
		t.Fatal("the pre-edit follow-up ran: a Draft list exists")
	}
	if lists["retro"] == 0 {
		t.Fatalf("the edited follow-up did not create the Retro list: %v", lists)
	}
	open := c.todoTitles(launchID, "all")
	if len(open) != 1 || !containsFold(open, skip) {
		t.Fatalf("Launch prep should leave only %q open, got %v", skip, open)
	}
	done := c.todoTitles(launchID, "done")
	for _, title := range append(append([]string{}, seeds...), "Notify the team") {
		if title != skip && !containsFold(done, title) {
			t.Fatalf("%q was not completed: done=%v", title, done)
		}
	}
	retroOpen := c.todoTitles(lists["retro"], "all")
	if len(retroOpen) != 1 || !containsFold(retroOpen, "Collect feedback") {
		t.Fatalf("Retro should hold one open todo, Collect feedback, got %v", retroOpen)
	}
	if want := fmt.Sprintf("OPEN_COUNT=%d", len(c.todoTitles(0, "all"))); !strings.Contains(countReply.Content, want) {
		t.Fatalf("open count reply %q, want %s", countReply.Content, want)
	}
	t.Logf("all done: %d todos completed, %q left open, Retro ready, %q", len(done), skip, countReply.Content)
}

// agentMessage returns the first agent message containing token and how many contain it.
func agentMessage(rows []Message, token string) (Message, int) {
	var first Message
	n := 0
	for _, message := range rows {
		if message.Role == "agent" && strings.Contains(message.Content, token) {
			if n == 0 {
				first = message
			}
			n++
		}
	}
	return first, n
}

func containsFold(items []string, want string) bool {
	for _, item := range items {
		if strings.Contains(strings.ToLower(item), strings.ToLower(want)) {
			return true
		}
	}
	return false
}

func (c *scenarioClient) seedTodoList(name string, titles ...string) int64 {
	c.t.Helper()
	var list struct {
		ID int64 `json:"id"`
	}
	if status := c.do("POST", "/api/apps/todo/lists", map[string]any{"name": name}, &list); status != http.StatusOK || list.ID == 0 {
		c.t.Fatalf("seed todo list %q: status=%d list=%+v", name, status, list)
	}
	for _, title := range titles {
		if status := c.do("POST", "/api/apps/todo/todos", map[string]any{
			"title": title, "list_id": list.ID, "source": "human",
		}, nil); status != http.StatusOK {
			c.t.Fatalf("seed todo %q: status=%d", title, status)
		}
	}
	return list.ID
}

// todoTitles reads one Todo view: "all" is every open todo, "done" every
// completed one. A zero listID spans every list.
func (c *scenarioClient) todoTitles(listID int64, view string) []string {
	c.t.Helper()
	path := "/api/apps/todo/todos?view=" + view
	if listID != 0 {
		path += fmt.Sprintf("&list_id=%d", listID)
	}
	var todos []struct {
		Title string `json:"title"`
	}
	if status := c.do("GET", path, nil, &todos); status != http.StatusOK {
		c.t.Fatalf("read %s todos: status=%d", view, status)
	}
	titles := make([]string, len(todos))
	for i, todo := range todos {
		titles[i] = todo.Title
	}
	return titles
}

// todoListIDs maps lower-cased list names to their IDs.
func (c *scenarioClient) todoListIDs() map[string]int64 {
	c.t.Helper()
	var lists []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if status := c.do("GET", "/api/apps/todo/lists", nil, &lists); status != http.StatusOK {
		c.t.Fatalf("read todo lists: status=%d", status)
	}
	ids := make(map[string]int64, len(lists))
	for _, list := range lists {
		ids[strings.ToLower(strings.TrimSpace(list.Name))] = list.ID
	}
	return ids
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
