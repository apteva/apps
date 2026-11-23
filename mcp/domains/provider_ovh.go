package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

// ─── OVHcloud provider ─────────────────────────────────────────────
//
// OVH zones are keyed by the domain name (zoneName) and records by an
// integer id. zone_record_list only returns ids, so List fetches each
// record with zone_record_get (bounded concurrency). Record changes are
// staged until zone_refresh is called, so every mutation ends with a
// refresh; a failed refresh is reported as an error because the change
// is saved at OVH but not yet served.
//
// MX/SRV priorities live inside OVH's target ("10 mx.example.com."), so
// they are split into DNSRecord.Prio on read and joined back on write.
// OVH's SPF/DKIM/DMARC pseudo-types are TXT records on the wire and are
// exposed as TXT with the original type kept in Notes.

type ovhProvider struct{ bound *sdk.BoundIntegration }

const ovhRecordFetchConcurrency = 6

func (p *ovhProvider) List(ctx *sdk.AppCtx, domain string) ([]DNSRecord, error) {
	raw, err := providerCall(ctx, p.bound, "zone_record_list", map[string]any{"zoneName": domain})
	if err != nil {
		return nil, err
	}
	ids, err := parseOVHRecordIDs(raw)
	if err != nil {
		return nil, err
	}
	out := make([]DNSRecord, len(ids))
	errs := make([]error, len(ids))
	sem := make(chan struct{}, ovhRecordFetchConcurrency)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id int64) {
			defer wg.Done()
			defer func() { <-sem }()
			body, err := providerCall(ctx, p.bound, "zone_record_get", map[string]any{"zoneName": domain, "id": id})
			if err != nil {
				errs[i] = err
				return
			}
			out[i], errs[i] = parseOVHRecord(domain, body)
		}(i, id)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func parseOVHRecordIDs(raw json.RawMessage) ([]int64, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var nums []json.Number
	if err := dec.Decode(&nums); err != nil {
		return nil, fmt.Errorf("OVH record list was not an array of ids: %w", err)
	}
	ids := make([]int64, 0, len(nums))
	for _, n := range nums {
		id, err := n.Int64()
		if err != nil {
			return nil, fmt.Errorf("invalid OVH record id %q", n.String())
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func parseOVHRecord(domain string, raw json.RawMessage) (DNSRecord, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return DNSRecord{}, fmt.Errorf("parse OVH record: %w", err)
	}
	id, ok := m["id"].(json.Number)
	if !ok || id.String() == "" {
		return DNSRecord{}, errors.New("OVH record omitted its id")
	}
	fieldType := strings.ToUpper(strings.TrimSpace(fmt.Sprint(m["fieldType"])))
	if fieldType == "" || fieldType == "<NIL>" {
		return DNSRecord{}, errors.New("OVH record omitted its fieldType")
	}
	sub, _ := m["subDomain"].(string)
	sub = strings.TrimSuffix(strings.TrimSpace(sub), ".")
	name := domain
	if sub != "" && sub != "@" {
		name = sub + "." + domain
	}
	target, _ := m["target"].(string)
	rec := DNSRecord{ID: id.String(), Name: name, Type: fieldType, Value: target, TTL: ovhInt(m["ttl"]), Raw: m}
	switch fieldType {
	case "SPF", "DKIM", "DMARC":
		rec.Type = "TXT"
		rec.Notes = "OVH " + fieldType + " record"
	}
	switch rec.Type {
	case "MX", "SRV":
		if content, prio, err := priorityValue(rec.Type, target, nil); err == nil {
			rec.Value, rec.Prio = content, prio
		}
	case "TXT":
		rec.Value = unquoteTXT(target)
	}
	return rec, nil
}

func ovhInt(v any) int {
	switch x := v.(type) {
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case float64:
		return int(x)
	}
	return 0
}

func ovhTarget(rtype, content string, prio int) string {
	if rtype == "MX" || rtype == "SRV" {
		return strconv.Itoa(prio) + " " + content
	}
	return content
}

func ovhRecordID(id string) (int64, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid OVH record id %q", id)
	}
	return n, nil
}

func (p *ovhProvider) refresh(ctx *sdk.AppCtx, domain string) error {
	if _, err := providerCall(ctx, p.bound, "zone_refresh", map[string]any{"zoneName": domain}); err != nil {
		return fmt.Errorf("record saved at OVH but zone refresh failed (change not live yet; retry the same edit to refresh): %w", err)
	}
	return nil
}

func (p *ovhProvider) Upsert(ctx *sdk.AppCtx, domain, sub, rtype, value string, ttl int, recordID string, existing []DNSRecord) (string, error) {
	content, prio, err := priorityValue(rtype, value, nil)
	if err != nil {
		return "", err
	}
	matches := recordsAtName(existing, domain, sub, rtype)
	if recordID == createRecordID {
		matches = nil
		recordID = ""
	}
	var match *DNSRecord
	if recordID != "" {
		if match, err = selectedRecord(matches, recordID); err != nil {
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
		if recordValueEqual(rtype, match.Value, content) && match.TTL == ttl && match.Prio == prio {
			return "unchanged", nil
		}
		id, err := ovhRecordID(match.ID)
		if err != nil {
			return "", err
		}
		if _, err := providerCall(ctx, p.bound, "zone_record_update", map[string]any{
			"zoneName": domain,
			"id":       id,
			"target":   ovhTarget(rtype, content, prio),
			"ttl":      ttl,
		}); err != nil {
			return "", fmt.Errorf("update: %w", err)
		}
		return "updated", p.refresh(ctx, domain)
	}

	if _, err := providerCall(ctx, p.bound, "zone_record_create", map[string]any{
		"zoneName":  domain,
		"fieldType": rtype,
		"subDomain": sub,
		"target":    ovhTarget(rtype, content, prio),
		"ttl":       ttl,
	}); err != nil {
		return "", fmt.Errorf("create: %w", err)
	}
	return "created", p.refresh(ctx, domain)
}

func (p *ovhProvider) Delete(ctx *sdk.AppCtx, domain, sub, rtype, recordID string, existing []DNSRecord) error {
	matches := recordsAtName(existing, domain, sub, rtype)
	if recordID != "" {
		selected, err := selectedRecord(matches, recordID)
		if err != nil {
			return err
		}
		matches = []DNSRecord{*selected}
	}
	if len(matches) == 0 {
		return nil
	}
	for _, r := range matches {
		id, err := ovhRecordID(r.ID)
		if err != nil {
			return err
		}
		if _, err := providerCall(ctx, p.bound, "zone_record_delete", map[string]any{"zoneName": domain, "id": id}); err != nil {
			return fmt.Errorf("delete record %s: %w", r.ID, err)
		}
	}
	return p.refresh(ctx, domain)
}
