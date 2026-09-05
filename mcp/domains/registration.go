package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func decimalCents(price string) (int64, error) {
	parts := strings.Split(price, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, errors.New("invalid decimal price")
	}
	for _, p := range parts {
		if p == "" {
			return 0, errors.New("invalid decimal price")
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return 0, errors.New("invalid decimal price")
			}
		}
	}
	if len(parts) == 2 && len(parts[1]) > 2 {
		return 0, errors.New("price must have at most two decimal places")
	}
	r, ok := new(big.Rat).SetString(price)
	if !ok || r.Sign() <= 0 {
		return 0, errors.New("invalid registration price")
	}
	r.Mul(r, big.NewRat(100, 1))
	if !r.IsInt() || !r.Num().IsInt64() {
		return 0, errors.New("registration price must be an exact number of cents")
	}
	return r.Num().Int64(), nil
}

func (a *App) toolDomainRegistrationPrepare(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	domain, err := normaliseDomainName(strArg(args, "domain"))
	if err != nil {
		return nil, err
	}
	connID, err := strictIntArg(args, "connection_id", 0, 0, 9007199254740991)
	if err != nil {
		return nil, err
	}
	if strArg(args, "coupon") != "" {
		return nil, errors.New("Porkbun API registration does not support coupons")
	}
	if !boolArg(args, "auto_renew", true) {
		return nil, errors.New("Porkbun registration uses automatic renewal; configure renewal separately after registration")
	}
	reg, bound, err := a.registrarFor(ctx, int64(connID), pid)
	if err != nil {
		return nil, err
	}
	if bound.AppSlug != "porkbun" {
		return nil, errors.New("provider does not support paid registration")
	}
	unlock, err := acquireDNSMutation(ctx.Done(), "registration:"+pid+":"+domain)
	if err != nil {
		return nil, err
	}
	defer unlock()
	var pending int
	err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM registration_intents WHERE project_id=? AND domain=? AND status IN ('processing','unknown')`, pid, domain).Scan(&pending)
	if err != nil {
		return nil, err
	}
	if pending > 0 {
		return nil, apiError(409, "a prior purchase outcome is unresolved; inspect registration status and resume that same intent")
	}
	availability, err := reg.CheckAvailability(ctx, domain)
	if err != nil {
		return nil, err
	}
	if !availability.Known || !availability.Available {
		return nil, errors.New("domain is not confirmed available")
	}
	if availability.Premium {
		return nil, errors.New("Porkbun API does not register premium domains")
	}
	if availability.Currency != "USD" || availability.MinDuration < 1 {
		return nil, errors.New("provider omitted registration currency or minimum duration")
	}
	years, err := strictIntArg(args, "years", availability.MinDuration, 1, 10)
	if err != nil {
		return nil, err
	}
	if years != availability.MinDuration {
		return nil, fmt.Errorf("this domain must be registered for the registry minimum of %d year(s)", availability.MinDuration)
	}
	cents, err := decimalCents(availability.Price)
	if err != nil {
		return nil, err
	}
	if cents > 100000000/int64(years) {
		return nil, errors.New("registration quote exceeds supported range")
	}
	cents *= int64(years)
	token, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	intent := &RegistrationIntent{Token: token, ProjectID: pid, Domain: domain, Years: years, AutoRenew: true, WhoisPrivacy: boolArg(args, "whois_privacy", true), Notes: strArg(args, "notes"), Provider: bound.AppSlug, ConnectionID: bound.ConnectionID, Price: fmt.Sprintf("%d.%02d", cents/100, cents%100), Currency: "USD", CostCents: cents, Status: "prepared", ExpiresAt: time.Now().UTC().Add(10 * time.Minute)}
	// Rehearse the exact billable request: validate total price, duration, funds,
	// and registry eligibility before producing a user-reviewable purchase token.
	_, err = reg.Register(ctx, DomainRegistrationRequest{Domain: domain, Years: years, CostCents: cents, WhoisPrivacy: intent.WhoisPrivacy, DryRun: true, IdempotencyKey: "preview-" + token})
	if err != nil {
		return nil, fmt.Errorf("registration preview: %w", err)
	}
	if _, err = ctx.AppDB().Exec(`UPDATE registration_intents SET status='expired' WHERE project_id=? AND domain=? AND status='prepared'`, pid, domain); err != nil {
		return nil, err
	}
	if err = dbRegistrationIntentInsert(ctx.AppDB(), intent); err != nil {
		return nil, err
	}
	return registrationDetails(intent), nil
}

func registrationDetails(in *RegistrationIntent) map[string]any {
	return map[string]any{"confirmation_token": in.Token, "domain": in.Domain, "years": in.Years, "auto_renew": in.AutoRenew, "whois_privacy": in.WhoisPrivacy, "provider": in.Provider, "connection_id": in.ConnectionID, "price": in.Price, "cost_cents": in.CostCents, "currency": in.Currency, "expires_at": in.ExpiresAt.Format(time.RFC3339), "status": in.Status, "error": in.Error, "attempted_at": in.AttemptedAt}
}

func (a *App) toolDomainRegister(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(strArg(args, "confirmation_token"))
	if token == "" {
		return nil, errors.New("confirmation_token required; prepare and confirm the purchase first")
	}
	in, err := dbRegistrationIntentGet(ctx.AppDB(), pid, token)
	if err != nil {
		return nil, err
	}
	if in == nil {
		return nil, apiError(404, "confirmation token not found")
	}
	if in.Status == "succeeded" {
		return a.registrationResult(ctx, in, true), nil
	}
	now := time.Now().UTC()
	if in.Status == "prepared" {
		if now.After(in.ExpiresAt) {
			_ = dbRegistrationIntentStatus(ctx.AppDB(), token, "expired", nil, "quote expired")
			return nil, apiError(409, "quote expired; prepare a new purchase")
		}
	} else {
		if !boolArg(args, "resume", false) || (in.Status != "unknown" && in.Status != "processing") {
			return nil, apiError(409, "inspect registration status; an unresolved purchase requires explicit resume of this same token")
		}
		attempted, err := time.Parse(time.RFC3339, in.AttemptedAt)
		if err != nil || now.Sub(attempted) > 23*time.Hour {
			return nil, apiError(409, "provider idempotency window unavailable; inspect ownership and contact the registrar before another purchase")
		}
		lastAttempt, _ := time.Parse(time.RFC3339, in.UpdatedAt)
		if in.Status == "processing" && now.Sub(lastAttempt) < 2*time.Minute {
			return nil, apiError(409, "purchase is still in progress")
		}
	}
	if in.CostCents <= 0 {
		return nil, apiError(409, "legacy intent has no validated cost; prepare a new purchase")
	}
	reg, bound, err := a.registrarFor(ctx, in.ConnectionID, pid)
	if err != nil {
		return nil, err
	}
	if bound.AppSlug != in.Provider {
		return nil, apiError(409, "registrar identity changed")
	}
	// Use the original attempted_at to bound all retries to the provider's
	// idempotency lifetime; retries must never extend that window.
	attempted := in.AttemptedAt
	if attempted == "" {
		attempted = now.Format(time.RFC3339)
	}
	res, err := ctx.AppDB().Exec(`UPDATE registration_intents SET status='processing',attempted_at=?,updated_at=? WHERE project_id=? AND token=? AND status=? AND updated_at=?`, attempted, now.Format(time.RFC3339), pid, token, in.Status, in.UpdatedAt)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, apiError(409, "purchase is already in progress")
	}
	raw, err := reg.Register(ctx, DomainRegistrationRequest{Domain: in.Domain, Years: in.Years, AutoRenew: in.AutoRenew, WhoisPrivacy: in.WhoisPrivacy, CostCents: in.CostCents, IdempotencyKey: in.Token})
	if err != nil {
		persistErr := dbRegistrationIntentStatus(ctx.AppDB(), token, "unknown", nil, err.Error())
		if persistErr != nil {
			return nil, fmt.Errorf("purchase outcome unknown and status could not be saved: %v; %w", persistErr, err)
		}
		return nil, apiError(502, "purchase outcome unknown; inspect status and resume the same token: "+err.Error())
	}
	in.Raw = raw
	return completeRegistration(ctx, in)
}

func completeRegistration(ctx *sdk.AppCtx, in *RegistrationIntent) (map[string]any, error) {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, fmt.Errorf("purchase completed; persistence failed, resume same token: %w", err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO domains(project_id,name,registrar_slug,dns_provider_slug,connection_id,connection_mode,notes) VALUES(?,?,?,?,?,'pinned',?) ON CONFLICT(project_id,name) WHERE deleted_at IS NULL DO NOTHING`, in.ProjectID, in.Domain, in.Provider, in.Provider, in.ConnectionID, in.Notes)
	if err != nil {
		return nil, err
	}
	d, err := scanDomain(tx.QueryRow(`SELECT `+domainSelectCols+` FROM domains WHERE project_id=? AND name=? AND deleted_at IS NULL`, in.ProjectID, in.Domain))
	if err != nil {
		return nil, err
	}
	result := map[string]any{"registered": true, "domain": d, "provider": in.Provider, "connection_id": in.ConnectionID, "raw": in.Raw, "idempotent_replay": false}
	bytes, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(`UPDATE registration_intents SET status='succeeded',response_json=?,result_json=?,error_message='',updated_at=CURRENT_TIMESTAMP WHERE token=? AND project_id=?`, string(in.Raw), string(bytes), in.Token, in.ProjectID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("purchase completed; result persistence failed, resume same token: %w", err)
	}
	return result, nil
}

func (a *App) toolRegistrationStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	token := strArg(args, "confirmation_token")
	if token != "" {
		in, err := dbRegistrationIntentGet(ctx.AppDB(), pid, token)
		if err != nil {
			return nil, err
		}
		if in == nil {
			return nil, apiError(404, "intent not found")
		}
		if boolArg(args, "cancel", false) {
			res, err := ctx.AppDB().Exec(`UPDATE registration_intents SET status='cancelled',updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND token=? AND status='prepared'`, pid, token)
			if err != nil {
				return nil, err
			}
			n, _ := res.RowsAffected()
			if n != 1 {
				return nil, apiError(409, "only an unsubmitted purchase can be cancelled")
			}
			in.Status = "cancelled"
		}
		result := registrationDetails(in)
		if boolArg(args, "inspect_ownership", false) {
			_, bound, err := a.registrarFor(ctx, in.ConnectionID, pid)
			if err != nil {
				return nil, err
			}
			row, err := porkbunOwnedDomain(ctx, bound, in.Domain)
			if err != nil {
				return nil, err
			}
			result["ownership"] = row
			result["ownership_note"] = "Ownership alone does not prove which purchase charged the account."
		}
		return result, nil
	}
	rows, err := ctx.AppDB().Query(`SELECT token FROM registration_intents WHERE project_id=? AND status IN ('prepared','processing','unknown') ORDER BY created_at DESC LIMIT 100`, pid)
	if err != nil {
		return nil, err
	}
	tokens := []string{}
	for rows.Next() {
		var t string
		if err = rows.Scan(&t); err != nil {
			rows.Close()
			return nil, err
		}
		tokens = append(tokens, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, token := range tokens {
		in, err := dbRegistrationIntentGet(ctx.AppDB(), pid, token)
		if err != nil {
			return nil, err
		}
		if in != nil {
			out = append(out, registrationDetails(in))
		}
	}
	return map[string]any{"intents": out}, nil
}

func porkbunOwnedDomain(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, domain string) (map[string]any, error) {
	seen := map[string]bool{}
	for start := 0; start < 100000; start += 1000 {
		select {
		case <-ctx.Done():
			return nil, errors.New("inventory lookup cancelled")
		default:
		}
		raw, err := providerCall(ctx, bound, "list_domains", map[string]any{"start": start})
		if err != nil {
			return nil, err
		}
		var body struct {
			Domains []map[string]any `json:"domains"`
		}
		if json.Unmarshal(raw, &body) != nil || body.Domains == nil {
			return nil, errors.New("provider omitted domain inventory")
		}
		page, _ := json.Marshal(body.Domains)
		hash := recordHash(page)
		if seen[hash] {
			return nil, errors.New("provider repeated an inventory page")
		}
		seen[hash] = true
		for _, d := range body.Domains {
			if strings.EqualFold(strArg(d, "domain"), domain) {
				return d, nil
			}
		}
		if len(body.Domains) < 1000 {
			return nil, nil
		}
	}
	return nil, errors.New("domain inventory exceeds safety limit")
}
