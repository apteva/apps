package main

import (
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

// Dynadot's catalog currently exposes DNS append and nameserver inspection,
// but not a record-list/delete contract. Refuse destructive record operations
// until the integration exposes a complete read-modify-write API.
type dynadotProvider struct{ bound *sdk.BoundIntegration }

func (p *dynadotProvider) List(ctx *sdk.AppCtx, domain string) ([]DNSRecord, error) {
	return nil, errors.New("Dynadot integration does not expose DNS record listing yet")
}
func (p *dynadotProvider) Upsert(ctx *sdk.AppCtx, domain, sub, rtype, value string, ttl int, recordID string, existing []DNSRecord) (string, error) {
	if recordID != "" {
		return "", errors.New("Dynadot does not expose record IDs")
	}
	if sub != "" {
		return "", errors.New("Dynadot integration currently supports apex records only")
	}
	payload := map[string]any{"domain": domain, "recordType": strings.ToLower(rtype), "recordValue": value}
	if ttl > 0 {
		payload["ttl"] = fmt.Sprint(ttl)
	}
	if _, err := providerCall(ctx, p.bound, "set_dns_record", payload); err != nil {
		return "", err
	}
	return "created", nil
}
func (p *dynadotProvider) Delete(ctx *sdk.AppCtx, domain, sub, rtype, recordID string, existing []DNSRecord) error {
	return errors.New("Dynadot integration does not expose DNS record deletion yet")
}

type dynadotRegistrar struct{ bound *sdk.BoundIntegration }

func (r *dynadotRegistrar) CheckAvailability(ctx *sdk.AppCtx, domain string) (*DomainAvailability, error) {
	raw, err := providerCall(ctx, r.bound, "search_domain", map[string]any{"domain": domain})
	if err != nil {
		return nil, err
	}
	out := DomainAvailability{Domain: domain, Provider: "dynadot", ConnectionID: r.bound.ConnectionID, Source: "provider", Confidence: "provider", Known: true, Raw: raw}
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return nil, errors.New("invalid Dynadot availability response")
	}
	var v map[string]any = root
	for _, k := range []string{"SearchResponse", "response", "data"} {
		if m, ok := v[k].(map[string]any); ok {
			v = m
		}
	}
	s := strings.ToLower(fmt.Sprint(v["Available"]))
	out.Available = s == "yes" || s == "true" || s == "1"
	out.Price = fmt.Sprint(v["Price"])
	return &out, nil
}
func (r *dynadotRegistrar) Pricing(ctx *sdk.AppCtx, tld string) (any, error) {
	raw, err := providerCall(ctx, r.bound, "tld_price", map[string]any{})
	if err != nil {
		return nil, err
	}
	var v any
	if err = json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}
func (r *dynadotRegistrar) Register(ctx *sdk.AppCtx, req DomainRegistrationRequest) (json.RawMessage, error) {
	if req.DryRun {
		return nil, errors.New("Dynadot does not provide a non-purchasing registration preview")
	}
	return providerCall(ctx, r.bound, "register_domain", map[string]any{"domain": req.Domain, "duration": fmt.Sprint(req.Years)})
}
