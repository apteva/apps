package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type ambiguousSendError struct{ cause error }

func (e *ambiguousSendError) Error() string {
	return "provider send outcome unknown; do not resend blindly: " + e.cause.Error()
}
func (e *ambiguousSendError) Unwrap() error { return e.cause }

// emailBinding is resolved from the install's currently authorized bindings,
// never from a connection ID supplied directly to a provider call.
func emailBinding(ctx *sdk.AppCtx, connectionID int64) (*sdk.BoundIntegration, error) {
	for _, bound := range ctx.IntegrationsFor("email_provider") {
		if bound.ConnectionID == connectionID {
			return bound, nil
		}
	}
	return nil, fmt.Errorf("email connection %d is not bound to Messaging", connectionID)
}

func chooseEmailBinding(ctx *sdk.AppCtx, requested int64, sender *senderRow) (*sdk.BoundIntegration, error) {
	if sender != nil && sender.ProviderConnectionID != 0 {
		if requested != 0 && requested != sender.ProviderConnectionID {
			return nil, errors.New("connection_id does not match the registered sender")
		}
		bound, err := emailBinding(ctx, sender.ProviderConnectionID)
		if err != nil {
			return nil, err
		}
		if bound.AppSlug != sender.Provider {
			return nil, fmt.Errorf("sender provider %s no longer matches connection %d", sender.Provider, sender.ProviderConnectionID)
		}
		return bound, nil
	}
	if requested != 0 {
		return emailBinding(ctx, requested)
	}
	if sender != nil && sender.Provider != "" {
		var match *sdk.BoundIntegration
		for _, bound := range ctx.IntegrationsFor("email_provider") {
			if bound.AppSlug == sender.Provider {
				if match != nil {
					return nil, fmt.Errorf("sender %s has no pinned connection and multiple %s connections are bound", sender.Address, sender.Provider)
				}
				match = bound
			}
		}
		if match == nil {
			return nil, fmt.Errorf("no bound %s connection for sender %s", sender.Provider, sender.Address)
		}
		return match, nil
	}
	bound := ctx.IntegrationFor("email_provider")
	if bound == nil {
		return nil, errors.New("no email_provider bound")
	}
	return bound, nil
}

func sendViaEmailProvider(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, in providerSendInput) (string, string, error) {
	switch bound.AppSlug {
	case "aws-ses":
		id, err := sendViaSESConnection(ctx, bound, in)
		return id, "", err
	case "gmail":
		return sendViaGmail(ctx, bound.ConnectionID, in)
	default:
		return "", "", fmt.Errorf("unsupported email provider %q", bound.AppSlug)
	}
}

func emailProviderSlug(bound *sdk.BoundIntegration) string {
	if bound == nil {
		return ""
	}
	return bound.AppSlug
}

func emailProviderConnection(bound *sdk.BoundIntegration) int64 {
	if bound == nil {
		return 0
	}
	return bound.ConnectionID
}

// SES replaces Message-ID on delivery. Its send API returns only the opaque
// local part; the sending region supplies the domain used in the delivered
// RFC header. If either component is unavailable, leave the header unknown
// rather than confusing the provider ID with an RFC Message-ID.
func sesMessageIDHeader(providerID, region string) string {
	providerID = strings.TrimSpace(providerID)
	region = strings.ToLower(strings.TrimSpace(region))
	if providerID == "" || region == "" {
		return ""
	}
	for _, c := range providerID {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_') {
			return ""
		}
	}
	for _, c := range region {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return ""
		}
	}
	return "<" + providerID + "@" + region + ".amazonses.com>"
}

func sendViaGmail(ctx *sdk.AppCtx, connectionID int64, in providerSendInput) (string, string, error) {
	// Gmail has no separate recipient envelope: Bcc must be in the raw
	// message handed to its send endpoint. The SES path intentionally omits it.
	raw, err := buildRawEmail(in)
	if err != nil {
		return "", "", err
	}
	if len(in.BCC) > 0 {
		raw = append([]byte("Bcc: "+strings.Join(in.BCC, ", ")+"\r\n"), raw...)
	}
	payload := map[string]any{"raw": base64.RawURLEncoding.EncodeToString(raw)}
	if in.ProviderThreadID != "" {
		payload["threadId"] = in.ProviderThreadID
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "send_raw_email", payload)
	if err != nil {
		return "", "", &ambiguousSendError{cause: err}
	}
	if res == nil || !res.Success {
		return "", "", fmt.Errorf("Gmail send rejected: %s", truncateResData(res))
	}
	var out struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	}
	if err := json.Unmarshal(res.Data, &out); err != nil || out.ID == "" {
		return "", "", &ambiguousSendError{cause: errors.New("Gmail accepted send but returned no message ID")}
	}
	return out.ID, out.ThreadID, nil
}

type gmailAlias struct {
	SendAsEmail        string `json:"sendAsEmail"`
	DisplayName        string `json:"displayName"`
	VerificationStatus string `json:"verificationStatus"`
	IsPrimary          bool   `json:"isPrimary"`
}

func gmailAliases(ctx *sdk.AppCtx, connectionID int64) ([]gmailAlias, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "list_send_as", map[string]any{})
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		return nil, fmt.Errorf("Gmail aliases: %s", truncateResData(res))
	}
	var out struct {
		SendAs []gmailAlias `json:"sendAs"`
	}
	if err := json.Unmarshal(res.Data, &out); err != nil {
		return nil, err
	}
	return out.SendAs, nil
}

func findGmailVerifiedAlias(aliases []gmailAlias, address string) (*gmailAlias, error) {
	for _, alias := range aliases {
		if strings.EqualFold(alias.SendAsEmail, address) {
			if alias.IsPrimary || strings.EqualFold(alias.VerificationStatus, "accepted") {
				return &alias, nil
			}
			return nil, fmt.Errorf("Gmail alias %s is not verified (%s)", address, alias.VerificationStatus)
		}
	}
	return nil, fmt.Errorf("%s is not a configured Gmail send-as address", address)
}

func gmailVerifiedAlias(ctx *sdk.AppCtx, connectionID int64, address string) (*gmailAlias, error) {
	aliases, err := gmailAliases(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	return findGmailVerifiedAlias(aliases, address)
}

func (a *App) sendersCreateGmail(ctx *sdk.AppCtx, pid string, connectionID int64, address string, req sendersCreateReq, resp *sendersCreateResp) (*sendersCreateResp, error) {
	alias, err := gmailVerifiedAlias(ctx, connectionID, address)
	if err != nil {
		return nil, err
	}
	displayName := req.DisplayName
	if displayName == "" {
		displayName = alias.DisplayName
	}
	if _, err := dbUpsertSender(ctx.AppDB(), &senderUpsert{
		ProjectID: pid, Channel: "email", Address: address, Kind: "email_mailbox",
		DisplayName: displayName, Provider: "gmail", ProviderConnectionID: connectionID,
		ProviderIdentityID: alias.SendAsEmail, Verified: true, VerificationStatus: "verified",
		SendingEnabled: true, MarkSyncedNow: true,
	}); err != nil {
		return nil, err
	}
	resp.Pending = false
	resp.NextStep = "Gmail send-as address registered; ready to send"
	resp.Steps = append(resp.Steps, bootstrapStep{Step: "gmail_verify_send_as", OK: true})
	return resp, nil
}

func (a *App) refreshGmailSenders(ctx *sdk.AppCtx, pid string, connectionID int64) error {
	rows, err := dbListSenders(ctx.AppDB(), pid, "email", false)
	if err != nil {
		return err
	}
	tracked := false
	for _, row := range rows {
		if row.Provider == "gmail" && row.ProviderConnectionID == connectionID {
			tracked = true
			break
		}
	}
	if !tracked {
		return nil
	}
	aliases, err := gmailAliases(ctx, connectionID)
	if err != nil {
		return err
	} // preserve cached verification on transient failures
	for _, row := range rows {
		if row.Provider != "gmail" || row.ProviderConnectionID != connectionID {
			continue
		}
		alias, err := findGmailVerifiedAlias(aliases, row.Address)
		if err != nil {
			_, updateErr := ctx.AppDB().Exec(`UPDATE senders SET verified=0,sending_enabled=0,verification_status='missing',last_sync_error=?,last_synced_at=CURRENT_TIMESTAMP WHERE id=?`, truncate(err.Error(), 500), row.ID)
			if updateErr != nil {
				return updateErr
			}
			continue
		}
		_, err = dbUpsertSender(ctx.AppDB(), &senderUpsert{
			ProjectID: pid, Channel: "email", Address: row.Address, Kind: "email_mailbox",
			Provider: "gmail", ProviderConnectionID: connectionID, ProviderIdentityID: alias.SendAsEmail,
			Verified: true, VerificationStatus: "verified", SendingEnabled: true, MarkSyncedNow: true,
		})
		if err != nil {
			return err
		}
	}
	return nil
}
