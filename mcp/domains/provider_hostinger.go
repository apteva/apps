package main

import (
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
)

type hostingerProvider struct{ bound *sdk.BoundIntegration }

func hostingerRecords(raw json.RawMessage, domain string) ([]DNSRecord, error) {
	var root any
	if e := json.Unmarshal(raw, &root); e != nil {
		return nil, e
	}
	var arr []any
	if m, ok := root.(map[string]any); ok {
		for _, k := range []string{"records", "data", "result"} {
			if a, yes := m[k].([]any); yes {
				arr = a
				break
			}
		}
	} else if a, ok := root.([]any); ok {
		arr = a
	}
	if arr == nil {
		return nil, errors.New("Hostinger DNS response did not contain a records array")
	}
	out := make([]DNSRecord, 0, len(arr))
	for _, x := range arr {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		name := hostingerName(fmt.Sprint(m["name"]), domain)
		value := fmt.Sprint(m["content"])
		if value == "<nil>" {
			value = fmt.Sprint(m["value"])
		}
		out = append(out, DNSRecord{ID: fmt.Sprint(m["id"]), Name: name, Type: strings.ToUpper(fmt.Sprint(m["type"])), Value: value, TTL: intArg(m, "ttl", 600), Prio: intArg(m, "priority", 0), Raw: m})
	}
	return out, nil
}
func hostingerName(name, domain string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if name == "" || name == "@" {
		return domain
	}
	if strings.HasSuffix(name, "."+domain) {
		return name
	}
	return name
}
func (p *hostingerProvider) List(ctx *sdk.AppCtx, domain string) ([]DNSRecord, error) {
	raw, e := providerCall(ctx, p.bound, "dns_get", map[string]any{"domain": domain})
	if e != nil {
		return nil, e
	}
	return hostingerRecords(raw, domain)
}
func (p *hostingerProvider) Upsert(ctx *sdk.AppCtx, domain, sub, rtype, value string, ttl int, recordID string, existing []DNSRecord) (string, error) {
	name := sub
	if name == "" || name == domain {
		name = "@"
	}
	recs := make([]any, 0, len(existing)+1)
	updated := false
	for _, r := range existing {
		storedName := r.Name
		if storedName == domain {
			storedName = "@"
		}
		if strings.EqualFold(storedName, name) {
			if recordID != "" && r.ID != recordID {
				recs = append(recs, r.Raw)
				continue
			}
			if recordID == "" && !strings.EqualFold(r.Type, rtype) {
				recs = append(recs, r.Raw)
				continue
			}
			if r.Raw == nil {
				return "", errors.New("Hostinger returned an incomplete record; refusing a zone overwrite")
			}
			recs = append(recs, map[string]any{"type": rtype, "name": name, "content": value, "ttl": ttl})
			updated = true
		} else {
			recs = append(recs, r.Raw)
		}
	}
	if !updated {
		recs = append(recs, map[string]any{"type": rtype, "name": name, "content": value, "ttl": ttl})
	}
	_, e := providerCall(ctx, p.bound, "dns_update", map[string]any{"domain": domain, "records": recs, "overwrite": true})
	if e != nil {
		return "", e
	}
	if updated {
		return "updated", nil
	}
	return "created", nil
}
func (p *hostingerProvider) Delete(ctx *sdk.AppCtx, domain, sub, rtype, recordID string, existing []DNSRecord) error {
	var dels []any
	for _, r := range existing {
		if strings.EqualFold(r.Type, rtype) && (recordID == "" || r.ID == recordID) && (sub == "" || strings.EqualFold(r.Name, sub) || strings.EqualFold(r.Name, domain) || (sub == "@" && strings.EqualFold(r.Name, domain))) {
			dels = append(dels, map[string]any{"type": r.Type, "name": r.Name, "content": r.Value})
		}
	}
	if len(dels) == 0 {
		return errors.New("record not found")
	}
	_, e := providerCall(ctx, p.bound, "dns_delete", map[string]any{"domain": domain, "records": dels})
	return e
}
