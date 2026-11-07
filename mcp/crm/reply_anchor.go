package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type crmReplyActivity struct {
	ID, MessagingID, MessagingInstallID int64
	Kind, MessageIDHeader, SourceDetail string
}

// Resolve once and pin the activity before routing, fingerprinting or dispatch.
// Activity IDs are scoped to this project, contact and conversation.
func dbReplyActivity(db *sql.DB, pid string, cid, convoID int64, transport string, activityID int64) (*crmReplyActivity, error) {
	a := &crmReplyActivity{}
	err := db.QueryRow(`SELECT id,kind,COALESCE(message_id_header,''),COALESCE(messaging_id,0),COALESCE(messaging_install_id,0),COALESCE(source_detail,'{}')
		FROM contact_activities WHERE project_id=? AND contact_id=? AND conversation_id=? AND (?=0 OR id=?)
		AND kind IN ('email_received','sms_received','whatsapp_received')
		ORDER BY CASE WHEN kind=? THEN 0 ELSE 1 END,julianday(occurred_at) DESC,id DESC LIMIT 1`,
		pid, cid, convoID, activityID, activityID, receivedKindForChannel(transport)).Scan(&a.ID, &a.Kind, &a.MessageIDHeader, &a.MessagingID, &a.MessagingInstallID, &a.SourceDetail)
	if err == sql.ErrNoRows {
		if activityID > 0 {
			return nil, errors.New("reply_to_activity_id must identify an inbound message in this conversation")
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

type crmMessagingReplyMessage struct {
	ID              int64    `json:"id"`
	Channel         string   `json:"channel"`
	Direction       string   `json:"direction"`
	Subject         string   `json:"subject"`
	MessageIDHeader string   `json:"message_id_header"`
	InReplyTo       string   `json:"in_reply_to"`
	References      []string `json:"references"`
}

func loadMessagingReplyMessage(ctx *sdk.AppCtx, pid string, a *crmReplyActivity) (*crmMessagingReplyMessage, error) {
	if a == nil || a.MessagingID <= 0 {
		return nil, nil
	}
	boundID := messagingInstallID(ctx)
	if a.MessagingInstallID > 0 && a.MessagingInstallID != boundID {
		return nil, errors.New("reply message belongs to a different Messaging installation; restore its binding before replying")
	}
	// Unknown legacy provenance is never used to look up an ID in a new store.
	if boundID <= 0 || a.MessagingInstallID == 0 {
		return nil, nil
	}
	var out struct {
		Message *crmMessagingReplyMessage `json:"message"`
	}
	if err := callMessagingTool(ctx, "message_get", map[string]any{"_project_id": pid, "id": a.MessagingID}, &out); err != nil {
		return nil, fmt.Errorf("load reply message: %w", err)
	}
	m := out.Message
	if m == nil || m.ID != a.MessagingID || m.Direction != "in" || m.Channel != channelEmail ||
		(a.MessageIDHeader != "" && m.MessageIDHeader != a.MessageIDHeader) {
		return nil, errors.New("reply message is missing or does not match the stored inbound email")
	}
	return m, nil
}

func emailReplySubject(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		subject = "(no subject)"
	}
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	return subject
}

// Intentional sender/subject overrides retain the regular send contract. Normal
// replies delegate all transport, RFC/Gmail threading and ticket data to Messaging.
func messagingAnchoredReplyArgs(send map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"_project_id", "reply_to_message_id", "body", "body_html", "attachments", "attachment_storage_ids", "template_id", "vars", "content_sid", "idempotency_key"} {
		if value, ok := send[key]; ok {
			out[key] = value
		}
	}
	return out
}

// Legacy activities without a usable Messaging anchor still use their selected
// RFC ID and top-level references, never filtered custom transport headers.
func applyCRMEmailThread(send map[string]any, convo *Conversation, a *crmReplyActivity, m *crmMessagingReplyMessage) {
	parent := convo.RootMessageID
	refs := []string{}
	if a != nil {
		parent = a.MessageIDHeader
		var detail map[string]any
		_ = json.Unmarshal([]byte(a.SourceDetail), &detail)
		refs = stringSliceArg(detail, "references")
		if len(refs) == 0 && strArg(detail, "in_reply_to") != "" {
			refs = append(refs, strArg(detail, "in_reply_to"))
		}
	}
	if m != nil {
		parent = m.MessageIDHeader
		refs = append([]string(nil), m.References...)
		if len(refs) == 0 && m.InReplyTo != "" {
			refs = append(refs, m.InReplyTo)
		}
	}
	if strings.HasPrefix(parent, "<") && strings.HasSuffix(parent, ">") {
		send["in_reply_to"] = parent
		refs = append(refs, parent)
	}
	unique := []string{}
	seen := map[string]bool{}
	for _, ref := range refs {
		if ref != "" && !seen[ref] {
			unique = append(unique, ref)
			seen[ref] = true
		}
	}
	if len(unique) > 0 {
		send["references"] = unique
	}
}
