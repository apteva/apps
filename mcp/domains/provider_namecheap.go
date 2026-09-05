package main

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ─── Namecheap provider ────────────────────────────────────────────
//
// Namecheap's API model is read-modify-write: getHosts returns the
// full list of records as XML; setHosts replaces them all atomically.
// So upsert is "list, modify in memory, write back the full set" —
// detect stale snapshots and preserve mail/CAA fields before replacement.
// External writers can still race the final read and write: the API has no CAS.
//
// Namecheap also requires (a) IP whitelisting on the API key and
// (b) the domain to be split into SLD ("acme") + TLD ("com").
//
// XML responses come back as JSON-encoded strings (the platform
// runner falls through non-JSON Content-Type to string).

type namecheapProvider struct {
	bound             *sdk.BoundIntegration
	emailType         string
	emailTypeOverride string
}

type namecheapHost struct {
	HostID  string `xml:"HostId,attr" json:"-"`
	Name    string `xml:"Name,attr"`
	Type    string `xml:"Type,attr"`
	Address string `xml:"Address,attr"`
	TTL     string `xml:"TTL,attr"`
	MXPref  string `xml:"MXPref,attr"`
	Flag    string `xml:"Flag,attr"`
	Tag     string `xml:"Tag,attr"`
}

// namecheapStatus is the Status + Errors envelope every Namecheap API
// response carries. Embed in per-command response structs to share the
// error-detection helper.
type namecheapStatus struct {
	Status string `xml:"Status,attr"`
	Errors struct {
		Errors []struct {
			Number string `xml:"Number,attr"`
			Text   string `xml:",chardata"`
		} `xml:"Error"`
	} `xml:"Errors"`
}

func (s namecheapStatus) err() error {
	if strings.EqualFold(s.Status, "OK") && len(s.Errors.Errors) == 0 {
		return nil
	}
	var msgs []string
	for _, e := range s.Errors.Errors {
		msgs = append(msgs, fmt.Sprintf("[%s] %s", e.Number, strings.TrimSpace(e.Text)))
	}
	if len(msgs) == 0 {
		return fmt.Errorf("namecheap error: status=%s (no details)", s.Status)
	}
	return fmt.Errorf("namecheap error: %s", strings.Join(msgs, "; "))
}

type namecheapHostsResponse struct {
	XMLName xml.Name `xml:"ApiResponse"`
	namecheapStatus
	CommandResponse struct {
		Hosts *namecheapHostResult `xml:"DomainDNSGetHostsResult"`
	} `xml:"CommandResponse"`
}

// xmlDataToString unwraps the runner's response shape: when the
// integration's response Content-Type isn't JSON, the runner stores
// the raw body as a JSON-encoded string. Strip the JSON quoting to
// get the original XML bytes.
func xmlDataToString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("empty response")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s, nil
		}
	}
	return string(raw), nil
}

func (n *namecheapProvider) callGetHosts(ctx *sdk.AppCtx, domain string) (*namecheapHostsResponse, error) {
	sld, tld := splitSLDTLD(domain)
	if sld == "" || tld == "" {
		return nil, fmt.Errorf("namecheap requires a 2-label domain (got %q)", domain)
	}
	raw, err := providerCall(ctx, n.bound, "get_dns_hosts", map[string]any{
		"SLD": sld,
		"TLD": tld,
	})
	if err != nil {
		return nil, err
	}
	body, err := xmlDataToString(raw)
	if err != nil {
		return nil, err
	}
	var parsed namecheapHostsResponse
	if err := xml.Unmarshal([]byte(body), &parsed); err != nil {
		return nil, fmt.Errorf("parse namecheap XML: %w", err)
	}
	if err := parsed.err(); err != nil {
		return nil, err
	}
	if parsed.CommandResponse.Hosts == nil || !strings.EqualFold(parsed.CommandResponse.Hosts.Domain, domain) {
		return nil, errors.New("Namecheap omitted or mismatched domain host list")
	}
	return &parsed, nil
}

func (n *namecheapProvider) List(ctx *sdk.AppCtx, domain string) ([]DNSRecord, error) {
	parsed, err := n.callGetHosts(ctx, domain)
	if err != nil {
		return nil, err
	}
	n.emailType = parsed.CommandResponse.Hosts.EmailType
	warnings := []string{}
	if strings.EqualFold(parsed.CommandResponse.Hosts.IsUsingOurDNS, "false") {
		warnings = append(warnings, "Namecheap is not the authoritative DNS provider for this domain")
	}
	out := make([]DNSRecord, 0, len(parsed.CommandResponse.Hosts.Hosts))
	for _, h := range parsed.CommandResponse.Hosts.Hosts {
		ttl, err := strconv.Atoi(h.TTL)
		if err != nil || ttl < 0 {
			return nil, errors.New("Namecheap returned an invalid TTL")
		}
		prio, err := strconv.Atoi(h.MXPref)
		if (h.Type == "MX" || h.Type == "MXE") && (err != nil || prio < 0 || prio > 65535) {
			return nil, errors.New("Namecheap returned an invalid mail priority")
		}
		value := h.Address
		if h.Type == "CAA" {
			if h.Flag == "" || h.Tag == "" {
				return nil, errors.New("Namecheap omitted CAA flag/tag; cannot safely reconstruct zone")
			}
			value = h.Flag + " " + h.Tag + " " + h.Address
		}
		out = append(out, DNSRecord{
			Warnings: warnings,
			ID:       h.HostID,
			Name:     h.Name, // Namecheap returns the local part only ("@", "www", "mail")
			Type:     strings.ToUpper(h.Type),
			Value:    value,
			TTL:      ttl,
			Prio:     prio,
			Raw: map[string]any{
				"host_id": h.HostID, "name": h.Name, "type": h.Type,
				"address": h.Address, "ttl": h.TTL, "mx_pref": h.MXPref, "flag": h.Flag, "tag": h.Tag, "email_type": parsed.CommandResponse.Hosts.EmailType,
			},
		})
	}
	return out, nil
}

func (n *namecheapProvider) Upsert(ctx *sdk.AppCtx, domain, sub, rtype, value string, ttl int, recordID string, existing []DNSRecord) (string, error) {
	hosts := namecheapHostsFromRecords(existing)
	wantName := sub
	if wantName == "" {
		wantName = "@"
	}

	prio := ""
	content := value
	caaFlag, caaTag := "", ""
	if rtype == "CAA" {
		f := strings.SplitN(value, " ", 3)
		if len(f) != 3 {
			return "", errors.New("invalid CAA")
		}
		caaFlag, caaTag, content = f[0], f[1], f[2]
	}
	if rtype == "MX" {
		parts := strings.SplitN(value, " ", 2)
		if len(parts) == 2 {
			prio = parts[0]
			content = parts[1]
		}
	}

	matches := recordsAtName(existing, domain, sub, rtype)
	creating := recordID == createRecordID
	if creating {
		matches = nil
		recordID = ""
	}

	if recordID != "" {
		if _, err := selectedRecord(matches, recordID); err != nil {
			return "", err
		}
	} else if len(matches) > 1 {
		return "", ambiguousRRSetError(domain, sub, rtype, len(matches))
	}

	keep := make([]namecheapHost, 0, len(hosts)+1)
	matched := false
	for _, h := range hosts {
		matchesTarget := strings.EqualFold(h.Name, wantName) && strings.EqualFold(h.Type, rtype)
		if recordID != "" {
			matchesTarget = h.HostID == recordID
		}
		if !creating && !matched && matchesTarget {
			matched = true
			if h.Address == content && h.TTL == strconv.Itoa(ttl) && (prio == "" || h.MXPref == prio) && (rtype != "CAA" || h.Flag == caaFlag && h.Tag == caaTag) {
				return "unchanged", nil
			}
			h.Address = content
			if rtype == "CAA" {
				h.Flag, h.Tag = caaFlag, caaTag
			}
			h.TTL = fmt.Sprintf("%d", ttl)
			if prio != "" {
				h.MXPref = prio
			}
			keep = append(keep, h)
			continue
		}
		keep = append(keep, h)
	}
	action := "updated"
	if !matched {
		newHost := namecheapHost{
			Flag: caaFlag, Tag: caaTag,
			Name:    wantName,
			Type:    rtype,
			Address: content,
			TTL:     fmt.Sprintf("%d", ttl),
		}
		switch {
		case prio != "":
			newHost.MXPref = prio
		case rtype == "MX":
			// Namecheap rejects MX records without an MXPref. The tool
			// docs ask for "<prio> <host>" values, but be forgiving when
			// the caller forgets and pass a conventional default.
			newHost.MXPref = "10"
		}
		keep = append(keep, newHost)
		action = "created"
	}
	if err := n.checkSnapshot(ctx, domain, existing); err != nil {
		return "", err
	}
	if err := n.writeHosts(ctx, domain, keep); err != nil {
		return "", err
	}
	return action, nil
}

func (n *namecheapProvider) Delete(ctx *sdk.AppCtx, domain, sub, rtype, recordID string, existing []DNSRecord) error {
	if recordID != "" {
		if _, err := selectedRecord(recordsAtName(existing, domain, sub, rtype), recordID); err != nil {
			return err
		}
	}

	hosts := namecheapHostsFromRecords(existing)
	wantName := sub
	if wantName == "" {
		wantName = "@"
	}
	keep := make([]namecheapHost, 0, len(hosts))
	removed := false
	for _, h := range hosts {
		matches := strings.EqualFold(h.Name, wantName) && strings.EqualFold(h.Type, rtype)
		if recordID != "" {
			matches = h.HostID == recordID
		}
		if matches {
			removed = true
			continue
		}
		keep = append(keep, h)
	}
	if recordID != "" && !removed {
		return fmt.Errorf("record_id %q not found in %s %s RRset", recordID, wantName, rtype)
	}
	if !removed {
		return nil
	}
	if err := n.checkSnapshot(ctx, domain, existing); err != nil {
		return err
	}
	return n.writeHosts(ctx, domain, keep)
}

func namecheapHostsFromRecords(records []DNSRecord) []namecheapHost {
	hosts := make([]namecheapHost, 0, len(records))
	for _, r := range records {
		host := namecheapHost{
			HostID: r.ID, Name: r.Name, Type: r.Type, Address: r.Value,
			TTL: strconv.Itoa(r.TTL), MXPref: strconv.Itoa(r.Prio),
		}
		if r.Type != "MX" && r.Type != "MXE" {
			host.MXPref = ""
		}
		if r.Type == "CAA" {
			f := strings.SplitN(r.Value, " ", 3)
			if len(f) == 3 {
				host.Flag, host.Tag, host.Address = f[0], f[1], f[2]
			}
		}
		if flag, ok := r.Raw["flag"].(string); ok {
			host.Flag = flag
		}
		if tag, ok := r.Raw["tag"].(string); ok {
			host.Tag = tag
		}
		hosts = append(hosts, host)
	}
	return hosts
}

// writeHosts replaces the entire DNS host list for a domain via
// Namecheap's setHosts. Builds the numbered-form-param payload
// (HostName1, RecordType1, Address1, TTL1, MXPref1, …).
func (n *namecheapProvider) writeHosts(ctx *sdk.AppCtx, domain string, hosts []namecheapHost) error {
	if n.emailTypeOverride != "" {
		n.emailType = n.emailTypeOverride
	}
	sld, tld := splitSLDTLD(domain)
	if sld == "" || tld == "" {
		return fmt.Errorf("namecheap requires a 2-label domain (got %q)", domain)
	}
	payload := map[string]any{
		"SLD": sld,
		"TLD": tld,
	}
	if n.emailType != "" {
		payload["EmailType"] = n.emailType
	}
	for i, h := range hosts {
		if !includes(capabilities("namecheap").WriteTypes, strings.ToUpper(h.Type)) {
			return errors.New("Namecheap zone contains an unsupported record; refusing lossy rewrite")
		}
		idx := i + 1
		payload[fmt.Sprintf("HostName%d", idx)] = h.Name
		payload[fmt.Sprintf("RecordType%d", idx)] = h.Type
		payload[fmt.Sprintf("Address%d", idx)] = h.Address
		if h.TTL != "" {
			payload[fmt.Sprintf("TTL%d", idx)] = h.TTL
		}
		if h.Flag != "" {
			payload[fmt.Sprintf("Flag%d", idx)] = h.Flag
		}
		if h.Tag != "" {
			payload[fmt.Sprintf("Tag%d", idx)] = h.Tag
		}
		if h.Type == "MX" && (n.emailType == "" || n.emailType == "MX" || n.emailType == "MXE") {
			payload["EmailType"] = "MX"
		}
		if h.Type == "MXE" && (n.emailType == "" || n.emailType == "MX" || n.emailType == "MXE") {
			payload["EmailType"] = "MXE"
		}
		if h.MXPref != "" {
			payload[fmt.Sprintf("MXPref%d", idx)] = h.MXPref
		}
	}
	raw, err := providerCall(ctx, n.bound, "set_dns_hosts", payload)
	if err != nil {
		return err
	}
	body, err := xmlDataToString(raw)
	if err != nil {
		return err
	}
	var parsed struct {
		XMLName xml.Name `xml:"ApiResponse"`
		namecheapStatus
		Result *struct {
			Success string `xml:"IsSuccess,attr"`
		} `xml:"CommandResponse>DomainDNSSetHostsResult"`
	}
	if err := xml.Unmarshal([]byte(body), &parsed); err != nil {
		return fmt.Errorf("parse namecheap setHosts XML: %w", err)
	}
	if err := parsed.err(); err != nil {
		return err
	}
	if parsed.Result == nil || parsed.Result.Success != "true" {
		return errors.New("Namecheap did not confirm zone replacement")
	}
	return nil
}

// splitSLDTLD splits "acme.com" into ("acme", "com"). Subdomains are
// rejected — Namecheap's API operates at the registered-domain level
// and treats subdomains as host records (Name="mail" within domain
// "acme.com"). Splitting at the first dot also preserves Namecheap's
// expected multi-label TLD form, e.g. "acme.co.uk" -> ("acme", "co.uk").
func splitSLDTLD(domain string) (sld, tld string) {
	idx := strings.IndexByte(domain, '.')
	if idx <= 0 {
		return "", ""
	}
	return domain[:idx], domain[idx+1:]
}
