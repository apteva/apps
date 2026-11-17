package main

import (
	"database/sql"
	"errors"
	"strings"
)

// sesProviderIDFromReference accepts only an SES RFC Message-ID. A bare
// provider ID is never a general substitute for a Message-ID: other email
// providers can use the same local part on unrelated domains.
func sesProviderIDFromReference(ref string) string {
	ref = strings.TrimSpace(ref)
	if len(ref) < 5 || ref[0] != '<' || ref[len(ref)-1] != '>' {
		return ""
	}
	value := ref[1 : len(ref)-1]
	local, domain, ok := strings.Cut(value, "@")
	if !ok || local == "" || strings.ContainsAny(local, "<> \t\r\n@") {
		return ""
	}
	domain = strings.ToLower(domain)
	if !strings.HasSuffix(domain, ".amazonses.com") || len(domain) <= len(".amazonses.com") || strings.ContainsAny(domain, "<> \t\r\n@") {
		return ""
	}
	return local
}

// automatedEmailConversationTx returns the latest email conversation of an
// automated sender, so its messages share one thread. A sender counts as
// automated when this message was classified so or the contact carries the
// automated tag. Returns 0 for human senders or when no thread exists yet.
func automatedEmailConversationTx(tx *sql.Tx, pid string, contactID int64, messageAutomated bool) (int64, error) {
	if !messageAutomated {
		var tagged int
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM contact_tags WHERE project_id=? AND contact_id=? AND tag_name=?)`, pid, contactID, tagAutomated).Scan(&tagged); err != nil {
			return 0, err
		}
		if tagged == 0 {
			return 0, nil
		}
	}
	var conversationID int64
	err := tx.QueryRow(`SELECT id FROM contact_conversations
		WHERE project_id=? AND contact_id=? AND channel='email'
		ORDER BY COALESCE(last_activity_at, started_at) DESC, id DESC LIMIT 1`, pid, contactID).Scan(&conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return conversationID, err
}

// Match the full RFC header first. For old CRM rows, or a send where SES did
// not expose its region, use the SES-only local part against the recorded
// provider ID. Both lookups stay within the resolved project and contact.
func emailConversationByReferenceTx(tx *sql.Tx, pid string, contactID int64, ref string) (int64, bool, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, false, nil
	}
	var conversationID int64
	err := tx.QueryRow(`SELECT c.id FROM contact_conversations c
		WHERE c.project_id=? AND c.contact_id=? AND c.channel='email'
		AND (c.root_message_id=? OR EXISTS (
			SELECT 1 FROM contact_activities a
			WHERE a.project_id=c.project_id AND a.contact_id=c.contact_id
			AND a.conversation_id=c.id AND a.message_id_header=?))
		ORDER BY c.id LIMIT 1`, pid, contactID, ref, ref).Scan(&conversationID)
	if err == nil {
		return conversationID, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	providerID := sesProviderIDFromReference(ref)
	if providerID == "" {
		return 0, false, nil
	}
	err = tx.QueryRow(`SELECT c.id FROM contact_conversations c
		WHERE c.project_id=? AND c.contact_id=? AND c.channel='email'
		AND (c.root_message_id=? OR EXISTS (
			SELECT 1 FROM contact_activities a
			WHERE a.project_id=c.project_id AND a.contact_id=c.contact_id
			AND a.conversation_id=c.id AND a.kind='email_sent'
			AND (a.message_id_header=? OR
				(CASE WHEN json_valid(a.source_detail) THEN json_extract(a.source_detail, '$.provider_message_id') ELSE '' END)=?)))
		ORDER BY c.id LIMIT 1`, pid, contactID, providerID, providerID, providerID).Scan(&conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return conversationID, err == nil, err
}
