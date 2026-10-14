package main

import (
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

type cloudflareProvider struct{ bound *sdk.BoundIntegration }

func (p *cloudflareProvider) zone(ctx *sdk.AppCtx, domain string) (string, error) {
	raw, e := providerCall(ctx, p.bound, "list_zones", map[string]any{"name": domain, "status": "active"})
	if e != nil {
		return "", e
	}
	var r struct {
		Result []struct{ ID, Name string } `json:"result"`
	}
	if json.Unmarshal(raw, &r) != nil || len(r.Result) == 0 {
		return "", fmt.Errorf("Cloudflare zone not found for %s", domain)
	}
	return r.Result[0].ID, nil
}
func (p *cloudflareProvider) List(ctx *sdk.AppCtx, domain string) ([]DNSRecord, error) {
	z, e := p.zone(ctx, domain)
	if e != nil {
		return nil, e
	}
	raw, e := providerCall(ctx, p.bound, "list_dns_records", map[string]any{"zone_id": z, "per_page": 50000})
	if e != nil {
		return nil, e
	}
	var r struct {
		Result []map[string]any `json:"result"`
	}
	if e = json.Unmarshal(raw, &r); e != nil {
		return nil, e
	}
	out := make([]DNSRecord, 0, len(r.Result))
	for _, m := range r.Result {
		out = append(out, cloudflareRecord(domain, m))
	}
	return out, nil
}
func cloudflareRecord(domain string, m map[string]any) DNSRecord {
	v := fmt.Sprint(m["content"])
	if v == "<nil>" {
		v = fmt.Sprint(m["data"])
	}
	ttl := intArg(m, "ttl", 0)
	prio := intArg(m, "priority", 0)
	return DNSRecord{ID: fmt.Sprint(m["id"]), Name: fmt.Sprint(m["name"]), Type: strings.ToUpper(fmt.Sprint(m["type"])), Value: v, TTL: ttl, Prio: prio, Raw: m}
}
func (p *cloudflareProvider) Upsert(ctx *sdk.AppCtx, domain, sub, rtype, value string, ttl int, recordID string, existing []DNSRecord) (string, error) {
	z, e := p.zone(ctx, domain)
	if e != nil {
		return "", e
	}
	name := domain
	if sub != "" {
		name = sub + "." + domain
	}
	m := map[string]any{"zone_id": z, "type": strings.ToUpper(rtype), "name": name, "content": value, "ttl": ttl}
	if rtype == "MX" {
		parts := strings.SplitN(value, " ", 2)
		if len(parts) == 2 {
			m["priority"] = intArg(map[string]any{"p": parts[0]}, "p", 0)
			m["content"] = parts[1]
		}
	}
	if recordID != "" {
		m["record_id"] = recordID
		_, e = providerCall(ctx, p.bound, "update_dns_record", m)
		return "updated", e
	}
	_, e = providerCall(ctx, p.bound, "create_dns_record", m)
	return "created", e
}
func (p *cloudflareProvider) Delete(ctx *sdk.AppCtx, domain, sub, rtype, recordID string, existing []DNSRecord) error {
	if recordID == "" {
		return errors.New("Cloudflare deletion requires record_id")
	}
	z, e := p.zone(ctx, domain)
	if e != nil {
		return e
	}
	_, e = providerCall(ctx, p.bound, "delete_dns_record", map[string]any{"zone_id": z, "record_id": recordID})
	return e
}
