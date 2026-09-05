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
	"net/url"
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
type sesInventoryIdentity struct {
	IdentityName       string `json:"IdentityName"`
	IdentityType       string `json:"IdentityType"`
	SendingEnabled     bool   `json:"SendingEnabled"`
	VerificationStatus string `json:"VerificationStatus"`
}

func (a *App) refreshSESIdentities(ctx *sdk.AppCtx, pid string, connID int64) error {
	known, err := dbListSenders(ctx.AppDB(), pid, "email", false)
	if err != nil {
		return err
	}
	anchors, err := dbListIdentities(ctx.AppDB(), pid, "email_domain")
	if err != nil {
		return err
	}
	if len(known)+len(anchors) == 0 {
		return nil
	}
	inventory := map[string]sesInventoryIdentity{}
	tokens := map[string]bool{}
	token := ""
	for page := 0; ; page++ {
		if page >= 1000 {
			return fmt.Errorf("SES inventory incomplete: page limit reached")
		}
		args := map[string]any{"PageSize": 100}
		if token != "" {
			args["NextToken"] = token
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_identities", args)
		if err != nil {
			return err
		}
		if res == nil || !res.Success {
			return fmt.Errorf("SES inventory: %s", truncateResData(res))
		}
		var data struct {
			EmailIdentities *[]sesInventoryIdentity `json:"EmailIdentities"`
			NextToken       string                  `json:"NextToken"`
		}
		if err := json.Unmarshal(res.Data, &data); err != nil {
			return err
		}
		if data.EmailIdentities == nil {
			return fmt.Errorf("SES inventory missing EmailIdentities")
		}
		for _, v := range *data.EmailIdentities {
			if v.IdentityName == "" || v.IdentityType == "" {
				return fmt.Errorf("SES inventory contains invalid identity")
			}
			inventory[strings.ToLower(v.IdentityName)] = v
		}
		token = data.NextToken
		if token == "" {
			break
		}
		if tokens[token] {
			return fmt.Errorf("SES inventory repeated pagination token")
		}
		tokens[token] = true
	}
	// Only change state after every page has been validated. Preserve local wiring.
	for _, r := range anchors {
		if r.Provider != "aws-ses" {
			continue
		}
		v, ok := inventory[r.Address]
		if !ok {
			if _, err := ctx.AppDB().Exec(`UPDATE identities SET verified=0,verification_status='missing',last_synced_at=CURRENT_TIMESTAMP,last_sync_error='' WHERE id=?`, r.ID); err != nil {
				return err
			}
			continue
		}
		if _, err := dbUpsertIdentity(ctx.AppDB(), &identityUpsert{ProjectID: pid, Kind: r.Kind, Address: r.Address, Provider: r.Provider, ProviderIdentityID: r.ProviderIdentityID, Verified: strings.EqualFold(v.VerificationStatus, "SUCCESS"), VerificationStatus: sesStatusToInternal(v.VerificationStatus), DkimStatus: v.VerificationStatus, InboundBootstrapped: r.InboundBootstrapped, InboundConfig: r.InboundConfig, MarkSyncedNow: true}); err != nil {
			return err
		}
	}
	for _, r := range known {
		if r.Provider != "aws-ses" {
			continue
		}
		v, ok := inventory[r.Address]
		parentID := int64(0)
		if r.ParentIdentityID != nil {
			parentID = *r.ParentIdentityID
			parent, err := dbGetIdentity(ctx.AppDB(), parentID)
			if err != nil {
				return err
			}
			if parent != nil {
				v, ok = inventory[parent.Address]
			}
		} else if !ok {
			v, ok = inventory[parentDomainOf(r.Address)]
		}
		if !ok {
			if parentID != 0 {
				if _, err := ctx.AppDB().Exec(`UPDATE senders SET verified=0,sending_enabled=0,verification_status='missing',last_synced_at=CURRENT_TIMESTAMP,last_sync_error='' WHERE id=?`, r.ID); err != nil {
					return err
				}
			} else if err := dbSoftDeleteSender(ctx.AppDB(), pid, r.Channel, r.Address); err != nil {
				return err
			}
			continue
		}
		if _, err := dbUpsertSender(ctx.AppDB(), &senderUpsert{ProjectID: pid, Channel: r.Channel, Kind: r.Kind, Address: r.Address, Provider: r.Provider, ProviderIdentityID: r.ProviderIdentityID, Verified: strings.EqualFold(v.VerificationStatus, "SUCCESS"), VerificationStatus: sesStatusToInternal(v.VerificationStatus), DkimStatus: v.VerificationStatus, SendingEnabled: v.SendingEnabled, ParentIdentityID: parentID, InboundBootstrapped: r.InboundBootstrapped, InboundConfig: r.InboundConfig, MarkSyncedNow: true}); err != nil {
			return err
		}
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
	var first error
	for _, channel := range []string{channelSMS, channelWhatsApp} {
		if err := a.refreshTwilioChannel(ctx, pid, connID, channel); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Collect and validate every page before any local mutation. Only PageToken is
// extracted from provider pagination URLs; we never fetch arbitrary next URLs.
func twilioInventory(ctx *sdk.AppCtx, connID int64, channel string) ([]map[string]any, error) {
	tool, key := "list_phone_numbers", "incoming_phone_numbers"
	if channel == channelWhatsApp {
		tool, key = "list_whatsapp_senders", "senders"
	}
	all := []map[string]any{}
	token := ""
	seen := map[string]bool{}
	for page := 0; ; page++ {
		if page >= 1000 {
			return nil, fmt.Errorf("Twilio inventory incomplete: page limit reached")
		}
		args := map[string]any{"PageSize": 100}
		if token != "" {
			args["PageToken"] = token
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, args)
		if err != nil {
			return nil, err
		}
		if res == nil || !res.Success {
			return nil, fmt.Errorf("%s: %s", tool, truncateResData(res))
		}
		var root map[string]json.RawMessage
		if err := json.Unmarshal(res.Data, &root); err != nil {
			return nil, err
		}
		raw, ok := root[key]
		if !ok {
			return nil, fmt.Errorf("%s missing %s inventory", tool, key)
		}
		var rows []map[string]any
		if err := json.Unmarshal(raw, &rows); err != nil || rows == nil {
			return nil, fmt.Errorf("%s invalid inventory array", tool)
		}
		for _, row := range rows {
			if firstStringField(row, "sid") == "" || firstStringField(row, "phone_number", "sender_id") == "" {
				return nil, fmt.Errorf("%s malformed sender", tool)
			}
		}
		all = append(all, rows...)
		var next string
		if raw, ok := root["next_page_uri"]; ok {
			if err := json.Unmarshal(raw, &next); err != nil {
				return nil, fmt.Errorf("invalid Twilio next page: %w", err)
			}
		}
		var meta struct {
			Next string `json:"next_page_url"`
		}
		if raw, ok := root["meta"]; ok {
			if err := json.Unmarshal(raw, &meta); err != nil {
				return nil, err
			}
		}
		if meta.Next != "" {
			next = meta.Next
		}
		if next == "" {
			return all, nil
		}
		u, err := url.Parse(next)
		if err != nil {
			return nil, err
		}
		token = u.Query().Get("PageToken")
		if token == "" || seen[token] {
			return nil, fmt.Errorf("%s invalid pagination token", tool)
		}
		seen[token] = true
	}
}

func (a *App) refreshTwilioChannel(ctx *sdk.AppCtx, pid string, connID int64, channel string) error {
	known, err := dbListSenders(ctx.AppDB(), pid, channel, false)
	if err != nil {
		return err
	}
	hasTwilio := false
	for _, r := range known {
		if r.Provider == "twilio" {
			hasTwilio = true
		}
	}
	if !hasTwilio {
		return nil
	}
	rows, err := twilioInventory(ctx, connID, channel)
	if err != nil {
		return err
	}
	byAddress := map[string]map[string]any{}
	for _, r := range rows {
		byAddress[stripScheme(firstStringField(r, "phone_number", "sender_id"))] = r
	}
	for _, r := range known {
		if r.Provider != "twilio" {
			continue
		}
		v, ok := byAddress[r.Address]
		if !ok {
			if err := dbSoftDeleteSender(ctx.AppDB(), pid, channel, r.Address); err != nil {
				return err
			}
			continue
		}
		verified, status := true, "verified"
		if channel == channelWhatsApp {
			verified = twilioWhatsAppSenderVerified(firstStringField(v, "status"))
			status = twilioWhatsAppVerificationStatus(firstStringField(v, "status"))
		}
		if _, err := dbUpsertSender(ctx.AppDB(), &senderUpsert{ProjectID: pid, Channel: channel, Address: r.Address, Kind: r.Kind, Provider: "twilio", ProviderIdentityID: firstStringField(v, "sid"), Verified: verified, VerificationStatus: status, SendingEnabled: verified, InboundBootstrapped: r.InboundBootstrapped, InboundConfig: r.InboundConfig, MarkSyncedNow: true}); err != nil {
			return err
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

// toolSendersUpdate patches local-mutable fields on a sender row
// (display_name, notes). No provider round-trip — pure DB write.
// Mirror of the panel-side POST /senders/edit route, exposed via MCP
// so agents can rename a sender ("Marco at Socialcast") without
// going through the panel.
func (a *App) toolSendersUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
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
	displayName := strArg(args, "display_name")
	notes := strArg(args, "notes")
	if displayName == "" && notes == "" {
		return nil, fmt.Errorf("at least one of display_name, notes must be set (empty values preserve existing)")
	}
	if err := dbUpdateSenderLocal(ctx.AppDB(), pid, channel, addr, displayName, notes); err != nil {
		return nil, err
	}
	row, _ := dbFindSender(ctx.AppDB(), pid, channel, addr)
	if row == nil {
		return nil, fmt.Errorf("sender %s not found in channel %s", addr, channel)
	}
	return senderRowToMap(row), nil
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
			m["inbound_config"] = redactCallbackCredentials(cfg)
		}
	}
	if r.Metadata != "" {
		var meta map[string]any
		if err := json.Unmarshal([]byte(r.Metadata), &meta); err == nil {
			m["metadata"] = redactCallbackCredentials(meta)
			for _, key := range []string{"mail_from_domain", "mail_from_domain_status", "mail_from_mx_failure_mode"} {
				if v, ok := meta[key]; ok {
					m[key] = v
				}
			}
		}
	}
	if r.LastSyncedAt != nil {
		m["last_synced_at"] = r.LastSyncedAt.Format(time.RFC3339)
	}
	return m
}
