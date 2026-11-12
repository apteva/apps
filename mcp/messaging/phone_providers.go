package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	sdk "github.com/apteva/app-sdk"
)

// SMS accounts may coexist. The old phone_provider remains the authority for
// unpinned Twilio senders and WhatsApp, preserving existing installations.
func phoneBindings(ctx *sdk.AppCtx, channel string) []*sdk.BoundIntegration {
	out := []*sdk.BoundIntegration{}
	seen := map[int64]bool{}
	add := func(b *sdk.BoundIntegration) {
		if b == nil || seen[b.ConnectionID] {
			return
		}
		c := *b
		if c.AppSlug == "" {
			c.AppSlug = "twilio"
		}
		if c.AppSlug != "twilio" && (channel != channelSMS || c.AppSlug != "telnyx") {
			return
		}
		seen[c.ConnectionID] = true
		out = append(out, &c)
	}
	if channel == channelSMS {
		for _, b := range ctx.IntegrationsFor("sms_provider") {
			add(b)
		}
	}
	add(ctx.IntegrationFor("phone_provider"))
	return out
}

func choosePhoneBinding(ctx *sdk.AppCtx, requested int64, sender *senderRow, channel string) (*sdk.BoundIntegration, error) {
	if sender != nil && sender.ProviderConnectionID != 0 {
		if requested != 0 && requested != sender.ProviderConnectionID {
			return nil, errors.New("connection_id does not match registered phone sender")
		}
		requested = sender.ProviderConnectionID
	}
	bounds := phoneBindings(ctx, channel)
	if requested == 0 && sender != nil && sender.Provider == "twilio" {
		if legacy := ctx.IntegrationFor("phone_provider"); legacy != nil {
			requested = legacy.ConnectionID
		}
	}
	matches := []*sdk.BoundIntegration{}
	for _, b := range bounds {
		if requested != 0 && b.ConnectionID != requested {
			continue
		}
		if sender != nil && sender.Provider != "" && sender.Provider != b.AppSlug {
			continue
		}
		matches = append(matches, b)
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("phone provider is missing or ambiguous; select a bound connection_id for channel=%s", channel)
	}
	return matches[0], nil
}

func phoneSenderOnConnection(ctx *sdk.AppCtx, sender *senderRow, connID int64) bool {
	if sender.Provider != "twilio" {
		return false
	}
	b, err := choosePhoneBinding(ctx, 0, sender, sender.Channel)
	return err == nil && b.ConnectionID == connID
}

func setReceiveOnly(ctx *sdk.AppCtx, pid, channel, address string, receiveOnly *bool) error {
	if receiveOnly == nil {
		return nil
	}
	if channel == channelEmail {
		return errors.New("receive_only is available for phone numbers")
	}
	result, err := ctx.AppDB().Exec(`UPDATE senders SET outbound_enabled=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND channel=? AND address=? AND deleted_at IS NULL`, boolInt(!*receiveOnly), pid, channel, address)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("could not persist receive-only number policy")
	}
	return nil
}

func phoneOwner(ctx *sdk.AppCtx, provider string, connID int64, channel, address string) (*senderRow, error) {
	rows, err := ctx.AppDB().Query(`SELECT `+senderColumns+` FROM senders WHERE provider=? AND channel=? AND address=? AND deleted_at IS NULL AND (provider_connection_id=? OR provider_connection_id=0)`, provider, channel, address, connID)
	if err != nil {
		return nil, err
	}
	var found *senderRow
	for rows.Next() {
		row, err := scanSender(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if row.ProviderConnectionID == 0 && !phoneSenderOnConnection(ctx, row, connID) {
			continue
		}
		if found != nil {
			rows.Close()
			return nil, errors.New("phone destination is assigned to multiple projects")
		}
		found = row
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, errors.New("phone destination does not belong to this provider connection")
	}
	return found, nil
}

func sendViaPhoneProvider(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, in providerSendInput) (string, error) {
	if bound == nil {
		return "", errors.New("no phone_provider bound — install/select a phone provider")
	}
	if bound.AppSlug == "twilio" {
		return sendViaTwilioConnection(ctx, bound, in)
	}
	if bound.AppSlug != "telnyx" || in.Channel != channelSMS {
		return "", errors.New("unsupported phone provider/channel")
	}
	if in.ContentSid != "" {
		return "", errors.New("Twilio ContentSid is not supported by Telnyx")
	}
	payload := map[string]any{"from": in.From, "to": in.To[0], "text": in.BodyText, "use_profile_webhooks": true}
	var urls []string
	for _, a := range in.Attachments {
		if a.MediaURL != "" {
			urls = append(urls, a.MediaURL)
		}
	}
	if len(urls) > 0 {
		payload["media_urls"] = urls
		payload["type"] = "MMS"
	} else {
		payload["type"] = "SMS"
	}
	if cb := phoneWebhookURL(ctx, "/webhooks/telnyx-inbound", in.ProjectID, bound.ConnectionID); cb != "" {
		payload["webhook_url"] = cb
	}
	var response struct {
		ID string `json:"id"`
	}
	if err := telnyxCall(ctx, bound.ConnectionID, "send_sms", payload, &response); err != nil {
		var rejected *telnyxRejected
		if errors.As(err, &rejected) {
			return "", err
		}
		return "", &ambiguousSendError{cause: err}
	}
	if response.ID == "" {
		return "", &ambiguousSendError{cause: errors.New("Telnyx returned no message ID")}
	}
	return response.ID, nil
}

func phoneWebhookURL(ctx *sdk.AppCtx, path, pid string, connID int64) string {
	raw := twilioWebhookURL(ctx, path, pid)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	q := u.Query()
	if path == "/webhooks/telnyx-inbound" {
		q.Del("project_id")
	}
	q.Set("connection_id", fmt.Sprint(connID))
	u.RawQuery = q.Encode()
	return u.String()
}

// Authenticated request data chooses a candidate credential, never ownership.
// All candidates are authorized bindings, including the legacy account.
func verifiedTwilioConnection(ctx *sdk.AppCtx, r *http.Request, form url.Values) (*sdk.BoundIntegration, error) {
	var match *sdk.BoundIntegration
	available := false
	for _, b := range phoneBindings(ctx, channelSMS) {
		if b.AppSlug != "twilio" {
			continue
		}
		if hinted := r.URL.Query().Get("connection_id"); hinted != "" && hinted != fmt.Sprint(b.ConnectionID) {
			continue
		}
		token := phoneCredential(ctx, b.ConnectionID, "auth_token")
		if token != "" {
			available = true
		}
		if token == "" || !verifyTwilioSignature(reconstructPublicURL(r), form, token, r.Header.Get("X-Twilio-Signature")) {
			continue
		}
		account := phoneCredential(ctx, b.ConnectionID, "account_sid")
		if claimed := form.Get("AccountSid"); claimed != "" && account != "" && claimed != account {
			continue
		}
		if match != nil {
			return nil, errors.New("ambiguous Twilio signing account; include connection_id in webhook URL")
		}
		match = b
	}
	if match == nil {
		if !available {
			return nil, errors.New("Twilio credentials unavailable")
		}
		return nil, errors.New("Twilio signature verification failed")
	}
	return match, nil
}

func providerMessageForAccount(ctx *sdk.AppCtx, pid, provider string, connID int64, providerID string) (*Message, error) {
	q := `SELECT id,project_id FROM messages WHERE provider_message_id=? AND direction='out' AND (provider_slug=? AND provider_connection_id=?`
	args := []any{providerID, provider, connID}
	// Historical Twilio rows predate provider columns. Only the original
	// connection may claim them; new accounts never get this fallback.
	if legacy := ctx.IntegrationFor("phone_provider"); provider == "twilio" && legacy != nil && legacy.ConnectionID == connID {
		q += ` OR provider_slug='' AND provider_connection_id=0`
	}
	q += `)`
	if pid != "" {
		q += ` AND project_id=?`
		args = append(args, pid)
	}
	rows, err := ctx.AppDB().Query(q, args...)
	if err != nil {
		return nil, err
	}
	var id int64
	var project string
	count := 0
	for rows.Next() {
		if err := rows.Scan(&id, &project); err != nil {
			rows.Close()
			return nil, err
		}
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}
	if count != 1 {
		return nil, errors.New("provider message account is ambiguous")
	}
	return dbMessageGet(ctx.AppDB(), project, id)
}

func telnyxProfileFromSender(row *senderRow) string {
	var cfg map[string]any
	_ = json.Unmarshal([]byte(row.InboundConfig), &cfg)
	return strArg(cfg, "messaging_profile_id")
}

func phoneInboxOnly(ctx *sdk.AppCtx, m *Message) (bool, error) {
	if m.Channel == channelEmail || len(m.To) != 1 {
		return false, nil
	}
	row, err := dbFindSender(ctx.AppDB(), m.ProjectID, m.Channel, m.To[0])
	if err != nil {
		return false, err
	}
	if row == nil || row.OutboundEnabled {
		return false, nil
	}
	_, err = ctx.AppDB().Exec(`UPDATE messages SET route_status='inbox_only',route_error='',matched_recipient=? WHERE id=? AND project_id=?`, m.To[0], m.ID, m.ProjectID)
	return true, err
}

func twilioPhoneWebhookURL(ctx *sdk.AppCtx, path, pid string, connID int64) string {
	if len(ctx.IntegrationsFor("sms_provider")) > 0 {
		return phoneWebhookURL(ctx, path, pid, connID)
	}
	return twilioWebhookURL(ctx, path, pid)
}
func senderMapWithHealth(ctx *sdk.AppCtx, row *senderRow) map[string]any {
	out := senderRowToMap(row)
	if row.Channel != channelEmail {
		var received string
		_ = ctx.AppDB().QueryRow(`SELECT COALESCE(max(received_at),'') FROM messages WHERE project_id=? AND channel=? AND direction='in' AND to_addrs=?`, row.ProjectID, row.Channel, string(mustJSON([]string{row.Address}))).Scan(&received)
		out["last_received_at"] = received
	}
	return out
}

func phoneCredential(ctx *sdk.AppCtx, connID int64, field string) string {
	if creds, err := ctx.PlatformAPI().GetConnectionCredentials(connID); err == nil && creds != nil {
		if value := creds.Fields[field]; value != "" {
			return value
		}
	}
	if legacy := ctx.IntegrationFor("phone_provider"); legacy != nil && legacy.ConnectionID == connID {
		return lookupConnectionCredential(ctx, connID, field)
	}
	return ""
}

func legacyUnassignedPhoneAllowed(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, channel, address string) bool {
	legacy := ctx.IntegrationFor("phone_provider")
	if legacy == nil || legacy.ConnectionID != bound.ConnectionID {
		return false
	}
	var n int
	err := ctx.AppDB().QueryRow(`SELECT count(*) FROM senders WHERE channel=? AND address=? AND deleted_at IS NULL`, channel, address).Scan(&n)
	return err == nil && n == 0
}
