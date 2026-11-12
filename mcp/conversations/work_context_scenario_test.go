//go:build scenario

package main

import (
	"testing"
	"time"
)

// Real model calls verify the local report capability and main's destination
// selection without telling main which conversation id to use.
func TestScenario_WorkConversationInbox(t *testing.T) {
	c := newScenarioClient(t)
	agent, _ := c.ensureAgent()
	create := func(title string) string {
		t.Helper()
		var conv Conversation
		if status := c.do("POST", "/api/apps/conversations/chats", map[string]any{
			"agent_id": agent, "title": title,
		}, &conv); status != 200 || conv.ID == "" {
			t.Fatalf("create %q: %d", title, status)
		}
		return conv.ID
	}
	origin := create("AI Agent Operating Brief")
	defer c.deleteConversation(origin)
	other := create("Unrelated inventory review")
	defer c.deleteConversation(other)
	send := func(text, key string) {
		t.Helper()
		if status := c.do("POST", "/api/apps/conversations/messages?chat_id="+origin, map[string]any{
			"content": text, "client_message_id": key,
		}, nil); status != 200 {
			t.Fatalf("send: %d", status)
		}
	}
	assertOwner := func(item liveInboxItem) {
		t.Helper()
		if item.Message.ConversationID != origin {
			t.Fatalf("%s went to %s, want originating %s", item.Message.ComponentKind, item.Message.ConversationID, origin)
		}
		var chats []Conversation
		if status := c.do("GET", "/api/apps/conversations/chats", nil, &chats); status != 200 || len(chats) != 2 {
			t.Fatalf("created an unrelated inbox conversation: status=%d chats=%+v", status, chats)
		}
		t.Logf("%s message %d stays in %s", item.Message.ComponentKind, item.Message.ID, origin)
	}
	dismiss := func(item liveInboxItem) {
		t.Helper()
		if status := c.do("POST", "/api/apps/conversations/message-dismiss", map[string]any{
			"message_id": item.Message.ID,
		}, nil); status != 200 {
			t.Fatalf("dismiss: %d", status)
		}
	}

	send("The AI Agent Operating Brief article is published. Its canonical URL and navigation were verified, "+
		"and the evidence is at https://example.com/article-verification. File a substantive publication report "+
		"for this work in my Inbox. Use these supplied facts; no external investigation is needed.", "work-report")
	report := c.pollInbox(agent, kindReport, 180*time.Second)
	assertOwner(report)
	dismiss(report)

	send("A new verification check for this same article found the canonical link broken. It is a material "+
		"publication problem. Raise an error alert in my Inbox about this work; the next step is to repair "+
		"and verify the canonical link. Do not fix it yet.", "work-alert")
	alert := c.pollInbox(agent, kindAlert, 180*time.Second)
	assertOwner(alert)
	dismiss(alert)

	send("Deleting the obsolete draft artifacts for this article is destructive. Request my approval "+
		"through the Inbox before doing it. This is an isolated test; after the verdict, acknowledge "+
		"it here and perform no deletion.", "work-approval")
	approval := c.pollInbox(agent, kindApproval, 180*time.Second)
	assertOwner(approval)
	if status := c.do("POST", "/api/apps/conversations/message-action", map[string]any{
		"message_id": approval.Message.ID, "action_id": approvalActionID(approval),
		"note": "Approved for this test. Acknowledge here and do not delete anything.",
	}, nil); status != 200 {
		t.Fatalf("approval: %d", status)
	}

	// Main receives work context by topic, as a scheduler would. The model must
	// search and reuse the existing article conversation.
	c.injectEvent(agent, "[scheduler] Follow-up report due for the AI Agent Operating Brief article. "+
		"The canonical link has now been repaired and the verification checks passed. File a substantive "+
		"report in the operator Inbox using these supplied facts. This is a continuation of the article work.")
	followUp := c.pollInbox(agent, kindReport, 180*time.Second)
	assertOwner(followUp)
	if followUp.Message.ID <= report.Message.ID {
		t.Fatal("main did not publish the follow-up report")
	}
	var history []Message
	if status := c.do("GET", "/api/apps/conversations/messages?chat_id="+origin, nil, &history); status != 200 {
		t.Fatalf("history: %d", status)
	}
	seen := map[int64]bool{}
	for _, message := range history {
		seen[message.ID] = true
	}
	for _, id := range []int64{report.Message.ID, alert.Message.ID, approval.Message.ID, followUp.Message.ID} {
		if !seen[id] {
			t.Fatalf("Inbox message %d missing from the work transcript", id)
		}
	}
}
