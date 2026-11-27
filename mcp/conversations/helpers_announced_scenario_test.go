//go:build scenario

package main

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestScenario_HelpersAnnounced mirrors a prod H Sites conversation
// (2026-11-26) where five helpers appeared with no word from the agent about
// why, what each covered or whether they would change anything. The operator
// asks for two watchers that keep going after the reply, the kind of work the
// guidance hands to helpers. The agent must first say how many helpers, what
// each covers and what they will not change, then spawn each with a _reason
// that becomes its title. The YAML asserts a conversations_send precedes the
// first spawn.
func TestScenario_HelpersAnnounced(t *testing.T) {
	c := newScenarioClient(t)
	agentID, cleanupAgent := c.ensureAgent()
	defer cleanupAgent()

	paidList := c.seedTodoList("Invoices to close", "Invoice 1044 Liumera awaiting transfer")
	refundList := c.seedTodoList("Support inbox", "Change delivery address for order 91")

	var conv struct {
		ID string `json:"id"`
	}
	if status := c.do("POST", "/api/apps/conversations/chats", map[string]any{"agent_id": agentID, "title": "Month-end watch"}, &conv); status != http.StatusOK || conv.ID == "" {
		t.Fatalf("create conversation: status=%d", status)
	}
	defer c.do("DELETE", "/api/apps/conversations/chats?id="+conv.ID, nil, nil)

	content := "For the next three minutes, use two helpers that check every 20 seconds. One watches my todo list " +
		"Invoices to close and marks done every todo whose title starts with PAID as soon as it appears. The other " +
		"watches my Support inbox list and tells you about every new refund request, without changing anything " +
		"there; post each one here. Reply with WATCHERS_READY once both are running, and with WATCH_DONE and " +
		"what they did when the three minutes are over."
	if status := c.do("POST", "/api/apps/conversations/messages?chat_id="+conv.ID, map[string]any{
		"content": content, "client_message_id": "helpers-announced-1",
	}, nil); status != http.StatusOK {
		t.Fatalf("post message: status=%d", status)
	}

	transcript := func() []Message {
		var out []Message
		c.do("GET", "/api/apps/conversations/messages?chat_id="+conv.ID, nil, &out)
		return out
	}
	agentSaid := func(messages []Message, marker string) *Message {
		for i := range messages {
			if messages[i].Role == "agent" && strings.Contains(messages[i].Content, marker) {
				return &messages[i]
			}
		}
		return nil
	}
	helpers := func() []ConversationWorker {
		var workers []ConversationWorker
		if status := c.do("GET", "/api/apps/conversations/workers?chat_id="+conv.ID, nil, &workers); status != http.StatusOK {
			t.Fatalf("read workers: status=%d", status)
		}
		var direct []ConversationWorker
		for _, w := range workers {
			if w.Depth == 1 {
				direct = append(direct, w)
			}
		}
		return direct
	}

	// Both watchers run.
	var messages []Message
	var direct []ConversationWorker
	for deadline := time.Now().Add(180 * time.Second); time.Now().Before(deadline); time.Sleep(3 * time.Second) {
		messages, direct = transcript(), helpers()
		if len(direct) >= 2 && agentSaid(messages, "WATCHERS_READY") != nil {
			break
		}
	}
	if len(direct) < 2 {
		t.Fatalf("asked for two helpers, conversation shows %d; agent messages=%+v", len(direct), agentLines(messages))
	}

	// New work arrives while they watch.
	if status := c.do("POST", "/api/apps/todo/todos", map[string]any{"title": "PAID Invoice 1045 Green Cleaning", "list_id": paidList, "source": "human"}, nil); status != http.StatusOK {
		t.Fatalf("add PAID todo: status=%d", status)
	}
	if status := c.do("POST", "/api/apps/todo/todos", map[string]any{"title": "Refund request: damaged parcel 97", "list_id": refundList, "source": "human"}, nil); status != http.StatusOK {
		t.Fatalf("add refund todo: status=%d", status)
	}

	var final *Message
	for deadline := time.Now().Add(360 * time.Second); time.Now().Before(deadline) && final == nil; time.Sleep(5 * time.Second) {
		messages = transcript()
		final = agentSaid(messages, "WATCH_DONE")
	}
	if final == nil {
		t.Fatalf("no WATCH_DONE reply within 360s; agent messages=%+v", agentLines(messages))
	}
	direct = helpers()

	// Each helper is titled by its spawn reason, not its directive.
	firstStart := time.Time{}
	labels := make([]string, 0, len(direct))
	for _, w := range direct {
		label := strings.TrimSpace(w.Label)
		labels = append(labels, label)
		if label == "" {
			t.Errorf("helper %s has no title (spawn gave no _reason)", w.ThreadID)
		}
		if label != "" && label == strings.TrimSpace(w.Purpose) {
			t.Errorf("helper %s is titled by its whole directive: %q", w.ThreadID, label)
		}
		if len([]rune(label)) > 120 {
			t.Errorf("helper %s title is not short: %q", w.ThreadID, label)
		}
		if started, err := time.Parse(time.RFC3339Nano, w.StartedAt); err == nil && (firstStart.IsZero() || started.Before(firstStart)) {
			firstStart = started
		}
	}

	// Before the first helper started, the agent said how many and what they
	// will not change.
	count := regexp.MustCompile(`(?i)\b(two|2)\b`)
	scope := regexp.MustCompile(`(?i)read[- ]only|only read|won't|will not|not change|no changes|without changing|nothing (else )?(is|will be) changed|only report|only tell|leave .* (as is|unchanged|untouched)`)
	var announcement *Message
	for i := range messages {
		m := &messages[i]
		if m.Role != "agent" || (!firstStart.IsZero() && m.CreatedAt.After(firstStart)) {
			continue
		}
		if count.MatchString(m.Content) && scope.MatchString(m.Content) {
			announcement = m
			break
		}
	}
	if announcement == nil {
		t.Fatalf("no announcement naming the helper count and what they won't change before the first helper started at %s; agent messages=%+v",
			firstStart.Format(time.RFC3339Nano), agentLines(messages))
	}

	if done := c.todoTitles(paidList, "done"); len(done) != 1 || !strings.HasPrefix(done[0], "PAID") {
		t.Errorf("only the PAID invoice should be done, got %v", done)
	}
	if done := c.todoTitles(refundList, "done"); len(done) != 0 {
		t.Errorf("the read-only helper changed Support inbox: %v", done)
	}
	if agentSaid(messages, "97") == nil {
		t.Errorf("the new refund request was never posted; agent messages=%+v", agentLines(messages))
	}
	c.waitIdle(conv.ID, 120*time.Second)
	t.Logf("announcement: %q · helper titles: %q · final: %s", announcement.Content, labels, final.Content)
}

func agentLines(transcript []Message) []string {
	var lines []string
	for _, m := range transcript {
		if m.Role == "agent" {
			lines = append(lines, m.CreatedAt.Format("15:04:05")+" ["+m.Phase+"] "+m.Content)
		}
	}
	return lines
}
