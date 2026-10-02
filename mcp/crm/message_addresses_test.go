package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestMessageAddressesProjection(t *testing.T) {
	for _, tc := range []struct {
		name, kind, raw string
		want            *MessageAddresses
	}{
		{"inbound", ActivityKindEmailReceived, `{"from":"sender@example.test","to":["sales@example.test","support@example.test"],"cc":["team@example.test"],"receiving_identity":"alias@example.test","private":"secret"}`, &MessageAddresses{From: "sender@example.test", To: []string{"sales@example.test", "support@example.test"}, CC: []string{"team@example.test"}, ReceivedAt: "alias@example.test"}},
		{"outbound", ActivityKindEmailSent, `{"from":"sales@example.test","to":"customer@example.test","bcc":["archive@example.test"]}`, &MessageAddresses{From: "sales@example.test", To: []string{"customer@example.test"}, BCC: []string{"archive@example.test"}}},
		{"phone", ActivityKindSMSSent, `{"to":"+15551230000"}`, &MessageAddresses{To: []string{"+15551230000"}}},
		{"partial", ActivityKindWhatsAppReceived, `{"matched_recipient":"+15551230000"}`, &MessageAddresses{ReceivedAt: "+15551230000"}},
		{"malformed", ActivityKindEmailReceived, `{`, nil},
		{"wrong shapes", ActivityKindEmailSent, `{"to":42,"from":{"address":"x"}}`, nil},
		{"note", ActivityKindNote, `{"to":"not-a-message"}`, nil},
		{"dedup", ActivityKindEmailSendFailed, `{"to":[" a@example.test ",null,42,"a@example.test",""]}`, &MessageAddresses{To: []string{"a@example.test"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := messageAddresses(tc.kind, tc.raw); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			body, err := json.Marshal(&Activity{Kind: tc.kind, SourceDetail: tc.raw, Body: "unchanged"})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "private") || strings.Contains(string(body), "secret") || strings.Contains(string(body), "source_detail") {
				t.Fatalf("private metadata exposed: %s", body)
			}
			var wire struct {
				Addresses *MessageAddresses `json:"message_addresses"`
				Body      string            `json:"body"`
			}
			if err := json.Unmarshal(body, &wire); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(wire.Addresses, tc.want) || wire.Body != "unchanged" {
				t.Fatalf("wrong wire projection: %s", body)
			}
		})
	}
}

func TestInboxRecipientsUseLatestMessageNotContactOrThreadUnion(t *testing.T) {
	ctx := newTestCtx(t)
	_, err := ingestInbound(ctx, "test-proj", inboundPayload{Channel: "email", From: "customer@example.test", To: []string{"original@example.test"}, MatchedRecipient: "inbound-alias@example.test", MessageIDHeader: "thread@example.test", Subject: "Recipient visibility", BodyText: "First", ReceivedAt: "2026-10-01T12:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := dbInboxConversations(ctx.AppDB(), "test-proj", "all", 50, 0, nil)
	if err != nil || len(rows) != 1 {
		t.Fatalf("inbox: %v %v", rows, err)
	}
	want := &MessageAddresses{From: "customer@example.test", To: []string{"inbound-alias@example.test"}, ReceivedAt: "inbound-alias@example.test"}
	if !reflect.DeepEqual(rows[0].LastMessageAddresses, want) {
		t.Fatalf("inbox=%+v", rows[0].LastMessageAddresses)
	}
	_, err = logMessageActivity(ctx.AppDB(), logMessageActivityInput{ProjectID: "test-proj", ContactID: rows[0].ContactID, ConversationID: rows[0].ID, Kind: ActivityKindEmailSent, Body: "Reply", OccurredAt: "2026-10-02T12:00:00Z", SourceDetail: map[string]any{"from": "sales@example.test", "to": "alternate@example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err = dbInboxConversations(ctx.AppDB(), "test-proj", "all", 50, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rows[0].LastMessageAddresses, &MessageAddresses{From: "sales@example.test", To: []string{"alternate@example.test"}}) {
		t.Fatalf("wrong latest recipients: %+v", rows[0].LastMessageAddresses)
	}
	other, _, err := dbInboxConversations(ctx.AppDB(), "other-project", "all", 50, 0, nil)
	if err != nil || len(other) != 0 {
		t.Fatalf("cross-project addresses leaked: %v %v", other, err)
	}
	activities, err := dbConversationActivities(ctx.AppDB(), "test-proj", rows[0].ID, 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(activities) != 2 || !reflect.DeepEqual(messageAddresses(activities[0].Kind, activities[0].SourceDetail), want) {
		t.Fatalf("per-message addresses lost: %+v", activities)
	}
	// Malformed legacy JSON must not break the whole inbox or fabricate headers.
	if _, err := ctx.AppDB().Exec(`UPDATE contact_activities SET source_detail='{' WHERE id=?`, activities[1].ID); err != nil {
		t.Fatal(err)
	}
	rows, _, err = dbInboxConversations(ctx.AppDB(), "test-proj", "all", 50, 0, nil)
	if err != nil || rows[0].LastMessageAddresses != nil {
		t.Fatalf("malformed historical envelope: %v %v", rows, err)
	}
}
