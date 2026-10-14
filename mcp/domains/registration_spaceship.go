package main

import (
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"regexp"
	"time"
)

var spaceshipContactID = regexp.MustCompile(`^[a-zA-Z0-9]{27,32}$`)
var spaceshipOperationID = regexp.MustCompile(`^[a-zA-Z0-9]{1,36}$`)

// Spaceship bills its account rates, with no dry run or maximum-price parameter.
// A prepared intent explicitly records this billing consent rather than inventing a quote.
func (a *App) prepareSpaceshipRegistration(ctx *sdk.AppCtx, args map[string]any, pid, domain string, bound *sdk.BoundIntegration) (any, error) {
	if !boolArg(args, "accept_account_rate", false) {
		return nil, errors.New("Spaceship has no price ceiling or dry run; set accept_account_rate:true only after reviewing account pricing and authorizing its charge")
	}
	if strArg(args, "coupon") != "" {
		return nil, errors.New("Spaceship registration API does not support coupons")
	}
	years, err := strictIntArg(args, "years", 1, 1, 10)
	if err != nil {
		return nil, err
	}
	contacts, ok := args["contacts"].(map[string]any)
	if !ok {
		return nil, errors.New("Spaceship contact IDs required (contacts.registrant)")
	}
	for key, v := range contacts {
		if key == "attributes" {
			list, ok := v.([]any)
			if !ok || len(list) > 5 {
				return nil, errors.New("contact attributes must be an array of at most five IDs")
			}
			for _, id := range list {
				s, ok := id.(string)
				if !ok || !spaceshipContactID.MatchString(s) {
					return nil, errors.New("invalid contact attribute ID")
				}
			}
			continue
		}
		if !includes([]string{"registrant", "admin", "tech", "billing"}, key) {
			return nil, fmt.Errorf("unsupported contact field %q", key)
		}
		id, ok := v.(string)
		if !ok || !spaceshipContactID.MatchString(id) {
			return nil, fmt.Errorf("invalid Spaceship %s contact ID", key)
		}
	}
	if !spaceshipContactID.MatchString(strArg(contacts, "registrant")) {
		return nil, errors.New("registrant contact ID required")
	}
	unlock, err := acquireDNSMutation(ctx.Done(), "registration:"+pid+":"+domain)
	if err != nil {
		return nil, err
	}
	defer unlock()
	var pending int
	if err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM registration_intents WHERE project_id=? AND domain=? AND status IN ('processing','unknown')`, pid, domain).Scan(&pending); err != nil {
		return nil, err
	}
	if pending > 0 {
		return nil, apiError(409, "prior purchase unresolved; inspect its status before another purchase")
	}
	if d, err := dbDomainGetByName(ctx.AppDB(), pid, domain); err != nil {
		return nil, err
	} else if d != nil {
		return nil, apiError(409, "domain already exists in inventory")
	}
	availability, err := (&spaceshipRegistrar{bound: bound}).CheckAvailability(ctx, domain)
	if err != nil {
		return nil, err
	}
	if !availability.Known || !availability.Available {
		return nil, errors.New("domain is not confirmed available")
	}
	if availability.Premium {
		return nil, errors.New("premium Spaceship registration is not supported by this app")
	}
	// Validate contact ownership/visibility before offering a purchase confirmation.
	seen := map[string]bool{}
	for _, key := range []string{"registrant", "admin", "tech", "billing"} {
		id := strArg(contacts, key)
		if id != "" && !seen[id] {
			if _, err = providerCall(ctx, bound, "get_contact", map[string]any{"contact": id}); err != nil {
				return nil, fmt.Errorf("validate %s contact: %w", key, err)
			}
			seen[id] = true
		}
	}
	return a.storeSpaceshipIntent(ctx, args, pid, domain, bound, years, contacts)
}
func (a *App) storeSpaceshipIntent(ctx *sdk.AppCtx, args map[string]any, pid, domain string, bound *sdk.BoundIntegration, years int, contacts map[string]any) (any, error) {
	if list, ok := contacts["attributes"].([]any); ok {
		for _, id := range list {
			if _, err := providerCall(ctx, bound, "get_contact_attributes", map[string]any{"contact": id}); err != nil {
				return nil, err
			}
		}
	}
	privacy := "high"
	if !boolArg(args, "whois_privacy", true) {
		privacy = "public"
	}
	request, err := json.Marshal(map[string]any{"domain": domain, "autoRenew": boolArg(args, "auto_renew", true), "years": years, "privacyProtection": map[string]any{"level": privacy, "userConsent": true}, "contacts": contacts})
	if err != nil {
		return nil, err
	}
	token, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	in := &RegistrationIntent{Token: token, ProjectID: pid, Domain: domain, Provider: "spaceship", ConnectionID: bound.ConnectionID, Years: years, AutoRenew: boolArg(args, "auto_renew", true), WhoisPrivacy: boolArg(args, "whois_privacy", true), Notes: strArg(args, "notes"), Status: "prepared", BillingMode: "account_rate", RequestJSON: request, ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE registration_intents SET status='expired' WHERE project_id=? AND domain=? AND status='prepared'`, pid, domain); err != nil {
		return nil, err
	}
	_, err = tx.Exec(`INSERT INTO registration_intents(token,project_id,domain,years,auto_renew,whois_privacy,notes,provider_slug,connection_id,status,expires_at,price,currency,cost_cents,request_json,billing_mode) VALUES(?,?,?,?,?,?,?,?,?,'prepared',?,'','',0,?,'account_rate')`, token, pid, domain, years, in.AutoRenew, in.WhoisPrivacy, in.Notes, in.Provider, in.ConnectionID, in.ExpiresAt.Format(time.RFC3339), string(request))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return registrationDetails(in), nil
}
func (a *App) registerSpaceshipIntent(ctx *sdk.AppCtx, in *RegistrationIntent) (any, error) {
	if in.Status == "succeeded" {
		return a.registrationResult(ctx, in, true), nil
	}
	if in.Status == "processing" && in.OperationID != "" {
		return a.pollSpaceshipRegistration(ctx, in)
	}
	if in.Status != "prepared" {
		return nil, apiError(409, "Spaceship submission must not be repeated; inspect the saved operation or account and contact Spaceship support")
	}
	if time.Now().UTC().After(in.ExpiresAt) {
		if err := dbRegistrationIntentStatus(ctx.AppDB(), in.Token, "expired", nil, "confirmation expired"); err != nil {
			return nil, err
		}
		return nil, apiError(409, "confirmation expired; prepare again")
	}
	_, bound, err := a.registrarFor(ctx, in.ConnectionID, in.ProjectID)
	if err != nil {
		return nil, err
	}
	if bound.AppSlug != "spaceship" {
		return nil, apiError(409, "registrar identity changed")
	}
	var payload map[string]any
	if json.Unmarshal(in.RequestJSON, &payload) != nil || strArg(payload, "domain") != in.Domain || in.BillingMode != "account_rate" {
		return nil, apiError(409, "purchase options missing or invalid")
	}
	// No retrying a POST: SQL claims the intent before submitting once.
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := ctx.AppDB().Exec(`UPDATE registration_intents SET status='processing',attempted_at=?,updated_at=? WHERE project_id=? AND token=? AND status='prepared'`, now, now, in.ProjectID, in.Token)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, apiError(409, "purchase already submitted")
	}
	result, callErr := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "register_domain", payload)
	var envelope struct {
		OperationID string `json:"operation_id"`
	}
	if callErr == nil && result != nil && result.Success && result.Status == 202 {
		_ = json.Unmarshal(result.Data, &envelope)
	}
	if !spaceshipOperationID.MatchString(envelope.OperationID) {
		message := "Spaceship purchase outcome unknown; do not resubmit. Check account/support (integration must preserve spaceship-async-operationid)."
		if err = dbRegistrationIntentStatus(ctx.AppDB(), in.Token, "unknown", nil, message); err != nil {
			return nil, err
		}
		return nil, apiError(502, message)
	}
	in.OperationID = envelope.OperationID
	in.Status = "processing"
	_, err = ctx.AppDB().Exec(`UPDATE registration_intents SET operation_id=?,response_json=?,updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND token=?`, in.OperationID, string(result.Data), in.ProjectID, in.Token)
	if err != nil {
		return nil, fmt.Errorf("Spaceship accepted operation %s but persistence failed; do not resubmit: %w", in.OperationID, err)
	}
	return map[string]any{"registered": false, "status": "processing", "operation_id": in.OperationID, "confirmation_token": in.Token}, nil
}
func (a *App) pollSpaceshipRegistration(ctx *sdk.AppCtx, in *RegistrationIntent) (any, error) {
	_, bound, err := a.registrarFor(ctx, in.ConnectionID, in.ProjectID)
	if err != nil {
		return nil, err
	}
	if bound.AppSlug != "spaceship" {
		return nil, apiError(409, "registrar identity changed")
	}
	raw, err := providerCall(ctx, bound, "get_async_operation", map[string]any{"operationId": in.OperationID})
	if err != nil {
		return nil, err
	}
	var operation struct {
		Status string `json:"status"`
		Type   string `json:"type"`
	}
	if json.Unmarshal(raw, &operation) != nil || operation.Type != "domains_Create" {
		return nil, errors.New("Spaceship operation did not confirm a domain registration")
	}
	switch operation.Status {
	case "pending":
		return map[string]any{"registered": false, "status": "processing", "operation_id": in.OperationID, "confirmation_token": in.Token}, nil
	case "failed":
		if err = dbRegistrationIntentStatus(ctx.AppDB(), in.Token, "failed", raw, "Spaceship registration failed; inspect provider operation details"); err != nil {
			return nil, err
		}
		return map[string]any{"registered": false, "status": "failed", "operation_id": in.OperationID, "raw": raw}, nil
	case "success":
		if _, err = spaceshipOwnedDomain(ctx, bound, in.Domain); err != nil {
			return nil, err
		}
		in.Raw = raw
		return completeRegistration(ctx, in)
	default:
		return nil, errors.New("unknown Spaceship operation status")
	}
}
func spaceshipOwnedDomain(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, domain string) (map[string]any, error) {
	raw, err := providerCall(ctx, bound, "get_domain_info", map[string]any{"domain": domain})
	if err != nil {
		return nil, err
	}
	var row map[string]any
	if json.Unmarshal(raw, &row) != nil || strArg(row, "name") != domain || strArg(row, "lifecycleStatus") != "registered" {
		return nil, errors.New("Spaceship did not confirm ownership of the requested domain")
	}
	return row, nil
}
