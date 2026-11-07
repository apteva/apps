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

// Recovery runs every 30s beside normal traffic, so each pass is bounded:
// discovery reads go through the read pool in fixed id windows with a query
// deadline, and writes are short transactions on the serialized writer.
const (
	autoResponseScanWindow   = 1000
	autoResponseReadyBatch   = 50
	autoResponseQueryTimeout = 5 * time.Second
	autoResponsePassBudget   = 2 * time.Second
)

func reconcilePendingAutoResponses(ctx *sdk.AppCtx) error {
	deadline := time.Now().Add(autoResponsePassBudget)
	for {
		more, err := indexAutoResponseBounces(ctx)
		if err != nil {
			return err
		}
		if !more || time.Now().After(deadline) {
			break
		}
	}
	for {
		more, err := markAutoResponseInbound(ctx)
		if err != nil {
			return err
		}
		if !more || time.Now().After(deadline) {
			break
		}
	}
	if err := reconcileReadyAutoResponses(ctx); err != nil {
		return err
	}
	return flushDeliveryCorrections(ctx)
}

func autoResponseCursor(ctx *sdk.AppCtx, qctx context.Context, name, table string) (last, max int64, err error) {
	db := ctx.AppReadDB()
	if err = db.QueryRowContext(qctx, `SELECT last_id FROM auto_response_scan_state WHERE name=?`, name).Scan(&last); err != nil {
		return 0, 0, err
	}
	err = db.QueryRowContext(qctx, `SELECT COALESCE(MAX(id),0) FROM `+table).Scan(&max)
	return last, max, err
}

// feedbackIDHeader extracts the SES feedback ID from "<feedback@domain>".
func feedbackIDHeader(header string) string {
	header = strings.TrimSpace(header)
	at := strings.IndexByte(header, '@')
	if !strings.HasPrefix(header, "<") || at < 2 {
		return ""
	}
	return header[1:at]
}

// autoResponseFeedbackID returns the feedback ID of a bounce that could be an
// SES-forwarded automatic response, or "" for every other bounce.
func autoResponseFeedbackID(stored string) string {
	raw := json.RawMessage(stored)
	var wrapper struct {
		Raw json.RawMessage `json:"raw"`
	}
	_ = json.Unmarshal(raw, &wrapper)
	if len(wrapper.Raw) > 0 {
		raw = wrapper.Raw
	}
	var ev struct {
		Bounce struct {
			Type       string `json:"bounceType"`
			SubType    string `json:"bounceSubType"`
			FeedbackID string `json:"feedbackId"`
		} `json:"bounce"`
	}
	if json.Unmarshal(raw, &ev) != nil || ev.Bounce.Type != "Transient" || ev.Bounce.SubType != "General" {
		return ""
	}
	return ev.Bounce.FeedbackID
}

// Indexes one window of new bounce events. Each event is considered once; a
// candidate whose inbound copy is already stored is marked ready immediately.
func indexAutoResponseBounces(ctx *sdk.AppCtx) (bool, error) {
	qctx, cancel := context.WithTimeout(context.Background(), autoResponseQueryTimeout)
	defer cancel()
	last, max, err := autoResponseCursor(ctx, qctx, "bounces", "delivery_events")
	if err != nil || last >= max {
		return false, err
	}
	end := min(last+autoResponseScanWindow, max)
	rows, err := ctx.AppReadDB().QueryContext(qctx, `SELECT d.id,m.project_id,m.id,d.raw FROM delivery_events d JOIN messages m ON m.id=d.message_id
 WHERE d.id>? AND d.id<=? AND d.kind='bounced' AND m.direction='out' AND m.channel='email'`, last, end)
	if err != nil {
		return false, err
	}
	type candidate struct {
		event, message int64
		pid, feedback  string
		ready          bool
	}
	var found []candidate
	for rows.Next() {
		var c candidate
		var raw string
		if err = rows.Scan(&c.event, &c.pid, &c.message, &raw); err != nil {
			rows.Close()
			return false, err
		}
		if c.feedback = autoResponseFeedbackID(raw); c.feedback != "" {
			found = append(found, c)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for i := range found {
		c := &found[i]
		var one int
		err = ctx.AppReadDB().QueryRowContext(qctx, `SELECT 1 FROM messages WHERE project_id=? AND message_id_header>=? AND message_id_header<? AND direction='in' AND provider_slug='aws-ses' LIMIT 1`, c.pid, "<"+c.feedback+"@", "<"+c.feedback+"A").Scan(&one)
		if err != nil && err != sql.ErrNoRows {
			return false, err
		}
		c.ready = err == nil
	}
	tx, err := ctx.AppDB().BeginTx(qctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	for _, c := range found {
		if _, err = tx.Exec(`INSERT OR IGNORE INTO auto_response_candidates(event_id,project_id,message_id,feedback_id,ready) VALUES(?,?,?,?,?)`, c.event, c.pid, c.message, c.feedback, c.ready); err != nil {
			return false, err
		}
	}
	if _, err = tx.Exec(`UPDATE auto_response_scan_state SET last_id=? WHERE name='bounces' AND last_id=?`, end, last); err != nil {
		return false, err
	}
	return end < max, tx.Commit()
}

// Marks candidates ready when their SES inbound copy arrives after the bounce.
func markAutoResponseInbound(ctx *sdk.AppCtx) (bool, error) {
	qctx, cancel := context.WithTimeout(context.Background(), autoResponseQueryTimeout)
	defer cancel()
	last, max, err := autoResponseCursor(ctx, qctx, "inbound", "messages")
	if err != nil || last >= max {
		return false, err
	}
	end := min(last+autoResponseScanWindow, max)
	rows, err := ctx.AppReadDB().QueryContext(qctx, `SELECT project_id,COALESCE(message_id_header,'') FROM messages
 WHERE id>? AND id<=? AND direction='in' AND channel='email' AND provider_slug='aws-ses'`, last, end)
	if err != nil {
		return false, err
	}
	type arrival struct{ pid, feedback string }
	var arrivals []arrival
	for rows.Next() {
		var a arrival
		var header string
		if err = rows.Scan(&a.pid, &header); err != nil {
			rows.Close()
			return false, err
		}
		if a.feedback = feedbackIDHeader(header); a.feedback != "" {
			arrivals = append(arrivals, a)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	tx, err := ctx.AppDB().BeginTx(qctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	for _, a := range arrivals {
		if _, err = tx.Exec(`UPDATE auto_response_candidates SET ready=1 WHERE project_id=? AND feedback_id=?`, a.pid, a.feedback); err != nil {
			return false, err
		}
	}
	if _, err = tx.Exec(`UPDATE auto_response_scan_state SET last_id=? WHERE name='inbound' AND last_id=?`, end, last); err != nil {
		return false, err
	}
	return end < max, tx.Commit()
}

// A ready candidate is reconsidered once; it becomes ready again only if
// another inbound copy with the same feedback ID arrives.
func reconcileReadyAutoResponses(ctx *sdk.AppCtx) error {
	qctx, cancel := context.WithTimeout(context.Background(), autoResponseQueryTimeout)
	rows, err := ctx.AppReadDB().QueryContext(qctx, `SELECT DISTINCT project_id,message_id FROM auto_response_candidates WHERE ready=1 ORDER BY message_id LIMIT ?`, autoResponseReadyBatch)
	if err != nil {
		cancel()
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
			cancel()
			return err
		}
		targets = append(targets, t)
	}
	err = rows.Err()
	rows.Close()
	cancel()
	if err != nil {
		return err
	}
	for _, t := range targets {
		if err = reconcileMessageAutoResponses(ctx, t.pid, t.id); err != nil {
			return err
		}
		if _, err = ctx.AppDB().Exec(`UPDATE auto_response_candidates SET ready=0 WHERE message_id=? AND ready=1`, t.id); err != nil {
			return err
		}
	}
	return nil
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
