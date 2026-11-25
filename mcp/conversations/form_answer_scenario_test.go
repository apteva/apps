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
	"sync"
	"testing"
	"time"
)

// TestScenario_FormAnswerShowsLiveWork mirrors the prod Management chat of
// 2026-11-25: main asked with a blocking form, the operator answered, and main
// archived 16 items. The thread showed no Thinking for 26 seconds and only one
// of the 16 tool calls, because an answered form opened no response.
func TestScenario_FormAnswerShowsLiveWork(t *testing.T) {
	c := newScenarioClient(t)
	agentID, cleanupAgent := c.ensureAgent()
	defer cleanupAgent()
	// App telemetry ownership refreshes every 60s; main's events must reach
	// Conversations before the answer, or there is nothing to attribute.
	t.Log("waiting for the new agent to join app telemetry ownership")
	time.Sleep(65 * time.Second)

	todos := []string{"Old invoice reminder", "Expired supplier quote", "Duplicate client note", "Stale meeting follow-up"}
	var list struct {
		ID int64 `json:"id"`
	}
	if status := c.do("POST", "/api/apps/todo/lists", map[string]any{"name": "Cleanup"}, &list); status != http.StatusOK || list.ID == 0 {
		t.Fatalf("seed list: status=%d", status)
	}
	for _, title := range todos {
		if status := c.do("POST", "/api/apps/todo/todos", map[string]any{"title": title, "list_id": list.ID, "source": "human"}, nil); status != http.StatusOK {
			t.Fatalf("seed todo %q: status=%d", title, status)
		}
	}

	var conv struct {
		ID string `json:"id"`
	}
	if status := c.do("POST", "/api/apps/conversations/chats", map[string]any{"agent_id": agentID, "title": "Management"}, &conv); status != http.StatusOK || conv.ID == "" {
		t.Fatalf("create conversation: status=%d", status)
	}
	defer c.do("DELETE", "/api/apps/conversations/chats?id="+conv.ID, nil, nil)
	path := "/api/apps/conversations/messages?chat_id=" + conv.ID
	content := `I want to clear one of my todo lists. Before changing anything, ask me which list with exactly one blocking form: conversations_send phase=final, text="Which list should I clear?", components=[{"id":"which-list","version":1,"app":"conversations","name":"form-card","props":{"title":"List to clear"},"interaction":{"blocking":true,"inbox":true,"fields":[{"id":"list","label":"List name","type":"text","required":true}]}}]. Then pace until component.result arrives. When it does, mark every todo in that list as done yourself, one todo_todos_complete call per todo, and finish with a phase=final reply containing FORM_WORK_DONE.`
	if status := c.do("POST", path, map[string]any{"content": content, "client_message_id": "form-answer-live-work-1"}, nil); status != http.StatusOK {
		t.Fatalf("post message: status=%d", status)
	}
	var participants struct {
		Agents []AgentModelInfo `json:"agents"`
	}
	c.do("GET", "/api/apps/conversations/participants?id="+conv.ID, nil, &participants)
	if len(participants.Agents) != 1 || participants.Agents[0].ThreadID != mainThreadID {
		t.Fatalf("first operator chat should be answered by main: %+v", participants.Agents)
	}
	rows := func() []Message {
		var out []Message
		c.do("GET", path, nil, &out)
		return out
	}

	// 1. The blocking form, then a quiet conversation while main paces.
	var form Message
	for deadline := time.Now().Add(180 * time.Second); time.Now().Before(deadline) && form.ID == 0; {
		for _, m := range rows() {
			for _, cmp := range m.Components {
				if cmp.ID == "which-list" && cmp.Interaction != nil && cmp.Interaction.Blocking {
					form = m
				}
			}
		}
		if form.ID == 0 {
			time.Sleep(2 * time.Second)
		}
	}
	if form.ID == 0 {
		t.Fatal("no blocking form within 180s")
	}
	c.waitIdle(conv.ID, 60*time.Second)

	// 2. Watch the live stream the way the UI does, then answer.
	frames := c.liveFrames(conv.ID)
	defer frames.stop()
	answeredAt := time.Now()
	if status := c.do("POST", "/api/apps/conversations/message-action", map[string]any{
		"message_id": form.ID, "component_id": "which-list", "action_id": "submit", "values": map[string]any{"list": "Cleanup"},
	}, nil); status != http.StatusOK {
		t.Fatalf("answer form: status=%d", status)
	}

	// 3. Main does the work; wait for its final reply.
	var final *Message
	for deadline := time.Now().Add(240 * time.Second); time.Now().Before(deadline) && final == nil; {
		transcript := rows()
		for i := range transcript {
			if transcript[i].Role == "agent" && transcript[i].Phase == "final" && transcript[i].ID > form.ID && strings.Contains(transcript[i].Content, "FORM_WORK_DONE") {
				final = &transcript[i]
			}
		}
		if final == nil {
			time.Sleep(2 * time.Second)
		}
	}
	if final == nil {
		t.Fatal("no final reply after the answer within 240s")
	}
	finalAt := final.CreatedAt

	// The work itself happened.
	var done []struct {
		ID int64 `json:"id"`
	}
	c.do("GET", fmt.Sprintf("/api/apps/todo/todos?view=done&list_id=%d", list.ID), nil, &done)
	if len(done) != len(todos) {
		t.Fatalf("all %d Cleanup todos should be done, got %d", len(todos), len(done))
	}

	// Thinking showed while main worked: a live response opened after the
	// answer and was still open before the final reply landed.
	thinking := frames.thinkingBetween(answeredAt, finalAt)
	if thinking == 0 {
		t.Fatalf("no Thinking in the conversation between the answer and the final reply; frames=%s", frames.summary())
	}

	// Every write after the answer is a tool row in the conversation.
	var activity []ToolActivity
	if status := c.do("GET", "/api/apps/conversations/activity?chat_id="+conv.ID, nil, &activity); status != http.StatusOK {
		t.Fatalf("read activity: status=%d", status)
	}
	completes := 0
	for _, row := range activity {
		started, _ := time.Parse(time.RFC3339Nano, row.StartedAt)
		if row.Name == "todo_todos_complete" && started.After(answeredAt) {
			completes++
			if row.Status != "completed" {
				t.Errorf("tool row did not finish: %+v", row)
			}
		}
	}
	if completes != len(todos) {
		t.Fatalf("conversation shows %d of %d todo_todos_complete calls after the answer; rows=%+v", completes, len(todos), activity)
	}

	// And Thinking ends with the reply.
	c.waitIdle(conv.ID, 40*time.Second)
	t.Logf("answer → %d Thinking frames, %d tool rows, final: %s", thinking, completes, final.Content)
}

// waitIdle waits until the conversation has no live response.
func (c *scenarioClient) waitIdle(chatID string, within time.Duration) {
	c.t.Helper()
	for deadline := time.Now().Add(within); time.Now().Before(deadline); {
		var summary struct {
			Active []string `json:"active_conversation_ids"`
		}
		if code := c.do("GET", "/api/apps/conversations/activity-summary", nil, &summary); code != http.StatusOK {
			c.t.Fatalf("activity summary: status=%d", code)
		}
		active := false
		for _, id := range summary.Active {
			active = active || id == chatID
		}
		if !active {
			return
		}
		time.Sleep(time.Second)
	}
	c.t.Fatalf("conversation %s still active after %s", chatID, within)
}

type timedFrame struct {
	at    time.Time
	frame StreamFrame
}

type frameLog struct {
	mu     sync.Mutex
	frames []timedFrame
	stop   func()
}

// liveFrames records every live frame of the conversation, progress included.
func (c *scenarioClient) liveFrames(chatID string) *frameLog {
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
	if err != nil || resp.StatusCode != http.StatusOK {
		cancel()
		c.t.Fatalf("open conversation stream: %v", err)
	}
	log := &frameLog{stop: func() { cancel(); resp.Body.Close() }}
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var frame StreamFrame
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame) != nil {
				continue
			}
			log.mu.Lock()
			now := time.Now()
			log.frames = append(log.frames, timedFrame{now, frame})
			for _, inner := range frame.Frames {
				log.frames = append(log.frames, timedFrame{now, inner})
			}
			log.mu.Unlock()
		}
	}()
	return log
}

// thinkingBetween counts progress frames that report an open response
// (not idle) received after from and before to.
func (l *frameLog) thinkingBetween(from, to time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, f := range l.frames {
		if f.frame.Progress != nil && f.frame.Progress.Phase != "idle" && f.at.After(from) && f.at.Before(to) {
			n++
		}
	}
	return n
}

func (l *frameLog) summary() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var parts []string
	for _, f := range l.frames {
		kind := "text"
		switch {
		case f.frame.Progress != nil:
			kind = "progress:" + f.frame.Progress.Phase
		case f.frame.Activity != nil:
			kind = "tool:" + f.frame.Activity.Name
		case f.frame.Done:
			kind = "done"
		}
		parts = append(parts, f.at.Format("15:04:05.000")+" "+kind)
	}
	return strings.Join(parts, ", ")
}
