package main

// senders_sync.go — reconcile the local senders table with the
// bound providers.
//
// As of v0.10 reconciliation is strictly "refresh-known", never
// import. The local senders table is the operator's curated set;
// upstream identities the operator hasn't explicitly added via
// senders_create stay invisible. The pre-v0.10 behavior (import
// every SES identity on first panel mount) flooded fresh installs
// with leftover test identities from the AWS account and gave
// operators no clean way to curate the list. Adopting an existing
// upstream identity is still a one-liner: call senders_create with
// the address, which short-circuits on the upstream side when
// already verified and just writes the local row.
//
// Two entrypoints:
//
//   - refreshSendersFromProviders: for every bound provider, list
//     upstream identities and update the matching local rows. Rows
//     present locally but missing upstream get soft-deleted. Rows
//     present upstream but not locally are ignored.
//
//   - toolSendersRefresh / toolSendersSetDefault: small MCP wrappers
//     over the reconciliation + dbSetDefaultSender.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// refreshSendersFromProviders runs a full reconcile pass. Returns
// the first provider error (if any) but tries every provider — a
// missing binding on one channel doesn't block the others.
func (a *App) refreshSendersFromProviders(ctx *sdk.AppCtx, pid string) error {
	var firstErr error
	if bound := ctx.IntegrationFor("email_provider"); bound != nil {
		if err := a.refreshSESIdentities(ctx, pid, bound.ConnectionID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if bound := ctx.IntegrationFor("phone_provider"); bound != nil {
		if err := a.refreshTwilioNumbers(ctx, pid, bound.ConnectionID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// refreshSESIdentities lists every identity in the SES account
// (paginated by NextToken) and demuxes into the two local tables:
// kind=email_mailbox rows update senders, kind=email_domain rows
// update identities. Upstream identities not already tracked locally
// are ignored (the v0.10 no-auto-import rule still holds). Local rows
// whose address didn't come back from SES get soft-deleted — EXCEPT
// inheritance mailboxes whose parent identity is still alive (the
// parent_identity_id FK gives us a clean check; no more string-suffix
// gymnastics from v0.11.3).
func (a *App) refreshSESIdentities(ctx *sdk.AppCtx, pid string, connID int64) error {
	const maxIdentitiesToList = 1000

	knownSenders, err := dbListSenders(ctx.AppDB(), pid, "email", false)
	if err != nil {
		return fmt.Errorf("list local senders: %w", err)
	}
	knownIdentities, err := dbListIdentities(ctx.AppDB(), pid, "email_domain")
	if err != nil {
		return fmt.Errorf("list local identities: %w", err)
	}

	sesSenderSet := map[string]bool{}
	for _, r := range knownSenders {
		if r.Provider == "aws-ses" && r.Kind == "email_mailbox" {
			sesSenderSet[r.Address] = true
		}
	}
	sesIdentitySet := map[string]bool{}
	for _, r := range knownIdentities {
		if r.Provider == "aws-ses" {
			sesIdentitySet[r.Address] = true
		}
	}
	if len(sesSenderSet) == 0 && len(sesIdentitySet) == 0 {
		return nil
	}

	seenMailboxes := map[string]bool{}
	seenDomains := map[string]bool{}
	nextToken := ""
	for {
		args := map[string]any{"PageSize": 100}
		if nextToken != "" {
			args["NextToken"] = nextToken
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_identities", args)
		if err != nil {
			return fmt.Errorf("ses list_identities: %w", err)
		}
		if res == nil || !res.Success {
			body := ""
			if res != nil {
				body = string(res.Data)
			}
			return fmt.Errorf("ses list_identities non-2xx: %s", truncate(body, 400))
		}
		var raw struct {
			EmailIdentities []struct {
				IdentityName       string `json:"IdentityName"`
				IdentityType       string `json:"IdentityType"`
				SendingEnabled     bool   `json:"SendingEnabled"`
				VerificationStatus string `json:"VerificationStatus"`
			} `json:"EmailIdentities"`
			NextToken string `json:"NextToken"`
		}
		_ = json.Unmarshal(res.Data, &raw)
		for _, id := range raw.EmailIdentities {
			addr := strings.ToLower(id.IdentityName)
			verified := strings.EqualFold(id.VerificationStatus, "SUCCESS")
			internalStatus := sesStatusToInternal(id.VerificationStatus)
			if id.IdentityType == "DOMAIN" || id.IdentityType == "MANAGED_DOMAIN" {
				seenDomains[addr] = true
				if !sesIdentitySet[addr] {
					continue
				}
				_, err := dbUpsertIdentity(ctx.AppDB(), &identityUpsert{
					ProjectID:          pid,
					Kind:               "email_domain",
					Address:            addr,
					Provider:           "aws-ses",
					ProviderIdentityID: addr,
					Verified:           verified,
					VerificationStatus: internalStatus,
					DkimStatus:         id.VerificationStatus,
					MarkSyncedNow:      true,
				})
				if err != nil {
					ctx.Logger().Warn("upsert identity during ses refresh", "addr", addr, "err", err)
				}
				continue
			}
			seenMailboxes[addr] = true
			if !sesSenderSet[addr] {
				continue
			}
			_, err := dbUpsertSender(ctx.AppDB(), &senderUpsert{
				ProjectID:          pid,
				Channel:            "email",
				Address:            addr,
				Kind:               "email_mailbox",
				Provider:           "aws-ses",
				ProviderIdentityID: addr,
				Verified:           verified,
				VerificationStatus: internalStatus,
				SendingEnabled:     id.SendingEnabled,
				DkimStatus:         id.VerificationStatus,
				MarkSyncedNow:      true,
			})
			if err != nil {
				ctx.Logger().Warn("upsert sender during ses refresh", "addr", addr, "err", err)
			}
		}
		totalSeen := len(seenMailboxes) + len(seenDomains)
		if raw.NextToken == "" || totalSeen >= maxIdentitiesToList {
			break
		}
		nextToken = raw.NextToken
	}

	// Soft-delete senders missing upstream — except inheritance
	// mailboxes whose parent identity is alive. The FK + the seen
	// sets together cover the two cases (parent persisted locally vs.
	// parent only visible at SES via this refresh's list).
	for _, r := range knownSenders {
		if r.Provider != "aws-ses" || seenMailboxes[r.Address] || r.DeletedAt != nil {
			continue
		}
		if r.Kind == "email_mailbox" && r.ParentIdentityID != nil {
			parent, _ := dbGetIdentity(ctx.AppDB(), *r.ParentIdentityID)
			if parent != nil && parent.DeletedAt == nil {
				continue
			}
		}
		if r.Kind == "email_mailbox" {
			// Edge case: FK never got set (pre-v0.12 row or inheritance
			// row whose parent didn't persist). Fall back to seenDomains
			// from this refresh so we don't wipe a legit mailbox.
			if seenDomains[parentDomainOf(r.Address)] {
				continue
			}
		}
		_ = dbSoftDeleteSender(ctx.AppDB(), pid, r.Channel, r.Address)
	}

	// Soft-delete identities missing upstream — only when no active
	// senders inherit from them. Killing an anchor while mailboxes
	// still depend on it would orphan those mailboxes.
	for _, r := range knownIdentities {
		if r.Provider != "aws-ses" || seenDomains[r.Address] || r.DeletedAt != nil {
			continue
		}
		n, _ := dbCountSendersForIdentity(ctx.AppDB(), r.ID)
		if n > 0 {
			continue
		}
		_ = dbSoftDeleteIdentity(ctx.AppDB(), pid, r.Kind, r.Address)
	}
	return nil
}

// parentDomainOf returns the domain part of a mailbox address — e.g.
// "alice@acme.com" → "acme.com". Empty string for malformed input
// (callers treat that as "no parent" and don't skip the soft-delete).
func parentDomainOf(addr string) string {
	at := strings.IndexByte(addr, '@')
	if at <= 0 || at == len(addr)-1 {
		return ""
	}
	return strings.ToLower(addr[at+1:])
}

// sesStatusToInternal maps SES's VerificationStatus enum to ours.
func sesStatusToInternal(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "SUCCESS":
		return "verified"
	case "PENDING", "NOT_STARTED":
		return "pending"
	case "FAILED":
		return "failed"
	case "TEMPORARY_FAILURE":
		return "pending"
	}
	return "pending"
}

// refreshTwilioNumbers lists every phone number in the Twilio account
// and updates the matching local sms/whatsapp rows. Upstream numbers
// not already tracked locally are ignored; local rows whose number
// didn't come back from Twilio are soft-deleted.
func (a *App) refreshTwilioNumbers(ctx *sdk.AppCtx, pid string, connID int64) error {
	// Collect known Twilio numbers across the channels we surface
	// them on (sms + whatsapp share the same upstream identity).
	knownRows, err := dbListSenders(ctx.AppDB(), pid, "", false)
	if err != nil {
		return fmt.Errorf("list local senders: %w", err)
	}
	known := map[string]bool{}
	for _, r := range knownRows {
		if r.Provider == "twilio" {
			known[r.Address] = true
		}
	}
	if len(known) == 0 {
		return nil
	}

	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_phone_numbers", map[string]any{
		"PageSize": 1000,
	})
	if err != nil {
		return fmt.Errorf("twilio list_phone_numbers: %w", err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return fmt.Errorf("twilio list_phone_numbers non-2xx: %s", truncate(body, 400))
	}
	var listed struct {
		IncomingPhoneNumbers []struct {
			SID         string `json:"sid"`
			PhoneNumber string `json:"phone_number"`
			SmsURL      string `json:"sms_url"`
		} `json:"incoming_phone_numbers"`
	}
	_ = json.Unmarshal(res.Data, &listed)
	seen := map[string]bool{}
	for _, pn := range listed.IncomingPhoneNumbers {
		seen[pn.PhoneNumber] = true
		if !known[pn.PhoneNumber] {
			continue
		}
		// Default to "sms"; if there's an existing row on whatsapp we
		// don't touch it (the upsert keys on (project, channel, address)).
		_, err := dbUpsertSender(ctx.AppDB(), &senderUpsert{
			ProjectID:          pid,
			Channel:            "sms",
			Address:            pn.PhoneNumber,
			Kind:               "phone",
			Provider:           "twilio",
			ProviderIdentityID: pn.SID,
			Verified:           true,
			VerificationStatus: "verified",
			SendingEnabled:     true,
			MarkSyncedNow:      true,
		})
		if err != nil {
			ctx.Logger().Warn("upsert sender during twilio refresh", "addr", pn.PhoneNumber, "err", err)
		}
	}
	// Soft-delete Twilio rows that no longer exist upstream.
	for _, r := range knownRows {
		if r.Provider == "twilio" && !seen[r.Address] && r.DeletedAt == nil {
			_ = dbSoftDeleteSender(ctx.AppDB(), pid, r.Channel, r.Address)
		}
	}
	return nil
}

// toolSendersRefresh — explicit reconcile. The panel calls it on the
// "Refresh" button; agents call it after a senders_create when they
// want SES to flip dkim_status before the TTL elapses.
func (a *App) toolSendersRefresh(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if err := a.refreshSendersFromProviders(ctx, pid); err != nil {
		return nil, err
	}
	rows, err := dbListSenders(ctx.AppDB(), pid, "", false)
	if err != nil {
		return nil, err
	}
	return map[string]any{"refreshed": len(rows), "count": len(rows)}, nil
}

// toolSendersSetDefault flips the per-(project, channel) default to
// the named address. The partial unique index on (project, channel)
// WHERE is_default = 1 enforces uniqueness at the SQL layer; the
// helper clears the previous default first inside a transaction.
func (a *App) toolSendersSetDefault(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	addr := strArg(args, "address")
	if addr == "" {
		return nil, fmt.Errorf("address required")
	}
	channel := strArg(args, "channel")
	if channel == "" {
		channel = inferChannelFromAddress(addr)
		if channel == "" {
			channel = "email"
		}
	}
	if err := dbSetDefaultSender(ctx.AppDB(), pid, channel, addr); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "address": addr, "channel": channel}, nil
}

// toolIdentitiesList exposes the anchor table to MCP. Operator-facing
// admin surface; agents typically don't need it. Args: kind? to filter
// by anchor kind (currently only email_domain ships; whatsapp_business_
// account etc. land later).
func (a *App) toolIdentitiesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	kind := strArg(args, "kind")
	rows, err := dbListIdentities(ctx.AppDB(), pid, kind)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, identityRowToMap(r))
	}
	return map[string]any{"identities": out, "count": len(out)}, nil
}

func identityRowToMap(r *identityRow) map[string]any {
	m := map[string]any{
		"id":                   r.ID,
		"kind":                 r.Kind,
		"address":              r.Address,
		"provider":             r.Provider,
		"verified":             r.Verified,
		"verification_status":  r.VerificationStatus,
		"dkim_status":          r.DkimStatus,
		"inbound_bootstrapped": r.InboundBootstrapped,
	}
	if r.InboundConfig != "" {
		var cfg map[string]any
		if err := json.Unmarshal([]byte(r.InboundConfig), &cfg); err == nil {
			m["inbound_config"] = cfg
		}
	}
	if r.LastSyncedAt != nil {
		m["last_synced_at"] = r.LastSyncedAt.Format(time.RFC3339)
	}
	return m
}
