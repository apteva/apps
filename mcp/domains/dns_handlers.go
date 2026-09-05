package main

import (
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ─── Tool handlers (use dnsProviderImpl) ───────────────────────────

// resolveProviderForDomain looks up the connection pinned on the
// domain row (when one exists) and returns the matching provider.
// Falls back to the role binding for domains not in the inventory or
// rows added before per-domain pinning landed.
func (a *App) resolveProviderForDomain(ctx *sdk.AppCtx, args map[string]any, name string) (dnsProviderImpl, *sdk.BoundIntegration, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, nil, err
	}
	d, err := dbDomainGetByName(ctx.AppDB(), pid, name)
	if err != nil {
		return nil, nil, fmt.Errorf("read domain inventory: %w", err)
	}
	var connID int64
	if d != nil {
		if d.ConnectionMode == "unmanaged" || d.ConnectionID <= 0 {
			return nil, nil, apiError(409, "domain is unmanaged; select a DNS connection first")
		}
		connID = d.ConnectionID
	}
	// Compatibility: never-added domains may use the explicitly configured default.
	return a.providerFor(ctx, connID, pid)
}

func (a *App) toolDomainRecordsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	domain, err := normaliseDomainName(strArg(args, "domain"))
	if err != nil {
		return nil, fmt.Errorf("domain: %w", err)
	}
	prov, bound, err := a.resolveProviderForDomain(ctx, args, domain)
	if err != nil {
		return nil, err
	}
	records, err := prov.List(ctx, domain)
	if err != nil {
		return nil, err
	}
	if t := strings.ToUpper(strArg(args, "type")); t != "" {
		records = filterRecords(records, func(r DNSRecord) bool { return r.Type == t })
	}
	if _, present := args["name"]; present {
		sub, err := recordSubaddress(domain, strArg(args, "name"))
		if err != nil {
			return nil, err
		}
		records = filterRecords(records, func(r DNSRecord) bool { return spaceshipRecordNameMatches(r.Name, domain, sub) })
	}

	out := map[string]any{"records": records, "count": len(records), "domain": domain, "capabilities": capabilities(bound.AppSlug), "connection_id": bound.ConnectionID}
	if n, ok := prov.(*namecheapProvider); ok {
		out["namecheap_email_type"] = n.emailType
		required := false
		for _, r := range records {
			if (r.Type == "MX" || r.Type == "MXE") && n.emailType == "" {
				required = true
			}
		}
		out["namecheap_email_type_required"] = required
	}
	return out, nil
}

func (a *App) toolDomainRecordsSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	domain, err := normaliseDomainName(strArg(args, "domain"))
	if err != nil {
		return nil, fmt.Errorf("domain: %w", err)
	}
	sub, subErr := recordSubaddress(domain, strArg(args, "name"))
	if err := subErr; err != nil {
		return nil, err
	}
	rtype, err := normaliseRecordType(strArg(args, "type"))
	if err != nil {
		return nil, err
	}
	value := strArg(args, "value")
	if value == "" {
		return nil, errors.New("value required")
	}
	ttl, err := strictIntArg(args, "ttl", 600, 60, 2147483647)
	if err != nil {
		return nil, err
	}
	if ttl < 60 || ttl > 2147483647 {
		return nil, fmt.Errorf("ttl must be between 60 and 2147483647 seconds, got %d", ttl)
	}
	recordID := strings.TrimSpace(strArg(args, "record_id"))
	if recordID == createRecordID {
		return nil, errors.New("invalid record ID")
	}

	unlock, lockErr := acquireDNSMutation(ctx.Done(), domain)
	if lockErr != nil {
		return nil, lockErr
	}
	defer unlock()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	var pending int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM dns_recoveries WHERE project_id=? AND domain=? AND status IN ('pending','unknown')`, pid, domain).Scan(&pending); err != nil {
		return nil, err
	}
	if pending > 0 {
		return nil, apiError(409, "an earlier DNS replacement needs reconciliation; inspect domain_dns_recovery before another mutation")
	}

	ctx = ctx.WithProject(pid)
	prov, bound, err := a.resolveProviderForDomain(ctx, args, domain)
	if err != nil {
		return nil, err
	}
	if err := validateRecordValue(bound.AppSlug, rtype, value, ttl); err != nil {
		return nil, err
	}

	if _, present := args["expected_connection_id"]; present {
		want, err := strictIntArg(args, "expected_connection_id", 0, 1, 9007199254740991)
		if err != nil {
			return nil, err
		}
		if int64(want) != bound.ConnectionID {
			return nil, apiError(409, "DNS connection changed; refresh before editing")
		}
	}

	if err := setNamecheapMailMode(prov, args); err != nil {
		return nil, err
	}
	existing, err := prov.List(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("list before mutation: %w", err)
	}
	if expected, ok := args["expected_record"].(map[string]any); ok {
		selected, err := selectedRecord(recordsAtName(existing, domain, sub, rtype), recordID)
		if err != nil {
			return nil, err
		}
		if selected.Value != strArg(expected, "value") || selected.Prio != intArg(expected, "prio", 0) || selected.TTL != intArg(expected, "ttl", 0) {
			return nil, apiError(409, "record changed at the provider; refresh before editing")
		}
	}

	if err := validateApexAddressConflicts(existing, domain, sub, rtype); err != nil {
		return nil, err
	}
	mode := strArg(args, "mode")
	if mode != "" && mode != "upsert" && mode != "create" && mode != "ensure" {
		return nil, errors.New("mode must be upsert, create or ensure")
	}
	if (mode == "create" || mode == "ensure") && recordID != "" {
		return nil, errors.New("record_id is only valid for an update")
	}
	if mode == "create" || mode == "ensure" {
		content, prio, err := priorityValue(rtype, value, nil)
		if err != nil {
			return nil, err
		}
		for _, r := range recordsAtName(existing, domain, sub, rtype) {
			if recordValueEqual(rtype, r.Value, content) && r.Prio == prio {
				if mode == "create" {
					return nil, apiError(409, "record value already exists")
				}
				return map[string]any{"action": "unchanged", "domain": domain, "record_id": r.ID}, nil
			}
		}
		recordID = createRecordID
	}

	action, err := prov.Upsert(ctx, domain, sub, rtype, value, ttl, recordID, existing)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"action": action,
		"domain": domain,
		"name":   sub,
		"type":   rtype,
		"value":  value,
		"ttl":    ttl,
	}
	if recordID != "" {
		out["record_id"] = recordID
	}
	return out, nil
}

func validateApexAddressConflicts(records []DNSRecord, domain, sub, rtype string) error {
	for _, r := range records {
		if spaceshipRecordNameMatches(r.Name, domain, sub) && r.Type != rtype && (rtype == "CNAME" || r.Type == "CNAME") {
			return apiError(409, "CNAME cannot coexist with another record type at this name")
		}
	}

	if sub != "" {
		return nil
	}
	conflicts := apexAddressConflictTypes(rtype)
	if len(conflicts) == 0 {
		return nil
	}
	for _, conflictType := range conflicts {
		if !hasRecordAtName(records, domain, "", conflictType) {
			continue
		}
		return fmt.Errorf("cannot set apex %s while an apex %s record exists; delete the conflicting record explicitly first", rtype, conflictType)
	}
	return nil
}

func apexAddressConflictTypes(rtype string) []string {
	switch rtype {
	case "A", "AAAA":
		return []string{"ALIAS", "CNAME"}
	case "ALIAS":
		return []string{"A", "AAAA", "CNAME"}
	case "CNAME":
		return []string{"A", "AAAA", "ALIAS"}
	default:
		return nil
	}
}

func hasRecordAtName(records []DNSRecord, domain, sub, rtype string) bool {
	wantFQ := domain
	if sub != "" {
		wantFQ = sub + "." + domain
	}
	for _, r := range records {
		if !strings.EqualFold(r.Type, rtype) {
			continue
		}
		if strings.EqualFold(r.Name, wantFQ) || strings.EqualFold(r.Name, sub) {
			return true
		}
		if sub == "" && (r.Name == "" || r.Name == "@") {
			return true
		}
	}
	return false
}

func (a *App) toolDomainRecordsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	domain, err := normaliseDomainName(strArg(args, "domain"))
	if err != nil {
		return nil, err
	}
	sub, subErr := recordSubaddress(domain, strArg(args, "name"))
	if err := subErr; err != nil {
		return nil, err
	}
	rtype, err := normaliseRecordType(strArg(args, "type"))
	if err != nil {
		return nil, err
	}
	recordID := strings.TrimSpace(strArg(args, "record_id"))
	if recordID == createRecordID {
		return nil, errors.New("invalid record ID")
	}
	unlock, lockErr := acquireDNSMutation(ctx.Done(), domain)
	if lockErr != nil {
		return nil, lockErr
	}
	defer unlock()
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	var pending int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM dns_recoveries WHERE project_id=? AND domain=? AND status IN ('pending','unknown')`, pid, domain).Scan(&pending); err != nil {
		return nil, err
	}
	if pending > 0 {
		return nil, apiError(409, "an earlier DNS replacement needs reconciliation; inspect domain_dns_recovery before another mutation")
	}

	ctx = ctx.WithProject(pid)
	prov, bound, err := a.resolveProviderForDomain(ctx, args, domain)
	if err != nil {
		return nil, err
	}
	if !includes(capabilities(bound.AppSlug).DeleteTypes, rtype) {
		return nil, fmt.Errorf("%s cannot delete %s records", bound.AppSlug, rtype)
	}

	if _, present := args["expected_connection_id"]; present {
		want, err := strictIntArg(args, "expected_connection_id", 0, 1, 9007199254740991)
		if err != nil {
			return nil, err
		}
		if int64(want) != bound.ConnectionID {
			return nil, apiError(409, "DNS connection changed; refresh before editing")
		}
	}

	if err := setNamecheapMailMode(prov, args); err != nil {
		return nil, err
	}
	existing, err := prov.List(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("list before delete: %w", err)
	}
	if expected, ok := args["expected_record"].(map[string]any); ok {
		selected, err := selectedRecord(recordsAtName(existing, domain, sub, rtype), recordID)
		if err != nil {
			return nil, err
		}
		if selected.Value != strArg(expected, "value") || selected.Prio != intArg(expected, "prio", 0) || selected.TTL != intArg(expected, "ttl", 0) {
			return nil, apiError(409, "record changed at the provider; refresh before editing")
		}
	}

	if err := prov.Delete(ctx, domain, sub, rtype, recordID, existing); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true, "domain": domain, "name": sub, "type": rtype, "record_id": recordID}, nil
}
