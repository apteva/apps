package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Per-tenant domain attach/detach via optional Domains DNS and
// server-native ingress. Tenant targets are derived from the tenant's
// apteva-server port (stored in BaseURL).

// ─── envelope helpers ─────────────────────────────────────────────

// callDomainsTool invokes a tool on the Domains app and unwraps the
// MCP envelope. projectID is injected as `_project_id` so the call
// resolves when Domains is installed global-scoped (no
// APTEVA_PROJECT_ID in its env) — see feedback_project_id_global_calls.
func callDomainsTool(ctx *sdk.AppCtx, projectID, tool string, args map[string]any, out any) error {
	return callSiblingTool(ctx, "domains", projectID, tool, args, out)
}

// callCertsTool — same shape; project-scoped per tenant.
func callCertsTool(ctx *sdk.AppCtx, projectID, tool string, args map[string]any, out any) error {
	return callSiblingTool(ctx, "certs", projectID, tool, args, out)
}

// callRoutesTool — routes data is project-agnostic, so no _project_id
// injection; the route owner_install_id is what scopes ownership.
func callRoutesTool(ctx *sdk.AppCtx, tool string, args map[string]any, out any) error {
	return callSiblingTool(ctx, "routes", "", tool, args, out)
}

func callSiblingTool(ctx *sdk.AppCtx, appName, projectID, tool string, args map[string]any, out any) error {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform unavailable")
	}
	if projectID != "" {
		if args == nil {
			args = map[string]any{}
		}
		if _, ok := args["_project_id"]; !ok {
			args["_project_id"] = projectID
		}
	}
	raw, err := ctx.PlatformAPI().CallApp(appName, tool, args)
	if err != nil {
		return fmt.Errorf("call %s.%s: %w", appName, tool, err)
	}
	var env struct {
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decode %s.%s envelope: %w", appName, tool, err)
	}
	if env.Error != nil {
		return fmt.Errorf("%s.%s: %s", appName, tool, env.Error.Message)
	}
	content, _ := env.Result["content"].([]any)
	if len(content) == 0 {
		return nil
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if text == "" || out == nil {
		return nil
	}
	return json.Unmarshal([]byte(text), out)
}

// ─── integration availability ─────────────────────────────────────

func (a *App) domainsAvailable(ctx *sdk.AppCtx) bool {
	return integrationBound(ctx, "domains")
}
func (a *App) certsAvailable(ctx *sdk.AppCtx) bool {
	return integrationBound(ctx, "certs")
}
func (a *App) routesAvailable(ctx *sdk.AppCtx) bool {
	return integrationBound(ctx, "routes")
}

func integrationBound(ctx *sdk.AppCtx, role string) bool {
	if ctx == nil {
		return false
	}
	b := ctx.IntegrationFor(role)
	return b != nil && b.Kind == "app"
}

// ─── apex resolution ──────────────────────────────────────────────

// resolveApex finds the registered domain that's a suffix of fqdn.
// "acme.fleet.example.com" with "fleet.example.com" registered →
// apex="fleet.example.com", sub="acme". An exact match returns
// sub="".
func resolveApex(ctx *sdk.AppCtx, projectID, fqdn string) (apex, sub string, err error) {
	var resp struct {
		Domains []struct {
			Name string `json:"name"`
		} `json:"domains"`
	}
	if err := callDomainsTool(ctx, projectID, "domain_list", map[string]any{}, &resp); err != nil {
		return "", "", err
	}
	fqdn = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(fqdn, ".")))
	best := ""
	for _, d := range resp.Domains {
		name := strings.ToLower(d.Name)
		if name == "" {
			continue
		}
		if fqdn == name || strings.HasSuffix(fqdn, "."+name) {
			if len(name) > len(best) {
				best = name
			}
		}
	}
	if best == "" {
		return "", "", fmt.Errorf("no registered domain matches %q — register it with the Domains app first", fqdn)
	}
	if fqdn == best {
		return best, "", nil
	}
	return best, strings.TrimSuffix(fqdn, "."+best), nil
}

// ─── attach / detach spec ─────────────────────────────────────────

type attachDomainSpec struct {
	FQDN   string
	Target string // record value; empty → fleet's publicHost
	Type   string // "A" | "CNAME"; empty → inferred from target shape
	TTL    int
	// ManageDNS=true (default): the Domains app writes/owns the DNS
	// record for fqdn. Used when the apex sits in our Domains catalog.
	// ManageDNS=false: client already pointed fqdn at our parent
	// machine; fleet skips the DNS write and only registers server
	// ingress. The parent apteva-server owns automatic HTTPS for this
	// hostname.
	ManageDNS bool
}

type domainRecordSpec struct {
	Domain string
	Name   string
	Type   string
	Value  string
	TTL    int
}

// resolveTarget picks the DNS record value: explicit > publicHost.
// publicHost is set at OnMount via detectPublicHost; on a cloud box
// that's the outbound interface IP, perfect for an A record pointing
// at the parent apteva-server.
func (a *App) resolveTarget(spec attachDomainSpec) string {
	if t := strings.TrimSpace(spec.Target); t != "" {
		return t
	}
	if a != nil && a.publicHost != "" && a.publicHost != "localhost" {
		return a.publicHost
	}
	return ""
}

func (a *App) resolveTenantTarget(ctx *sdk.AppCtx, t *Tenant, spec attachDomainSpec) (string, error) {
	if target := strings.TrimSpace(spec.Target); target != "" {
		return target, nil
	}
	if t != nil && t.UsesDirectIngress() {
		info, err := a.getInstanceInfo(ctx, t.InstanceID)
		if err != nil {
			return "", fmt.Errorf("resolve hosted ingress target: %w", err)
		}
		if net.ParseIP(info.PublicIPv4) == nil {
			return "", fmt.Errorf("hosted instance has no usable public IPv4 address")
		}
		return info.PublicIPv4, nil
	}
	return a.resolveTarget(spec), nil
}

// inferRecordType: IP literal → A, else CNAME. Same heuristic as
// deploy/domain_link.go.
func inferRecordType(target string) string {
	if net.ParseIP(target) != nil {
		return "A"
	}
	return "CNAME"
}

func normaliseGrantDomain(s string) (string, error) {
	d := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(s, ".")))
	d = strings.TrimPrefix(d, "*.")
	if d == "" {
		return "", errors.New("domain required")
	}
	if strings.ContainsAny(d, " \t\r\n/?:#") {
		return "", fmt.Errorf("invalid domain %q", s)
	}
	if !strings.Contains(d, ".") {
		return "", fmt.Errorf("domain %q must contain a dot", s)
	}
	return d, nil
}

func normaliseExactHostname(s string) (string, error) {
	hostname := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(s, ".")))
	if hostname == "" {
		return "", errors.New("hostname required")
	}
	if len(hostname) > 253 || strings.ContainsAny(hostname, " \t\r\n/?:#@*") || net.ParseIP(hostname) != nil {
		return "", fmt.Errorf("invalid exact hostname %q", s)
	}
	labels := strings.Split(hostname, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("hostname %q must contain a dot", s)
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid exact hostname %q", s)
		}
		for _, ch := range label {
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
				return "", fmt.Errorf("invalid exact hostname %q", s)
			}
		}
	}
	return hostname, nil
}

func wildcardHost(domain string) string {
	if domain == "" {
		return ""
	}
	return "*." + strings.TrimPrefix(domain, "*.")
}

func recordID(apex, name, rtype string) string {
	return apex + "|" + name + "|" + strings.ToUpper(rtype)
}

func splitGrantRecordID(s string) (apex, name, rtype string, ok bool) {
	parts := strings.Split(s, "|")
	if len(parts) == 3 {
		return parts[0], parts[1], parts[2], true
	}
	// Backward compatibility with tenant_attach_domain's older
	// "<apex>|<type>" shape. Grant records always use three parts.
	if len(parts) == 2 {
		return parts[0], "@", parts[1], true
	}
	return "", "", "", false
}

func composeRecordFQDN(domain, name string) string {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if name == "" || name == "@" {
		return domain
	}
	return name + "." + domain
}

func grantCoversFQDN(grantDomain, fqdn string, wildcard bool) bool {
	grantDomain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(grantDomain)), ".")
	fqdn = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(fqdn)), ".")
	return fqdn == grantDomain || (wildcard && strings.HasSuffix(fqdn, "."+grantDomain))
}

func wildcardSubArg(apex, domain string) string {
	if domain == apex {
		return "*"
	}
	return "*." + strings.TrimSuffix(domain, "."+apex)
}

func (a *App) writeDomainRecord(ctx *sdk.AppCtx, projectID, fqdn, target, rtype string, ttl int) (string, error) {
	apex, sub, err := resolveApex(ctx, projectID, fqdn)
	if err != nil {
		return "", err
	}
	subArg := sub
	if subArg == "" {
		subArg = "@"
	}
	if rtype == "CNAME" && sub == "" {
		return "", errors.New("apex CNAME isn't allowed by DNS; use type=A with an IP, or attach a subdomain")
	}
	if err := callDomainsTool(ctx, projectID, "domain_records_set", map[string]any{
		"domain": apex, "name": subArg, "type": rtype, "value": target, "ttl": ttl,
	}, nil); err != nil {
		return "", err
	}
	return recordID(apex, subArg, rtype), nil
}

// ─── attach orchestration ─────────────────────────────────────────

// attachDomain runs the orchestration: optional domain_records_set,
// then platform ingress expose. Persists (domain, record_id,
// attached_at) on the tenant. Idempotent on re-attach: domains
// upserts and platform ingress replaces same-owner routes in place.
//
// projectID is the operator's project — we run cross-app calls with
// it so global-scoped Domains (the prod default) resolve their
// per-project data correctly.
func (a *App) attachDomain(ctx *sdk.AppCtx, projectID string, t *Tenant, spec attachDomainSpec) error {
	if t == nil {
		return errors.New("tenant required")
	}
	if t.UsesDirectIngress() {
		return errors.New("restore parent ingress before changing the tenant primary domain")
	}
	done, err := a.beginTenantOperation(t.ID, "attach domain")
	if err != nil {
		return err
	}
	defer done()
	fqdn := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(spec.FQDN, ".")))
	if fqdn == "" {
		return errors.New("fqdn required")
	}

	var recordID string
	if spec.ManageDNS {
		if !a.domainsAvailable(ctx) {
			return errors.New("domains app not installed — install + bind it as fleet's domains integration, or pass manage_dns=false if the client already pointed DNS at this machine")
		}
		target, err := a.resolveTenantTarget(ctx, t, spec)
		if err != nil {
			return err
		}
		if target == "" {
			return errors.New("target required — pass target explicitly or ensure APTEVA_PUBLIC_URL / detectPublicHost yields a usable IP")
		}
		rtype := strings.ToUpper(strings.TrimSpace(spec.Type))
		if rtype == "" {
			rtype = inferRecordType(target)
		}
		if rtype != "A" && rtype != "CNAME" {
			return fmt.Errorf("unsupported record type %q (A or CNAME)", rtype)
		}
		if rtype == "CNAME" {
			target = strings.TrimSuffix(target, ".")
		}
		ttl := spec.TTL
		if ttl <= 0 {
			ttl = 600
		}

		apex, sub, err := resolveApex(ctx, projectID, fqdn)
		if err != nil {
			return err
		}
		subArg := sub
		if subArg == "" {
			subArg = "@"
		}
		if rtype == "CNAME" && sub == "" {
			return errors.New("apex CNAME isn't allowed by DNS; use type=A with an IP, or attach a subdomain")
		}

		if err := callDomainsTool(ctx, projectID, "domain_records_set", map[string]any{
			"domain": apex, "name": subArg, "type": rtype, "value": target, "ttl": ttl,
		}, nil); err != nil {
			return err
		}
		recordID = apex + "|" + rtype
		_ = a.store.recordEvent(t.ID, "domain.attached", "tool:attach_domain", map[string]any{
			"fqdn": fqdn, "apex": apex, "type": rtype, "target": target,
		})
	} else {
		// Client-managed DNS: we don't touch the registrar. We just
		// trust the operator that fqdn resolves to this machine and
		// continue with ingress registration. detach() keys off
		// record_id == "" to skip the corresponding DNS cleanup.
		_ = a.store.recordEvent(t.ID, "domain.attached", "tool:attach_domain", map[string]any{
			"fqdn": fqdn, "manage_dns": false,
		})
	}

	// Register the hostname through server-native ingress. Certs for
	// exact hostnames are owned by apteva-server once DNS points here.
	if err := a.registerRouteForTenantHost(ctx, t, fqdn, fqdn, "tool:attach_domain"); err != nil {
		cleanupErr := a.deleteAttachedDNSRecord(ctx, projectID, fqdn, recordID)
		if cleanupErr != nil {
			return fmt.Errorf("register tenant ingress: %v; DNS rollback failed: %w", err, cleanupErr)
		}
		return fmt.Errorf("register tenant ingress: %w", err)
	}
	if err := a.store.setDomain(t.ID, fqdn, recordID, nowUTC()); err != nil {
		a.unregisterRouteForTenant(ctx, fqdn)
		_ = a.deleteAttachedDNSRecord(ctx, projectID, fqdn, recordID)
		return err
	}
	return nil
}

func (a *App) deleteAttachedDNSRecord(ctx *sdk.AppCtx, projectID, fqdn, storedID string) error {
	if storedID == "" {
		return nil
	}
	apex, rtype, ok := splitRecordID(storedID)
	if !ok {
		return fmt.Errorf("invalid stored DNS record id %q", storedID)
	}
	sub := strings.TrimSuffix(strings.ToLower(strings.TrimSuffix(fqdn, ".")), "."+apex)
	if strings.EqualFold(strings.TrimSuffix(fqdn, "."), apex) || sub == "" {
		sub = "@"
	}
	return callDomainsTool(ctx, projectID, "domain_records_delete", map[string]any{
		"domain": apex, "name": sub, "type": rtype,
	}, nil)
}

// detachDomain best-effort deletes the DNS record, unregisters the
// route, and clears the tenant's domain link. The
// local clear runs even on remote-side failures — a dangling registrar
// record is operator-recoverable via the Domains panel; a dangling
// tenant row pointing at a domain that doesn't resolve is worse.
func (a *App) detachDomain(ctx *sdk.AppCtx, projectID string, t *Tenant) error {
	if t == nil {
		return errors.New("tenant required")
	}
	if t.UsesDirectIngress() {
		return errors.New("restore parent ingress before detaching the tenant domain")
	}
	done, err := a.beginTenantOperation(t.ID, "detach domain")
	if err != nil {
		return err
	}
	defer done()
	if t.Domain == "" && t.DomainRecordID == "" {
		return nil
	}
	var deleteErr error
	if t.DomainRecordID != "" && a.domainsAvailable(ctx) {
		apex, rtype, ok := splitRecordID(t.DomainRecordID)
		if ok {
			fqdn := strings.ToLower(strings.TrimSuffix(t.Domain, "."))
			sub := ""
			if fqdn != apex {
				sub = strings.TrimSuffix(fqdn, "."+apex)
			}
			subArg := sub
			if subArg == "" {
				subArg = "@"
			}
			deleteErr = callDomainsTool(ctx, projectID, "domain_records_delete", map[string]any{
				"domain": apex, "name": subArg, "type": rtype,
			}, nil)
		}
	}
	a.unregisterRouteForTenant(ctx, t.Domain)
	if err := a.store.clearDomain(t.ID); err != nil {
		return err
	}
	_ = a.store.recordEvent(t.ID, "domain.detached", "tool:detach_domain", map[string]any{"fqdn": t.Domain})
	return deleteErr
}

func splitRecordID(s string) (apex, rtype string, ok bool) {
	i := strings.IndexByte(s, '|')
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// ─── domain grants ─────────────────────────────────────────────────

// grantDomain delegates domain to a tenant. Unlike attachDomain, this
// is not just the tenant dashboard hostname; it is the base zone that
// tenant-local apps can use through the Domains facade.
func (a *App) grantDomain(ctx *sdk.AppCtx, projectID string, t *Tenant, spec attachDomainSpec) (*DomainGrant, error) {
	if t == nil {
		return nil, errors.New("tenant required")
	}
	done, err := a.beginTenantOperation(t.ID, "grant domain")
	if err != nil {
		return nil, err
	}
	defer done()
	domain, err := normaliseGrantDomain(spec.FQDN)
	if err != nil {
		return nil, err
	}
	if !spec.ManageDNS {
		return nil, errors.New("domain grants require managed DNS so Fleet can maintain the delegation boundary")
	}
	if !a.domainsAvailable(ctx) {
		return nil, errors.New("domains app not installed — install + bind it as fleet's domains integration")
	}
	target, err := a.resolveTenantTarget(ctx, t, spec)
	if err != nil {
		return nil, err
	}
	if target == "" {
		return nil, errors.New("target required — pass target explicitly or ensure APTEVA_PUBLIC_URL / detectPublicHost yields a usable IP")
	}
	rtype := strings.ToUpper(strings.TrimSpace(spec.Type))
	if rtype == "" {
		rtype = inferRecordType(target)
	}
	if rtype != "A" && rtype != "CNAME" {
		return nil, fmt.Errorf("unsupported record type %q (A or CNAME)", rtype)
	}
	if rtype == "CNAME" {
		target = strings.TrimSuffix(target, ".")
	}
	ttl := spec.TTL
	if ttl <= 0 {
		ttl = 600
	}

	record, err := a.writeDomainRecord(ctx, projectID, domain, target, rtype, ttl)
	if err != nil {
		return nil, err
	}
	wildcardRecord := ""
	cleanupDNS := func() {
		for _, storedID := range []string{record, wildcardRecord} {
			if apex, name, recordType, ok := splitGrantRecordID(storedID); ok {
				_ = callDomainsTool(ctx, projectID, "domain_records_delete", map[string]any{
					"domain": apex, "name": name, "type": recordType,
				}, nil)
			}
		}
	}
	if spec.ManageDNS {
		apex, _, err := resolveApex(ctx, projectID, domain)
		if err != nil {
			cleanupDNS()
			return nil, err
		}
		wildcardName := wildcardSubArg(apex, domain)
		if err := callDomainsTool(ctx, projectID, "domain_records_set", map[string]any{
			"domain": apex, "name": wildcardName, "type": rtype, "value": target, "ttl": ttl,
		}, nil); err != nil {
			cleanupDNS()
			return nil, err
		}
		wildcardRecord = recordID(apex, wildcardName, rtype)
	}

	g := &DomainGrant{
		TenantID:         t.ID,
		Domain:           domain,
		Wildcard:         true,
		Status:           "active",
		DomainRecordID:   record,
		WildcardRecordID: wildcardRecord,
	}
	if err := a.store.upsertDomainGrant(g); err != nil {
		cleanupDNS()
		return nil, err
	}
	_ = a.store.recordEvent(t.ID, "domain_grant.granted", "tool:tenant_domain_grant", map[string]any{
		"domain": domain, "wildcard": true, "target": target, "type": rtype,
	})

	return g, nil
}

func (a *App) revokeDomainGrant(ctx *sdk.AppCtx, projectID string, t *Tenant, domain string) (*DomainGrant, error) {
	if t == nil {
		return nil, errors.New("tenant required")
	}
	done, err := a.beginTenantOperation(t.ID, "revoke domain")
	if err != nil {
		return nil, err
	}
	defer done()
	domain, err = normaliseGrantDomain(domain)
	if err != nil {
		return nil, err
	}
	g, err := a.store.getDomainGrant(t.ID, domain)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return nil, fmt.Errorf("domain grant not found: %s", domain)
	}
	hosts, err := a.store.listTenantHostsByGrant(t.ID, g.ID)
	if err != nil {
		return nil, err
	}
	for _, host := range hosts {
		if err := a.unregisterTenantHost(ctx, host.Hostname); err != nil {
			_ = a.store.setTenantHostStatus(t.ID, host.Hostname, "error", err.Error())
			return nil, fmt.Errorf("remove ingress for %s: %w", host.Hostname, err)
		}
	}
	for _, host := range hosts {
		if err := a.store.deleteTenantHost(t.ID, host.Hostname); err != nil {
			return nil, err
		}
	}
	var deleteErr error
	if a.domainsAvailable(ctx) {
		for _, rec := range []string{g.WildcardRecordID, g.DomainRecordID} {
			if rec == "" {
				continue
			}
			apex, name, rtype, ok := splitGrantRecordID(rec)
			if !ok {
				continue
			}
			if err := callDomainsTool(ctx, projectID, "domain_records_delete", map[string]any{
				"domain": apex, "name": name, "type": rtype,
			}, nil); err != nil && deleteErr == nil {
				deleteErr = err
			}
		}
	}
	if a.certsAvailable(ctx) {
		_ = callCertsTool(ctx, projectID, "cert_revoke", map[string]any{"fqdn": domain}, nil)
		if g.Wildcard {
			_ = callCertsTool(ctx, projectID, "cert_revoke", map[string]any{"fqdn": wildcardHost(domain)}, nil)
		}
	}
	a.unregisterRouteForTenant(ctx, domain)
	if g.Wildcard {
		a.unregisterRouteForTenant(ctx, wildcardHost(domain))
	}
	if err := a.store.deleteDomainGrant(t.ID, domain); err != nil {
		return nil, err
	}
	_ = a.store.recordEvent(t.ID, "domain_grant.revoked", "tool:tenant_domain_revoke", map[string]any{"domain": domain})
	return g, deleteErr
}

// proxyTenantDomainRecordSet is the Fleet-side endpoint the tenant
// Domains facade will call. It validates that the requested FQDN sits
// under a grant for the tenant, then writes the real DNS record via
// the parent Domains app.
func (a *App) proxyTenantDomainRecordSet(ctx *sdk.AppCtx, projectID string, t *Tenant, rec domainRecordSpec) error {
	if t == nil {
		return errors.New("tenant required")
	}
	domain, err := normaliseGrantDomain(rec.Domain)
	if err != nil {
		return err
	}
	g, err := a.store.getDomainGrant(t.ID, domain)
	if err != nil {
		return err
	}
	if g == nil || g.Status != "active" {
		return fmt.Errorf("domain %q is not granted to tenant %s", domain, t.ID)
	}
	fqdn := composeRecordFQDN(domain, rec.Name)
	if !grantCoversFQDN(domain, fqdn, g.Wildcard) {
		return fmt.Errorf("record %q is outside granted domain %q", fqdn, domain)
	}
	rtype := strings.ToUpper(strings.TrimSpace(rec.Type))
	if rtype == "" {
		return errors.New("record type required")
	}
	if strings.TrimSpace(rec.Value) == "" {
		return errors.New("record value required")
	}
	ttl := rec.TTL
	if ttl <= 0 {
		ttl = 600
	}
	apex, sub, err := resolveApex(ctx, projectID, fqdn)
	if err != nil {
		return err
	}
	subArg := sub
	if subArg == "" {
		subArg = "@"
	}
	if err := callDomainsTool(ctx, projectID, "domain_records_set", map[string]any{
		"domain": apex, "name": subArg, "type": rtype, "value": rec.Value, "ttl": ttl,
	}, nil); err != nil {
		return err
	}
	_ = a.store.recordEvent(t.ID, "domain_grant.record_set", "tool:tenant_domain_record_set", map[string]any{
		"domain": domain, "fqdn": fqdn, "type": rtype,
	})
	return nil
}

func (a *App) proxyTenantDomainRecordDelete(ctx *sdk.AppCtx, projectID string, t *Tenant, rec domainRecordSpec) error {
	if t == nil {
		return errors.New("tenant required")
	}
	domain, err := normaliseGrantDomain(rec.Domain)
	if err != nil {
		return err
	}
	g, err := a.store.getDomainGrant(t.ID, domain)
	if err != nil {
		return err
	}
	if g == nil || g.Status != "active" {
		return fmt.Errorf("domain %q is not granted to tenant %s", domain, t.ID)
	}
	fqdn := composeRecordFQDN(domain, rec.Name)
	if !grantCoversFQDN(domain, fqdn, g.Wildcard) {
		return fmt.Errorf("record %q is outside granted domain %q", fqdn, domain)
	}
	rtype := strings.ToUpper(strings.TrimSpace(rec.Type))
	if rtype == "" {
		return errors.New("record type required")
	}
	apex, sub, err := resolveApex(ctx, projectID, fqdn)
	if err != nil {
		return err
	}
	subArg := sub
	if subArg == "" {
		subArg = "@"
	}
	if err := callDomainsTool(ctx, projectID, "domain_records_delete", map[string]any{
		"domain": apex, "name": subArg, "type": rtype,
	}, nil); err != nil {
		return err
	}
	_ = a.store.recordEvent(t.ID, "domain_grant.record_deleted", "tool:tenant_domain_record_delete", map[string]any{
		"domain": domain, "fqdn": fqdn, "type": rtype,
	})
	return nil
}

// ─── route integration ────────────────────────────────────────────

// registerRouteForTenant publishes (fqdn → tenant apteva-server port)
// through server-native ingress. Idempotent — replaces an existing
// route from the same owner.
func (a *App) registerRouteForTenant(ctx *sdk.AppCtx, tenantID, fqdn string) error {
	if fqdn == "" || ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("platform ingress is unavailable")
	}
	t, _, err := a.store.get(tenantID)
	if err != nil || t == nil {
		if err != nil {
			return err
		}
		return errors.New("tenant not found")
	}
	return a.registerRouteForTenantHost(ctx, t, fqdn, fqdn, "tool:attach_domain")
}

func (a *App) registerRouteForTenantHost(ctx *sdk.AppCtx, t *Tenant, hostname, certFQDN, actor string) error {
	if t == nil || hostname == "" || ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("tenant, hostname, and platform ingress are required")
	}
	port, _ := portFromBaseURL(t.BaseURL)
	if port == 0 {
		_ = a.store.recordEvent(t.ID, "route.register_skipped", actor,
			map[string]any{"reason": "no_port_in_base_url"})
		return fmt.Errorf("tenant %s has no port in base_url", t.ID)
	}
	if t.IngressMode == IngressDirect {
		if err := a.verifyTenantLocalIngressRoute(context.Background(), ctx, t, hostname); err != nil {
			_ = a.store.recordEvent(t.ID, "route.register_failed", actor,
				map[string]any{"fqdn": hostname, "error": err.Error(), "mode": IngressDirect})
			return err
		}
		_ = a.store.recordEvent(t.ID, "route.registered", actor,
			map[string]any{"fqdn": hostname, "mode": IngressDirect})
		return nil
	}
	target, err := a.internalTenantBaseURL(ctx, t)
	if err != nil {
		_ = a.store.recordEvent(t.ID, "route.register_failed", actor,
			map[string]any{"fqdn": hostname, "error": err.Error()})
		return err
	}
	if _, err := ctx.PlatformAPI().ExposeIngress(sdk.IngressExposeRequest{
		Hostname:  hostname,
		Target:    target,
		OwnerKind: "fleet",
		CertFQDN:  certFQDN,
	}); err != nil {
		_ = a.store.recordEvent(t.ID, "route.register_failed", actor,
			map[string]any{"fqdn": hostname, "error": err.Error()})
		return err
	}
	_ = a.store.recordEvent(t.ID, "route.registered", actor,
		map[string]any{"fqdn": hostname, "port": port})
	return nil
}

func (a *App) refreshTenantIngressTargets(ctx *sdk.AppCtx, t *Tenant, target string) error {
	if t == nil {
		return errors.New("tenant required")
	}
	if t.IngressMode == IngressDirect {
		return nil
	}
	return a.refreshParentTenantIngressTargets(ctx, t, target)
}

func (a *App) refreshParentTenantIngressTargets(ctx *sdk.AppCtx, t *Tenant, target string) error {
	_, err := a.refreshParentTenantIngressTargetsCount(ctx, t, target)
	return err
}

type ingressRefreshResult struct {
	Expected  int
	Rewritten int
}

func (a *App) refreshParentTenantIngressTargetsCount(ctx *sdk.AppCtx, t *Tenant, target string) (ingressRefreshResult, error) {
	hosts, err := a.store.listTenantHosts(t.ID)
	if err != nil {
		return ingressRefreshResult{}, err
	}
	if t.Domain == "" && len(hosts) == 0 {
		return ingressRefreshResult{}, nil
	}
	if ctx == nil || ctx.PlatformAPI() == nil {
		return ingressRefreshResult{}, errors.New("platform ingress is unavailable")
	}
	target = strings.TrimRight(target, "/")
	result := ingressRefreshResult{}
	seen := map[string]bool{}
	expose := func(hostname, certFQDN string) error {
		hostname = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(hostname, ".")))
		if hostname == "" {
			return nil
		}
		if seen[hostname] {
			return nil
		}
		seen[hostname] = true
		result.Expected++
		_, err := ctx.PlatformAPI().ExposeIngress(sdk.IngressExposeRequest{
			Hostname: hostname, Target: target, OwnerKind: "fleet", CertFQDN: certFQDN,
		})
		if err == nil {
			result.Rewritten++
		}
		return err
	}
	var firstErr error
	if err := expose(t.Domain, t.Domain); err != nil {
		firstErr = err
	}
	for _, host := range hosts {
		if err := expose(host.Hostname, host.Hostname); err != nil {
			_ = a.store.setTenantHostStatus(t.ID, host.Hostname, "error", err.Error())
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := a.store.setTenantHostStatus(t.ID, host.Hostname, "active", ""); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return result, firstErr
}

func (a *App) refreshTenantIngress(ctx *sdk.AppCtx, t *Tenant) error {
	if t == nil {
		return errors.New("tenant required")
	}
	if t.IngressMode == IngressDirect {
		return nil
	}
	return a.refreshParentTenantIngress(ctx, t)
}

func (a *App) refreshParentTenantIngress(ctx *sdk.AppCtx, t *Tenant) error {
	hosts, err := a.store.listTenantHosts(t.ID)
	if err != nil {
		return err
	}
	if t.Domain == "" && len(hosts) == 0 {
		return nil
	}
	target, err := a.internalTenantBaseURL(ctx, t)
	if err != nil {
		return err
	}
	return a.refreshParentTenantIngressTargets(ctx, t, target)
}

func (a *App) resolveTenantHostGrant(tenantID, hostname string, grantID int64) (*DomainGrant, error) {
	if grantID > 0 {
		grant, err := a.store.getDomainGrantByID(grantID)
		if err != nil {
			return nil, err
		}
		if grant == nil || grant.TenantID != tenantID {
			return nil, fmt.Errorf("domain grant %d not found for tenant", grantID)
		}
		if grant.Status != "active" || !grantCoversFQDN(grant.Domain, hostname, grant.Wildcard) {
			return nil, fmt.Errorf("domain grant %d does not cover %s", grantID, hostname)
		}
		return grant, nil
	}

	grants, err := a.store.listDomainGrants(tenantID)
	if err != nil {
		return nil, err
	}
	var best *DomainGrant
	for _, grant := range grants {
		if grant.Status != "active" || !grantCoversFQDN(grant.Domain, hostname, grant.Wildcard) {
			continue
		}
		if best == nil || len(grant.Domain) > len(best.Domain) {
			best = grant
		}
	}
	return best, nil
}

func (a *App) attachTenantHost(ctx *sdk.AppCtx, t *Tenant, rawHostname string, grantID int64) (*TenantHost, error) {
	if t == nil {
		return nil, errors.New("tenant required")
	}
	done, err := a.beginTenantOperation(t.ID, "attach hostname")
	if err != nil {
		return nil, err
	}
	defer done()

	hostname, err := normaliseExactHostname(rawHostname)
	if err != nil {
		return nil, err
	}
	existing, err := a.store.getTenantHostByHostname(hostname)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.TenantID != t.ID {
		return nil, fmt.Errorf("hostname %s is already assigned to another tenant", hostname)
	}
	grant, err := a.resolveTenantHostGrant(t.ID, hostname, grantID)
	if err != nil {
		return nil, err
	}
	host := &TenantHost{TenantID: t.ID, Hostname: hostname, Status: "pending"}
	if grant != nil {
		host.DomainGrantID = grant.ID
	}
	if err := a.store.upsertTenantHost(host); err != nil {
		return nil, err
	}
	if err := a.registerRouteForTenantHost(ctx, t, hostname, hostname, "tool:tenant_host_attach"); err != nil {
		_ = a.store.setTenantHostStatus(t.ID, hostname, "error", err.Error())
		failed, _ := a.store.getTenantHost(t.ID, hostname)
		return failed, err
	}
	if err := a.store.setTenantHostStatus(t.ID, hostname, "active", ""); err != nil {
		return nil, err
	}
	_ = a.store.recordEvent(t.ID, "tenant_host.attached", "tool:tenant_host_attach", map[string]any{
		"hostname": hostname, "domain_grant_id": host.DomainGrantID,
	})
	return a.store.getTenantHost(t.ID, hostname)
}

func (a *App) removeTenantHost(ctx *sdk.AppCtx, t *Tenant, rawHostname string) (*TenantHost, error) {
	if t == nil {
		return nil, errors.New("tenant required")
	}
	done, err := a.beginTenantOperation(t.ID, "remove hostname")
	if err != nil {
		return nil, err
	}
	defer done()

	hostname, err := normaliseExactHostname(rawHostname)
	if err != nil {
		return nil, err
	}
	host, err := a.store.getTenantHost(t.ID, hostname)
	if err != nil {
		return nil, err
	}
	if host == nil {
		return nil, fmt.Errorf("tenant hostname not found: %s", hostname)
	}
	if t.IngressMode != IngressDirect {
		if err := a.unregisterTenantHost(ctx, hostname); err != nil {
			_ = a.store.setTenantHostStatus(t.ID, hostname, "error", err.Error())
			return nil, err
		}
	}
	if err := a.store.deleteTenantHost(t.ID, hostname); err != nil {
		return nil, err
	}
	_ = a.store.recordEvent(t.ID, "tenant_host.removed", "tool:tenant_host_remove", map[string]any{"hostname": hostname})
	return host, nil
}

func (a *App) unregisterTenantHostMappings(ctx *sdk.AppCtx, tenantID string) error {
	tenant, _, err := a.store.get(tenantID)
	if err != nil {
		return err
	}
	if tenant.IngressMode == IngressDirect {
		return nil
	}
	hosts, err := a.store.listTenantHosts(tenantID)
	if err != nil {
		return err
	}
	for _, host := range hosts {
		if err := a.unregisterTenantHost(ctx, host.Hostname); err != nil {
			_ = a.store.setTenantHostStatus(tenantID, host.Hostname, "error", err.Error())
			return fmt.Errorf("remove ingress for %s: %w", host.Hostname, err)
		}
	}
	return nil
}

// unregisterRouteForTenant — cleanup half. Safe when the route was
// never registered.
func (a *App) unregisterRouteForTenant(ctx *sdk.AppCtx, fqdn string) {
	_ = a.unregisterTenantHost(ctx, fqdn)
}

func (a *App) unregisterTenantHost(ctx *sdk.AppCtx, hostname string) error {
	if hostname == "" || ctx == nil || ctx.PlatformAPI() == nil {
		return errors.New("hostname and platform ingress are required")
	}
	return ctx.PlatformAPI().UnexposeIngress(hostname)
}

// myInstallID reads APTEVA_INSTALL_ID — the platform injects it at
// spawn so the routes app can tag ownership. 0 when unset (the routes
// app rejects with a clear error).
func myInstallID() int64 {
	v := strings.TrimSpace(os.Getenv("APTEVA_INSTALL_ID"))
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func nowUTC() time.Time { return time.Now().UTC() }
