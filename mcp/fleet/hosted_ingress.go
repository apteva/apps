package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const directIngressVerifyTimeout = 90 * time.Second

type hostedIngressCheck struct {
	TargetIP string            `json:"target_ip"`
	DNS      map[string]string `json:"dns"`
	HTTPS    map[string]int    `json:"https"`
}

func (a *App) prepareHostedDirectIngress(ctx *sdk.AppCtx, projectID string, t *Tenant) (map[string]any, error) {
	if t == nil {
		return nil, errors.New("tenant required")
	}
	if !t.IsHosted() {
		return nil, errors.New("direct ingress is only available for hosted tenants")
	}
	if _, err := normaliseExactHostname(t.Domain); err != nil {
		return nil, errors.New("attach the tenant primary domain before preparing direct ingress")
	}
	done, err := a.beginTenantOperation(t.ID, "prepare direct ingress")
	if err != nil {
		return nil, err
	}
	defer done()

	info, err := a.getInstanceInfo(ctx, t.InstanceID)
	if err != nil {
		return nil, err
	}
	if err := a.ensureHostedRuntime(ctx, t.InstanceID); err != nil {
		return nil, err
	}
	grants, err := a.store.listDomainGrants(t.ID)
	if err != nil {
		return nil, err
	}
	managedDNS := t.DomainRecordID != "" || len(grants) > 0
	if managedDNS && strings.TrimSpace(projectID) == "" {
		return nil, errors.New("project_id required to repoint Fleet-managed DNS")
	}

	port, err := portFromBaseURL(t.BaseURL)
	if err != nil || port == 0 {
		return nil, fmt.Errorf("cannot derive hosted tenant port from %q", t.BaseURL)
	}
	if t.IngressMode == "" {
		t.IngressMode = IngressParent
	}
	if t.IngressMode == IngressParent {
		hosts, err := a.store.listTenantHosts(t.ID)
		if err != nil {
			return nil, err
		}
		for _, host := range hosts {
			if err := a.verifyTenantLocalIngressRoute(context.Background(), ctx, t, host.Hostname); err != nil {
				return nil, fmt.Errorf("direct ingress preflight: %w", err)
			}
		}
		if err := ensureHostedIngressPortsFree(ctx, t.InstanceID); err != nil {
			return nil, err
		}
		if err := prepareHostedIngressFirewall(ctx, t.InstanceID); err != nil {
			return nil, err
		}
		if err := a.store.setIngressMode(t.ID, IngressDirectPending, ""); err != nil {
			return nil, err
		}
		t.IngressMode = IngressDirectPending
		if err := stopHostedTenant(ctx, t.InstanceID, t.Slug, port, 10*time.Second); err != nil {
			_ = a.store.setIngressMode(t.ID, IngressParent, err.Error())
			t.IngressMode = IngressParent
			return nil, err
		}
		spec := hostedSpawnSpecForTenant(t, info.PublicIPv4, port)
		if _, _, err := a.spawnHostedTenant(ctx, spec); err != nil {
			_ = a.store.setIngressMode(t.ID, IngressParent, err.Error())
			t.IngressMode = IngressParent
			_, _, rollbackErr := a.spawnHostedTenant(ctx, hostedSpawnSpecForTenant(t, info.PublicIPv4, port))
			if rollbackErr != nil {
				return nil, fmt.Errorf("start direct ingress: %v; parent-mode rollback failed: %w", err, rollbackErr)
			}
			return nil, fmt.Errorf("start direct ingress: %w", err)
		}
	}
	if err := verifyHostedIngressListeners(ctx, t.InstanceID); err != nil {
		_ = a.store.setIngressMode(t.ID, IngressDirectPending, err.Error())
		return nil, err
	}

	if managedDNS {
		if err := a.repointTenantManagedDNS(ctx, projectID, t, info.PublicIPv4); err != nil {
			_ = a.store.setIngressMode(t.ID, IngressDirectPending, err.Error())
			return nil, fmt.Errorf("direct ingress is running, but DNS update failed: %w", err)
		}
	}
	_ = a.store.setIngressMode(t.ID, IngressDirectPending, "")
	_ = a.store.recordEvent(t.ID, "ingress.direct_prepared", "tool:tenant_ingress_prepare_direct", map[string]any{
		"target_ip": info.PublicIPv4, "managed_dns": managedDNS,
	})
	return map[string]any{
		"tenant_id":           t.ID,
		"mode":                IngressDirectPending,
		"target_ip":           info.PublicIPv4,
		"managed_dns":         managedDNS,
		"dns_action_required": !managedDNS,
		"next":                "point every tenant hostname to target_ip, then run tenant_ingress_verify and tenant_ingress_finalize",
	}, nil
}

func ensureHostedIngressPortsFree(ctx *sdk.AppCtx, instanceID int64) error {
	script := `set -eu
for PORT in 80 443; do
  if ss -ltnH "sport = :$PORT" | grep -q .; then
    echo "port $PORT is already in use" >&2
    exit 73
  fi
done`
	out, code, err := instanceRunCommand(ctx, instanceID, script, 10)
	if err != nil || code != 0 {
		return fmt.Errorf("hosted direct ingress requires exclusive ports 80/443: %w (exit %d): %s", err, code, strings.TrimSpace(out))
	}
	return nil
}

func prepareHostedIngressFirewall(ctx *sdk.AppCtx, instanceID int64) error {
	script := `set -eu
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | head -1 | grep -qi active; then
  ufw allow 80/tcp >/dev/null
  ufw allow 443/tcp >/dev/null
fi
if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
  firewall-cmd --permanent --add-service=http >/dev/null
  firewall-cmd --permanent --add-service=https >/dev/null
  firewall-cmd --reload >/dev/null
fi
echo ready`
	out, code, err := instanceRunCommand(ctx, instanceID, script, 20)
	if err != nil || code != 0 {
		return fmt.Errorf("prepare hosted firewall: %w (exit %d): %s", err, code, strings.TrimSpace(out))
	}
	return nil
}

func verifyHostedIngressListeners(ctx *sdk.AppCtx, instanceID int64) error {
	out, code, err := instanceRunCommand(ctx, instanceID,
		`set -eu; for PORT in 80 443; do ss -ltnH "sport = :$PORT" | grep -q . || { echo "port $PORT is not listening" >&2; exit 1; }; done; echo ready`, 10)
	if err != nil || code != 0 {
		return fmt.Errorf("hosted ingress listener check: %w (exit %d): %s", err, code, strings.TrimSpace(out))
	}
	return nil
}

func (a *App) repointTenantManagedDNS(ctx *sdk.AppCtx, projectID string, t *Tenant, target string) error {
	target = strings.TrimSuffix(strings.TrimSpace(target), ".")
	if target == "" {
		return errors.New("DNS target required")
	}
	recordType := inferRecordType(target)
	if t.DomainRecordID != "" {
		apex, oldType, ok := splitRecordID(t.DomainRecordID)
		if !ok {
			return fmt.Errorf("invalid stored tenant DNS record %q", t.DomainRecordID)
		}
		fqdn := strings.TrimSuffix(strings.ToLower(t.Domain), ".")
		name := strings.TrimSuffix(fqdn, "."+apex)
		if fqdn == apex || name == "" {
			name = "@"
		}
		if recordType == "CNAME" && name == "@" {
			return fmt.Errorf("cannot restore apex %s with a CNAME target; configure the parent public host as an IP", apex)
		}
		if !strings.EqualFold(oldType, recordType) {
			if err := callDomainsTool(ctx, projectID, "domain_records_delete", map[string]any{"domain": apex, "name": name, "type": oldType}, nil); err != nil {
				return err
			}
		}
		if err := callDomainsTool(ctx, projectID, "domain_records_set", map[string]any{"domain": apex, "name": name, "type": recordType, "value": target, "ttl": 600}, nil); err != nil {
			return err
		}
		t.DomainRecordID = apex + "|" + recordType
		if err := a.store.setDomain(t.ID, t.Domain, t.DomainRecordID, time.Now().UTC()); err != nil {
			return err
		}
	}
	grants, err := a.store.listDomainGrants(t.ID)
	if err != nil {
		return err
	}
	for _, grant := range grants {
		for _, ref := range []*string{&grant.DomainRecordID, &grant.WildcardRecordID} {
			if *ref == "" {
				continue
			}
			apex, name, oldType, ok := splitGrantRecordID(*ref)
			if !ok {
				return fmt.Errorf("invalid stored domain grant record %q", *ref)
			}
			if recordType == "CNAME" && name == "@" {
				return fmt.Errorf("cannot restore apex %s with a CNAME target; configure the parent public host as an IP", apex)
			}
			if !strings.EqualFold(oldType, recordType) {
				if err := callDomainsTool(ctx, projectID, "domain_records_delete", map[string]any{"domain": apex, "name": name, "type": oldType}, nil); err != nil {
					return err
				}
			}
			if err := callDomainsTool(ctx, projectID, "domain_records_set", map[string]any{"domain": apex, "name": name, "type": recordType, "value": target, "ttl": 600}, nil); err != nil {
				return err
			}
			*ref = recordID(apex, name, recordType)
		}
		if err := a.store.upsertDomainGrant(grant); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) verifyHostedDirectIngress(ctx context.Context, app *sdk.AppCtx, t *Tenant) (*hostedIngressCheck, error) {
	if t == nil || !t.IsHosted() || !t.UsesDirectIngress() {
		return nil, errors.New("tenant is not prepared for hosted direct ingress")
	}
	info, err := a.getInstanceInfo(app, t.InstanceID)
	if err != nil {
		return nil, err
	}
	if err := verifyHostedIngressListeners(app, t.InstanceID); err != nil {
		return nil, err
	}
	hosts := []string{t.Domain}
	recorded, err := a.store.listTenantHosts(t.ID)
	if err != nil {
		return nil, err
	}
	for _, host := range recorded {
		hosts = append(hosts, host.Hostname)
		if err := a.verifyTenantLocalIngressRoute(ctx, app, t, host.Hostname); err != nil {
			return nil, err
		}
	}
	check := &hostedIngressCheck{TargetIP: info.PublicIPv4, DNS: map[string]string{}, HTTPS: map[string]int{}}
	for _, host := range hosts {
		if err := verifyHostnameTargetsIP(ctx, host, info.PublicIPv4); err != nil {
			return check, err
		}
		check.DNS[host] = info.PublicIPv4
		path := "/"
		if host == t.Domain {
			path = "/api/health"
		}
		status, err := verifyPublicHTTPS(ctx, host, path, "")
		if err != nil {
			return check, err
		}
		check.HTTPS[host] = status
	}
	return check, nil
}

func verifyHostnameTargetsIP(ctx context.Context, hostname, targetIP string) error {
	lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(lookupCtx, hostname)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", hostname, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%s has no address records", hostname)
	}
	for _, addr := range addrs {
		if addr != targetIP {
			return fmt.Errorf("%s resolves to %s, want only %s", hostname, strings.Join(addrs, ", "), targetIP)
		}
	}
	return nil
}

func verifyHostnamesTarget(ctx context.Context, hostnames []string, target string) error {
	target = strings.TrimSuffix(strings.TrimSpace(target), ".")
	if target == "" {
		return errors.New("parent DNS target is unavailable")
	}
	targetIPs := []string{target}
	if net.ParseIP(target) == nil {
		lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var err error
		targetIPs, err = net.DefaultResolver.LookupHost(lookupCtx, target)
		if err != nil {
			return fmt.Errorf("resolve parent target %s: %w", target, err)
		}
	}
	for _, hostname := range hostnames {
		matched := false
		var lastErr error
		for _, targetIP := range targetIPs {
			if err := verifyHostnameTargetsIP(ctx, hostname, targetIP); err == nil {
				matched = true
				break
			} else {
				lastErr = err
			}
		}
		if !matched {
			return lastErr
		}
	}
	return nil
}

func verifyPublicHTTPS(ctx context.Context, hostname, path, apiKey string) (int, error) {
	deadline := time.Now().Add(directIngressVerifyTimeout)
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var lastErr error
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+hostname+path, nil)
		if err != nil {
			return 0, err
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return resp.StatusCode, nil
			}
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return 0, fmt.Errorf("verify https://%s%s: %w", hostname, path, lastErr)
}

func (a *App) verifyTenantLocalIngressRoute(ctx context.Context, app *sdk.AppCtx, t *Tenant, hostname string) error {
	baseURL, err := a.internalTenantBaseURL(app, t)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/ingress/routes/"+url.PathEscape(hostname), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+string(key))
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("check tenant-local ingress for %s: %w", hostname, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tenant-local ingress route %s is not registered (HTTP %d)", hostname, resp.StatusCode)
	}
	return nil
}

func (a *App) finalizeHostedDirectIngress(ctx context.Context, app *sdk.AppCtx, t *Tenant) (map[string]any, error) {
	check, err := a.verifyHostedDirectIngress(ctx, app, t)
	if err != nil {
		_ = a.store.setIngressMode(t.ID, IngressDirectPending, err.Error())
		return nil, err
	}
	if t.Domain != "" {
		if err := a.unregisterTenantHost(app, t.Domain); err != nil {
			return nil, fmt.Errorf("remove parent primary ingress: %w", err)
		}
	}
	hosts, err := a.store.listTenantHosts(t.ID)
	if err != nil {
		return nil, err
	}
	for _, host := range hosts {
		if err := a.unregisterTenantHost(app, host.Hostname); err != nil {
			return nil, fmt.Errorf("remove parent ingress for %s: %w", host.Hostname, err)
		}
		_ = a.store.setTenantHostStatus(t.ID, host.Hostname, "active", "")
	}
	if err := a.store.setIngressMode(t.ID, IngressDirect, ""); err != nil {
		return nil, err
	}
	_ = a.store.recordEvent(t.ID, "ingress.direct_finalized", "tool:tenant_ingress_finalize", check)
	return map[string]any{"tenant_id": t.ID, "mode": IngressDirect, "checks": check}, nil
}

func (a *App) rollbackHostedDirectIngress(ctx *sdk.AppCtx, projectID string, t *Tenant, confirmDisable bool) (map[string]any, error) {
	if t == nil || !t.IsHosted() || !t.UsesDirectIngress() {
		return nil, errors.New("tenant is not using hosted direct ingress")
	}
	if confirmDisable && t.IngressMode == IngressDirect {
		return nil, errors.New("restore parent routing first, wait for DNS propagation, then disable direct ingress")
	}
	if err := a.refreshParentTenantIngress(ctx, t); err != nil {
		return nil, fmt.Errorf("restore parent ingress: %w", err)
	}
	grants, err := a.store.listDomainGrants(t.ID)
	if err != nil {
		return nil, err
	}
	if t.DomainRecordID != "" || len(grants) > 0 {
		if projectID == "" {
			return nil, errors.New("project_id required to restore Fleet-managed DNS")
		}
		if err := a.repointTenantManagedDNS(ctx, projectID, t, a.publicHost); err != nil {
			return nil, fmt.Errorf("restore parent DNS: %w", err)
		}
	}
	if !confirmDisable {
		_ = a.store.setIngressMode(t.ID, IngressDirectPending, "")
		return map[string]any{
			"tenant_id":              t.ID,
			"mode":                   IngressDirectPending,
			"parent_routes_restored": true,
			"parent_dns_target":      a.publicHost,
			"dns_action_required":    t.DomainRecordID == "" && len(grants) == 0,
			"next":                   "wait for the previous DNS TTL, then call tenant_ingress_rollback with confirm_disable=true",
		}, nil
	}
	hosts := []string{t.Domain}
	recorded, err := a.store.listTenantHosts(t.ID)
	if err != nil {
		return nil, err
	}
	for _, host := range recorded {
		hosts = append(hosts, host.Hostname)
	}
	if err := verifyHostnamesTarget(context.Background(), hosts, a.publicHost); err != nil {
		return nil, fmt.Errorf("refusing to disable direct ingress before DNS reaches the parent: %w", err)
	}
	port, _ := portFromBaseURL(t.BaseURL)
	info, err := a.getInstanceInfo(ctx, t.InstanceID)
	if err != nil {
		return nil, err
	}
	if err := a.store.setIngressMode(t.ID, IngressParent, ""); err != nil {
		return nil, err
	}
	t.IngressMode = IngressParent
	if err := stopHostedTenant(ctx, t.InstanceID, t.Slug, port, 10*time.Second); err != nil {
		_ = a.store.setIngressMode(t.ID, IngressDirectPending, err.Error())
		t.IngressMode = IngressDirectPending
		return nil, err
	}
	if _, _, err := a.spawnHostedTenant(ctx, hostedSpawnSpecForTenant(t, info.PublicIPv4, port)); err != nil {
		_ = a.store.setIngressMode(t.ID, IngressDirectPending, err.Error())
		t.IngressMode = IngressDirectPending
		_, _, directErr := a.spawnHostedTenant(ctx, hostedSpawnSpecForTenant(t, info.PublicIPv4, port))
		if directErr != nil {
			return nil, fmt.Errorf("restart parent-mode tenant: %v; direct-mode recovery also failed: %w", err, directErr)
		}
		return nil, fmt.Errorf("restart parent-mode tenant: %w; direct mode was restored", err)
	}
	_ = a.store.recordEvent(t.ID, "ingress.direct_rolled_back", "tool:tenant_ingress_rollback", nil)
	return map[string]any{"tenant_id": t.ID, "mode": IngressParent}, nil
}

func optionalProjectFromArgs(args map[string]any) string {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env
	}
	return strings.TrimSpace(getStr(args, "_project_id"))
}

func (a *App) directIngressTenant(args map[string]any) (*Tenant, error) {
	id := strings.TrimSpace(getStr(args, "tenant_id"))
	if id == "" {
		return nil, errors.New("tenant_id required")
	}
	t, _, err := a.store.get(id)
	return t, err
}

func (a *App) toolIngressPrepareDirect(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	done, err := a.beginTenantOperation(getStr(args, "tenant_id"), "ingress cutover")
	if err != nil {
		return nil, err
	}
	defer done()
	t, err := a.directIngressTenant(args)
	if err != nil {
		return nil, err
	}
	return a.prepareHostedDirectIngress(ctx, optionalProjectFromArgs(args), t)
}

func (a *App) toolIngressVerify(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	t, err := a.directIngressTenant(args)
	if err != nil {
		return nil, err
	}
	check, err := a.verifyHostedDirectIngress(context.Background(), ctx, t)
	if err != nil {
		_, _ = a.store.db.Exec(`UPDATE fleet_tenants SET ingress_error=? WHERE id=?`, err.Error(), t.ID)
		return nil, err
	}
	_, _ = a.store.db.Exec(`UPDATE fleet_tenants SET ingress_error=NULL WHERE id=?`, t.ID)
	return map[string]any{"tenant_id": t.ID, "mode": t.IngressMode, "checks": check}, nil
}

func (a *App) toolIngressFinalize(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	done, err := a.beginTenantOperation(getStr(args, "tenant_id"), "ingress cutover")
	if err != nil {
		return nil, err
	}
	defer done()
	t, err := a.directIngressTenant(args)
	if err != nil {
		return nil, err
	}
	if t.IngressMode != IngressDirectPending {
		return nil, errors.New("direct ingress must be prepared and verified before finalizing")
	}
	return a.finalizeHostedDirectIngress(context.Background(), ctx, t)
}

func (a *App) toolIngressRollback(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	done, err := a.beginTenantOperation(getStr(args, "tenant_id"), "ingress cutover")
	if err != nil {
		return nil, err
	}
	defer done()
	t, err := a.directIngressTenant(args)
	if err != nil {
		return nil, err
	}
	return a.rollbackHostedDirectIngress(ctx, optionalProjectFromArgs(args), t, boolArg(args, "confirm_disable"))
}
