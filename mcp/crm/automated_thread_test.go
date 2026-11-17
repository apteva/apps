package main

import (
	"fmt"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// A newsletter's bounces arrive as one DSN per failed recipient, none of them
// referencing a stored message. They must share one conversation, not open
// one each (prod: 1,866 conversations from one mailer-daemon contact).
func TestAutomatedEmailSharesOneConversationPerSender(t *testing.T) {
	ctx := newTestCtx(t, tk.WithConfig(map[string]string{"automated_inbound_policy": "review_new"}))
	var first int64
	for i := 0; i < 25; i++ {
		out, err := ingestInbound(ctx, "test-proj", inboundPayload{
			MatchedRecipient: "inbox@example.test", Channel: channelEmail,
			From: "mailer-daemon@eu-west-1.amazonses.com", MessageID: int64(9000 + i),
			MessageIDHeader: fmt.Sprintf("<dsn-%d@eu-west-1.amazonses.com>", i),
			Subject:         "Delivery Status Notification (Failure)",
			BodyText:        fmt.Sprintf("could not deliver to user%d@example.test", i),
		})
		if err != nil {
			t.Fatal(err)
		}
		id := out["conversation_id"].(int64)
		if i == 0 {
			first = id
		} else if id != first {
			t.Fatalf("bounce %d opened conversation %d, want %d", i, id, first)
		}
	}
	var convos, activities int
	db := ctx.AppDB()
	if err := db.QueryRow(`SELECT COUNT(*) FROM contact_conversations WHERE channel='email'`).Scan(&convos); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM contact_activities WHERE conversation_id=?`, first).Scan(&activities); err != nil {
		t.Fatal(err)
	}
	if convos != 1 || activities != 25 {
		t.Fatalf("conversations=%d activities=%d, want 1 and 25", convos, activities)
	}
}

// A closed automated thread is reused (and reopened) rather than replaced.
func TestAutomatedEmailReusesClosedConversation(t *testing.T) {
	ctx := newTestCtx(t, tk.WithConfig(map[string]string{"automated_inbound_policy": "review_new"}))
	send := func(n int) int64 {
		out, err := ingestInbound(ctx, "test-proj", inboundPayload{
			MatchedRecipient: "dmarc@example.test", Channel: channelEmail,
			From: "noreply@dmarc.yahoo.com", MessageID: int64(9100 + n),
			MessageIDHeader: fmt.Sprintf("<report-%d@yahoo.com>", n),
			Subject:         fmt.Sprintf("Report Domain: example.test Report-ID: %d", n), BodyText: "aggregate report",
		})
		if err != nil {
			t.Fatal(err)
		}
		return out["conversation_id"].(int64)
	}
	first := send(1)
	if _, err := ctx.AppDB().Exec(`UPDATE contact_conversations SET status='closed' WHERE id=?`, first); err != nil {
		t.Fatal(err)
	}
	if second := send(2); second != first {
		t.Fatalf("second report opened conversation %d, want %d", second, first)
	}
	var status string
	if err := ctx.AppDB().QueryRow(`SELECT status FROM contact_conversations WHERE id=?`, first).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "open" {
		t.Fatalf("status=%q, want open", status)
	}
}

// Human senders keep one conversation per email thread.
func TestHumanEmailStillOpensNewConversationPerThread(t *testing.T) {
	ctx := newTestCtx(t)
	ids := map[int64]bool{}
	for i := 0; i < 3; i++ {
		out, err := ingestInbound(ctx, "test-proj", inboundPayload{
			MatchedRecipient: "inbox@example.test", Channel: channelEmail,
			From: "person@example.test", MessageID: int64(9200 + i),
			MessageIDHeader: fmt.Sprintf("<human-%d@example.test>", i),
			Subject:         fmt.Sprintf("Question %d", i), BodyText: "hello",
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[out["conversation_id"].(int64)] = true
	}
	if len(ids) != 3 {
		t.Fatalf("human threads=%d, want 3", len(ids))
	}
}

func TestDeliveryReportContentTypeIsAutomated(t *testing.T) {
	for _, ct := range []string{
		`multipart/report; report-type=delivery-status; boundary="x"`,
		`multipart/report; report-type=feedback-report`,
		`message/delivery-status`,
	} {
		ok, reason := isAutomatedSender(channelEmail, "reports@mail.example.test", map[string]any{"Content-Type": ct})
		if !ok || reason != "delivery report" {
			t.Fatalf("%s: automated=%v reason=%q", ct, ok, reason)
		}
	}
	if ok, _ := isAutomatedSender(channelEmail, "person@example.test", map[string]any{"Content-Type": "multipart/alternative"}); ok {
		t.Fatal("ordinary multipart mail classified as automated")
	}
}
