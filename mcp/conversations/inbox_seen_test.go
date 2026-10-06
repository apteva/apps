package main

import "testing"

func TestReadingAlertsClearsOnlyTheReadersSeenNotifications(t *testing.T) {
	app, _, _ := newTestEnv(t)
	conv, err := app.store.CreateConversation(CreateConversationInput{ProjectID: testProject, LeadAgentID: 41, Title: "Shared alerts"})
	if err != nil {
		t.Fatal(err)
	}
	appendCard := func(kind, severity string, inboxOnly bool) *Message {
		t.Helper()
		m, err := app.store.AppendMessage(&Message{ConversationID: conv.ID, Role: "agent", AgentID: 41, Content: "Notice", ComponentKind: kind, Severity: severity, InboxOnly: inboxOnly, ActionStatus: "pending"})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	page := func(user int64) InboxPage {
		t.Helper()
		p, err := app.store.InboxPage(testProject, user, 41, 100, "")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	first := appendCard(kindAlert, "info", false)
	if p := page(1); p.Total != 1 || p.Attention[conv.ID] != 1 {
		t.Fatalf("unseen alert: %+v", p)
	}
	if err := app.store.MarkSeen(1, conv.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if p := page(1); p.Total != 0 || len(p.Items) != 0 || len(p.Attention) != 0 {
		t.Fatalf("read alert still pending: %+v", p)
	}
	if p := page(2); p.Total != 1 || p.Attention[conv.ID] != 1 {
		t.Fatalf("another reader lost alert: %+v", p)
	}
	stored, err := app.store.GetMessage(first.ID)
	if err != nil || stored.ComponentKind != kindAlert {
		t.Fatalf("reading changed durable card: %+v, %v", stored, err)
	}
	warning := appendCard(kindAlert, "warn", false)
	latest := appendCard(kindAlert, "error", false)
	if err := app.store.MarkSeen(1, conv.ID, warning.ID); err != nil {
		t.Fatal(err)
	}
	if p := page(1); p.Total != 1 || p.Items[0].Message.ID != latest.ID || p.Attention[conv.ID] != 4 {
		t.Fatalf("newer alert must remain unread: %+v", p)
	}
	if err := app.store.MarkSeen(1, conv.ID, latest.ID); err != nil {
		t.Fatal(err)
	}
	if p := page(1); p.Total != 0 || len(p.Attention) != 0 {
		t.Fatalf("read error still pending: %+v", p)
	}
	approval := appendCard(kindApproval, "", false)
	appendCard(kindReport, "", false)
	appendCard(kindAlert, "info", true)
	final, err := app.store.AppendMessage(&Message{ConversationID: conv.ID, Role: "agent", AgentID: 41, Content: "Reply after inbox-only alert"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.MarkSeen(1, conv.ID, final.ID); err != nil {
		t.Fatal(err)
	}
	if p := page(1); p.Total != 3 || p.Attention[conv.ID] != 2 || p.Items[0].Message.ID != approval.ID {
		t.Fatalf("reading must retain actionable/inbox-only cards: %+v", p)
	}
	global, err := app.store.InboxPageAcrossProjects([]string{testProject}, 1, 0, 1, "")
	if err != nil || global.Total != 3 || global.Attention[conv.ID] != 2 || len(global.Items) != 1 || global.NextCursor == "" {
		t.Fatalf("global inbox/pagination inconsistent: %+v, %v", global, err)
	}
}
