package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

var replyMessageIDPattern = regexp.MustCompile(`^<[^<>\s@]+@[^<>\s@]+>$`)
var replyTicketIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// New messages require channel/to; anchored replies derive their transport
// fields. Advertise both modes without making channel/to mandatory for replies.
func sendMessageInputSchema(properties map[string]any) map[string]any {
	schema := schemaObject(properties, nil)
	replyProperties := map[string]any{}
	for _, field := range []string{"channel", "from", "from_name", "to", "cc", "bcc", "connection_id", "subject", "reply_to", "in_reply_to", "references", "headers", "message_id_header"} {
		replyProperties[field] = false
	}
	schema["oneOf"] = []any{
		map[string]any{
			"required":   []string{"channel", "to"},
			"properties": map[string]any{"reply_to_message_id": false, "reply_all": false},
		},
		map[string]any{
			"required":   []string{"reply_to_message_id"},
			"properties": replyProperties,
		},
	}
	return schema
}

// sendReply builds a new send request from a project-owned anchor. Only
// new content is caller-controlled; transport fields never come from args.
func (a *App) sendReply(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	send := map[string]any{"_project_id": pid}
	for key, value := range args {
		switch key {
		case "body", "body_html", "attachments", "attachment_storage_ids", "media_url", "template_id", "vars", "content_sid", "content_variables":
			send[key] = value
		case "reply_to_message_id", "reply_all", "idempotency_key", "_project_id":
		default:
			return nil, fmt.Errorf("send_message reply: unsupported argument %q; addressing and threading are derived from reply_to_message_id", key)
		}
	}
	id := int64Arg(args, "reply_to_message_id")
	if id <= 0 {
		return nil, errors.New("reply_to_message_id: positive integer required")
	}
	if n, ok := args["reply_to_message_id"].(float64); ok && n != float64(id) {
		return nil, errors.New("reply_to_message_id: positive integer required")
	}
	replyAll := false
	if value, exists := args["reply_all"]; exists {
		var ok bool
		if replyAll, ok = value.(bool); !ok {
			return nil, errors.New("reply_all: boolean required")
		}
	}
	m, err := dbMessageGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("reply_to_message_id: message not found in this project")
	}
	if !validChannel(m.Channel) || (m.Direction != "in" && m.Direction != "out") {
		return nil, errors.New("reply_to_message_id: unsupported message channel or direction")
	}
	if replyAll && m.Channel != channelEmail {
		return nil, errors.New("reply_all: supported only for email")
	}
	// Namespace retries by anchor so a key used for a different reply (or a
	// plain send_message) cannot return an unrelated send result.
	if value, exists := args["idempotency_key"]; exists {
		key, ok := value.(string)
		if !ok {
			return nil, errors.New("idempotency_key: string required")
		}
		if key != "" {
			key = fmt.Sprintf("reply:%d:%s", id, key)
			send["idempotency_key"] = key
			previous, err := dbFindByIdempotencyKey(ctx.AppDB(), pid, key)
			if err != nil {
				return nil, err
			}
			if previous != nil {
				return sendResponse(previous), nil
			}
		}
	}
	headers := map[string]any{}
	if len(m.Headers) > 0 && string(m.Headers) != "null" {
		if err := json.Unmarshal(m.Headers, &headers); err != nil {
			return nil, fmt.Errorf("original message headers: %w", err)
		}
	}
	send["channel"] = m.Channel
	if m.Direction == "in" {
		from, taggedReplyTo, err := replySender(ctx, pid, m)
		if err != nil {
			return nil, err
		}
		send["from"] = from
		if taggedReplyTo != "" {
			send["reply_to"] = taggedReplyTo
		}
		target := m.From
		if m.Channel == channelEmail {
			if value, err := replyHeader(headers, "Reply-To"); err != nil {
				return nil, err
			} else if value != "" {
				target = value
			}
		}
		to, err := replyAddresses(m.Channel, []string{target})
		if err != nil {
			return nil, fmt.Errorf("reply recipient: %w", err)
		}
		send["to"] = to
	} else {
		from, err := replyAddresses(m.Channel, []string{m.From})
		if err != nil || len(from) != 1 {
			return nil, errors.New("original message must have one valid sender")
		}
		if _, err := validateOutboundSender(ctx.AppDB(), pid, m.Channel, from[0], true); err != nil {
			return nil, err
		}
		send["from"] = from[0]
		to, err := replyAddresses(m.Channel, m.To)
		if err != nil {
			return nil, fmt.Errorf("original recipients: %w", err)
		}
		send["to"] = to
		if m.Channel == channelEmail {
			value, err := replyHeader(headers, "Reply-To")
			if err != nil {
				return nil, err
			}
			if value != "" {
				addresses, err := replyAddresses(channelEmail, []string{value})
				if err != nil || len(addresses) != 1 {
					return nil, errors.New("original Reply-To must contain one valid address")
				}
				send["reply_to"] = addresses[0]
			}
		}
	}
	if m.Channel == channelEmail {
		// Keep Gmail replies on the receiving account, and outbound follow-ups
		// on their original account even if a sender was deleted/re-registered.
		if m.ProviderConnectionID > 0 && (m.ProviderSlug == "gmail" || m.Direction == "out") {
			send["connection_id"] = m.ProviderConnectionID
		}
		if err := prepareEmailReply(ctx, pid, m, headers, send, replyAll); err != nil {
			return nil, err
		}
	}
	return a.sendMessage(ctx, send, m.ID)
}

// Sender candidates come from the stored delivery/routing data, never from an
// arbitrary default mailbox. A tagged recipient can use its registered base
// mailbox while retaining the full tag as the outgoing Reply-To.
func replySender(ctx *sdk.AppCtx, pid string, m *Message) (string, string, error) {
	candidates := []string{m.MatchedRecipient, m.ReceivingIdentity}
	candidates = append(candidates, m.EnvelopeRecipients...)
	candidates = append(candidates, m.To...)
	candidates = append(candidates, m.CC...)
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		addresses, err := replyAddresses(m.Channel, []string{candidate})
		if err != nil || len(addresses) != 1 {
			continue
		}
		address := addresses[0]
		if _, err := validateOutboundSender(ctx.AppDB(), pid, m.Channel, address, true); err == nil {
			return address, "", nil
		}
		if m.Channel == channelEmail {
			local, domain, _ := strings.Cut(address, "@")
			if base, _, tagged := strings.Cut(local, "+"); tagged {
				baseAddress := base + "@" + domain
				if _, err := validateOutboundSender(ctx.AppDB(), pid, m.Channel, baseAddress, true); err == nil {
					return baseAddress, address, nil
				}
			}
		}
	}
	return "", "", errors.New("send_message reply: no verified, enabled registered sender matches the original delivery addresses")
}

func prepareEmailReply(ctx *sdk.AppCtx, pid string, m *Message, headers map[string]any, send map[string]any, replyAll bool) error {
	subject := strings.TrimSpace(m.Subject)
	if subject == "" {
		subject = "(no subject)"
	}
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	send["subject"] = subject
	refs := append([]string(nil), m.References...)
	if len(refs) == 0 && m.InReplyTo != "" {
		refs = append(refs, m.InReplyTo)
	}
	if m.MessageIDHeader != "" {
		if !replyMessageIDPattern.MatchString(m.MessageIDHeader) {
			return errors.New("original RFC Message-ID is invalid")
		}
		send["in_reply_to"] = m.MessageIDHeader
		refs = append(refs, m.MessageIDHeader)
	}
	uniqueRefs := []string{}
	seen := map[string]bool{}
	for _, ref := range refs {
		if !replyMessageIDPattern.MatchString(ref) {
			return errors.New("original RFC References contains an invalid Message-ID")
		}
		if !seen[ref] {
			uniqueRefs = append(uniqueRefs, ref)
			seen[ref] = true
		}
	}
	send["references"] = uniqueRefs
	// Explicit allowlist: these identifiers are inert correlation metadata,
	// never authorization or a reason to alter recipients/routing.
	ticketHeaders := map[string]any{}
	for _, name := range []string{"X-Ticket-ID", "X-Ticket-Number"} {
		value, err := replyHeader(headers, name)
		if err != nil {
			return err
		}
		if value != "" && replyTicketIDPattern.MatchString(value) {
			ticketHeaders[name] = value
		}
	}
	send["headers"] = ticketHeaders
	if !replyAll {
		return nil
	}
	// Exclude this project's own mailboxes (including plus aliases) and
	// deduplicate To/Cc. No Bcc list or original raw Cc/Bcc header is used.
	senders, err := dbListSenders(ctx.AppDB(), pid, channelEmail, false)
	if err != nil {
		return err
	}
	owned := map[string]bool{send["from"].(string): true}
	for _, sender := range senders {
		owned[sender.Address] = true
	}
	if address, ok := send["reply_to"].(string); ok {
		owned[address] = true
	}
	isOwned := func(address string) bool {
		local, domain, _ := strings.Cut(address, "@")
		base, _, _ := strings.Cut(local, "+")
		return owned[address] || owned[base+"@"+domain]
	}
	seen = map[string]bool{}
	to := []string{}
	for _, address := range send["to"].([]string) {
		if !isOwned(address) && !seen[address] {
			to = append(to, address)
			seen[address] = true
		}
	}
	ccCandidates := append([]string(nil), m.CC...)
	if m.Direction == "in" {
		ccCandidates = append(append([]string(nil), m.To...), ccCandidates...)
	}
	cc, err := replyAddresses(channelEmail, ccCandidates)
	if err != nil {
		return fmt.Errorf("reply-all recipients: %w", err)
	}
	filteredCC := []string{}
	for _, address := range cc {
		if !isOwned(address) && !seen[address] {
			filteredCC = append(filteredCC, address)
			seen[address] = true
		}
	}
	send["to"], send["cc"] = to, filteredCC
	return nil
}

func replyHeader(headers map[string]any, name string) (string, error) {
	value := ""
	for key, raw := range headers {
		if strings.EqualFold(key, name) {
			candidate, ok := raw.(string)
			if !ok {
				return "", fmt.Errorf("original %s header must be a string", name)
			}
			if candidate == "" {
				continue
			}
			if value != "" && value != candidate {
				return "", fmt.Errorf("original message has conflicting %s headers", name)
			}
			value = candidate
		}
	}
	return value, nil
}

func replyAddresses(channel string, values []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		if strings.ContainsAny(value, "\r\n") {
			return nil, errors.New("address contains a line break")
		}
		addresses := []string{value}
		if channel == channelEmail {
			parsed, err := mail.ParseAddressList(stripScheme(strings.TrimSpace(value)))
			if err != nil {
				return nil, err
			}
			addresses = nil
			for _, address := range parsed {
				addresses = append(addresses, address.Address)
			}
		}
		for _, value := range addresses {
			address, err := normaliseAddress(channel, value)
			if err != nil {
				return nil, err
			}
			if !seen[address] {
				out = append(out, address)
				seen[address] = true
			}
		}
	}
	return out, nil
}
