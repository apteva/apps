package main

import (
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
)

// Revision checks prevent late status reads from overwriting an execution claim.
func claimPayment(ctx *sdk.AppCtx, p bankPayment, state string) (bankPayment, bool, error) {
	res, err := ctx.AppDB().Exec(`UPDATE bank_payments SET state=?,continued=CASE WHEN ? THEN 1 ELSE continued END,revision=revision+1,updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND state=? AND revision=?`, state, state == "continuing" && p.Request.Provider == "enable-banking", p.ID, projectID(ctx), p.State, p.Revision)
	if err != nil {
		return p, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return p, false, err
	}
	if n != 1 {
		p, err = readBankPayment(ctx, p.ID)
		return p, false, err
	}
	p.State = state
	p.Revision++
	if state == "continuing" && p.Request.Provider == "enable-banking" {
		p.Continued = true
	}
	return p, true, nil
}
func plaidACHHandoff(ctx *sdk.AppCtx, p bankPayment) (any, error) {
	id := firstString(p.Response, "authorization.id")
	if id == "" || firstString(p.Response, "authorization.decision") != "user_action_required" {
		return nil, errors.New("no Plaid authorization challenge is available")
	}
	var out map[string]any
	err := executeIntegrationJSON(ctx, p.Request.ConnectionID, "create_link_token", map[string]any{"client_name": "Apteva Finance", "language": "en", "country_codes": []string{"US"}, "products": []string{"transfer"}, "user": map[string]any{"client_user_id": projectID(ctx) + ":" + fmt.Sprint(p.Request.AccountID)}, "transfer": map[string]any{"authorization_id": id}, "hosted_link": map[string]any{}}, &out)
	if err != nil {
		return nil, err
	}
	return map[string]any{"provider": "plaid", "hosted_link_url": out["hosted_link_url"], "link_token": out["link_token"]}, nil
}
func (a *App) toolBankingPaymentContinue(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if !boolArg(args, "confirmed", false) {
		return nil, errors.New("confirmed=true required to execute the reviewed payment after bank authorization")
	}
	p, err := readBankPayment(ctx, strArg(args, "id", ""))
	if err != nil {
		return nil, err
	}
	_, provider, err := paymentConnection(ctx, p.Request.ConnectionID)
	if err != nil {
		return nil, err
	}
	if provider != p.Request.Provider {
		return nil, errors.New("connection provider changed")
	}
	if provider == "enable-banking" {
		if p.Continued || p.State != "ready_to_execute" {
			return p, nil
		}
		if !p.Request.Deferred || !p.CallbackVerified || p.ProviderID == "" {
			return nil, errors.New("verified bank authorization required for deferred execution")
		}
		// The provider enforces its authorized/not-yet-submitted preconditions. No
		// interpretation of RCVD/ACTC/etc. is used as evidence of authorization.
		p, claimed, err := claimPayment(ctx, p, "continuing")
		if err != nil || !claimed {
			return p, err
		}
		p.Continued = true
		var raw map[string]any
		if err := executeIntegrationJSON(ctx, p.Request.ConnectionID, "submit_payment", map[string]any{"payment_id": p.ProviderID}, &raw); err != nil {
			return uncertainPayment(ctx, p, "Deferred execution outcome is uncertain. Refresh bank status; execution will not be replayed.")
		}
		p.State = "submitted"
		p.ProviderStatus = firstString(raw, "status")
		p.Response = raw
		p.Error = ""
		return saveBankPayment(ctx, p)
	}
	if provider != "plaid" || p.Request.Mode != "ach" {
		return nil, errors.New("this provider completes execution through its bank authorization page")
	}
	if p.State != "authorization_required" || firstString(p.Response, "authorization.decision") != "user_action_required" {
		return p, nil
	}
	links, err := bankingLinks(ctx, provider, p.Request.ConnectionID, p.Request.AccountID)
	if err != nil {
		return nil, err
	}
	if len(links) != 1 || links[0].ExternalID != p.Request.ExternalAccountID || links[0].Account.Archived || links[0].Account.Currency != "USD" {
		return nil, errors.New("source account changed")
	}
	token, err := bankingLinkAccessToken(links[0])
	if err != nil {
		return nil, err
	}
	p, claimed, err := claimPayment(ctx, p, "continuing")
	if err != nil || !claimed {
		return p, err
	}
	r := p.Request
	var raw map[string]any
	err = executeIntegrationJSON(ctx, r.ConnectionID, "create_transfer_authorization", map[string]any{"access_token": token, "account_id": r.ExternalAccountID, "type": r.Direction, "network": r.Network, "amount": paymentDecimal(r.Amount), "ach_class": r.ACHClass, "idempotency_key": p.ID, "user": map[string]any{"legal_name": r.LegalName}}, &raw)
	if err != nil {
		return uncertainPayment(ctx, p, "Transfer authorization outcome is uncertain; inspect the provider record.")
	}
	auth := childMap(raw, "authorization")
	p.Response = map[string]any{"authorization": auth}
	p.ProviderStatus = firstString(auth, "decision")
	p.Error = ""
	if p.ProviderStatus != "approved" || firstString(auth, "id") == "" || firstString(auth, "decision_rationale.code") != "" {
		p.State = "authorization_blocked"
		if p.ProviderStatus == "user_action_required" {
			p.State = "authorization_required"
		}
		if p.ProviderStatus == "declined" {
			p.State = "declined"
		}
		return saveBankPayment(ctx, p)
	}
	p, err = saveBankPayment(ctx, p)
	if err != nil {
		return nil, err
	}
	if p.State != "continuing" {
		return p, nil
	}
	err = executeIntegrationJSON(ctx, r.ConnectionID, "create_transfer", map[string]any{"access_token": token, "account_id": r.ExternalAccountID, "authorization_id": firstString(auth, "id"), "description": r.Reference, "idempotency_key": p.ID}, &raw)
	if err != nil {
		return uncertainPayment(ctx, p, "Transfer creation outcome is uncertain; reconcile before another payment.")
	}
	p.Response = childMap(raw, "transfer")
	p.ProviderID = firstString(p.Response, "id")
	p.ProviderStatus = firstString(p.Response, "status")
	p.State = "submitted"
	if p.ProviderID == "" {
		return uncertainPayment(ctx, p, "Transfer returned no ID; reconcile provider records.")
	}
	return saveBankPayment(ctx, p)
}

func cancelPlaidTransfer(ctx *sdk.AppCtx, p bankPayment, args map[string]any) (any, error) {
	if !boolArg(args, "confirmed", false) {
		return nil, errors.New("confirmed=true required to request cancellation at the bank")
	}
	if p.Request.Provider != "plaid" || p.Request.Mode != "ach" || p.ProviderID == "" || p.State != "submitted" {
		return nil, errors.New("only a submitted, cancellable Plaid Transfer can be cancelled at the bank")
	}
	_, provider, err := paymentConnection(ctx, p.Request.ConnectionID)
	if err != nil {
		return nil, err
	}
	if provider != "plaid" {
		return nil, errors.New("connection provider changed")
	}
	p, claimed, err := claimPayment(ctx, p, "cancelling")
	if err != nil || !claimed {
		return p, err
	}
	var raw map[string]any
	if err = executeIntegrationJSON(ctx, p.Request.ConnectionID, "get_transfer", map[string]any{"transfer_id": p.ProviderID}, &raw); err != nil {
		p.State = "submitted"
		p.Error = "Could not verify cancellation eligibility"
		return saveBankPayment(ctx, p)
	}
	transfer := childMap(raw, "transfer")
	if firstString(transfer, "id") != p.ProviderID || !boolArg(transfer, "cancellable", false) {
		p.State = "submitted"
		p.Error = "The provider does not currently allow cancellation"
		return saveBankPayment(ctx, p)
	}
	if err = executeIntegrationJSON(ctx, p.Request.ConnectionID, "cancel_transfer", map[string]any{"transfer_id": p.ProviderID}, &raw); err != nil {
		return uncertainPayment(ctx, p, "Cancellation outcome is uncertain; refresh bank status before taking another action.")
	}
	p.State = "submitted"
	p.Error = ""
	p.ProviderStatus = "cancellation_requested"
	return saveBankPayment(ctx, p)
}
