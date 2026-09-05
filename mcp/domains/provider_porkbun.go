package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ─── Porkbun provider ──────────────────────────────────────────────

type porkbunProvider struct{ bound *sdk.BoundIntegration }

type porkbunRegistrar struct{ bound *sdk.BoundIntegration }

func (p *porkbunRegistrar) CheckAvailability(ctx *sdk.AppCtx, domain string) (*DomainAvailability, error) {
	raw, err := providerCall(ctx, p.bound, "check_availability", map[string]any{"domain": domain})
	if err != nil {
		return nil, err
	}
	out := parsePorkbunAvailability(domain, p.bound, raw)
	if !out.Known {
		return nil, errors.New("Porkbun omitted availability")
	}
	return &out, nil
}

func (p *porkbunRegistrar) Pricing(ctx *sdk.AppCtx, tld string) (any, error) {
	raw, err := providerCall(ctx, p.bound, "get_pricing", map[string]any{})
	if err != nil {
		return nil, err
	}
	if tld == "" {
		var all any
		if err := json.Unmarshal(raw, &all); err != nil {
			return nil, fmt.Errorf("parse pricing: %w", err)
		}
		return all, nil
	}
	entry := pricingEntryForTLD(raw, tld)
	if entry == nil {
		return nil, fmt.Errorf("no pricing found for .%s", tld)
	}
	return entry, nil
}

func (p *porkbunRegistrar) Register(ctx *sdk.AppCtx, req DomainRegistrationRequest) (json.RawMessage, error) {
	if req.IdempotencyKey == "" {
		return nil, errors.New("registration idempotency key required")
	}
	if req.CostCents <= 0 {
		return nil, errors.New("validated registration cost required")
	}
	payload := map[string]any{"domain": req.Domain, "cost": req.CostCents, "agreeToTerms": "yes", "whoisPrivacy": req.WhoisPrivacy, "idempotency_key": req.IdempotencyKey}
	if req.DryRun {
		payload["dryRun"] = true
	}
	raw, err := providerCall(ctx, p.bound, "register_domain", payload)
	if err != nil {
		return nil, err
	}
	if req.DryRun {
		var preview struct {
			Domain       string `json:"domain"`
			DryRun       bool   `json:"dryRun"`
			WouldSucceed bool   `json:"wouldSucceed"`
			Cost         int64  `json:"cost"`
			Duration     int    `json:"duration"`
		}
		if json.Unmarshal(raw, &preview) != nil || preview.Domain != req.Domain || !preview.DryRun || !preview.WouldSucceed || preview.Cost != req.CostCents || preview.Duration != req.Years {
			return nil, errors.New("provider did not confirm registration quote and eligibility")
		}
		return raw, nil
	}
	var result struct {
		Domain  string      `json:"domain"`
		OrderID json.Number `json:"orderId"`
		Cost    int64       `json:"cost"`
		DryRun  bool        `json:"dryRun"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Domain != req.Domain || result.OrderID == "" || result.Cost != req.CostCents || result.DryRun {
		return nil, errors.New("provider did not confirm the expected registration result; outcome unknown")
	}
	return raw, nil
}

func (p *porkbunProvider) List(ctx *sdk.AppCtx, domain string) ([]DNSRecord, error) {
	raw, err := providerCall(ctx, p.bound, "list_dns_records", map[string]any{"domain": domain})
	if err != nil {
		return nil, err
	}
	return parsePorkbunRecords(raw)
}

func (p *porkbunProvider) Upsert(ctx *sdk.AppCtx, domain, sub, rtype, value string, ttl int, recordID string, existing []DNSRecord) (string, error) {
	wantFQ := domain
	if sub != "" {
		wantFQ = sub + "." + domain
	}
	matches := filterRecords(existing, func(r DNSRecord) bool {
		if !strings.EqualFold(r.Type, rtype) {
			return false
		}
		return strings.EqualFold(r.Name, wantFQ) || strings.EqualFold(r.Name, sub)
	})
	creating := recordID == createRecordID
	if creating {
		matches = nil
		recordID = ""
	}

	if recordID != "" {
		selected, err := selectedRecord(matches, recordID)
		if err != nil {
			return "", err
		}
		matches = []DNSRecord{*selected}
	} else if len(matches) > 1 {
		return "", ambiguousRRSetError(domain, sub, rtype, len(matches))
	}

	prio := ""
	content := value
	if rtype == "MX" || rtype == "SRV" {
		var previous *DNSRecord
		if len(matches) == 1 {
			previous = &matches[0]
		}
		parsed, priority, err := priorityValue(rtype, value, previous)
		if err != nil {
			return "", err
		}
		content = parsed
		prio = strconv.Itoa(priority)
	}

	if len(matches) > 0 {
		// Unchanged-value short-circuit. Porkbun's edit endpoint rejects
		// an edit whose value is identical to what's already stored with
		// EDIT_ERROR_WE_WERE_UNABLE_TO_EDIT_THE_DNS_RECORD — which we'd
		// otherwise surface as a failure even though the desired state is
		// already present. When the matched record already equals what we
		// want, skip the edit and report a no-op.
		wantPrio := 0
		if prio != "" {
			wantPrio, _ = strconv.Atoi(prio)
		}
		if porkbunRecordUnchanged(matches[0], content, ttl, wantPrio) {
			return "unchanged", nil
		}
		payload := map[string]any{
			"domain":    domain,
			"type":      rtype,
			"subdomain": sub,
			"content":   content,
			"ttl":       fmt.Sprintf("%d", ttl),
		}
		if prio != "" {
			payload["prio"] = prio
		}
		tool := "edit_dns_record"
		if len(matches) == 1 {
			recordID = matches[0].ID
			if recordID == "" {
				return "", errors.New("provider omitted record ID")
			}
			tool = "edit_dns_record"
			payload["id"] = recordID
			payload["name"] = sub
		}
		if _, err := providerCall(ctx, p.bound, tool, payload); err != nil {
			return "", fmt.Errorf("edit: %w", err)
		}
		return "updated", nil
	}
	createPayload := map[string]any{
		"domain":  domain,
		"name":    sub,
		"type":    rtype,
		"content": content,
		"ttl":     fmt.Sprintf("%d", ttl),
	}
	if prio != "" {
		createPayload["prio"] = prio
	}
	if _, err := providerCall(ctx, p.bound, "create_dns_record", createPayload); err != nil {
		// Idempotency rescue: Porkbun returns a non-2xx (often a
		// generic 406 HTML page) when the record we tried to create
		// already exists under a name our filter didn't match — apex
		// returned as "acme.com" vs our wantFQ build, or a case-fold
		// mismatch on TXT content. Re-list and check: if the record
		// is now there with the value we wanted, the upsert succeeded
		// regardless of the original error path. Otherwise we surface
		// the create error as before.
		if after, lErr := p.List(ctx, domain); lErr == nil && hasExactDesiredRecord(after, domain, sub, rtype, content, ttl, intArg(map[string]any{"prio": prio}, "prio", 0)) {
			return "updated", nil
		}
		return "", fmt.Errorf("create: %w", err)
	}
	return "created", nil
}

// hasMatchingRecord reports whether the provider's record set
// already contains the (name, type, value) we wanted to upsert.
// Used by Porkbun's Upsert to rescue a duplicate-record failure
// after a list-miss + create-conflict — the record was there all
// along, the filter just didn't catch it.
func hasMatchingRecord(records []DNSRecord, wantFQ, sub, rtype, content string) bool {
	for _, r := range records {
		if !strings.EqualFold(r.Type, rtype) {
			continue
		}
		// Match the name the same way Upsert's filter did, plus a
		// looser fallback: providers sometimes return the apex as
		// just the registered domain ("acme.com") when we asked for
		// the bare apex (sub == "").
		nameOK := strings.EqualFold(r.Name, wantFQ) || strings.EqualFold(r.Name, sub)
		if !nameOK && sub == "" {
			nameOK = strings.EqualFold(r.Name, wantFQ) || r.Name == "" || r.Name == "@"
		}
		if !nameOK {
			continue
		}
		if recordValueEqual(rtype, r.Value, content) {
			return true
		}
	}
	return false
}

// porkbunRecordUnchanged reports whether an existing record already
// holds exactly the value/ttl/prio we'd write — i.e. the edit would be
// a true no-op (the case Porkbun rejects with EDIT_ERROR). Value is
// compared trimmed + case-insensitively, matching hasMatchingRecord.
func porkbunRecordUnchanged(existing DNSRecord, content string, ttl, prio int) bool {
	if !recordValueEqual(existing.Type, existing.Value, content) {
		return false
	}
	if existing.TTL != ttl {
		return false
	}
	return existing.Prio == prio
}

func (p *porkbunProvider) Delete(ctx *sdk.AppCtx, domain, sub, rtype, recordID string, existing []DNSRecord) error {
	tool := "delete_dns_records_by_type"
	payload := map[string]any{
		"domain":    domain,
		"type":      rtype,
		"subdomain": sub,
	}
	if recordID != "" {
		if _, err := selectedRecord(recordsAtName(existing, domain, sub, rtype), recordID); err != nil {
			return err
		}
		tool = "delete_dns_record"
		payload = map[string]any{"domain": domain, "id": recordID}
	}
	_, err := providerCall(ctx, p.bound, tool, payload)
	return err
}
