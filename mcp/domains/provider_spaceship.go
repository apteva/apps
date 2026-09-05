package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ─── Spaceship provider ───────────────────────────────────────────
//
// Spaceship DNS works with batch "save" and "delete" operations. The
// integration catalog exposes those as list_dns_records, save_dns_records,
// and delete_dns_records, so this adapter stays inside the integration
// boundary and never talks to Spaceship directly.

type spaceshipProvider struct{ bound *sdk.BoundIntegration }

type spaceshipRegistrar struct{ bound *sdk.BoundIntegration }

func (r *spaceshipRegistrar) CheckAvailability(ctx *sdk.AppCtx, domain string) (*DomainAvailability, error) {
	raw, err := providerCall(ctx, r.bound, "check_single_domain_availability", map[string]any{"domain": domain})
	if err != nil {
		return nil, err
	}
	out := parseSpaceshipAvailability(domain, r.bound, raw)
	if !out.Known {
		return nil, errors.New("Spaceship omitted availability")
	}
	return &out, nil
}

func (r *spaceshipRegistrar) Pricing(*sdk.AppCtx, string) (any, error) {
	return nil, errors.New("Spaceship pricing is not exposed in domains; paid registration flows are disabled for this provider")
}

func (r *spaceshipRegistrar) Register(*sdk.AppCtx, DomainRegistrationRequest) (json.RawMessage, error) {
	return nil, errors.New("Spaceship registration is intentionally not supported by domains because it would spend money; use Porkbun for domain_register")
}

func (p *spaceshipProvider) List(ctx *sdk.AppCtx, domain string) ([]DNSRecord, error) {
	const pageSize = 500
	all := make([]DNSRecord, 0, pageSize)
	seenPages := map[string]bool{}
	for skip := 0; skip < 100000; skip += pageSize {
		select {
		case <-ctx.Done():
			return nil, errors.New("listing cancelled")
		default:
		}
		raw, err := providerCall(ctx, p.bound, "list_dns_records", map[string]any{
			"domain": domain, "take": pageSize, "skip": skip,
		})
		if err != nil {
			return nil, err
		}
		page, err := parseSpaceshipRecords(domain, raw)
		if err != nil {
			return nil, err
		}
		if len(page) > 0 {
			bytes, _ := json.Marshal(page)
			hash := recordHash(bytes)
			if seenPages[hash] {
				return nil, errors.New("provider repeated a DNS page")
			}
			seenPages[hash] = true
		}
		all = append(all, page...)
		if len(page) < pageSize {
			return all, nil
		}
	}
	return nil, errors.New("DNS record listing exceeds safety limit")
}

func (p *spaceshipProvider) Upsert(ctx *sdk.AppCtx, domain, sub, rtype, value string, ttl int, recordID string, existing []DNSRecord) (string, error) {
	content, prio := spaceshipCanonicalValuePrio(rtype, value)
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
		value = fmt.Sprintf("%d %s", match.Prio, strings.TrimSpace(value))
		content, prio = spaceshipCanonicalValuePrio(rtype, value)
	}
	if match != nil &&
		recordValueEqual(rtype, match.Value, content) &&
		match.TTL == ttl && match.Prio == prio {
		return "unchanged", nil
	}
	item, err := spaceshipRecordItem(domain, sub, rtype, value, ttl)
	if err != nil {
		return "", err
	}
	if match != nil && (!recordValueEqual(rtype, match.Value, content) || match.Prio != prio) {
		if err := p.replace(ctx, domain, *match, item); err != nil {
			return "", err
		}
		return "updated", nil
	}
	if _, err := providerCall(ctx, p.bound, "save_dns_records", map[string]any{"domain": domain, "items": []any{item}}); err != nil {
		return "", fmt.Errorf("save: %w", err)
	}

	if match != nil {
		return "updated", nil
	}
	return "created", nil
}

func (p *spaceshipProvider) Delete(ctx *sdk.AppCtx, domain, sub, rtype, recordID string, existing []DNSRecord) error {
	if recordID != "" {
		r, err := selectedRecord(recordsAtName(existing, domain, sub, rtype), recordID)
		if err != nil {
			return err
		}
		existing = []DNSRecord{*r}
	}

	records := make([]any, 0, 1)
	for _, r := range existing {
		if !strings.EqualFold(r.Type, rtype) {
			continue
		}
		if !spaceshipRecordNameMatches(r.Name, domain, sub) {
			continue
		}
		if recordID != "" && r.ID != recordID {
			continue
		}
		records = append(records, spaceshipDeleteItem(r))
	}
	if recordID != "" && len(records) == 0 {
		return fmt.Errorf("record_id %q not found in %s %s RRset", recordID, sub, rtype)
	}
	if len(records) == 0 {
		return nil
	}
	for start := 0; start < len(records); start += 500 {
		end := start + 500
		if end > len(records) {
			end = len(records)
		}
		if _, err := providerCall(ctx, p.bound, "delete_dns_records", map[string]any{"domain": domain, "records": records[start:end]}); err != nil {
			return fmt.Errorf("delete stopped after %d records: %w", start, err)
		}
	}
	return nil
}

func parseSpaceshipRecords(domain string, raw json.RawMessage) ([]DNSRecord, error) {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("parse Spaceship DNS records: %w", err)
	}
	items := spaceshipArrayFrom(root, "items", "records")
	if items == nil {
		return nil, errors.New("parse Spaceship DNS records: response contains no items or records array")
	}
	out := make([]DNSRecord, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("malformed DNS record item")
		}
		rtype := strings.ToUpper(spaceshipStringField(m, "type"))
		name := spaceshipStringField(m, "name")
		value, prio := spaceshipRecordValue(rtype, m)
		out = append(out, DNSRecord{
			ID:    spaceshipRecordID(m, rtype, name, value),
			Name:  spaceshipOwner(m, domain),
			Type:  rtype,
			Value: value,
			TTL:   spaceshipIntField(m, "ttl"),
			Prio:  prio,
			Raw:   copyStringAnyMap(m),
		})
	}
	return out, nil
}

func parseSpaceshipAvailability(domain string, bound *sdk.BoundIntegration, raw json.RawMessage) DomainAvailability {
	out := DomainAvailability{
		Domain:       domain,
		Provider:     bound.AppSlug,
		ConnectionID: bound.ConnectionID,
		Source:       "spaceship",
		Confidence:   "provider",
		Raw:          raw,
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return out
	}
	scopes := []map[string]any{root}
	for _, key := range []string{"response", "data", "result"} {
		if nested, ok := root[key].(map[string]any); ok {
			scopes = append(scopes, nested)
		}
	}
	if v, ok := firstBool(scopes, "available", "isAvailable"); ok {
		out.Available = v
		out.Known = true
	} else if s := firstString(scopes, "availability", "status"); s != "" {
		out.Available = availabilityStringIsAvailable(s)
		out.Known = includes([]string{"yes", "no", "true", "false", "available", "unavailable", "taken", "1", "0"}, strings.ToLower(s))
	}
	if s := firstString(scopes, "price", "registrationPrice"); s != "" {
		out.Price = s
	}
	if s := firstString(scopes, "currency"); s != "" {
		out.Currency = s
	}
	if v, ok := firstBool(scopes, "premium", "isPremium"); ok {
		out.Premium = v
	}
	return out
}

func spaceshipArrayFrom(root any, keys ...string) []any {
	switch v := root.(type) {
	case []any:
		return v
	case map[string]any:
		for _, key := range keys {
			if arr, ok := v[key].([]any); ok {
				return arr
			}
		}
		for _, key := range []string{"data", "result", "response"} {
			if nested, ok := v[key]; ok {
				if arr := spaceshipArrayFrom(nested, keys...); arr != nil {
					return arr
				}
			}
		}
	}
	return nil
}

func spaceshipCanonicalName(domain, name string) string {
	name = strings.TrimSpace(strings.TrimSuffix(name, "."))
	if name == "" || name == "@" {
		return domain
	}
	return strings.ToLower(name)
}

func spaceshipRecordNameMatches(name, domain, sub string) bool {
	name = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(name), "."))
	sub = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(sub), "."))
	domain = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(domain), "."))
	if sub == "" {
		return name == "" || name == "@" || name == domain
	}
	wantFQ := sub + "." + domain
	return name == sub || name == wantFQ
}

func spaceshipStringField(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			switch t := v.(type) {
			case string:
				if strings.TrimSpace(t) != "" {
					return strings.TrimSpace(t)
				}
			case float64:
				return strconv.FormatFloat(t, 'f', -1, 64)
			case int:
				return strconv.Itoa(t)
			}
		}
	}
	return ""
}

func spaceshipIntField(m map[string]any, keys ...string) int {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			switch t := v.(type) {
			case int:
				return t
			case float64:
				return int(t)
			case string:
				n, _ := strconv.Atoi(strings.TrimSpace(t))
				return n
			}
		}
	}
	return 0
}

func spaceshipRecordValue(rtype string, m map[string]any) (string, int) {
	switch rtype {
	case "TXT":
		v, _ := m["value"].(string)
		return v, 0
	case "MX":
		return spaceshipStringField(m, "exchange", "value", "target"), spaceshipIntField(m, "preference", "priority")
	case "SRV":
		prio := spaceshipIntField(m, "priority")
		weight := spaceshipIntField(m, "weight")
		port := spaceshipIntField(m, "port")
		target := spaceshipStringField(m, "target", "value")
		if target != "" {
			return fmt.Sprintf("%d %d %s", weight, port, target), prio
		}
		return "", prio
	case "CAA":
		flag := spaceshipIntField(m, "flag")
		tag := spaceshipStringField(m, "tag")
		value := spaceshipStringField(m, "value")
		if tag != "" && value != "" {
			return fmt.Sprintf("%d %s %s", flag, tag, value), 0
		}
		return value, 0
	default:
		return spaceshipStringField(m, "address", "value", "cname", "exchange", "nameserver", "aliasName", "pointer", "target", "targetName"), spaceshipIntField(m, "priority", "preference")
	}
}

func spaceshipCanonicalValuePrio(rtype, value string) (string, int) {
	if rtype == "TXT" {
		return value, 0
	}
	value = strings.TrimSpace(value)
	switch rtype {
	case "MX":
		parts := strings.SplitN(value, " ", 2)
		if len(parts) == 2 {
			if prio, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
				return strings.TrimSpace(parts[1]), prio
			}
		}
		return value, 10
	case "SRV":
		parts := strings.Fields(value)
		if len(parts) >= 4 {
			if prio, err := strconv.Atoi(parts[0]); err == nil {
				return strings.Join(parts[1:], " "), prio
			}
		}
	}
	return value, 0
}

func hasNumericPrefix(value string) bool {
	fields := strings.Fields(value)
	if len(fields) < 2 {
		return false
	}
	_, err := strconv.Atoi(fields[0])
	return err == nil
}

func spaceshipRecordItem(domain, sub, rtype, value string, ttl int) (map[string]any, error) {
	name := "@"
	if sub != "" {
		name = sub
	}
	item := map[string]any{
		"type": rtype,
		"name": name,
	}
	if ttl > 0 {
		item["ttl"] = ttl
	}
	content, prio := spaceshipCanonicalValuePrio(rtype, value)
	switch rtype {
	case "A", "AAAA":
		item["address"] = content
	case "TXT":
		item["value"] = content
	case "CNAME":
		item["cname"] = content
	case "MX":
		item["exchange"] = content
		item["preference"] = prio
	case "NS":
		item["nameserver"] = content
	case "ALIAS":
		item["aliasName"] = content
	case "PTR":
		item["pointer"] = content
	case "CAA":
		parts := strings.Fields(content)
		if len(parts) < 3 {
			return nil, errors.New("Spaceship CAA value must be '<flag> <tag> <value>', for example '0 issue letsencrypt.org'")
		}
		flag, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("Spaceship CAA flag must be numeric: %w", err)
		}
		item["flag"] = flag
		item["tag"] = parts[1]
		item["value"] = strings.Join(parts[2:], " ")
	case "SRV":
		owner := strings.SplitN(sub, ".", 3)
		if len(owner) < 2 || !strings.HasPrefix(owner[0], "_") || !strings.HasPrefix(owner[1], "_") {
			return nil, errors.New("SRV name must start with _service._protocol")
		}
		item["service"], item["protocol"] = owner[0], owner[1]
		item["name"] = "@"
		if len(owner) == 3 {
			item["name"] = owner[2]
		}
		parts := strings.Fields(value)
		if len(parts) < 4 {
			return nil, errors.New("Spaceship SRV value must be '<priority> <weight> <port> <target>'")
		}
		priority, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("Spaceship SRV priority must be numeric: %w", err)
		}
		weight, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("Spaceship SRV weight must be numeric: %w", err)
		}
		port, err := strconv.Atoi(parts[2])
		if err != nil {
			return nil, fmt.Errorf("Spaceship SRV port must be numeric: %w", err)
		}
		item["priority"] = priority
		item["weight"] = weight
		item["port"] = port
		item["target"] = strings.Join(parts[3:], " ")
	default:
		return nil, fmt.Errorf("Spaceship DNS write support is not implemented for %s records", rtype)
	}
	_ = domain
	return item, nil
}

func spaceshipDeleteItem(r DNSRecord) map[string]any {
	if len(r.Raw) > 0 {
		item := copyStringAnyMap(r.Raw)
		delete(item, "ttl")
		delete(item, "group")
		delete(item, "id")
		delete(item, "recordId")
		return item
	}
	item := map[string]any{
		"type": r.Type,
		"name": r.Name,
	}
	if r.Prio != 0 {
		item["priority"] = r.Prio
	}
	switch r.Type {
	case "A", "AAAA":
		item["address"] = r.Value
	case "TXT", "CAA":
		item["value"] = r.Value
	case "CNAME":
		item["cname"] = r.Value
	case "MX":
		item["exchange"] = r.Value
		item["preference"] = r.Prio
	case "NS":
		item["nameserver"] = r.Value
	case "ALIAS":
		item["aliasName"] = r.Value
	case "PTR":
		item["pointer"] = r.Value
	default:
		item["value"] = r.Value
	}
	return item
}

func spaceshipRecordID(m map[string]any, rtype, name, value string) string {
	if id := spaceshipStringField(m, "id", "recordId"); id != "" {
		return id
	}
	identity := copyStringAnyMap(m)
	delete(identity, "ttl")
	delete(identity, "group")
	identity["name"] = strings.ToLower(strings.TrimSuffix(name, "."))
	identity["type"] = rtype
	raw, _ := json.Marshal(identity)
	return recordHash(raw)
}

func copyStringAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
