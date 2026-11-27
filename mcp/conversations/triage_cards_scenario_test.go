//go:build scenario

package main

import (
	"strings"
	"testing"
	"time"
)

// TestScenario_ScheduledTriageFilesCards reproduces an email-triage agent
// whose main thread already answers an operator chat. Main may post plain
// replies there, so a scheduled check that should alert the operator could
// end as an ordinary message. Important items must become an alert card and
// the daily summary a report card.
func TestScenario_ScheduledTriageFilesCards(t *testing.T) {
	c := newScenarioClient(t)
	agent, _ := c.ensureAgent()

	var conv struct {
		ID string `json:"id"`
	}
	if status := c.do("POST", "/api/apps/conversations/chats", map[string]any{"agent_id": agent, "title": "Operator"}, &conv); status != 200 || conv.ID == "" {
		t.Fatalf("create operator chat: %d", status)
	}
	defer c.deleteConversation(conv.ID)
	if status := c.do("POST", "/api/apps/conversations/messages?chat_id="+conv.ID, map[string]any{
		"content": "Hi, I'm the operator. Reply with exactly: ready", "client_message_id": "triage-hello",
	}, nil); status != 200 {
		t.Fatalf("post operator message: %d", status)
	}
	waitForMainReply(t, c, conv.ID, "ready")

	c.injectEvent(agent, "[scheduler] Hourly email check. Open CRM email conversations supplied for this check:\n"+
		"1. From Dana Reyes <dana@northwind.example>, subject \"Invoice 2214 is 30 days overdue\": asks for payment by Friday or the account goes to collections.\n"+
		"2. From Weekly Deals <news@deals.example>, subject \"This week's offers\": a marketing newsletter.\n"+
		"3. From Security <alerts@bank.example>, subject \"New sign-in from an unknown device\": a sign-in to the business bank account from a new device.\n"+
		"There is no CRM tool in this test: treat these as already read and closed after you notify the operator.")
	alert := c.pollInbox(agent, kindAlert, 240*time.Second)
	if alert.Message.Severity == "" {
		t.Fatalf("alert without severity: %+v", alert.Message)
	}
	if text := strings.ToLower(alert.Message.Content); !strings.Contains(text, "2214") && !strings.Contains(text, "sign-in") && !strings.Contains(text, "invoice") {
		t.Fatalf("alert does not name the important items: %q", alert.Message.Content)
	}
	t.Logf("alert %d (%s) in %s: %s", alert.Message.ID, alert.Message.Severity, alert.Message.ConversationID, alert.Message.Content)

	c.injectEvent(agent, "[scheduler] Daily summary due (18:00 Europe/Paris). Today's email conversations: 3 came in. "+
		"Closed as unimportant: 1 marketing newsletter. Alerted: the overdue invoice 2214 and the new bank sign-in. "+
		"Still waiting on the operator: payment of invoice 2214. Send the operator the daily summary.")
	report := c.pollInbox(agent, kindReport, 240*time.Second)
	t.Logf("report %d in %s: %s", report.Message.ID, report.Message.ConversationID, report.Message.Content)
}

// waitForMainReply waits for main to answer in its operator conversation.
func waitForMainReply(t *testing.T, c *scenarioClient, conversationID, want string) {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		var rows []Message
		c.do("GET", "/api/apps/conversations/messages?chat_id="+conversationID, nil, &rows)
		for _, row := range rows {
			if row.Role == "agent" && strings.Contains(strings.ToLower(row.Content), want) {
				if row.ThreadID != mainThreadID {
					t.Fatalf("operator chat answered from %q, want main", row.ThreadID)
				}
				return
			}
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("main did not reply %q within 120s", want)
}
