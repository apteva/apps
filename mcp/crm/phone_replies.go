package main

import (
	"database/sql"
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

func phoneTransport(channel string) bool {
	return channel == channelSMS || channel == channelWhatsApp
}

func validateReplyTransport(original, requested string) error {
	if requested == original || (phoneTransport(original) && phoneTransport(requested)) {
		return nil
	}
	return errors.New("conversation transport can only switch between SMS and WhatsApp; email threads stay separate")
}

// Never assume a WhatsApp sender can also send SMS (or vice versa).
func verifyReplySender(ctx *sdk.AppCtx, pid, channel, from string) error {
	var out struct {
		Senders []struct {
			Channel string `json:"channel"`
			Address string `json:"address"`
		} `json:"senders"`
	}
	if err := callMessagingTool(ctx, "senders_list", map[string]any{"_project_id": pid, "channel": channel, "verified_only": true}, &out); err != nil {
		return err
	}
	for _, s := range out.Senders {
		if s.Channel == channel && canonicalParticipantAddress(channel, s.Address) == from {
			return nil
		}
	}
	return fmt.Errorf("from must be a verified %s sender in this project's bound Messaging app", channel)
}

// An explicit successful channel switch establishes an exact return route.
// It is stored with the outbound activity, so failed sends cannot establish a
// route and there is no migration or historical conversation merge. Match the
// installation and BOTH phone numbers, not merely the contact's primary phone.
func switchedPhoneConversationTx(tx *sql.Tx, pid string, cid, sourceID int64, channel, remote, local, receivedAt string) (int64, error) {
	var id int64
	err := tx.QueryRow(`SELECT a.conversation_id FROM contact_activities a
	 JOIN contact_conversations c ON c.project_id=a.project_id AND c.id=a.conversation_id AND c.contact_id=a.contact_id
	 WHERE a.project_id=? AND a.contact_id=? AND a.messaging_install_id=? AND a.kind=?
	 AND c.channel IN ('sms','whatsapp') AND json_valid(a.source_detail)
	 AND json_extract(a.source_detail,'$.reply_channel_switch')=1
	 AND json_extract(a.source_detail,'$.to')=? AND json_extract(a.source_detail,'$.from')=?
	 AND julianday(a.occurred_at)<=julianday(?)
	 ORDER BY julianday(a.occurred_at) DESC,a.id DESC LIMIT 1`, pid, cid, sourceID, sentKindForChannel(channel), remote, local, receivedAt).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}
