package main

// senders_sync.go — reconcile the local senders table with the
// bound providers. Two entrypoints:
//
//   - refreshSendersFromProviders: paginate every bound provider's
//     identity list, upsert each into the local table, soft-delete
//     local rows that no longer exist upstream. Used on first-call
//     seeding (empty table), TTL-driven background refresh from
//     toolSendersList, and the explicit senders_refresh tool.
//
//   - toolSendersRefresh / toolSendersSetDefault: small MCP wrappers
//     over the reconciliation + dbSetDefaultSender.

import (
	"encoding/json"
	"fmt"
	"strings"

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
// (paginated by NextToken) and upserts each into the local senders
// table. Local rows whose address didn't come back from SES are
// soft-deleted — handles the "operator removed it via AWS console"
// case.
func (a *App) refreshSESIdentities(ctx *sdk.AppCtx, pid string, connID int64) error {
	const maxIdentitiesToList = 1000
	seen := map[string]bool{}
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
			kind := "email"
			if id.IdentityType == "DOMAIN" || id.IdentityType == "MANAGED_DOMAIN" {
				kind = "domain"
			}
			addr := strings.ToLower(id.IdentityName)
			verified := strings.EqualFold(id.VerificationStatus, "SUCCESS")
			seen[addr] = true
			_, err := dbUpsertSender(ctx.AppDB(), &senderUpsert{
				ProjectID:          pid,
				Channel:            "email",
				Address:            addr,
				Kind:               kind,
				Provider:           "aws-ses",
				ProviderIdentityID: addr,
				Verified:           verified,
				VerificationStatus: sesStatusToInternal(id.VerificationStatus),
				SendingEnabled:     id.SendingEnabled,
				DkimStatus:         id.VerificationStatus, // best-effort; full DKIM status needs get_identity_verification per row
				MarkSyncedNow:      true,
			})
			if err != nil {
				ctx.Logger().Warn("upsert sender during ses refresh", "addr", addr, "err", err)
			}
		}
		if raw.NextToken == "" || len(seen) >= maxIdentitiesToList {
			break
		}
		nextToken = raw.NextToken
	}
	// Soft-delete local SES rows that didn't come back from the list.
	rows, err := dbListSenders(ctx.AppDB(), pid, "email", false)
	if err == nil {
		for _, r := range rows {
			if r.Provider == "aws-ses" && !seen[r.Address] && r.DeletedAt == nil {
				_ = dbSoftDeleteSender(ctx.AppDB(), pid, r.Channel, r.Address)
			}
		}
	}
	return nil
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
// and upserts it. Twilio's IncomingPhoneNumbers don't have
// "verification status" — owning the number IS verification. We mark
// every adopted number verified=true.
//
// NOTE: this only refreshes existing local rows (or imports
// previously-unknown numbers). Twilio also has Messaging Services and
// alphanumeric Sender IDs which live behind different list APIs; both
// are future scope.
func (a *App) refreshTwilioNumbers(ctx *sdk.AppCtx, pid string, connID int64) error {
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
		// We don't know which channel the operator wired the number on.
		// Default to "sms"; if there's an existing row on whatsapp we
		// don't touch it.
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
	// Soft-delete Twilio SMS rows that no longer exist upstream.
	rows, err := dbListSenders(ctx.AppDB(), pid, "sms", false)
	if err == nil {
		for _, r := range rows {
			if r.Provider == "twilio" && !seen[r.Address] && r.DeletedAt == nil {
				_ = dbSoftDeleteSender(ctx.AppDB(), pid, r.Channel, r.Address)
			}
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
