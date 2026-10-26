package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// SES forwards auto-responses using the feedback ID as the RFC Message-ID.
// Transient/General alone is not evidence: require an authenticated SES inbound
// copy, exact threading/feedback/ownership, and delivery to the same recipient.
func confirmedAutoResponse(tx *sql.Tx, msg *Message, ev providerEvent) (int64, error) {
	if msg.Channel != channelEmail || msg.Direction != "out" || ev.Provider != "aws-ses" || ev.ProviderMessageID != msg.ProviderMessageID || ev.Kind != "bounced" || ev.Permanent || ev.BounceType != "Transient" || ev.BounceSubType != "General" {
		return 0, nil
	}
	var raw struct {
		Bounce struct {
			FeedbackID string `json:"feedbackId"`
			Recipients []struct {
				Address    string `json:"emailAddress"`
				Action     string `json:"action"`
				Status     string `json:"status"`
				Diagnostic string `json:"diagnosticCode"`
			} `json:"bouncedRecipients"`
		} `json:"bounce"`
	}
	if json.Unmarshal(ev.Raw, &raw) != nil || raw.Bounce.FeedbackID == "" || len(raw.Bounce.Recipients) != 1 {
		return 0, nil
	}
	r := raw.Bounce.Recipients[0]
	// A delivery-status report remains a failure even if an automatic sender
	// adds Auto-Submitted. These acknowledgements contain no DSN failure fields.
	if r.Action != "" || r.Status != "" || r.Diagnostic != "" {
		return 0, nil
	}
	recipient := canonicalAddrForChannel(channelEmail, ev.Recipient)
	delivered := false
	rows, err := tx.Query(`SELECT recipient FROM delivery_events WHERE message_id=? AND kind='delivered'`, msg.ID)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var addr string
		if err = rows.Scan(&addr); err != nil {
			rows.Close()
			return 0, err
		}
		if canonicalAddrForChannel(channelEmail, addr) == recipient {
			delivered = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || !delivered {
		return 0, err
	}
	// Indexed by project and RFC Message-ID. Some auto responders omit
	// In-Reply-To, so exact SES feedback identity is the required correlation.
	rows, err = tx.Query(`SELECT id,from_addr,message_id_header,headers,verdicts,receiving_identity,envelope_recipients,COALESCE(in_reply_to,'') FROM messages
 WHERE project_id=? AND direction='in' AND channel='email' AND provider_slug='aws-ses' AND message_id_header>=? AND message_id_header<?`, msg.ProjectID, "<"+raw.Bounce.FeedbackID+"@", "<"+raw.Bounce.FeedbackID+"A")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var from, mid, headers, verdicts, identity, envelope, inReplyTo string
		if err = rows.Scan(&id, &from, &mid, &headers, &verdicts, &identity, &envelope, &inReplyTo); err != nil {
			return 0, err
		}
		if inReplyTo != "" && inReplyTo != msg.MessageIDHeader {
			continue
		}
		address := canonicalAddrForChannel(channelEmail, from)
		at := strings.LastIndexByte(address, '@')
		if at < 0 || address[:at] != "mailer-daemon" {
			continue
		}
		domain := address[at+1:]
		if domain != "amazonses.com" && !strings.HasSuffix(domain, ".amazonses.com") {
			continue
		}
		if strings.TrimSpace(mid) != "<"+raw.Bounce.FeedbackID+"@"+domain+">" {
			continue
		}
		var h map[string]string
		var v map[string]string
		var recipients []string
		if json.Unmarshal([]byte(headers), &h) != nil || json.Unmarshal([]byte(verdicts), &v) != nil || json.Unmarshal([]byte(envelope), &recipients) != nil {
			continue
		}
		auto, contentType := "", ""
		for k, val := range h {
			if strings.EqualFold(k, "Auto-Submitted") {
				auto = strings.TrimSpace(strings.SplitN(val, ";", 2)[0])
			}
			if strings.EqualFold(k, "Content-Type") {
				contentType = strings.ToLower(val)
			}
		}
		if !strings.EqualFold(auto, "auto-replied") || strings.Contains(contentType, "multipart/report") || strings.Contains(contentType, "delivery-status") {
			continue
		}
		if v["dkim"] != "PASS" || v["dmarc"] != "PASS" || v["virus"] != "PASS" {
			continue
		}
		sender := canonicalAddrForChannel(channelEmail, msg.From)
		if canonicalAddrForChannel(channelEmail, identity) != sender {
			continue
		}
		for _, to := range recipients {
			if canonicalAddrForChannel(channelEmail, to) == sender {
				return id, nil
			}
		}
	}
	return 0, rows.Err()
}

// Called in the provider transaction, and by inbound processing/recovery. This
// makes either arrival order and retries converge without changing raw events.
func reconcileAutoResponsesTx(tx *sql.Tx, msg *Message) (bool, error) {
	rows, err := tx.Query(`SELECT id,COALESCE(provider_event_id,''),recipient,raw,COALESCE(occurred_at,'') FROM delivery_events d
 WHERE message_id=? AND kind='bounced' AND NOT EXISTS(SELECT 1 FROM delivery_event_interpretations i WHERE i.event_id=d.id)`, msg.ID)
	if err != nil {
		return false, err
	}
	type candidate struct {
		id                                int64
		eventID, recipient, raw, occurred string
	}
	var pending []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.eventID, &c.recipient, &c.raw, &c.occurred); err != nil {
			rows.Close()
			return false, err
		}
		pending = append(pending, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	changed := false
	for _, c := range pending {
		raw := json.RawMessage(c.raw)
		var wrapper struct {
			Raw json.RawMessage `json:"raw"`
		}
		_ = json.Unmarshal(raw, &wrapper)
		if len(wrapper.Raw) > 0 {
			raw = wrapper.Raw
		}
		events, err := parseSESProviderEvents(string(raw))
		if err != nil {
			continue
		}
		for _, ev := range events {
			if canonicalAddrForChannel(msg.Channel, ev.Recipient) != canonicalAddrForChannel(msg.Channel, c.recipient) {
				continue
			}
			inbound, err := confirmedAutoResponse(tx, msg, ev)
			if err != nil {
				return false, err
			}
			if inbound == 0 {
				continue
			}
			if _, err = tx.Exec(`INSERT INTO delivery_event_interpretations(event_id,kind,inbound_message_id) VALUES(?,'auto_replied',?)`, c.id, inbound); err != nil {
				return false, err
			}
			originalID := c.eventID
			if originalID == "" {
				originalID = fmt.Sprintf("legacy:%d", c.id)
			}
			eventID := "auto-response:" + originalID
			payload := map[string]any{"event_id": eventID, "kind": "auto_replied", "provider_kind": "bounced", "reclassified_event_id": originalID, "message_id": msg.ID, "channel": msg.Channel, "recipient": canonicalAddrForChannel(msg.Channel, c.recipient), "occurred_at": c.occurred, "auto_reply_message_id": inbound, "reason": "confirmed SES automatic response", "permanent": false}
			if _, err = tx.Exec(`INSERT OR IGNORE INTO delivery_correction_outbox(event_id,project_id,payload) VALUES(?,?,?)`, eventID, msg.ProjectID, string(mustJSON(payload))); err != nil {
				return false, err
			}
			changed = true
			break
		}
	}
	return changed, nil
}

// Only a confirmed reinterpretation may demote bounced. Unrelated hard failures,
// complaints and other recipients still participate in the normal status rank.
func rebuildDeliverySummaryTx(tx *sql.Tx, msg *Message) error {
	rows, err := tx.Query(`SELECT effective_kind,COALESCE(recipient,''),COALESCE(occurred_at,'') FROM effective_delivery_events WHERE message_id=? ORDER BY julianday(occurred_at),id`, msg.ID)
	if err != nil {
		return err
	}
	type outcome struct{ status, at string }
	outcomes := map[string]outcome{}
	var status string
	if err := tx.QueryRow(`SELECT status FROM messages WHERE id=?`, msg.ID).Scan(&status); err != nil {
		rows.Close()
		return err
	}
	if status == "bounced" {
		status = "sent"
	}
	for rows.Next() {
		var kind, recipient, at string
		if err = rows.Scan(&kind, &recipient, &at); err != nil {
			rows.Close()
			return err
		}
		next := mapSESKindToStatus(kind)
		if shouldPromoteMessageStatus(status, next) {
			status = next
		}
		recipient = canonicalAddrForChannel(msg.Channel, recipient)
		prior := outcomes[recipient]
		if recipient != "" && shouldPromoteMessageStatus(prior.status, next) {
			outcomes[recipient] = outcome{next, at}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM recipient_delivery_status WHERE message_id=?`, msg.ID); err != nil {
		return err
	}
	for addr, o := range outcomes {
		if _, err = tx.Exec(`INSERT INTO recipient_delivery_status(message_id,recipient,status,occurred_at) VALUES(?,?,?,?)`, msg.ID, addr, o.status, o.at); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`UPDATE messages SET status=? WHERE id=?`, status, msg.ID)
	return err
}

func reconcileMessageAutoResponses(ctx *sdk.AppCtx, pid string, id int64) error {
	msg, err := dbMessageGet(ctx.AppDB(), pid, id)
	if err != nil || msg == nil {
		return err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changed, err := reconcileAutoResponsesTx(tx, msg)
	if err != nil {
		return err
	}
	if changed {
		if err = rebuildDeliverySummaryTx(tx, msg); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func reconcilePendingAutoResponses(ctx *sdk.AppCtx) error {
	rows, err := ctx.AppDB().Query(`SELECT DISTINCT m.project_id,m.id FROM messages m JOIN delivery_events d ON d.message_id=m.id
 WHERE m.direction='out' AND m.channel='email' AND d.kind='bounced'
 AND NOT EXISTS(SELECT 1 FROM delivery_event_interpretations i WHERE i.event_id=d.id)
 AND json_valid(d.raw) AND EXISTS(SELECT 1 FROM messages r WHERE r.project_id=m.project_id AND r.provider_slug='aws-ses' AND r.direction='in' AND r.message_id_header LIKE '<' || COALESCE(json_extract(d.raw,'$.raw.bounce.feedbackId'),json_extract(d.raw,'$.bounce.feedbackId')) || '@%')`)
	if err != nil {
		return err
	}
	type target struct {
		pid string
		id  int64
	}
	var targets []target
	for rows.Next() {
		var t target
		if err = rows.Scan(&t.pid, &t.id); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, t := range targets {
		if err = reconcileMessageAutoResponses(ctx, t.pid, t.id); err != nil {
			return err
		}
	}
	return flushDeliveryCorrections(ctx)
}

func flushDeliveryCorrections(ctx *sdk.AppCtx) error {
	rows, err := ctx.AppDB().Query(`SELECT event_id,project_id,payload FROM delivery_correction_outbox WHERE delivered_at IS NULL ORDER BY event_id LIMIT 100`)
	if err != nil {
		return err
	}
	type pending struct{ id, pid, body string }
	var batch []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.pid, &p.body); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range batch {
		var payload map[string]any
		if err = json.Unmarshal([]byte(p.body), &payload); err != nil {
			return err
		}
		cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = ctx.EmitWithProjectAck(cancelCtx, "message.event", p.pid, payload)
		cancel()
		if err != nil {
			return err
		}
		if _, err = ctx.AppDB().Exec(`UPDATE delivery_correction_outbox SET delivered_at=CURRENT_TIMESTAMP WHERE event_id=?`, p.id); err != nil {
			return err
		}
	}
	return nil
}
