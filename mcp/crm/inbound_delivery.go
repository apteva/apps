package main

import (
	"fmt"
	"net/mail"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Validate before any contact, conversation, activity, attachment or event writes.
// A project-scoped webhook alone does not establish recipient ownership: shared
// SES subscriptions can deliver a message to the wrong project's catch-all route.
func validateInboundDelivery(ctx *sdk.AppCtx, pid string, body *inboundPayload) (string, string, error) {
	status := strings.ToLower(strings.TrimSpace(body.RouteStatus))
	if status == "suppressed" || status == "no_match" || status == "no-match" {
		return "", "inbound route status: " + status, nil
	}
	if body.Channel != channelEmail {
		return strings.TrimSpace(body.MatchedRecipient), "", nil
	}
	recipient := inboundEmailRecipient(body.MatchedRecipient)
	if recipient == "" && strings.TrimSpace(body.MatchedRecipient) == "" && len(body.EnvelopeRecipients) == 1 {
		recipient = inboundEmailRecipient(body.EnvelopeRecipients[0])
	}
	if recipient == "" {
		return "", "missing or invalid canonical receiving identity", nil
	}
	if len(body.EnvelopeRecipients) > 0 {
		found := false
		for _, address := range body.EnvelopeRecipients {
			if inboundEmailRecipient(address) == recipient {
				found = true
				break
			}
		}
		if !found {
			return recipient, "receiving identity does not match SMTP envelope", nil
		}
	}
	body.MatchedRecipient = recipient
	// Standalone HTTP callers retain compatibility but must supply a valid
	// canonical recipient; bound Messaging installs get authoritative ownership.
	if messagingBound(ctx) == nil {
		return recipient, "", nil
	}
	var senders struct {
		Senders []struct {
			Address string `json:"address"`
		} `json:"senders"`
	}
	if err := callMessagingTool(ctx, "senders_list", map[string]any{
		"_project_id": pid, "channel": channelEmail, "verified_only": false,
	}, &senders); err != nil {
		return "", "", fmt.Errorf("validate inbound sender ownership: %w", err)
	}
	for _, sender := range senders.Senders {
		if inboundEmailRecipient(sender.Address) == recipient {
			return recipient, "", nil
		}
	}
	var identities struct {
		Identities []struct {
			Kind    string `json:"kind"`
			Address string `json:"address"`
		} `json:"identities"`
	}
	if err := callMessagingTool(ctx, "identities_list", map[string]any{
		"_project_id": pid, "kind": "email_domain",
	}, &identities); err != nil {
		return "", "", fmt.Errorf("validate inbound domain ownership: %w", err)
	}
	for _, identity := range identities.Identities {
		if strings.EqualFold(identity.Kind, "email_domain") && strings.EqualFold(strings.TrimSpace(identity.Address), domainOf(recipient)) {
			return recipient, "", nil
		}
	}
	return recipient, "receiving identity is not owned by CRM project", nil
}

func inboundEmailRecipient(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 7 && strings.EqualFold(raw[:7], "mailto:") {
		raw = raw[7:]
	}
	address, err := mail.ParseAddress(raw)
	if err != nil || !strings.Contains(address.Address, "@") {
		return ""
	}
	return strings.ToLower(address.Address)
}
