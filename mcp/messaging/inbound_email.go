package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"regexp"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Only SES's outermost receiving hop is evidence of SMTP delivery. A lower
// Received header, To, Cc, or Delivered-To can have been supplied by the sender.
var receivedByHost = regexp.MustCompile(`(?i)\bby\s+([^\s;]+)`)
var sesReceivingHop = regexp.MustCompile(`(?i)^inbound-smtp\.[a-z0-9-]+\.amazonaws\.com(?:\.cn)?$`)
var receivedForRecipient = regexp.MustCompile(`(?i)\bfor\s+(?:<([^<>]+)>|([^\s;<>]+))\s*(?:;|$)`)

// Only evidence/ownership failures are permanent. Database failures must be
// retried and must not turn a valid delivery into a completed quarantine job.
var errInboundOwnership = errors.New("invalid inbound email ownership")

func receivedSMTPRecipients(received string) []string {
	// Ignore comments, which may contain sender-controlled host names or
	// misleading "by" / "for" tokens. Trust only the first actual by host.
	var clean strings.Builder
	depth := 0
	escaped := false
	for _, char := range received {
		if escaped {
			escaped = false
			continue
		}
		if depth > 0 && char == '\\' {
			escaped = true
			continue
		}
		if char == '(' {
			depth++
		} else if char == ')' && depth > 0 {
			depth--
			clean.WriteByte(' ')
		} else if depth == 0 {
			clean.WriteRune(char)
		}
	}
	if depth != 0 {
		return nil
	}
	received = clean.String()
	by := receivedByHost.FindStringSubmatchIndex(received)
	if len(by) == 0 || !sesReceivingHop.MatchString(received[by[2]:by[3]]) {
		return nil
	}
	match := receivedForRecipient.FindStringSubmatch(received[by[1]:])
	if len(match) == 0 {
		return nil
	}
	address := match[1]
	if address == "" {
		address = match[2]
	}
	addresses, err := canonicalSMTPRecipients([]string{address})
	if err != nil {
		return nil
	}
	return addresses
}

func canonicalSMTPRecipients(values []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		address, err := mail.ParseAddress(strings.TrimSpace(stripScheme(value)))
		if err != nil || !looksLikeEmail(address.Address) {
			return nil, fmt.Errorf("%w: invalid SMTP envelope recipient", errInboundOwnership)
		}
		clean := strings.ToLower(address.Address)
		if !seen[clean] {
			seen[clean] = true
			out = append(out, clean)
		}
	}
	return out, nil
}

func sesSMTPRecipients(parsed *parsedInbound, env *sesInboundEnvelope) ([]string, error) {
	if env != nil {
		if len(env.Receipt.Recipients) > 0 {
			return canonicalSMTPRecipients(env.Receipt.Recipients)
		}
		if len(env.Mail.Destination) > 0 {
			return canonicalSMTPRecipients(env.Mail.Destination)
		}
	}
	if parsed != nil && len(parsed.EnvelopeRecipients) > 0 {
		return canonicalSMTPRecipients(parsed.EnvelopeRecipients)
	}
	// SES mail.headers describes the original headers and can contain a
	// sender-supplied Received hop. Only the raw delivered MIME's outermost
	// SES hop is usable as fallback, not that metadata copy or visible To.
	return nil, nil
}

// Ownership is independent of routing patterns and webhook project hints.
// Every owned SMTP recipient must belong to the same project. Conflicting
// mailbox/domain assignments are rejected, rather than preferring one owner.
func inboundEmailOwnership(ctx *sdk.AppCtx, recipients []string, provider string, connectionID int64) (string, []string, error) {
	if ctx == nil || ctx.AppDB() == nil {
		return "", nil, errors.New("email ownership database unavailable")
	}
	recipients, err := canonicalSMTPRecipients(recipients)
	if err != nil {
		return "", nil, err
	}
	if provider == "" {
		provider = "aws-ses"
	}
	project := ""
	owned := []string{}
	for _, address := range recipients {
		query := `SELECT DISTINCT project_id FROM senders WHERE channel='email' AND address=? AND deleted_at IS NULL AND provider=? AND provider_connection_id=?`
		args := []any{address, provider, connectionID}
		if provider == "aws-ses" {
			query = `SELECT DISTINCT project_id FROM (
				SELECT project_id FROM senders WHERE channel='email' AND address=? AND deleted_at IS NULL AND provider IN ('','aws-ses')
				UNION SELECT project_id FROM identities WHERE kind='email_domain' AND address=? AND deleted_at IS NULL AND provider IN ('','aws-ses')
			) ORDER BY project_id LIMIT 2`
			args = []any{address, parentDomainOf(address)}
		}
		rows, err := ctx.AppDB().Query(query, args...)
		if err != nil {
			return "", nil, err
		}
		owners := []string{}
		for rows.Next() {
			var owner string
			if err := rows.Scan(&owner); err != nil {
				rows.Close()
				return "", nil, err
			}
			owners = append(owners, owner)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", nil, err
		}
		if len(owners) > 1 || (len(owners) == 1 && project != "" && owners[0] != project) {
			return "", nil, fmt.Errorf("%w: SMTP recipients have ambiguous or cross-project ownership", errInboundOwnership)
		}
		if len(owners) == 1 && owners[0] != "" {
			project = owners[0]
			owned = append(owned, address)
		}
	}
	if project == "" {
		return "", nil, fmt.Errorf("%w: SMTP recipient has no registered project owner", errInboundOwnership)
	}
	return project, owned, nil
}

func quarantineInbound(ctx *sdk.AppCtx, pid string, id int64, reason string) error {
	_, err := ctx.AppDB().Exec(`UPDATE messages SET route_status='quarantined', route_error=?,
		receiving_identity='', matched_recipient=NULL, matched_pattern=NULL,
		route_target_app=NULL, route_target_route=NULL WHERE id=? AND project_id=? AND direction='in'`, reason, id, pid)
	return err
}

// Apply this again before processing durable/legacy jobs: their original
// project assignment or sender ownership might no longer be trustworthy.
func ensureInboundEmailOwnership(ctx *sdk.AppCtx, pid string, m *Message) (bool, error) {
	if m.Channel != channelEmail {
		return true, nil
	}
	recipients := m.EnvelopeRecipients
	if m.ProviderSlug == "gmail" {
		// Legacy Gmail rows stored visible header addresses as their envelope.
		// Re-derive them from the authenticated connection before any retry.
		var err error
		recipients, err = gmailMailboxRecipients(ctx, pid, m.ProviderConnectionID)
		if err != nil {
			if errors.Is(err, errInboundOwnership) {
				return false, quarantineInbound(ctx, pid, m.ID, err.Error())
			}
			return false, err
		}
	}
	if len(recipients) == 0 && (m.ProviderSlug == "" || m.ProviderSlug == "aws-ses") {
		var headers map[string]string
		_ = json.Unmarshal(m.Headers, &headers)
		for key, value := range headers {
			if strings.EqualFold(key, "Received") {
				recipients = receivedSMTPRecipients(value)
				break
			}
		}
	}
	owner, owned, err := inboundEmailOwnership(ctx, recipients, m.ProviderSlug, m.ProviderConnectionID)
	if err != nil && !errors.Is(err, errInboundOwnership) {
		return false, err
	}
	scoped := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID"))
	if err != nil || owner != pid || m.ProjectID != pid || (scoped != "" && scoped != pid) {
		reason := "SMTP recipient/project ownership mismatch"
		if err != nil {
			reason = err.Error()
		}
		return false, quarantineInbound(ctx, pid, m.ID, reason)
	}
	m.EnvelopeRecipients, err = canonicalSMTPRecipients(recipients)
	if err != nil {
		return false, err
	}
	m.ReceivingIdentity = owned[0]
	m.ownedRecipients = owned
	_, err = ctx.AppDB().Exec(`UPDATE messages SET envelope_recipients=?,receiving_identity=? WHERE id=? AND project_id=? AND direction='in'`, string(mustJSON(m.EnvelopeRecipients)), m.ReceivingIdentity, m.ID, pid)
	return err == nil, err
}

var errInboundDeliveryConflict = errors.New("inbound delivery already belongs to another project or message")

func sesDeliveryKeys(env *snsEnvelope, ses *sesInboundEnvelope, s3Key string) []string {
	keys := []string{}
	if s3Key != "" {
		keys = append(keys, "s3:"+s3Key)
	}
	if ses.Mail.MessageID != "" {
		keys = append(keys, "message:"+ses.Mail.MessageID)
	}
	keys = append(keys, "sns:"+env.TopicARN+":"+env.MessageID)
	return keys
}

// Claim all delivery identities globally in the same transaction as the
// message and durable job. The sender-controlled RFC Message-ID is not a key.
func persistSESInbound(ctx *sdk.AppCtx, pid, s3Key, providerID string, keys, recipients []string, receivingIdentity string, source any, query string, args ...any) (int64, bool, error) {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	var id int64
	for _, key := range keys {
		var owner string
		var claimed int64
		err := tx.QueryRow(`SELECT project_id,message_id FROM inbound_delivery_keys WHERE provider='aws-ses' AND delivery_key=?`, key).Scan(&owner, &claimed)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return 0, false, err
		}
		if owner != pid || (id != 0 && id != claimed) {
			return 0, false, errInboundDeliveryConflict
		}
		id = claimed
	}
	duplicate := id != 0
	// Pre-ledger records must also be checked globally. A legacy wrong-project
	// copy is not authority to materialize this delivery in another project.
	checks, checkArgs := []string{}, []any{}
	if s3Key != "" {
		checks = append(checks, "s3_key=?")
		checkArgs = append(checkArgs, s3Key)
	}
	if providerID != "" {
		checks = append(checks, "provider_message_id=?")
		checkArgs = append(checkArgs, providerID)
	}
	if len(checks) > 0 {
		rows, err := tx.Query(`SELECT id,project_id FROM messages WHERE direction='in' AND channel='email'
			AND provider_slug IN ('','aws-ses') AND (`+strings.Join(checks, " OR ")+`)`, checkArgs...)
		if err != nil {
			return 0, false, err
		}
		for rows.Next() {
			var legacyID int64
			var legacyOwner string
			if err := rows.Scan(&legacyID, &legacyOwner); err != nil {
				rows.Close()
				return 0, false, err
			}
			if legacyOwner != pid || (id != 0 && id != legacyID) {
				rows.Close()
				return 0, false, errInboundDeliveryConflict
			}
			id, duplicate = legacyID, true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return 0, false, err
		}
	}
	if id == 0 {
		result, err := tx.Exec(query, args...)
		if err != nil {
			return 0, false, err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return 0, false, fmt.Errorf("inbound insert did not create a message: %w", errInboundDeliveryConflict)
		}
		id, err = result.LastInsertId()
		if err != nil {
			return 0, false, err
		}
		if _, err := tx.Exec(`INSERT INTO inbound_jobs(message_id,project_id,source_kind,source) VALUES(?,?,'email',?)`, id, pid, string(mustJSON(source))); err != nil {
			return 0, false, err
		}
	} else {
		// Safe, authenticated retry metadata repairs legacy missing envelopes;
		// neither visible headers nor historical project hints are copied.
		if _, err := tx.Exec(`UPDATE messages SET envelope_recipients=?,receiving_identity=? WHERE id=? AND project_id=?`, string(mustJSON(recipients)), receivingIdentity, id, pid); err != nil {
			return 0, false, err
		}
	}
	for _, key := range keys {
		if _, err := tx.Exec(`INSERT INTO inbound_delivery_keys(provider,delivery_key,project_id,message_id) VALUES('aws-ses',?,?,?) ON CONFLICT(provider,delivery_key) DO NOTHING`, key, pid, id); err != nil {
			return 0, false, err
		}
		var owner string
		var claimed int64
		if err := tx.QueryRow(`SELECT project_id,message_id FROM inbound_delivery_keys WHERE provider='aws-ses' AND delivery_key=?`, key).Scan(&owner, &claimed); err != nil {
			return 0, false, err
		}
		if owner != pid || claimed != id {
			return 0, false, errInboundDeliveryConflict
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return id, duplicate, nil
}
