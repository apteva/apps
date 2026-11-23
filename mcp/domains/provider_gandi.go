package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ─── Gandi LiveDNS provider ────────────────────────────────────────
//
// LiveDNS stores RRsets: one (rrset_name, rrset_type) pair holds a TTL
// and a list of values. The domains app works with individual records,
// so List expands every value into its own DNSRecord with a synthetic,
// value-derived ID, and mutations rewrite the whole RRset:
//
//   - create (or upsert on an empty name/type) → POST create_record
//   - update one value / append a value        → PUT replace_record with
//     the full value list (other values are preserved verbatim)
//   - delete one value                         → PUT the remaining values,
//     or DELETE when none remain
//   - delete without record_id                 → DELETE the whole RRset
//
// TTL is per RRset at Gandi, so changing one value's TTL applies to
// every value at that name and type. Values use zone-file syntax: MX/SRV
// carry their priority inline and TXT values are quoted strings.

type gandiProvider struct{ bound *sdk.BoundIntegration }

const (
	gandiPageSize = 100
	gandiMaxPages = 100
)

func (p *gandiProvider) List(ctx *sdk.AppCtx, domain string) ([]DNSRecord, error) {
	var out []DNSRecord
	for page := 1; page <= gandiMaxPages; page++ {
		raw, err := providerCall(ctx, p.bound, "list_records", map[string]any{"fqdn": domain, "page": page, "per_page": gandiPageSize})
		if err != nil {
			return nil, err
		}
		records, rrsets, err := parseGandiRecords(domain, raw)
		if err != nil {
			return nil, err
		}
		out = append(out, records...)
		if rrsets < gandiPageSize {
			return out, nil
		}
	}
	return nil, fmt.Errorf("Gandi zone %s has more than %d RRsets; refusing a partial record list", domain, gandiPageSize*gandiMaxPages)
}

type gandiRRSet struct {
	Name   string   `json:"rrset_name"`
	Type   string   `json:"rrset_type"`
	TTL    int      `json:"rrset_ttl"`
	Values []string `json:"rrset_values"`
}

// parseGandiRecords returns the expanded records and the RRset count
// (the unit Gandi paginates on).
func parseGandiRecords(domain string, raw json.RawMessage) ([]DNSRecord, int, error) {
	var sets []gandiRRSet
	if err := json.Unmarshal(raw, &sets); err != nil {
		return nil, 0, fmt.Errorf("Gandi record list was not an RRset array: %w", err)
	}
	var out []DNSRecord
	for _, s := range sets {
		if s.Name == "" || s.Type == "" {
			return nil, 0, errors.New("Gandi RRset omitted its name or type")
		}
		rtype := strings.ToUpper(s.Type)
		name := domain
		if s.Name != "@" {
			name = strings.TrimSuffix(s.Name, ".") + "." + domain
		}
		for _, v := range s.Values {
			rec := DNSRecord{
				ID:    gandiRecordID(s.Name, rtype, v),
				Name:  name,
				Type:  rtype,
				Value: v,
				TTL:   s.TTL,
				Raw:   map[string]any{"rrset_name": s.Name, "rrset_type": rtype, "rrset_ttl": s.TTL, "rrset_value": v},
			}
			switch rtype {
			case "MX", "SRV":
				if content, prio, err := priorityValue(rtype, v, nil); err == nil {
					rec.Value, rec.Prio = content, prio
				}
			case "TXT":
				rec.Value = unquoteTXT(v)
			}
			out = append(out, rec)
		}
	}
	return out, len(sets), nil
}

func gandiRecordID(name, rtype, value string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(name) + "\x00" + rtype + "\x00" + value))
	return "gandi-" + hex.EncodeToString(sum[:8])
}

func gandiValue(rtype, content string, prio int) string {
	switch rtype {
	case "MX", "SRV":
		return strconv.Itoa(prio) + " " + content
	case "TXT":
		return quoteTXT(content)
	}
	return content
}

func gandiRRSetName(sub string) string {
	if sub == "" {
		return "@"
	}
	return sub
}

func gandiRawValue(r DNSRecord) (string, error) {
	v, ok := r.Raw["rrset_value"].(string)
	if !ok {
		return "", errors.New("Gandi record is missing its raw value; refresh records before editing")
	}
	return v, nil
}

func (p *gandiProvider) writeRRSet(ctx *sdk.AppCtx, domain, sub, rtype string, values []string, ttl int) error {
	_, err := providerCall(ctx, p.bound, "replace_record", map[string]any{
		"fqdn":         domain,
		"rrset_name":   gandiRRSetName(sub),
		"rrset_type":   rtype,
		"rrset_values": values,
		"rrset_ttl":    ttl,
	})
	return err
}

func (p *gandiProvider) Upsert(ctx *sdk.AppCtx, domain, sub, rtype, value string, ttl int, recordID string, existing []DNSRecord) (string, error) {
	content, prio, err := priorityValue(rtype, value, nil)
	if err != nil {
		return "", err
	}
	rrset := recordsAtName(existing, domain, sub, rtype)
	creating := recordID == createRecordID

	var match *DNSRecord
	switch {
	case creating:
	case recordID != "":
		if match, err = selectedRecord(rrset, recordID); err != nil {
			return "", err
		}
	case len(rrset) > 1:
		return "", ambiguousRRSetError(domain, sub, rtype, len(rrset))
	case len(rrset) == 1:
		match = &rrset[0]
	}
	if match != nil && (rtype == "MX" || rtype == "SRV") && !hasNumericPrefix(value) {
		prio = match.Prio
		content = strings.TrimSpace(value)
	}
	if match != nil && recordValueEqual(rtype, match.Value, content) && match.TTL == ttl && match.Prio == prio {
		return "unchanged", nil
	}

	newValue := gandiValue(rtype, content, prio)
	if len(rrset) == 0 {
		if _, err := providerCall(ctx, p.bound, "create_record", map[string]any{
			"fqdn":         domain,
			"rrset_name":   gandiRRSetName(sub),
			"rrset_type":   rtype,
			"rrset_values": []string{newValue},
			"rrset_ttl":    ttl,
		}); err != nil {
			return "", fmt.Errorf("create: %w", err)
		}
		return "created", nil
	}

	values := make([]string, 0, len(rrset)+1)
	for _, r := range rrset {
		raw, err := gandiRawValue(r)
		if err != nil {
			return "", err
		}
		if match != nil && r.ID == match.ID {
			raw = newValue
		}
		values = append(values, raw)
	}
	action := "updated"
	if match == nil {
		values = append(values, newValue)
		action = "created"
	}
	if err := p.writeRRSet(ctx, domain, sub, rtype, values, ttl); err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSuffix(action, "d"), err)
	}
	return action, nil
}

func (p *gandiProvider) Delete(ctx *sdk.AppCtx, domain, sub, rtype, recordID string, existing []DNSRecord) error {
	rrset := recordsAtName(existing, domain, sub, rtype)
	if len(rrset) == 0 {
		return nil
	}
	if recordID != "" {
		selected, err := selectedRecord(rrset, recordID)
		if err != nil {
			return err
		}
		remaining := make([]string, 0, len(rrset)-1)
		for _, r := range rrset {
			if r.ID == selected.ID {
				continue
			}
			raw, err := gandiRawValue(r)
			if err != nil {
				return err
			}
			remaining = append(remaining, raw)
		}
		if len(remaining) > 0 {
			return p.writeRRSet(ctx, domain, sub, rtype, remaining, selected.TTL)
		}
	}
	_, err := providerCall(ctx, p.bound, "delete_record", map[string]any{
		"fqdn":       domain,
		"rrset_name": gandiRRSetName(sub),
		"rrset_type": rtype,
	})
	return err
}

// ─── TXT zone-file quoting ─────────────────────────────────────────

// unquoteTXT turns zone-file TXT syntax (`"part one" "part two"`) into
// the plain concatenated value. Anything that is not a well-formed
// sequence of quoted strings is returned unchanged.
func unquoteTXT(v string) string {
	s := strings.TrimSpace(v)
	if !strings.HasPrefix(s, `"`) {
		return v
	}
	var b strings.Builder
	for len(s) > 0 {
		if s[0] != '"' {
			return v
		}
		i := 1
		closed := false
		for i < len(s) {
			c := s[i]
			if c == '\\' && i+1 < len(s) {
				b.WriteByte(s[i+1])
				i += 2
				continue
			}
			if c == '"' {
				closed = true
				i++
				break
			}
			b.WriteByte(c)
			i++
		}
		if !closed {
			return v
		}
		s = strings.TrimLeft(s[i:], " \t")
	}
	return b.String()
}

// quoteTXT renders a plain TXT value as zone-file quoted strings, split
// into 255-byte character-strings so long DKIM keys stay valid. A value
// that is already well-formed zone-file syntax is passed through.
func quoteTXT(v string) string {
	if unquoteTXT(v) != v {
		return strings.TrimSpace(v)
	}
	const chunk = 255
	escape := func(s string) string {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	}
	if len(v) <= chunk {
		return escape(v)
	}
	parts := make([]string, 0, len(v)/chunk+1)
	for len(v) > chunk {
		parts = append(parts, escape(v[:chunk]))
		v = v[chunk:]
	}
	parts = append(parts, escape(v))
	return strings.Join(parts, " ")
}
