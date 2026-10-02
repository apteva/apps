package main

import (
	"encoding/json"
	"strings"
)

// MessageAddresses projects the recorded message envelope for display. Never
// infer it from the contact or accumulated conversation participants.
type MessageAddresses struct {
	From       string   `json:"from,omitempty"`
	To         []string `json:"to,omitempty"`
	CC         []string `json:"cc,omitempty"`
	BCC        []string `json:"bcc,omitempty"`
	ReceivedAt string   `json:"received_at,omitempty"`
}

func messageAddresses(kind, raw string) *MessageAddresses {
	switch kind {
	case ActivityKindEmailSent, ActivityKindEmailReceived, ActivityKindEmailSendFailed, ActivityKindEmailTestSent,
		ActivityKindSMSSent, ActivityKindSMSReceived, ActivityKindSMSSendFailed, ActivityKindSMSTestSent,
		ActivityKindWhatsAppSent, ActivityKindWhatsAppReceived, ActivityKindWhatsAppSendFailed, ActivityKindWhatsAppTestSent:
	default:
		return nil
	}
	var detail map[string]any
	if json.Unmarshal([]byte(raw), &detail) != nil {
		return nil
	}
	text := func(key string) string {
		value, _ := detail[key].(string)
		return strings.TrimSpace(value)
	}
	addresses := func(key string) []string {
		var values []any
		switch value := detail[key].(type) {
		case string:
			values = []any{value}
		case []any:
			values = value
		}
		var out []string
		seen := map[string]bool{}
		for _, value := range values {
			address, _ := value.(string)
			address = strings.TrimSpace(address)
			if address != "" && !seen[address] {
				out = append(out, address)
				seen[address] = true
			}
		}
		return out
	}
	out := &MessageAddresses{From: text("from"), To: addresses("to"), CC: addresses("cc"), BCC: addresses("bcc"), ReceivedAt: text("receiving_identity")}
	if out.ReceivedAt == "" {
		out.ReceivedAt = text("matched_recipient")
	}
	if out.From == "" && len(out.To) == 0 && len(out.CC) == 0 && len(out.BCC) == 0 && out.ReceivedAt == "" {
		return nil
	}
	return out
}

// All response paths gain the same safe projection, including legacy records,
// without exposing private provider/source metadata or writing to the database.
func (a Activity) MarshalJSON() ([]byte, error) {
	type plainActivity Activity
	return json.Marshal(struct {
		plainActivity
		Addresses *MessageAddresses `json:"message_addresses,omitempty"`
	}{plainActivity(a), messageAddresses(a.Kind, a.SourceDetail)})
}
