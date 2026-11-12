package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type telnyxRejected struct{ detail string }

func (e *telnyxRejected) Error() string { return "Telnyx rejected request: " + e.detail }

func telnyxCall(ctx *sdk.AppCtx, connID int64, tool string, input map[string]any, out any) error {
	result, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, input)
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("Telnyx returned no result")
	}
	if !result.Success {
		return &telnyxRejected{detail: truncateResData(result)}
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(result.Data, &envelope); err != nil {
		return fmt.Errorf("Telnyx response: %w", err)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return errors.New("Telnyx response has no data")
	}
	if out != nil {
		return json.Unmarshal(envelope.Data, out)
	}
	return nil
}

type telnyxNumber struct {
	ID                 string `json:"id"`
	PhoneNumber        string `json:"phone_number"`
	CountryCode        string `json:"country_code"`
	MessagingProfileID string `json:"messaging_profile_id"`
	Status             string `json:"status"`
}

// Traverse complete inventories before reconciling. A failed/partial listing
// never marks missing numbers deleted.
func telnyxListNumbers(ctx *sdk.AppCtx, connID int64, tool string, filter map[string]any) ([]telnyxNumber, error) {
	out := []telnyxNumber{}
	for page := 1; page <= 100; page++ {
		input := map[string]any{"page[number]": page, "page[size]": 100}
		for k, v := range filter {
			input[k] = v
		}
		result, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, input)
		if err != nil {
			return nil, err
		}
		if result == nil || !result.Success {
			return nil, fmt.Errorf("Telnyx inventory: %s", truncateResData(result))
		}
		var envelope struct {
			Data []telnyxNumber `json:"data"`
			Meta struct {
				TotalPages int `json:"total_pages"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(result.Data, &envelope); err != nil {
			return nil, err
		}
		if envelope.Data == nil {
			return nil, errors.New("Telnyx inventory missing data array")
		}
		out = append(out, envelope.Data...)
		if (envelope.Meta.TotalPages == 0 && len(envelope.Data) < 100) || (envelope.Meta.TotalPages > 0 && page >= envelope.Meta.TotalPages) {
			return out, nil
		}
	}
	return nil, errors.New("Telnyx inventory exceeds pagination limit")
}

type telnyxMessagingSettings struct {
	MessagingProfileID string `json:"messaging_profile_id"`
	SMS                struct {
		Enabled *bool `json:"enabled"`
	} `json:"sms"`
}
type telnyxProfile struct {
	WebhookAPIVersion string `json:"webhook_api_version"`
	ID                string `json:"id"`
	WebhookURL        string `json:"webhook_url"`
	Enabled           *bool  `json:"enabled"`
}

func (a *App) sendersCreateTelnyx(ctx *sdk.AppCtx, pid, addr string, req sendersCreateReq, bound *sdk.BoundIntegration) (*sendersCreateResp, error) {
	numbers, err := telnyxListNumbers(ctx, bound.ConnectionID, "list_phone_numbers", map[string]any{"filter[phone_number]": addr})
	if err != nil {
		return nil, err
	}
	var number *telnyxNumber
	for i := range numbers {
		if numbers[i].PhoneNumber == addr {
			number = &numbers[i]
			break
		}
	}
	if number == nil || number.ID == "" {
		return nil, errors.New("number is not owned by the selected Telnyx connection")
	}
	var otherOwners int
	if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM senders WHERE provider='telnyx' AND provider_connection_id=? AND address=? AND channel='sms' AND project_id<>? AND deleted_at IS NULL`, bound.ConnectionID, addr, pid).Scan(&otherOwners); err != nil {
		return nil, err
	}
	if otherOwners > 0 {
		return nil, errors.New("Telnyx number is already assigned to another project")
	}
	var settings telnyxMessagingSettings
	if err := telnyxCall(ctx, bound.ConnectionID, "get_phone_number_messaging_settings", map[string]any{"id": number.ID}, &settings); err != nil {
		return nil, err
	}
	if settings.SMS.Enabled != nil && !*settings.SMS.Enabled {
		return nil, errors.New("SMS is not enabled for this Telnyx number")
	}
	profileID := settings.MessagingProfileID
	if req.MessagingProfileID != "" {
		profileID = req.MessagingProfileID
	}
	callback := phoneWebhookURL(ctx, "/webhooks/telnyx-inbound", pid, bound.ConnectionID)
	mode := strings.ToLower(strings.TrimSpace(req.Inbound))
	doInbound := callback != ""
	switch mode {
	case "", "auto":
	case "true", "yes", "1":
		if callback == "" {
			return nil, errors.New("inbound=true requires a platform Public URL")
		}
	case "false", "no", "0":
		doInbound = false
	default:
		return nil, errors.New("inbound must be auto, true or false")
	}
	if doInbound && lookupConnectionCredential(ctx, bound.ConnectionID, "public_key") == "" {
		return nil, errors.New("Telnyx webhook public_key is required before enabling inbound SMS")
	}
	if profileID == "" {
		if !doInbound {
			return nil, errors.New("select a messaging_profile_id for this number")
		}
		if len(number.CountryCode) != 2 {
			return nil, errors.New("provider did not supply a country code; create a messaging profile and select its ID")
		}
		var created telnyxProfile
		if err := telnyxCall(ctx, bound.ConnectionID, "create_messaging_profile", map[string]any{"name": "Apteva " + pid + " " + addr, "enabled": true, "whitelisted_destinations": []string{number.CountryCode}, "webhook_url": callback, "webhook_api_version": "2"}, &created); err != nil {
			return nil, err
		}
		if created.ID == "" {
			return nil, errors.New("Telnyx created no profile ID")
		}
		profileID = created.ID
	}
	var profile telnyxProfile
	if err := telnyxCall(ctx, bound.ConnectionID, "get_messaging_profile", map[string]any{"id": profileID}, &profile); err != nil {
		return nil, err
	}
	if profile.ID != profileID {
		return nil, errors.New("Telnyx profile ownership response did not match requested profile")
	}
	if doInbound && profile.Enabled != nil && !*profile.Enabled {
		return nil, errors.New("Telnyx messaging profile is disabled")
	}
	if doInbound && (profile.WebhookURL != callback || (profile.WebhookAPIVersion != "" && profile.WebhookAPIVersion != "2")) {
		members, err := telnyxListNumbers(ctx, bound.ConnectionID, "list_messaging_profile_phone_numbers", map[string]any{"id": profileID})
		if err != nil {
			return nil, err
		}
		for _, member := range members {
			if member.PhoneNumber != addr && !req.AllowSharedProfile {
				return nil, errors.New("messaging profile is shared; select a dedicated profile or explicitly set allow_shared_profile=true")
			}
		}
		if err := telnyxCall(ctx, bound.ConnectionID, "update_messaging_profile", map[string]any{"id": profileID, "webhook_url": callback, "webhook_api_version": "2"}, &profile); err != nil {
			return nil, err
		}
		if profile.WebhookURL != callback {
			return nil, errors.New("Telnyx did not confirm the requested messaging webhook")
		}
	}
	// Only messaging settings are changed; voice application/connection fields
	// and the profile's unrelated sending configuration are never rewritten.
	if settings.MessagingProfileID != profileID {
		if err := telnyxCall(ctx, bound.ConnectionID, "update_phone_number_messaging_settings", map[string]any{"id": number.ID, "messaging_profile_id": profileID}, &settings); err != nil {
			return nil, err
		}
		if settings.MessagingProfileID != profileID {
			return nil, errors.New("Telnyx did not confirm the number profile assignment")
		}
	}
	config := map[string]any{"messaging_profile_id": profileID, "webhook_url": profile.WebhookURL}
	if _, err := dbUpsertSender(ctx.AppDB(), &senderUpsert{ProjectID: pid, Channel: channelSMS, Address: addr, Kind: "phone", DisplayName: req.DisplayName, Provider: "telnyx", ProviderConnectionID: bound.ConnectionID, ProviderIdentityID: number.ID, Verified: true, VerificationStatus: "verified", SendingEnabled: true, InboundBootstrapped: doInbound, InboundConfig: string(mustJSON(config)), MarkSyncedNow: true}); err != nil {
		return nil, err
	}
	if err := setReceiveOnly(ctx, pid, channelSMS, addr, req.ReceiveOnly); err != nil {
		return nil, err
	}
	return &sendersCreateResp{Address: addr, Kind: "phone", Inbound: &sendersCreateInbound{Bootstrapped: doInbound, WebhookURL: profile.WebhookURL}, Steps: []bootstrapStep{{Step: "telnyx_number_adopted", OK: true, Detail: "connection=" + fmt.Sprint(bound.ConnectionID) + " profile=" + profileID}}, NextStep: "Telnyx number registered; receive-only policy is preserved during refresh."}, nil
}

func (a *App) refreshTelnyxNumbers(ctx *sdk.AppCtx, pid string, bound *sdk.BoundIntegration) error {
	known, err := dbListSenders(ctx.AppDB(), pid, channelSMS, false)
	if err != nil {
		return err
	}
	relevant := false
	for _, s := range known {
		if s.Provider == "telnyx" && s.ProviderConnectionID == bound.ConnectionID {
			relevant = true
		}
	}
	if !relevant {
		return nil
	}
	numbers, err := telnyxListNumbers(ctx, bound.ConnectionID, "list_phone_numbers", nil)
	if err != nil {
		return err
	}
	byID := map[string]telnyxNumber{}
	for _, n := range numbers {
		byID[n.ID] = n
	}
	for _, s := range known {
		if s.Provider != "telnyx" || s.ProviderConnectionID != bound.ConnectionID {
			continue
		}
		n, ok := byID[s.ProviderIdentityID]
		if !ok || n.PhoneNumber != s.Address {
			if err := dbSoftDeleteSender(ctx.AppDB(), pid, channelSMS, s.Address); err != nil {
				return err
			}
			continue
		}
		var settings telnyxMessagingSettings
		if err := telnyxCall(ctx, bound.ConnectionID, "get_phone_number_messaging_settings", map[string]any{"id": n.ID}, &settings); err != nil {
			return err
		}
		var profile telnyxProfile
		if err := telnyxCall(ctx, bound.ConnectionID, "get_messaging_profile", map[string]any{"id": settings.MessagingProfileID}, &profile); err != nil {
			return err
		}
		ready := profile.WebhookURL == phoneWebhookURL(ctx, "/webhooks/telnyx-inbound", pid, bound.ConnectionID) && telnyxProfileFromSender(s) == settings.MessagingProfileID && (profile.Enabled == nil || *profile.Enabled) && (profile.WebhookAPIVersion == "" || profile.WebhookAPIVersion == "2") && lookupConnectionCredential(ctx, bound.ConnectionID, "public_key") != ""
		enabled := settings.SMS.Enabled == nil || *settings.SMS.Enabled
		if _, err := dbUpsertSender(ctx.AppDB(), &senderUpsert{ProjectID: pid, Channel: channelSMS, Address: s.Address, Kind: s.Kind, Provider: "telnyx", ProviderConnectionID: bound.ConnectionID, ProviderIdentityID: n.ID, Verified: enabled, VerificationStatus: "verified", SendingEnabled: enabled, InboundBootstrapped: ready, InboundConfig: string(mustJSON(map[string]any{"messaging_profile_id": settings.MessagingProfileID, "webhook_url": profile.WebhookURL})), MarkSyncedNow: true}); err != nil {
			return err
		}
	}
	return nil
}

type telnyxMessageEvent struct {
	Data struct {
		ID         string `json:"id"`
		EventType  string `json:"event_type"`
		OccurredAt string `json:"occurred_at"`
		Payload    struct {
			ID                 string `json:"id"`
			Direction          string `json:"direction"`
			Text               string `json:"text"`
			ReceivedAt         string `json:"received_at"`
			MessagingProfileID string `json:"messaging_profile_id"`
			From               struct {
				PhoneNumber string `json:"phone_number"`
			} `json:"from"`
			To []struct {
				PhoneNumber string `json:"phone_number"`
				Status      string `json:"status"`
			} `json:"to"`
			Media []struct {
				URL         string `json:"url"`
				ContentType string `json:"content_type"`
			} `json:"media"`
		} `json:"payload"`
	} `json:"data"`
}

func verifyTelnyxWebhook(raw []byte, timestamp, signature, key string, now time.Time) bool {
	stamp, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || stamp < now.Unix()-300 || stamp > now.Unix()+300 {
		return false
	}
	pub := decodeTelnyxWebhookValue(key, ed25519.PublicKeySize)
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig := decodeTelnyxWebhookValue(signature, ed25519.SignatureSize)
	if len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, append([]byte(timestamp+"|"), raw...), sig)
}

func (a *App) handleTelnyxWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, 405, "POST only")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInboundRequestBytes))
	if err != nil {
		httpErr(w, 413, "invalid webhook body")
		return
	}
	var ev telnyxMessageEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		httpErr(w, 400, "invalid webhook JSON")
		return
	}
	payload := ev.Data.Payload
	if ev.Data.ID == "" || payload.ID == "" {
		httpErr(w, 400, "event and message ID required")
		return
	}
	var bound *sdk.BoundIntegration
	var owner *senderRow
	var outbound *Message
	for _, b := range phoneBindings(globalCtx, channelSMS) {
		if b.AppSlug != "telnyx" {
			continue
		}
		if hint := r.URL.Query().Get("connection_id"); hint != "" && hint != fmt.Sprint(b.ConnectionID) {
			continue
		}
		if !verifyTelnyxWebhook(raw, r.Header.Get("telnyx-timestamp"), r.Header.Get("telnyx-signature-ed25519"), lookupConnectionCredential(globalCtx, b.ConnectionID, "public_key"), time.Now()) {
			continue
		}
		if ev.Data.EventType == "message.received" {
			if payload.Direction != "inbound" || len(payload.To) != 1 {
				continue
			}
			candidate, err := phoneOwner(globalCtx, "telnyx", b.ConnectionID, channelSMS, payload.To[0].PhoneNumber)
			if err != nil || telnyxProfileFromSender(candidate) != payload.MessagingProfileID || payload.MessagingProfileID == "" {
				continue
			}
			if bound != nil {
				httpErr(w, 403, "ambiguous Telnyx ownership")
				return
			}
			bound, owner = b, candidate
		} else if ev.Data.EventType == "message.sent" || ev.Data.EventType == "message.finalized" {
			if payload.Direction != "outbound" {
				continue
			}
			candidate, err := providerMessageForAccount(globalCtx, "", "telnyx", b.ConnectionID, payload.ID)
			if err != nil {
				httpErr(w, 500, "provider lookup failed")
				return
			}
			// Before the send API response is stored, authenticate the callback via
			// the registered From number/profile and queue its delivery events.
			row, ownerErr := phoneOwner(globalCtx, "telnyx", b.ConnectionID, channelSMS, payload.From.PhoneNumber)
			if candidate == nil && (ownerErr != nil || telnyxProfileFromSender(row) != payload.MessagingProfileID) {
				continue
			}
			if bound != nil {
				httpErr(w, 403, "ambiguous Telnyx ownership")
				return
			}
			bound, owner, outbound = b, row, candidate
		} else {
			bound = b
			break
		}
	}
	if bound == nil {
		httpErr(w, 403, "Telnyx signature or number/profile ownership failed")
		return
	}
	pid := ""
	if owner != nil {
		pid = owner.ProjectID
	}
	if outbound != nil {
		pid = outbound.ProjectID
	}
	if requested, err := resolveProjectFromRequest(r); err == nil && pid != "" && requested != pid {
		httpErr(w, 403, "destination project mismatch")
		return
	}
	if ev.Data.EventType == "message.sent" || ev.Data.EventType == "message.finalized" {
		for _, recipient := range payload.To {
			kind := mapTwilioMessageStatusToEventKind(recipient.Status)
			if recipient.Status == "delivery_failed" || recipient.Status == "sending_failed" {
				kind = "failed"
			}
			if kind == "" {
				continue
			}
			event := providerEvent{ConnectionID: bound.ConnectionID, Provider: "telnyx", ProviderEventID: fmt.Sprintf("telnyx:%d:%s:%s", bound.ConnectionID, ev.Data.ID, recipient.PhoneNumber), ProviderMessageID: payload.ID, Kind: kind, Reason: recipient.Status, Recipient: recipient.PhoneNumber, Raw: raw, OccurredAt: ev.Data.OccurredAt}
			if outbound == nil {
				err = queueProviderEvents(globalCtx, pid, "", []providerEvent{event})
			} else {
				_, err = persistAndEmitProviderEvent(globalCtx, outbound, event)
			}
			if err != nil {
				httpErr(w, 500, "could not persist delivery event")
				return
			}
		}
		httpJSON(w, map[string]any{"ok": true})
		return
	}
	if ev.Data.EventType != "message.received" {
		httpJSON(w, map[string]any{"ok": true, "skipped": true})
		return
	}
	if payload.From.PhoneNumber == "" || len(payload.From.PhoneNumber) > 256 {
		httpErr(w, 400, "invalid SMS sender")
		return
	}
	occurred := payload.ReceivedAt
	if occurred == "" {
		occurred = ev.Data.OccurredAt
	}
	parsed, err := time.Parse(time.RFC3339Nano, occurred)
	if err != nil {
		httpErr(w, 400, "invalid message timestamp")
		return
	}
	occurred = parsed.UTC().Format(time.RFC3339Nano)
	attachments := []providerAttachment{}
	for i, m := range payload.Media {
		attachments = append(attachments, providerAttachment{MessageAttachment: MessageAttachment{URL: m.URL, Filename: filenameFromURL(m.URL), ContentType: m.ContentType, Source: "telnyx", Disposition: "attachment", ProviderRef: fmt.Sprintf("%s:%d", payload.ID, i)}})
	}
	_, err = persistInbound(globalCtx, pid, "telnyx", attachments, `INSERT OR IGNORE INTO messages(project_id,channel,direction,from_addr,to_addrs,body_text,status,route_status,provider_message_id,provider_slug,provider_connection_id,headers,received_at,created_at,last_event_at,receiving_identity) VALUES(?,'sms','in',?,? ,?,'received','pending',?,'telnyx',?,?,?,?,?,?)`, pid, payload.From.PhoneNumber, string(mustJSON([]string{payload.To[0].PhoneNumber})), payload.Text, payload.ID, bound.ConnectionID, string(mustJSON(map[string]string{"X-Telnyx-Profile-ID": payload.MessagingProfileID})), occurred, occurred, occurred, payload.To[0].PhoneNumber)
	if err != nil {
		httpErr(w, 500, "could not persist inbound SMS")
		return
	}
	httpJSON(w, map[string]any{"ok": true})
}

func decodeTelnyxWebhookValue(value string, size int) []byte {
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		if raw, err := encoding.DecodeString(value); err == nil && len(raw) == size {
			return raw
		}
	}
	if raw, err := hex.DecodeString(value); err == nil && len(raw) == size {
		return raw
	}
	return nil
}
