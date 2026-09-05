package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"time"
)

func messagingInstallID(ctx *sdk.AppCtx) int64 {
	if binding := messagingBound(ctx); binding != nil {
		return binding.InstallID
	}
	return 0
}
func queueCRMEvent(tx *sql.Tx, pid, topic string, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO crm_event_outbox(project_id,topic,payload) VALUES(?,?,?)`, pid, topic, string(raw))
	return err
}

// Provider calls happen before the transaction. All local ingestion writes and
// their business events commit together; retries see either the whole message or none.
func ingestInbound(ctx *sdk.AppCtx, pid string, body inboundPayload) (map[string]any, error) {
	if ctx == nil {
		return nil, errors.New("crm context not initialized")
	}
	if body.Channel != "email" && body.Channel != "sms" && body.Channel != "whatsapp" {
		return nil, errors.New("invalid inbound channel")
	}
	body.From = canonicalAddress(body.Channel, body.From)
	if body.From == "" {
		return nil, errors.New("from required")
	}
	if body.ReceivedAt == "" {
		body.ReceivedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if _, err := time.Parse(time.RFC3339Nano, body.ReceivedAt); err != nil {
		return nil, errors.New("received_at must be RFC3339")
	}
	db := ctx.AppDB()
	sourceID := messagingInstallID(ctx)
	rawChannels := []any{map[string]any{"kind": contactChannelKindFor(body.Channel), "value": body.From, "is_primary": true}}
	channels, err := parseChannelInputs(rawChannels)
	if err != nil {
		return nil, err
	}
	contact, fuzzy, err := matchInboundContact(db, pid, body.Channel, body.From)
	if err != nil {
		return nil, err
	}
	automated, reason := isAutomatedSender(body.Channel, body.From, body.Headers)
	reviewAutomated := ctx.Config().Get("automated_inbound_policy") == "review_new"
	if automated && contact == nil && !reviewAutomated {
		return map[string]any{"ok": true, "ignored": true, "reason": reason}, nil
	}
	var verifications map[string]channelVerificationRecord
	if contact == nil {
		verifications, _, err = prepareAutomaticEmailVerifications(ctx, pid, rawChannels, nil, false)
		if err != nil {
			return nil, err
		}
	}
	if err = seedSystemAttributes(db, pid); err != nil {
		return nil, err
	}
	rules, err := dbListRoutingRules(db, pid)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Acquire the SQLite writer before reading state; another process cannot
	// change identity or win the same delivery between our reads and writes.
	if _, err = tx.Exec(`UPDATE contacts SET id=id WHERE 0`); err != nil {
		return nil, err
	}
	if body.MessageID > 0 {
		var aid, cid, convoID int64
		err = tx.QueryRow(`SELECT id,contact_id,COALESCE(conversation_id,0) FROM contact_activities WHERE project_id=? AND messaging_install_id=? AND messaging_id=?`, pid, sourceID, body.MessageID).Scan(&aid, &cid, &convoID)
		if err == nil {
			if err = insertActivityAttachmentsTx(tx, pid, aid, body.Attachments); err != nil {
				return nil, err
			}
			if err = tx.Commit(); err != nil {
				return nil, err
			}
			if err = deliverQueuedCRMEvents(ctx); err != nil {
				ctx.Logger().Warn("inbound events pending", "err", err)
			}
			return map[string]any{"ok": true, "deduped": true, "contact_id": cid, "activity_id": aid, "conversation_id": convoID}, nil
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}
	var cid int64
	err = tx.QueryRow(`SELECT ch.contact_id FROM contact_channels ch JOIN contacts c ON c.id=ch.contact_id AND c.project_id=ch.project_id WHERE ch.project_id=? AND ch.kind=? AND ch.value=? AND c.deleted_at IS NULL AND c.status!='merged'`, pid, contactChannelKindFor(body.Channel), body.From).Scan(&cid)
	created := false
	if err == sql.ErrNoRows {
		res, err := tx.Exec(`INSERT INTO contacts(project_id,display_name,status,source) VALUES(?,?,'active','messaging:inbound')`, pid, parseFromName(body.From, body.Headers))
		if err != nil {
			return nil, err
		}
		cid, err = res.LastInsertId()
		if err != nil {
			return nil, err
		}
		if err = applyParsedChannelsPatchTx(tx, pid, cid, channels, "messaging:inbound", verifications); err != nil {
			return nil, err
		}
		created = true
	} else if err != nil {
		return nil, err
	}
	if created && automated {
		if err = dbAddTag(tx, pid, cid, "automated"); err != nil {
			return nil, err
		}
	}
	contact, err = dbGetByID(tx, pid, cid)
	if err != nil || contact == nil {
		return nil, fmt.Errorf("contact resolution failed: %v", err)
	}
	if created && len(fuzzy) > 0 {
		if _, err = logMessageActivityTx(tx, logMessageActivityInput{ProjectID: pid, ContactID: cid, Kind: ActivityKindSystem, Body: "stub contact created from inbound; possible duplicates flagged", Source: "crm", SourceDetail: map[string]any{"possible_match_ids": fuzzy}}); err != nil {
			return nil, err
		}
	}
	recipients := append([]string{body.MatchedRecipient}, body.To...)
	for _, rule := range rules {
		if !rule.Enabled || !ruleRecipientMatches(rule, recipients) || !ruleSenderMatches(rule, body.From) {
			continue
		}
		if rule.AddListID != nil {
			if _, err = dbListAddContactChanged(tx, pid, *rule.AddListID, cid, "routing_rule"); err != nil {
				return nil, err
			}
		}
		if tag := strings.TrimSpace(rule.AddTag); tag != "" {
			if err = dbAddTag(tx, pid, cid, tag); err != nil {
				return nil, err
			}
		}
	}
	var listID int64
	if body.MatchedPattern != "" {
		list, err := dbListByInboundPattern(tx, pid, body.MatchedPattern)
		if err != nil {
			return nil, err
		}
		if list != nil {
			listID = list.ID
			if _, err = dbListAddContactChanged(tx, pid, listID, cid, "messaging:inbound"); err != nil {
				return nil, err
			}
		}
	}
	var convoID int64
	if body.Channel != "email" {
		err = tx.QueryRow(`SELECT id FROM contact_conversations WHERE project_id=? AND contact_id=? AND channel=?`, pid, cid, body.Channel).Scan(&convoID)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
	} else {
		refs := append([]string{body.InReplyTo}, body.References...)
		for _, ref := range refs {
			if ref == "" {
				continue
			}
			err = tx.QueryRow(`SELECT id FROM contact_conversations WHERE project_id=? AND contact_id=? AND channel='email' AND (root_message_id=? OR id IN (SELECT conversation_id FROM contact_activities WHERE project_id=? AND contact_id=? AND message_id_header=?)) ORDER BY id LIMIT 1`, pid, cid, ref, pid, cid, ref).Scan(&convoID)
			if err == nil {
				break
			}
			if err != sql.ErrNoRows {
				return nil, err
			}
		}
	}
	threadCreated := convoID == 0
	if threadCreated {
		convoID, err = dbConversationCreate(tx, pid, cid, body.Channel, body.Subject, body.MessageIDHeader, body.ReceivedAt)
		if err != nil {
			return nil, err
		}
	}
	var status string
	if err = tx.QueryRow(`SELECT status FROM contact_conversations WHERE project_id=? AND id=?`, pid, convoID).Scan(&status); err != nil {
		return nil, err
	}
	var spamTag int
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM contact_tags WHERE project_id=? AND contact_id=? AND lower(tag_name)='spam')`, pid, cid).Scan(&spamTag); err != nil {
		return nil, err
	}
	targetStatus := status
	if contact.Status == "spam" || spamTag != 0 {
		targetStatus = "spam"
	} else if status == "pending" || status == "closed" {
		targetStatus = "open"
	}
	if targetStatus != status {
		if _, err = tx.Exec(`UPDATE contact_conversations SET status=?,status_changed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE project_id=? AND id=?`, targetStatus, pid, convoID); err != nil {
			return nil, err
		}
	}
	text := body.BodyText
	if text == "" && body.BodyHTML != "" {
		text = plainTextFromHTML(body.BodyHTML)
	}
	if body.Channel == "email" && body.Subject != "" {
		text = body.Subject + "\n\n" + text
	}
	replyTo := ""
	for key, value := range body.Headers {
		if strings.EqualFold(key, "Reply-To") {
			replyTo = anyString(value)
		}
	}
	act, err := logMessageActivityTx(tx, logMessageActivityInput{ProjectID: pid, ContactID: cid, Kind: receivedKindForChannel(body.Channel), Body: text, OccurredAt: body.ReceivedAt, Source: "messaging", ConversationID: convoID, MessagingID: body.MessageID, MessagingInstallID: sourceID, MessageIDHeader: body.MessageIDHeader, Attachments: body.Attachments, SourceDetail: map[string]any{"messaging_id": body.MessageID, "source_install_id": sourceID, "message_id_header": body.MessageIDHeader, "in_reply_to": body.InReplyTo, "matched_pattern": body.MatchedPattern, "from": body.From, "reply_to": replyTo, "receiving_identity": body.MatchedRecipient, "to": body.To, "cc": body.CC, "sender_automated": automated, "sender_class": reason}})
	if err != nil {
		return nil, err
	}
	parts := []conversationParticipant{{Role: "from", Address: body.From, ContactID: cid}}
	for _, address := range append(body.To, body.MatchedRecipient) {
		parts = append(parts, conversationParticipant{Role: "to", Address: address})
	}
	for _, address := range body.CC {
		parts = append(parts, conversationParticipant{Role: "cc", Address: address})
	}
	if err = dbConversationParticipantsAdd(tx, pid, convoID, body.Channel, parts); err != nil {
		return nil, err
	}
	if created {
		ids := []int64{}
		rows, err := tx.Query(`SELECT list_id FROM contact_list_members WHERE project_id=? AND contact_id=? ORDER BY list_id`, pid, cid)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		payload := map[string]any{"id": cid, "contact_id": cid, "display_name": contact.DisplayName, "primary_email": contact.PrimaryEmail, "primary_phone": contact.PrimaryPhone, "source": contact.Source, "list_ids": ids}
		if err = queueCRMEvent(tx, pid, "contact.added", payload); err != nil {
			return nil, err
		}
	}
	if targetStatus != status {
		if err = queueCRMEvent(tx, pid, "conversation.status.changed", map[string]any{"contact_id": cid, "conversation_id": convoID, "status": targetStatus, "previous_status": status, "auto_reopened": targetStatus == "open"}); err != nil {
			return nil, err
		}
	}
	payload := map[string]any{"contact_id": cid, "activity_id": act.ID, "conversation_id": convoID, "kind": act.Kind, "source": "messaging", "attachment_count": len(body.Attachments)}
	if err = queueCRMEvent(tx, pid, "contact.activity.added", payload); err != nil {
		return nil, err
	}
	payload["channel"] = body.Channel
	payload["thread_created"] = threadCreated
	payload["thread_state"] = "existing"
	if threadCreated {
		payload["thread_state"] = "new"
	}
	payload["messaging_id"] = body.MessageID
	if err = queueCRMEvent(tx, pid, "conversation.message.received", payload); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if err = deliverQueuedCRMEvents(ctx); err != nil {
		ctx.Logger().Warn("inbound events pending", "err", err)
	}
	out := map[string]any{"ok": true, "contact_id": cid, "stub_created": created, "activity_id": act.ID, "conversation_id": convoID}
	if listID != 0 {
		out["list_id"] = listID
	}
	return out, nil
}
