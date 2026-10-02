package main

import (
	"testing"
	"time"
)

func notificationEvent(t *testing.T, appEvents []capturedAppEvent, topic string) capturedAppEvent {
	t.Helper()
	for _, event := range appEvents {
		if event.Topic == topic {
			return event
		}
	}
	t.Fatalf("event %q not found in %+v", topic, appEvents)
	return capturedAppEvent{}
}

func notificationRecipientIDs(t *testing.T, event capturedAppEvent) []int64 {
	t.Helper()
	raw, ok := event.Data["recipient_user_ids"].([]any)
	if !ok {
		t.Fatalf("recipient_user_ids = %#v, want JSON array", event.Data["recipient_user_ids"])
	}
	ids := make([]int64, 0, len(raw))
	for _, value := range raw {
		n, ok := value.(float64)
		if !ok {
			t.Fatalf("recipient id = %#v, want number", value)
		}
		ids = append(ids, int64(n))
	}
	return ids
}

func TestConversationManifestDeclaresRecipientNotifications(t *testing.T) {
	manifest := (&App{}).Manifest()
	want := map[string]string{
		"conversation.message.created":  "conversation-message-created",
		"conversation.approval.created": "conversation-approval-created",
		"conversation.alert.created":    "conversation-alert-created",
		"conversation.report.created":   "conversation-report-created",
	}
	seen := map[string]bool{}
	for _, event := range manifest.Provides.Publishes {
		id, ok := want[event.Name]
		if !ok {
			continue
		}
		if event.Notification == nil {
			t.Fatalf("%s has no notification declaration", event.Name)
		}
		seen[event.Name] = true
		n := event.Notification
		if n.ID != id || n.Audience != "recipients" || n.RecipientsField != "recipient_user_ids" {
			t.Fatalf("%s notification = %+v", event.Name, *n)
		}
		if !n.Defaults.InApp || !n.Defaults.Tab || n.Defaults.Desktop || n.Defaults.Mobile {
			t.Fatalf("%s defaults = %+v", event.Name, n.Defaults)
		}
		if n.GroupBy != "conversation_id" || n.Link != "?chat={conversation_id}" {
			t.Fatalf("%s routing = group %q link %q", event.Name, n.GroupBy, n.Link)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Fatalf("manifest is missing %s", name)
		}
	}
}

func TestConversationNotificationTopicFollowsDurableMessageKind(t *testing.T) {
	for _, test := range []struct {
		kind string
		want string
	}{
		{kind: "", want: "conversation.message.created"},
		{kind: kindApproval, want: "conversation.approval.created"},
		{kind: kindAlert, want: "conversation.alert.created"},
		{kind: kindReport, want: "conversation.report.created"},
	} {
		t.Run(test.want, func(t *testing.T) {
			if got := conversationEventTopic(&Message{ComponentKind: test.kind}); got != test.want {
				t.Fatalf("topic = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAppendPublishesPrivateRecipientScopedNotification(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	if _, err := app.store.db.Exec(`INSERT INTO participants(conversation_id,user_id) VALUES(?,?)`, conv.ID, 2); err != nil {
		t.Fatalf("add participant: %v", err)
	}
	msg, inserted, err := app.appendAndDeliver(ctx, conv, &Message{
		ConversationID: conv.ID,
		Role:           "agent",
		AgentID:        41,
		Content:        "A private update",
		ClientID:       "notification-private-1",
	})
	if err != nil || !inserted {
		t.Fatalf("append = message=%+v inserted=%v err=%v", msg, inserted, err)
	}
	event := notificationEvent(t, platform.appEvents, "conversation.message.created")
	if event.ProjectID != testProject {
		t.Fatalf("project_id = %q, want %q", event.ProjectID, testProject)
	}
	if got := notificationRecipientIDs(t, event); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("recipients = %v, want [1 2]", got)
	}
	if event.Data["preview"] != "A private update" {
		t.Fatalf("preview = %#v", event.Data["preview"])
	}
}

func TestAppendPublishesCardSpecificNotificationTopics(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	for _, test := range []struct {
		kind    string
		content string
		topic   string
	}{
		{kind: kindApproval, content: "Approval requested: deploy", topic: "conversation.approval.created"},
		{kind: kindAlert, content: "A service is degraded", topic: "conversation.alert.created"},
		{kind: kindReport, content: "Report: weekly status", topic: "conversation.report.created"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			msg, inserted, err := app.appendAndDeliver(ctx, conv, &Message{
				ConversationID: conv.ID,
				Role:           "agent",
				AgentID:        41,
				Content:        test.content,
				ComponentKind:  test.kind,
				ClientID:       "notification-card-" + test.kind,
			})
			if err != nil || !inserted {
				t.Fatalf("append = message=%+v inserted=%v err=%v", msg, inserted, err)
			}
			event := notificationEvent(t, platform.appEvents, test.topic)
			if event.Data["message_id"] != float64(msg.ID) || event.Data["preview"] != test.content {
				t.Fatalf("event data = %#v", event.Data)
			}
		})
	}
}

func TestAppendNotificationRetryUsesStableEventID(t *testing.T) {
	app, ctx, platform := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	request := &Message{ConversationID: conv.ID, Role: "agent", AgentID: 41, Content: "Retry me", ClientID: "notification-retry-1"}
	first, inserted, err := app.appendAndDeliver(ctx, conv, request)
	if err != nil || !inserted {
		t.Fatalf("first append = message=%+v inserted=%v err=%v", first, inserted, err)
	}
	second, inserted, err := app.appendAndDeliver(ctx, conv, request)
	if err != nil || inserted || second.ID != first.ID {
		t.Fatalf("retry = message=%+v inserted=%v err=%v", second, inserted, err)
	}
	if len(platform.appEvents) != 2 {
		t.Fatalf("published events = %+v, want two idempotent attempts", platform.appEvents)
	}
	if platform.appEvents[0].EventID != platform.appEvents[1].EventID || platform.appEvents[0].Topic != platform.appEvents[1].Topic {
		t.Fatalf("retry changed event identity: %+v", platform.appEvents)
	}
}

func TestStreamingFragmentsDoNotPublishConversationNotifications(t *testing.T) {
	app, _, platform := newTestEnv(t)
	app.streamer.Ingest("llm.tool_chunk", 41, "main", `{"chunk":"partial"}`, time.Now())
	app.streamer.Ingest("tool.call", 41, "main", `{"id":"call-1","name":"search_tools"}`, time.Now())
	if len(platform.appEvents) != 0 {
		t.Fatalf("streaming published durable notifications: %+v", platform.appEvents)
	}
}
