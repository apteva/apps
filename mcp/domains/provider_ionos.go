package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ─── IONOS provider ────────────────────────────────────────────────
//
// IONOS's Hosting DNS API is zone-id oriented: records live under a
// zone identified by an opaque id, not by the domain name. So every op
// first resolves the domain to its zone id via list_zones, then reads
// the whole zone (get_zone) and does per-record CRUD by record id
// (create_records / update_record / delete_record).
//
// Record `name` is the full FQDN, the same convention Porkbun uses, so
// the canonical DNSRecord mapping and the wantFQ/sub matching below are
// identical in spirit to porkbunProvider.
//
// create_records takes a top-level JSON array body; the catalog tool
// declares body_root_param: "records" so the integration runner sends
// the `records` array verbatim as the request body.

type ionosProvider struct {
	bound  *sdk.BoundIntegration
	zoneID string
}

type ionosZoneRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type ionosRecord struct {
	ID       string `json:"id"`
	Name     string `json:"name"` // full FQDN, e.g. "mail.acme.com"
	RootName string `json:"rootName"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Prio     int    `json:"prio"`
	Disabled bool   `json:"disabled"`
}

type ionosZone struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Type    string        `json:"type"`
	Records []ionosRecord `json:"records"`
}

// zoneIDFor resolves a domain name to its IONOS zone id. list_zones
// returns a top-level array of {id,name,type}; match on name.
func (p *ionosProvider) zoneIDFor(ctx *sdk.AppCtx, domain string) (string, error) {
	if id := cachedZoneID(ctx, p.bound.ConnectionID, domain); id != "" {
		return id, nil
	}
	raw, err := providerCall(ctx, p.bound, "list_zones", map[string]any{})
	if err != nil {
		return "", err
	}
	var zones []ionosZoneRef
	if err := json.Unmarshal(raw, &zones); err != nil {
		return "", fmt.Errorf("parse ionos zones: %w", err)
	}
	for _, z := range zones {
		if strings.EqualFold(z.Name, domain) {
			if z.ID == "" {
				return "", errors.New("IONOS omitted zone ID")
			}
			cacheZoneID(ctx, p.bound.ConnectionID, domain, z.ID)
			return z.ID, nil
		}
	}
	return "", fmt.Errorf("no IONOS zone found for %q", domain)
}

// fetchZone resolves the zone id and pulls the full zone (id + records)
// in one place so Upsert/Delete have both without a second lookup.
func (p *ionosProvider) fetchZone(ctx *sdk.AppCtx, domain string) (*ionosZone, error) {
	zid, err := p.zoneIDFor(ctx, domain)
	if err != nil {
		return nil, err
	}
	raw, err := providerCall(ctx, p.bound, "get_zone", map[string]any{"zoneId": zid})
	if err != nil {
		cacheZoneID(ctx, p.bound.ConnectionID, domain, "")
		return nil, err
	}
	var z ionosZone
	if err := json.Unmarshal(raw, &z); err != nil {
		return nil, fmt.Errorf("parse ionos zone: %w", err)
	}
	if z.ID == "" || z.ID != zid || !strings.EqualFold(z.Name, domain) || z.Records == nil {
		cacheZoneID(ctx, p.bound.ConnectionID, domain, "")
		return nil, errors.New("IONOS omitted or mismatched zone details")
	}
	return &z, nil
}

func (p *ionosProvider) List(ctx *sdk.AppCtx, domain string) ([]DNSRecord, error) {
	z, err := p.fetchZone(ctx, domain)
	if err != nil {
		return nil, err
	}
	p.zoneID = z.ID
	out := make([]DNSRecord, 0, len(z.Records))
	for _, r := range z.Records {
		out = append(out, DNSRecord{
			ID:       r.ID,
			Name:     r.Name,
			Type:     strings.ToUpper(r.Type),
			Value:    r.Content,
			TTL:      r.TTL,
			Prio:     r.Prio,
			Disabled: r.Disabled,
		})
	}
	return out, nil
}

// ionosSplitMX pulls the priority out of an MX value of the form
// "<prio> <host>". When no priority is present it defaults to 10 — IONOS
// rejects MX/SRV records without one, mirroring namecheapProvider.
func ionosSplitMX(rtype, value string) (string, int) {
	content, prio, _ := priorityValue(rtype, value, nil)
	return content, prio
}

func (p *ionosProvider) Upsert(ctx *sdk.AppCtx, domain, sub, rtype, value string, ttl int, recordID string, existing []DNSRecord) (string, error) {
	if p.zoneID == "" {
		return "", errors.New("IONOS zone context missing; list records before mutation")
	}
	wantFQ := domain
	if sub != "" {
		wantFQ = sub + "." + domain
	}
	content, prio, parseErr := priorityValue(rtype, value, nil)
	if parseErr != nil {
		return "", parseErr
	}

	matches := recordsAtName(existing, domain, sub, rtype)
	var match *DNSRecord
	creating := recordID == createRecordID
	if creating {
		matches = nil
		recordID = ""
	}

	if recordID != "" {
		var err error
		match, err = selectedRecord(matches, recordID)
		if err != nil {
			return "", err
		}
	} else if len(matches) > 1 {
		return "", ambiguousRRSetError(domain, sub, rtype, len(matches))
	} else if len(matches) == 1 {
		match = &matches[0]
	}
	if match != nil && (rtype == "MX" || rtype == "SRV") && !hasNumericPrefix(value) {
		prio = match.Prio
		content = strings.TrimSpace(value)
	}

	if match != nil {
		if recordValueEqual(rtype, match.Value, content) &&
			match.TTL == ttl && match.Prio == prio {
			return "unchanged", nil
		}
		payload := map[string]any{
			"zoneId":   p.zoneID,
			"recordId": match.ID,
			"content":  content,
			"ttl":      ttl,
			"disabled": match.Disabled,
		}
		if rtype == "MX" || rtype == "SRV" {
			payload["prio"] = prio
		}
		if _, err := providerCall(ctx, p.bound, "update_record", payload); err != nil {
			return "", fmt.Errorf("update: %w", err)
		}
		return "updated", nil
	}

	rec := map[string]any{
		"name":    wantFQ,
		"type":    rtype,
		"content": content,
		"ttl":     ttl,
	}
	if rtype == "MX" || rtype == "SRV" {
		rec["prio"] = prio
	}
	createPayload := map[string]any{
		"zoneId":  p.zoneID,
		"records": []any{rec},
	}
	if _, err := providerCall(ctx, p.bound, "create_records", createPayload); err != nil {
		return "", fmt.Errorf("create: %w", err)
	}
	return "created", nil
}

func (p *ionosProvider) Delete(ctx *sdk.AppCtx, domain, sub, rtype, recordID string, existing []DNSRecord) error {
	if p.zoneID == "" {
		return errors.New("IONOS zone context missing; list records before mutation")
	}
	var ids []string
	matches := recordsAtName(existing, domain, sub, rtype)
	if recordID != "" {
		selected, err := selectedRecord(matches, recordID)
		if err != nil {
			return err
		}
		ids = append(ids, selected.ID)
	} else {
		for _, r := range matches {
			ids = append(ids, r.ID)
		}
	}
	for _, id := range ids {
		if _, err := providerCall(ctx, p.bound, "delete_record", map[string]any{
			"zoneId":   p.zoneID,
			"recordId": id,
		}); err != nil {
			return fmt.Errorf("delete record %s: %w", id, err)
		}
	}
	return nil
}
