package main

import (
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net"
	"net/url"
	"strings"
)

type managedExposure struct {
	hostname, project                 string
	apiID                             int64
	cleanup                           bool
	zone, name, kind, value, recordID string
}

func queueExposureCleanup(ctx *sdk.AppCtx, api *API) error {
	if api.Hostname == "" {
		return nil
	}
	_, err := ctx.AppDB().Exec(`INSERT INTO api_exposures(hostname,project_id,api_id,cleanup) VALUES(?,?,?,1) ON CONFLICT(hostname) DO UPDATE SET cleanup=1 WHERE api_id=excluded.api_id AND project_id=excluded.project_id`, api.Hostname, api.ProjectID, api.ID)
	return err
}
func (a *App) configureExposure(ctx *sdk.AppCtx, api *API) {
	if api == nil {
		return
	}
	if api.Hostname == "" || api.Status != "active" {
		_ = a.reconcileExposures(ctx)
		return
	}
	if err := ensureHostnameAvailable(ctx.AppDB(), api.Hostname, api.ID); err != nil {
		dbSetAPIExposureStatus(ctx.AppDB(), api.ProjectID, api.ID, "", "error: "+safeUpstreamError(err))
		return
	}
	_, err := ctx.AppDB().Exec(`INSERT INTO api_exposures(hostname,project_id,api_id) VALUES(?,?,?) ON CONFLICT(hostname) DO UPDATE SET cleanup=0 WHERE api_id=excluded.api_id AND project_id=excluded.project_id`, api.Hostname, api.ProjectID, api.ID)
	if err != nil {
		dbSetAPIExposureStatus(ctx.AppDB(), api.ProjectID, api.ID, "", safeUpstreamError(err))
		return
	}
	dnsStatus := api.DNSMode
	if api.DNSMode == "domains" {
		if err := writeDomainRecord(ctx, api); err != nil {
			dnsStatus = "error: " + safeUpstreamError(err)
		} else {
			dnsStatus = "ok"
		}
	}
	_, err = ctx.PlatformAPI().ExposeIngress(sdk.IngressExposeRequest{Hostname: api.Hostname, Target: "app://api/gw?project_id=" + url.QueryEscape(api.ProjectID), ProjectID: api.ProjectID, OwnerKind: "api", CertFQDN: api.Hostname, AllowHTTP: api.AllowHTTP, TLSMode: "auto"})
	status := "ok"
	if err != nil {
		status = "error: " + safeUpstreamError(err)
	}
	dbSetAPIExposureStatus(ctx.AppDB(), api.ProjectID, api.ID, dnsStatus, status)
	_ = a.reconcileExposures(ctx)
}
func (a *App) reconcileExposures(ctx *sdk.AppCtx) error {
	// Recover cleanup intent after a crash between an API update and reconciliation.
	if _, err := ctx.AppDB().Exec(`UPDATE api_exposures SET cleanup=1 WHERE NOT EXISTS
 (SELECT 1 FROM apis WHERE apis.id=api_exposures.api_id AND apis.project_id=api_exposures.project_id AND apis.hostname=api_exposures.hostname AND apis.status='active')`); err != nil {
		return err
	}
	rows, err := ctx.AppDB().Query(`SELECT hostname,project_id,api_id,cleanup,dns_zone,dns_name,dns_type,dns_value,dns_record_id FROM api_exposures WHERE cleanup=1`)
	if err != nil {
		return err
	}
	var work []managedExposure
	for rows.Next() {
		var x managedExposure
		if err := rows.Scan(&x.hostname, &x.project, &x.apiID, &x.cleanup, &x.zone, &x.name, &x.kind, &x.value, &x.recordID); err != nil {
			rows.Close()
			return err
		}
		work = append(work, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var errs []error
	for _, x := range work {
		scoped := ctx.WithProject(x.project)
		err := scoped.PlatformAPI().UnexposeIngress(x.hostname)
		if err == nil && x.zone != "" {
			err = deleteOwnedDNS(scoped, x)
		}
		if err != nil {
			_, _ = ctx.AppDB().Exec(`UPDATE api_exposures SET error=? WHERE hostname=?`, safeUpstreamError(err), x.hostname)
			errs = append(errs, err)
			continue
		}
		_, err = ctx.AppDB().Exec(`DELETE FROM api_exposures WHERE hostname=?`, x.hostname)
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
func splitHostnameForDNS(host string, zones ...string) (zone, name string) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, candidate := range zones {
		candidate = strings.ToLower(strings.TrimSuffix(candidate, "."))
		if candidate != "" && (host == candidate || strings.HasSuffix(host, "."+candidate)) && len(candidate) > len(zone) {
			zone = candidate
		}
	}
	if zone == "" {
		return "", ""
	}
	if host == zone {
		return zone, "@"
	}
	return zone, strings.TrimSuffix(host, "."+zone)
}
func writeDomainRecord(ctx *sdk.AppCtx, api *API) error {
	target := strings.TrimSpace(ctx.Config().Get("public_host"))
	if target == "" {
		return errors.New("public_host config required for dns_mode=domains")
	}
	var inventory struct {
		Domains []struct {
			Name string `json:"name"`
		} `json:"domains"`
	}
	if err := ctx.PlatformAPI().CallAppResult("domains", "domain_list", map[string]any{"_project_id": api.ProjectID}, &inventory); err != nil {
		return err
	}
	zones := make([]string, 0, len(inventory.Domains))
	for _, domain := range inventory.Domains {
		zones = append(zones, domain.Name)
	}
	zone, name := splitHostnameForDNS(api.Hostname, zones...)
	if zone == "" {
		return errors.New("hostname has no matching managed DNS zone")
	}
	kind := "CNAME"
	if ip := net.ParseIP(target); ip != nil {
		kind = "AAAA"
		if ip.To4() != nil {
			kind = "A"
		}
	} else {
		if _, err := normalizeHostname(target); err != nil {
			return err
		}
		if name == "@" {
			return errors.New("apex exposure requires an IP address")
		}
	}
	// Retire only a previously recorded value when its record type changes.
	var previous managedExposure
	previous.hostname = api.Hostname
	previous.project = api.ProjectID
	if err := ctx.AppDB().QueryRow(`SELECT dns_zone,dns_name,dns_type,dns_value,dns_record_id FROM api_exposures WHERE hostname=?`, api.Hostname).Scan(&previous.zone, &previous.name, &previous.kind, &previous.value, &previous.recordID); err != nil {
		return err
	}

	records, err := listDNSRecords(ctx, zone, api.ProjectID)
	if err != nil {
		return err
	}
	recordID := ""
	compatibleUnowned := false
	for _, record := range records {
		if !dnsRecordNameMatches(record.Name, name, zone) {
			continue
		}
		owned := previous.recordID != "" && record.ID == previous.recordID && record.Type == previous.kind && dnsValueEqual(record.Value, previous.value)
		if record.Type != kind {
			if (record.Type == "CNAME" || kind == "CNAME") && !owned {
				return errors.New("hostname has an unrelated conflicting DNS record; use manual DNS mode")
			}
			continue
		}
		if owned {
			recordID = record.ID
			continue
		}
		if dnsValueEqual(record.Value, target) {
			compatibleUnowned = true
			continue
		}
		return errors.New("refusing to overwrite a DNS record not owned by this API; use manual DNS mode")
	}
	if previous.zone != "" && previous.kind != kind {
		if err := deleteOwnedDNS(ctx, previous); err != nil {
			return err
		}
		if _, err := ctx.AppDB().Exec(`UPDATE api_exposures SET dns_zone='',dns_name='',dns_type='',dns_value='',dns_record_id='' WHERE hostname=?`, api.Hostname); err != nil {
			return err
		}
	}
	// Existing compatible records remain externally owned and survive API deletion.
	if compatibleUnowned && recordID == "" {
		return nil
	}
	args := map[string]any{"domain": zone, "name": name, "type": kind, "value": target, "ttl": 300, "_project_id": api.ProjectID}
	if recordID != "" {
		args["record_id"] = recordID
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("domains", "domain_records_set", args, &out); err != nil {
		return err
	}
	if recordID == "" {
		// Do not claim records updated by an external writer between list and set.
		if action, _ := out["action"].(string); action != "created" {
			return errors.New("DNS record changed concurrently; ownership was not acquired")
		}
		records, err = listDNSRecords(ctx, zone, api.ProjectID)
		if err != nil {
			return err
		}
		for _, record := range records {
			if dnsRecordNameMatches(record.Name, name, zone) && record.Type == kind && dnsValueEqual(record.Value, target) {
				if recordID != "" || record.ID == "" {
					return errors.New("cannot uniquely identify the created DNS record")
				}
				recordID = record.ID
			}
		}
		if recordID == "" {
			return errors.New("created DNS record is not yet visible; ownership was not acquired")
		}
	}
	_, err = ctx.AppDB().Exec(`UPDATE api_exposures SET dns_zone=?,dns_name=?,dns_type=?,dns_value=?,dns_record_id=? WHERE hostname=?`, zone, name, kind, target, recordID, api.Hostname)
	return err
}

type dnsRecord struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

func listDNSRecords(ctx *sdk.AppCtx, zone, project string) ([]dnsRecord, error) {
	var listing struct {
		Records []dnsRecord `json:"records"`
	}
	err := ctx.PlatformAPI().CallAppResult("domains", "domain_records_list", map[string]any{"domain": zone, "_project_id": project}, &listing)
	return listing.Records, err
}
func dnsValueEqual(a, b string) bool {
	return strings.EqualFold(strings.TrimSuffix(a, "."), strings.TrimSuffix(b, "."))
}
func deleteOwnedDNS(ctx *sdk.AppCtx, x managedExposure) error {
	if x.recordID == "" {
		return nil
	}
	records, err := listDNSRecords(ctx, x.zone, x.project)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.ID != x.recordID || !dnsRecordNameMatches(record.Name, x.name, x.zone) || record.Type != x.kind || !dnsValueEqual(record.Value, x.value) {
			continue
		}
		var out map[string]any
		if err := ctx.PlatformAPI().CallAppResult("domains", "domain_records_delete", map[string]any{"domain": x.zone, "name": x.name, "type": x.kind, "record_id": x.recordID, "_project_id": x.project}, &out); err != nil {
			return fmt.Errorf("remove managed DNS: %w", err)
		}
	}
	return nil
}

func dnsRecordNameMatches(record, name, zone string) bool {
	record = strings.TrimSuffix(strings.ToLower(record), ".")
	if name == "@" {
		return record == "@" || record == "" || record == zone
	}
	return record == name || record == name+"."+zone
}
