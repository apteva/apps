package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── Provider response normalisation ──────────────────────────────

func parsePorkbunRecords(raw json.RawMessage) ([]DNSRecord, error) {
	var probe struct {
		Status   string   `json:"status"`
		Warnings []string `json:"warnings"`
		Records  []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Type    string `json:"type"`
			Content string `json:"content"`
			TTL     string `json:"ttl"`
			Prio    string `json:"prio"`
			Notes   string `json:"notes"`
		} `json:"records"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("parse Porkbun DNS records: %w", err)
	}
	if !strings.EqualFold(probe.Status, "SUCCESS") {
		return nil, fmt.Errorf("parse Porkbun DNS records: unexpected status %q", probe.Status)
	}
	if probe.Records == nil {
		return nil, errors.New("Porkbun omitted DNS records")
	}
	out := make([]DNSRecord, 0, len(probe.Records))
	for _, r := range probe.Records {
		ttl, _ := strconv.Atoi(r.TTL)
		prio, _ := strconv.Atoi(r.Prio)
		out = append(out, DNSRecord{
			ID:    r.ID,
			Name:  r.Name,
			Type:  strings.ToUpper(r.Type),
			Value: r.Content,
			TTL:   ttl,
			Prio:  prio,
			Notes: r.Notes, Warnings: probe.Warnings,
		})
	}
	return out, nil
}

func parsePorkbunAvailability(domain string, bound *sdk.BoundIntegration, raw json.RawMessage) DomainAvailability {
	out := DomainAvailability{
		Domain:       domain,
		Provider:     bound.AppSlug,
		ConnectionID: bound.ConnectionID,
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
	if v, ok := firstBool(scopes, "available"); ok {
		out.Available = v
		out.Known = true
	} else if s := firstString(scopes, "avail", "available", "availability"); s != "" {
		out.Available = availabilityStringIsAvailable(s)
		out.Known = includes([]string{"yes", "no", "true", "false", "available", "unavailable", "taken", "1", "0"}, strings.ToLower(s))
	}
	if s := firstString(scopes, "price", "registration", "registrationPrice", "premiumRegistrationPrice"); s != "" {
		out.Price = s
		out.Currency = "USD"
	}
	if v, ok := firstBool(scopes, "premium", "isPremium", "IsPremiumName"); ok {
		out.Premium = v
	} else if s := firstString(scopes, "type"); strings.Contains(strings.ToLower(s), "premium") {
		out.Premium = true
	}
	for _, scope := range scopes {
		if v, ok := scope["minDuration"]; ok {
			n, err := strictIntArg(map[string]any{"minDuration": v}, "minDuration", 0, 1, 10)
			if err == nil {
				out.MinDuration = n
			}
		}
	}

	return out
}

func firstString(scopes []map[string]any, keys ...string) string {
	for _, m := range scopes {
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
				case bool:
					if t {
						return "true"
					}
					return "false"
				}
			}
		}
	}
	return ""
}

func firstBool(scopes []map[string]any, keys ...string) (bool, bool) {
	for _, m := range scopes {
		for _, key := range keys {
			if v, ok := m[key]; ok {
				switch t := v.(type) {
				case bool:
					return t, true
				case string:
					s := strings.ToLower(strings.TrimSpace(t))
					switch s {
					case "true", "yes", "y", "1", "available":
						return true, true
					case "false", "no", "n", "0", "unavailable", "taken":
						return false, true
					}
				}
			}
		}
	}
	return false, false
}

func availabilityStringIsAvailable(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "y", "1", "available":
		return true
	default:
		return false
	}
}

func pricingEntryForTLD(raw json.RawMessage, tld string) any {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	tld = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(tld)), ".")
	if m, ok := root.(map[string]any); ok {
		if entry := mapLookupTLD(m, tld); entry != nil {
			return entry
		}
		for _, key := range []string{"pricing", "prices", "response", "data"} {
			if nested, ok := m[key].(map[string]any); ok {
				if entry := mapLookupTLD(nested, tld); entry != nil {
					return entry
				}
			}
		}
	}
	return nil
}

func mapLookupTLD(m map[string]any, tld string) any {
	for _, key := range []string{tld, "." + tld} {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return nil
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

var rdapLookupBaseURL = "https://rdap.org/domain/"

func publicRDAPAvailability(domain string, primaryErr error) (*DomainAvailability, error) {
	client := &http.Client{Timeout: 6 * time.Second}
	req, err := http.NewRequest(http.MethodGet, rdapLookupBaseURL+domain, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/rdap+json, application/json")
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("availability check failed: %w; RDAP fallback failed: %w", primaryErr, err)
	}
	defer res.Body.Close()
	warning := fmt.Sprintf("Registrar availability check failed (%s). Used public RDAP fallback; availability is best-effort and final registration is still performed by Porkbun.", primaryErr)
	switch res.StatusCode {
	case http.StatusOK:
		var raw json.RawMessage
		_ = json.NewDecoder(res.Body).Decode(&raw)
		return &DomainAvailability{
			Domain:     domain,
			Available:  false,
			Known:      true,
			Provider:   "rdap",
			Source:     "rdap",
			Confidence: "high",
			Warning:    warning,
			Raw:        raw,
		}, nil
	case http.StatusNotFound:
		return &DomainAvailability{
			Domain:     domain,
			Available:  false,
			Known:      false,
			Provider:   "rdap",
			Source:     "rdap",
			Confidence: "unknown",
			Warning:    warning,
		}, nil
	default:
		return nil, fmt.Errorf("availability check failed: %w; RDAP fallback returned HTTP %d", primaryErr, res.StatusCode)
	}
}

func filterRecords(in []DNSRecord, keep func(DNSRecord) bool) []DNSRecord {
	out := make([]DNSRecord, 0, len(in))
	for _, r := range in {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

func recordsAtName(records []DNSRecord, domain, sub, rtype string) []DNSRecord {
	wantFQ := domain
	if sub != "" {
		wantFQ = sub + "." + domain
	}
	return filterRecords(records, func(r DNSRecord) bool {
		if !strings.EqualFold(r.Type, rtype) {
			return false
		}
		if strings.EqualFold(r.Name, wantFQ) || strings.EqualFold(r.Name, sub) {
			return true
		}
		return sub == "" && (r.Name == "" || r.Name == "@")
	})
}

func selectedRecord(records []DNSRecord, recordID string) (*DNSRecord, error) {
	var found *DNSRecord
	for i := range records {
		if records[i].ID == recordID {
			if found != nil {
				return nil, apiError(409, "record ID is ambiguous; refresh records")
			}
			found = &records[i]
		}
	}
	if found == nil {
		return nil, apiError(409, fmt.Sprintf("record_id %q was not found in the requested RRset", recordID))
	}
	return found, nil
}

func ambiguousRRSetError(domain, sub, rtype string, count int) error {
	name := sub
	if name == "" {
		name = "@"
	}
	return fmt.Errorf("%s %s.%s has %d records; pass record_id from domain_records_list to update exactly one", rtype, name, domain, count)
}
