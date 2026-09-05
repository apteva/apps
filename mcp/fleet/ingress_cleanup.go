package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) unregisterOwnedHostname(ctx *sdk.AppCtx, t *Tenant, hostname string) error {
	if t.UsesDirectIngress() {
		base, err := a.internalTenantBaseURL(ctx, t)
		if err != nil {
			return err
		}
		_, enc, err := a.store.get(t.ID)
		if err != nil {
			return err
		}
		key, err := a.keys.open(enc)
		if err != nil {
			return err
		}
		req, err := http.NewRequest(http.MethodDelete, strings.TrimRight(base, "/")+"/api/ingress/routes/"+url.PathEscape(hostname), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+string(key))
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 404 && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
			return fmt.Errorf("remove direct ingress: HTTP %d", resp.StatusCode)
		}
	}
	return a.unregisterTenantHost(ctx, hostname)
}

// Called with the tenant operation held. Failures preserve all retry metadata.
func (a *App) cleanupTenantRouting(ctx *sdk.AppCtx, t *Tenant, project string) error {
	done, err := lockResource(context.Background(), "dns-ownership")
	if err != nil {
		return err
	}
	defer done()
	if project == "" {
		_ = a.store.db.QueryRow(`SELECT dns_project_id FROM fleet_tenant_state WHERE tenant_id=?`, t.ID).Scan(&project)
	}
	if t.Domain != "" {
		if err = a.unregisterTenantHost(ctx, t.Domain); err != nil {
			return err
		}
	}
	hosts, err := a.store.listTenantHosts(t.ID)
	if err != nil {
		return err
	}
	for _, h := range hosts {
		if err = a.unregisterTenantHost(ctx, h.Hostname); err != nil {
			return err
		}
	}
	if t.DomainRecordID != "" {
		if !a.domainsAvailable(ctx) {
			return fmt.Errorf("Domains integration is required to remove managed DNS; reconnect it and retry")
		}
		if err = a.deleteAttachedDNSRecord(ctx, project, t.Domain, t.DomainRecordID); err != nil {
			return err
		}
	}
	grants, err := a.store.listDomainGrants(t.ID)
	if err != nil {
		return err
	}
	for _, g := range grants {
		for _, record := range []string{g.DomainRecordID, g.WildcardRecordID} {
			if record == "" {
				continue
			}
			if !a.domainsAvailable(ctx) {
				return fmt.Errorf("Domains integration required to revoke managed DNS")
			}
			apex, name, kind, ok := splitGrantRecordID(record)
			if !ok {
				return fmt.Errorf("invalid DNS record reference")
			}
			if err = callDomainsTool(ctx, project, "domain_records_delete", map[string]any{"domain": apex, "name": name, "type": kind}, nil); err != nil {
				return err
			}
		}
	}
	_, err = a.store.db.Exec(`UPDATE fleet_tenant_state SET dns_epoch=dns_epoch+1 WHERE tenant_id=?`, t.ID)
	return err
}
